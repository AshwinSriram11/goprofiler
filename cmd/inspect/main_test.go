package main

import (
	"bytes"
	"encoding/json"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"

	"github.com/AshwinSriram11/goprofiler/internal/inspect"
	"github.com/AshwinSriram11/goprofiler/internal/pprofwire"
)

// leakedGoroutineProfile parks n goroutines on one channel, takes a gzipped
// goroutine profile of this process, then releases them.
func leakedGoroutineProfile(t *testing.T, n int) []byte {
	t.Helper()
	stop := make(chan struct{})
	var started sync.WaitGroup
	started.Add(n)
	for i := 0; i < n; i++ {
		go parkForever(&started, stop)
	}
	started.Wait()
	defer close(stop)

	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 0); err != nil {
		t.Fatalf("writing goroutine profile: %v", err)
	}
	return buf.Bytes()
}

// parkForever is a named function so the test can look for it in the output.
func parkForever(started *sync.WaitGroup, stop <-chan struct{}) {
	started.Done()
	<-stop
}

func analyze(t *testing.T, data []byte) (*pprofwire.Profile, *inspect.Report) {
	t.Helper()
	p, err := pprofwire.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, inspect.Analyze(p, len(data))
}

func TestWriteSummary(t *testing.T) {
	data := leakedGoroutineProfile(t, 40)
	p, r := analyze(t, data)

	var out bytes.Buffer
	writeSummary(&out, "test.pb.gz", p, r, 3)
	got := out.String()

	for _, want := range []string{
		"profile      test.pb.gz",
		"type         goroutine/count",
		"validation   ok",
		"where the bytes go",
		"string_table",
		"top 3 stacks by goroutine/count",
		"parkForever",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}

	// The 40 parked goroutines share one stack, so it must be ranked first
	// with at least that many goroutines.
	_, rest, ok := strings.Cut(got, "#1  ")
	if !ok {
		t.Fatalf("no first-ranked stack in summary:\n%s", got)
	}
	first, _, _ := strings.Cut(rest, "#2  ")
	if !strings.Contains(first, "parkForever") {
		t.Errorf("rank 1 stack is not the parked goroutines:\n%s", first)
	}
}

func TestWriteSummaryTopZeroOmitsStacks(t *testing.T) {
	p, r := analyze(t, leakedGoroutineProfile(t, 3))

	var out bytes.Buffer
	writeSummary(&out, "x", p, r, 0)
	if strings.Contains(out.String(), "top ") {
		t.Errorf("-top 0 should print no stacks:\n%s", out.String())
	}
}

func TestWriteSummaryShowsUnresolvedLocations(t *testing.T) {
	// Take a real profile and strip the lines from one location, as if it had
	// never been symbolized.
	p, _ := analyze(t, leakedGoroutineProfile(t, 3))
	p.Locations[0].Lines = nil
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	r := inspect.Analyze(p, 0)

	var out bytes.Buffer
	writeSummary(&out, "x", p, r, len(r.Samples))
	if !strings.Contains(out.String(), "(unresolved)") {
		t.Errorf("a location with no lines should print as unresolved:\n%s", out.String())
	}
	if r.Sharing.UnresolvedLocations != 1 {
		t.Errorf("UnresolvedLocations = %d, want 1", r.Sharing.UnresolvedLocations)
	}
}

func TestWriteJSON(t *testing.T) {
	_, r := analyze(t, leakedGoroutineProfile(t, 3))

	var out bytes.Buffer
	if err := writeJSON(&out, r); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	var back inspect.Report
	if err := json.Unmarshal(out.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid report JSON: %v", err)
	}
	if len(back.Samples) != len(r.Samples) || back.Overview.RawSize != r.Overview.RawSize {
		t.Errorf("round trip changed the report: %d samples / %d bytes, want %d / %d",
			len(back.Samples), back.Overview.RawSize, len(r.Samples), r.Overview.RawSize)
	}
}
