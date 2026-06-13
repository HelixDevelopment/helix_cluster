module github.com/HelixDevelopment/helix_cluster/edge/gateway

go 1.26.4

require (
	github.com/HelixDevelopment/helix_cluster/edge/mqttclient v0.0.0-00010101000000-000000000000
	github.com/HelixDevelopment/helix_cluster/transport/quic v0.0.0-00010101000000-000000000000
	github.com/gorilla/websocket v1.5.3
	github.com/quic-go/quic-go v0.60.0
)

require (
	github.com/eclipse/paho.mqtt.golang v1.5.1 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

// In-workspace the root go.work resolves these sibling modules. The replace
// directives make GOWORK=off standalone builds (and `go test` outside the
// workspace) resolve them from their on-disk locations too.
replace github.com/HelixDevelopment/helix_cluster/transport/quic => ../../transport/quic

replace github.com/HelixDevelopment/helix_cluster/edge/mqttclient => ../mqttclient
