package storage

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is a minimal in-memory S3 emulator served over httptest. It honors
// PUT/GET/DELETE/HEAD on /{bucket}/{key} and ListObjectsV2 (list-type=2) with
// prefix + continuation-token pagination, returning 404 for missing objects.
// It also records the Authorization header of the last request so tests can
// assert real SigV4 signing reached the wire.
type fakeS3 struct {
	mu       sync.Mutex
	bucket   string
	objects  map[string][]byte
	lastAuth string
	pageSize int // 0 == no pagination
}

func newFakeS3(bucket string) *fakeS3 {
	return &fakeS3{bucket: bucket, objects: map[string][]byte{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.lastAuth = r.Header.Get("Authorization")
	f.mu.Unlock()

	// Reject anything that did not carry a SigV4 Authorization header so a store
	// that forgets to sign fails loudly instead of silently "working".
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		http.Error(w, "missing sigv4 auth", http.StatusForbidden)
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] != f.bucket {
		http.Error(w, "no such bucket", http.StatusNotFound)
		return
	}

	// Bucket-level GET with list-type=2 is a listing request.
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		f.handleList(w, r)
		return
	}

	if len(parts) < 2 || parts[1] == "" {
		http.Error(w, "no key", http.StatusBadRequest)
		return
	}
	// Object paths are escaped on the wire; decode back to the logical key.
	key, err := url.PathUnescape(parts[1])
	if err != nil {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		f.mu.Lock()
		data, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(data)
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) handleList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	cont := r.URL.Query().Get("continuation-token")

	f.mu.Lock()
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	f.mu.Unlock()
	sort.Strings(keys)

	// Pagination: keys after the continuation token, capped at pageSize.
	start := 0
	if cont != "" {
		for i, k := range keys {
			if k == cont {
				start = i
				break
			}
		}
	}
	page := keys[start:]
	truncated := false
	next := ""
	if f.pageSize > 0 && len(page) > f.pageSize {
		truncated = true
		next = page[f.pageSize] // token is the first key of the NEXT page
		page = page[:f.pageSize]
	}

	type contents struct {
		Key string `xml:"Key"`
	}
	type result struct {
		XMLName     xml.Name   `xml:"ListBucketResult"`
		IsTruncated bool       `xml:"IsTruncated"`
		NextToken   string     `xml:"NextContinuationToken"`
		Contents    []contents `xml:"Contents"`
	}
	res := result{IsTruncated: truncated, NextToken: next}
	for _, k := range page {
		res.Contents = append(res.Contents, contents{Key: k})
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

// newTestS3Store wires an S3Store to a fakeS3 over a real loopback HTTP server.
func newTestS3Store(t *testing.T, fake *fakeS3) *S3Store {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	signer := &SigV4Signer{
		AccessKeyID:     "AKIAEXAMPLE",
		SecretAccessKey: "secretexamplekey",
		Region:          "us-east-1",
		Service:         "s3",
	}
	store, err := NewS3Store(S3Config{
		Endpoint: srv.URL,
		Bucket:   fake.bucket,
		Signer:   signer,
		Client:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("new s3 store: %v", err)
	}
	return store
}

// TestS3SigV4PinnedVector reproduces the published AWS "GET Object with Range"
// SigV4 example end-to-end and asserts the exact Authorization header. This is
// the cryptographic anchor: any drift in canonical-request construction,
// string-to-sign, signing-key derivation, or HMAC chaining changes the hex
// signature and fails this test.
//
// Mutation that kills it: change canonicalPath to return "" instead of "/" for
// the root, or drop x-amz-content-sha256 from the signed headers, or reorder
// the string-to-sign elements — any yields a different signature than the
// AWS-published f0e8bdb8...db41.
func TestS3SigV4PinnedVector(t *testing.T) {
	signer := &SigV4Signer{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:          "us-east-1",
		Service:         "s3",
		now: func() time.Time {
			return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
		},
	}
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// The AWS example signs a Range header alongside the defaults.
	req.Header.Set("Range", "bytes=0-9")

	if err := signer.Sign(req, emptyPayloadHash); err != nil {
		t.Fatalf("sign: %v", err)
	}

	const wantSig = "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	const wantAuth = "AWS4-HMAC-SHA256 " +
		"Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=" + wantSig

	got := req.Header.Get("Authorization")
	if got != wantAuth {
		t.Fatalf("Authorization mismatch:\n got=%q\nwant=%q", got, wantAuth)
	}
	if !strings.Contains(got, wantSig) {
		t.Fatalf("signature %q not present in %q", wantSig, got)
	}
	// Sanity: the signer must also have set x-amz-date to the pinned instant.
	if d := req.Header.Get("X-Amz-Date"); d != "20130524T000000Z" {
		t.Errorf("x-amz-date = %q, want 20130524T000000Z", d)
	}
}

// TestS3PutThenGetExactBytes proves a round trip returns the SERVER's stored
// bytes verbatim, including a NUL and high bytes that a naive string path would
// mangle.
//
// Mutation that kills it: make Get return a cached copy of the just-Put data
// (or `return nil, nil`) instead of reading resp.Body — the emulator is the
// only source of truth here, so any non-server bytes fail bytes.Equal.
func TestS3PutThenGetExactBytes(t *testing.T) {
	fake := newFakeS3("mybucket")
	store := newTestS3Store(t, fake)

	want := []byte{0x00, 'h', 'e', 'l', 'l', 'o', 0xff, 0xfe, '\n'}
	if err := store.Put("dir/obj.bin", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Prove the bytes actually landed in the emulator (sink-side evidence).
	fake.mu.Lock()
	stored, ok := fake.objects["dir/obj.bin"]
	fake.mu.Unlock()
	if !ok {
		t.Fatal("object never reached the S3 emulator")
	}
	if !bytes.Equal(stored, want) {
		t.Fatalf("emulator stored %v, want %v", stored, want)
	}

	got, err := store.Get("dir/obj.bin")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("get returned %v, want %v", got, want)
	}
}

// TestS3GetMissingIsNotFound proves a 404 from the server maps to ErrKeyNotFound.
//
// Mutation that kills it: treat 404 as success (drop the StatusNotFound branch)
// — errors.Is would then fail.
func TestS3GetMissingIsNotFound(t *testing.T) {
	store := newTestS3Store(t, newFakeS3("mybucket"))
	_, err := store.Get("does/not/exist")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

// TestS3DeleteThenGetNotFound proves Delete removes the object server-side.
//
// Mutation that kills it: make Delete a no-op (return nil without issuing the
// request) — the subsequent Get would still find the object.
func TestS3DeleteThenGetNotFound(t *testing.T) {
	fake := newFakeS3("mybucket")
	store := newTestS3Store(t, fake)

	if err := store.Put("k", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Delete("k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	fake.mu.Lock()
	_, stillThere := fake.objects["k"]
	fake.mu.Unlock()
	if stillThere {
		t.Fatal("delete did not remove the object from the emulator")
	}
	if _, err := store.Get("k"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

// TestS3ExistsHEAD proves HEAD-based existence checks reflect server state.
//
// Mutation that kills it: have Exists always return true — the missing-key
// assertion fails.
func TestS3ExistsHEAD(t *testing.T) {
	store := newTestS3Store(t, newFakeS3("mybucket"))
	if ok, err := store.Exists("absent"); err != nil || ok {
		t.Fatalf("Exists(absent) = %v,%v; want false,nil", ok, err)
	}
	if err := store.Put("present", []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if ok, err := store.Exists("present"); err != nil || !ok {
		t.Fatalf("Exists(present) = %v,%v; want true,nil", ok, err)
	}
}

// TestS3ListPrefix proves List returns EXACTLY the keys under the prefix,
// sorted, and nothing outside it.
//
// Mutation that kills it: ignore the prefix server-side (return all keys) — the
// exact-set assertion below fails because "other/z" would leak in.
func TestS3ListPrefix(t *testing.T) {
	fake := newFakeS3("mybucket")
	store := newTestS3Store(t, fake)

	for _, k := range []string{"a/1", "a/2", "a/3", "other/z"} {
		if err := store.Put(k, []byte("v")); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	keys, err := store.List("a/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"a/1", "a/2", "a/3"}
	if len(keys) != len(want) {
		t.Fatalf("List(a/) = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("List(a/)[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

// TestS3ListPaginates proves List follows continuation tokens and accumulates
// every page, not just the first.
//
// Mutation that kills it: stop the loop after the first page (break
// unconditionally) — only pageSize keys would return instead of all 5.
func TestS3ListPaginates(t *testing.T) {
	fake := newFakeS3("mybucket")
	fake.pageSize = 2
	store := newTestS3Store(t, fake)

	for _, k := range []string{"p/1", "p/2", "p/3", "p/4", "p/5"} {
		if err := store.Put(k, []byte("v")); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	keys, err := store.List("p/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"p/1", "p/2", "p/3", "p/4", "p/5"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("paginated List = %v, want %v", keys, want)
	}
}

// TestS3StoreSatisfiesStore proves S3Store drops into the Store interface (the
// registry's contract) and behaves through the interface, not just the concrete
// type.
//
// Mutation that kills it: change a method signature so *S3Store no longer
// implements Store — this stops compiling.
func TestS3StoreSatisfiesStore(t *testing.T) {
	fake := newFakeS3("mybucket")
	var s Store = newTestS3Store(t, fake)
	if err := s.Put("via/iface", []byte("data")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get("via/iface")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, []byte("data")) {
		t.Fatalf("iface get = %q, want data", got)
	}
}

// TestS3EmptyKeyRejected proves the empty-key guard matches the other stores.
//
// Mutation that kills it: drop the key=="" check in Put — it would attempt a
// request instead of returning ErrEmptyKey.
func TestS3EmptyKeyRejected(t *testing.T) {
	store := newTestS3Store(t, newFakeS3("mybucket"))
	if err := store.Put("", []byte("x")); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("expected ErrEmptyKey, got %v", err)
	}
}

// TestS3SignerReachesWire proves every Put actually carries a SigV4
// Authorization header to the server (the emulator rejects unsigned requests
// with 403, which would surface as a Put error).
//
// Mutation that kills it: make S3Store skip s.signer.Sign — the emulator's 403
// turns the Put into an error.
func TestS3SignerReachesWire(t *testing.T) {
	fake := newFakeS3("mybucket")
	store := newTestS3Store(t, fake)
	if err := store.Put("signed", []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	fake.mu.Lock()
	auth := fake.lastAuth
	fake.mu.Unlock()
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		t.Fatalf("server saw Authorization %q, want a SigV4 header", auth)
	}
}

// TestNewS3StoreValidation proves construction fails loudly on missing config.
//
// Mutation that kills it: drop any of the required-field checks in NewS3Store —
// the corresponding sub-case would no longer return an error.
func TestNewS3StoreValidation(t *testing.T) {
	signer := &SigV4Signer{AccessKeyID: "x", SecretAccessKey: "y", Region: "us-east-1", Service: "s3"}
	cases := map[string]S3Config{
		"no endpoint": {Bucket: "b", Signer: signer},
		"no bucket":   {Endpoint: "http://x", Signer: signer},
		"no signer":   {Endpoint: "http://x", Bucket: "b"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewS3Store(cfg); err == nil {
				t.Fatalf("expected error for %s, got nil", name)
			}
		})
	}
}
