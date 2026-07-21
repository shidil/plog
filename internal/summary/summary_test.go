package summary

import (
	"fmt"
	"testing"
	"time"

	"github.com/shidil/plog/internal/record"
)

// rec builds a parsed record at the given effective level. The declared level
// matches unless the test overrides it afterwards; Observe reads Effective
// only, mirroring the pipeline where Severity has already re-ranked.
func rec(eff record.Level, msg string) record.Record {
	return record.Record{Parsed: true, Level: eff, Effective: eff, Message: msg}
}

func TestObserveCountsByEffective(t *testing.T) {
	s := New(true)
	// A re-ranked panic: declared INFO, effective ERR — must count as an error.
	reranked := rec(record.LevelError, "panic: nil pointer")
	reranked.Level = record.LevelInfo
	for _, r := range []record.Record{
		reranked,
		rec(record.LevelWarn, "slow request"),
		rec(record.LevelInfo, "listening"),
		rec(record.LevelDebug, "cache lookup"),
		rec(record.LevelUnknown, "???"),
		{Parsed: false, Raw: "mojibake ┼┼┼"},
	} {
		s.Observe(r)
	}

	got := s.Report()
	if got.Errors != 1 || got.Warns != 1 || got.Infos != 2 || got.Unknown != 1 || got.Passthrough != 1 {
		t.Errorf("Report counts = %d err / %d warn / %d info / %d unknown / %d passthrough, want 1/1/2/1/1",
			got.Errors, got.Warns, got.Infos, got.Unknown, got.Passthrough)
	}
}

func TestObserveFoldsTemplatesKeepsFirstMessageWhole(t *testing.T) {
	s := New(true)
	// Same event shape, differing only in masked tokens (numbers): one template.
	s.Observe(rec(record.LevelError, "write failed after 3 retries"))
	s.Observe(rec(record.LevelError, "write failed after 17 retries"))
	s.Observe(rec(record.LevelWarn, "falling back to unoptimized response"))

	got := s.Report()
	if got.UniqueErrors != 1 || got.UniqueWarns != 1 {
		t.Fatalf("unique = %d errors / %d warns, want 1/1", got.UniqueErrors, got.UniqueWarns)
	}
	wantTop := Line{Count: 2, Message: "write failed after 3 retries"}
	if len(got.TopErrors) != 1 || got.TopErrors[0] != wantTop {
		t.Errorf("TopErrors = %+v, want [%+v] (first-seen message, untruncated)", got.TopErrors, wantTop)
	}
}

func TestObserveUsesStackHeaderAsRepresentative(t *testing.T) {
	s := New(true)
	r := rec(record.LevelError, "ignored when a stack is present")
	r.Stack = &record.StackTrace{Header: "panic: runtime error: index out of range"}
	s.Observe(r)

	got := s.Report()
	if len(got.TopErrors) != 1 || got.TopErrors[0].Message != r.Stack.Header {
		t.Errorf("TopErrors = %+v, want the stack header %q", got.TopErrors, r.Stack.Header)
	}
}

func TestReportOrdersMostFrequentFirst(t *testing.T) {
	s := New(true)
	s.Observe(rec(record.LevelError, "rare failure"))
	for range 3 {
		s.Observe(rec(record.LevelError, "storm failure"))
	}
	for range 2 {
		s.Observe(rec(record.LevelError, "middling failure"))
	}

	got := s.Report()
	want := []Line{
		{Count: 3, Message: "storm failure"},
		{Count: 2, Message: "middling failure"},
		{Count: 1, Message: "rare failure"},
	}
	if len(got.TopErrors) != len(want) {
		t.Fatalf("TopErrors has %d lines, want %d", len(got.TopErrors), len(want))
	}
	for i := range want {
		if got.TopErrors[i] != want[i] {
			t.Errorf("TopErrors[%d] = %+v, want %+v", i, got.TopErrors[i], want[i])
		}
	}
}

func TestReportCapsShownPerLevel(t *testing.T) {
	s := New(true)
	for i := range maxShown + 2 {
		s.Observe(rec(record.LevelError, "distinct failure "+letters(i)))
	}

	got := s.Report()
	if len(got.TopErrors) != maxShown || got.MoreErrors != 2 {
		t.Errorf("TopErrors has %d lines with MoreErrors %d, want %d and 2",
			len(got.TopErrors), got.MoreErrors, maxShown)
	}
	if got.UniqueErrors != maxShown+2 {
		t.Errorf("UniqueErrors = %d, want %d (shown cap must not hide uniqueness)", got.UniqueErrors, maxShown+2)
	}
}

func TestObserveCountsUntrackedBeyondTemplateCap(t *testing.T) {
	s := New(true)
	for i := range maxTemplates + 3 {
		s.Observe(rec(record.LevelError, "distinct failure "+letters(i)))
	}
	// An already-tracked template must still fold in after the cap fills.
	s.Observe(rec(record.LevelError, "distinct failure "+letters(0)))

	got := s.Report()
	if got.UniqueErrors != maxTemplates || got.Untracked != 3 {
		t.Errorf("UniqueErrors = %d with Untracked %d, want %d and 3", got.UniqueErrors, got.Untracked, maxTemplates)
	}
	if got.TopErrors[0] != (Line{Count: 2, Message: "distinct failure " + letters(0)}) {
		t.Errorf("TopErrors[0] = %+v, want the refolded first template with count 2", got.TopErrors[0])
	}
	if got.Errors != maxTemplates+4 {
		t.Errorf("Errors = %d, want %d (untracked records still count)", got.Errors, maxTemplates+4)
	}
}

func TestObserveTracksTimeSpan(t *testing.T) {
	s := New(true)
	first := time.Date(2026, 7, 21, 14, 29, 1, 0, time.UTC)
	last := time.Date(2026, 7, 21, 14, 32, 47, 0, time.UTC)

	r := rec(record.LevelInfo, "a")
	r.Time = first
	s.Observe(r)
	s.Observe(rec(record.LevelInfo, "no timestamp"))
	r = rec(record.LevelInfo, "b")
	r.Time = last
	s.Observe(r)

	got := s.Report()
	if !got.First.Equal(first) || !got.Last.Equal(last) {
		t.Errorf("span = %v–%v, want %v–%v", got.First, got.Last, first, last)
	}
}

func TestDisabledObservesNothing(t *testing.T) {
	s := New(false)
	s.Observe(rec(record.LevelError, "boom"))
	s.Observe(record.Record{Parsed: false, Raw: "raw"})

	got := s.Report()
	if got.Errors != 0 || got.Passthrough != 0 || len(got.TopErrors) != 0 {
		t.Errorf("disabled Report = %+v, want all zero", got)
	}
}

// letters encodes i as a two-letter token so generated messages stay distinct
// after template masking (digits would be masked to <n> and collapse).
func letters(i int) string {
	return fmt.Sprintf("%c%c", 'a'+i/26, 'a'+i%26)
}
