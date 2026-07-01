# Multi-language stack-trace intelligence (extends IDEA.md #3)

Status: proposed · Scope: `enrich.Stack` only (parsed records) · Owner: spike

plog is a multi-language log viewer, but stack-trace intelligence (#3) is
Go-only and looks in the wrong place. This generalizes the existing enrichment
stage — no new pipeline stage, no buffering — to cover the common production
case across languages. It is also the reusable core of #16 (the per-language
frame grammar + classify built here is exactly what #16's collapse step calls).

## Problem — two gaps in `enrich.Stack`

`enrich.Stack` parses a trace from **only `rec.Message`** and recognizes **only
Go** (`parseTrace(rec.Message, module)`, keyed on `goroutine N [running]:` —
`stacktrace.go:23,13`). So:

1. **It misses traces in dedicated fields.** Production structured loggers don't
   put the trace in `msg`: pino/winston use `err.stack` or `stack`, Python
   json-logging uses `exception`/`exc_info`. A clean
   `{"level":"error","msg":"request failed","stack":"Error: x\n    at ..."}`
   gets **zero** stack treatment today — the trace sits in `Fields`, not
   `Message`, so the renderer prints it as one giant raw field value.
2. **It is Go-only**, so even an embedded Node/Python trace would not collapse.

The first gap matters most: services that emit structured JSON — the
`docker logs -f` target — embed the stack in a field *within one record*. That
is the majority of production logging. (#16 handles the orthogonal case of a
trace split across separate **physical lines**; this doc does not.)

## Scope boundary

- **Parsed records only.** This touches the trace *source* (Message + fields)
  and the *frame grammar* (per language). It does **not** touch passthrough or
  line buffering — sacred passthrough is untouched. Split-across-physical-lines
  is #16, which reuses the grammars defined here.
- **Initial collapse set: Go + Node** (per the #16 scoping decision). Python,
  Rust, and Java are deferred; their frame grammars follow incrementally (Python
  next). Until then, a Python/Rust/Java trace embedded in a field still renders
  as today (raw) — no regression, just not yet collapsed.

## Design

### 1. Find the trace: scan Message, then fields

`Stack` first tries `rec.Message` (today's behavior, preserved). If no trace is
found, it scans `rec.Fields` **values** in source order and lifts the first
value that parses as a trace into `rec.Stack`, **consuming that field** (removed
from `Fields`, like `msg` — so it is not also rendered raw). Folding is
unaffected: `Template` already keys off `Stack.Header` when a Stack is present
(`cluster.go:29`).

Detection is **content-driven**, not key-name-driven, so it works regardless of
which key a logger chose (`stack`, `err.stack`, `exception`, …). A cheap
pre-check guards the cost: only attempt a parse when the value contains a newline
**and** a language pre-marker (`goroutine `/`\tat `/`\n    at `/…). Known keys
(`stack`, `err.stack`, `error.stack`, `exception`, `stacktrace`, `trace`) are
tried first as a fast path, but any field can match on content.

### 2. Parse frames: a per-language grammar registry

Generalize the single `parseTrace` into a small registry that mirrors the #15
format-parser shape:

```go
type traceGrammar interface {
	Lang() string
	detect(s string) bool                       // header/shape signature
	parse(s string, proj projectRule) *record.StackTrace
}
```

- **Go** — existing `parseTrace` logic (goroutine header / `panic:` + col-0 fn
  followed by indented `\t…:NN`).
- **Node/Bun** — header `^\w*(Error|Exception):` then `^\s*at <fn> (<file>:<line>:<col>)`
  (or `at <file>:<line>:<col>`). Highly regular; `<eval>`/anonymous frames handled.

The registry tries grammars by `detect`; the first match parses. `record.Frame`
gains an optional `Col int` (0 when absent) for the JS `file:line:col` shape;
`record.StackTrace` gains `Lang string` for render hints. Both are additive.

### 3. Classify frames: per-language project / third-party / runtime

`classify(fn, file, module)` is Go-specific today (module prefix + dotted-segment
heuristic). Generalize to a per-language `projectRule`:

- **Go** — `--module` prefix (unchanged); leading dotless segment ⇒ runtime.
- **Node** — `node:internal/…` ⇒ runtime; under `node_modules/` ⇒ third-party;
  everything else ⇒ project.

`FrameKind` is unchanged (`FrameStdlib` reads as "runtime/builtin",
`FrameThirdParty`, `FrameProject`); only the per-language decision differs. The
renderer's existing "► project frame / … N framework frames" treatment then
works for both languages with no render change beyond surfacing `Col`.

### 4. Project-path configuration

`--module` stays (Go). Node needs no flag — `node_modules`/`node:` detection is
automatic. Reserve a future general `--project <prefix>` (repeatable) for
languages without a clean convention, but it is not required for Go+Node.

## Invariants & risks

- **Sacred passthrough untouched** — this is purely the parsed-record path.
- **No new latency / no buffering** — it stays a pure `Record`-in/`Record`-out
  enrich stage; cost is bounded by the newline+marker pre-check so non-trace
  records pay almost nothing.
- **False positive: a non-trace field read as a trace** — mitigated by requiring
  ≥1 well-formed frame and a conservative grammar; a value that does not parse is
  left as an ordinary field.
- **Which field wins when several look like traces** — first in source order,
  known keys prioritized. Documented; multiple stacks in one record are rare.
- **No regression for deferred languages** — a Python/Rust/Java trace simply is
  not lifted yet and renders exactly as today.

## Phasing

1. **Field-scan + consume** for the existing Go grammar — immediately fixes the
   biggest gap (Go stacks in a `stack`/`error` field) with almost no new code.
2. **Node grammar + classify** — the first new language; the registry refactor
   lands with it.
3. (Later) Python, then Rust, Java — each a grammar + `projectRule`, reused by
   #16.

## Files (estimate)

- `internal/enrich/stacktrace.go` — split into a grammar registry; add field
  scan + field consumption; generalize `classify` to a `projectRule`
- `internal/enrich/stack_node.go` (+ Go grammar extracted alongside) — new
- `internal/record/record.go` — additive `Frame.Col`, `StackTrace.Lang`
- `internal/render/plain.go` — surface `Col` in the frame line (minor)
- `internal/enrich/*_test.go` — per-language grammar + classify tables;
  field-selection tests; `FuzzParseTrace` over each grammar (untrusted input)
- `testdata/` — a JSON log with a Node stack in `stack`, a Go stack in a field

## Decisions

- **Source**: scan `Message` then field values, content-driven, consume the
  matched field. ✓ (recommended)
- **Initial languages**: Go + Node; Python/Rust/Java deferred. ✓ (per #16)
- **Open**: confirm the additive `record` changes (`Frame.Col`,
  `StackTrace.Lang`) are acceptable, and whether to ship phase 1 (Go field-scan)
  as its own small step before the Node grammar refactor.
