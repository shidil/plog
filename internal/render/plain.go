package render

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/shidil/plog/internal/record"
)

// Plain is a streaming, line-oriented Renderer. It prints one compact line per
// record — timestamp, level badge, message, fields — and expands embedded stack
// traces into an indented block with framework frames folded away. Color is
// applied only when enabled (typically a TTY); otherwise output is clean text.
type Plain struct {
	w           io.Writer
	color       bool
	expandStack bool

	dim     lipgloss.Style
	key     lipgloss.Style
	project lipgloss.Style
	rerank  lipgloss.Style
	levels  map[record.Level]lipgloss.Style
}

// PlainConfig configures a Plain renderer.
type PlainConfig struct {
	Color       bool // apply ANSI styling
	ExpandStack bool // show every frame instead of folding framework frames
}

// NewPlain returns a Plain renderer writing to w.
func NewPlain(w io.Writer, cfg PlainConfig) *Plain {
	return &Plain{
		w:           w,
		color:       cfg.Color,
		expandStack: cfg.ExpandStack,
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
			fmt.Fprintf(b, "    %s %s  %s\n", marker, p.paint(p.project, location(f)), p.paint(p.dim, f.Func))
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

// location formats a frame as file:line using only the file's base name.
func location(f record.Frame) string {
	file := f.File
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		file = file[i+1:]
	}
	if f.Line == 0 {
		return file
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
