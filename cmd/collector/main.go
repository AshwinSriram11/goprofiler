package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	target := flag.String("target", "http://localhost:6061", "base URL of the service to profile")
	service := flag.String("service", "workload", "label identifying the profiled service, used in output filenames")
	seconds := flag.Int("seconds", 10, "duration in seconds to sample the CPU profile for")
	outDir := flag.String("out", "./profiles", "directory to write profile files to")
	interval := flag.Duration("interval", 0, "if set, repeatedly collect a profile on this interval instead of once")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("collector: creating output dir: %v", err)
	}

	if *interval <= 0 {
		if err := collectOnce(*target, *service, *seconds, *outDir); err != nil {
			log.Fatalf("collector: %v", err)
		}
		return
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		if err := collectOnce(*target, *service, *seconds, *outDir); err != nil {
			log.Printf("collector: %v", err)
		}
		<-ticker.C
	}
}

func collectOnce(target, service string, seconds int, outDir string) error {
	data, err := fetchCPUProfile(target, seconds)
	if err != nil {
		return fmt.Errorf("Error fetching profile: %w", err)
	}

	path := filepath.Join(outDir, fmt.Sprintf("%s-%d-cpu.pb.gz", service, time.Now().Unix()))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	log.Printf("collector: wrote %s (%d bytes)", path, len(data))
	return nil
}

func fetchCPUProfile(target string, seconds int) ([]byte, error) {
	url := fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", target, seconds)

	client := &http.Client{Timeout: time.Duration(seconds+10) * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, body)
	}

	return io.ReadAll(resp.Body)
}
