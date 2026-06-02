// Package bootstrap provides rendezvous / bootstrap strategies for Helix Cluster OS.
//
// Only the StaticStrategy is fully operational (host-provable, offline-safe). Network
// strategies (DNS-SRV, mDNS) exist as typed placeholders that return ErrUnsupported so
// callers can compose them in a Bootstrapper without needing conditional compilation —
// but they MUST NOT fabricate peers (CLAUDE-2 mandate).
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
)

// ErrUnsupported is returned by strategies that cannot operate in the current
// environment (no real resolver available). Callers use errors.Is to detect it.
var ErrUnsupported = errors.New("bootstrap: strategy unsupported in this environment")

// Peer is a cluster peer reachable at Address ("host:port").
// ID is a deterministic, stable identifier derived from the address.
type Peer struct {
	ID      string
	Address string
}

// Strategy is the interface every bootstrap strategy must implement.
type Strategy interface {
	// Name returns a human-readable identifier for the strategy.
	Name() string
	// Resolve discovers peers and returns them, or an error.
	// Returning (nil, ErrUnsupported) signals the strategy cannot run here.
	Resolve(ctx context.Context) ([]Peer, error)
}

// ────────────────────────────────────────────────────────────────────────────
// StaticStrategy
// ────────────────────────────────────────────────────────────────────────────

// StaticStrategy returns a fixed, pre-validated set of seed peers.
// It is the only fully operational strategy: no network calls are required and
// it is provable on every host including offline sandboxes.
type StaticStrategy struct {
	peers []Peer
}

// NewStaticStrategy parses and validates each "host:port" seed.
// Rules:
//   - seeds must not be empty (at least one entry required).
//   - each seed must parse as host:port via net.SplitHostPort.
//   - duplicate addresses are rejected.
//
// IDs are assigned in sorted seed order so results are deterministic across
// calls regardless of the order seeds were originally supplied.
func NewStaticStrategy(seeds []string) (*StaticStrategy, error) {
	if len(seeds) == 0 {
		return nil, fmt.Errorf("bootstrap: at least one seed address is required")
	}

	// Deduplicate while preserving information for the error message.
	seen := make(map[string]struct{}, len(seeds))
	for _, s := range seeds {
		if _, dup := seen[s]; dup {
			return nil, fmt.Errorf("bootstrap: duplicate seed address %q", s)
		}
		seen[s] = struct{}{}

		// Validate via net.SplitHostPort; rejects bare hosts, empty strings, etc.
		// Additionally require both host and port to be non-empty: "host:" is parsed
		// successfully by SplitHostPort but is not a usable peer address.
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: invalid seed address %q: %w", s, err)
		}
		if host == "" || port == "" {
			return nil, fmt.Errorf("bootstrap: invalid seed address %q: both host and port must be non-empty", s)
		}
	}

	// Assign deterministic IDs by sorting addresses lexicographically.
	sorted := make([]string, len(seeds))
	copy(sorted, seeds)
	sort.Strings(sorted)

	peers := make([]Peer, len(sorted))
	for i, addr := range sorted {
		peers[i] = Peer{
			ID:      fmt.Sprintf("static-%d", i),
			Address: addr,
		}
	}

	return &StaticStrategy{peers: peers}, nil
}

// Name returns "static".
func (s *StaticStrategy) Name() string { return "static" }

// Resolve returns the pre-validated seed peers.
// It never returns an error under normal operation.
func (s *StaticStrategy) Resolve(_ context.Context) ([]Peer, error) {
	out := make([]Peer, len(s.peers))
	copy(out, s.peers)
	return out, nil
}

// ────────────────────────────────────────────────────────────────────────────
// DNSSRVStrategy — typed placeholder (returns ErrUnsupported)
// ────────────────────────────────────────────────────────────────────────────

// DNSSRVStrategy is a strategy placeholder for DNS-SRV based peer discovery.
// No real DNS resolver is available in the sandbox; Resolve returns ErrUnsupported
// and MUST NOT fabricate peers (CLAUDE-2 / CLAUDE-1 mandate).
type DNSSRVStrategy struct {
	service string
	domain  string
}

// NewDNSSRVStrategy creates a DNSSRVStrategy for the given SRV service and domain.
func NewDNSSRVStrategy(service, domain string) *DNSSRVStrategy {
	return &DNSSRVStrategy{service: service, domain: domain}
}

// Name returns "dns-srv".
func (d *DNSSRVStrategy) Name() string { return "dns-srv" }

// Resolve always returns (nil, ErrUnsupported) because no real DNS resolver is
// available. It never fabricates peers — doing so would be a PASS-bluff.
func (d *DNSSRVStrategy) Resolve(_ context.Context) ([]Peer, error) {
	return nil, ErrUnsupported
}

// ────────────────────────────────────────────────────────────────────────────
// MDNSStrategy — typed placeholder (returns ErrUnsupported)
// ────────────────────────────────────────────────────────────────────────────

// MDNSStrategy is a strategy placeholder for mDNS (zero-config) peer discovery.
// No real mDNS implementation is available in the sandbox; Resolve returns
// ErrUnsupported and MUST NOT fabricate peers (CLAUDE-2 / CLAUDE-1 mandate).
type MDNSStrategy struct {
	service string
}

// NewMDNSStrategy creates an MDNSStrategy for the given mDNS service name.
func NewMDNSStrategy(service string) *MDNSStrategy {
	return &MDNSStrategy{service: service}
}

// Name returns "mdns".
func (m *MDNSStrategy) Name() string { return "mdns" }

// Resolve always returns (nil, ErrUnsupported) because no real mDNS stack is
// available. It never fabricates peers — doing so would be a PASS-bluff.
func (m *MDNSStrategy) Resolve(_ context.Context) ([]Peer, error) {
	return nil, ErrUnsupported
}

// ────────────────────────────────────────────────────────────────────────────
// Bootstrapper
// ────────────────────────────────────────────────────────────────────────────

// Bootstrapper tries strategies in the order they were registered and returns
// the peers from the first strategy that yields at least one peer.
// If no strategy yields peers, all errors are joined via errors.Join.
type Bootstrapper struct {
	strategies []Strategy
}

// NewBootstrapper creates a Bootstrapper that will try strategies in order.
func NewBootstrapper(strategies ...Strategy) *Bootstrapper {
	return &Bootstrapper{strategies: strategies}
}

// Bootstrap iterates strategies in registration order. The first strategy that
// returns at least one peer short-circuits the loop and its peers are returned.
// If every strategy returns an error or zero peers, all collected errors are
// joined with errors.Join and returned alongside a nil peer slice.
//
// Zero-peer contract: a strategy that returns ([]Peer{}, nil) — zero peers with
// no error — is treated as a non-fatal miss; a synthetic error
// ("strategy %q: resolved zero peers") is appended to the running error list and
// iteration continues. A strategy implementation that intends "nothing to report
// yet" should return (nil, ErrUnsupported) or a domain-specific error instead of
// (nil, nil), to avoid surprising the caller with an error on total failure.
// If Bootstrap is called with zero strategies it returns (nil, nil) immediately
// because there is nothing to fail on — callers must register at least one
// strategy to obtain peers.
func (b *Bootstrapper) Bootstrap(ctx context.Context) ([]Peer, error) {
	var errs []error
	for _, s := range b.strategies {
		peers, err := s.Resolve(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("strategy %q: %w", s.Name(), err))
			continue
		}
		if len(peers) > 0 {
			return peers, nil
		}
		// Strategy returned zero peers without an error — treat as non-fatal miss.
		errs = append(errs, fmt.Errorf("strategy %q: resolved zero peers", s.Name()))
	}
	return nil, errors.Join(errs...)
}
