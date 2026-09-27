package main

import (
	"flag"
	"log"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"time"
)

func main() {
	addr := flag.String("addr", ":6061", "address to serve pprof on")
	flag.Parse()

	go burnCPU()

	log.Printf("workload: serving pprof on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// burnCPU keeps a goroutine busy indefinitely so a CPU profile taken at any
// point in the process's lifetime has real samples to show.
func burnCPU() {
	for {
		for i := 0; i < 2000; i++ {
			isPrime(largeRandomOdd())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func largeRandomOdd() int {
	n := rand.Intn(1_000_000)
	if n%2 == 0 {
		n++
	}
	return n
}

func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	for i := 2; i*i <= n; i++ {
		if n%i == 0 {
			return false
		}
	}
	return true
}
