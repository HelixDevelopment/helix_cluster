package edgeheartbeat

// fakeSource is a controllable TelemetrySource for tests. Fields are read live
// on each interface call so a test can mutate inputs between heartbeats and
// observe the collector's view track them.
type fakeSource struct {
	pct      int
	charging bool
	temp     float64
	netType  string
}

func (f *fakeSource) Battery() (int, bool) { return f.pct, f.charging }
func (f *fakeSource) CPUTempC() float64    { return f.temp }
func (f *fakeSource) NetworkType() string  { return f.netType }

var _ TelemetrySource = (*fakeSource)(nil)
