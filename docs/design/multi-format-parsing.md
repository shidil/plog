# Multi-format parsing (IDEA.md #15)

Status: implemented · Scope: parse layer only · Owner: spike

## Problem

`parse.Line` recognizes one wire format: a JSON object. logfmt
(`ts=… level=info msg="…"` — Go kit, Heroku, logrus text mode) and other
non-JSON structured streams fall through as `Parsed: false` and are emitted
verbatim. Passthrough is correct, but it means none of the enrichment
(severity, stack, columns, folding, correlation) ever runs on those streams.

## Insight: syntax and semantics are separate concerns

`decodeObject`/`assign` tangle two jobs that multi-format makes worth splitting:

1. **Syntax** — decode the line into ordered key/value pairs. Format-specific.
2. **Semantics** — pull `time`/`level`/`msg` out, keep the rest as ordered
   `Fields`. Format-*independent*: zap names time `ts`, slog names it `time`,
   logfmt might use `timestamp` — the same aliasing problem regardless of wire
   format.

Separating them is the whole design:

```
                  ┌──── syntax (per-format) ────┐   ┌ semantics (shared) ┐
raw ─▶ detect ─▶ JSON walk | logfmt scan | … ────▶ []pair (ordered) ─▶ canon ─▶ Record
          │                                                                       │
          └────────────── no format matches ───────────────────────▶ Record{Raw: raw}
```

`pair` is `struct{ Key, Val string }` where `Val` is the **display string** —
the format parser owns flattening (JSON keeps today's `render()` to unquote
strings / compact composites; logfmt unquotes its own quoted values), so
`canon` never knows which format produced the pairs.

## Detection seam

A sequential chain, not a registry — least mechanism for two formats, and it
reads in priority order:

```go
func Line(raw string) record.Record {
	trimmed := strings.TrimSpace(raw)
	switch {
	case sniffJSON(trimmed):
		if pairs, ok := parseJSON(raw); ok {
			return finish(raw, pairs)
		}
	case sniffLogfmt(trimmed):
		if pairs, ok := parseLogfmt(raw); ok {
			return finish(raw, pairs)
		}
	}
	return record.Record{Raw: raw} // sacred passthrough
}
```

JSON sniffs first (unambiguous leading `{`, cheapest). A format that *sniffs*
but fails to *parse* falls through to passthrough — never error, never
half-parse.

Reach for a `Format` interface + ordered slice only when formats grow past ~3
or become user-pluggable; the `switch` refactors into it trivially. Not now.

## logfmt parser

Grammar (go-logfmt): space-separated `key=value`; values may be double-quoted
to hold spaces and escaped quotes; a bare `key` is a valueless flag.

**Sniff** (avoiding prose false-positives): the first whitespace-delimited
token must be `key=` with an identifier-ish key. Real logfmt always leads with
a pair (`ts=`, `level=`); prose almost never starts with `word=`. Full-parse
validation backs it up — a prose line past the sniff still fails parse →
passthrough.

**Parse edge cases the implementation must handle:**

- Split each pair on the **first** `=` only — `url=http://x?a=b` survives.
- Quoted values with spaces: `msg="connection refused: dial tcp"`; handle `\"`.
- Bare key → `pair{Key, Val: ""}` (logfmt convention; rendered dim).
- **Embedded `\n` in a quoted `msg`** flows into `Record.Message`, so the
  existing `Stack` enrichment lights up for logfmt panics too — free synergy.
- Order preserved by appending as scanned, mirroring the JSON token walk.

## Shared canonicalization + wider time parsing

`canon` replaces the hardcoded `assign` switch with small alias tables, so
adding zap/zerolog conventions is a table edit, not new code:

```go
var timeKeys  = set("time", "ts", "timestamp", "@timestamp", "t")
var levelKeys = set("level", "lvl", "severity", "levelname")
var msgKeys   = set("msg", "message")
```

First match wins; unmatched keys stay in `Fields` in order. logfmt forces a
contained improvement: time is often epoch or zone-less, so the time path tries
an ordered layout list (`RFC3339Nano`, `RFC3339`, `2006-01-02T15:04:05`, unix
secs/millis) instead of today's single `RFC3339Nano`. JSON benefits too.

Caller-facing caution: aliasing changes behavior for a JSON stream that carries
`ts`/`message` as *data*. First-match-wins is conservative, and `--format`
(below) is the escape hatch, but it's a semantics shift worth noting.

## Why downstream is untouched — the payoff

Every stage after `parse` consumes only `record.Record`. Because logfmt lands
in the same canonical shape, severity re-ranking, stack intelligence,
`--field`/`--grep`, adaptive columns, folding, and correlation all work on
logfmt streams **with zero changes**. The enrichment pipeline lights up for a
whole class of streams currently passed through verbatim. That is the entire
argument for doing this at the parse layer.

## Invariants preserved

1. **Sacred passthrough** — any sniff/parse miss → `Record{Raw: raw}`, never
   errors, never half-parses.
2. **Field order** — both parsers emit pairs in source order.
3. **One record at a time** — per-line, no buffering. Stream-level format
   *locking* is a possible later optimization (adds state, mixed-stream risk) —
   deferred.
4. **JSON path stays behavior-compatible** — proven by the existing
   `parse_test.go` + `FuzzParseLine` passing unchanged after the refactor.

## Package placement

Stay inside `internal/parse`: `parse.go` (dispatch + `canon`), `json.go`
(extracted current logic), `logfmt.go`. Not a subpackage — they share
`pair`/`canon`, and a subpackage would force exporting the intermediate.

## CLI

`--format auto|json|logfmt|text` (default `auto`). `text` forces passthrough;
an explicit format skips sniffing — the escape hatch when auto-detect misfires
on a prose-heavy or alias-colliding stream.

## Phased plan & verification

1. **Refactor, no behavior change** — extract `pair`, `canon`, `parseJSON` out
   of `decodeObject`/`assign`; `Line` becomes the dispatch switch, JSON only.
   `parse_test.go` + `FuzzParseLine` stay green unchanged (the proof).
2. **logfmt** — `sniffLogfmt` + `parseLogfmt`; table tests (quotes, escapes,
   `=`-in-value, bare keys, embedded newline→stack) + prose false-positive
   tests + a `FuzzParseLogfmt` (parsing ⇒ fuzz required: never panic, never
   error).
3. **canon aliases + time layouts** — table tests per alias and time format.
4. **`--format` flag** — wire into `main.go`.

Verify: `gofmt`, `go vet ./...`, `go test ./...`, and
`go test -run=^$ -fuzz=FuzzParseLogfmt -fuzztime=30s ./internal/parse`.
