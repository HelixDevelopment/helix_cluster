package scheduler

import (
	"testing"
	"time"
)

// AttestationGate must satisfy the Plugin interface (backward-compatible API).
var _ Plugin = (*AttestationGate)(nil)

// Attestor must be satisfiable by the in-package reference implementation.
var _ Attestor = (*HMACAttestor)(nil)

// fixedNow returns a deterministic clock for expiry assertions.
func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestAttestationName(t *testing.T) {
	p := &AttestationGate{}
	if got := p.Name(); got != "AttestationGate" {
		t.Fatalf("Name() = %q, want AttestationGate", got)
	}
	// Mutation: returning any other string -> FAIL.
}

// ---------- HMACAttestor: working stdlib reference verifies/round-trips ----------

func TestHMACAttestorRoundTripVerifies(t *testing.T) {
	att := NewHMACAttestor([]byte("k0"))
	now := time.Unix(1_000_000, 0)
	tok, err := att.Issue("node-a", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !att.Verify("node-a", tok, now) {
		t.Fatal("a freshly issued, unexpired, node-bound token must verify")
	}
	// Mutation: making Verify always return false (or Issue produce a constant
	// string) breaks this round trip -> FAIL.
}

func TestHMACAttestorRejectsForgedMAC(t *testing.T) {
	att := NewHMACAttestor([]byte("real-key"))
	now := time.Unix(1_000_000, 0)
	tok, _ := att.Issue("node-a", now.Add(time.Hour))
	// Flip the final MAC byte to forge the signature.
	forged := []byte(tok)
	forged[len(forged)-1] ^= 0x01
	if att.Verify("node-a", string(forged), now) {
		t.Fatal("a token with a tampered MAC must NOT verify")
	}
	// Mutation: replacing hmac.Equal(...) with `true`, or skipping the MAC
	// check, admits the forged token -> FAIL.
}

func TestHMACAttestorRejectsWrongKey(t *testing.T) {
	issuer := NewHMACAttestor([]byte("issuer-key"))
	verifier := NewHMACAttestor([]byte("different-key"))
	now := time.Unix(1_000_000, 0)
	tok, _ := issuer.Issue("node-a", now.Add(time.Hour))
	if verifier.Verify("node-a", tok, now) {
		t.Fatal("a token signed with a different key must NOT verify")
	}
	// Mutation: ignoring h.Key (e.g. hashing a constant) makes both attestors
	// agree and this passes the forged token -> FAIL.
}

func TestHMACAttestorRejectsWrongNode(t *testing.T) {
	att := NewHMACAttestor([]byte("k0"))
	now := time.Unix(1_000_000, 0)
	tok, _ := att.Issue("node-a", now.Add(time.Hour))
	if att.Verify("node-b", tok, now) {
		t.Fatal("a token issued for node-a must NOT verify for node-b")
	}
	// Mutation: dropping the `string(idRaw) != nodeID` binding check lets a
	// valid token be replayed on a different node -> FAIL.
}

func TestHMACAttestorRejectsExpired(t *testing.T) {
	att := NewHMACAttestor([]byte("k0"))
	issuedAt := time.Unix(1_000_000, 0)
	tok, _ := att.Issue("node-a", issuedAt.Add(time.Minute))
	// Verify one second AFTER expiry.
	after := issuedAt.Add(time.Minute + time.Second)
	if att.Verify("node-a", tok, after) {
		t.Fatal("an expired token must NOT verify")
	}
	// And it must still verify strictly before expiry.
	if !att.Verify("node-a", tok, issuedAt) {
		t.Fatal("an unexpired token must verify before its expiry")
	}
	// Mutation: dropping the `now.Before(expiry)` check (or using After/!Before
	// inverted) admits the expired token -> FAIL.
}

func TestHMACAttestorRejectsMalformed(t *testing.T) {
	att := NewHMACAttestor([]byte("k0"))
	now := time.Unix(1_000_000, 0)
	for _, bad := range []string{"", "a", "a.b", "a.b.c.d"} {
		if att.Verify("node-a", bad, now) {
			t.Fatalf("malformed token %q must NOT verify", bad)
		}
	}
	// Mutation: removing the `len(parts) != 3` structural guard panics or
	// admits garbage -> FAIL.
}

// ---------- Filter: attestation-required jobs ----------

func newGate(att Attestor, now time.Time) *AttestationGate {
	return &AttestationGate{Attestor: att, Now: fixedNow(now)}
}

func TestFilterKeepsNodeWithValidToken(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	now := time.Unix(2_000_000, 0)
	gate := newGate(att, now)

	job := &Job{ID: "secure-job"}
	SetJobRequireAttestation(job, true)

	node := &Node{ID: "tee-node"}
	tok, _ := att.Issue("tee-node", now.Add(time.Hour))
	SetNodeAttestationToken(node, tok)

	if !gate.Filter(job, node) {
		t.Fatal("attestation-required job must be ADMITTED on a node with a valid token")
	}
	// Mutation: a Filter that rejects even validly-attested nodes (always
	// returning false for required jobs) fails this admit assertion -> FAIL.
}

func TestFilterRejectsNodeWithNoToken(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	now := time.Unix(2_000_000, 0)
	gate := newGate(att, now)

	job := &Job{ID: "secure-job"}
	SetJobRequireAttestation(job, true)

	bare := &Node{ID: "untrusted-node"} // no token label

	if gate.Filter(job, bare) {
		t.Fatal("attestation-required job must be FILTERED OUT on a node with no token")
	}
	// Mutation: removing the `token == ""` reject (returning true on empty
	// token) admits an unattested node -> FAIL.
}

func TestFilterRejectsForgedToken(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	now := time.Unix(2_000_000, 0)
	gate := newGate(att, now)

	job := &Job{ID: "secure-job"}
	SetJobRequireAttestation(job, true)

	// Attacker mints a token with a DIFFERENT key (no access to the real one).
	attacker := NewHMACAttestor([]byte("attacker-key"))
	forgedTok, _ := attacker.Issue("evil-node", now.Add(time.Hour))
	node := &Node{ID: "evil-node"}
	SetNodeAttestationToken(node, forgedTok)

	if gate.Filter(job, node) {
		t.Fatal("attestation-required job must REJECT a node presenting a forged token")
	}
	// Mutation: a Filter that admits without calling Attestor.Verify (e.g.
	// `return token != ""`) lets the forged-token node through -> FAIL.
}

func TestFilterRejectsExpiredToken(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	issued := time.Unix(2_000_000, 0)
	// Gate clock is AFTER the token's expiry.
	gate := newGate(att, issued.Add(2*time.Hour))

	job := &Job{ID: "secure-job"}
	SetJobRequireAttestation(job, true)

	node := &Node{ID: "tee-node"}
	expiredTok, _ := att.Issue("tee-node", issued.Add(time.Hour))
	SetNodeAttestationToken(node, expiredTok)

	if gate.Filter(job, node) {
		t.Fatal("attestation-required job must REJECT a node presenting an expired token")
	}
	// Mutation: a Filter that does not pass a.now() (or an Attestor that skips
	// the expiry check) admits the expired token -> FAIL.
}

func TestFilterFailsClosedWithoutAttestor(t *testing.T) {
	gate := &AttestationGate{Now: fixedNow(time.Unix(2_000_000, 0))} // no Attestor

	job := &Job{ID: "secure-job"}
	SetJobRequireAttestation(job, true)
	node := &Node{ID: "n", Labels: map[string]string{LabelNodeAttestationToken: "anything"}}

	if gate.Filter(job, node) {
		t.Fatal("attestation-required job must be REJECTED when no Attestor is configured (fail closed)")
	}
	// Mutation: returning true when a.Attestor == nil silently admits every
	// node for security-critical jobs -> FAIL.
}

func TestFilterIgnoresNonAttestationJob(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	now := time.Unix(2_000_000, 0)
	gate := newGate(att, now)

	// Job does NOT require attestation.
	job := &Job{ID: "ordinary-job"}
	bare := &Node{ID: "any-node"} // no token at all

	if !gate.Filter(job, bare) {
		t.Fatal("a job that does not require attestation must be unaffected (admitted)")
	}
	// Mutation: making Filter demand a token regardless of the job's
	// requirement breaks non-attestation workloads -> FAIL.
}

func TestFilterExplicitFalseIsUnaffected(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	gate := newGate(att, time.Unix(2_000_000, 0))

	job := &Job{ID: "j"}
	SetJobRequireAttestation(job, false) // explicit false
	bare := &Node{ID: "n"}

	if !gate.Filter(job, bare) {
		t.Fatal("a job with required=false must be admitted on an unattested node")
	}
	// Mutation: treating any present 'required' label as truthy would reject
	// this node -> FAIL.
}

func TestFilterNilJobOrNodeRejected(t *testing.T) {
	gate := newGate(NewHMACAttestor([]byte("k")), time.Unix(1, 0))
	if gate.Filter(nil, &Node{ID: "n"}) {
		t.Fatal("nil job must be rejected")
	}
	if gate.Filter(&Job{ID: "j"}, nil) {
		t.Fatal("nil node must be rejected")
	}
	// Mutation: dropping the nil guards panics or admits -> FAIL.
}

func TestAttestationScoreAndBindAreNeutral(t *testing.T) {
	gate := newGate(NewHMACAttestor([]byte("k")), time.Unix(1, 0))
	job := &Job{ID: "j"}
	node := &Node{ID: "n"}
	if s := gate.Score(job, node); s != 0 {
		t.Fatalf("Score = %d, want 0 (admission plugin must not bias ranking)", s)
	}
	if !gate.Bind(job, node) {
		t.Fatal("Bind must succeed (no-op) so it does not break the bind pipeline")
	}
	// Mutation: a non-zero Score would skew ranking; a false Bind would break
	// the pipeline -> FAIL.
}

// ---------- End-to-end: scheduler only places on the attested node ----------

func TestSchedulerPlacesAttestationJobOnAttestedNodeOnly(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	now := time.Unix(3_000_000, 0)

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&AttestationGate{Attestor: att, Now: fixedNow(now)})

	// Attested node: valid token + ample resources.
	attested := &Node{
		ID:                 "attested",
		AvailableResources: Resources{CPU: 8, Memory: 16384, GPU: 2},
	}
	tok, _ := att.Issue("attested", now.Add(time.Hour))
	SetNodeAttestationToken(attested, tok)
	s.RegisterNode(attested)

	// Unattested node: NO token but MORE resources (so absent the gate it would
	// outscore the attested node via NodeResourcesFit).
	unattested := &Node{
		ID:                 "unattested",
		AvailableResources: Resources{CPU: 64, Memory: 131072, GPU: 8},
	}
	s.RegisterNode(unattested)

	job := &Job{ID: "secure", Resources: Resources{CPU: 1, Memory: 1024}}
	SetJobRequireAttestation(job, true)

	res, err := s.Schedule(job)
	if err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}
	if res.AssignedNode != "attested" {
		t.Fatalf("attestation-required job placed on %q, want the attested node", res.AssignedNode)
	}
	// Mutation: a Filter that admits unattested nodes lets the richer
	// 'unattested' node win the score -> AssignedNode == "unattested" -> FAIL.
}

func TestSchedulerRejectsAttestationJobWhenOnlyUnattestedNodes(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	now := time.Unix(3_000_000, 0)

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&AttestationGate{Attestor: att, Now: fixedNow(now)})

	// Only an unattested node exists.
	s.RegisterNode(&Node{
		ID:                 "plain",
		AvailableResources: Resources{CPU: 64, Memory: 131072, GPU: 8},
	})

	job := &Job{ID: "secure", Resources: Resources{CPU: 1, Memory: 1024}}
	SetJobRequireAttestation(job, true)

	if _, err := s.Schedule(job); err != ErrNoNodeAvailable {
		t.Fatalf("Schedule err = %v, want ErrNoNodeAvailable (no attested node)", err)
	}
	// Mutation: a Filter that admits the unattested node would place the job
	// (nil err) instead of returning ErrNoNodeAvailable -> FAIL.
}

func TestSchedulerExpiredTokenNodeIsNotPlaced(t *testing.T) {
	att := NewHMACAttestor([]byte("cluster-secret"))
	issued := time.Unix(3_000_000, 0)
	// Scheduler clock is AFTER the token expiry.
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&AttestationGate{Attestor: att, Now: fixedNow(issued.Add(2 * time.Hour))})

	node := &Node{ID: "stale", AvailableResources: Resources{CPU: 8, Memory: 16384}}
	expiredTok, _ := att.Issue("stale", issued.Add(time.Hour))
	SetNodeAttestationToken(node, expiredTok)
	s.RegisterNode(node)

	job := &Job{ID: "secure", Resources: Resources{CPU: 1, Memory: 1024}}
	SetJobRequireAttestation(job, true)

	if _, err := s.Schedule(job); err != ErrNoNodeAvailable {
		t.Fatalf("Schedule err = %v, want ErrNoNodeAvailable (token expired)", err)
	}
	// Mutation: skipping the expiry check admits the stale node and places the
	// job -> FAIL.
}
