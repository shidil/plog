package link

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shidil/plog/internal/record"
)

// writeFile creates an empty file at rel under dir (making parent dirs) and
// returns its absolute path, for building resolvable frames.
func writeFile(t *testing.T, dir, rel string) string {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(abs, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return abs
}

// TestResolveLongestSuffix checks that a runtime-emitted path (module-qualified,
// with leading segments absent locally) resolves to the local file by its
// longest existing suffix.
func TestResolveLongestSuffix(t *testing.T) {
	src := t.TempDir()
	want := writeFile(t, src, "internal/rpc/location_rpc.go")

	got, ok := resolve(src, "github.com/example/storefront/internal/rpc/location_rpc.go")
	if !ok {
		t.Fatalf("resolve did not find the file under %q", src)
	}
	if got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

// TestResolveMissing checks that a path with no local counterpart (a remote or
// container trace) resolves to nothing rather than a wrong file.
func TestResolveMissing(t *testing.T) {
	src := t.TempDir()
	if got, ok := resolve(src, "/app/does/not/exist.go"); ok {
		t.Errorf("resolve(%q) = %q, true; want no match", "/app/does/not/exist.go", got)
	}
}

// TestResolveSkipsDirectories checks that a suffix matching a directory is not
// accepted as a source file.
func TestResolveSkipsDirectories(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, ok := resolve(src, "example.com/pkg"); ok {
		t.Error("resolve matched a directory, want no match")
	}
}

// TestFrameURIFormats checks that each scheme renders the resolved absolute path
// plus the frame's line/column into the URI the editor expects, omitting parts
// the trace did not carry.
func TestFrameURIFormats(t *testing.T) {
	src := t.TempDir()
	abs := writeFile(t, src, "main.go")

	tests := []struct {
		scheme    string
		line, col int
		want      string
	}{
		{"vscode", 72, 5, "vscode://file" + abs + ":72:5"},
		{"vscode", 72, 0, "vscode://file" + abs + ":72"}, // Go frames carry no column
		{"cursor", 72, 5, "cursor://file" + abs + ":72:5"},
		{"zed", 72, 5, "zed://file" + abs + ":72:5"},
		{"zed", 72, 0, "zed://file" + abs + ":72"}, // no column
		{"idea", 72, 5, "idea://open?file=" + abs + "&line=72"},
		{"file", 72, 5, "file://" + abs},
		{"editor://{path}:{line}:{col}", 72, 5, "editor://" + abs + ":72:5"},
		{"editor://{path}:{line}:{col}", 72, 0, "editor://" + abs + ":72:"}, // template keeps its literal separators
	}
	for _, test := range tests {
		l, err := New(test.scheme, src)
		if err != nil {
			t.Fatalf("New(%q): %v", test.scheme, err)
		}
		got := l.FrameURI(record.Frame{File: "main.go", Line: test.line, Col: test.col})
		if got != test.want {
			t.Errorf("New(%q).FrameURI(line=%d,col=%d) = %q, want %q", test.scheme, test.line, test.col, got, test.want)
		}
	}
}

// TestFrameURIUnresolvedEmpty checks that a frame whose file is not found yields
// no link, so callers emit nothing rather than a dead hyperlink.
func TestFrameURIUnresolvedEmpty(t *testing.T) {
	l, err := New("vscode", t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := l.FrameURI(record.Frame{File: "go.opentelemetry.io/otel/trace/span.go", Line: 528}); got != "" {
		t.Errorf("FrameURI(unresolvable) = %q, want %q", got, "")
	}
}

// TestNewRejectsUnknownScheme checks that a scheme that is neither a preset nor a
// {path} template is a startup error, not a silent no-op.
func TestNewRejectsUnknownScheme(t *testing.T) {
	if _, err := New("emacs", ""); err == nil {
		t.Error("New(\"emacs\") = nil error, want an error naming the accepted forms")
	}
}
