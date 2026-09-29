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
	"strings"
	"time"
)

// Target identifies one service to pull a profile from, as read from a
// targets config file (see targets.json).
type Target struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// profileKind describes one pprof endpoint the collector knows how to pull.
// CPU profiles are sampled over a window (needsDuration), so they need a
// ?seconds= param and a longer client timeout; heap and other snapshot
// profiles are read instantly.
type profileKind struct {
	name          string
	path          string
	needsDuration bool
}

var knownProfiles = map[string]profileKind{
	"cpu":  {name: "cpu", path: "/debug/pprof/profile", needsDuration: true},
	"heap": {name: "heap", path: "/debug/pprof/heap", needsDuration: false},
}

// parseProfiles turns a comma-separated flag value like "cpu,heap" into the
// profile kinds to collect, in the order given.
func parseProfiles(csv string) ([]profileKind, error) {
	names := strings.Split(csv, ",")
	kinds := make([]profileKind, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		kind, ok := knownProfiles[n]
		if !ok {
			return nil, fmt.Errorf("unknown profile type %q (known: cpu, heap)", n)
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
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
	profilesFlag := flag.String("profiles", "cpu", "comma-separated list of profile types to collect (cpu, heap)")
	seconds := flag.Int("seconds", 10, "duration in seconds to sample the CPU profile for")
	outDir := flag.String("out", "./profiles", "directory to write profile files to")
	interval := flag.Duration("interval", 0, "if set, repeatedly collect a profile on this interval instead of once")
	flag.Parse()

	targets, err := loadTargets(*configPath)
	if err != nil {
		log.Fatalf("collector: %v", err)
	}

	profiles, err := parseProfiles(*profilesFlag)
	if err != nil {
		log.Fatalf("collector: %v", err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("collector: creating output dir: %v", err)
	}

	if *interval <= 0 {
		collectAll(targets, profiles, *seconds, *outDir)
		return
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		collectAll(targets, profiles, *seconds, *outDir)
		<-ticker.C
	}
}

// collectAll pulls every requested profile kind from every target, logging
// rather than aborting on an individual failure so one down service or one
// unavailable profile type doesn't stop collection from the rest.
func collectAll(targets []Target, profiles []profileKind, seconds int, outDir string) {
	for _, t := range targets {
		for _, p := range profiles {
			if err := collectOnce(t, p, seconds, outDir); err != nil {
				log.Printf("collector: %s: %s: %v", t.Name, p.name, err)
			}
		}
	}
}

func collectOnce(target Target, kind profileKind, seconds int, outDir string) error {
	data, err := fetchProfile(target.URL, kind, seconds)
	if err != nil {
		return fmt.Errorf("fetching %s profile: %w", kind.name, err)
	}

	path := filepath.Join(outDir, fmt.Sprintf("%s-%d-%s.pb.gz", target.Name, time.Now().Unix(), kind.name))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	log.Printf("collector: wrote %s (%d bytes)", path, len(data))
	return nil
}

func fetchProfile(target string, kind profileKind, seconds int) ([]byte, error) {
	url := target + kind.path
	timeout := 10 * time.Second
	if kind.needsDuration {
		url = fmt.Sprintf("%s?seconds=%d", url, seconds)
		timeout = time.Duration(seconds+10) * time.Second
	}

	client := &http.Client{Timeout: timeout}
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
