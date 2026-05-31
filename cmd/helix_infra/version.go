package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

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

// resolveVersion returns the effective version string. When the build-time
// Version is the "dev" default, it falls back to the contents of a VERSION file
// (trimmed) if one can be read at versionFilePath. The value is returned, never
// mutated into a global, so the function is deterministic and testable.
func resolveVersion(buildVersion, versionFilePath string) string {
	if buildVersion != "dev" {
		return buildVersion
	}
	if versionFilePath == "" {
		return buildVersion
	}
	if data, err := os.ReadFile(versionFilePath); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	return buildVersion
}

// writeVersion renders the version block to w. Pure given its inputs.
func writeVersion(w io.Writer, version, gitCommit, buildDate, goVersion string) {
	fmt.Fprintf(w, "Version:   %s\n", version)
	fmt.Fprintf(w, "GitCommit: %s\n", gitCommit)
	fmt.Fprintf(w, "BuildDate: %s\n", buildDate)
	fmt.Fprintf(w, "GoVersion: %s\n", goVersion)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Long:  `Prints the binary version, git commit, build date, and Go version.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		version := resolveVersion(Version, "VERSION")
		writeVersion(cmd.OutOrStdout(), version, GitCommit, BuildDate, runtime.Version())
		return nil
	},
}
