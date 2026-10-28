// Package pprofwire decodes the protobuf wire format of pprof's profile.proto
// by hand, keeping the profile's tables (strings, functions, locations,
// mappings) exactly as they are stored on the wire.
//
// Libraries such as github.com/google/pprof/profile resolve the IDs and
// string indexes into pointers and strings on load. This package deliberately
// doesn't, so the structure of a profile, and how much sharing it contains,
// stays visible.
package pprofwire

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
)

// ValueType describes one column of sample values, e.g. goroutine/count.
// Both fields are indexes into Profile.Strings.
type ValueType struct {
	Type int64
	Unit int64
}

// Label is a key/value tag attached to a sample. Key, Str and NumUnit are
// string-table indexes; Num is a plain number.
type Label struct {
	Key     int64
	Str     int64
	Num     int64
	NumUnit int64
}

// Sample is one unique stack and its values. LocationIDs are leaf first;
// Values has one entry per Profile.SampleTypes.
type Sample struct {
	LocationIDs []uint64
	Values      []int64
	Labels      []Label
}

// Mapping is a loaded binary or library. Filename and BuildID are
// string-table indexes.
type Mapping struct {
	ID              uint64
	MemoryStart     uint64
	MemoryLimit     uint64
	FileOffset      uint64
	Filename        int64
	BuildID         int64
	HasFunctions    bool
	HasFilenames    bool
	HasLineNumbers  bool
	HasInlineFrames bool
}

// Line is one function at one source line. A Location with several Lines is
// an address where the compiler inlined calls; the last Line is the outermost
// caller.
type Line struct {
	FunctionID uint64
	Line       int64
}

// Location is one code address, as seen at a call site in a stack.
type Location struct {
	ID        uint64
	MappingID uint64
	Address   uint64
	Lines     []Line
	IsFolded  bool
}

// Function is a named function. Name, SystemName and Filename are
// string-table indexes.
type Function struct {
	ID         uint64
	Name       int64
	SystemName int64
	Filename   int64
	StartLine  int64
}

// Profile is a decoded profile with every table still in its stored form.
type Profile struct {
	SampleTypes       []ValueType
	Samples           []Sample
	Mappings          []Mapping
	Locations         []Location
	Functions         []Function
	Strings           []string
	DropFrames        int64
	KeepFrames        int64
	TimeNanos         int64
	DurationNanos     int64
	PeriodType        ValueType
	Period            int64
	Comments          []int64 // string-table indexes
	DefaultSampleType int64   // string-table index

	// RawSize is the decoded (uncompressed) size in bytes. FieldBytes maps a
	// top-level field number (e.g. 6 = string_table) to the total wire bytes
	// all its occurrences took, key and length prefix included, so the values
	// add up to RawSize. Both are only set by Decode.
	RawSize    int
	FieldBytes map[int]int

	locByID  map[uint64]*Location
	funcByID map[uint64]*Function
	mapByID  map[uint64]*Mapping
}

// Decompress gunzips data if it starts with the gzip magic bytes, and returns
// it unchanged otherwise. Go serves profiles gzipped.
func Decompress(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	return raw, nil
}

// Parse decompresses data if needed and decodes it.
func Parse(data []byte) (*Profile, error) {
	raw, err := Decompress(data)
	if err != nil {
		return nil, err
	}
	return Decode(raw)
}

// Decode decodes an uncompressed profile.proto message. Fields can arrive in
// any order (Go writes the string table last), so nothing is resolved until
// every table has been read. Unknown fields are skipped.
func Decode(raw []byte) (*Profile, error) {
	p := &Profile{RawSize: len(raw), FieldBytes: make(map[int]int)}
	err := eachField(raw, func(f field) error {
		p.FieldBytes[f.num] += f.size
		switch f.num {
		case 1, 2, 3, 4, 5, 11:
			b, err := f.bytes()
			if err != nil {
				return err
			}
			return p.decodeMessage(f.num, b)
		case 6:
			b, err := f.bytes()
			if err != nil {
				return err
			}
			p.Strings = append(p.Strings, string(b))
		case 7:
			return setInt(&p.DropFrames, f)
		case 8:
			return setInt(&p.KeepFrames, f)
		case 9:
			return setInt(&p.TimeNanos, f)
		case 10:
			return setInt(&p.DurationNanos, f)
		case 12:
			return setInt(&p.Period, f)
		case 13:
			vs, err := appendVarints(nil, f)
			if err != nil {
				return err
			}
			for _, v := range vs {
				p.Comments = append(p.Comments, int64(v))
			}
		case 14:
			return setInt(&p.DefaultSampleType, f)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("decoding profile: %w", err)
	}
	p.index()
	return p, nil
}

// decodeMessage decodes one length-delimited sub-message of Profile.
func (p *Profile) decodeMessage(num int, b []byte) error {
	switch num {
	case 1:
		vt, err := decodeValueType(b)
		if err != nil {
			return fmt.Errorf("sample_type: %w", err)
		}
		p.SampleTypes = append(p.SampleTypes, vt)
	case 2:
		s, err := decodeSample(b)
		if err != nil {
			return fmt.Errorf("sample: %w", err)
		}
		p.Samples = append(p.Samples, s)
	case 3:
		m, err := decodeMapping(b)
		if err != nil {
			return fmt.Errorf("mapping: %w", err)
		}
		p.Mappings = append(p.Mappings, m)
	case 4:
		l, err := decodeLocation(b)
		if err != nil {
			return fmt.Errorf("location: %w", err)
		}
		p.Locations = append(p.Locations, l)
	case 5:
		fn, err := decodeFunction(b)
		if err != nil {
			return fmt.Errorf("function: %w", err)
		}
		p.Functions = append(p.Functions, fn)
	case 11:
		vt, err := decodeValueType(b)
		if err != nil {
			return fmt.Errorf("period_type: %w", err)
		}
		p.PeriodType = vt
	}
	return nil
}

func decodeValueType(b []byte) (ValueType, error) {
	var vt ValueType
	err := eachField(b, func(f field) error {
		switch f.num {
		case 1:
			return setInt(&vt.Type, f)
		case 2:
			return setInt(&vt.Unit, f)
		}
		return nil
	})
	return vt, err
}

func decodeSample(b []byte) (Sample, error) {
	var s Sample
	err := eachField(b, func(f field) error {
		switch f.num {
		case 1:
			ids, err := appendVarints(s.LocationIDs, f)
			if err != nil {
				return err
			}
			s.LocationIDs = ids
		case 2:
			vs, err := appendVarints(nil, f)
			if err != nil {
				return err
			}
			for _, v := range vs {
				s.Values = append(s.Values, int64(v))
			}
		case 3:
			lb, err := f.bytes()
			if err != nil {
				return err
			}
			l, err := decodeLabel(lb)
			if err != nil {
				return fmt.Errorf("label: %w", err)
			}
			s.Labels = append(s.Labels, l)
		}
		return nil
	})
	return s, err
}

func decodeLabel(b []byte) (Label, error) {
	var l Label
	err := eachField(b, func(f field) error {
		switch f.num {
		case 1:
			return setInt(&l.Key, f)
		case 2:
			return setInt(&l.Str, f)
		case 3:
			return setInt(&l.Num, f)
		case 4:
			return setInt(&l.NumUnit, f)
		}
		return nil
	})
	return l, err
}

func decodeMapping(b []byte) (Mapping, error) {
	var m Mapping
	err := eachField(b, func(f field) error {
		switch f.num {
		case 1:
			return setUint(&m.ID, f)
		case 2:
			return setUint(&m.MemoryStart, f)
		case 3:
			return setUint(&m.MemoryLimit, f)
		case 4:
			return setUint(&m.FileOffset, f)
		case 5:
			return setInt(&m.Filename, f)
		case 6:
			return setInt(&m.BuildID, f)
		case 7:
			return setBool(&m.HasFunctions, f)
		case 8:
			return setBool(&m.HasFilenames, f)
		case 9:
			return setBool(&m.HasLineNumbers, f)
		case 10:
			return setBool(&m.HasInlineFrames, f)
		}
		return nil
	})
	return m, err
}

func decodeLocation(b []byte) (Location, error) {
	var l Location
	err := eachField(b, func(f field) error {
		switch f.num {
		case 1:
			return setUint(&l.ID, f)
		case 2:
			return setUint(&l.MappingID, f)
		case 3:
			return setUint(&l.Address, f)
		case 4:
			lb, err := f.bytes()
			if err != nil {
				return err
			}
			line, err := decodeLine(lb)
			if err != nil {
				return fmt.Errorf("line: %w", err)
			}
			l.Lines = append(l.Lines, line)
		case 5:
			return setBool(&l.IsFolded, f)
		}
		return nil
	})
	return l, err
}

func decodeLine(b []byte) (Line, error) {
	var l Line
	err := eachField(b, func(f field) error {
		switch f.num {
		case 1:
			return setUint(&l.FunctionID, f)
		case 2:
			return setInt(&l.Line, f)
		}
		return nil
	})
	return l, err
}

func decodeFunction(b []byte) (Function, error) {
	var fn Function
	err := eachField(b, func(f field) error {
		switch f.num {
		case 1:
			return setUint(&fn.ID, f)
		case 2:
			return setInt(&fn.Name, f)
		case 3:
			return setInt(&fn.SystemName, f)
		case 4:
			return setInt(&fn.Filename, f)
		case 5:
			return setInt(&fn.StartLine, f)
		}
		return nil
	})
	return fn, err
}

// index (re)builds the ID lookup maps. The pointers refer into the table
// slices, so they stay valid as long as those slices aren't appended to.
func (p *Profile) index() {
	p.locByID = make(map[uint64]*Location, len(p.Locations))
	for i := range p.Locations {
		p.locByID[p.Locations[i].ID] = &p.Locations[i]
	}
	p.funcByID = make(map[uint64]*Function, len(p.Functions))
	for i := range p.Functions {
		p.funcByID[p.Functions[i].ID] = &p.Functions[i]
	}
	p.mapByID = make(map[uint64]*Mapping, len(p.Mappings))
	for i := range p.Mappings {
		p.mapByID[p.Mappings[i].ID] = &p.Mappings[i]
	}
}

// Str returns the string at table index i, or "" if i is out of range.
func (p *Profile) Str(i int64) string {
	if i < 0 || i >= int64(len(p.Strings)) {
		return ""
	}
	return p.Strings[i]
}

// Location returns the location with the given ID.
func (p *Profile) Location(id uint64) (*Location, bool) {
	l, ok := p.locByID[id]
	return l, ok
}

// Function returns the function with the given ID.
func (p *Profile) Function(id uint64) (*Function, bool) {
	f, ok := p.funcByID[id]
	return f, ok
}

// Mapping returns the mapping with the given ID.
func (p *Profile) Mapping(id uint64) (*Mapping, bool) {
	m, ok := p.mapByID[id]
	return m, ok
}

// Validate checks that the tables are consistent: string_table[0] is empty,
// every string index is in range, and every ID reference resolves. It returns
// the first problem found.
func (p *Profile) Validate() error {
	p.index()

	if len(p.Strings) == 0 || p.Strings[0] != "" {
		return errors.New("string_table[0] must be the empty string")
	}
	str := func(what string, i int64) error {
		if i < 0 || i >= int64(len(p.Strings)) {
			return fmt.Errorf("%s: string index %d out of range (table has %d)", what, i, len(p.Strings))
		}
		return nil
	}

	for i, st := range p.SampleTypes {
		if err := errors.Join(str(fmt.Sprintf("sample_type[%d].type", i), st.Type), str(fmt.Sprintf("sample_type[%d].unit", i), st.Unit)); err != nil {
			return err
		}
	}
	if err := errors.Join(str("period_type.type", p.PeriodType.Type), str("period_type.unit", p.PeriodType.Unit)); err != nil {
		return err
	}

	for _, f := range p.Functions {
		what := fmt.Sprintf("function %d", f.ID)
		if err := errors.Join(str(what+" name", f.Name), str(what+" system_name", f.SystemName), str(what+" filename", f.Filename)); err != nil {
			return err
		}
	}
	for _, m := range p.Mappings {
		what := fmt.Sprintf("mapping %d", m.ID)
		if err := errors.Join(str(what+" filename", m.Filename), str(what+" build_id", m.BuildID)); err != nil {
			return err
		}
	}
	for _, l := range p.Locations {
		if l.MappingID != 0 {
			if _, ok := p.mapByID[l.MappingID]; !ok {
				return fmt.Errorf("location %d: mapping %d not found", l.ID, l.MappingID)
			}
		}
		for _, line := range l.Lines {
			if _, ok := p.funcByID[line.FunctionID]; !ok {
				return fmt.Errorf("location %d: function %d not found", l.ID, line.FunctionID)
			}
		}
	}
	for i, s := range p.Samples {
		if len(s.Values) != len(p.SampleTypes) {
			return fmt.Errorf("sample %d: %d values for %d sample types", i, len(s.Values), len(p.SampleTypes))
		}
		for _, id := range s.LocationIDs {
			if _, ok := p.locByID[id]; !ok {
				return fmt.Errorf("sample %d: location %d not found", i, id)
			}
		}
	}
	return nil
}
