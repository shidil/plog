# plog — Log Viewer / Formatter Spike: Ideas

A token-friendly, human-readable formatter for structured logs.

```sh
docker logs -f storefront | plog
```

The sample log that motivates this contains the three things that make
structured logs painful:

- **Embedded multi-line stack traces** (a Go panic serialized inside a single
  JSON `msg` field, ~95% framework noise).
- **Repetitive noise** (the OTel `failed to upload metrics ... connection
  refused` spam repeated every minute).
- **Mislabeled severity** (a `runtime error... nil pointer` panic logged at
  `level: INFO`).

Ideas below are biased toward those real pain points rather than generic
table-stakes features.

---

## High-leverage ideas (build first)

### 1. Semantic severity re-ranking — trust content over the `level` field
The sample logs a nil-pointer panic *and* OTel export failures at `INFO`.
Don't believe the level field blindly: scan `msg` for signals (`panic`,
`nil pointer`, `error`, `failed to`, `connection refused`, `timeout`) and
compute an effective severity that overrides the declared one, marked like
`INFO→ERR`. Turns a wall of uniform text into something where the eye lands on
the real problem.

### 2. Log templating / clustering (Drain algorithm) to fold noise
Mask variable tokens (IPs, ports, durations, hex pointers, UUIDs, timestamps)
to cluster lines into templates and collapse repeats:

```
INFO  failed to upload metrics ... dial tcp <ADDR>: connection refused   ×3 (last 04:31:03)  ▸
```

Expand to see instances. A `--top` mode becomes a live "what patterns dominate
this stream" dashboard. This is the feature that scales to high-volume
`docker logs -f`.

### 3. Stack-trace intelligence (killer feature for the sample)
The panic is mostly `net/http`, `otel`, `connectrpc`, `golang.org/x/net`
noise. The actual bug is one frame:
`storefront/internal/rpc/location/v1beta1/location_rpc.go:72`. plog should:
- **Reassemble** the embedded `\n` trace into a proper rendered block.
- **Collapse stdlib/vendor/runtime frames by default**, surfacing only "your
  code" (configurable via a module-prefix like `github.com/example`).
- **Strip pointers/addresses** (`0x4ca8107f2c0`) so traces normalize.
- **Dedup identical traces** — show `panic ×2` with only the diff (goroutine
  id, IP).
- **Clickable frames** → open `location_rpc.go:72` in `$EDITOR`.

### 4. Adaptive columns — hide what doesn't vary
Detect fields constant across the visible window (`component`, `service`) and
demote them to a header/dim text; promote fields that actually distinguish
lines (`rpc.method`, `error`, `rpc.duration`). The log reads itself.

### 5. Trace / request correlation
RPC logs carry `rpc.service`, `rpc.method`, `rpc.duration`, `rpc.status`.
Group by trace/request id (or synthesize correlation by IP + time proximity)
to reconstruct request timelines — and **causally link** the panic at
`04:29:01` to the `invalid_argument: slug does not match regex` at `04:29:02`.

---

## AI-native ideas (differentiated layer)

### 6. Incident summarizer
On demand (or on an error burst), feed the recent window to a model:
*"In the last 90s, `ResolveLocationSlug` panicked twice (nil pointer at
location_rpc.go:72), correlated with an invalid-argument slug-regex rejection.
OTel collector unreachable on :4317 throughout."* One paragraph beats 200
lines.

### 7. Root-cause linking
Connect the nil-pointer panic to the validation error and the missing
collector, surfacing a hypothesis chain rather than isolated events.

### 8. Natural-language filters
`plog where errors in location service last 5m` compiled to the underlying
predicate — lower barrier than learning a query DSL.

### 9. First-seen / anomaly flagging
Mark a template the first time it ever appears ("🆕 new error signature") and
flag rate spikes.

---

## Streaming & ops realities

### 10. Multi-stream merge
`plog -f storefront payments gateway` interleaving multiple `docker logs` by
timestamp, color-coded per source. Probably the most-used feature in practice.

### 11. Diff mode
Compare two windows or two deploys: "errors that appeared *after* the 04:29
deploy that weren't there before."

### 12. Pattern-triggered actions
`--on 'level=ERROR' --exec notify-send` / desktop notification / webhook.
A lightweight local alerting tool.

### 13. Live histogram + scrubbing
A sparkline of log volume by level across time at the top; jump to spikes.
Pause the tail without losing the buffer, scroll back, resume.

### 14. Replayable ring buffer
Keep the last N MB on disk to answer "what happened in the 30s *before* I
started watching," and share/replay an incident session.

---

## Parsing robustness (unglamorous foundation)

### 15. Format auto-detection & normalization
slog / zap / zerolog / logrus / Python-logging / plain-text all normalized to
one internal shape, so the same renderer works everywhere. Pass through
non-JSON lines gracefully — a malformed line should never crash the tail.

**logfmt / `key=value` parsing.** Structured-but-not-JSON: space-separated
`ts=... level=info msg="..." rpc.method=...` pairs (Go kit, Heroku, logrus
text mode, many others). Today these fall through as `Parsed: false` and are
emitted verbatim — sacred passthrough holds, but none of the enrichment
(severity, columns, folding, correlation) applies. logfmt is structurally
close to what those stages already expect (ordered key/value pairs), so
parsing it into the same `Record` lights up the whole pipeline for a large
class of streams the spike currently passes through untouched. Detect by
sniffing the line shape (bare `k=v` runs, no leading `{`) and preserve key
order like the JSON token walk does.

### 16. Continuation-line gluing
Heuristics to reattach stack traces that arrive as separate physical lines
(not always wrapped in one JSON record like the sample).

### 17. Schema-drift detection
Warn when a field changes type or disappears — often signals a logging
regression.

---

## Baseline features (from the original brief)

1. **Filtering and search** — by level, time range, component; quick find.
2. **Color coding** — errors red, warnings yellow, info green.
3. **Log aggregation** — unified view across multiple sources/containers.
4. **Export and save** — JSON, CSV, plain text for offline analysis/sharing.
5. **Real-time updates** — live streaming without manual refresh.
6. **Log highlighting** — highlight keywords/phrases.
7. **Log grouping** — by component, service, or request id.
8. **Customizable layout** — column widths, ordering, field visibility.
9. **Error stack-trace visualization** — readable, collapsible frames.
10. **DuckDB integration** — SQL queries over log data (aggregations, joins,
    complex filters).

---

## Suggested spike scope

Start with the four that directly attack everything painful in the sample and
deliver an immediate "wow":

- #1 Severity re-ranking
- #3 Stack-trace collapse
- #2 Template folding
- #15 Robust parse / passthrough

DuckDB / AI / correlation features are phase-2 layers once core rendering is
solid.

**Stack suggestion:** Go or Rust — fast streaming CLI, fits the Go-heavy
ecosystem here. Pipeline shape: `parser → normalizer → enricher → renderer
(TUI / plain)`.
