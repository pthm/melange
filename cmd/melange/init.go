package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/pthm/melange/pkg/clientgen"
)

var (
	initYes       bool
	initNoInstall bool
	initSchema    string
	initDB        string
	initTemplate  string
	initRuntime   string
	initOutput    string
	initPackage   string
	initIDType    string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new Melange project",
	Long: `Initialize a new Melange project with a configuration file and starter schema.

Creates a melange/ directory with config.yaml and an OpenFGA schema file.
Detects your project type (Go/TypeScript) and offers to set up client code
generation with the appropriate runtime dependency.

Use --yes to accept all defaults without prompting.`,
	Example: `  # Interactive wizard (detects project type automatically)
  melange init

  # Accept all defaults
  melange init -y

  # Custom schema path and database URL
  melange init -y --schema melange/auth.fga --db postgres://prod:5432/app

  # Skip dependency installation
  melange init -y --no-install`,
	RunE: runInit,
}

func init() {
	f := initCmd.Flags()
	f.BoolVarP(&initYes, "yes", "y", false, "accept all defaults without prompting")
	f.BoolVar(&initNoInstall, "no-install", false, "skip installing runtime dependencies")
	f.StringVar(&initSchema, "schema", "", "schema file path (default: melange/schema.fga)")
	f.StringVar(&initDB, "db", "", "database URL (default: postgres://localhost:5432/mydb)")
	f.StringVar(&initTemplate, "template", "", "starter model: org-rbac, doc-sharing, minimal, none")
	f.StringVar(&initRuntime, "runtime", "", "client runtime: "+strings.Join(clientgen.ListRuntimes(), ", "))
	f.StringVar(&initOutput, "output", "", "client output directory")
	f.StringVar(&initPackage, "package", "", "client package name (default: authz)")
	f.StringVar(&initIDType, "id-type", "", "client ID type: string, int64, uuid.UUID")
}

// initAnswers is the resolved configuration that flows through the entire init
// lifecycle: populated by defaultAnswers, overridden by CLI flags, refined by
// runWizard (when interactive), then written to disk by writeConfig and writeSchema.
type initAnswers struct {
	SchemaPath    string
	Template      string
	DatabaseURL   string
	GenerateCode  bool
	ClientRuntime string
	ClientOutput  string
	ClientPackage string
	ClientIDType  string
}

// detectedProject captures what kind of project exists in the current directory.
// When both go.mod and package.json are present, Go takes precedence so that
// monorepos with embedded frontends get Go-appropriate defaults.
type detectedProject struct {
	runtime string // "go", "typescript", or "" when unrecognized
	goMod   bool
	pkgJSON bool
}

// detectProject infers the project runtime from the current working directory
// by looking for go.mod (Go) and package.json (TypeScript/JavaScript).
// The result feeds defaultAnswers and controls which client output paths and
// runtime dependency are pre-selected.
func detectProject() detectedProject {
	d := detectedProject{}
	if _, err := os.Stat("go.mod"); err == nil {
		d.goMod = true
		d.runtime = "go"
	}
	if _, err := os.Stat("package.json"); err == nil {
		d.pkgJSON = true
		// TypeScript wins if no go.mod, otherwise Go takes precedence
		if !d.goMod {
			d.runtime = "typescript"
		}
	}
	return d
}

// defaultAnswers builds the baseline initAnswers for the detected project type.
// If a project is recognized, GenerateCode is enabled and client settings are
// pre-populated with idiomatic paths for that runtime. The wizard and flag
// overrides layer on top of these values, never below them.
func defaultAnswers(proj detectedProject) initAnswers {
	a := initAnswers{
		SchemaPath:    "melange/schema.fga",
		Template:      "org-rbac",
		DatabaseURL:   "postgres://localhost:5432/mydb",
		GenerateCode:  proj.runtime != "", // default to true if project detected
		ClientRuntime: "go",
		ClientOutput:  "internal/authz",
		ClientPackage: "authz",
		ClientIDType:  "string",
	}
	if proj.runtime != "" {
		a.ClientRuntime = proj.runtime
	}
	if proj.runtime == "typescript" {
		a.ClientOutput = "src/authz"
	}
	return a
}

func runInit(_ *cobra.Command, _ []string) error {
	// Detect project type
	proj := detectProject()

	// Check for existing config
	existingConfig := findExistingConfig()
	if existingConfig != "" {
		if initYes {
			return fmt.Errorf("config file already exists: %s (use interactive mode to overwrite)", existingConfig)
		}
		var overwrite bool
		err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("Config file already exists: %s\nOverwrite?", existingConfig)).
				Value(&overwrite),
		)).WithTheme(melangeTheme()).Run()
		if err != nil {
			return err
		}
		if !overwrite {
			return fmt.Errorf("aborted")
		}
	}

	answers := defaultAnswers(proj)

	// Apply flag overrides
	if initSchema != "" {
		answers.SchemaPath = initSchema
	}
	if initDB != "" {
		answers.DatabaseURL = initDB
	}
	if initTemplate != "" {
		answers.Template = initTemplate
	}
	if initRuntime != "" {
		answers.GenerateCode = true
		answers.ClientRuntime = initRuntime
	}
	if initOutput != "" {
		answers.ClientOutput = initOutput
	}
	if initPackage != "" {
		answers.ClientPackage = initPackage
	}
	if initIDType != "" {
		answers.ClientIDType = initIDType
	}

	if !initYes {
		if err := runWizard(&answers, proj); err != nil {
			return err
		}
	}

	// Check for existing schema file
	if answers.Template != "none" {
		if _, err := os.Stat(answers.SchemaPath); err == nil {
			if initYes {
				// In non-interactive mode, skip schema creation
				answers.Template = "none"
			} else {
				var overwriteSchema bool
				err := huh.NewForm(huh.NewGroup(
					huh.NewConfirm().
						Title(fmt.Sprintf("Schema file already exists: %s\nOverwrite?", answers.SchemaPath)).
						Value(&overwriteSchema),
				)).WithTheme(melangeTheme()).Run()
				if err != nil {
					return err
				}
				if !overwriteSchema {
					answers.Template = "none"
				}
			}
		}
	}

	// Write files
	var created []string

	// Write config
	configPath := configPathForSchema(answers.SchemaPath)
	if err := writeConfig(configPath, &answers); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	created = append(created, configPath)

	// Write schema
	if answers.Template != "none" {
		if err := writeSchema(answers.SchemaPath, answers.Template); err != nil {
			return fmt.Errorf("writing schema: %w", err)
		}
		created = append(created, answers.SchemaPath)
	}

	// Print summary
	fmt.Println()
	fmt.Println("Created:")
	for _, f := range created {
		fmt.Printf("  %s\n", f)
	}

	// Install runtime dependencies
	if answers.GenerateCode && !initNoInstall {
		fmt.Println()
		if err := installDeps(answers.ClientRuntime, proj); err != nil {
			fmt.Printf("Warning: failed to install dependencies: %v\n", err)
			fmt.Println("You can install manually:")
			printManualInstall(answers.ClientRuntime)
		}
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  melange validate    # Check your schema")
	fmt.Println("  melange migrate     # Apply schema to database")

	return nil
}

// Theme colors used across the init wizard and forms.
var (
	colorBurntOrange = lipgloss.AdaptiveColor{Light: "#C45A20", Dark: "#E8813A"}
	colorSand        = lipgloss.AdaptiveColor{Light: "#A68A64", Dark: "#D4B896"}
	colorWarmGray    = lipgloss.AdaptiveColor{Light: "#6B5E50", Dark: "#9C8E80"}
	colorCream       = lipgloss.AdaptiveColor{Light: "#8C7A66", Dark: "#F5E6D0"}
)

// melangeTheme returns the consistent huh form theme used across all interactive
// prompts in the init command. The warm palette (burnt orange as the accent,
// sand/cream for secondary text) is applied to both focused and blurred states
// so all forms feel cohesive regardless of where focus sits.
func melangeTheme() *huh.Theme {
	t := huh.ThemeBase()

	// Focused field styles
	t.Focused.Title = t.Focused.Title.Foreground(colorBurntOrange)
	t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(colorBurntOrange)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(colorBurntOrange)
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(colorBurntOrange)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(colorBurntOrange)
	t.Focused.SelectedPrefix = t.Focused.SelectedPrefix.Foreground(colorBurntOrange)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Background(colorBurntOrange)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(colorBurntOrange)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(colorSand)
	t.Focused.Description = t.Focused.Description.Foreground(colorWarmGray)
	t.Focused.Option = t.Focused.Option.Foreground(colorCream)
	t.Focused.Next = t.Focused.Next.Foreground(colorSand)
	t.Focused.Card = t.Focused.Card.BorderForeground(colorSand)

	// Blurred field styles
	t.Blurred.Title = t.Blurred.Title.Foreground(colorWarmGray)
	t.Blurred.TextInput.Prompt = t.Blurred.TextInput.Prompt.Foreground(colorWarmGray)
	t.Blurred.TextInput.Text = t.Blurred.TextInput.Text.Foreground(colorSand)
	t.Blurred.SelectSelector = t.Blurred.SelectSelector.Foreground(colorWarmGray)
	t.Blurred.SelectedOption = t.Blurred.SelectedOption.Foreground(colorSand)

	return t
}

const melangeBanner = `       ▜
▛▚▀▖▞▀▖▐ ▝▀▖▛▀▖▞▀▌▞▀▖
▌▐ ▌▛▀ ▐ ▞▀▌▌ ▌▚▄▌▛▀
▘▝ ▘▝▀▘ ▘▝▀▘▘ ▘▗▄▘▝▀▘`

// runWizard presents the interactive setup experience, mutating a in place.
// The wizard runs in two stages: a required first form covering schema path,
// starter model, database URL, and whether to generate client code; then an
// optional second form (shown only when GenerateCode is true) that collects
// runtime-specific client generation settings. The detected project is used
// only for the banner display; defaults have already been applied by the caller.
func runWizard(a *initAnswers, proj detectedProject) error {
	bannerStyle := lipgloss.NewStyle().
		Foreground(colorBurntOrange).
		Bold(true)
	detectedStyle := lipgloss.NewStyle().
		Foreground(colorSand)
	runtimeStyle := lipgloss.NewStyle().
		Foreground(colorBurntOrange).
		Bold(true)

	content := bannerStyle.Render(melangeBanner)
	if proj.runtime != "" {
		content += "\n" + detectedStyle.Render("Detected ") + runtimeStyle.Render(proj.runtime) + detectedStyle.Render(" project")
	}

	fmt.Println(lipgloss.NewStyle().PaddingLeft(1).PaddingTop(1).Render(content))
	fmt.Println()

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Schema path").
				Value(&a.SchemaPath),
			huh.NewSelect[string]().
				Title("Starter model").
				Options(
					huh.NewOption("Organization RBAC", "org-rbac"),
					huh.NewOption("Document sharing", "doc-sharing"),
					huh.NewOption("Minimal", "minimal"),
					huh.NewOption("None", "none"),
				).
				Value(&a.Template),
			huh.NewInput().
				Title("Database URL").
				Value(&a.DatabaseURL),
			huh.NewConfirm().
				Title("Generate client code?").
				Value(&a.GenerateCode),
		),
	).WithTheme(melangeTheme()).Run()
	if err != nil {
		return err
	}

	// Client code generation prompts
	if a.GenerateCode {
		runtimes := clientgen.ListRuntimes()
		runtimeOptions := make([]huh.Option[string], len(runtimes))
		for i, r := range runtimes {
			runtimeOptions[i] = huh.NewOption(r, r)
		}

		err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Runtime").
					Options(runtimeOptions...).
					Value(&a.ClientRuntime),
			),
		).WithTheme(melangeTheme()).Run()
		if err != nil {
			return err
		}

		// Adjust default output based on runtime
		if a.ClientOutput == "internal/authz" && a.ClientRuntime == "typescript" {
			a.ClientOutput = "src/authz"
		}

		fields := []huh.Field{
			huh.NewInput().
				Title("Output directory").
				Value(&a.ClientOutput),
		}

		if a.ClientRuntime == "go" {
			fields = append(fields,
				huh.NewInput().
					Title("Package name").
					Value(&a.ClientPackage),
			)
		}

		fields = append(fields,
			huh.NewSelect[string]().
				Title("ID type").
				Options(
					huh.NewOption("string", "string"),
					huh.NewOption("int64", "int64"),
					huh.NewOption("uuid.UUID", "uuid.UUID"),
				).
				Value(&a.ClientIDType),
		)

		err = huh.NewForm(
			huh.NewGroup(fields...),
		).WithTheme(melangeTheme()).Run()
		if err != nil {
			return err
		}
	}

	return nil
}

// configPathForSchema determines where to write the config file based on the
// chosen schema location. When the schema lives under melange/, the config is
// co-located there as melange/config.yaml; otherwise it is written to
// melange.yaml at the project root. This mirrors the discovery order in
// lib/cli.findConfigFile so the generated config is always found automatically.
func configPathForSchema(schemaPath string) string {
	dir := filepath.Dir(schemaPath)
	if dir == "melange" || strings.HasPrefix(dir, "melange/") || strings.HasPrefix(dir, "melange\\") {
		return filepath.Join("melange", "config.yaml")
	}
	return "melange.yaml"
}

// initConfig is a write-only struct used solely by writeConfig to produce clean
// YAML. It intentionally omits the full set of cli.Config fields so that the
// generated config only contains values the user actually specified. The pointer
// to initGenConfig ensures the generate block is absent rather than empty when
// code generation was not requested.
type initConfig struct {
	Schema   string         `json:"schema"`
	Database initDBConfig   `json:"database"`
	Generate *initGenConfig `json:"generate,omitempty"`
}

// initDBConfig holds the minimal database section written during init.
type initDBConfig struct {
	URL string `json:"url"`
}

// initGenConfig is the generate section of the written config.
// Package is omitted for non-Go runtimes (see initClientConfig).
type initGenConfig struct {
	Client initClientConfig `json:"client"`
}

// initClientConfig holds client code generation settings in the written config.
// Package uses omitempty because it is not meaningful for non-Go runtimes.
type initClientConfig struct {
	Runtime string `json:"runtime"`
	Output  string `json:"output"`
	Package string `json:"package,omitempty"`
	IDType  string `json:"id_type"`
}

// writeConfig serializes initAnswers to YAML and writes the config file at
// configPath, creating parent directories as needed. The generate block is
// omitted entirely when GenerateCode is false.
func writeConfig(configPath string, a *initAnswers) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	c := initConfig{
		Schema:   a.SchemaPath,
		Database: initDBConfig{URL: a.DatabaseURL},
	}

	if a.GenerateCode {
		gen := &initGenConfig{
			Client: initClientConfig{
				Runtime: a.ClientRuntime,
				Output:  a.ClientOutput,
				IDType:  a.ClientIDType,
			},
		}
		if a.ClientRuntime == "go" {
			gen.Client.Package = a.ClientPackage
		}
		c.Generate = gen
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0o644)
}

// writeSchema writes the named starter template to schemaPath, creating
// parent directories as needed. Valid template names are the keys of
// schemaTemplates. Use template "none" to skip schema creation entirely
// (handled by the caller before invoking this function).
func writeSchema(schemaPath, template string) error {
	content := schemaTemplates[template]
	if content == "" {
		return fmt.Errorf("unknown template: %s", template)
	}

	if err := os.MkdirAll(filepath.Dir(schemaPath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(schemaPath, []byte(content), 0o644)
}

// findExistingConfig looks for a config file in the current directory using
// the same candidate list as lib/cli.findConfigFile. It returns the first path
// found, or an empty string if no config exists. This allows runInit to warn
// the user before overwriting an existing configuration.
func findExistingConfig() string {
	for _, name := range []string{
		"melange.yaml",
		"melange.yml",
		filepath.Join("melange", "config.yaml"),
		filepath.Join("melange", "config.yml"),
		filepath.Join("melange", "melange.yaml"),
		filepath.Join("melange", "melange.yml"),
	} {
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return ""
}

// installDeps installs the appropriate melange client package into the project.
// The proj argument is used to verify that a module manifest exists before
// invoking the package manager; if none is found, instructions are printed and
// the function returns without error so the broader init still succeeds.
func installDeps(runtime string, proj detectedProject) error {
	switch runtime {
	case "go":
		if !proj.goMod {
			fmt.Println("No go.mod found, skipping dependency installation.")
			printManualInstall(runtime)
			return nil
		}
		fmt.Println("Installing github.com/pthm/melange/melange ...")
		cmd := exec.Command("go", "get", "github.com/pthm/melange/melange")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "typescript":
		if !proj.pkgJSON {
			fmt.Println("No package.json found, skipping dependency installation.")
			printManualInstall(runtime)
			return nil
		}
		// Detect package manager
		installer, args := detectPkgManager()
		fmt.Printf("Installing @pthm/melange using %s ...\n", installer)
		cmd := exec.Command(installer, append(args, "@pthm/melange")...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	default:
		return nil
	}
}

// detectPkgManager infers the active Node package manager from lock files in
// the current directory. Lock file presence is a reliable signal because each
// tool writes a distinct file. Priority order: bun > pnpm > yarn > npm.
func detectPkgManager() (cmd string, args []string) {
	if _, err := os.Stat("bun.lockb"); err == nil {
		return "bun", []string{"add"}
	}
	if _, err := os.Stat("bun.lock"); err == nil {
		return "bun", []string{"add"}
	}
	if _, err := os.Stat("pnpm-lock.yaml"); err == nil {
		return "pnpm", []string{"add"}
	}
	if _, err := os.Stat("yarn.lock"); err == nil {
		return "yarn", []string{"add"}
	}
	return "npm", []string{"install"}
}

// printManualInstall prints the package-manager command the user can run
// manually when automatic installation is skipped or fails.
func printManualInstall(runtime string) {
	switch runtime {
	case "go":
		fmt.Println("  go get github.com/pthm/melange/melange")
	case "typescript":
		fmt.Println("  npm install @pthm/melange")
	}
}

// schemaTemplates contains the built-in OpenFGA starter models keyed by
// template name. Each template is a complete, valid FGA schema that can be
// compiled and migrated immediately. "org-rbac" is the default because it
// demonstrates tuple-to-userset relations, which are the most common pattern
// in real applications.
var schemaTemplates = map[string]string{
	"org-rbac": `model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin

type repository
  relations
    define org: [organization]
    define owner: [user]
    define admin: [user] or owner
    define can_read: member from org or admin
    define can_write: admin
    define can_delete: owner
`,
	"doc-sharing": `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
`,
	"minimal": `model
  schema 1.1

type user
`,
}
