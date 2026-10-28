package pprofwire

import (
	"bytes"
	"reflect"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
)

// profileWithSample wraps an encoded Sample body in a Profile message: one
// sample (Profile field 2) followed by an empty string_table[0].
func profileWithSample(body []byte) []byte {
	out := []byte{0x12, byte(len(body))}
	out = append(out, body...)
	return append(out, 0x32, 0x00)
}

func TestDecodeSample(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		wantIDs    []uint64
		wantValues []int64
	}{
		{
			name:       "unpacked location ids, packed values",
			body:       []byte{0x08, 0x01, 0x08, 0x02, 0x12, 0x02, 0x05, 0x07},
			wantIDs:    []uint64{1, 2},
			wantValues: []int64{5, 7},
		},
		{
			name:       "packed location ids, unpacked values",
			body:       []byte{0x0a, 0x02, 0x01, 0x02, 0x10, 0x05, 0x10, 0x07},
			wantIDs:    []uint64{1, 2},
			wantValues: []int64{5, 7},
		},
		{
			// The leak sample from a real profile: value 142 needs a
			// two-byte varint (8e 01), ids are packed.
			name:       "multi-byte varint",
			body:       []byte{0x10, 0x8e, 0x01, 0x0a, 0x04, 0x01, 0x02, 0x03, 0x04},
			wantIDs:    []uint64{1, 2, 3, 4},
			wantValues: []int64{142},
		},
		{
			// -1 as an int64 is a ten-byte two's-complement varint.
			name:       "negative value",
			body:       []byte{0x10, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
			wantValues: []int64{-1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Decode(profileWithSample(tt.body))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if len(p.Samples) != 1 {
				t.Fatalf("got %d samples, want 1", len(p.Samples))
			}
			s := p.Samples[0]
			if !reflect.DeepEqual(s.LocationIDs, tt.wantIDs) {
				t.Errorf("LocationIDs = %v, want %v", s.LocationIDs, tt.wantIDs)
			}
			if !reflect.DeepEqual(s.Values, tt.wantValues) {
				t.Errorf("Values = %v, want %v", s.Values, tt.wantValues)
			}
		})
	}
}

func TestDecodeSkipsUnknownFields(t *testing.T) {
	raw := []byte{
		0x98, 0x06, 0x05, // field 99, varint 5
		0xa2, 0x06, 0x01, 0x00, // field 100, bytes (1 byte)
		0x32, 0x00, // string_table[0] = ""
	}
	p, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(p.Strings, []string{""}) {
		t.Errorf("Strings = %q, want [\"\"]", p.Strings)
	}
}

func TestDecodeMalformed(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"length longer than data", []byte{0x12, 0x08, 0x01}},
		{"key with no length", []byte{0x12}},
		{"varint field with no value", []byte{0x48}},
		{"unterminated varint", []byte{0x48, 0xff, 0xff}},
		{"unsupported wire type", []byte{0x0b}},
		{"truncated nested message", []byte{0x12, 0x02, 0x0a, 0x05}},
		{"truncated fixed32", []byte{0x0d, 0x00, 0x00}},
		{"truncated fixed64", []byte{0x09, 0x00, 0x00, 0x00, 0x00}},
		{"string_table sent as varint", []byte{0x30, 0x01}},
		{"time_nanos sent as bytes", []byte{0x4a, 0x00}},
		{"location_ids sent as fixed32", []byte{0x12, 0x05, 0x0d, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(tt.raw); err == nil {
				t.Errorf("Decode(% x) returned no error", tt.raw)
			}
		})
	}
}

// frameNames resolves one sample's stack to function names, leaf first, going
// through the same location -> function -> string lookups the inspector shows.
func frameNames(t *testing.T, p *Profile, s Sample) []string {
	t.Helper()
	var names []string
	for _, id := range s.LocationIDs {
		loc, ok := p.Location(id)
		if !ok {
			t.Fatalf("location %d not found", id)
		}
		for _, line := range loc.Lines {
			fn, ok := p.Function(line.FunctionID)
			if !ok {
				t.Fatalf("function %d not found", line.FunctionID)
			}
			names = append(names, p.Str(fn.Name))
		}
	}
	return names
}

// goroutineProfile parks n goroutines on a channel, takes a gzipped goroutine
// profile from this very process, then releases them.
func goroutineProfile(t *testing.T, n int) []byte {
	t.Helper()
	stop := make(chan struct{})
	var started sync.WaitGroup
	started.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			started.Done()
			<-stop
		}()
	}
	started.Wait()
	defer close(stop)

	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 0); err != nil {
		t.Fatalf("writing goroutine profile: %v", err)
	}
	return buf.Bytes()
}

func TestParseRealGoroutineProfile(t *testing.T) {
	const n = 50
	data := goroutineProfile(t, n)

	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if len(p.SampleTypes) != 1 || p.Str(p.SampleTypes[0].Type) != "goroutine" || p.Str(p.SampleTypes[0].Unit) != "count" {
		t.Fatalf("SampleTypes = %+v, want one goroutine/count", p.SampleTypes)
	}

	// The n goroutines share one stack, so the profile aggregates them into
	// a single sample whose value is the count.
	var matching, total int64
	for _, s := range p.Samples {
		for _, name := range frameNames(t, p, s) {
			if strings.HasSuffix(name, "goroutineProfile.func1") {
				matching++
				total += s.Values[0]
				break
			}
		}
	}
	if matching != 1 {
		t.Errorf("got %d samples containing the test goroutines, want 1 aggregated sample", matching)
	}
	if total < n {
		t.Errorf("aggregated value = %d, want at least %d", total, n)
	}
}

func TestParseGzippedMatchesRaw(t *testing.T) {
	data := goroutineProfile(t, 5)

	raw, err := Decompress(data)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if len(raw) == len(data) {
		t.Fatal("Decompress returned the input unchanged for gzipped data")
	}

	again, err := Decompress(raw)
	if err != nil || !bytes.Equal(again, raw) {
		t.Errorf("Decompress on non-gzip data should return it unchanged (err=%v)", err)
	}

	a, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse gzipped: %v", err)
	}
	b, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode raw: %v", err)
	}
	if !reflect.DeepEqual(a.Strings, b.Strings) || len(a.Samples) != len(b.Samples) || len(a.Locations) != len(b.Locations) {
		t.Error("gzipped and raw decodes disagree")
	}

	// RawSize is the decoded size even when the input was gzipped, and the
	// per-field byte counts add up to it.
	sum := 0
	for _, n := range a.FieldBytes {
		sum += n
	}
	if a.RawSize != len(raw) || sum != len(raw) {
		t.Errorf("RawSize = %d, sum(FieldBytes) = %d, want both %d", a.RawSize, sum, len(raw))
	}
}

func TestValidate(t *testing.T) {
	good := func() *Profile {
		return &Profile{
			Strings:     []string{"", "goroutine", "count", "f", "f.go"},
			SampleTypes: []ValueType{{Type: 1, Unit: 2}},
			PeriodType:  ValueType{Type: 1, Unit: 2},
			Functions:   []Function{{ID: 1, Name: 3, SystemName: 3, Filename: 4}},
			Locations:   []Location{{ID: 1, Lines: []Line{{FunctionID: 1, Line: 7}}}},
			Samples:     []Sample{{LocationIDs: []uint64{1}, Values: []int64{1}}},
		}
	}

	if err := good().Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Profile)
	}{
		{"string_table[0] not empty", func(p *Profile) { p.Strings[0] = "x" }},
		{"function name out of range", func(p *Profile) { p.Functions[0].Name = 99 }},
		{"location refers to missing function", func(p *Profile) { p.Locations[0].Lines[0].FunctionID = 9 }},
		{"location refers to missing mapping", func(p *Profile) { p.Locations[0].MappingID = 9 }},
		{"sample refers to missing location", func(p *Profile) { p.Samples[0].LocationIDs = []uint64{9} }},
		{"sample value count mismatch", func(p *Profile) { p.Samples[0].Values = []int64{1, 2} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := good()
			tt.mutate(p)
			if err := p.Validate(); err == nil {
				t.Error("Validate returned no error")
			}
		})
	}
}
