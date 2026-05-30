package main

import (
	"context"
	"fmt"
	"os"
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
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}

		var services []string
		if len(args) > 0 {
			services = args[:1]
		}

		for {
			ctx := context.Background()
			results, err := orch.Status(ctx, services)
			if err != nil {
				return err
			}

			if statusJSONFlag {
				if err := printJSON(results); err != nil {
					return err
				}
			} else {
				headers := []string{"SERVICE", "STATUS", "HEALTHY", "UPTIME", "PORTS"}
				rows := make([][]string, 0, len(results))
				for _, r := range results {
					rows = append(rows, []string{
						r.Name,
						r.Status,
						fmt.Sprintf("%v", r.Healthy),
						r.Uptime.String(),
						fmt.Sprintf("%v", r.Ports),
					})
				}
				printTable(headers, rows)
			}

			if !statusWatchFlag {
				break
			}
			time.Sleep(2 * time.Second)
			fmt.Fprint(os.Stdout, "\033[H\033[2J")
		}
		return nil
	},
}
