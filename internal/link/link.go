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
	abs, _, ok := resolve(l.src, f.File)
	if !ok {
		return ""
	}
	return l.format(abs, f.Line, f.Col)
}

// resolve finds the local file for a runtime-emitted path by trying successively
// shorter path suffixes joined onto src — longest first — and returning the first
// that names an existing regular file. It returns the absolute path and the
// matched suffix (the path relative to src, i.e. the repo-relative path when src
// is the repo root). Trying suffixes drops the leading module-path, container, or
// build-output segments that do not exist locally without needing to know the
// module prefix, and works the same across languages.
func resolve(src, file string) (abs, rel string, ok bool) {
	segs := strings.Split(filepath.ToSlash(file), "/")
	for start := range segs {
		if segs[start] == "" {
			continue
		}
		suffix := segs[start:]
		cand := filepath.Join(append([]string{src}, suffix...)...)
		fi, err := os.Stat(cand)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		abs, err := filepath.Abs(cand)
		if err != nil {
			return "", "", false
		}
		return abs, strings.Join(suffix, "/"), true
	}
	return "", "", false
}

// GitHubLinker builds github.com blob URLs for stack frames, so a trace from a
// remote or containerized process links to the source on GitHub even when it is
// not checked out locally. Unlike Linker it does not require the file on disk: it
// derives the repo-relative path from the frame path itself.
type GitHubLinker struct {
	owner, repo, ref string
	module           string // import-path prefix to strip for the repo-relative path (--module); repo name need not match it
	src              string // optional local checkout, a fallback when neither slug nor module prefix is in the path
}

// NewGitHub returns a GitHubLinker from a spec of the form "owner/repo" or
// "owner/repo@ref" (ref defaults to "main"). module is the import-path prefix
// (--module) stripped to obtain the repo-relative path, so the GitHub repo need
// not be named after the module. src is an optional local source root used as a
// last-resort fallback (e.g. non-Go traces with no strippable prefix); "" off.
func NewGitHub(spec, module, src string) (*GitHubLinker, error) {
	slug, ref, ok := strings.Cut(spec, "@")
	if !ok {
		ref = "main"
	}
	owner, repo, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || repo == "" || ref == "" {
		return nil, fmt.Errorf("invalid --github %q: want owner/repo or owner/repo@ref", spec)
	}
	return &GitHubLinker{owner: owner, repo: repo, ref: ref, module: module, src: src}, nil
}

// FrameURI returns the github.com blob URL for f, or "" when the repo-relative
// path cannot be derived (a non-Go path with no local checkout to fall back on).
func (l *GitHubLinker) FrameURI(f record.Frame) string {
	rel := l.repoRelative(f)
	if rel == "" {
		return ""
	}
	u := "https://github.com/" + l.owner + "/" + l.repo + "/blob/" + l.ref + "/" + rel
	if f.Line > 0 {
		u += fmt.Sprintf("#L%d", f.Line)
	}
	return u
}

// repoRelative derives f's path relative to the repository root, trying in order
// of specificity: (1) the "owner/repo" slug in the frame path (pins the repo root
// exactly, the github.com/owner/repo/... case); (2) the module import prefix —
// the repo need not be named after the module, so the module-relative path maps
// straight onto the given repo; (3) a local checkout under src. Empty if none
// apply, so a frame with no strippable prefix and no checkout gets no link.
func (l *GitHubLinker) repoRelative(f record.Frame) string {
	file := filepath.ToSlash(f.File)
	if rel, ok := afterPrefix(file, l.owner+"/"+l.repo); ok {
		return rel
	}
	if rel, ok := afterPrefix(file, l.module); ok {
		return rel
	}
	if l.src != "" {
		if _, rel, ok := resolve(l.src, f.File); ok {
			return rel
		}
	}
	return ""
}

// afterPrefix returns the path following "prefix/" when prefix occurs on a
// segment boundary in file and leaves a non-empty remainder.
func afterPrefix(file, prefix string) (string, bool) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return "", false
	}
	prefix += "/"
	i := strings.Index(file, prefix)
	if i != 0 && !(i > 0 && file[i-1] == '/') {
		return "", false
	}
	rel := file[i+len(prefix):]
	return rel, rel != ""
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
