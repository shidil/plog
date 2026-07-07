package parse

import (
	"strings"

	"github.com/shidil/plog/internal/record"
)

// pyAsctimeLen is the fixed width of Python logging's default asctime,
// "YYYY-MM-DD HH:MM:SS,mmm" — 23 bytes, with a comma before the milliseconds.
const pyAsctimeLen = 23

// sniffPylog reports whether trimmed looks like the common configured Python
// logging shape:
//
//	%(asctime)s - %(name)s - %(levelname)s - %(message)s
//	2023-10-05 14:23:01,123 - myapp - INFO - started
//
// The signature is the fixed-width asctime with comma-milliseconds followed by
// " - ". Python's bare "%(levelname)s:%(name)s:%(message)s" default is not
// matched: it collides too easily with prose. parsePylog re-validates the level
// token, so a near-miss falls through to passthrough.
func sniffPylog(trimmed string) bool {
	if len(trimmed) < pyAsctimeLen+3 {
		return false
	}
	// Date: 2023-10-05
	if !(allDigits(trimmed[0:4]) && trimmed[4] == '-' && allDigits(trimmed[5:7]) &&
		trimmed[7] == '-' && allDigits(trimmed[8:10])) {
		return false
	}
	if trimmed[10] != ' ' {
		return false
	}
	// Time: 14:23:01
	if !(allDigits(trimmed[11:13]) && trimmed[13] == ':' && allDigits(trimmed[14:16]) &&
		trimmed[16] == ':' && allDigits(trimmed[17:19])) {
		return false
	}
	// ,mmm then the " - " separator.
	if trimmed[19] != ',' || !allDigits(trimmed[20:23]) {
		return false
	}
	return strings.HasPrefix(trimmed[pyAsctimeLen:], " - ")
}

// parsePylog decodes a Python logging line of the form
// "asctime - name - LEVEL - message". The asctime is fixed-width; the logger
// name and level are the next two " - "-separated fields, and the message keeps
// any remaining " - ". It reports ok == false — falling through to passthrough —
// when the level field is not a recognized Python level, which guards against a
// prose line that happens to open with a timestamp.
func parsePylog(raw string) ([]pair, bool) {
	line := strings.TrimLeft(raw, " \t")
	if len(line) < pyAsctimeLen+3 || line[pyAsctimeLen:pyAsctimeLen+3] != " - " {
		return nil, false
	}
	// Comma-milliseconds -> dot so canon's date-time layout parses it.
	asctime := strings.Replace(line[:pyAsctimeLen], ",", ".", 1)

	rest := line[pyAsctimeLen+3:] // "name - LEVEL - message"
	parts := strings.SplitN(rest, " - ", 3)
	if len(parts) < 3 {
		return nil, false
	}
	name, level, msg := parts[0], parts[1], parts[2]
	if record.ParseLevel(level) == record.LevelUnknown {
		return nil, false
	}

	return []pair{
		{key: "time", val: asctime},
		{key: "level", val: level},
		{key: "logger", val: name},
		{key: "msg", val: msg},
	}, true
}
