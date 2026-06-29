package filter

import (
	"testing"

	"github.com/shidil/plog/internal/record"
)

// parsed builds a parsed record with the effective level set, as the enrich
// stages would produce before filtering.
func parsed(level record.Level, msg string, fields ...record.KV) record.Record {
	return record.Record{
		Level:     level,
		Effective: level,
		Message:   msg,
		Fields:    fields,
		Parsed:    true,
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New("verbose", "", nil); err == nil {
		t.Error("New accepted an unknown min-level")
	}
	if _, err := New("", "(unclosed", nil); err == nil {
		t.Error("New accepted an invalid grep pattern")
	}
	if _, err := New("", "", []string{"noequals"}); err == nil {
		t.Error("New accepted a --field spec without '='")
	}
	if _, err := New("", "", []string{"=value"}); err == nil {
		t.Error("New accepted a --field spec with an empty key")
	}
	if _, err := New("", "", nil); err != nil {
		t.Errorf("New rejected the all-empty (match-all) config: %v", err)
	}
}

func TestMatch(t *testing.T) {
	tests := []struct {
		name     string
		minLevel string
		grep     string
		fields   []string
		rec      record.Record
		want     bool
	}{
		{
			name:     "min_level_blocks_below_threshold",
			minLevel: "warn",
			rec:      parsed(record.LevelInfo, "ok"),
			want:     false,
		},
		{
			name:     "min_level_passes_at_threshold",
			minLevel: "warn",
			rec:      parsed(record.LevelWarn, "careful"),
			want:     true,
		},
		{
			name:     "min_level_uses_effective_not_declared",
			minLevel: "error",
			// declared INFO, re-ranked to ERROR by the severity stage upstream.
			rec:  record.Record{Level: record.LevelInfo, Effective: record.LevelError, Message: "panic", Parsed: true},
			want: true,
		},
		{
			name: "grep_matches_message",
			grep: "ResolveLocationSlug",
			rec:  parsed(record.LevelInfo, "finished call ResolveLocationSlug"),
			want: true,
		},
		{
			name: "grep_matches_field_value",
			grep: "invalid_argument",
			rec:  parsed(record.LevelInfo, "finished call", record.KV{Key: "error", Val: "invalid_argument: bad slug"}),
			want: true,
		},
		{
			name: "grep_alternation",
			grep: "slug|timeout",
			rec:  parsed(record.LevelInfo, "request timeout"),
			want: true,
		},
		{
			name: "grep_no_match_filtered",
			grep: "panic",
			rec:  parsed(record.LevelInfo, "everything fine"),
			want: false,
		},
		{
			name:   "field_substring_case_insensitive",
			fields: []string{"rpc.method=resolve"},
			rec:    parsed(record.LevelInfo, "finished call", record.KV{Key: "rpc.method", Val: "ResolveLocationSlug"}),
			want:   true,
		},
		{
			name:   "field_value_mismatch_filtered",
			fields: []string{"rpc.status=ok"},
			rec:    parsed(record.LevelInfo, "x", record.KV{Key: "rpc.status", Val: "invalid_argument"}),
			want:   false,
		},
		{
			name:   "field_key_absent_filtered",
			fields: []string{"logger=auth"},
			rec:    parsed(record.LevelInfo, "x", record.KV{Key: "component", Val: "auth"}),
			want:   false,
		},
		{
			name:   "field_empty_value_requires_presence",
			fields: []string{"rpc.component="},
			rec:    parsed(record.LevelInfo, "x", record.KV{Key: "rpc.component", Val: "rpc.server"}),
			want:   true,
		},
		{
			name:   "multiple_fields_combine_with_and",
			fields: []string{"rpc.method=Resolve", "rpc.status=ok"},
			// matches method but not status => filtered.
			rec:  parsed(record.LevelInfo, "x", record.KV{Key: "rpc.method", Val: "ResolveLocationSlug"}, record.KV{Key: "rpc.status", Val: "internal"}),
			want: false,
		},
		{
			name:     "level_and_field_combine_with_and",
			minLevel: "warn",
			fields:   []string{"service=location"},
			// passes field but not min-level => filtered.
			rec:  parsed(record.LevelInfo, "x", record.KV{Key: "service", Val: "location"}),
			want: false,
		},
		{
			name: "no_filters_matches_everything",
			rec:  parsed(record.LevelDebug, "trivial"),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := New(tc.minLevel, tc.grep, tc.fields)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := f.Match(tc.rec); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchPassthrough(t *testing.T) {
	pass := record.Record{Raw: "plain text with token xyz", Parsed: false}

	// Level and field tests never hide a passthrough line.
	f, _ := New("error", "", []string{"service=location"})
	if !f.Match(pass) {
		t.Error("passthrough hidden by level/field filter, want always shown")
	}

	// grep, however, applies to the raw line.
	f, _ = New("", "xyz", nil)
	if !f.Match(pass) {
		t.Error("passthrough with matching grep was filtered")
	}
	f, _ = New("", "nomatch", nil)
	if f.Match(pass) {
		t.Error("passthrough with non-matching grep was shown")
	}
}
