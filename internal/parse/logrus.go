package parse

import "strings"

// logrusLevels maps the level token in logrus's colored text output to a level
// word canon understands. logrus truncates the level to four characters by
// default (ERRO, WARN, DEBU, ...) but emits the full word when
// DisableLevelTruncation is set, so both spellings are recognized.
var logrusLevels = map[string]string{
	"TRAC": "trace", "TRACE": "trace",
	"DEBU": "debug", "DEBUG": "debug",
	"INFO": "info",
	"WARN": "warn", "WARNING": "warn",
	"ERRO": "error", "ERROR": "error",
	"FATA": "fatal", "FATAL": "fatal",
	"PANI": "panic", "PANIC": "panic",
}

// sniffLogrus reports whether trimmed looks like logrus's colored TextFormatter
// output:
//
//	LEVEL[timestamp] message            key=value key=value
//
// This shape only occurs with color enabled, so the level is wrapped in ANSI
// SGR codes that stripANSI removes first. The bracket must open on a digit — an
// elapsed-seconds counter or a timestamp — which keeps prose like "TODO[fix]"
// out. parseLogrus re-validates, so a near-miss falls through to passthrough.
func sniffLogrus(trimmed string) bool {
	s := stripANSI(trimmed)
	lb := strings.IndexByte(s, '[')
	if lb <= 0 {
		return false
	}
	if _, ok := logrusLevels[s[:lb]]; !ok {
		return false
	}
	if lb+1 >= len(s) || s[lb+1] < '0' || s[lb+1] > '9' {
		return false
	}
	return strings.IndexByte(s[lb+1:], ']') >= 0
}

// parseLogrus decodes a logrus colored text line. It strips the ANSI codes,
// reads the level and the bracketed stamp, then splits the remainder into the
// message and its trailing logfmt fields. A bracket holding a wall-clock
// timestamp becomes the time; the default elapsed-seconds counter is kept as an
// "elapsed" field instead, since it is not a clock. It reports ok == false when
// the level or bracket is malformed so the caller falls through to passthrough.
func parseLogrus(raw string) ([]pair, bool) {
	s := strings.TrimLeft(stripANSI(raw), " \t")
	lb := strings.IndexByte(s, '[')
	if lb <= 0 {
		return nil, false
	}
	level, ok := logrusLevels[s[:lb]]
	if !ok {
		return nil, false
	}
	rb := strings.IndexByte(s[lb:], ']')
	if rb < 0 {
		return nil, false
	}
	stamp := s[lb+1 : lb+rb]
	msg, fields := splitLogrusMessage(strings.TrimLeft(s[lb+rb+1:], " "))

	pairs := make([]pair, 0, 3+len(fields))
	// A timestamp carries date/clock punctuation; the elapsed counter is bare
	// digits and is not wall-clock time, so it is kept as a field, not the time.
	if strings.ContainsAny(stamp, ":-T") {
		pairs = append(pairs, pair{key: "time", val: stamp})
	} else if stamp != "" {
		pairs = append(pairs, pair{key: "elapsed", val: stamp})
	}
	pairs = append(pairs, pair{key: "level", val: level})
	pairs = append(pairs, pair{key: "msg", val: msg})
	return append(pairs, fields...), true
}

// splitLogrusMessage separates a logrus message from its trailing fields.
// logrus left-justifies the message to 44 columns before appending logfmt
// pairs, so a run of two or more spaces marks the boundary when the message is
// short; the field side must itself sniff as logfmt (guarding against a message
// that merely contains a double space). A message at or past 44 columns has no
// such gap and is returned whole, with its fields unseparated.
func splitLogrusMessage(rest string) (msg string, fields []pair) {
	if i := strings.Index(rest, "  "); i >= 0 {
		right := strings.TrimLeft(rest[i:], " ")
		if sniffLogfmt(right) {
			if fs, ok := parseLogfmt(right); ok {
				return strings.TrimRight(rest[:i], " "), fs
			}
		}
	}
	return strings.TrimRight(rest, " "), nil
}

// stripANSI removes ANSI CSI escape sequences (e.g. the color codes logrus
// wraps its level in) so the payload can be parsed as plain text. The original
// line is preserved elsewhere; only the decode works on the stripped copy.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
				j++
			}
			if j < len(s) { // skip the final byte of the sequence
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
