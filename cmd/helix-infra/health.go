package main

import (
	"context"
	"fmt"
	"os"
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
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}

		services := args
		if len(services) == 0 {
			services = nil
		}

		for {
			ctx := context.Background()
			results, err := orch.Health(ctx, services)
			if err != nil {
				return err
			}

			allHealthy := true
			if healthJSONFlag {
				if err := printJSON(results); err != nil {
					return err
				}
			} else {
				headers := []string{"SERVICE", "HEALTHY", "LATENCY", "MESSAGE"}
				rows := make([][]string, 0, len(results))
				for _, r := range results {
					if !r.Healthy {
						allHealthy = false
					}
					rows = append(rows, []string{
						r.Name,
						fmt.Sprintf("%v", r.Healthy),
						r.Latency.String(),
						r.Message,
					})
				}
				printTable(headers, rows)
			}

			if !healthWatchFlag {
				if !allHealthy {
					os.Exit(1)
				}
				break
			}
			time.Sleep(2 * time.Second)
			fmt.Println()
		}
		return nil
	},
}
