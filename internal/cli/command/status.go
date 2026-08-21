package command

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
			report.Drift = driftDetail(rec, schemaPath, &report.Notes)
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

// driftDetail diffs the deployed model against the local schema so `status` can
// say what drifted, not just that something did. Every failure to get there
// (a record predating model storage, an unparseable side) yields nil plus a
// note: the checksum-level drift verdict already stands on its own.
func driftDetail(rec *migrator.MigrationRecord, schemaPath string, notes *[]string) *driftReport {
	deployed, derr := deployedTypesFromRecord(rec)
	if derr != nil {
		*notes = append(*notes, fmt.Sprintf("could not read the deployed model for a detailed diff: %v", derr))
		return nil
	}
	if deployed == nil {
		return nil // pre-storage record; the ModelRecorded hint already covers it
	}
	local, lerr := parser.ParseSchema(schemaPath)
	if lerr != nil {
		*notes = append(*notes, fmt.Sprintf("could not parse local schema for a detailed diff: %v", lerr))
		return nil
	}
	return summarizeDrift(deployed, local)
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
	case syncUnknown:
		return "unknown — no local schema file to compare"
	default:
		return "not recorded"
	}
}
