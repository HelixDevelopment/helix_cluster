package schema

// Unit tests (package-internal) for the psql connection-argument builder.
//
// D11 regression coverage: the connection arguments handed to psql MUST be
// derived from the resolved PSQLConfig — honoring the DSN's user/host/port/db —
// and MUST NOT hardcode "-U postgres". Against the live container (role "helix",
// no "postgres" role) the old "-U postgres -d helix" form failed with
// `role "postgres" does not exist`.

import (
	"strings"
	"testing"
)

// assertNoHardcodedPostgres fails if the args contain the "-U postgres" pair.
func assertNoHardcodedPostgres(t *testing.T, args []string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-U" && args[i+1] == "postgres" {
			t.Fatalf("psql args hardcode '-U postgres' (D11 defect): %v", args)
		}
	}
}

// flagValue returns the value following flag, or "" if absent.
func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestPsqlConnArgs_LocalDSN_PassesDSNPositionally(t *testing.T) {
	cfg := PSQLConfig{
		UseContainer: false,
		DSN:          "postgres://helix:helix@127.0.0.1:5432/helix?sslmode=disable",
		Database:     "helix",
	}
	args := psqlConnArgs(cfg)
	assertNoHardcodedPostgres(t, args)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, cfg.DSN) {
		t.Fatalf("local DSN not passed to psql; args=%v", args)
	}
}

func TestPsqlConnArgs_ContainerWithDSN_UsesDSNNotPostgres(t *testing.T) {
	cfg := PSQLConfig{
		UseContainer:  true,
		ContainerName: "helix-postgres-primary",
		DSN:           "postgres://helix:helix@127.0.0.1:5432/helix?sslmode=disable",
		Database:      "helix",
	}
	args := psqlConnArgs(cfg)
	assertNoHardcodedPostgres(t, args)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, cfg.DSN) {
		t.Fatalf("container DSN not passed to psql; args=%v", args)
	}
}

func TestPsqlConnArgs_ContainerNoDSN_DerivesUserAndDB(t *testing.T) {
	// Without a DSN, container mode must NOT default to "-U postgres". It must
	// use the resolved Database and User — never a hardcoded role that may not
	// exist in the target.
	cfg := PSQLConfig{
		UseContainer:  true,
		ContainerName: "helix-postgres-primary",
		DSN:           "",
		Database:      "helix",
		User:          "helix",
	}
	args := psqlConnArgs(cfg)
	assertNoHardcodedPostgres(t, args)
	if got := flagValue(args, "-U"); got != "helix" {
		t.Fatalf("-U = %q, want helix (derived from cfg.User)", got)
	}
	if got := flagValue(args, "-d"); got != "helix" {
		t.Fatalf("-d = %q, want helix (derived from cfg.Database)", got)
	}
}

func TestParseDSNUserDB(t *testing.T) {
	cases := []struct {
		dsn      string
		wantUser string
		wantDB   string
	}{
		{"postgres://helix:helix@127.0.0.1:5432/helix?sslmode=disable", "helix", "helix"},
		{"postgres://alice@db.example.com:6543/mydb", "alice", "mydb"},
		{"postgresql://bob:pw@host/otherdb", "bob", "otherdb"},
	}
	for _, c := range cases {
		u, db := parseDSNUserDB(c.dsn)
		if u != c.wantUser {
			t.Errorf("parseDSNUserDB(%q) user = %q, want %q", c.dsn, u, c.wantUser)
		}
		if db != c.wantDB {
			t.Errorf("parseDSNUserDB(%q) db = %q, want %q", c.dsn, db, c.wantDB)
		}
	}
}
