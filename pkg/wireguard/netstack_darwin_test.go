//go:build darwin

package wireguard

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

// freeUDPPort grabs an ephemeral UDP port from the host kernel and releases it,
// returning a port number very likely to be free for the userspace WG bind.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// wgEndpoint is a fully wired userspace WG peer for the test: device + its
// tunnel address + its public key + its real UDP listen port on localhost.
type wgEndpoint struct {
	dev  *UserspaceDevice
	addr netip.Addr
	pub  string
	port int
}

// newEndpoint builds one userspace WG device on its own netstack TUN.
func newEndpoint(t *testing.T, tunIP string) *wgEndpoint {
	t.Helper()
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	addr := netip.MustParseAddr(tunIP)
	port := freeUDPPort(t)
	dev, err := NewUserspaceDevice([]netip.Addr{addr}, nil, 1420, port, priv)
	if err != nil {
		t.Fatalf("NewUserspaceDevice(%s): %v", tunIP, err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	return &wgEndpoint{dev: dev, addr: addr, pub: pub, port: port}
}

// TestWGNetstackUserspaceDatapath proves a REAL userspace WireGuard data path
// on darwin (no root): two in-process wireguard-go devices, each on its own
// gVisor netstack TUN, are configured as peers (keys + allowed-ips + localhost
// UDP endpoints over the userspace bind). A TCP echo server runs INSIDE peer
// B's tunnel stack; peer A dials B's tunnel address through its own tunnel
// stack and a payload round-trips. Success requires a genuine Noise handshake
// and encrypted transport between the two userspace devices.
//
// real_datapath=true: traffic actually traverses the encrypted WG tunnel.
func TestWGNetstackUserspaceDatapath(t *testing.T) {
	a := newEndpoint(t, "10.55.0.1")
	b := newEndpoint(t, "10.55.0.2")

	// Peer them over the userspace UDP bind on localhost.
	if err := a.dev.AddPeer(&PeerConfig{
		PublicKey:           b.pub,
		AllowedIPs:          []string{"10.55.0.2/32"},
		Endpoint:            netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(b.port)).String(),
		PersistentKeepalive: 1,
	}); err != nil {
		t.Fatalf("A.AddPeer(B): %v", err)
	}
	if err := b.dev.AddPeer(&PeerConfig{
		PublicKey:           a.pub,
		AllowedIPs:          []string{"10.55.0.1/32"},
		Endpoint:            netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(a.port)).String(),
		PersistentKeepalive: 1,
	}); err != nil {
		t.Fatalf("B.AddPeer(A): %v", err)
	}

	// TCP echo server bound INSIDE peer B's tunnel network stack.
	const echoPort = 5566
	ln, err := b.dev.Net().ListenTCP(&net.TCPAddr{IP: b.addr.AsSlice(), Port: echoPort})
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
		_, _ = io.Copy(conn, conn) // echo
	}()

	want := []byte("helix-userspace-wireguard-roundtrip")

	// Dial B's tunnel address FROM peer A's tunnel stack — forces a handshake
	// and pushes real encrypted transport packets over the WG tunnel.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var conn net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err = a.dev.Net().DialContextTCP(ctx, &net.TCPAddr{IP: b.addr.AsSlice(), Port: echoPort})
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
