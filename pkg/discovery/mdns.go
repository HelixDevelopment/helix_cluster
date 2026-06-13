// Package discovery provides service discovery for Helix Cluster OS.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// mDNS / DNS-SD LAN cell discovery (HXC-1347).
//
// This file implements zero-configuration discovery of Helix cells on a local
// link using multicast DNS (RFC 6762) + DNS-based service discovery (RFC 6763)
// over the standard mDNS group 224.0.0.251:5353. A cell advertises itself as an
// instance of the service type _helix-cluster._tcp, publishing its identifying
// attributes (cell id, node id, version, WireGuard public key, cluster address)
// in a TXT record alongside an SRV record (host+port) and an A record (IPv4).
//
// SECURITY / TRUST BOUNDARY: mDNS/DNS-SD is for DISCOVERY ONLY. The TXT records
// it carries are UNAUTHENTICATED — any host on the LAN can forge them. A
// DiscoveredPeer returned by the browser is therefore an *untrusted candidate*.
// Before a peer is admitted to the cluster (e.g. before a WireGuard tunnel is
// established or a node is added to a trust tier) it MUST complete SPIFFE
// attestation against the control plane; only then may it be promoted via the
// trust-aware registry (see trust.go / TrustRegistry). Nothing in this file
// performs or implies trust — do NOT wire a discovered peer straight into a
// privileged path.

const (
	// MDNSServiceType is the DNS-SD service type for Helix cells.
	MDNSServiceType = "_helix-cluster._tcp"

	// MDNSDomain is the link-local DNS-SD domain.
	MDNSDomain = "local."

	// mdnsMulticastAddrV4 is the standard IPv4 mDNS group + port (RFC 6762).
	mdnsMulticastAddrV4 = "224.0.0.251:5353"

	// mdnsDefaultTTL is the default record TTL advertised in responses, in
	// seconds. RFC 6762 §10 recommends 120s for host records.
	mdnsDefaultTTL = 120
)

// TXT record attribute keys carried in a Helix cell advertisement. These are
// the required identifying fields; an entry missing any of them is rejected by
// the browser (see DiscoveredPeerFromTXT).
const (
	txtKeyCellID   = "cellid"
	txtKeyNodeID   = "nodeid"
	txtKeyVersion  = "version"
	txtKeyWGPubKey = "wgpubkey"
	txtKeyClusterA = "clusteraddr"
)

var (
	// ErrInvalidServiceEntry is returned when a parsed mDNS service entry is
	// missing a required field or carries an invalid value, and is therefore
	// rejected by the browser.
	ErrInvalidServiceEntry = errors.New("invalid mdns service entry")

	// ErrMDNSClosed is returned by browser/server operations after Close.
	ErrMDNSClosed = errors.New("mdns: closed")
)

// DiscoveredPeer is an untrusted cell-discovery candidate parsed from an mDNS
// advertisement. It is the sink-side output of the browser. Fields map 1:1 onto
// the advertised TXT/SRV/A records.
//
// A DiscoveredPeer carries NO trust: the WireGuardPubKey and ClusterAddr are
// claimed by the (unauthenticated) advertiser and MUST be validated by SPIFFE
// attestation before use.
type DiscoveredPeer struct {
	CellID          string
	NodeID          string
	Version         string
	WireGuardPubKey string
	ClusterAddr     string
	IP              net.IP
	Port            int
	// TTL is the advertised record lifetime; a browser may evict the peer when
	// it elapses without a refresh.
	TTL time.Duration
}

// ServiceEntry is the decoded, transport-level view of a single advertised
// instance assembled from the records in an mDNS response (the SRV target/port,
// the A address, and the flattened TXT key/value pairs). It is an intermediate
// representation; DiscoveredPeerFromTXT validates it into a DiscoveredPeer.
type ServiceEntry struct {
	// Instance is the DNS-SD instance label (typically the cell or node id).
	Instance string
	// Host is the SRV target hostname.
	Host string
	// Port is the SRV port.
	Port int
	// AddrV4 is the resolved IPv4 address (from the A record), if any.
	AddrV4 net.IP
	// TTL is the smallest record TTL observed for the entry.
	TTL time.Duration
	// Text holds the flattened TXT key=value attributes.
	Text map[string]string
}

// MDNSAdvertisement is the set of identifying attributes a cell publishes.
type MDNSAdvertisement struct {
	CellID          string
	NodeID          string
	Version         string
	WireGuardPubKey string
	ClusterAddr     string
	// Host is the SRV target (advertised hostname). If empty, NodeID + domain
	// is used.
	Host string
	// Port is the SRV port advertised for the cluster endpoint.
	Port int
	// IPv4 is the A-record address advertised for Host.
	IPv4 net.IP
	// TTL is the advertised record lifetime; zero defaults to mdnsDefaultTTL.
	TTL time.Duration
}

// validate ensures the advertisement carries all required identifying fields.
func (a *MDNSAdvertisement) validate() error {
	if strings.TrimSpace(a.CellID) == "" {
		return fmt.Errorf("%w: missing cellid", ErrInvalidServiceEntry)
	}
	if strings.TrimSpace(a.NodeID) == "" {
		return fmt.Errorf("%w: missing nodeid", ErrInvalidServiceEntry)
	}
	if strings.TrimSpace(a.WireGuardPubKey) == "" {
		return fmt.Errorf("%w: missing wgpubkey", ErrInvalidServiceEntry)
	}
	if strings.TrimSpace(a.ClusterAddr) == "" {
		return fmt.Errorf("%w: missing clusteraddr", ErrInvalidServiceEntry)
	}
	if a.Port <= 0 || a.Port > 65535 {
		return fmt.Errorf("%w: invalid port %d", ErrInvalidServiceEntry, a.Port)
	}
	return nil
}

// txtAttributes returns the TXT key=value strings for the advertisement in a
// stable order.
func (a *MDNSAdvertisement) txtAttributes() []string {
	return []string{
		txtKeyCellID + "=" + a.CellID,
		txtKeyNodeID + "=" + a.NodeID,
		txtKeyVersion + "=" + a.Version,
		txtKeyWGPubKey + "=" + a.WireGuardPubKey,
		txtKeyClusterA + "=" + a.ClusterAddr,
	}
}

// instanceName returns the fully-qualified DNS-SD instance name for the
// advertisement: <instance>._helix-cluster._tcp.local.
func (a *MDNSAdvertisement) instanceName() string {
	label := a.NodeID
	if label == "" {
		label = a.CellID
	}
	return label + "." + MDNSServiceType + "." + MDNSDomain
}

// hostName returns the advertised SRV target as a fully-qualified name.
func (a *MDNSAdvertisement) hostName() string {
	h := a.Host
	if h == "" {
		h = a.NodeID + "." + MDNSDomain
	}
	if !strings.HasSuffix(h, ".") {
		h += "."
	}
	return h
}

// clampPort narrows a port number to the valid uint16 range without wrapping.
func clampPort(p int) uint16 {
	if p < 0 {
		return 0
	}
	if p > 0xffff {
		return 0xffff
	}
	return uint16(p) //gosec:disable G115 -- bounds-checked to [0,0xffff] above
}

// ttlSeconds returns the advertised TTL in whole seconds (>=1).
func (a *MDNSAdvertisement) ttlSeconds() uint32 {
	if a.TTL <= 0 {
		return mdnsDefaultTTL
	}
	secs := int64(a.TTL / time.Second)
	if secs > int64(^uint32(0)) { // cap absurd TTLs at the uint32 max
		return ^uint32(0)
	}
	s := uint32(secs) //gosec:disable G115 -- secs is >0 (a.TTL>0 checked above) and capped to uint32 max
	if s == 0 {
		s = 1
	}
	return s
}

// parseTXTAttributes flattens a slice of "key=value" TXT strings into a map.
// Keys are lowercased; a bare key (no '=') maps to an empty value, matching
// DNS-SD semantics (RFC 6763 §6.4).
func parseTXTAttributes(txt []string) map[string]string {
	out := make(map[string]string, len(txt))
	for _, kv := range txt {
		if kv == "" {
			continue
		}
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[strings.ToLower(kv[:i])] = kv[i+1:]
		} else {
			out[strings.ToLower(kv)] = ""
		}
	}
	return out
}

// DiscoveredPeerFromTXT validates a ServiceEntry into a DiscoveredPeer,
// REJECTING any entry that is missing a required field (cellid, nodeid,
// wgpubkey, clusteraddr) or that carries an invalid port. This is the sink-side
// gate that prevents malformed/forged-incomplete advertisements from becoming
// peers. The version field is recorded but not required.
func DiscoveredPeerFromTXT(entry *ServiceEntry) (*DiscoveredPeer, error) {
	if entry == nil {
		return nil, fmt.Errorf("%w: nil entry", ErrInvalidServiceEntry)
	}
	attrs := entry.Text
	if attrs == nil {
		return nil, fmt.Errorf("%w: no TXT attributes", ErrInvalidServiceEntry)
	}

	cellID := strings.TrimSpace(attrs[txtKeyCellID])
	nodeID := strings.TrimSpace(attrs[txtKeyNodeID])
	wgKey := strings.TrimSpace(attrs[txtKeyWGPubKey])
	clusterAddr := strings.TrimSpace(attrs[txtKeyClusterA])

	if cellID == "" {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidServiceEntry, txtKeyCellID)
	}
	if nodeID == "" {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidServiceEntry, txtKeyNodeID)
	}
	if wgKey == "" {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidServiceEntry, txtKeyWGPubKey)
	}
	if clusterAddr == "" {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidServiceEntry, txtKeyClusterA)
	}
	if entry.Port <= 0 || entry.Port > 65535 {
		return nil, fmt.Errorf("%w: invalid port %d", ErrInvalidServiceEntry, entry.Port)
	}

	return &DiscoveredPeer{
		CellID:          cellID,
		NodeID:          nodeID,
		Version:         strings.TrimSpace(attrs[txtKeyVersion]),
		WireGuardPubKey: wgKey,
		ClusterAddr:     clusterAddr,
		IP:              entry.AddrV4,
		Port:            entry.Port,
		TTL:             entry.TTL,
	}, nil
}

// buildAdvertisementResponse packs a DNS-SD mDNS response message for the
// advertisement: a PTR (service-type -> instance), SRV (instance -> host:port),
// TXT (attributes), and an A record (host -> IPv4) when an IPv4 is present. The
// returned bytes are ready to send to the mDNS group. It is exported-internal
// (lowercase) but exercised directly by tests for deterministic round-trips.
func buildAdvertisementResponse(a *MDNSAdvertisement) ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}

	serviceName, err := dnsmessage.NewName(MDNSServiceType + "." + MDNSDomain)
	if err != nil {
		return nil, fmt.Errorf("service name: %w", err)
	}
	instName, err := dnsmessage.NewName(a.instanceName())
	if err != nil {
		return nil, fmt.Errorf("instance name: %w", err)
	}
	hostName, err := dnsmessage.NewName(a.hostName())
	if err != nil {
		return nil, fmt.Errorf("host name: %w", err)
	}

	ttl := a.ttlSeconds()

	// RFC 6762 §18: response with QR=1, AA=1, ID=0.
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}

	// PTR: _helix-cluster._tcp.local. -> instance
	if err := b.PTRResource(dnsmessage.ResourceHeader{
		Name:  serviceName,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
		TTL:   ttl,
	}, dnsmessage.PTRResource{PTR: instName}); err != nil {
		return nil, fmt.Errorf("ptr: %w", err)
	}

	// SRV: instance -> host:port
	if err := b.SRVResource(dnsmessage.ResourceHeader{
		Name:  instName,
		Type:  dnsmessage.TypeSRV,
		Class: dnsmessage.ClassINET,
		TTL:   ttl,
	}, dnsmessage.SRVResource{
		Priority: 0,
		Weight:   0,
		Port:     clampPort(a.Port),
		Target:   hostName,
	}); err != nil {
		return nil, fmt.Errorf("srv: %w", err)
	}

	// TXT: instance -> attributes
	if err := b.TXTResource(dnsmessage.ResourceHeader{
		Name:  instName,
		Type:  dnsmessage.TypeTXT,
		Class: dnsmessage.ClassINET,
		TTL:   ttl,
	}, dnsmessage.TXTResource{TXT: a.txtAttributes()}); err != nil {
		return nil, fmt.Errorf("txt: %w", err)
	}

	// A: host -> IPv4 (only if a usable IPv4 is supplied)
	if v4 := a.IPv4.To4(); v4 != nil {
		var arr [4]byte
		copy(arr[:], v4)
		if err := b.AResource(dnsmessage.ResourceHeader{
			Name:  hostName,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
			TTL:   ttl,
		}, dnsmessage.AResource{A: arr}); err != nil {
			return nil, fmt.Errorf("a: %w", err)
		}
	}

	return b.Finish()
}

// parseServiceEntries decodes an mDNS response wire message into ServiceEntry
// values keyed by instance name. It reassembles the SRV target/port, A address,
// and TXT attributes for each advertised instance of MDNSServiceType. Records
// for unrelated service types are ignored. This is the decode half of the
// round-trip and is exercised directly by tests.
func parseServiceEntries(msg []byte) ([]*ServiceEntry, error) {
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		return nil, fmt.Errorf("parse start: %w", err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, fmt.Errorf("skip questions: %w", err)
	}

	// Aggregate records across the answer + additional sections.
	type agg struct {
		host string
		port int
		txt  map[string]string
		ttl  time.Duration
	}
	byInstance := make(map[string]*agg)
	hostToV4 := make(map[string]net.IP)
	srvSuffix := "." + MDNSServiceType + "." + MDNSDomain

	getInst := func(name string) *agg {
		a, ok := byInstance[name]
		if !ok {
			a = &agg{txt: make(map[string]string)}
			byInstance[name] = a
		}
		return a
	}

	consume := func(next func() (dnsmessage.Resource, error)) error {
		for {
			r, err := next()
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				return nil
			}
			if err != nil {
				return err
			}
			name := r.Header.Name.String()
			ttl := time.Duration(r.Header.TTL) * time.Second
			switch r.Header.Type {
			case dnsmessage.TypeSRV:
				if !strings.HasSuffix(strings.TrimSuffix(name, "."), strings.TrimSuffix(srvSuffix, ".")) {
					continue
				}
				srv, ok := r.Body.(*dnsmessage.SRVResource)
				if !ok {
					continue
				}
				inst := getInst(name)
				inst.host = srv.Target.String()
				inst.port = int(srv.Port)
				if inst.ttl == 0 || ttl < inst.ttl {
					inst.ttl = ttl
				}
			case dnsmessage.TypeTXT:
				if !strings.HasSuffix(strings.TrimSuffix(name, "."), strings.TrimSuffix(srvSuffix, ".")) {
					continue
				}
				txt, ok := r.Body.(*dnsmessage.TXTResource)
				if !ok {
					continue
				}
				inst := getInst(name)
				for k, v := range parseTXTAttributes(txt.TXT) {
					inst.txt[k] = v
				}
				if inst.ttl == 0 || ttl < inst.ttl {
					inst.ttl = ttl
				}
			case dnsmessage.TypeA:
				a, ok := r.Body.(*dnsmessage.AResource)
				if !ok {
					continue
				}
				ip := net.IPv4(a.A[0], a.A[1], a.A[2], a.A[3])
				hostToV4[name] = ip
			default:
				// ignore other record types
			}
		}
	}

	if err := consume(p.Answer); err != nil {
		return nil, fmt.Errorf("answers: %w", err)
	}
	if err := p.SkipAllAuthorities(); err != nil {
		return nil, fmt.Errorf("skip authorities: %w", err)
	}
	if err := consume(p.Additional); err != nil {
		return nil, fmt.Errorf("additionals: %w", err)
	}

	var out []*ServiceEntry
	for name, a := range byInstance {
		entry := &ServiceEntry{
			Instance: strings.TrimSuffix(name, srvSuffix),
			Host:     a.host,
			Port:     a.port,
			TTL:      a.ttl,
			Text:     a.txt,
		}
		if ip, ok := hostToV4[a.host]; ok {
			entry.AddrV4 = ip
		}
		out = append(out, entry)
	}
	return out, nil
}

// buildQuery packs an mDNS PTR query for the Helix service type. Browsers send
// this to the mDNS group to solicit advertisements.
func buildQuery() ([]byte, error) {
	serviceName, err := dnsmessage.NewName(MDNSServiceType + "." + MDNSDomain)
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{
		Name:  serviceName,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// mdnsConn abstracts the multicast socket so the server/browser logic is
// testable against an in-process fake without real multicast.
type mdnsConn interface {
	// WriteToGroup sends a packed mDNS message to the multicast group.
	WriteToGroup(b []byte) error
	// Read returns the next received packet (payload, source addr).
	Read() ([]byte, net.Addr, error)
	Close() error
}

// MDNSServer advertises a single cell as a DNS-SD instance of
// MDNSServiceType. On receiving a matching PTR query (or on a periodic
// announce), it emits the PTR/SRV/TXT/A response. Trust is NOT established here
// (see file-level note): the server merely publishes discovery metadata.
type MDNSServer struct {
	ad   MDNSAdvertisement
	conn mdnsConn

	mu     sync.Mutex
	closed bool
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewMDNSServer creates a server that will advertise ad over conn. The
// advertisement is validated up front so a malformed cell cannot be published.
func NewMDNSServer(ad MDNSAdvertisement, conn mdnsConn) (*MDNSServer, error) {
	if conn == nil {
		return nil, errors.New("mdns: nil conn")
	}
	if err := ad.validate(); err != nil {
		return nil, err
	}
	return &MDNSServer{
		ad:     ad,
		conn:   conn,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}, nil
}

// Announce sends one unsolicited advertisement to the group immediately.
func (s *MDNSServer) Announce() error {
	resp, err := buildAdvertisementResponse(&s.ad)
	if err != nil {
		return err
	}
	return s.conn.WriteToGroup(resp)
}

// Serve runs the responder loop: it answers PTR queries for the Helix service
// type and re-announces on the given interval (interval<=0 disables periodic
// re-announce). It blocks until ctx is cancelled or Close is called.
func (s *MDNSServer) Serve(ctx context.Context, announceInterval time.Duration) error {
	defer close(s.doneCh)

	// Initial announce so browsers already listening discover us promptly.
	if err := s.Announce(); err != nil {
		return err
	}

	var ticker *time.Ticker
	var tickCh <-chan time.Time
	if announceInterval > 0 {
		ticker = time.NewTicker(announceInterval)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	readCh := make(chan []byte, 16)
	readErr := make(chan error, 1)
	go func() {
		for {
			pkt, _, err := s.conn.Read()
			if err != nil {
				select {
				case readErr <- err:
				default:
				}
				return
			}
			cp := make([]byte, len(pkt))
			copy(cp, pkt)
			select {
			case readCh <- cp:
			case <-s.stopCh:
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stopCh:
			return nil
		case <-tickCh:
			if err := s.Announce(); err != nil {
				return err
			}
		case pkt := <-readCh:
			if s.isQueryForUs(pkt) {
				if err := s.Announce(); err != nil {
					return err
				}
			}
		case <-readErr:
			// Reader stopped (conn closed); exit cleanly.
			return nil
		}
	}
}

// isQueryForUs reports whether pkt is an mDNS PTR query for MDNSServiceType.
func (s *MDNSServer) isQueryForUs(pkt []byte) bool {
	var p dnsmessage.Parser
	h, err := p.Start(pkt)
	if err != nil || h.Response {
		return false
	}
	want := MDNSServiceType + "." + MDNSDomain
	for {
		q, err := p.Question()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			return false
		}
		if err != nil {
			return false
		}
		if q.Type == dnsmessage.TypePTR && strings.EqualFold(q.Name.String(), want) {
			return true
		}
	}
}

// Close stops the server.
func (s *MDNSServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stopCh)
	s.mu.Unlock()
	err := s.conn.Close()
	return err
}

// MDNSBrowser discovers Helix cells on the LAN. It sends PTR queries and parses
// the resulting responses into DiscoveredPeer values, REJECTING any entry that
// fails validation. Discovered peers are untrusted candidates (see file note).
type MDNSBrowser struct {
	conn mdnsConn

	mu     sync.Mutex
	closed bool
}

// NewMDNSBrowser creates a browser over conn.
func NewMDNSBrowser(conn mdnsConn) (*MDNSBrowser, error) {
	if conn == nil {
		return nil, errors.New("mdns: nil conn")
	}
	return &MDNSBrowser{conn: conn}, nil
}

// Query sends a single PTR query for the Helix service type to the group.
func (b *MDNSBrowser) Query() error {
	q, err := buildQuery()
	if err != nil {
		return err
	}
	return b.conn.WriteToGroup(q)
}

// Browse discovers cells until ctx is cancelled or the discovery window
// elapses. It sends an initial query, then reads responses, decoding and
// validating each into a DiscoveredPeer. Invalid/incomplete entries are
// silently dropped (never returned as peers). Peers are de-duplicated by
// CellID+NodeID; the latest observation wins. The returned map is keyed by
// "<cellid>/<nodeid>".
func (b *MDNSBrowser) Browse(ctx context.Context, window time.Duration) (map[string]*DiscoveredPeer, error) {
	if err := b.Query(); err != nil {
		return nil, err
	}

	peers := make(map[string]*DiscoveredPeer)

	deadline := time.NewTimer(window)
	defer deadline.Stop()

	readCh := make(chan []byte, 16)
	readErr := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			pkt, _, err := b.conn.Read()
			if err != nil {
				select {
				case readErr <- err:
				default:
				}
				return
			}
			cp := make([]byte, len(pkt))
			copy(cp, pkt)
			select {
			case readCh <- cp:
			case <-stop:
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return peers, ctx.Err()
		case <-deadline.C:
			return peers, nil
		case <-readErr:
			return peers, nil
		case pkt := <-readCh:
			entries, err := parseServiceEntries(pkt)
			if err != nil {
				continue // ignore undecodable packets
			}
			for _, e := range entries {
				peer, err := DiscoveredPeerFromTXT(e)
				if err != nil {
					continue // REJECT invalid/incomplete entry
				}
				peers[peer.CellID+"/"+peer.NodeID] = peer
			}
		}
	}
}

// --- Real multicast transport (CLAUDE-2: portable stdlib net, no build tags) ---

// udpMulticastConn is a real mDNS socket bound to the IPv4 multicast group on a
// chosen interface. It uses only the portable golang.org/x/net/ipv4 multicast
// API over a UDP4 socket, so the same code path runs on Linux and macOS without
// OS-specific build tags (CLAUDE-2).
type udpMulticastConn struct {
	pc    *ipv4.PacketConn
	raw   net.PacketConn
	group *net.UDPAddr
	iface *net.Interface
}

// newUDPMulticastConn joins the mDNS IPv4 group on iface (nil => let the OS
// choose) and returns a conn usable by MDNSServer/MDNSBrowser. Multicast
// loopback is enabled so a server and browser on the same host observe each
// other's traffic — required for the in-host loopback integration test.
func newUDPMulticastConn(iface *net.Interface) (*udpMulticastConn, error) {
	group, err := net.ResolveUDPAddr("udp4", mdnsMulticastAddrV4)
	if err != nil {
		return nil, err
	}
	raw, err := net.ListenPacket("udp4", mdnsMulticastAddrV4)
	if err != nil {
		return nil, err
	}
	pc := ipv4.NewPacketConn(raw)
	if err := pc.JoinGroup(iface, &net.UDPAddr{IP: group.IP}); err != nil {
		_ = raw.Close()
		return nil, err
	}
	// Best-effort: enable loopback + set the multicast interface. Failures here
	// are non-fatal (some platforms/sandboxes restrict these), but loopback is
	// what lets an in-host server+browser see each other.
	_ = pc.SetMulticastLoopback(true)
	if iface != nil {
		_ = pc.SetMulticastInterface(iface)
	}
	return &udpMulticastConn{pc: pc, raw: raw, group: group, iface: iface}, nil
}

func (c *udpMulticastConn) WriteToGroup(b []byte) error {
	_, err := c.pc.WriteTo(b, nil, c.group)
	return err
}

func (c *udpMulticastConn) Read() ([]byte, net.Addr, error) {
	buf := make([]byte, 9000)
	n, _, addr, err := c.pc.ReadFrom(buf)
	if err != nil {
		return nil, nil, err
	}
	return buf[:n], addr, nil
}

func (c *udpMulticastConn) Close() error { return c.raw.Close() }
