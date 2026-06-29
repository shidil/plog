// Package parse converts a single raw log line into a record.Record. It
// recognizes JSON object lines (the common structured-logging shape) and
// preserves field order; anything else is returned as a passthrough record so
// a malformed or plain-text line never interrupts the stream.
package parse

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/shidil/plog/internal/record"
)

// Field keys carrying special meaning in the canonical record. Remaining keys
// are kept verbatim in Record.Fields.
const (
	keyTime  = "time"
	keyLevel = "level"
	keyMsg   = "msg"
)

// Line parses one raw log line (without trailing newline) into a Record. It
// never returns an error: an unrecognized or malformed line becomes a
// passthrough record (Parsed == false) carrying the original text in Raw.
func Line(raw string) record.Record {
	if trimmed := bytes.TrimSpace([]byte(raw)); len(trimmed) == 0 || trimmed[0] != '{' {
		return record.Record{Raw: raw}
	}
	rec, ok := decodeObject(raw)
	if !ok {
		return record.Record{Raw: raw}
	}
	rec.Raw = raw
	rec.Parsed = true
	rec.Effective = rec.Level
	return rec
}

// decodeObject walks a JSON object as a token stream, decoding each value into
// a json.RawMessage so field order is preserved and nested values stay intact.
// It reports ok == false unless the line is exactly one well-formed object.
func decodeObject(raw string) (record.Record, bool) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return record.Record{}, false
	}

	var rec record.Record
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return record.Record{}, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return record.Record{}, false
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return record.Record{}, false
		}
		assign(&rec, key, val)
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return record.Record{}, false
	}
	if _, err := dec.Token(); err != io.EOF { // nothing may follow the object
		return record.Record{}, false
	}
	return rec, true
}

// assign routes a value to the typed field it maps to, or appends it to Fields
// in source order.
func assign(rec *record.Record, key string, val json.RawMessage) {
	switch key {
	case keyTime:
		if s, ok := asString(val); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				rec.Time = t
				return
			}
		}
		rec.Fields = append(rec.Fields, record.KV{Key: key, Val: render(val)})
	case keyLevel:
		if s, ok := asString(val); ok {
			rec.Level = record.ParseLevel(s)
			return
		}
		rec.Fields = append(rec.Fields, record.KV{Key: key, Val: render(val)})
	case keyMsg:
		rec.Message = render(val)
	default:
		rec.Fields = append(rec.Fields, record.KV{Key: key, Val: render(val)})
	}
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
