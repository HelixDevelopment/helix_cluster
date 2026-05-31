package build

import (
	"fmt"
	"testing"
)

func TestManifestAddAndGet(t *testing.T) {
	m := NewManifest()
	p := Platform{OS: "linux", Arch: "amd64"}
	if err := m.Add(p, "sha256:abc123"); err != nil {
		t.Fatalf("add: %v", err)
	}
	digest, ok := m.Get(p)
	if !ok {
		t.Fatal("expected platform to exist")
	}
	if digest != "sha256:abc123" {
		t.Errorf("digest = %q, want sha256:abc123", digest)
	}
}

func TestManifestGetMissing(t *testing.T) {
	m := NewManifest()
	_, ok := m.Get(Platform{OS: "linux", Arch: "arm64"})
	if ok {
		t.Error("expected missing platform")
	}
}

func TestManifestEmptyDigest(t *testing.T) {
	m := NewManifest()
	if err := m.Add(Platform{OS: "linux", Arch: "amd64"}, ""); err == nil {
		t.Error("expected error for empty digest")
	}
}

func TestManifestPlatforms(t *testing.T) {
	m := NewManifest()
	m.Add(Platform{OS: "linux", Arch: "arm64"}, "sha256:bbb")
	m.Add(Platform{OS: "linux", Arch: "amd64"}, "sha256:aaa")
	m.Add(Platform{OS: "windows", Arch: "amd64"}, "sha256:ccc")

	platforms := m.Platforms()
	if len(platforms) != 3 {
		t.Fatalf("len(platforms) = %d, want 3", len(platforms))
	}
	want := []string{"linux/amd64", "linux/arm64", "windows/amd64"}
	for i, w := range want {
		if platforms[i] != w {
			t.Errorf("platforms[%d] = %q, want %q", i, platforms[i], w)
		}
	}
}

func TestManifestRemove(t *testing.T) {
	m := NewManifest()
	p := Platform{OS: "linux", Arch: "amd64"}
	m.Add(p, "sha256:abc")
	m.Remove(p)
	_, ok := m.Get(p)
	if ok {
		t.Error("expected platform to be removed")
	}
}

func TestManifestConcurrentAccess(t *testing.T) {
	m := NewManifest()
	done := make(chan bool, 20)
	for i := 0; i < 10; i++ {
		go func(n int) {
			p := Platform{OS: "linux", Arch: fmt.Sprintf("arch%d", n)}
			m.Add(p, fmt.Sprintf("sha256:%d", n))
			done <- true
		}(i)
	}
	for i := 0; i < 10; i++ {
		go func(n int) {
			_ = m.Platforms()
			done <- true
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if len(m.Platforms()) != 10 {
		t.Errorf("expected 10 platforms, got %d", len(m.Platforms()))
	}
}
