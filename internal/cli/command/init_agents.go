package command

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
)

var initAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Print Melange instructions for coding agents",
	Long: `Print a Markdown section describing this project's Melange setup and
workflow for coding agents, filled in from the config file. Append it to the
file your agent reads at the start of a session, such as AGENTS.md or
CLAUDE.md. melange init offers to add the same section when it creates a
project.`,
	Example: `  # Add the section to AGENTS.md
  melange init agent >> AGENTS.md

  # Or to CLAUDE.md, for Claude Code
  melange init agent >> CLAUDE.md`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// A leading blank line keeps the heading off the last line of a file
		// being appended to, even one without a trailing newline.
		fmt.Print("\n" + agentInstructions(answersFromConfig(cfg), displayPath(configPath)))
		return nil
	},
}

func init() {
	initCmd.AddCommand(initAgentCmd)
}

// answersFromConfig maps a loaded config onto the init answers that shape the
// agent instructions. Client generation and versioned migrations count as
// enabled when their output directory is configured, as migrate assumes.
func answersFromConfig(c *cli.Config) *initAnswers {
	a := &initAnswers{
		SchemaPath:        c.Schema,
		GenerateCode:      c.Generate.Client.Output != "",
		ClientOutput:      c.Generate.Client.Output,
		MigrationStrategy: "builtin",
		MigrationOutput:   c.Generate.Migration.Output,
	}
	if a.MigrationOutput != "" {
		a.MigrationStrategy = "versioned"
	}
	return a
}

// displayPath shows path relative to the working directory when it is inside
// it, since the instructions are read from the project root.
func displayPath(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	if rel, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// agentsHeading opens the section init adds to a project's coding-agent
// instructions. A file that already contains it is left alone, so re-running
// init never duplicates the section.
const agentsHeading = "## Authorization (Melange)"

// agentsFiles are the instructions files init knows about, in preference
// order: AGENTS.md is read by Codex, Cursor, and most agents.
var agentsFiles = []string{"AGENTS.md", "CLAUDE.md"}

// agentsFile picks the instructions file to add the section to: the first of
// agentsFiles that exists, else a new AGENTS.md. Only one file is written
// because a CLAUDE.md often just imports AGENTS.md, and the section would then
// appear twice.
func agentsFile() string {
	for _, name := range agentsFiles {
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return agentsFiles[0]
}

// agentInstructions renders the section for a project's setup. It is the one
// source of the section: init writes it, `melange init agent` prints it, and
// docs/content/docs/guides/coding-agents.md shows sample output. configPath
// may be empty when the project has no config file.
//
// The section is loaded into every agent session, so each line has to earn
// its tokens: facts the agent cannot infer from the repo, the edit loop, and
// guardrails against the mistakes that waste turns (inserting tuples, running
// migrate against a shared database, improvising a Postgres in a sandbox).
// Commands use text output; agents read it as well as JSON, and it is shorter.
func agentInstructions(a *initAnswers, configPath string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w(agentsHeading)
	w("")
	w("Permissions are an OpenFGA model that Melange compiles into PostgreSQL functions.")
	w("")
	if configPath != "" {
		w("- Model: `%s`. Config: `%s`.", a.SchemaPath, configPath)
	} else {
		w("- Model: `%s`.", a.SchemaPath)
	}
	w("- Relationships come from the `melange_tuples` view over our tables, defined in our own migrations (Melange doesn't create it). Never insert tuples; change the underlying rows, or the view definition.")
	if a.GenerateCode {
		w("- `%s` is generated; don't edit it.", a.ClientOutput)
	}
	w("- No OpenFGA conditions. Use `check_permission` (or the client's check) for access decisions; listing objects or subjects can be slow and suits batch or admin use.")
	w("")
	w("After editing the model:")
	w("")
	step := 0
	item := func(format string, args ...any) {
		step++
		w("%d. "+format, append([]any{step}, args...)...)
	}
	item("`melange validate`")
	if a.GenerateCode {
		item("`melange generate client` (regenerates `%s`)", a.ClientOutput)
	}
	if a.MigrationStrategy == "versioned" {
		item("`melange generate migration` writes the SQL to `%s`; apply it with the project's migration tool.", a.MigrationOutput)
	} else {
		item("`melange migrate` applies it to the configured database (`melange config show` prints the target). Only run it against a local dev database.")
	}
	w("")
	w("Unexpected allow or deny: run `melange explain <subject> <relation> <object>` (e.g. `user:alice viewer document:1`) before changing the model; it shows each path tried and its tuples. `melange doctor` diagnoses setup problems.")
	w("")
	w("`validate` and `generate` work offline; `migrate`, `explain`, and `doctor` need the database. If the project's usual setup can't reach one, say so instead of improvising one.")
	w("")
	w("Docs, when needed: https://melange.sh/llms.txt")
	return b.String()
}

// writeAgentInstructions appends section to path, creating the file if
// needed. It reports false without writing when any instructions file already
// has the section, so a section pasted into CLAUDE.md is not repeated in a
// later AGENTS.md.
func writeAgentInstructions(path, section string) (bool, error) {
	for _, name := range agentsFiles {
		data, err := os.ReadFile(name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		if strings.Contains(string(data), agentsHeading) {
			return false, nil
		}
	}

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}

	var sep string
	switch {
	case len(existing) == 0:
	case strings.HasSuffix(string(existing), "\n"):
		sep = "\n"
	default:
		sep = "\n\n"
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	if _, err := f.WriteString(sep + section); err != nil {
		_ = f.Close()
		return false, err
	}
	return true, f.Close()
}
