package netutil

import (
	"fmt"
	"net"
	"testing"
)

// TestFreeTCPPort_RealBindAndConnect proves the returned port is actually
// usable: we bind a listener to it and make a real TCP connection through it.
func TestFreeTCPPort_RealBindAndConnect(t *testing.T) {
	port, err := FreeTCPPort()
	if err != nil {
		t.Fatalf("FreeTCPPort: %v", err)
	}
	if !IsValidPort(port) {
		t.Fatalf("FreeTCPPort returned out-of-range port %d", port)
	}

	// REAL: bind a listener on the chosen port — proves it was free/reusable.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("could not re-bind reported free TCP port %d: %v", port, err)
	}
	defer ln.Close()

	// REAL: accept side.
	accepted := make(chan string, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			accepted <- ""
			return
		}
		defer conn.Close()
		buf := make([]byte, 5)
		n, _ := conn.Read(buf)
		accepted <- string(buf[:n])
	}()

	// REAL: client side actually connects and sends data.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial to free port failed: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if got := <-accepted; got != "hello" {
		t.Fatalf("end-to-end data mismatch: got %q want %q", got, "hello")
	}
}

// TestFreeTCPPort_Distinct proves multiple calls hand out usable ports and that
// the helper does not keep the socket bound (otherwise re-binding would fail).
func TestFreeTCPPort_Distinct(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 5; i++ {
		p, err := FreeTCPPort()
		if err != nil {
			t.Fatalf("FreeTCPPort #%d: %v", i, err)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			t.Fatalf("port %d not actually free: %v", p, err)
		}
		ln.Close()
		seen[p] = true
	}
	if len(seen) == 0 {
		t.Fatal("no ports allocated")
	}
}

// TestFreeUDPPort_RealBindAndSend proves the UDP port is usable end-to-end.
func TestFreeUDPPort_RealBindAndSend(t *testing.T) {
	port, err := FreeUDPPort()
	if err != nil {
		t.Fatalf("FreeUDPPort: %v", err)
	}
	if !IsValidPort(port) {
		t.Fatalf("FreeUDPPort returned out-of-range port %d", port)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	srv, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("could not re-bind reported free UDP port %d: %v", port, err)
	}
	defer srv.Close()

	cli, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer cli.Close()
	if _, err := cli.Write([]byte("ping")); err != nil {
		t.Fatalf("udp write: %v", err)
	}
	buf := make([]byte, 8)
	n, _, err := srv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("udp read: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("udp payload mismatch: got %q", string(buf[:n]))
	}
}

// --- Mutation tests ---

// Mutation: if FreeTCPPort returned 0 or an invalid port, this fails.
func TestFreeTCPPort_NonZero_Mutation(t *testing.T) {
	p, err := FreeTCPPort()
	if err != nil {
		t.Fatalf("FreeTCPPort: %v", err)
	}
	if p == 0 {
		t.Fatal("FreeTCPPort must not return 0 (the wildcard placeholder)")
	}
	if !IsValidPort(p) {
		t.Fatalf("port %d out of valid range", p)
	}
}

// Mutation: if FreeUDPPort returned 0 or an invalid port, this fails.
func TestFreeUDPPort_NonZero_Mutation(t *testing.T) {
	p, err := FreeUDPPort()
	if err != nil {
		t.Fatalf("FreeUDPPort: %v", err)
	}
	if p == 0 {
		t.Fatal("FreeUDPPort must not return 0")
	}
	if !IsValidPort(p) {
		t.Fatalf("port %d out of valid range", p)
	}
}
