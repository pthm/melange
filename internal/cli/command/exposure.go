package command

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// exposureFamilies are the optional function families, with their --no-*
// flag and their sql.functions field.
var exposureFamilies = []struct {
	flag  string
	key   string
	field func(*cli.SQLFunctionsConfig) *bool
}{
	{"no-list-objects", "list_objects", func(f *cli.SQLFunctionsConfig) *bool { return &f.ListObjects }},
	{"no-list-subjects", "list_subjects", func(f *cli.SQLFunctionsConfig) *bool { return &f.ListSubjects }},
	{"no-accessible", "accessible", func(f *cli.SQLFunctionsConfig) *bool { return &f.Accessible }},
	{"no-parent-key", "parent_key", func(f *cli.SQLFunctionsConfig) *bool { return &f.ParentKey }},
	{"no-expand", "expand", func(f *cli.SQLFunctionsConfig) *bool { return &f.Expand }},
	{"no-explain", "explain", func(f *cli.SQLFunctionsConfig) *bool { return &f.Explain }},
}

// addExposureFlags registers the SQL API exposure flags, which override the
// sql section of the configuration for one run. They are read back by
// resolveExposure.
func addExposureFlags(f *pflag.FlagSet) {
	f.StringSlice("include-relation", nil, "expose only relations matching this pattern, relation or type#relation (repeatable; replaces sql.relations.include)")
	f.StringSlice("exclude-relation", nil, "do not expose relations matching this pattern (repeatable; replaces sql.relations.exclude)")
	for _, family := range exposureFamilies {
		f.Bool(family.flag, false, fmt.Sprintf("turn off the %s function family (sets sql.functions.%s to false)", family.key, family.key))
	}
}

// resolveExposure returns the exposure a command applies: the sql section of
// the configuration (file, then MELANGE_SQL_* variables, over the defaults)
// with the exposure flags given to cmd on top. It is always explicit: an
// absent sql section is the default exposure, so a migrate without one
// widens a restricted database back to the default.
func resolveExposure(cmd *cobra.Command, sqlCfg cli.SQLConfig) (compiler.Exposure, error) {
	f := cmd.Flags()
	if err := applyRelationFlags(f, &sqlCfg.Relations); err != nil {
		return compiler.Exposure{}, err
	}
	for _, family := range exposureFamilies {
		if !f.Changed(family.flag) {
			continue
		}
		off, err := f.GetBool(family.flag)
		if err != nil {
			return compiler.Exposure{}, cli.ConfigError("reading --"+family.flag, err)
		}
		*family.field(&sqlCfg.Functions) = !off
	}
	return sqlCfg.Exposure(), nil
}

// applyRelationFlags replaces relations' include and exclude lists with the
// --include-relation and --exclude-relation values, for the flags given.
func applyRelationFlags(f *pflag.FlagSet, relations *cli.SQLRelationsConfig) error {
	for _, list := range []struct {
		flag   string
		target *[]string
	}{
		{"include-relation", &relations.Include},
		{"exclude-relation", &relations.Exclude},
	} {
		if !f.Changed(list.flag) {
			continue
		}
		patterns, err := f.GetStringSlice(list.flag)
		if err != nil {
			return cli.ConfigError("reading --"+list.flag, err)
		}
		patterns = cli.TrimPatterns(patterns)
		for _, p := range patterns {
			if err := compiler.ValidateRelationPattern(p); err != nil {
				return cli.ConfigError("--"+list.flag, err)
			}
		}
		*list.target = patterns
	}
	return nil
}

// warnUnmatchedPatterns warns about relation patterns that match no relation
// of the schema, usually a typo or a renamed relation. Only the patterns are
// checked: a schema that does not parse is left for the command to report.
func warnUnmatchedPatterns(schemaPath string, exposure compiler.Exposure) {
	n := exposure.Normalized()
	if quiet || len(n.Relations.Include)+len(n.Relations.Exclude) == 0 {
		return
	}
	types, err := parser.ParseSchema(schemaPath)
	if err != nil {
		return
	}
	analyses := compiler.AnalyzeRelations(types, schema.ComputeRelationClosure(types))
	for _, p := range n.UnmatchedPatterns(analyses) {
		fmt.Fprintf(os.Stderr, "warning: SQL API exposure pattern %q matches no relation in %s\n", p, schemaPath)
	}
}
