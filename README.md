# plog

A zero-config pretty-printer for structured JSON logs. Pipe a noisy log stream
in, get a readable one out:

```sh
docker logs -f storefront | plog
```

This is a spike. It implements the four highest-leverage ideas from
[`IDEA.md`](./IDEA.md); the rest are phase-2.

## What it does

- **Semantic severity re-ranking** — an `INFO` line whose message says
  `panic` / `nil pointer` / `connection refused` is shown as `INFO→ERR` (or
  `→WARN`). Declared severity is never lowered.
- **Stack-trace collapse** — a Go panic serialized into a `msg` field is
  parsed; project frames (your module) are surfaced with `►` and `file:line`,
  while stdlib/third-party frames fold to `… N framework frames (pkgs…)`.
- **Consecutive-duplicate folding** — runs of near-identical lines (variable
  tokens masked: IPs, ports, hex, UUIDs, numbers) collapse to `… ×N`.
- **Robust passthrough** — non-JSON or malformed lines are emitted verbatim; a
  bad line never interrupts the stream.

Color is applied only when stdout is a terminal.

## Usage

```sh
plog [flags]            # reads stdin, writes stdout

--module string         import-path prefix treated as project code
                        (default "github.com/example")
--no-fold               do not collapse consecutive near-identical lines
--expand-stack          show every stack frame instead of folding
--no-color              disable ANSI color even on a terminal
```

Try it against the bundled sample:

```sh
go run ./cmd/plog < testdata/sample.log
```

## Architecture

A streaming pipeline, one record at a time, with bounded memory:

```
stdin ──▶ parse ──▶ enrich ──▶ fold ──▶ render ──▶ stdout
         (line→     (severity   (collapse  (lipgloss
          Record)    + stack)    repeats)   line/block)
```

| Package            | Responsibility                                          |
|--------------------|---------------------------------------------------------|
| `internal/record`  | canonical `Record` type shared by every stage           |
| `internal/parse`   | line → `Record` (ordered JSON walk, passthrough)        |
| `internal/enrich`  | pure severity re-rank, stack-trace parse, fold/template |
| `internal/render`  | `Renderer` interface + streaming `Plain` (lipgloss)     |
| `cmd/plog`         | flags, TTY detection, pipeline wiring                   |

`Renderer` is a small interface so a future interactive TUI (for the
`plog <file>` case) drops in without changing the pipeline.

### Known trade-off

Folding holds the current run's first record until the run ends, so a live tail
shows folded lines with one line of latency. Use `--no-fold` for zero-latency
raw streaming.

## Develop

```sh
go test ./...                                              # unit tests
go test -run=^$ -fuzz=FuzzParseLine -fuzztime=30s ./internal/parse
```
