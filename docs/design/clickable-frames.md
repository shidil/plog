# Clickable stack frames (IDEA.md #3, "$EDITOR" bullet)

Status: implemented · Scope: render + a small `internal/link` package · Owner: spike

## The constraint that reshapes the idea

IDEA #3 lists "clickable frames → open `location_rpc.go:72` in `$EDITOR`." Taken
literally that cannot work here: plog is **stream-only** — stdin carries the log
pipe (`docker logs -f | plog`), so there is no keyboard channel and plog can
never receive a click or a keypress to spawn an editor. A pipe tool has exactly
one lever: **OSC 8 hyperlinks** — escape sequences (`ESC ] 8 ; ; URI ESC \ text
ESC ] 8 ; ; ESC \`) that the *terminal* renders as clickable and the OS opens.
plog emits the link; the terminal + OS do the opening. Terminals without OSC 8
support ignore the escape and show the text unchanged.

## The second constraint: paths usually don't resolve locally

The paths in real traces are rarely openable on the machine running plog:

- Go project frame: `github.com/example/storefront/internal/rpc/.../location_rpc.go`
  — a **module import path**, not a filesystem path.
- Go dependency: `go.opentelemetry.io/otel/sdk@v1.43.0/trace/span.go` — module cache.
- Node: `.next/server/chunks/ssr/…js` — **minified build output**, not source.
- Container tail: `/app/…` — a path inside a container, absent on your laptop.

So a link is only useful in the **local-dev** case (tailing a process whose
source is checked out on the same machine). A naive `file://` around the raw path
would be a dead link from any remote/container tail — worse than no link.

## Design

Opt-in, TTY-gated, resolution-checked. Two flags:

- `--link SCHEME` — an editor preset (`vscode`, `cursor`, `zed`, `idea`, `file`) or a
  URI template containing `{path}` (plus optional `{line}`/`{col}`). Empty = off.
- `--src DIR` — the local source root paths resolve against (default: CWD).

### Resolution: longest-suffix existence match (`internal/link`)

`Linker.FrameURI(frame)` resolves `frame.File` to a local file by trying
successively shorter path suffixes joined onto `--src`, **longest first**, and
returning the first that names an existing regular file. For
`github.com/example/storefront/internal/rpc/.../location_rpc.go` under
`--src ~/storefront`, the leading module segments don't exist locally and are
skipped; `internal/rpc/.../location_rpc.go` matches. This:

- needs **no `--module`** (it is not a prefix-strip) and is **language-agnostic**;
- yields **no link** when nothing resolves (remote/container/minified paths), so
  there are never dead links — the promise that makes opt-in worthwhile;
- is a heuristic: a coincidental short suffix could match the wrong file, which
  longest-first minimizes. Acceptable for a local-dev convenience.

`--module` is unchanged — it still governs only project/3p/stdlib *classification*.

### URI formatting

Presets: `vscode`/`cursor`/`zed` → `scheme://file<abs>:line[:col]`; `idea` →
`idea://open?file=<abs>&line=N`; `file` → `file://<abs>`. `:line`/`:col` are
omitted when the trace didn't carry them (Go frames have no column). A template
substitutes `{path}`/`{line}`/`{col}` literally (a zero line/col → empty string).

### Renderer stays presentational

`render.Plain` holds a `FrameLinker` interface (nil = off) and only *wraps* a
frame's location in the OSC 8 escape when the injected linker returns a URI. The
filesystem lookup and URI formatting live in `internal/link`, not the renderer —
consistent with "the renderer never computes, upstream supplies." The style is
applied inside the link text so color and clickability compose.

### Gating

`main.frameLinker` validates the scheme **even when piped** (a bad `--link` fails
fast, exit 1) but returns a nil linker when stdout is not a TTY — OSC 8 escapes
would corrupt redirected/piped output — with a one-line note on stderr. This is
the same `os.ModeCharDevice` signal that gates color, applied independently
(`--no-color` on a TTY still links).

## Invariants & risks

- **Piped output stays byte-clean** — links are TTY-gated; a non-terminal stdout
  gets exactly today's output.
- **No dead links** — a frame that doesn't resolve locally is rendered plain.
- **Heuristic resolution** — longest-suffix match can mis-resolve in a repo with
  duplicate filenames; documented, not solved. `--src` scopes the search.
- **Presentational renderer preserved** — link logic is an injected dependency.

## Files

- `internal/link/link.go` (+ `link_test.go`) — `Linker`, resolution, formatting.
- `internal/render/plain.go` — `FrameLinker` interface, `linkedLocation`,
  `hyperlink` (OSC 8 wrap).
- `cmd/plog/main.go` — `--link`/`--src` flags, `frameLinker` (validate + TTY gate).
