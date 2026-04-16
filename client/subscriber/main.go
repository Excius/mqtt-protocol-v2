package main

import (
	"fmt"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://localhost:1883")
	opts.SetClientID("subscriber")

	opts.SetDefaultPublishHandler(func(clinet mqtt.Client, msg mqtt.Message) {
		fmt.Printf("Received: %s\n", msg.Payload())
	})

	client := mqtt.NewClient(opts)

	token := client.Connect()
	token.Wait()

	client.Subscribe("test/topic", 0, nil)

	select {}
}
