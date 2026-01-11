package main

import (
	"fmt"
	"io"

	"github.com/pthm/melange/internal/licenses"
	"github.com/spf13/cobra"
)

var licenseCmd = &cobra.Command{
	Use:   "license",
	Short: "Print license and third-party notices",
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()

		printPrimaryLicense(out)
		printThirdPartyEmbedded(out)
		return nil
	},
}

func printPrimaryLicense(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Melange License")
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, licenses.LicenseText())
	_, _ = fmt.Fprintln(out)
}

func printThirdPartyEmbedded(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Third-Party Notices")
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, licenses.ThirdPartyText())
	_, _ = fmt.Fprintln(out)
}
