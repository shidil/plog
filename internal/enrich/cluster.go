package enrich

import (
	"regexp"

	"github.com/shidil/plog/internal/record"
)

// Maskers normalize the variable parts of a message so that lines describing
// the same event collapse to one template. Order matters: hex and addresses
// are masked before bare numbers so ports and digits inside them are not
// partially rewritten.
var maskers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`0x[0-9a-fA-F]+`), "<hex>"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`), "<ip>"},
	{regexp.MustCompile(`\[[0-9a-fA-F:]*::[0-9a-fA-F:]*\]`), "<ip6>"},
	{regexp.MustCompile(`\d+`), "<n>"},
}

// Template returns a key identifying the event a record describes, with
// variable tokens masked out. Records sharing a template, level, and component
// are treated as repetitions of the same event by a Folder.
func Template(rec record.Record) string {
	msg := rec.Message
	if rec.Stack != nil {
		msg = rec.Stack.Header
	}
	for _, m := range maskers {
		msg = m.re.ReplaceAllString(msg, m.with)
	}
	return rec.Effective.String() + "\x00" + component(rec) + "\x00" + msg
}

// component returns the value plog groups folded runs by: the component field,
// falling back to service, or "" when neither is present.
func component(rec record.Record) string {
	if v := fieldValue(rec, "component"); v != "" {
		return v
	}
	return fieldValue(rec, "service")
}

// Folder collapses consecutive records that share a Template into a single
// record whose Repeat count reflects the run length. It holds the current run's
// first record until the run ends, trading one line of latency for clean
// counts; folding is skipped entirely when disabled or for passthrough lines.
type Folder struct {
	enabled bool
	pending record.Record
	has     bool
	key     string
	count   int
}

// NewFolder returns a Folder. When enabled is false, Add emits every record
// immediately and never collapses.
func NewFolder(enabled bool) *Folder {
	return &Folder{enabled: enabled}
}

// Add feeds the next record and returns the records ready to emit now (zero
// when the record extends the current run, otherwise the flushed run followed
// by nothing until its own run ends).
func (f *Folder) Add(rec record.Record) []record.Record {
	if !f.enabled || !rec.Parsed {
		return append(f.Flush(), rec)
	}
	key := Template(rec)
	if f.has && key == f.key {
		f.count++
		return nil
	}
	out := f.Flush()
	f.pending = rec
	f.has = true
	f.key = key
	f.count = 1
	return out
}

// Flush emits the buffered run, if any. Call it once after the input ends.
func (f *Folder) Flush() []record.Record {
	if !f.has {
		return nil
	}
	rec := f.pending
	rec.Repeat = f.count
	f.has = false
	f.count = 0
	f.key = ""
	return []record.Record{rec}
}
