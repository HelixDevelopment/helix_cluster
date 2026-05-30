package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"build"
	helixv1 "api"
	"google.golang.org/grpc"
)

type server struct {
	helixv1.UnimplementedBuildServiceServer
	svc *build.Service
}

func (s *server) SubmitBuild(ctx context.Context, req *helixv1.SubmitBuildRequest) (*helixv1.SubmitBuildResponse, error) {
	j := &build.Job{
		ID:             generateID(),
		RepoURL:        req.RepoUrl,
		Ref:            req.Ref,
		DockerfilePath: req.DockerfilePath,
		BuildArgs:      req.BuildArgs,
	}
	if err := s.svc.Submit(j); err != nil {
		return nil, fmt.Errorf("submit: %w", err)
	}
	return &helixv1.SubmitBuildResponse{BuildId: j.ID, Queued: true}, nil
}

func (s *server) GetBuildStatus(ctx context.Context, req *helixv1.GetBuildStatusRequest) (*helixv1.BuildStatus, error) {
	j, err := s.svc.Get(req.BuildId)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}
	return &helixv1.BuildStatus{
		BuildId:     j.ID,
		State:       string(j.State),
		StartedAt:   timestamp(j.StartedAt),
		CompletedAt: timestamp(j.CompletedAt),
		ImageTag:    j.ImageTag,
	}, nil
}

func (s *server) StreamBuildLogs(req *helixv1.StreamBuildLogsRequest, stream helixv1.BuildService_StreamBuildLogsServer) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		default:
		}

		j, err := s.svc.Get(req.BuildId)
		if err != nil {
			return err
		}

		logs := j.GetLogs()
		for _, line := range logs {
			if err := stream.Send(&helixv1.BuildLogLine{
				BuildId:   j.ID,
				Line:      line,
				Timestamp: time.Now().Unix(),
			}); err != nil {
				return err
			}
		}

		if j.IsTerminal() {
			return nil
		}
	}
}

func (s *server) CancelBuild(ctx context.Context, req *helixv1.CancelBuildRequest) (*helixv1.CancelBuildResponse, error) {
	if err := s.svc.Cancel(req.BuildId); err != nil {
		return nil, fmt.Errorf("cancel: %w", err)
	}
	return &helixv1.CancelBuildResponse{Cancelled: true}, nil
}

var idCounter int

func generateID() string {
	idCounter++
	return fmt.Sprintf("build-%d-%d", os.Getpid(), idCounter)
}

func timestamp(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

func main() {
	port := os.Getenv("HELIX_BUILD_PORT")
	if port == "" {
		port = "50051"
	}

	svc := build.NewService(4)
	svc.Start(context.Background())
	defer svc.Stop()

	s := grpc.NewServer()
	helixv1.RegisterBuildServiceServer(s, &server{svc: svc})

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	go func() {
		log.Printf("helix-build listening on :%s", port)
		if err := s.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	s.GracefulStop()
}
