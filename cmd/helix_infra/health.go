package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	healthJSONFlag  bool
	healthWatchFlag bool
)

func init() {
	healthCmd.Flags().BoolVar(&healthJSONFlag, "json", false, "Output in JSON format")
	healthCmd.Flags().BoolVar(&healthWatchFlag, "watch", false, "Periodic recheck")
	rootCmd.AddCommand(healthCmd)
}

var healthCmd = &cobra.Command{
	Use:   "health [services...]",
	Short: "Run health checks against services",
	Long:  `Runs health checks against the specified services, or all if none are specified.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		for {
			code, err := runHealth(context.Background(), orch, args, healthJSONFlag, out)
			if err != nil {
				return finish(code, err)
			}
			if !healthWatchFlag {
				return finish(code, nil)
			}
			time.Sleep(2 * time.Second)
			fmt.Fprintln(out)
		}
	},
}
