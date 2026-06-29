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
