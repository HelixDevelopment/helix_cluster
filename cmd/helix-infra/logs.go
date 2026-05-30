package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	logsFollowFlag      bool
	logsTailFlag        string
	logsSinceFlag       string
	logsTimestampsFlag  bool
)

func init() {
	logsCmd.Flags().BoolVarP(&logsFollowFlag, "follow", "f", true, "Follow log output")
	logsCmd.Flags().StringVar(&logsTailFlag, "tail", "100", "Number of lines to show from the end of the logs")
	logsCmd.Flags().StringVar(&logsSinceFlag, "since", "", "Show logs since timestamp")
	logsCmd.Flags().BoolVar(&logsTimestampsFlag, "timestamps", false, "Show timestamps")
	rootCmd.AddCommand(logsCmd)
}

var logsCmd = &cobra.Command{
	Use:   "logs <service>",
	Short: "Stream logs from a service",
	Long:  `Streams logs from the specified infrastructure service.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}

		service := args[0]
		tail, _ := strconv.Atoi(logsTailFlag)

		ctx := context.Background()
		if err := orch.Logs(ctx, service, logsFollowFlag, tail, logsSinceFlag, logsTimestampsFlag); err != nil {
			return err
		}

		fmt.Printf("Streaming logs from %s... (follow=%v, tail=%d, since=%s, timestamps=%v)\n",
			service, logsFollowFlag, tail, logsSinceFlag, logsTimestampsFlag)
		return nil
	},
}
