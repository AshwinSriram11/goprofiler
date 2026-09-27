package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Target identifies one service to pull a profile from, as read from a
// targets config file (see targets.json).
type Target struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// loadTargets reads a targets config file (see targets.json) and returns the
// list of services to collect from.
func loadTargets(path string) ([]Target, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var targets []Target
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("%s: no targets defined", path)
	}

	return targets, nil
}

func main() {
	configPath := flag.String("config", "targets.json", "path to a JSON file listing targets to collect from")
	seconds := flag.Int("seconds", 10, "duration in seconds to sample the CPU profile for")
	outDir := flag.String("out", "./profiles", "directory to write profile files to")
	interval := flag.Duration("interval", 0, "if set, repeatedly collect a profile on this interval instead of once")
	flag.Parse()

	targets, err := loadTargets(*configPath)
	if err != nil {
		log.Fatalf("collector: %v", err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("collector: creating output dir: %v", err)
	}

	if *interval <= 0 {
		collectAll(targets, *seconds, *outDir)
		return
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		collectAll(targets, *seconds, *outDir)
		<-ticker.C
	}
}

// collectAll pulls a profile from every target, logging rather than
// aborting on an individual target's failure so one down service doesn't
// stop collection from the rest.
func collectAll(targets []Target, seconds int, outDir string) {
	for _, t := range targets {
		if err := collectOnce(t, seconds, outDir); err != nil {
			log.Printf("collector: %s: %v", t.Name, err)
		}
	}
}

func collectOnce(target Target, seconds int, outDir string) error {
	data, err := fetchCPUProfile(target.URL, seconds)
	if err != nil {
		return fmt.Errorf("fetching profile: %w", err)
	}

	path := filepath.Join(outDir, fmt.Sprintf("%s-%d-cpu.pb.gz", target.Name, time.Now().Unix()))
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
