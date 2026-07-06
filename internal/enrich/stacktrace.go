package enrich

import (
	"strings"

	"github.com/shidil/plog/internal/record"
)

// traceMarkers are cheap substring signals that a value may embed a stack trace.
// looksLikeTrace uses them to skip the (comparatively costly) grammars for the
// vast majority of field values that are ordinary data. Node/Bun markers are
// listed too so this guard need not change as grammars are added; a value they
// admit that no grammar can parse is simply left untouched.
var traceMarkers = []string{"goroutine ", "\n\tat ", "\n    at "}

// Stack returns a copy of rec with Stack populated when it embeds a recognized
// panic/goroutine trace. The trace is looked for first in the message, then in
// the field values (as structured loggers emit it) — the first field that parses
// is consumed so it is not also rendered raw. module is the import-path prefix
// treated as project code; pass "" to disable project highlighting. Records
// without a trace, or already carrying one, are returned unchanged.
func Stack(rec record.Record, module string) record.Record {
	if !rec.Parsed || rec.Stack != nil {
		return rec
	}
	if st := detectAndParse(rec.Message, module); st != nil {
		st.Header = pickHeader(st, rec.Message, rec.Message)
		rec.Stack = st
		return rec
	}
	for i := range rec.Fields {
		val := rec.Fields[i].Val
		if !looksLikeTrace(val) {
			continue
		}
		st := detectAndParse(val, module)
		if st == nil {
			continue
		}
		st.Header = pickHeader(st, val, rec.Message)
		rec.Stack = st
		rec.Fields = append(rec.Fields[:i:i], rec.Fields[i+1:]...)
		return rec
	}
	return rec
}

// pickHeader chooses a lifted trace's display header: the grammar's own header
// when it extracted one, else the record's message when the trace came from a
// separate field (so the message is a distinct summary), else the trace's first
// non-empty line.
func pickHeader(st *record.StackTrace, source, message string) string {
	if st.Header != "" {
		return st.Header
	}
	if message != "" && message != source {
		return message
	}
	return firstLine(source)
}

// firstLine returns the first non-empty, trimmed line of s.
func firstLine(s string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(ln); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// looksLikeTrace reports whether s is worth attempting to parse as a stack
// trace: it must span multiple lines and contain a known language pre-marker.
// It is a fast reject, not a parser — a true result only earns a parse attempt.
func looksLikeTrace(s string) bool {
	if !strings.ContainsRune(s, '\n') {
		return false
	}
	for _, m := range traceMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
