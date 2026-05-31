// Package scheduler provides tests for the Scheduler gRPC server.
package scheduler

import (
	"context"
	"net"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startTestServer(t *testing.T) (helixv1.SchedulerServiceClient, func()) {
	t.Helper()

	srv := NewServer()
	gs := grpc.NewServer()
	helixv1.RegisterSchedulerServiceServer(gs, srv)

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

	client := helixv1.NewSchedulerServiceClient(conn)
	return client, func() {
		conn.Close()
		gs.GracefulStop()
	}
}

func TestServerScheduleJob(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "job-1"})
	if err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if !resp.Scheduled {
		t.Error("expected scheduled=true")
	}
	if resp.JobId != "job-1" {
		t.Errorf("job id = %s, want job-1", resp.JobId)
	}
}

func TestServerGetJobStatus(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "job-2"}); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	status, err := client.GetJobStatus(ctx, &helixv1.GetJobStatusRequest{JobId: "job-2"})
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	if status.JobId != "job-2" {
		t.Errorf("job id = %s, want job-2", status.JobId)
	}
}

func TestServerCancelJob(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "job-3"}); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	resp, err := client.CancelJob(ctx, &helixv1.CancelJobRequest{JobId: "job-3"})
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if !resp.Cancelled {
		t.Error("expected cancelled=true")
	}
}

func TestServerListJobs(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "job-4"}); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	list, err := client.ListJobs(ctx, &helixv1.ListJobsRequest{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list.Jobs) == 0 {
		t.Error("expected at least one job")
	}
}

func TestServerStreamJobEvents(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "job-5"}); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	stream, err := client.StreamJobEvents(ctx, &helixv1.StreamJobEventsRequest{JobId: "job-5"})
	if err != nil {
		t.Fatalf("StreamJobEvents: %v", err)
	}

	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}
}
