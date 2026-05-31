package device

import "testing"

// MUTATION: change any RouteFor case bound (e.g. `t >= T6` -> `t >= T7`) and the
// corresponding tier maps to the wrong bucket, failing the exact-route row.
func TestRouteFor(t *testing.T) {
	cases := map[Tier]RouteClass{
		TierUnknown: RouteNone,
		T1:          RouteEdge,
		T2:          RouteEdge,
		T3:          RouteEdge,
		T4:          RouteGeneral,
		T5:          RouteGeneral,
		T6:          RouteAccelerated,
		T7:          RouteAccelerated,
		T8:          RouteAccelerated,
	}
	for tr, want := range cases {
		if got := RouteFor(tr); got != want {
			t.Errorf("RouteFor(%s) = %q, want %q", tr, got, want)
		}
	}
}

// MUTATION: make Route ignore ClassifyTier (e.g. return RouteEdge always) and
// the accelerated-server row fails.
func TestRoute_EndToEnd(t *testing.T) {
	server := canonical()[T8]
	if got := Route(server); got != RouteAccelerated {
		t.Errorf("Route(server) = %q, want %q", got, RouteAccelerated)
	}
	sbc := canonical()[T3]
	if got := Route(sbc); got != RouteEdge {
		t.Errorf("Route(sbc) = %q, want %q", got, RouteEdge)
	}
}
