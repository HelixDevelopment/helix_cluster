package messaging

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecodeMessageNilHeadersGuard proves DecodeMessage always yields a usable
// (non-nil) Headers map even when the wire form omitted headers entirely. A
// caller that does decoded.Headers["k"]="v" must not panic.
func TestDecodeMessageNilHeadersGuard(t *testing.T) {
	// Hand-rolled JSON with no "headers" key (omitempty drops nil maps).
	raw := []byte(`{"id":"abc","topic":"t","payload":null,"timestamp":"2020-01-01T00:00:00Z"}`)

	decoded, err := DecodeMessage(raw)
	require.NoError(t, err)
	require.NotNil(t, decoded.Headers, "Headers must be initialised to a usable map")

	// The real end-user behaviour: writing to it must not panic.
	decoded.Headers["k"] = "v"
	assert.Equal(t, "v", decoded.Headers["k"])
}

// TestDecodeMessageInvalidJSON proves malformed input is reported as an error
// rather than yielding a zero-value Message that a caller would mistake for
// valid data.
func TestDecodeMessageInvalidJSON(t *testing.T) {
	_, err := DecodeMessage([]byte("{not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

// TestMessageEncodePreservesBinaryPayload proves an arbitrary binary payload
// survives Encode/Decode byte-for-byte (JSON base64-encodes []byte; a broken
// codec would corrupt it).
func TestMessageEncodePreservesBinaryPayload(t *testing.T) {
	payload := []byte{0x00, 0xff, 0x10, 0x7f, 0x80}
	original := NewMessage("bin", payload)

	data, err := original.Encode()
	require.NoError(t, err)

	decoded, err := DecodeMessage(data)
	require.NoError(t, err)
	assert.Equal(t, payload, decoded.Payload)
}
