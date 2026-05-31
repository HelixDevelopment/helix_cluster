package main

import (
	"context"

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
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runDown(context.Background(), orch, args, downVolumesFlag, cmd.OutOrStdout())
		return finish(code, err)
	},
}
