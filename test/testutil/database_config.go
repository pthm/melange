package testutil

import (
	"fmt"
	"os"
	"strconv"
)

// DatabaseConfig holds configuration for connecting to a database.
type DatabaseConfig struct {
	URL            string
	MaxConnections int
	EnablePooling  bool
}

// GetDatabaseConfig reads database configuration from environment variables.
// If DATABASE_URL is set, it returns configuration for a remote database.
// Otherwise, returns an empty config which signals to use testcontainers.
func GetDatabaseConfig() DatabaseConfig {
	// Check for direct DATABASE_URL (highest priority)
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return DatabaseConfig{
			URL:            url,
			MaxConnections: getEnvInt("DATABASE_MAX_CONNS", 50),
			EnablePooling:  getEnvBool("DATABASE_POOLING", true),
		}
	}

	// Check for individual components
	host := os.Getenv("DATABASE_HOST")
	if host != "" {
		return DatabaseConfig{
			URL: buildDatabaseURL(
				getEnv("DATABASE_USER", "postgres"),
				getEnv("DATABASE_PASSWORD", ""),
				host,
				getEnv("DATABASE_PORT", "5432"),
				getEnv("DATABASE_NAME", "postgres"),
				getEnv("DATABASE_SSLMODE", "prefer"),
			),
			MaxConnections: getEnvInt("DATABASE_MAX_CONNS", 50),
			EnablePooling:  getEnvBool("DATABASE_POOLING", true),
		}
	}

	// Default: use testcontainers (empty config)
	return DatabaseConfig{}
}

// buildDatabaseURL constructs a PostgreSQL connection string.
func buildDatabaseURL(user, password, host, port, dbname, sslmode string) string {
	if password != "" {
		return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
			user, password, host, port, dbname, sslmode)
	}
	return fmt.Sprintf("postgres://%s@%s:%s/%s?sslmode=%s",
		user, host, port, dbname, sslmode)
}

// getEnv gets an environment variable with a fallback default value.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// getEnvInt gets an integer environment variable with a fallback default value.
func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

// getEnvBool gets a boolean environment variable with a fallback default value.
// Accepts: "1", "true", "yes" as true; anything else is false.
func getEnvBool(key string, fallback bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val == "1" || val == "true" || val == "yes"
}
