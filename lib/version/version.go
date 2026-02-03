package version

import (
	"fmt"
	"runtime"
)

// These variables are set via ldflags by GoReleaser
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info returns formatted version information
func Info() string {
	return fmt.Sprintf("melange %s (commit: %s, built: %s) %s",
		Version, Commit, Date, runtime.Version())
}

// Short returns just the version string
func Short() string {
	return Version
}
