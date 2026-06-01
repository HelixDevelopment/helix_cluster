// Package security — spiffe_ca.go provides an in-process SPIFFE Certificate
// Authority for Helix Cluster OS. It mints X.509 SVIDs for SPIFFE-identified
// workloads without requiring an external SPIRE server.
//
// Design:
//   - A single self-signed CA certificate is generated at bootstrap with the
//     CA flag set and keyCertSign usage, rooted to spiffe://helix.local.
//   - WorkloadSVID mints a leaf SVID signed by the CA for a caller-supplied
//     SPIFFE ID. The leaf has DigitalSignature key usage, clientAuth +
//     serverAuth extended key usage, and exactly one URI SAN carrying the
//     SPIFFE ID — satisfying x509svid.Parse validation requirements.
//   - The trust bundle (x509bundle.Bundle) is exported so callers can wire
//     it into tlsconfig.MTLSServerConfig / MTLSClientConfig authorizers.
//
// No external SPIRE server, no workloadapi — fully hermetic and cross-platform.
package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

// HelixCA is an in-process Certificate Authority that issues X.509 SVIDs for
// the spiffe://helix.local trust domain.
type HelixCA struct {
	trustDomain spiffeid.TrustDomain
	caCert      *x509.Certificate
	caKey       *ecdsa.PrivateKey
	bundle      *x509bundle.Bundle
}

// NewHelixCA creates a new in-process CA for spiffe://helix.local.
// It generates a fresh ECDSA P-256 CA key and self-signed CA certificate.
func NewHelixCA() (*HelixCA, error) {
	td, err := spiffeid.TrustDomainFromString("helix.local")
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: parse trust domain: %w", err)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: generate CA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: generate serial: %w", err)
	}

	// The CA URI SAN is the SPIFFE ID for the trust domain root.
	caURI, _ := url.Parse("spiffe://helix.local")

	caTmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Helix Cluster CA"},
		URIs:         []*url.URL{caURI},
		NotBefore:    time.Now().Add(-time.Second),
		NotAfter:     time.Now().Add(24 * time.Hour),
		// CA constraints.
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: create CA cert: %w", err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: parse CA cert: %w", err)
	}

	bundle := x509bundle.FromX509Authorities(td, []*x509.Certificate{caCert})

	return &HelixCA{
		trustDomain: td,
		caCert:      caCert,
		caKey:       caKey,
		bundle:      bundle,
	}, nil
}

// TrustDomain returns the trust domain for which this CA issues SVIDs.
func (ca *HelixCA) TrustDomain() spiffeid.TrustDomain {
	return ca.trustDomain
}

// Bundle returns the x509bundle.Bundle containing the CA certificate. Wire
// this into tlsconfig.MTLSServerConfig / MTLSClientConfig as the bundle source.
func (ca *HelixCA) Bundle() *x509bundle.Bundle {
	return ca.bundle
}

// BundleSet returns an x509bundle.Set containing the bundle, convenient for
// tlsconfig functions that accept a Source.
func (ca *HelixCA) BundleSet() *x509bundle.Set {
	return x509bundle.NewSet(ca.bundle)
}

// MintSVID issues a new X.509 SVID for the given SPIFFE ID string, e.g.
// "spiffe://helix.local/svc/gateway". The returned *x509svid.SVID satisfies
// x509svid.Source (implements GetX509SVID) and can be used directly with
// tlsconfig.MTLSServerConfig / MTLSClientConfig.
//
// The leaf certificate:
//   - carries exactly one URI SAN equal to the SPIFFE ID
//   - has IsCA=false, KeyUsageDigitalSignature (no CertSign / CRLSign)
//   - has ExtKeyUsageServerAuth + ExtKeyUsageClientAuth
//   - is signed by the HelixCA
func (ca *HelixCA) MintSVID(spiffeIDStr string) (*x509svid.SVID, error) {
	id, err := spiffeid.FromString(spiffeIDStr)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: parse SPIFFE ID %q: %w", spiffeIDStr, err)
	}
	if id.TrustDomain() != ca.trustDomain {
		return nil, fmt.Errorf("spiffe_ca: SPIFFE ID %q is not in trust domain %q",
			spiffeIDStr, ca.trustDomain)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: generate leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: generate serial: %w", err)
	}

	spiffeURI := id.URL()

	leafTmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: id.Path()},
		URIs:         []*url.URL{spiffeURI},
		NotBefore:    time.Now().Add(-time.Second),
		NotAfter:     time.Now().Add(1 * time.Hour),
		// Leaf SVID constraints (per SPIFFE spec + x509svid.validateLeafCertificate):
		IsCA:                  false,
		BasicConstraintsValid: true,
		// DigitalSignature required; CertSign + CRLSign explicitly forbidden.
		KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca.caCert, &leafKey.PublicKey, ca.caKey)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: create leaf cert: %w", err)
	}

	// Build PEM blocks for x509svid.Parse.
	// The key must be PKCS#8 for x509svid.Parse (pemutil.ParsePrivateKey expects PKCS#8).
	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	// Include CA cert in chain so the bundle verification can chain leaf -> CA.
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.caCert.Raw})
	fullChainPEM := append(leafCertPEM, caCertPEM...)

	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: marshal leaf key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	svid, err := x509svid.Parse(fullChainPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: parse minted SVID: %w", err)
	}

	return svid, nil
}

// MintSVIDLeafOnly returns an SVID whose PEM chain contains only the leaf
// certificate (no CA cert). Used in integration tests to verify that a client
// presenting an SVID from an untrusted chain is rejected.
func (ca *HelixCA) MintSVIDLeafOnly(spiffeIDStr string) (*x509svid.SVID, error) {
	id, err := spiffeid.FromString(spiffeIDStr)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: parse SPIFFE ID %q: %w", spiffeIDStr, err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: generate leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: generate serial: %w", err)
	}

	spiffeURI := id.URL()

	leafTmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: id.Path()},
		URIs:                  []*url.URL{spiffeURI},
		NotBefore:             time.Now().Add(-time.Second),
		NotAfter:              time.Now().Add(1 * time.Hour),
		IsCA:                  false,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca.caCert, &leafKey.PublicKey, ca.caKey)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: create leaf cert: %w", err)
	}

	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})

	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: marshal leaf key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	svid, err := x509svid.Parse(leafCertPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("spiffe_ca: parse minted SVID leaf-only: %w", err)
	}

	return svid, nil
}
