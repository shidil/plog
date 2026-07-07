package main

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shidil/plog/internal/filter"
	"github.com/shidil/plog/internal/parse"
	"github.com/shidil/plog/internal/render"
)

// TestScanLines_streamsThenClosesOnEOF checks the happy path: every line is
// delivered in order, the channel is closed at EOF, and errc reports nil.
func TestScanLines_streamsThenClosesOnEOF(t *testing.T) {
	done := make(chan struct{})
	defer close(done)

	lines, errc := scanLines(strings.NewReader("a\nb\nc\n"), done)

	var got []string
	for line := range lines {
		got = append(got, line)
	}
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scanLines lines = %v, want %v", got, want)
	}
	if err := <-errc; err != nil {
		t.Errorf("scanLines err = %v, want nil", err)
	}
}

// TestScanLines_stopsOnDone checks the cancellation path: when the consumer
// stops reading and closes done, the reader goroutine unblocks from its pending
// send and returns instead of leaking. blockingReader never reaches EOF, so the
// goroutine can only finish via done — a value on errc proves it took that path
// without racing on a final read from lines, after which lines must close.
func TestScanLines_stopsOnDone(t *testing.T) {
	done := make(chan struct{})
	lines, errc := scanLines(blockingReader{strings.NewReader("a\nb\nc\n")}, done)

	if first := <-lines; first != "a" {
		t.Fatalf("first line = %q, want %q", first, "a")
	}
	// Stop reading: the goroutine is now blocked sending "b". Cancel it.
	close(done)

	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("scanLines err = %v, want nil after cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scanLines did not stop after done closed")
	}

	// The goroutine took the done path, so it sends nothing more; lines must
	// close (no leak). Draining is safe now that no further sends can occur.
	closed := make(chan struct{})
	go func() {
		for range lines {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("lines not closed after goroutine returned")
	}
}

// BenchmarkPipeline drives the full stdin-to-stdout pipeline over a large
// in-memory stream, validating IDEA.md's "scales to high-volume docker logs -f"
// claim and guarding against throughput regressions. Output is discarded so the
// measurement reflects parse/enrich/render work, not terminal I/O. Report
// throughput with `go test -bench=BenchmarkPipeline -benchmem ./cmd/plog`.
func BenchmarkPipeline(b *testing.B) {
	stream := buildBenchStream(10000)
	flt, err := filter.New("", "", nil)
	if err != nil {
		b.Fatalf("filter.New: %v", err)
	}
	cfg := render.PlainConfig{Color: false}
	opts := options{
		format:    parse.FormatAuto,
		module:    "github.com/example",
		fold:      true,
		columns:   true,
		correlate: true,
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	for b.Loop() {
		if err := run(strings.NewReader(stream), io.Discard, cfg, flt, opts); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// buildBenchStream returns n lines of representative structured-log traffic: a
// repeated OTel failure (exercises severity escalation + folding), distinct RPC
// result logs (columns + correlation), and varying app logs. It is deterministic
// so runs are comparable.
func buildBenchStream(n int) string {
	methods := []string{"ResolveLocationSlug", "ListStores", "GetMenu", "PlaceOrder"}
	var b strings.Builder
	for i := range n {
		switch i % 5 {
		case 0, 1:
			fmt.Fprintf(&b, `{"time":"2026-07-07T04:31:%02dZ","level":"info","msg":"failed to upload metrics to collector: dial tcp 10.0.0.%d:4317: connection refused"}`+"\n", i%60, i%256)
		case 2, 3:
			fmt.Fprintf(&b, `{"time":"2026-07-07T04:31:%02dZ","level":"info","msg":"finished call","rpc.service":"storefront.LocationService","rpc.method":%q,"rpc.duration":"%dms","rpc.status":"ok","client":"192.168.1.%d"}`+"\n", i%60, methods[i%len(methods)], i%400, i%256)
		default:
			fmt.Fprintf(&b, `{"time":"2026-07-07T04:31:%02dZ","level":"debug","msg":"cache lookup","key":"store:%d","hit":%t}`+"\n", i%60, i, i%2 == 0)
		}
	}
	return b.String()
}

// blockingReader yields its wrapped data, then blocks forever instead of
// returning io.EOF, standing in for a follow tail that never ends.
type blockingReader struct{ r io.Reader }

func (b blockingReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		select {} // never returns, like docker logs -f
	}
	return n, err
}
