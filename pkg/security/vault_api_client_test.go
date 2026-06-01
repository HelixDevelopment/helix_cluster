package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
)

// ---------------------------------------------------------------------------
// helpers — generate a real self-signed cert PEM for tests
// ---------------------------------------------------------------------------

func generateSelfSignedCertPEM(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.helix.local"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     []string{"test.helix.local"},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
}

// vaultResponse writes a Vault-shaped JSON response to w.
func vaultResponse(w http.ResponseWriter, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}

// ---------------------------------------------------------------------------
// NewAPIVaultClient constructor
// ---------------------------------------------------------------------------

func TestNewAPIVaultClient_ValidAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewAPIVaultClient(srv.URL, "root")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewAPIVaultClient_BadAddress(t *testing.T) {
	// An empty address string should still succeed in construction (the Vault
	// SDK validates lazily on first request); what matters is the client is
	// non-nil and usable.
	c, err := NewAPIVaultClient("http://127.0.0.1:0", "root")
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client even for unreachable address")
	}
}

// ---------------------------------------------------------------------------
// KVv2Write — assert the client issues POST to the correct path+payload
// ---------------------------------------------------------------------------

func TestAPIVaultClient_KVv2Write_Path(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad body", 400)
			return
		}
		// KVv2 Put returns version metadata; provide a minimal valid response.
		vaultResponse(w, map[string]interface{}{
			"request_id": "test",
			"data": map[string]interface{}{
				"created_time":  "2026-01-01T00:00:00Z",
				"deletion_time": "",
				"destroyed":     false,
				"version":       1,
			},
		})
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	ctx := context.Background()
	err := c.KVv2Write(ctx, "secret", "myapp/config", map[string]interface{}{"password": "s3cr3t"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// §7.1 sink-side: verify the EXACT path issued to Vault
	wantPath := "/v1/secret/data/myapp/config"
	if capturedPath != wantPath {
		t.Errorf("path: want %q, got %q", wantPath, capturedPath)
	}

	// Vault KVv2 wraps data under a "data" key
	dataField, ok := capturedBody["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected 'data' key in body, got %v", capturedBody)
	}
	if dataField["password"] != "s3cr3t" {
		t.Errorf("expected password=s3cr3t in data, got %v", dataField)
	}
}

// ---------------------------------------------------------------------------
// KVv2Read — assert correct path + data extraction
// ---------------------------------------------------------------------------

func TestAPIVaultClient_KVv2Read_PathAndData(t *testing.T) {
	var capturedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		vaultResponse(w, map[string]interface{}{
			"request_id": "test",
			"data": map[string]interface{}{
				"data": map[string]interface{}{"api_key": "abc123"},
				"metadata": map[string]interface{}{
					"created_time":  "2026-01-01T00:00:00Z",
					"deletion_time": "",
					"destroyed":     false,
					"version":       1,
				},
			},
		})
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	data, err := c.KVv2Read(context.Background(), "secret", "myapp/config")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// path assertion
	wantPath := "/v1/secret/data/myapp/config"
	if capturedPath != wantPath {
		t.Errorf("path: want %q, got %q", wantPath, capturedPath)
	}

	// sink-side: actual value must survive the round-trip
	if data["api_key"] != "abc123" {
		t.Errorf("expected api_key=abc123, got %v", data["api_key"])
	}
}

func TestAPIVaultClient_KVv2Read_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	_, err := c.KVv2Read(context.Background(), "secret", "missing")
	if err == nil {
		t.Error("expected error for 404, got nil")
	}
}

// ---------------------------------------------------------------------------
// PKIIssue — correct path+payload AND cert PEM round-trips x509.ParseCertificate
// ---------------------------------------------------------------------------

func TestAPIVaultClient_PKIIssue_PathPayloadAndCertParse(t *testing.T) {
	certPEM := generateSelfSignedCertPEM(t)

	var capturedPath string
	var capturedBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad body", 400)
			return
		}
		vaultResponse(w, map[string]interface{}{
			"request_id":     "pki-req",
			"lease_id":       "pki/issue/server/abc-lease",
			"lease_duration": 3600,
			"renewable":      true,
			"data": map[string]interface{}{
				"certificate":   certPEM,
				"private_key":   "-----BEGIN EC PRIVATE KEY-----\nMIGH...\n-----END EC PRIVATE KEY-----\n",
				"serial_number": "12:34:56",
				"ca_chain":      []interface{}{certPEM},
			},
		})
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	cert, err := c.PKIIssue(context.Background(), "pki", "server", PKIIssueParams{
		CommonName: "test.helix.local",
		TTL:        24 * time.Hour,
		AltNames:   []string{"alt.helix.local"},
		IPSANs:     []string{"10.0.0.1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// §7.1 sink: path
	wantPath := "/v1/pki/issue/server"
	if capturedPath != wantPath {
		t.Errorf("path: want %q, got %q", wantPath, capturedPath)
	}

	// common_name forwarded
	if capturedBody["common_name"] != "test.helix.local" {
		t.Errorf("common_name: want test.helix.local, got %v", capturedBody["common_name"])
	}
	// ttl forwarded (24h)
	if capturedBody["ttl"] != "24h0m0s" {
		t.Errorf("ttl: want 24h0m0s, got %v", capturedBody["ttl"])
	}
	// alt_names forwarded
	if capturedBody["alt_names"] != "alt.helix.local" {
		t.Errorf("alt_names: want alt.helix.local, got %v", capturedBody["alt_names"])
	}
	// ip_sans forwarded
	if capturedBody["ip_sans"] != "10.0.0.1" {
		t.Errorf("ip_sans: want 10.0.0.1, got %v", capturedBody["ip_sans"])
	}

	// §7.1 sink: the cert PEM must decode back through x509.ParseCertificate
	block, _ := pem.Decode([]byte(cert.Certificate))
	if block == nil {
		t.Fatal("cert.Certificate is not a valid PEM block")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate failed: %v", err)
	}
	if parsed.Subject.CommonName != "test.helix.local" {
		t.Errorf("parsed CN: want test.helix.local, got %s", parsed.Subject.CommonName)
	}

	// lease metadata
	if cert.Serial != "12:34:56" {
		t.Errorf("serial: want 12:34:56, got %q", cert.Serial)
	}
	if cert.LeaseID != "pki/issue/server/abc-lease" {
		t.Errorf("lease_id: want pki/issue/server/abc-lease, got %q", cert.LeaseID)
	}
	if cert.LeaseDuration != 3600*time.Second {
		t.Errorf("lease_duration: want 3600s, got %v", cert.LeaseDuration)
	}
	if len(cert.CAChain) != 1 {
		t.Errorf("ca_chain length: want 1, got %d", len(cert.CAChain))
	}
}

func TestAPIVaultClient_PKIIssue_MissingCertificateField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Response omits the "certificate" key — must be rejected
		vaultResponse(w, map[string]interface{}{
			"request_id": "bad",
			"data": map[string]interface{}{
				"private_key":   "key",
				"serial_number": "00",
			},
		})
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	_, err := c.PKIIssue(context.Background(), "pki", "server", PKIIssueParams{CommonName: "x"})
	if err == nil {
		t.Error("expected error when 'certificate' field absent")
	}
}

// ---------------------------------------------------------------------------
// PKIRenew — assert correct path+payload, lease fields mapped
// ---------------------------------------------------------------------------

func TestAPIVaultClient_PKIRenew_PathAndLease(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad body", 400)
			return
		}
		vaultResponse(w, map[string]interface{}{
			"request_id":     "renew-req",
			"lease_id":       "pki/issue/server/abc-lease",
			"lease_duration": 7200,
			"renewable":      true,
			"data":           nil,
		})
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	cert, err := c.PKIRenew(context.Background(), "pki", "pki/issue/server/abc-lease")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// path assertion
	wantPath := "/v1/sys/leases/renew"
	if capturedPath != wantPath {
		t.Errorf("path: want %q, got %q", wantPath, capturedPath)
	}

	// lease_id forwarded in body
	if capturedBody["lease_id"] != "pki/issue/server/abc-lease" {
		t.Errorf("lease_id in body: want pki/issue/server/abc-lease, got %v", capturedBody["lease_id"])
	}

	// response fields mapped
	if cert.LeaseID != "pki/issue/server/abc-lease" {
		t.Errorf("cert.LeaseID: want pki/issue/server/abc-lease, got %q", cert.LeaseID)
	}
	if cert.LeaseDuration != 7200*time.Second {
		t.Errorf("cert.LeaseDuration: want 7200s, got %v", cert.LeaseDuration)
	}
}

func TestAPIVaultClient_PKIRenew_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return HTTP 500 to force an error path
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"errors":["server error"]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	_, err := c.PKIRenew(context.Background(), "pki", "some-lease")
	if err == nil {
		t.Error("expected error on 500, got nil")
	}
}

// ---------------------------------------------------------------------------
// apiVaultClient implements the VaultClient interface (compile-time check)
// ---------------------------------------------------------------------------

var _ VaultClient = (*apiVaultClient)(nil)

// ---------------------------------------------------------------------------
// TLS round-trip smoke: the httptest.NewTLSServer variant works too
// ---------------------------------------------------------------------------

func TestAPIVaultClient_TLSServer_KVv2Write(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vaultResponse(w, map[string]interface{}{
			"request_id": "tls-test",
			"data": map[string]interface{}{
				"created_time":  "2026-01-01T00:00:00Z",
				"deletion_time": "",
				"destroyed":     false,
				"version":       1,
			},
		})
	}))
	defer srv.Close()

	// Build a Vault client that trusts the httptest TLS server's self-signed
	// cert by injecting a custom HTTP client into the Vault Config. This is
	// the correct extension point in hashicorp/vault/api v1.23.0 — the
	// Config.HttpClient field is the only way to override transport.
	vaultCfg := vaultapi.DefaultConfig()
	vaultCfg.Address = srv.URL
	vaultCfg.HttpClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig,
		},
	}
	raw, err := vaultapi.NewClient(vaultCfg)
	if err != nil {
		t.Fatalf("vault NewClient: %v", err)
	}
	raw.SetToken("root")
	c := &apiVaultClient{client: raw}

	err = c.KVv2Write(context.Background(), "secret", "tls/test", map[string]interface{}{"x": "1"})
	if err != nil {
		t.Fatalf("TLS KVv2Write: %v", err)
	}
}

// TestPkiCertFromSecret_NoCertificate is tested indirectly via
// TestAPIVaultClient_PKIIssue_MissingCertificateField above, which exercises
// the same code path (pkiCertFromSecret returns error when "certificate" key
// absent) without requiring a direct reference to the unexported function.

// ---------------------------------------------------------------------------
// KVv2Write round-trip: write then read must return identical payload
// ---------------------------------------------------------------------------

func TestAPIVaultClient_KVv2WriteReadRoundTrip(t *testing.T) {
	store := make(map[string]map[string]interface{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost || r.Method == http.MethodPut:
			// Write: capture data
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad", 400)
				return
			}
			dataMap, _ := body["data"].(map[string]interface{})
			store[r.URL.Path] = dataMap
			vaultResponse(w, map[string]interface{}{
				"request_id": "w",
				"data": map[string]interface{}{
					"created_time":  "2026-01-01T00:00:00Z",
					"deletion_time": "",
					"destroyed":     false,
					"version":       1,
				},
			})

		default:
			// Read: return stored data
			dataMap, ok := store[strings.Replace(r.URL.Path, "/data/", "/data/", 1)]
			if !ok || dataMap == nil {
				http.Error(w, `{"errors":[]}`, 404)
				return
			}
			vaultResponse(w, map[string]interface{}{
				"request_id": "r",
				"data": map[string]interface{}{
					"data": dataMap,
					"metadata": map[string]interface{}{
						"created_time":  "2026-01-01T00:00:00Z",
						"deletion_time": "",
						"destroyed":     false,
						"version":       1,
					},
				},
			})
		}
	}))
	defer srv.Close()

	c, _ := NewAPIVaultClient(srv.URL, "root")
	ctx := context.Background()

	writeData := map[string]interface{}{"db_password": "hunter2", "db_user": "admin"}
	if err := c.KVv2Write(ctx, "secret", "app/db", writeData); err != nil {
		t.Fatalf("write: %v", err)
	}

	readData, err := c.KVv2Read(ctx, "secret", "app/db")
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// §7.1 sink-side: both fields survive the wire round-trip
	if readData["db_password"] != "hunter2" {
		t.Errorf("db_password: want hunter2, got %v", readData["db_password"])
	}
	if readData["db_user"] != "admin" {
		t.Errorf("db_user: want admin, got %v", readData["db_user"])
	}
}
