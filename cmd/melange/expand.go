package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/cmd/melange/internal/render"
	"github.com/pthm/melange/lib/cli"
	"github.com/pthm/melange/melange"
)

var (
	expandDB          string
	expandDBSchema    string
	expandFormat      string
	expandSubjectType string
	expandMaxLeaf     int
	expandFlatten     bool
	expandColor       string
)

var expandCmd = &cobra.Command{
	Use:   "expand <object> <relation>",
	Short: "Show who has a permission on an object (OpenFGA-shaped UsersetTree)",
	Long: `Expand prints the OpenFGA UsersetTree for the given (object, relation):
a tree of who has the permission, broken down by the rewrites in the schema.

Resolution is shallow by default — computed-userset rewrites and TTU
("relation from parent") rewrites surface as unresolved pointers
(leaf.computed / leaf.tuple_to_userset) that callers chase with follow-up
Expand calls. This matches OpenFGA's wire format so existing tooling
(UI builders, audit exporters, SDK consumers) deserialises the response
unchanged.

For the simpler "give me a flat list of users with access" use case, pass
--flatten — the CLI walks every Leaf.Users entry in the tree and prints
the deduplicated, sorted list. Computed/TTU pointers are NOT chased by
--flatten; consumers that want recursive resolution should use
Checker.ExpandRecursive from the Go or TypeScript client.

Subject and object are typed identifiers in "<type>:<id>" form. Examples:

  melange expand document:1 viewer
  melange expand repository:42 can_write

Use --format=json to emit the raw UsersetTree JSONB. --subject-type
narrows the Leaf.Users arrays to one subject type (Melange extension —
OpenFGA returns all subject types together). --max-leaf caps each
Leaf.Users array; capped leaves carry users_truncated: true.`,
	Example: `  # Show the rewrite tree for document:1's viewer relation
  melange expand document:1 viewer --db postgres://localhost/mydb

  # Just give me a flat list of users with access
  melange expand document:1 viewer --flatten --db postgres://localhost/mydb

  # Narrow to one subject type
  melange expand document:1 viewer --subject-type=user

  # Raw JSON for OpenFGA-compatible tooling
  melange expand document:1 viewer --format=json`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseSchema := resolveString(expandDBSchema, cfg.Database.Schema)

		dsn, err := resolveDSN(expandDB)
		if err != nil {
			return err
		}

		object, err := parseTypedIdent(args[0], "object")
		if err != nil {
			return err
		}
		relation := melange.Relation(args[1])

		return runExpand(dsn, databaseSchema, object, relation,
			expandFormat, expandSubjectType, expandMaxLeaf, expandFlatten)
	},
}

func init() {
	f := expandCmd.Flags()
	f.StringVar(&expandDB, "db", "", "database URL")
	f.StringVar(&expandDBSchema, "db-schema", "public", "database schema")
	f.StringVar(&expandFormat, "format", "tree", "output format: tree (default) or json")
	f.StringVar(&expandSubjectType, "subject-type", "", "Melange extension: narrow Leaf.Users to this subject type (empty = no filter)")
	f.IntVar(&expandMaxLeaf, "max-leaf", 0, "Melange extension: cap entries per Leaf.Users (0 = unbounded, OpenFGA-equivalent)")
	f.BoolVar(&expandFlatten, "flatten", false, "print a flat deduplicated user list instead of the tree (Leaf.Users only; does not chase computed/TTU pointers)")
	f.StringVar(&expandColor, "color", "auto", "colour output: auto|always|never (auto = stdout-is-TTY and NO_COLOR unset)")
}

func runExpand(dsn, databaseSchema string, object melange.Object, relation melange.Relation, format, subjectType string, maxLeaf int, flatten bool) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	checker := melange.NewChecker(db, melange.WithDatabaseSchema(databaseSchema))

	var opts []melange.ExpandOption
	if subjectType != "" {
		opts = append(opts, melange.WithSubjectTypeFilter(melange.ObjectType(subjectType)))
	}
	if maxLeaf > 0 {
		opts = append(opts, melange.WithExpandMaxLeaf(maxLeaf))
	}

	ctx := context.Background()
	tree, err := checker.Expand(ctx, object, relation, opts...)
	if err != nil {
		return cli.GeneralError("expand", err)
	}

	if flatten {
		for _, u := range tree.FlattenUsers() {
			fmt.Println(u)
		}
		return nil
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tree)
	case "tree", "":
		render.Expand(os.Stdout, tree, render.WithColor(shouldColorize(expandColor)))
		return nil
	default:
		return cli.GeneralError("output format", fmt.Errorf("unknown format %q (want tree|json)", format))
	}
}
