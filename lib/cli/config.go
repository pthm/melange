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
	// Schema is the path to the OpenFGA schema file (e.g., "schemas/schema.fga")
	Schema string `mapstructure:"schema"`

	// Database configuration
	Database DatabaseConfig `mapstructure:"database"`

	// Per-command configuration
	Generate GenerateConfig `mapstructure:"generate"`
	Migrate  MigrateConfig  `mapstructure:"migrate"`
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

	// Generate client defaults
	v.SetDefault("generate.client.runtime", "")
	v.SetDefault("generate.client.schema", "")
	v.SetDefault("generate.client.output", "")
	v.SetDefault("generate.client.package", "authz")
	v.SetDefault("generate.client.filter", "")
	v.SetDefault("generate.client.id_type", "string")

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

// ResolvedSchema returns the effective schema path,
// with generate.client.schema taking precedence over top-level schema (for generate command).
func (c *Config) ResolvedSchema() string {
	if c.Generate.Client.Schema != "" {
		return c.Generate.Client.Schema
	}
	return c.Schema
}
