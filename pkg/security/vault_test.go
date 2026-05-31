package security

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewVaultWrapper(t *testing.T) {
	w := NewVaultWrapper(nil)
	if w == nil {
		t.Fatal("expected non-nil wrapper")
	}
}

func TestVaultWrapper_ReadSecret(t *testing.T) {
	mock := &MockVaultClient{
		ReadFn: func(_ context.Context, mount, path string) (map[string]interface{}, error) {
			if mount != "secret" || path != "foo" {
				return nil, errors.New("not found")
			}
			return map[string]interface{}{"key": "value"}, nil
		},
	}
	w := NewVaultWrapper(mock)
	ctx := context.Background()

	data, err := w.ReadSecret(ctx, "secret", "foo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data["key"] != "value" {
		t.Errorf("expected value, got %v", data["key"])
	}

	_, err = w.ReadSecret(ctx, "secret", "bar")
	if err == nil {
		t.Error("expected error for missing path")
	}
}

func TestVaultWrapper_ReadSecret_NilClient(t *testing.T) {
	w := NewVaultWrapper(nil)
	_, err := w.ReadSecret(context.Background(), "secret", "foo")
	if err == nil {
		t.Error("expected error when client is nil")
	}
}

func TestVaultWrapper_WriteSecret(t *testing.T) {
	var written map[string]interface{}
	mock := &MockVaultClient{
		WriteFn: func(_ context.Context, mount, path string, data map[string]interface{}) error {
			if mount == "secret" && path == "foo" {
				written = data
				return nil
			}
			return errors.New("not found")
		},
	}
	w := NewVaultWrapper(mock)
	ctx := context.Background()

	if err := w.WriteSecret(ctx, "secret", "foo", map[string]interface{}{"key": "value"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if written["key"] != "value" {
		t.Errorf("expected written value, got %v", written["key"])
	}
}

func TestVaultWrapper_WriteSecret_NilClient(t *testing.T) {
	w := NewVaultWrapper(nil)
	if err := w.WriteSecret(context.Background(), "secret", "foo", nil); err == nil {
		t.Error("expected error when client is nil")
	}
}

func TestVaultWrapper_IssueCertificate(t *testing.T) {
	mock := &MockVaultClient{
		IssueFn: func(_ context.Context, mount, role string, params PKIIssueParams) (*PKICertificate, error) {
			if mount == "pki" && role == "server" {
				return &PKICertificate{
					Certificate:   "CERT",
					PrivateKey:    "KEY",
					Serial:        "1234",
					LeaseDuration: params.TTL,
				}, nil
			}
			return nil, errors.New("not allowed")
		},
	}
	w := NewVaultWrapper(mock)
	ctx := context.Background()

	cert, err := w.IssueCertificate(ctx, "pki", "server", PKIIssueParams{CommonName: "test", TTL: time.Hour})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cert.Serial != "1234" {
		t.Errorf("expected serial 1234, got %s", cert.Serial)
	}
	if cert.LeaseDuration != time.Hour {
		t.Errorf("expected lease duration 1h, got %v", cert.LeaseDuration)
	}
}

func TestVaultWrapper_IssueCertificate_NilClient(t *testing.T) {
	w := NewVaultWrapper(nil)
	_, err := w.IssueCertificate(context.Background(), "pki", "server", PKIIssueParams{})
	if err == nil {
		t.Error("expected error when client is nil")
	}
}

func TestVaultWrapper_RenewCertificate(t *testing.T) {
	mock := &MockVaultClient{
		RenewFn: func(_ context.Context, mount, leaseID string) (*PKICertificate, error) {
			if mount == "pki" && leaseID == "lease-1" {
				return &PKICertificate{Serial: "renewed"}, nil
			}
			return nil, errors.New("bad lease")
		},
	}
	w := NewVaultWrapper(mock)
	ctx := context.Background()

	cert, err := w.RenewCertificate(ctx, "pki", "lease-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cert.Serial != "renewed" {
		t.Errorf("expected serial renewed, got %s", cert.Serial)
	}
}

func TestVaultWrapper_RenewCertificate_NilClient(t *testing.T) {
	w := NewVaultWrapper(nil)
	_, err := w.RenewCertificate(context.Background(), "pki", "lease-1")
	if err == nil {
		t.Error("expected error when client is nil")
	}
}

func TestMockVaultClient_DefaultErrors(t *testing.T) {
	mock := &MockVaultClient{}
	ctx := context.Background()

	if _, err := mock.KVv2Read(ctx, "", ""); err == nil {
		t.Error("expected default read error")
	}
	if err := mock.KVv2Write(ctx, "", "", nil); err == nil {
		t.Error("expected default write error")
	}
	if _, err := mock.PKIIssue(ctx, "", "", PKIIssueParams{}); err == nil {
		t.Error("expected default issue error")
	}
	if _, err := mock.PKIRenew(ctx, "", ""); err == nil {
		t.Error("expected default renew error")
	}
}
