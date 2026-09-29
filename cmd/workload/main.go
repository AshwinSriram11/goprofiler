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
	go steadyChurn()
	go genuineLeak()

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

// steadyChurn repeatedly allocates and discards short-lived buffers, so a
// heap profile shows steady allocation traffic without any growth in
// in-use memory — the baseline "healthy" pattern to contrast against
// genuineLeak.
func steadyChurn() {
	for {
		buf := make([]byte, 64*1024)
		for i := range buf {
			buf[i] = byte(i)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// genuineLeak accumulates buffers in a slice that's never drained, so a
// heap profile taken over time shows in-use memory growing without
// bound — the failure case steadyChurn is meant to contrast against.
func genuineLeak() {
	var leaked [][]byte
	for {
		leaked = append(leaked, make([]byte, 64*1024))
		time.Sleep(50 * time.Millisecond)
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
