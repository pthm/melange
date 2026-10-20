package update

import (
	"context"
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
