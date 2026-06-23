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

// TestParseDSNUserDB_NonURIInputsReturnEmpty exercises the three early-return
// branches of parseDSNUserDB that each yield ("", ""): an empty DSN, an
// unparseable URI, and a parseable-but-non-postgres scheme (e.g. a libpq
// keyword/value DSN). These are the branches whose `return "", ""` statements
// survived mutation (schema.go.28/31/32) because no test asserted the
// empty-result contract — and the caller (ResolvePSQLConfig) relies on it to
// fall back to PGUSER/PGDATABASE/"helix" (D11).
func TestParseDSNUserDB_NonURIInputsReturnEmpty(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		// schema.go.28: empty DSN must short-circuit to ("", "").
		{"empty", ""},
		// schema.go.31: url.Parse error (invalid percent-escape) → ("", "").
		{"unparseable", "postgres://%zz@host/db"},
		// schema.go.32: parseable URL but non-postgres scheme → ("", "").
		{"wrong scheme", "mysql://alice:pw@host:3306/mydb"},
		// libpq keyword/value DSN — url.Parse yields a scheme that is never
		// "postgres", so the scheme guard must reject it (also schema.go.32).
		{"libpq kv", "host=localhost user=alice dbname=mydb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, db := parseDSNUserDB(c.dsn)
			if u != "" || db != "" {
				t.Fatalf("parseDSNUserDB(%q) = (%q, %q), want (\"\", \"\")", c.dsn, u, db)
			}
		})
	}
}

// TestResolvePSQLConfig_NoDSN_EnvAndDefaults pins the no-DSN container path of
// ResolvePSQLConfig, covering the four assignment branches that survived
// mutation: PGDATABASE/PGUSER overrides (schema.go.21/26 setting the value) AND
// the "helix" fallbacks when those env vars are unset (schema.go.25/27). Without
// a DSN the resolved User/Database must come from the environment, defaulting to
// "helix" — never "postgres" (D11).
func TestResolvePSQLConfig_NoDSN_EnvAndDefaults(t *testing.T) {
	// Clear every input env var so the test is hermetic regardless of host env.
	for _, k := range []string{"HELIX_PG_CONTAINER", "PSQL_DSN", "DATABASE_URL", "PGDATABASE", "PGUSER"} {
		t.Setenv(k, "")
	}

	t.Run("explicit PGUSER/PGDATABASE used", func(t *testing.T) {
		t.Setenv("HELIX_PG_CONTAINER", "helix-pg")
		t.Setenv("PGUSER", "alice")
		t.Setenv("PGDATABASE", "appdb")
		cfg, ok := ResolvePSQLConfig()
		if !ok {
			t.Fatal("ResolvePSQLConfig returned available=false with a container set")
		}
		if cfg.User != "alice" {
			t.Fatalf("User = %q, want alice (from PGUSER)", cfg.User)
		}
		if cfg.Database != "appdb" {
			t.Fatalf("Database = %q, want appdb (from PGDATABASE)", cfg.Database)
		}
	})

	t.Run("defaults to helix when env unset", func(t *testing.T) {
		t.Setenv("HELIX_PG_CONTAINER", "helix-pg")
		t.Setenv("PGUSER", "")     // forces the user=="" → "helix" fallback (schema.go.27)
		t.Setenv("PGDATABASE", "") // forces the db=="" → "helix" fallback (schema.go.25)
		cfg, ok := ResolvePSQLConfig()
		if !ok {
			t.Fatal("ResolvePSQLConfig returned available=false with a container set")
		}
		if cfg.User != "helix" {
			t.Fatalf("User = %q, want helix (default)", cfg.User)
		}
		if cfg.Database != "helix" {
			t.Fatalf("Database = %q, want helix (default)", cfg.Database)
		}
	})
}

// TestResolvePSQLConfig_DSNDerivesUserDB exercises the DSN-derived default
// branches (schema.go.21 db=dsnDB, schema.go.26 user=dsnUser): when no explicit
// PGUSER/PGDATABASE is set, the user/dbname must be lifted from the DSN itself.
func TestResolvePSQLConfig_DSNDerivesUserDB(t *testing.T) {
	for _, k := range []string{"HELIX_PG_CONTAINER", "PSQL_DSN", "DATABASE_URL", "PGDATABASE", "PGUSER"} {
		t.Setenv(k, "")
	}
	t.Setenv("PSQL_DSN", "postgres://dsnuser:pw@db.example.com:5432/dsndb?sslmode=disable")
	cfg, ok := ResolvePSQLConfig()
	if !ok {
		t.Fatal("ResolvePSQLConfig returned available=false with a DSN set")
	}
	if cfg.UseContainer {
		t.Fatal("UseContainer = true, want false (DSN-only, no container)")
	}
	if cfg.User != "dsnuser" {
		t.Fatalf("User = %q, want dsnuser (derived from DSN, schema.go.26)", cfg.User)
	}
	if cfg.Database != "dsndb" {
		t.Fatalf("Database = %q, want dsndb (derived from DSN, schema.go.21)", cfg.Database)
	}
	if cfg.DSN == "" {
		t.Fatal("DSN must be threaded into the config verbatim")
	}
}

// TestResolvePSQLConfig_DATABASE_URLFallback proves DATABASE_URL is consulted
// when PSQL_DSN is unset (the dsn=="" → os.Getenv("DATABASE_URL") branch), and
// that its user/db are then DSN-derived.
func TestResolvePSQLConfig_DATABASE_URLFallback(t *testing.T) {
	for _, k := range []string{"HELIX_PG_CONTAINER", "PSQL_DSN", "DATABASE_URL", "PGDATABASE", "PGUSER"} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://fb_user:pw@host:5432/fb_db")
	cfg, ok := ResolvePSQLConfig()
	if !ok {
		t.Fatal("ResolvePSQLConfig returned available=false with DATABASE_URL set")
	}
	if cfg.User != "fb_user" || cfg.Database != "fb_db" {
		t.Fatalf("got User=%q Database=%q, want fb_user/fb_db from DATABASE_URL", cfg.User, cfg.Database)
	}
}

// TestPsqlConnArgs_NoDSN_EmptyFieldsDefaultToHelix kills the empty-field
// fallback mutants in psqlConnArgs (schema.go.33 user="helix", schema.go.36
// db="helix"): with a DSN-less config whose User and Database are BOTH empty,
// the produced -U / -d values must each default to "helix".
func TestPsqlConnArgs_NoDSN_EmptyFieldsDefaultToHelix(t *testing.T) {
	cfg := PSQLConfig{UseContainer: false, DSN: "", User: "", Database: ""}
	args := psqlConnArgs(cfg)
	assertNoHardcodedPostgres(t, args)
	if got := flagValue(args, "-U"); got != "helix" {
		t.Fatalf("-U = %q, want helix (empty User fallback, schema.go.33)", got)
	}
	if got := flagValue(args, "-d"); got != "helix" {
		t.Fatalf("-d = %q, want helix (empty Database fallback, schema.go.36)", got)
	}
}

// TestBuildPsqlArgs_LocalAndContainer pins the full (name, cmdArgs) assembly of
// buildPsqlArgs for both transports, killing the structural mutants in that
// function (schema.go.110-114): the local executable name and arg slice, and
// the container name, podman-exec prefix, and trailing extraArgs.
func TestBuildPsqlArgs_LocalAndContainer(t *testing.T) {
	t.Run("local transport", func(t *testing.T) {
		cfg := PSQLConfig{
			UseContainer: false,
			DSN:          "postgres://helix:helix@127.0.0.1:5432/helix?sslmode=disable",
		}
		name, args := buildPsqlArgs(cfg, "-c", "SELECT 1")
		if name != "psql" {
			t.Fatalf("name = %q, want psql (schema.go.110)", name)
		}
		assertNoHardcodedPostgres(t, args)
		// conn portion (DSN + ON_ERROR_STOP) must come first, extraArgs appended.
		if args[0] != cfg.DSN {
			t.Fatalf("args[0] = %q, want the DSN first (conn portion)", args[0])
		}
		if got := args[len(args)-2:]; got[0] != "-c" || got[1] != "SELECT 1" {
			t.Fatalf("extraArgs not appended last: tail=%v (schema.go.111)", got)
		}
	})

	t.Run("container transport", func(t *testing.T) {
		cfg := PSQLConfig{
			UseContainer:  true,
			ContainerName: "helix-pg-primary",
			DSN:           "postgres://helix:helix@127.0.0.1:5432/helix?sslmode=disable",
		}
		name, args := buildPsqlArgs(cfg, "-c", "SELECT 1")
		if name != "podman" {
			t.Fatalf("name = %q, want podman (schema.go.112)", name)
		}
		// schema.go.113: the "exec -i <container> psql" prefix must be present and
		// in order at the head of cmdArgs.
		wantPrefix := []string{"exec", "-i", "helix-pg-primary", "psql"}
		for i, w := range wantPrefix {
			if i >= len(args) || args[i] != w {
				t.Fatalf("cmdArgs prefix = %v, want leading %v (schema.go.113)", args, wantPrefix)
			}
		}
		// The DSN (conn portion) must follow the exec prefix.
		if !containsArg(args, cfg.DSN) {
			t.Fatalf("DSN missing from container args: %v", args)
		}
		// schema.go.114: extraArgs ("-c", "SELECT 1") must be appended at the tail.
		if got := args[len(args)-2:]; got[0] != "-c" || got[1] != "SELECT 1" {
			t.Fatalf("extraArgs not appended last: tail=%v (schema.go.114)", got)
		}
	})
}

// containsArg reports whether want appears anywhere in args.
func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
