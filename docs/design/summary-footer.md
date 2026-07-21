# Triage summary footer (`--summary`)

Status: implemented · Scope: new `internal/summary` package + render + main · Owner: spike

## Motivation: the first question is "is anything wrong?"

The most common query against a bounded log read — a human after an incident, or
an LLM agent handed `docker logs <id> | plog` — is not "show me the stream", it
is "what's wrong in here?". Today plog answers that losslessly but *serially*:
the reader must scan to the end to learn whether errors exist at all. Tools like
`rtk docker logs` answer it up front (`13 errors (6 unique)` + deduplicated
error lines) but lossily — their dedup truncates messages (`EACCES: permi...`),
destroying the diagnostic payload.

plog already computes everything a triage summary needs, more accurately:

- **Counts by `Effective`**, not by declared level — an `INFO→ERR` re-ranked
  panic counts as an error, which level-string matching misses.
- **"N unique"** is exactly `enrich.Template` — structural masking (hex, uuid,
  ip, numbers), so uniqueness is by event shape and the representative message
  is kept *whole*, never prefix-truncated.

`--summary` appends a short footer after the stream at EOF: counts per severity
with unique-template counts, plus one full representative line per distinct
error/warn template with its `×count`. The verdict costs a glance (or ~30
tokens); the full folded stream remains above it, so nothing is a second
command away.

## The constraint: a footer needs an end

A summary is a bounded-read feature. A follow (`docker logs -f`) never reaches
EOF, so the footer would never print — and periodic mid-stream summaries would
break the "output is the stream" contract. The design accepts this: the footer
emits from the existing EOF path in `run` (beside the final `folder.Flush()`).
Under a follow, `--summary` simply never fires unless the producer closes the
pipe. Trapping SIGINT to print the footer on Ctrl-C is deliberately deferred:
the signal hits the whole foreground process group, so plog cannot count on
outliving its upstream, and signal handling would be the first non-trivial
control flow in `main` — phase-2 if wanted.

## Design

One flag:

- `--summary` — after EOF, print a summary footer to stdout. Off by default;
  when off, output is byte-identical to today.

### Aggregation: `internal/summary`, an observer stage

A new package (not `enrich`): it annotates nothing and emits nothing
per-record, it only *observes*. `Summary.Observe(rec)` is called in the
`process` closure **after `Filter.Match` and before `Folder.Add`**:

- after Filter, so the summary describes what the reader actually saw — a
  `--grep`-narrowed stream gets a summary of the narrowed stream (consistent
  with how Columns/Correlator windows already work);
- before Folder, so counts are exact per-record and immune to fold-split
  artifacts (`maxHold` splitting a storm into `×3`+`×5` must not double-count
  or under-count).

State per record:

- severity tallies by `Effective` (error / warn / info+debug / unknown), plus a
  passthrough count for `!Parsed` lines (their severity is unknowable —
  sacred-passthrough's summary analogue: counted, never guessed);
- for `Effective >= Warn`: a bounded map keyed by `enrich.Template(rec)` (the
  existing exported function — masking logic is written once) holding the
  first-seen `Message`/`Stack.Header` as the representative and a count;
- first/last `Time` seen, for a span line.

Bounded memory, no silent caps: the template map holds at most `maxTemplates`
(64) distinct warn/error templates; beyond that, records with new templates
fold into an overflow counter the footer reports explicitly (`… +12 more
warn/error lines beyond the tracked templates`) — never dropped without saying
so. (The counter is per record, not per distinct template: distinctness beyond
the cap is unknowable in bounded memory.)

### Rendering: the footer, and the seam

`summary.Report` (a plain struct) is formatted by a new
`render.Plain.RenderSummary(summary.Report)` method. Deliberately **not** added
to the `render.Renderer` interface: that seam is `Render(record.Record) error`,
the per-record contract a future TUI implements; a TUI would present live
aggregates its own way, not as an EOF footer. `run` already constructs the
concrete `*render.Plain`, so main wires it directly. The aggregation lives in
`internal/summary`, the formatting in the renderer — "the renderer never
computes, upstream supplies" holds.

Footer shape (color gated exactly like the stream; plain text when piped):

```
── summary ─────────────────────────────────────────────
4 errors (2 unique) · 1 warn (1 unique) · 37 info · 3 passthrough
span 14:29:01–14:32:47

  ERR  ×3  Failed to write image to cache <hex> Error: EACCES: permission denied, mkdir '/app/booking-app/.next/cache/images'
  ERR  ×1  unhandledRejection: Error: EACCES: permission denied, mkdir '/app/booking-app/.next/cache/images'
  WARN ×1  image optimizer falling back to unoptimized response
```

Representative lines are the masked-template's first-seen message, printed in
full — one line each, most-frequent first, errors before warns, capped at
`maxShown` (8) per level with an explicit `+N more` overflow line. Masked
tokens (`<hex>`, `<n>`) may appear where the masker fired; that is the honest
signature of "these lines differed only there".

### Wiring

`process` gains one line (`sum.Observe(rec)` between `Filter.Match` and
`cor.Mark`); the EOF branch in `run` gains one call after the final
`emit(folder.Flush())`:

```go
if opts.summary {
    if err := renderer.RenderSummary(sum.Report()); err != nil { ... }
}
```

`--no-fold`, `--min-level`, `--field`, `--grep` all compose for free: the
summary describes the post-filter stream whatever shape it has.

## Non-goals (this iteration)

- **No `--summary-only`** (suppress the stream, print just the footer). It is
  the obvious agent-facing sibling and trivially layered on later (skip `emit`,
  keep `Observe`), but it changes plog's contract from "pretty-print the
  stream" to "replace the stream" — a bigger positioning step than one flag.
- **No periodic summaries under `-f`**, no SIGINT trap (see the constraint).
- **No JSON/machine format** for the footer; if agents want structured output,
  that is a separate design (it would want the whole stream, not just the footer).
- **No corr-aware rollup** ("12 of 13 errors share one request") — the data
  exists (`Record.Corr`), deferred to keep the footer one screen tall.

## Invariants & risks

- **Stream untouched.** The footer is appended after the final flush; every
  stream byte is identical with and without `--summary`. Sacred passthrough is
  unaffected (passthrough lines are counted, never interpreted).
- **Bounded memory** — fixed-size tallies + a capped template map with an
  explicit overflow count.
- **Template masking is heuristic** — two genuinely different errors that mask
  to the same template merge into one row (first-seen message wins). Same
  trade-off Folder already makes; acceptable for triage.
- **Footer interleaving** — anything downstream parsing plog output line-by-line
  will see non-log lines at the end. Opt-in flag; documented.

## Files

- `internal/summary/summary.go` (+ `summary_test.go`) — `Summary`
  (`Observe`/`Report`), tallies, bounded template map, `maxTemplates`/`maxShown`.
- `internal/render/plain.go` — `RenderSummary(summary.Report)`.
- `cmd/plog/main.go` — `--summary` flag, `options.summary`, `Observe` in
  `process`, footer emit on the EOF branch.
- `CLAUDE.md` / `README.md` — flag + pipeline note, on implementation.
