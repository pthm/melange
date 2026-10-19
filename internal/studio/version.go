package studio

import (
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/pthm/melange/internal/version"
)

// Where the top bar links to.
const (
	docsURL     = "https://melange.sh/docs/"
	repoURL     = "https://github.com/pthm/melange"
	releasesURL = repoURL + "/releases"
	upgradeHint = "brew upgrade melange  or  go install github.com/pthm/melange@latest"
)

// latestRelease is a newer melange release, set once the CLI's update check
// reports one (SetLatestRelease); empty means none known. Package-level for
// the same reason as listSlots: a process serves one studio.
var latestRelease atomic.Pointer[string]

// SetLatestRelease records a newer release ("0.9.7") for the top bar. The
// check runs in the CLI, server-side, so the page itself never calls out.
func SetLatestRelease(v string) { latestRelease.Store(&v) }

func availableUpdate() string {
	if v := latestRelease.Load(); v != nil {
		return *v
	}
	return ""
}

// pseudoVersion matches a Go pseudo-version (an untagged or dirty build):
// v0.9.6-0.20261018190901-38c4ca1c55f9+dirty.
var pseudoVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+-(?:0\.)?\d{14}-([0-9a-f]{12})(?:\+dirty)?$`)

// shortVersion is the running version as the top bar shows it: a release as
// "v0.9.6", a development build as "dev 38c4ca1" (the full string goes in
// the title).
func shortVersion(v string) string {
	if m := pseudoVersion.FindStringSubmatch(v); m != nil {
		return "dev " + m[1][:7]
	}
	if v == "" || v == "dev" || v == "(devel)" {
		return "dev"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

// versionURL links a release to its notes; development builds have none.
func versionURL(v string) string {
	tag := shortVersion(v)
	if strings.HasPrefix(tag, "dev") {
		return ""
	}
	return releasesURL + "/tag/" + tag
}

func runningVersion() string { return version.Version }
