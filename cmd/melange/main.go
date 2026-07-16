// Command melange (deprecated install path) prints a migration notice.
//
// The melange CLI now lives in the root module and installs from there:
//
//	go install github.com/pthm/melange@latest
//
// This shim is the terminal version of the github.com/pthm/melange/cmd/melange
// module. It carries no build dependencies and exists only so that the old
// install path fails loudly with guidance instead of silently installing a
// frozen binary. See specs/module-layout-proposal.md.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, `melange: this install path is deprecated.

The melange CLI now installs from the module root:

    go install github.com/pthm/melange@latest

(or use Homebrew: brew install pthm/tap/melange)`)
	os.Exit(1)
}
