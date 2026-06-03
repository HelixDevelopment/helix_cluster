package main

import (
	"bytes"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// goToolOrSkip returns the path to the `go` tool, or skips the test if the Go
// toolchain is somehow unavailable (e.g. a stripped CI image). This is the only
// permitted skip per CLAUDE-2: no `go` => the cross-build genuinely cannot run.
func goToolOrSkip(t *testing.T) string {
	t.Helper()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err == nil {
		return goBin
	}
	p, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable, cannot exercise cross-build: %v", err)
	}
	return p
}

// scriptStampUUID and scriptStampVersion are the identity values we force the
// production script to stamp into every artifact of one invocation. The tests
// assert these exact bytes appear in the produced binaries, which is the only
// way a regression in the script's `-ldflags "-X main.BuildUUID=..."` wiring is
// caught: if the script stops passing the UUID through, the stamp disappears
// and TestCrossCompileScriptStampsSharedBuildUUID fails.
const (
	scriptStampUUID    = "script-driven-build-uuid-7f3a9c21"
	scriptStampVersion = "script-driven-version-v9.9.9"
)

// crossArtifacts is the result of running the production cross-compile script
// exactly once for the whole test binary: the directory it wrote into and the
// build UUID it was told to stamp.
type crossArtifacts struct {
	distDir string
	uuid    string
	version string
}

var (
	crossOnce    sync.Once
	crossResult  crossArtifacts
	crossSkip    string // non-empty => prerequisite missing, skip dependents
	crossDistDir string // shared scratch dir, removed by TestMain
)

// runScriptOnce invokes scripts/cross-compile-agent.sh ONE TIME against a
// scratch DIST_DIR with a deterministic BUILD_UUID/VERSION. Every per-arch test
// reuses the artifacts so we drive the *real production script* (not a Go
// reimplementation of it) without paying for three cross builds per assertion.
//
// This is the load-bearing change for §1.1 mutation-pairing of the DELIVERABLE:
// the assertions below open the files THIS SCRIPT produced, so a regression in
// the script's GOARCH / GOARM / env-array / ldflags is detected. (Previously the
// Go test re-implemented the build invocation, so mutating the script changed
// nothing the test observed — a PASS-bluff for the ticket's actual deliverable.)
func runScriptOnce(t *testing.T) crossArtifacts {
	t.Helper()
	crossOnce.Do(func() {
		goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, err := os.Stat(goBin); err != nil {
			if _, lerr := exec.LookPath("go"); lerr != nil {
				crossSkip = "go toolchain unavailable, cannot exercise cross-build script"
				return
			}
		}
		bashBin, err := exec.LookPath("bash")
		if err != nil {
			crossSkip = "bash unavailable, cannot exercise scripts/cross-compile-agent.sh"
			return
		}

		// Locate the script relative to this package: cmd/helix-agent/../.. .
		wd, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
		script := filepath.Join(repoRoot, "scripts", "cross-compile-agent.sh")
		if _, err := os.Stat(script); err != nil {
			t.Fatalf("production deliverable not found at %s: %v", script, err)
		}

		dist, err := os.MkdirTemp("", "helix-agent-cross-dist-")
		if err != nil {
			t.Fatalf("mkdtemp: %v", err)
		}
		crossDistDir = dist

		cmd := exec.Command(bashBin, script)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(),
			"DIST_DIR="+dist,
			"BUILD_UUID="+scriptStampUUID,
			"VERSION="+scriptStampVersion,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("scripts/cross-compile-agent.sh failed: %v\n%s", err, out)
		}

		crossResult = crossArtifacts{distDir: dist, uuid: scriptStampUUID, version: scriptStampVersion}
	})

	if crossSkip != "" {
		t.Skip(crossSkip)
	}
	return crossResult
}

// TestMain runs the suite then removes the shared scratch dist dir produced by
// the cross-compile script.
func TestMain(m *testing.M) {
	code := m.Run()
	if crossDistDir != "" {
		_ = os.RemoveAll(crossDistDir)
	}
	os.Exit(code)
}

// openScriptArtifact opens an ELF artifact the production script wrote into its
// DIST_DIR. A missing file means the script did not emit that target — itself a
// regression worth failing on.
func openScriptArtifact(t *testing.T, art crossArtifacts, name string) *elf.File {
	t.Helper()
	p := filepath.Join(art.distDir, name)
	f, err := elf.Open(p)
	if err != nil {
		t.Fatalf("script artifact %s is not an ELF object (script did not cross-compile it correctly): %v", name, err)
	}
	return f
}

// assertStampedUUID scans the raw bytes of a script-produced artifact for the
// BUILD_UUID the script was told to stamp via `-ldflags -X main.BuildUUID=`.
// This is what couples the test to the script's ldflags wiring: drop the -X and
// the bytes vanish. We can't *run* a linux/android ARM binary on the darwin/arm64
// host, so byte-presence of the linker-stamped string is the host-checkable
// proof that the script's stamping survived into the artifact.
func assertStampedUUID(t *testing.T, art crossArtifacts, name string) {
	t.Helper()
	p := filepath.Join(art.distDir, name)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading script artifact %s: %v", name, err)
	}
	if !bytes.Contains(raw, []byte(art.uuid)) {
		t.Fatalf("artifact %s does not embed the script-stamped BuildUUID %q; the script's -ldflags -X main.BuildUUID wiring regressed", name, art.uuid)
	}
}

// TestCrossCompileLinuxARM64ProducesAARCH64ELF is the load-bearing assertion for
// the linux/arm64 target: it opens the file THE PRODUCTION SCRIPT produced and
// inspects the actual machine + class bytes in the ELF header. A binary that
// compiled "successfully" but for the wrong arch (e.g. host darwin/arm64 Mach-O,
// or x86-64 ELF, or — the mutation the reviewer flagged — GOARCH=amd64) fails
// here, because the assertion now reads the SCRIPT's output instead of a Go
// reimplementation.
func TestCrossCompileLinuxARM64ProducesAARCH64ELF(t *testing.T) {
	art := runScriptOnce(t)
	f := openScriptArtifact(t, art, "helix-agent-linux-arm64")
	defer f.Close()

	if f.Machine != elf.EM_AARCH64 {
		t.Fatalf("ELF machine = %v, want EM_AARCH64 (0xB7); the script's linux/arm64 binary is not 64-bit ARM", f.Machine)
	}
	if f.Class != elf.ELFCLASS64 {
		t.Fatalf("ELF class = %v, want ELFCLASS64; the binary is not 64-bit", f.Class)
	}
	if f.OSABI != elf.ELFOSABI_LINUX && f.OSABI != elf.ELFOSABI_NONE {
		t.Fatalf("ELF OSABI = %v, want Linux/None (a Linux target)", f.OSABI)
	}
}

// TestCrossCompileLinuxARMv7ProducesARMELF asserts the script's 32-bit ARMv7
// variant is a real EM_ARM / ELFCLASS32 object — distinct from the arm64
// machine. This is what catches a regression in the script's `GOARCH=arm
// GOARM=7` / env-array handling.
func TestCrossCompileLinuxARMv7ProducesARMELF(t *testing.T) {
	art := runScriptOnce(t)
	f := openScriptArtifact(t, art, "helix-agent-linux-armv7")
	defer f.Close()

	if f.Machine != elf.EM_ARM {
		t.Fatalf("ELF machine = %v, want EM_ARM (0x28); the script's armv7 binary is not 32-bit ARM", f.Machine)
	}
	if f.Class != elf.ELFCLASS32 {
		t.Fatalf("ELF class = %v, want ELFCLASS32; armv7 must be 32-bit", f.Class)
	}
}

// TestCrossCompileAndroidARM64ProducesAARCH64ELF asserts the script's
// android/arm64 variant is also a 64-bit ARM ELF (Android binaries are ELF like
// Linux).
func TestCrossCompileAndroidARM64ProducesAARCH64ELF(t *testing.T) {
	art := runScriptOnce(t)
	f := openScriptArtifact(t, art, "helix-agent-android-arm64")
	defer f.Close()

	if f.Machine != elf.EM_AARCH64 {
		t.Fatalf("ELF machine = %v, want EM_AARCH64 for the script's android/arm64 binary", f.Machine)
	}
	if f.Class != elf.ELFCLASS64 {
		t.Fatalf("ELF class = %v, want ELFCLASS64 for android/arm64", f.Class)
	}
}

// TestCrossCompileScriptStampsSharedBuildUUID is the §1.1 pairing for the
// script's ldflags: it proves the SAME BuildUUID the script was given is stamped
// into ALL THREE per-arch artifacts of one invocation. This both (a) verifies
// the script's `-ldflags -X main.BuildUUID=` wiring is intact and (b) verifies
// the "single shared UUID across all artifacts" reproducibility guarantee the
// script's header documents. Dropping or hardcoding the UUID in the script fails
// this test.
func TestCrossCompileScriptStampsSharedBuildUUID(t *testing.T) {
	art := runScriptOnce(t)
	for _, name := range []string{
		"helix-agent-linux-arm64",
		"helix-agent-linux-armv7",
		"helix-agent-android-arm64",
	} {
		assertStampedUUID(t, art, name)
	}
}

// TestVersionFlagPrintsStampedIdentity is the CLAUDE-1 sink-side proof for the
// version path: a NATIVE build is run with --version and we assert the stamped
// Version + BuildUUID actually reach the user via stdout. This exercises the
// exact code path the cross-built binaries use, on the host where we can run it.
// (The cross artifacts above are ARM ELF and cannot execute on darwin/arm64, so
// the runnable proof is done with a native build that uses the same -X stamping.)
func TestVersionFlagPrintsStampedIdentity(t *testing.T) {
	got := captureVersion(t)
	if !strings.Contains(got, "test-version-stamp") {
		t.Fatalf("--version output missing stamped Version; got %q", got)
	}
	if !strings.Contains(got, "uuid-sink-proof") {
		t.Fatalf("--version output missing stamped BuildUUID; got %q", got)
	}
}

// captureVersion builds the native binary with stamped ldflags, runs it with
// --version, and returns its stdout. Building+running (rather than calling
// printVersion directly) proves the -X linker stamping reaches a real process.
func captureVersion(t *testing.T) string {
	t.Helper()
	goBin := goToolOrSkip(t)
	bin := filepath.Join(t.TempDir(), "helix-agent-native")
	build := exec.Command(goBin, "build",
		"-ldflags", "-X main.Version=test-version-stamp -X main.BuildUUID=uuid-sink-proof",
		"-o", bin, ".")
	wd, _ := os.Getwd()
	build.Dir = wd
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native build failed: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("running native --version failed: %v\n%s", err, out)
	}
	return string(out)
}
