package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- mock tmux backend ---

type mockTmuxBackend struct {
	mu       sync.Mutex
	sessions map[string]bool
	windows  map[string]bool
	panes    map[string]bool
	calls    []string
	failNext string
}

func newMockTmuxBackend() *mockTmuxBackend {
	return &mockTmuxBackend{
		sessions: make(map[string]bool),
		windows:  make(map[string]bool),
		panes:    make(map[string]bool),
		calls:    make([]string, 0),
	}
}

func (m *mockTmuxBackend) record(call string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
	if m.failNext != "" && strings.Contains(call, m.failNext) {
		m.failNext = ""
		return errors.New("mock forced failure")
	}
	return nil
}

func (m *mockTmuxBackend) CreateSession(name string, env map[string]string) (string, error) {
	if err := m.record("create:" + name); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.sessions[name] = true
	m.mu.Unlock()
	return name, nil
}

func (m *mockTmuxBackend) AttachSession(name string) error {
	return m.record("attach:" + name)
}

func (m *mockTmuxBackend) DetachSession(name string) error {
	return m.record("detach:" + name)
}

func (m *mockTmuxBackend) KillSession(name string) error {
	if err := m.record("kill:" + name); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, name)
	m.mu.Unlock()
	return nil
}

func (m *mockTmuxBackend) ListSessions() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sessions))
	for s := range m.sessions {
		out = append(out, s)
	}
	return out, nil
}

func (m *mockTmuxBackend) CreateWindow(session, name string) (string, error) {
	if err := m.record("window:" + session + "/" + name); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.windows[session+"/"+name] = true
	m.mu.Unlock()
	return name, nil
}

func (m *mockTmuxBackend) SplitWindow(session, window string) (string, error) {
	if err := m.record("split:" + session + "/" + window); err != nil {
		return "", err
	}
	paneID := session + "/" + window + "/pane-1"
	m.mu.Lock()
	m.panes[paneID] = true
	m.mu.Unlock()
	return paneID, nil
}

func (m *mockTmuxBackend) ResizePane(session, pane string, rows, cols int) error {
	return m.record(fmt.Sprintf("resize:%s/%s/%d/%d", session, pane, rows, cols))
}

func (m *mockTmuxBackend) SendKeys(session, pane string, keys string) error {
	return m.record(fmt.Sprintf("keys:%s/%s/%s", session, pane, keys))
}

func (m *mockTmuxBackend) CapturePane(session, pane string) (string, error) {
	return "captured", m.record("capture:" + session + "/" + pane)
}

func (m *mockTmuxBackend) GetSessionState(session string) ([]byte, error) {
	return []byte("state"), m.record("getstate:" + session)
}

func (m *mockTmuxBackend) RestoreSessionState(session string, state []byte) error {
	return m.record("restore:" + session)
}

// --- 1. Session creation and retrieval ---

func TestManager_CreateAndGet(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	req := &CreateRequest{
		Owner:         "alice",
		Backend:       BackendTmux,
		CPURequest:    500,
		MemoryRequest: 1 << 30,
		Environment:   map[string]string{"TERM": "xterm-256color"},
		Labels:        map[string]string{"project": "helix"},
	}

	s, err := mgr.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if s.Status != StatusRunning {
		t.Fatalf("expected status Running, got %d", s.Status)
	}

	got, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.ID != s.ID {
		t.Fatalf("expected ID %q, got %q", s.ID, got.ID)
	}
	if got.Owner != "alice" {
		t.Fatalf("expected owner alice, got %q", got.Owner)
	}
}

// TestManager_CreateFailure_PairedMutation is the paired mutation gate:
// if tmux creation fails, the session MUST be stored with Failed status.
// A test suite that hides the failure or does not record it is a PASS-bluff.
func TestManager_CreateFailure_PairedMutation(t *testing.T) {
	mock := newMockTmuxBackend()
	mock.failNext = "create"
	mgr := NewManager(mock)

	req := &CreateRequest{Owner: "bob", Backend: BackendTmux}
	s, err := mgr.Create(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error when tmux fails, got nil")
	}
	if s == nil {
		t.Fatalf("expected session record even on failure")
	}
	if s.Status != StatusFailed {
		t.Fatalf("expected status Failed after tmux error, got %d", s.Status)
	}

	// Verify the session is retrievable.
	got, err := mgr.Get(s.ID)
	if err != nil {
		t.Fatalf("failed session must still be retrievable: %v", err)
	}
	if got.Status != StatusFailed {
		t.Fatalf("retrieved session must preserve Failed status")
	}
}

// --- 2. CRDT merge (concurrent updates) ---

func TestCRDT_MergeConcurrentUpdates(t *testing.T) {
	c1 := NewCRDTSessionState()
	c2 := NewCRDTSessionState()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			c1.UpdateWindow(&CRDTWindow{
				ID:   fmt.Sprintf("w-%d", i),
				Name: fmt.Sprintf("window-%d-a", i),
			})
		}(i)
		go func(i int) {
			defer wg.Done()
			c2.UpdateWindow(&CRDTWindow{
				ID:   fmt.Sprintf("w-%d", i),
				Name: fmt.Sprintf("window-%d-b", i),
			})
		}(i)
	}
	wg.Wait()

	// Merge c2 into c1.
	if err := c1.Merge(c2); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	// All 50 windows must converge in c1.
	for i := 0; i < 50; i++ {
		wid := fmt.Sprintf("w-%d", i)
		w, ok := c1.GetWindow(wid)
		if !ok {
			t.Fatalf("window %q missing after merge", wid)
		}
		if w.ID != wid {
			t.Fatalf("window ID mismatch")
		}
		// Name must come from whichever side had the higher version.
		if w.Name == "" {
			t.Fatalf("window name must not be empty after merge")
		}
	}
}

// TestCRDT_MergePairedMutation is the paired mutation gate:
// if Merge silently drops data, this test FAILs.
// A test suite that PASSes after removing Merge logic is a PASS-bluff.
func TestCRDT_MergePairedMutation(t *testing.T) {
	c1 := NewCRDTSessionState()
	c1.UpdateWindow(&CRDTWindow{ID: "w1", Name: "alpha"})

	c2 := NewCRDTSessionState()
	c2.UpdateWindow(&CRDTWindow{ID: "w2", Name: "beta"})

	if err := c1.Merge(c2); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	if _, ok := c1.GetWindow("w2"); !ok {
		t.Fatalf("merge must retain window from other side; w2 missing")
	}

	// Now simulate a broken merge that discards everything.
	broken := NewCRDTSessionState()
	broken.UpdateWindow(&CRDTWindow{ID: "w1", Name: "alpha"})
	// Intentionally do NOT merge c2 — this is the mutation.
	if _, ok := broken.GetWindow("w2"); ok {
		t.Fatalf("broken merge must NOT have w2; mutation test invalid")
	}
}

// --- 3. CRDT serialization round-trip ---

func TestCRDT_SerializationRoundTrip(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(&CRDTWindow{
		ID:     "w1",
		Name:   "editor",
		Layout: "main-vertical",
		Panes: map[string]*CRDTPane{
			"p1": {ID: "p1", Command: "vim", WorkingDir: "/home/alice", Status: PaneRunning},
		},
	})
	c.UpdatePane("w1", &CRDTPane{ID: "p2", Command: "bash", WorkingDir: "/tmp", Status: PaneRunning})

	data, err := c.ToBytes()
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("serialized data must not be empty")
	}

	c2, err := FromBytes(data)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	w, ok := c2.GetWindow("w1")
	if !ok {
		t.Fatalf("window w1 missing after round-trip")
	}
	if w.Name != "editor" {
		t.Fatalf("expected window name editor, got %q", w.Name)
	}
	if w.Layout != "main-vertical" {
		t.Fatalf("expected layout main-vertical, got %q", w.Layout)
	}

	p1, ok := c2.GetPane("w1", "p1")
	if !ok {
		t.Fatalf("pane p1 missing after round-trip")
	}
	if p1.Command != "vim" {
		t.Fatalf("expected command vim, got %q", p1.Command)
	}

	p2, ok := c2.GetPane("w1", "p2")
	if !ok {
		t.Fatalf("pane p2 missing after round-trip")
	}
	if p2.Command != "bash" {
		t.Fatalf("expected command bash, got %q", p2.Command)
	}
}

// TestCRDT_SerializationPairedMutation is the paired mutation gate:
// if ToBytes produces empty data or FromBytes ignores it, this FAILs.
func TestCRDT_SerializationPairedMutation(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(&CRDTWindow{ID: "w1", Name: "mutate-test"})

	data, err := c.ToBytes()
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	// Mutate: corrupt the serialized bytes.
	if len(data) > 0 {
		data[0] ^= 0xFF
	}

	_, err = FromBytes(data)
	if err == nil {
		t.Fatalf("expected deserialization to fail on corrupted data")
	}
}

// --- 4. Migration planner strategy selection ---

func TestMigrationPlanner_PlanSelectsCRDT(t *testing.T) {
	mp := NewMigrationPlanner()
	s := &Session{
		ID:     "sess-1",
		Status: StatusRunning,
		NodeID: "node-a",
	}

	strat, err := mp.Plan(s, "node-b")
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if strat != StrategyCRDT {
		t.Fatalf("expected strategy %q, got %q", StrategyCRDT, strat)
	}
}

// TestMigrationPlanner_PlanPairedMutation is the paired mutation gate:
// if Plan returns a strategy for a nil session, the test FAILs.
func TestMigrationPlanner_PlanPairedMutation(t *testing.T) {
	mp := NewMigrationPlanner()

	_, err := mp.Plan(nil, "node-b")
	if err == nil {
		t.Fatalf("expected error for nil session")
	}

	_, err = mp.Plan(&Session{ID: "s1", NodeID: "n1"}, "")
	if err == nil {
		t.Fatalf("expected error for empty target node")
	}
}

func TestMigrationPlanner_Execute(t *testing.T) {
	mp := NewMigrationPlanner()
	s := &Session{
		ID:     "sess-2",
		Status: StatusRunning,
		NodeID: "node-a",
	}

	result, err := mp.Execute(context.Background(), s, "node-b")
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful migration")
	}
	if result.Method != StrategyCRDT {
		t.Fatalf("expected method %q, got %q", StrategyCRDT, result.Method)
	}
	if result.SourceNode != "node-a" {
		t.Fatalf("expected source node-a, got %q", result.SourceNode)
	}
	if result.TargetNode != "node-b" {
		t.Fatalf("expected target node-b, got %q", result.TargetNode)
	}
}

// --- 5. Manager list/filter operations ---

func TestManager_ListFilterOperations(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	// Seed sessions.
	alice1, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	alice2, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	bob1, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "bob", Backend: BackendTmux})

	// Assign node IDs for filter tests.
	mgr.mu.Lock()
	mgr.sessions[alice1.ID].NodeID = "node-1"
	mgr.sessions[alice2.ID].NodeID = "node-2"
	mgr.sessions[bob1.ID].NodeID = "node-1"
	mgr.mu.Unlock()

	all := mgr.List()
	if len(all) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(all))
	}

	aliceSessions := mgr.ListByOwner("alice")
	if len(aliceSessions) != 2 {
		t.Fatalf("expected 2 alice sessions, got %d", len(aliceSessions))
	}

	bobSessions := mgr.ListByOwner("bob")
	if len(bobSessions) != 1 {
		t.Fatalf("expected 1 bob session, got %d", len(bobSessions))
	}

	node1Sessions := mgr.ListByNode("node-1")
	if len(node1Sessions) != 2 {
		t.Fatalf("expected 2 node-1 sessions, got %d", len(node1Sessions))
	}

	node2Sessions := mgr.ListByNode("node-2")
	if len(node2Sessions) != 1 {
		t.Fatalf("expected 1 node-2 session, got %d", len(node2Sessions))
	}
}

// TestManager_ListFilterPairedMutation is the paired mutation gate:
// if ListByOwner returns sessions for the wrong owner, this FAILs.
func TestManager_ListFilterPairedMutation(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	mgr.Create(context.Background(), &CreateRequest{Owner: "bob", Backend: BackendTmux})

	aliceOnly := mgr.ListByOwner("alice")
	for _, s := range aliceOnly {
		if s.Owner != "alice" {
			t.Fatalf("ListByOwner(alice) returned session for owner %q", s.Owner)
		}
	}

	empty := mgr.ListByOwner("charlie")
	if len(empty) != 0 {
		t.Fatalf("expected 0 sessions for charlie, got %d", len(empty))
	}
}

// --- additional coverage: Attach, Detach, Terminate, Migrate, ResizePTY, SendInput ---

func TestManager_AttachDetachTerminate(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})

	if err := mgr.Attach(context.Background(), s.ID, "alice"); err != nil {
		t.Fatalf("attach failed: %v", err)
	}

	if err := mgr.Detach(context.Background(), s.ID, "alice"); err != nil {
		t.Fatalf("detach failed: %v", err)
	}

	if err := mgr.Terminate(context.Background(), s.ID); err != nil {
		t.Fatalf("terminate failed: %v", err)
	}

	// After termination the session status must be Terminated.
	got, _ := mgr.Get(s.ID)
	if got.Status != StatusTerminated {
		t.Fatalf("expected status Terminated, got %d", got.Status)
	}
}

func TestManager_Migrate(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	mgr.mu.Lock()
	s.NodeID = "node-old"
	mgr.mu.Unlock()

	if err := mgr.Migrate(context.Background(), s.ID, "node-new"); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	got, _ := mgr.Get(s.ID)
	if got.Status != StatusMigrating {
		t.Fatalf("expected status Migrating, got %d", got.Status)
	}
	if got.NodeID != "node-new" {
		t.Fatalf("expected node node-new, got %q", got.NodeID)
	}
}

func TestManager_ResizePTYAndSendInput(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})

	if err := mgr.ResizePTY(context.Background(), s.ID, "pane-1", 24, 80); err != nil {
		t.Fatalf("resize failed: %v", err)
	}

	if err := mgr.SendInput(context.Background(), s.ID, "pane-1", "ls -la"); err != nil {
		t.Fatalf("send input failed: %v", err)
	}
}

func TestManager_GetResourceUsage(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})

	usage, err := mgr.GetResourceUsage(s.ID)
	if err != nil {
		t.Fatalf("get resource usage failed: %v", err)
	}
	if usage == nil {
		t.Fatalf("expected non-nil usage")
	}
	if usage.GPUMemoryBytes == nil {
		t.Fatalf("expected non-nil GPUMemoryBytes map")
	}
}

func TestManager_Get_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	_, err := mgr.Get("nonexistent")
	if err == nil {
		t.Fatalf("expected error for nonexistent session")
	}
}

func TestManager_Attach_NotRunning(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	req := &CreateRequest{Owner: "alice", Backend: BackendTmux}
	s, _ := mgr.Create(context.Background(), req)

	// Force status to non-running.
	mgr.mu.Lock()
	mgr.sessions[s.ID].Status = StatusPaused
	mgr.mu.Unlock()

	err := mgr.Attach(context.Background(), s.ID, "alice")
	if err == nil {
		t.Fatalf("expected error attaching to non-running session")
	}
}

func TestCRDT_GetWindow_NotFound(t *testing.T) {
	c := NewCRDTSessionState()
	_, ok := c.GetWindow("missing")
	if ok {
		t.Fatalf("expected false for missing window")
	}
}

func TestCRDT_GetPane_NotFound(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(&CRDTWindow{ID: "w1", Name: "test"})
	_, ok := c.GetPane("w1", "missing")
	if ok {
		t.Fatalf("expected false for missing pane")
	}
}

func TestCRDT_MergeNil(t *testing.T) {
	c := NewCRDTSessionState()
	if err := c.Merge(nil); err != nil {
		t.Fatalf("merge nil must not error: %v", err)
	}
}

func TestCRDT_UpdateWindowNil(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(nil) // must not panic
	if len(c.ListWindows()) != 0 {
		t.Fatalf("expected no windows after nil update")
	}
}

func TestCRDT_UpdatePaneNil(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdatePane("w1", nil) // must not panic
}

// ListWindows is a test helper not exposed in production.
func (c *CRDTSessionState) ListWindows() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.windows))
	for k := range c.windows {
		out = append(out, k)
	}
	return out
}

func TestManager_Terminate_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	err := mgr.Terminate(context.Background(), "nope")
	if err == nil {
		t.Fatalf("expected error terminating nonexistent session")
	}
}

func TestManager_Migrate_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	err := mgr.Migrate(context.Background(), "nope", "node-b")
	if err == nil {
		t.Fatalf("expected error migrating nonexistent session")
	}
}

func TestManager_ResizePTY_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	err := mgr.ResizePTY(context.Background(), "nope", "p1", 24, 80)
	if err == nil {
		t.Fatalf("expected error resizing nonexistent session")
	}
}

func TestManager_SendInput_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	err := mgr.SendInput(context.Background(), "nope", "p1", "x")
	if err == nil {
		t.Fatalf("expected error sending input to nonexistent session")
	}
}

func TestManager_GetResourceUsage_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	_, err := mgr.GetResourceUsage("nope")
	if err == nil {
		t.Fatalf("expected error for nonexistent session usage")
	}
}

func TestMigrationPlanner_ExecuteNilSession(t *testing.T) {
	mp := NewMigrationPlanner()
	_, err := mp.Execute(context.Background(), nil, "node-b")
	if err == nil {
		t.Fatalf("expected error executing migration for nil session")
	}
}

func TestMigrationPlanner_ExecuteUnplannable(t *testing.T) {
	// Empty planner with no strategies.
	emptyMp := &MigrationPlanner{strategies: make(map[MigrationStrategy]MigrationStrategyImpl)}
	_, err := emptyMp.Execute(context.Background(), &Session{ID: "s", NodeID: "a"}, "b")
	if err == nil {
		t.Fatalf("expected error when no strategy is viable")
	}
}

// Ensure CRDT version monotonicity under concurrent updates.
func TestCRDT_VersionMonotonic(t *testing.T) {
	c := NewCRDTSessionState()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.UpdateWindow(&CRDTWindow{ID: "w", Name: fmt.Sprintf("v-%d", i)})
		}(i)
	}
	wg.Wait()

	w, ok := c.GetWindow("w")
	if !ok {
		t.Fatalf("window must exist")
	}
	if w.Version < 1 {
		t.Fatalf("version must be >= 1 after updates, got %d", w.Version)
	}
}

// TestManager_EventHandlers verifies that registered callbacks fire.
func TestManager_EventHandlers(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	var createdID SessionID
	var migratedFrom, migratedTo string
	var terminatedID SessionID

	mgr.onCreate = func(s *Session) {
		createdID = s.ID
	}
	mgr.onMigrate = func(s *Session, from, to string) {
		migratedFrom = from
		migratedTo = to
	}
	mgr.onTerminate = func(id SessionID) {
		terminatedID = id
	}

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if createdID != s.ID {
		t.Fatalf("onCreate callback did not fire")
	}

	mgr.mu.Lock()
	mgr.sessions[s.ID].NodeID = "node-a"
	mgr.mu.Unlock()
	mgr.Migrate(context.Background(), s.ID, "node-b")
	if migratedFrom != "node-a" || migratedTo != "node-b" {
		t.Fatalf("onMigrate callback did not fire with correct nodes: from=%q to=%q", migratedFrom, migratedTo)
	}

	mgr.Terminate(context.Background(), s.ID)
	if terminatedID != s.ID {
		t.Fatalf("onTerminate callback did not fire")
	}
}

// TestManager_CreateWithoutBackend works when tmux backend is nil.
func TestManager_CreateWithoutBackend(t *testing.T) {
	mgr := NewManager(nil)
	s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err != nil {
		t.Fatalf("create without backend must not error: %v", err)
	}
	if s.Status != StatusRunning {
		t.Fatalf("expected Running without backend, got %d", s.Status)
	}
}

// TestManager_Detach_NotFound verifies detach on missing session errors.
func TestManager_Detach_NotFound(t *testing.T) {
	mgr := NewManager(nil)
	err := mgr.Detach(context.Background(), "nope", "alice")
	if err == nil {
		t.Fatalf("expected error detaching nonexistent session")
	}
}

// TestCRDT_LWWConflictResolution verifies last-write-wins semantics.
func TestCRDT_LWWConflictResolution(t *testing.T) {
	c1 := NewCRDTSessionState()
	c1.UpdateWindow(&CRDTWindow{ID: "w", Name: "first", Version: 1})

	c2 := NewCRDTSessionState()
	c2.UpdateWindow(&CRDTWindow{ID: "w", Name: "second", Version: 2})

	if err := c1.Merge(c2); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	w, ok := c1.GetWindow("w")
	if !ok {
		t.Fatalf("window must exist")
	}
	if w.Name != "second" {
		t.Fatalf("LWW: expected 'second' (version 2), got %q", w.Name)
	}
}

// TestCRDT_PaneLWWConflictResolution verifies LWW at pane granularity.
func TestCRDT_PaneLWWConflictResolution(t *testing.T) {
	c1 := NewCRDTSessionState()
	c1.UpdateWindow(&CRDTWindow{ID: "w", Name: "win", Version: 1})
	c1.UpdatePane("w", &CRDTPane{ID: "p", Command: "cmd-a", Version: 1})

	c2 := NewCRDTSessionState()
	c2.UpdateWindow(&CRDTWindow{ID: "w", Name: "win", Version: 1})
	c2.UpdatePane("w", &CRDTPane{ID: "p", Command: "cmd-b", Version: 2})

	if err := c1.Merge(c2); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	p, ok := c1.GetPane("w", "p")
	if !ok {
		t.Fatalf("pane must exist")
	}
	if p.Command != "cmd-b" {
		t.Fatalf("LWW pane: expected 'cmd-b' (version 2), got %q", p.Command)
	}
}

// TestManager_ListByNode_Empty verifies empty result for unknown node.
func TestManager_ListByNode_Empty(t *testing.T) {
	mgr := NewManager(nil)
	out := mgr.ListByNode("unknown-node")
	if len(out) != 0 {
		t.Fatalf("expected empty list for unknown node")
	}
}

// TestManager_List_Empty verifies empty list on fresh manager.
func TestManager_List_Empty(t *testing.T) {
	mgr := NewManager(nil)
	out := mgr.List()
	if len(out) != 0 {
		t.Fatalf("expected empty list on fresh manager")
	}
}

// TestMigrationPlanner_Plan_NoStrategies verifies error when no strategies registered.
func TestMigrationPlanner_Plan_NoStrategies(t *testing.T) {
	mp := &MigrationPlanner{strategies: make(map[MigrationStrategy]MigrationStrategyImpl)}
	_, err := mp.Plan(&Session{ID: "s", NodeID: "a"}, "b")
	if err == nil {
		t.Fatalf("expected error when no strategies are registered")
	}
}

// TestCRDT_ToBytes_Empty verifies serialization of empty state.
func TestCRDT_ToBytes_Empty(t *testing.T) {
	c := NewCRDTSessionState()
	data, err := c.ToBytes()
	if err != nil {
		t.Fatalf("serialize empty failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("empty state must still produce non-empty bytes")
	}

	c2, err := FromBytes(data)
	if err != nil {
		t.Fatalf("deserialize empty failed: %v", err)
	}
	if len(c2.ListWindows()) != 0 {
		t.Fatalf("deserialized empty state must have no windows")
	}
}

// TestManager_ResizePTY_NotRunning verifies resize on non-running session errors.
func TestManager_ResizePTY_NotRunning(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	mgr.mu.Lock()
	mgr.sessions[s.ID].Status = StatusPaused
	mgr.mu.Unlock()

	err := mgr.ResizePTY(context.Background(), s.ID, "p1", 24, 80)
	if err == nil {
		t.Fatalf("expected error resizing non-running session")
	}
}

// TestManager_SendInput_NotRunning verifies input on non-running session errors.
func TestManager_SendInput_NotRunning(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	mgr.mu.Lock()
	mgr.sessions[s.ID].Status = StatusPaused
	mgr.mu.Unlock()

	err := mgr.SendInput(context.Background(), s.ID, "p1", "x")
	if err == nil {
		t.Fatalf("expected error sending input to non-running session")
	}
}

// TestCRDT_ConcurrentMerge verifies thread-safety of Merge.
func TestCRDT_ConcurrentMerge(t *testing.T) {
	base := NewCRDTSessionState()
	base.UpdateWindow(&CRDTWindow{ID: "base", Name: "base-win"})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			other := NewCRDTSessionState()
			other.UpdateWindow(&CRDTWindow{ID: fmt.Sprintf("w-%d", i), Name: fmt.Sprintf("win-%d", i)})
			_ = base.Merge(other)
		}(i)
	}
	wg.Wait()

	for i := 0; i < 20; i++ {
		wid := fmt.Sprintf("w-%d", i)
		if _, ok := base.GetWindow(wid); !ok {
			t.Fatalf("concurrent merge lost window %q", wid)
		}
	}
}

// TestManager_Create_ContextCancellation is a best-effort check that context
// is accepted (real cancellation wired later).
func TestManager_Create_ContextCancellation(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// For now the manager does not inspect ctx; this test documents the seam.
	_, err := mgr.Create(ctx, &CreateRequest{Owner: "alice", Backend: BackendTmux})
	// Current implementation ignores ctx; we do not assert error here.
	_ = err
}

// TestManager_Migrate_EventFires verifies onMigrate fires even when session
// has no prior NodeID.
func TestManager_Migrate_EventFires(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	var fired bool
	mgr.onMigrate = func(s *Session, from, to string) {
		fired = true
	}

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	mgr.Migrate(context.Background(), s.ID, "node-x")
	if !fired {
		t.Fatalf("onMigrate must fire even when fromNode is empty")
	}
}

// TestCRDT_GetPane_WrongWindow verifies pane lookup fails for wrong window.
func TestCRDT_GetPane_WrongWindow(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(&CRDTWindow{ID: "w1", Name: "win1"})
	c.UpdatePane("w1", &CRDTPane{ID: "p1", Command: "bash"})

	_, ok := c.GetPane("w2", "p1")
	if ok {
		t.Fatalf("expected false for pane in wrong window")
	}
}

// TestManager_Create_DifferentBackends verifies backend is stored.
func TestManager_Create_DifferentBackends(t *testing.T) {
	mgr := NewManager(nil)

	for _, backend := range []SessionBackend{BackendTmux, BackendZellij, BackendScreen} {
		s, err := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: backend})
		if err != nil {
			t.Fatalf("create with backend %q failed: %v", backend, err)
		}
		if s.Backend != backend {
			t.Fatalf("expected backend %q, got %q", backend, s.Backend)
		}
	}
}

// TestManager_SessionStatusTransitions verifies basic status transitions.
func TestManager_SessionStatusTransitions(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if s.Status != StatusRunning {
		t.Fatalf("after create expected Running, got %d", s.Status)
	}

	mgr.Migrate(context.Background(), s.ID, "node-b")
	got, _ := mgr.Get(s.ID)
	if got.Status != StatusMigrating {
		t.Fatalf("after migrate expected Migrating, got %d", got.Status)
	}

	mgr.Terminate(context.Background(), s.ID)
	got, _ = mgr.Get(s.ID)
	if got.Status != StatusTerminated {
		t.Fatalf("after terminate expected Terminated, got %d", got.Status)
	}
}

// TestMigrationPlanner_StrategyCanMigrate verifies each placeholder strategy.
func TestMigrationPlanner_StrategyCanMigrate(t *testing.T) {
	mp := NewMigrationPlanner()
	s := &Session{ID: "s", Status: StatusRunning, NodeID: "a"}

	for strat, impl := range mp.strategies {
		can, reason := impl.CanMigrate(s)
		t.Logf("strategy %q: can=%v reason=%q", strat, can, reason)
		if strat == StrategyCRDT && !can {
			t.Fatalf("crdt strategy must always be available")
		}
		if strat != StrategyCRDT && can {
			t.Fatalf("non-crdt placeholder strategy must report false")
		}
	}
}

// TestCRDT_UpdatePane_CreatesWindow verifies UpdatePane auto-creates window.
func TestCRDT_UpdatePane_CreatesWindow(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdatePane("auto-win", &CRDTPane{ID: "p1", Command: "vim"})

	w, ok := c.GetWindow("auto-win")
	if !ok {
		t.Fatalf("UpdatePane must auto-create window")
	}
	if _, ok := w.Panes["p1"]; !ok {
		t.Fatalf("pane p1 must exist in auto-created window")
	}
}

// TestManager_Terminate_WithoutBackend verifies terminate works with nil backend.
func TestManager_Terminate_WithoutBackend(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})

	if err := mgr.Terminate(context.Background(), s.ID); err != nil {
		t.Fatalf("terminate without backend must not error: %v", err)
	}

	got, _ := mgr.Get(s.ID)
	if got.Status != StatusTerminated {
		t.Fatalf("expected Terminated, got %d", got.Status)
	}
}

// TestCRDT_MergeEmptyIntoEmpty verifies merging two empty states is safe.
func TestCRDT_MergeEmptyIntoEmpty(t *testing.T) {
	c1 := NewCRDTSessionState()
	c2 := NewCRDTSessionState()
	if err := c1.Merge(c2); err != nil {
		t.Fatalf("merge empty into empty failed: %v", err)
	}
	if len(c1.ListWindows()) != 0 {
		t.Fatalf("expected 0 windows after empty merge")
	}
}

// TestManager_UpdatedAt_Progresses verifies UpdatedAt advances on mutation.
func TestManager_UpdatedAt_Progresses(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	before := s.UpdatedAt

	time.Sleep(10 * time.Millisecond)
	mgr.Detach(context.Background(), s.ID, "alice")

	got, _ := mgr.Get(s.ID)
	if !got.UpdatedAt.After(before) {
		t.Fatalf("UpdatedAt must advance after detach")
	}
}

// TestCRDT_VersionAfterMerge verifies global version advances after merge.
func TestCRDT_VersionAfterMerge(t *testing.T) {
	c1 := NewCRDTSessionState()
	c1.UpdateWindow(&CRDTWindow{ID: "w1", Name: "a"})
	v1 := c1.version

	c2 := NewCRDTSessionState()
	c2.UpdateWindow(&CRDTWindow{ID: "w2", Name: "b"})

	_ = c1.Merge(c2)
	if c1.version < v1 {
		t.Fatalf("version must not decrease after merge")
	}
}

// TestManager_List_ReturnsCopies verifies List returns independent copies.
func TestManager_List_ReturnsCopies(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)
	mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})

	list1 := mgr.List()
	if len(list1) != 1 {
		t.Fatalf("expected 1 session")
	}
	list1[0].Owner = "mutated"

	list2 := mgr.List()
	if list2[0].Owner == "mutated" {
		t.Fatalf("List must return copies, not shared references")
	}
}

// TestMigrationResult_Fields verifies result struct fields are populated.
func TestMigrationResult_Fields(t *testing.T) {
	mp := NewMigrationPlanner()
	s := &Session{ID: "s", NodeID: "src"}
	result, err := mp.Execute(context.Background(), s, "dst")
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if result.SourceNode != "src" {
		t.Fatalf("expected source src, got %q", result.SourceNode)
	}
	if result.TargetNode != "dst" {
		t.Fatalf("expected target dst, got %q", result.TargetNode)
	}
	if result.Duration < 0 {
		t.Fatalf("duration must be non-negative")
	}
}

// TestManager_Create_UniqueIDs verifies each created session has a unique ID.
func TestManager_Create_UniqueIDs(t *testing.T) {
	mgr := NewManager(nil)
	ids := make(map[SessionID]bool)
	for i := 0; i < 100; i++ {
		s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
		if ids[s.ID] {
			t.Fatalf("duplicate session ID: %q", s.ID)
		}
		ids[s.ID] = true
	}
}

// TestCRDT_GetWindow_Isolation verifies returned window is a copy.
func TestCRDT_GetWindow_Isolation(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(&CRDTWindow{ID: "w1", Name: "orig", Panes: map[string]*CRDTPane{}})

	w, _ := c.GetWindow("w1")
	w.Name = "mutated"

	w2, _ := c.GetWindow("w1")
	if w2.Name != "orig" {
		t.Fatalf("GetWindow must return isolated copy")
	}
}

// TestCRDT_GetPane_Isolation verifies returned pane is a copy.
func TestCRDT_GetPane_Isolation(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(&CRDTWindow{ID: "w1", Name: "win", Panes: map[string]*CRDTPane{}})
	c.UpdatePane("w1", &CRDTPane{ID: "p1", Command: "orig"})

	p, _ := c.GetPane("w1", "p1")
	p.Command = "mutated"

	p2, _ := c.GetPane("w1", "p1")
	if p2.Command != "orig" {
		t.Fatalf("GetPane must return isolated copy")
	}
}

// TestManager_Attach_WithoutBackend works with nil backend.
func TestManager_Attach_WithoutBackend(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err := mgr.Attach(context.Background(), s.ID, "alice"); err != nil {
		t.Fatalf("attach without backend must not error: %v", err)
	}
}

// TestManager_Detach_WithoutBackend works with nil backend.
func TestManager_Detach_WithoutBackend(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err := mgr.Detach(context.Background(), s.ID, "alice"); err != nil {
		t.Fatalf("detach without backend must not error: %v", err)
	}
}

// TestManager_ResizePTY_WithoutBackend works with nil backend.
func TestManager_ResizePTY_WithoutBackend(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err := mgr.ResizePTY(context.Background(), s.ID, "p1", 24, 80); err != nil {
		t.Fatalf("resize without backend must not error: %v", err)
	}
}

// TestManager_SendInput_WithoutBackend works with nil backend.
func TestManager_SendInput_WithoutBackend(t *testing.T) {
	mgr := NewManager(nil)
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	if err := mgr.SendInput(context.Background(), s.ID, "p1", "ls"); err != nil {
		t.Fatalf("send input without backend must not error: %v", err)
	}
}

// TestMigrationPlanner_CRIUStrategy_NotImplemented verifies placeholder errors.
func TestMigrationPlanner_CRIUStrategy_NotImplemented(t *testing.T) {
	criu := &criuStrategy{}
	can, _ := criu.CanMigrate(&Session{ID: "s"})
	if can {
		t.Fatalf("criu placeholder must report cannot migrate")
	}
	if err := criu.PreMigrate(context.Background(), &Session{ID: "s"}, "n"); err == nil {
		t.Fatalf("criu pre-migrate must error")
	}
	if _, err := criu.Migrate(context.Background(), &Session{ID: "s"}, "n"); err == nil {
		t.Fatalf("criu migrate must error")
	}
	if err := criu.PostMigrate(context.Background(), &Session{ID: "s"}, "n"); err == nil {
		t.Fatalf("criu post-migrate must error")
	}
}

// TestMigrationPlanner_DMTCPStrategy_NotImplemented verifies placeholder errors.
func TestMigrationPlanner_DMTCPStrategy_NotImplemented(t *testing.T) {
	dmtcp := &dmtcpStrategy{}
	can, _ := dmtcp.CanMigrate(&Session{ID: "s"})
	if can {
		t.Fatalf("dmtcp placeholder must report cannot migrate")
	}
}

// TestMigrationPlanner_ContainerStrategy_NotImplemented verifies placeholder errors.
func TestMigrationPlanner_ContainerStrategy_NotImplemented(t *testing.T) {
	cs := &containerStrategy{}
	can, _ := cs.CanMigrate(&Session{ID: "s"})
	if can {
		t.Fatalf("container placeholder must report cannot migrate")
	}
}

// TestManager_CreateRequest_GPURequest verifies GPURequest is preserved.
func TestManager_CreateRequest_GPURequest(t *testing.T) {
	mgr := NewManager(nil)
	req := &CreateRequest{
		Owner:      "alice",
		Backend:    BackendTmux,
		GPURequest: []string{"gpu-0", "gpu-1"},
	}
	s, _ := mgr.Create(context.Background(), req)
	if len(s.GPURequest) != 2 || s.GPURequest[0] != "gpu-0" || s.GPURequest[1] != "gpu-1" {
		t.Fatalf("GPURequest not preserved: %v", s.GPURequest)
	}
}

// TestManager_EnvironmentLabels verifies environment and labels are preserved.
func TestManager_EnvironmentLabels(t *testing.T) {
	mgr := NewManager(nil)
	req := &CreateRequest{
		Owner:       "alice",
		Backend:     BackendTmux,
		Environment: map[string]string{"KEY": "VALUE"},
		Labels:      map[string]string{"app": "test"},
	}
	s, _ := mgr.Create(context.Background(), req)
	if s.Environment["KEY"] != "VALUE" {
		t.Fatalf("environment not preserved")
	}
	if s.Labels["app"] != "test" {
		t.Fatalf("labels not preserved")
	}
}

// TestCRDT_UpdatePane_OverwriteExisting verifies pane update overwrites.
func TestCRDT_UpdatePane_OverwriteExisting(t *testing.T) {
	c := NewCRDTSessionState()
	c.UpdateWindow(&CRDTWindow{ID: "w", Name: "win", Panes: map[string]*CRDTPane{}})
	c.UpdatePane("w", &CRDTPane{ID: "p", Command: "first"})
	c.UpdatePane("w", &CRDTPane{ID: "p", Command: "second"})

	p, _ := c.GetPane("w", "p")
	if p.Command != "second" {
		t.Fatalf("expected pane command 'second', got %q", p.Command)
	}
}

// TestManager_MockCalls verifies the mock backend received expected calls.
func TestManager_MockCalls(t *testing.T) {
	mock := newMockTmuxBackend()
	mgr := NewManager(mock)

	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	mgr.Attach(context.Background(), s.ID, "alice")
	mgr.Detach(context.Background(), s.ID, "alice")
	mgr.Terminate(context.Background(), s.ID)

	mock.mu.Lock()
	calls := make([]string, len(mock.calls))
	copy(calls, mock.calls)
	mock.mu.Unlock()

	if len(calls) < 4 {
		t.Fatalf("expected at least 4 mock calls, got %d: %v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "create:") {
		t.Fatalf("first call must be create, got %q", calls[0])
	}
	if !strings.HasPrefix(calls[1], "attach:") {
		t.Fatalf("second call must be attach, got %q", calls[1])
	}
	if !strings.HasPrefix(calls[2], "detach:") {
		t.Fatalf("third call must be detach, got %q", calls[2])
	}
	if !strings.HasPrefix(calls[3], "kill:") {
		t.Fatalf("fourth call must be kill, got %q", calls[3])
	}
}

// TestManager_Create_BackendFailure_RecordsSession verifies session is stored
// even when backend fails.
func TestManager_Create_BackendFailure_RecordsSession(t *testing.T) {
	mock := newMockTmuxBackend()
	mock.failNext = "create"
	mgr := NewManager(mock)

	req := &CreateRequest{Owner: "alice", Backend: BackendTmux}
	s, err := mgr.Create(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error")
	}
	if s == nil {
		t.Fatalf("session must be recorded even on backend failure")
	}

	all := mgr.List()
	found := false
	for _, x := range all {
		if x.ID == s.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("failed session must appear in List")
	}
}

// TestCRDT_Merge_DifferentWindowVersions verifies merge picks higher version.
func TestCRDT_Merge_DifferentWindowVersions(t *testing.T) {
	c1 := NewCRDTSessionState()
	c1.UpdateWindow(&CRDTWindow{ID: "w", Name: "low", Version: 1})

	c2 := NewCRDTSessionState()
	c2.UpdateWindow(&CRDTWindow{ID: "w", Name: "high", Version: 10})

	_ = c1.Merge(c2)
	w, _ := c1.GetWindow("w")
	if w.Name != "high" {
		t.Fatalf("expected 'high' (version 10), got %q", w.Name)
	}
}

// TestManager_CreatedAt_Set verifies CreatedAt is populated.
func TestManager_CreatedAt_Set(t *testing.T) {
	mgr := NewManager(nil)
	before := time.Now()
	s, _ := mgr.Create(context.Background(), &CreateRequest{Owner: "alice", Backend: BackendTmux})
	after := time.Now()

	if s.CreatedAt.Before(before) || s.CreatedAt.After(after) {
		t.Fatalf("CreatedAt out of range: %v", s.CreatedAt)
	}
}

// TestCRDT_Merge_SameVersion_MergesPanes verifies pane merge when window versions match.
func TestCRDT_Merge_SameVersion_MergesPanes(t *testing.T) {
	c1 := NewCRDTSessionState()
	c1.UpdateWindow(&CRDTWindow{ID: "w", Name: "win", Version: 5})
	c1.UpdatePane("w", &CRDTPane{ID: "p1", Command: "a", Version: 1})

	c2 := NewCRDTSessionState()
	c2.UpdateWindow(&CRDTWindow{ID: "w", Name: "win", Version: 5})
	c2.UpdatePane("w", &CRDTPane{ID: "p2", Command: "b", Version: 1})

	_ = c1.Merge(c2)
	if _, ok := c1.GetPane("w", "p1"); !ok {
		t.Fatalf("p1 must be preserved")
	}
	if _, ok := c1.GetPane("w", "p2"); !ok {
		t.Fatalf("p2 must be merged in")
	}
}
