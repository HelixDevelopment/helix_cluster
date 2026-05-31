package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	statusJSONFlag  bool
	statusWatchFlag bool
)

func init() {
	statusCmd.Flags().BoolVar(&statusJSONFlag, "json", false, "Output in JSON format")
	statusCmd.Flags().BoolVar(&statusWatchFlag, "watch", false, "Continuously refresh status")
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status [service]",
	Short: "Show status of infrastructure services",
	Long:  `Shows status of all services or a specific service.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}

		var services []string
		if len(args) > 0 {
			services = args[:1]
		}

		out := cmd.OutOrStdout()
		for {
			code, err := runStatus(context.Background(), orch, services, statusJSONFlag, out)
			if err != nil {
				return finish(code, err)
			}
			if !statusWatchFlag {
				return finish(code, nil)
			}
			time.Sleep(2 * time.Second)
			fmt.Fprint(out, "\033[H\033[2J")
		}
	},
}
