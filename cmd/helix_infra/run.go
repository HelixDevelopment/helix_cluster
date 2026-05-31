package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/infra"
)

// errInvalidTimeout reports a non-positive timeout flag.
func errInvalidTimeout(d time.Duration) error {
	return fmt.Errorf("--timeout must be positive, got %s", d)
}

// upOptions configures the "up" action.
type upOptions struct {
	wait    bool
	timeout time.Duration
}

// runUp boots the named services (all defaults when services is empty) and
// reports per-service progress to stdout. It returns a non-zero exit code if
// any service failed to come up healthy, instead of calling os.Exit directly,
// so the outcome is assertable in tests.
func runUp(ctx context.Context, orch infra.Orchestrator, services []string, opts upOptions, stdout, stderr io.Writer) (int, error) {
	results, err := orch.Boot(ctx, services)
	if err != nil {
		return 1, err
	}

	failed := make([]string, 0)
	for _, r := range results {
		fmt.Fprintf(stdout, "Booting %s...\n", r.Name)
		if r.Healthy {
			fmt.Fprintf(stdout, "%s: healthy\n", r.Name)
		} else {
			fmt.Fprintf(stdout, "%s: failed\n", r.Name)
			failed = append(failed, r.Name)
		}
	}

	if opts.wait {
		fmt.Fprintln(stdout, "Waiting for services to be healthy...")
	}

	if len(failed) > 0 {
		fmt.Fprintf(stderr, "Failed services: %v\n", failed)
		return 1, nil
	}

	fmt.Fprintln(stdout, "Infrastructure stack is up.")
	return 0, nil
}

// runDown stops the named services (all when empty) and reports progress.
func runDown(ctx context.Context, orch infra.Orchestrator, services []string, removeVolumes bool, stdout io.Writer) (int, error) {
	results, err := orch.Stop(ctx, services, removeVolumes)
	if err != nil {
		return 1, err
	}
	for _, r := range results {
		fmt.Fprintf(stdout, "Stopping %s... %s\n", r.Name, r.Message)
	}
	fmt.Fprintln(stdout, "Infrastructure stack is down.")
	return 0, nil
}

// statusRows builds the table rows for a status listing. Pure: no I/O.
func statusRows(results []infra.ServiceStatus) [][]string {
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		rows = append(rows, []string{
			r.Name,
			r.Status,
			fmt.Sprintf("%v", r.Healthy),
			r.Uptime.String(),
			fmt.Sprintf("%v", r.Ports),
		})
	}
	return rows
}

var statusHeaders = []string{"SERVICE", "STATUS", "HEALTHY", "UPTIME", "PORTS"}

// runStatus prints the status of the given services once. Watch-mode looping is
// handled by the cobra wrapper; the single-shot core is what we test.
func runStatus(ctx context.Context, orch infra.Orchestrator, services []string, asJSON bool, stdout io.Writer) (int, error) {
	results, err := orch.Status(ctx, services)
	if err != nil {
		return 1, err
	}
	if asJSON {
		if err := printJSON(stdout, results); err != nil {
			return 1, err
		}
		return 0, nil
	}
	printTable(stdout, statusHeaders, statusRows(results))
	return 0, nil
}

var healthHeaders = []string{"SERVICE", "HEALTHY", "LATENCY", "MESSAGE"}

// healthRows builds the table rows and reports whether all are healthy. Pure.
func healthRows(results []infra.ServiceStatus) ([][]string, bool) {
	allHealthy := true
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		if !r.Healthy {
			allHealthy = false
		}
		rows = append(rows, []string{
			r.Name,
			fmt.Sprintf("%v", r.Healthy),
			r.Latency.String(),
			r.Message,
		})
	}
	return rows, allHealthy
}

// runHealth runs health checks once and returns exit code 1 if any service is
// unhealthy (the contract the original os.Exit(1) encoded), without exiting.
func runHealth(ctx context.Context, orch infra.Orchestrator, services []string, asJSON bool, stdout io.Writer) (int, error) {
	results, err := orch.Health(ctx, services)
	if err != nil {
		return 1, err
	}
	if asJSON {
		if err := printJSON(stdout, results); err != nil {
			return 1, err
		}
	}
	rows, allHealthy := healthRows(results)
	if !asJSON {
		printTable(stdout, healthHeaders, rows)
	}
	if !allHealthy {
		return 1, nil
	}
	return 0, nil
}

// parseTail validates and parses the --tail flag. A non-numeric value is an
// error rather than being silently coerced to zero.
func parseTail(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --tail value %q: must be an integer", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid --tail value %d: must be >= 0", n)
	}
	return n, nil
}

// runLogs validates inputs and streams logs from a service.
func runLogs(ctx context.Context, orch infra.Orchestrator, service string, follow bool, tailStr, since string, timestamps bool, stdout io.Writer) (int, error) {
	tail, err := parseTail(tailStr)
	if err != nil {
		return 1, err
	}
	if err := orch.Logs(ctx, service, follow, tail, since, timestamps); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Streaming logs from %s... (follow=%v, tail=%d, since=%s, timestamps=%v)\n",
		service, follow, tail, since, timestamps)
	return 0, nil
}

// parseReplicas validates and parses a replica count argument.
func parseReplicas(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid replica count %q: must be an integer", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid replica count %d: must be >= 0", n)
	}
	return n, nil
}

// runScale validates the replica count and scales the service.
func runScale(ctx context.Context, orch infra.Orchestrator, service, replicasStr string, wait bool, stdout io.Writer) (int, error) {
	replicas, err := parseReplicas(replicasStr)
	if err != nil {
		return 1, err
	}
	status, err := orch.Scale(ctx, service, replicas)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Scaling %s to %d replicas...\n", service, replicas)
	if wait {
		fmt.Fprintln(stdout, "Waiting for scale to complete...")
	}
	fmt.Fprintf(stdout, "%s: %s\n", service, status.Message)
	return 0, nil
}

// runVMSpawn validates the count and spawns VM nodes.
func runVMSpawn(ctx context.Context, orch infra.Orchestrator, count int, stdout io.Writer) (int, error) {
	if count < 1 {
		return 1, fmt.Errorf("--count must be >= 1, got %d", count)
	}
	nodes, err := orch.VMSpawn(ctx, count)
	if err != nil {
		return 1, err
	}
	for _, n := range nodes {
		fmt.Fprintf(stdout, "Spawned VM node: %s (%s)\n", n.ID, n.IP)
	}
	return 0, nil
}

var vmListHeaders = []string{"ID", "NAME", "STATUS", "IP", "CPU", "MEMORY"}

// vmListRows builds the table rows for vm list. Pure.
func vmListRows(nodes []infra.VMNode) [][]string {
	rows := make([][]string, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, []string{n.ID, n.Name, n.Status, n.IP, strconv.Itoa(n.CPU), strconv.FormatInt(n.Memory, 10)})
	}
	return rows
}

// runVMList lists all VM nodes as a table.
func runVMList(ctx context.Context, orch infra.Orchestrator, stdout io.Writer) (int, error) {
	nodes, err := orch.VMList(ctx)
	if err != nil {
		return 1, err
	}
	printTable(stdout, vmListHeaders, vmListRows(nodes))
	return 0, nil
}

// runVMStatus prints details of a single VM node.
func runVMStatus(ctx context.Context, orch infra.Orchestrator, nodeID string, stdout io.Writer) (int, error) {
	node, err := orch.VMStatus(ctx, nodeID)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "ID:       %s\n", node.ID)
	fmt.Fprintf(stdout, "Name:     %s\n", node.Name)
	fmt.Fprintf(stdout, "Status:   %s\n", node.Status)
	fmt.Fprintf(stdout, "IP:       %s\n", node.IP)
	fmt.Fprintf(stdout, "CPU:      %d\n", node.CPU)
	fmt.Fprintf(stdout, "Memory:   %d MB\n", node.Memory)
	fmt.Fprintf(stdout, "Created:  %s\n", node.Created.Format(time.RFC3339))
	return 0, nil
}

// runVMDestroy destroys a VM node by ID.
func runVMDestroy(ctx context.Context, orch infra.Orchestrator, nodeID string, stdout io.Writer) (int, error) {
	if err := orch.VMDestroy(ctx, nodeID); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Destroyed VM node: %s\n", nodeID)
	return 0, nil
}

// runVMSSH prints the SSH connection string for a VM node.
func runVMSSH(ctx context.Context, orch infra.Orchestrator, nodeID string, stdout io.Writer) (int, error) {
	sshCmd, err := orch.VMSSH(ctx, nodeID)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "SSH command: %s\n", sshCmd)
	return 0, nil
}

// runVMSimulateFailure simulates a node failure.
func runVMSimulateFailure(ctx context.Context, orch infra.Orchestrator, nodeID string, stdout io.Writer) (int, error) {
	if err := orch.VMSimulateFailure(ctx, nodeID); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Simulated failure on VM node: %s\n", nodeID)
	return 0, nil
}

// runVMSimulatePartition simulates a network partition for a duration.
func runVMSimulatePartition(ctx context.Context, orch infra.Orchestrator, nodeID string, duration time.Duration, stdout io.Writer) (int, error) {
	if duration <= 0 {
		return 1, fmt.Errorf("--duration must be positive, got %s", duration)
	}
	if err := orch.VMSimulatePartition(ctx, nodeID, duration); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "Simulated network partition on VM node: %s for %s\n", nodeID, duration)
	return 0, nil
}
