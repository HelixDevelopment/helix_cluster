package gpuattest

import "errors"

// HXC-1573: multi-GPU node enumeration.
//
// A node descriptor with N GPUs yields N distinct, independently-verifiable
// per-device attestation work products keyed by device UUID. Each per-device
// proof bundles the challenge issued to that device, the device's own signed
// response, and the descriptor that was attested — enough to verify the proof
// in isolation via VerifyNode without any shared/global state beyond the
// Verifier's single-use nonce set.

var (
	// ErrDuplicateGPUUUID is returned when two devices in a NodeDescriptor share
	// the same descriptor UUID. Per-device proofs are keyed by UUID, so a
	// duplicate would make a proof ambiguous; enumeration rejects the node
	// outright rather than silently collapsing the pair.
	ErrDuplicateGPUUUID = errors.New("gpuattest: duplicate GPU UUID in node")
	// ErrNoDevices is returned when a NodeDescriptor carries zero devices. A node
	// with nothing to attest is a caller error, not an empty-but-valid result.
	ErrNoDevices = errors.New("gpuattest: node has no devices")
)

// NodeDescriptor names a node and the set of GPU devices physically present on
// it. Devices each carry their own key pair and descriptor (see Device).
type NodeDescriptor struct {
	NodeID  string
	Devices []*Device
}

// DeviceProof is the per-device attestation work product. It is keyed by
// GPUUUID (the attested descriptor's UUID) and bundles everything needed to
// independently verify this single device: the issued Challenge, the device's
// signed Response, and the Descriptor that was attested. VerifyNode verifies
// each DeviceProof on its own.
type DeviceProof struct {
	GPUUUID    string
	Challenge  Challenge
	Response   Response
	Descriptor Descriptor
}

// EnumerateNode produces one DeviceProof per device in the node. For each
// device it issues a fresh challenge from v, has THAT device Respond at
// respondTick, and records the proof keyed by the device's descriptor UUID.
//
// It rejects an empty device list (ErrNoDevices) and any duplicate device UUID
// (ErrDuplicateGPUUUID) BEFORE issuing any challenge, so a malformed node never
// consumes verifier nonces. The returned slice has exactly len(node.Devices)
// proofs, in device order, each with GPUUUID equal to that device's UUID.
func EnumerateNode(v *Verifier, node NodeDescriptor, issueTick, expiryTick, respondTick int64) ([]DeviceProof, error) {
	if len(node.Devices) == 0 {
		return nil, ErrNoDevices
	}

	// Reject duplicate UUIDs up front so we never half-enumerate a bad node.
	seen := make(map[string]struct{}, len(node.Devices))
	for _, dev := range node.Devices {
		uuid := dev.Descriptor.UUID
		if _, dup := seen[uuid]; dup {
			return nil, ErrDuplicateGPUUUID
		}
		seen[uuid] = struct{}{}
	}

	proofs := make([]DeviceProof, 0, len(node.Devices))
	for _, dev := range node.Devices {
		ch, err := v.Issue(issueTick, expiryTick)
		if err != nil {
			return nil, err
		}
		resp := dev.Respond(ch, respondTick)
		proofs = append(proofs, DeviceProof{
			GPUUUID:    dev.Descriptor.UUID,
			Challenge:  ch,
			Response:   resp,
			Descriptor: dev.Descriptor,
		})
	}
	return proofs, nil
}

// VerifyNode independently verifies every DeviceProof at nowTick. Each proof is
// checked in isolation against its own challenge, response and descriptor; any
// single failure makes VerifyNode return that proof's verification error. A nil
// return means every per-device proof verified genuinely.
func VerifyNode(v *Verifier, proofs []DeviceProof, nowTick int64) error {
	for _, p := range proofs {
		if err := v.Verify(p.Challenge, p.Response, p.Descriptor, nowTick); err != nil {
			return err
		}
	}
	return nil
}
