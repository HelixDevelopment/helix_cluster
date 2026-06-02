package deviceplugin

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

// init registers the JSON codec with gRPC's global encoding registry under
// CodecName. Registration is idempotent and process-global; gRPC selects this
// codec for a call when the content subtype matches CodecName (which Dial and
// RegistrarClient.Register set automatically). Registering an encoding.Codec is
// the supported way to use a non-protobuf message format without protoc.
func init() {
	encoding.RegisterCodec(jsonCodec{})
}

// NewServer constructs a grpc.Server preconfigured for the device-plugin
// service. Additional grpc.ServerOptions (interceptors, creds, ...) may be
// supplied and are appended.
func NewServer(opts ...grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(opts...)
}

// DefaultDialOptions returns the dial options required so a ClientConn speaks
// the JSON codec by default. Callers still supply transport credentials and any
// dialer (e.g. a bufconn ContextDialer) themselves.
func DefaultDialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.CallContentSubtype(CodecName)),
	}
}
