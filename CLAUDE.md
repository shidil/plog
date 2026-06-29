# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`plog` is a streaming pretty-printer for structured JSON logs: it reads log lines on stdin and writes a readable, colorized stream to stdout (`docker logs -f storefront | plog`). It is a spike implementing four features from `IDEA.md`; the rest are deferred phase-2 (also listed there).

## Commands

```sh
go run ./cmd/plog < testdata/sample.log    # run against the bundled sample
go build -o plog ./cmd/plog                # build the binary
go test ./...                              # all tests
go test ./internal/enrich -run TestStack   # a single test
go test -run=^$ -fuzz=FuzzParseLine -fuzztime=30s ./internal/parse   # fuzz the parser
gofmt -l . && go vet ./...                 # format check + vet
```

Run flags: `--module` (import-path prefix treated as project code, default `github.com/example`), `--no-fold`, `--expand-stack`, `--no-color`.

## Architecture

A single-goroutine streaming pipeline in `cmd/plog/main.go`, one record at a time with bounded memory:

```
stdin ─▶ parse ─▶ enrich.Severity ─▶ enrich.Stack ─▶ Folder.Add ─▶ render ─▶ stdout
```

`internal/record.Record` is the canonical type every stage reads and returns — it has no behavior of its own, which keeps the stages independent.

Key design constraints a change must respect:

- **Stream-only, by design.** When piped, stdin carries the log stream, so there is no interactive keyboard TUI — it cannot coexist with a live pipe. A future TUI (for a `plog <file>` case) is meant to be a second `render.Renderer` implementation, not a change to the pipeline. `render.Renderer` is the extension seam.
- **Passthrough is sacred.** `parse.Line` never errors: a non-JSON or malformed line returns `Record{Parsed: false, Raw: line}` and the renderer emits it verbatim. A bad line must never interrupt or crash the tail. JSON is decoded with a token walk (not into a map) to preserve field order.
- **Enrichment is pure.** `enrich.Severity` and `enrich.Stack` are `Record` in → `Record` out with no state, which is why their tests are trivial table tests. The one stateful piece is `enrich.Folder` (in `cluster.go`), which collapses consecutive runs and is the only stage holding cross-record state.
- **Severity is re-ranked, never lowered.** `enrich.Severity` raises `Record.Effective` above the declared `Level` when message/error content matches escalation patterns (panic, nil pointer, connection refused, ...). An explicit `ERROR` stays `ERROR`. The renderer shows a re-rank as `INFO→ERR`.
- **Stack-frame classification depends on `--module`.** `enrich.Stack` parses a Go trace embedded in a `msg` field and classifies each frame as project / third-party / stdlib. "Project" means the function *or file path* contains the module prefix; this is what surfaces `location_rpc.go:72` (`►`) and folds framework frames. Changing the default prefix changes what gets highlighted.

### Known trade-off

`Folder` holds a run's first record until the run ends, so folded lines appear with one line of latency on a live tail (a quiet tail leaves the last repeated line unflushed until the next distinct line). `--no-fold` disables folding for zero-latency raw streaming. Do not "fix" this by buffering more — it is an intentional spike-scope choice.

## Conventions

- Pure Go stdlib except `lipgloss` (styling). Keep the core dependency-light; color is gated on TTY detection (`os.ModeCharDevice`) so piped output stays clean text.
- The `go-styleguide` and `go-test-author` skills are the source of truth for Go style and test scaffolding in this repo.
