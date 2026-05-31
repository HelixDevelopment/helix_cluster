package costbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// FetchOffers retrieves candidate offers from an HTTP endpoint that returns a
// JSON array of Offer objects. This is the real, working provider seam: in
// production it points at a price-discovery service; in tests it points at a
// local httptest server returning the same JSON shape.
//
// The endpoint must respond 200 with a JSON array; any other status or a body
// that does not decode into []Offer is reported as an error so a broker never
// silently selects from an empty/garbled offer set.
func FetchOffers(ctx context.Context, client *http.Client, url string) ([]Offer, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("costbroker: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("costbroker: fetch offers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("costbroker: offer source returned status %d", resp.StatusCode)
	}

	var offers []Offer
	if err := json.NewDecoder(resp.Body).Decode(&offers); err != nil {
		return nil, fmt.Errorf("costbroker: decode offers: %w", err)
	}
	return offers, nil
}
