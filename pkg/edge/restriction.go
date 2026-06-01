package edge

import "fmt"

// WorkloadType enumerates the workload categories that the restriction matrix
// controls. Values are intentionally sequential so a map[WorkloadType]... slice
// can index them cheaply.
type WorkloadType int

const (
	// InteractiveSession is an interactive shell / remote-desktop session.
	// Requires a highly-trusted device because session data is not encrypted
	// at rest and keystrokes / screen contents are sensitive.
	InteractiveSession WorkloadType = iota + 1

	// SensitiveData is any workload that processes PII, credentials, or
	// data classified as CONFIDENTIAL or above.
	SensitiveData

	// PersistentStorage is a workload that writes to durable cluster storage
	// (distributed FS, object store). Requires STANDARD or above to ensure
	// data durability guarantees can be met.
	PersistentStorage

	// NetworkRelay is a workload that forwards cluster-internal traffic.
	// Requires a device trusted enough not to MITM cluster communications.
	NetworkRelay

	// AIInference is a stateless inference request (model serving). Safe to
	// run on any trust level.
	AIInference

	// DataProcessing is a batch analytics or ETL job whose intermediate
	// results are not sensitive.
	DataProcessing

	// SmallBatch is a short-lived, low-memory batch task (typically <30 s,
	// <512 MiB). No sensitivity constraints.
	SmallBatch
)

// String returns a human-readable name for the workload type.
func (wt WorkloadType) String() string {
	switch wt {
	case InteractiveSession:
		return "InteractiveSession"
	case SensitiveData:
		return "SensitiveData"
	case PersistentStorage:
		return "PersistentStorage"
	case NetworkRelay:
		return "NetworkRelay"
	case AIInference:
		return "AIInference"
	case DataProcessing:
		return "DataProcessing"
	case SmallBatch:
		return "SmallBatch"
	default:
		return fmt.Sprintf("WorkloadType(%d)", int(wt))
	}
}

// AdmissionDecision captures the result of a single Allowed() evaluation.
// It is the sink-side evidence artefact for CLAUDE-1.
type AdmissionDecision struct {
	// WorkloadType is the workload being evaluated.
	WorkloadType WorkloadType
	// TrustLevel is the device's trust level.
	TrustLevel TrustLevel
	// Allowed is true iff the workload is permitted.
	Allowed bool
	// PolicyReason is a human-readable explanation of the policy that fired.
	PolicyReason string
}

// Allowed evaluates the restriction matrix and returns true iff a device at
// the given TrustLevel may execute workload w.
//
// Policy matrix (rows = workload, columns = trust level):
//
//	                  EDGE_DONOR   SEMI   STANDARD   FULL
//	InteractiveSession   DENY      DENY    ALLOW     ALLOW
//	SensitiveData        DENY      DENY    DENY      ALLOW
//	PersistentStorage    DENY      DENY    ALLOW     ALLOW
//	NetworkRelay         DENY      DENY    ALLOW     ALLOW
//	AIInference          ALLOW     ALLOW   ALLOW     ALLOW
//	DataProcessing       ALLOW     ALLOW   ALLOW     ALLOW
//	SmallBatch           ALLOW     ALLOW   ALLOW     ALLOW
//
// Note: EDGE_DONOR may ONLY run AIInference, DataProcessing, SmallBatch.
func Allowed(level TrustLevel, w WorkloadType) AdmissionDecision {
	decision := func(allowed bool, reason string) AdmissionDecision {
		return AdmissionDecision{
			WorkloadType: w,
			TrustLevel:   level,
			Allowed:      allowed,
			PolicyReason: reason,
		}
	}

	switch w {
	case InteractiveSession:
		// Requires STANDARD or above.
		if level >= STANDARD {
			return decision(true, "InteractiveSession: requires STANDARD+; level satisfies constraint")
		}
		return decision(false, fmt.Sprintf("InteractiveSession: requires STANDARD+; device has %s (DENIED)", level))

	case SensitiveData:
		// Requires FULL only.
		if level >= FULL {
			return decision(true, "SensitiveData: requires FULL; level satisfies constraint")
		}
		return decision(false, fmt.Sprintf("SensitiveData: requires FULL; device has %s (DENIED)", level))

	case PersistentStorage:
		// Requires STANDARD or above; denied to SEMI and EDGE_DONOR.
		if level >= STANDARD {
			return decision(true, "PersistentStorage: requires STANDARD+; level satisfies constraint")
		}
		return decision(false, fmt.Sprintf("PersistentStorage: requires STANDARD+; device has %s (DENIED)", level))

	case NetworkRelay:
		// Requires STANDARD or above; denied to SEMI and EDGE_DONOR.
		if level >= STANDARD {
			return decision(true, "NetworkRelay: requires STANDARD+; level satisfies constraint")
		}
		return decision(false, fmt.Sprintf("NetworkRelay: requires STANDARD+; device has %s (DENIED)", level))

	case AIInference, DataProcessing, SmallBatch:
		// Allowed at all trust levels (including EDGE_DONOR).
		return decision(true, fmt.Sprintf("%s: permitted at all trust levels including %s", w, level))

	default:
		return decision(false, fmt.Sprintf("unknown WorkloadType %d (DENIED by default)", int(w)))
	}
}
