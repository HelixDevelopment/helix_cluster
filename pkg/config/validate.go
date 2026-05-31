package config

// This file extends the config package with typed, validating parsers and an
// aggregated multi-error so operators see every configuration problem at once.
// Stdlib only — no new dependencies.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// MultiError accumulates multiple errors so that callers can report ALL
// validation/parse failures at once instead of stopping at the first. It
// implements the error interface and supports errors.Is/As via Unwrap.
type MultiError struct {
	errs []error
}

// Add appends err to the collection. A nil err is ignored, which lets callers
// write `me.Add(parseSomething())` without guarding every call site.
func (m *MultiError) Add(err error) {
	if err == nil {
		return
	}
	m.errs = append(m.errs, err)
}

// Addf is a convenience that formats and appends an error.
func (m *MultiError) Addf(format string, args ...any) {
	m.errs = append(m.errs, fmt.Errorf(format, args...))
}

// Len returns the number of accumulated (non-nil) errors.
func (m *MultiError) Len() int {
	return len(m.errs)
}

// Error renders all accumulated errors as a single, operator-readable string.
func (m *MultiError) Error() string {
	if len(m.errs) == 0 {
		return ""
	}
	parts := make([]string, len(m.errs))
	for i, e := range m.errs {
		parts[i] = e.Error()
	}
	return fmt.Sprintf("%d configuration error(s): %s", len(m.errs), strings.Join(parts, "; "))
}

// Unwrap exposes the underlying errors so errors.Is/As can match any of them.
func (m *MultiError) Unwrap() []error {
	return m.errs
}

// ErrorOrNil returns nil when no errors were accumulated, otherwise the
// MultiError itself. Callers should always return this rather than the
// MultiError directly, to avoid a non-nil interface wrapping an empty value.
func (m *MultiError) ErrorOrNil() error {
	if len(m.errs) == 0 {
		return nil
	}
	return m
}

// ParseDuration parses a Go duration string (e.g. "30s", "5m", "1h30m").
// A malformed or empty value returns a precise error and a zero duration —
// it NEVER silently defaults to 0.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("duration is empty")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}

// byteUnits maps a (case-folded) unit suffix to its multiplier. Both binary
// (IEC: KiB/MiB/GiB/TiB) and decimal (SI: KB/MB/GB/TB) units are supported, as
// well as a bare "B" or no suffix at all (interpreted as raw bytes).
var byteUnits = []struct {
	suffix string
	mult   int64
}{
	{"tib", 1024 * 1024 * 1024 * 1024},
	{"gib", 1024 * 1024 * 1024},
	{"mib", 1024 * 1024},
	{"kib", 1024},
	{"tb", 1000 * 1000 * 1000 * 1000},
	{"gb", 1000 * 1000 * 1000},
	{"mb", 1000 * 1000},
	{"kb", 1000},
	{"b", 1},
}

// ParseByteSize parses a human byte-size string into an exact byte count.
// Examples: "256MiB" -> 268435456, "1MB" -> 1000000, "512" -> 512.
// Binary (MiB) and decimal (MB) units are distinct. Malformed input returns a
// precise error and 0 — it NEVER silently defaults to 0 on bad data.
func ParseByteSize(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("byte size is empty")
	}
	lower := strings.ToLower(s)

	// Find the longest matching unit suffix.
	var (
		numPart string
		mult    int64 = 1
		matched bool
	)
	for _, u := range byteUnits {
		if strings.HasSuffix(lower, u.suffix) {
			numPart = strings.TrimSpace(lower[:len(lower)-len(u.suffix)])
			mult = u.mult
			matched = true
			break
		}
	}
	if !matched {
		// No unit suffix at all: treat the whole thing as a byte count.
		numPart = lower
	}
	if numPart == "" {
		return 0, fmt.Errorf("invalid byte size %q: missing numeric value", orig)
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size %q: %w", orig, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid byte size %q: must not be negative", orig)
	}
	return n * mult, nil
}

// ParseBool parses a boolean using Go's accepted forms (1,t,T,TRUE,true,True,
// 0,f,F,FALSE,false,False). Malformed input returns a precise error.
func ParseBool(s string) (bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return false, errors.New("bool is empty")
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("invalid bool %q: %w", s, err)
	}
	return b, nil
}

// ParseIntInRange parses an integer and enforces an inclusive [min,max] range.
// Out-of-range or malformed input returns a precise error and 0 — values are
// NEVER clamped to a bound.
func ParseIntInRange(s string, min, max int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("integer is empty")
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q: %w", s, err)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("integer %d out of range [%d,%d]", v, min, max)
	}
	return v, nil
}

// ParseOneOf returns s only if it exactly matches one of allowed. On mismatch
// it returns an empty string and an error that enumerates the allowed values so
// an operator can immediately correct the input.
func ParseOneOf(s string, allowed ...string) (string, error) {
	for _, a := range allowed {
		if s == a {
			return s, nil
		}
	}
	return "", fmt.Errorf("invalid value %q: must be one of [%s]", s, strings.Join(allowed, ", "))
}

// ParseList parses a comma-separated list, trimming whitespace around each
// element and dropping empties. A value that yields no non-empty elements
// returns an error rather than a silent empty slice.
func ParseList(s string) ([]string, error) {
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if t := strings.TrimSpace(r); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("list %q contains no non-empty elements", s)
	}
	return out, nil
}

// validLogLevels enumerates accepted log levels (kept in sync with Validate).
var validLogLevels = []string{"debug", "info", "warn", "error"}

// ValidateAll validates the Config and aggregates EVERY problem into a single
// MultiError so an operator sees all issues at once. It is additive to the
// existing Validate (which returns a joined-string error and remains for
// backward compatibility); ValidateAll returns a structured, unwrappable error.
func (c *Config) ValidateAll() error {
	var me MultiError

	if strings.TrimSpace(c.AppName) == "" {
		me.Addf("app name is required")
	}
	if c.HTTPPort <= 0 || c.HTTPPort > 65535 {
		me.Addf("invalid http port: %d (must be 1..65535)", c.HTTPPort)
	}
	if c.GRPCPort <= 0 || c.GRPCPort > 65535 {
		me.Addf("invalid grpc port: %d (must be 1..65535)", c.GRPCPort)
	}
	if c.HTTPPort > 0 && c.HTTPPort == c.GRPCPort {
		me.Addf("http port and grpc port must differ (both %d)", c.HTTPPort)
	}
	if len(c.EtcdEndpoints) == 0 {
		me.Addf("at least one etcd endpoint is required")
	}
	for i, ep := range c.EtcdEndpoints {
		if strings.TrimSpace(ep) == "" {
			me.Addf("etcd endpoint %d cannot be empty", i)
		}
	}
	if _, err := ParseOneOf(strings.ToLower(c.LogLevel), validLogLevels...); err != nil {
		me.Addf("invalid log level: %s (must be one of [%s])", c.LogLevel, strings.Join(validLogLevels, ", "))
	}
	if c.ReadTimeout <= 0 {
		me.Addf("read timeout must be positive")
	}
	if c.WriteTimeout <= 0 {
		me.Addf("write timeout must be positive")
	}
	if c.MaxConns <= 0 {
		me.Addf("max connections must be positive")
	}
	if c.MaxBodyBytes < 0 {
		me.Addf("max body bytes must not be negative")
	}
	if (c.TLSCertPath != "" || c.TLSKeyPath != "") && (c.TLSCertPath == "" || c.TLSKeyPath == "") {
		me.Addf("both tls_cert_path and tls_key_path must be set when using TLS")
	}

	return me.ErrorOrNil()
}

// LoadStrict loads configuration from environment variables like Load, but uses
// the typed validating parsers. Defaults are applied for any unset field, and
// EVERY parse or validation failure is aggregated into a single MultiError so an
// operator sees all problems at once. A malformed value NEVER silently falls
// back to a default — it is reported.
func LoadStrict() (*Config, error) {
	cfg := &Config{}
	cfg.SetDefaults()
	var me MultiError

	cfg.AppName = getEnv(envVarName("app-name"), cfg.AppName)
	cfg.Version = getEnv(envVarName("version"), cfg.Version)
	cfg.BindAddr = getEnv(envVarName("bind-addr"), cfg.BindAddr)
	cfg.DataDir = getEnv(envVarName("data-dir"), cfg.DataDir)
	cfg.TLSCertPath = getEnv(envVarName("tls-cert-path"), cfg.TLSCertPath)
	cfg.TLSKeyPath = getEnv(envVarName("tls-key-path"), cfg.TLSKeyPath)
	cfg.TLSCAPath = getEnv(envVarName("tls-ca-path"), cfg.TLSCAPath)
	cfg.MetricsAddr = getEnv(envVarName("metrics-addr"), cfg.MetricsAddr)

	if v := envSet(envVarName("debug")); v != "" {
		if b, err := ParseBool(v); err != nil {
			me.Addf("HELIX_DEBUG: %v", err)
		} else {
			cfg.Debug = b
		}
	}
	if v := envSet(envVarName("http-port")); v != "" {
		if p, err := ParseIntInRange(v, 1, 65535); err != nil {
			me.Addf("HELIX_HTTP_PORT: %v", err)
		} else {
			cfg.HTTPPort = p
		}
	}
	if v := envSet(envVarName("grpc-port")); v != "" {
		if p, err := ParseIntInRange(v, 1, 65535); err != nil {
			me.Addf("HELIX_GRPC_PORT: %v", err)
		} else {
			cfg.GRPCPort = p
		}
	}
	if v := envSet(envVarName("etcd-endpoints")); v != "" {
		if list, err := ParseList(v); err != nil {
			me.Addf("HELIX_ETCD_ENDPOINTS: %v", err)
		} else {
			cfg.EtcdEndpoints = list
		}
	}
	if v := envSet(envVarName("log-level")); v != "" {
		if lvl, err := ParseOneOf(strings.ToLower(v), validLogLevels...); err != nil {
			me.Addf("HELIX_LOG_LEVEL: %v", err)
		} else {
			cfg.LogLevel = lvl
		}
	}
	if v := envSet(envVarName("read-timeout")); v != "" {
		if d, err := ParseDuration(v); err != nil {
			me.Addf("HELIX_READ_TIMEOUT: %v", err)
		} else {
			cfg.ReadTimeout = d
		}
	}
	if v := envSet(envVarName("write-timeout")); v != "" {
		if d, err := ParseDuration(v); err != nil {
			me.Addf("HELIX_WRITE_TIMEOUT: %v", err)
		} else {
			cfg.WriteTimeout = d
		}
	}
	if v := envSet(envVarName("max-conns")); v != "" {
		if n, err := ParseIntInRange(v, 1, 1<<31-1); err != nil {
			me.Addf("HELIX_MAX_CONNS: %v", err)
		} else {
			cfg.MaxConns = n
		}
	}
	if v := envSet(envVarName("max-body-bytes")); v != "" {
		if n, err := ParseByteSize(v); err != nil {
			me.Addf("HELIX_MAX_BODY_BYTES: %v", err)
		} else {
			cfg.MaxBodyBytes = n
		}
	}

	// Aggregate parse errors with structural validation so the operator sees
	// everything in one pass.
	if verr := cfg.ValidateAll(); verr != nil {
		me.Add(verr)
	}

	if err := me.ErrorOrNil(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// envSet returns the raw (untrimmed) value of an environment variable, or ""
// if it is unset or set to the empty string. This deliberately mirrors the
// existing getEnv* helpers' "empty == unset" convention so an explicitly empty
// var falls back to the default rather than erroring.
func envSet(key string) string {
	return os.Getenv(key)
}
