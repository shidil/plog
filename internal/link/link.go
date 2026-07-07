// Package link turns stack-trace frames into terminal hyperlink (OSC 8) target
// URIs, resolving the runtime-emitted path to a local source file. It exists so
// the renderer can make "your code" frames clickable without itself touching the
// filesystem or knowing about editors.
package link

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shidil/plog/internal/record"
)

// Linker builds an editor/terminal URI for a stack frame whose source file can
// be found on disk. Frames that do not resolve to a local file yield "", so a
// remote/container trace or a module-cache dependency simply gets no link.
type Linker struct {
	src    string
	format func(path string, line, col int) string
}

// New returns a Linker for the given scheme and source root. scheme is either a
// built-in editor preset (vscode, cursor, idea, file) or a URI template
// containing at least the "{path}" placeholder (with optional "{line}"/"{col}").
// src is the local directory frame paths are resolved against; "" means the
// current directory.
func New(scheme, src string) (*Linker, error) {
	if src == "" {
		src = "."
	}
	format, err := formatter(scheme)
	if err != nil {
		return nil, err
	}
	return &Linker{src: src, format: format}, nil
}

// FrameURI returns the hyperlink target for f, or "" when f's source file is not
// found under the source root — so a remote/container trace, a module-cache
// dependency, or a minified bundle path yields no link rather than a dead one.
func (l *Linker) FrameURI(f record.Frame) string {
	abs, ok := resolve(l.src, f.File)
	if !ok {
		return ""
	}
	return l.format(abs, f.Line, f.Col)
}

// resolve finds the local file for a runtime-emitted path by trying successively
// shorter path suffixes joined onto src — longest first — and returning the first
// that names an existing regular file. This drops the leading module-path,
// container, or build-output segments that do not exist locally without needing
// to know the module prefix, and works the same across languages.
func resolve(src, file string) (string, bool) {
	segs := strings.Split(filepath.ToSlash(file), "/")
	for start := range segs {
		if segs[start] == "" {
			continue
		}
		cand := filepath.Join(append([]string{src}, segs[start:]...)...)
		fi, err := os.Stat(cand)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		abs, err := filepath.Abs(cand)
		if err != nil {
			return "", false
		}
		return abs, true
	}
	return "", false
}

// formatter maps a scheme name or URI template to a function rendering a frame's
// resolved path, line, and column into a target URI.
func formatter(scheme string) (func(path string, line, col int) string, error) {
	switch scheme {
	case "vscode":
		return func(p string, line, col int) string { return "vscode://file" + p + lineCol(line, col) }, nil
	case "cursor":
		return func(p string, line, col int) string { return "cursor://file" + p + lineCol(line, col) }, nil
	case "zed":
		return func(p string, line, col int) string { return "zed://file" + p + lineCol(line, col) }, nil
	case "idea":
		return func(p string, line, _ int) string {
			u := "idea://open?file=" + p
			if line > 0 {
				u += "&line=" + strconv.Itoa(line)
			}
			return u
		}, nil
	case "file":
		return func(p string, _, _ int) string { return "file://" + p }, nil
	}
	if strings.Contains(scheme, "{path}") {
		tmpl := scheme
		return func(p string, line, col int) string {
			return strings.NewReplacer(
				"{path}", p,
				"{line}", intOrEmpty(line),
				"{col}", intOrEmpty(col),
			).Replace(tmpl)
		}, nil
	}
	return nil, fmt.Errorf("unknown --link %q: want an editor preset (vscode|cursor|zed|idea|file) or a URI template containing {path}", scheme)
}

// lineCol renders the ":line" / ":line:col" suffix editors accept, omitting parts
// the trace did not provide (Go traces carry no column; some frames no line).
func lineCol(line, col int) string {
	if line == 0 {
		return ""
	}
	if col > 0 {
		return fmt.Sprintf(":%d:%d", line, col)
	}
	return fmt.Sprintf(":%d", line)
}

// intOrEmpty renders a positive int for template substitution, or "" for zero so
// an absent line/col leaves no stray "0" in the URI.
func intOrEmpty(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}
