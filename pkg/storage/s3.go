package storage

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// emptyPayloadHash is the SHA-256 of an empty byte slice, used as the
// x-amz-content-sha256 header value for requests with no body.
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// unsignedPayload is the sentinel S3 accepts in place of a body hash. We do not
// use it: we always sign the real payload so a tampered body fails server-side
// signature verification. Kept documented as an honest seam for future
// streaming uploads where buffering the whole body is undesirable.
const unsignedPayload = "UNSIGNED-PAYLOAD"

// Signer signs an outgoing S3 HTTP request in place, adding the Authorization
// header (and any required x-amz-* headers). It is a pluggable seam so callers
// can swap in alternative auth (e.g. STS session tokens, anonymous) without
// touching S3Store. payloadHash is the lowercase hex SHA-256 of the request
// body, which the signer must fold into the canonical request.
type Signer interface {
	Sign(req *http.Request, payloadHash string) error
}

// SigV4Signer implements AWS Signature Version 4 (HMAC-SHA256) over net/http.
// It is stdlib-only and deterministic given a fixed clock, which is what makes
// the pinned-vector test possible.
type SigV4Signer struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // optional; emitted as x-amz-security-token when set
	Region          string
	Service         string // "s3" for object storage

	// now returns the signing time. Nil means time.Now().UTC(). Tests pin it to
	// reproduce published AWS vectors.
	now func() time.Time
}

func (s *SigV4Signer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// Sign computes the SigV4 Authorization header for req and sets it, along with
// the x-amz-date and x-amz-content-sha256 headers. payloadHash must be the
// lowercase hex SHA-256 of the exact bytes that will be sent as the body.
func (s *SigV4Signer) Sign(req *http.Request, payloadHash string) error {
	if s.AccessKeyID == "" || s.SecretAccessKey == "" {
		return fmt.Errorf("sigv4: missing credentials")
	}
	t := s.clock().UTC()
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")

	// Host header is mandatory in the canonical request; net/http stores it on
	// req.Host rather than the header map for outgoing requests.
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.SessionToken)
	}

	canonicalRequest, signedHeaders := s.canonicalRequest(req, host, payloadHash)
	hashedCanonical := sha256Hex([]byte(canonicalRequest))

	scope := strings.Join([]string{dateStamp, s.Region, s.Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hashedCanonical,
	}, "\n")

	signingKey := s.signingKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.AccessKeyID, scope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

// canonicalRequest builds the SigV4 canonical request string and returns it
// alongside the semicolon-joined signed-header list.
func (s *SigV4Signer) canonicalRequest(req *http.Request, host, payloadHash string) (string, string) {
	canonicalURI := canonicalPath(req.URL)
	canonicalQuery := canonicalQueryString(req.URL)

	// Collect headers to sign. We always include host and any x-amz-* headers
	// present, lowercased and sorted, with trimmed single-spaced values.
	headers := map[string]string{"host": strings.TrimSpace(host)}
	for name, vals := range req.Header {
		ln := strings.ToLower(name)
		if ln == "host" || strings.HasPrefix(ln, "x-amz-") ||
			ln == "content-type" || ln == "range" {
			headers[ln] = trimAll(vals)
		}
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)

	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(headers[n])
		canonHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	return canonicalRequest, signedHeaders
}

func (s *SigV4Signer) signingKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.SecretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.Region))
	kService := hmacSHA256(kRegion, []byte(s.Service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func trimAll(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		// Collapse internal runs of whitespace to a single space per SigV4 rules.
		parts[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(parts, ",")
}

// canonicalPath returns the URI-encoded path per SigV4 for S3. S3 (unlike most
// services) does NOT double-encode the path; EscapedPath already gives the
// single-encoded form, and an empty path canonicalises to "/".
func canonicalPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

// canonicalQueryString returns the SigV4 canonical query: keys sorted, each
// key and value RFC3986-encoded, joined by '&', '=' even for empty values.
func canonicalQueryString(u *url.URL) string {
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, rfc3986Escape(k)+"="+rfc3986Escape(v))
		}
	}
	return strings.Join(parts, "&")
}

// rfc3986Escape escapes per the AWS SigV4 spec: unreserved characters
// (A-Z a-z 0-9 - _ . ~) are left as-is, everything else is %XX uppercase.
func rfc3986Escape(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}
	return b.String()
}

// S3Store is an S3-compatible object-storage Store implementation. It speaks
// the S3 REST API directly over net/http (no aws-sdk) and signs every request
// with the configured Signer (SigV4 by default). Objects are addressed as
// {endpoint}/{bucket}/{key}; keys are used verbatim as object paths.
type S3Store struct {
	endpoint string // e.g. "https://s3.us-east-1.amazonaws.com" or a test server URL
	bucket   string
	client   *http.Client
	signer   Signer
}

// S3Config configures an S3Store.
type S3Config struct {
	// Endpoint is the S3 service base URL (scheme+host[+path]), WITHOUT the
	// bucket. Required.
	Endpoint string
	// Bucket is the target bucket name. Required.
	Bucket string
	// Signer signs each request. Required (use a SigV4Signer for real S3).
	Signer Signer
	// Client is the HTTP client to use. Nil means http.DefaultClient.
	Client *http.Client
}

// NewS3Store constructs an S3Store. It validates required fields so a
// misconfigured store fails loudly at construction rather than on first call.
func NewS3Store(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("s3: endpoint required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket required")
	}
	if cfg.Signer == nil {
		return nil, fmt.Errorf("s3: signer required")
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &S3Store{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		bucket:   cfg.Bucket,
		client:   client,
		signer:   cfg.Signer,
	}, nil
}

// objectURL builds the full object URL for a key.
func (s *S3Store) objectURL(key string) string {
	// Each path segment is RFC3986-escaped but '/' separators are preserved so
	// nested keys map to nested object paths (and the signed canonical path
	// matches what the server routes on).
	segs := strings.Split(key, "/")
	for i, seg := range segs {
		segs[i] = rfc3986Escape(seg)
	}
	return s.endpoint + "/" + rfc3986Escape(s.bucket) + "/" + strings.Join(segs, "/")
}

// Put uploads data to {bucket}/{key}.
func (s *S3Store) Put(key string, data []byte) error {
	if key == "" {
		return fmt.Errorf("key cannot be empty: %w", ErrEmptyKey)
	}
	req, err := http.NewRequest(http.MethodPut, s.objectURL(key), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("s3 put: new request: %w", err)
	}
	req.ContentLength = int64(len(data))
	if err := s.signer.Sign(req, sha256Hex(data)); err != nil {
		return fmt.Errorf("s3 put: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put: do: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("s3 put %q: unexpected status %d", key, resp.StatusCode)
	}
	return nil
}

// Get downloads {bucket}/{key}. A 404 maps to ErrKeyNotFound.
func (s *S3Store) Get(key string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, s.objectURL(key), nil)
	if err != nil {
		return nil, fmt.Errorf("s3 get: new request: %w", err)
	}
	if err := s.signer.Sign(req, emptyPayloadHash); err != nil {
		return nil, fmt.Errorf("s3 get: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 get: do: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("key not found: %s: %w", key, ErrKeyNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("s3 get %q: unexpected status %d", key, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("s3 get %q: read body: %w", key, err)
	}
	return body, nil
}

// Delete removes {bucket}/{key}. S3 returns 204 whether or not the object
// existed, which matches the idempotent-delete contract of the other stores.
func (s *S3Store) Delete(key string) error {
	req, err := http.NewRequest(http.MethodDelete, s.objectURL(key), nil)
	if err != nil {
		return fmt.Errorf("s3 delete: new request: %w", err)
	}
	if err := s.signer.Sign(req, emptyPayloadHash); err != nil {
		return fmt.Errorf("s3 delete: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 delete: do: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("s3 delete %q: unexpected status %d", key, resp.StatusCode)
	}
	return nil
}

// Exists reports whether {bucket}/{key} exists, via a HEAD request. This is an
// additive convenience beyond the Store interface.
func (s *S3Store) Exists(key string) (bool, error) {
	req, err := http.NewRequest(http.MethodHead, s.objectURL(key), nil)
	if err != nil {
		return false, fmt.Errorf("s3 head: new request: %w", err)
	}
	if err := s.signer.Sign(req, emptyPayloadHash); err != nil {
		return false, fmt.Errorf("s3 head: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("s3 head: do: %w", err)
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("s3 head %q: unexpected status %d", key, resp.StatusCode)
	}
}

// listBucketResult models the subset of the S3 ListObjectsV2 XML response we
// consume.
type listBucketResult struct {
	XMLName     xml.Name `xml:"ListBucketResult"`
	IsTruncated bool     `xml:"IsTruncated"`
	NextToken   string   `xml:"NextContinuationToken"`
	Contents    []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

// List returns all object keys under prefix, sorted, following pagination
// (continuation tokens) until the listing is exhausted.
func (s *S3Store) List(prefix string) ([]string, error) {
	var out []string
	token := ""
	for {
		page, next, err := s.listPage(prefix, token)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			break
		}
		token = next
	}
	sort.Strings(out)
	return out, nil
}

func (s *S3Store) listPage(prefix, token string) ([]string, string, error) {
	u, err := url.Parse(s.endpoint + "/" + rfc3986Escape(s.bucket))
	if err != nil {
		return nil, "", fmt.Errorf("s3 list: parse endpoint: %w", err)
	}
	q := url.Values{}
	q.Set("list-type", "2")
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if token != "" {
		q.Set("continuation-token", token)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("s3 list: new request: %w", err)
	}
	if err := s.signer.Sign(req, emptyPayloadHash); err != nil {
		return nil, "", fmt.Errorf("s3 list: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("s3 list: do: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("s3 list: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("s3 list: read body: %w", err)
	}
	var result listBucketResult
	if err := xml.Unmarshal(body, &result); err != nil {
		return nil, "", fmt.Errorf("s3 list: unmarshal: %w", err)
	}
	keys := make([]string, 0, len(result.Contents))
	for _, c := range result.Contents {
		keys = append(keys, c.Key)
	}
	next := ""
	if result.IsTruncated {
		next = result.NextToken
	}
	return keys, next, nil
}

// drain reads and closes the response body so the connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// Compile-time assertion that S3Store satisfies the Store interface so it drops
// into any registry keyed on Store.
var _ Store = (*S3Store)(nil)
