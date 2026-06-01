package swim

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"
)

// MessageType defines SWIM message types.
type MessageType byte

const (
	MsgPing MessageType = iota
	MsgPingReq
	MsgAck
	MsgSuspect
	MsgAlive
	MsgDead
	MsgJoin
	MsgLeave
)

// Message is the wire format for SWIM gossip.
type Message struct {
	Type        MessageType
	SourceID    string
	TargetID    string
	Incarnation uint32
	Payload     []byte
}

// Protocol implements the SWIM gossip protocol.
type Protocol struct {
	mu        sync.RWMutex
	localID   string
	localAddr string
	members   map[string]*Member
	transport MessageTransport
	ctx       context.Context
	cancel    context.CancelFunc

	// Config
	probeInterval  time.Duration
	probeTimeout   time.Duration
	suspicionMult  int
	gossipCount    int
	gossipInterval time.Duration

	// State
	incarnation  uint32
	shutdown     chan struct{}
	wg           sync.WaitGroup
	gossipBuffer *GossipBuffer
	fd           *FailureDetector
}

// Config holds SWIM protocol configuration.
type Config struct {
	LocalID        string
	BindAddr       string
	BindPort       int
	ProbeInterval  time.Duration // default 1s
	ProbeTimeout   time.Duration // default 500ms
	SuspicionMult  int           // default 4
	GossipCount    int           // default 3
	GossipInterval time.Duration // default 200ms

	// Transport is an optional pre-built MessageTransport. When non-nil it is
	// used directly and BindAddr/BindPort are not used to open a UDP socket.
	// When nil (the default), NewProtocol creates a real UDP *Transport bound
	// to BindAddr:BindPort — preserving the existing production behaviour.
	Transport MessageTransport
}

// NewProtocol creates a new SWIM protocol instance.
func NewProtocol(cfg *Config) (*Protocol, error) {
	if cfg.LocalID == "" {
		return nil, fmt.Errorf("local_id is required")
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1"
	}
	if cfg.ProbeInterval <= 0 {
		cfg.ProbeInterval = 1 * time.Second
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 500 * time.Millisecond
	}
	if cfg.SuspicionMult <= 0 {
		cfg.SuspicionMult = 4
	}
	if cfg.GossipCount <= 0 {
		cfg.GossipCount = 3
	}
	if cfg.GossipInterval <= 0 {
		cfg.GossipInterval = 200 * time.Millisecond
	}

	// Use the injected transport if provided; otherwise create a real UDP socket.
	var transport MessageTransport
	if cfg.Transport != nil {
		transport = cfg.Transport
	} else {
		udpT, err := NewTransport(cfg.BindAddr, cfg.BindPort)
		if err != nil {
			return nil, fmt.Errorf("create transport: %w", err)
		}
		transport = udpT
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Protocol{
		localID:        cfg.LocalID,
		localAddr:      transport.LocalAddr().String(),
		members:        make(map[string]*Member),
		transport:      transport,
		ctx:            ctx,
		cancel:         cancel,
		probeInterval:  cfg.ProbeInterval,
		probeTimeout:   cfg.ProbeTimeout,
		suspicionMult:  cfg.SuspicionMult,
		gossipCount:    cfg.GossipCount,
		gossipInterval: cfg.GossipInterval,
		shutdown:       make(chan struct{}),
		gossipBuffer:   NewGossipBuffer(100),
	}

	p.fd = NewFailureDetector(
		time.Duration(cfg.SuspicionMult)*cfg.ProbeInterval,
		p.handleConfirm,
		p.handleRefute,
	)

	// Register self
	p.members[cfg.LocalID] = &Member{
		ID:          cfg.LocalID,
		Address:     p.localAddr,
		State:       StateAlive,
		Incarnation: 0,
		Metadata:    make(map[string]string),
	}
	p.members[cfg.LocalID].Touch()

	// Register message handlers
	p.transport.RegisterHandler(MsgPing, p.handlePing)
	p.transport.RegisterHandler(MsgPingReq, p.handlePingReq)
	p.transport.RegisterHandler(MsgAck, p.handleAck)
	p.transport.RegisterHandler(MsgSuspect, p.handleSuspect)
	p.transport.RegisterHandler(MsgAlive, p.handleAlive)
	p.transport.RegisterHandler(MsgDead, p.handleDead)
	p.transport.RegisterHandler(MsgJoin, p.handleJoin)
	p.transport.RegisterHandler(MsgLeave, p.handleLeave)

	return p, nil
}

// Start begins the protocol goroutines (probe, gossip, listener).
func (p *Protocol) Start() error {
	p.wg.Add(3)
	go p.listenLoop()
	go p.probeLoop()
	go p.gossipLoop()
	return nil
}

// Stop gracefully shuts down the protocol.
func (p *Protocol) Stop() error {
	p.cancel()
	close(p.shutdown)
	p.transport.Close()
	p.wg.Wait()
	return nil
}

// Join attempts to join an existing cluster via seed nodes.
func (p *Protocol) Join(seedAddrs []string) error {
	for _, addr := range seedAddrs {
		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			continue
		}
		msg := &Message{
			Type:     MsgJoin,
			SourceID: p.localID,
			Payload:  []byte(p.localAddr),
		}
		if err := p.transport.Send(msg, *udpAddr); err == nil {
			return nil
		}
	}
	return fmt.Errorf("failed to join any seed node")
}

// Leave gracefully leaves the cluster.
func (p *Protocol) Leave() error {
	p.mu.Lock()
	p.incarnation++
	inc := p.incarnation
	p.mu.Unlock()

	p.gossipBuffer.Add(GossipEvent{
		MemberID:    p.localID,
		State:       StateLeft,
		Incarnation: inc,
		Timestamp:   time.Now(),
	})

	members := p.HealthyMembers()
	for _, m := range members {
		if m.ID == p.localID {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", m.Address)
		if err != nil {
			continue
		}
		msg := &Message{
			Type:        MsgLeave,
			SourceID:    p.localID,
			Incarnation: inc,
		}
		p.transport.Send(msg, *addr)
	}

	return p.Stop()
}

// Members returns a snapshot of current members.
func (p *Protocol) Members() []*Member {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*Member, 0, len(p.members))
	for _, m := range p.members {
		result = append(result, m)
	}
	return result
}

// HealthyMembers returns only alive members.
func (p *Protocol) HealthyMembers() []*Member {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*Member, 0, len(p.members))
	for _, m := range p.members {
		if m.State == StateAlive {
			result = append(result, m)
		}
	}
	return result
}

// LocalID returns the local node ID.
func (p *Protocol) LocalID() string {
	return p.localID
}

// LocalAddr returns the local transport address.
func (p *Protocol) LocalAddr() string {
	return p.localAddr
}

// Incarnation returns the current incarnation number.
func (p *Protocol) Incarnation() uint32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.incarnation
}

// GossipBuffer returns the gossip buffer.
func (p *Protocol) GossipBuffer() *GossipBuffer {
	return p.gossipBuffer
}

// FailureDetector returns the failure detector.
func (p *Protocol) FailureDetector() *FailureDetector {
	return p.fd
}

// NewHardenedSuspicion builds a SuspicionManager wired to this protocol's real
// UDP transport. It implements incarnation-number refutation and a
// suspicion->Dead timeout that first attempts indirect probes through k random
// alive relay members before declaring a node Dead. The suspicion timeout is
// suspicionMult * probeInterval, and k is gossipCount.
//
// clock is injectable for deterministic testing; pass nil to use the real
// system clock. onDead/onRefute may be nil. This is an additive, opt-in API and
// does not alter the protocol's existing default behaviour.
func (p *Protocol) NewHardenedSuspicion(clock Clock, onDead, onRefute func(string)) *SuspicionManager {
	if clock == nil {
		clock = NewSystemClock()
	}
	prober := newTransportProber(p.transport, p.localID, p.probeTimeout)
	return NewSuspicionManager(SuspicionConfig{
		Clock:            clock,
		Prober:           prober,
		SuspicionTimeout: time.Duration(p.suspicionMult) * p.probeInterval,
		IndirectProbes:   p.gossipCount,
		OnDead:           onDead,
		OnRefute:         onRefute,
	})
}

// SelectRelays returns up to k random alive members suitable for indirect
// probing of excludeID, excluding the local node and excludeID itself. Use this
// together with SuspicionManager.SuspectWithRelays.
func (p *Protocol) SelectRelays(excludeID string, k int) []*Member {
	members := p.HealthyMembers()
	candidates := make([]*Member, 0, len(members))
	for _, m := range members {
		if m.ID == p.localID || m.ID == excludeID {
			continue
		}
		candidates = append(candidates, m)
	}
	return RandomMembers(candidates, k, "")
}

// --- internal handlers ---

func (p *Protocol) handlePing(msg *Message, addr net.Addr) {
	ack := &Message{
		Type:        MsgAck,
		SourceID:    p.localID,
		TargetID:    msg.SourceID,
		Incarnation: p.Incarnation(),
	}
	udpAddr, _ := net.ResolveUDPAddr("udp", addr.String())
	if udpAddr != nil {
		p.transport.Send(ack, *udpAddr)
	}

	// Indirect-probe support (backward compatible): if the ping carries an
	// origin requester address in its Payload (set by a relay forwarding a
	// Ping-Req), also ack that original requester directly so its indirect
	// probe can observe us as alive. Plain direct pings carry an empty Payload
	// and are unaffected.
	if len(msg.Payload) > 0 {
		if originAddr, err := net.ResolveUDPAddr("udp", string(msg.Payload)); err == nil {
			p.transport.Send(ack, *originAddr)
		}
	}
}

func (p *Protocol) handlePingReq(msg *Message, addr net.Addr) {
	// Forward ping to target. The original requester address is carried in the
	// Payload so the target can ack the requester directly, completing the SWIM
	// indirect-probe path: requester -> relay (ping-req) -> target -> requester.
	targetAddr, err := net.ResolveUDPAddr("udp", string(msg.Payload))
	if err != nil {
		return
	}
	origin := addr.String()
	ping := &Message{
		Type:     MsgPing,
		SourceID: p.localID,
		TargetID: msg.TargetID,
		Payload:  []byte(origin),
	}
	p.transport.Send(ping, *targetAddr)
}

func (p *Protocol) handleAck(msg *Message, addr net.Addr) {
	p.mu.RLock()
	m, ok := p.members[msg.SourceID]
	p.mu.RUnlock()
	if ok {
		m.Touch()
		// Transition Suspect->Alive under the member's own lock so this does not
		// race with concurrent UpdateState callers (e.g. the suspicion manager).
		m.ClearSuspect()
	}
	p.fd.Refute(msg.SourceID)
}

func (p *Protocol) handleSuspect(msg *Message, addr net.Addr) {
	p.mu.Lock()
	m, ok := p.members[msg.TargetID]
	if !ok {
		p.mu.Unlock()
		return
	}
	if msg.TargetID == p.localID {
		// Refute suspicion about self by incrementing incarnation
		p.incarnation++
		inc := p.incarnation
		p.mu.Unlock()
		p.broadcastAlive(inc)
		return
	}
	if m.UpdateState(StateSuspect, msg.Incarnation) {
		p.gossipBuffer.Add(GossipEvent{
			MemberID:    msg.TargetID,
			State:       StateSuspect,
			Incarnation: msg.Incarnation,
			Timestamp:   time.Now(),
		})
	}
	p.mu.Unlock()
	p.fd.Suspect(msg.TargetID, msg.Incarnation)
}

func (p *Protocol) handleAlive(msg *Message, addr net.Addr) {
	p.mu.Lock()
	m, ok := p.members[msg.SourceID]
	if !ok {
		m = &Member{
			ID:       msg.SourceID,
			Address:  addr.String(),
			State:    StateAlive,
			Metadata: make(map[string]string),
		}
		p.members[msg.SourceID] = m
	}
	m.UpdateState(StateAlive, msg.Incarnation)
	p.mu.Unlock()
	p.fd.Refute(msg.SourceID)
}

func (p *Protocol) handleDead(msg *Message, addr net.Addr) {
	p.mu.Lock()
	if m, ok := p.members[msg.TargetID]; ok {
		m.UpdateState(StateDead, msg.Incarnation)
		p.gossipBuffer.Add(GossipEvent{
			MemberID:    msg.TargetID,
			State:       StateDead,
			Incarnation: msg.Incarnation,
			Timestamp:   time.Now(),
		})
	}
	p.mu.Unlock()
	p.fd.Refute(msg.TargetID)
}

func (p *Protocol) handleJoin(msg *Message, addr net.Addr) {
	p.mu.Lock()
	if existing, ok := p.members[msg.SourceID]; ok {
		// Rejoin: a previously known (possibly Dead/Left) node is re-joining.
		// UpdateState to StateAlive at its current incarnation+1 so the state
		// machine accepts the transition (Dead > Alive numerically, so we must
		// use a higher incarnation to override).
		existing.mu.Lock()
		newInc := existing.Incarnation + 1
		existing.Address = string(msg.Payload)
		existing.State = StateAlive
		existing.Incarnation = newInc
		existing.lastSeen = time.Now()
		existing.mu.Unlock()
	} else {
		p.members[msg.SourceID] = &Member{
			ID:       msg.SourceID,
			Address:  string(msg.Payload),
			State:    StateAlive,
			Metadata: make(map[string]string),
		}
		p.members[msg.SourceID].Touch()
	}
	p.mu.Unlock()

	// Acknowledge with alive message
	ack := &Message{
		Type:        MsgAlive,
		SourceID:    p.localID,
		Incarnation: p.Incarnation(),
	}
	udpAddr, _ := net.ResolveUDPAddr("udp", addr.String())
	if udpAddr != nil {
		p.transport.Send(ack, *udpAddr)
	}
}

func (p *Protocol) handleLeave(msg *Message, addr net.Addr) {
	p.mu.Lock()
	if m, ok := p.members[msg.SourceID]; ok {
		m.UpdateState(StateLeft, msg.Incarnation)
		p.gossipBuffer.Add(GossipEvent{
			MemberID:    msg.SourceID,
			State:       StateLeft,
			Incarnation: msg.Incarnation,
			Timestamp:   time.Now(),
		})
	}
	p.mu.Unlock()
}

func (p *Protocol) handleConfirm(memberID string) {
	p.mu.Lock()
	if m, ok := p.members[memberID]; ok {
		p.incarnation++
		inc := p.incarnation
		m.UpdateState(StateDead, inc)
		p.gossipBuffer.Add(GossipEvent{
			MemberID:    memberID,
			State:       StateDead,
			Incarnation: inc,
			Timestamp:   time.Now(),
		})
	}
	p.mu.Unlock()
}

func (p *Protocol) handleRefute(memberID string) {
	// Member was refuted, already handled in other handlers
}

func (p *Protocol) broadcastAlive(incarnation uint32) {
	members := p.HealthyMembers()
	for _, m := range members {
		if m.ID == p.localID {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", m.Address)
		if err != nil {
			continue
		}
		msg := &Message{
			Type:        MsgAlive,
			SourceID:    p.localID,
			Incarnation: incarnation,
		}
		p.transport.Send(msg, *addr)
	}
}

// --- loops ---

func (p *Protocol) listenLoop() {
	defer p.wg.Done()
	p.transport.Listen(p.ctx)
}

func (p *Protocol) probeLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.probeRandomMember()
		case <-p.shutdown:
			return
		}
	}
}

func (p *Protocol) gossipLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.gossipInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.gossip()
		case <-p.shutdown:
			return
		}
	}
}

func (p *Protocol) probeRandomMember() {
	members := p.HealthyMembers()
	if len(members) <= 1 {
		return
	}

	// Pick a random member that isn't self
	var target *Member
	candidates := make([]*Member, len(members))
	copy(candidates, members)
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	for _, m := range candidates {
		if m.ID != p.localID {
			target = m
			break
		}
	}
	if target == nil {
		return
	}

	addr, err := net.ResolveUDPAddr("udp", target.Address)
	if err != nil {
		return
	}

	msg := &Message{
		Type:     MsgPing,
		SourceID: p.localID,
		TargetID: target.ID,
	}

	// Send ping and wait for ack with timeout
	ackCh := make(chan bool, 1)
	go func() {
		// In a real implementation, we'd correlate acks with pings.
		// Here we just wait for any ack handler to touch the member.
		oldSeen := target.TimeSinceLastSeen()
		time.Sleep(p.probeTimeout)
		if target.TimeSinceLastSeen() < oldSeen {
			ackCh <- true
		} else {
			ackCh <- false
		}
	}()

	p.transport.Send(msg, *addr)

	if !<-ackCh {
		// No ack received, suspect the member
		p.mu.Lock()
		p.incarnation++
		inc := p.incarnation
		p.mu.Unlock()

		p.fd.Suspect(target.ID, inc)
		p.gossipBuffer.Add(GossipEvent{
			MemberID:    target.ID,
			State:       StateSuspect,
			Incarnation: inc,
			Timestamp:   time.Now(),
		})

		// Broadcast suspicion
		for _, m := range p.HealthyMembers() {
			if m.ID == p.localID || m.ID == target.ID {
				continue
			}
			maddr, err := net.ResolveUDPAddr("udp", m.Address)
			if err != nil {
				continue
			}
			suspectMsg := &Message{
				Type:        MsgSuspect,
				SourceID:    p.localID,
				TargetID:    target.ID,
				Incarnation: inc,
			}
			p.transport.Send(suspectMsg, *maddr)
		}
	}
}

func (p *Protocol) gossip() {
	events := p.gossipBuffer.Recent(p.gossipCount)
	if len(events) == 0 {
		return
	}

	members := p.HealthyMembers()
	targets := RandomMembers(members, p.gossipCount, p.localID)
	if len(targets) == 0 {
		return
	}

	for _, target := range targets {
		addr, err := net.ResolveUDPAddr("udp", target.Address)
		if err != nil {
			continue
		}
		for _, event := range events {
			var msgType MessageType
			switch event.State {
			case StateSuspect:
				msgType = MsgSuspect
			case StateDead:
				msgType = MsgDead
			case StateLeft:
				msgType = MsgLeave
			case StateAlive:
				msgType = MsgAlive
			}
			msg := &Message{
				Type:        msgType,
				SourceID:    event.MemberID,
				TargetID:    event.MemberID,
				Incarnation: event.Incarnation,
			}
			p.transport.Send(msg, *addr)
		}
	}
}
