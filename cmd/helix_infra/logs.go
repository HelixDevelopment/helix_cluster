package main

import (
	"context"

	"github.com/spf13/cobra"
)

var (
	logsFollowFlag     bool
	logsTailFlag       string
	logsSinceFlag      string
	logsTimestampsFlag bool
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
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runLogs(context.Background(), orch, args[0], logsFollowFlag, logsTailFlag, logsSinceFlag, logsTimestampsFlag, cmd.OutOrStdout())
		return finish(code, err)
	},
}
