package multiraft

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pb "go.etcd.io/raft/v3/raftpb"
)

// asyncTransport is a REAL in-process RaftTransport that delivers every raft
// message from a DEDICATED background goroutine — NOT inline on the sender's
// goroutine like InProcTransport. This is the network-transport shape the
// SYNCHRONOUS-DELIVERY contract used to forbid: the registered inbox handler is
// invoked concurrently with the manager's own driving (Tick/Propose/Run), so it
// races dst.stopped / RawNode.Step UNLESS the handler serializes under sh.mu.
//
// It exists to PROVE (HXC-909, CLAUDE-1) that the multiraft inbox path is now
// async-delivery-safe by construction: under -race, concurrent Propose/Tick with
// goroutine-delivered messages must show NO data race AND still reach correct
// commits. If the inbox handler were left unguarded (no sh.mu), -race would flag
// the Step here racing processReadyLocked — that is the mutation guard.
type asyncTransport struct {
	mu    sync.RWMutex
	inbox map[inprocKey]MessageHandler

	wg     sync.WaitGroup
	closed chan struct{}
	once   sync.Once

	// delivered counts messages handed to a registered handler from the worker
	// goroutine — sink-side evidence that delivery was genuinely asynchronous.
	deliveredMu sync.Mutex
	delivered   int
}

func newAsyncTransport() *asyncTransport {
	return &asyncTransport{
		inbox:  make(map[inprocKey]MessageHandler),
		closed: make(chan struct{}),
	}
}

var _ RaftTransport = (*asyncTransport)(nil)

func (t *asyncTransport) RegisterPeer(shard ShardID, nodeID uint64, inbox MessageHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inbox[inprocKey{shard: shard, nodeID: nodeID}] = inbox
}

func (t *asyncTransport) UnregisterPeer(shard ShardID, nodeID uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inbox, inprocKey{shard: shard, nodeID: nodeID})
}

// Send dispatches delivery to a NEW goroutine and returns immediately. The handler
// therefore runs concurrently with whatever the manager goroutine does next. This
// is the asynchronous delivery path the fix must make race-free.
func (t *asyncTransport) Send(shard ShardID, msg pb.Message) error {
	if msg.To == 0 {
		return fmt.Errorf("multiraft: async transport refusing zero destination in shard %v", shard)
	}
	t.mu.RLock()
	handler, ok := t.inbox[inprocKey{shard: shard, nodeID: msg.To}]
	t.mu.RUnlock()
	if !ok {
		return nil // downed/unknown peer: drop, like a real network
	}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		select {
		case <-t.closed:
			return
		default:
		}
		handler(msg)
		t.deliveredMu.Lock()
		t.delivered++
		t.deliveredMu.Unlock()
	}()
	return nil
}

// SendRead is unused by these tests but required by the interface.
func (t *asyncTransport) SendRead(shard ShardID, leaseholderID uint64) ([]byte, error) {
	return nil, fmt.Errorf("%w: shard %v node %d", ErrNoReadHandler, shard, leaseholderID)
}

// quiesce waits for all in-flight async deliveries to finish so the test can take a
// stable snapshot of committed state.
func (t *asyncTransport) quiesce() { t.wg.Wait() }

func (t *asyncTransport) deliveredCount() int {
	t.deliveredMu.Lock()
	defer t.deliveredMu.Unlock()
	return t.delivered
}

// driveAsyncUntilLeaders ticks until every shard elects a leader, waiting for the
// async transport to quiesce between rounds so goroutine-delivered vote/heartbeat
// messages actually land before we re-check leadership.
func driveAsyncUntilLeaders(t *testing.T, m *MultiRaftManager, tr *asyncTransport, shards []ShardID, maxTicks int) {
	t.Helper()
	for i := 0; i < maxTicks; i++ {
		m.Tick()
		tr.quiesce()
		m.Run()
		tr.quiesce()
		all := true
		for _, id := range shards {
			if m.LeaderID(id) == 0 {
				all = false
				break
			}
		}
		if all {
			return
		}
	}
	for _, id := range shards {
		if m.LeaderID(id) == 0 {
			t.Fatalf("shard %q never elected a leader within %d ticks (async)", id, maxTicks)
		}
	}
}

// TestAsyncTransportConcurrentProposeTickNoRaceAndCommits is the HXC-909 regression.
//
// It drives the manager with an asynchronous (goroutine-delivering) transport while
// MULTIPLE goroutines concurrently Propose and Tick. Under `go test -race` this
// exercises the inbox handler's Step CONCURRENTLY with processReadyLocked. The test
// asserts (CLAUDE-1 sink-side):
//   - no data race is reported by the race detector (regression: today's unguarded
//     handler would race here), and
//   - the shard's leader actually COMMITS every proposed payload (the feature still
//     works end-to-end over the async path, not merely "doesn't panic").
//
// MUTATION GUARD: if the inbox Step in CreateShard is left unguarded (sh.mu removed),
// this test FAILS under -race because asyncTransport.Send delivers from its own
// goroutine, so handler→dst.raw.Step races the manager goroutine's
// processReadyLocked. The guard is the sh.mu Lock/Unlock around the handler body.
func TestAsyncTransportConcurrentProposeTickNoRaceAndCommits(t *testing.T) {
	tr := newAsyncTransport()
	m := NewMultiRaftManager(tr)

	const nShards = 3
	ids := make([]ShardID, nShards)
	for i := 0; i < nShards; i++ {
		ids[i] = ShardID(fmt.Sprintf("async-shard-%d", i))
		if err := m.CreateShard(ids[i], threePeers()); err != nil {
			t.Fatalf("CreateShard %q: %v", ids[i], err)
		}
	}

	driveAsyncUntilLeaders(t, m, tr, ids, 400)

	const perShard = 5
	// Concurrency: a background ticker keeps driving the clock while proposer
	// goroutines fire proposals. Tick and Propose both queue outbound messages that
	// the async transport delivers from other goroutines — so the inbox handler runs
	// concurrently with these manager calls. This is the race the fix must prevent.
	stop := make(chan struct{})
	var tickWG sync.WaitGroup
	tickWG.Add(1)
	go func() {
		defer tickWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.Tick()
				m.Run()
			}
		}
	}()

	var pwg sync.WaitGroup
	errs := make(chan error, nShards*perShard)
	for _, id := range ids {
		for j := 0; j < perShard; j++ {
			pwg.Add(1)
			go func(id ShardID, j int) {
				defer pwg.Done()
				payload := []byte(fmt.Sprintf("%s:async:%d", id, j))
				var lastErr error
				for attempt := 0; attempt < 200; attempt++ {
					if err := m.Propose(context.Background(), id, payload); err != nil {
						lastErr = err
						time.Sleep(time.Millisecond)
						continue
					}
					lastErr = nil
					break
				}
				if lastErr != nil {
					errs <- fmt.Errorf("propose %s/%d: %w", id, j, lastErr)
				}
			}(id, j)
		}
	}
	pwg.Wait()
	close(stop)
	tickWG.Wait()

	// Settle: drive + quiesce repeatedly so every async-delivered append/commit lands.
	for i := 0; i < 400; i++ {
		m.Tick()
		tr.quiesce()
		m.Run()
		tr.quiesce()
	}

	if tr.deliveredCount() == 0 {
		t.Fatal("async transport delivered zero messages — delivery was not actually exercised")
	}

	// Sink-side: each shard's leader committed all perShard proposals routed to it.
	for _, id := range ids {
		leader := m.LeaderID(id)
		if leader == 0 {
			t.Fatalf("shard %q has no leader after async run", id)
		}
		entries, err := m.CommittedEntries(id, leader)
		if err != nil {
			t.Fatalf("CommittedEntries %q/%d: %v", id, leader, err)
		}
		for j := 0; j < perShard; j++ {
			want := fmt.Sprintf("%s:async:%d", id, j)
			found := false
			for _, e := range entries {
				if string(e) == want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("shard %q leader %d did not commit %q over async transport (got %d entries)", id, leader, want, len(entries))
			}
		}
	}
}

// TestAsyncTransportDeliversFromSeparateGoroutine proves the asyncTransport actually
// delivers off the caller's goroutine (otherwise the regression above would not be
// exercising the concurrent path at all). It registers a handler that records the
// goroutine-id-distinct fact via a channel and asserts Send returns BEFORE delivery.
func TestAsyncTransportDeliversFromSeparateGoroutine(t *testing.T) {
	tr := newAsyncTransport()

	release := make(chan struct{})
	delivered := make(chan struct{})
	tr.RegisterPeer("s", 2, func(msg pb.Message) {
		<-release // block delivery until the test allows it
		close(delivered)
	})

	if err := tr.Send("s", pb.Message{To: 2, From: 1}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// If delivery were synchronous, Send would have blocked on <-release and we'd
	// never reach here. Because it returned, delivery is on another goroutine.
	select {
	case <-delivered:
		t.Fatal("handler ran before release — delivery was synchronous, not async")
	default:
	}
	close(release)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("async delivery never happened")
	}
	tr.quiesce()
}
