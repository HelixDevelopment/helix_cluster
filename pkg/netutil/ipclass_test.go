package netutil

import (
	"net"
	"testing"
)

func TestIsPrivate_RFC1918_KnownVectors(t *testing.T) {
	private := []string{
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.0.1", "192.168.255.255",
		"fd00::1", "fdff::ffff", // ULA fc00::/7
	}
	for _, s := range private {
		if !IsPrivateIP(net.ParseIP(s)) {
			t.Errorf("IsPrivateIP(%s) = false, want true", s)
		}
	}
}

func TestIsPrivate_PublicNotPrivate_Mutation(t *testing.T) {
	public := []string{
		"8.8.8.8", "1.1.1.1",
		"172.15.0.1", "172.32.0.1", // just outside 172.16/12
		"192.167.0.1", "192.169.0.1",
		"9.255.255.255", "11.0.0.0",
		"2001:4860:4860::8888", // public IPv6
	}
	for _, s := range public {
		if IsPrivateIP(net.ParseIP(s)) {
			t.Errorf("IsPrivateIP(%s) = true, want false (public address)", s)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	if !IsLoopbackIP(net.ParseIP("127.0.0.1")) {
		t.Error("127.0.0.1 must be loopback")
	}
	if !IsLoopbackIP(net.ParseIP("::1")) {
		t.Error("::1 must be loopback")
	}
	// Mutation pair: non-loopback must be false.
	if IsLoopbackIP(net.ParseIP("10.0.0.1")) {
		t.Error("10.0.0.1 must NOT be loopback")
	}
	if IsLoopbackIP(net.ParseIP("128.0.0.1")) {
		t.Error("128.0.0.1 must NOT be loopback")
	}
}

func TestIsLinkLocal(t *testing.T) {
	if !IsLinkLocalIP(net.ParseIP("169.254.1.1")) {
		t.Error("169.254.1.1 must be link-local")
	}
	if !IsLinkLocalIP(net.ParseIP("fe80::1")) {
		t.Error("fe80::1 must be link-local")
	}
	// Mutation pair.
	if IsLinkLocalIP(net.ParseIP("169.253.1.1")) {
		t.Error("169.253.1.1 must NOT be link-local")
	}
	if IsLinkLocalIP(net.ParseIP("10.0.0.1")) {
		t.Error("10.0.0.1 must NOT be link-local")
	}
}

func TestIPVersion(t *testing.T) {
	if !IsIPv4(net.ParseIP("192.168.0.1")) {
		t.Error("192.168.0.1 must be IPv4")
	}
	if IsIPv6(net.ParseIP("192.168.0.1")) {
		t.Error("192.168.0.1 must NOT be classified IPv6")
	}
	if !IsIPv6(net.ParseIP("2001:db8::1")) {
		t.Error("2001:db8::1 must be IPv6")
	}
	if IsIPv4(net.ParseIP("2001:db8::1")) {
		t.Error("2001:db8::1 must NOT be classified IPv4")
	}
}

// Mutation: 172.16/12 boundary must be exact (not 172.x for all x).
func TestIsPrivate_172Boundary_Mutation(t *testing.T) {
	if !IsPrivateIP(net.ParseIP("172.16.0.0")) {
		t.Error("172.16.0.0 is the start of the private range")
	}
	if !IsPrivateIP(net.ParseIP("172.31.255.255")) {
		t.Error("172.31.255.255 is the end of the private range")
	}
	if IsPrivateIP(net.ParseIP("172.15.255.255")) {
		t.Error("172.15.255.255 is just below the private range")
	}
	if IsPrivateIP(net.ParseIP("172.32.0.0")) {
		t.Error("172.32.0.0 is just above the private range")
	}
}

// Mutation: ULA fc00::/7 must include fc.. and fd.. but exclude fe.. (link-local).
func TestIsPrivate_ULA_Mutation(t *testing.T) {
	if !IsPrivateIP(net.ParseIP("fc00::1")) {
		t.Error("fc00::1 is ULA private")
	}
	if !IsPrivateIP(net.ParseIP("fd12:3456::1")) {
		t.Error("fd12:3456::1 is ULA private")
	}
	if IsPrivateIP(net.ParseIP("fe80::1")) {
		t.Error("fe80::1 is link-local, not ULA private")
	}
}
