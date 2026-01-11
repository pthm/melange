package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

const (
	maxWalkDepth = 25
)

// Config represents the melange configuration from melange.yaml.
type Config struct {
	// Top-level convenience fields
	Schema     string `mapstructure:"schema"`
	SchemasDir string `mapstructure:"schemas_dir"`

	// Database configuration
	Database DatabaseConfig `mapstructure:"database"`

	// Per-command configuration
	Generate GenerateConfig `mapstructure:"generate"`
	Migrate  MigrateConfig  `mapstructure:"migrate"`
	Status   StatusConfig   `mapstructure:"status"`
	Doctor   DoctorConfig   `mapstructure:"doctor"`
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
}

// GenerateConfig holds code generation settings.
type GenerateConfig struct {
	Client ClientConfig `mapstructure:"client"`
}

// ClientConfig holds client code generation settings.
type ClientConfig struct {
	Runtime string `mapstructure:"runtime"`
	Schema  string `mapstructure:"schema"`
	Output  string `mapstructure:"output"`
	Package string `mapstructure:"package"`
	Filter  string `mapstructure:"filter"`
	IDType  string `mapstructure:"id_type"`
}

// MigrateConfig holds migration settings.
type MigrateConfig struct {
	SchemasDir string `mapstructure:"schemas_dir"`
	DryRun     bool   `mapstructure:"dry_run"`
	Force      bool   `mapstructure:"force"`
}

// StatusConfig holds status command settings.
type StatusConfig struct {
	SchemasDir string `mapstructure:"schemas_dir"`
}

// DoctorConfig holds doctor command settings.
type DoctorConfig struct {
	SchemasDir string `mapstructure:"schemas_dir"`
	Verbose    bool   `mapstructure:"verbose"`
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

func setDefaults(v *viper.Viper) {
	// Top-level defaults
	v.SetDefault("schema", "schemas/schema.fga")
	v.SetDefault("schemas_dir", "schemas")

	// Database defaults
	v.SetDefault("database.url", "")
	v.SetDefault("database.host", "")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.name", "")
	v.SetDefault("database.user", "")
	v.SetDefault("database.password", "")
	v.SetDefault("database.sslmode", "prefer")

	// Generate client defaults
	v.SetDefault("generate.client.runtime", "")
	v.SetDefault("generate.client.schema", "")
	v.SetDefault("generate.client.output", "")
	v.SetDefault("generate.client.package", "authz")
	v.SetDefault("generate.client.filter", "")
	v.SetDefault("generate.client.id_type", "string")

	// Migrate defaults
	v.SetDefault("migrate.schemas_dir", "")
	v.SetDefault("migrate.dry_run", false)
	v.SetDefault("migrate.force", false)

	// Status defaults
	v.SetDefault("status.schemas_dir", "")

	// Doctor defaults
	v.SetDefault("doctor.schemas_dir", "")
	v.SetDefault("doctor.verbose", false)
}

// findConfigFile finds the config file to use.
// If explicitPath is provided, it validates the file exists.
// Otherwise, it walks up from cwd looking for melange.yaml or melange.yml,
// stopping at a .git directory or after maxWalkDepth levels.
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
		for _, name := range []string{"melange.yaml", "melange.yml"} {
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

// ResolvedSchemasDir returns the effective schemas_dir for a command,
// with command-specific override taking precedence over top-level.
func (c *Config) ResolvedSchemasDir(commandDir string) string {
	if commandDir != "" {
		return commandDir
	}
	return c.SchemasDir
}

// ResolvedSchema returns the effective schema path for generate client,
// with generate.client.schema taking precedence over top-level schema.
func (c *Config) ResolvedSchema() string {
	if c.Generate.Client.Schema != "" {
		return c.Generate.Client.Schema
	}
	return c.Schema
}
