# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`plog` is a streaming pretty-printer for structured logs (JSON or logfmt): it reads log lines on stdin and writes a readable, colorized stream to stdout (`docker logs -f storefront | plog`). It began as a spike around four features from `IDEA.md`; the shipped set is now broader (severity re-ranking, template folding, Go+Node stack collapse, adaptive columns, request correlation, filtering, multi-format parsing), with the rest deferred phase-2 (also listed there).

This file is the single source of truth for coding agents — architecture, invariants, the package map, and where to make common changes. `README.md` is the user-facing overview (different audience); when the two disagree, this file wins on design intent and code details.

## Commands

```sh
go run ./cmd/plog < testdata/sample.log    # run against the bundled sample
go build -o plog ./cmd/plog                # build the binary
go test ./...                              # all tests
go test ./internal/enrich -run TestStack   # a single test
go test -run=^$ -fuzz=FuzzParseLine -fuzztime=30s ./internal/parse   # fuzz the parser
gofmt -l . && go vet ./...                 # format check + vet
```

Run flags: `--module` (import-path prefix treated as project code, default `github.com/example`), `--format` (`auto` (sniff, default) / `json` / `logfmt` / `text` (force passthrough)), `--no-fold`, `--no-columns`, `--no-correlate`, `--min-level`, `--grep`, `--field` (repeatable `key=val`), `--expand-stack`, `--no-color`.

Bundled `testdata/` samples, each exercising a different path:

| Sample | Exercises |
|---|---|
| `sample.log` | Plain JSON app logs (severity, columns) |
| `rpc.log` | Connect-RPC `finished call` logs with `rpc.*` fields — correlation grouping/linking + column demotion |
| `go-stack-field.log` | A Go panic trace in a `stack` field — `enrich.Stack` (Go grammar), project/3p/stdlib classification, severity escalation |
| `node-stack.log` | A Node.js `TypeError` stack in a `stack` field — Node grammar + `node_modules`/`node:internal` classification |
| `logfmt.log` | logfmt (`key=value`) lines — the non-JSON decoder path |
| `nornic.log` | Mojibake + emoji non-JSON lines — the sacred-passthrough guarantee |

## Architecture

A streaming pipeline in `cmd/plog/main.go`, one record at a time with bounded memory:

```
stdin ─▶ parse ─▶ enrich.Severity ─▶ enrich.Stack ─▶ Filter.Match ─▶ Correlator.Mark ─▶ Columns.Mark ─▶ Folder.Add ─▶ render ─▶ stdout
```

All record processing — every stage above — runs on one goroutine, so no stage holds shared state or needs locking. A second goroutine (`scanLines`) does nothing but read raw lines off stdin and hand them to the main loop over a channel; this exists only so the main loop can also wake on a timer (`flushTick`, 250ms) to flush a folded run that is still open — see the known trade-off below. The reader is stopped via a `done` channel on return, so an early exit never strands it.

`internal/record.Record` is the canonical type every stage reads and returns — it has no behavior of its own, which keeps the stages independent.

The pipeline is two closures in `run`: `process(line, now)` runs the per-record chain and early-returns when `Filter.Match` is false (a dropped line emits nothing), otherwise ending in `emit(Folder.Add(rec, now))`; `emit` renders each record a fold stage releases. Only `Folder` (`Add`/`FlushIdle`/`Flush`) ever returns more than the current record, which is why it is the sole latency-adding stage. The main `select` loop multiplexes a new line (→ `process`) against a `flushTick` tick (→ `emit(Folder.FlushIdle(now))`); on EOF it does a final `emit(Folder.Flush())` and returns any scan error. Bad flags exit before streaming (validation → stderr + non-zero exit); there is no in-stream parse error (a malformed line becomes a passthrough `Record`); only scan errors (`errc`) and render/write errors propagate out of `run`.

Key design constraints a change must respect:

- **Stream-only, by design.** When piped, stdin carries the log stream, so there is no interactive keyboard TUI — it cannot coexist with a live pipe. A future TUI (for a `plog <file>` case) is meant to be a second `render.Renderer` implementation, not a change to the pipeline. `render.Renderer` is the extension seam.
- **Passthrough is sacred.** `parse.Line` never errors: a non-JSON or malformed line returns `Record{Parsed: false, Raw: line}` and the renderer emits it verbatim. A bad line must never interrupt or crash the tail. JSON is decoded with a token walk (not into a map) to preserve field order.
- **Enrichment is pure.** `enrich.Severity` and `enrich.Stack` are `Record` in → `Record` out with no state, which is why their tests are trivial table tests. Three stages hold cross-record state: `enrich.Folder` (in `cluster.go`) collapses repeated events (including interleaved ones), `enrich.Columns` (in `columns.go`) tracks a bounded window to flag constant fields, and `enrich.Correlator` (in `correlate.go`) remembers a bounded window to group requests and link related events.
- **Adaptive columns demote, never drop.** `enrich.Columns.Mark` keeps a sliding window (default 256 records) of recent field sets and flags a field `Demoted` only with evidence — a single distinct value seen at least `minSeen` (3) times. New, rare, or varying fields stay salient, so signal is never hidden. Unlike `Folder` it adds no latency (it annotates and passes through); the renderer leads with salient fields and trails demoted ones dimmed. `--no-columns` disables it. It runs before `Folder` so the window sees every record, and because it only sets a flag (never changes a value) `Template`/folding are unaffected.
- **Filtering selects, it does not enrich.** `internal/filter` is a separate, pure-predicate package (`Filter.Match`) depending only on `record`, not part of `enrich`. It runs after `Stack` (so `Effective` and `Message` are final) and before `Columns`/`Folder`, so the column window and fold runs reflect only what's displayed. `--min-level` compares `Effective` (the re-ranked level); `--field key=val` is a repeatable, case-insensitive substring over a named field's value (the record must carry that key) — it makes no assumption about which field names a stream uses, unlike a hardcoded "component" key would; `--grep` is a regexp over message + field values. Sacred-passthrough holds: `--min-level`/`--field` never drop a non-JSON line (their criteria are unknowable for it) — only `--grep`, matched against the raw line, can.
- **Correlation looks only backward, never reorders.** `enrich.Correlator.Mark` runs after `Filter` (so it correlates only displayed lines) and before `Columns`/`Folder`. It does two independent things off a bounded window (512 entries, `corrWindow`): (1) **grouping** — it sets `Record.Corr` to a short stable tag derived from a correlation *key* (the first explicit id field in `corrIDKeys`, else a client IPv4 pulled from the message), but only once that key has *recurred* within `groupSpan` (1m), so a one-off id adds no noise; (2) **causal linking** — it sets `Record.Related` to a backward reference when a record shares a *method token* (`rpc.method`, else the trailing name of the first project stack frame) with a recent **elevated** (`Effective >= Warn`), **distinct-template** event within `linkSpan` (5s). Because the stream emits immediately (never held), the link attaches to the *later* event pointing back — the validation `finished call` shows `↳ likely related: panic …`, not the reverse. The link is **advisory**, a heuristic hint, not a proven causal edge; it never drops or reorders a record. `--no-correlate` disables both. Two consequences of backward-only: the *first* occurrence of a group key is untagged (the tag appears from the second on), and the tag is a hash (rare collisions only blur which lines share a tag). It runs before `Folder`/`Columns` so its window sees every displayed record, and it only sets `Corr`/`Related` (never touches `Fields` or `Message`), so `Template`, folding, and columns are unaffected.
- **Severity is re-ranked, never lowered.** `enrich.Severity` raises `Record.Effective` above the declared `Level` when message/error content matches escalation patterns (panic, nil pointer, connection refused, ...). An explicit `ERROR` stays `ERROR`. The renderer shows a re-rank as `INFO→ERR`.
- **Stack-frame classification depends on `--module`.** `enrich.Stack` (`stacktrace.go`) finds a trace embedded in a `msg`/`stack` field and hands it to the first matching grammar in the `traceGrammar` registry (`trace.go`; Go and Node.js today), which classifies each frame as project / third-party / stdlib. For Go, "project" means the function *or file path* contains the module prefix; this is what surfaces `location_rpc.go:72` (`►`) and folds framework frames. Changing the default prefix changes what gets highlighted. Adding a language is a new `trace_<lang>.go` grammar plus a registry entry — `Stack` and the renderer are language-agnostic.

### Known trade-off

`Folder` keeps a bounded set of open runs (one per distinct `Template`, oldest head first) rather than a single pending record, so interleaved event types fold in parallel: the common request-log / result-log pairing (`A, B, A, B, …`) collapses to `A×n` and `B×n` instead of defeating folding, because a run tolerates up to `foldWindow` (10) intervening records before it is considered ended. Runs are always flushed in head order, so output stays time-ordered (no folded line ever prints out of timestamp sequence); `maxOpenRuns` (8) caps concurrent runs, evicting the oldest. The cost is head latency: a run's head is held until the run ends — structurally (a different template takes over within `foldWindow`), by pausing, or by aging out (see the timer below) — so folded lines lag on a live tail. Structural end aside, a run is otherwise only flushed at EOF — which a follow (`docker logs -f`) never reaches — so a `flushTick` (250ms) timer in the main loop calls `Folder.FlushIdle(now)` to reveal still-open runs. This matters most under filtering: a `--grep`/`--field`/`--min-level` that narrows the stream to one near-identical event leaves nothing distinct to end the run, so without the timer nothing printed until the pipe closed (the original "grep does nothing under `-f`" bug). `FlushIdle` applies a wall-clock policy rather than flushing everything each tick, which is what keeps counts clean: a run that has folded nothing for `idleFor` (750ms) has *paused* and is revealed promptly (head latency ≈ `idleFor` + a tick), while a run *still folding* records is held — accumulating one clean count — until `maxHold` (3s). So a sustained storm surfaces as one `×n` per `maxHold` window instead of splitting on every tick (the earlier blunt 1s flush split a busy run into `×3` then `×5` …); the residual cost is that a storm lasting longer than `maxHold` still splits at that boundary. The wall-clock `now` is injected into `Folder.Add`/`FlushIdle` (the main loop passes `time.Now()`), keeping `Folder` clock-free and unit-testable. `--no-fold` disables folding for zero-latency raw streaming. Bound the latency by revealing sooner (the timer), not by buffering more, which is an intentional spike-scope choice.

## Package map

| Path | Role |
|---|---|
| `cmd/plog/main.go` | Flag parsing (`main`), pipeline driver (`run`), reader goroutine (`scanLines`), `fieldFlags` flag.Value, TTY detection (`isTerminal`), the `process`/`emit` closures |
| `internal/record/record.go` | The canonical `Record` struct + value types (`Level`, `KV`, `Frame`, `StackTrace`, `FrameKind`, `Related`) and `ParseLevel`/`Level.String`. Behavior-free data carrier. |
| `internal/parse/parse.go` | Public `LineAs(line, format)` (+ `Line` auto-sniff wrapper), `Format`/`FormatFromString`, format dispatch, and the format-agnostic canonicalizer (time/level/msg alias extraction) |
| `internal/parse/json.go` | JSON token-walk decoder (`parseJSON`), value render, `sniffJSON` |
| `internal/parse/logfmt.go` | logfmt byte-scanner decoder (`parseLogfmt`), `sniffLogfmt`, quoted-value handling |
| `internal/enrich/severity.go` | `Severity` — pure, raises `Effective` on escalation patterns (`escalation` table) |
| `internal/enrich/stacktrace.go` | `Stack(rec, module)` — entry point: finds a trace in `msg`/fields (`looksLikeTrace`), delegates parsing, picks a header (`pickHeader`) |
| `internal/enrich/trace.go` | `traceGrammar` interface + the language registry `grammars = {goGrammar, nodeGrammar}` and `detectAndParse` — **the seam for new languages** |
| `internal/enrich/trace_go.go` | `goGrammar`: Go trace parsing, `parseLocation`, frame `classify` (project/3p/stdlib by `--module`) |
| `internal/enrich/trace_node.go` | `nodeGrammar`: Node.js `at …` frame parsing, `classifyNode` (`node_modules`/`node:internal`) |
| `internal/enrich/cluster.go` | `Folder` — template masking, open-run set, `foldWindow`/`maxOpenRuns`, idle/`maxHold` flush policy, injected clock |
| `internal/enrich/columns.go` | `Columns` — sliding-window demote-never-drop field analysis |
| `internal/enrich/correlate.go` | `Correlator` — backward-only grouping (`Corr`) + causal linking (`Related`) |
| `internal/filter/filter.go` | `Filter`, `New` (flag parse/validate), `Match` predicate |
| `internal/render/render.go` | `Renderer` interface (`Render(record.Record) error`) — the extension seam |
| `internal/render/plain.go` | `Plain` renderer: badge/re-rank, salient-vs-demoted fields, fold counts, corr tags, related notes, stack folding, color gate |

Each package has adjacent `*_test.go` files; the parser also has `FuzzParseLine`.

## Making changes

- **Add a CLI flag:** declare it in `cmd/plog/main.go` (`main`), thread it into the stage config, and consume it in the relevant stage. Validate up front and exit non-zero on error.
- **Add an enrich stage:** if pure, model it on `enrich.Severity` (`Record` in → `Record` out, trivial table test). If stateful, model it on `Folder`/`Columns`/`Correlator` and wire it into the `process` closure at the right position — before `Folder`, and after `Filter` if it should see only displayed records.
- **Add a renderer (e.g. TUI):** implement `render.Renderer` as a second type; do not touch the pipeline. `main.go` chooses which `Renderer` to construct.
- **Support a new log format:** add a decoder + cheap sniff in `internal/parse/` (mirror `logfmt.go`), add a `Format` constant + `FormatFromString` case, and wire it into `LineAs`'s dispatch (and the auto-sniff chain). The canonicalizer maps decoded pairs → `Record` format-agnostically, so downstream stages light up for free; extend the time/level/msg alias tables in `parse.go` for new key names.
- **Add stack-trace support for a new language:** implement the `traceGrammar` interface (`lang`/`detect`/`parse`) in a new `internal/enrich/trace_<lang>.go`, append it to `grammars` in `trace.go`, and provide a `classify*` mapping frames to `FrameKind`. Add a `testdata/<lang>-stack.log` and a table test beside `trace_node_test.go`. See `docs/design/multi-language-stack-traces.md`.

## Gotchas

- **`lines` channel is unbuffered** — the reader parks mid-send under backpressure; the `done` channel (closed via `defer` in `run`) is the only way to unstick it on early exit.
- **Two distinct `Folder` drains:** `FlushIdle(now)` (timer, policy-based, reveals paused/aged runs) vs `Flush()` (EOF + before a passthrough/`--no-fold` line, unconditional). Only `Flush()` guarantees everything is emitted, and it runs first before a passthrough line so nothing jumps ahead of held runs.
- **`--field` matches the KEY exactly but the VALUE as a case-insensitive substring**; the split is on the *first* `=` (so `a=b=c` → key `a`, substr `b=c`), and `key=` is a presence check.
- **JSON must be exactly one object per line** — trailing content after the closing brace falls to passthrough. Sniff success ≠ parse success (a sniffed-logfmt line with an unterminated quote still passes through).
- **`FrameKind` zero value is `FrameStdlib`, not "unknown"** — `FrameProject`/`FrameThirdParty` are only meaningful after `enrich.Stack` runs. `Record.Stack` and `Record.Related` are pointers — nil-check them.
- **The renderer is presentational only** — it never computes `Repeat`/`Corr`/`Related`/`Demoted`/`Effective`; upstream stages set those. Color is set from `isTerminal`, not detected in the renderer.

## Conventions

- Pure Go stdlib except `lipgloss` (styling). Keep the core dependency-light; color is gated on TTY detection (`os.ModeCharDevice`) so piped output stays clean text.
- The `go-styleguide` and `go-test-author` skills are the source of truth for Go style and test scaffolding in this repo.
- **Design docs (`docs/design/`):** multi-format parsing (logfmt) and Go+Node stack traces are **implemented**; continuation-line gluing (reassembling traces split across *physical* lines) and further stack languages (Python/Rust/Java) are **proposed, not built**. Don't assume a proposed doc reflects shipped code.

### Keeping the docs in sync

Only two docs describe this project — this file (agent-facing, authoritative) and `README.md` (user-facing). Keep them from drifting: when a change touches a **CLI flag**, a **pipeline stage/order/tuning constant**, a **new file/package/format/grammar**, or an **invariant**, update this file in the *same* change, and update `README.md` too if the change is user-visible (a flag, or a "What it does" behavior). This file's claims cite real symbols/files, so a `codex exec` (or subagent) fact-check against the source catches drift cheaply. Keep `README.md`'s "Known trade-off" a *simplified* summary that points here rather than duplicating the full folding policy.
