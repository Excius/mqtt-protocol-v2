package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	opts := mqtt.NewClientOptions()

	brokerURL := os.Getenv("MQTT_BROKER_URL")
	if brokerURL == "" {
		brokerURL = "tcp://localhost:1883"
	}
	opts.AddBroker(brokerURL)

	clientID := os.Getenv("MQTT_CLIENT_ID")
	if clientID == "" {
		clientID = "publisher"
	}
	opts.SetClientID(clientID)

	if strings.HasPrefix(strings.ToLower(brokerURL), "ssl://") || strings.HasPrefix(strings.ToLower(brokerURL), "tls://") {
		tlsConfig, err := loadTLSConfigFromEnv()
		if err != nil {
			fmt.Printf("TLS config error: %v\n", err)
			os.Exit(1)
		}
		opts.SetTLSConfig(tlsConfig)
	}

	client := mqtt.NewClient(opts)

	token := client.Connect()
	token.Wait()
	if token.Error() != nil {
		fmt.Printf("Connect failed: %v\n", token.Error())
		os.Exit(1)
	}
	fmt.Println("Connected to broker")

	topic := os.Getenv("MQTT_TOPIC")
	if topic == "" {
		topic = "test/topic"
	}
	messageCount := envInt("MQTT_PUBLISH_COUNT", 10)
	publishDelayMS := envInt("MQTT_PUBLISH_DELAY_MS", 1000)

	for i := 0; i < messageCount; i++ {
		text := fmt.Sprintf("Message %d", i)

		start := time.Now()

		token = client.Publish(topic, 0, false, text)
		token.Wait()
		if token.Error() != nil {
			fmt.Printf("Publish failed: %v\n", token.Error())
			continue
		}

		elapsed := time.Since(start)
		fmt.Println("Latency:", elapsed)

		fmt.Printf("Published: %s\n", text)
		time.Sleep(time.Duration(publishDelayMS) * time.Millisecond)
	}

	client.Disconnect(250)
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
