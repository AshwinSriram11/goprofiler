// Package inspect turns a decoded profile into a Report: the raw tables plus
// the statistics that show how much of the profile is shared. The Report is
// shaped for JSON, so the inspector page can render it as-is.
package inspect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AshwinSriram11/goprofiler/internal/pprofwire"
)

// maxSafeID is the largest integer a JavaScript number holds exactly. IDs are
// sent as JSON numbers, so anything above this is flagged in Overview.Validation.
const maxSafeID = 1 << 53

// Report is everything the inspector shows about one profile. Tables are kept
// as stored: string fields are indexes into Strings, and the page does the
// lookups.
type Report struct {
	Overview  Overview      `json:"overview"`
	Columns   []Column      `json:"columns"`
	Sharing   Sharing       `json:"sharing"`
	Bytes     []ByteShare   `json:"bytes"`
	Samples   []SampleRow   `json:"samples"`
	Locations []LocationRow `json:"locations"`
	Functions []FunctionRow `json:"functions"`
	Strings   []StringRow   `json:"strings"`
	Mappings  []MappingRow  `json:"mappings"`
}

// Overview holds the file-level facts.
type Overview struct {
	RawSize       int    `json:"rawSize"`  // decoded bytes
	GzipSize      int    `json:"gzipSize"` // bytes as received
	Time          string `json:"time"`     // RFC 3339, "" if the profile has no timestamp
	DurationNanos int64  `json:"durationNanos"`
	Period        int64  `json:"period"`
	PeriodType    string `json:"periodType"`    // e.g. "goroutine/count"
	DefaultColumn int    `json:"defaultColumn"` // index into Columns
	Validation    string `json:"validation"`    // "" when the tables are consistent
}

// Column is one value column of the samples (one per sample type).
// Aggregation is only set for columns that count things (unit "count"); for
// bytes or nanoseconds, "events per stack" means nothing.
type Column struct {
	Type        string       `json:"type"`
	Unit        string       `json:"unit"`
	Total       int64        `json:"total"`
	Aggregation *Aggregation `json:"aggregation"`
}

// Sharing summarises how much structure the profile reuses.
type Sharing struct {
	Samples             int     `json:"samples"`         // unique stacks
	Slots               int     `json:"slots"`           // total stack positions across samples
	UniqueLocations     int     `json:"uniqueLocations"` // distinct locations the samples reference
	FrameSharing        float64 `json:"frameSharing"`    // Slots / UniqueLocations
	MaxDepth            int     `json:"maxDepth"`
	AvgDepth            float64 `json:"avgDepth"`
	UnresolvedLocations int     `json:"unresolvedLocations"` // locations with no function lines

}

// Aggregation compares what a count column counts with how many unique stacks
// hold it. Estimated is set for Go heap columns (alloc_*, inuse_*): the heap
// profiler samples about one allocation per 512 KB and scales the counts up,
// so Events is "sum of values", not a literal event count.
type Aggregation struct {
	Events    int64   `json:"events"`    // sum of the column
	Factor    float64 `json:"factor"`    // Events / unique stacks
	FlatSlots int64   `json:"flatSlots"` // stack positions if every event kept its own copy of its stack
	Estimated bool    `json:"estimated"`
}

// ByteShare is the share of the decoded file taken by one top-level field.
type ByteShare struct {
	Name    string  `json:"name"`
	Field   int     `json:"field"`
	Bytes   int     `json:"bytes"`
	Percent float64 `json:"percent"`
}

// SampleRow is one unique stack.
type SampleRow struct {
	Index       int      `json:"index"`
	Values      []int64  `json:"values"`      // one per Column
	LocationIDs []uint64 `json:"locationIds"` // leaf first
	Depth       int      `json:"depth"`
}

// LineRow is one function at one source line within a location.
type LineRow struct {
	FunctionID uint64 `json:"functionId"`
	Line       int64  `json:"line"`
}

// LocationRow is one code address. UsedBy counts stack positions that
// reference it (a location can occur more than once in a stack). Weighted has
// one entry per Column: the summed value of the samples whose stack contains
// the location, each sample counted once.
type LocationRow struct {
	ID        uint64    `json:"id"`
	Address   string    `json:"address"` // hex; addresses can exceed 2^53
	MappingID uint64    `json:"mappingId"`
	Lines     []LineRow `json:"lines"`
	UsedBy    int       `json:"usedBy"`
	Weighted  []int64   `json:"weighted"`
}

// FunctionRow is one function. Name, SystemName and Filename are indexes into
// Strings. UsedBy counts the lines that reference it.
type FunctionRow struct {
	ID         uint64 `json:"id"`
	Name       int64  `json:"name"`
	SystemName int64  `json:"systemName"`
	Filename   int64  `json:"filename"`
	StartLine  int64  `json:"startLine"`
	UsedBy     int    `json:"usedBy"`
}

// StringRow is one string-table entry. UsedBy counts the rows (functions,
// mappings, value types, labels, comments) that reference it at least once;
// references to index 0, which means "none", are not counted.
type StringRow struct {
	Index  int    `json:"index"`
	Text   string `json:"text"`
	UsedBy int    `json:"usedBy"`
}

// MappingRow is one loaded binary or library. Filename and BuildID are
// indexes into Strings; Locations counts the locations that point at it.
type MappingRow struct {
	ID        uint64   `json:"id"`
	Start     string   `json:"start"`
	Limit     string   `json:"limit"`
	Offset    string   `json:"offset"`
	Filename  int64    `json:"filename"`
	BuildID   int64    `json:"buildId"`
	Flags     []string `json:"flags"`
	Locations int      `json:"locations"`
}

// Analyze builds the Report for p. gzipSize is the size of the file as
// received, which the decoder can't know. It tolerates inconsistent profiles:
// problems are reported in Overview.Validation instead of panicking.
func Analyze(p *pprofwire.Profile, gzipSize int) *Report {
	cols := columns(p)
	def := defaultColumn(p)

	r := &Report{
		Overview: overview(p, gzipSize, def),
		Columns:  cols,
		Sharing:  sharing(p),
		Bytes:    byteShares(p),
	}

	r.Samples = make([]SampleRow, len(p.Samples))
	for i, s := range p.Samples {
		r.Samples[i] = SampleRow{
			Index:       i,
			Values:      append([]int64{}, s.Values...),
			LocationIDs: append([]uint64{}, s.LocationIDs...),
			Depth:       len(s.LocationIDs),
		}
	}

	locUsed, locWeighted := locationUsage(p, len(cols))
	fnUsed := make(map[uint64]int)
	mapLocs := make(map[uint64]int)

	r.Locations = make([]LocationRow, len(p.Locations))
	for i, l := range p.Locations {
		lines := make([]LineRow, len(l.Lines))
		for j, line := range l.Lines {
			lines[j] = LineRow{FunctionID: line.FunctionID, Line: line.Line}
			fnUsed[line.FunctionID]++
		}
		mapLocs[l.MappingID]++
		weighted := locWeighted[l.ID]
		if weighted == nil {
			weighted = make([]int64, len(cols))
		}
		r.Locations[i] = LocationRow{
			ID:        l.ID,
			Address:   fmt.Sprintf("%#x", l.Address),
			MappingID: l.MappingID,
			Lines:     lines,
			UsedBy:    locUsed[l.ID],
			Weighted:  weighted,
		}
	}

	r.Functions = make([]FunctionRow, len(p.Functions))
	for i, f := range p.Functions {
		r.Functions[i] = FunctionRow{
			ID: f.ID, Name: f.Name, SystemName: f.SystemName, Filename: f.Filename,
			StartLine: f.StartLine, UsedBy: fnUsed[f.ID],
		}
	}

	strRefs := stringUsage(p)
	r.Strings = make([]StringRow, len(p.Strings))
	for i, s := range p.Strings {
		r.Strings[i] = StringRow{Index: i, Text: s, UsedBy: strRefs[i]}
	}

	r.Mappings = make([]MappingRow, len(p.Mappings))
	for i, m := range p.Mappings {
		r.Mappings[i] = MappingRow{
			ID:        m.ID,
			Start:     fmt.Sprintf("%#x", m.MemoryStart),
			Limit:     fmt.Sprintf("%#x", m.MemoryLimit),
			Offset:    fmt.Sprintf("%#x", m.FileOffset),
			Filename:  m.Filename,
			BuildID:   m.BuildID,
			Flags:     mappingFlags(m),
			Locations: mapLocs[m.ID],
		}
	}
	return r
}

// defaultColumn is the column used to rank stacks: the profile's default
// sample type if it names one, otherwise the last, as go tool pprof does. That
// makes inuse_space the default for heap profiles and cpu time for CPU ones.
func defaultColumn(p *pprofwire.Profile) int {
	if p.DefaultSampleType != 0 {
		for i, st := range p.SampleTypes {
			if st.Type == p.DefaultSampleType {
				return i
			}
		}
	}
	if n := len(p.SampleTypes); n > 0 {
		return n - 1
	}
	return 0
}

// value returns a sample's value in column c, tolerating short Values slices
// from malformed profiles.
func value(s pprofwire.Sample, c int) int64 {
	if c < len(s.Values) {
		return s.Values[c]
	}
	return 0
}

func overview(p *pprofwire.Profile, gzipSize, def int) Overview {
	o := Overview{
		RawSize:       p.RawSize,
		GzipSize:      gzipSize,
		DurationNanos: p.DurationNanos,
		Period:        p.Period,
		DefaultColumn: def,
	}
	if p.TimeNanos != 0 {
		o.Time = time.Unix(0, p.TimeNanos).UTC().Format(time.RFC3339Nano)
	}
	if t, u := p.Str(p.PeriodType.Type), p.Str(p.PeriodType.Unit); t != "" || u != "" {
		o.PeriodType = t + "/" + u
	}
	if err := validate(p); err != nil {
		o.Validation = err.Error()
	}
	return o
}

// validate runs the decoder's consistency checks, then checks that IDs fit in
// a JSON number without losing precision in the browser.
func validate(p *pprofwire.Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	tooBig := func(what string, id uint64) error {
		if id > maxSafeID {
			return fmt.Errorf("%s ID %d exceeds 2^53 and can't be shown exactly", what, id)
		}
		return nil
	}
	for _, l := range p.Locations {
		if err := tooBig("location", l.ID); err != nil {
			return err
		}
	}
	for _, f := range p.Functions {
		if err := tooBig("function", f.ID); err != nil {
			return err
		}
	}
	for _, m := range p.Mappings {
		if err := tooBig("mapping", m.ID); err != nil {
			return err
		}
	}
	return nil
}

func columns(p *pprofwire.Profile) []Column {
	cols := make([]Column, len(p.SampleTypes))
	for i, st := range p.SampleTypes {
		cols[i] = Column{Type: p.Str(st.Type), Unit: p.Str(st.Unit)}
		for _, s := range p.Samples {
			cols[i].Total += value(s, i)
		}
		if cols[i].Unit == "count" {
			cols[i].Aggregation = aggregation(p, i, cols[i])
		}
	}
	return cols
}

func aggregation(p *pprofwire.Profile, col int, c Column) *Aggregation {
	a := &Aggregation{
		Events:    c.Total,
		Estimated: strings.HasPrefix(c.Type, "alloc_") || strings.HasPrefix(c.Type, "inuse_"),
	}
	for _, s := range p.Samples {
		a.FlatSlots += value(s, col) * int64(len(s.LocationIDs))
	}
	if len(p.Samples) > 0 {
		a.Factor = float64(a.Events) / float64(len(p.Samples))
	}
	return a
}

func sharing(p *pprofwire.Profile) Sharing {
	s := Sharing{Samples: len(p.Samples)}
	seen := make(map[uint64]bool)
	for _, smp := range p.Samples {
		depth := len(smp.LocationIDs)
		s.Slots += depth
		if depth > s.MaxDepth {
			s.MaxDepth = depth
		}
		for _, id := range smp.LocationIDs {
			seen[id] = true
		}
	}
	s.UniqueLocations = len(seen)
	if s.UniqueLocations > 0 {
		s.FrameSharing = float64(s.Slots) / float64(s.UniqueLocations)
	}
	if s.Samples > 0 {
		s.AvgDepth = float64(s.Slots) / float64(s.Samples)
	}
	for _, l := range p.Locations {
		if len(l.Lines) == 0 {
			s.UnresolvedLocations++
		}
	}
	return s
}

var fieldNames = map[int]string{
	1: "sample_type", 2: "sample", 3: "mapping", 4: "location", 5: "function",
	6: "string_table", 7: "drop_frames", 8: "keep_frames", 9: "time_nanos",
	10: "duration_nanos", 11: "period_type", 12: "period", 13: "comment",
	14: "default_sample_type",
}

// byteShares lists the top-level fields by the wire bytes they take, largest
// first.
func byteShares(p *pprofwire.Profile) []ByteShare {
	shares := make([]ByteShare, 0, len(p.FieldBytes))
	for field, n := range p.FieldBytes {
		name, ok := fieldNames[field]
		if !ok {
			name = fmt.Sprintf("field %d", field)
		}
		s := ByteShare{Name: name, Field: field, Bytes: n}
		if p.RawSize > 0 {
			s.Percent = 100 * float64(n) / float64(p.RawSize)
		}
		shares = append(shares, s)
	}
	sort.Slice(shares, func(i, j int) bool {
		if shares[i].Bytes != shares[j].Bytes {
			return shares[i].Bytes > shares[j].Bytes
		}
		return shares[i].Field < shares[j].Field
	})
	return shares
}

// locationUsage counts, per location ID, the stack positions that reference it
// and the per-column value of the samples containing it (each sample once).
func locationUsage(p *pprofwire.Profile, ncols int) (used map[uint64]int, weighted map[uint64][]int64) {
	used = make(map[uint64]int)
	weighted = make(map[uint64][]int64)
	for _, smp := range p.Samples {
		inSample := make(map[uint64]bool, len(smp.LocationIDs))
		for _, id := range smp.LocationIDs {
			used[id]++
			if inSample[id] {
				continue
			}
			inSample[id] = true
			w := weighted[id]
			if w == nil {
				w = make([]int64, ncols)
				weighted[id] = w
			}
			for c := range w {
				w[c] += value(smp, c)
			}
		}
	}
	return used, weighted
}

// stringUsage counts, per string index, the rows that reference it at least
// once. A function whose name and system name are the same string counts once.
func stringUsage(p *pprofwire.Profile) []int {
	refs := make([]int, len(p.Strings))
	row := func(idxs ...int64) {
		for k, i := range idxs {
			if i <= 0 || i >= int64(len(refs)) {
				continue
			}
			dup := false
			for _, earlier := range idxs[:k] {
				if earlier == i {
					dup = true
					break
				}
			}
			if !dup {
				refs[i]++
			}
		}
	}
	for _, st := range p.SampleTypes {
		row(st.Type, st.Unit)
	}
	row(p.PeriodType.Type, p.PeriodType.Unit)
	for _, f := range p.Functions {
		row(f.Name, f.SystemName, f.Filename)
	}
	for _, m := range p.Mappings {
		row(m.Filename, m.BuildID)
	}
	for _, s := range p.Samples {
		for _, l := range s.Labels {
			row(l.Key, l.Str, l.NumUnit)
		}
	}
	for _, c := range p.Comments {
		row(c)
	}
	row(p.DefaultSampleType)
	return refs
}

func mappingFlags(m pprofwire.Mapping) []string {
	flags := []string{}
	if m.HasFunctions {
		flags = append(flags, "has_functions")
	}
	if m.HasFilenames {
		flags = append(flags, "has_filenames")
	}
	if m.HasLineNumbers {
		flags = append(flags, "has_line_numbers")
	}
	if m.HasInlineFrames {
		flags = append(flags, "has_inline_frames")
	}
	return flags
}
