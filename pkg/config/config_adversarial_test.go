package config

// Adversarial, mutation-verified tests pinning the pkg/config contract.
//
// Centerpieces:
//   - DEFAULTS APPLIED: an omitted field receives its documented default, never
//     the Go zero value. Asserted field-by-field across SetDefaults, Load,
//     LoadFromFile, and LoadStrict.
//   - VALIDATION FAIL-CLOSED: every documented rule rejects an out-of-range /
//     invalid value and accepts a valid one; boundaries probed.
//
// These complement (do not duplicate) config_test.go / validate_test.go. Each
// test pins behavior the package actually implements; no invented rules.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (a) DEFAULTS — centerpiece. Every defaulted field, asserted explicitly.
// ---------------------------------------------------------------------------

// wantDefaults enumerates the documented defaults from SetDefaults so a dropped
// assignment is caught precisely (mutation guard).
func assertDocumentedDefaults(t *testing.T, c *Config) {
	t.Helper()
	if c.AppName != "helix-cluster" {
		t.Errorf("default AppName: want helix-cluster, got %q", c.AppName)
	}
	if c.Version != "0.1.0" {
		t.Errorf("default Version: want 0.1.0, got %q", c.Version)
	}
	if c.BindAddr != "0.0.0.0" {
		t.Errorf("default BindAddr: want 0.0.0.0, got %q", c.BindAddr)
	}
	if c.HTTPPort != 8080 {
		t.Errorf("default HTTPPort: want 8080, got %d", c.HTTPPort)
	}
	if c.GRPCPort != 9090 {
		t.Errorf("default GRPCPort: want 9090, got %d", c.GRPCPort)
	}
	if len(c.EtcdEndpoints) != 1 || c.EtcdEndpoints[0] != "localhost:2379" {
		t.Errorf("default EtcdEndpoints: want [localhost:2379], got %v", c.EtcdEndpoints)
	}
	if c.LogLevel != "info" {
		t.Errorf("default LogLevel: want info, got %q", c.LogLevel)
	}
	if c.DataDir != "./data" {
		t.Errorf("default DataDir: want ./data, got %q", c.DataDir)
	}
	if c.MetricsAddr != ":9091" {
		t.Errorf("default MetricsAddr: want :9091, got %q", c.MetricsAddr)
	}
	if c.ReadTimeout != 30*time.Second {
		t.Errorf("default ReadTimeout: want 30s, got %v", c.ReadTimeout)
	}
	if c.WriteTimeout != 30*time.Second {
		t.Errorf("default WriteTimeout: want 30s, got %v", c.WriteTimeout)
	}
	if c.MaxConns != 1000 {
		t.Errorf("default MaxConns: want 1000, got %d", c.MaxConns)
	}
	if c.Labels == nil {
		t.Error("default Labels: want non-nil empty map, got nil")
	}
}

func TestAdversarialSetDefaultsEveryField(t *testing.T) {
	c := &Config{}
	c.SetDefaults()
	assertDocumentedDefaults(t, c)
	// The default config must itself be valid — defaults are a usable baseline.
	if err := c.Validate(); err != nil {
		t.Errorf("all-defaults config must pass Validate, got: %v", err)
	}
	if err := c.ValidateAll(); err != nil {
		t.Errorf("all-defaults config must pass ValidateAll, got: %v", err)
	}
}

// LoadFromFile must apply defaults for fields the document OMITS (not zero them).
func TestAdversarialLoadFromFileOmittedFieldsKeepDefaults(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, file, body string
	}{
		{"yaml", "c.yaml", "app_name: only-name\nhttp_port: 1234\n"},
		{"json", "c.json", `{"app_name":"only-name","http_port":1234}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.file)
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			c, err := LoadFromFile(p)
			if err != nil {
				t.Fatalf("LoadFromFile: %v", err)
			}
			// Present fields take the file value (precedence over default).
			if c.AppName != "only-name" {
				t.Errorf("file value lost: AppName=%q", c.AppName)
			}
			if c.HTTPPort != 1234 {
				t.Errorf("file value lost: HTTPPort=%d", c.HTTPPort)
			}
			// Omitted fields MUST retain their documented defaults, not zero.
			if c.WriteTimeout != 30*time.Second {
				t.Errorf("omitted WriteTimeout zeroed: %v (want 30s default)", c.WriteTimeout)
			}
			if c.ReadTimeout != 30*time.Second {
				t.Errorf("omitted ReadTimeout zeroed: %v", c.ReadTimeout)
			}
			if c.GRPCPort != 9090 {
				t.Errorf("omitted GRPCPort zeroed: %d (want 9090)", c.GRPCPort)
			}
			if c.MaxConns != 1000 {
				t.Errorf("omitted MaxConns zeroed: %d (want 1000)", c.MaxConns)
			}
			if len(c.EtcdEndpoints) != 1 || c.EtcdEndpoints[0] != "localhost:2379" {
				t.Errorf("omitted EtcdEndpoints zeroed: %v", c.EtcdEndpoints)
			}
			if c.MetricsAddr != ":9091" {
				t.Errorf("omitted MetricsAddr zeroed: %q", c.MetricsAddr)
			}
			if c.LogLevel != "info" {
				t.Errorf("omitted LogLevel zeroed: %q", c.LogLevel)
			}
		})
	}
}

// LoadStrict with all relevant vars explicitly EMPTY ("" == unset) must apply
// every default and pass validation.
func TestAdversarialLoadStrictAllDefaults(t *testing.T) {
	for _, k := range []string{
		"HELIX_APP_NAME", "HELIX_VERSION", "HELIX_BIND_ADDR", "HELIX_HTTP_PORT",
		"HELIX_GRPC_PORT", "HELIX_ETCD_ENDPOINTS", "HELIX_LOG_LEVEL",
		"HELIX_DATA_DIR", "HELIX_METRICS_ADDR", "HELIX_READ_TIMEOUT",
		"HELIX_WRITE_TIMEOUT", "HELIX_MAX_CONNS", "HELIX_MAX_BODY_BYTES",
		"HELIX_DEBUG",
	} {
		t.Setenv(k, "")
	}
	c, err := LoadStrict()
	if err != nil {
		t.Fatalf("LoadStrict all-defaults: %v", err)
	}
	assertDocumentedDefaults(t, c)
}

// ---------------------------------------------------------------------------
// (b) VALIDATION FAIL-CLOSED — centerpiece. Each rule: invalid rejected,
//     boundary accepted. Probes both Validate and ValidateAll.
// ---------------------------------------------------------------------------

// validBase returns a config that passes validation, so each test can perturb a
// single field and prove that field alone flips validity.
func validBase() *Config {
	c := &Config{AppName: "ok"}
	c.SetDefaults()
	return c
}

func TestAdversarialValidatePortBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"zero-rejected", 0, true},
		{"negative-rejected", -1, true},
		{"low-boundary-1-ok", 1, false},
		{"high-boundary-65535-ok", 65535, false},
		{"over-65536-rejected", 65536, true},
		{"huge-rejected", 99999, true},
	}
	for _, tc := range cases {
		t.Run("http/"+tc.name, func(t *testing.T) {
			c := validBase()
			c.HTTPPort = tc.port
			gotErr := c.Validate() != nil
			if gotErr != tc.wantErr {
				t.Errorf("HTTPPort=%d: Validate err=%v want err=%v", tc.port, gotErr, tc.wantErr)
			}
			// ValidateAll must agree on the same rule (with GRPC kept distinct).
			c2 := validBase()
			c2.HTTPPort = tc.port
			if tc.port == c2.GRPCPort { // avoid the http==grpc clash masking the port rule
				c2.GRPCPort = 1
			}
			if (c2.ValidateAll() != nil) != tc.wantErr {
				t.Errorf("HTTPPort=%d: ValidateAll disagrees", tc.port)
			}
		})
	}
}

func TestAdversarialValidateGRPCPortRejected(t *testing.T) {
	c := validBase()
	c.GRPCPort = 70000
	if err := c.Validate(); err == nil {
		t.Error("GRPCPort=70000 must be rejected")
	}
}

func TestAdversarialValidateLogLevelEnum(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "INFO", "Error"} {
		c := validBase()
		c.LogLevel = lvl
		if err := c.Validate(); err != nil {
			t.Errorf("valid log level %q rejected: %v", lvl, err)
		}
	}
	for _, lvl := range []string{"verbose", "trace", "loud", "", "warning"} {
		c := validBase()
		c.LogLevel = lvl
		if err := c.Validate(); err == nil {
			t.Errorf("invalid log level %q must be rejected", lvl)
		}
		if err := c.ValidateAll(); err == nil {
			t.Errorf("invalid log level %q must be rejected by ValidateAll", lvl)
		}
	}
}

func TestAdversarialValidateEtcdEndpoints(t *testing.T) {
	// Empty list rejected.
	c := validBase()
	c.EtcdEndpoints = nil
	if err := c.Validate(); err == nil {
		t.Error("empty EtcdEndpoints must be rejected")
	}
	// A blank element among valid ones must be rejected (fail-closed).
	c = validBase()
	c.EtcdEndpoints = []string{"a:1", "   ", "b:2"}
	if err := c.Validate(); err == nil {
		t.Error("blank etcd endpoint must be rejected")
	}
	if err := c.ValidateAll(); err == nil {
		t.Error("blank etcd endpoint must be rejected by ValidateAll")
	}
}

func TestAdversarialValidateTimeoutsAndConns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Config)
	}{
		{"read-timeout-zero", func(c *Config) { c.ReadTimeout = 0 }},
		{"read-timeout-negative", func(c *Config) { c.ReadTimeout = -1 }},
		{"write-timeout-zero", func(c *Config) { c.WriteTimeout = 0 }},
		{"max-conns-zero", func(c *Config) { c.MaxConns = 0 }},
		{"max-conns-negative", func(c *Config) { c.MaxConns = -5 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			tc.apply(c)
			if err := c.Validate(); err == nil {
				t.Errorf("%s must be rejected by Validate", tc.name)
			}
			if err := c.ValidateAll(); err == nil {
				t.Errorf("%s must be rejected by ValidateAll", tc.name)
			}
		})
	}
}

func TestAdversarialValidateTLSPairing(t *testing.T) {
	// Cert without key -> reject; key without cert -> reject; both -> ok; neither -> ok.
	for _, tc := range []struct {
		name      string
		cert, key string
		wantErr   bool
	}{
		{"cert-only", "/c.pem", "", true},
		{"key-only", "", "/k.pem", true},
		{"both", "/c.pem", "/k.pem", false},
		{"neither", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			c.TLSCertPath, c.TLSKeyPath = tc.cert, tc.key
			if (c.Validate() != nil) != tc.wantErr {
				t.Errorf("%s: Validate mismatch (wantErr=%v)", tc.name, tc.wantErr)
			}
			if (c.ValidateAll() != nil) != tc.wantErr {
				t.Errorf("%s: ValidateAll mismatch (wantErr=%v)", tc.name, tc.wantErr)
			}
		})
	}
}

// ValidateAll has an ADDITIONAL rule beyond Validate: http and grpc port must
// differ. Pin it (and confirm the boundary where they differ passes).
func TestAdversarialValidateAllPortClash(t *testing.T) {
	c := validBase()
	c.HTTPPort = 7000
	c.GRPCPort = 7000
	if err := c.ValidateAll(); err == nil {
		t.Error("ValidateAll must reject identical http/grpc ports")
	} else if !strings.Contains(err.Error(), "differ") {
		t.Errorf("clash error should explain the rule, got %q", err.Error())
	}
	// Distinct ports pass.
	c.GRPCPort = 7001
	if err := c.ValidateAll(); err != nil {
		t.Errorf("distinct ports must pass ValidateAll: %v", err)
	}
}

// ValidateAll-only field rule: MaxBodyBytes must not be negative; zero allowed.
func TestAdversarialValidateAllMaxBodyBytes(t *testing.T) {
	c := validBase()
	c.MaxBodyBytes = -1
	if err := c.ValidateAll(); err == nil {
		t.Error("negative MaxBodyBytes must be rejected")
	}
	c.MaxBodyBytes = 0
	if err := c.ValidateAll(); err != nil {
		t.Errorf("zero MaxBodyBytes (unlimited) must be allowed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (c) PRECEDENCE — explicit value overrides default; Merge order pinned.
// ---------------------------------------------------------------------------

func TestAdversarialEnvOverridesDefault(t *testing.T) {
	t.Setenv("HELIX_HTTP_PORT", "3333")
	t.Setenv("HELIX_LOG_LEVEL", "warn")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPPort != 3333 {
		t.Errorf("env must override default: HTTPPort=%d", c.HTTPPort)
	}
	if c.LogLevel != "warn" {
		t.Errorf("env must override default: LogLevel=%q", c.LogLevel)
	}
	// Unset field keeps default — precedence holds per-field.
	if c.GRPCPort != 9090 {
		t.Errorf("unset GRPCPort must keep default, got %d", c.GRPCPort)
	}
}

// Merge: non-zero from other wins; zero from other is ignored (keeps base) —
// EXCEPT Debug, which is unconditionally taken from other. Pin both halves.
func TestAdversarialMergePrecedence(t *testing.T) {
	base := validBase()
	base.AppName = "base"
	base.HTTPPort = 8080
	base.Debug = true

	other := &Config{AppName: "", HTTPPort: 0, Debug: false} // all zero
	base.Merge(other)
	if base.AppName != "base" {
		t.Errorf("zero AppName from other must not overwrite base, got %q", base.AppName)
	}
	if base.HTTPPort != 8080 {
		t.Errorf("zero HTTPPort from other must not overwrite base, got %d", base.HTTPPort)
	}
	// Debug is taken unconditionally (documented "c.Debug = other.Debug").
	if base.Debug != false {
		t.Error("Debug must be taken unconditionally from other (true->false)")
	}

	// Non-zero override wins.
	base2 := validBase()
	base2.AppName = "base"
	base2.HTTPPort = 8080
	base2.Merge(&Config{AppName: "win", HTTPPort: 9999})
	if base2.AppName != "win" || base2.HTTPPort != 9999 {
		t.Errorf("non-zero other must win: AppName=%q HTTPPort=%d", base2.AppName, base2.HTTPPort)
	}
}

// ---------------------------------------------------------------------------
// (d) PARSE ERRORS SURFACED — malformed file/format returns error, not a
//     silent zero-config.
// ---------------------------------------------------------------------------

func TestAdversarialMalformedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	// Type-mismatched scalar -> yaml unmarshal error (http_port wants int).
	if err := os.WriteFile(p, []byte("http_port: not-an-int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromFile(p); err == nil {
		t.Fatal("malformed YAML (type mismatch) must return an error, not a zero-config")
	}
}

func TestAdversarialMalformedJSONErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte(`{"http_port": "nope"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromFile(p); err == nil {
		t.Fatal("malformed JSON must return an error, not a zero-config")
	}
}

func TestAdversarialMissingFileErrors(t *testing.T) {
	if _, err := LoadFromFile(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("missing file must return an error")
	}
}

func TestAdversarialUnsupportedFormatErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(p, []byte("x = 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromFile(p); err == nil {
		t.Fatal("unsupported extension must return an error")
	}
}

// LoadStrict surfaces a malformed value rather than silently defaulting it —
// contrast with lenient Load. Pin the fail-closed contract precisely.
func TestAdversarialLoadStrictSurfacesMalformed(t *testing.T) {
	t.Setenv("HELIX_HTTP_PORT", "70000") // out of [1,65535]
	if _, err := LoadStrict(); err == nil {
		t.Fatal("LoadStrict must reject out-of-range HTTP_PORT, not clamp/default it")
	}
}

// Lenient Load deliberately tolerates a malformed env scalar by falling back to
// the default (documented split vs LoadStrict). Pin that documented behavior so
// a future change to Load is a conscious one.
func TestAdversarialLoadLenientFallsBackOnGarbage(t *testing.T) {
	t.Setenv("HELIX_HTTP_PORT", "not-a-number")
	c, err := Load()
	if err != nil {
		t.Fatalf("lenient Load must not error on garbage scalar: %v", err)
	}
	if c.HTTPPort != 8080 {
		t.Errorf("lenient Load must fall back to default on garbage, got %d", c.HTTPPort)
	}
}

// ---------------------------------------------------------------------------
// (e) EDGES — empty/nil, aliasing, no panic.
// ---------------------------------------------------------------------------

// Merge must DEEP-COPY other's slice/map so a later caller mutation cannot
// corrupt the merged config (no aliasing).
func TestAdversarialMergeNoAliasing(t *testing.T) {
	other := &Config{
		EtcdEndpoints: []string{"a:1", "b:2"},
		Labels:        map[string]string{"k": "v"},
	}
	base := validBase()
	base.Merge(other)

	other.EtcdEndpoints[0] = "CORRUPT"
	other.Labels["k"] = "CORRUPT"

	if base.EtcdEndpoints[0] == "CORRUPT" {
		t.Error("aliasing: merged EtcdEndpoints shares backing array with other")
	}
	if base.Labels["k"] == "CORRUPT" {
		t.Error("aliasing: merged Labels shares map with other")
	}
}

func TestAdversarialMergeNilNoPanic(t *testing.T) {
	c := validBase()
	c.Merge(nil) // must not panic and must leave config unchanged
	if c.AppName != "ok" {
		t.Error("Merge(nil) must be a no-op")
	}
}

func TestAdversarialByteSizeOverflowOrBoundary(t *testing.T) {
	// Exact max int64 as plain bytes must round-trip (no spurious overflow error).
	const maxInt64 = "9223372036854775807"
	n, err := ParseByteSize(maxInt64)
	if err != nil {
		t.Fatalf("max int64 plain bytes must parse: %v", err)
	}
	if n != 9223372036854775807 {
		t.Errorf("max int64 mis-parsed: %d", n)
	}
	// One past max int64 must be a parse error, never a wrapped/zero value.
	if _, err := ParseByteSize("9223372036854775808"); err == nil {
		t.Error("int64 overflow input must error")
	}
}

func TestAdversarialLoadStrictEtcdListPrecedence(t *testing.T) {
	t.Setenv("HELIX_ETCD_ENDPOINTS", "x:1, y:2 ,z:3")
	c, err := LoadStrict()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x:1", "y:2", "z:3"}
	if len(c.EtcdEndpoints) != len(want) {
		t.Fatalf("etcd list: want %v, got %v", want, c.EtcdEndpoints)
	}
	for i := range want {
		if c.EtcdEndpoints[i] != want[i] {
			t.Errorf("etcd[%d]: want %q got %q (trim expected)", i, want[i], c.EtcdEndpoints[i])
		}
	}
}
