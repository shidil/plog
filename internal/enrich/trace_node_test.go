package enrich

import (
	"testing"

	"github.com/shidil/plog/internal/record"
)

const nodeTrace = "TypeError: Cannot read properties of undefined (reading 'id')\n" +
	"    at resolveUser (/app/src/handlers/user.js:42:18)\n" +
	"    at /app/src/anon.js:7:3\n" +
	"    at Layer.handle (/app/node_modules/express/lib/router/layer.js:95:5)\n" +
	"    at Query.run (C:\\app\\node_modules\\pg\\lib\\query.js:88:11)\n" +
	"    at process.processTicksAndRejections (node:internal/process/task_queues:95:5)"

func TestNodeGrammarParse(t *testing.T) {
	st := detectAndParse(nodeTrace, "")
	if st == nil {
		t.Fatal("detectAndParse returned nil for a Node trace")
	}
	if st.Lang != "node" {
		t.Errorf("Lang = %q, want %q", st.Lang, "node")
	}
	if want := "TypeError: Cannot read properties of undefined (reading 'id')"; st.Header != want {
		t.Errorf("Header = %q, want %q", st.Header, want)
	}

	// One trace exercises every classification (including a Windows-emitted
	// dependency path) and the named/anonymous frame shapes.
	want := []record.Frame{
		{Func: "resolveUser", File: "/app/src/handlers/user.js", Line: 42, Col: 18, Kind: record.FrameProject},
		{Func: "<anonymous>", File: "/app/src/anon.js", Line: 7, Col: 3, Kind: record.FrameProject},
		{Func: "Layer.handle", File: "/app/node_modules/express/lib/router/layer.js", Line: 95, Col: 5, Kind: record.FrameThirdParty},
		{Func: "Query.run", File: `C:\app\node_modules\pg\lib\query.js`, Line: 88, Col: 11, Kind: record.FrameThirdParty},
		{Func: "process.processTicksAndRejections", File: "node:internal/process/task_queues", Line: 95, Col: 5, Kind: record.FrameStdlib},
	}
	if len(st.Frames) != len(want) {
		t.Fatalf("got %d frames, want %d: %+v", len(st.Frames), len(want), st.Frames)
	}
	for i, w := range want {
		if st.Frames[i] != w {
			t.Errorf("frame[%d] = %+v, want %+v", i, st.Frames[i], w)
		}
	}
}

func TestNodeGrammarNotParsed(t *testing.T) {
	// Values that resemble the loose "at ...:N:N" shape but are not traces must
	// be left alone, so an ordinary field is never mistaken for a stack.
	tests := []struct {
		name string
		in   string
	}{
		{name: "indented_clock", in: "job summary\n    at 10:30:00\n    at 11:00:00"},
		{name: "prose_two_lines", in: "just a message\nwith two lines"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if st := detectAndParse(tc.in, ""); st != nil {
				t.Errorf("detectAndParse(%q) = %+v, want nil", tc.in, st)
			}
		})
	}
}

func FuzzParseTrace(f *testing.F) {
	seeds := []string{
		"",
		"plain text",
		sampleTrace,
		nodeTrace,
		"goroutine 1 [running]:\nmain.x()\n\t/a/b.go:1 +0x1",
		"    at foo (/a/b.js:1:2)",
		"    at 10:30:00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		st := detectAndParse(in, "github.com/example")
		// Invariant: grammars return nil rather than a frameless trace, so a
		// non-nil result always carries at least one frame.
		if st != nil && len(st.Frames) == 0 {
			t.Errorf("detectAndParse(%q) returned a trace with no frames", in)
		}
	})
}
