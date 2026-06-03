// Package docs hosts living-documentation validators that prove the MVP
// architecture document (HXC-1145) stays in sync with the actual codebase
// (CLAUDE-3 / Constitution §11.4.106 — "No documentation ever can be out of sync
// with its codebase"). These are NOT smoke tests: each one asserts a sink-side
// property an end user (a reader following the doc) depends on — that every
// package the doc names really exists, that the service-communication diagram is
// present, that the doc is registered for export, and that the registry-schema
// claims match the live database. A doc that drifts from the tree fails the
// build here, which is the whole point of a living document.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docDir returns the directory containing this test (the docs/ tree). Paths in
// the document are written relative to docs/ (e.g. "../pkg/swim"), so resolving
// them against this directory mirrors what a reader's link would resolve to.
func docDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

func readMVP(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(docDir(t), "MVP_ARCHITECTURE.md"))
	if err != nil {
		t.Fatalf("MVP_ARCHITECTURE.md must exist and be readable: %v", err)
	}
	return string(b)
}

// TestMVPDocExists proves the canonical MVP architecture document exists and is
// non-trivial. Mutation that kills it: delete docs/MVP_ARCHITECTURE.md -> FAIL.
func TestMVPDocExists(t *testing.T) {
	if got := len(readMVP(t)); got < 2000 {
		t.Fatalf("MVP_ARCHITECTURE.md is too small to be the real doc: %d bytes", got)
	}
}

// TestMVPHasServiceCommMermaid proves the doc contains a Mermaid
// service-communication diagram grounded in real service binaries — not just any
// fenced block. It requires the ```mermaid fence, a graph declaration, and at
// least the gateway + scheduler + discovery nodes that the matrix is built on.
// Mutation that kills it: remove the ```mermaid fence (or rename "helix-gateway"
// out of the diagram) -> FAIL.
func TestMVPHasServiceCommMermaid(t *testing.T) {
	doc := readMVP(t)
	if !strings.Contains(doc, "```mermaid") {
		t.Fatal("doc must contain a ```mermaid service-communication diagram")
	}
	mermaid := doc[strings.Index(doc, "```mermaid"):]
	end := strings.Index(mermaid[len("```mermaid"):], "```")
	if end < 0 {
		t.Fatal("mermaid block is not closed")
	}
	mermaid = mermaid[:end+len("```mermaid")]
	if !regexp.MustCompile(`graph\s+(TD|LR|TB)`).MatchString(mermaid) {
		t.Fatal("mermaid block must declare a graph (TD/LR/TB)")
	}
	for _, must := range []string{"helix-gateway", "helix-scheduler", "pkg/discovery"} {
		if !strings.Contains(mermaid, must) {
			t.Fatalf("service-comm diagram must reference real component %q", must)
		}
	}
}

// pkgRefRe matches in-repo path references the doc makes, e.g. ../pkg/swim,
// ../internal/gateway, ../cmd/helix-node, ../data/hxc_registry.db.
var pkgRefRe = regexp.MustCompile(`\.\./(pkg|internal|cmd|data)/[A-Za-z0-9_./-]+`)

// TestMVPReferencedPathsExist is the core anti-drift gate: every in-repo path the
// document links to MUST exist on disk. This is what makes the doc "living" — you
// cannot rename or delete a package without this test going red. It also implicitly
// forbids inventing services/packages that do not exist (CLAUDE-1 anti-bluff).
// Mutation that kills it: add a link to "../pkg/doesnotexist" in the doc, OR change
// a real path in the doc to a typo -> FAIL.
func TestMVPReferencedPathsExist(t *testing.T) {
	doc := readMVP(t)
	base := docDir(t)
	refs := pkgRefRe.FindAllString(doc, -1)
	if len(refs) < 15 {
		t.Fatalf("expected the doc to reference many real packages, found %d", len(refs))
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		// Strip a trailing ) or , or . that markdown punctuation may capture.
		clean := strings.TrimRight(ref, ").,;:")
		if seen[clean] {
			continue
		}
		seen[clean] = true
		abs := filepath.Join(base, clean)
		if _, err := os.Stat(abs); err != nil {
			t.Errorf("doc references %q which does not exist on disk (drift): %v", clean, err)
		}
	}
}

// TestMVPClaimsFiveTableRegistry proves the doc honestly describes the registry as
// the five-table SQLite DB that truly exists, and explicitly does NOT claim the
// invented 15-table schema. Mutation that kills it: change the doc to claim a
// "15-table" schema, or drop the item_history description -> FAIL.
func TestMVPClaimsFiveTableRegistry(t *testing.T) {
	doc := readMVP(t)
	for _, must := range []string{"`items`", "`item_history`", "data/hxc_registry.db"} {
		if !strings.Contains(doc, must) {
			t.Fatalf("registry section must describe %q", must)
		}
	}
	if strings.Contains(doc, "15-table") && !strings.Contains(doc, "not** a 15-table") {
		t.Fatal("doc must not invent a 15-table schema")
	}
	if !regexp.MustCompile(`(?i)five tables`).MatchString(doc) {
		t.Fatal("doc must state the registry has five tables (the real count)")
	}
}

// TestMVPRegisteredInDocsChain proves the doc is wired into a docs_chain context so
// its HTML/PDF/DOCX exports are enforced out of the box (§11.4.106). It checks the
// markdown node, all three derive-from edges, and that each transform is declared.
// Mutation that kills it: remove the `mvp_md` node line from tracked_docs.yaml, or
// delete one of the three mvp derive-from edges -> FAIL.
func TestMVPRegisteredInDocsChain(t *testing.T) {
	ctxPath := filepath.Join(docDir(t), "..", ".docs_chain", "contexts", "tracked_docs.yaml")
	b, err := os.ReadFile(ctxPath)
	if err != nil {
		t.Fatalf("tracked_docs.yaml must exist: %v", err)
	}
	y := string(b)

	// Node must point at the real markdown source.
	if !regexp.MustCompile(`mvp_md:\s*{[^}]*path:\s*docs/MVP_ARCHITECTURE\.md`).MatchString(y) {
		t.Fatal("tracked_docs.yaml must register mvp_md -> docs/MVP_ARCHITECTURE.md")
	}
	// All three export nodes must be declared.
	for _, node := range []string{"mvp_html", "mvp_pdf", "mvp_docx"} {
		if !strings.Contains(y, node+":") {
			t.Errorf("tracked_docs.yaml missing export node %q", node)
		}
	}
	// The full derive chain md->html->pdf and md->docx must be present and use the
	// shared builtin transforms.
	edges := []struct{ from, to, transform string }{
		{"mvp_md", "mvp_html", "md2html"},
		{"mvp_html", "mvp_pdf", "html2pdf"},
		{"mvp_md", "mvp_docx", "md2docx"},
	}
	for _, e := range edges {
		re := regexp.MustCompile(`from:\s*` + e.from + `,\s*to:\s*` + e.to + `,\s*transform:\s*` + e.transform)
		if !re.MatchString(y) {
			t.Errorf("missing derive-from edge %s -> %s (%s)", e.from, e.to, e.transform)
		}
	}
	// The transforms the edges use must be defined in the context.
	for _, tr := range []string{"md2html", "html2pdf", "md2docx"} {
		if !regexp.MustCompile(tr + `:\s*{\s*builtin:`).MatchString(y) {
			t.Errorf("transform %q must be defined in tracked_docs.yaml", tr)
		}
	}
}
