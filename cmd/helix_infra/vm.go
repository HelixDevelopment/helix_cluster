package main

import (
	"context"
	"time"

	"github.com/spf13/cobra"
)

var (
	vmSpawnCountFlag        int
	vmPartitionDurationFlag time.Duration
)

func init() {
	vmSpawnCmd.Flags().IntVar(&vmSpawnCountFlag, "count", 1, "Number of VM nodes to spawn")
	vmSimulatePartitionCmd.Flags().DurationVar(&vmPartitionDurationFlag, "duration", 30*time.Second, "Partition duration")

	vmCmd.AddCommand(vmSpawnCmd)
	vmCmd.AddCommand(vmDestroyCmd)
	vmCmd.AddCommand(vmListCmd)
	vmCmd.AddCommand(vmStatusCmd)
	vmCmd.AddCommand(vmSSHCmd)
	vmCmd.AddCommand(vmSimulateFailureCmd)
	vmCmd.AddCommand(vmSimulatePartitionCmd)
	rootCmd.AddCommand(vmCmd)
}

var vmCmd = &cobra.Command{
	Use:   "vm <subcommand>",
	Short: "Manage VM nodes",
	Long:  `Commands for managing QEMU VM-based node simulation.`,
}

var vmSpawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Spawn N VM nodes",
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runVMSpawn(context.Background(), orch, vmSpawnCountFlag, cmd.OutOrStdout())
		return finish(code, err)
	},
}

var vmDestroyCmd = &cobra.Command{
	Use:   "destroy <node-id>",
	Short: "Destroy a VM node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runVMDestroy(context.Background(), orch, args[0], cmd.OutOrStdout())
		return finish(code, err)
	},
}

var vmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VM nodes",
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runVMList(context.Background(), orch, cmd.OutOrStdout())
		return finish(code, err)
	},
}

var vmStatusCmd = &cobra.Command{
	Use:   "status <node-id>",
	Short: "Show VM node status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runVMStatus(context.Background(), orch, args[0], cmd.OutOrStdout())
		return finish(code, err)
	},
}

var vmSSHCmd = &cobra.Command{
	Use:   "ssh <node-id>",
	Short: "SSH into a VM node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runVMSSH(context.Background(), orch, args[0], cmd.OutOrStdout())
		return finish(code, err)
	},
}

var vmSimulateFailureCmd = &cobra.Command{
	Use:   "simulate-failure <node-id>",
	Short: "Simulate node failure",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runVMSimulateFailure(context.Background(), orch, args[0], cmd.OutOrStdout())
		return finish(code, err)
	},
}

var vmSimulatePartitionCmd = &cobra.Command{
	Use:   "simulate-partition <node-id>",
	Short: "Simulate network partition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, _, err := loadOrchestrator("")
		if err != nil {
			return err
		}
		code, err := runVMSimulatePartition(context.Background(), orch, args[0], vmPartitionDurationFlag, cmd.OutOrStdout())
		return finish(code, err)
	},
}
