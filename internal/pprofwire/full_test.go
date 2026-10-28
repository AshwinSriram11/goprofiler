package pprofwire

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// Tiny protobuf encoder for building test profiles by hand. Field numbers are
// written out explicitly at each call site, straight from profile.proto, so
// a wrong number in the decoder fails the test instead of being mirrored here.

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func varint(v uint64) []byte { return binary.AppendUvarint(nil, v) }

func key(num, wire int) []byte { return varint(uint64(num<<3 | wire)) }

func fVarint(num int, v uint64) []byte { return cat(key(num, wireVarint), varint(v)) }

func fBytes(num int, b []byte) []byte {
	return cat(key(num, wireBytes), varint(uint64(len(b))), b)
}

func fFixed32(num int, v uint32) []byte {
	return cat(key(num, wireFixed32), binary.LittleEndian.AppendUint32(nil, v))
}

func fFixed64(num int, v uint64) []byte {
	return cat(key(num, wireFixed64), binary.LittleEndian.AppendUint64(nil, v))
}

func TestDecodeFullProfile(t *testing.T) {
	strs := []string{"", "goroutine", "count", "runtime.gopark", "proc.go", "main.main", "/bin/app", "abc123"}
	var stringTable []byte
	for _, s := range strs {
		stringTable = append(stringTable, fBytes(6, []byte(s))...)
	}

	valueType := cat(fVarint(1, 1), fVarint(2, 2)) // goroutine/count
	label := cat(fVarint(1, 2), fVarint(2, 3), fVarint(3, 5), fVarint(4, 2))
	sample := cat(
		fBytes(1, cat(varint(1), varint(2))), // location_id, packed
		fVarint(2, 3),                        // value, unpacked
		fBytes(3, label),
	)
	mapping := cat(
		fVarint(1, 1), fVarint(2, 0x400000), fVarint(3, 0x6ea000), fVarint(4, 0x1000),
		fVarint(5, 6), fVarint(6, 7),
		fVarint(7, 1), fVarint(8, 1), fVarint(9, 1), fVarint(10, 1),
	)
	// Two Lines in one Location is how inlined calls are stored.
	location := cat(
		fVarint(1, 1), fVarint(2, 1), fVarint(3, 0x48c1e9),
		fBytes(4, cat(fVarint(1, 1), fVarint(2, 474))),
		fBytes(4, cat(fVarint(1, 2), fVarint(2, 22))),
		fVarint(5, 1),
	)
	location2 := cat(
		fVarint(1, 2), fVarint(2, 1), fVarint(3, 0x48c200),
		fBytes(4, cat(fVarint(1, 2), fVarint(2, 30))),
	)
	function1 := cat(fVarint(1, 1), fVarint(2, 3), fVarint(3, 3), fVarint(4, 4), fVarint(5, 457))
	function2 := cat(fVarint(1, 2), fVarint(2, 5), fVarint(3, 5), fVarint(4, 4), fVarint(5, 20))

	raw := cat(
		// Deliberately not in proto field order, like a real Go profile.
		fBytes(11, valueType),
		fVarint(12, 1),
		fBytes(1, valueType),
		fBytes(4, location),
		fBytes(4, location2),
		fBytes(5, function1),
		fBytes(5, function2),
		fBytes(2, sample),
		fVarint(9, 1792924549732552375),
		fVarint(10, 3_000_000_000),
		fVarint(7, 4),
		fVarint(8, 5),
		fBytes(13, cat(varint(7), varint(3))), // comment, packed
		fVarint(13, 4),                        // comment, unpacked
		fVarint(14, 1),
		fFixed32(98, 0xdeadbeef), // unknown fields of the other wire types
		fFixed64(99, 0x0123456789abcdef),
		fBytes(3, mapping),
		stringTable,
	)

	p, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	checks := []struct {
		name      string
		got, want any
	}{
		{"SampleTypes", p.SampleTypes, []ValueType{{Type: 1, Unit: 2}}},
		{"PeriodType", p.PeriodType, ValueType{Type: 1, Unit: 2}},
		{"Period", p.Period, int64(1)},
		{"Samples", p.Samples, []Sample{{
			LocationIDs: []uint64{1, 2},
			Values:      []int64{3},
			Labels:      []Label{{Key: 2, Str: 3, Num: 5, NumUnit: 2}},
		}}},
		{"Mappings", p.Mappings, []Mapping{{
			ID: 1, MemoryStart: 0x400000, MemoryLimit: 0x6ea000, FileOffset: 0x1000,
			Filename: 6, BuildID: 7,
			HasFunctions: true, HasFilenames: true, HasLineNumbers: true, HasInlineFrames: true,
		}}},
		{"Locations", p.Locations, []Location{
			{
				ID: 1, MappingID: 1, Address: 0x48c1e9,
				Lines:    []Line{{FunctionID: 1, Line: 474}, {FunctionID: 2, Line: 22}},
				IsFolded: true,
			},
			{
				ID: 2, MappingID: 1, Address: 0x48c200,
				Lines: []Line{{FunctionID: 2, Line: 30}},
			},
		}},
		{"Functions", p.Functions, []Function{
			{ID: 1, Name: 3, SystemName: 3, Filename: 4, StartLine: 457},
			{ID: 2, Name: 5, SystemName: 5, Filename: 4, StartLine: 20},
		}},
		{"Strings", p.Strings, strs},
		{"DropFrames", p.DropFrames, int64(4)},
		{"KeepFrames", p.KeepFrames, int64(5)},
		{"TimeNanos", p.TimeNanos, int64(1792924549732552375)},
		{"DurationNanos", p.DurationNanos, int64(3_000_000_000)},
		{"Comments", p.Comments, []int64{7, 3, 4}},
		{"DefaultSampleType", p.DefaultSampleType, int64(1)},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}

	if err := p.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestLookups(t *testing.T) {
	p := &Profile{
		Strings:   []string{"", "a", "b"},
		Mappings:  []Mapping{{ID: 1}},
		Locations: []Location{{ID: 7}},
		Functions: []Function{{ID: 3}},
	}
	p.index()

	if l, ok := p.Location(7); !ok || l.ID != 7 {
		t.Errorf("Location(7) = %v, %v", l, ok)
	}
	if f, ok := p.Function(3); !ok || f.ID != 3 {
		t.Errorf("Function(3) = %v, %v", f, ok)
	}
	if m, ok := p.Mapping(1); !ok || m.ID != 1 {
		t.Errorf("Mapping(1) = %v, %v", m, ok)
	}
	if _, ok := p.Location(1); ok {
		t.Error("Location(1) found, want not found (IDs are not positions)")
	}
	if _, ok := p.Function(9); ok {
		t.Error("Function(9) found, want not found")
	}
	if _, ok := p.Mapping(9); ok {
		t.Error("Mapping(9) found, want not found")
	}

	if got := p.Str(2); got != "b" {
		t.Errorf("Str(2) = %q, want \"b\"", got)
	}
	for _, i := range []int64{-1, 3, 99} {
		if got := p.Str(i); got != "" {
			t.Errorf("Str(%d) = %q, want \"\" for out-of-range index", i, got)
		}
	}
}
