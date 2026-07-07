package parse

import (
	"testing"

	"github.com/shidil/plog/internal/record"
)

func TestLinePylog(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantParsed bool
		wantLevel  record.Level
		wantMsg    string
		wantFields []record.KV
	}{
		{
			name:       "asctime_name_level_message",
			in:         "2023-10-05 14:23:01,123 - myapp.service - INFO - Something happened",
			wantParsed: true,
			wantLevel:  record.LevelInfo,
			wantMsg:    "Something happened",
			wantFields: []record.KV{{Key: "logger", Val: "myapp.service"}},
		},
		{
			name:       "message_keeps_its_own_separator",
			in:         "2023-10-05 14:23:02,000 - root - WARNING - retrying - attempt 2",
			wantParsed: true,
			wantLevel:  record.LevelWarn,
			wantMsg:    "retrying - attempt 2",
			wantFields: []record.KV{{Key: "logger", Val: "root"}},
		},
		{
			name:       "critical_maps_to_error",
			in:         "2023-10-05 14:23:03,500 - db - CRITICAL - connection lost",
			wantParsed: true,
			wantLevel:  record.LevelError,
			wantMsg:    "connection lost",
			wantFields: []record.KV{{Key: "logger", Val: "db"}},
		},
		{
			name:       "unknown_level_field_passthrough",
			in:         "2023-10-05 14:23:04,000 - svc - NOTALEVEL - text",
			wantParsed: false,
		},
		{
			name:       "prose_with_leading_date_passthrough",
			in:         "2023-10-05 14:23:01,123 shipped the release today",
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

func TestPylogParsesTime(t *testing.T) {
	got := Line("2023-10-05 14:23:01,123 - myapp - INFO - up")
	if got.Time.IsZero() {
		t.Fatalf("pylog time not parsed; got zero time")
	}
	if y := got.Time.Year(); y != 2023 {
		t.Errorf("pylog time year = %d, want 2023", y)
	}
}

func TestPylogForcedFormat(t *testing.T) {
	line := "2023-10-05 14:23:01,123 - myapp - INFO - up"
	if got := LineAs(line, FormatPylog); !got.Parsed {
		t.Errorf("LineAs(%q, FormatPylog).Parsed = false, want parsed", line)
	}
	if got := LineAs(line, FormatJSON); got.Parsed {
		t.Errorf("LineAs(%q, FormatJSON).Parsed = true, want passthrough (not JSON)", line)
	}
}
