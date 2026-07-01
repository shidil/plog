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
		// The grammar falls back to a synthetic header when the value carries no
		// text of its own; for a field-borne trace the record's message is the
		// real summary, so prefer it in that case.
		if rec.Message != "" && !hasLeadingText(val) {
			st.Header = rec.Message
		}
		rec.Stack = st
		rec.Fields = append(rec.Fields[:i:i], rec.Fields[i+1:]...)
		return rec
	}
	return rec
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
