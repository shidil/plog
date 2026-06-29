// Package render turns enriched records into human-readable output. The
// Renderer interface keeps the pipeline's final stage swappable: the spike
// ships a streaming plain-text renderer, and a future interactive TUI can
// satisfy the same contract.
package render

import "github.com/shidil/plog/internal/record"

// Renderer writes one record to its destination. Implementations are expected
// to be stateless with respect to ordering: the pipeline feeds records in
// stream order and calls Render exactly once per emitted record.
type Renderer interface {
	Render(rec record.Record) error
}
