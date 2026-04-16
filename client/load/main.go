package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func worker(id int, wg *sync.WaitGroup) {
	defer wg.Done()

	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://localhost:1883")
	opts.SetClientID(fmt.Sprintf("client-%d", id))

	client := mqtt.NewClient(opts)
	client.Connect().Wait()

	for i := 0; i < 10; i++ {
		client.Publish("test/topic", 0, false, "load test")
		time.Sleep(100 * time.Millisecond)
	}

	client.Disconnect(250)
}

func main() {
	args := os.Args[1:]

	if len(args) < 1 {
		fmt.Println("Usage: go run main.go <number_of_workers>")
		os.Exit(1)
	}

	noOfWorkers, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Printf("Invalid number of workers: %s\n", args[0])
		os.Exit(1)
	}

	var wg sync.WaitGroup

	for i := 0; i < noOfWorkers; i++ {
		wg.Add(1)
		go worker(i, &wg)
	}

	fmt.Println("Completed launching workers, waiting for them to finish...")

	wg.Wait()

	fmt.Println("All workers completed.")
}
