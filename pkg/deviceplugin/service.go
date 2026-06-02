package deviceplugin

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc"
)

// ----------------------------------------------------------------------------
// Hand-written gRPC service surface.
//
// We deliberately avoid protoc/.proto generation (forbidden in the build path).
// Instead the service is described with a hand-written grpc.ServiceDesc and the
// request/response messages are carried with a JSON codec registered on both
// the server and the dialled client. Crucially, the RPC still goes through a
// real grpc.Server and grpc.ClientConn over a real net.Conn (bufconn or TCP) —
// there is no in-memory shortcut, so integration tests exercise true wire
// traffic as a consumer would.
// ----------------------------------------------------------------------------

// ServiceName is the fully-qualified gRPC service name for the registrar.
const ServiceName = "helix.deviceplugin.v1.Registrar"

// CodecName is the name under which the JSON codec is registered.
const CodecName = "helix-deviceplugin-json"

// RegisterRequest is the message a plugin sends to push a fingerprint.
type RegisterRequest struct {
	Fingerprint Fingerprint `json:"fingerprint"`
}

// RegisterResponse acknowledges a successful registration and echoes the number
// of devices the Manager ingested (so the plugin gets sink-side confirmation).
type RegisterResponse struct {
	Accepted     bool   `json:"accepted"`
	DevicesKnown int    `json:"devices_known"`
	Message      string `json:"message,omitempty"`
}

// jsonCodec is a grpc.Codec / encoding.Codec that marshals messages as JSON.
// Using JSON keeps the service free of generated protobuf types while still
// traversing the real gRPC framing layer.
type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (jsonCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
func (jsonCodec) Name() string                       { return CodecName }

// JSONCodec returns the codec instance used by both server and client. Callers
// pass grpc.ForceServerCodecV2 / grpc.CallContentSubtype to opt into it; see
// NewServer and Dial below which wire it automatically.
func JSONCodec() encodingCodec { return jsonCodec{} }

// encodingCodec is the minimal codec interface we depend on. It matches
// google.golang.org/grpc/encoding.Codec so JSONCodec can be registered.
type encodingCodec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
	Name() string
}

// RegistrarServer is the server-side handler interface for the registrar
// service. Implementations receive plugin fingerprints.
type RegistrarServer interface {
	// Register ingests one fingerprint from a plugin and returns an ack.
	Register(ctx context.Context, req *RegisterRequest) (*RegisterResponse, error)
}

// registrarServiceDesc is the hand-written gRPC service descriptor. It maps the
// single "Register" method to a handler that decodes the JSON request, invokes
// the RegistrarServer, applies any unary interceptor, and returns the response.
var registrarServiceDesc = grpc.ServiceDesc{
	ServiceName: ServiceName,
	HandlerType: (*RegistrarServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Register",
			Handler:    registerHandler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "helix/deviceplugin/v1",
}

// registerHandler is the server-side method handler invoked by grpc.Server when
// a "Register" call arrives. dec populates the request via the registered codec.
func registerHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(RegisterRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RegistrarServer).Register(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/" + ServiceName + "/Register",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(RegistrarServer).Register(ctx, req.(*RegisterRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// RegisterRegistrarServer wires a RegistrarServer implementation into a
// grpc.Server using the hand-written descriptor.
func RegisterRegistrarServer(s grpc.ServiceRegistrar, impl RegistrarServer) {
	s.RegisterService(&registrarServiceDesc, impl)
}

// RegistrarClient is the plugin-side client for the registrar service.
type RegistrarClient struct {
	cc *grpc.ClientConn
}

// NewRegistrarClient wraps an existing ClientConn. The ClientConn MUST have been
// dialled with the JSON codec as its default content subtype (use Dial or pass
// grpc.WithDefaultCallOptions(grpc.CallContentSubtype(CodecName))).
func NewRegistrarClient(cc *grpc.ClientConn) *RegistrarClient {
	return &RegistrarClient{cc: cc}
}

// Register sends a fingerprint to the Manager over real gRPC and returns the
// server's acknowledgement.
func (c *RegistrarClient) Register(ctx context.Context, req *RegisterRequest, opts ...grpc.CallOption) (*RegisterResponse, error) {
	out := new(RegisterResponse)
	// Force the JSON content subtype for this call so the registered codec is
	// used regardless of the dial-time default.
	callOpts := append([]grpc.CallOption{grpc.CallContentSubtype(CodecName)}, opts...)
	if err := c.cc.Invoke(ctx, "/"+ServiceName+"/Register", req, out, callOpts...); err != nil {
		return nil, fmt.Errorf("deviceplugin: Register RPC failed: %w", err)
	}
	return out, nil
}

// managerServer adapts a *Registry into a RegistrarServer: each incoming
// fingerprint is applied to the registry and acknowledged with the resulting
// device count.
type managerServer struct {
	reg *Registry
}

// NewManagerServer returns a RegistrarServer that ingests fingerprints into reg.
func NewManagerServer(reg *Registry) RegistrarServer {
	return &managerServer{reg: reg}
}

func (m *managerServer) Register(ctx context.Context, req *RegisterRequest) (*RegisterResponse, error) {
	if req == nil {
		return &RegisterResponse{Accepted: false, Message: "nil request"}, nil
	}
	if err := m.reg.ApplyFingerprint(req.Fingerprint); err != nil {
		return &RegisterResponse{Accepted: false, Message: err.Error()}, nil
	}
	return &RegisterResponse{
		Accepted:     true,
		DevicesKnown: m.reg.Count(),
	}, nil
}
