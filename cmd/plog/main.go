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
	"os"
	"strings"

	"github.com/shidil/plog/internal/enrich"
	"github.com/shidil/plog/internal/filter"
	"github.com/shidil/plog/internal/parse"
	"github.com/shidil/plog/internal/record"
	"github.com/shidil/plog/internal/render"
)

// maxLine bounds a single log line; the embedded panic traces in real logs run
// to a few KB, so the default bufio.Scanner limit is raised generously.
const maxLine = 4 << 20 // 4 MiB

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
// rendered.
func run(in *os.File, out *os.File, cfg render.PlainConfig, flt *filter.Filter, module string, fold, columns bool) error {
	bw := bufio.NewWriter(out)
	defer bw.Flush()

	renderer := render.NewPlain(bw, cfg)
	cols := enrich.NewColumns(columns)
	folder := enrich.NewFolder(fold)

	emit := func(recs []record.Record) error {
		for _, rec := range recs {
			if err := renderer.Render(rec); err != nil {
				return err
			}
		}
		return nil
	}

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		rec := parse.Line(sc.Text())
		rec = enrich.Severity(rec)
		rec = enrich.Stack(rec, module)
		if !flt.Match(rec) {
			continue
		}
		rec = cols.Mark(rec)
		if err := emit(folder.Add(rec)); err != nil {
			return err
		}
		// Flush per line so a live tail stays responsive.
		if err := bw.Flush(); err != nil {
			return err
		}
	}
	if err := emit(folder.Flush()); err != nil {
		return err
	}
	return sc.Err()
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
