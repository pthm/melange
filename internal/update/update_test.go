package update

import (
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		// Basic comparisons
		{name: "1.0.0 < 1.0.1", a: "1.0.0", b: "1.0.1", want: -1},
		{name: "1.0.1 > 1.0.0", a: "1.0.1", b: "1.0.0", want: 1},
		{name: "1.0.0 == 1.0.0", a: "1.0.0", b: "1.0.0", want: 0},

		// With v prefix
		{name: "v1.0.0 < 1.0.1", a: "v1.0.0", b: "1.0.1", want: -1},
		{name: "1.0.0 < v1.0.1", a: "1.0.0", b: "v1.0.1", want: -1},
		{name: "v1.0.0 == v1.0.0", a: "v1.0.0", b: "v1.0.0", want: 0},

		// Minor and major version changes
		{name: "1.0.0 < 1.1.0", a: "1.0.0", b: "1.1.0", want: -1},
		{name: "1.0.0 < 2.0.0", a: "1.0.0", b: "2.0.0", want: -1},
		{name: "2.0.0 > 1.9.9", a: "2.0.0", b: "1.9.9", want: 1},

		// dev version handling
		{name: "dev > 1.0.0", a: "dev", b: "1.0.0", want: 1},
		{name: "1.0.0 < dev", a: "1.0.0", b: "dev", want: -1},
		{name: "dev > 999.999.999", a: "dev", b: "999.999.999", want: 1},

		// Pre-release versions (simplified - just check base version)
		{name: "1.0.0-beta < 1.0.1", a: "1.0.0-beta", b: "1.0.1", want: -1},
		{name: "1.0.0-beta == 1.0.0", a: "1.0.0-beta", b: "1.0.0", want: 0},

		// Different digit counts
		{name: "0.4.4 < 0.5.0", a: "0.4.4", b: "0.5.0", want: -1},
		{name: "0.10.0 > 0.9.0", a: "0.10.0", b: "0.9.0", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCacheDir(t *testing.T) {
	dir, err := cacheDir()
	if err != nil {
		t.Fatalf("cacheDir() error: %v", err)
	}
	if dir == "" {
		t.Error("cacheDir() returned empty string")
	}
	// Should end with "melange"
	if !contains(dir, "melange") {
		t.Errorf("cacheDir() = %q, should contain 'melange'", dir)
	}
}

func TestCacheDir_XDGOverride(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	dir, err := cacheDir()
	if err != nil {
		t.Fatalf("cacheDir() error: %v", err)
	}
	if want := filepath.Join(xdg, "melange"); dir != want {
		t.Errorf("cacheDir() = %q, want %q", dir, want)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s[len(s)-len(substr):] == substr || containsPath(s, substr))
}

func containsPath(path, segment string) bool {
	for i := 0; i < len(path)-len(segment); i++ {
		if path[i:i+len(segment)] == segment {
			return true
		}
	}
	return false
}

func TestUpgradeHintFor(t *testing.T) {
	const (
		scoop = "scoop update melange"
		brew  = "brew upgrade pthm/tap/melange"
		goget = "go install github.com/pthm/melange@latest"
	)
	tests := []struct {
		name                     string
		exe, gobin, gopath, home string
		want                     string
	}{
		{name: "scoop", exe: `C:\Users\me\scoop\apps\melange\0.10.0\melange.exe`, want: scoop},
		{name: "scoop global", exe: `C:\ProgramData\Scoop\Apps\melange\current\melange.exe`, want: scoop},
		{name: "brew cask", exe: "/opt/homebrew/Caskroom/melange/0.10.0/melange", want: brew},
		{name: "linuxbrew cellar", exe: "/home/linuxbrew/.linuxbrew/Cellar/melange/0.10.0/bin/melange", want: brew},
		{name: "GOBIN", exe: "/opt/gobin/melange", gobin: "/opt/gobin/", home: "/home/me", want: goget},
		{name: "GOPATH bin", exe: "/work/go/bin/melange", gopath: "/other" + string(filepath.ListSeparator) + "/work/go", home: "/home/me", want: goget},
		{name: "default GOPATH", exe: "/home/me/go/bin/melange", home: "/home/me", want: goget},
		{name: "GOPATH set skips default", exe: "/home/me/go/bin/melange", gopath: "/work/go", home: "/home/me", want: upgradeDocs},
		{name: "deb rpm docker", exe: "/usr/bin/melange", home: "/home/me", want: upgradeDocs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upgradeHintFor(tt.exe, tt.gobin, tt.gopath, tt.home); got != tt.want {
				t.Errorf("upgradeHintFor(%q) = %q, want %q", tt.exe, got, tt.want)
			}
		})
	}
}
