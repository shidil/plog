package main

import (
	"io"
	"strings"
	"testing"
	"time"
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
