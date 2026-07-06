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

// foldWindow is how many intervening records of other templates a run tolerates
// before it is considered ended. A run that recurs at least this often keeps
// folding even when another event type interleaves with it — so the common
// request-log / result-log pairing (A, B, A, B, ...) collapses to A×n and B×n
// rather than defeating folding entirely. Head latency is bounded by the
// caller's flush timer, not by this window.
const foldWindow = 10

// maxOpenRuns caps how many distinct templates fold concurrently, keeping memory
// flat when many event types interleave; the oldest run is flushed to make room.
const maxOpenRuns = 8

// foldRun is one open run: a head record standing in for its repetitions, the
// template that identifies them, the count so far, and the position at which the
// run last folded a record (to detect when it has ended).
type foldRun struct {
	head     record.Record
	key      string
	count    int
	lastSeen int
}

// Folder collapses records that share a Template into a single record whose
// Repeat count reflects how many were folded. It keeps a bounded set of open
// runs, oldest head first, so interleaved event types fold in parallel; a run is
// held until it ends (no match within foldWindow) or Flush is called, trading a
// little latency for clean counts. Runs are always emitted in head order, so
// output stays time-ordered. Folding is skipped for disabled Folders and
// passthrough lines.
type Folder struct {
	enabled bool
	runs    []foldRun // open runs, oldest head first
	pos     int       // monotonic count of parsed records seen
}

// NewFolder returns a Folder. When enabled is false, Add emits every record
// immediately and never collapses.
func NewFolder(enabled bool) *Folder {
	return &Folder{enabled: enabled}
}

// Add feeds the next record and returns the records ready to emit now: any runs
// that just ended (plus, for a disabled or passthrough line, that line itself).
// A record that extends an open run folds into it and emits nothing.
func (f *Folder) Add(rec record.Record) []record.Record {
	if !f.enabled || !rec.Parsed {
		return append(f.Flush(), rec)
	}
	f.pos++
	out := f.flushEnded()

	key := Template(rec)
	for i := range f.runs {
		if f.runs[i].key == key {
			f.runs[i].count++
			f.runs[i].lastSeen = f.pos
			return out
		}
	}

	if len(f.runs) >= maxOpenRuns {
		out = append(out, f.flushPrefix(1)...)
	}
	f.runs = append(f.runs, foldRun{head: rec, key: key, count: 1, lastSeen: f.pos})
	return out
}

// Flush finalizes and clears every open run in head order. Call it after input
// ends and on the flush timer, bounding how long a still-open run is held on a
// live tail.
func (f *Folder) Flush() []record.Record {
	return f.flushPrefix(len(f.runs))
}

// flushEnded finalizes every run that has folded nothing within foldWindow,
// along with any older run ahead of it, so emitted heads stay in time order.
func (f *Folder) flushEnded() []record.Record {
	last := -1
	for i := range f.runs {
		if f.pos-f.runs[i].lastSeen > foldWindow {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	return f.flushPrefix(last + 1)
}

// flushPrefix finalizes and removes the first n open runs (the oldest heads),
// returning them ready to render. Flushing a prefix keeps output in head order.
func (f *Folder) flushPrefix(n int) []record.Record {
	if n <= 0 {
		return nil
	}
	out := make([]record.Record, n)
	for i := range out {
		rec := f.runs[i].head
		rec.Repeat = f.runs[i].count
		out[i] = rec
	}
	f.runs = f.runs[:copy(f.runs, f.runs[n:])]
	return out
}
