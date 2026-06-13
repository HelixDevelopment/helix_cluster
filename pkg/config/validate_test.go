package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// --- Typed parser: Duration ---

func TestParseDurationValid(t *testing.T) {
	d, err := ParseDuration("30s")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 30*time.Second {
		t.Errorf("expected 30s, got %v", d)
	}
}

func TestParseDurationInvalidErrorsNotZero(t *testing.T) {
	// Mutation guard: malformed input MUST error, never silently default to 0.
	d, err := ParseDuration("notaduration")
	if err == nil {
		t.Fatal("expected error for malformed duration")
	}
	if d != 0 {
		t.Errorf("on error duration should be zero value, got %v", d)
	}
	if !strings.Contains(err.Error(), "notaduration") {
		t.Errorf("error should mention the bad input, got %q", err.Error())
	}
}

func TestParseDurationEmptyErrors(t *testing.T) {
	if _, err := ParseDuration(""); err == nil {
		t.Fatal("expected error for empty duration")
	}
}

// --- Typed parser: ByteSize ---

func TestParseByteSizeExactBinary(t *testing.T) {
	// Mutation guard: "256MiB" MUST equal exactly 256*1024*1024 bytes.
	n, err := ParseByteSize("256MiB")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 256*1024*1024 {
		t.Errorf("expected %d, got %d", 256*1024*1024, n)
	}
}

func TestParseByteSizeDecimalVsBinary(t *testing.T) {
	mb, err := ParseByteSize("1MB")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mb != 1000*1000 {
		t.Errorf("expected 1MB=1000000, got %d", mb)
	}
	mib, err := ParseByteSize("1MiB")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mib != 1024*1024 {
		t.Errorf("expected 1MiB=1048576, got %d", mib)
	}
	if mb == mib {
		t.Error("MB and MiB must not be equal")
	}
}

func TestParseByteSizePlainBytes(t *testing.T) {
	n, err := ParseByteSize("512")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 512 {
		t.Errorf("expected 512, got %d", n)
	}
	n, err = ParseByteSize("4096B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 4096 {
		t.Errorf("expected 4096, got %d", n)
	}
}

func TestParseByteSizeKiGiTi(t *testing.T) {
	cases := map[string]int64{
		"1KiB": 1024,
		"2GiB": 2 * 1024 * 1024 * 1024,
		"1TiB": 1024 * 1024 * 1024 * 1024,
		"1KB":  1000,
		"3GB":  3 * 1000 * 1000 * 1000,
	}
	for in, want := range cases {
		got, err := ParseByteSize(in)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", in, err)
		}
		if got != want {
			t.Errorf("%s: expected %d, got %d", in, want, got)
		}
	}
}

func TestParseByteSizeInvalidErrorsNotZero(t *testing.T) {
	// Mutation guard: junk MUST error, not silently return 0.
	for _, bad := range []string{"", "abc", "12XB", "MiB", "-5MiB", "1.5.2KiB"} {
		n, err := ParseByteSize(bad)
		if err == nil {
			t.Errorf("expected error for %q", bad)
		}
		if n != 0 {
			t.Errorf("%q: expected 0 on error, got %d", bad, n)
		}
	}
}

func TestParseByteSizeCaseInsensitiveUnit(t *testing.T) {
	n, err := ParseByteSize("256mib")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 256*1024*1024 {
		t.Errorf("expected %d, got %d", 256*1024*1024, n)
	}
}

// --- Typed parser: Bool ---

func TestParseBoolValid(t *testing.T) {
	for _, s := range []string{"true", "TRUE", "1", "t", "false", "0", "F"} {
		if _, err := ParseBool(s); err != nil {
			t.Errorf("unexpected error for %q: %v", s, err)
		}
	}
	b, _ := ParseBool("TRUE")
	if !b {
		t.Error("TRUE should parse true")
	}
}

func TestParseBoolInvalidErrors(t *testing.T) {
	if _, err := ParseBool("yes-ish"); err == nil {
		t.Fatal("expected error for malformed bool")
	}
}

// --- Typed parser: IntInRange ---

func TestParseIntInRangeValid(t *testing.T) {
	v, err := ParseIntInRange("8080", 1, 65535)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 8080 {
		t.Errorf("expected 8080, got %d", v)
	}
}

func TestParseIntInRangeBoundsInclusive(t *testing.T) {
	if _, err := ParseIntInRange("1", 1, 65535); err != nil {
		t.Errorf("lower bound should be inclusive: %v", err)
	}
	if _, err := ParseIntInRange("65535", 1, 65535); err != nil {
		t.Errorf("upper bound should be inclusive: %v", err)
	}
}

func TestParseIntInRangeOutOfRangeErrorsNotClamped(t *testing.T) {
	// Mutation guard: out-of-range MUST error, never clamp to a bound.
	v, err := ParseIntInRange("70000", 1, 65535)
	if err == nil {
		t.Fatal("expected error for out-of-range int")
	}
	if v != 0 {
		t.Errorf("expected 0 on error (not clamped), got %d", v)
	}
}

func TestParseIntInRangeMalformedErrors(t *testing.T) {
	if _, err := ParseIntInRange("not-int", 1, 100); err == nil {
		t.Fatal("expected error for malformed int")
	}
}

// --- Typed parser: OneOf (string enum) ---

func TestParseOneOfValid(t *testing.T) {
	v, err := ParseOneOf("info", "debug", "info", "warn", "error")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "info" {
		t.Errorf("expected info, got %s", v)
	}
}

func TestParseOneOfInvalidErrorsAndListsAllowed(t *testing.T) {
	v, err := ParseOneOf("verbose", "debug", "info", "warn", "error")
	if err == nil {
		t.Fatal("expected error for value not in set")
	}
	if v != "" {
		t.Errorf("expected empty string on error, got %q", v)
	}
	// Mutation guard: error must enumerate the allowed values so an operator can fix it.
	for _, want := range []string{"debug", "info", "warn", "error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list allowed value %q, got %q", want, err.Error())
		}
	}
}

// --- Typed parser: List (comma-separated) ---

func TestParseListValid(t *testing.T) {
	got, err := ParseList("a, b ,c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("element %d: expected %q, got %q (trimming expected)", i, want[i], got[i])
		}
	}
}

func TestParseListEmptyErrors(t *testing.T) {
	// Mutation guard: an all-empty list MUST error rather than yield silent empties.
	if _, err := ParseList(""); err == nil {
		t.Fatal("expected error for empty list")
	}
	if _, err := ParseList(" , , "); err == nil {
		t.Fatal("expected error for list of only blanks")
	}
}

func TestParseListSingle(t *testing.T) {
	got, err := ParseList("solo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "solo" {
		t.Errorf("expected [solo], got %v", got)
	}
}

// --- MultiError aggregation ---

func TestMultiErrorAggregatesAll(t *testing.T) {
	var me MultiError
	me.Add(errors.New("first problem"))
	me.Add(errors.New("second problem"))
	me.Add(nil) // nils ignored
	me.Addf("third %s", "problem")

	if me.Len() != 3 {
		t.Fatalf("expected 3 errors, got %d", me.Len())
	}
	err := me.ErrorOrNil()
	if err == nil {
		t.Fatal("expected non-nil aggregated error")
	}
	msg := err.Error()
	for _, want := range []string{"first problem", "second problem", "third problem"} {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error missing %q: %q", want, msg)
		}
	}
}

func TestMultiErrorEmptyIsNil(t *testing.T) {
	var me MultiError
	me.Add(nil)
	if me.Len() != 0 {
		t.Errorf("expected 0, got %d", me.Len())
	}
	if me.ErrorOrNil() != nil {
		t.Error("empty MultiError should yield nil")
	}
}

func TestMultiErrorUnwrapExposesAll(t *testing.T) {
	sentinel := errors.New("sentinel")
	var me MultiError
	me.Add(errors.New("other"))
	me.Add(sentinel)
	err := me.ErrorOrNil()
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find a wrapped sentinel error")
	}
}

// --- ValidateAll: required fields + defaults + aggregated multi-error ---

func TestValidateAllReportsEveryProblemAtOnce(t *testing.T) {
	// Mutation guard: ALL problems must surface, not just the first.
	cfg := &Config{
		AppName:       "",     // required, missing
		HTTPPort:      99999,  // out of range
		GRPCPort:      -1,     // out of range
		LogLevel:      "loud", // not one-of
		MaxConns:      0,      // must be positive
		EtcdEndpoints: nil,    // required non-empty
	}
	err := cfg.ValidateAll()
	if err == nil {
		t.Fatal("expected aggregated validation error")
	}
	msg := err.Error()
	for _, want := range []string{"app name", "http", "grpc", "log level", "etcd", "max"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("aggregated error should mention %q, got %q", want, msg)
		}
	}
}

func TestValidateAllPassesValidConfig(t *testing.T) {
	cfg := &Config{AppName: "helix"}
	cfg.SetDefaults()
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Required env loading with typed parsing + aggregation ---

func TestLoadStrictAggregatesParseErrors(t *testing.T) {
	t.Setenv("HELIX_HTTP_PORT", "not-a-number")
	t.Setenv("HELIX_READ_TIMEOUT", "bogusdur")
	t.Setenv("HELIX_LOG_LEVEL", "screaming")

	_, err := LoadStrict()
	if err == nil {
		t.Fatal("expected aggregated parse/validation error")
	}
	msg := strings.ToLower(err.Error())
	// Mutation guard: a malformed duration/int/enum must NOT be silently defaulted;
	// all three must be reported together. (Field names surface in env-var form,
	// e.g. helix_log_level.)
	for _, want := range []string{"http", "timeout", "log_level"} {
		if !strings.Contains(msg, want) {
			t.Errorf("LoadStrict should report %q, got %q", want, msg)
		}
	}
}

func TestLoadStrictAppliesDefaultsWhenUnset(t *testing.T) {
	// No env set: defaults must apply and validation must pass.
	for _, k := range []string{
		"HELIX_HTTP_PORT", "HELIX_GRPC_PORT", "HELIX_READ_TIMEOUT",
		"HELIX_WRITE_TIMEOUT", "HELIX_LOG_LEVEL", "HELIX_MAX_CONNS",
		"HELIX_APP_NAME", "HELIX_ETCD_ENDPOINTS",
	} {
		t.Setenv(k, "")
	}
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("default http port not applied: %d", cfg.HTTPPort)
	}
	if cfg.ReadTimeout != 30*time.Second {
		t.Errorf("default read timeout not applied: %v", cfg.ReadTimeout)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default log level not applied: %s", cfg.LogLevel)
	}
}

func TestLoadStrictValidEnv(t *testing.T) {
	t.Setenv("HELIX_HTTP_PORT", "3000")
	t.Setenv("HELIX_READ_TIMEOUT", "15s")
	t.Setenv("HELIX_LOG_LEVEL", "warn")
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPPort != 3000 {
		t.Errorf("expected 3000, got %d", cfg.HTTPPort)
	}
	if cfg.ReadTimeout != 15*time.Second {
		t.Errorf("expected 15s, got %v", cfg.ReadTimeout)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("expected warn, got %s", cfg.LogLevel)
	}
}

// --- MaxBytes typed field round-trips through LoadStrict ---

func TestLoadStrictParsesByteSize(t *testing.T) {
	t.Setenv("HELIX_MAX_BODY_BYTES", "256MiB")
	cfg, err := LoadStrict()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxBodyBytes != 256*1024*1024 {
		t.Errorf("expected %d, got %d", 256*1024*1024, cfg.MaxBodyBytes)
	}
}

func TestLoadStrictByteSizeMalformedErrors(t *testing.T) {
	t.Setenv("HELIX_MAX_BODY_BYTES", "256Xib")
	_, err := LoadStrict()
	if err == nil {
		t.Fatal("expected error for malformed byte size")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "body") {
		t.Errorf("error should mention the offending field, got %q", err.Error())
	}
}
