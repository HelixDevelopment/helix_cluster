//go:build integration

package storage

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// minioEndpoint is the address of the real minio container used by the
// integration tier. The orchestrator starts minio before running tests.
const minioEndpoint = "http://127.0.0.1:9000"
const minioAccessKey = "minioadmin"
const minioSecretKey = "minioadmin"
const minioBucket = "helix-wave12"
const minioRegion = "us-east-1"

// newMinioStore builds an S3Store pointing at the local minio container using
// path-style addressing (endpoint/bucket/key — required for minio).
func newMinioStore(t *testing.T, bucket string) *S3Store {
	t.Helper()
	signer := &SigV4Signer{
		AccessKeyID:     minioAccessKey,
		SecretAccessKey: minioSecretKey,
		Region:          minioRegion,
		Service:         "s3",
	}
	store, err := NewS3Store(S3Config{
		Endpoint: minioEndpoint,
		Bucket:   bucket,
		Signer:   signer,
	})
	if err != nil {
		t.Fatalf("NewS3Store(%q): %v", bucket, err)
	}
	return store
}

// ensureBucket creates the named bucket in minio via a signed PUT-bucket
// request if it does not already exist. S3 returns 200 or 409 (BucketAlreadyExists)
// for an idempotent create; both are treated as success.
func ensureBucket(t *testing.T, bucket string) {
	t.Helper()
	signer := &SigV4Signer{
		AccessKeyID:     minioAccessKey,
		SecretAccessKey: minioSecretKey,
		Region:          minioRegion,
		Service:         "s3",
	}
	u := minioEndpoint + "/" + rfc3986Escape(bucket)
	req, err := http.NewRequest(http.MethodPut, u, nil)
	if err != nil {
		t.Fatalf("ensureBucket(%q): new request: %v", bucket, err)
	}
	req.ContentLength = 0
	if err := signer.Sign(req, emptyPayloadHash); err != nil {
		t.Fatalf("ensureBucket(%q): sign: %v", bucket, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("minio unreachable at %s — skipping S3 integration test (no container running): %v", minioEndpoint, err)
	}
	defer drain(resp)
	// 200 = created, 409 = already exists — both acceptable.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		t.Fatalf("ensureBucket(%q): unexpected status %d", bucket, resp.StatusCode)
	}
}

// checkMinioReachable pings minio. If unreachable, the test is skipped with a
// written reason as required by §CLAUDE-1.
func checkMinioReachable(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(minioEndpoint + "/minio/health/live")
	if err != nil {
		t.Skipf("minio not reachable at %s — skipping S3 integration test (orchestrator starts the container): %v",
			minioEndpoint, err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		t.Skipf("minio health-check returned %d — skipping", resp.StatusCode)
	}
}

// randToken returns a lowercase hex 16-byte random token, used as the per-run
// UUID embedded in blob content to defeat stale-cache false passes (§7.1 #4).
func randToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("rand.Read: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// sha256Digest returns the lowercase hex sha256 of data.
func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestHXC1214_S3RoundTrip_RealMinio is the §7.1 integration proof for HXC-1214.
// It drives a REAL minio container through the full Put / Get / List lifecycle:
//
//  1. Put a blob whose content embeds a per-run UUID.
//  2. Get returns byte-identical content (sha256 verified).
//  3. List(prefix) enumerates the key.
//  4. Wrong-bucket Get FAILS with ErrKeyNotFound.
//
// Mutation proof (documented; orchestrator runs):
//
//	If Put targets the wrong bucket (e.g. uses a different bucket variable),
//	the subsequent Get on the correct bucket returns ErrKeyNotFound, failing the
//	"sha256 match" assertion in step 2.
func TestHXC1214_S3RoundTrip_RealMinio(t *testing.T) {
	checkMinioReachable(t)
	ensureBucket(t, minioBucket)

	tok := randToken()

	// Per-run unique key so parallel test runs don't collide.
	key := fmt.Sprintf("wave12/hxc1214/%s/blob.bin", tok)
	content := []byte("helix-wave12-hxc1214-run-" + tok + "-payload")
	wantHash := sha256Digest(content)

	store := newMinioStore(t, minioBucket)

	// --- ACTION: Put blob ------------------------------------------------
	if err := store.Put(key, content); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	t.Logf("HXC-1214: Put key=%q content=%d bytes token=%s", key, len(content), tok)

	// --- SINK: Get returns byte-identical content (sha256 verified) ------
	got, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("Get returned different bytes: got %q, want %q", got, content)
	}
	gotHash := sha256Digest(got)
	if gotHash != wantHash {
		t.Fatalf("sha256 mismatch: got %s, want %s", gotHash, wantHash)
	}
	t.Logf("HXC-1214: Get sha256 verified: %s", gotHash)

	// --- SINK: List enumerates the key under prefix ----------------------
	prefix := "wave12/hxc1214/" + tok + "/"
	keys, err := store.List(prefix)
	if err != nil {
		t.Fatalf("List(%q): %v", prefix, err)
	}
	found := false
	for _, k := range keys {
		if k == key {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("List(%q) = %v — key %q not enumerated", prefix, keys, key)
	}
	t.Logf("HXC-1214: List(%q) returned %d key(s) including %q", prefix, len(keys), key)

	// --- SINK: Wrong-bucket Get FAILS with ErrKeyNotFound ----------------
	// The mismatch bucket might not exist; ensureBucket creates it so we get
	// a 404 for the key rather than a 404 for the bucket.
	wrongBucket := minioBucket + "-wrong"
	ensureBucket(t, wrongBucket)
	wrongStore := newMinioStore(t, wrongBucket)

	_, errWrong := wrongStore.Get(key)
	if errWrong == nil {
		t.Fatal("wrong-bucket Get must return an error, got nil")
	}
	if !errors.Is(errWrong, ErrKeyNotFound) {
		t.Fatalf("wrong-bucket Get: expected ErrKeyNotFound, got: %v", errWrong)
	}
	t.Logf("HXC-1214: wrong-bucket Get correctly failed with ErrKeyNotFound")
}
