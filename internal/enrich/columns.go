package enrich

import "github.com/shidil/plog/internal/record"

// Column-analysis tuning. The window bounds memory and how far back constancy
// is judged; minSeen is the evidence threshold below which a single-valued key
// is left salient (a field seen only once or twice is not yet proven noise).
const (
	defaultWindow  = 256
	defaultMinSeen = 3
)

// keyStat tracks, for one field key, how many records in the current window
// carry it and the distribution of their values. A key is constant when it has
// exactly one distinct value; total gates that on enough evidence.
type keyStat struct {
	values map[string]int // value -> occurrences within the window
	total  int            // records in the window carrying this key
}

// Columns is a stateful enrich stage that marks fields constant across a recent
// window as demoted, so the renderer can foreground the fields that vary. It
// adds no latency: Mark annotates and returns each record immediately, holding
// only a bounded ring of recent field sets to keep memory flat on a live tail.
type Columns struct {
	enabled bool
	window  int
	minSeen int

	ring  [][]record.KV       // recent records' fields, oldest first, bounded by window
	stats map[string]*keyStat // per-key value distribution across the ring
}

// NewColumns returns a Columns stage. When enabled is false, Mark returns every
// record unchanged so source field order and prominence are preserved.
func NewColumns(enabled bool) *Columns {
	return &Columns{
		enabled: enabled,
		window:  defaultWindow,
		minSeen: defaultMinSeen,
		stats:   make(map[string]*keyStat),
	}
}

// Mark records rec's fields into the window and returns a copy whose fields are
// flagged Demoted when proven constant. Passthrough records carry no fields and
// are returned untouched.
func (c *Columns) Mark(rec record.Record) record.Record {
	if !c.enabled || !rec.Parsed || len(rec.Fields) == 0 {
		return rec
	}

	c.admit(rec.Fields)

	fields := make([]record.KV, len(rec.Fields))
	copy(fields, rec.Fields)
	for i := range fields {
		fields[i].Demoted = c.constant(fields[i].Key)
	}
	rec.Fields = fields
	return rec
}

// admit folds a record's fields into the rolling stats and evicts the oldest
// record once the window is full, keeping the distribution bounded.
func (c *Columns) admit(fields []record.KV) {
	snapshot := make([]record.KV, len(fields))
	copy(snapshot, fields)
	for _, kv := range snapshot {
		st := c.stats[kv.Key]
		if st == nil {
			st = &keyStat{values: make(map[string]int)}
			c.stats[kv.Key] = st
		}
		st.values[kv.Val]++
		st.total++
	}
	c.ring = append(c.ring, snapshot)

	if len(c.ring) > c.window {
		c.evict(c.ring[0])
		c.ring = c.ring[1:]
	}
}

// evict removes an aged-out record's contribution to the stats, dropping keys
// that no longer appear so the map does not grow without bound.
func (c *Columns) evict(fields []record.KV) {
	for _, kv := range fields {
		st := c.stats[kv.Key]
		if st == nil {
			continue
		}
		if st.values[kv.Val]--; st.values[kv.Val] <= 0 {
			delete(st.values, kv.Val)
		}
		if st.total--; st.total <= 0 {
			delete(c.stats, kv.Key)
		}
	}
}

// constant reports whether key has held a single value across enough of the
// window to be treated as noise. Keys with too little history, or more than one
// observed value, stay salient.
func (c *Columns) constant(key string) bool {
	st := c.stats[key]
	if st == nil || st.total < c.minSeen {
		return false
	}
	return len(st.values) == 1
}
