package render

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/shidil/plog/internal/record"
	"github.com/shidil/plog/internal/summary"
)

// Plain is a streaming, line-oriented Renderer. It prints one compact line per
// record — timestamp, level badge, message, fields — and expands embedded stack
// traces into an indented block with framework frames folded away. Color is
// applied only when enabled (typically a TTY); otherwise output is clean text.
type Plain struct {
	w           io.Writer
	color       bool
	expandStack bool
	link        FrameLinker

	dim     lipgloss.Style
	key     lipgloss.Style
	project lipgloss.Style
	rerank  lipgloss.Style
	levels  map[record.Level]lipgloss.Style
}

// FrameLinker turns a stack frame into a terminal hyperlink (OSC 8) target URI,
// or "" when the frame has no resolvable local file. The renderer takes it as a
// dependency (see internal/link) so it stays presentational — it never touches
// the filesystem or knows about editors.
type FrameLinker interface {
	FrameURI(f record.Frame) string
}

// PlainConfig configures a Plain renderer.
type PlainConfig struct {
	Color       bool        // apply ANSI styling
	ExpandStack bool        // show every frame instead of folding framework frames
	Link        FrameLinker // when non-nil, make resolvable project frames clickable
}

// NewPlain returns a Plain renderer writing to w.
func NewPlain(w io.Writer, cfg PlainConfig) *Plain {
	return &Plain{
		w:           w,
		color:       cfg.Color,
		expandStack: cfg.ExpandStack,
		link:        cfg.Link,
		dim:         lipgloss.NewStyle().Faint(true),
		key:         lipgloss.NewStyle().Faint(true),
		project:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14")),
		rerank:      lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
		levels: map[record.Level]lipgloss.Style{
			record.LevelError: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")),
			record.LevelWarn:  lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
			record.LevelInfo:  lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
			record.LevelDebug: lipgloss.NewStyle().Faint(true),
		},
	}
}

// Render writes one record. Passthrough records are emitted verbatim.
func (p *Plain) Render(rec record.Record) error {
	if !rec.Parsed {
		_, err := fmt.Fprintln(p.w, rec.Raw)
		return err
	}

	var b strings.Builder
	b.WriteString(p.paint(p.dim, timeOf(rec)))
	b.WriteByte(' ')
	b.WriteString(p.badge(rec))
	b.WriteString("  ")
	if rec.Corr != "" {
		b.WriteString(p.paint(p.dim, "⟨"+rec.Corr+"⟩"))
		b.WriteByte(' ')
	}
	b.WriteString(p.message(rec))
	if rec.Repeat > 1 {
		b.WriteString(p.paint(p.dim, fmt.Sprintf("  ×%d", rec.Repeat)))
	}
	if fields := p.fields(rec); fields != "" {
		b.WriteString("  ")
		b.WriteString(fields)
	}
	b.WriteByte('\n')

	if rec.Related != nil {
		fmt.Fprintf(&b, "    %s\n", p.paint(p.dim, relatedNote(rec.Related)))
	}
	if rec.Stack != nil {
		p.writeStack(&b, rec.Stack)
	}

	_, err := io.WriteString(p.w, b.String())
	return err
}

// RenderSummary writes the --summary triage footer: a rule, per-severity
// counts with unique-template counts, the time span, and one whole
// representative line per distinct warn/error template. It is deliberately not
// part of the Renderer interface — that seam is the per-record contract a
// future TUI implements; the footer is specific to this EOF-bounded, streaming
// renderer.
func (p *Plain) RenderSummary(rep summary.Report) error {
	var b strings.Builder
	b.WriteString(p.paint(p.dim, "── summary "+strings.Repeat("─", 45)))
	b.WriteByte('\n')
	b.WriteString(p.summaryCounts(rep))
	b.WriteByte('\n')
	if !rep.First.IsZero() {
		span := rep.First.Format("15:04:05")
		if !rep.Last.Equal(rep.First) {
			span += "–" + rep.Last.Format("15:04:05")
		}
		b.WriteString(p.paint(p.dim, "span "+span))
		b.WriteByte('\n')
	}
	if len(rep.TopErrors)+len(rep.TopWarns) > 0 {
		b.WriteByte('\n')
		p.writeSummaryLines(&b, record.LevelError, rep.TopErrors, rep.MoreErrors)
		p.writeSummaryLines(&b, record.LevelWarn, rep.TopWarns, rep.MoreWarns)
	}
	if rep.Untracked > 0 {
		fmt.Fprintf(&b, "  %s\n", p.paint(p.dim,
			fmt.Sprintf("… +%d more warn/error lines beyond the tracked templates", rep.Untracked)))
	}
	_, err := io.WriteString(p.w, b.String())
	return err
}

// summaryCounts renders the footer's verdict line. Errors and warns always
// appear — "0 errors" is the answer triage came for — while info, unknown, and
// passthrough tallies appear only when nonzero.
func (p *Plain) summaryCounts(rep summary.Report) string {
	errs := countWithUnique(rep.Errors, "error", rep.UniqueErrors)
	if rep.Errors > 0 {
		errs = p.paint(p.levels[record.LevelError], errs)
	}
	warns := countWithUnique(rep.Warns, "warn", rep.UniqueWarns)
	if rep.Warns > 0 {
		warns = p.paint(p.levels[record.LevelWarn], warns)
	}
	segs := []string{errs, warns}
	if rep.Infos > 0 {
		segs = append(segs, fmt.Sprintf("%d info", rep.Infos))
	}
	if rep.Unknown > 0 {
		segs = append(segs, fmt.Sprintf("%d unknown", rep.Unknown))
	}
	if rep.Passthrough > 0 {
		segs = append(segs, fmt.Sprintf("%d passthrough", rep.Passthrough))
	}
	return strings.Join(segs, " · ")
}

// countWithUnique formats a level tally like "4 errors (2 unique)"; the
// unique-template count is omitted when the tally is zero.
func countWithUnique(n int, noun string, unique int) string {
	s := fmt.Sprintf("%d %s", n, noun)
	if n != 1 {
		s += "s"
	}
	if n > 0 {
		s += fmt.Sprintf(" (%d unique)", unique)
	}
	return s
}

// writeSummaryLines appends one badge-prefixed line per representative
// template, then an explicit "+N more" overflow line when the level had more
// distinct templates than were shown.
func (p *Plain) writeSummaryLines(b *strings.Builder, lvl record.Level, lines []summary.Line, more int) {
	if len(lines) == 0 {
		return
	}
	st := p.levels[lvl]
	for _, ln := range lines {
		fmt.Fprintf(b, "  %s ×%d  %s\n", p.paint(st, fmt.Sprintf("%-4s", lvl.String())), ln.Count, flatten(ln.Message))
	}
	if more > 0 {
		fmt.Fprintf(b, "  %s\n", p.paint(p.dim, fmt.Sprintf("… +%d more", more)))
	}
}

// badge renders the level token, marking a semantic re-rank as "INFO→ERR".
func (p *Plain) badge(rec record.Record) string {
	st := p.levels[rec.Effective]
	if rec.Effective == rec.Level {
		return p.paint(st, rec.Level.String())
	}
	return p.paint(st, rec.Level.String()) + p.paint(p.rerank, "→"+rec.Effective.String())
}

// message returns the primary text: a stack trace's header when present,
// otherwise the message field with internal newlines flattened.
func (p *Plain) message(rec record.Record) string {
	msg := rec.Message
	if rec.Stack != nil {
		msg = rec.Stack.Header
	}
	return flatten(msg)
}

// fields renders the structured fields as key=val pairs, leading with the
// fields that distinguish this line and trailing the ones the Columns stage
// judged constant — dimmed and prefixed "·" so they recede. Within each group
// source order is preserved.
func (p *Plain) fields(rec record.Record) string {
	var salient, demoted []string
	for _, kv := range rec.Fields {
		val := flatten(kv.Val)
		if strings.ContainsAny(val, " \t\"") {
			val = strconv.Quote(val)
		}
		if kv.Demoted {
			demoted = append(demoted, p.paint(p.dim, "·"+kv.Key+"="+val))
			continue
		}
		salient = append(salient, p.paint(p.key, kv.Key+"=")+val)
	}
	return strings.Join(append(salient, demoted...), " ")
}

// relatedNote formats a backward causal hint as an indented, dimmed line, e.g.
// "↳ likely related: http: panic serving … @04:29:02".
func relatedNote(r *record.Related) string {
	if r.Time.IsZero() {
		return "↳ likely related: " + r.Summary
	}
	return fmt.Sprintf("↳ likely related: %s @%s", r.Summary, r.Time.Format("15:04:05"))
}

// writeStack appends an indented frame block, surfacing project frames and
// folding consecutive framework frames into a single summary line.
func (p *Plain) writeStack(b *strings.Builder, st *record.StackTrace) {
	var framework []record.Frame
	flush := func() {
		if len(framework) == 0 {
			return
		}
		fmt.Fprintf(b, "    %s\n", p.paint(p.dim, foldSummary(framework)))
		framework = framework[:0]
	}

	for _, f := range st.Frames {
		if f.Kind == record.FrameProject || p.expandStack {
			flush()
			marker := "  "
			if f.Kind == record.FrameProject {
				marker = p.paint(p.project, "►")
			}
			fmt.Fprintf(b, "    %s %s  %s\n", marker, p.linkedLocation(f), p.paint(p.dim, f.Func))
			continue
		}
		framework = append(framework, f)
	}
	flush()
}

// foldSummary describes a run of folded framework frames with a few example
// packages so the collapse stays informative.
func foldSummary(frames []record.Frame) string {
	seen := make(map[string]bool)
	var pkgs []string
	for _, f := range frames {
		pkg := topPackage(f.Func)
		if pkg != "" && !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
	}
	if len(pkgs) > 3 {
		pkgs = append(pkgs[:3], "…")
	}
	return fmt.Sprintf("… %d framework frames (%s)", len(frames), strings.Join(pkgs, ", "))
}

// linkedLocation renders a frame's location, wrapping it in an OSC 8 terminal
// hyperlink when a linker is configured and resolves the frame to a local file.
// The style is applied inside the link text so color and clickability compose.
func (p *Plain) linkedLocation(f record.Frame) string {
	loc := p.paint(p.project, location(f))
	if p.link == nil {
		return loc
	}
	if uri := p.link.FrameURI(f); uri != "" {
		return hyperlink(uri, loc)
	}
	return loc
}

// hyperlink wraps text in an OSC 8 terminal hyperlink pointing at uri. Terminals
// that support OSC 8 render the text as clickable; others ignore the escape and
// show the text unchanged. The caller gates emission on stdout being a terminal.
func hyperlink(uri, text string) string {
	return "\x1b]8;;" + uri + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// location formats a frame as file:line using only the file's base name.
func location(f record.Frame) string {
	file := f.File
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		file = file[i+1:]
	}
	if f.Line == 0 {
		return file
	}
	if f.Col > 0 {
		return fmt.Sprintf("%s:%d:%d", file, f.Line, f.Col)
	}
	return fmt.Sprintf("%s:%d", file, f.Line)
}

// topPackage returns a short package label for a frame function, e.g.
// "net/http" or "go.opentelemetry.io/otel".
func topPackage(fn string) string {
	if i := strings.LastIndexByte(fn, '/'); i >= 0 {
		host := fn[:i]
		if before, _, found := strings.Cut(host, "/"); found {
			host = before
		}
		return host
	}
	if before, _, found := strings.Cut(fn, "."); found {
		return before
	}
	return fn
}

// paint applies a style only when color is enabled.
func (p *Plain) paint(st lipgloss.Style, s string) string {
	if !p.color {
		return s
	}
	return st.Render(s)
}

// timeOf formats a record's timestamp as HH:MM:SS, or a placeholder when unset.
func timeOf(rec record.Record) string {
	if rec.Time.IsZero() {
		return "--:--:--"
	}
	return rec.Time.Format("15:04:05")
}

// flatten collapses internal whitespace runs containing newlines to a single
// space so multi-line field values stay on one line.
func flatten(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	return strings.Join(strings.Fields(s), " ")
}
