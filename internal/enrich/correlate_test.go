package enrich

import (
	"strings"
	"testing"
	"time"

	"github.com/shidil/plog/internal/record"
)

// at stamps rec with a fixed-day timestamp at the given second, so span gating
// (groupSpan/linkSpan) can be exercised without depending on the wall clock.
func at(rec record.Record, sec int) record.Record {
	rec.Time = time.Date(2026, 1, 1, 4, 29, sec, 0, time.UTC)
	return rec
}

func TestCorrelatorGroupsByID(t *testing.T) {
	c := NewCorrelator(true)

	first := c.Mark(mkRec(record.LevelInfo, "started", record.KV{Key: "trace_id", Val: "abc123"}))
	second := c.Mark(mkRec(record.LevelInfo, "finished", record.KV{Key: "trace_id", Val: "abc123"}))

	// The first sighting of a key is not yet recurring, so it stays untagged; the
	// second carries the stable tag derived from the key.
	if first.Corr != "" {
		t.Errorf("first record Corr = %q, want empty (key not yet recurring)", first.Corr)
	}
	if want := tag("abc123"); second.Corr != want {
		t.Errorf("second record Corr = %q, want %q", second.Corr, want)
	}
}

func TestCorrelatorGroupsByClientIP(t *testing.T) {
	c := NewCorrelator(true)

	c.Mark(mkRec(record.LevelInfo, "panic serving 10.0.0.1:5000: boom"))
	second := c.Mark(mkRec(record.LevelInfo, "panic serving 10.0.0.1:5050: boom again"))

	if want := tag("10.0.0.1"); second.Corr != want {
		t.Errorf("second record Corr = %q, want %q (grouped by client ip)", second.Corr, want)
	}
}

func TestCorrelatorNoKeyNoTag(t *testing.T) {
	c := NewCorrelator(true)

	got := c.Mark(mkRec(record.LevelInfo, "no id and no address here"))

	if got.Corr != "" {
		t.Errorf("Corr = %q, want empty when no correlation key present", got.Corr)
	}
}

func TestCorrelatorLinksRelatedEvent(t *testing.T) {
	c := NewCorrelator(true)

	// An elevated event for a method, then a distinct later event for the same
	// method: the later record links back to the elevated one.
	panicRec := at(mkRec(record.LevelError, "panic in ResolveLocationSlug", record.KV{Key: "rpc.method", Val: "ResolveLocationSlug"}), 1)
	c.Mark(panicRec)
	finished := at(mkRec(record.LevelWarn, "finished call", record.KV{Key: "rpc.method", Val: "ResolveLocationSlug"}), 2)
	got := c.Mark(finished)

	if got.Related == nil {
		t.Fatal("Related = nil, want a link back to the panic")
	}
	if !strings.Contains(got.Related.Summary, "panic in ResolveLocationSlug") {
		t.Errorf("Related.Summary = %q, want it to describe the panic", got.Related.Summary)
	}
	if !got.Related.Time.Equal(panicRec.Time) {
		t.Errorf("Related.Time = %v, want %v (the panic's time)", got.Related.Time, panicRec.Time)
	}
}

func TestCorrelatorNoLink(t *testing.T) {
	method := func(m string) record.KV { return record.KV{Key: "rpc.method", Val: m} }
	tests := []struct {
		name   string
		prior  record.Record
		latest record.Record
	}{
		{
			name:   "different method",
			prior:  at(mkRec(record.LevelError, "boom", method("Foo")), 1),
			latest: at(mkRec(record.LevelWarn, "finished call", method("Bar")), 2),
		},
		{
			name:   "prior not elevated",
			prior:  at(mkRec(record.LevelInfo, "ok", method("Foo")), 1),
			latest: at(mkRec(record.LevelWarn, "finished call", method("Foo")), 2),
		},
		{
			name:   "same event repeated",
			prior:  at(mkRec(record.LevelError, "boom", method("Foo")), 1),
			latest: at(mkRec(record.LevelError, "boom", method("Foo")), 2),
		},
		{
			name:   "outside link span",
			prior:  at(mkRec(record.LevelError, "boom", method("Foo")), 1),
			latest: at(mkRec(record.LevelWarn, "finished call", method("Foo")), 30),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCorrelator(true)
			c.Mark(tc.prior)
			got := c.Mark(tc.latest)
			if got.Related != nil {
				t.Errorf("Related = %+v, want nil", got.Related)
			}
		})
	}
}

func TestCorrelatorDisabledLeavesRecordUntouched(t *testing.T) {
	c := NewCorrelator(false)

	c.Mark(mkRec(record.LevelError, "panic", record.KV{Key: "trace_id", Val: "x"}, record.KV{Key: "rpc.method", Val: "Foo"}))
	got := c.Mark(mkRec(record.LevelWarn, "finished call", record.KV{Key: "trace_id", Val: "x"}, record.KV{Key: "rpc.method", Val: "Foo"}))

	if got.Corr != "" || got.Related != nil {
		t.Errorf("disabled correlator annotated record: Corr=%q Related=%+v", got.Corr, got.Related)
	}
}

func TestCorrelatorPassthroughUnchanged(t *testing.T) {
	c := NewCorrelator(true)
	in := record.Record{Raw: "plain text 10.0.0.1", Parsed: false}
	got := c.Mark(in)
	if got.Corr != "" || got.Related != nil {
		t.Errorf("passthrough record annotated: Corr=%q Related=%+v", got.Corr, got.Related)
	}
}

func TestMethodToken(t *testing.T) {
	withStack := mkRec(record.LevelError, "panic")
	withStack.Stack = &record.StackTrace{Frames: []record.Frame{
		{Func: "runtime.gopanic", Kind: record.FrameStdlib},
		{Func: "github.com/example/storefront/internal/rpc/location/v1beta1.(*locationService).ResolveLocationSlug", Kind: record.FrameProject},
	}}

	tests := []struct {
		name string
		in   record.Record
		want string
	}{
		{name: "rpc.method field wins", in: mkRec(record.LevelInfo, "m", record.KV{Key: "rpc.method", Val: "Foo"}), want: "Foo"},
		{name: "project frame method", in: withStack, want: "ResolveLocationSlug"},
		{name: "neither", in: mkRec(record.LevelInfo, "m"), want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := methodToken(tc.in); got != tc.want {
				t.Errorf("methodToken(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
