package main

import "github.com/spf13/cobra"

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate code from schema",
	Long:  `Generate type-safe client code from an authorization schema.`,
}

func init() {
	generateCmd.AddCommand(generateClientCmd)
}
