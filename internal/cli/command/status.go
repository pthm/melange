package command

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

var (
	statusDB       string
	statusDBSchema string
	statusSchema   string
	statusFormat   string
)

// Sync states comparing the local schema file against the deployed model.
const (
	syncInSync      = "in_sync"      // local checksum matches the deployed one
	syncDrift       = "drift"        // local schema differs from deployed
	syncUnknown     = "unknown"      // no local schema file to compare
	syncNotRecorded = "not_recorded" // the database has no migration record

	// syncDatabaseAhead is drift where the deployed model is not any version of
	// the local schema git has recorded — someone deployed from elsewhere.
	// Best-effort: see databaseAhead for what it cannot see.
	syncDatabaseAhead = "database_ahead"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show schema, tuples, and deployed-model status",
	Long: `Show whether the schema file and melange_tuples view are present, and what
model the database has deployed — including whether the local schema file is
in sync with it.`,
	Example: `  # Check status
  melange status --db postgres://localhost/mydb

  # Against a named environment, as JSON
  melange status --env production --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseSchema := resolveString(statusDBSchema, cfg.Database.Schema, "public")
		schemaPath := resolveString(statusSchema, cfg.Schema)

		dsn, err := resolveDSN(statusDB)
		if err != nil {
			return err
		}

		return runStatus(dsn, databaseSchema, schemaPath, statusFormat)
	},
}

func init() {
	f := statusCmd.Flags()
	f.StringVar(&statusDB, "db", "", "database URL")
	f.StringVar(&statusDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&statusSchema, "schema", "", "path to schema.fga or fga.mod file")
	f.StringVar(&statusFormat, "format", "text", "output format: text (default) or json")
}

// statusReport is the machine-readable status, shared by text and JSON output.
type statusReport struct {
	SchemaFile string          `json:"schema_file"` // present|missing
	TuplesView string          `json:"tuples_view"` // present|missing
	Sync       string          `json:"sync"`
	Deployed   *deployedReport `json:"deployed,omitempty"`
	Drift      *driftReport    `json:"drift,omitempty"`
	Notes      []string        `json:"notes,omitempty"` // non-fatal warnings
}

// driftReport is the semantic detail behind a `drift` sync state: what the
// local schema would change if migrated. Absent when the schemas match, when
// the deployed model was never recorded, or when either side could not be
// parsed — status stays a reachability report, so none of those are errors.
type driftReport struct {
	Additive int             `json:"additive"`
	Breaking int             `json:"breaking"`
	Changes  []schema.Change `json:"changes"`
}

type deployedReport struct {
	MelangeVersion string `json:"melange_version,omitempty"`
	MigratedAt     string `json:"migrated_at,omitempty"`
	Checksum       string `json:"schema_checksum"`
	Format         string `json:"schema_format,omitempty"`
	ModelRecorded  bool   `json:"model_recorded"` // schema_dsl present → `schema pull` works
}

func runStatus(dsn, databaseSchema, schemaPath, format string) error {
	if format != "text" && format != "json" {
		return cli.GeneralError(fmt.Sprintf("invalid --format %q (want text or json)", format), nil)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	m := migrator.NewMigrator(db, schemaPath)
	m.SetDatabaseSchema(databaseSchema)

	s, err := m.GetStatus(ctx)
	if err != nil {
		return cli.GeneralError("getting status", err)
	}

	report := statusReport{
		SchemaFile: presentMissing(s.SchemaExists),
		TuplesView: presentMissing(s.TuplesExists),
		Sync:       syncNotRecorded,
	}

	// Local checksum, computed the same way migrate records it, for drift
	// detection. A present-but-unreadable schema file is surfaced, not silently
	// treated as "no local schema".
	localChecksum := ""
	if s.SchemaExists {
		content, rerr := parser.ReadSchemaContent(schemaPath)
		if rerr != nil {
			report.Notes = append(report.Notes, fmt.Sprintf("could not read local schema %s: %v", schemaPath, rerr))
		} else {
			localChecksum = migrator.ComputeSchemaChecksum(string(content))
		}
	}

	// Reading the migration record is non-fatal: schema/tuples presence is still
	// worth reporting even when the record can't be read (e.g. a permissions
	// error), so status never loses its basic reachability signal.
	rec, recErr := m.GetLastMigration(ctx)
	switch {
	case recErr != nil:
		report.Notes = append(report.Notes, fmt.Sprintf("could not read migration record: %v", recErr))
		report.Sync = syncUnknown
	case rec != nil:
		report.Sync = classifySync(localChecksum, rec)
		report.Deployed = &deployedReport{
			MelangeVersion: rec.MelangeVersion,
			Checksum:       rec.SchemaChecksum,
			Format:         rec.SchemaFormat,
			ModelRecorded:  rec.SchemaDSL != "",
		}
		if !rec.MigratedAt.IsZero() {
			report.Deployed.MigratedAt = rec.MigratedAt.Format(time.RFC3339)
		}
		if report.Sync == syncDrift {
			detail, equivalent := driftDetail(rec, schemaPath, &report.Notes)
			report.Drift = detail
			// A checksum that moved without a semantic change — reformatting, an
			// edited comment — is drift worth reporting, but never worth alleging
			// that someone else migrated this database.
			if !equivalent && databaseAhead(schemaPath, rec.SchemaChecksum, maxHistoryRevisions) {
				report.Sync = syncDatabaseAhead
			}
		}
	default:
		report.Sync = classifySync(localChecksum, nil) // not_recorded
	}

	if format == "json" {
		out, merr := json.MarshalIndent(report, "", "  ")
		if merr != nil {
			return cli.GeneralError("encoding status", merr)
		}
		fmt.Println(string(out))
		return nil
	}

	printStatusText(report, s, schemaPath)
	return nil
}

// maxHistoryRevisions caps how far back the database-ahead probe looks. Each
// revision costs a `git cat-file`, so the window bounds a `status` call against
// a long-lived schema; a schema with more history than this reports plain drift
// rather than guessing.
const maxHistoryRevisions = 50

// databaseAhead reports whether the deployed checksum matches no version of the
// local schema in the last maxRevisions of git history — meaning the database is
// running a model that never existed in this checkout, so migrating would
// overwrite someone else's work rather than move the database forward.
//
// It is an advisory and answers false on every uncertainty: a false negative is
// a missing hint, while a false positive accuses a colleague of a migration they
// did not perform. So it claims true only when git could search every version of
// this schema and none of them produced the deployed checksum; each bail-out
// below names what git cannot see there.
func databaseAhead(schemaPath, deployedChecksum string, maxRevisions int) bool {
	// Nothing to search for, or — for a modular schema — a checksum spanning the
	// manifest and every module, which this single-path search cannot reconstruct.
	if deployedChecksum == "" || parser.IsModularSchema(schemaPath) {
		return false
	}
	relPath, err := gitRelativePath(schemaPath)
	if err != nil {
		return false // not a git repository
	}
	// A shallow clone's history stops at the graft boundary, and git reports that
	// truncation as success — the revision count alone cannot detect it. CI
	// checkouts are shallow by default, which is exactly where this state would
	// be read as an alarm.
	if shallow, serr := exec.Command("git", "rev-parse", "--is-shallow-repository").Output(); serr != nil ||
		strings.TrimSpace(string(shallow)) != "false" {
		return false
	}
	// ":(top)" anchors the pathspec at the repository root. relPath is
	// root-relative (as `git cat-file <rev>:<path>` requires), but a bare pathspec
	// is resolved against the current directory — which would silently match
	// nothing whenever melange runs from a subdirectory.
	pathspec := ":(top)" + relPath
	// An uncommitted edit to the schema is itself a version git cannot see, and
	// migrating from a dirty working tree is routine in local development — so a
	// deployed model missing from history may well be one this developer applied
	// themselves. Only a clean file makes the history search complete.
	if status, serr := exec.Command("git", "status", "--porcelain", "--", pathspec).Output(); serr != nil || len(status) > 0 { //nolint:gosec // relPath comes from a trusted config/flag
		return false
	}
	// --follow tracks the file across renames, and --name-only reports the path it
	// had at each revision — which is what reading that revision needs, since the
	// current path does not exist before the rename. quotepath=false keeps
	// non-ASCII paths literal instead of C-quoted, which would make every read
	// fail. One extra revision is requested so a truncated history is detectable.
	out, err := exec.Command("git", "-c", "core.quotepath=false", "log", "--follow", "--format=%H", "--name-only", //nolint:gosec // relPath comes from a trusted config/flag
		"-n", strconv.Itoa(maxRevisions+1), "--", pathspec).Output()
	if err != nil {
		return false
	}
	revisions, commits := parseRevisionPaths(string(out))
	if commits > maxRevisions {
		return false // history longer than the window: the search cannot be exhaustive
	}
	if len(revisions) == 0 {
		return false // never committed: nothing to compare against
	}
	read := 0
	for _, r := range revisions {
		// --filters converts the blob the way a checkout would (end-of-line
		// conversion, smudge filters), so the bytes compared are the bytes melange
		// would have checksummed from a working tree — not the raw blob, which
		// differs under core.autocrlf or a text attribute.
		content, serr := exec.Command("git", "cat-file", "--filters", r.revision+":"+r.path).Output() //nolint:gosec // revision and path come from git itself
		if serr != nil {
			continue // file absent at this revision, or unreadable in a partial clone
		}
		read++
		if migrator.ComputeSchemaChecksum(string(content)) == deployedChecksum {
			return false
		}
	}
	// Every revision failed to read (a blobless clone offline, say). Absence of
	// evidence is not evidence here.
	return read > 0
}

// revisionPath is a commit and the path the schema had at that commit.
type revisionPath struct {
	revision string
	path     string
}

// parseRevisionPaths reads `git log --format=%H --name-only` output: a commit
// hash on its own line, a blank line, then the paths touched. It returns one
// entry per commit that reported a path — the first only, since the log is
// filtered to a single pathspec and --follow reports at most one name per commit
// — plus the number of commits git listed, which is what truncation must be
// judged on: a commit that reported no path still consumed the window.
func parseRevisionPaths(out string) (revisions []revisionPath, commits int) {
	var pending string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case isCommitHash(line):
			commits++
			pending = line
		case pending != "":
			revisions = append(revisions, revisionPath{revision: pending, path: line})
			pending = ""
		}
	}
	return revisions, commits
}

// isCommitHash distinguishes a %H line from a path line. Full hashes are 40 hex
// characters (SHA-1) or 64 (SHA-256 repositories).
func isCommitHash(line string) bool {
	if len(line) != 40 && len(line) != 64 {
		return false
	}
	return strings.TrimLeft(line, "0123456789abcdef") == ""
}

// driftDetail diffs the deployed model against the local schema so `status` can
// say what drifted, not just that something did. Every failure to get there
// (a record predating model storage, an unparseable side) yields nil plus a
// note: the checksum-level drift verdict already stands on its own.
//
// The second return says the models were compared and found semantically equal.
// A nil report alone conflates that with a comparison that could not be made,
// and callers must not read the latter as proof of equivalence.
func driftDetail(rec *migrator.MigrationRecord, schemaPath string, notes *[]string) (detail *driftReport, equivalent bool) {
	deployed, derr := deployedTypesFromRecord(rec)
	if derr != nil {
		*notes = append(*notes, fmt.Sprintf("could not read the deployed model for a detailed diff: %v", derr))
		return nil, false
	}
	if deployed == nil {
		return nil, false // pre-storage record; the ModelRecorded hint already covers it
	}
	local, lerr := parser.ParseSchema(schemaPath)
	if lerr != nil {
		*notes = append(*notes, fmt.Sprintf("could not parse local schema for a detailed diff: %v", lerr))
		return nil, false
	}
	detail = summarizeDrift(deployed, local)
	return detail, detail == nil
}

// summarizeDrift renders a diff of the two models as a drift report, or nil
// when they are semantically equivalent. Equivalence with differing checksums
// is real and worth reporting as such: formatting or comment changes move the
// checksum without changing behavior.
func summarizeDrift(deployed, local []schema.TypeDefinition) *driftReport {
	d := schema.Diff(deployed, local)
	if d.Empty() {
		return nil
	}
	additive, breaking := d.Counts()
	return &driftReport{Additive: additive, Breaking: breaking, Changes: d.Changes}
}

// classifySync compares a local schema checksum against the deployed record.
// An empty localChecksum means there is no local schema file to compare.
func classifySync(localChecksum string, rec *migrator.MigrationRecord) string {
	switch {
	case rec == nil:
		return syncNotRecorded
	case localChecksum == "":
		return syncUnknown
	case localChecksum == rec.SchemaChecksum:
		return syncInSync
	default:
		return syncDrift
	}
}

func presentMissing(ok bool) string {
	if ok {
		return "present"
	}
	return "missing"
}

// shortChecksum abbreviates a hex checksum for human-readable output.
func shortChecksum(sum string) string {
	if len(sum) <= 12 {
		return sum
	}
	return sum[:12] + "…"
}

func printStatusText(r statusReport, s *migrator.Status, schemaPath string) {
	fmt.Printf("Schema file:  %s\n", r.SchemaFile)
	fmt.Printf("Tuples view:  %s\n", r.TuplesView)

	if r.Deployed == nil {
		fmt.Println("Deployed:     no migration recorded")
	} else {
		d := r.Deployed
		fmt.Printf("Deployed:     checksum %s", shortChecksum(d.Checksum))
		if d.MelangeVersion != "" {
			fmt.Printf(" · melange %s", d.MelangeVersion)
		}
		if d.MigratedAt != "" {
			fmt.Printf(" · %s", d.MigratedAt)
		}
		fmt.Println()
		fmt.Printf("Sync:         %s\n", syncDescription(r.Sync))
		printDriftDetail(r.Drift)
		if !d.ModelRecorded {
			fmt.Println("              model DSL not recorded — re-migrate to enable `melange schema pull`")
		}
	}

	for _, note := range r.Notes {
		fmt.Printf("Note:         %s\n", note)
	}

	// Actionable hints, matching the pre-enrichment behavior.
	if !s.SchemaExists {
		fmt.Printf("\nNo schema found at %s\n", schemaPath)
	} else if !s.TuplesExists {
		fmt.Println("\nTuples view not found.")
		fmt.Println("Create melange_tuples before running checks.")
	}
}

// maxDriftLines caps the change list in text output. A full model rewrite can
// produce hundreds of changes; status is a summary, and `melange diff` prints
// the complete list.
const maxDriftLines = 5

// printDriftDetail prints the change summary under the Sync line. Nil (no
// detail available) prints nothing — the Sync line already says drift.
func printDriftDetail(d *driftReport) {
	if d == nil {
		return
	}
	fmt.Printf("              %d breaking, %d additive\n", d.Breaking, d.Additive)
	for i, c := range d.Changes {
		if i == maxDriftLines {
			fmt.Printf("              … and %d more (`melange diff` for the full list)\n", len(d.Changes)-maxDriftLines)
			break
		}
		marker := "+"
		if c.Class == schema.ClassBreaking {
			marker = "-"
		}
		fmt.Printf("              %s %s\n", marker, c.Summary)
	}
}

func syncDescription(sync string) string {
	switch sync {
	case syncInSync:
		return "in sync — local schema matches deployed"
	case syncDrift:
		return "drift — local schema differs from deployed (`melange diff` to see changes, `melange migrate` to apply)"
	case syncDatabaseAhead:
		return "database ahead — deployed model is not any version of your local schema in recent git history; someone else may have migrated it"
	case syncUnknown:
		return "unknown — no local schema file to compare"
	default:
		return "not recorded"
	}
}
