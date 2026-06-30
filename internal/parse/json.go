package parse

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// sniffJSON reports whether trimmed looks like a JSON object.
func sniffJSON(trimmed string) bool {
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// parseJSON walks a JSON object as a token stream, decoding each value into a
// json.RawMessage so field order is preserved and nested values stay intact. It
// reports ok == false unless the line is exactly one well-formed object.
func parseJSON(raw string) ([]pair, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}

	var pairs []pair
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, false
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, false
		}
		pairs = append(pairs, pair{key: key, val: render(val)})
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF { // nothing may follow the object
		return nil, false
	}
	return pairs, true
}

// asString reports whether raw is a JSON string and, if so, its decoded value.
func asString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// render produces the display text for a value: the unquoted contents for a
// JSON string, otherwise the compacted JSON (numbers, bools, nested composites).
func render(raw json.RawMessage) string {
	if s, ok := asString(raw); ok {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
