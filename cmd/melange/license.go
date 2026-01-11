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
	fmt.Fprintln(out, "Melange License")
	fmt.Fprintln(out)
	fmt.Fprintln(out, licenses.LicenseText())
	fmt.Fprintln(out)
}

func printThirdPartyEmbedded(out io.Writer) error {
	fmt.Fprintln(out, "Third-Party Notices")
	fmt.Fprintln(out)
	fmt.Fprintln(out, licenses.ThirdPartyText())
	fmt.Fprintln(out)
	return nil
}
