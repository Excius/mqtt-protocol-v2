// priority_bench measures whether the priority-messaging module actually
// reorders delivery under contention — the load-based experiment in
// experiments/priority_messaging only ever sends one uniform priority value,
// so it can't demonstrate this. This program:
//
//  1. Subscribes to a fresh topic and records the receive order and time of
//     every message.
//  2. Runs several trials. Each trial fires a "flood" of QoS 0 Normal-priority
//     messages back-to-back, then immediately fires one Urgent-priority
//     message, and waits for delivery.
//  3. For each trial, records the Urgent message's receive RANK (1 = received
//     first, despite being sent last) and its latency vs the flood's average.
//
// Run once against a broker with no modules (baseline) and once against a
// broker with --modules priority-messaging, and compare the two CSVs: under
// strict FIFO the Urgent message should rank near the back of the flood；
// with the module active it should consistently rank at or near the front.
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

func main() {
	broker := flag.String("broker", "tcp://127.0.0.1:1883", "Broker address")
	trials := flag.Int("trials", 20, "Number of trials")
	floodCount := flag.Int("flood", 500, "Total Normal-priority messages sent per trial, spread across --flood-conns connections, racing against the one Urgent message")
	floodConns := flag.Int("flood-conns", 25, "Concurrent connections used to send the flood (a single connection's serial read loop on the broker can't outpace its own scheduler, so concurrency is what actually builds a queue backlog)")
	waitMS := flag.Int("wait-ms", 1500, "How long to wait for delivery per trial")
	out := flag.String("out", "priority_ordering.csv", "Output CSV path")
	label := flag.String("label", "run", "Scenario label written into the CSV (e.g. baseline / module)")
	flag.Parse()

	u := strings.TrimPrefix(strings.TrimPrefix(*broker, "tcp://"), "tls://")
	topic := fmt.Sprintf("bench/priority/%d", time.Now().UnixNano())

	var mu sync.Mutex
	received := make(map[string]int64) // msgID -> recvAtNs
	var recvOrder []string             // msgID in the order received

	subConn, err := net.Dial("tcp", u)
	if err != nil {
		fmt.Printf("subscriber dial failed: %v\n", err)
		os.Exit(1)
	}
	sub := paho.NewClient(paho.ClientConfig{
		Conn: subConn,
		Router: paho.NewSingleHandlerRouter(func(p *paho.Publish) {
			parts := strings.SplitN(string(p.Payload), "|", 3)
			if len(parts) != 3 {
				return
			}
			mu.Lock()
			received[parts[0]] = time.Now().UnixNano()
			recvOrder = append(recvOrder, parts[0])
			mu.Unlock()
		}),
	})
	ctx := context.Background()
	if _, err := sub.Connect(ctx, &paho.Connect{ClientID: fmt.Sprintf("bench-sub-%d", time.Now().UnixNano()), CleanStart: true, KeepAlive: 30}); err != nil {
		fmt.Printf("subscriber connect failed: %v\n", err)
		os.Exit(1)
	}
	if _, err := sub.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 0}}}); err != nil {
		fmt.Printf("subscribe failed: %v\n", err)
		os.Exit(1)
	}

	floodPubs := make([]*paho.Client, *floodConns)
	for i := range floodPubs {
		conn, err := net.Dial("tcp", u)
		if err != nil {
			fmt.Printf("flood-publisher %d dial failed: %v\n", i, err)
			os.Exit(1)
		}
		client := paho.NewClient(paho.ClientConfig{Conn: conn})
		if _, err := client.Connect(ctx, &paho.Connect{ClientID: fmt.Sprintf("bench-flood-%d-%d", i, time.Now().UnixNano()), CleanStart: true, KeepAlive: 30}); err != nil {
			fmt.Printf("flood-publisher %d connect failed: %v\n", i, err)
			os.Exit(1)
		}
		floodPubs[i] = client
	}

	rows := [][]string{{"trial", "label", "flood_count", "flood_conns", "urgent_recv_rank", "total_received", "urgent_latency_ms", "flood_avg_latency_ms", "flood_p50_latency_ms"}}

	publishOn := func(client *paho.Client, msgID, priority string) int64 {
		sendAt := time.Now().UnixNano()
		payload := fmt.Sprintf("%s|%s|%d", msgID, priority, sendAt)
		props := &paho.PublishProperties{User: []paho.UserProperty{{Key: "priority", Value: priority}}}
		_, _ = client.Publish(ctx, &paho.Publish{Topic: topic, Payload: []byte(payload), QoS: 0, Properties: props})
		return sendAt
	}

	for trial := 1; trial <= *trials; trial++ {
		mu.Lock()
		received = make(map[string]int64)
		recvOrder = nil
		mu.Unlock()

		var floodMu sync.Mutex
		floodIDs := make([]string, 0, *floodCount)
		floodSendAt := make(map[string]int64, *floodCount)

		var wg sync.WaitGroup
		var urgentSendAt int64
		urgentID := fmt.Sprintf("t%d-urgent", trial)

		perConn := *floodCount / *floodConns
		start := make(chan struct{})

		// Fire the flood from many concurrent connections, all released at
		// once by closing `start` — concurrency across connections is what
		// builds a genuine backlog (a single connection's serial read loop
		// on the broker can't outpace its own scheduler goroutine, so with
		// only one publisher there's never anything to reorder). The single
		// Urgent message is sent from connection 0, interleaved halfway
		// through *that connection's own* flood send loop — deliberately
		// NOT from a separate dedicated connection, which would give it an
		// unfair, priority-unrelated scheduling advantage (an earlier
		// version of this benchmark did that, and baseline showed a strong,
		// bogus "cuts in line" effect purely from being alone on its own
		// socket). This way the only reason the Urgent message could rank
		// differently from its neighboring Normal sends on the same
		// connection is the priority-messaging module itself.
		urgentAtIdx := perConn / 2
		for c := 0; c < *floodConns; c++ {
			wg.Add(1)
			go func(conn *paho.Client, connIdx int) {
				defer wg.Done()
				<-start
				for i := 0; i < perConn; i++ {
					if connIdx == 0 && i == urgentAtIdx {
						urgentSendAt = publishOn(conn, urgentID, "Urgent")
					}
					id := fmt.Sprintf("t%d-flood-%d-%d", trial, connIdx, i)
					sendAt := publishOn(conn, id, "Normal")
					floodMu.Lock()
					floodIDs = append(floodIDs, id)
					floodSendAt[id] = sendAt
					floodMu.Unlock()
				}
			}(floodPubs[c], c)
		}

		close(start)
		wg.Wait()

		time.Sleep(time.Duration(*waitMS) * time.Millisecond)

		mu.Lock()
		rank := -1
		for i, id := range recvOrder {
			if id == urgentID {
				rank = i + 1 // 1-indexed: 1 = received first
				break
			}
		}
		totalReceived := len(recvOrder)

		var floodLatencies []float64
		for _, id := range floodIDs {
			if recvAt, ok := received[id]; ok {
				floodLatencies = append(floodLatencies, float64(recvAt-floodSendAt[id])/1e6)
			}
		}
		var urgentLatencyMS float64 = -1
		if recvAt, ok := received[urgentID]; ok {
			urgentLatencyMS = float64(recvAt-urgentSendAt) / 1e6
		}
		mu.Unlock()

		avg, p50 := stats(floodLatencies)
		rows = append(rows, []string{
			strconv.Itoa(trial),
			*label,
			strconv.Itoa(*floodCount),
			strconv.Itoa(*floodConns),
			strconv.Itoa(rank),
			strconv.Itoa(totalReceived),
			fmt.Sprintf("%.3f", urgentLatencyMS),
			fmt.Sprintf("%.3f", avg),
			fmt.Sprintf("%.3f", p50),
		})

		fmt.Printf("trial=%d label=%s urgent_rank=%d/%d urgent_latency_ms=%.3f flood_avg_ms=%.3f\n", trial, *label, rank, totalReceived, urgentLatencyMS, avg)
	}

	_ = sub.Disconnect(&paho.Disconnect{ReasonCode: 0})
	for _, client := range floodPubs {
		_ = client.Disconnect(&paho.Disconnect{ReasonCode: 0})
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Printf("failed to write csv: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.WriteAll(rows)
	w.Flush()

	fmt.Printf("Wrote %s\n", *out)
}

func stats(vals []float64) (avg, p50 float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	avg = sum / float64(len(vals))

	sorted := append([]float64(nil), vals...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	p50 = sorted[len(sorted)/2]
	return avg, p50
}
