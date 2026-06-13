package turn

import (
	"bytes"
	"net"
	"testing"
	"time"

	pionturn "github.com/pion/turn/v4"
)

// testRealm is the long-term-credential realm advertised by the in-test server.
const testRealm = "helix.test"

// testServer is a real pion/turn server bound to a loopback UDP port with
// long-term authentication. It relays for real over real UDP.
type testServer struct {
	server   *pionturn.Server
	udpConn  net.PacketConn
	addr     string
	username string
	password string
}

// startTestServer stands up a REAL pion/turn server on 127.0.0.1:<ephemeral>
// with a single long-term credential. The relay address generator hands out
// real loopback-bound relay sockets, so traffic is genuinely forwarded.
func startTestServer(t *testing.T, username, password string) *testServer {
	t.Helper()

	udpConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen turn server udp: %v", err)
	}

	// Precompute the long-term auth key for the single valid credential.
	credKey := pionturn.GenerateAuthKey(username, testRealm, password)

	server, err := pionturn.NewServer(pionturn.ServerConfig{
		Realm: testRealm,
		AuthHandler: func(u, realm string, _ net.Addr) ([]byte, bool) {
			if u == username && realm == testRealm {
				return credKey, true
			}
			return nil, false
		},
		PacketConnConfigs: []pionturn.PacketConnConfig{
			{
				PacketConn: udpConn,
				RelayAddressGenerator: &pionturn.RelayAddressGeneratorStatic{
					RelayAddress: net.ParseIP("127.0.0.1"),
					Address:      "127.0.0.1",
				},
			},
		},
	})
	if err != nil {
		_ = udpConn.Close()
		t.Fatalf("new turn server: %v", err)
	}

	ts := &testServer{
		server:   server,
		udpConn:  udpConn,
		addr:     udpConn.LocalAddr().String(),
		username: username,
		password: password,
	}
	t.Cleanup(func() { _ = ts.server.Close() })
	return ts
}

func (ts *testServer) endpoint() Endpoint {
	return Endpoint{
		Address:  ts.addr,
		Username: ts.username,
		Password: ts.password,
		Realm:    testRealm,
	}
}

// deadEndpoint returns an endpoint pointing at a genuinely closed UDP port on
// loopback: we bind a socket to grab a free port, then close it, so nothing is
// listening there. Allocation against it must fail (and fall back).
func deadEndpoint(t *testing.T) Endpoint {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grab dead port: %v", err)
	}
	addr := c.LocalAddr().String()
	if err := c.Close(); err != nil {
		t.Fatalf("close dead port: %v", err)
	}
	return Endpoint{
		Address:  addr,
		Username: "u",
		Password: "p",
		Realm:    testRealm,
	}
}

// TestAllocateRelayedAddress proves a real client Allocates a real relayed
// address from a real server (CLOSURE 1, part 1).
func TestAllocateRelayedAddress(t *testing.T) {
	srv := startTestServer(t, "helix", "s3cr3t")

	mgr, err := NewRelayManager(Config{
		Endpoints:   []Endpoint{srv.endpoint()},
		DialTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	alloc, err := mgr.Allocate()
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	defer func() { _ = alloc.Close() }()

	if alloc.RelayedAddr == nil {
		t.Fatal("relayed address is nil — server did not grant a relay")
	}
	if alloc.RelayConn == nil {
		t.Fatal("relay conn is nil")
	}
	if alloc.RelayedAddr.String() != alloc.RelayConn.LocalAddr().String() {
		t.Fatalf("relayed addr %q != relay conn local addr %q",
			alloc.RelayedAddr, alloc.RelayConn.LocalAddr())
	}
	if alloc.Endpoint.Address != srv.addr {
		t.Fatalf("served by %q, want %q", alloc.Endpoint.Address, srv.addr)
	}
	t.Logf("granted relayed address %s from %s", alloc.RelayedAddr, alloc.Endpoint.Address)
}

// TestRelayedTrafficThroughServer proves real bytes flow THROUGH the relay
// between two independent clients (CLOSURE 1, part 2). A sends to B's relayed
// address and B receives it byte-exact. If the server were not actually
// relaying, B would never receive the payload and this test would time out/fail.
func TestRelayedTrafficThroughServer(t *testing.T) {
	srv := startTestServer(t, "helix", "s3cr3t")
	ep := srv.endpoint()

	mgr, err := NewRelayManager(Config{Endpoints: []Endpoint{ep}, DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	allocA, err := mgr.Allocate()
	if err != nil {
		t.Fatalf("allocate A: %v", err)
	}
	defer func() { _ = allocA.Close() }()

	allocB, err := mgr.Allocate()
	if err != nil {
		t.Fatalf("allocate B: %v", err)
	}
	defer func() { _ = allocB.Close() }()

	// TURN permissions are directional and installed on the RECEIVING
	// allocation, keyed by the sender's IP. For A->B traffic the server checks
	// B's allocation for a permission covering A's relayed IP, so B must permit
	// A. We also let A permit B for symmetry / replies.
	if err := allocB.Client.CreatePermission(allocA.RelayedAddr); err != nil {
		t.Fatalf("B create permission for A: %v", err)
	}
	if err := allocA.Client.CreatePermission(allocB.RelayedAddr); err != nil {
		t.Fatalf("A create permission for B: %v", err)
	}

	payload := []byte("HELIX-RELAY-PROOF-7f3a9c2e-byte-exact")

	// Reader on B's relay conn.
	type rx struct {
		n    int
		from net.Addr
		err  error
		buf  []byte
	}
	got := make(chan rx, 1)
	go func() {
		buf := make([]byte, 1500)
		_ = allocB.RelayConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, from, err := allocB.RelayConn.ReadFrom(buf)
		got <- rx{n: n, from: from, err: err, buf: buf[:n]}
	}()

	// Give B's reader a moment to arm, then A sends to B's relayed address.
	time.Sleep(100 * time.Millisecond)
	if _, err := allocA.RelayConn.WriteTo(payload, allocB.RelayedAddr); err != nil {
		t.Fatalf("A write to B relayed addr: %v", err)
	}

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("B read from relay: %v", r.err)
		}
		if !bytes.Equal(r.buf, payload) {
			t.Fatalf("B received %q, want byte-exact %q", r.buf, payload)
		}
		t.Logf("B received %d bytes THROUGH the relay from %s, byte-exact", r.n, r.from)
	case <-time.After(6 * time.Second):
		t.Fatal("B never received the relayed payload — relay did not forward")
	}
}

// TestFallbackChain proves the manager fails over a genuinely dead first
// endpoint and allocates from the second (CLOSURE 2). It also captures WHICH
// endpoint served the allocation.
func TestFallbackChain(t *testing.T) {
	dead := deadEndpoint(t)
	srv := startTestServer(t, "helix", "s3cr3t")
	good := srv.endpoint()

	mgr, err := NewRelayManager(Config{
		Endpoints:   []Endpoint{dead, good}, // dead first, good second
		DialTimeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	alloc, err := mgr.Allocate()
	if err != nil {
		t.Fatalf("allocate should have fallen back to the good server, got: %v", err)
	}
	defer func() { _ = alloc.Close() }()

	if alloc.Endpoint.Address != good.Address {
		t.Fatalf("served by %q, want the good (second) endpoint %q",
			alloc.Endpoint.Address, good.Address)
	}
	if alloc.Endpoint.Address == dead.Address {
		t.Fatal("allocation was served by the DEAD endpoint — impossible without bluff")
	}
	t.Logf("fell over dead endpoint %s, allocation served by %s",
		dead.Address, alloc.Endpoint.Address)
}

// TestWrongCredentialsRejected proves the real server rejects allocation with
// wrong credentials (CLOSURE 3).
//
// RFC 5766 long-term auth is a two-step exchange: the server's FIRST response
// is always 401 Unauthorized carrying a realm+nonce challenge (this is the
// normal handshake, surfaced by pion at internal/server/util.go:70-71). The
// client then retries with MESSAGE-INTEGRITY. When the credential is wrong the
// integrity check fails and the real pion/turn/v4 server returns 400 Bad
// Request (util.go:116-120) — NOT a second 401. We therefore assert that the
// allocation is REJECTED (the load-bearing guarantee) rather than hard-coding a
// status the real server does not send, which would itself be a bluff. Both the
// wrong-password and unknown-username paths are exercised.
func TestWrongCredentialsRejected(t *testing.T) {
	srv := startTestServer(t, "helix", "s3cr3t")

	t.Run("wrong password", func(t *testing.T) {
		bad := srv.endpoint()
		bad.Password = "WRONG-PASSWORD" // valid username, wrong password

		mgr, err := NewRelayManager(Config{Endpoints: []Endpoint{bad}, DialTimeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		alloc, err := mgr.Allocate()
		if err == nil {
			_ = alloc.Close()
			t.Fatal("allocation with wrong password succeeded — auth not enforced")
		}
		t.Logf("wrong-password allocation correctly rejected: %v", err)
	})

	t.Run("unknown user", func(t *testing.T) {
		bad := srv.endpoint()
		bad.Username = "intruder" // AuthHandler returns ok=false

		mgr, err := NewRelayManager(Config{Endpoints: []Endpoint{bad}, DialTimeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		alloc, err := mgr.Allocate()
		if err == nil {
			_ = alloc.Close()
			t.Fatal("allocation with unknown user succeeded — auth not enforced")
		}
		t.Logf("unknown-user allocation correctly rejected: %v", err)
	})
}

// TestSingleEndpointDeadFails proves that with only a dead endpoint the chain
// exhausts and returns a joined error (no silent success).
func TestSingleEndpointDeadFails(t *testing.T) {
	dead := deadEndpoint(t)
	mgr, err := NewRelayManager(Config{Endpoints: []Endpoint{dead}, DialTimeout: 1 * time.Second})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if alloc, err := mgr.Allocate(); err == nil {
		_ = alloc.Close()
		t.Fatal("allocation against a dead endpoint succeeded — impossible")
	}
}

// TestNewRelayManagerValidation covers constructor input validation.
func TestNewRelayManagerValidation(t *testing.T) {
	if _, err := NewRelayManager(Config{}); err == nil {
		t.Fatal("expected error for empty endpoints")
	}
	if _, err := NewRelayManager(Config{Endpoints: []Endpoint{{Address: "x:1"}}}); err == nil {
		t.Fatal("expected error for missing username/password")
	}
}
