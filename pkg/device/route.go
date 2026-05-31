package device

// RouteClass is a coarse scheduling bucket derived from a device's tier. It lets
// the scheduler route workloads without re-deriving thresholds.
type RouteClass string

const (
	// RouteEdge covers constrained / battery devices (T1–T3): latency-local,
	// best-effort, no heavy accelerated work.
	RouteEdge RouteClass = "edge"
	// RouteGeneral covers capable non-accelerated or lightly-accelerated nodes
	// (T4–T5): general compute and inference at the edge of capability.
	RouteGeneral RouteClass = "general"
	// RouteAccelerated covers GPU/server nodes (T6–T8): training, large-model
	// inference, batch.
	RouteAccelerated RouteClass = "accelerated"
	// RouteNone is for an unclassifiable device.
	RouteNone RouteClass = "none"
)

// RouteFor maps a tier to its RouteClass deterministically.
func RouteFor(t Tier) RouteClass {
	switch {
	case t >= T6 && t <= T8:
		return RouteAccelerated
	case t >= T4 && t <= T5:
		return RouteGeneral
	case t >= T1 && t <= T3:
		return RouteEdge
	default:
		return RouteNone
	}
}

// Route classifies a descriptor and returns its RouteClass in one step.
func Route(d DeviceDescriptor) RouteClass {
	return RouteFor(ClassifyTier(d))
}
