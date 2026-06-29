package enrich

import (
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shidil/plog/internal/record"
)

// Correlation tuning. corrWindow bounds how many recent records are remembered
// for both grouping and causal linking, keeping memory flat on a live tail.
// groupSpan is how far apart two records sharing a key may be and still count as
// the same request; linkSpan is the tighter window for a causal "related" link.
const (
	corrWindow = 512
	groupSpan  = time.Minute
	linkSpan   = 5 * time.Second
)

// corrIDKeys are the field names, in priority order, treated as an explicit
// request/trace identifier when present. Different stacks label the concept
// differently, so the list is generous; the first match wins.
var corrIDKeys = []string{
	"trace_id", "trace.id", "traceID", "traceId",
	"request_id", "request.id", "requestID", "requestId", "req_id",
	"correlation_id", "correlationID", "x-request-id",
}

// ipv4 captures a bare IPv4 address — the client address embedded in messages
// like "panic serving 192.168.117.3:58986" — used as a fallback grouping key
// when no explicit id field is present.
var ipv4 = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)

// corrEntry is the remembered summary of a recent record, used to detect when a
// later record shares its request (same key) or is causally linked (same method
// within linkSpan of an elevated event).
type corrEntry struct {
	at       time.Time
	key      string // grouping key (id or client ip), "" when none
	method   string // rpc method or project frame method, "" when none
	template string // event template, so a link skips repeats of the same event
	elevated bool   // Effective >= Warn: only elevated events are worth linking to
	summary  string // short text for a back-reference note
}

// Correlator is a stateful enrich stage that reconstructs request structure
// without reordering the stream. It tags records that share a recent
// correlation key (Record.Corr) so one request reads as a group, and annotates a
// record with a backward Related link when it follows a recent elevated event
// for the same method — surfacing, e.g., the validation failure tied to the
// panic just before it. It looks only backward, so records still emit
// immediately; it holds a bounded window of recent entries to stay flat in
// memory.
type Correlator struct {
	enabled bool
	recent  []corrEntry
}

// NewCorrelator returns a Correlator. When enabled is false, Mark returns every
// record unchanged.
func NewCorrelator(enabled bool) *Correlator {
	return &Correlator{enabled: enabled}
}

// Mark annotates rec with correlation metadata and remembers it for later
// records. Passthrough records carry no structure to correlate and pass through
// untouched.
func (c *Correlator) Mark(rec record.Record) record.Record {
	if !c.enabled || !rec.Parsed {
		return rec
	}

	key := groupKey(rec)
	method := methodToken(rec)
	tmpl := Template(rec)

	rec.Corr = c.tagFor(key, rec.Time)
	rec.Related = c.linkFor(method, tmpl, rec.Time)

	c.remember(corrEntry{
		at:       rec.Time,
		key:      key,
		method:   method,
		template: tmpl,
		elevated: rec.Effective >= record.LevelWarn,
		summary:  summarize(rec),
	})
	return rec
}

// tagFor returns the group tag for key: a short stable code derived from the
// key, but only once the key has already appeared in the recent window, so a
// one-off id adds no noise. Returns "" when key is empty or not yet recurring.
func (c *Correlator) tagFor(key string, at time.Time) string {
	if key == "" {
		return ""
	}
	for _, e := range c.recent {
		if e.key == key && within(e.at, at, groupSpan) {
			return tag(key)
		}
	}
	return ""
}

// linkFor finds the most recent elevated event that shares method, sits within
// linkSpan, and describes a different event, then returns a backward Related
// link to it. Requiring a different template avoids linking an event to repeats
// of itself; requiring the prior event be elevated keeps the note pointed at a
// real problem. Returns nil when there is no method or no match.
func (c *Correlator) linkFor(method, tmpl string, at time.Time) *record.Related {
	if method == "" {
		return nil
	}
	for i := len(c.recent) - 1; i >= 0; i-- {
		e := c.recent[i]
		if e.elevated && e.method == method && e.template != tmpl && within(e.at, at, linkSpan) {
			return &record.Related{Time: e.at, Summary: e.summary}
		}
	}
	return nil
}

// remember appends e to the bounded window, dropping the oldest entry once the
// window is full so memory stays flat on an endless stream.
func (c *Correlator) remember(e corrEntry) {
	c.recent = append(c.recent, e)
	if len(c.recent) > corrWindow {
		c.recent = c.recent[1:]
	}
}

// groupKey returns the value that identifies rec's request: the first explicit
// id field present, else a client IP embedded in the message, else "".
func groupKey(rec record.Record) string {
	for _, k := range corrIDKeys {
		if v := fieldValue(rec, k); v != "" {
			return v
		}
	}
	return ipv4.FindString(messageText(rec))
}

// methodToken returns the RPC method a record concerns: the rpc.method field if
// present, else the method name from the first project stack frame. Empty when
// neither is available.
func methodToken(rec record.Record) string {
	if v := fieldValue(rec, "rpc.method"); v != "" {
		return v
	}
	if rec.Stack != nil {
		for _, f := range rec.Stack.Frames {
			if f.Kind == record.FrameProject {
				return frameMethod(f.Func)
			}
		}
	}
	return ""
}

// frameMethod extracts the trailing method/function name from a fully qualified
// frame function, e.g. ".../v1beta1.(*locationService).ResolveLocationSlug"
// yields "ResolveLocationSlug".
func frameMethod(fn string) string {
	if i := strings.LastIndexByte(fn, '.'); i >= 0 {
		return fn[i+1:]
	}
	return fn
}

// messageText returns the text a record's identity is read from: a stack trace's
// header when present, otherwise the message field.
func messageText(rec record.Record) string {
	if rec.Stack != nil {
		return rec.Stack.Header
	}
	return rec.Message
}

// summarize renders a short single-line description of rec for a back-reference
// note: the leading text of its message or panic header, whitespace collapsed
// and length capped so the note stays compact.
func summarize(rec record.Record) string {
	msg := strings.Join(strings.Fields(messageText(rec)), " ")
	const max = 60
	if len(msg) > max {
		msg = msg[:max] + "…"
	}
	return msg
}

// within reports whether two record times fall inside span. A zero time (no
// parseable timestamp) defeats proximity reasoning, so the check defers to the
// caller's bounded window and returns true.
func within(a, b time.Time, span time.Duration) bool {
	if a.IsZero() || b.IsZero() {
		return true
	}
	d := b.Sub(a)
	if d < 0 {
		d = -d
	}
	return d <= span
}

// tag derives a short, stable group label from a correlation key via a hash, so
// no per-key state has to be retained as keys churn. Collisions only blur which
// lines share a tag; they never affect parsing or filtering.
func tag(key string) string {
	h := fnv.New32a()
	h.Write([]byte(key))
	return "c" + strconv.FormatUint(uint64(h.Sum32()%1000), 36)
}
