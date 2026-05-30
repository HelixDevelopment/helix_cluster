package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	vmSpawnCountFlag      int
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
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}
		ctx := context.Background()
		nodes, err := orch.VMSpawn(ctx, vmSpawnCountFlag)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			fmt.Printf("Spawned VM node: %s (%s)\n", n.ID, n.IP)
		}
		return nil
	},
}

var vmDestroyCmd = &cobra.Command{
	Use:   "destroy <node-id>",
	Short: "Destroy a VM node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}
		ctx := context.Background()
		if err := orch.VMDestroy(ctx, args[0]); err != nil {
			return err
		}
		fmt.Printf("Destroyed VM node: %s\n", args[0])
		return nil
	},
}

var vmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VM nodes",
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}
		ctx := context.Background()
		nodes, err := orch.VMList(ctx)
		if err != nil {
			return err
		}
		headers := []string{"ID", "NAME", "STATUS", "IP", "CPU", "MEMORY"}
		rows := make([][]string, 0, len(nodes))
		for _, n := range nodes {
			rows = append(rows, []string{n.ID, n.Name, n.Status, n.IP, fmt.Sprintf("%d", n.CPU), fmt.Sprintf("%d", n.Memory)})
		}
		printTable(headers, rows)
		return nil
	},
}

var vmStatusCmd = &cobra.Command{
	Use:   "status <node-id>",
	Short: "Show VM node status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}
		ctx := context.Background()
		node, err := orch.VMStatus(ctx, args[0])
		if err != nil {
			return err
		}
		fmt.Printf("ID:       %s\n", node.ID)
		fmt.Printf("Name:     %s\n", node.Name)
		fmt.Printf("Status:   %s\n", node.Status)
		fmt.Printf("IP:       %s\n", node.IP)
		fmt.Printf("CPU:      %d\n", node.CPU)
		fmt.Printf("Memory:   %d MB\n", node.Memory)
		fmt.Printf("Created:  %s\n", node.Created.Format(time.RFC3339))
		return nil
	},
}

var vmSSHCmd = &cobra.Command{
	Use:   "ssh <node-id>",
	Short: "SSH into a VM node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}
		ctx := context.Background()
		sshCmd, err := orch.VMSSH(ctx, args[0])
		if err != nil {
			return err
		}
		fmt.Printf("SSH command: %s\n", sshCmd)
		return nil
	},
}

var vmSimulateFailureCmd = &cobra.Command{
	Use:   "simulate-failure <node-id>",
	Short: "Simulate node failure",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}
		ctx := context.Background()
		if err := orch.VMSimulateFailure(ctx, args[0]); err != nil {
			return err
		}
		fmt.Printf("Simulated failure on VM node: %s\n", args[0])
		return nil
	},
}

var vmSimulatePartitionCmd = &cobra.Command{
	Use:   "simulate-partition <node-id>",
	Short: "Simulate network partition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		orch, err := loadOrchestrator()
		if err != nil {
			return err
		}
		ctx := context.Background()
		if err := orch.VMSimulatePartition(ctx, args[0], vmPartitionDurationFlag); err != nil {
			return err
		}
		fmt.Printf("Simulated network partition on VM node: %s for %s\n", args[0], vmPartitionDurationFlag)
		return nil
	},
}
