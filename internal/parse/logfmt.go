package parse

import (
	"strconv"
	"strings"
)

// sniffLogfmt reports whether trimmed looks like logfmt: its first
// whitespace-delimited token must be `key=` with an identifier-ish key. Real
// logfmt lines always lead with a pair (time=, level=, ts=); prose almost never
// starts with word=, which keeps lines like "buildkitd: got SIGTERM" out. A
// line that slips past the sniff but yields no pair still falls through to
// passthrough in parseLogfmt.
func sniffLogfmt(trimmed string) bool {
	tok := trimmed
	if i := strings.IndexByte(tok, ' '); i >= 0 {
		tok = tok[:i]
	}
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	return isIdentKey(tok[:eq])
}

// isIdentKey reports whether s is a plausible logfmt key: a non-empty run of
// letters, digits, and the punctuation common in structured keys (rpc.method,
// @timestamp, trace-id, http/status).
func isIdentKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '/', r == '@', r == ':':
		default:
			return false
		}
	}
	return true
}

// parseLogfmt scans space-separated key=value pairs in source order. A value
// may be double-quoted to contain spaces and escaped quotes; a bare key with no
// '=' becomes an empty-valued pair. The key/value split is on the first '=', so
// a value may itself contain '=' (url=http://x?a=b). It reports ok == false
// when the line yields no pair, so the caller falls through to passthrough.
func parseLogfmt(raw string) ([]pair, bool) {
	var pairs []pair
	i, n := 0, len(raw)
	for i < n {
		for i < n && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}

		keyStart := i
		for i < n && raw[i] != '=' && raw[i] != ' ' && raw[i] != '\t' {
			i++
		}
		key := raw[keyStart:i]
		if key == "" { // a stray '='; skip it to make progress
			i++
			continue
		}

		if i >= n || raw[i] != '=' { // bare key, no value
			pairs = append(pairs, pair{key: key, val: ""})
			continue
		}
		i++ // consume '='

		var val string
		if i < n && raw[i] == '"' {
			v, end, ok := scanQuoted(raw, i)
			if !ok {
				return nil, false // unterminated quote: not valid logfmt
			}
			val, i = v, end
		} else {
			valStart := i
			for i < n && raw[i] != ' ' && raw[i] != '\t' {
				i++
			}
			val = raw[valStart:i]
		}
		pairs = append(pairs, pair{key: key, val: val})
	}
	if len(pairs) == 0 {
		return nil, false
	}
	return pairs, true
}

// scanQuoted reads a double-quoted value beginning at s[start] (which must be
// '"'), honoring backslash escapes, and returns the unquoted contents and the
// index just past the closing quote. An escaped \n inside the value becomes a
// real newline, so an embedded stack trace flows into the message and reaches
// the Stack enrichment. It reports ok == false for an unterminated quote.
func scanQuoted(s string, start int) (val string, end int, ok bool) {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // skip the escaped byte
		case '"':
			lit := s[start : i+1]
			if uq, err := strconv.Unquote(lit); err == nil {
				return uq, i + 1, true
			}
			return lit[1 : len(lit)-1], i + 1, true // keep literal contents
		}
	}
	return "", 0, false
}
