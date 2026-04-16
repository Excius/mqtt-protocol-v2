package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type sampleResult struct {
	value   float64
	success bool
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	mode := os.Args[1]
	var err error

	switch mode {
	case "connect":
		err = runConnect(os.Args[2:])
	case "reconnect":
		err = runReconnect(os.Args[2:])
	case "pubsub":
		err = runPubSub(os.Args[2:])
	default:
		printUsage()
		err = fmt.Errorf("unknown mode: %s", mode)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "probe failed: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: go run ./client/probe <connect|reconnect|pubsub> [flags]")
	fmt.Println("Modes:")
	fmt.Println("  connect   Measure connection setup latency")
	fmt.Println("  reconnect Measure first-connect and reconnect latency")
	fmt.Println("  pubsub    Measure publish-to-receive RTT latency")
}

func buildTLSConfigFromEnv() (*tls.Config, error) {
	insecureSkipVerify := strings.EqualFold(os.Getenv("MQTT_TLS_INSECURE_SKIP_VERIFY"), "true")
	serverName := os.Getenv("MQTT_TLS_SERVER_NAME")
	caFile := os.Getenv("MQTT_TLS_CA_FILE")
	cacheSize, err := tlsSessionCacheSizeFromEnv()
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecureSkipVerify,
	}
	if cacheSize > 0 {
		tlsConfig.ClientSessionCache = tls.NewLRUClientSessionCache(cacheSize)
	}

	if serverName != "" {
		tlsConfig.ServerName = serverName
	}

	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read MQTT_TLS_CA_FILE: %w", err)
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("failed to parse CA certificates in %s", caFile)
		}
		tlsConfig.RootCAs = pool
	}

	return tlsConfig, nil
}

func tlsSessionCacheSizeFromEnv() (int, error) {
	val := os.Getenv("MQTT_TLS_SESSION_CACHE_SIZE")
	if val == "" {
		return 100, nil
	}

	size, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("invalid MQTT_TLS_SESSION_CACHE_SIZE: %w", err)
	}
	if size < 0 {
		return 0, fmt.Errorf("invalid MQTT_TLS_SESSION_CACHE_SIZE: must be >= 0")
	}
	return size, nil
}

func tlsConfigForBroker(broker string) (*tls.Config, error) {
	if isTLSBrokerURL(broker) {
		return buildTLSConfigFromEnv()
	}
	return nil, nil
}

func isTLSBrokerURL(broker string) bool {
	lowerBroker := strings.ToLower(broker)
	return strings.HasPrefix(lowerBroker, "ssl://") || strings.HasPrefix(lowerBroker, "tls://")
}

func newClientOptions(broker, clientID string, timeout time.Duration, tlsConfig *tls.Config) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(clientID)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(false)
	opts.SetConnectRetry(false)
	opts.SetConnectTimeout(timeout)
	if tlsConfig != nil {
		opts.SetTLSConfig(tlsConfig)
	}

	return opts
}

func connectOnce(broker, clientID string, timeout time.Duration, tlsConfig *tls.Config, postConnectPause time.Duration) (float64, error) {
	start := time.Now()
	opts := newClientOptions(broker, clientID, timeout, tlsConfig)
	client := mqtt.NewClient(opts)
	token := client.Connect()
	ok := token.WaitTimeout(timeout + (500 * time.Millisecond))
	duration := float64(time.Since(start).Microseconds()) / 1000.0

	if !ok {
		return duration, errors.New("connect timeout")
	}
	if token.Error() != nil {
		return duration, token.Error()
	}

	if postConnectPause > 0 {
		time.Sleep(postConnectPause)
	}
	client.Disconnect(100)
	return duration, nil
}

func runConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	broker := fs.String("broker", "tcp://127.0.0.1:1883", "Broker address")
	attempts := fs.Int("attempts", 500, "Number of connection attempts")
	concurrency := fs.Int("concurrency", 20, "Number of concurrent workers")
	timeoutMS := fs.Int("timeout-ms", 5000, "Connect timeout in milliseconds")
	out := fs.String("out", "connect_latency.csv", "Output CSV path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *attempts < 1 || *concurrency < 1 {
		return errors.New("attempts and concurrency must be > 0")
	}

	timeout := time.Duration(*timeoutMS) * time.Millisecond
	tlsConfig, err := tlsConfigForBroker(*broker)
	if err != nil {
		return err
	}

	type row struct {
		attempt string
		worker  string
		ts      string
		ms      string
		success string
		err     string
	}

	jobs := make(chan int)
	results := make(chan row, *attempts)
	var wg sync.WaitGroup

	for worker := 0; worker < *concurrency; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for attemptID := range jobs {
				clientID := fmt.Sprintf("probe-connect-%d-%d-%d", workerID, attemptID, time.Now().UnixNano())
				durMS, err := connectOnce(*broker, clientID, timeout, tlsConfig, 0)
				r := row{
					attempt: strconv.Itoa(attemptID),
					worker:  strconv.Itoa(workerID),
					ts:      strconv.FormatInt(time.Now().UnixMilli(), 10),
					ms:      fmt.Sprintf("%.3f", durMS),
				}
				if err != nil {
					r.success = "false"
					r.err = sanitizeErr(err.Error())
				} else {
					r.success = "true"
					r.err = ""
				}
				results <- r
			}
		}(worker)
	}

	for i := 1; i <= *attempts; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)

	records := make([]row, 0, *attempts)
	samples := make([]sampleResult, 0, *attempts)
	for r := range results {
		records = append(records, r)
		v, _ := strconv.ParseFloat(r.ms, 64)
		samples = append(samples, sampleResult{value: v, success: r.success == "true"})
	}

	sort.Slice(records, func(i, j int) bool {
		ai, _ := strconv.Atoi(records[i].attempt)
		aj, _ := strconv.Atoi(records[j].attempt)
		if ai == aj {
			wi, _ := strconv.Atoi(records[i].worker)
			wj, _ := strconv.Atoi(records[j].worker)
			return wi < wj
		}
		return ai < aj
	})

	rows := make([][]string, 0, len(records)+1)
	rows = append(rows, []string{"attempt_id", "worker_id", "timestamp_ms", "connect_ms", "success", "error"})
	for _, r := range records {
		rows = append(rows, []string{r.attempt, r.worker, r.ts, r.ms, r.success, r.err})
	}

	if err := writeCSV(*out, rows); err != nil {
		return err
	}

	printSummary("connect", samples)
	return nil
}

func runReconnect(args []string) error {
	fs := flag.NewFlagSet("reconnect", flag.ContinueOnError)
	broker := fs.String("broker", "tcp://127.0.0.1:1883", "Broker address")
	attempts := fs.Int("attempts", 400, "Number of reconnect attempts")
	timeoutMS := fs.Int("timeout-ms", 5000, "Connect timeout in milliseconds")
	gapMS := fs.Int("gap-ms", 20, "Pause between disconnect and reconnect in milliseconds")
	ticketWaitMS := fs.Int("ticket-wait-ms", 100, "How long to keep first TLS connection open for session ticket delivery")
	out := fs.String("out", "reconnect_latency.csv", "Output CSV path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *attempts < 1 {
		return errors.New("attempts must be > 0")
	}

	timeout := time.Duration(*timeoutMS) * time.Millisecond
	gap := time.Duration(*gapMS) * time.Millisecond
	ticketWait := time.Duration(*ticketWaitMS) * time.Millisecond
	useTLS := isTLSBrokerURL(*broker)

	rows := make([][]string, 0, *attempts+1)
	rows = append(rows, []string{"attempt_id", "first_connect_ms", "reconnect_ms", "delta_ms", "success", "error"})
	reconnectSamples := make([]sampleResult, 0, *attempts)
	firstConnectSamples := make([]sampleResult, 0, *attempts)

	for i := 1; i <= *attempts; i++ {
		clientID := fmt.Sprintf("probe-reconnect-%d", i)
		var tlsConfig *tls.Config
		if useTLS {
			var err error
			tlsConfig, err = buildTLSConfigFromEnv()
			if err != nil {
				return err
			}
		}

		firstMS, firstErr := connectOnce(*broker, clientID, timeout, tlsConfig, ticketWait)
		if firstErr != nil {
			rows = append(rows, []string{strconv.Itoa(i), fmt.Sprintf("%.3f", firstMS), "", "", "false", sanitizeErr(firstErr.Error())})
			firstConnectSamples = append(firstConnectSamples, sampleResult{value: firstMS, success: false})
			reconnectSamples = append(reconnectSamples, sampleResult{value: firstMS, success: false})
			continue
		}
		firstConnectSamples = append(firstConnectSamples, sampleResult{value: firstMS, success: true})

		time.Sleep(gap)

		reMS, reErr := connectOnce(*broker, clientID, timeout, tlsConfig, 0)
		if reErr != nil {
			rows = append(rows, []string{strconv.Itoa(i), fmt.Sprintf("%.3f", firstMS), fmt.Sprintf("%.3f", reMS), "", "false", sanitizeErr(reErr.Error())})
			reconnectSamples = append(reconnectSamples, sampleResult{value: reMS, success: false})
			continue
		}

		deltaMS := firstMS - reMS
		rows = append(rows, []string{
			strconv.Itoa(i),
			fmt.Sprintf("%.3f", firstMS),
			fmt.Sprintf("%.3f", reMS),
			fmt.Sprintf("%.3f", deltaMS),
			"true",
			"",
		})
		reconnectSamples = append(reconnectSamples, sampleResult{value: reMS, success: true})
	}

	if err := writeCSV(*out, rows); err != nil {
		return err
	}

	printSummary("reconnect", reconnectSamples)
	printResumptionSummary(firstConnectSamples, reconnectSamples)
	return nil
}

func runPubSub(args []string) error {
	fs := flag.NewFlagSet("pubsub", flag.ContinueOnError)
	broker := fs.String("broker", "tcp://127.0.0.1:1883", "Broker address")
	samplesCount := fs.Int("samples", 1200, "Number of pubsub RTT samples")
	qos := fs.Int("qos", 0, "QoS level (0 or 1)")
	payloadBytes := fs.Int("payload-bytes", 128, "Payload size in bytes")
	timeoutMS := fs.Int("timeout-ms", 5000, "Per-message timeout in milliseconds")
	warmup := fs.Int("warmup", 20, "Warmup messages before collecting samples")
	out := fs.String("out", "pubsub_rtt.csv", "Output CSV path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *samplesCount < 1 {
		return errors.New("samples must be > 0")
	}
	if *qos != 0 && *qos != 1 {
		return errors.New("qos must be 0 or 1")
	}
	if *payloadBytes < 8 {
		return errors.New("payload-bytes must be >= 8")
	}

	timeout := time.Duration(*timeoutMS) * time.Millisecond
	tlsConfig, err := tlsConfigForBroker(*broker)
	if err != nil {
		return err
	}
	topic := fmt.Sprintf("probe/rtt/%d", time.Now().UnixNano())
	subID := fmt.Sprintf("probe-sub-%d", time.Now().UnixNano())
	pubID := fmt.Sprintf("probe-pub-%d", time.Now().UnixNano())

	pending := map[string]chan time.Time{}
	var pendingMu sync.Mutex

	subOpts := newClientOptions(*broker, subID, timeout, tlsConfig)
	sub := mqtt.NewClient(subOpts)
	subConn := sub.Connect()
	subConn.WaitTimeout(timeout + (500 * time.Millisecond))
	if subConn.Error() != nil {
		return fmt.Errorf("subscriber connect failed: %w", subConn.Error())
	}
	defer sub.Disconnect(100)

	cb := func(_ mqtt.Client, msg mqtt.Message) {
		payload := string(msg.Payload())
		parts := strings.SplitN(payload, "|", 2)
		msgID := parts[0]

		pendingMu.Lock()
		ch, ok := pending[msgID]
		if ok {
			delete(pending, msgID)
		}
		pendingMu.Unlock()

		if ok {
			select {
			case ch <- time.Now():
			default:
			}
		}
	}

	subTok := sub.Subscribe(topic, byte(*qos), cb)
	subTok.WaitTimeout(timeout + (500 * time.Millisecond))
	if subTok.Error() != nil {
		return fmt.Errorf("subscribe failed: %w", subTok.Error())
	}
	defer sub.Unsubscribe(topic)

	pubOpts := newClientOptions(*broker, pubID, timeout, tlsConfig)
	pub := mqtt.NewClient(pubOpts)
	pubConn := pub.Connect()
	pubConn.WaitTimeout(timeout + (500 * time.Millisecond))
	if pubConn.Error() != nil {
		return fmt.Errorf("publisher connect failed: %w", pubConn.Error())
	}
	defer pub.Disconnect(100)

	rows := make([][]string, 0, *samplesCount+1)
	rows = append(rows, []string{"sample_id", "qos", "payload_bytes", "publish_wait_ms", "rtt_ms", "success", "error"})
	stats := make([]sampleResult, 0, *samplesCount)

	total := *warmup + *samplesCount
	for i := 1; i <= total; i++ {
		msgID := fmt.Sprintf("%d-%d", i, rand.Int63())
		payload := buildPayload(msgID, *payloadBytes)

		ch := make(chan time.Time, 1)
		pendingMu.Lock()
		pending[msgID] = ch
		pendingMu.Unlock()

		pubStart := time.Now()
		pubTok := pub.Publish(topic, byte(*qos), false, payload)
		pubOk := pubTok.WaitTimeout(timeout)
		pubWaitMS := float64(time.Since(pubStart).Microseconds()) / 1000.0

		if !pubOk || pubTok.Error() != nil {
			pendingMu.Lock()
			delete(pending, msgID)
			pendingMu.Unlock()

			errText := "publish timeout"
			if pubTok.Error() != nil {
				errText = sanitizeErr(pubTok.Error().Error())
			}

			if i > *warmup {
				rows = append(rows, []string{strconv.Itoa(i - *warmup), strconv.Itoa(*qos), strconv.Itoa(*payloadBytes), fmt.Sprintf("%.3f", pubWaitMS), "", "false", errText})
				stats = append(stats, sampleResult{value: pubWaitMS, success: false})
			}
			continue
		}

		sendAt := time.Now()
		select {
		case recvAt := <-ch:
			rttMS := float64(recvAt.Sub(sendAt).Microseconds()) / 1000.0
			if i > *warmup {
				rows = append(rows, []string{strconv.Itoa(i - *warmup), strconv.Itoa(*qos), strconv.Itoa(*payloadBytes), fmt.Sprintf("%.3f", pubWaitMS), fmt.Sprintf("%.3f", rttMS), "true", ""})
				stats = append(stats, sampleResult{value: rttMS, success: true})
			}
		case <-time.After(timeout):
			pendingMu.Lock()
			delete(pending, msgID)
			pendingMu.Unlock()
			if i > *warmup {
				rows = append(rows, []string{strconv.Itoa(i - *warmup), strconv.Itoa(*qos), strconv.Itoa(*payloadBytes), fmt.Sprintf("%.3f", pubWaitMS), "", "false", "receive timeout"})
				stats = append(stats, sampleResult{value: pubWaitMS, success: false})
			}
		}
	}

	if err := writeCSV(*out, rows); err != nil {
		return err
	}

	printSummary("pubsub", stats)
	return nil
}

func buildPayload(id string, bytes int) string {
	if len(id) >= bytes {
		return id
	}

	prefix := id + "|"
	if len(prefix) >= bytes {
		return prefix[:bytes]
	}

	padLen := bytes - len(prefix)
	return prefix + strings.Repeat("x", padLen)
}

func sanitizeErr(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

func writeCSV(path string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := make([]float64, len(values))
	copy(cp, values)
	sort.Float64s(cp)

	idx := int((p / 100.0) * float64(len(cp)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func printSummary(mode string, samples []sampleResult) {
	successValues := make([]float64, 0, len(samples))
	successCount := 0
	for _, s := range samples {
		if s.success {
			successCount++
			successValues = append(successValues, s.value)
		}
	}

	failureCount := len(samples) - successCount
	p50 := percentile(successValues, 50)
	p95 := percentile(successValues, 95)
	p99 := percentile(successValues, 99)
	avg := 0.0
	for _, v := range successValues {
		avg += v
	}
	if len(successValues) > 0 {
		avg /= float64(len(successValues))
	}

	fmt.Printf("SUMMARY mode=%s total=%d success=%d failure=%d avg_ms=%.3f p50_ms=%.3f p95_ms=%.3f p99_ms=%.3f\n",
		mode,
		len(samples),
		successCount,
		failureCount,
		avg,
		p50,
		p95,
		p99,
	)
}

func printResumptionSummary(first []sampleResult, reconnect []sampleResult) {
	firstAvg := averageSuccessfulSamples(first)
	reconnectAvg := averageSuccessfulSamples(reconnect)
	if firstAvg == 0 || reconnectAvg == 0 {
		fmt.Println("RESUMPTION first_avg_ms=0 reconnect_avg_ms=0 speedup_x=0")
		return
	}
	fmt.Printf("RESUMPTION first_avg_ms=%.3f reconnect_avg_ms=%.3f speedup_x=%.2f\n", firstAvg, reconnectAvg, firstAvg/reconnectAvg)
}

func averageSuccessfulSamples(samples []sampleResult) float64 {
	if len(samples) == 0 {
		return 0
	}

	sum := 0.0
	count := 0
	for _, sample := range samples {
		if !sample.success {
			continue
		}
		sum += sample.value
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
