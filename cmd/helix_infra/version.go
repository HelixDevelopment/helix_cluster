package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

// Build-time variables, set via ldflags.
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Long:  `Prints the binary version, git commit, build date, and Go version.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Try to read from VERSION file at root if variables are not set.
		if Version == "dev" {
			if data, err := os.ReadFile("VERSION"); err == nil {
				Version = string(data)
			}
		}
		fmt.Printf("Version:   %s\n", Version)
		fmt.Printf("GitCommit: %s\n", GitCommit)
		fmt.Printf("BuildDate: %s\n", BuildDate)
		fmt.Printf("GoVersion: %s\n", runtime.Version())
		return nil
	},
}
