// Package deviceplugin implements a Nomad/Kubernetes-style gRPC device-plugin
// framework for the Helix scheduler.
//
// The design mirrors how real device-plugin frameworks work: an out-of-process
// (or out-of-package) plugin connects to a central Manager over gRPC and
// "fingerprints" the hardware it manages. The Manager keeps an allocatable
// inventory of devices and matches scheduler GRES (generic-resource) requests
// such as "gpu:rtx3080:2" against that inventory, allocating concrete devices
// and rejecting any request that would oversubscribe beyond the reported count.
//
// WHY a hand-written gRPC surface: the Constitution forbids requiring `protoc`
// in the build/CI path, so the service is described with a hand-written
// grpc.ServiceDesc plus a JSON message codec (see service.go). The calls still
// traverse a real grpc.Server / grpc.ClientConn — there is no in-memory shortcut
// around the wire — which is what makes the integration tests honest under
// CLAUDE-1 (no mock-only validation).
package deviceplugin

import (
	"fmt"
	"strconv"
	"strings"
)

// Health describes the operational state of a single device as reported by its
// plugin. Only Healthy devices are considered allocatable by the Manager.
type Health string

const (
	// HealthUnknown is the zero value; treated as not-allocatable.
	HealthUnknown Health = ""
	// HealthHealthy means the device is usable and allocatable.
	HealthHealthy Health = "healthy"
	// HealthUnhealthy means the device exists but must not be scheduled onto.
	HealthUnhealthy Health = "unhealthy"
)

// Device is the full fingerprint a plugin reports for one physical device.
//
// Every field is part of the wire contract and is asserted end-to-end by the
// integration tests — a device discovered by the Manager must arrive with all
// of these populated exactly as the plugin sent them.
type Device struct {
	// ID is the globally-unique device identifier (e.g. a PCI address or UUID).
	ID string `json:"id"`
	// Model is the marketing/SKU model string used for GRES matching, e.g.
	// "rtx3080". Matching is case-insensitive but the value is preserved.
	Model string `json:"model"`
	// MemoryBytes is the on-device memory in bytes.
	MemoryBytes uint64 `json:"memory_bytes"`
	// DriverVersion is the installed driver version string.
	DriverVersion string `json:"driver_version"`
	// PCIeBandwidth is the negotiated link bandwidth string, e.g. "16GT/s".
	PCIeBandwidth string `json:"pcie_bandwidth"`
	// Capabilities is the set of feature flags the device exposes, e.g.
	// "fp16", "tensor-cores".
	Capabilities []string `json:"capabilities,omitempty"`
	// Health is the operational state; only HealthHealthy is allocatable.
	Health Health `json:"health"`
	// UtilizationPercent is the current device utilization in [0,100].
	UtilizationPercent float64 `json:"utilization_percent"`
}

// Fingerprint is the payload a plugin streams to the Manager during
// registration. It groups every device of a single resource kind reported by
// one plugin instance.
type Fingerprint struct {
	// PluginName identifies the reporting plugin (e.g. "nvidia-gpu").
	PluginName string `json:"plugin_name"`
	// ResourceKind is the GRES kind these devices satisfy, e.g. "gpu".
	ResourceKind string `json:"resource_kind"`
	// Devices is the full set of devices this plugin currently manages.
	Devices []Device `json:"devices"`
}

// Resource is one parsed component of a GRES descriptor string.
//
// A descriptor like "gpu:rtx3080:2" parses into
// {Kind:"gpu", Model:"rtx3080", Count:2}. A typed sub-resource like
// "memory:10Gi" parses into {Kind:"memory", Quantity:"10Gi"} with Count==1.
type Resource struct {
	// Kind is the resource kind, e.g. "gpu", "memory", "pcie".
	Kind string `json:"kind"`
	// Model is the optional model qualifier (only meaningful for device kinds
	// such as "gpu"); empty for quantity-style resources.
	Model string `json:"model"`
	// Count is the number of devices requested; defaults to 1 when omitted.
	Count int `json:"count"`
	// Quantity is the raw quantity token for non-counted resources, e.g.
	// "10Gi" or "16GT/s"; empty for device-count resources.
	Quantity string `json:"quantity,omitempty"`
}

// IsDeviceRequest reports whether this resource is a counted device request
// (it carries an explicit model) rather than a quantity-style resource.
func (r Resource) IsDeviceRequest() bool { return r.Model != "" }

// String formats the resource back into its canonical GRES token.
func (r Resource) String() string {
	switch {
	case r.Model != "":
		return fmt.Sprintf("%s:%s:%d", r.Kind, r.Model, r.Count)
	case r.Quantity != "":
		return fmt.Sprintf("%s:%s", r.Kind, r.Quantity)
	default:
		// Bare counted kind with no model, e.g. "gpu:2".
		return fmt.Sprintf("%s:%d", r.Kind, r.Count)
	}
}

// ResourceRequest is an ordered list of parsed GRES resources.
type ResourceRequest []Resource

// String formats a full request back into a comma-separated GRES descriptor,
// the inverse of ParseGRES (round-trip stable for canonical input).
func (rr ResourceRequest) String() string {
	parts := make([]string, len(rr))
	for i, r := range rr {
		parts[i] = r.String()
	}
	return strings.Join(parts, ",")
}

// ParseGRES parses a GRES descriptor string such as
// "gpu:rtx3080:2,memory:10Gi,pcie:16GT/s" into a structured ResourceRequest.
//
// Grammar (per comma-separated token):
//
//	kind ":" model ":" count   -> counted device request (count is an integer)
//	kind ":" quantity          -> quantity resource (quantity is non-integer)
//	kind ":" count             -> bare counted request (count is an integer)
//
// The third field of a 3-part token MUST be an integer count; the second field
// of a 2-part token is interpreted as a count if it is a pure integer, else as
// an opaque quantity. Whitespace around tokens and fields is tolerated. An
// empty or whitespace-only descriptor yields an empty request and no error.
func ParseGRES(s string) (ResourceRequest, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ResourceRequest{}, nil
	}

	var out ResourceRequest
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return nil, fmt.Errorf("deviceplugin: empty GRES token in %q", s)
		}
		fields := strings.Split(tok, ":")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		switch len(fields) {
		case 2:
			kind, second := fields[0], fields[1]
			if kind == "" || second == "" {
				return nil, fmt.Errorf("deviceplugin: malformed GRES token %q", tok)
			}
			if n, err := strconv.Atoi(second); err == nil {
				if n < 0 {
					return nil, fmt.Errorf("deviceplugin: negative count in %q", tok)
				}
				out = append(out, Resource{Kind: kind, Count: n})
			} else {
				out = append(out, Resource{Kind: kind, Quantity: second})
			}
		case 3:
			kind, model, countStr := fields[0], fields[1], fields[2]
			if kind == "" || model == "" || countStr == "" {
				return nil, fmt.Errorf("deviceplugin: malformed GRES token %q", tok)
			}
			n, err := strconv.Atoi(countStr)
			if err != nil {
				return nil, fmt.Errorf("deviceplugin: non-integer count %q in token %q", countStr, tok)
			}
			if n < 0 {
				return nil, fmt.Errorf("deviceplugin: negative count in %q", tok)
			}
			out = append(out, Resource{Kind: kind, Model: model, Count: n})
		default:
			return nil, fmt.Errorf("deviceplugin: GRES token %q must have 2 or 3 colon-separated fields", tok)
		}
	}
	return out, nil
}
