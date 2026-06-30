package parse

import (
	"strings"
	"testing"

	"github.com/shidil/plog/internal/record"
)

func TestLineLogfmt(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantParsed bool
		wantLevel  record.Level
		wantMsg    string
		wantFields []record.KV
	}{
		{
			name:       "buildkit_quoted_msg",
			in:         `time="2026-06-26T23:24:29Z" level=warning msg="skipping containerd worker"`,
			wantParsed: true,
			wantLevel:  record.LevelWarn,
			wantMsg:    "skipping containerd worker",
		},
		{
			name:       "escaped_quotes_and_equals_inside_value",
			in:         `level=info msg="found 1 workers, default=\"abc\""`,
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantMsg:    `found 1 workers, default="abc"`,
		},
		{
			name:       "extra_fields_preserve_order",
			in:         `level=error msg=boom rpc.method=Resolve rpc.duration=5ms`,
			wantParsed: true,
			wantLevel:  record.LevelError,
			wantMsg:    "boom",
			wantFields: []record.KV{{Key: "rpc.method", Val: "Resolve"}, {Key: "rpc.duration", Val: "5ms"}},
		},
		{
			name:       "equals_inside_unquoted_value_splits_on_first",
			in:         `level=info url=http://x?a=b&c=d`,
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantFields: []record.KV{{Key: "url", Val: "http://x?a=b&c=d"}},
		},
		{
			name:       "bare_key_becomes_empty_value",
			in:         `level=info retrying msg=again`,
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantMsg:    "again",
			wantFields: []record.KV{{Key: "retrying", Val: ""}},
		},
		{
			name:       "plain_text_passthrough",
			in:         "buildkitd: got 1 SIGTERM/SIGINTs, forcing shutdown",
			wantParsed: false,
		},
		{
			name:       "stack_frame_line_passthrough",
			in:         "github.com/moby/buildkit/util/appcontext.Context.func1.1",
			wantParsed: false,
		},
		{
			name:       "unterminated_quote_passthrough",
			in:         `level=info msg="never closed`,
			wantParsed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Line(tc.in)

			if got.Parsed != tc.wantParsed {
				t.Fatalf("Line(%q).Parsed = %v, want %v", tc.in, got.Parsed, tc.wantParsed)
			}
			if got.Raw != tc.in {
				t.Errorf("Line(%q).Raw = %q, want the original line", tc.in, got.Raw)
			}
			if !tc.wantParsed {
				return
			}
			if got.Level != tc.wantLevel {
				t.Errorf("Line(%q).Level = %v, want %v", tc.in, got.Level, tc.wantLevel)
			}
			if got.Message != tc.wantMsg {
				t.Errorf("Line(%q).Message = %q, want %q", tc.in, got.Message, tc.wantMsg)
			}
			if !equalFields(got.Fields, tc.wantFields) {
				t.Errorf("Line(%q).Fields = %+v, want %+v", tc.in, got.Fields, tc.wantFields)
			}
		})
	}
}

func TestLogfmtParsesTimeAndStack(t *testing.T) {
	got := Line(`time="2026-06-26T23:24:29Z" level=info msg="up"`)
	if got.Time.IsZero() {
		t.Errorf("logfmt time field not parsed; got zero time")
	}
	if y := got.Time.Year(); y != 2026 {
		t.Errorf("parsed time year = %d, want 2026", y)
	}

	// An escaped newline inside a quoted msg must survive as a real newline so
	// the Stack enrichment (which scans Message) can find an embedded trace.
	got = Line(`level=error msg="boom\n\tgoroutine 1"`)
	if !strings.Contains(got.Message, "\n") {
		t.Errorf("Message = %q, want an embedded newline from the \\n escape", got.Message)
	}
}

func TestLineAsForcesFormat(t *testing.T) {
	logfmtLine := `level=info msg=hi`

	if got := LineAs(logfmtLine, FormatText); got.Parsed {
		t.Errorf("LineAs(%q, FormatText).Parsed = true, want passthrough", logfmtLine)
	}
	if got := LineAs(logfmtLine, FormatJSON); got.Parsed {
		t.Errorf("LineAs(%q, FormatJSON).Parsed = true, want passthrough (not JSON)", logfmtLine)
	}
	if got := LineAs(logfmtLine, FormatLogfmt); !got.Parsed {
		t.Errorf("LineAs(%q, FormatLogfmt).Parsed = false, want parsed", logfmtLine)
	}
}

func FuzzParseLogfmt(f *testing.F) {
	seeds := []string{
		"",
		"plain text no pairs",
		`level=info msg=hi`,
		`time="2026-06-26T23:24:29Z" level=warning msg="quoted value"`,
		`msg="escaped \" quote" k=v`,
		`bare`,
		`a= =b key`,
		`url=http://x?a=b`,
		"k=\"unterminated",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := LineAs(in, FormatLogfmt)
		// Invariant: the original line is always retained verbatim.
		if got.Raw != in {
			t.Errorf("LineAs(%q).Raw = %q, want input preserved", in, got.Raw)
		}
		// Invariant: a passthrough record carries no derived data.
		if !got.Parsed && (got.Message != "" || len(got.Fields) != 0 || !got.Time.IsZero()) {
			t.Errorf("LineAs(%q) not parsed but has derived fields: %+v", in, got)
		}
	})
}
