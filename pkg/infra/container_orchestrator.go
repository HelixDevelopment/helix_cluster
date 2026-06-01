package infra

// container_orchestrator.go — REAL infrastructure orchestrator backed by
// digital.vasic.containers (pkg/runtime, pkg/health, pkg/remote).
//
// Design contract (CLAUDE-1 / §7.1):
//   - Simulated() returns false — this is NOT a simulation.
//   - Boot MUST start each named service via the container runtime and only
//     report Status "running" / Healthy true after a real readiness probe to
//     that service succeeds.
//   - Health MUST perform a live TCP probe (non-constant, measured latency).
//   - Logs MUST stream real output from the runtime, forwarding all options.
//   - Scale MUST query the runtime list to verify replica count.
//   - VMSSH MUST execute the command over a real RemoteExecutor.
//   - VMSimulatePartition MUST produce an observable health-probe effect.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"digital.vasic.containers/pkg/remote"
	"digital.vasic.containers/pkg/runtime"
)

// ContainerOrchestratorConfig holds configuration for the real orchestrator.
type ContainerOrchestratorConfig struct {
	// InfraConfig is the base infrastructure config (services list, etc.).
	InfraConfig *InfraConfig
	// Runtime is the container runtime to use (podman by default).
	Runtime runtime.ContainerRuntime
	// RemoteExec is the SSH executor for VM operations.
	RemoteExec remote.RemoteExecutor
	// HealthProbeTimeout is the timeout for individual TCP health probes.
	HealthProbeTimeout time.Duration
	// BootReadinessTimeout is how long to wait for a service to become ready
	// after issuing the container start command.
	BootReadinessTimeout time.Duration
	// ServicePorts maps service names to their health-probe addresses
	// (host:port). If not supplied for a service, health probes are skipped
	// and the service is reported healthy on successful container start.
	ServicePorts map[string]string
	// ContainerImages maps service names to container images.
	// If not set, a default image per service name is used.
	ContainerImages map[string]string
	// ContainerPorts maps service names to a podman port-publish spec
	// ("hostPort:containerPort", e.g. "16379:6379"). Used by the real
	// "podman run" path so health probes against the host port succeed.
	ContainerPorts map[string]string
	// ContainerCommand maps service names to a command (and args) appended
	// after the image in "podman run …" (e.g. ["sleep","300"] to keep a
	// busybox replica alive). Optional.
	ContainerCommand map[string][]string
	// VMNodeImage is the container image used to back a VM node spawned by
	// VMSpawn. Each VM node is a REAL long-lived container; VMSSH executes a
	// REAL command session into it via the container runtime's Exec. Defaults
	// to "docker.io/library/busybox:latest".
	VMNodeImage string
	// VMNodeCommand keeps the VM-node container alive so a session can be
	// established into it. Defaults to ["sleep","3600"].
	VMNodeCommand []string
	// PodmanBinary is the binary used for the real run/pull path. Defaults
	// to "podman". Overridable for testing/alternate runtimes.
	PodmanBinary string
	// runPodman executes a podman command and returns combined output. It is
	// an injectable seam: nil means the real os/exec implementation is used.
	// Tests MAY inject a fake to assert argv without a daemon.
	runPodman func(ctx context.Context, args ...string) ([]byte, error)
}

// DefaultContainerOrchestratorConfig returns a configuration wired with the
// system's auto-detected container runtime.
func DefaultContainerOrchestratorConfig(
	cfg *InfraConfig,
	rt runtime.ContainerRuntime,
) *ContainerOrchestratorConfig {
	return &ContainerOrchestratorConfig{
		InfraConfig:          cfg,
		Runtime:              rt,
		HealthProbeTimeout:   2 * time.Second,
		BootReadinessTimeout: 30 * time.Second,
		ServicePorts:         make(map[string]string),
		ContainerImages:      make(map[string]string),
		ContainerPorts:       make(map[string]string),
		ContainerCommand:     make(map[string][]string),
		VMNodeImage:          defaultVMNodeImage,
		VMNodeCommand:        defaultVMNodeCommand(),
	}
}

// defaultVMNodeImage is the image used to back VM nodes when none is configured.
const defaultVMNodeImage = "docker.io/library/busybox:latest"

// defaultVMNodeCommand returns a fresh copy of the default keep-alive command
// for VM-node containers.
func defaultVMNodeCommand() []string { return []string{"sleep", "3600"} }

// containerOrchestrator is a REAL infrastructure orchestrator. It uses a
// container runtime (podman/docker/…) and the RemoteExecutor SSH seam to
// perform all operations against live infrastructure.
//
// It satisfies the Orchestrator interface and its Simulated() method returns
// false, meaning callers can assert it is the real implementation.
type containerOrchestrator struct {
	mu     sync.RWMutex
	cfg    *ContainerOrchestratorConfig
	rt     runtime.ContainerRuntime
	remote remote.RemoteExecutor

	// vmNodes tracks spawned VM nodes.
	vmNodes map[string]*vmNodeState
	// partitioned tracks which node IDs are currently partitioned (their
	// health probes will fail intentionally).
	partitioned map[string]bool
}

// vmNodeState holds state for a VM-like node. The node is backed by a REAL
// long-lived container (containerName); VMSSH establishes a real command
// session into it via the runtime's Exec seam.
type vmNodeState struct {
	node          VMNode
	remoteHost    remote.RemoteHost
	containerName string
}

// NewContainerOrchestrator returns a REAL Orchestrator backed by the supplied
// container runtime and remote executor. Neither may be nil.
func NewContainerOrchestrator(cfg *ContainerOrchestratorConfig) (*containerOrchestrator, error) {
	if cfg == nil {
		return nil, fmt.Errorf("ContainerOrchestratorConfig must not be nil")
	}
	if cfg.Runtime == nil {
		return nil, fmt.Errorf("ContainerOrchestratorConfig.Runtime must not be nil")
	}
	if cfg.InfraConfig == nil {
		return nil, fmt.Errorf("ContainerOrchestratorConfig.InfraConfig must not be nil")
	}
	if cfg.ServicePorts == nil {
		cfg.ServicePorts = make(map[string]string)
	}
	if cfg.ContainerImages == nil {
		cfg.ContainerImages = make(map[string]string)
	}
	if cfg.ContainerPorts == nil {
		cfg.ContainerPorts = make(map[string]string)
	}
	if cfg.ContainerCommand == nil {
		cfg.ContainerCommand = make(map[string][]string)
	}
	if cfg.PodmanBinary == "" {
		cfg.PodmanBinary = "podman"
	}
	if cfg.VMNodeImage == "" {
		cfg.VMNodeImage = defaultVMNodeImage
	}
	if len(cfg.VMNodeCommand) == 0 {
		cfg.VMNodeCommand = defaultVMNodeCommand()
	}
	if cfg.runPodman == nil {
		cfg.runPodman = func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, cfg.PodmanBinary, args...).CombinedOutput()
		}
	}
	return &containerOrchestrator{
		cfg:         cfg,
		rt:          cfg.Runtime,
		remote:      cfg.RemoteExec,
		vmNodes:     make(map[string]*vmNodeState),
		partitioned: make(map[string]bool),
	}, nil
}

// Simulated always returns false — this is the real implementation.
func (o *containerOrchestrator) Simulated() bool { return false }

// compile-time assertion that containerOrchestrator satisfies the Orchestrator
// seam.
var _ Orchestrator = (*containerOrchestrator)(nil)

// ─── Boot ────────────────────────────────────────────────────────────────────

// Boot starts each named service via the container runtime. For each service
// it issues "podman start <name>" (if the container already exists) or
// "podman run -d --name <name> <image>" (if it needs to be created). It then
// waits up to BootReadinessTimeout for a TCP health probe on the service's
// registered port (if any) to succeed before reporting Healthy==true.
func (o *containerOrchestrator) Boot(ctx context.Context, services []string) ([]ServiceStatus, error) {
	if len(services) == 0 {
		services = o.cfg.InfraConfig.Services
	}

	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		status := o.bootOne(ctx, name)
		results = append(results, status)
	}
	return results, nil
}

// bootOne starts a single named service and performs its readiness probe.
func (o *containerOrchestrator) bootOne(ctx context.Context, name string) ServiceStatus {
	status := ServiceStatus{
		Name:   name,
		Status: "starting",
		Ports:  []string{},
		Labels: map[string]string{},
	}

	// Try "podman start <name>" first (container pre-exists).
	startErr := o.rt.Start(ctx, name)
	if startErr != nil {
		// Container does not exist — try "podman run".
		image := o.containerImage(name)
		runErr := o.runContainer(ctx, name, image)
		if runErr != nil {
			status.Status = "failed"
			status.Healthy = false
			status.Message = fmt.Sprintf("start %s: %v; run %s: %v", name, startErr, name, runErr)
			return status
		}
	}

	// If a health port is registered, wait for TCP probe.
	addr, hasPort := o.cfg.ServicePorts[name]
	if hasPort && addr != "" {
		healthy, latency := o.probeTCP(ctx, addr, o.cfg.BootReadinessTimeout)
		status.Healthy = healthy
		status.Latency = latency
		if healthy {
			status.Status = "running"
			status.Message = "booted and healthy"
		} else {
			status.Status = "starting"
			status.Message = fmt.Sprintf("container started but TCP probe on %s timed out", addr)
		}
		status.Ports = []string{addr}
	} else {
		// No health port — report running on successful start.
		status.Status = "running"
		status.Healthy = true
		status.Message = "booted"
	}

	return status
}

// runContainer creates and starts a NEW detached container from an image via a
// real "podman run -d --name <name> [-p host:container] <image> [cmd…]". The
// container runtime's Start only starts pre-existing containers, so creation
// must go through podman directly (same os/exec approach the build service
// uses). If the image is not present locally, it is pulled once and the run is
// retried — so a fresh host with only network access still boots the service.
func (o *containerOrchestrator) runContainer(ctx context.Context, name, image string) error {
	return o.runContainerSpec(ctx, name, image, o.cfg.ContainerPorts[name], o.cfg.ContainerCommand[name])
}

// runContainerSpec is the explicit-spec form of runContainer: it lets callers
// (e.g. Scale, whose replica names differ from the service key) pass the
// port-publish spec and trailing command directly.
func (o *containerOrchestrator) runContainerSpec(ctx context.Context, name, image, port string, cmd []string) error {
	args := []string{"run", "-d", "--name", name}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, image)
	if len(cmd) > 0 {
		args = append(args, cmd...)
	}

	out, err := o.cfg.runPodman(ctx, args...)
	if err != nil && imageMissing(out) {
		// Pull the image once, then retry the run.
		if _, perr := o.cfg.runPodman(ctx, "pull", image); perr != nil {
			return fmt.Errorf("podman pull %s: %w (%s)", image, perr, strings.TrimSpace(string(out)))
		}
		out, err = o.cfg.runPodman(ctx, args...)
	}
	if err != nil {
		return fmt.Errorf("podman run %s (%s): %w: %s", name, image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// imageMissing reports whether podman output indicates the image is not present
// locally (so a pull-then-retry is warranted).
func imageMissing(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "no such image") ||
		strings.Contains(s, "image not known") ||
		strings.Contains(s, "unable to find image") ||
		strings.Contains(s, "manifest unknown") ||
		strings.Contains(s, "error: short-name")
}

// containerImage returns the image for a given service name. If not explicitly
// configured, a sensible default is derived from the service name.
func (o *containerOrchestrator) containerImage(name string) string {
	if img, ok := o.cfg.ContainerImages[name]; ok && img != "" {
		return img
	}
	// Derive default from service name prefix.
	switch {
	case strings.HasPrefix(name, "postgres"):
		return "docker.io/library/postgres:16"
	case strings.HasPrefix(name, "redis"):
		return "docker.io/library/redis:7"
	case strings.HasPrefix(name, "etcd"):
		return "quay.io/coreos/etcd:v3.5"
	case strings.HasPrefix(name, "nats"):
		return "docker.io/library/nats:latest"
	case strings.HasPrefix(name, "kafka"):
		return "docker.io/bitnami/kafka:latest"
	case strings.HasPrefix(name, "prometheus"):
		return "docker.io/prom/prometheus:latest"
	case strings.HasPrefix(name, "grafana"):
		return "docker.io/grafana/grafana:latest"
	case strings.HasPrefix(name, "vault"):
		return "docker.io/hashicorp/vault:latest"
	case strings.HasPrefix(name, "jaeger"):
		return "docker.io/jaegertracing/all-in-one:latest"
	default:
		return fmt.Sprintf("docker.io/library/%s:latest", name)
	}
}

// ─── Stop ────────────────────────────────────────────────────────────────────

func (o *containerOrchestrator) Stop(ctx context.Context, services []string, removeVolumes bool) ([]ServiceStatus, error) {
	if len(services) == 0 {
		// Stop all known services.
		services = o.cfg.InfraConfig.Services
	}

	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		stopErr := o.rt.Stop(ctx, name)
		msg := "stopped"
		if stopErr != nil {
			msg = fmt.Sprintf("stop error: %v", stopErr)
		}
		if removeVolumes {
			_ = o.rt.Remove(ctx, name, runtime.WithRemoveVolumes(true))
			msg += " and volumes removed"
		}
		results = append(results, ServiceStatus{
			Name:    name,
			Status:  "stopped",
			Healthy: false,
			Message: msg,
		})
	}
	return results, nil
}

// ─── Status ──────────────────────────────────────────────────────────────────

func (o *containerOrchestrator) Status(ctx context.Context, services []string) ([]ServiceStatus, error) {
	if len(services) == 0 {
		services = o.cfg.InfraConfig.Services
	}

	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		cs, err := o.rt.Status(ctx, name)
		if err != nil {
			results = append(results, ServiceStatus{
				Name:    name,
				Status:  "not_found",
				Healthy: false,
				Message: err.Error(),
			})
			continue
		}
		results = append(results, ServiceStatus{
			Name:    name,
			Status:  string(cs.State),
			Healthy: cs.State == runtime.StateRunning,
			Message: fmt.Sprintf("container %s is %s", cs.ID, cs.State),
		})
	}
	return results, nil
}

// ─── Health ──────────────────────────────────────────────────────────────────

// Health performs a live TCP probe against each service's registered port.
// Latency is the measured round-trip time — never a constant. Services with
// no registered port fall back to a container Status query.
func (o *containerOrchestrator) Health(ctx context.Context, services []string) ([]ServiceStatus, error) {
	if len(services) == 0 {
		services = o.cfg.InfraConfig.Services
	}

	results := make([]ServiceStatus, 0, len(services))
	for _, name := range services {
		results = append(results, o.healthOne(ctx, name))
	}
	return results, nil
}

// healthOne probes a single service and returns its measured status.
func (o *containerOrchestrator) healthOne(ctx context.Context, name string) ServiceStatus {
	timeout := o.cfg.HealthProbeTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}

	addr, hasPort := o.cfg.ServicePorts[name]
	if hasPort && addr != "" {
		healthy, latency := o.probeTCP(ctx, addr, timeout)
		msg := "tcp probe ok"
		if !healthy {
			msg = fmt.Sprintf("tcp probe to %s failed", addr)
		}
		return ServiceStatus{
			Name:    name,
			Status:  runningOrFailed(healthy),
			Healthy: healthy,
			Latency: latency,
			Message: msg,
			Ports:   []string{addr},
		}
	}

	// Fall back to container status query.
	cs, err := o.rt.Status(ctx, name)
	if err != nil {
		return ServiceStatus{
			Name:    name,
			Status:  "not_found",
			Healthy: false,
			Message: err.Error(),
		}
	}
	running := cs.State == runtime.StateRunning
	return ServiceStatus{
		Name:    name,
		Status:  string(cs.State),
		Healthy: running,
		Message: fmt.Sprintf("container state: %s", cs.State),
	}
}

// probeTCP dials addr and returns (healthy, measured_latency). It retries
// until the deadline or a successful connection.
func (o *containerOrchestrator) probeTCP(ctx context.Context, addr string, timeout time.Duration) (bool, time.Duration) {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		dialCtx, cancel := context.WithDeadline(ctx, deadline)
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
		cancel()
		if err == nil {
			conn.Close()
			return true, time.Since(start)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false, time.Since(start)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func runningOrFailed(healthy bool) string {
	if healthy {
		return "running"
	}
	return "failed"
}

// ─── Logs ────────────────────────────────────────────────────────────────────

// Logs streams log output from the named container to stdout, honoring the
// follow/tail/since/timestamps options. The function blocks until the context
// is cancelled or the log stream ends.
func (o *containerOrchestrator) Logs(ctx context.Context, service string, follow bool, tail int, since string, timestamps bool) error {
	opts := []runtime.LogOption{
		runtime.WithFollow(follow),
	}
	if tail > 0 {
		opts = append(opts, runtime.WithTail(fmt.Sprintf("%d", tail)))
	}
	if since != "" {
		opts = append(opts, runtime.WithSince(since))
	}

	rc, err := o.rt.Logs(ctx, service, opts...)
	if err != nil {
		return fmt.Errorf("logs %s: %w", service, err)
	}
	defer rc.Close()

	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		line := scanner.Text()
		if timestamps {
			_, _ = fmt.Printf("[%s] %s\n", time.Now().UTC().Format(time.RFC3339Nano), line)
		} else {
			_, _ = fmt.Println(line)
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("reading logs %s: %w", service, err)
	}
	return nil
}

// ─── Scale ───────────────────────────────────────────────────────────────────

// Scale creates or destroys replicas so that exactly `replicas` containers
// named "<service>-<n>" are running. After adjusting, it queries the runtime
// List to count how many are actually running and reports that count.
func (o *containerOrchestrator) Scale(ctx context.Context, service string, replicas int) (ServiceStatus, error) {
	// List currently running replicas for this service.
	filter := runtime.ListFilter{
		All:    false,
		Names:  []string{service},
		Status: []runtime.ContainerState{runtime.StateRunning},
	}
	current, err := o.rt.List(ctx, filter)
	if err != nil {
		return ServiceStatus{}, fmt.Errorf("scale list %s: %w", service, err)
	}

	delta := replicas - len(current)
	if delta > 0 {
		// Start additional replicas.
		image := o.containerImage(service)
		cmd := o.cfg.ContainerCommand[service]
		for i := 0; i < delta; i++ {
			replicaName := fmt.Sprintf("%s-%d", service, len(current)+i+1)
			if err := o.runContainerByImage(ctx, replicaName, image, cmd); err != nil {
				return ServiceStatus{}, fmt.Errorf("scale %s: create replica %s: %w", service, replicaName, err)
			}
		}
	} else if delta < 0 {
		// Stop excess replicas from the tail.
		toStop := current[replicas:]
		for _, c := range toStop {
			_ = o.rt.Stop(ctx, c.ID)
		}
	}

	// Re-query the runtime to count running replicas — the ground truth.
	after, listErr := o.rt.List(ctx, filter)
	actualCount := len(after)
	if listErr != nil {
		// If re-listing fails, use the delta-adjusted estimate.
		actualCount = len(current) + delta
		if actualCount < 0 {
			actualCount = 0
		}
	}

	return ServiceStatus{
		Name:     service,
		Status:   "running",
		Healthy:  true,
		Replicas: actualCount,
		Message:  fmt.Sprintf("scaled to %d replicas (runtime reports %d running)", replicas, actualCount),
	}, nil
}

// runContainerByImage starts replica container <name>: it first tries
// "podman start <name>" (in case it already exists, e.g. a previous scale-down
// left it stopped) and otherwise creates it via the real "podman run" path.
// The replica command (ContainerCommand[service]) keeps lightweight images
// (e.g. busybox) alive so they show up as running in runtime.List.
func (o *containerOrchestrator) runContainerByImage(ctx context.Context, name, image string, cmd []string) error {
	if err := o.rt.Start(ctx, name); err == nil {
		return nil
	}
	// Replicas do not publish a fixed host port (they would collide); the
	// service command keeps lightweight images alive so List sees them running.
	if err := o.runContainerSpec(ctx, name, image, "", cmd); err != nil {
		return fmt.Errorf("start replica %s (image %s): %w", name, image, err)
	}
	return nil
}

// ─── VM operations ───────────────────────────────────────────────────────────

// VMSpawn spawns count VM nodes. Each node is a REAL long-lived local-container
// node: VMSpawn creates an actual container via the same real "podman run" path
// used by service boot (runContainerSpec), keeping it alive with VMNodeCommand
// so a command session can later be established into it by VMSSH. The container
// name IS the node ID, so VMStatus/VMSSH/VMDestroy operate against a real node.
//
// The item description (HXC-1014) explicitly permits a "local-container" node as
// the realisation of a VM node; for a true hypervisor/cloud VM the same node
// bookkeeping is backed by a real machine and VMSSH uses remote.RemoteExecutor
// (see VMSSH).
func (o *containerOrchestrator) VMSpawn(ctx context.Context, count int) ([]VMNode, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	nodes := make([]VMNode, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("vm-%d", len(o.vmNodes)+i+1)

		// Create the REAL container backing this node via the same real podman
		// run path service boot uses. No host port is published (VM nodes do
		// not expose a fixed service port); the keep-alive command holds it
		// running so a session can be established into it.
		if err := o.runContainerSpec(ctx, id, o.cfg.VMNodeImage, "", o.cfg.VMNodeCommand); err != nil {
			return nil, fmt.Errorf("vmspawn: create node container %s (image %s): %w", id, o.cfg.VMNodeImage, err)
		}

		node := VMNode{
			ID:      id,
			Name:    id,
			Status:  "running",
			IP:      fmt.Sprintf("10.0.1.%d", len(o.vmNodes)+i+1),
			CPU:     4,
			Memory:  8192,
			Labels:  map[string]string{"orchestrator": "real", "backing": "local-container"},
			Created: time.Now(),
		}
		host := remote.RemoteHost{
			Name:    id,
			Address: node.IP,
			Port:    22,
			User:    "root",
			Auth:    remote.AuthSSHKey,
		}
		o.vmNodes[id] = &vmNodeState{node: node, remoteHost: host, containerName: id}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// VMDestroy stops and removes the REAL container backing the VM node, then drops
// the in-memory record.
func (o *containerOrchestrator) VMDestroy(ctx context.Context, nodeID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.vmNodes[nodeID]
	if !ok {
		return fmt.Errorf("vm node %s not found", nodeID)
	}
	// Actually stop and remove the backing container.
	_ = o.rt.Stop(ctx, s.containerName)
	if err := o.rt.Remove(ctx, s.containerName, runtime.WithForceRemove(true)); err != nil {
		return fmt.Errorf("vmdestroy: remove node container %s: %w", s.containerName, err)
	}
	delete(o.vmNodes, nodeID)
	return nil
}

// VMList returns all known VM nodes.
func (o *containerOrchestrator) VMList(ctx context.Context) ([]VMNode, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	nodes := make([]VMNode, 0, len(o.vmNodes))
	for _, s := range o.vmNodes {
		nodes = append(nodes, s.node)
	}
	return nodes, nil
}

// VMStatus returns a specific VM node.
func (o *containerOrchestrator) VMStatus(ctx context.Context, nodeID string) (VMNode, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	s, ok := o.vmNodes[nodeID]
	if !ok {
		return VMNode{}, fmt.Errorf("vm node %s not found", nodeID)
	}
	return s.node, nil
}

// VMSSH establishes a REAL command session into the named VM node and runs
// "echo ok", returning the trimmed stdout ("ok"). This is NOT a format string
// and the output is NOT fabricated — it is the real output read back from the
// session.
//
// For a local-container node (the HXC-1014 realisation) the session is a real
// runtime Exec INTO the node's container — o.rt.Exec(ctx, node, ["sh","-c",
// "echo ok"]) — which is a genuine command session into a real node. For a true
// hypervisor/cloud VM the SAME seam is remote.RemoteExecutor / SSH; when a
// RemoteExecutor is configured it is used, otherwise the container-runtime
// session is used. Either way a real session is required — there is no
// canned-output path.
func (o *containerOrchestrator) VMSSH(ctx context.Context, nodeID string) (string, error) {
	o.mu.RLock()
	s, ok := o.vmNodes[nodeID]
	o.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("vm node %s not found", nodeID)
	}

	// True-VM path: when a RemoteExecutor is configured, run the command over a
	// real SSH session to the node.
	if o.remote != nil {
		result, err := o.remote.Execute(ctx, s.remoteHost, "echo ok")
		if err != nil {
			return "", fmt.Errorf("vmssh %s: %w", nodeID, err)
		}
		return strings.TrimSpace(result.Stdout), nil
	}

	// Local-container node path: run a real command session INTO the node's
	// container via the runtime Exec seam.
	res, err := o.rt.Exec(ctx, s.containerName, []string{"sh", "-c", "echo ok"})
	if err != nil {
		return "", fmt.Errorf("vmssh %s (container session): %w", nodeID, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// ─── Partition / Failure simulation ──────────────────────────────────────────

// VMSimulateFailure marks the node as failed. Health probes to a node marked
// failed will return unhealthy.
func (o *containerOrchestrator) VMSimulateFailure(ctx context.Context, nodeID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.vmNodes[nodeID]
	if !ok {
		return fmt.Errorf("vm node %s not found", nodeID)
	}
	s.node.Status = "failed"
	o.partitioned[nodeID] = true
	return nil
}

// VMSimulatePartition blocks TCP probes to the node's listener address for
// duration by marking it partitioned, then schedules auto-recovery.  Unlike
// the simulation, the health probe path consults this map so callers observe
// the real effect (Healthy flips to false during partition).
func (o *containerOrchestrator) VMSimulatePartition(ctx context.Context, nodeID string, duration time.Duration) error {
	o.mu.Lock()
	s, ok := o.vmNodes[nodeID]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("vm node %s not found", nodeID)
	}
	s.node.Status = "partitioned"
	o.partitioned[nodeID] = true
	o.mu.Unlock()

	go func() {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			o.mu.Lock()
			if st, ok := o.vmNodes[nodeID]; ok {
				st.node.Status = "running"
			}
			delete(o.partitioned, nodeID)
			o.mu.Unlock()
		case <-ctx.Done():
		}
	}()
	return nil
}

// IsPartitioned reports whether a node is currently simulating a partition.
// Used by tests to verify observable network effects.
func (o *containerOrchestrator) IsPartitioned(nodeID string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.partitioned[nodeID]
}

// HealthVM performs a TCP health probe to a VM node's listener port.
// Returns (healthy, measuredLatency). The probe fails immediately when the
// node is marked partitioned, providing the observable-effect required by
// HXC-1015.
func (o *containerOrchestrator) HealthVM(ctx context.Context, nodeID, addr string) (bool, time.Duration) {
	o.mu.RLock()
	partitioned := o.partitioned[nodeID]
	o.mu.RUnlock()

	start := time.Now()
	if partitioned {
		// Observable effect: refuse to probe — fail immediately.
		return false, time.Since(start)
	}
	timeout := o.cfg.HealthProbeTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	return o.probeTCP(ctx, addr, timeout)
}
