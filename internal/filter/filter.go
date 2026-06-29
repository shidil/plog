// Package filter selects which records reach the renderer. It is a pure
// predicate stage, distinct from enrichment: a Filter is built once from the
// command-line flags and asked Match for each record. Selection respects the
// pipeline's sacred-passthrough rule — a non-JSON line is never dropped by a
// severity or field test it cannot be judged against.
package filter

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shidil/plog/internal/record"
)

// fieldMatch is one --field KEY=SUBSTR predicate: the record must carry KEY and
// its value must contain SUBSTR (compared case-insensitively).
type fieldMatch struct {
	key    string
	substr string // already lowercased
}

// Filter decides whether a record is displayed. The zero-value behavior (no
// flags set) matches everything, so wiring a Filter in is free when unused.
type Filter struct {
	minLevel record.Level
	hasMin   bool
	grep     *regexp.Regexp
	fields   []fieldMatch
}

// New builds a Filter from raw flag values. An empty minLevel/grep and a nil
// fields slice disable the corresponding tests. Each fields entry is a
// "key=substr" spec. New returns an error for an unknown level, an invalid grep
// pattern, or a malformed field spec, so the command can report it before
// streaming begins.
func New(minLevel, grep string, fields []string) (*Filter, error) {
	f := &Filter{}

	if minLevel != "" {
		lvl := record.ParseLevel(minLevel)
		if lvl == record.LevelUnknown {
			return nil, fmt.Errorf("unknown level %q (want debug, info, warn, or error)", minLevel)
		}
		f.minLevel = lvl
		f.hasMin = true
	}

	if grep != "" {
		re, err := regexp.Compile(grep)
		if err != nil {
			return nil, err
		}
		f.grep = re
	}

	for _, spec := range fields {
		key, substr, ok := strings.Cut(spec, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --field %q (want key=value)", spec)
		}
		f.fields = append(f.fields, fieldMatch{key: key, substr: strings.ToLower(substr)})
	}

	return f, nil
}

// Match reports whether rec should be displayed. Active tests are combined with
// AND. Passthrough records are exempt from the level and field tests (their
// severity and fields are unknowable); only --grep, matched against the raw
// line, can hide one.
func (f *Filter) Match(rec record.Record) bool {
	if !rec.Parsed {
		return f.grep == nil || f.grep.MatchString(rec.Raw)
	}

	if f.hasMin && rec.Effective < f.minLevel {
		return false
	}
	for _, fm := range f.fields {
		val, ok := fieldValue(rec, fm.key)
		if !ok || !strings.Contains(strings.ToLower(val), fm.substr) {
			return false
		}
	}
	if f.grep != nil && !f.grepParsed(rec) {
		return false
	}
	return true
}

// grepParsed reports whether the pattern matches a parsed record's visible
// content: its message (which still holds any embedded stack trace) or any
// field value.
func (f *Filter) grepParsed(rec record.Record) bool {
	if f.grep.MatchString(rec.Message) {
		return true
	}
	for _, kv := range rec.Fields {
		if f.grep.MatchString(kv.Val) {
			return true
		}
	}
	return false
}

// fieldValue returns the value of the named field and whether it was present.
func fieldValue(rec record.Record, key string) (string, bool) {
	for _, kv := range rec.Fields {
		if kv.Key == key {
			return kv.Val, true
		}
	}
	return "", false
}
