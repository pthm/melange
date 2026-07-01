package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/cmd/melange/internal/render"
	"github.com/pthm/melange/lib/cli"
	"github.com/pthm/melange/melange"
)

var (
	explainDB       string
	explainDBSchema string
	explainFormat   string
	explainMaxNodes int
	explainColor    string
)

var explainCmd = &cobra.Command{
	Use:   "explain <subject> <relation> <object>",
	Short: "Show why a permission check returns true or false",
	Long: `Explain prints the resolution path the authorization engine walked when
deciding whether subject has relation on object. The output is a tree of
nodes — direct grants, implied rewrites, userset references, etc. — each
either contributing to the proof (on success) or recording a failed branch
(on denial, the "tried 3 paths, all failed" view).

Subject and object are typed identifiers in "<type>:<id>" form. For
example:

  melange explain user:alice viewer document:1
  melange explain user:bob can_write repository:42

Use --format=json to emit the raw JSONB trace; otherwise a unicode-tree
pretty-print is rendered to stdout. --max-nodes caps the trace size;
when the cap is hit the rendered output ends in "... truncated".`,
	Example: `  # Inspect a successful permission
  melange explain user:alice viewer document:1 --db postgres://localhost/mydb

  # Debug a denied check — see every branch the engine attempted
  melange explain user:bob viewer document:1 --db postgres://localhost/mydb

  # Raw JSONB output for tooling
  melange explain user:alice viewer document:1 --format=json`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseSchema := resolveString(explainDBSchema, cfg.Database.Schema)

		dsn, err := resolveDSN(explainDB)
		if err != nil {
			return err
		}

		subject, err := parseTypedIdent(args[0], "subject")
		if err != nil {
			return err
		}
		relation := melange.Relation(args[1])
		object, err := parseTypedIdent(args[2], "object")
		if err != nil {
			return err
		}

		return runExplain(dsn, databaseSchema, subject, relation, object, explainFormat, explainMaxNodes)
	},
}

func init() {
	f := explainCmd.Flags()
	f.StringVar(&explainDB, "db", "", "database URL")
	f.StringVar(&explainDBSchema, "db-schema", "public", "database schema")
	f.StringVar(&explainFormat, "format", "tree", "output format: tree (default) or json")
	f.IntVar(&explainMaxNodes, "max-nodes", 0, "cap total nodes in the trace (0 = session GUC melange.max_explain_nodes or built-in 100)")
	f.StringVar(&explainColor, "color", "auto", "color output: auto|always|never (auto = stdout-is-TTY and NO_COLOR unset)")
}

// shouldColorize resolves the --color mode to a boolean. auto detects a TTY
// on stdout and honors the NO_COLOR convention (https://no-color.org).
func shouldColorize(mode string) bool {
	switch strings.ToLower(mode) {
	case "always":
		return true
	case "never":
		return false
	case "auto", "":
		if os.Getenv("NO_COLOR") != "" {
			return false
		}
		fi, err := os.Stdout.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	default:
		return false
	}
}

// parseTypedIdent splits "<type>:<id>" into a melange.Object. Empty type or id
// is a usage error rather than a SQL error so the user sees the problem at
// argument parse time.
func parseTypedIdent(raw, role string) (melange.Object, error) {
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 || colon == len(raw)-1 {
		return melange.Object{}, cli.GeneralError(role+" identifier",
			fmt.Errorf("expected <type>:<id>, got %q", raw))
	}
	return melange.Object{
		Type: melange.ObjectType(raw[:colon]),
		ID:   raw[colon+1:],
	}, nil
}

func runExplain(dsn, databaseSchema string, subject melange.Object, relation melange.Relation, object melange.Object, format string, maxNodes int) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))

	ctx := context.Background()
	var opts []melange.ExplainOption
	if maxNodes > 0 {
		opts = append(opts, melange.WithExplainMaxNodes(maxNodes))
	}

	trace, err := checker.Explain(ctx, subject, relation, object, opts...)
	if err != nil {
		return cli.GeneralError("explain", err)
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(trace)
	case "tree", "":
		render.Trace(os.Stdout, trace, render.WithColor(shouldColorize(explainColor)))
		return nil
	default:
		return cli.GeneralError("output format", fmt.Errorf("unknown format %q (want tree|json)", format))
	}
}
