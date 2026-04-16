package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type runStats struct {
	connectErrors  uint64
	publishErrors  uint64
	totalPublishes uint64
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

func worker(id int, brokerURL string, workerPublishes int, delay time.Duration, tlsConfig *tls.Config, stats *runStats, wg *sync.WaitGroup) {
	defer wg.Done()

	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerURL)
	opts.SetClientID(fmt.Sprintf("client-%d", id))
	if tlsConfig != nil {
		opts.SetTLSConfig(tlsConfig)
	}

	client := mqtt.NewClient(opts)
	connectToken := client.Connect()
	connectToken.Wait()
	if connectToken.Error() != nil {
		atomic.AddUint64(&stats.connectErrors, 1)
		return
	}

	for i := 0; i < workerPublishes; i++ {
		token := client.Publish("test/topic", 0, false, "load test")
		token.Wait()
		if token.Error() != nil {
			atomic.AddUint64(&stats.publishErrors, 1)
			continue
		}

		atomic.AddUint64(&stats.totalPublishes, 1)
		if delay > 0 {
			time.Sleep(delay)
		}
	}

	client.Disconnect(250)
}

func main() {
	args := os.Args[1:]

	if len(args) < 1 {
		fmt.Println("Usage: go run main.go <number_of_workers> [messages_per_worker] [delay_ms]")
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

	delay := time.Duration(delayMS) * time.Millisecond
	brokerURL := os.Getenv("MQTT_BROKER_URL")
	if brokerURL == "" {
		brokerURL = "tcp://localhost:1883"
	}

	var tlsConfig *tls.Config
	if strings.HasPrefix(strings.ToLower(brokerURL), "ssl://") || strings.HasPrefix(strings.ToLower(brokerURL), "tls://") {
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
		go worker(i, brokerURL, workerPublishes, delay, tlsConfig, stats, &wg)
	}

	fmt.Println("Completed launching workers, waiting for them to finish...")

	wg.Wait()

	duration := time.Since(runStart).Seconds()
	fmt.Printf("SUMMARY workers=%d messages_per_worker=%d delay_ms=%d total_publishes=%d connect_errors=%d publish_errors=%d duration_seconds=%.3f\n",
		noOfWorkers,
		workerPublishes,
		delayMS,
		atomic.LoadUint64(&stats.totalPublishes),
		atomic.LoadUint64(&stats.connectErrors),
		atomic.LoadUint64(&stats.publishErrors),
		duration,
	)

	fmt.Println("All workers completed.")
}
