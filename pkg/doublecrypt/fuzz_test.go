package doublecrypt_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/doublecrypt"
)

// fuzzGenPEM builds a real self-signed ECDSA cert + key PEM pair so the fuzz
// corpus contains at least one set of inputs that NewMTLSConfig must accept.
func fuzzGenPEM() (certPEM, keyPEM []byte, ok bool) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, false
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fuzz"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31, 0),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, false
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, false
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, true
}

// FuzzNewMTLSConfig feeds arbitrary bytes to the three PEM parse-from-bytes
// inputs of NewMTLSConfig (certPEM, keyPEM, caPEM). This is the real
// PEM-decode + x509.ParseCertificate + tls.X509KeyPair path. Invariant: hostile
// bytes must always produce a clean (non-nil) error with a nil config, never a
// panic and never a config built from garbage.
func FuzzNewMTLSConfig(f *testing.F) {
	f.Add([]byte{}, []byte{}, []byte{}, true)
	f.Add([]byte("-----BEGIN CERTIFICATE-----\nnotbase64\n-----END CERTIFICATE-----\n"),
		[]byte("garbage"), []byte("garbage"), false)

	// A real, valid cert/key/ca triple (self-signed cert serves as its own CA).
	if certPEM, keyPEM, ok := fuzzGenPEM(); ok {
		f.Add(certPEM, keyPEM, certPEM, true)
		f.Add(certPEM, keyPEM, certPEM, false)
	}

	f.Fuzz(func(t *testing.T, certPEM, keyPEM, caPEM []byte, isServer bool) {
		cfg, err := doublecrypt.NewMTLSConfig(certPEM, keyPEM, caPEM, isServer)
		if err != nil {
			if cfg != nil {
				t.Fatalf("NewMTLSConfig returned non-nil config alongside error")
			}
			return
		}
		// On success the config must be usable: TLS 1.3 floor, one cert loaded.
		if cfg == nil {
			t.Fatal("NewMTLSConfig returned nil config with nil error")
		}
	})
}
