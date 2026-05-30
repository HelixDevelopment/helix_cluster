package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	downVolumesFlag bool
	downForceFlag   bool
)

func init() {
	downCmd.Flags().BoolVar(&downVolumesFlag, "volumes", false, "Also remove volumes")
	downCmd.Flags().BoolVar(&downForceFlag, "force", false, "Force immediate kill")
	rootCmd.AddCommand(downCmd)
}

var downCmd = &cobra.Command{
	Use:   "down [services...]",
	Short: "Stop infrastructure services",
	Long:  `Stops infrastructure services. If no services are specified, all are stopped.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}

		services := args
		if len(services) == 0 {
			services = nil
		}

		ctx := context.Background()
		results, err := orch.Stop(ctx, services, downVolumesFlag)
		if err != nil {
			return err
		}

		for _, r := range results {
			fmt.Printf("Stopping %s... %s\n", r.Name, r.Message)
		}

		fmt.Println("Infrastructure stack is down.")
		return nil
	},
}
