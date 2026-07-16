package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pthm/melange/internal/version"
)

const (
	githubAPIURL = "https://api.github.com/repos/pthm/melange/releases/latest"
	cacheTTL     = 24 * time.Hour
	cacheFile    = "update-check.json"
)

// Info contains update check results
type Info struct {
	LatestVersion   string    `json:"latest_version"`
	CurrentVersion  string    `json:"current_version"`
	CheckedAt       time.Time `json:"checked_at"`
	UpdateAvailable bool      `json:"update_available"`
}

// githubRelease represents the GitHub API response
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// CheckWithCache checks for updates using cache when available
func CheckWithCache(ctx context.Context) (*Info, error) {
	// Try to load from cache first
	info, err := loadCache()
	if err == nil && time.Since(info.CheckedAt) < cacheTTL {
		// Cache is valid, update current version for comparison
		info.CurrentVersion = version.Version
		info.UpdateAvailable = compareVersions(info.CurrentVersion, info.LatestVersion) < 0
		return info, nil
	}

	// Cache miss or expired, fetch from GitHub
	info, err = check(ctx)
	if err != nil {
		return nil, err
	}

	// Save to cache (ignore errors)
	_ = saveCache(info)

	return info, nil
}

// check fetches the latest release from GitHub
func check(ctx context.Context) (*Info, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubAPIURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "melange/"+version.Version)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := version.Version

	return &Info{
		LatestVersion:   latestVersion,
		CurrentVersion:  currentVersion,
		CheckedAt:       time.Now(),
		UpdateAvailable: compareVersions(currentVersion, latestVersion) < 0,
	}, nil
}

// cacheDir returns the cache directory path
func cacheDir() (string, error) {
	// Use XDG_CACHE_HOME if set, otherwise ~/.cache
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cacheHome = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheHome, "melange"), nil
}

// loadCache loads the cached update info
func loadCache() (*Info, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(dir, cacheFile))
	if err != nil {
		return nil, err
	}

	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	return &info, nil
}

// saveCache saves the update info to cache
func saveCache(info *Info) error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, cacheFile), data, 0o644)
}

// compareVersions compares two semver strings
// Returns -1 if a < b, 0 if a == b, 1 if a > b
func compareVersions(a, b string) int {
	// Strip v prefix
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")

	// Handle dev version
	if a == "dev" {
		return 1 // dev is always "latest"
	}
	if b == "dev" {
		return -1
	}

	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	// Compare each part
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			// Handle pre-release suffixes like "1.0.0-beta"
			partA := strings.Split(partsA[i], "-")[0]
			numA, _ = strconv.Atoi(partA)
		}
		if i < len(partsB) {
			partB := strings.Split(partsB[i], "-")[0]
			numB, _ = strconv.Atoi(partB)
		}

		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}

	return 0
}
