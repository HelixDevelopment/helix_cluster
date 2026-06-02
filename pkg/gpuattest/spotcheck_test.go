package gpuattest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const runUUIDSpot = "gpuattest-HXC1570-1b9f0e44-5a3d-47c2-8e1b-4c7a2f905de8"

func makeWords(n int) [][]byte {
	w := make([][]byte, n)
	for i := range w {
		w[i] = []byte(fmt.Sprintf("word-%04d-payload", i))
	}
	return w
}

// A genuine response passes spot-checks at several random indices (all pass). A
// response with exactly ONE tampered word FAILS the spot-check precisely when
// that index is among those checked, and the failure NAMES that index; checking
// only untouched indices still passes. Full closure for HXC-1570.
//
// Mutation: if SpotCheck ignores the index/word mismatch (e.g. always returns
// nil, or compares against a recomputed-from-presented root instead of the
// trusted root), the "tampered index checked" sub-case stops failing and this
// test fails. verifyInclusion is O(log N): it consumes only the supplied proof
// path, never the other words.
func TestSpotCheck_GenuineAndSingleTamper(t *testing.T) {
	const n = 16
	resp := NewWordResponse(makeWords(n))
	root := resp.Root

	// Genuine: open + check several indices — all pass.
	checkIdx := []int{1, 4, 7, 12, 15}
	var opened []OpenWord
	for _, i := range checkIdx {
		ow, ok := resp.Open(i)
		if !ok {
			t.Fatalf("Open(%d) failed", i)
		}
		opened = append(opened, ow)
	}
	t.Logf("run=%s before: spotcheck genuine indices=%v", runUUIDSpot, checkIdx)
	if err := SpotCheck(root, n, opened); err != nil {
		t.Fatalf("genuine spot-check failed: %v", err)
	}
	t.Logf("run=%s genuine: all %d indices passed", runUUIDSpot, len(checkIdx))

	// Tamper EXACTLY one word (index 7). The prover substitutes the word in its
	// opened message but the proof path is the genuine one.
	const tIdx = 7
	tw, _ := resp.Open(tIdx)
	tw.Word = []byte("TAMPERED-payload-XXXX")

	// (a) tampered index IS among those checked -> fails, names index 7.
	openedWithTamper := []OpenWord{}
	for _, i := range []int{4, tIdx, 12} {
		if i == tIdx {
			openedWithTamper = append(openedWithTamper, tw)
			continue
		}
		ow, _ := resp.Open(i)
		openedWithTamper = append(openedWithTamper, ow)
	}
	err := SpotCheck(root, n, openedWithTamper)
	t.Logf("run=%s after: spotcheck with tampered index %d checked -> err=%v", runUUIDSpot, tIdx, err)
	if !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("want ErrSpotCheckFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("index %d", tIdx)) {
		t.Fatalf("error must name index %d, got %q", tIdx, err.Error())
	}

	// (b) tampered index NOT among those checked -> still passes. This proves the
	// check is truly per-index, not a global recompute.
	openedNoTamper := []OpenWord{}
	for _, i := range []int{1, 4, 12} { // excludes 7
		ow, _ := resp.Open(i)
		openedNoTamper = append(openedNoTamper, ow)
	}
	if err := SpotCheck(root, n, openedNoTamper); err != nil {
		t.Fatalf("spot-check of untampered indices should pass, got %v", err)
	}
	t.Logf("run=%s control: tampered index NOT checked -> pass (per-index proof)", runUUIDSpot)
}

// Out-of-range indices are rejected and named, and a forged proof path (wrong
// sibling) for a genuine word is rejected — proving the proof itself is bound to
// the trusted root, not just the word bytes.
//
// Mutation: if verifyInclusion returns true whenever the word bytes are correct
// (ignoring the proof path), the forged-path sub-case stops failing.
func TestSpotCheck_RangeAndForgedPath(t *testing.T) {
	const n = 8
	resp := NewWordResponse(makeWords(n))
	root := resp.Root

	if err := SpotCheck(root, n, []OpenWord{{Index: 99, Word: []byte("x")}}); !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("out-of-range should fail, got %v", err)
	}

	ow, _ := resp.Open(3)
	// Corrupt one sibling hash in the proof path; the word is genuine.
	if len(ow.Proof) == 0 {
		t.Fatalf("expected a non-empty proof path")
	}
	ow.Proof[0][0] ^= 0xFF
	err := SpotCheck(root, n, []OpenWord{ow})
	t.Logf("run=%s forged-path: genuine word index 3 with corrupted sibling -> err=%v", runUUIDSpot, err)
	if !errors.Is(err, ErrSpotCheckFailed) {
		t.Fatalf("forged proof path should fail, got %v", err)
	}
}
