package main

import (
	"context"

	"github.com/spf13/cobra"
)

var (
	scaleWaitFlag bool
)

func init() {
	scaleCmd.Flags().BoolVar(&scaleWaitFlag, "wait", false, "Block until scaled")
	rootCmd.AddCommand(scaleCmd)
}

var scaleCmd = &cobra.Command{
	Use:   "scale <service> <replicas>",
	Short: "Scale a service to N replicas",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runScale(context.Background(), orch, args[0], args[1], scaleWaitFlag, cmd.OutOrStdout())
		return finish(code, err)
	},
}
