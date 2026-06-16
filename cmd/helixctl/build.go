package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/spf13/cobra"
)

// newBuildCmd assembles the `helixctl build` command group: a set of thin gRPC
// clients of BuildService. Each leaf command dials the service, then delegates
// to a runXxx action function (the testable seam).
func newBuildCmd() *cobra.Command {
	build := &cobra.Command{
		Use:   "build",
		Short: "Submit and inspect container builds via the build service",
		// RunE only fires when no leaf subcommand matched. With no leaf args we
		// show help (exit 0); with a leftover token the user mistyped a
		// subcommand, so we MUST surface an error (non-zero exit) instead of
		// silently printing help and exiting 0 — a silent no-op would make a
		// script's `helixctl build submt ... || handle_error` never trip.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
		},
	}
	build.AddCommand(newBuildSubmitCmd())
	build.AddCommand(newBuildStatusCmd())
	build.AddCommand(newBuildCancelCmd())
	build.AddCommand(newBuildLogsCmd())
	return build
}

// withClient dials the build service, runs fn with the connected client under a
// timeout-bounded context derived from the command's context, and closes the
// connection. It centralises the connect/teardown boilerplate for the leaf
// commands so each action stays focused on its single RPC.
func withClient(cmd *cobra.Command, fn func(ctx context.Context, client helixv1.BuildServiceClient) error) error {
	conn := connection()
	ctx, cancel := context.WithTimeout(cmd.Context(), conn.Timeout)
	defer cancel()

	client, closeConn, err := dialClient(ctx, conn)
	if err != nil {
		return err
	}
	defer func() { _ = closeConn() }()

	return fn(ctx, client)
}

func newBuildSubmitCmd() *cobra.Command {
	var (
		repoURL        string
		ref            string
		dockerfilePath string
		buildArgs      map[string]string
	)
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a build job to the build service",
		Long: `Submit a build job. Performs a real SubmitBuild RPC and prints the
returned build id and queued status.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, client helixv1.BuildServiceClient) error {
				return runSubmit(ctx, client, cmd.OutOrStdout(), &helixv1.SubmitBuildRequest{
					RepoUrl:        repoURL,
					Ref:            ref,
					DockerfilePath: dockerfilePath,
					BuildArgs:      buildArgs,
				})
			})
		},
	}
	cmd.Flags().StringVar(&repoURL, "repo-url", "", "git repository URL to build (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "git ref (branch/tag/sha) to build (required)")
	cmd.Flags().StringVar(&dockerfilePath, "dockerfile", "Dockerfile", "path to the Dockerfile within the repo")
	cmd.Flags().StringToStringVar(&buildArgs, "build-arg", nil, "build args as key=value (repeatable)")
	_ = cmd.MarkFlagRequired("repo-url")
	_ = cmd.MarkFlagRequired("ref")
	return cmd
}

func newBuildStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <build-id>",
		Short: "Show the status of a build job",
		Long:  `Performs a real GetBuildStatus RPC and prints the build's state.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(ctx context.Context, client helixv1.BuildServiceClient) error {
				return runStatus(ctx, client, cmd.OutOrStdout(), args[0])
			})
		},
	}
}

func newBuildCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <build-id>",
		Short: "Cancel a build job",
		Long:  `Performs a real CancelBuild RPC and reports whether the build was cancelled.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(ctx context.Context, client helixv1.BuildServiceClient) error {
				return runCancel(ctx, client, cmd.OutOrStdout(), args[0])
			})
		},
	}
}

func newBuildLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <build-id>",
		Short: "Stream a build job's logs",
		Long: `Consumes the StreamBuildLogs server stream and writes each log line to
stdout until the stream terminates.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(ctx context.Context, client helixv1.BuildServiceClient) error {
				return runLogs(ctx, client, cmd.OutOrStdout(), args[0])
			})
		},
	}
}

// runSubmit performs the real SubmitBuild RPC and reports the returned id.
func runSubmit(ctx context.Context, client helixv1.BuildServiceClient, out io.Writer, req *helixv1.SubmitBuildRequest) error {
	resp, err := client.SubmitBuild(ctx, req)
	if err != nil {
		return fmt.Errorf("submit build: %w", err)
	}
	if resp.BuildId == "" {
		return errors.New("submit build: service returned an empty build id")
	}
	fmt.Fprintf(out, "build submitted: id=%s queued=%t\n", resp.BuildId, resp.Queued)
	return nil
}

// runStatus performs the real GetBuildStatus RPC and reports the build's state.
func runStatus(ctx context.Context, client helixv1.BuildServiceClient, out io.Writer, buildID string) error {
	st, err := client.GetBuildStatus(ctx, &helixv1.GetBuildStatusRequest{BuildId: buildID})
	if err != nil {
		return fmt.Errorf("get build status: %w", err)
	}
	fmt.Fprintf(out, "build %s: state=%s", st.BuildId, st.State)
	if st.ImageTag != "" {
		fmt.Fprintf(out, " image=%s", st.ImageTag)
	}
	if st.StartedAt != 0 {
		fmt.Fprintf(out, " started=%s", time.Unix(st.StartedAt, 0).UTC().Format(time.RFC3339))
	}
	if st.CompletedAt != 0 {
		fmt.Fprintf(out, " completed=%s", time.Unix(st.CompletedAt, 0).UTC().Format(time.RFC3339))
	}
	fmt.Fprintln(out)
	return nil
}

// runCancel performs the real CancelBuild RPC and reports the outcome.
func runCancel(ctx context.Context, client helixv1.BuildServiceClient, out io.Writer, buildID string) error {
	resp, err := client.CancelBuild(ctx, &helixv1.CancelBuildRequest{BuildId: buildID})
	if err != nil {
		return fmt.Errorf("cancel build: %w", err)
	}
	fmt.Fprintf(out, "build %s: cancelled=%t\n", buildID, resp.Cancelled)
	return nil
}

// runLogs consumes the StreamBuildLogs server stream, writing each line to out
// until the stream ends with io.EOF (terminal build state) or the context ends.
func runLogs(ctx context.Context, client helixv1.BuildServiceClient, out io.Writer, buildID string) error {
	stream, err := client.StreamBuildLogs(ctx, &helixv1.StreamBuildLogsRequest{BuildId: buildID})
	if err != nil {
		return fmt.Errorf("stream build logs: %w", err)
	}
	for {
		line, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stream build logs: %w", err)
		}
		fmt.Fprintln(out, line.Line)
	}
}
