module mqtt-ng-client

go 1.26.1

require (
	github.com/eclipse/paho.golang v0.23.0
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/mochi-mqtt/server/v2 v2.7.9
	github.com/quic-go/quic-go v0.62.0
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/mochi-mqtt/server/v2 => ./broker
