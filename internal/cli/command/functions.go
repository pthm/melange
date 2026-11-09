package command

import (
	"bufio"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/cli/style"
	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

var (
	functionsDB       string
	functionsDBSchema string
	functionsSchema   string
	functionsFormat   string
	functionsTypes    []string
	functionsKinds    []string
	functionsExposed  bool
	functionsInternal bool
)

var functionsCmd = &cobra.Command{
	Use:   "functions",
	Short: "List the SQL functions generated for each relation",
	Long: `Functions lists the SQL API melange generates for the model: for every
relation, whether the exposure exposes it or it is internal (modeling only),
and each function a migration installs for it, under its exact name (long
names are truncated to 63 bytes with a hash), with its kind and whether
applications are meant to call it. The dispatchers follow.

The source is chosen as diff chooses it:
  - with --db, --env or MELANGE_ENV: the model and exposure the database's
    last migration recorded, checked against pg_proc in --db-schema. Each
    function is installed, missing, or installed with other arguments or
    another body; stale overloads, orphans the last migration recorded and
    unmanaged functions named like generated ones are listed too. A
    database that records no model is compared with --schema: under the
    recorded exposure when a migration is recorded without its model, else
    under the sql section of the configuration.
  - otherwise: the schema file and the sql section of the configuration
    (with the exposure flags), without a database; statuses are "not
    checked".

'melange functions show <type#relation>' adds each function's signature, an
example call, the functions it may call, and why a family is not installed.
It never writes to the database.`,
	Example: `  # The SQL API of the local schema and melange.yaml
  melange functions

  # What the database has installed, and what drifted
  melange functions --db postgres://localhost/mydb

  # One relation, with signatures and example calls
  melange functions show document#viewer

  # The exposed accessible functions, as JSON
  melange functions --kind accessible --kind parent_key --exposed --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if functionsExposed && functionsInternal {
			return cli.ConfigError("--exposed and --internal are mutually exclusive", nil)
		}
		for _, k := range functionsKinds {
			if !slices.Contains(sqlgen.FunctionKinds, sqlgen.FunctionKind(k)) {
				return cli.ConfigError(fmt.Sprintf("invalid --kind %q (want one of %s)", k, kindList()), nil)
			}
		}
		r, err := functionsReport(cmd)
		if err != nil {
			return err
		}
		for _, typ := range functionsTypes {
			if !slices.ContainsFunc(r.Relations, func(rel sqlapi.Relation) bool { return rel.Type == typ }) {
				return cli.GeneralError(fmt.Sprintf("--type %q: the model has no type of that name, or it has no relations", typ), nil)
			}
		}
		filterFunctions(r, functionsTypes, functionsKinds, functionsExposed, functionsInternal)
		return writeFunctions(os.Stdout, r, func(w io.Writer) { printFunctionsText(w, r) })
	},
}

var functionsShowCmd = &cobra.Command{
	Use:   "show <type#relation>",
	Short: "Show one relation's functions with signatures and example calls",
	Long: `Show prints one relation's functions: name, kind, public or internal,
status, signature, an example call that runs as written (given what its
note asks for), how an application uses the result, the functions its body
may call (from the model analysis), and why each function family that is
not installed is not.

The relation is type#relation; type.relation also works, split at the dot
that names a relation.`,
	Example: `  melange functions show document#viewer
  melange functions show document#viewer --db postgres://localhost/mydb --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := functionsReport(cmd)
		if err != nil {
			return err
		}
		rel, err := resolveRelationArg(r, args[0])
		if err != nil {
			return err
		}
		// Only what concerns the relation; the summary still counts the
		// whole model.
		r.Relations, r.Dispatchers, r.Orphans, r.Unmanaged = []sqlapi.Relation{*rel}, nil, nil, nil
		r.Collisions = slices.DeleteFunc(r.Collisions, func(c sqlgen.NameCollision) bool {
			return !slices.ContainsFunc(rel.Functions, func(f sqlapi.Function) bool { return f.Name == c.Name })
		})
		return writeFunctions(os.Stdout, r, func(w io.Writer) { printRelationText(w, r, rel) })
	},
}

func init() {
	pf := functionsCmd.PersistentFlags()
	pf.StringVar(&functionsDB, "db", "", "database URL: report the deployed model and what is installed")
	pf.StringVar(&functionsDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	pf.StringVar(&functionsSchema, "schema", "", "path to schema.fga or fga.mod (without a database, or when it records no model)")
	pf.StringVar(&functionsFormat, "format", "text", "output format: text (default) or json")
	addExposureFlags(pf)

	f := functionsCmd.Flags()
	f.StringSliceVar(&functionsTypes, "type", nil, "only this type's relations (repeatable)")
	f.StringSliceVar(&functionsKinds, "kind", nil, "only functions of this kind (repeatable): "+kindList())
	f.BoolVar(&functionsExposed, "exposed", false, "only exposed relations")
	f.BoolVar(&functionsInternal, "internal", false, "only internal relations (not exposed, modeling only)")

	functionsCmd.AddCommand(functionsShowCmd)
}

func kindList() string {
	kinds := make([]string, len(sqlgen.FunctionKinds))
	for i, k := range sqlgen.FunctionKinds {
		kinds[i] = string(k)
	}
	return strings.Join(kinds, ", ")
}

// exposureFlagNames are the flags addExposureFlags registers.
func exposureFlagNames() []string {
	names := make([]string, 0, 2+len(exposureFamilies))
	names = append(names, "include-relation", "exclude-relation")
	for _, family := range exposureFamilies {
		names = append(names, family.flag)
	}
	return names
}

// functionsReport validates the flags, then builds the report from the
// source they choose.
func functionsReport(cmd *cobra.Command) (*sqlapi.Report, error) {
	if functionsFormat != "text" && functionsFormat != "json" {
		return nil, cli.ConfigError(fmt.Sprintf("invalid --format %q (want text or json)", functionsFormat), nil)
	}
	schemaPath := resolveString(functionsSchema, cfg.Schema)
	databaseSchema := resolveString(functionsDBSchema, cfg.Database.Schema, "public")
	explicitDB := functionsDB != "" || resolveString(envFlag, os.Getenv("MELANGE_ENV")) != ""

	if !explicitDB {
		if schemaPath == "" {
			return nil, cli.ConfigError("no schema path: set --schema or `schema` in config, or pass --db to read the deployed model", nil)
		}
		exposure, err := resolveExposure(cmd, cfg.SQL)
		if err != nil {
			return nil, err
		}
		types, err := parser.ParseSchema(schemaPath)
		if err != nil {
			return nil, cli.SchemaParseError("parsing schema", err)
		}
		r, err := sqlapi.Describe(types, exposure, databaseSchema)
		if err != nil {
			return nil, cli.GeneralError("building the SQL API catalog", err)
		}
		r.SchemaFile = schemaPath
		return r, nil
	}

	for _, name := range exposureFlagNames() {
		if cmd.Flags().Changed(name) {
			return nil, cli.ConfigError("--"+name+" applies to a schema file; against a database, melange functions reports the exposure its last migration recorded", nil)
		}
	}
	dsn, err := resolveDSN(functionsDB)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		return nil, cli.DBConnectError("connecting to database", err)
	}
	opts := sqlapi.CheckOptions{Bodies: true}
	r, _, err := sqlapi.Deployed(ctx, db, databaseSchema, nil, opts)
	// The configured schema is the fallback when it exists; the default path
	// usually does not in a project that never ran init.
	if errors.Is(err, sqlapi.ErrNoRecordedModel) && schemaPath != "" && (functionsSchema != "" || fileExists(schemaPath)) {
		var types []schema.TypeDefinition
		if types, err = parser.ParseSchema(schemaPath); err != nil {
			return nil, cli.SchemaParseError("parsing schema", err)
		}
		// The exposure flags were refused above, so this is the config's.
		var exposure compiler.Exposure
		if exposure, err = resolveExposure(cmd, cfg.SQL); err != nil {
			return nil, err
		}
		r, _, err = sqlapi.Deployed(ctx, db, databaseSchema, &sqlapi.Fallback{Types: types, Exposure: exposure}, opts)
		if r != nil {
			r.SchemaFile = schemaPath
		}
	}
	if errors.Is(err, sqlapi.ErrNoRecordedModel) {
		return nil, cli.GeneralError(err.Error()+"; pass --schema to compare a schema file", nil)
	}
	if err != nil {
		return nil, cli.GeneralError("reading the deployed SQL API", err)
	}
	return r, nil
}

// filterFunctions keeps the relations and functions the list filters
// select. The dispatchers stay unless a type, an exposure or a kind other
// than dispatcher is asked for. The summary keeps counting everything.
func filterFunctions(r *sqlapi.Report, types, kinds []string, exposed, internal bool) {
	kindOK := func(k sqlgen.FunctionKind) bool { return len(kinds) == 0 || slices.Contains(kinds, string(k)) }
	var rels []sqlapi.Relation
	for _, rel := range r.Relations {
		if (len(types) > 0 && !slices.Contains(types, rel.Type)) || (exposed && !rel.Exposed) || (internal && rel.Exposed) {
			continue
		}
		if len(kinds) > 0 {
			rel.Functions = slices.DeleteFunc(slices.Clone(rel.Functions), func(f sqlapi.Function) bool { return !kindOK(f.Kind) })
			rel.NotInstalled = slices.DeleteFunc(slices.Clone(rel.NotInstalled), func(n sqlgen.NotInstalled) bool { return !kindOK(n.Kind) })
			if len(rel.Functions) == 0 {
				continue
			}
		}
		rels = append(rels, rel)
	}
	r.Relations = rels
	if len(types) > 0 || exposed || internal || !kindOK(sqlgen.KindDispatcher) {
		r.Dispatchers = nil
	}
}

// resolveRelationArg finds type#relation, or type.relation split at the
// '.' that names a relation (both names can hold dots; code generation keys
// relations by type.relation, so only one split can).
func resolveRelationArg(r *sqlapi.Report, arg string) (*sqlapi.Relation, error) {
	if typ, rel, ok := strings.Cut(arg, "#"); ok {
		if found := r.Find(typ, rel); found != nil {
			return found, nil
		}
		return nil, unknownRelation(r, arg, typ, true)
	}
	for i := range len(arg) {
		if arg[i] == '.' {
			if rel := r.Find(arg[:i], arg[i+1:]); rel != nil {
				return rel, nil
			}
		}
	}
	typ, _, _ := strings.Cut(arg, ".")
	return nil, unknownRelation(r, arg, typ, false)
}

// unknownRelation is the error for an argument that names no relation:
// the type's relations when typ has some, else that typ has none or, when
// arg had no '#', the expected form.
func unknownRelation(r *sqlapi.Report, arg, typ string, hashed bool) error {
	var rels []string
	for _, rel := range r.Relations {
		if rel.Type == typ {
			rels = append(rels, rel.Relation)
		}
	}
	msg := fmt.Sprintf("%s is not a relation of the model", arg)
	switch {
	case len(rels) > 0:
		msg += fmt.Sprintf(" (%s has %s)", typ, strings.Join(rels, ", "))
	case hashed:
		msg += fmt.Sprintf(" (%s is not a type with relations)", typ)
	default:
		msg += "; use type#relation"
	}
	return cli.GeneralError(msg, nil)
}

// writeFunctions writes r as JSON or with text, and reports a failed write.
func writeFunctions(dst io.Writer, r *sqlapi.Report, text func(io.Writer)) error {
	w := bufio.NewWriter(dst)
	if functionsFormat == "json" {
		out, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return cli.GeneralError("encoding JSON", err)
		}
		_, _ = fmt.Fprintln(w, string(out))
	} else {
		text(w)
	}
	if err := w.Flush(); err != nil {
		return cli.GeneralError("writing output", err)
	}
	return nil
}

// printFunctionsHeader prints where the report comes from, its counts and
// its notes, which say how to read the statuses (codegen skew, no record).
func printFunctionsHeader(w io.Writer, r *sqlapi.Report) {
	st := style.StatusStyles
	switch {
	case r.Source == "database" && r.SchemaFile != "":
		_, _ = fmt.Fprintf(w, "Source:    database schema %s, compared with %s\n", r.DatabaseSchema, r.SchemaFile)
	case r.Source == "database":
		_, _ = fmt.Fprintf(w, "Source:    database schema %s (deployed model)\n", r.DatabaseSchema)
	default:
		_, _ = fmt.Fprintf(w, "Source:    %s %s\n", r.SchemaFile, st.Muted.Render("(not checked against a database)"))
	}
	exposure := "default (every relation, every family)"
	if !r.Exposure.IsDefault() {
		if data, err := json.Marshal(r.Exposure); err == nil {
			exposure = string(data)
		}
	}
	_, _ = fmt.Fprintf(w, "Exposure:  %s\n", exposure)
	s := r.Summary
	_, _ = fmt.Fprintf(w, "Functions: %d for %d relations (%d exposed), dispatchers included\n", s.Functions, s.Relations, s.Exposed)
	if r.Checked {
		_, _ = fmt.Fprintf(w, "Status:    %s\n", summaryText(r))
	}
	for _, n := range r.Notes {
		_, _ = fmt.Fprintf(w, "\n%s %s\n", st.Warn.Render("Note:"), n)
	}
}

func summaryText(r *sqlapi.Report) string {
	st := style.StatusStyles
	s := r.Summary
	if !s.Drift() {
		msg := fmt.Sprintf("all %d installed", s.Functions)
		if !r.BodiesChecked {
			msg += " (bodies not compared)"
		}
		return st.Good.Render(msg)
	}
	var parts []string
	for _, c := range []struct {
		n    int
		what string
	}{
		{s.Missing, "missing"},
		{s.SignatureDiffers, "with other arguments"},
		{s.BodyChanged, "with a changed body"},
		{s.CodegenDiffers, "generated differently by this melange"},
		{s.StaleOverloads, "stale overloads"},
		{s.Orphans, "orphans"},
	} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	return st.Warn.Render(fmt.Sprintf("%d of %d installed; ", s.Installed, s.Functions) + strings.Join(parts, ", "))
}

// statusText renders a function's status, with its stale overloads.
func statusText(f sqlapi.Function) string {
	st := style.StatusStyles
	var s string
	switch f.Status {
	case sqlapi.StatusNotChecked:
		s = st.Muted.Render("not checked")
	case sqlapi.StatusInstalled:
		s = st.Good.Render("installed")
	case sqlapi.StatusMissing:
		s = st.Bad.Render("missing")
	default:
		s = st.Warn.Render(strings.ReplaceAll(string(f.Status), "_", " "))
	}
	if n := len(f.StaleOverloads); n > 0 {
		s += " " + st.Warn.Render(fmt.Sprintf("+%d stale overload%s", n, plural(n)))
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func visibility(public bool) string {
	if public {
		return "public"
	}
	return "internal"
}

// nameWidth is the width of the longest function name in r.
func nameWidth(r *sqlapi.Report) int {
	n := 0
	for _, rel := range r.Relations {
		for _, f := range rel.Functions {
			n = max(n, len(f.Name))
		}
	}
	for _, d := range r.Dispatchers {
		n = max(n, len(d.Name))
	}
	return n
}

// printFunctionsText prints the list: types, then relations, then each
// function's name, kind, visibility and status.
func printFunctionsText(w io.Writer, r *sqlapi.Report) {
	w = style.Writer(w)
	st := style.StatusStyles
	printFunctionsHeader(w, r)
	width := nameWidth(r)
	row := func(f sqlapi.Function) {
		line := fmt.Sprintf("    %-*s  %-13s  %-8s  %s", width, f.Name, f.Kind, visibility(f.Public), statusText(f))
		if f.Truncated {
			line += " " + st.Muted.Render("(truncated)")
		}
		_, _ = fmt.Fprintln(w, line)
	}
	typ := ""
	for _, rel := range r.Relations {
		if rel.Type != typ {
			typ = rel.Type
			_, _ = fmt.Fprintf(w, "\n%s\n", st.Bold.Render(typ))
		}
		label := st.Good.Render("exposed")
		if !rel.Exposed {
			label = st.Muted.Render("internal")
		}
		_, _ = fmt.Fprintf(w, "  %s  %s\n", rel.Relation, label)
		if len(rel.Functions) == 0 {
			_, _ = fmt.Fprintf(w, "    %s\n", st.Muted.Render("no functions installed (melange functions show "+rel.Type+"#"+rel.Relation+" says why)"))
		}
		for _, f := range rel.Functions {
			row(f)
		}
	}
	if len(r.Dispatchers) > 0 {
		_, _ = fmt.Fprintf(w, "\n%s\n", st.Bold.Render("dispatchers"))
		for _, d := range r.Dispatchers {
			row(d)
		}
	}
	printFunctionsFooter(w, r)
}

// printFunctionsFooter prints orphans, unmanaged functions and collisions.
func printFunctionsFooter(w io.Writer, r *sqlapi.Report) {
	st := style.StatusStyles
	list := func(title string, fns []sqlapi.InstalledFunction) {
		if len(fns) == 0 {
			return
		}
		_, _ = fmt.Fprintf(w, "\n%s\n", title)
		for _, f := range fns {
			_, _ = fmt.Fprintf(w, "  %s(%s)\n", f.Name, f.Arguments)
		}
	}
	if r.CodegenAhead {
		list(st.Warn.Render("Orphans")+" (recorded by the last migration, run by a newer melange; this melange does not generate them, and its migrate leaves them):", r.Orphans)
	} else {
		list(st.Warn.Render("Orphans")+" (recorded by the last migration, no longer generated; melange migrate drops them):", r.Orphans)
	}
	list(st.Muted.Render("Unmanaged")+" (named like generated functions, recorded by no migration; never dropped):", r.Unmanaged)
	if len(r.Collisions) > 0 {
		_, _ = fmt.Fprintf(w, "\n%s function names are generated for more than one relation:\n", st.Warn.Render("Warning:"))
		for _, c := range r.Collisions {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", c.Name, strings.Join(c.Owners, ", "))
		}
	}
}

// printRelationText prints one relation in full.
func printRelationText(w io.Writer, r *sqlapi.Report, rel *sqlapi.Relation) {
	w = style.Writer(w)
	st := style.StatusStyles
	label := st.Good.Render("exposed")
	if !rel.Exposed {
		label = st.Muted.Render("internal (not exposed: modeling only; the public dispatchers not listed under Answered by raise M2003 for it)")
	}
	_, _ = fmt.Fprintf(w, "%s  %s\n", st.Bold.Render(rel.Type+"#"+rel.Relation), label)
	printFunctionsHeader(w, r)
	none := st.Muted.Render("none")
	field := func(name, value string) {
		if value != "" {
			_, _ = fmt.Fprintf(w, "  %-11s %s\n", name+":", value)
		}
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "%-12s %s\n", "Answered by:", cmp.Or(strings.Join(rel.Dispatchers, ", "), none))
	_, _ = fmt.Fprintf(w, "%-12s %s\n", "Depends on:", cmp.Or(strings.Join(rel.DependsOn, ", "), none))
	for _, f := range rel.Functions {
		_, _ = fmt.Fprintf(w, "\n%s\n", st.Bold.Render(f.Name))
		field("kind", fmt.Sprintf("%s, %s · %s", f.Kind, visibility(f.Public), statusText(f)))
		if f.Truncated {
			field("name", "truncated to 63 bytes with a hash of the type and relation; copy it from here or the generated client")
		}
		paged := ""
		if f.Signature.Paged {
			paged = ", paged"
		}
		field("signature", fmt.Sprintf("%s%s · LANGUAGE %s%s", f.Name, f.Signature.String(), f.Signature.Language, paged))
		if f.Link != "" {
			field("link", fmt.Sprintf("%s → %s (reads %s)", f.Link, f.ParentType, strings.Join(f.ParentRelations, ", ")))
		}
		field("example", f.Example)
		field("usage", f.Usage)
		field("calls", strings.Join(f.Calls, ", "))
		field("note", f.Note)
		field("detail", f.Detail)
		if len(f.StaleOverloads) > 0 {
			field("overloads", strings.Join(f.StaleOverloads, "; "))
		}
	}
	if len(rel.NotInstalled) > 0 {
		_, _ = fmt.Fprintf(w, "\n%s\n", st.Bold.Render("Not installed"))
		for _, n := range rel.NotInstalled {
			_, _ = fmt.Fprintf(w, "  %-13s %s: %s\n", n.Kind, strings.ReplaceAll(string(n.Reason), "_", " "), n.Detail)
		}
	}
	printFunctionsFooter(w, r)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
