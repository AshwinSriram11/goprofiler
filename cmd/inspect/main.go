package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/AshwinSriram11/goprofiler/internal/inspect"
	"github.com/AshwinSriram11/goprofiler/internal/pprofwire"
)

func main() {
	asJSON := flag.Bool("json", false, "print the full report as JSON instead of a text summary")
	top := flag.Int("top", 3, "number of heaviest stacks to print in the text summary")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: inspect [-json] [-top N] <profile.pb.gz>")
		os.Exit(2)
	}
	path := flag.Arg(0)

	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("inspect: %v", err)
	}
	p, err := pprofwire.Parse(data)
	if err != nil {
		log.Fatalf("inspect: %s: %v", path, err)
	}
	report := inspect.Analyze(p, len(data))

	if *asJSON {
		if err := writeJSON(os.Stdout, report); err != nil {
			log.Fatalf("inspect: %v", err)
		}
		return
	}
	writeSummary(os.Stdout, path, p, report, *top)
}

func writeJSON(w io.Writer, r *inspect.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// writeSummary prints the report as text, then the heaviest stacks with their
// frames resolved through the profile's location -> function -> string tables.
func writeSummary(w io.Writer, path string, p *pprofwire.Profile, r *inspect.Report, top int) {
	o, s := r.Overview, r.Sharing
	row := func(label, format string, args ...any) {
		fmt.Fprintf(w, "%-12s %s\n", label, fmt.Sprintf(format, args...))
	}

	row("profile", "%s", path)
	row("size", "%d bytes as read, %d decoded", o.GzipSize, o.RawSize)
	if o.Time != "" {
		row("time", "%s", o.Time)
	}
	colLabel := "none"
	if o.DefaultColumn < len(r.Columns) {
		c := r.Columns[o.DefaultColumn]
		colLabel = c.Type + "/" + c.Unit
		row("type", "%s (default of %d columns)", colLabel, len(r.Columns))
	}
	if o.Validation == "" {
		row("validation", "ok")
	} else {
		row("validation", "PROBLEM: %s", o.Validation)
	}

	fmt.Fprintln(w)
	row("tables", "strings %d, functions %d, locations %d, mappings %d, samples %d",
		len(r.Strings), len(r.Functions), len(r.Locations), len(r.Mappings), len(r.Samples))
	row("stacks", "%d unique, depth avg %.1f, max %d", s.Samples, s.AvgDepth, s.MaxDepth)
	row("sharing", "%d stack slots over %d unique locations (%.2fx reuse)", s.Slots, s.UniqueLocations, s.FrameSharing)
	if a := s.Aggregation; a != nil {
		row("aggregation", "%d %s across %d stacks (%.1fx); %d slots if every event kept its own copy",
			a.Events, colLabel, s.Samples, a.Factor, a.FlatSlots)
	} else {
		row("aggregation", "not applicable: %s is not a count", colLabel)
	}
	row("unresolved", "%d locations without a function", s.UnresolvedLocations)

	fmt.Fprintln(w, "\nwhere the bytes go")
	for _, b := range r.Bytes {
		fmt.Fprintf(w, "  %-14s %6d  %5.1f%%\n", b.Name, b.Bytes, b.Percent)
	}

	if len(r.Mappings) > 0 {
		fmt.Fprintln(w, "\nmappings")
		for _, m := range r.Mappings {
			build := p.Str(m.BuildID)
			if len(build) > 8 {
				build = build[:8]
			}
			fmt.Fprintf(w, "  %d  %s  build=%q flags=%v locations=%d\n", m.ID, p.Str(m.Filename), build, m.Flags, m.Locations)
		}
	}

	if top <= 0 || len(r.Samples) == 0 {
		return
	}
	def := o.DefaultColumn
	order := make([]int, len(r.Samples))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return sampleValue(r.Samples[order[a]], def) > sampleValue(r.Samples[order[b]], def)
	})
	if top > len(order) {
		top = len(order)
	}
	fmt.Fprintf(w, "\ntop %d stacks by %s\n", top, colLabel)
	for rank, i := range order[:top] {
		smp := r.Samples[i]
		fmt.Fprintf(w, "  #%d  %d\n", rank+1, sampleValue(smp, def))
		writeStack(w, p, smp.LocationIDs)
	}
}

func sampleValue(s inspect.SampleRow, col int) int64 {
	if col < len(s.Values) {
		return s.Values[col]
	}
	return 0
}

// writeStack prints one stack leaf first. A location with several lines is an
// address where calls were inlined: every line but the last was inlined into
// the one after it. A location with no lines was never symbolized, so only
// its address is known.
func writeStack(w io.Writer, p *pprofwire.Profile, ids []uint64) {
	for _, id := range ids {
		loc, ok := p.Location(id)
		if !ok {
			fmt.Fprintf(w, "      location %d (not found)\n", id)
			continue
		}
		if len(loc.Lines) == 0 {
			fmt.Fprintf(w, "      %#x  (unresolved)\n", loc.Address)
			continue
		}
		for i, line := range loc.Lines {
			name, file := "?", ""
			if fn, ok := p.Function(line.FunctionID); ok {
				name, file = p.Str(fn.Name), filepath.Base(p.Str(fn.Filename))
			}
			note := ""
			if i < len(loc.Lines)-1 {
				note = "  [inlined]"
			}
			fmt.Fprintf(w, "      %s  %s:%d%s\n", name, file, line.Line, note)
		}
	}
}
