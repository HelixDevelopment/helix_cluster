package cloudspot

import "time"

// FakeSource is a software reference PreemptionSource for tests and local runs.
// It never touches a real provider metadata endpoint; instead it emits exactly
// the notices the test injects. This is the HONEST stand-in for the
// out-of-scope live signal source.
type FakeSource struct {
	ch chan PreemptionNotice
}

// NewFakeSource returns a FakeSource with an unbuffered notice channel.
func NewFakeSource() *FakeSource {
	return &FakeSource{ch: make(chan PreemptionNotice)}
}

// Notifications implements PreemptionSource.
func (f *FakeSource) Notifications() <-chan PreemptionNotice {
	return f.ch
}

// Emit delivers a preemption notice with the given deadline. It blocks until a
// consumer (the Handler) receives it, so tests can sequence deterministically.
func (f *FakeSource) Emit(deadline time.Time) {
	f.ch <- PreemptionNotice{Deadline: deadline}
}

// EmitNotice delivers a fully-specified notice. It blocks until received.
func (f *FakeSource) EmitNotice(notice PreemptionNotice) {
	f.ch <- notice
}

// Close closes the notice channel, signalling the source is exhausted. A
// Handler blocked in Run with no notice will then return OutcomeIdle.
func (f *FakeSource) Close() {
	close(f.ch)
}
