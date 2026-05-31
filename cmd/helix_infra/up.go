package main

import (
	"context"
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
	Long:  `Boots infrastructure services. If no services are specified, all default services are started.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if upTimeoutFlag <= 0 {
			return errInvalidTimeout(upTimeoutFlag)
		}
		orch, _, err := loadOrchestrator(upConfigFlag)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), upTimeoutFlag)
		defer cancel()
		setupSignalHandler(cancel)

		code, err := runUp(ctx, orch, args, upOptions{wait: upWaitFlag, timeout: upTimeoutFlag}, cmd.OutOrStdout(), cmd.ErrOrStderr())
		return finish(code, err)
	},
}
