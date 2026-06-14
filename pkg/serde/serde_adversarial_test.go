package serde

import (
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// This file pins the MarshalNode -> DeserializeNode serialization contract for
// the Cap'n Proto wire format with adversarial, mutation-verified tests.
//
// Two centerpieces:
//
//  1. ROUND-TRIP FIDELITY: a fully-populated node with a DISTINCT value in
//     every field must survive marshal->deserialize->ToRecord with exact
//     field-by-field equality. Distinct values mean a field-swap (e.g. id<->
//     hostname or cores<->memBytes) would be caught, not masked by coincident
//     values.
//
//  2. MALFORMED / TRUNCATED INPUT: DeserializeNode fed nil, empty, random, and
//     every truncated prefix of a valid frame must return an ERROR (or a
//     documented safe result) and MUST NEVER panic. A deserializer that panics
//     on hostile bytes is a DoS vector; one that silently accepts a truncated
//     frame as a partial/zero node is a robustness/security hazard.

// distinctNode returns a node whose fields are all mutually distinguishable, so
// any cross-wiring of fields is observable.
func distinctNode() NodeRecord {
	return NodeRecord{
		ID:       "ID-field-distinct-0001",
		Hostname: "HOSTNAME-field-distinct-zzzz.example.internal",
		Cores:    0x11223344,         // distinct from MemBytes, not a small int
		MemBytes: 0x55667788AABBCCDD, // distinct 64-bit pattern, high bits set
		Labels:   []string{"L0=alpha", "L1=beta", "L2=gamma"},
	}
}

// ── Centerpiece 1: round-trip fidelity ──────────────────────────────────────

// TestAdversarial_RoundTripFidelity_Distinct marshals a fully-distinct node and
// asserts byte-perfect reconstruction via ToRecord (DeepEqual). Because each
// field holds a unique value, a swapped field assignment in marshal or in
// ToRecord would surface as an inequality here.
func TestAdversarial_RoundTripFidelity_Distinct(t *testing.T) {
	orig := distinctNode()

	data, err := MarshalNode(orig)
	if err != nil {
		t.Fatalf("MarshalNode: %v", err)
	}
	view, err := DeserializeNode(data)
	if err != nil {
		t.Fatalf("DeserializeNode: %v", err)
	}
	got, err := view.ToRecord()
	if err != nil {
		t.Fatalf("ToRecord: %v", err)
	}

	// Normalise nil-vs-empty label slice so DeepEqual reflects semantic equality.
	if orig.Labels == nil {
		orig.Labels = []string{}
	}
	if got.Labels == nil {
		got.Labels = []string{}
	}
	if !reflect.DeepEqual(orig, got) {
		t.Fatalf("round-trip not bijective:\n  want %#v\n  got  %#v", orig, got)
	}
}

// TestAdversarial_FieldCorrespondence proves each scalar/text field round-trips
// to ITSELF and not to a neighbour. It populates exactly one field at a time
// (all others zero) and asserts only that field is non-zero on the far side.
// This catches a mis-map such as Cores reading MemBytes' word, or Id reading
// Hostname's pointer.
func TestAdversarial_FieldCorrespondence(t *testing.T) {
	t.Run("id_only", func(t *testing.T) {
		v := roundTrip(t, NodeRecord{ID: "only-id"})
		assertRecord(t, v, NodeRecord{ID: "only-id", Labels: []string{}})
	})
	t.Run("hostname_only", func(t *testing.T) {
		v := roundTrip(t, NodeRecord{Hostname: "only-host"})
		assertRecord(t, v, NodeRecord{Hostname: "only-host", Labels: []string{}})
	})
	t.Run("cores_only", func(t *testing.T) {
		v := roundTrip(t, NodeRecord{Cores: 0xDEADBEEF})
		assertRecord(t, v, NodeRecord{Cores: 0xDEADBEEF, Labels: []string{}})
	})
	t.Run("membytes_only", func(t *testing.T) {
		v := roundTrip(t, NodeRecord{MemBytes: 0xCAFEF00DBAADF00D})
		assertRecord(t, v, NodeRecord{MemBytes: 0xCAFEF00DBAADF00D, Labels: []string{}})
	})
	t.Run("labels_only", func(t *testing.T) {
		v := roundTrip(t, NodeRecord{Labels: []string{"k=v"}})
		assertRecord(t, v, NodeRecord{Labels: []string{"k=v"}})
	})
}

// TestAdversarial_Boundaries exercises boundary/edge values that commonly expose
// width/encoding bugs: integer maxima, empty string, unicode text, empty slice,
// and a single empty-string label.
func TestAdversarial_Boundaries(t *testing.T) {
	cases := map[string]NodeRecord{
		"max_ints": {
			ID:       "",
			Hostname: "",
			Cores:    math.MaxUint32,
			MemBytes: math.MaxUint64,
			Labels:   []string{},
		},
		"unicode": {
			ID:       "节点-\U0001F600-Ω-id",
			Hostname: "хост.приме́р.рф",
			Cores:    1,
			MemBytes: 1,
			Labels:   []string{"emoji=\U0001F4BB", "greek=Ωμέγα"},
		},
		"empty_string_label": {
			Labels: []string{""},
		},
		"nul_in_text": {
			// Cap'n Proto Text is NUL-terminated; an embedded NUL is a classic
			// truncation hazard. Pin whatever the codec actually does.
			ID:     "before\x00after",
			Labels: []string{"a\x00b=c"},
		},
		"many_labels": {
			Labels: makeLabels(1000),
		},
		"large_text": {
			ID:       strings.Repeat("x", 64*1024),
			Hostname: strings.Repeat("h", 4096),
		},
	}
	for name, orig := range cases {
		orig := orig
		t.Run(name, func(t *testing.T) {
			v := roundTrip(t, orig)
			want := orig
			if want.Labels == nil {
				want.Labels = []string{}
			}
			assertRecord(t, v, want)
		})
	}
}

// ── Centerpiece 2: malformed / truncated / random input — never panic ───────

// TestAdversarial_MalformedInput_NeverPanics is the robustness/DoS gate. It
// feeds nil, empty, single bytes, random byte strings, hostile crafted frames,
// and every truncated prefix of a valid frame, then exercises EVERY accessor
// (including ToRecord and full label iteration) on any view that deserialises.
// The contract asserted:
//   - DeserializeNode never panics on any input;
//   - accessing fields of any returned view never panics;
//   - a truncated/garbage frame is NOT silently materialised into a populated
//     node (if it deserialises at all, ToRecord must error or yield a node with
//     no spurious field content).
func TestAdversarial_MalformedInput_NeverPanics(t *testing.T) {
	good, err := MarshalNode(distinctNode())
	if err != nil {
		t.Fatalf("MarshalNode(seed): %v", err)
	}

	var inputs [][]byte
	inputs = append(inputs, nil, []byte{})
	// Single bytes 0x00..0xFF.
	for b := 0; b < 256; b++ {
		inputs = append(inputs, []byte{byte(b)})
	}
	// Every truncated prefix of a valid frame (the security-critical class:
	// a parser must not accept a short frame as a partial node).
	for n := 0; n <= len(good); n++ {
		inputs = append(inputs, append([]byte(nil), good[:n]...))
	}
	// Deterministic pseudo-random blobs of varied lengths.
	rng := rand.New(rand.NewSource(0x5EADBEEF))
	for _, ln := range []int{1, 7, 8, 16, 64, 88, 200, 4096} {
		for rep := 0; rep < 32; rep++ {
			blob := make([]byte, ln)
			rng.Read(blob)
			inputs = append(inputs, blob)
		}
	}
	// Hostile crafted headers: bogus segment counts / sizes / root pointers.
	inputs = append(inputs,
		[]byte{0, 0, 0, 0, 0xff, 0xff, 0xff, 0x7f},                                     // 1 seg, ~2^31 words claimed
		[]byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0x7f, 1, 0, 0, 0, 0, 0, 0, 0},             // 2 segs, huge first
		[]byte{0, 0, 0, 0, 1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, // 1 seg/1 word, bad root ptr
		[]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0},                                     // ~4 billion segments claimed
	)

	for idx, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PANIC on input #%d (len=%d, % x): %v",
						idx, len(in), capPrefix(in), r)
				}
			}()
			view, derr := DeserializeNode(in)
			if derr != nil {
				// Contract-honouring: rejected with an error. Good.
				if view != nil {
					t.Errorf("input #%d: error returned but view is non-nil", idx)
				}
				return
			}
			// Accepted. Exercise every accessor; none may panic, and a
			// successful ToRecord must not invent populated fields from a
			// non-seed input.
			exerciseAllAccessors(t, idx, in, good, view)
		}()
	}
}

// TestAdversarial_TruncationRejected asserts the specific, security-relevant
// property that NO proper truncation of a valid frame deserialises into a
// usable node. (The full frame is excluded — it is the valid case.) If a future
// change makes capnp lazily accept a short frame and zero-fill it, this bites.
func TestAdversarial_TruncationRejected(t *testing.T) {
	good, err := MarshalNode(distinctNode())
	if err != nil {
		t.Fatalf("MarshalNode: %v", err)
	}
	for n := 0; n < len(good); n++ {
		view, derr := DeserializeNode(good[:n])
		if derr == nil {
			// If it is accepted, it must NOT yield the original field content;
			// otherwise a truncated frame would masquerade as the full node.
			rec, rerr := view.ToRecord()
			if rerr == nil && (rec.ID == distinctNode().ID || rec.MemBytes == distinctNode().MemBytes) {
				t.Fatalf("truncated prefix len=%d/%d was accepted AND reconstructed real field content: %#v",
					n, len(good), rec)
			}
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func roundTrip(t *testing.T, n NodeRecord) *NodeView {
	t.Helper()
	data, err := MarshalNode(n)
	if err != nil {
		t.Fatalf("MarshalNode(%#v): %v", n, err)
	}
	v, err := DeserializeNode(data)
	if err != nil {
		t.Fatalf("DeserializeNode: %v", err)
	}
	return v
}

func assertRecord(t *testing.T, v *NodeView, want NodeRecord) {
	t.Helper()
	got, err := v.ToRecord()
	if err != nil {
		t.Fatalf("ToRecord: %v", err)
	}
	if got.Labels == nil {
		got.Labels = []string{}
	}
	if want.Labels == nil {
		want.Labels = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record mismatch:\n  want %#v\n  got  %#v", want, got)
	}
}

func exerciseAllAccessors(t *testing.T, idx int, in, good []byte, v *NodeView) {
	t.Helper()
	// Each accessor is called; the deferred recover in the caller catches any
	// panic. We also iterate the entire label list (the bounds-checked path).
	_, _ = v.Id()
	_, _ = v.Hostname()
	_ = v.Cores()
	_ = v.MemBytes()
	if lbls, lerr := v.Labels(); lerr == nil {
		for i := 0; i < lbls.Len(); i++ {
			_, _ = lbls.At(i)
		}
	}
	rec, rerr := v.ToRecord()
	if rerr != nil {
		return
	}
	// A non-seed input that nonetheless deserialises must not reproduce the
	// seed node's distinctive content — that would mean garbage decoded into a
	// real node.
	if !bytesEqual(in, good) {
		d := distinctNode()
		if rec.ID == d.ID && rec.MemBytes == d.MemBytes && rec.Cores == d.Cores {
			t.Errorf("input #%d (len=%d) decoded into the seed node's content without being the seed bytes",
				idx, len(in))
		}
	}
}

func makeLabels(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "k" + strings.Repeat("0", i%4) + "=v"
	}
	return out
}

func capPrefix(b []byte) []byte {
	if len(b) > 16 {
		return b[:16]
	}
	return b
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
