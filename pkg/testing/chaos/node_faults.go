package chaos

// NodeRestart models a VM restart with a recovery window. Unlike NodeCrash (which
// represents a hard crash that stays down until manually restored), NodeRestart
// tracks an explicit recovering state: Apply marks the node as down and in
// recovery; Restore brings it fully back online (recovering=false). Callers can
// poll IsRecovering to observe the intermediate window.
type NodeRestart struct {
	NodeID     string
	st         applyState
	recovering bool
}

// Name returns the fault name.
func (f *NodeRestart) Name() string { return "node-restart" }

// Apply marks the node as down and entering its recovery window.
// Returns ErrAlreadyApplied if already applied.
func (f *NodeRestart) Apply() error {
	if err := f.st.apply(f.Name()); err != nil {
		return err
	}
	f.recovering = true
	return nil
}

// Restore brings the node fully back online (clears the recovery window).
// Returns ErrNotApplied if not currently applied.
func (f *NodeRestart) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.recovering = false
	return nil
}

// IsDown reports whether the node is currently unavailable (down and/or recovering).
func (f *NodeRestart) IsDown() bool { return f.st.isApplied() }

// IsRecovering reports whether the node is in the post-restart recovery window.
// It is true between Apply and Restore, and false before Apply or after Restore.
func (f *NodeRestart) IsRecovering() bool { return f.recovering }

// ---------------------------------------------------------------------------

// DiskSpaceExhaustion models capacity exhaustion: the node stays online but all
// write operations are blocked once capacity is exhausted. This is distinct from
// SlowDisk (latency injection) — here writes are outright blocked, not slowed.
type DiskSpaceExhaustion struct {
	NodeID  string
	st      applyState
	blocked bool
}

// Name returns the fault name.
func (f *DiskSpaceExhaustion) Name() string { return "disk-space-exhaustion" }

// Apply marks writes as blocked (capacity exhausted).
// Returns ErrAlreadyApplied if already applied.
func (f *DiskSpaceExhaustion) Apply() error {
	if err := f.st.apply(f.Name()); err != nil {
		return err
	}
	f.blocked = true
	return nil
}

// Restore clears the write-blocked state (capacity restored).
// Returns ErrNotApplied if not currently applied.
func (f *DiskSpaceExhaustion) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.blocked = false
	return nil
}

// WritesBlocked reports whether writes are currently blocked due to capacity
// exhaustion. True between Apply and Restore; false otherwise.
func (f *DiskSpaceExhaustion) WritesBlocked() bool { return f.blocked }

// ---------------------------------------------------------------------------

// ProcessTerminate models a generic process kill (e.g. SIGKILL to a named
// service). Unlike OOMKill (which is triggered by memory pressure from the
// kernel), ProcessTerminate is an explicit external kill of a named process.
// The node itself stays online; only the named process is considered dead.
type ProcessTerminate struct {
	NodeID  string
	Process string
	st      applyState
	killed  bool
}

// Name returns the fault name.
func (f *ProcessTerminate) Name() string { return "process-terminate" }

// Apply marks the named process as killed.
// Returns ErrAlreadyApplied if already applied.
func (f *ProcessTerminate) Apply() error {
	if err := f.st.apply(f.Name()); err != nil {
		return err
	}
	f.killed = true
	return nil
}

// Restore resurrects the process (clears the killed flag).
// Returns ErrNotApplied if not currently applied.
func (f *ProcessTerminate) Restore() error {
	if err := f.st.restore(f.Name()); err != nil {
		return err
	}
	f.killed = false
	return nil
}

// Killed reports whether the process is currently dead.
// True between Apply and Restore; false otherwise.
func (f *ProcessTerminate) Killed() bool { return f.killed }

// ---------------------------------------------------------------------------

// IsActive reports whether the fault is currently applied (forwards to
// applyState so ChaosRunner.ActiveFaults reports it).
func (f *NodeRestart) IsActive() bool { return f.st.isApplied() }

// IsActive reports whether the fault is currently applied.
func (f *DiskSpaceExhaustion) IsActive() bool { return f.st.isApplied() }

// IsActive reports whether the fault is currently applied.
func (f *ProcessTerminate) IsActive() bool { return f.st.isApplied() }
