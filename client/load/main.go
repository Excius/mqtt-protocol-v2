package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/paho"
	"github.com/quic-go/quic-go"
)

type runStats struct {
	connectErrors  uint64
	publishErrors  uint64
	totalPublishes uint64
	reconnects     uint64
}

// quicConn is an adapter that wraps a quic.Connection and quic.Stream into a net.Conn.
type quicConn struct {
	*quic.Conn
	*quic.Stream
}

func (q *quicConn) Read(b []byte) (n int, err error)   { return q.Stream.Read(b) }
func (q *quicConn) Write(b []byte) (n int, err error)  { return q.Stream.Write(b) }
func (q *quicConn) Close() error {
	err := q.Stream.Close()
	_ = q.Conn.CloseWithError(0, "client disconnect")
	return err
}
func (q *quicConn) LocalAddr() net.Addr                { return q.Conn.LocalAddr() }
func (q *quicConn) RemoteAddr() net.Addr               { return q.Conn.RemoteAddr() }
func (q *quicConn) SetDeadline(t time.Time) error      { return q.Stream.SetDeadline(t) }
func (q *quicConn) SetReadDeadline(t time.Time) error  { return q.Stream.SetReadDeadline(t) }
func (q *quicConn) SetWriteDeadline(t time.Time) error { return q.Stream.SetWriteDeadline(t) }

func dialBroker(ctx context.Context, brokerURL string, tlsConfig *tls.Config) (net.Conn, error) {
	u, err := url.Parse(brokerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid broker url: %w", err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		host = host + ":1883"
	}

	switch u.Scheme {
	case "quic", "quic+tls":
		// tlsConfig may be shared across concurrent callers (e.g. multiple
		// worker goroutines reconnecting), so clone before mutating it.
		if tlsConfig == nil {
			tlsConfig = &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"mqtt"}}
		} else if len(tlsConfig.NextProtos) == 0 {
			tlsConfig = tlsConfig.Clone()
			tlsConfig.NextProtos = []string{"mqtt"}
		}

		qc, err := quic.DialAddr(ctx, host, tlsConfig, nil)
		if err != nil {
			return nil, fmt.Errorf("quic dial failed: %w", err)
		}
		
		stream, err := qc.OpenStreamSync(ctx)
		if err != nil {
			qc.CloseWithError(0, "")
			return nil, fmt.Errorf("quic open stream failed: %w", err)
		}
		
		return &quicConn{Conn: qc, Stream: stream}, nil

	case "tls", "ssl", "tcps":
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		}
		return tls.Dial("tcp", host, tlsConfig)
	
	default:
		// tcp
		return net.Dial("tcp", host)
	}
}

func loadTLSConfigFromEnv() (*tls.Config, error) {
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

type ClientWrapper struct {
	*paho.Client
	conn net.Conn
}

func worker(id int, brokerURL string, workerPublishes int, delay time.Duration, reconnectEvery int, tlsConfig *tls.Config, stats *runStats, wg *sync.WaitGroup) {
	defer wg.Done()

	priority := os.Getenv("MQTT_PRIORITY")
	integritySecret := os.Getenv("MQTT_INTEGRITY_SECRET")

	connectClient := func() *ClientWrapper {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		
		conn, err := dialBroker(ctx, brokerURL, tlsConfig)
		if err != nil {
			atomic.AddUint64(&stats.connectErrors, 1)
			return nil
		}

		client := paho.NewClient(paho.ClientConfig{
			Conn: conn,
		})

		_, err = client.Connect(ctx, &paho.Connect{
			ClientID:   fmt.Sprintf("load-%d-%d", id, time.Now().UnixNano()),
			CleanStart: true,
			KeepAlive:  30,
		})
		if err != nil {
			conn.Close()
			atomic.AddUint64(&stats.connectErrors, 1)
			return nil
		}
		return &ClientWrapper{Client: client, conn: conn}
	}

	wrapper := connectClient()
	if wrapper == nil {
		return
	}

	topic := "test/load"

	for i := 0; i < workerPublishes; i++ {
		// Periodically reconnect to exercise TLS/QUIC handshake throughout the load
		if reconnectEvery > 0 && i > 0 && i%reconnectEvery == 0 {
			_ = wrapper.Disconnect(&paho.Disconnect{ReasonCode: 0})
			wrapper = connectClient()
			if wrapper == nil {
				return
			}
			atomic.AddUint64(&stats.reconnects, 1)
		}

		payloadBytes := []byte("load test payload")
		
		props := &paho.PublishProperties{}
		if priority != "" {
			props.User = append(props.User, paho.UserProperty{Key: "priority", Value: priority})
		}
		if integritySecret != "" {
			mac := hmac.New(sha256.New, []byte(integritySecret))
			mac.Write([]byte(topic))
			mac.Write(payloadBytes)
			sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
			props.User = append(props.User, paho.UserProperty{Key: "integrity-signature", Value: sig})
		}

		// A QoS 1 publish blocks until the broker PUBACKs it. Some hook
		// rejections (packets.ErrRejectPacket, used by defense modules like
		// property-validator/message-integrity) are silently dropped by the
		// broker with no ack at all. paho's client already caps this at its
		// own PacketTimeout (10s default) via context.Background(), but that
		// means every rejected message burns a full 10 seconds — enough for
		// hundreds of rejected publishes in one worker to turn into tens of
		// minutes of wall-clock time. Use a much shorter, explicit timeout
		// so a rejected/dropped message fails fast as a publish error
		// instead of stalling the load generator.
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := wrapper.Publish(pubCtx, &paho.Publish{
			Topic:      topic,
			Payload:    payloadBytes,
			QoS:        1,
			Properties: props,
		})
		pubCancel()

		if err != nil {
			atomic.AddUint64(&stats.publishErrors, 1)
			continue
		}

		atomic.AddUint64(&stats.totalPublishes, 1)
		if delay > 0 {
			time.Sleep(delay)
		}
	}

	_ = wrapper.Disconnect(&paho.Disconnect{ReasonCode: 0})
}

func main() {
	args := os.Args[1:]

	if len(args) < 1 {
		fmt.Println("Usage: load_client <number_of_workers> [messages_per_worker] [delay_ms] [reconnect_every]")
		fmt.Println("  reconnect_every: reconnect after this many publishes per worker (0 = never)")
		os.Exit(1)
	}

	noOfWorkers, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Printf("Invalid number of workers: %s\n", args[0])
		os.Exit(1)
	}

	workerPublishes := 10
	if len(args) >= 2 {
		workerPublishes, err = strconv.Atoi(args[1])
		if err != nil || workerPublishes < 1 {
			fmt.Printf("Invalid messages_per_worker: %s\n", args[1])
			os.Exit(1)
		}
	}

	delayMS := 100
	if len(args) >= 3 {
		delayMS, err = strconv.Atoi(args[2])
		if err != nil || delayMS < 0 {
			fmt.Printf("Invalid delay_ms: %s\n", args[2])
			os.Exit(1)
		}
	}

	reconnectEvery := 0
	if len(args) >= 4 {
		reconnectEvery, err = strconv.Atoi(args[3])
		if err != nil || reconnectEvery < 0 {
			fmt.Printf("Invalid reconnect_every: %s\n", args[3])
			os.Exit(1)
		}
	}

	delay := time.Duration(delayMS) * time.Millisecond
	brokerURL := os.Getenv("MQTT_BROKER_URL")
	if brokerURL == "" {
		brokerURL = "tcp://localhost:1883"
	}

	var tlsConfig *tls.Config
	if strings.Contains(brokerURL, "tls") || strings.Contains(brokerURL, "ssl") || strings.Contains(brokerURL, "quic") {
		tlsConfig, err = loadTLSConfigFromEnv()
		if err != nil {
			fmt.Printf("TLS config error: %v\n", err)
			os.Exit(1)
		}
	}
	stats := &runStats{}
	runStart := time.Now()

	var wg sync.WaitGroup

	for i := 0; i < noOfWorkers; i++ {
		wg.Add(1)
		go worker(i, brokerURL, workerPublishes, delay, reconnectEvery, tlsConfig, stats, &wg)
	}

	fmt.Println("Completed launching workers, waiting for them to finish (MQTTv5 mode)...")

	wg.Wait()

	duration := time.Since(runStart).Seconds()
	fmt.Printf("SUMMARY workers=%d messages_per_worker=%d delay_ms=%d reconnect_every=%d total_publishes=%d connect_errors=%d publish_errors=%d reconnects=%d duration_seconds=%.3f\n",
		noOfWorkers,
		workerPublishes,
		delayMS,
		reconnectEvery,
		atomic.LoadUint64(&stats.totalPublishes),
		atomic.LoadUint64(&stats.connectErrors),
		atomic.LoadUint64(&stats.publishErrors),
		atomic.LoadUint64(&stats.reconnects),
		duration,
	)

	fmt.Println("All workers completed.")
}
