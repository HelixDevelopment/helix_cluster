//go:build e2e

package main

import (
	"bufio"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// e2eFreePort reserves an ephemeral TCP port by binding ":0", reading the
// kernel-assigned port, and releasing it immediately. The number is returned so
// it can be handed to the binary via its env-driven port config. This keeps the
// test free of any hard-coded port while still working with binaries whose
// config validation rejects a literal "0" (port must be in [1,65535]). There is
// an inherent (tiny) race between releasing and the binary re-binding; it is
// acceptable for a local e2e and avoids hard-coding. It is named with an e2e
// prefix to avoid colliding with the freePort helper some packages already
// define in their default-lane main_test.go.
func e2eFreePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	_ = lis.Close()
	return port
}

// portEnv formats a KEY=VALUE pair for an integer port.
func portEnv(key string, port int) string { return key + "=" + strconv.Itoa(port) }

// repoRoot walks up from this test file to the module root (the dir holding
// go.mod). This file lives at <root>/cmd/helix-health/, so it is two levels deep.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

// e2eBuild builds the given cmd package into a temp dir and returns the artifact
// path. Building (not `go run`) proves the binary genuinely compiles and links
// as a shippable artifact, then we exec that artifact directly.
func e2eBuild(t *testing.T, pkg string) string {
	t.Helper()
	root := repoRoot(t)
	name := filepath.Base(pkg)
	bin := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s failed: %v\n%s", pkg, err, out)
	}
	return bin
}

// e2eProc is a running binary subprocess whose stderr is captured line-by-line
// so the test can discover the actual bound (ephemeral) listen address from the
// process's own startup log — no hard-coded ports.
type e2eProc struct {
	cmd   *exec.Cmd
	lines chan string
	mu    sync.Mutex
	tail  []string
}

// e2eStart launches bin with the given extra env (KEY=VALUE) appended to the
// parent environment and begins streaming its stderr.
func e2eStart(t *testing.T, bin string, env []string) *e2eProc {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(cmd.Environ(), env...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	p := &e2eProc{cmd: cmd, lines: make(chan string, 256)}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			p.mu.Lock()
			p.tail = append(p.tail, line)
			p.mu.Unlock()
			select {
			case p.lines <- line:
			default:
			}
		}
		close(p.lines)
		_, _ = io.Discard.Write(nil)
	}()
	return p
}

// waitForAddr scans the process stderr for a "listening on <addr>" log line
// containing marker and returns the host:port it announced. It fails the test
// if the line does not appear within a bounded timeout.
func (p *e2eProc) waitForAddr(t *testing.T, marker string) string {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				t.Fatalf("process exited before announcing %q; stderr:\n%s", marker, p.stderrTail())
			}
			if idx := strings.Index(line, marker); idx >= 0 {
				addr := strings.TrimSpace(line[idx+len(marker):])
				// Normalize a wildcard bind (e.g. "[::]:54321") to loopback so
				// the test dials a routable address.
				return normalizeLoopback(addr)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; stderr:\n%s", marker, p.stderrTail())
		}
	}
}

// normalizeLoopback rewrites a wildcard host (empty, 0.0.0.0, ::) to 127.0.0.1
// so the announced ephemeral port is dialable from the test.
func normalizeLoopback(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr
	}
	host, port := addr[:i], addr[i+1:]
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	switch host {
	case "", "0.0.0.0", "::":
		return "127.0.0.1:" + port
	default:
		return addr
	}
}

func (p *e2eProc) stderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.tail, "\n")
}

// stop tears the subprocess down cleanly: it cancels via SIGINT-equivalent
// (Process.Kill on the bounded-wait path) and waits for exit so no orphan
// listener leaks between tests.
func (p *e2eProc) stop(t *testing.T) {
	t.Helper()
	if p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	done := make(chan struct{})
	go func() {
		_ = p.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Logf("warning: process did not exit within 10s after kill")
	}
}
