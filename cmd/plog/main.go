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
	"strings"
	"time"

	"github.com/shidil/plog/internal/enrich"
	"github.com/shidil/plog/internal/filter"
	"github.com/shidil/plog/internal/parse"
	"github.com/shidil/plog/internal/record"
	"github.com/shidil/plog/internal/render"
)

// maxLine bounds a single log line; the embedded panic traces in real logs run
// to a few KB, so the default bufio.Scanner limit is raised generously.
const maxLine = 4 << 20 // 4 MiB

// flushInterval caps how long Folder may hold a still-open run before it is
// emitted. Without it, a live tail (docker logs -f) narrowed by a filter to one
// near-identical event folds into a single run that no distinct line ever ends,
// so nothing prints until EOF — which a follow never reaches. The tick bounds
// that latency; counts then split across ticks instead of accumulating to one.
const flushInterval = time.Second

func main() {
	module := flag.String("module", "github.com/example", "import-path prefix treated as project code in stack traces")
	noFold := flag.Bool("no-fold", false, "do not collapse consecutive near-identical lines")
	noColumns := flag.Bool("no-columns", false, "do not demote fields that stay constant across the recent window")
	expandStack := flag.Bool("expand-stack", false, "show every stack frame instead of folding framework frames")
	noColor := flag.Bool("no-color", false, "disable ANSI color even on a terminal")
	minLevel := flag.String("min-level", "", "drop parsed records below this effective severity (debug|info|warn|error)")
	grep := flag.String("grep", "", "show only lines matching this regular expression (message/fields, or raw for passthrough)")
	var fields fieldFlags
	flag.Var(&fields, "field", "show only records whose named field contains a substring, e.g. -field rpc.method=Resolve (repeatable)")
	flag.Parse()

	flt, err := filter.New(*minLevel, *grep, fields)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plog:", err)
		os.Exit(1)
	}

	cfg := render.PlainConfig{
		Color:       !*noColor && isTerminal(os.Stdout),
		ExpandStack: *expandStack,
	}
	if err := run(os.Stdin, os.Stdout, cfg, flt, *module, !*noFold, !*noColumns); err != nil {
		fmt.Fprintln(os.Stderr, "plog:", err)
		os.Exit(1)
	}
}

// run drives the pipeline: each line is parsed, enriched, filtered, folded, and
// rendered. Lines are read on a separate goroutine so the main loop can also
// wake on a timer and flush a folded run that is still open (see flushInterval);
// all record processing stays on this one goroutine, so the pipeline holds no
// shared state.
func run(in *os.File, out *os.File, cfg render.PlainConfig, flt *filter.Filter, module string, fold, columns bool) error {
	bw := bufio.NewWriter(out)
	defer bw.Flush()

	renderer := render.NewPlain(bw, cfg)
	cols := enrich.NewColumns(columns)
	folder := enrich.NewFolder(fold)

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

	process := func(line string) error {
		rec := parse.Line(line)
		rec = enrich.Severity(rec)
		rec = enrich.Stack(rec, module)
		if !flt.Match(rec) {
			return nil
		}
		rec = cols.Mark(rec)
		return emit(folder.Add(rec))
	}

	done := make(chan struct{})
	defer close(done)
	lines, errc := scanLines(in, done)

	ticker := time.NewTicker(flushInterval)
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
			if err := process(line); err != nil {
				return err
			}
		case <-ticker.C:
			// A run still open after a full tick has waited long enough; emit it
			// rather than hold it for a distinct line that a follow may never see.
			if err := emit(folder.Flush()); err != nil {
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
