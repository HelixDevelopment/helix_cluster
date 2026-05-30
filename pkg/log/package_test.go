package log

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func captureOutput(fn func()) string {
	var buf bytes.Buffer
	old := defaultOutput
	defaultOutput = &buf
	defer func() { defaultOutput = old }()
	fn()
	return buf.String()
}

var defaultOutput *bytes.Buffer

func TestNew(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	if l == nil {
		t.Fatal("expected non-nil logger")
	}
	if l.Level() != InfoLevel {
		t.Errorf("expected default level INFO, got %v", l.Level())
	}
}

func TestNew_Mutation(t *testing.T) {
	// Mutation: nil output panics instead of defaulting to stdout
	l := New("test", nil)
	if l == nil {
		t.Fatal("expected non-nil logger for nil output")
	}
	// Should not panic on log
	l.Info("hello")
}

func TestDefault(t *testing.T) {
	l := Default()
	if l == nil {
		t.Fatal("expected non-nil default logger")
	}
}

func TestSetLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l.SetLevel(DebugLevel)
	if l.Level() != DebugLevel {
		t.Errorf("expected level DEBUG, got %v", l.Level())
	}
	l.Debug("debug msg")
	if !strings.Contains(buf.String(), "debug msg") {
		t.Errorf("expected debug message in output, got %s", buf.String())
	}
}

func TestSetLevel_Mutation(t *testing.T) {
	// Mutation: level filter not applied
	var buf bytes.Buffer
	l := New("test", &buf)
	l.SetLevel(ErrorLevel)
	l.Info("should be filtered")
	if strings.Contains(buf.String(), "should be filtered") {
		t.Error("expected INFO to be filtered at ERROR level")
	}
}

func TestDebug(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l.SetLevel(DebugLevel)
	l.Debug("debug message", "key", "value")
	out := buf.String()
	if !strings.Contains(out, "debug message") {
		t.Errorf("expected message, got %s", out)
	}
	if !strings.Contains(out, "key") {
		t.Errorf("expected structured field key, got %s", out)
	}
}

func TestInfo(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l.Info("info message", "request_id", "abc123")
	out := buf.String()
	if !strings.Contains(out, "info message") {
		t.Errorf("expected message, got %s", out)
	}
	if !strings.Contains(out, "request_id") {
		t.Errorf("expected structured field, got %s", out)
	}
}

func TestInfo_Mutation(t *testing.T) {
	// Mutation: structured fields dropped
	var buf bytes.Buffer
	l := New("test", &buf)
	l.Info("info message", "request_id", "abc123")
	out := buf.String()
	if !strings.Contains(out, "abc123") {
		t.Error("expected structured field value in output")
	}
}

func TestWarn(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l.Warn("warn message")
	out := buf.String()
	if !strings.Contains(out, "warn message") {
		t.Errorf("expected warn message, got %s", out)
	}
}

func TestError(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l.Error("error message", "err_code", 500)
	out := buf.String()
	if !strings.Contains(out, "error message") {
		t.Errorf("expected error message, got %s", out)
	}
	if !strings.Contains(out, "500") {
		t.Errorf("expected error code in output, got %s", out)
	}
}

func TestWithField(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l2 := l.WithField("service", "auth")
	l2.Info("request handled")
	out := buf.String()
	if !strings.Contains(out, "service") {
		t.Errorf("expected service field, got %s", out)
	}
	if !strings.Contains(out, "auth") {
		t.Errorf("expected auth value, got %s", out)
	}
}

func TestWithField_Mutation(t *testing.T) {
	// Mutation: WithField mutates original logger
	var buf1, buf2 bytes.Buffer
	l := New("test", &buf1)
	l2 := l.WithField("service", "auth")
	l2.Info("has service")
	if !strings.Contains(buf1.String(), "service") {
		t.Error("expected derived logger to contain service")
	}
	l.Info("no service")
	out := buf2.String()
	if strings.Contains(out, "service") {
		t.Error("expected original logger to be unaffected")
	}
}

func TestWithFields(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l2 := l.WithFields(map[string]interface{}{
		"node_id": "node-1",
		"shard":   7,
	})
	l2.Info("event")
	out := buf.String()
	if !strings.Contains(out, "node-1") || !strings.Contains(out, "7") {
		t.Errorf("expected all fields in output, got %s", out)
	}
}

func TestContextWithFields(t *testing.T) {
	ctx := ContextWithFields(context.Background(), map[string]interface{}{
		"trace_id": "t1",
	})
	ctx = ContextWithFields(ctx, map[string]interface{}{
		"span_id": "s1",
	})

	fields, ok := ctx.Value(loggerFieldsKey).(map[string]interface{})
	if !ok {
		t.Fatal("expected fields in context")
	}
	if fields["trace_id"] != "t1" {
		t.Errorf("expected trace_id t1, got %v", fields["trace_id"])
	}
	if fields["span_id"] != "s1" {
		t.Errorf("expected span_id s1, got %v", fields["span_id"])
	}
}

func TestContextWithFields_Mutation(t *testing.T) {
	// Mutation: ContextWithFields overwrites instead of merging
	ctx := ContextWithFields(context.Background(), map[string]interface{}{
		"trace_id": "t1",
	})
	ctx = ContextWithFields(ctx, map[string]interface{}{
		"span_id": "s1",
	})
	fields := ctx.Value(loggerFieldsKey).(map[string]interface{})
	if _, ok := fields["trace_id"]; !ok {
		t.Error("expected original fields to be preserved")
	}
}

func TestWithContext(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	ctx := ContextWithFields(context.Background(), map[string]interface{}{
		"trace_id": "abc",
	})
	l2 := l.WithContext(ctx)
	l2.Info("handled")
	out := buf.String()
	if !strings.Contains(out, "trace_id") {
		t.Errorf("expected trace_id from context, got %s", out)
	}
}

func TestWithContext_Mutation(t *testing.T) {
	// Mutation: WithContext ignores context fields
	var buf bytes.Buffer
	l := New("test", &buf)
	ctx := ContextWithFields(context.Background(), map[string]interface{}{
		"trace_id": "abc",
	})
	l2 := l.WithContext(ctx)
	l2.Info("handled")
	out := buf.String()
	if !strings.Contains(out, "abc") {
		t.Error("expected context field value in output")
	}
}

func TestParseLevel(t *testing.T) {
	for _, tc := range []struct {
		input    string
		expected Level
	}{
		{"DEBUG", DebugLevel},
		{"INFO", InfoLevel},
		{"WARN", WarnLevel},
		{"ERROR", ErrorLevel},
		{"FATAL", FatalLevel},
	} {
		lvl, err := ParseLevel(tc.input)
		if err != nil {
			t.Errorf("unexpected error for %s: %v", tc.input, err)
		}
		if lvl != tc.expected {
			t.Errorf("expected %v, got %v", tc.expected, lvl)
		}
	}
	_, err := ParseLevel("UNKNOWN")
	if err == nil {
		t.Error("expected error for unknown level")
	}
}

func TestStructuredJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	l := New("test", &buf)
	l.Info("user login", "user_id", "u42", "ip", "10.0.0.1")
	out := buf.String()
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("expected valid JSON output: %v", err)
	}
	if record["msg"] != "user login" {
		t.Errorf("expected msg 'user login', got %v", record["msg"])
	}
	if record["user_id"] != "u42" {
		t.Errorf("expected user_id u42, got %v", record["user_id"])
	}
	if record["ip"] != "10.0.0.1" {
		t.Errorf("expected ip 10.0.0.1, got %v", record["ip"])
	}
	if record["level"] != "INFO" {
		t.Errorf("expected level INFO, got %v", record["level"])
	}
}

func TestStructuredJSONOutput_Mutation(t *testing.T) {
	// Mutation: output is not valid JSON
	var buf bytes.Buffer
	l := New("test", &buf)
	l.Info("test")
	out := buf.String()
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Error("expected valid JSON output")
	}
}

func TestLevelString(t *testing.T) {
	if DebugLevel.String() != "DEBUG" {
		t.Errorf("expected DEBUG, got %s", DebugLevel.String())
	}
	if FatalLevel.String() != "FATAL" {
		t.Errorf("expected FATAL, got %s", FatalLevel.String())
	}
	if Level(99).String() != "UNKNOWN" {
		t.Errorf("expected UNKNOWN, got %s", Level(99).String())
	}
}
