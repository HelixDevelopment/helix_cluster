package main

import "testing"

// TestParsePeers covers the membership parser's happy path and its validation
// rejections (bad form, duplicate id/addr, empty), since the daemon's whole
// cross-process wiring depends on this string being parsed correctly.
func TestParsePeers(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		peers, servers, err := parsePeers("n1=127.0.0.1:7001, n2=127.0.0.1:7002 ,n3=127.0.0.1:7003")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(peers) != 3 || len(servers) != 3 {
			t.Fatalf("want 3 peers/servers, got %d/%d", len(peers), len(servers))
		}
		if peers[0].id != "n1" || peers[0].addr != "127.0.0.1:7001" {
			t.Fatalf("peer[0] = %+v", peers[0])
		}
		if string(servers[2].ID) != "n3" || string(servers[2].Address) != "127.0.0.1:7003" {
			t.Fatalf("server[2] = %+v", servers[2])
		}
	})

	for _, tc := range []struct{ name, spec string }{
		{"empty", ""},
		{"no-eq", "n1-127.0.0.1:7001"},
		{"empty-id", "=127.0.0.1:7001"},
		{"empty-addr", "n1="},
		{"dup-id", "n1=127.0.0.1:7001,n1=127.0.0.1:7002"},
		{"dup-addr", "n1=127.0.0.1:7001,n2=127.0.0.1:7001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parsePeers(tc.spec); err == nil {
				t.Fatalf("expected error for %q, got nil", tc.spec)
			}
		})
	}
}

// TestParseFlags verifies required-flag enforcement and the bind/peers
// consistency check (a node's --bind must equal the address recorded for its --id
// in --peers, and its --id must be present).
func TestParseFlags(t *testing.T) {
	t.Parallel()
	base := []string{
		"--id", "n1",
		"--data-dir", "/tmp/x",
		"--bind", "127.0.0.1:7001",
		"--admin", "127.0.0.1:8001",
		"--peers", "n1=127.0.0.1:7001,n2=127.0.0.1:7002",
	}
	cfg, err := parseFlags(base)
	if err != nil {
		t.Fatalf("valid flags rejected: %v", err)
	}
	if cfg.id != "n1" || cfg.bind != "127.0.0.1:7001" || len(cfg.servers) != 2 {
		t.Fatalf("bad cfg: %+v", cfg)
	}

	t.Run("missing-id", func(t *testing.T) {
		if _, err := parseFlags([]string{"--data-dir", "/tmp/x", "--bind", "127.0.0.1:7001", "--admin", "127.0.0.1:8001", "--peers", "n1=127.0.0.1:7001"}); err == nil {
			t.Fatal("expected error for missing --id")
		}
	})
	t.Run("id-not-in-peers", func(t *testing.T) {
		args := []string{"--id", "nX", "--data-dir", "/tmp/x", "--bind", "127.0.0.1:7001", "--admin", "127.0.0.1:8001", "--peers", "n1=127.0.0.1:7001"}
		if _, err := parseFlags(args); err == nil {
			t.Fatal("expected error: --id not in --peers")
		}
	})
	t.Run("bind-mismatch", func(t *testing.T) {
		args := []string{"--id", "n1", "--data-dir", "/tmp/x", "--bind", "127.0.0.1:9999", "--admin", "127.0.0.1:8001", "--peers", "n1=127.0.0.1:7001"}
		if _, err := parseFlags(args); err == nil {
			t.Fatal("expected error: --bind != peers addr")
		}
	})
}
