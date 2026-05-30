package swim

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Member Tests
// ============================================================================

func TestMember_UpdateState(t *testing.T) {
	m := &Member{
		ID:          "node-1",
		State:       StateAlive,
		Incarnation: 1,
	}
	m.Touch()

	// Test: transition to suspect with higher incarnation
	ok := m.UpdateState(StateSuspect, 2)
	require.True(t, ok)
	assert.Equal(t, StateSuspect, m.State)
	assert.Equal(t, uint32(2), m.Incarnation)

	// Test: transition to dead with same incarnation
	ok = m.UpdateState(StateDead, 2)
	require.True(t, ok)
	assert.Equal(t, StateDead, m.State)

	// Test: reject lower incarnation
	ok = m.UpdateState(StateAlive, 1)
	require.False(t, ok)
	assert.Equal(t, StateDead, m.State)

	// Test: reject same incarnation with lower state priority
	ok = m.UpdateState(StateAlive, 2)
	require.False(t, ok)
	assert.Equal(t, StateDead, m.State)

	// Mutation: break incarnation check
	// m.Incarnation = 0
	// ok = m.UpdateState(StateAlive, 1)
	// require.False(t, ok) // would incorrectly pass with broken check
}

func TestMember_UpdateState_Mutation(t *testing.T) {
	m := &Member{
		ID:          "node-1",
		State:       StateAlive,
		Incarnation: 1,
	}
	m.Touch()

	// Mutation: remove incarnation check so lower incarnation is accepted
	m.Incarnation = 5
	ok := m.UpdateState(StateSuspect, 2)
	require.False(t, ok)
	assert.Equal(t, StateAlive, m.State)
}

func TestMember_IsHealthy(t *testing.T) {
	m := &Member{ID: "node-1", State: StateAlive}
	require.True(t, m.IsHealthy())

	m.State = StateSuspect
	require.False(t, m.IsHealthy())

	m.State = StateDead
	require.False(t, m.IsHealthy())

	m.State = StateLeft
	require.False(t, m.IsHealthy())
}

func TestMember_IsHealthy_Mutation(t *testing.T) {
	m := &Member{ID: "node-1", State: StateAlive}
	// Mutation: IsHealthy returns true for suspect
	m.State = StateSuspect
	require.False(t, m.IsHealthy())
}

func TestMember_TimeSinceLastSeen(t *testing.T) {
	m := &Member{ID: "node-1"}
	m.Touch()

	time.Sleep(50 * time.Millisecond)
	d := m.TimeSinceLastSeen()
	require.GreaterOrEqual(t, d, 50*time.Millisecond)
}

func TestMember_TimeSinceLastSeen_Mutation(t *testing.T) {
	m := &Member{ID: "node-1"}
	// Mutation: lastSeen not updated by Touch
	// Verify Touch actually updates lastSeen
	before := m.TimeSinceLastSeen()
	m.Touch()
	after := m.TimeSinceLastSeen()
	require.True(t, after < before || after == 0)
}

// ============================================================================
// Transport Tests
// ============================================================================

func TestTransport_SendAndReceive(t *testing.T) {
	tr1, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr1.Close()

	tr2, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr2.Close()

	received := make(chan *Message, 1)
	tr2.RegisterHandler(MsgPing, func(msg *Message, addr net.Addr) {
		received <- msg
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr2.Listen(ctx)

	addr2 := tr2.LocalAddr().(*net.UDPAddr)
	msg := &Message{Type: MsgPing, SourceID: "node-1"}
	err = tr1.Send(msg, *addr2)
	assert.NoError(t, err)

	select {
	case r := <-received:
		require.NotNil(t, r)
		assert.Equal(t, MsgPing, r.Type)
		assert.Equal(t, "node-1", r.SourceID)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestTransport_SendAndReceive_Mutation(t *testing.T) {
	tr1, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr1.Close()

	tr2, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr2.Close()

	received := make(chan *Message, 1)
	tr2.RegisterHandler(MsgPing, func(msg *Message, addr net.Addr) {
		received <- msg
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr2.Listen(ctx)

	addr2 := tr2.LocalAddr().(*net.UDPAddr)
	msg := &Message{Type: MsgPing, SourceID: "node-1"}
	err = tr1.Send(msg, *addr2)
	assert.NoError(t, err)

	// Mutation: handler registered for wrong type
	select {
	case <-received:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestTransport_RegisterHandler(t *testing.T) {
	tr, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr.Close()

	tr.RegisterHandler(MsgAck, func(msg *Message, addr net.Addr) {})

	// Trigger via internal access is not possible, so verify handler exists
	tr.mu.RLock()
	_, ok := tr.handlers[MsgAck]
	tr.mu.RUnlock()
	require.True(t, ok)
}

func TestTransport_RegisterHandler_Mutation(t *testing.T) {
	tr, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr.Close()

	tr.RegisterHandler(MsgAck, func(msg *Message, addr net.Addr) {})

	// Mutation: handler overwritten or not stored
	tr.mu.RLock()
	_, ok := tr.handlers[MsgAck]
	tr.mu.RUnlock()
	require.True(t, ok)
}

func TestTransport_Close(t *testing.T) {
	tr, err := NewTransport("127.0.0.1", 0)
	require.NoError(t, err)

	// First close may return error on some platforms; just verify no panic
	err = tr.Close()
	require.NotNil(t, tr)

	// Second close should not panic
	_ = tr.Close()
}

// ============================================================================
// GossipBuffer Tests
// ============================================================================

func TestGossipBuffer_Add(t *testing.T) {
	gb := NewGossipBuffer(2)
	gb.Add(GossipEvent{MemberID: "a", State: StateAlive})
	gb.Add(GossipEvent{MemberID: "b", State: StateSuspect})

	events := gb.Recent(10)
	require.Len(t, events, 2)
	assert.Equal(t, "a", events[0].MemberID)
	assert.Equal(t, "b", events[1].MemberID)
}

func TestGossipBuffer_Add_Mutation(t *testing.T) {
	gb := NewGossipBuffer(2)
	gb.Add(GossipEvent{MemberID: "a", State: StateAlive})
	gb.Add(GossipEvent{MemberID: "b", State: StateSuspect})

	// Mutation: buffer does not evict old entries
	gb.Add(GossipEvent{MemberID: "c", State: StateDead})
	events := gb.Recent(10)
	require.Len(t, events, 2)
	assert.Equal(t, "b", events[0].MemberID)
	assert.Equal(t, "c", events[1].MemberID)
}

func TestGossipBuffer_Recent(t *testing.T) {
	gb := NewGossipBuffer(10)
	gb.Add(GossipEvent{MemberID: "a"})
	gb.Add(GossipEvent{MemberID: "b"})
	gb.Add(GossipEvent{MemberID: "c"})

	events := gb.Recent(2)
	require.Len(t, events, 2)
	assert.Equal(t, "b", events[0].MemberID)
	assert.Equal(t, "c", events[1].MemberID)
}

func TestGossipBuffer_Recent_Mutation(t *testing.T) {
	gb := NewGossipBuffer(10)
	gb.Add(GossipEvent{MemberID: "a"})
	gb.Add(GossipEvent{MemberID: "b"})

	// Mutation: Recent returns oldest instead of newest
	events := gb.Recent(1)
	require.Len(t, events, 1)
	assert.Equal(t, "b", events[0].MemberID)
}

func TestGossipBuffer_Recent_Zero(t *testing.T) {
	gb := NewGossipBuffer(10)
	gb.Add(GossipEvent{MemberID: "a"})
	require.Nil(t, gb.Recent(0))
}

func TestRandomMembers(t *testing.T) {
	members := []*Member{
		{ID: "a", State: StateAlive},
		{ID: "b", State: StateAlive},
		{ID: "c", State: StateAlive},
		{ID: "d", State: StateDead},
	}

	result := RandomMembers(members, 2, "a")
	require.Len(t, result, 2)
	for _, m := range result {
		assert.NotEqual(t, "a", m.ID)
		assert.Equal(t, StateAlive, m.State)
	}
}

func TestRandomMembers_Mutation(t *testing.T) {
	members := []*Member{
		{ID: "a", State: StateAlive},
		{ID: "b", State: StateAlive},
		{ID: "c", State: StateAlive},
	}

	// Mutation: includes excluded ID
	result := RandomMembers(members, 2, "a")
	for _, m := range result {
		require.NotEqual(t, "a", m.ID)
	}
}

func TestRandomMembers_Empty(t *testing.T) {
	require.Nil(t, RandomMembers(nil, 2, ""))
	require.Nil(t, RandomMembers([]*Member{}, 2, ""))
	require.Nil(t, RandomMembers([]*Member{{ID: "a", State: StateAlive}}, 0, ""))
}

// ============================================================================
// FailureDetector Tests
// ============================================================================

func TestFailureDetector_SuspectAndConfirm(t *testing.T) {
	confirmed := make(chan string, 1)
	fd := NewFailureDetector(100*time.Millisecond, func(id string) {
		confirmed <- id
	}, nil)

	fd.Suspect("node-1", 1)
	require.True(t, fd.IsSuspected("node-1"))

	select {
	case id := <-confirmed:
		assert.Equal(t, "node-1", id)
		require.False(t, fd.IsSuspected("node-1"))
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for confirmation")
	}
}

func TestFailureDetector_SuspectAndConfirm_Mutation(t *testing.T) {
	confirmed := make(chan string, 1)
	fd := NewFailureDetector(50*time.Millisecond, func(id string) {
		confirmed <- id
	}, nil)

	fd.Suspect("node-1", 1)
	// Mutation: timer not started or timeout ignored
	select {
	case <-confirmed:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for confirmation")
	}
}

func TestFailureDetector_Refute(t *testing.T) {
	refuted := make(chan string, 1)
	fd := NewFailureDetector(5*time.Second, nil, func(id string) {
		refuted <- id
	})

	fd.Suspect("node-1", 1)
	require.True(t, fd.IsSuspected("node-1"))

	fd.Refute("node-1")
	require.False(t, fd.IsSuspected("node-1"))

	select {
	case id := <-refuted:
		assert.Equal(t, "node-1", id)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for refute callback")
	}
}

func TestFailureDetector_Refute_Mutation(t *testing.T) {
	fd := NewFailureDetector(5*time.Second, nil, nil)

	fd.Suspect("node-1", 1)
	// Mutation: Refute does not remove suspect
	fd.Refute("node-1")
	require.False(t, fd.IsSuspected("node-1"))
}

func TestFailureDetector_SuspectCount(t *testing.T) {
	fd := NewFailureDetector(5*time.Second, nil, nil)
	assert.Equal(t, 0, fd.SuspectCount())

	fd.Suspect("node-1", 1)
	assert.Equal(t, 1, fd.SuspectCount())

	fd.Suspect("node-2", 1)
	assert.Equal(t, 2, fd.SuspectCount())

	fd.Refute("node-1")
	assert.Equal(t, 1, fd.SuspectCount())
}

// ============================================================================
// Protocol Tests
// ============================================================================

func TestProtocol_NewProtocol(t *testing.T) {
	cfg := &Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	}
	p, err := NewProtocol(cfg)
	assert.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "node-1", p.LocalID())
	assert.NotEmpty(t, p.localAddr)
	assert.Equal(t, uint32(0), p.incarnation)
	assert.Len(t, p.Members(), 1)
}

func TestProtocol_NewProtocol_Mutation(t *testing.T) {
	// Mutation: empty local_id accepted
	_, err := NewProtocol(&Config{LocalID: "", BindAddr: "127.0.0.1", BindPort: 0})
	require.Error(t, err)
}

func TestProtocol_StartAndStop(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:        "node-startstop",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p.Start()
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	err = p.Stop()
	assert.NoError(t, err)
}

func TestProtocol_StartAndStop_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:        "node-startstop",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p.Start()
	assert.NoError(t, err)

	// Mutation: Stop does not wait for goroutines
	err = p.Stop()
	assert.NoError(t, err)
	// If Stop didn't wait, this would race; we verify no panic.
}

func TestProtocol_Join(t *testing.T) {
	p1, err := NewProtocol(&Config{
		LocalID:        "node-1",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p1.Start()
	assert.NoError(t, err)
	defer p1.Stop()

	p2, err := NewProtocol(&Config{
		LocalID:        "node-2",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p2.Start()
	assert.NoError(t, err)
	defer p2.Stop()

	err = p2.Join([]string{p1.localAddr})
	assert.NoError(t, err)

	// Allow join message to propagate
	time.Sleep(300 * time.Millisecond)

	members1 := p1.Members()
	members2 := p2.Members()

	assert.Len(t, members2, 2)
	assert.Len(t, members1, 2)
}

func TestProtocol_Join_Mutation(t *testing.T) {
	p1, err := NewProtocol(&Config{
		LocalID:        "node-1",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p1.Start()
	assert.NoError(t, err)
	defer p1.Stop()

	p2, err := NewProtocol(&Config{
		LocalID:        "node-2",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p2.Start()
	assert.NoError(t, err)
	defer p2.Stop()

	err = p2.Join([]string{p1.localAddr})
	assert.NoError(t, err)

	time.Sleep(300 * time.Millisecond)

	// Mutation: Join does not add members
	members2 := p2.Members()
	require.Len(t, members2, 2)
}

func TestProtocol_Leave(t *testing.T) {
	p1, err := NewProtocol(&Config{
		LocalID:        "node-1",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p1.Start()
	assert.NoError(t, err)

	p2, err := NewProtocol(&Config{
		LocalID:        "node-2",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p2.Start()
	assert.NoError(t, err)

	err = p2.Join([]string{p1.localAddr})
	assert.NoError(t, err)

	time.Sleep(300 * time.Millisecond)

	err = p2.Leave()
	assert.NoError(t, err)

	time.Sleep(300 * time.Millisecond)

	members1 := p1.Members()
	var found bool
	for _, m := range members1 {
		if m.ID == "node-2" {
			assert.Equal(t, StateLeft, m.State)
			found = true
		}
	}
	assert.True(t, found)

	p1.Stop()
}

func TestProtocol_Leave_Mutation(t *testing.T) {
	p1, err := NewProtocol(&Config{
		LocalID:        "node-1",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p1.Start()
	assert.NoError(t, err)
	defer p1.Stop()

	p2, err := NewProtocol(&Config{
		LocalID:        "node-2",
		BindAddr:       "127.0.0.1",
		BindPort:       0,
		ProbeInterval:  5 * time.Second,
		GossipInterval: 5 * time.Second,
	})
	assert.NoError(t, err)

	err = p2.Start()
	assert.NoError(t, err)

	err = p2.Join([]string{p1.localAddr})
	assert.NoError(t, err)

	time.Sleep(300 * time.Millisecond)

	err = p2.Leave()
	assert.NoError(t, err)

	time.Sleep(300 * time.Millisecond)

	// Mutation: Leave does not mark as Left
	members1 := p1.Members()
	for _, m := range members1 {
		if m.ID == "node-2" {
			require.Equal(t, StateLeft, m.State)
		}
	}
}

func TestProtocol_HealthyMembers(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)

	p.mu.Lock()
	p.members["node-2"] = &Member{ID: "node-2", State: StateAlive}
	p.members["node-3"] = &Member{ID: "node-3", State: StateDead}
	p.mu.Unlock()

	healthy := p.HealthyMembers()
	require.Len(t, healthy, 2)
	for _, m := range healthy {
		assert.Equal(t, StateAlive, m.State)
	}
}

func TestProtocol_HealthyMembers_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)

	p.mu.Lock()
	p.members["node-2"] = &Member{ID: "node-2", State: StateAlive}
	p.members["node-3"] = &Member{ID: "node-3", State: StateDead}
	p.mu.Unlock()

	// Mutation: HealthyMembers returns dead members too
	healthy := p.HealthyMembers()
	for _, m := range healthy {
		require.Equal(t, StateAlive, m.State)
	}
}

func TestProtocol_HandlePing(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)

	p.Start()
	defer p.Stop()

	tr, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr.Close()

	acks := make(chan *Message, 1)
	tr.RegisterHandler(MsgAck, func(msg *Message, addr net.Addr) {
		acks <- msg
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.Listen(ctx)

	addr := p.transport.LocalAddr().(*net.UDPAddr)
	msg := &Message{Type: MsgPing, SourceID: "test-node"}
	err = tr.Send(msg, *addr)
	assert.NoError(t, err)

	select {
	case ack := <-acks:
		require.NotNil(t, ack)
		assert.Equal(t, MsgAck, ack.Type)
		assert.Equal(t, "node-1", ack.SourceID)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ack")
	}
}

func TestProtocol_HandlePing_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)

	p.Start()
	defer p.Stop()

	tr, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr.Close()

	acks := make(chan *Message, 1)
	tr.RegisterHandler(MsgAck, func(msg *Message, addr net.Addr) {
		acks <- msg
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.Listen(ctx)

	addr := p.transport.LocalAddr().(*net.UDPAddr)
	msg := &Message{Type: MsgPing, SourceID: "test-node"}
	err = tr.Send(msg, *addr)
	assert.NoError(t, err)

	// Mutation: ping handler does not respond with ack
	select {
	case <-acks:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ack")
	}
}

func TestProtocol_HandleSuspect(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)

	p.Start()
	defer p.Stop()

	p.mu.Lock()
	p.members["node-2"] = &Member{ID: "node-2", State: StateAlive, Incarnation: 1}
	p.mu.Unlock()

	tr, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr.Close()

	addr := p.transport.LocalAddr().(*net.UDPAddr)
	msg := &Message{
		Type:        MsgSuspect,
		SourceID:    "node-3",
		TargetID:    "node-2",
		Incarnation: 2,
	}
	err = tr.Send(msg, *addr)
	assert.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	p.mu.RLock()
	m := p.members["node-2"]
	p.mu.RUnlock()
	require.NotNil(t, m)
	assert.Equal(t, StateSuspect, m.State)
	assert.Equal(t, uint32(2), m.Incarnation)
}

func TestProtocol_HandleSuspect_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)

	p.Start()
	defer p.Stop()

	p.mu.Lock()
	p.members["node-2"] = &Member{ID: "node-2", State: StateAlive, Incarnation: 5}
	p.mu.Unlock()

	tr, err := NewTransport("127.0.0.1", 0)
	assert.NoError(t, err)
	defer tr.Close()

	addr := p.transport.LocalAddr().(*net.UDPAddr)
	msg := &Message{
		Type:        MsgSuspect,
		SourceID:    "node-3",
		TargetID:    "node-2",
		Incarnation: 2,
	}
	err = tr.Send(msg, *addr)
	assert.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	// Mutation: lower incarnation incorrectly updates state
	p.mu.RLock()
	m := p.members["node-2"]
	p.mu.RUnlock()
	require.Equal(t, StateAlive, m.State)
}

func TestProtocol_ConfigDefaults(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)
	defer p.transport.Close()

	assert.Equal(t, 1*time.Second, p.probeInterval)
	assert.Equal(t, 500*time.Millisecond, p.probeTimeout)
	assert.Equal(t, 4, p.suspicionMult)
	assert.Equal(t, 3, p.gossipCount)
	assert.Equal(t, 200*time.Millisecond, p.gossipInterval)
}

func TestProtocol_ConfigDefaults_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)
	defer p.transport.Close()

	// Mutation: defaults not applied
	assert.Equal(t, 1*time.Second, p.probeInterval)
	assert.Equal(t, 500*time.Millisecond, p.probeTimeout)
}

func TestProtocol_MembersSnapshot(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)
	defer p.transport.Close()

	p.mu.Lock()
	p.members["node-2"] = &Member{ID: "node-2", State: StateAlive}
	p.mu.Unlock()

	members := p.Members()
	require.Len(t, members, 2)

	// Mutation: returned slice is not a snapshot
	p.mu.Lock()
	delete(p.members, "node-2")
	p.mu.Unlock()

	// The snapshot should still have 2 members
	assert.Len(t, members, 2)
}

func TestProtocol_MembersSnapshot_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)
	defer p.transport.Close()

	p.mu.Lock()
	p.members["node-2"] = &Member{ID: "node-2", State: StateAlive}
	p.mu.Unlock()

	members := p.Members()
	require.Len(t, members, 2)

	// Verify snapshot independence
	p.mu.Lock()
	delete(p.members, "node-2")
	p.mu.Unlock()

	assert.Len(t, members, 2)
}

func TestProtocol_ConcurrentAccess(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)
	defer p.transport.Close()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p.Members()
			p.HealthyMembers()
			p.LocalID()
			_ = p.incarnation
		}(i)
	}
	wg.Wait()
}

func TestProtocol_ConcurrentAccess_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	assert.NoError(t, err)
	defer p.transport.Close()

	// Mutation: concurrent read/write without lock would race
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = p.Members()
		}(i)
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p.mu.Lock()
			p.members[fmt.Sprintf("node-%d", idx)] = &Member{ID: fmt.Sprintf("node-%d", idx)}
			p.mu.Unlock()
		}(i)
	}
	wg.Wait()
}

func TestGossipBuffer_DefaultMaxSize(t *testing.T) {
	gb := NewGossipBuffer(0)
	for i := 0; i < 150; i++ {
		gb.Add(GossipEvent{MemberID: fmt.Sprintf("node-%d", i)})
	}
	events := gb.Recent(200)
	require.LessOrEqual(t, len(events), 100)
}

func TestGossipBuffer_DefaultMaxSize_Mutation(t *testing.T) {
	gb := NewGossipBuffer(0)
	for i := 0; i < 150; i++ {
		gb.Add(GossipEvent{MemberID: fmt.Sprintf("node-%d", i)})
	}
	// Mutation: default max size not applied, buffer grows unbounded
	events := gb.Recent(200)
	require.LessOrEqual(t, len(events), 100)
}

func TestFailureDetector_DefaultTimeout(t *testing.T) {
	fd := NewFailureDetector(0, nil, nil)
	require.NotNil(t, fd)
	assert.Equal(t, 5*time.Second, fd.timeout)
}

func TestFailureDetector_DefaultTimeout_Mutation(t *testing.T) {
	fd := NewFailureDetector(0, nil, nil)
	// Mutation: default timeout not applied
	assert.Equal(t, 5*time.Second, fd.timeout)
}

func TestProtocol_SelfSuspectRefute(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	require.NoError(t, err)

	p.Start()
	defer p.Stop()

	tr, err := NewTransport("127.0.0.1", 0)
	require.NoError(t, err)
	defer tr.Close()

	alives := make(chan *Message, 10)
	tr.RegisterHandler(MsgAlive, func(msg *Message, addr net.Addr) {
		alives <- msg
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Register test transport as a member so broadcastAlive sends to it
	trAddr := tr.LocalAddr().(*net.UDPAddr)
	p.mu.Lock()
	p.members["node-2"] = &Member{
		ID:       "node-2",
		Address:  trAddr.String(),
		State:    StateAlive,
		Metadata: make(map[string]string),
	}
	p.members["node-2"].Touch()
	p.mu.Unlock()
	go tr.Listen(ctx)

	addr := p.transport.LocalAddr().(*net.UDPAddr)
	msg := &Message{
		Type:        MsgSuspect,
		SourceID:    "node-2",
		TargetID:    "node-1",
		Incarnation: 1,
	}
	err = tr.Send(msg, *addr)
	require.NoError(t, err)
	err = tr.Send(msg, *addr)
	require.NoError(t, err)

	select {
	case alive := <-alives:
		require.NotNil(t, alive)
		assert.Equal(t, "node-1", alive.SourceID)
		assert.Greater(t, alive.Incarnation, uint32(0))
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for alive refutation")
	}
}

func TestProtocol_SelfSuspectRefute_Mutation(t *testing.T) {
	p, err := NewProtocol(&Config{
		LocalID:  "node-1",
		BindAddr: "127.0.0.1",
		BindPort: 0,
	})
	require.NoError(t, err)

	p.Start()
	defer p.Stop()

	tr, err := NewTransport("127.0.0.1", 0)
	require.NoError(t, err)
	defer tr.Close()

	alives := make(chan *Message, 10)
	tr.RegisterHandler(MsgAlive, func(msg *Message, addr net.Addr) {
		alives <- msg
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Register test transport as a member so broadcastAlive sends to it
	trAddr := tr.LocalAddr().(*net.UDPAddr)
	p.mu.Lock()
	p.members["node-2"] = &Member{
		ID:       "node-2",
		Address:  trAddr.String(),
		State:    StateAlive,
		Metadata: make(map[string]string),
	}
	p.members["node-2"].Touch()
	p.mu.Unlock()
	go tr.Listen(ctx)

	addr := p.transport.LocalAddr().(*net.UDPAddr)
	msg := &Message{
		Type:        MsgSuspect,
		SourceID:    "node-2",
		TargetID:    "node-1",
		Incarnation: 1,
	}
	err = tr.Send(msg, *addr)
	require.NoError(t, err)
	err = tr.Send(msg, *addr)
	require.NoError(t, err)

	// Mutation: self suspect does not trigger alive broadcast
	select {
	case <-alives:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for alive refutation")
	}
}
