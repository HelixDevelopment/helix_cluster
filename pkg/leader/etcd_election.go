// Package leader – etcd-backed leader election with fencing tokens.
//
// EtcdElection implements distributed leader election over a real etcd cluster
// using etcd sessions + elections (go.etcd.io/etcd/client/v3/concurrency).
// It exposes the same fencing-token contract as the in-memory LeaseRegistry:
// every successful leadership acquisition issues a strictly-increasing epoch,
// and a stale leader can be detected by any consumer that tracks the highest
// epoch it has observed.
package leader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// EtcdElectionBackend is the injectable seam that isolates EtcdElection from
// the real clientv3/concurrency library during unit tests.
// The concurrency.Session + concurrency.Election pair satisfies this interface
// in production; a fake satisfies it in unit tests.
type EtcdElectionBackend interface {
	// Campaign blocks until this candidate wins the election, then writes value
	// as the leader value.
	Campaign(ctx context.Context, value string) error
	// Resign gives up leadership.
	Resign(ctx context.Context) error
	// Leader returns the current leader's KV, or an error if there is none.
	Leader(ctx context.Context) (*clientv3.GetResponse, error)
	// Observe returns a channel that fires whenever leadership changes.
	Observe(ctx context.Context) <-chan clientv3.GetResponse
	// LeaseID returns the underlying session lease ID (used in FencingToken).
	LeaseID() clientv3.LeaseID
	// Close releases the session.
	Close() error
}

// EtcdEpochStore persists and retrieves the monotonic fencing epoch in etcd.
// This makes the epoch durable across restarts: a new leader reads the current
// epoch from etcd and increments it, so the token is guaranteed to be strictly
// greater even after a crash-restart cycle.
type EtcdEpochStore interface {
	// IncrementAndGet atomically increments the epoch stored at epochKey and
	// returns the new value. It MUST be strictly monotonic.
	IncrementAndGet(ctx context.Context, epochKey string) (uint64, error)
	// Get reads the current epoch (0 if not yet written).
	Get(ctx context.Context, epochKey string) (uint64, error)
}

// EtcdElection is a distributed leader election backed by real etcd sessions
// and the concurrency.Election primitive. It satisfies HXC-1093.
type EtcdElection struct {
	mu sync.RWMutex

	localID  string
	elecKey  string // etcd election key prefix, e.g. "/helix/election/<service>"
	epochKey string // etcd key for durable epoch counter

	backend    EtcdElectionBackend
	epochStore EtcdEpochStore

	currentEpoch  uint64
	currentToken  FencingToken
	isLeader      bool
	leaderID      string
	leaderPayload string // raw JSON from the winner's Campaign value

	// stopCh / wg for background campaigner goroutine.
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// EtcdElectionConfig configures an EtcdElection.
type EtcdElectionConfig struct {
	// LocalID uniquely identifies this candidate node.
	LocalID string
	// ElectionKey is the etcd key prefix under which the election runs,
	// e.g. "/helix/election/scheduler".
	ElectionKey string
	// EpochKey is the etcd key used to store the durable fencing epoch counter,
	// e.g. "/helix/election/scheduler/epoch". If empty, defaults to ElectionKey+"/epoch".
	EpochKey string
}

// LeaderValue is the JSON payload written to etcd by the winning candidate.
// Embedding the per-run UUID and epoch here makes the sink-side value inspectable.
type LeaderValue struct {
	HolderID string `json:"holder_id"`
	Epoch    uint64 `json:"epoch"`
	RunID    string `json:"run_id"` // per-run UUID for anti-bluff evidence
}

// NewEtcdElection creates a new EtcdElection.
//
// backend is the injectable seam (pass nil to force the production path — but
// production callers should use NewEtcdElectionFromClient).
// epochStore is the injectable seam for epoch persistence (pass nil only in
// tests that inject a backend that manages epoch counting themselves).
func NewEtcdElection(cfg EtcdElectionConfig, backend EtcdElectionBackend, epochStore EtcdEpochStore) (*EtcdElection, error) {
	if cfg.LocalID == "" {
		return nil, errors.New("leader.EtcdElection: LocalID is required")
	}
	if cfg.ElectionKey == "" {
		return nil, errors.New("leader.EtcdElection: ElectionKey is required")
	}
	epochKey := cfg.EpochKey
	if epochKey == "" {
		epochKey = cfg.ElectionKey + "/epoch"
	}
	return &EtcdElection{
		localID:    cfg.LocalID,
		elecKey:    cfg.ElectionKey,
		epochKey:   epochKey,
		backend:    backend,
		epochStore: epochStore,
		stopCh:     make(chan struct{}),
	}, nil
}

// concreteEtcdBackend wraps a real concurrency.Session + concurrency.Election.
type concreteEtcdBackend struct {
	s *concurrency.Session
	e *concurrency.Election
}

func (b *concreteEtcdBackend) Campaign(ctx context.Context, value string) error {
	return b.e.Campaign(ctx, value)
}
func (b *concreteEtcdBackend) Resign(ctx context.Context) error {
	return b.e.Resign(ctx)
}
func (b *concreteEtcdBackend) Leader(ctx context.Context) (*clientv3.GetResponse, error) {
	return b.e.Leader(ctx)
}
func (b *concreteEtcdBackend) Observe(ctx context.Context) <-chan clientv3.GetResponse {
	return b.e.Observe(ctx)
}
func (b *concreteEtcdBackend) LeaseID() clientv3.LeaseID {
	return b.s.Lease()
}
func (b *concreteEtcdBackend) Close() error {
	return b.s.Close()
}

// concreteEpochStore uses real etcd compare-and-swap to increment a counter.
type concreteEpochStore struct {
	cli *clientv3.Client
}

// IncrementAndGet increments the epoch at key using an etcd transaction loop.
// The loop retries if a concurrent writer updates the value between our read and CAS.
func (s *concreteEpochStore) IncrementAndGet(ctx context.Context, key string) (uint64, error) {
	for {
		// Read current value.
		resp, err := s.cli.Get(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("epoch get: %w", err)
		}

		var cur uint64
		var version int64
		if len(resp.Kvs) > 0 {
			kv := resp.Kvs[0]
			if _, err := fmt.Sscanf(string(kv.Value), "%d", &cur); err != nil {
				return 0, fmt.Errorf("epoch parse %q: %w", string(kv.Value), err)
			}
			version = kv.Version
		}
		next := cur + 1

		var txnResp *clientv3.TxnResponse
		if version == 0 {
			// Key does not exist: create it only if it still does not exist.
			txnResp, err = s.cli.Txn(ctx).
				If(clientv3.Compare(clientv3.Version(key), "=", 0)).
				Then(clientv3.OpPut(key, fmt.Sprintf("%d", next))).
				Commit()
		} else {
			// Key exists: update only if version unchanged (optimistic CAS).
			txnResp, err = s.cli.Txn(ctx).
				If(clientv3.Compare(clientv3.Version(key), "=", version)).
				Then(clientv3.OpPut(key, fmt.Sprintf("%d", next))).
				Commit()
		}
		if err != nil {
			return 0, fmt.Errorf("epoch txn: %w", err)
		}
		if txnResp.Succeeded {
			return next, nil
		}
		// CAS failed — another writer updated; retry.
	}
}

func (s *concreteEpochStore) Get(ctx context.Context, key string) (uint64, error) {
	resp, err := s.cli.Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("epoch get: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return 0, nil
	}
	var v uint64
	if _, err := fmt.Sscanf(string(resp.Kvs[0].Value), "%d", &v); err != nil {
		return 0, fmt.Errorf("epoch parse: %w", err)
	}
	return v, nil
}

// NewEtcdElectionFromClient creates a production EtcdElection from a real
// etcd client. It creates a new session and election under cfg.ElectionKey.
func NewEtcdElectionFromClient(ctx context.Context, cli *clientv3.Client, cfg EtcdElectionConfig, ttl int) (*EtcdElection, error) {
	if ttl <= 0 {
		ttl = 5
	}
	s, err := concurrency.NewSession(cli, concurrency.WithTTL(ttl))
	if err != nil {
		return nil, fmt.Errorf("leader.NewEtcdElectionFromClient: session: %w", err)
	}
	e := concurrency.NewElection(s, cfg.ElectionKey)
	backend := &concreteEtcdBackend{s: s, e: e}
	store := &concreteEpochStore{cli: cli}
	return NewEtcdElection(cfg, backend, store)
}

// Campaign blocks until this node wins the election, then records the
// fencing token and sets IsLeader. The per-run runID is embedded in the
// leader value (etcd key payload) for sink-side evidence.
func (ee *EtcdElection) Campaign(ctx context.Context, runID string) (FencingToken, error) {
	// Increment epoch before campaigning so that even if two campaigns race,
	// each winner gets a strictly higher epoch (the increment is serialised by
	// etcd's CAS transaction inside IncrementAndGet).
	var epoch uint64
	var err error
	if ee.epochStore != nil {
		epoch, err = ee.epochStore.IncrementAndGet(ctx, ee.epochKey)
		if err != nil {
			return FencingToken{}, fmt.Errorf("leader.Campaign: epoch increment: %w", err)
		}
	} else {
		ee.mu.Lock()
		ee.currentEpoch++
		epoch = ee.currentEpoch
		ee.mu.Unlock()
	}

	payload, err := json.Marshal(LeaderValue{
		HolderID: ee.localID,
		Epoch:    epoch,
		RunID:    runID,
	})
	if err != nil {
		return FencingToken{}, fmt.Errorf("leader.Campaign: marshal payload: %w", err)
	}

	if err := ee.backend.Campaign(ctx, string(payload)); err != nil {
		return FencingToken{}, fmt.Errorf("leader.Campaign: campaign: %w", err)
	}

	tok := FencingToken{
		HolderID: ee.localID,
		Epoch:    epoch,
		Expiry:   time.Now().Add(5 * time.Second), // advisory; real expiry is the etcd lease TTL
	}
	ee.mu.Lock()
	ee.isLeader = true
	ee.leaderID = ee.localID
	ee.currentToken = tok
	ee.currentEpoch = epoch
	ee.mu.Unlock()

	return tok, nil
}

// Resign gives up leadership.
func (ee *EtcdElection) Resign(ctx context.Context) error {
	err := ee.backend.Resign(ctx)
	ee.mu.Lock()
	ee.isLeader = false
	if ee.leaderID == ee.localID {
		ee.leaderID = ""
	}
	ee.mu.Unlock()
	return err
}

// IsLeader returns true if this node currently holds the etcd election.
func (ee *EtcdElection) IsLeader() bool {
	ee.mu.RLock()
	defer ee.mu.RUnlock()
	return ee.isLeader
}

// LeaderID returns the current leader node ID (empty if unknown).
func (ee *EtcdElection) LeaderID() string {
	ee.mu.RLock()
	defer ee.mu.RUnlock()
	return ee.leaderID
}

// CurrentToken returns the most recently issued fencing token.
func (ee *EtcdElection) CurrentToken() FencingToken {
	ee.mu.RLock()
	defer ee.mu.RUnlock()
	return ee.currentToken
}

// CurrentEpoch returns the most recently issued fencing epoch.
func (ee *EtcdElection) CurrentEpoch() uint64 {
	ee.mu.RLock()
	defer ee.mu.RUnlock()
	return ee.currentEpoch
}

// QueryLeader reads the current leader's value from etcd and parses it.
// Returns an error if no leader exists.
func (ee *EtcdElection) QueryLeader(ctx context.Context) (LeaderValue, error) {
	resp, err := ee.backend.Leader(ctx)
	if err != nil {
		return LeaderValue{}, fmt.Errorf("leader.QueryLeader: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return LeaderValue{}, errors.New("leader.QueryLeader: no leader elected")
	}
	var lv LeaderValue
	if err := json.Unmarshal(resp.Kvs[0].Value, &lv); err != nil {
		return LeaderValue{}, fmt.Errorf("leader.QueryLeader: parse value: %w", err)
	}
	return lv, nil
}

// Close releases the underlying session.
func (ee *EtcdElection) Close() error {
	return ee.backend.Close()
}

// StaleLeader checks if a given fencing token is stale relative to the
// highest observed epoch. Consumers call this to reject requests from
// former leaders whose lease was revoked.
func StaleLeader(tok FencingToken, highestSeenEpoch uint64) bool {
	return tok.IsStale(highestSeenEpoch)
}
