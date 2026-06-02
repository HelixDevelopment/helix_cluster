package deviceplugin

import (
	"reflect"
	"testing"
)

// TestParseGRES_TableDriven proves descriptor parsing for the canonical forms,
// including the multi-component example from the spec.
func TestParseGRES_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    ResourceRequest
		wantErr bool
	}{
		{
			name: "spec example three components",
			in:   "gpu:rtx3080:1,memory:10Gi,pcie:16GT/s",
			want: ResourceRequest{
				{Kind: "gpu", Model: "rtx3080", Count: 1},
				{Kind: "memory", Quantity: "10Gi"},
				{Kind: "pcie", Quantity: "16GT/s"},
			},
		},
		{
			name: "device with count two plus memory",
			in:   "gpu:rtx3080:2,memory:10Gi",
			want: ResourceRequest{
				{Kind: "gpu", Model: "rtx3080", Count: 2},
				{Kind: "memory", Quantity: "10Gi"},
			},
		},
		{
			name: "bare counted kind",
			in:   "gpu:4",
			want: ResourceRequest{{Kind: "gpu", Count: 4}},
		},
		{
			name: "whitespace tolerated",
			in:   "  gpu : rtx3080 : 2 , memory : 10Gi ",
			want: ResourceRequest{
				{Kind: "gpu", Model: "rtx3080", Count: 2},
				{Kind: "memory", Quantity: "10Gi"},
			},
		},
		{name: "empty yields empty", in: "   ", want: ResourceRequest{}},
		{name: "non integer count rejected", in: "gpu:rtx3080:two", wantErr: true},
		{name: "negative count rejected", in: "gpu:rtx3080:-1", wantErr: true},
		{name: "too many fields rejected", in: "a:b:c:d", wantErr: true},
		{name: "empty token rejected", in: "gpu:rtx3080:1,,memory:10Gi", wantErr: true},
		{name: "single field rejected", in: "gpu", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseGRES(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseGRES(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGRES(%q) unexpected error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseGRES(%q)\n got=%#v\nwant=%#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseGRES_IntegerQuantityPrecedence locks the documented grammar
// precedence for a 2-field token whose value is a bare integer: the integer
// (count) branch wins over the opaque-quantity branch. So "pcie:8" parses to
// {Kind:"pcie", Count:8} (NOT Quantity:"8") and therefore formats back to
// "pcie:8" — a deliberate, documented round-trip where the value lands in Count.
//
// This is the lossy-but-defined case flagged in review: without a test, an
// accidental flip of the strconv.Atoi branch order in ParseGRES (treating a bare
// integer as a Quantity) would go unnoticed. This test fails if that branch is
// inverted or removed.
func TestParseGRES_IntegerQuantityPrecedence(t *testing.T) {
	got, err := ParseGRES("pcie:8")
	if err != nil {
		t.Fatalf("ParseGRES(\"pcie:8\"): %v", err)
	}
	want := ResourceRequest{{Kind: "pcie", Count: 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseGRES(\"pcie:8\")\n got=%#v\nwant=%#v", got, want)
	}
	// The integer branch MUST have won: Count set, Quantity empty.
	if got[0].Count != 8 || got[0].Quantity != "" {
		t.Fatalf("integer precedence violated: %#v (want Count=8, Quantity empty)", got[0])
	}
	// Round-trip formats back to the same token (value carried in Count).
	if s := got.String(); s != "pcie:8" {
		t.Fatalf("round-trip of bare-integer quantity: got %q want %q", s, "pcie:8")
	}

	// Contrast: a non-integer 2-field value takes the Quantity branch.
	q, err := ParseGRES("pcie:16GT/s")
	if err != nil {
		t.Fatalf("ParseGRES(\"pcie:16GT/s\"): %v", err)
	}
	if q[0].Quantity != "16GT/s" || q[0].Count != 0 {
		t.Fatalf("non-integer quantity branch violated: %#v", q[0])
	}
}

// TestParseGRES_RoundTrip proves the structured request formats back to the
// canonical descriptor string (closure criterion #2).
func TestParseGRES_RoundTrip(t *testing.T) {
	const canonical = "gpu:rtx3080:2,memory:10Gi,pcie:16GT/s"
	req, err := ParseGRES(canonical)
	if err != nil {
		t.Fatalf("ParseGRES: %v", err)
	}
	if got := req.String(); got != canonical {
		t.Fatalf("round-trip mismatch:\n got=%q\nwant=%q", got, canonical)
	}

	// And the structured request must carry the parsed device request intact.
	if len(req) == 0 || !req[0].IsDeviceRequest() || req[0].Model != "rtx3080" || req[0].Count != 2 {
		t.Fatalf("structured request not as expected: %#v", req)
	}

	// Re-parsing the formatted output must yield an equal structure (stable).
	req2, err := ParseGRES(req.String())
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if !reflect.DeepEqual(req, req2) {
		t.Fatalf("re-parse not stable:\n a=%#v\n b=%#v", req, req2)
	}
}
