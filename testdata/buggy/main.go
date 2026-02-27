package main

import (
	"fmt"
	"os"
	"runtime/trace"
	"time"
)

var counter int // shared without mutex — data race

func leakyHandler(id int) {
	ch := make(chan int) // unbuffered, no receiver — goroutine will block forever
	go func() {
		ch <- id // THIS GOROUTINE LEAKS: blocks forever, no one reads ch
	}()
	// function returns; the goroutine above is permanently abandoned
}

func racyIncrement(done chan struct{}) {
	for i := 0; i < 1000; i++ {
		counter++ // DATA RACE: no mutex
	}
	done <- struct{}{}
}

func main() {
	f, err := os.Create("trace.out")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	if err := trace.Start(f); err != nil {
		panic(err)
	}
	defer trace.Stop()

	// Create 5 goroutine leaks
	for i := 0; i < 5; i++ {
		leakyHandler(i)
	}

	// Trigger data race between two goroutines
	done := make(chan struct{}, 2)
	go racyIncrement(done)
	go racyIncrement(done)
	<-done
	<-done

	// Give trace time to capture the blocked (leaked) goroutines
	time.Sleep(300 * time.Millisecond)

	fmt.Printf("counter: %d (expected 2000 — data race may corrupt this)\n", counter)
	fmt.Println("trace.out written")
}
