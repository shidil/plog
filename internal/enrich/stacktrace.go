package enrich

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/shidil/plog/internal/record"
)

// goroutineHeader matches the "goroutine 23 [running]:" line that introduces a
// Go stack trace, used to locate where the trace begins within a message.
var goroutineHeader = regexp.MustCompile(`(?m)^goroutine \d+ \[[^\]]*\]:$`)

// Stack returns a copy of rec with Stack populated when its message embeds a Go
// panic/goroutine trace. module is the import-path prefix treated as project
// code; pass "" to disable project highlighting. Records without a trace, or
// already carrying one, are returned unchanged.
func Stack(rec record.Record, module string) record.Record {
	if !rec.Parsed || rec.Stack != nil {
		return rec
	}
	if st := parseTrace(rec.Message, module); st != nil {
		rec.Stack = st
	}
	return rec
}

// parseTrace extracts the header text and frames from a message, or nil if it
// does not contain a recognizable goroutine trace.
func parseTrace(msg, module string) *record.StackTrace {
	loc := goroutineHeader.FindStringIndex(msg)
	if loc == nil {
		return nil
	}
	header := strings.TrimRight(msg[:loc[0]], "\n")
	lines := strings.Split(msg[loc[0]:], "\n")

	var frames []record.Frame
	for i := 1; i < len(lines); {
		fnLine := lines[i]
		if fnLine == "" || strings.HasPrefix(fnLine, "\t") {
			i++
			continue
		}
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\t") {
			file, line := parseLocation(lines[i+1])
			// "created by pkg.Fn in goroutine N" marks a spawn point; keep just
			// the function so classification and labels match other frames.
			fnLine = strings.TrimPrefix(fnLine, "created by ")
			if sp := strings.Index(fnLine, " in goroutine "); sp >= 0 {
				fnLine = fnLine[:sp]
			}
			fn := stripArgs(fnLine)
			frames = append(frames, record.Frame{
				Func: fn,
				File: file,
				Line: line,
				Kind: classify(fn, file, module),
			})
			i += 2
			continue
		}
		i++
	}
	if len(frames) == 0 {
		return nil
	}
	if header == "" {
		header = strings.TrimSpace(lines[0])
	}
	return &record.StackTrace{Header: header, Frames: frames}
}

// parseLocation reads "file.go:line +0xNN" from an indented location line and
// returns the file path and line number (0 if absent).
func parseLocation(s string) (string, int) {
	s = strings.TrimSpace(s)
	if fields := strings.Fields(s); len(fields) > 0 {
		s = fields[0] // drop the trailing " +0x.." instruction offset
	}
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, 0
	}
	line, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return s[:i], 0
	}
	return s[:i], line
}

// stripArgs removes the trailing argument list (and its embedded pointers) from
// a frame's function text while preserving any receiver parentheses earlier in
// the name. "pkg.(*T).M(0x1, {..})" => "pkg.(*T).M".
func stripArgs(fn string) string {
	fn = strings.TrimSpace(fn)
	if !strings.HasSuffix(fn, ")") {
		return fn
	}
	if i := strings.LastIndexByte(fn, '('); i >= 0 {
		return fn[:i]
	}
	return fn
}

// classify decides a frame's origin. A frame is project code when its function
// or file path contains the module prefix; otherwise it is standard library
// when its leading import segment has no dot (no domain), else third-party.
func classify(fn, file, module string) record.FrameKind {
	if module != "" && (strings.Contains(fn, module) || strings.Contains(file, module)) {
		return record.FrameProject
	}
	if !strings.Contains(firstSegment(fn), ".") {
		return record.FrameStdlib
	}
	return record.FrameThirdParty
}

// firstSegment returns the leading import-path segment of a function string:
// the text before the first "/", or before the first "." when there is no
// slash. A dot in this segment indicates a domain (third-party); its absence
// indicates the standard library.
func firstSegment(fn string) string {
	if before, _, found := strings.Cut(fn, "/"); found {
		return before
	}
	if before, _, found := strings.Cut(fn, "("); found {
		fn = before
	}
	if before, _, found := strings.Cut(fn, "."); found {
		return before
	}
	return fn
}
