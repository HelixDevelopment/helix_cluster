// Package session provides tests for the Session gRPC server.
package session

import (
	"context"
	"net"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// wantCode asserts that err is a gRPC status error carrying the given code.
func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("error code = %s, want %s (err: %v)", got, want, err)
	}
}

func startTestServer(t *testing.T) (helixv1.SessionServiceClient, func()) {
	t.Helper()

	// Use nil backend for hermetic behavior tests: they assert gRPC protocol
	// correctness (field round-trips, validation, FSM guards), not whether a
	// real tmux process was launched.  A nil backend keeps the tests fast,
	// deterministic, and free of tmux session accumulation.
	srv := NewServerWithTmuxBackend(nil)
	gs := grpc.NewServer()
	helixv1.RegisterSessionServiceServer(gs, srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		if err := gs.Serve(lis); err != nil {
			t.Logf("serve: %v", err)
		}
	}()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	client := helixv1.NewSessionServiceClient(conn)
	return client, func() {
		conn.Close()
		gs.GracefulStop()
	}
}

func TestServerCreateSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "test", Owner: "alice"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.Name != "test" {
		t.Errorf("name = %s, want test", resp.Name)
	}
	if resp.Owner != "alice" {
		t.Errorf("owner = %s, want alice", resp.Owner)
	}
}

func TestServerGetSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s1", Owner: "bob"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Id != created.Id {
		t.Errorf("id = %s, want %s", got.Id, created.Id)
	}
}

func TestServerListSessions(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s2", Owner: "carol"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	list, err := client.ListSessions(ctx, &helixv1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list.Sessions) == 0 {
		t.Error("expected at least one session")
	}
}

func TestServerUpdateSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s3", Owner: "dave"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	updated, err := client.UpdateSession(ctx, &helixv1.UpdateSessionRequest{SessionId: created.Id, Status: "paused"})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if updated.Status != "paused" {
		t.Errorf("status = %s, want paused", updated.Status)
	}
}

func TestServerDeleteSession(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s4", Owner: "eve"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	resp, err := client.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !resp.Success {
		t.Error("expected success=true")
	}
}

// TestUpdateSessionRejectsInvalidStatus proves the core anti-bluff fix: an
// unrecognized status string must be rejected with InvalidArgument and MUST NOT
// silently coerce the session to "pending" (StatusFromString's default).
//
// Mutation that makes this FAIL: in UpdateSession, delete the
// `if req.Status != "" && !validStatus(req.Status) { return InvalidArgument }`
// guard. Then "runnig" passes through, StatusFromString returns StatusPending,
// the update succeeds, and the status assertion below flips.
func TestUpdateSessionRejectsInvalidStatus(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s", Owner: "o"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.Status != "running" {
		t.Fatalf("precondition: created status = %s, want running", created.Status)
	}

	_, err = client.UpdateSession(ctx, &helixv1.UpdateSessionRequest{SessionId: created.Id, Status: "runnig"})
	wantCode(t, err, codes.InvalidArgument)

	// Sink-side proof: the session was NOT silently reset to pending.
	got, err := client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("status corrupted to %s; want unchanged running", got.Status)
	}
}

// TestCreateSessionRoundTripsModeAndGPU proves the Mode and GpuIds fields, which
// the proto carries but the original server dropped, now survive the round trip.
//
// Mutation that makes this FAIL: in sessionToProto, change `Mode: sess.Labels[modeLabel]`
// to `Mode: ""` (or in CreateSession drop the Labels/GPURequest assignment).
func TestCreateSessionRoundTripsModeAndGPU(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{
		Name:  "gpu-sess",
		Owner: "o",
		Mode:  "interactive",
		Resources: &helixv1.ResourceAllocation{
			CpuMillicores: 500,
			MemoryBytes:   1 << 20,
			GpuIds:        []string{"gpu-0", "gpu-1"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.Mode != "interactive" {
		t.Errorf("mode = %q, want interactive", created.Mode)
	}
	if created.Resources == nil || len(created.Resources.GpuIds) != 2 {
		t.Fatalf("gpu_ids not preserved: %+v", created.Resources)
	}

	// Sink-side proof: a fresh Get from the manager also carries the fields.
	got, err := client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Mode != "interactive" {
		t.Errorf("persisted mode = %q, want interactive", got.Mode)
	}
	if got.Resources.GpuIds[0] != "gpu-0" || got.Resources.GpuIds[1] != "gpu-1" {
		t.Errorf("persisted gpu_ids = %v, want [gpu-0 gpu-1]", got.Resources.GpuIds)
	}
}

// TestCreateSessionRejectsUnknownBackend proves backend validation.
//
// Mutation that makes this FAIL: in CreateSession, delete the
// `if !validBackend(req.Backend) { return InvalidArgument }` guard, so an
// arbitrary backend string is accepted and CreateSession returns nil error.
func TestCreateSessionRejectsUnknownBackend(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{
		Name:    "s",
		Owner:   "o",
		Backend: "not-a-real-backend",
	})
	wantCode(t, err, codes.InvalidArgument)

	// A recognized backend must still succeed.
	ok, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{
		Name:    "s2",
		Owner:   "o",
		Backend: "zellij",
	})
	if err != nil {
		t.Fatalf("CreateSession with valid backend: %v", err)
	}
	if ok.Backend != "zellij" {
		t.Errorf("backend = %q, want zellij", ok.Backend)
	}
}

// TestCreateSessionRequiresNameAndOwner proves required-field validation.
//
// Mutation that makes this FAIL: in CreateSession, delete the
// `if req.Name == "" { return InvalidArgument }` guard; the empty-name call
// then succeeds and wantCode fails on a nil error.
func TestCreateSessionRequiresNameAndOwner(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Owner: "o"})
	wantCode(t, err, codes.InvalidArgument)

	_, err = client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "n"})
	wantCode(t, err, codes.InvalidArgument)
}

// TestCreateSessionRejectsNegativeResources proves resource validation for both
// guarded fields (memory and cpu), each exercised independently so neither guard
// can be deleted without a test failing.
//
// Mutation A that makes this FAIL: in CreateSession, delete the
// `if req.Resources.MemoryBytes < 0 { return InvalidArgument }` guard; the
// negative-memory request then succeeds.
// Mutation B that makes this FAIL: in CreateSession, delete the
// `if req.Resources.CpuMillicores < 0 { return InvalidArgument }` guard; the
// negative-cpu request then succeeds.
func TestCreateSessionRejectsNegativeResources(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{
		Name:      "s",
		Owner:     "o",
		Resources: &helixv1.ResourceAllocation{MemoryBytes: -1},
	})
	wantCode(t, err, codes.InvalidArgument)

	// Negative CPU is guarded independently and must also be rejected.
	_, err = client.CreateSession(ctx, &helixv1.CreateSessionRequest{
		Name:      "s-cpu",
		Owner:     "o",
		Resources: &helixv1.ResourceAllocation{CpuMillicores: -1},
	})
	wantCode(t, err, codes.InvalidArgument)
}

// TestEmptyIDsReturnInvalidArgument proves Get/Update/Delete distinguish a
// missing identifier (InvalidArgument) from a non-existent one (NotFound).
//
// Mutation that makes this FAIL: in GetSession, delete the
// `if req.SessionId == "" { return InvalidArgument }` guard; the empty-ID Get
// then falls through to the manager and returns NotFound, flipping the assert.
func TestEmptyIDsReturnInvalidArgument(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: ""})
	wantCode(t, err, codes.InvalidArgument)

	_, err = client.UpdateSession(ctx, &helixv1.UpdateSessionRequest{SessionId: ""})
	wantCode(t, err, codes.InvalidArgument)

	_, err = client.DeleteSession(ctx, &helixv1.DeleteSessionRequest{SessionId: ""})
	wantCode(t, err, codes.InvalidArgument)

	// A well-formed but unknown ID must still be NotFound, not InvalidArgument.
	_, err = client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: "session-does-not-exist"})
	wantCode(t, err, codes.NotFound)
}

// TestListSessionsRejectsUnknownStatusFilter proves the list status filter is
// validated rather than silently returning an empty slice for a typo'd filter.
//
// Mutation that makes this FAIL: in ListSessions, delete the
// `if req.Status != "" && !validStatus(req.Status) { return InvalidArgument }`
// guard; the bad filter then yields a successful empty response.
func TestListSessionsRejectsUnknownStatusFilter(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s", Owner: "o"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err := client.ListSessions(ctx, &helixv1.ListSessionsRequest{Status: "runing"})
	wantCode(t, err, codes.InvalidArgument)

	// A valid status filter must work and select the running session.
	list, err := client.ListSessions(ctx, &helixv1.ListSessionsRequest{Status: "running"})
	if err != nil {
		t.Fatalf("ListSessions(running): %v", err)
	}
	if len(list.Sessions) == 0 {
		t.Error("expected the running session to be listed")
	}
}

// TestUpdateSessionAppliesValidStatusAndResources proves a legal update is
// actually persisted (sink-side), guarding against a silent no-op regression.
//
// Mutation that makes this FAIL: in UpdateSession, change the mutator body so
// it no longer assigns `s.Status = session.StatusFromString(req.Status)`; the
// follow-up Get then still reports "running".
func TestUpdateSessionAppliesValidStatusAndResources(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s", Owner: "o"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err = client.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
		SessionId: created.Id,
		Status:    "paused",
		Resources: &helixv1.ResourceAllocation{CpuMillicores: 250, MemoryBytes: 4096},
	})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	got, err := client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != "paused" {
		t.Errorf("status = %s, want paused", got.Status)
	}
	if got.Resources.CpuMillicores != 250 || got.Resources.MemoryBytes != 4096 {
		t.Errorf("resources = %+v, want cpu=250 mem=4096", got.Resources)
	}
}

// TestUpdateSessionPersistsGpuIds proves that an UpdateSession carrying new
// GpuIds actually stores them — guarding against the silent-drop defect where
// the mutator updated CPU/memory but left GPURequest untouched.
//
// Mutation that makes this FAIL: in UpdateSession's mutator closure, delete the
// `s.GPURequest = req.Resources.GpuIds` assignment; the follow-up Get then still
// reports the original (empty) GPU list and the assertion below flips.
func TestUpdateSessionPersistsGpuIds(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create with no GPUs so the update is the only source of the IDs.
	created, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "s", Owner: "o"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.Resources != nil && len(created.Resources.GpuIds) != 0 {
		t.Fatalf("precondition: created with unexpected gpus %v", created.Resources.GpuIds)
	}

	_, err = client.UpdateSession(ctx, &helixv1.UpdateSessionRequest{
		SessionId: created.Id,
		Resources: &helixv1.ResourceAllocation{GpuIds: []string{"gpu-7", "gpu-8"}},
	})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	// Sink-side proof: a fresh Get reflects the GPUs supplied to UpdateSession.
	got, err := client.GetSession(ctx, &helixv1.GetSessionRequest{SessionId: created.Id})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Resources == nil || len(got.Resources.GpuIds) != 2 {
		t.Fatalf("gpu_ids not persisted by UpdateSession: %+v", got.Resources)
	}
	if got.Resources.GpuIds[0] != "gpu-7" || got.Resources.GpuIds[1] != "gpu-8" {
		t.Errorf("persisted gpu_ids = %v, want [gpu-7 gpu-8]", got.Resources.GpuIds)
	}
}

// TestConcurrentCreateAndList exercises the server under -race to prove the
// RWMutex/manager locking holds up under concurrent create + list traffic.
//
// Mutation that makes this FAIL (under -race): none needed for correctness, but
// removing the manager's internal locking would make this report a data race.
func TestConcurrentCreateAndList(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const n = 16
	errCh := make(chan error, n*2)
	for i := 0; i < n; i++ {
		go func() {
			_, err := client.CreateSession(ctx, &helixv1.CreateSessionRequest{Name: "c", Owner: "race"})
			errCh <- err
		}()
		go func() {
			_, err := client.ListSessions(ctx, &helixv1.ListSessionsRequest{Owner: "race"})
			errCh <- err
		}()
	}
	for i := 0; i < n*2; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent op: %v", err)
		}
	}

	list, err := client.ListSessions(ctx, &helixv1.ListSessionsRequest{Owner: "race"})
	if err != nil {
		t.Fatalf("final ListSessions: %v", err)
	}
	if len(list.Sessions) != n {
		t.Errorf("listed %d sessions, want %d", len(list.Sessions), n)
	}
}
