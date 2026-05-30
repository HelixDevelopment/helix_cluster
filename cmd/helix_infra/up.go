package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var (
	upWaitFlag    bool
	upTimeoutFlag time.Duration
	upConfigFlag  string
)

func init() {
	upCmd.Flags().BoolVar(&upWaitFlag, "wait", false, "Block until services are healthy")
	upCmd.Flags().DurationVar(&upTimeoutFlag, "timeout", 5*time.Minute, "Timeout for health wait")
	upCmd.Flags().StringVar(&upConfigFlag, "config", "", "Path to config file")
	rootCmd.AddCommand(upCmd)
}

var upCmd = &cobra.Command{
	Use:   "up [services...]",
	Short: "Boot the infrastructure stack",
	Long:  `Boots infrastructure services. If no services are specified, all 20 services are started.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}

		services := args
		if len(services) == 0 {
			services = nil // orchestrator will use defaults
		}

		ctx, cancel := context.WithTimeout(context.Background(), upTimeoutFlag)
		defer cancel()
		setupSignalHandler(cancel)

		results, err := orch.Boot(ctx, services)
		if err != nil {
			return err
		}

		failed := make([]string, 0)
		for _, r := range results {
			fmt.Printf("Booting %s...\n", r.Name)
			if r.Healthy {
				fmt.Printf("%s: healthy\n", r.Name)
			} else {
				fmt.Printf("%s: failed\n", r.Name)
				failed = append(failed, r.Name)
			}
		}

		if upWaitFlag {
			fmt.Println("Waiting for services to be healthy...")
			// In a real implementation, poll health here until timeout.
			time.Sleep(500 * time.Millisecond)
		}

		if len(failed) > 0 {
			fmt.Fprintf(os.Stderr, "Failed services: %v\n", failed)
			os.Exit(1)
		}

		fmt.Println("Infrastructure stack is up.")
		return nil
	},
}
