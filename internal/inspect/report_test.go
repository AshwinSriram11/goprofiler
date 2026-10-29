package inspect

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"

	"github.com/AshwinSriram11/goprofiler/internal/pprofwire"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// toyProfile is the three-goroutine example: two goroutines leaked in
// leak.func1 (stack A) and one sleeping in worker (stack B). Both stacks start
// at gopark, so location 1 is shared.
//
//	A x2: gopark <- chanrecv <- chanrecv1 <- leak.func1   (locations 1,2,3,4)
//	B x1: gopark <- time.Sleep <- worker                  (locations 1,5,6)
func toyProfile() *pprofwire.Profile {
	return &pprofwire.Profile{
		Strings: []string{
			"", "goroutine", "count", "runtime.gopark", "runtime.chanrecv", "runtime.chanrecv1",
			"main.leak.func1", "time.Sleep", "main.worker", "proc.go", "chan.go", "main.go", "time.go",
			"/bin/app", "abc123",
		},
		SampleTypes: []pprofwire.ValueType{{Type: 1, Unit: 2}},
		PeriodType:  pprofwire.ValueType{Type: 1, Unit: 2},
		Period:      1,
		TimeNanos:   1792924549732552375,
		Functions: []pprofwire.Function{
			{ID: 1, Name: 3, SystemName: 3, Filename: 9},
			{ID: 2, Name: 4, SystemName: 4, Filename: 10},
			{ID: 3, Name: 5, SystemName: 5, Filename: 10},
			{ID: 4, Name: 6, SystemName: 6, Filename: 11},
			{ID: 5, Name: 7, SystemName: 7, Filename: 12},
			{ID: 6, Name: 8, SystemName: 8, Filename: 11},
		},
		Locations: []pprofwire.Location{
			{ID: 1, MappingID: 1, Address: 0x43a1c5, Lines: []pprofwire.Line{{FunctionID: 1, Line: 474}}},
			{ID: 2, MappingID: 1, Address: 0x4086f8, Lines: []pprofwire.Line{{FunctionID: 2, Line: 667}}},
			{ID: 3, MappingID: 1, Address: 0x40829c, Lines: []pprofwire.Line{{FunctionID: 3, Line: 509}}},
			{ID: 4, MappingID: 1, Address: 0x4b3a1e, Lines: []pprofwire.Line{{FunctionID: 4, Line: 69}}},
			{ID: 5, MappingID: 1, Address: 0x4421c8, Lines: []pprofwire.Line{{FunctionID: 5, Line: 368}}},
			{ID: 6, MappingID: 1, Address: 0x4b3b05, Lines: []pprofwire.Line{{FunctionID: 6, Line: 32}}},
		},
		Mappings: []pprofwire.Mapping{
			{ID: 1, MemoryStart: 0x400000, MemoryLimit: 0x6ea000, Filename: 13, BuildID: 14, HasFunctions: true},
		},
		Samples: []pprofwire.Sample{
			{LocationIDs: []uint64{1, 2, 3, 4}, Values: []int64{2}},
			{LocationIDs: []uint64{1, 5, 6}, Values: []int64{1}},
		},
		RawSize:    200,
		FieldBytes: map[int]int{6: 120, 4: 50, 2: 30},
	}
}

func TestAnalyzeToyProfile(t *testing.T) {
	r := Analyze(toyProfile(), 90)

	o := r.Overview
	if o.RawSize != 200 || o.GzipSize != 90 || o.Validation != "" || o.PeriodType != "goroutine/count" || o.DefaultColumn != 0 {
		t.Errorf("Overview = %+v", o)
	}
	if o.Time != "2026-10-25T10:35:49.732552375Z" {
		t.Errorf("Time = %q, want the UTC RFC 3339 form", o.Time)
	}

	if want := []Column{{Type: "goroutine", Unit: "count", Total: 3}}; !reflect.DeepEqual(r.Columns, want) {
		t.Errorf("Columns = %+v, want %+v", r.Columns, want)
	}

	s := r.Sharing
	if s.Samples != 2 || s.Slots != 7 || s.UniqueLocations != 6 || s.MaxDepth != 4 || s.UnresolvedLocations != 0 {
		t.Errorf("Sharing counts = %+v", s)
	}
	if !approx(s.FrameSharing, 7.0/6.0) || !approx(s.AvgDepth, 3.5) {
		t.Errorf("FrameSharing = %v, AvgDepth = %v, want 7/6 and 3.5", s.FrameSharing, s.AvgDepth)
	}
	// 3 goroutines in 2 stacks; flat, A would take 2*4 slots and B 1*3.
	if a := s.Aggregation; a == nil || a.Events != 3 || !approx(a.Factor, 1.5) || a.FlatSlots != 11 {
		t.Errorf("Aggregation = %+v, want events 3, factor 1.5, flat slots 11", a)
	}

	wantBytes := []ByteShare{
		{Name: "string_table", Field: 6, Bytes: 120, Percent: 60},
		{Name: "location", Field: 4, Bytes: 50, Percent: 25},
		{Name: "sample", Field: 2, Bytes: 30, Percent: 15},
	}
	if !reflect.DeepEqual(r.Bytes, wantBytes) {
		t.Errorf("Bytes = %+v, want %+v", r.Bytes, wantBytes)
	}

	if len(r.Samples) != 2 || r.Samples[0].Depth != 4 || r.Samples[0].Values[0] != 2 || r.Samples[1].Depth != 3 {
		t.Errorf("Samples = %+v", r.Samples)
	}

	// Location usage: gopark is in both stacks; chanrecv only in A (2 goroutines);
	// time.Sleep only in B (1 goroutine).
	locs := map[uint64]LocationRow{}
	for _, l := range r.Locations {
		locs[l.ID] = l
	}
	for _, c := range []struct {
		id       uint64
		used     int
		weighted int64
	}{{1, 2, 3}, {2, 1, 2}, {5, 1, 1}} {
		if l := locs[c.id]; l.UsedBy != c.used || !reflect.DeepEqual(l.Weighted, []int64{c.weighted}) {
			t.Errorf("location %d: UsedBy = %d, Weighted = %v, want %d and [%d]", c.id, l.UsedBy, l.Weighted, c.used, c.weighted)
		}
	}
	if locs[1].Address != "0x43a1c5" {
		t.Errorf("Address = %q, want hex string", locs[1].Address)
	}

	for _, f := range r.Functions {
		if f.UsedBy != 1 {
			t.Errorf("function %d UsedBy = %d, want 1", f.ID, f.UsedBy)
		}
	}

	// Strings: value types are referenced by the sample type and the period
	// type; a function's name and system name are one reference; chan.go and
	// main.go are each shared by two functions.
	wantUsed := map[int]int{
		0: 0, 1: 2, 2: 2, 3: 1, 4: 1, 5: 1, 6: 1, 7: 1, 8: 1, 9: 1, 10: 2, 11: 2, 12: 1, 13: 1, 14: 1,
	}
	for _, sr := range r.Strings {
		if sr.UsedBy != wantUsed[sr.Index] {
			t.Errorf("strings[%d] %q UsedBy = %d, want %d", sr.Index, sr.Text, sr.UsedBy, wantUsed[sr.Index])
		}
	}

	if len(r.Mappings) != 1 {
		t.Fatalf("got %d mappings", len(r.Mappings))
	}
	m := r.Mappings[0]
	if m.Locations != 6 || m.Start != "0x400000" || !reflect.DeepEqual(m.Flags, []string{"has_functions"}) {
		t.Errorf("Mapping = %+v", m)
	}

	// The page iterates these, so empty tables and slices must encode as
	// [], never null.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Contains(b, []byte("null")) {
		t.Errorf("JSON contains null: %s", b)
	}
}

func TestAnalyzeMultiColumnHasNoAggregation(t *testing.T) {
	p := &pprofwire.Profile{
		Strings:           []string{"", "alloc_space", "bytes", "inuse_space", "f", "f.go"},
		SampleTypes:       []pprofwire.ValueType{{Type: 1, Unit: 2}, {Type: 3, Unit: 2}},
		DefaultSampleType: 3,
		Functions:         []pprofwire.Function{{ID: 1, Name: 4, SystemName: 4, Filename: 5}},
		Locations:         []pprofwire.Location{{ID: 1, Lines: []pprofwire.Line{{FunctionID: 1, Line: 1}}}},
		Samples: []pprofwire.Sample{
			{LocationIDs: []uint64{1}, Values: []int64{100, 40}},
			{LocationIDs: []uint64{1}, Values: []int64{10, 5}},
		},
	}
	r := Analyze(p, 0)

	if r.Overview.Validation != "" {
		t.Fatalf("Validation = %q", r.Overview.Validation)
	}
	if r.Overview.DefaultColumn != 1 {
		t.Errorf("DefaultColumn = %d, want 1 (inuse_space)", r.Overview.DefaultColumn)
	}
	if r.Sharing.Aggregation != nil {
		t.Errorf("Aggregation = %+v, want nil for a bytes column", r.Sharing.Aggregation)
	}
	if r.Columns[0].Total != 110 || r.Columns[1].Total != 45 {
		t.Errorf("Columns = %+v", r.Columns)
	}
	if l := r.Locations[0]; l.UsedBy != 2 || !reflect.DeepEqual(l.Weighted, []int64{110, 45}) {
		t.Errorf("location: UsedBy = %d, Weighted = %v, want 2 and [110 45]", l.UsedBy, l.Weighted)
	}
}

func TestAnalyzeFlagsIDsAboveJSONPrecision(t *testing.T) {
	p := toyProfile()
	p.Locations[0].ID = 1<<53 + 1
	p.Samples[0].LocationIDs[0] = 1<<53 + 1
	p.Samples[1].LocationIDs[0] = 1<<53 + 1

	r := Analyze(p, 0)
	if !strings.Contains(r.Overview.Validation, "2^53") {
		t.Errorf("Validation = %q, want a note about 2^53", r.Overview.Validation)
	}
}

func TestAnalyzeInconsistentProfileDoesNotPanic(t *testing.T) {
	p := &pprofwire.Profile{
		SampleTypes: []pprofwire.ValueType{{Type: 1, Unit: 2}},
		Samples:     []pprofwire.Sample{{LocationIDs: []uint64{9}}}, // no values, missing location, no strings
	}
	r := Analyze(p, 0)
	if r.Overview.Validation == "" {
		t.Error("Validation is empty for an inconsistent profile")
	}
	if len(r.Samples) != 1 || r.Samples[0].Depth != 1 {
		t.Errorf("Samples = %+v", r.Samples)
	}
}

func TestAnalyzeRealGoroutineProfile(t *testing.T) {
	stop := make(chan struct{})
	var started sync.WaitGroup
	started.Add(30)
	for i := 0; i < 30; i++ {
		go func() {
			started.Done()
			<-stop
		}()
	}
	started.Wait()
	var buf bytes.Buffer
	err := pprof.Lookup("goroutine").WriteTo(&buf, 0)
	close(stop)
	if err != nil {
		t.Fatalf("writing goroutine profile: %v", err)
	}

	p, err := pprofwire.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := Analyze(p, buf.Len())

	if r.Overview.Validation != "" {
		t.Fatalf("Validation = %q", r.Overview.Validation)
	}
	if r.Overview.GzipSize != buf.Len() || r.Overview.Time == "" {
		t.Errorf("Overview = %+v", r.Overview)
	}

	// Structural invariants that hold for any valid profile.
	slots := 0
	for _, l := range r.Locations {
		slots += l.UsedBy
	}
	if slots != r.Sharing.Slots {
		t.Errorf("sum of location UsedBy = %d, want Slots = %d", slots, r.Sharing.Slots)
	}

	lines, fnRefs := 0, 0
	for _, l := range r.Locations {
		lines += len(l.Lines)
	}
	for _, f := range r.Functions {
		fnRefs += f.UsedBy
	}
	if lines != fnRefs {
		t.Errorf("sum of function UsedBy = %d, want total lines = %d", fnRefs, lines)
	}

	bytesSum, pct := 0, 0.0
	for _, b := range r.Bytes {
		bytesSum += b.Bytes
		pct += b.Percent
	}
	if bytesSum != r.Overview.RawSize || math.Abs(pct-100) > 1e-6 {
		t.Errorf("byte shares sum to %d bytes / %.6f%%, want %d / 100%%", bytesSum, pct, r.Overview.RawSize)
	}

	a := r.Sharing.Aggregation
	if a == nil || a.Events != r.Columns[0].Total || a.Factor < 1 || a.FlatSlots < int64(r.Sharing.Slots) {
		t.Errorf("Aggregation = %+v for Slots %d, total %d", a, r.Sharing.Slots, r.Columns[0].Total)
	}

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if bytes.Contains(b, []byte("null")) {
		t.Errorf("JSON contains null")
	}
}
