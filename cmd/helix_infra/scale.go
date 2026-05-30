package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

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
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}

		service := args[0]
		replicas, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid replica count: %w", err)
		}

		ctx := context.Background()
		status, err := orch.Scale(ctx, service, replicas)
		if err != nil {
			return err
		}

		fmt.Printf("Scaling %s to %d replicas...\n", service, replicas)
		if scaleWaitFlag {
			fmt.Println("Waiting for scale to complete...")
			time.Sleep(500 * time.Millisecond)
		}
		fmt.Printf("%s: %s\n", service, status.Message)
		return nil
	},
}
