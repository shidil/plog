// Package record defines the canonical log representation that flows through
// the plog pipeline. Every stage (parse, enrich, render) operates on a Record;
// it carries no behavior of its own so the stages stay independent.
package record

import "time"

// Level is the severity of a log line. The zero value is LevelUnknown so that
// records whose level could not be determined sort below explicit levels.
type Level int

// Severity levels in ascending order of importance.
const (
	LevelUnknown Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
)

// String returns the short uppercase label used in rendered output.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERR"
	default:
		return "?"
	}
}

// ParseLevel maps a textual level (as found in a log field) to a Level. It is
// case-insensitive and tolerant of common spellings; unknown input yields
// LevelUnknown.
func ParseLevel(s string) Level {
	switch s {
	case "DEBUG", "debug", "Debug", "TRACE", "trace":
		return LevelDebug
	case "INFO", "info", "Info", "INFORMATION":
		return LevelInfo
	case "WARN", "warn", "Warn", "WARNING", "warning":
		return LevelWarn
	case "ERROR", "error", "Error", "ERR", "err", "FATAL", "fatal", "PANIC", "panic":
		return LevelError
	default:
		return LevelUnknown
	}
}

// KV is one structured field, preserved in the order it appeared in the source
// line so rendered output mirrors how the producer emitted it.
type KV struct {
	Key string
	Val string
	// Demoted marks a field the Columns stage judged constant across the recent
	// window — noise that should recede behind the fields that distinguish lines.
	Demoted bool
}

// FrameKind classifies a stack frame by origin so the renderer can foreground
// project code and fold framework noise.
type FrameKind int

// Frame origins.
const (
	FrameStdlib     FrameKind = iota // language runtime/builtin (Go stdlib, node:internal, ...)
	FrameThirdParty                  // external dependency (otel, connectrpc, node_modules, ...)
	FrameProject                     // the user's own code
)

// Frame is a single parsed stack-trace frame.
type Frame struct {
	Func string // fully qualified function, pointers stripped
	File string // source path as emitted by the runtime
	Line int    // source line, 0 if not parsed
	Col  int    // source column, 0 if not parsed (e.g. Go traces omit it)
	Kind FrameKind
}

// StackTrace is a parsed stack trace from a supported language.
type StackTrace struct {
	Header string  // the human text preceding the trace (e.g. the panic line)
	Frames []Frame // frames in emission order (innermost first)
	Lang   string  // source language, e.g. "go" or "node"
}

// Related links a record back to a recent, more severe event it appears to stem
// from — set by the Correlator when both concern the same method within a few
// seconds. It is advisory: a heuristic hint pointed at a prior problem, not a
// proven causal edge.
type Related struct {
	Time    time.Time // when the earlier event was logged
	Summary string    // short description of that event, for a back-reference note
}

// Record is one log line after parsing. Parsed reports whether the line was
// recognized as structured; when false only Raw is meaningful and the line is
// passed through verbatim.
type Record struct {
	Time      time.Time
	Level     Level       // severity as declared by the producer
	Effective Level       // severity after enrichment; equals Level when unchanged
	Message   string      // the primary message (msg field)
	Fields    []KV        // remaining structured fields, in source order
	Stack     *StackTrace // non-nil when the record embedded a stack trace, in the message or a field
	Raw       string      // the original line, always retained
	Parsed    bool        // false => passthrough line
	Repeat    int         // occurrences collapsed into this record; >1 => folded run

	// Corr groups records that share a recent correlation key (an explicit
	// trace/request id, or the client address) under a short tag, so one request
	// reads as a group. Empty when the record has no key or its key has not yet
	// recurred in the window.
	Corr string
	// Related, when non-nil, points back to a recent elevated event this record
	// is heuristically linked to (same method, close in time).
	Related *Related
}
