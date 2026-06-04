package hlc

import (
	"bytes"
	"testing"
)

// FuzzDecode feeds arbitrary bytes to Decode — the parse-from-bytes entry point
// of the HLC wire format. Invariant: any input that is not exactly the encoded
// length must return ErrShortBuffer with no panic; any input that IS the right
// length must decode successfully and round-trip back to the same bytes via
// Encode (Decode is the documented exact inverse of Encode).
func FuzzDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, encodedLen))
	f.Add(make([]byte, encodedLen-1))
	f.Add(Timestamp{Physical: 123456789, Logical: 7}.Encode())

	f.Fuzz(func(t *testing.T, b []byte) {
		ts, err := Decode(b)
		if err != nil {
			if err != ErrShortBuffer {
				t.Fatalf("unexpected error from Decode: %v", err)
			}
			return
		}
		// Correct-length input: Decode succeeded, so Encode(ts) must reproduce
		// the exact input bytes (lossless inverse).
		if got := ts.Encode(); !bytes.Equal(got, b) {
			t.Fatalf("round-trip mismatch: Decode(b).Encode()=%x, want %x", got, b)
		}
	})
}
