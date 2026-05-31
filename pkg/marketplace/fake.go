package marketplace

import "context"

// FakeSource is a software reference OfferSource for tests and local runs. It
// never touches a real marketplace; it returns exactly the offers (or error)
// the test injects. This is the HONEST stand-in for the out-of-scope live
// Chutes API client, which implements the same OfferSource interface.
type FakeSource struct {
	Offers []Offer
	Err    error
}

// Fetch implements OfferSource.
func (f *FakeSource) Fetch(ctx context.Context) ([]Offer, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	// Return a copy so callers cannot mutate the fake's backing slice.
	out := make([]Offer, len(f.Offers))
	copy(out, f.Offers)
	return out, nil
}
