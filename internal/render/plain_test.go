package render

import (
	"strings"
	"testing"
	"time"

	"github.com/shidil/plog/internal/record"
	"github.com/shidil/plog/internal/summary"
)

func renderOne(t *testing.T, rec record.Record, cfg PlainConfig) string {
	t.Helper()
	var b strings.Builder
	if err := NewPlain(&b, cfg).Render(rec); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return b.String()
}

func TestRenderPassthroughVerbatim(t *testing.T) {
	got := renderOne(t, record.Record{Raw: "not json — keep me as-is", Parsed: false}, PlainConfig{})
	if got != "not json — keep me as-is\n" {
		t.Errorf("passthrough = %q, want the raw line unchanged", got)
	}
}

func TestRenderBadgeShowsRerank(t *testing.T) {
	rec := record.Record{
		Time:      time.Date(2026, 6, 29, 4, 29, 1, 0, time.UTC),
		Level:     record.LevelInfo,
		Effective: record.LevelError,
		Message:   "boom",
		Parsed:    true,
		Repeat:    1,
	}
	got := renderOne(t, rec, PlainConfig{})

	if !strings.Contains(got, "INFO→ERR") {
		t.Errorf("output %q missing re-rank badge INFO→ERR", got)
	}
	if !strings.Contains(got, "04:29:01") {
		t.Errorf("output %q missing HH:MM:SS timestamp", got)
	}
}

func TestRenderNoRerankNoArrow(t *testing.T) {
	rec := record.Record{
		Level:     record.LevelInfo,
		Effective: record.LevelInfo,
		Message:   "fine",
		Parsed:    true,
	}
	got := renderOne(t, rec, PlainConfig{})
	if strings.Contains(got, "→") {
		t.Errorf("output %q should not contain a re-rank arrow when level is unchanged", got)
	}
}

func TestRenderFoldCount(t *testing.T) {
	rec := record.Record{Level: record.LevelWarn, Effective: record.LevelWarn, Message: "spam", Parsed: true, Repeat: 3}
	got := renderOne(t, rec, PlainConfig{})
	if !strings.Contains(got, "×3") {
		t.Errorf("output %q missing fold count ×3", got)
	}
}

func TestRenderDemotedFieldsTrailSalient(t *testing.T) {
	rec := record.Record{
		Level:     record.LevelInfo,
		Effective: record.LevelInfo,
		Message:   "finished call",
		Parsed:    true,
		Fields: []record.KV{
			{Key: "service", Val: "location", Demoted: true},
			{Key: "rpc.method", Val: "ResolveLocationSlug"},
			{Key: "rpc.status", Val: "ok"},
		},
	}
	got := renderOne(t, rec, PlainConfig{})

	method := strings.Index(got, "rpc.method=")
	demoted := strings.Index(got, "·service=")
	if method < 0 || demoted < 0 {
		t.Fatalf("output missing expected fields:\n%s", got)
	}
	if method > demoted {
		t.Errorf("demoted field should trail salient fields:\n%s", got)
	}
}

func TestRenderStackFoldsFrameworkSurfacesProject(t *testing.T) {
	rec := record.Record{
		Level:     record.LevelInfo,
		Effective: record.LevelError,
		Message:   "panic",
		Parsed:    true,
		Stack: &record.StackTrace{
			Header: "http: panic serving: nil pointer dereference",
			Frames: []record.Frame{
				{Func: "net/http.(*conn).serve", File: "net/http/server.go", Line: 1907, Kind: record.FrameStdlib},
				{Func: "go.opentelemetry.io/otel/sdk/trace.(*recordingSpan).End", File: "trace/span.go", Line: 528, Kind: record.FrameThirdParty},
				{Func: "github.com/example/storefront/internal/rpc/location/v1beta1.(*locationService).ResolveLocationSlug", File: "internal/rpc/location/v1beta1/location_rpc.go", Line: 72, Kind: record.FrameProject},
			},
		},
	}
	got := renderOne(t, rec, PlainConfig{})

	if !strings.Contains(got, "► location_rpc.go:72") {
		t.Errorf("output missing surfaced project frame:\n%s", got)
	}
	if !strings.Contains(got, "2 framework frames") {
		t.Errorf("output did not fold the two framework frames:\n%s", got)
	}
	if strings.Contains(got, "server.go:1907") {
		t.Errorf("framework frame should be folded, not shown:\n%s", got)
	}
}

// fakeLinker links a fixed file, standing in for internal/link so the render
// test does not touch the filesystem.
type fakeLinker struct{ file, uri string }

func (l fakeLinker) FrameURI(f record.Frame) string {
	if f.File == l.file {
		return l.uri
	}
	return ""
}

// TestRenderProjectFrameHyperlinked checks that a configured linker wraps a
// resolvable project frame's location in an OSC 8 hyperlink, and leaves an
// unresolvable framework frame (under expand-stack) as plain text.
func TestRenderProjectFrameHyperlinked(t *testing.T) {
	rec := record.Record{
		Level:     record.LevelError,
		Effective: record.LevelError,
		Message:   "panic",
		Parsed:    true,
		Stack: &record.StackTrace{
			Header: "boom",
			Frames: []record.Frame{
				{Func: "net/http.(*conn).serve", File: "net/http/server.go", Line: 1907, Kind: record.FrameStdlib},
				{Func: "storefront/rpc.Resolve", File: "internal/rpc/location_rpc.go", Line: 72, Kind: record.FrameProject},
			},
		},
	}
	linker := fakeLinker{file: "internal/rpc/location_rpc.go", uri: "vscode://file/src/internal/rpc/location_rpc.go:72"}
	got := renderOne(t, rec, PlainConfig{ExpandStack: true, Link: linker})

	wantLink := "\x1b]8;;vscode://file/src/internal/rpc/location_rpc.go:72\x1b\\"
	if !strings.Contains(got, wantLink) {
		t.Errorf("project frame not wrapped in an OSC 8 hyperlink:\n%q", got)
	}
	// Exactly one hyperlink: the resolvable project frame. The framework frame
	// does not resolve, so it adds no escape. Each link is one open + one close
	// marker, so a single link means exactly two "\x1b]8;;" markers.
	if n := strings.Count(got, "\x1b]8;;"); n != 2 {
		t.Errorf("want exactly one hyperlink (2 OSC 8 markers), got %d:\n%q", n, got)
	}
}

// TestRenderNoLinkerNoEscape checks that without a linker the stack renders with
// no OSC 8 escapes at all.
func TestRenderNoLinkerNoEscape(t *testing.T) {
	rec := record.Record{
		Level:     record.LevelError,
		Effective: record.LevelError,
		Parsed:    true,
		Stack: &record.StackTrace{
			Header: "boom",
			Frames: []record.Frame{
				{Func: "storefront/rpc.Resolve", File: "internal/rpc/location_rpc.go", Line: 72, Kind: record.FrameProject},
			},
		},
	}
	got := renderOne(t, rec, PlainConfig{})
	if strings.Contains(got, "\x1b]8;;") {
		t.Errorf("no linker configured, but output carries an OSC 8 escape:\n%q", got)
	}
}

func TestRenderExpandStackShowsAllFrames(t *testing.T) {
	rec := record.Record{
		Level:     record.LevelError,
		Effective: record.LevelError,
		Parsed:    true,
		Stack: &record.StackTrace{
			Header: "panic",
			Frames: []record.Frame{
				{Func: "net/http.(*conn).serve", File: "net/http/server.go", Line: 1907, Kind: record.FrameStdlib},
			},
		},
	}
	got := renderOne(t, rec, PlainConfig{ExpandStack: true})
	if !strings.Contains(got, "server.go:1907") {
		t.Errorf("expand-stack should show every frame:\n%s", got)
	}
	if strings.Contains(got, "framework frames") {
		t.Errorf("expand-stack should not fold frames:\n%s", got)
	}
}

func TestRenderSummaryFooterShape(t *testing.T) {
	rep := summary.Report{
		Errors: 4, UniqueErrors: 2,
		Warns: 1, UniqueWarns: 1,
		Infos:       37,
		Passthrough: 3,
		First:       time.Date(2026, 7, 21, 14, 29, 1, 0, time.UTC),
		Last:        time.Date(2026, 7, 21, 14, 32, 47, 0, time.UTC),
		TopErrors: []summary.Line{
			{Count: 3, Message: "Failed to write image to cache"},
			{Count: 1, Message: "unhandledRejection: EACCES"},
		},
		TopWarns: []summary.Line{{Count: 1, Message: "falling back to unoptimized response"}},
	}

	var b strings.Builder
	if err := NewPlain(&b, PlainConfig{}).RenderSummary(rep); err != nil {
		t.Fatalf("RenderSummary: %v", err)
	}

	want := "── summary " + strings.Repeat("─", 45) + "\n" +
		"4 errors (2 unique) · 1 warn (1 unique) · 37 info · 3 passthrough\n" +
		"span 14:29:01–14:32:47\n" +
		"\n" +
		"  ERR  ×3  Failed to write image to cache\n" +
		"  ERR  ×1  unhandledRejection: EACCES\n" +
		"  WARN ×1  falling back to unoptimized response\n"
	if got := b.String(); got != want {
		t.Errorf("RenderSummary output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderSummaryZeroErrorsIsTheVerdict(t *testing.T) {
	rep := summary.Report{Infos: 12}

	var b strings.Builder
	if err := NewPlain(&b, PlainConfig{}).RenderSummary(rep); err != nil {
		t.Fatalf("RenderSummary: %v", err)
	}

	want := "── summary " + strings.Repeat("─", 45) + "\n" +
		"0 errors · 0 warns · 12 info\n"
	if got := b.String(); got != want {
		t.Errorf("RenderSummary output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderSummaryOverflowLinesAreExplicit(t *testing.T) {
	rep := summary.Report{
		Errors: 30, UniqueErrors: 10,
		TopErrors:  []summary.Line{{Count: 21, Message: "boom"}},
		MoreErrors: 9,
		Untracked:  12,
	}

	var b strings.Builder
	if err := NewPlain(&b, PlainConfig{}).RenderSummary(rep); err != nil {
		t.Fatalf("RenderSummary: %v", err)
	}

	got := b.String()
	for _, want := range []string{"… +9 more\n", "… +12 more warn/error lines beyond the tracked templates\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderSummary output missing %q:\n%s", want, got)
		}
	}
}
