package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/pthm/melange/internal/version"
)

// advisoriesURL lists the repository's security advisories. Unauthenticated
// requests only ever see published advisories, never drafts.
const advisoriesURL = "https://api.github.com/repos/pthm/melange/security-advisories?state=published&per_page=100"

// modulePath is the module whose version the CLI reports and the migrator
// records as the deployed melange_version. Advisories for other modules (the
// Go runtime, the retired cmd/melange path) cannot be matched against either.
const modulePath = "github.com/pthm/melange"

// Advisory is a published security advisory affecting the melange module.
type Advisory struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity string `json:"severity"`
	URL      string `json:"url"`
	// Range is the vulnerable version range in GitHub's syntax, e.g.
	// "< 0.9.5" or ">= 0.4.0, < 0.6.5".
	Range string `json:"range"`
	// Patched is the first fixed version, empty when none is published.
	Patched string `json:"patched,omitempty"`
}

type githubAdvisory struct {
	GHSAID          string `json:"ghsa_id"`
	Summary         string `json:"summary"`
	Severity        string `json:"severity"`
	HTMLURL         string `json:"html_url"`
	Vulnerabilities []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
		VulnerableVersionRange string `json:"vulnerable_version_range"`
		PatchedVersions        string `json:"patched_versions"`
	} `json:"vulnerabilities"`
}

// fetchAdvisories returns the published advisories that affect modulePath.
func fetchAdvisories(ctx context.Context) ([]Advisory, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", advisoriesURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
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

	return parseAdvisories(resp.Body)
}

// parseAdvisories decodes GitHub's advisory list and keeps the vulnerable
// ranges reported for modulePath.
func parseAdvisories(r io.Reader) ([]Advisory, error) {
	var raw []githubAdvisory
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}

	// Non-nil even when empty, so the cache records "checked, none" rather
	// than "never checked".
	out := []Advisory{}
	for _, a := range raw {
		for _, v := range a.Vulnerabilities {
			if v.Package.Name != modulePath || v.VulnerableVersionRange == "" {
				continue
			}
			out = append(out, Advisory{
				ID:       a.GHSAID,
				Summary:  a.Summary,
				Severity: a.Severity,
				URL:      a.HTMLURL,
				Range:    v.VulnerableVersionRange,
				Patched:  v.PatchedVersions,
			})
		}
	}
	return out, nil
}

// Affecting returns the advisories whose vulnerable range contains ver, once
// per advisory ID.
// An empty or development version matches nothing: there is no release to
// compare it with.
func Affecting(advisories []Advisory, ver string) []Advisory {
	if !Comparable(ver) {
		return nil
	}
	ver = withV(ver)
	var out []Advisory
	seen := map[string]bool{}
	for _, a := range advisories {
		// An advisory can list the module more than once; report it once.
		if !seen[a.ID] && inRange(ver, a.Range) {
			seen[a.ID] = true
			out = append(out, a)
		}
	}
	return out
}

// Comparable reports whether ver is a release version advisories can be
// matched against. Development builds are not, so "no advisories match" says
// nothing about them.
func Comparable(ver string) bool {
	return semver.IsValid(withV(ver))
}

// inRange reports whether ver (a "v"-prefixed semver) satisfies every
// comma-separated constraint in rng ("< 1.2.3", ">= 1.0.0, < 1.2.3",
// "= 1.0.0"). Prereleases and Go pseudo-versions order before their release,
// so v0.9.5-rc.1 is inside "< 0.9.5". An unparseable constraint matches
// nothing, so a malformed advisory cannot flag every version.
func inRange(ver, rng string) bool {
	matched := false
	for _, c := range strings.Split(rng, ",") {
		c = strings.TrimSpace(c)
		var op string
		for _, o := range []string{"<=", ">=", "<", ">", "="} {
			if strings.HasPrefix(c, o) {
				op = o
				break
			}
		}
		bound := withV(strings.TrimPrefix(c, op))
		if op == "" || !semver.IsValid(bound) {
			return false
		}
		var ok bool
		switch cmp := semver.Compare(ver, bound); op {
		case "<":
			ok = cmp < 0
		case "<=":
			ok = cmp <= 0
		case ">":
			ok = cmp > 0
		case ">=":
			ok = cmp >= 0
		case "=":
			ok = cmp == 0
		}
		if !ok {
			return false
		}
		matched = true
	}
	return matched
}

// withV trims v and gives it the "v" prefix golang.org/x/mod/semver requires.
func withV(v string) string {
	return "v" + strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// FixedIn describes where the advisory is fixed, for user-facing output.
func (a Advisory) FixedIn() string {
	if a.Patched == "" {
		return "no fixed release yet"
	}
	return "fixed in " + a.Patched
}
