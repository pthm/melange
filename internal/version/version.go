package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These variables are set via ldflags by GoReleaser
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// init fills Version/Commit/Date from the embedded build info when they were
// not stamped via ldflags. This makes "go install github.com/pthm/melange/...@v"
// builds report their real module version, and — importantly — lets library
// consumers of pkg/migrator (who never run cmd/melange's init) get a real
// CodegenVersion so the phase-1 migration fast-path skip is not permanently
// disabled. ldflags-stamped builds are left untouched (Version != "dev").
func init() {
	if Version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v := melangeModuleVersion(info); v != "" {
		Version = v
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if len(setting.Value) >= 7 {
				Commit = setting.Value[:7]
			} else {
				Commit = setting.Value
			}
		case "vcs.time":
			Date = setting.Value
		}
	}
}

// melangeModuleVersion returns Melange's own module version from the build info,
// or "" if it cannot be resolved. For the melange CLI, Melange is the main
// module; for library consumers of pkg/migrator it is a dependency, so Deps is
// consulted. Using info.Main.Version unconditionally would pick up the consuming
// application's version and make CodegenVersion track the wrong module — a
// Melange dependency upgrade without an app version bump could then wrongly
// trigger the phase-1 migration skip and leave stale generated SQL installed.
func melangeModuleVersion(info *debug.BuildInfo) string {
	const modulePath = "github.com/pthm/melange"

	version := info.Main.Version
	if info.Main.Path != modulePath {
		version = ""
		for _, dep := range info.Deps {
			if dep.Path == modulePath {
				version = dep.Version
				break
			}
		}
	}

	if version == "(devel)" {
		return ""
	}
	return version
}

// Info returns formatted version information
func Info() string {
	return fmt.Sprintf("melange %s (commit: %s, built: %s) %s",
		Version, Commit, Date, runtime.Version())
}

// Short returns just the version string
func Short() string {
	return Version
}
