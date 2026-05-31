package netutil

import (
	"net"
	"testing"
)

func TestParseCIDR(t *testing.T) {
	c, err := ParseCIDR("192.168.1.10/24")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	if c.Network().String() != "192.168.1.0" {
		t.Fatalf("network = %s, want 192.168.1.0", c.Network())
	}
	if c.Broadcast().String() != "192.168.1.255" {
		t.Fatalf("broadcast = %s, want 192.168.1.255", c.Broadcast())
	}
	if ones, _ := c.IPNet.Mask.Size(); ones != 24 {
		t.Fatalf("prefix = %d, want 24", ones)
	}
}

func TestParseCIDR_Invalid(t *testing.T) {
	if _, err := ParseCIDR("not-a-cidr"); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
	if _, err := ParseCIDR("192.168.1.0/33"); err == nil {
		t.Fatal("expected error for invalid prefix length")
	}
}

func TestContainsIP_KnownVectors(t *testing.T) {
	c, err := ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.254", true},
		{"11.0.0.1", false},
		{"9.255.255.255", false},
		{"192.168.0.1", false},
	}
	for _, tc := range cases {
		got := c.Contains(net.ParseIP(tc.ip))
		if got != tc.want {
			t.Errorf("Contains(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestNetworkBroadcast_KnownVectors(t *testing.T) {
	cases := []struct {
		cidr      string
		network   string
		broadcast string
	}{
		{"192.168.1.130/26", "192.168.1.128", "192.168.1.191"},
		{"172.16.5.4/30", "172.16.5.4", "172.16.5.7"},
		{"10.1.2.3/32", "10.1.2.3", "10.1.2.3"},
		{"203.0.113.0/24", "203.0.113.0", "203.0.113.255"},
	}
	for _, tc := range cases {
		c, err := ParseCIDR(tc.cidr)
		if err != nil {
			t.Fatalf("ParseCIDR(%s): %v", tc.cidr, err)
		}
		if c.Network().String() != tc.network {
			t.Errorf("%s network = %s, want %s", tc.cidr, c.Network(), tc.network)
		}
		if c.Broadcast().String() != tc.broadcast {
			t.Errorf("%s broadcast = %s, want %s", tc.cidr, c.Broadcast(), tc.broadcast)
		}
	}
}

func TestNextIP_KnownVectors(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"192.168.1.1", "192.168.1.2"},
		{"192.168.1.255", "192.168.2.0"}, // carry across octet
		{"10.255.255.255", "11.0.0.0"},   // multi-octet carry
		{"0.0.0.0", "0.0.0.1"},
	}
	for _, tc := range cases {
		got := NextIP(net.ParseIP(tc.in))
		if got.String() != tc.want {
			t.Errorf("NextIP(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestNextIP_OverflowWrap(t *testing.T) {
	// 255.255.255.255 wraps to 0.0.0.0
	got := NextIP(net.ParseIP("255.255.255.255"))
	if got.String() != "0.0.0.0" {
		t.Fatalf("NextIP(255.255.255.255) = %s, want 0.0.0.0", got)
	}
}

func TestHosts_BoundedEnumeration(t *testing.T) {
	c, err := ParseCIDR("192.168.1.0/29") // 8 addrs: .0 net, .7 bcast → 6 hosts
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := c.Hosts(100)
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	want := []string{
		"192.168.1.1", "192.168.1.2", "192.168.1.3",
		"192.168.1.4", "192.168.1.5", "192.168.1.6",
	}
	if len(hosts) != len(want) {
		t.Fatalf("got %d hosts %v, want %d", len(hosts), hosts, len(want))
	}
	for i := range want {
		if hosts[i].String() != want[i] {
			t.Errorf("host[%d] = %s, want %s", i, hosts[i], want[i])
		}
	}
}

func TestHosts_LimitEnforced(t *testing.T) {
	c, err := ParseCIDR("10.0.0.0/8") // ~16M hosts
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Hosts(1000); err == nil {
		t.Fatal("expected error when host count exceeds limit")
	}
	// A /31 has no usable hosts in the classic model (network+broadcast only).
	c31, _ := ParseCIDR("10.0.0.0/31")
	h, err := c31.Hosts(10)
	if err != nil {
		t.Fatalf("Hosts(/31): %v", err)
	}
	if len(h) != 0 {
		t.Fatalf("/31 should yield 0 classic hosts, got %v", h)
	}
}

// --- Mutation tests ---

// Mutation: Contains off-by-one — boundary IPs must be classified correctly.
func TestContainsIP_Boundary_Mutation(t *testing.T) {
	c, _ := ParseCIDR("192.168.1.128/26") // .128 - .191
	if !c.Contains(net.ParseIP("192.168.1.128")) {
		t.Error("network address must be contained")
	}
	if !c.Contains(net.ParseIP("192.168.1.191")) {
		t.Error("broadcast address must be contained")
	}
	if c.Contains(net.ParseIP("192.168.1.127")) {
		t.Error("address just below range must NOT be contained")
	}
	if c.Contains(net.ParseIP("192.168.1.192")) {
		t.Error("address just above range must NOT be contained")
	}
}

// Mutation: NextIP must change the address (not return same / not skip).
func TestNextIP_Increment_Mutation(t *testing.T) {
	in := net.ParseIP("192.168.1.1")
	out := NextIP(in)
	if out.String() == "192.168.1.1" {
		t.Fatal("NextIP must not return the same address")
	}
	if out.String() != "192.168.1.2" {
		t.Fatalf("NextIP must increment by exactly 1, got %s", out)
	}
	// Ensure input is not mutated in place.
	if in.String() != "192.168.1.1" {
		t.Fatalf("NextIP must not mutate its input, input became %s", in)
	}
}

// Mutation: broadcast vs network must not be swapped/equal for multi-host nets.
func TestNetworkNotEqualBroadcast_Mutation(t *testing.T) {
	c, _ := ParseCIDR("172.16.5.4/30")
	if c.Network().String() == c.Broadcast().String() {
		t.Fatal("network and broadcast must differ for a /30")
	}
	if c.Network().String() != "172.16.5.4" {
		t.Fatalf("network wrong: %s", c.Network())
	}
	if c.Broadcast().String() != "172.16.5.7" {
		t.Fatalf("broadcast wrong: %s", c.Broadcast())
	}
}
