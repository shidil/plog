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

Run flags: `--module` (import-path prefix treated as project code, default `github.com/example`), `--no-fold`, `--no-columns`, `--min-level`, `--grep`, `--field` (repeatable), `--expand-stack`, `--no-color`.

## Architecture

A streaming pipeline in `cmd/plog/main.go`, one record at a time with bounded memory:

```
stdin ─▶ parse ─▶ enrich.Severity ─▶ enrich.Stack ─▶ Filter.Match ─▶ Columns.Mark ─▶ Folder.Add ─▶ render ─▶ stdout
```

All record processing — every stage above — runs on one goroutine, so no stage holds shared state or needs locking. A second goroutine (`scanLines`) does nothing but read raw lines off stdin and hand them to the main loop over a channel; this exists only so the main loop can also wake on a timer (`flushInterval`) to flush a folded run that is still open — see the known trade-off below. The reader is stopped via a `done` channel on return, so an early exit never strands it.

`internal/record.Record` is the canonical type every stage reads and returns — it has no behavior of its own, which keeps the stages independent.

Key design constraints a change must respect:

- **Stream-only, by design.** When piped, stdin carries the log stream, so there is no interactive keyboard TUI — it cannot coexist with a live pipe. A future TUI (for a `plog <file>` case) is meant to be a second `render.Renderer` implementation, not a change to the pipeline. `render.Renderer` is the extension seam.
- **Passthrough is sacred.** `parse.Line` never errors: a non-JSON or malformed line returns `Record{Parsed: false, Raw: line}` and the renderer emits it verbatim. A bad line must never interrupt or crash the tail. JSON is decoded with a token walk (not into a map) to preserve field order.
- **Enrichment is pure.** `enrich.Severity` and `enrich.Stack` are `Record` in → `Record` out with no state, which is why their tests are trivial table tests. Two stages hold cross-record state: `enrich.Folder` (in `cluster.go`) collapses consecutive runs, and `enrich.Columns` (in `columns.go`) tracks a bounded window to flag constant fields.
- **Adaptive columns demote, never drop.** `enrich.Columns.Mark` keeps a sliding window (default 256 records) of recent field sets and flags a field `Demoted` only with evidence — a single distinct value seen at least `minSeen` (3) times. New, rare, or varying fields stay salient, so signal is never hidden. Unlike `Folder` it adds no latency (it annotates and passes through); the renderer leads with salient fields and trails demoted ones dimmed. `--no-columns` disables it. It runs before `Folder` so the window sees every record, and because it only sets a flag (never changes a value) `Template`/folding are unaffected.
- **Filtering selects, it does not enrich.** `internal/filter` is a separate, pure-predicate package (`Filter.Match`) depending only on `record`, not part of `enrich`. It runs after `Stack` (so `Effective` and `Message` are final) and before `Columns`/`Folder`, so the column window and fold runs reflect only what's displayed. `--min-level` compares `Effective` (the re-ranked level); `--field key=val` is a repeatable, case-insensitive substring over a named field's value (the record must carry that key) — it makes no assumption about which field names a stream uses, unlike a hardcoded "component" key would; `--grep` is a regexp over message + field values. Sacred-passthrough holds: `--min-level`/`--field` never drop a non-JSON line (their criteria are unknowable for it) — only `--grep`, matched against the raw line, can.
- **Severity is re-ranked, never lowered.** `enrich.Severity` raises `Record.Effective` above the declared `Level` when message/error content matches escalation patterns (panic, nil pointer, connection refused, ...). An explicit `ERROR` stays `ERROR`. The renderer shows a re-rank as `INFO→ERR`.
- **Stack-frame classification depends on `--module`.** `enrich.Stack` parses a Go trace embedded in a `msg` field and classifies each frame as project / third-party / stdlib. "Project" means the function *or file path* contains the module prefix; this is what surfaces `location_rpc.go:72` (`►`) and folds framework frames. Changing the default prefix changes what gets highlighted.

### Known trade-off

`Folder` holds a run's first record until a record with a different `Template` ends the run, so folded lines appear with one line of latency on a live tail. The run is otherwise only flushed at EOF — which a follow (`docker logs -f`) never reaches, so a `flushInterval` (1s) timer in the main loop emits any still-open run rather than hold it indefinitely. This matters most under filtering: a `--grep`/`--field`/`--min-level` that narrows the stream to one near-identical event leaves nothing distinct to end the run, so without the timer nothing printed until the pipe closed (the original "grep does nothing under `-f`" bug). The timer bounds that latency to ~1s; the cost is that a busy run's count splits across ticks (`×3` then `×5` …) instead of accumulating to one. `--no-fold` disables folding for zero-latency raw streaming. Bound the latency by flushing sooner (the timer), not by buffering more, which is an intentional spike-scope choice.

## Conventions

- Pure Go stdlib except `lipgloss` (styling). Keep the core dependency-light; color is gated on TTY detection (`os.ModeCharDevice`) so piped output stays clean text.
- The `go-styleguide` and `go-test-author` skills are the source of truth for Go style and test scaffolding in this repo.
