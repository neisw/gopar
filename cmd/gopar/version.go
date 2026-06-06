package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// Version is the semantic version of the CLI tool
	Version = "dev"
	// Commit is the git commit hash
	Commit = "unknown"
	// BuildDate is when the binary was built
	BuildDate = "unknown"
)

// NewVersionCommand creates the version command
func NewVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("gopar version %s\n", Version)
			fmt.Printf("Commit: %s\n", Commit)
			fmt.Printf("Built: %s\n", BuildDate)
		},
	}
}
