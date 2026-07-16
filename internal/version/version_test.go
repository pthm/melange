package version

import (
	"runtime/debug"
	"testing"
)

func TestMelangeModuleVersion(t *testing.T) {
	const modulePath = "github.com/pthm/melange"

	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "cli main module carries a real version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: modulePath, Version: "v0.8.4"},
			},
			want: "v0.8.4",
		},
		{
			name: "cli built from source (devel) resolves to empty",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: modulePath, Version: "(devel)"},
			},
			want: "",
		},
		{
			// The bug codex flagged: when a tagged application imports
			// pkg/migrator, Main is the app — Melange must come from Deps, not
			// the app's own version.
			name: "library consumer reads melange from deps, not the app version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/acme/app", Version: "v1.2.3"},
				Deps: []*debug.Module{
					{Path: "github.com/some/other", Version: "v9.9.9"},
					{Path: modulePath, Version: "v0.8.4"},
				},
			},
			want: "v0.8.4",
		},
		{
			name: "library consumer with melange absent from deps resolves to empty",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/acme/app", Version: "v1.2.3"},
				Deps: []*debug.Module{{Path: "github.com/some/other", Version: "v9.9.9"}},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := melangeModuleVersion(tt.info); got != tt.want {
				t.Fatalf("melangeModuleVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
