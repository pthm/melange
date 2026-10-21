package update

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAffecting(t *testing.T) {
	advisories := []Advisory{
		{ID: "A", Range: "< 0.9.5"},
		{ID: "B", Range: ">= 0.4.0, < 0.6.5"},
		{ID: "C", Range: "= 0.10.0"},
		{ID: "D", Range: "<= 0.8.7"},
		{ID: "F", Range: "> 0.10.0"},
		{ID: "E", Range: "garbage"},           // unparseable: never matches
		{ID: "A", Range: ">= 0.9.0, < 0.9.5"}, // A listed twice: reported once
	}
	tests := []struct {
		version string
		want    string // matching IDs, comma-separated
	}{
		{"v0.6.4", "A,B,D"},
		{"0.6.5", "A,D"},
		{"0.8.7", "A,D"},
		{"0.9.4", "A"},
		{"v0.9.5", ""},
		{"0.10.0", "C"},
		{"0.11.0", "F"},
		{"0.3.0", "A,D"},
		{"", ""},
		{"dev", ""},
		{"(devel)", ""},
		{"v0.9.5-rc.1", "A"}, // prerelease orders before its release
		{"v0.9.5-0.20261001120000-abcdef123456", "A"}, // pseudo-version likewise
	}
	for _, tt := range tests {
		var ids []string
		for _, a := range Affecting(advisories, tt.version) {
			ids = append(ids, a.ID)
		}
		if got := strings.Join(ids, ","); got != tt.want {
			t.Errorf("Affecting(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestParseAdvisories(t *testing.T) {
	const body = `[
		{"ghsa_id": "GHSA-1", "summary": "S1", "severity": "high", "html_url": "https://u/1",
		 "vulnerabilities": [
			{"package": {"name": "github.com/pthm/melange"}, "vulnerable_version_range": "< 0.9.5", "patched_versions": "0.9.5"},
			{"package": {"name": "github.com/pthm/melange/melange"}, "vulnerable_version_range": "< 0.9.5"},
			{"package": {"name": "github.com/pthm/melange"}, "vulnerable_version_range": ">= 0.10.0, < 0.10.2"}
		 ]},
		{"ghsa_id": "GHSA-2", "summary": "S2", "severity": "low", "html_url": "https://u/2",
		 "vulnerabilities": [{"package": {"name": "github.com/pthm/melange"}, "vulnerable_version_range": ""}]}
	]`
	got, err := parseAdvisories(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	// Other modules and empty ranges are dropped; each matching range is kept.
	want := []Advisory{
		{ID: "GHSA-1", Summary: "S1", Severity: "high", URL: "https://u/1", Range: "< 0.9.5", Patched: "0.9.5"},
		{ID: "GHSA-1", Summary: "S1", Severity: "high", URL: "https://u/1", Range: ">= 0.10.0, < 0.10.2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseAdvisories() = %+v, want %+v", got, want)
	}
	if got[0].FixedIn() != "fixed in 0.9.5" || got[1].FixedIn() != "no fixed release yet" {
		t.Errorf("FixedIn() = %q, %q", got[0].FixedIn(), got[1].FixedIn())
	}

	// No advisories is non-nil, so the cache records "checked, none".
	if got, err := parseAdvisories(strings.NewReader("[]")); err != nil || got == nil || len(got) != 0 {
		t.Errorf(`parseAdvisories("[]") = %#v, %v, want empty non-nil`, got, err)
	}
	if _, err := parseAdvisories(strings.NewReader("{")); err == nil {
		t.Error("malformed JSON must return an error")
	}
}

// A cache saved without advisories (fetch failed) is still served within
// advisoryRetryTTL instead of hitting the network on every run.
func TestCheckWithCache_NilAdvisoriesWithinRetryTTL(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := saveCache(&Info{LatestVersion: "0.10.0", CheckedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // any network fetch would fail
	info, err := CheckWithCache(ctx)
	if err != nil || info.LatestVersion != "0.10.0" {
		t.Fatalf("CheckWithCache = %+v, %v; want cached result", info, err)
	}
}
