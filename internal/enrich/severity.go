// Package enrich derives extra signal from a parsed record without mutating its
// source meaning: it re-ranks severity from message content, extracts embedded
// Go stack traces, and folds runs of near-identical lines. Each function is
// pure (Record in, Record out) so the stages stay trivially testable.
package enrich

import (
	"strings"

	"github.com/shidil/plog/internal/record"
)

// escalation maps a lowercase substring found in a message or error field to
// the minimum effective level it implies. Ordered from most to least severe so
// the first match wins; entries are intentionally conservative.
var escalation = []struct {
	needle string
	level  record.Level
}{
	{"panic", record.LevelError},
	{"nil pointer", record.LevelError},
	{"runtime error", record.LevelError},
	{"fatal", record.LevelError},
	{"connection refused", record.LevelWarn},
	{"unavailable", record.LevelWarn},
	{"timeout", record.LevelWarn},
	{"failed", record.LevelWarn},
	{"error", record.LevelWarn},
}

// Severity returns a copy of rec with Effective raised when the message or an
// error field signals a higher severity than the declared level. It never
// lowers severity, so an explicit ERROR stays ERROR even on a benign message.
func Severity(rec record.Record) record.Record {
	if !rec.Parsed {
		return rec
	}
	if rec.Effective == 0 {
		rec.Effective = rec.Level
	}

	haystack := strings.ToLower(rec.Message)
	if errVal := fieldValue(rec, "error"); errVal != "" {
		haystack += "\n" + strings.ToLower(errVal)
	}

	for _, e := range escalation {
		if e.level <= rec.Effective {
			continue
		}
		if strings.Contains(haystack, e.needle) {
			rec.Effective = e.level
		}
	}
	return rec
}

// fieldValue returns the value of the named field, or "" if absent.
func fieldValue(rec record.Record, key string) string {
	for _, kv := range rec.Fields {
		if kv.Key == key {
			return kv.Val
		}
	}
	return ""
}
