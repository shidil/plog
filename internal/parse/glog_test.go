package parse

import (
	"testing"

	"github.com/shidil/plog/internal/record"
)

func TestLineGlog(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantParsed bool
		wantLevel  record.Level
		wantMsg    string
		wantFields []record.KV
	}{
		{
			name:       "info_with_caller",
			in:         "I0605 14:23:01.123456   12345 location_rpc.go:72] resolving location slug",
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantMsg:    "resolving location slug",
			wantFields: []record.KV{{Key: "thread", Val: "12345"}, {Key: "caller", Val: "location_rpc.go:72"}},
		},
		{
			name:       "error_prefix",
			in:         "E0605 14:23:02.000001   12345 server.go:150] connection refused",
			wantParsed: true,
			wantLevel:  record.LevelError,
			wantMsg:    "connection refused",
			wantFields: []record.KV{{Key: "thread", Val: "12345"}, {Key: "caller", Val: "server.go:150"}},
		},
		{
			name:       "message_keeps_bracket_after_first",
			in:         "W0605 14:23:03.000001   12345 cache.go:5] evicted key [store:42]",
			wantParsed: true,
			wantLevel:  record.LevelWarn,
			wantMsg:    "evicted key [store:42]",
			wantFields: []record.KV{{Key: "thread", Val: "12345"}, {Key: "caller", Val: "cache.go:5"}},
		},
		{
			name:       "no_closing_bracket_passthrough",
			in:         "I0605 14:23:01.123456 12345 no bracket here",
			wantParsed: false,
		},
		{
			name:       "prose_starting_with_severity_letter_passthrough",
			in:         "Increment 0605 by one",
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

func TestGlogParsesYearlessTime(t *testing.T) {
	// glog omits the year, so the parsed time carries the month/day/clock the
	// renderer shows (HH:MM:SS) with a zero year — enough for a live tail.
	got := Line("I0605 14:23:01.123456   12345 location_rpc.go:72] hi")
	if got.Time.IsZero() {
		t.Fatalf("glog time not parsed; got zero time")
	}
	if h, m, s := got.Time.Clock(); h != 14 || m != 23 || s != 1 {
		t.Errorf("glog time clock = %02d:%02d:%02d, want 14:23:01", h, m, s)
	}
	if mo := got.Time.Month(); mo != 6 {
		t.Errorf("glog time month = %v, want June", mo)
	}
}

func TestGlogForcedFormat(t *testing.T) {
	line := "I0605 14:23:01.123456   12345 f.go:1] ok"
	if got := LineAs(line, FormatGlog); !got.Parsed {
		t.Errorf("LineAs(%q, FormatGlog).Parsed = false, want parsed", line)
	}
	if got := LineAs(line, FormatJSON); got.Parsed {
		t.Errorf("LineAs(%q, FormatJSON).Parsed = true, want passthrough (not JSON)", line)
	}
}
