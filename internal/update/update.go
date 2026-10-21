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

	// advisoryGrace is how long a finished release check waits for the
	// advisory request. The exit notice waits only 1s in total, so a stalled
	// advisory request must not hold back the update notice.
	advisoryGrace = 500 * time.Millisecond

	// advisoryRetryTTL is how soon a cache saved without advisories is
	// refreshed. Shorter than cacheTTL so advisories are not hidden for a day,
	// long enough that a rate-limited client does not retry on every run.
	advisoryRetryTTL = time.Hour
)

// Info contains update check results
type Info struct {
	LatestVersion   string    `json:"latest_version"`
	CurrentVersion  string    `json:"current_version"`
	CheckedAt       time.Time `json:"checked_at"`
	UpdateAvailable bool      `json:"update_available"`
	// Advisories are the published advisories for the melange module, nil
	// when they could not be fetched. Use Affecting to match a version.
	Advisories []Advisory `json:"advisories"`
}

// githubRelease represents the GitHub API response
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// CheckWithCache checks for updates using cache when available
func CheckWithCache(ctx context.Context) (*Info, error) {
	// Try to load from cache first
	cached, err := loadCache()
	if err == nil {
		// A cache without advisories predates them or failed to fetch them;
		// retry sooner than the full TTL, but not on every run.
		ttl := cacheTTL
		if cached.Advisories == nil {
			ttl = advisoryRetryTTL
		}
		cached.CurrentVersion = version.Version
		cached.UpdateAvailable = compareVersions(cached.CurrentVersion, cached.LatestVersion) < 0
		if time.Since(cached.CheckedAt) < ttl {
			return cached, nil
		}
	}

	// Cache miss or expired, fetch from GitHub
	info, err := check(ctx)
	if err != nil {
		// An early advisory retry that fails still has a usable release check.
		if cached != nil && time.Since(cached.CheckedAt) < cacheTTL {
			return cached, nil
		}
		return nil, err
	}

	// Save to cache (ignore errors)
	_ = saveCache(info)

	return info, nil
}

// check fetches the latest release from GitHub
func check(ctx context.Context) (*Info, error) {
	// Fetched alongside the release so a slow advisory request adds little
	// latency to the update notice; see advisoryGrace.
	advisoriesCh := make(chan []Advisory, 1)
	go func() {
		a, _ := fetchAdvisories(ctx)
		advisoriesCh <- a
	}()

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

	var advisories []Advisory
	select {
	case advisories = <-advisoriesCh:
	case <-time.After(advisoryGrace):
		// Leave Advisories nil: the result is still delivered, and the nil
		// marks the cache for a refresh on the next run.
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := version.Version

	return &Info{
		LatestVersion:   latestVersion,
		CurrentVersion:  currentVersion,
		CheckedAt:       time.Now(),
		UpdateAvailable: compareVersions(currentVersion, latestVersion) < 0,
		Advisories:      advisories,
	}, nil
}

// cacheDir returns the cache directory path
func cacheDir() (string, error) {
	// XDG_CACHE_HOME wins on every OS, not just where os.UserCacheDir
	// honors it, so it can relocate the cache anywhere.
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		var err error
		if cacheHome, err = os.UserCacheDir(); err != nil {
			return "", err
		}
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

// UpgradeHint is the command that upgrades the running binary, chosen by
// where it is installed; a link to the update docs when that is unknown.
func UpgradeHint() string {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return upgradeDocs
	}
	home, _ := os.UserHomeDir()
	return upgradeHintFor(exe, os.Getenv("GOBIN"), os.Getenv("GOPATH"), home)
}

const upgradeDocs = "See https://melange.sh/docs/getting-started/installation/#updating"

// upgradeHintFor classifies a resolved executable path. A GOPATH set only
// through `go env -w` is not seen and falls back to the docs link.
func upgradeHintFor(exe, gobin, gopath, home string) string {
	segs := strings.FieldsFunc(exe, func(r rune) bool { return r == '/' || r == '\\' })
	for i := 0; i+1 < len(segs); i++ {
		if strings.EqualFold(segs[i], "scoop") && strings.EqualFold(segs[i+1], "apps") {
			return "scoop update melange"
		}
	}
	if strings.Contains(exe, "/Caskroom/") || strings.Contains(exe, "/Cellar/") {
		return "brew upgrade pthm/tap/melange"
	}

	bins := []string{gobin}
	if gopath == "" && home != "" {
		gopath = filepath.Join(home, "go")
	}
	for _, p := range filepath.SplitList(gopath) {
		bins = append(bins, filepath.Join(p, "bin"))
	}
	dir := filepath.Dir(exe)
	for _, b := range bins {
		// ponytail: case-insensitive everywhere for Windows; a case-only
		// mismatch on Linux is not a real install layout.
		if b != "" && strings.EqualFold(filepath.Clean(b), dir) {
			return "go install github.com/pthm/melange@latest"
		}
	}
	return upgradeDocs
}
