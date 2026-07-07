package parse

import "strings"

// glogLevels maps the single-character glog/klog severity prefix to a level word
// that canon's ParseLevel understands.
var glogLevels = map[byte]string{
	'I': "info",
	'W': "warn",
	'E': "error",
	'F': "fatal",
}

// sniffGlog reports whether trimmed looks like a glog/klog header:
//
//	Lmmdd hh:mm:ss...
//
// a severity letter (I/W/E/F), four digits of month+day, a space, then an
// hh:mm:ss clock. The shape is distinctive enough that prose never matches, and
// parseGlog re-validates, so a near-miss still falls through to passthrough.
func sniffGlog(trimmed string) bool {
	// "I0605 14:23:01" is the shortest valid prefix: 14 bytes.
	if len(trimmed) < 14 {
		return false
	}
	if _, ok := glogLevels[trimmed[0]]; !ok {
		return false
	}
	if !allDigits(trimmed[1:5]) || trimmed[5] != ' ' {
		return false
	}
	return trimmed[8] == ':' && trimmed[11] == ':' &&
		allDigits(trimmed[6:8]) && allDigits(trimmed[9:11]) && allDigits(trimmed[12:14])
}

// parseGlog decodes a glog/klog line:
//
//	Lmmdd hh:mm:ss.uuuuuu threadid file:line] message
//
// It emits the time as "mmdd hh:mm:ss.uuuuuu" (parsed by canon's yearless
// layout — glog omits the year), the level from the L prefix, the thread id and
// caller as fields, and everything after the first "]" as the message. It
// reports ok == false when the header is malformed so the caller falls through
// to passthrough.
func parseGlog(raw string) ([]pair, bool) {
	// The header ends at the first "]"; the message is whatever follows it.
	header, msg, ok := strings.Cut(raw, "]")
	if !ok {
		return nil, false
	}
	fields := strings.Fields(header)
	// Lmmdd, hh:mm:ss.uuuuuu, threadid, file:line.
	if len(fields) < 4 {
		return nil, false
	}
	head := fields[0]
	if len(head) != 5 {
		return nil, false
	}
	level, ok := glogLevels[head[0]]
	if !ok {
		return nil, false
	}

	pairs := []pair{
		{key: "time", val: head[1:] + " " + fields[1]},
		{key: "level", val: level},
		{key: "msg", val: strings.TrimSpace(msg)},
		{key: "thread", val: fields[2]},
		{key: "caller", val: fields[3]},
	}
	return pairs, true
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
