package enrich

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/shidil/plog/internal/record"
)

// nodeGrammar recognizes V8/Bun traces: an "Error: message" header followed by
// indented frames of the form "at <func> (<file>:<line>:<col>)" or the
// function-less "at <file>:<line>:<col>".
type nodeGrammar struct{}

func (nodeGrammar) lang() string { return "node" }

// nodeFrame matches one indented "at ..." frame line, capturing the optional
// function name and the file:line:col location. The multiline flag lets detect
// scan a whole value while parse matches individual lines.
var nodeFrame = regexp.MustCompile(`(?m)^\s+at (?:(.+) \()?(.+):(\d+):(\d+)\)?$`)

func (nodeGrammar) detect(s string) bool { return nodeFrame.MatchString(s) }

// parse extracts the header text and frames from s, or nil if it holds no
// recognizable frame. A line whose file is not path-like is skipped, so a value
// that merely resembles the "at ...:N:N" shape (e.g. an indented clock) does not
// masquerade as a trace.
func (nodeGrammar) parse(s, module string) *record.StackTrace {
	lines := strings.Split(s, "\n")
	var frames []record.Frame
	headerEnd := -1
	for idx, ln := range lines {
		m := nodeFrame.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		file := m[2]
		if !looksLikePath(file) {
			continue
		}
		fn := strings.TrimSpace(m[1])
		if fn == "" {
			fn = "<anonymous>"
		}
		line, _ := strconv.Atoi(m[3])
		col, _ := strconv.Atoi(m[4])
		frames = append(frames, record.Frame{
			Func: fn,
			File: file,
			Line: line,
			Col:  col,
			Kind: classifyNode(file),
		})
		if headerEnd < 0 {
			headerEnd = idx
		}
	}
	if len(frames) == 0 {
		return nil
	}
	var header string
	if headerEnd > 0 {
		header = strings.TrimSpace(strings.Join(lines[:headerEnd], "\n"))
	}
	return &record.StackTrace{Header: header, Frames: frames}
}

// looksLikePath reports whether a frame's file token names a real source
// location rather than an incidental "word:N:N" match.
func looksLikePath(file string) bool {
	return strings.ContainsAny(file, "./") || strings.HasPrefix(file, "node:")
}

// classifyNode decides a Node frame's origin from its file path: the "node:"
// scheme is the runtime, anything under node_modules is a dependency, and the
// rest is the user's own code. Both path separators are checked because the
// trace carries whatever the emitting host used, including Windows backslashes.
func classifyNode(file string) record.FrameKind {
	switch {
	case strings.HasPrefix(file, "node:"):
		return record.FrameStdlib
	case strings.Contains(file, "node_modules/"), strings.Contains(file, `node_modules\`):
		return record.FrameThirdParty
	default:
		return record.FrameProject
	}
}
