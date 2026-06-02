// Package spiffefed implements SPIFFE federation trust-bundle exchange across
// Helix Cluster cells. A trust bundle is a set of trusted root CA certificates
// for a named trust domain. Verification asserts both a real x509 chain and that
// the leaf certificate carries a spiffe:// URI SAN whose host equals the requested
// trust domain.
//
// Only stdlib is used (crypto/x509, net/url, sync, errors, sort).
package spiffefed

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"sync"
)

// ErrUnknownTrustDomain is returned by Verify when the requested trust domain
// has no bundle registered in this FederationStore.
var ErrUnknownTrustDomain = errors.New("spiffefed: unknown trust domain")

// bundle holds the trust material for one domain.
type bundle struct {
	pool  *x509.CertPool
	roots []*x509.Certificate // kept for Exchange copying
}

// FederationStore is a concurrency-safe registry that maps trust domain names
// (e.g. "a.example") to their respective sets of root CA certificates. It
// supports adding bundles, verifying leaf SVIDs, and merging bundles from a
// remote store (federation exchange).
type FederationStore struct {
	mu      sync.RWMutex
	bundles map[string]*bundle
}

// NewFederationStore returns an empty, ready-to-use FederationStore.
func NewFederationStore() *FederationStore {
	return &FederationStore{
		bundles: make(map[string]*bundle),
	}
}

// AddBundle registers (or replaces) the trust bundle for trustDomain. roots
// must be non-empty; each certificate is added to a new x509.CertPool. An
// error is returned if trustDomain is empty or roots is empty.
func (s *FederationStore) AddBundle(trustDomain string, roots []*x509.Certificate) error {
	if trustDomain == "" {
		return errors.New("spiffefed: trustDomain must not be empty")
	}
	if len(roots) == 0 {
		return errors.New("spiffefed: roots must not be empty")
	}

	pool := x509.NewCertPool()
	for _, r := range roots {
		pool.AddCert(r)
	}

	// Shallow copy the slice so the caller cannot mutate our stored copy.
	copied := make([]*x509.Certificate, len(roots))
	copy(copied, roots)

	s.mu.Lock()
	s.bundles[trustDomain] = &bundle{pool: pool, roots: copied}
	s.mu.Unlock()
	return nil
}

// TrustDomains returns a sorted slice of all registered trust domain names.
func (s *FederationStore) TrustDomains() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(s.bundles))
	for td := range s.bundles {
		out = append(out, td)
	}
	sort.Strings(out)
	return out
}

// Verify checks two things:
//  1. The leaf certificate (and optional intermediates) form a valid x509 chain
//     rooted at one of the certificates registered for trustDomain.
//  2. The leaf carries at least one spiffe:// URI SAN whose host equals
//     trustDomain (i.e. the SPIFFE ID's trust-domain component matches).
//
// Returns ErrUnknownTrustDomain if no bundle is registered for trustDomain.
// Returns a descriptive wrapped error for chain verification failures or missing
// / mismatched SPIFFE URI SANs.
func (s *FederationStore) Verify(trustDomain string, leaf *x509.Certificate, intermediates []*x509.Certificate) error {
	s.mu.RLock()
	b, ok := s.bundles[trustDomain]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownTrustDomain, trustDomain)
	}

	// Build the intermediates pool.
	interPool := x509.NewCertPool()
	for _, ic := range intermediates {
		interPool.AddCert(ic)
	}

	opts := x509.VerifyOptions{
		Roots:         b.pool,
		Intermediates: interPool,
		// KeyUsages: allow any; SPIFFE SVIDs may not set ExtKeyUsageServerAuth.
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("spiffefed: x509 chain verification failed for trust domain %q: %w", trustDomain, err)
	}

	// Assert a SPIFFE URI SAN whose host == trustDomain.
	if err := assertSpiffeURI(leaf, trustDomain); err != nil {
		return err
	}

	return nil
}

// assertSpiffeURI returns a non-nil error unless leaf has at least one URI SAN
// of the form spiffe://<trustDomain>/... .
func assertSpiffeURI(leaf *x509.Certificate, trustDomain string) error {
	for _, u := range leaf.URIs {
		if u.Scheme == "spiffe" && u.Host == trustDomain {
			return nil
		}
	}
	// Build a human-readable list of what we actually found.
	found := make([]string, 0, len(leaf.URIs))
	for _, u := range leaf.URIs {
		found = append(found, u.String())
	}
	if len(found) == 0 {
		return fmt.Errorf("spiffefed: leaf certificate has no URI SANs; expected spiffe://%s/...", trustDomain)
	}
	return fmt.Errorf("spiffefed: leaf certificate has no spiffe://%s/... URI SAN; found %v", trustDomain, found)
}

// Exchange merges all bundles from remote into this store. Existing bundles for
// a trust domain already known to this store are left unchanged (they are not
// replaced). Returns the number of new trust domains merged.
func (s *FederationStore) Exchange(remote *FederationStore) int {
	// Collect remote bundles under remote's read lock.
	remote.mu.RLock()
	snapshot := make(map[string]*bundle, len(remote.bundles))
	for td, b := range remote.bundles {
		snapshot[td] = b
	}
	remote.mu.RUnlock()

	// Merge under local write lock, skipping domains already present.
	s.mu.Lock()
	defer s.mu.Unlock()

	merged := 0
	for td, b := range snapshot {
		if _, exists := s.bundles[td]; exists {
			continue
		}
		// Build a fresh pool from the retained root slice so each store has its
		// own pool object (avoids sharing mutable state).
		newPool := x509.NewCertPool()
		for _, r := range b.roots {
			newPool.AddCert(r)
		}
		copied := make([]*x509.Certificate, len(b.roots))
		copy(copied, b.roots)
		s.bundles[td] = &bundle{pool: newPool, roots: copied}
		merged++
	}
	return merged
}

// SpiffeID constructs the canonical SPIFFE URI for a given trust domain and
// workload path, returning the parsed *url.URL. The path must begin with "/".
// This is a convenience helper for tests and callers that need to construct
// SPIFFE IDs without an external library.
func SpiffeID(trustDomain, path string) (*url.URL, error) {
	if trustDomain == "" {
		return nil, errors.New("spiffefed: trustDomain must not be empty")
	}
	if len(path) == 0 || path[0] != '/' {
		return nil, fmt.Errorf("spiffefed: path must start with '/'; got %q", path)
	}
	return &url.URL{Scheme: "spiffe", Host: trustDomain, Path: path}, nil
}
