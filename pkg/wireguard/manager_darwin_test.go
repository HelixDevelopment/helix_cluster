//go:build darwin

package wireguard

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestManagerDarwinRealDatapath proves the CLAUDE-2 fix at the *Manager* level:
// two normally-configured (NON-NoOp) Managers on darwin must each bring up a
// REAL userspace WireGuard device via Start(), install each other as peers via
// AddPeer(), complete a genuine Noise handshake, and move an actual TCP payload
// THROUGH the encrypted tunnel.
//
// Before the fix, a non-NoOp Manager on darwin drove wgctrl.ConfigureDevice
// against a non-existent kernel device, so Start() returned an error and no
// real transport existed — the only non-erroring darwin path was NoOp (a
// CLAUDE-2 PASS-bluff). This test fails RED in that world (Start errors / no
// Net()) and passes GREEN once the Manager delegates to the userspace backend.
//
// Sink-side: the dial through the tunnel only succeeds after a real handshake.
func TestManagerDarwinRealDatapath(t *testing.T) {
	a := newRealManager(t, "10.77.0.1")
	b := newRealManager(t, "10.77.0.2")

	if err := a.mgr.AddPeer(&PeerConfig{
		PublicKey:           b.pub,
		AllowedIPs:          []string{"10.77.0.2/32"},
		Endpoint:            net.JoinHostPort("127.0.0.1", itoa(b.port)),
		PersistentKeepalive: 1,
	}); err != nil {
		t.Fatalf("A.AddPeer(B): %v", err)
	}
	if err := b.mgr.AddPeer(&PeerConfig{
		PublicKey:           a.pub,
		AllowedIPs:          []string{"10.77.0.1/32"},
		Endpoint:            net.JoinHostPort("127.0.0.1", itoa(a.port)),
		PersistentKeepalive: 1,
	}); err != nil {
		t.Fatalf("B.AddPeer(A): %v", err)
	}

	// TCP echo server INSIDE peer B's tunnel netstack.
	const echoPort = 7788
	bnet := b.mgr.UserspaceNet()
	if bnet == nil {
		t.Fatal("CLAUDE-2 STUB SUSPECT: B Manager has no userspace netstack — Start() did not bring up a real device")
	}
	ln, err := bnet.ListenTCP(&net.TCPAddr{IP: net.ParseIP("10.77.0.2"), Port: echoPort})
	if err != nil {
		t.Fatalf("listen inside B tunnel: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	anet := a.mgr.UserspaceNet()
	if anet == nil {
		t.Fatal("CLAUDE-2 STUB SUSPECT: A Manager has no userspace netstack — Start() did not bring up a real device")
	}

	want := []byte("helix-manager-userspace-roundtrip")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var conn net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err = anet.DialContextTCP(ctx, &net.TCPAddr{IP: net.ParseIP("10.77.0.2"), Port: echoPort})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial through tunnel never succeeded (handshake failed?): %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("payload mismatch through tunnel: got %q want %q", got, want)
	}
}

// realManagerEndpoint is a fully started, NON-NoOp Manager for the test.
type realManagerEndpoint struct {
	mgr  *Manager
	pub  string
	port int
}

// newRealManager constructs a normally-configured Manager (NoOp=false) on the
// given tunnel address and brings it up with Start(). On darwin this must stand
// up a real userspace device with no special test plumbing.
func newRealManager(t *testing.T, tunIP string) *realManagerEndpoint {
	t.Helper()
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	port := freeUDPPort(t)
	mgr, err := NewManager(&Config{
		InterfaceName: "wg-test",
		ListenPort:    port,
		PrivateKey:    priv,
		Address:       tunIP + "/32",
		MTU:           1420,
		// NoOp deliberately false: this is the real-operation path.
	})
	if err != nil {
		t.Fatalf("NewManager(%s): %v", tunIP, err)
	}
	if err := mgr.Start(); err != nil {
		t.Fatalf("Manager.Start(%s) on darwin must bring up a REAL userspace device, got: %v", tunIP, err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })
	return &realManagerEndpoint{mgr: mgr, pub: pub, port: port}
}

// itoa is a tiny local int->string to avoid importing strconv in the test.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
