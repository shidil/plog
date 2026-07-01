# Continuation-line gluing (IDEA.md #16)

Status: proposed (scoping) · Scope: pre-parse layer + per-language collapse
· Owner: spike

plog is a **multi-language** log viewer — tracebacks arrive from Go, Python,
Rust, Node/Bun, Java, and more. That it is *written* in Go is incidental. This
scope treats traceback shapes across languages as a first-class requirement, not
a Go-only feature.

## Problem

A traceback does not always arrive inside one log record. Each runtime spreads
it across **separate physical lines** that the line reader hands to `parse`
individually → each becomes a passthrough record → emitted verbatim, never
reassembled. `enrich.Stack` only sees a trace already embedded in one record's
`Message`, so split traces get none of the frame collapse / project highlighting
that #3 delivers. The shapes differ per language but share a structure (a header
line followed by indented frame lines):

```
# Go (buildkit, testdata/logfmt.log:6-10) — frames at col 0, indented location
goroutine 23 [running]:
main.foo(...)
	/app/main.go:12 +0x1a

# Python — indented body, col-0 exception line closes the block
Traceback (most recent call last):
  File "/app/main.py", line 5, in foo
    raise ValueError("x")
ValueError: x

# Node/Bun — header then 4-space "at" frames
TypeError: cannot read properties of undefined
    at foo (/app/index.js:10:9)
    at Module._compile (node:internal/modules/cjs/loader:1234:14)

# Rust — panic line, then numbered frames with indented "at"
thread 'main' panicked at src/main.rs:3:5:
stack backtrace:
   0: core::panicking::panic_fmt
             at /rustc/.../library/core/src/panicking.rs:75
```

## Why this is harder than #15

Multi-format parsing (#15) was a pure per-line transform — sacred passthrough
held for free. #16 must decide that line N *continues* line N-1, which means
**buffering across physical lines** and **deferring emission** until a block is
complete. That collides with three constraints at once:

1. **Stream-only / bounded memory** — a block must be size-capped and
   timer-flushed, exactly as `Folder` already does (`enrich/cluster.go:68-96`).
2. **Sacred passthrough** — mis-gluing two unrelated lines *corrupts* output, a
   worse failure than not gluing. Detection must be conservative.
3. **Enrichment runs only on parsed records** — `enrich.Stack` and the renderer
   both early-return on `Parsed == false` (`render/plain.go:56`). Delivering the
   *collapse* (the win) requires reaching into Stack and render too.

## Two separable layers

The work splits cleanly, and the split is what makes multi-language tractable:

- **Assembly (largely language-agnostic).** Reattach the physical lines of a
  block into one logical unit. Most languages share one rule: *a header marker
  starts a block; indented lines continue it.* Go is the lone exception (its
  frame **function** lines sit at column 0). This layer is achievable for all
  target languages at once.
- **Collapse (inherently per-language).** Parse frames, fold runtime/vendor
  noise, surface "your code." This needs a frame grammar **and** a project-path
  heuristic per language. Go exists today; each new language is its own unit.

Treating these as one feature is the trap; scoping them apart is what lets
assembly ship broadly while collapse rolls out language by language.

## Design

### A `Gluer` stage, pre-parse, mirroring `Folder`

Insert between `scanLines` and `parse.Line` in `cmd/plog`. It mirrors `Folder`'s
buffer/flush contract but operates on raw strings:

```go
// internal/assemble (new package)
type Gluer struct { ... }
func NewGluer(enabled bool) *Gluer
func (g *Gluer) Add(line string) []string // completed logical lines (0+)
func (g *Gluer) Flush() []string           // open block, at EOF / on timer
```

A logical line is one or more physical lines joined by `\n`. The main loop's
existing `flushInterval` ticker (already there for `Folder`) also flushes the
Gluer, bounding live-tail latency to ~1s — the same trade-off `Folder` accepts.
On a tick: flush Gluer → its output runs through parse→…→Folder → flush Folder.

### Detection: a `TraceDetector` registry (parallels the #15 format parsers)

One detector per language, tried in order; the first whose `StartsBlock` fires
governs the block until it ends:

```go
type TraceDetector interface {
	Lang() string
	StartsBlock(line string) bool          // header marker
	Continues(line, prev string) bool      // is line part of the open block?
	EndsBlock(line string) bool            // closes and is included (e.g. Python exc line)
}
```

- **Generic continuation** (Python, Node, Rust, Java): a leading-whitespace line
  continues the open block. Header markers: `Traceback (most recent call last):`,
  `^\w[\w.]*(Error|Exception|Warning):` (Node/Python), `thread '.*' panicked at`,
  `stack backtrace:` (Rust), `Exception in thread` / `Caused by:` (Java).
- **Go special case**: a col-0 line that looks like a package-qualified function
  immediately followed by an indented `\t…:NN` location is a frame pair (Go
  frames are not indented). Reuse the grammar in `enrich.parseTrace`.
- **Python special close**: inside an open `Traceback` block, a col-0
  `ExceptionType: message` line is the final line — include it, then end.
- **End**: any line that is neither a continuation nor a closer ends the block
  and starts fresh (it may itself be a new record).

Conservative by construction: on ambiguity the block ends and lines emit
individually. `--no-glue` disables the whole stage.

### Closing the collapse gap (option B)

The Gluer joins a block into one passthrough record whose `Raw` holds the
`\n`-joined text. Then:

1. Extend `enrich.Stack` to parse a trace out of `Raw` when `Parsed == false`,
   and to dispatch on language (header-less Go dumps, plus Python/Node/Rust/Java
   frame grammars) — not just the current `goroutine`-headed Go path.
2. Generalize the `--module` notion to a per-language **project-path** heuristic
   for the classify step: Node = not under `node_modules/` or `node:internal`;
   Python = not stdlib / `site-packages`; Rust = not `/rustc/` or
   `.cargo/registry`; Go = `--module` prefix (today).
3. One new render branch: a passthrough record carrying a non-nil `Stack`
   renders the collapsed block. Folding already keys off `Stack.Header`
   (`cluster.go:29`), so collapsed traces fold correctly.

Rejected — **A** (Gluer builds the Record itself: couples assembly to
per-language parsing); **C** (a third record kind: more surface, no gain).

Out of MVP: merging a trace into the *preceding* error record (needs
cross-record lookahead, fragile). Emitting the trace as its own collapsed record
already delivers the win.

## Invariants & risks

- **Sacred passthrough** — a block that fails to parse emits as its original
  physical lines; gluing never drops or reorders.
- **Bounded memory** — cap a block (e.g. 500 lines / N·maxLine bytes); on
  overflow, emit what is buffered and stop gluing it.
- **False gluing** is the headline risk — mitigated by per-language markers + a
  strict end rule; `--no-glue` is the escape hatch.
- **Interleaved frames** — concurrent runtimes can interleave goroutine/async
  frames from different tasks; a glued block may mix them. Already true of the
  raw log; documented, not solved.
- **Live-tail latency** — identical to `Folder`'s timer-bounded trade-off.

## Phasing

1. **Assembly for all languages** — Gluer + `TraceDetector` registry + render of
   the reassembled (dedented, visually grouped) block. Universal; immediate
   readability win even before per-language frame folding.
2. **Collapse, per language** — option B. Go is done; **Node lands now** (its
   `at func (file:line:col)` grammar is the most regular). Each adds a frame
   parser + project-path heuristic. Python, Rust, and Java are **assembly-only**
   in this pass — they reassemble into a clean grouped block but are not yet
   frame-classified; their collapse follows incrementally (Python next).

## Files (estimate)

- `internal/assemble/glue.go`, `detectors.go` (+ tests, `FuzzGlue`) — new
- `internal/enrich/stacktrace.go` — language dispatch; parse from passthrough Raw
- `internal/enrich/` — per-language frame parsers + classify heuristics
- `internal/render/plain.go` — render a passthrough record carrying a Stack
- `cmd/plog/main.go` — wire Gluer before parse; `--no-glue`; project-path flags
- `testdata/` — split-trace samples per language

## Decisions

- **Assembly languages**: Go, Python, Rust, Node/Bun, Java (extensible
  registry) — all reassemble in this pass. ✓
- **Default on**, with `--no-glue`. ✓
- **Collapse now** (not assembly-only). ✓
- **Initial collapse set**: **Go + Node**. Python, Rust, Java are assembly-only
  until their per-language collapse follows (Python next). ✓
- Option B's narrow, documented widening of "passthrough is never enriched" is
  accepted as the mechanism for collapse.
