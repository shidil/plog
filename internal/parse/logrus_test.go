package parse

import (
	"testing"

	"github.com/shidil/plog/internal/record"
)

// esc wraps s in an ANSI SGR color sequence, mimicking how logrus colors the
// level in its TTY output.
func esc(color int, s string) string {
	return "\x1b[" + itoa(color) + "m" + s + "\x1b[0m"
}

func itoa(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestLineLogrus(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantParsed bool
		wantLevel  record.Level
		wantMsg    string
		wantFields []record.KV
	}{
		{
			name:       "elapsed_counter_and_fields",
			in:         esc(36, "INFO") + "[0000] A walrus appears                          animal=walrus number=8",
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantMsg:    "A walrus appears",
			// The elapsed counter is kept as a field, not mistaken for a clock.
			wantFields: []record.KV{{Key: "elapsed", Val: "0000"}, {Key: "animal", Val: "walrus"}, {Key: "number", Val: "8"}},
		},
		{
			name:       "full_timestamp_becomes_time",
			in:         esc(31, "ERRO") + "[2023-10-05T14:23:01Z] connection refused                 retries=3",
			wantParsed: true,
			wantLevel:  record.LevelError,
			wantMsg:    "connection refused",
			wantFields: []record.KV{{Key: "retries", Val: "3"}},
		},
		{
			name:       "no_trailing_fields",
			in:         esc(36, "INFO") + "[0000] server started",
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantMsg:    "server started",
			wantFields: []record.KV{{Key: "elapsed", Val: "0000"}},
		},
		{
			name:       "non_truncated_level",
			in:         esc(37, "DEBUG") + "[0000] verbose detail",
			wantParsed: true,
			wantLevel:  record.LevelDebug,
			wantMsg:    "verbose detail",
			wantFields: []record.KV{{Key: "elapsed", Val: "0000"}},
		},
		{
			name:       "bracket_not_digit_passthrough",
			in:         "ERROR[handler] something failed",
			wantParsed: false,
		},
		{
			name:       "unknown_prefix_passthrough",
			in:         "TODO[0001] not a level",
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

func TestLogrusFullTimestampParsesTime(t *testing.T) {
	got := Line(esc(31, "ERRO") + "[2023-10-05T14:23:01Z] boom")
	if got.Time.IsZero() {
		t.Fatalf("logrus full timestamp not parsed; got zero time")
	}
	if y := got.Time.Year(); y != 2023 {
		t.Errorf("logrus time year = %d, want 2023", y)
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"\x1b[36mINFO\x1b[0m", "INFO"},
		{"\x1b[1;31mERR\x1b[0m done", "ERR done"},
		{"no escapes here", "no escapes here"},
	}
	for _, tc := range tests {
		if got := stripANSI(tc.in); got != tc.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLogrusForcedFormat(t *testing.T) {
	line := esc(36, "INFO") + "[0000] hi"
	if got := LineAs(line, FormatLogrus); !got.Parsed {
		t.Errorf("LineAs(%q, FormatLogrus).Parsed = false, want parsed", line)
	}
}
