module helix-build

go 1.25.0

require (
	api v0.0.0
	build v0.0.0
	google.golang.org/grpc v1.81.1
)

require (
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace (
	api => ../../api/v1
	build => ../../pkg/build
)
