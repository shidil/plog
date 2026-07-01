# Implementation plan — multi-language stack-trace intelligence

Companion to `multi-language-stack-traces.md` (the scope). Delivers the Go + Node
collapse set and the field-scan fix, as a pure extension of the existing
`enrich.Stack` stage — no new pipeline stage, no buffering, sacred passthrough
untouched.

## Context

`enrich.Stack` today parses a trace from only `rec.Message` and recognizes only
Go (`stacktrace.go:23,13`). Structured loggers put the trace in a field
(`stack`, `err.stack`, `exception`), so those records get no stack treatment and
the field prints raw; and a Node/Python trace would not collapse anyway. This
plan (1) scans field values, not just `Message`, and (2) refactors the single
Go parser into a per-language grammar registry, adding Node. The grammars built
here are the reusable core of #16.

## Approach

Keep the exported signature `enrich.Stack(rec record.Record, module string)
record.Record` (Node needs no extra config, so no ripple into `cmd/plog`). Split
the source-finding (Message → fields) from the per-language frame grammar.

```
Stack(rec, module)
  ├─ guard: !Parsed || Stack!=nil → unchanged
  ├─ detectAndParse(rec.Message, module) → set Stack, return
  └─ scan rec.Fields in order: first value that looksLikeTrace && parses
       → set Stack, CONSUME that field (drop from Fields), return
```

`detectAndParse` walks a grammar registry; first `detect` wins.

## Step 1 — Field scan + consume (Go grammar only)

Smallest step that fixes the biggest gap; verifiable before any Node work.

- `internal/enrich/stacktrace.go`:
  - Add `looksLikeTrace(s string) bool` — cheap guard: requires a newline plus a
    language pre-marker (`"goroutine "`, `"\n\tat "`, `"\n    at "`, …). Keeps
    non-trace records near-free.
  - Add the field-scan loop to `Stack`: after the `Message` attempt fails, scan
    `rec.Fields`; on the first value that parses, set `rec.Stack` and remove that
    field via `rec.Fields = append(rec.Fields[:i:i], rec.Fields[i+1:]...)`.
  - Header fallback: if the parsed `Stack.Header` is empty (a field trace with no
    panic header), set it from `rec.Message` so folding/labeling stay meaningful.
- No grammar change yet — `parseTrace` still the only parser.

**Checkpoint:** a record with a Go trace in a `stack`/`error` field now collapses
identically to one in `msg`; existing `enrich_test.go` stays green.

## Step 2 — Grammar registry refactor (behavior-preserving)

- New `internal/enrich/trace.go`: the seam.

  ```go
  type traceGrammar interface {
  	lang() string
  	detect(s string) bool
  	parse(s, module string) *record.StackTrace
  }
  var grammars = []traceGrammar{goGrammar{}, nodeGrammar{}} // node added in Step 3
  func detectAndParse(s, module string) *record.StackTrace {
  	for _, g := range grammars {
  		if g.detect(s) {
  			return g.parse(s, module)
  		}
  	}
  	return nil
  }
  ```

- New `internal/enrich/trace_go.go`: move the existing Go logic verbatim into
  `goGrammar` — `parseTrace`→`parse`, plus `goroutineHeader`, `parseLocation`,
  `stripArgs`, `classify`, `firstSegment` (now Go-private). `detect` = the
  `goroutineHeader` match OR a col-0 fn line followed by an indented `…:NN`.
- `Stack`/`looksLikeTrace` call `detectAndParse` instead of `parseTrace`.

**Checkpoint:** registry with only Go behaves exactly as Step 1; `enrich_test.go`
+ Step 1 tests still green (the refactor proof).

## Step 3 — Node grammar

- `internal/record/record.go` — additive only: `Frame.Col int` (0 when absent),
  `StackTrace.Lang string`.
- `internal/enrich/trace_node.go` — `nodeGrammar`:
  - `detect`: a line matching `(?m)^\s+at .+:\d+:\d+\)?$`.
  - `parse`: header = text before the first `at ` frame (the `Error: msg` line);
    per frame, `^\s*at (?:(.+) \()?(.+):(\d+):(\d+)\)?$` → func (may be empty →
    "anonymous"), file, line, col.
  - classify: `node:internal/…` ⇒ `FrameStdlib` (runtime); path under
    `node_modules/` ⇒ `FrameThirdParty`; else `FrameProject`. `module` is
    ignored for Node.
  - set `StackTrace.Lang = "node"`.
- Register `nodeGrammar{}` in `grammars`.

## Step 4 — Render `Col`

- `internal/render/plain.go` — where a frame's `file:line` is printed, append
  `:col` when `Col > 0`. Single-spot change; everything else (`► project` /
  `… N framework frames`) already generalizes since it keys off `Frame.Kind`.

## Tests (go-test-author + go-styleguide govern)

- `enrich_test.go` — keep existing Go cases green throughout (refactor proof).
- New field-scan cases: Go trace in `stack` field and in `error` field → lifted,
  field consumed, `Fields` no longer contains it; a non-trace field with `"at "`
  text → NOT lifted (false-positive guard).
- `trace_node_test.go` — table tests: named frame, anonymous/`<eval>` frame,
  `node:internal` ⇒ runtime, `node_modules` ⇒ third-party, project frame, `Col`
  captured; header extraction.
- `FuzzParseTrace` over `detectAndParse` — seeds per language; invariants: never
  panics, and a returned `*StackTrace` has ≥1 frame (no empty/garbage trace).
- `testdata/` — `node-stack.log` (JSON record with a pino-style `stack` field),
  `go-stack-field.log` (Go trace in a non-`msg` field).

## Files

- `internal/enrich/stacktrace.go` — `Stack` (field scan + consume), `looksLikeTrace`
- `internal/enrich/trace.go` — `traceGrammar`, registry, `detectAndParse` (new)
- `internal/enrich/trace_go.go` — Go grammar, extracted (new)
- `internal/enrich/trace_node.go` — Node grammar (new)
- `internal/record/record.go` — additive `Frame.Col`, `StackTrace.Lang`
- `internal/render/plain.go` — surface `:col`
- `internal/enrich/{enrich,trace_node}_test.go` — tables + `FuzzParseTrace`
- `testdata/node-stack.log`, `testdata/go-stack-field.log`

## Verification

```sh
go test ./internal/enrich/...                                   # unit + tables
go test -run=^$ -fuzz=FuzzParseTrace -fuzztime=30s ./internal/enrich
go test ./...                                                   # full suite green
gofmt -l . && go vet ./...
go build -o /tmp/plog ./cmd/plog
/tmp/plog --no-color < testdata/node-stack.log   # Node stack collapses: ► project, … N framework
/tmp/plog --no-color < testdata/go-stack-field.log
/tmp/plog --no-color < testdata/sample.log       # regression: Go-in-msg unchanged
```

Success = a Node trace embedded in a JSON `stack` field renders collapsed (project
frame surfaced with `file:line:col`, `node_modules`/`node:internal` folded), a Go
trace in a non-`msg` field collapses the same as one in `msg`, and `sample.log`
output is unchanged.

## Sequencing note

Steps 1–2 are behavior-preserving for existing inputs (proven by `enrich_test.go`
staying green) and can land as one reviewable commit; Steps 3–4 add Node as a
second commit. Ship Step 1 first if an early Go-field-scan win is wanted before
the registry refactor.
