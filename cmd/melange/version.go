package main

import (
	"fmt"
	"runtime/debug"

	"github.com/pthm/melange/internal/version"
	"github.com/spf13/cobra"
)

func init() {
	// If version wasn't set via ldflags, try to get it from Go module info.
	// This works when installed via "go install github.com/pthm/melange/cmd/melange@version".
	if version.Version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			if info.Main.Version != "" && info.Main.Version != "(devel)" {
				version.Version = info.Main.Version
			}
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					if len(setting.Value) >= 7 {
						version.Commit = setting.Value[:7]
					} else {
						version.Commit = setting.Value
					}
				case "vcs.time":
					version.Date = setting.Value
				}
			}
		}
	}
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Info())
	},
}
