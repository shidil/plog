// Package parse converts a single raw log line into a record.Record. It
// recognizes JSON object lines and logfmt (key=value) lines, preserving field
// order; anything else is returned as a passthrough record so a malformed or
// plain-text line never interrupts the stream.
//
// Parsing is split into two concerns: a per-format decoder turns a line into an
// ordered list of key/value pairs, and a shared canonicalizer (canon) pulls the
// time/level/msg fields out by alias and keeps the rest in source order. The
// canonicalizer is format-agnostic, so a new wire format only needs a decoder.
package parse

import (
	"strconv"
	"strings"
	"time"

	"github.com/shidil/plog/internal/record"
)

// Format selects how a line is decoded. FormatAuto sniffs the line shape;
// the others force a single decoder (or passthrough for FormatText).
type Format int

// Input formats.
const (
	FormatAuto   Format = iota // sniff JSON, then logfmt, else passthrough
	FormatJSON                 // force JSON, passthrough on failure
	FormatLogfmt               // force logfmt, passthrough on failure
	FormatText                 // always passthrough
)

// FormatFromString maps a --format flag value to a Format, reporting ok ==
// false for an unrecognized name. An empty string means FormatAuto.
func FormatFromString(s string) (Format, bool) {
	switch s {
	case "", "auto":
		return FormatAuto, true
	case "json":
		return FormatJSON, true
	case "logfmt":
		return FormatLogfmt, true
	case "text":
		return FormatText, true
	default:
		return FormatAuto, false
	}
}

// pair is one decoded key/value from a log line, in source order. Val is the
// display string the decoder already flattened, so canon need not know which
// wire format produced it.
type pair struct {
	key string
	val string
}

// Line parses one raw log line (without trailing newline) into a Record by
// auto-detecting its format. It never returns an error: an unrecognized or
// malformed line becomes a passthrough record (Parsed == false) carrying the
// original text in Raw.
func Line(raw string) record.Record {
	return LineAs(raw, FormatAuto)
}

// LineAs parses one raw log line using the given format. FormatAuto sniffs the
// shape; an explicit format skips sniffing and falls through to a passthrough
// record if its decoder cannot parse the line. It never returns an error.
func LineAs(raw string, format Format) record.Record {
	switch format {
	case FormatJSON:
		if pairs, ok := parseJSON(raw); ok {
			return finish(raw, pairs)
		}
	case FormatLogfmt:
		if pairs, ok := parseLogfmt(raw); ok {
			return finish(raw, pairs)
		}
	case FormatText:
		// Always passthrough.
	default: // FormatAuto
		trimmed := strings.TrimSpace(raw)
		switch {
		case sniffJSON(trimmed):
			if pairs, ok := parseJSON(raw); ok {
				return finish(raw, pairs)
			}
		case sniffLogfmt(trimmed):
			if pairs, ok := parseLogfmt(raw); ok {
				return finish(raw, pairs)
			}
		}
	}
	return record.Record{Raw: raw}
}

// finish assembles the canonical Record from decoded pairs and stamps the
// fields every parsed record carries.
func finish(raw string, pairs []pair) record.Record {
	rec := canon(pairs)
	rec.Raw = raw
	rec.Parsed = true
	rec.Effective = rec.Level
	return rec
}

// Field-key aliases recognized for the canonical time/level/msg fields,
// independent of wire format (zap uses ts, slog uses time, logfmt may use
// timestamp). The first matching key wins; the rest stay as ordinary fields.
var (
	timeKeys  = set("time", "ts", "timestamp", "@timestamp", "t")
	levelKeys = set("level", "lvl", "severity", "levelname")
	msgKeys   = set("msg", "message")
)

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// canon maps decoded pairs into a Record: it pulls the first time/level/msg
// alias out as typed fields and keeps every other pair in Fields in source
// order. A time/level alias whose value does not parse stays an ordinary field
// rather than being dropped, so no information is lost.
func canon(pairs []pair) record.Record {
	var rec record.Record
	for _, p := range pairs {
		switch {
		case timeKeys[p.key] && rec.Time.IsZero():
			if t, ok := parseTime(p.val); ok {
				rec.Time = t
			} else {
				rec.Fields = append(rec.Fields, record.KV{Key: p.key, Val: p.val})
			}
		case levelKeys[p.key] && rec.Level == record.LevelUnknown:
			if lvl := record.ParseLevel(p.val); lvl != record.LevelUnknown {
				rec.Level = lvl
			} else {
				rec.Fields = append(rec.Fields, record.KV{Key: p.key, Val: p.val})
			}
		case msgKeys[p.key] && rec.Message == "":
			rec.Message = p.val
		default:
			rec.Fields = append(rec.Fields, record.KV{Key: p.key, Val: p.val})
		}
	}
	return rec
}

// timeLayouts are tried in order against a time-aliased value. logfmt and some
// JSON producers omit the zone or use epoch numbers, so the list is broader
// than a single RFC3339Nano.
var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// parseTime decodes a timestamp string, trying the textual layouts and then
// 10-digit unix seconds / 13-digit unix milliseconds. It reports ok == false
// when nothing matches, leaving the value to be kept as an ordinary field.
func parseTime(s string) (time.Time, bool) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		switch len(s) {
		case 10:
			return time.Unix(n, 0).UTC(), true
		case 13:
			return time.Unix(n/1000, (n%1000)*int64(time.Millisecond)).UTC(), true
		}
	}
	return time.Time{}, false
}
