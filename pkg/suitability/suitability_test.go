package suitability

import (
	"errors"
	"testing"
)

const runUUID = "8f3c2a16-7b41-4d59-9e02-suitability-hxc1529"

// TestHPCPinnedLocalNeverRemoteProxy proves clause 1: a tightly-coupled,
// interconnect-sensitive workload classifies HPC and is pinned to a local
// InfiniBand node, never the high-latency remote proxy that appears FIRST in
// the candidate list.
//
// Mutation: if Classify is mutated to return KindInference (or eligible is
// mutated to permit HPC on remote/Ethernet), Route would return the leading
// remote proxy candidate and these assertions fail.
func TestHPCPinnedLocalNeverRemoteProxy(t *testing.T) {
	w := Workload{Name: "mpi-cfd", TightlyCoupled: true, InterconnectSensitive: true}

	// Remote proxy is listed first on purpose: a naive router taking the first
	// candidate would (wrongly) send the HPC job remote.
	candidates := []Candidate{
		{NodeID: "remote-proxy-0", Locality: Remote, Interconnect: InterconnectInfiniBand},
		{NodeID: "local-eth-1", Locality: Local, Interconnect: InterconnectEthernet},
		{NodeID: "local-ib-2", Locality: Local, Interconnect: InterconnectInfiniBand},
	}

	kind := Classify(w)
	t.Logf("run=%s before: workload=%q classified=%s candidates[0]=%s/%s",
		runUUID, w.Name, kind, candidates[0].Locality, candidates[0].Interconnect)

	if kind != KindHPC {
		t.Fatalf("run=%s classify: got %s, want HPC", runUUID, kind)
	}

	got, err := Route(kind, candidates)
	if err != nil {
		t.Fatalf("run=%s route: unexpected error: %v", runUUID, err)
	}
	t.Logf("run=%s after: placement node=%s locality=%s interconnect=%s",
		runUUID, got.NodeID, got.Locality, got.Interconnect)

	if got.Locality != Local {
		t.Fatalf("run=%s HPC placed on %s locality, want Local (must never use remote proxy)", runUUID, got.Locality)
	}
	if !got.Interconnect.LowLatencyInterconnect() {
		t.Fatalf("run=%s HPC placed on %s interconnect, want low-latency", runUUID, got.Interconnect)
	}
	if got.NodeID != "local-ib-2" {
		t.Fatalf("run=%s HPC placed on %q, want local-ib-2", runUUID, got.NodeID)
	}
	if got.NodeID == "remote-proxy-0" {
		t.Fatalf("run=%s HPC routed to remote proxy: forbidden", runUUID)
	}
}

// TestInferenceAllowedRemoteHPCForbidden proves clause 2: an inference workload
// classifies Inference and is ALLOWED to route remote, while the SAME remote
// candidate set is forbidden for HPC.
//
// Mutation: if eligible permits HPC on remote, the HPC sub-check below would
// succeed and fail this test; if eligible forbids inference on remote, the
// inference sub-check fails.
func TestInferenceAllowedRemoteHPCForbidden(t *testing.T) {
	w := Workload{Name: "llm-gen", Stateless: true}

	// Only-remote candidates: proves remote is genuinely permitted (not merely
	// tolerated when a local node also happens to exist).
	remoteOnly := []Candidate{
		{NodeID: "remote-gpu-0", Locality: Remote, Interconnect: InterconnectEthernet},
	}

	kind := Classify(w)
	t.Logf("run=%s before: workload=%q classified=%s remoteOnly=%d", runUUID, w.Name, kind, len(remoteOnly))
	if kind != KindInference {
		t.Fatalf("run=%s classify: got %s, want Inference", runUUID, kind)
	}

	infPlace, err := Route(kind, remoteOnly)
	if err != nil {
		t.Fatalf("run=%s inference route: unexpected error: %v", runUUID, err)
	}
	t.Logf("run=%s after: inference placed node=%s locality=%s", runUUID, infPlace.NodeID, infPlace.Locality)
	if infPlace.Locality != Remote {
		t.Fatalf("run=%s inference placed on %s, expected the Remote candidate", runUUID, infPlace.Locality)
	}

	// Same remote-only set must be REFUSED for an HPC kind.
	_, hpcErr := Route(KindHPC, remoteOnly)
	t.Logf("run=%s after: HPC on remote-only err=%v", runUUID, hpcErr)
	if !errors.Is(hpcErr, ErrNoSuitablePlacement) {
		t.Fatalf("run=%s HPC on remote-only: got err=%v, want ErrNoSuitablePlacement (remote forbidden for HPC)", runUUID, hpcErr)
	}

	// Inference may ALSO use a local node when offered one.
	local := []Candidate{{NodeID: "local-1", Locality: Local, Interconnect: InterconnectEthernet}}
	localPlace, err := Route(KindInference, local)
	if err != nil {
		t.Fatalf("run=%s inference local route: unexpected error: %v", runUUID, err)
	}
	if localPlace.Locality != Local {
		t.Fatalf("run=%s inference local route: got %s, want Local", runUUID, localPlace.Locality)
	}
}

// TestHPCNoLocalInterconnectTypedError proves clause 3: an HPC workload with no
// local interconnect-capable node returns ErrNoSuitablePlacement and does NOT
// silently fall back to a remote proxy.
//
// Mutation: if eligible lets HPC accept the remote-IB proxy or the local
// Ethernet node, Route would succeed and this test fails. The candidate set is
// crafted so a fallback would be tempting (a remote InfiniBand proxy exists).
func TestHPCNoLocalInterconnectTypedError(t *testing.T) {
	w := Workload{Name: "mpi-weather", TightlyCoupled: true}

	candidates := []Candidate{
		{NodeID: "local-eth-only", Locality: Local, Interconnect: InterconnectEthernet},
		{NodeID: "remote-ib-proxy", Locality: Remote, Interconnect: InterconnectInfiniBand},
	}

	kind := Classify(w)
	t.Logf("run=%s before: workload=%q classified=%s (no local low-latency node present)", runUUID, w.Name, kind)
	if kind != KindHPC {
		t.Fatalf("run=%s classify: got %s, want HPC", runUUID, kind)
	}

	got, err := Route(kind, candidates)
	t.Logf("run=%s after: placement=%+v err=%v", runUUID, got, err)

	if err == nil {
		t.Fatalf("run=%s expected ErrNoSuitablePlacement, got placement node=%s (silent remote fallback forbidden)", runUUID, got.NodeID)
	}
	if !errors.Is(err, ErrNoSuitablePlacement) {
		t.Fatalf("run=%s got err=%v, want ErrNoSuitablePlacement", runUUID, err)
	}
	if got != (Candidate{}) {
		t.Fatalf("run=%s expected zero Candidate on error, got %+v", runUUID, got)
	}
}

// TestClassifyStatelessButCoupledIsHPC guards the classifier boundary: a
// stateless-but-tightly-coupled workload is HPC (coupling dominates), so
// Stateless alone cannot demote it to remote-tolerant inference.
//
// Mutation: if Classify keyed on Stateless instead of the coupling signals,
// this returns Inference and the test fails.
func TestClassifyStatelessButCoupledIsHPC(t *testing.T) {
	w := Workload{Name: "edge-case", Stateless: true, TightlyCoupled: true}
	kind := Classify(w)
	t.Logf("run=%s stateless+coupled classified=%s", runUUID, kind)
	if kind != KindHPC {
		t.Fatalf("run=%s got %s, want HPC (coupling must dominate Stateless)", runUUID, kind)
	}
}
