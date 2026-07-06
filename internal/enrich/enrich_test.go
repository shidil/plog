package enrich

import (
	"strings"
	"testing"

	"github.com/shidil/plog/internal/record"
)

// mkRec builds a parsed record with the declared level mirrored into Effective,
// as the parse stage would produce before enrichment.
func mkRec(level record.Level, msg string, fields ...record.KV) record.Record {
	return record.Record{
		Level:     level,
		Effective: level,
		Message:   msg,
		Fields:    fields,
		Parsed:    true,
	}
}

func TestSeverity(t *testing.T) {
	tests := []struct {
		name string
		rec  record.Record
		want record.Level
	}{
		{
			name: "panic_in_info_message_escalates_to_error",
			rec:  mkRec(record.LevelInfo, "http: panic serving: runtime error: nil pointer dereference"),
			want: record.LevelError,
		},
		{
			name: "connection_refused_escalates_info_to_warn",
			rec:  mkRec(record.LevelInfo, "failed to upload metrics: connection refused"),
			want: record.LevelWarn,
		},
		{
			name: "error_field_escalates",
			rec:  mkRec(record.LevelInfo, "finished call", record.KV{Key: "error", Val: "invalid_argument: validation error"}),
			want: record.LevelWarn,
		},
		{
			name: "explicit_error_never_lowered",
			rec:  mkRec(record.LevelError, "everything is fine"),
			want: record.LevelError,
		},
		{
			name: "benign_info_unchanged",
			rec:  mkRec(record.LevelInfo, "starting HTTP server"),
			want: record.LevelInfo,
		},
		{
			name: "passthrough_unchanged",
			rec:  record.Record{Raw: "panic everywhere", Parsed: false},
			want: record.LevelUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Severity(tc.rec)
			if got.Effective != tc.want {
				t.Errorf("Severity(%q).Effective = %v, want %v", tc.rec.Message, got.Effective, tc.want)
			}
		})
	}
}

const sampleTrace = "http: panic serving 192.168.117.3:58986: runtime error: invalid memory address or nil pointer dereference\n" +
	"goroutine 23 [running]:\n" +
	"net/http.(*conn).serve.func1()\n" +
	"\tnet/http/server.go:1907 +0xac\n" +
	"github.com/example/storefront/internal/rpc/location/v1beta1.(*locationService).ResolveLocationSlug(0x4ca80fbfa00, {0x10a5e18, 0x4ca81079110})\n" +
	"\tgithub.com/example/storefront/internal/rpc/location/v1beta1/location_rpc.go:72 +0x6c\n" +
	"go.opentelemetry.io/otel/sdk/trace.(*recordingSpan).End(0x4ca8107f2c0)\n" +
	"\tgo.opentelemetry.io/otel/sdk@v1.43.0/trace/span.go:528 +0xab4\n" +
	"created by net/http.(*Server).Serve in goroutine 10\n" +
	"\tnet/http/server.go:3464 +0x37c"

func TestStack(t *testing.T) {
	got := Stack(mkRec(record.LevelInfo, sampleTrace), "github.com/example")
	if got.Stack == nil {
		t.Fatal("Stack did not extract a trace from a panic message")
	}
	st := got.Stack

	if !strings.HasPrefix(st.Header, "http: panic serving") {
		t.Errorf("Header = %q, want it to start with the panic line", st.Header)
	}
	if len(st.Frames) != 4 {
		t.Fatalf("got %d frames, want 4: %+v", len(st.Frames), st.Frames)
	}

	// The project frame is the actionable one: pointers stripped, line kept.
	var project *record.Frame
	for i := range st.Frames {
		if st.Frames[i].Kind == record.FrameProject {
			project = &st.Frames[i]
			break
		}
	}
	if project == nil {
		t.Fatal("no project frame classified; module prefix not matched")
	}
	if project.Line != 72 {
		t.Errorf("project frame line = %d, want 72", project.Line)
	}
	if !strings.HasSuffix(project.File, "location_rpc.go") {
		t.Errorf("project frame file = %q, want it to end with location_rpc.go", project.File)
	}
	if strings.ContainsAny(project.Func, "(") && strings.Contains(project.Func, "0x") {
		t.Errorf("project frame func = %q, want pointers/args stripped", project.Func)
	}

	wantKinds := []record.FrameKind{record.FrameStdlib, record.FrameProject, record.FrameThirdParty, record.FrameStdlib}
	for i, want := range wantKinds {
		if st.Frames[i].Kind != want {
			t.Errorf("frame[%d] (%s) kind = %v, want %v", i, st.Frames[i].Func, st.Frames[i].Kind, want)
		}
	}
}

func TestStackNonTraceUnchanged(t *testing.T) {
	got := Stack(mkRec(record.LevelInfo, "just a normal message"), "github.com/example")
	if got.Stack != nil {
		t.Errorf("Stack populated for a non-trace message: %+v", got.Stack)
	}
}

func TestStackFromField(t *testing.T) {
	// Structured loggers carry the trace in a dedicated field rather than the
	// message; it must be lifted into Stack and the field consumed so it is not
	// also rendered raw.
	tests := []struct {
		name     string
		traceKey string
	}{
		{name: "stack_field", traceKey: "stack"},
		{name: "error_field", traceKey: "error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := mkRec(record.LevelError, "request failed",
				record.KV{Key: "rpc", Val: "ResolveLocationSlug"},
				record.KV{Key: tc.traceKey, Val: sampleTrace},
			)
			got := Stack(rec, "github.com/example")
			if got.Stack == nil {
				t.Fatalf("Stack did not lift a trace from the %q field", tc.traceKey)
			}
			if len(got.Stack.Frames) != 4 {
				t.Errorf("got %d frames, want 4: %+v", len(got.Stack.Frames), got.Stack.Frames)
			}
			want := []record.KV{{Key: "rpc", Val: "ResolveLocationSlug"}}
			if !equalKV(got.Fields, want) {
				t.Errorf("Fields = %+v, want only the untouched rpc field %+v", got.Fields, want)
			}
		})
	}
}

func TestStackFromFieldUsesMessageHeader(t *testing.T) {
	// A field holding only the trace (no panic line of its own) has no header;
	// the record's message stands in as the summary shown in place of it.
	bareTrace := "goroutine 1 [running]:\n" +
		"github.com/example/app.run()\n" +
		"\tgithub.com/example/app/main.go:20 +0x1a"
	rec := mkRec(record.LevelError, "request failed", record.KV{Key: "stack", Val: bareTrace})

	got := Stack(rec, "github.com/example")
	if got.Stack == nil {
		t.Fatal("Stack did not lift a trace from the stack field")
	}
	if got.Stack.Header != "request failed" {
		t.Errorf("Header = %q, want the record message as fallback", got.Stack.Header)
	}
}

func TestStackFieldNotLifted(t *testing.T) {
	// A multi-line field value that is not a trace must be left in place: neither
	// the fast-reject marker check nor the parser should claim it.
	tests := []struct {
		name string
		val  string
	}{
		{name: "no_marker", val: "meet me at the office\nsee you there"},
		{name: "marker_but_not_a_trace", val: "goroutine leak suspected\nplease investigate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := mkRec(record.LevelInfo, "note", record.KV{Key: "note", Val: tc.val})
			got := Stack(rec, "github.com/example")
			if got.Stack != nil {
				t.Errorf("Stack lifted a non-trace field: %+v", got.Stack)
			}
			if len(got.Fields) != 1 {
				t.Errorf("field wrongly consumed: Fields = %+v", got.Fields)
			}
		})
	}
}

func FuzzStackField(f *testing.F) {
	seeds := []string{
		"",
		"plain text",
		sampleTrace,
		nodeTrace,
		"goroutine 1 [running]:\nmain.x()\n\t/a/b.go:1 +0x1",
		"goroutine\n\n",
		"at foo (index.js:1:1)",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, val string) {
		rec := mkRec(record.LevelInfo, "msg", record.KV{Key: "stack", Val: val})
		got := Stack(rec, "github.com/example")
		// Invariant: lifting a stack consumes exactly its source field; not
		// lifting leaves the fields untouched.
		if got.Stack != nil {
			if len(got.Fields) != 0 {
				t.Errorf("stack lifted from %q but field not consumed: %+v", val, got.Fields)
			}
		} else if len(got.Fields) != 1 {
			t.Errorf("no stack lifted from %q but fields changed: %+v", val, got.Fields)
		}
	})
}

// equalKV reports whether two field slices are identical in order and content.
func equalKV(got, want []record.KV) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestTemplateMasksVariableTokens(t *testing.T) {
	a := Severity(mkRec(record.LevelInfo, "dial tcp [::1]:4317: connection refused"))
	b := Severity(mkRec(record.LevelInfo, "dial tcp [::1]:9999: connection refused"))
	if Template(a) != Template(b) {
		t.Errorf("templates differ after masking:\n a=%q\n b=%q", Template(a), Template(b))
	}

	c := Severity(mkRec(record.LevelInfo, "a completely different message"))
	if Template(a) == Template(c) {
		t.Errorf("unrelated messages share a template: %q", Template(a))
	}
}

func TestFolderCollapsesConsecutiveRun(t *testing.T) {
	f := NewFolder(true)
	r1 := Severity(mkRec(record.LevelInfo, "failed to upload metrics: dial tcp [::1]:4317: connection refused", record.KV{Key: "component", Val: "otel"}))
	r2 := Severity(mkRec(record.LevelInfo, "failed to upload metrics: dial tcp [::1]:4318: connection refused", record.KV{Key: "component", Val: "otel"}))
	r3 := Severity(mkRec(record.LevelInfo, "starting server", record.KV{Key: "component", Val: "otel"}))

	var emitted []record.Record
	for _, r := range []record.Record{r1, r2, r3} {
		emitted = append(emitted, f.Add(r)...)
	}
	emitted = append(emitted, f.Flush()...)

	if len(emitted) != 2 {
		t.Fatalf("emitted %d records, want 2 (one folded run + one distinct): %+v", len(emitted), emitted)
	}
	if emitted[0].Repeat != 2 {
		t.Errorf("folded run Repeat = %d, want 2", emitted[0].Repeat)
	}
	if emitted[1].Repeat != 1 {
		t.Errorf("distinct record Repeat = %d, want 1", emitted[1].Repeat)
	}
}

func TestFolderCollapsesInterleavedRuns(t *testing.T) {
	f := NewFolder(true)
	a := func() record.Record {
		return Severity(mkRec(record.LevelInfo, "no booking settings for location; returning no slots", record.KV{Key: "service", Val: "booking"}))
	}
	b := func() record.Record {
		return Severity(mkRec(record.LevelInfo, "finished call", record.KV{Key: "service", Val: "booking"}))
	}

	var emitted []record.Record
	for _, r := range []record.Record{a(), b(), a(), b(), a(), b()} {
		emitted = append(emitted, f.Add(r)...)
	}
	emitted = append(emitted, f.Flush()...)

	if len(emitted) != 2 {
		t.Fatalf("emitted %d records, want 2 (one folded run per interleaved template): %+v", len(emitted), emitted)
	}
	if emitted[0].Message != "no booking settings for location; returning no slots" || emitted[0].Repeat != 3 {
		t.Errorf("first run = %q ×%d, want %q ×3", emitted[0].Message, emitted[0].Repeat, "no booking settings for location; returning no slots")
	}
	if emitted[1].Message != "finished call" || emitted[1].Repeat != 3 {
		t.Errorf("second run = %q ×%d, want %q ×3", emitted[1].Message, emitted[1].Repeat, "finished call")
	}
}

func TestFolderFlushesRunEndedByWindow(t *testing.T) {
	f := NewFolder(true)
	rare := Severity(mkRec(record.LevelInfo, "rare event", record.KV{Key: "service", Val: "booking"}))
	storm := func() record.Record {
		return Severity(mkRec(record.LevelInfo, "finished call", record.KV{Key: "service", Val: "booking"}))
	}

	var emitted []record.Record
	emitted = append(emitted, f.Add(rare)...)
	for range foldWindow + 1 {
		emitted = append(emitted, f.Add(storm())...)
	}
	// The rare run has not matched within foldWindow, so it flushed before Flush.
	beforeFlush := len(emitted)
	emitted = append(emitted, f.Flush()...)

	if beforeFlush != 1 {
		t.Fatalf("emitted %d records before Flush, want 1 (the window-ended rare run): %+v", beforeFlush, emitted[:beforeFlush])
	}
	if len(emitted) != 2 {
		t.Fatalf("emitted %d records total, want 2: %+v", len(emitted), emitted)
	}
	if emitted[0].Message != "rare event" || emitted[0].Repeat != 1 {
		t.Errorf("first run = %q ×%d, want %q ×1", emitted[0].Message, emitted[0].Repeat, "rare event")
	}
	if emitted[1].Message != "finished call" || emitted[1].Repeat != foldWindow+1 {
		t.Errorf("second run = %q ×%d, want %q ×%d", emitted[1].Message, emitted[1].Repeat, "finished call", foldWindow+1)
	}
}

func TestFolderDisabledEmitsEverything(t *testing.T) {
	f := NewFolder(false)
	r := Severity(mkRec(record.LevelInfo, "same", record.KV{Key: "component", Val: "x"}))

	var emitted []record.Record
	for range 3 {
		emitted = append(emitted, f.Add(r)...)
	}
	emitted = append(emitted, f.Flush()...)

	if len(emitted) != 3 {
		t.Errorf("folding disabled: emitted %d records, want 3", len(emitted))
	}
}
