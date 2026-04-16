package main

import (
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://localhost:1883")
	opts.SetClientID("publisher")

	client := mqtt.NewClient(opts)

	token := client.Connect()
	token.Wait()
	fmt.Println("Connected to broker")

	for i := 0; i < 10; i++ {
		text := fmt.Sprintf("Message %d", i)

		start := time.Now()

		token = client.Publish("test/topic", 0, false, text)
		token.Wait()

		elapsed := time.Since(start)
		fmt.Println("Latency:", elapsed)

		fmt.Printf("Published: %s\n", text)
		time.Sleep(1 * time.Second)
	}

	client.Disconnect(250)
}
