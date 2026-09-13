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
	"time"

	"github.com/eclipse/paho.golang/paho"
	"github.com/quic-go/quic-go"
)

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
		// tlsConfig may be shared across concurrent callers, so clone before mutating it.
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

func main() {
	brokerURL := os.Getenv("MQTT_BROKER_URL")
	if brokerURL == "" {
		brokerURL = "tcp://localhost:1883"
	}

	clientID := os.Getenv("MQTT_CLIENT_ID")
	if clientID == "" {
		clientID = "publisher"
	}

	var tlsConfig *tls.Config
	if strings.Contains(brokerURL, "tls") || strings.Contains(brokerURL, "ssl") || strings.Contains(brokerURL, "quic") {
		var err error
		tlsConfig, err = loadTLSConfigFromEnv()
		if err != nil {
			fmt.Printf("TLS config error: %v\n", err)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := dialBroker(ctx, brokerURL, tlsConfig)
	if err != nil {
		fmt.Printf("Dial failed: %v\n", err)
		os.Exit(1)
	}
	cancel()

	client := paho.NewClient(paho.ClientConfig{
		Conn: conn,
	})

	ctx = context.Background()
	_, err = client.Connect(ctx, &paho.Connect{
		ClientID:   clientID,
		CleanStart: true,
		KeepAlive:  30,
	})
	if err != nil {
		fmt.Printf("Connect failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Connected to broker (MQTTv5)")

	topic := os.Getenv("MQTT_TOPIC")
	if topic == "" {
		topic = "test/topic"
	}
	messageCount := envInt("MQTT_PUBLISH_COUNT", 10)
	publishDelayMS := envInt("MQTT_PUBLISH_DELAY_MS", 1000)

	priority := os.Getenv("MQTT_PRIORITY")
	integritySecret := os.Getenv("MQTT_INTEGRITY_SECRET")

	for i := 0; i < messageCount; i++ {
		text := fmt.Sprintf("Message %d", i)
		payloadBytes := []byte(text)

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

		start := time.Now()

		_, err = client.Publish(ctx, &paho.Publish{
			Topic:      topic,
			Payload:    payloadBytes,
			QoS:        1,
			Properties: props,
		})
		if err != nil {
			fmt.Printf("Publish failed: %v\n", err)
			continue
		}

		elapsed := time.Since(start)
		fmt.Printf("Published: %s | Latency: %v\n", text, elapsed)
		time.Sleep(time.Duration(publishDelayMS) * time.Millisecond)
	}

	_ = client.Disconnect(&paho.Disconnect{ReasonCode: 0})
}

func loadTLSConfigFromEnv() (*tls.Config, error) {
	insecureSkipVerify := strings.EqualFold(os.Getenv("MQTT_TLS_INSECURE_SKIP_VERIFY"), "true")
	serverName := os.Getenv("MQTT_TLS_SERVER_NAME")
	caFile := os.Getenv("MQTT_TLS_CA_FILE")

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecureSkipVerify,
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

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}
