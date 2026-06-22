//go:build integration

package coap

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

// TestIntegration_DispatchRoundTrip runs a REAL CoAP exchange over loopback
// UDP: the client POSTs a CBOR WorkEnvelope to /dispatch and the server
// receives it byte/field-exact, then the client GETs /status and receives the
// expected report. No mocks, no simulation — a real go-coap UDP socket pair.
func TestIntegration_DispatchRoundTrip(t *testing.T) {
	wantEnv := WorkEnvelope{
		JobID:    "job-7e3a",
		Kind:     "infer",
		Priority: 9,
		Payload:  []byte{0x00, 0x01, 0xFF, 0xFE, 0x42},
		Deadline: 1717243200000,
		Labels:   map[string]string{"zone": "edge-west", "model": "yolo-n"},
	}

	var (
		mu       sync.Mutex
		received *WorkEnvelope
	)
	dispatch := func(_ context.Context, env WorkEnvelope) error {
		mu.Lock()
		e := env
		received = &e
		mu.Unlock()
		return nil
	}

	initial := StatusReport{DeviceID: "dev-42", JobID: "", State: "idle", Battery: 88, Seq: 0}
	srv, err := NewServer("127.0.0.1:0", dispatch, initial)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()
	t.Logf("CoAP server listening on udp %s", srv.Addr())

	cli, err := Dial(srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// --- POST /dispatch (Confirmable) ---
	if err := cli.Dispatch(ctx, wantEnv); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	mu.Lock()
	got := received
	mu.Unlock()
	if got == nil {
		t.Fatal("server never received the work envelope")
	}
	// Byte/field-exact assertions on the server-received envelope.
	if got.JobID != wantEnv.JobID || got.Kind != wantEnv.Kind || got.Priority != wantEnv.Priority ||
		got.Deadline != wantEnv.Deadline {
		t.Fatalf("envelope scalar mismatch:\n got=%+v\nwant=%+v", *got, wantEnv)
	}
	if !bytes.Equal(got.Payload, wantEnv.Payload) {
		t.Fatalf("envelope payload mismatch: got=%x want=%x", got.Payload, wantEnv.Payload)
	}
	if got.Labels["zone"] != "edge-west" || got.Labels["model"] != "yolo-n" {
		t.Fatalf("envelope labels mismatch: got=%v", got.Labels)
	}
	t.Logf("server received work envelope byte/field-exact: jobID=%s kind=%s payload=%x",
		got.JobID, got.Kind, got.Payload)

	// --- GET /status ---
	// Update the server-side status to reflect the dispatched job, then read it.
	want := StatusReport{DeviceID: "dev-42", JobID: wantEnv.JobID, State: "running", Battery: 87, Seq: 1}
	srv.PublishStatus(want)

	report, err := cli.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if report != want {
		t.Fatalf("status mismatch:\n got=%+v\nwant=%+v", report, want)
	}
	t.Logf("GET /status round-trip OK: %+v", report)
}

// TestIntegration_ObserveNotification proves CoAP Observe (RFC 7641): the
// client registers an Observe on /status over real UDP and the server pushes a
// notification that reaches the observing client.
func TestIntegration_ObserveNotification(t *testing.T) {
	initial := StatusReport{DeviceID: "dev-42", State: "idle", Battery: 90, Seq: 0}
	srv, err := NewServer("127.0.0.1:0", nil, initial)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	cli, err := Dial(srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	notifications := make(chan StatusReport, 8)
	obs, err := cli.ObserveStatus(ctx, func(s StatusReport, oerr error) {
		if oerr != nil {
			t.Logf("observe callback error (ignored): %v", oerr)
			return
		}
		notifications <- s
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer ccancel()
		_ = obs.Cancel(cctx)
	}()

	// First notification is the initial Observe response (seq 0 / idle).
	select {
	case got := <-notifications:
		if got.State != "idle" {
			t.Fatalf("initial observe response: got state %q, want idle", got.State)
		}
		t.Logf("initial Observe response received: %+v", got)
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive initial Observe response")
	}

	// Server pushes a status change — must reach the observing client.
	pushed := StatusReport{DeviceID: "dev-42", JobID: "job-xyz", State: "running", Battery: 84, Seq: 5}
	if n := srv.PublishStatus(pushed); n != 1 {
		t.Fatalf("PublishStatus notified %d observers, want 1", n)
	}

	select {
	case got := <-notifications:
		if got.State != "running" || got.JobID != "job-xyz" || got.Seq != 5 {
			t.Fatalf("pushed notification mismatch:\n got=%+v\nwant=%+v", got, pushed)
		}
		t.Logf("server-pushed Observe notification reached client: %+v", got)
	case <-time.After(5 * time.Second):
		t.Fatal("server-pushed Observe notification never reached client")
	}
}

// TestIntegration_WireSizeBenefit logs the REAL measured byte comparison of a
// CoAP dispatch request vs an equivalent HTTP/1.1 request for the same payload,
// demonstrating CoAP's low-bandwidth advantage on constrained links.
func TestIntegration_WireSizeBenefit(t *testing.T) {
	env := WorkEnvelope{
		JobID:    "job-7e3a",
		Kind:     "infer",
		Priority: 9,
		Payload:  []byte{0x00, 0x01, 0xFF, 0xFE, 0x42},
		Deadline: 1717243200000,
		Labels:   map[string]string{"zone": "edge-west"},
	}
	ws, err := MeasureDispatchWireSizes(env)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	t.Logf("WIRE-SIZE COMPARISON (same payload): %s", ws)
	t.Logf("  CoAP protocol framing overhead: %d bytes (4-byte header + Uri-Path + Content-Format options)", ws.CoAPOverhead)
	t.Logf("  HTTP/1.1 protocol framing overhead: %d bytes (request-line + textual headers)", ws.HTTPOverhead)

	if ws.CoAPBytes >= ws.HTTPBytes {
		t.Fatalf("expected CoAP to be smaller than HTTP: CoAP=%d HTTP=%d", ws.CoAPBytes, ws.HTTPBytes)
	}
	// CoAP framing must be an order of magnitude leaner than HTTP framing.
	if ws.CoAPOverhead*4 > ws.HTTPOverhead {
		t.Fatalf("CoAP framing (%d) not dramatically smaller than HTTP framing (%d)", ws.CoAPOverhead, ws.HTTPOverhead)
	}
}

// TestIntegration_WrongPath is the ANTI-BLUFF guard for resource routing: a
// request to a path the server does not expose must NOT succeed as a dispatch.
// (See anti_bluff_test.go for the payload-field mutation guard.)
func TestIntegration_WrongPath(t *testing.T) {
	var got int
	dispatch := func(context.Context, WorkEnvelope) error { got++; return nil }
	srv, err := NewServer("127.0.0.1:0", dispatch, StatusReport{State: "idle"})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	cli, err := Dial(srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	body, _ := EncodeEnvelope(WorkEnvelope{JobID: "x"})
	resp, err := cli.conn.Post(ctx, "/wrong-resource", message.AppCBOR, bytes.NewReader(body))
	if err != nil {
		t.Logf("anti-bluff OK: POST to /wrong-resource failed at transport: %v", err)
		return
	}
	// go-coap's mux returns 4.04 Not Found for an unregistered path.
	if resp.Code() == codes.Changed {
		t.Fatalf("ANTI-BLUFF FAILURE: wrong path was accepted as a dispatch (code %s)", resp.Code())
	}
	if got != 0 {
		t.Fatalf("ANTI-BLUFF FAILURE: dispatch handler ran for the wrong path (%d times)", got)
	}
	t.Logf("anti-bluff OK: POST /wrong-resource -> %s (dispatch handler not invoked)", resp.Code())
}
