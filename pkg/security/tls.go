// Package security provides TLS, SPIFFE, and Vault security primitives for Helix Cluster OS.
package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// TLSConfigBuilder generates mTLS configurations for servers and clients.
type TLSConfigBuilder struct {
	certPEM []byte
	keyPEM  []byte
	caPEM   []byte
}

// NewTLSConfigBuilder creates a builder from PEM-encoded certificate, key, and CA.
func NewTLSConfigBuilder(certPEM, keyPEM, caPEM []byte) *TLSConfigBuilder {
	return &TLSConfigBuilder{
		certPEM: certPEM,
		keyPEM:  keyPEM,
		caPEM:   caPEM,
	}
}

// ServerTLS returns a TLS config for a server that requires mutual authentication.
func (b *TLSConfigBuilder) ServerTLS() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(b.certPEM, b.keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(b.caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	return cfg, nil
}

// ClientTLS returns a TLS config for a client that presents a certificate.
func (b *TLSConfigBuilder) ClientTLS() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(b.certPEM, b.keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load client key pair: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(b.caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}
	return cfg, nil
}
