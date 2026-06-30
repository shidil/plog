package parse

import (
	"testing"

	"github.com/shidil/plog/internal/record"
)

func TestLine(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantParsed bool
		wantLevel  record.Level
		wantMsg    string
		wantFields []record.KV
	}{
		{
			name:       "structured_info",
			in:         `{"time":"2026-06-29T04:28:53.68Z","level":"INFO","msg":"starting","component":"http","addr":":4000"}`,
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantMsg:    "starting",
			wantFields: []record.KV{{Key: "component", Val: "http"}, {Key: "addr", Val: ":4000"}},
		},
		{
			name:       "preserves_field_order_and_scalars",
			in:         `{"level":"error","msg":"boom","count":3,"ok":false,"meta":{"b":2,"a":1}}`,
			wantParsed: true,
			wantLevel:  record.LevelError,
			wantMsg:    "boom",
			wantFields: []record.KV{{Key: "count", Val: "3"}, {Key: "ok", Val: "false"}, {Key: "meta", Val: `{"b":2,"a":1}`}},
		},
		{
			name:       "plain_text_passthrough",
			in:         "not json at all",
			wantParsed: false,
		},
		{
			name:       "malformed_json_passthrough",
			in:         `{"level":"INFO","msg":`,
			wantParsed: false,
		},
		{
			name:       "trailing_data_after_object_passthrough",
			in:         `{"msg":"a"} {"msg":"b"}`,
			wantParsed: false,
		},
		{
			name:       "empty_line_passthrough",
			in:         "",
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
			if got.Effective != tc.wantLevel {
				t.Errorf("Line(%q).Effective = %v, want %v (should equal declared before enrichment)", tc.in, got.Effective, tc.wantLevel)
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

func TestLineParsesTime(t *testing.T) {
	got := Line(`{"time":"2026-06-29T04:28:53.68Z","level":"INFO","msg":"x"}`)
	if got.Time.IsZero() {
		t.Fatalf("Line did not parse time field; got zero time")
	}
	if y := got.Time.Year(); y != 2026 {
		t.Errorf("parsed time year = %d, want 2026", y)
	}
}

func TestLineCanonAliases(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantLevel record.Level
		wantMsg   string
		wantYear  int // 0 => expect zero time
	}{
		{
			name:      "zap_style_ts_and_message",
			in:        `{"ts":"2026-06-29T04:28:53Z","severity":"warn","message":"hi"}`,
			wantLevel: record.LevelWarn,
			wantMsg:   "hi",
			wantYear:  2026,
		},
		{
			name:      "unix_seconds_timestamp",
			in:        `{"timestamp":"1751171333","level":"info","msg":"x"}`,
			wantLevel: record.LevelInfo,
			wantMsg:   "x",
			wantYear:  2025,
		},
		{
			name:      "zoneless_time_layout",
			in:        `{"time":"2026-06-29T04:28:53","level":"error","msg":"y"}`,
			wantLevel: record.LevelError,
			wantMsg:   "y",
			wantYear:  2026,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Line(tc.in)
			if !got.Parsed {
				t.Fatalf("Line(%q).Parsed = false, want true", tc.in)
			}
			if got.Level != tc.wantLevel {
				t.Errorf("Line(%q).Level = %v, want %v", tc.in, got.Level, tc.wantLevel)
			}
			if got.Message != tc.wantMsg {
				t.Errorf("Line(%q).Message = %q, want %q", tc.in, got.Message, tc.wantMsg)
			}
			if tc.wantYear == 0 {
				if !got.Time.IsZero() {
					t.Errorf("Line(%q).Time = %v, want zero", tc.in, got.Time)
				}
			} else if y := got.Time.Year(); y != tc.wantYear {
				t.Errorf("Line(%q).Time.Year() = %d, want %d", tc.in, y, tc.wantYear)
			}
		})
	}
}

func TestLineUnknownLevelKeptAsField(t *testing.T) {
	// A level value that is not a recognized severity stays an ordinary field
	// rather than being dropped, so no information is lost.
	got := Line(`{"level":"banana","msg":"x"}`)
	if got.Level != record.LevelUnknown {
		t.Errorf("Line.Level = %v, want LevelUnknown", got.Level)
	}
	want := []record.KV{{Key: "level", Val: "banana"}}
	if !equalFields(got.Fields, want) {
		t.Errorf("Line.Fields = %+v, want %+v", got.Fields, want)
	}
}

func equalFields(got, want []record.KV) bool {
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

func FuzzParseLine(f *testing.F) {
	seeds := []string{
		"",
		"plain text",
		`{"level":"INFO","msg":"ok"}`,
		`{"time":"2026-06-29T04:28:53Z","level":"warn","msg":"x","n":1}`,
		`{"msg":"a"} trailing`,
		`{"broken":`,
		"{\x00\x01}",
		`{"nested":{"deep":[1,{"x":true}]}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := Line(in)
		// Invariant: the original line is always retained verbatim.
		if got.Raw != in {
			t.Errorf("Line(%q).Raw = %q, want input preserved", in, got.Raw)
		}
		// Invariant: a passthrough record carries no derived data.
		if !got.Parsed && (got.Message != "" || len(got.Fields) != 0 || !got.Time.IsZero()) {
			t.Errorf("Line(%q) not parsed but has derived fields: %+v", in, got)
		}
	})
}
