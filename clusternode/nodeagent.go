// Package clusternode is the end-to-end integration point of Helix Cluster OS:
// it composes the project's proven, independently-tested building blocks into a
// single runnable node agent and a real, in-process 3-node cluster bring-up.
//
// This package writes NO new cluster mechanism. It is pure composition — every
// capability is delegated to an existing, in-workspace module that already has
// its own real (non-mock) tests:
//
//   - Capabilities : github.com/HelixDevelopment/helix_cluster/hwinventory
//     (real per-OS CPU/mem/GPU/NPU/FPGA probe).
//   - Discovery    : github.com/HelixDevelopment/helix_cluster/discovery/p2p
//     (a real libp2p host with a Kademlia DHT — registry-free peer discovery).
//   - Consensus    : github.com/HelixDevelopment/helix_cluster/pkg/raftleader
//     over pkg/raft (real embedded hashicorp/raft leader election).
//   - Messaging    : github.com/HelixDevelopment/helix_cluster/edge/gateway
//     (a transport-agnostic envelope router with an in-process loopback
//     transport).
//
// The value proven here is that these blocks COMPOSE for an end user
// (HelixConstitution CLAUDE-1): a node agent can, at once, discover its peers,
// take part in a real leader election, report its hardware capabilities, and
// route a message to a destination — all through the real components, with the
// integration test asserting the user-visible outcome of each.
//
// # Composition model
//
// Leader election in pkg/raft is ONE consensus group spanning N node IDs (see
// raftleader.NewInmemElectionCluster), not one isolated instance per process.
// A NodeCluster therefore owns the single shared raft ElectionCluster and hands
// each NodeAgent its own RaftElection seat in that group. Discovery and the
// messaging gateway are genuinely per-node: each agent runs its own libp2p host
// and its own Gateway, exactly as three separate machines would. This mirrors a
// real deployment (one raft group, per-node hosts) while staying in-process so
// the whole thing is testable without a network or disk.
package clusternode

import (
	"context"
	"fmt"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/discovery/p2p"
	"github.com/HelixDevelopment/helix_cluster/edge/gateway"
	"github.com/HelixDevelopment/helix_cluster/hwinventory"
	"github.com/HelixDevelopment/helix_cluster/pkg/raftleader"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Config configures a single NodeAgent. The zero value is not usable: ID and
// Election are required (Election is the agent's seat in the shared raft group).
type Config struct {
	// ID is the agent's logical node id. It is used as the agent's gateway
	// routing key and as its human-facing name. It SHOULD match the id of the
	// RaftElection seat so logs line up, but the two are independent surfaces.
	ID string

	// Election is this agent's seat in the shared raft consensus group, produced
	// by a raftleader.ElectionCluster. The agent does not own the underlying raft
	// node — the NodeCluster does — so the agent never shuts it down.
	Election *raftleader.RaftElection

	// BootstrapPeers seeds the discovery host's DHT. For a fresh anchor node this
	// is empty; joining nodes are handed the anchor's AddrInfo.
	BootstrapPeers []peer.AddrInfo

	// Logger receives discovery-layer log lines. When nil a no-op logger is used.
	Logger p2p.Logger
}

// NodeAgent is one composed cluster node: a discovery host, a gateway with an
// in-process loopback endpoint, a seat in the raft leader election, and an
// on-demand hardware-capability probe — all behind one Start/Stop lifecycle.
//
// It is the unit an end user reasons about: "a node that joins the cluster,
// finds its peers, takes part in leader election, knows its own hardware, and
// can send/receive routed messages."
type NodeAgent struct {
	id       string
	election *raftleader.RaftElection

	// discovery is the per-node libp2p host (real DHT-based peer discovery).
	discovery *p2p.Node

	// gw is the per-node messaging gateway; loop is its in-process loopback
	// transport; self is this node's own endpoint on the gateway, on which it
	// receives envelopes addressed to its id.
	gw   *gateway.Gateway
	loop *gateway.LoopbackTransport
	self *gateway.LoopbackConn

	bootstrap []peer.AddrInfo
	logger    p2p.Logger

	mu      sync.Mutex
	started bool
	stopped bool
}

// NewNodeAgent constructs a NodeAgent from cfg. It does not start any subsystem;
// call Start to bring the discovery host up and attach the gateway endpoint.
func NewNodeAgent(cfg Config) (*NodeAgent, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("clusternode: NodeAgent requires a non-empty ID")
	}
	if cfg.Election == nil {
		return nil, fmt.Errorf("clusternode: NodeAgent %q requires a raft Election seat", cfg.ID)
	}
	return &NodeAgent{
		id:        cfg.ID,
		election:  cfg.Election,
		bootstrap: cfg.BootstrapPeers,
		logger:    cfg.Logger,
	}, nil
}

// Start brings the agent's per-node subsystems online:
//
//   - it creates the libp2p discovery host (listening on a loopback TCP port),
//     seeded with any configured bootstrap peers;
//   - it creates the messaging gateway, attaches an in-process loopback
//     transport, and registers this node's own endpoint so envelopes addressed
//     to the agent's id are delivered to it.
//
// It does NOT start the raft election (the shared NodeCluster owns that). Start
// is idempotent-safe to call once; a second call returns an error.
func (a *NodeAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return fmt.Errorf("clusternode: agent %q already started", a.id)
	}
	if a.stopped {
		return fmt.Errorf("clusternode: agent %q cannot be restarted", a.id)
	}

	node, err := p2p.New(ctx, p2p.Config{
		BootstrapPeers: a.bootstrap,
		Logger:         a.logger,
	})
	if err != nil {
		return fmt.Errorf("clusternode: agent %q start discovery: %w", a.id, err)
	}

	gw := gateway.New()
	loop := gateway.NewLoopbackTransport(gw)
	self, err := loop.Connect(a.id)
	if err != nil {
		_ = node.Close()
		return fmt.Errorf("clusternode: agent %q attach gateway endpoint: %w", a.id, err)
	}

	a.discovery = node
	a.gw = gw
	a.loop = loop
	a.self = self
	a.started = true
	return nil
}

// Stop tears the agent's per-node subsystems down cleanly: it closes the gateway
// (which closes the loopback transport and the node's endpoint) and closes the
// discovery host (which tears down all libp2p transports and connections). It
// deliberately leaves the shared raft election untouched — the NodeCluster owns
// the raft group's lifecycle. Stop is idempotent.
func (a *NodeAgent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started || a.stopped {
		return nil
	}
	a.stopped = true

	var errs []error
	if a.gw != nil {
		if err := a.gw.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close gateway: %w", err))
		}
	}
	if a.discovery != nil {
		if err := a.discovery.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close discovery: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("clusternode: agent %q stop: %v", a.id, errs)
	}
	return nil
}

// ID returns the agent's logical node id.
func (a *NodeAgent) ID() string { return a.id }

// Discovery exposes the agent's libp2p discovery host so callers can read its
// AddrInfo (to bootstrap other agents), connect, bootstrap, and find peers
// through the real DHT.
func (a *NodeAgent) Discovery() *p2p.Node { return a.discovery }

// Election exposes the agent's seat in the shared raft consensus group, so
// callers can ask IsLeader()/LeaderID() and observe leadership changes.
func (a *NodeAgent) Election() *raftleader.RaftElection { return a.election }

// Gateway exposes the agent's messaging gateway, the transport-agnostic router
// other endpoints register with and route envelopes through.
func (a *NodeAgent) Gateway() *gateway.Gateway { return a.gw }

// IsLeader reports whether this agent currently holds raft leadership.
func (a *NodeAgent) IsLeader() bool { return a.election.IsLeader() }

// AddrInfo returns the agent's discovery AddrInfo (peer id + listen addrs),
// suitable for handing to another agent as a bootstrap peer.
func (a *NodeAgent) AddrInfo() peer.AddrInfo { return a.discovery.AddrInfo() }

// BootstrapPeers returns the discovery bootstrap peers this agent was seeded
// with. It is the structural anti-bluff witness for registry-free discovery: if
// a target peer is NOT among these (and was never otherwise hand-fed), then the
// agent learning that target's address can only have happened via the DHT.
func (a *NodeAgent) BootstrapPeers() []peer.AddrInfo {
	out := make([]peer.AddrInfo, len(a.bootstrap))
	copy(out, a.bootstrap)
	return out
}

// Capabilities probes the host and returns this node's real hardware-capability
// report (CPU/memory/GPU/NPU/FPGA) via the hwinventory module. It is collected
// on demand rather than cached, so it always reflects the current host.
func (a *NodeAgent) Capabilities(ctx context.Context) (hwinventory.Inventory, error) {
	inv, err := hwinventory.Collect(ctx)
	if err != nil {
		return hwinventory.Inventory{}, fmt.Errorf("clusternode: agent %q collect capabilities: %w", a.id, err)
	}
	return inv, nil
}

// Inbox returns the channel on which envelopes routed to this agent's id are
// delivered. It is the sink an end user observes to confirm a routed message
// actually arrived at its destination.
func (a *NodeAgent) Inbox() <-chan gateway.Envelope { return a.self.Inbox }

// Send routes an envelope into THIS agent's gateway for delivery to its
// destination endpoint. Within a single in-process gateway, dispatch envelopes
// are delivered to the connection(s) registered under env.Dest. (Each agent runs
// its own gateway, so cross-agent routing in the demo registers an observer
// endpoint for the destination on the sender's gateway — see NodeCluster.Route.)
func (a *NodeAgent) Send(env gateway.Envelope) error {
	if a.self == nil {
		return fmt.Errorf("clusternode: agent %q not started", a.id)
	}
	return a.self.Route(env)
}
