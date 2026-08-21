package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/viper"
)

const (
	maxWalkDepth = 25
)

// Config represents the melange configuration from melange.yaml.
type Config struct {
	// Schema is the path to the OpenFGA schema file (e.g., "schemas/schema.fga")
	Schema string `mapstructure:"schema"`

	// Database configuration
	Database DatabaseConfig `mapstructure:"database"`

	// Per-command configuration
	Generate GenerateConfig `mapstructure:"generate"`
	Migrate  MigrateConfig  `mapstructure:"migrate"`
	Doctor   DoctorConfig   `mapstructure:"doctor"`

	// DefaultEnvironment names the environment applied when neither the --env
	// flag nor MELANGE_ENV selects one. Empty means "use the base config".
	DefaultEnvironment string `mapstructure:"default_environment"`

	// Environments are named connection profiles. Each overlays the base
	// configuration (see ForEnvironment); a field an environment leaves unset
	// falls through to the base. Absent map ⇒ behavior identical to before
	// environments existed.
	Environments map[string]EnvironmentConfig `mapstructure:"environments"`
}

// EnvironmentConfig is a named connection profile overlaid onto the base
// Config by ForEnvironment. Typically only Database is set; Schema is available
// for the uncommon case of an environment that reads a different schema file.
type EnvironmentConfig struct {
	Database DatabaseConfig `mapstructure:"database"`
	Schema   string         `mapstructure:"schema"`
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	URL      string `mapstructure:"url"`
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Name     string `mapstructure:"name"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	SSLMode  string `mapstructure:"sslmode"`
	Schema   string `mapstructure:"schema"`
}

// GenerateConfig holds code generation settings.
type GenerateConfig struct {
	Client    ClientConfig       `mapstructure:"client"`
	Migration MigrationGenConfig `mapstructure:"migration"`
}

// ClientConfig holds client code generation settings.
type ClientConfig struct {
	Runtime string `mapstructure:"runtime"`
	Output  string `mapstructure:"output"`
	Package string `mapstructure:"package"`
	Filter  string `mapstructure:"filter"`
	IDType  string `mapstructure:"id_type"`
}

// MigrationGenConfig holds settings for `melange generate migration`, which
// produces versioned SQL files for use with external migration frameworks.
// This is distinct from MigrateConfig, which controls `melange migrate` (the
// built-in apply-to-database workflow). Configuring both for the same database
// is discouraged; the migrate command warns when generate.migration.output is set.
type MigrationGenConfig struct {
	Output string `mapstructure:"output"`
	Name   string `mapstructure:"name"`
	Format string `mapstructure:"format"`
}

// MigrateConfig holds settings for `melange migrate` (builtin migration).
type MigrateConfig struct {
	DryRun bool `mapstructure:"dry_run"`
	Force  bool `mapstructure:"force"`
}

// DoctorConfig holds doctor command settings.
type DoctorConfig struct {
	Verbose         bool `mapstructure:"verbose"`
	SkipPerformance bool `mapstructure:"skip_performance"`
}

// LoadConfig discovers and loads configuration with proper precedence:
// flags > env > config file > defaults.
//
// Returns the loaded config, the path to the config file (empty if none found),
// and any error encountered.
func LoadConfig(explicitConfigPath string) (*Config, string, error) {
	v := viper.New()

	// 1. Set defaults first (lowest precedence)
	setDefaults(v)

	// 2. Set up environment variable binding
	v.SetEnvPrefix("MELANGE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 3. Find and load config file
	configPath, err := findConfigFile(explicitConfigPath)
	if err != nil {
		return nil, "", err
	}

	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, configPath, fmt.Errorf("reading config file: %w", err)
		}
	}

	// 4. Unmarshal into Config struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, configPath, fmt.Errorf("unmarshaling config: %w", err)
	}

	return &cfg, configPath, nil
}

// envRefPattern matches ${VAR} references (braced form only). We deliberately
// do not honor bare $VAR so that literal '$' characters in passwords or DSNs
// pass through untouched — only the explicit ${...} syntax is expanded.
var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvRefs replaces every ${VAR} in s with the value of the OS environment
// variable VAR. The name of every referenced variable that is not set is
// appended to *missing so callers can fail loud rather than silently substitute
// an empty string. Bare $VAR and literal $ are left as-is.
func expandEnvRefs(s string, missing *[]string) string {
	return envRefPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		val, ok := os.LookupEnv(name)
		if !ok {
			*missing = append(*missing, name)
			return ""
		}
		return val
	})
}

// expandDatabaseRefs returns db with ${VAR} references expanded in every string
// field, so environment profiles can reference secrets like
// `url: ${PROD_DATABASE_URL}` instead of committing credentials. Unset variable
// names are collected into *missing.
func expandDatabaseRefs(db DatabaseConfig, missing *[]string) DatabaseConfig {
	db.URL = expandEnvRefs(db.URL, missing)
	db.Host = expandEnvRefs(db.Host, missing)
	db.Name = expandEnvRefs(db.Name, missing)
	db.User = expandEnvRefs(db.User, missing)
	db.Password = expandEnvRefs(db.Password, missing)
	db.SSLMode = expandEnvRefs(db.SSLMode, missing)
	db.Schema = expandEnvRefs(db.Schema, missing)
	return db
}

// overlayDatabase returns base with the environment's fields overlaid. Any field
// over sets replaces base's; unset fields fall through. When the environment
// specifies any connection identity (URL, host, or name), the base URL is
// dropped first: DSN prefers URL, so a stale base URL would otherwise shadow a
// discrete override. Melange does not decompose a base URL into fields, so an
// environment that overrides a URL-based base with discrete fields must supply a
// complete connection (including user); DSN() reports a clear error otherwise.
func overlayDatabase(base, over DatabaseConfig) DatabaseConfig {
	out := base
	if over.URL != "" || over.Host != "" || over.Name != "" {
		out.URL = ""
	}

	if over.URL != "" {
		out.URL = over.URL
	}
	if over.Host != "" {
		out.Host = over.Host
	}
	if over.Port != 0 {
		out.Port = over.Port
	}
	if over.Name != "" {
		out.Name = over.Name
	}
	if over.User != "" {
		out.User = over.User
	}
	if over.Password != "" {
		out.Password = over.Password
	}
	if over.SSLMode != "" {
		out.SSLMode = over.SSLMode
	}
	if over.Schema != "" {
		out.Schema = over.Schema
	}
	return out
}

// resolveDatabase overlays over onto base and expands ${VAR} references,
// recording unset variable names into *missing. Because a base URL is dropped
// whenever the environment specifies its own connection (see overlayDatabase),
// only references on surviving fields are expanded and required — selecting an
// environment never fails on an unrelated unset base/local secret it replaces.
func resolveDatabase(base, over DatabaseConfig, missing *[]string) DatabaseConfig {
	return expandDatabaseRefs(overlayDatabase(base, over), missing)
}

// HasEnvironment reports whether the named environment profile is defined.
func (c *Config) HasEnvironment(name string) bool {
	_, ok := c.Environments[name]
	return ok
}

// lookupEnvironment returns the named environment profile. An undefined name is
// an error — failing loud prevents `--env prod` from silently running against
// the base (often local) database after a typo.
func (c *Config) lookupEnvironment(name string) (EnvironmentConfig, error) {
	env, ok := c.Environments[name]
	if !ok {
		return EnvironmentConfig{}, fmt.Errorf("environment %q is not defined in configuration", name)
	}
	return env, nil
}

// ForEnvironment returns a copy of the config with the named environment
// overlaid and ${VAR} references expanded. An empty envName returns the base
// config (still with ${VAR} expansion applied, so the base database block may
// reference secrets too).
//
// A non-empty envName that is not defined is an error (returned with a nil
// config). An unset ${VAR} reference is also an error — an unset secret must
// fail loud rather than silently resolve and retarget the command — but there
// the returned config is still populated best-effort (unset references expand to
// empty), so callers may use its non-connection fields (e.g. Schema) and honor
// an explicit --db while any DSN attempt fails loud.
func (c *Config) ForEnvironment(envName string) (*Config, error) {
	resolved := *c

	over := DatabaseConfig{}
	schemaSource := c.Schema

	if envName != "" {
		env, err := c.lookupEnvironment(envName)
		if err != nil {
			return nil, err
		}
		over = env.Database
		if env.Schema != "" {
			schemaSource = env.Schema
		}
	}

	// Track database refs separately: only an unresolved *database* reference
	// blocks a connection. An unresolved schema reference is not a DSN failure —
	// it surfaces when the schema is loaded, and can be overridden by --schema.
	var dbMissing, schemaMissing []string
	resolved.Database = resolveDatabase(c.Database, over, &dbMissing)
	resolved.Schema = expandEnvRefs(schemaSource, &schemaMissing)

	if len(dbMissing) > 0 {
		// Return the best-effort config (unset refs expanded to empty) alongside
		// the error so callers can still use non-connection fields (e.g. Schema)
		// and honor an explicit --db, while DSN attempts fail loud.
		return &resolved, fmt.Errorf("unset environment variable(s) referenced in database configuration: %s",
			strings.Join(dedupeStrings(dbMissing), ", "))
	}

	return &resolved, nil
}

// dedupeStrings returns names with duplicates removed, preserving first-seen order.
func dedupeStrings(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := names[:0:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// EnvironmentSummary returns a redacted, human-readable connection target for
// the named environment (or the base config when name == ""). Unlike
// ForEnvironment it does not expand ${VAR} references — a listing should show
// what is configured, not whatever happens to be in the current shell — and it
// redacts any URL password. Returns an error only when name is a non-empty
// environment that is not defined.
func (c *Config) EnvironmentSummary(name string) (string, error) {
	db := c.Database
	if name != "" {
		env, err := c.lookupEnvironment(name)
		if err != nil {
			return "", err
		}
		db = overlayDatabase(c.Database, env.Database)
	}

	switch {
	case db.URL != "":
		return redactURL(db.URL), nil
	case db.Host != "" || db.Name != "":
		host := db.Host
		if host == "" {
			host = "localhost"
		}
		port := db.Port
		if port == 0 {
			port = 5432
		}
		target := fmt.Sprintf("%s:%d", host, port)
		if db.Name != "" {
			target += "/" + db.Name
		}
		return target, nil
	default:
		return "(no connection configured)", nil
	}
}

// Redacted returns a copy of the config safe to print: every database password
// (discrete field and URL userinfo) across the base block and all environment
// profiles is masked. Used by `config show` so resolved ${VAR} secrets are not
// echoed in cleartext.
func (c *Config) Redacted() *Config {
	out := *c
	out.Database = redactDatabase(c.Database)
	if c.Environments != nil {
		envs := make(map[string]EnvironmentConfig, len(c.Environments))
		for name, env := range c.Environments {
			env.Database = redactDatabase(env.Database)
			envs[name] = env
		}
		out.Environments = envs
	}
	return &out
}

// redactDatabase masks the password in both the URL and the discrete Password
// field of db.
func redactDatabase(db DatabaseConfig) DatabaseConfig {
	if db.URL != "" {
		db.URL = redactURL(db.URL)
	}
	if db.Password != "" {
		db.Password = "****"
	}
	return db
}

// urlPasswordParam matches a password (or pgpassword) query parameter so its
// value can be masked. PostgreSQL URLs may carry credentials in the query string
// (e.g. ?password=secret) as well as in the userinfo.
var urlPasswordParam = regexp.MustCompile(`(?i)([?&](?:password|pgpassword)=)[^&]*`)

// redactURL replaces any URL password with **** for safe display, covering both
// the userinfo (user:pass@host) and password query parameters. A string that
// does not parse as a URL (e.g. an unexpanded ${VAR} reference) or carries no
// password is returned unchanged — the reference itself is not a secret.
func redactURL(raw string) string {
	if _, err := url.Parse(raw); err != nil {
		return raw
	}
	out := redactUserinfoPassword(raw)
	return urlPasswordParam.ReplaceAllString(out, "${1}****")
}

// redactUserinfoPassword masks the password in a URL's userinfo segment.
//
// It operates on the raw bytes rather than the value returned by
// url.User.Password(): that accessor returns a *decoded* password, which does
// not match the raw bytes when the password is percent-encoded (e.g. p%40ss),
// so a naive replace would fail to redact and leak the credential.
func redactUserinfoPassword(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return raw
	}
	afterScheme := raw[schemeEnd+3:]

	// Bound the userinfo to the authority (up to the path/query/fragment), then
	// split at the LAST '@' within it — matching url.Parse, which treats the
	// final '@' as the userinfo delimiter so an unescaped '@' in the password
	// does not leak.
	authority := afterScheme
	if end := strings.IndexAny(afterScheme, "/?#"); end >= 0 {
		authority = afterScheme[:end]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return raw
	}
	userinfo := authority[:at]

	colon := strings.Index(userinfo, ":")
	if colon < 0 || colon == len(userinfo)-1 {
		// No password, or an empty one (user:@host) — nothing to redact, and
		// masking an empty password would misrepresent the config.
		return raw
	}
	return raw[:schemeEnd+3] + userinfo[:colon] + ":****" + afterScheme[at:]
}

// setDefaults registers the out-of-box values for every config key. These
// are the lowest-precedence values: env vars, the config file, and any
// explicit flag values all override them. Keeping defaults here rather than
// in the Config struct ensures viper's precedence chain works correctly.
func setDefaults(v *viper.Viper) {
	// Top-level defaults
	v.SetDefault("schema", "schemas/schema.fga")

	// Database defaults
	v.SetDefault("database.url", "")
	v.SetDefault("database.host", "")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.name", "")
	v.SetDefault("database.user", "")
	v.SetDefault("database.password", "")
	v.SetDefault("database.sslmode", "prefer")
	v.SetDefault("database.schema", "")

	// Generate client defaults
	v.SetDefault("generate.client.runtime", "")
	v.SetDefault("generate.client.output", "")
	v.SetDefault("generate.client.package", "authz")
	v.SetDefault("generate.client.filter", "")
	v.SetDefault("generate.client.id_type", "string")

	// Generate migration defaults
	v.SetDefault("generate.migration.output", "")
	v.SetDefault("generate.migration.name", "melange")
	v.SetDefault("generate.migration.format", "split")

	// Migrate defaults
	v.SetDefault("migrate.dry_run", false)
	v.SetDefault("migrate.force", false)

	// Doctor defaults
	v.SetDefault("doctor.verbose", false)
	v.SetDefault("doctor.skip_performance", false)
}

// findConfigFile locates the config file to load. When explicitPath is given,
// it is validated and returned directly. Otherwise the function walks upward
// from the current directory, checking six candidate names at each level:
// melange.yaml, melange.yml, melange/config.yaml, melange/config.yml,
// melange/melange.yaml, and melange/melange.yml. The walk stops at a .git
// boundary or after maxWalkDepth levels. This search order must stay in sync
// with findExistingConfig in cmd/melange/init.go so that configs written by
// init are always discovered by other commands.
func findConfigFile(explicitPath string) (string, error) {
	if explicitPath != "" {
		if _, err := os.Stat(explicitPath); err != nil {
			return "", fmt.Errorf("config file not found: %s", explicitPath)
		}
		return explicitPath, nil
	}

	// Auto-discovery: walk up to .git or maxWalkDepth
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting cwd: %w", err)
	}

	dir := cwd
	for i := 0; i < maxWalkDepth; i++ {
		// Try melange.yaml then melange.yml
		for _, name := range []string{
			"melange.yaml",
			"melange.yml",
			filepath.Join("melange", "config.yaml"),
			filepath.Join("melange", "config.yml"),
			filepath.Join("melange", "melange.yaml"),
			filepath.Join("melange", "melange.yml"),
		} {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}

		// Check for repo boundary (.git file or directory)
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			break // Stop at repo root
		}

		// Move up
		parent := filepath.Dir(dir)
		if parent == dir {
			break // Reached filesystem root
		}
		dir = parent
	}

	return "", nil // No config found, use defaults
}

// DSN returns the database connection string.
// If database.url is set, it's returned directly.
// Otherwise, builds a DSN from discrete fields.
func (c *Config) DSN() (string, error) {
	db := c.Database

	if db.URL != "" {
		return db.URL, nil
	}

	// Build DSN from discrete fields
	if db.Host == "" {
		return "", fmt.Errorf("database.host is required when database.url is not set")
	}
	if db.Name == "" {
		return "", fmt.Errorf("database.name is required when database.url is not set")
	}
	if db.User == "" {
		return "", fmt.Errorf("database.user is required when database.url is not set")
	}

	// Build postgres:// URL
	u := &url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", db.Host, db.Port),
		Path:   "/" + db.Name,
	}

	if db.Password != "" {
		u.User = url.UserPassword(db.User, db.Password)
	} else {
		u.User = url.User(db.User)
	}

	if db.SSLMode != "" {
		q := u.Query()
		q.Set("sslmode", db.SSLMode)
		u.RawQuery = q.Encode()
	}

	return u.String(), nil
}
