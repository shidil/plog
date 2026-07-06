package enrich

import "github.com/shidil/plog/internal/record"

// traceGrammar recognizes and parses the stack-trace shape of one language.
// Grammars are tried in registration order; the first whose detect fires owns
// the value. Adding a language is a new grammar plus a registry entry — the
// Stack stage that drives them stays language-agnostic.
type traceGrammar interface {
	lang() string                              // short language tag, e.g. "go" or "node"
	detect(s string) bool                      // does s look like this language's trace?
	parse(s, module string) *record.StackTrace // extract header + frames, or nil
}

// grammars lists the supported languages in priority order.
var grammars = []traceGrammar{goGrammar{}, nodeGrammar{}}

// detectAndParse returns the trace parsed by the first grammar that recognizes
// s, or nil when none do. The winning grammar's tag is recorded on the result.
func detectAndParse(s, module string) *record.StackTrace {
	for _, g := range grammars {
		if !g.detect(s) {
			continue
		}
		st := g.parse(s, module)
		if st != nil {
			st.Lang = g.lang()
		}
		return st
	}
	return nil
}
