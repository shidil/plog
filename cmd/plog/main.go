// Command plog formats structured JSON logs into a human-readable stream. It
// reads log lines from stdin and writes formatted output to stdout, e.g.:
//
//	docker logs -f storefront | plog
//
// It re-ranks severity from message content, collapses embedded Go stack traces
// to the frames that matter, and folds runs of near-identical lines.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/shidil/plog/internal/enrich"
	"github.com/shidil/plog/internal/filter"
	"github.com/shidil/plog/internal/parse"
	"github.com/shidil/plog/internal/record"
	"github.com/shidil/plog/internal/render"
)

// Build metadata, stamped at release time via -ldflags (see .goreleaser.yaml):
//
//	-X main.version=v0.1.0 -X main.commit=<sha> -X main.date=<rfc3339>
//
// They stay empty for a plain `go install`, where versionString falls back to
// the module version and VCS stamp the Go toolchain embeds in the binary.
var (
	version = ""
	commit  = ""
	date    = ""
)

// maxLine bounds a single log line; the embedded panic traces in real logs run
// to a few KB, so the default bufio.Scanner limit is raised generously.
const maxLine = 4 << 20 // 4 MiB

// flushTick is how often the main loop asks Folder to reveal runs that have
// paused or aged out (Folder.FlushIdle). It is the resolution of that check, not
// a flush period: a live tail (docker logs -f) narrowed by a filter to one
// near-identical event would otherwise fold into a run that no distinct line
// ever ends, printing nothing until EOF — which a follow never reaches. A finer
// tick tightens how promptly a paused run surfaces; the idle/max-hold policy in
// Folder decides which runs actually flush, so a busy run no longer splits on
// every tick.
const flushTick = 250 * time.Millisecond

func main() {
	module := flag.String("module", "github.com/example", "import-path prefix treated as project code in stack traces")
	noFold := flag.Bool("no-fold", false, "do not collapse consecutive near-identical lines")
	noColumns := flag.Bool("no-columns", false, "do not demote fields that stay constant across the recent window")
	noCorrelate := flag.Bool("no-correlate", false, "do not group records by request or link an event to a recent related one")
	expandStack := flag.Bool("expand-stack", false, "show every stack frame instead of folding framework frames")
	noColor := flag.Bool("no-color", false, "disable ANSI color even on a terminal")
	minLevel := flag.String("min-level", "", "drop parsed records below this effective severity (debug|info|warn|error)")
	grep := flag.String("grep", "", "show only lines matching this regular expression (message/fields, or raw for passthrough)")
	format := flag.String("format", "auto", "input format: auto (sniff), json, logfmt, glog, python, logrus, or text (passthrough)")
	showVersion := flag.Bool("version", false, "print version information and exit")
	var fields fieldFlags
	flag.Var(&fields, "field", "show only records whose named field contains a substring, e.g. -field rpc.method=Resolve (repeatable)")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionString())
		return
	}

	inFormat, ok := parse.FormatFromString(*format)
	if !ok {
		fmt.Fprintf(os.Stderr, "plog: unknown -format %q: want auto, json, logfmt, glog, python, logrus, or text\n", *format)
		os.Exit(1)
	}

	flt, err := filter.New(*minLevel, *grep, fields)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plog:", err)
		os.Exit(1)
	}

	cfg := render.PlainConfig{
		Color:       !*noColor && isTerminal(os.Stdout),
		ExpandStack: *expandStack,
	}
	opts := options{
		format:    inFormat,
		module:    *module,
		fold:      !*noFold,
		columns:   !*noColumns,
		correlate: !*noCorrelate,
	}
	if err := run(os.Stdin, os.Stdout, cfg, flt, opts); err != nil {
		fmt.Fprintln(os.Stderr, "plog:", err)
		os.Exit(1)
	}
}

// versionString reports the build version for the -version flag. It prefers the
// values a release build stamps in via -ldflags and falls back to the module
// version and VCS revision the Go toolchain embeds, so a plain
// `go install github.com/shidil/plog/cmd/plog@latest` still reports a real
// version rather than "dev".
func versionString() string {
	v, c, d := version, commit, date
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" {
			v = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			}
		}
	}
	if v == "" {
		v = "dev"
	}
	out := "plog " + v
	if len(c) > 12 {
		c = c[:12]
	}
	switch {
	case c != "" && d != "":
		out += fmt.Sprintf(" (%s, %s)", c, d)
	case c != "":
		out += fmt.Sprintf(" (%s)", c)
	}
	return out
}

// options holds the parse/enrich toggles for a run, resolved from flags.
type options struct {
	format    parse.Format // how each line is decoded (auto-sniff by default)
	module    string       // import-path prefix treated as project code in stack traces
	fold      bool         // collapse consecutive near-identical lines
	columns   bool         // demote fields constant across the recent window
	correlate bool         // group by request and link related events
}

// run drives the pipeline: each line is parsed, enriched, filtered, folded, and
// rendered. Lines are read on a separate goroutine so the main loop can also
// wake on a timer and reveal a folded run that has paused or aged out (see flushTick);
// all record processing stays on this one goroutine, so the pipeline holds no
// shared state.
func run(in io.Reader, out io.Writer, cfg render.PlainConfig, flt *filter.Filter, opts options) error {
	bw := bufio.NewWriter(out)
	defer bw.Flush()

	renderer := render.NewPlain(bw, cfg)
	cor := enrich.NewCorrelator(opts.correlate)
	cols := enrich.NewColumns(opts.columns)
	folder := enrich.NewFolder(opts.fold)

	emit := func(recs []record.Record) error {
		if len(recs) == 0 {
			return nil
		}
		for _, rec := range recs {
			if err := renderer.Render(rec); err != nil {
				return err
			}
		}
		// Flush per emit so a live tail stays responsive.
		return bw.Flush()
	}

	process := func(line string, now time.Time) error {
		rec := parse.LineAs(line, opts.format)
		rec = enrich.Severity(rec)
		rec = enrich.Stack(rec, opts.module)
		if !flt.Match(rec) {
			return nil
		}
		rec = cor.Mark(rec)
		rec = cols.Mark(rec)
		return emit(folder.Add(rec, now))
	}

	done := make(chan struct{})
	defer close(done)
	lines, errc := scanLines(in, done)

	ticker := time.NewTicker(flushTick)
	defer ticker.Stop()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				// Input closed: emit the final run, then report any scan error.
				if err := emit(folder.Flush()); err != nil {
					return err
				}
				return <-errc
			}
			if err := process(line, time.Now()); err != nil {
				return err
			}
		case now := <-ticker.C:
			// Reveal runs that have paused or aged out; a busy run keeps folding
			// into one count until max-hold rather than splitting every tick.
			if err := emit(folder.FlushIdle(now)); err != nil {
				return err
			}
		}
	}
}

// scanLines reads lines from in on its own goroutine and streams them on the
// returned channel, which it closes at EOF. The scan error (nil on a clean EOF)
// is delivered once on errc, which is buffered so the goroutine never blocks on
// it. Closing done makes the goroutine stop and return even if no one is reading
// lines, so an early return from run does not strand it.
func scanLines(in io.Reader, done <-chan struct{}) (<-chan string, <-chan error) {
	lines := make(chan string)
	errc := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64<<10), maxLine)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-done:
				errc <- nil
				return
			}
		}
		errc <- sc.Err()
	}()
	return lines, errc
}

// fieldFlags collects repeated -field values into a slice, so -field may be
// passed more than once. It satisfies flag.Value.
type fieldFlags []string

func (f *fieldFlags) String() string { return strings.Join(*f, ", ") }

func (f *fieldFlags) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// isTerminal reports whether f refers to a character device (a terminal),
// without pulling in an external dependency.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
