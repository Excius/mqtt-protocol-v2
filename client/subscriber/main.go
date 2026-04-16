package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

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
		clientID = "subscriber"
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

	opts.SetDefaultPublishHandler(func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("Received: %s\n", msg.Payload())
	})

	client := mqtt.NewClient(opts)

	token := client.Connect()
	token.Wait()
	if token.Error() != nil {
		fmt.Printf("Connect failed: %v\n", token.Error())
		os.Exit(1)
	}

	topic := os.Getenv("MQTT_TOPIC")
	if topic == "" {
		topic = "test/topic"
	}
	subToken := client.Subscribe(topic, 0, nil)
	subToken.Wait()
	if subToken.Error() != nil {
		fmt.Printf("Subscribe failed: %v\n", subToken.Error())
		os.Exit(1)
	}

	select {}
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
