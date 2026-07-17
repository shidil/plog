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

	got, rel, ok := resolve(src, "github.com/example/storefront/internal/rpc/location_rpc.go")
	if !ok {
		t.Fatalf("resolve did not find the file under %q", src)
	}
	if got != want {
		t.Errorf("resolve abs = %q, want %q", got, want)
	}
	if wantRel := "internal/rpc/location_rpc.go"; rel != wantRel {
		t.Errorf("resolve rel = %q, want %q", rel, wantRel)
	}
}

// TestResolveMissing checks that a path with no local counterpart (a remote or
// container trace) resolves to nothing rather than a wrong file.
func TestResolveMissing(t *testing.T) {
	src := t.TempDir()
	if got, _, ok := resolve(src, "/app/does/not/exist.go"); ok {
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
	if _, _, ok := resolve(src, "example.com/pkg"); ok {
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

// TestGitHubFrameURI checks that a Go frame path (which embeds the owner/repo
// slug) links to github.com without needing a local file, honoring the ref and
// omitting the line anchor when the frame carries no line.
func TestGitHubFrameURI(t *testing.T) {
	tests := []struct {
		spec string
		line int
		want string
	}{
		{"shidil/storefront@main", 72, "https://github.com/shidil/storefront/blob/main/internal/rpc/location_rpc.go#L72"},
		{"shidil/storefront", 72, "https://github.com/shidil/storefront/blob/main/internal/rpc/location_rpc.go#L72"}, // ref defaults to main
		{"shidil/storefront@v1.2.0", 72, "https://github.com/shidil/storefront/blob/v1.2.0/internal/rpc/location_rpc.go#L72"},
		{"shidil/storefront@main", 0, "https://github.com/shidil/storefront/blob/main/internal/rpc/location_rpc.go"}, // no line => no anchor
	}
	for _, test := range tests {
		l, err := NewGitHub(test.spec, "", "")
		if err != nil {
			t.Fatalf("NewGitHub(%q): %v", test.spec, err)
		}
		got := l.FrameURI(record.Frame{File: "github.com/shidil/storefront/internal/rpc/location_rpc.go", Line: test.line})
		if got != test.want {
			t.Errorf("NewGitHub(%q).FrameURI(line=%d) = %q, want %q", test.spec, test.line, got, test.want)
		}
	}
}

// TestGitHubSrcFallback checks that a frame path without the slug (e.g. a non-Go
// trace) still links when a local checkout under src supplies the repo-relative
// path.
func TestGitHubSrcFallback(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "app/handler.py")

	l, err := NewGitHub("shidil/svc@main", "", src)
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	got := l.FrameURI(record.Frame{File: "/srv/deploy/app/handler.py", Line: 10})
	want := "https://github.com/shidil/svc/blob/main/app/handler.py#L10"
	if got != want {
		t.Errorf("FrameURI(src-fallback) = %q, want %q", got, want)
	}
}

// TestGitHubNoRepoRelativeEmpty checks that a frame that embeds no slug and has
// no local checkout to fall back on yields no link rather than a wrong one.
func TestGitHubNoRepoRelativeEmpty(t *testing.T) {
	l, err := NewGitHub("shidil/svc", "", "")
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	if got := l.FrameURI(record.Frame{File: "/srv/deploy/app/handler.py", Line: 10}); got != "" {
		t.Errorf("FrameURI(no slug, no src) = %q, want %q", got, "")
	}
}

// TestNewGitHubInvalid checks that a spec without an owner/repo split is a
// startup error.
func TestNewGitHubInvalid(t *testing.T) {
	for _, spec := range []string{"noslash", "owner/", "/repo", "owner/repo@"} {
		if _, err := NewGitHub(spec, "", ""); err == nil {
			t.Errorf("NewGitHub(%q) = nil error, want an error", spec)
		}
	}
}

// TestGitHubModuleStrip checks that the GitHub repo need not be named after the
// module: with the slug absent from the frame path, the module prefix is stripped
// and the remainder mapped onto the given repo.
func TestGitHubModuleStrip(t *testing.T) {
	l, err := NewGitHub("oolio-group/bookings@main", "github.com/example/storefront", "")
	if err != nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	got := l.FrameURI(record.Frame{File: "github.com/example/storefront/internal/rpc/location_rpc.go", Line: 72})
	want := "https://github.com/oolio-group/bookings/blob/main/internal/rpc/location_rpc.go#L72"
	if got != want {
		t.Errorf("FrameURI(module-strip, repo != module) = %q, want %q", got, want)
	}
}
