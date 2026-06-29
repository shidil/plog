package enrich

import (
	"testing"

	"github.com/shidil/plog/internal/record"
)

// demoted reports whether the named field of rec was flagged demoted.
func demoted(rec record.Record, key string) bool {
	for _, kv := range rec.Fields {
		if kv.Key == key {
			return kv.Demoted
		}
	}
	return false
}

func TestColumnsDemotesConstantPromotesVarying(t *testing.T) {
	c := NewColumns(true)

	// Feed a run where component is constant and method varies. minSeen is 3,
	// so the constant field is only demoted once enough evidence accumulates.
	methods := []string{"A", "B", "C", "D"}
	var last record.Record
	for _, m := range methods {
		last = c.Mark(mkRec(record.LevelInfo, "finished call",
			record.KV{Key: "component", Val: "rpc.server"},
			record.KV{Key: "rpc.method", Val: m},
		))
	}

	if !demoted(last, "component") {
		t.Errorf("constant field component was not demoted after %d records", len(methods))
	}
	if demoted(last, "rpc.method") {
		t.Errorf("varying field rpc.method was demoted, want salient")
	}
}

func TestColumnsKeepsRareFieldSalient(t *testing.T) {
	c := NewColumns(true)

	// Two records with the same value is below minSeen (3): not yet proven noise.
	c.Mark(mkRec(record.LevelInfo, "msg", record.KV{Key: "region", Val: "ap-1"}))
	last := c.Mark(mkRec(record.LevelInfo, "msg", record.KV{Key: "region", Val: "ap-1"}))

	if demoted(last, "region") {
		t.Errorf("field seen only twice was demoted, want salient until minSeen reached")
	}
}

func TestColumnsForgetsEvictedValues(t *testing.T) {
	c := NewColumns(true)
	c.window = 3 // small window so eviction is easy to drive

	// host is constant within any 3-record window, so once the window is full
	// it stays demoted even as older records age out.
	for range 3 {
		c.Mark(mkRec(record.LevelInfo, "msg", record.KV{Key: "host", Val: "node-1"}))
	}
	last := c.Mark(mkRec(record.LevelInfo, "msg", record.KV{Key: "host", Val: "node-1"}))
	if !demoted(last, "host") {
		t.Fatalf("constant host not demoted within a full window")
	}

	st := c.stats["host"]
	if st.total != c.window {
		t.Errorf("window stats unbounded: total = %d, want %d", st.total, c.window)
	}
}

func TestColumnsDisabledLeavesFieldsUntouched(t *testing.T) {
	c := NewColumns(false)
	var last record.Record
	for range 5 {
		last = c.Mark(mkRec(record.LevelInfo, "msg", record.KV{Key: "component", Val: "rpc.server"}))
	}
	if demoted(last, "component") {
		t.Errorf("columns disabled: field was demoted")
	}
}

func TestColumnsPassthroughUnchanged(t *testing.T) {
	c := NewColumns(true)
	in := record.Record{Raw: "plain text line", Parsed: false}
	got := c.Mark(in)
	if got.Raw != in.Raw || got.Parsed != in.Parsed || got.Fields != nil {
		t.Errorf("passthrough record altered: %+v", got)
	}
}
