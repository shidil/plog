// Package summary accumulates the triage tallies behind the --summary footer:
// counts per effective severity, a time span, and one representative line per
// distinct warn/error template. It is an observer stage — Observe annotates
// nothing and emits nothing per record — so with the flag off (or on, above the
// footer) the rendered stream is byte-identical.
package summary

import (
	"slices"
	"time"

	"github.com/shidil/plog/internal/enrich"
	"github.com/shidil/plog/internal/record"
)

// maxTemplates bounds how many distinct warn/error templates are tracked;
// beyond it a new template folds into an explicit untracked counter rather
// than vanishing. maxShown caps how many representative lines Report surfaces
// per level, with the remainder reported as an explicit "+N more" count.
const (
	maxTemplates = 64
	maxShown     = 8
)

// entry tracks one distinct warn/error template: the level it counts under,
// the first-seen message kept whole as the representative, how many records
// matched, and the arrival order (to break frequency ties first-seen-first).
type entry struct {
	level record.Level
	msg   string
	count int
	order int
}

// Summary tallies the displayed stream for the --summary footer. Counts are
// per record and taken before folding, so fold-split artifacts cannot skew
// them; call Observe after the filter so the summary describes what the
// reader actually saw.
type Summary struct {
	enabled bool

	errors, warns, infos, unknown int
	passthrough                   int
	first, last                   time.Time

	templates map[string]*entry
	untracked int // warn/error records seen after the template cap filled
}

// New returns a Summary. When enabled is false, Observe is a no-op and Report
// returns an empty Report.
func New(enabled bool) *Summary {
	return &Summary{enabled: enabled, templates: make(map[string]*entry)}
}

// Observe tallies one displayed record. Passthrough lines are counted but
// never interpreted — their severity is unknowable, so they get their own
// bucket rather than a guess. Warn-or-worse records are additionally keyed by
// their masked template so the footer can report "N unique" with a whole,
// untruncated representative message per template.
func (s *Summary) Observe(rec record.Record) {
	if !s.enabled {
		return
	}
	if !rec.Parsed {
		s.passthrough++
		return
	}

	if !rec.Time.IsZero() {
		if s.first.IsZero() {
			s.first = rec.Time
		}
		s.last = rec.Time
	}

	switch rec.Effective {
	case record.LevelError:
		s.errors++
	case record.LevelWarn:
		s.warns++
	case record.LevelInfo, record.LevelDebug:
		s.infos++
	default:
		s.unknown++
	}
	if rec.Effective < record.LevelWarn {
		return
	}

	key := enrich.Template(rec)
	if e, ok := s.templates[key]; ok {
		e.count++
		return
	}
	if len(s.templates) >= maxTemplates {
		s.untracked++
		return
	}
	msg := rec.Message
	if rec.Stack != nil {
		msg = rec.Stack.Header
	}
	s.templates[key] = &entry{level: rec.Effective, msg: msg, count: 1, order: len(s.templates)}
}

// Line is one representative warn/error template in a Report: the first-seen
// message, whole, and how many records folded into it.
type Line struct {
	Count   int
	Message string
}

// Report is the aggregate the footer renders. Top lists are sorted
// most-frequent first (first-seen breaks ties) and capped at maxShown, with
// the per-level remainder in MoreErrors/MoreWarns; Untracked counts
// warn/error records whose template arrived after the tracking cap filled.
type Report struct {
	Errors, Warns, Infos, Unknown int
	Passthrough                   int
	UniqueErrors, UniqueWarns     int
	First, Last                   time.Time
	TopErrors, TopWarns           []Line
	MoreErrors, MoreWarns         int
	Untracked                     int
}

// Report snapshots the tallies for rendering. It does not reset or mutate the
// Summary.
func (s *Summary) Report() Report {
	rep := Report{
		Errors:      s.errors,
		Warns:       s.warns,
		Infos:       s.infos,
		Unknown:     s.unknown,
		Passthrough: s.passthrough,
		First:       s.first,
		Last:        s.last,
		Untracked:   s.untracked,
	}

	var errs, warns []*entry
	for _, e := range s.templates {
		if e.level == record.LevelError {
			errs = append(errs, e)
		} else {
			warns = append(warns, e)
		}
	}
	rep.UniqueErrors, rep.UniqueWarns = len(errs), len(warns)
	rep.TopErrors, rep.MoreErrors = top(errs)
	rep.TopWarns, rep.MoreWarns = top(warns)
	return rep
}

// top sorts entries most-frequent first (arrival order breaks ties) and
// returns at most maxShown of them as Lines, plus how many were left out.
func top(es []*entry) ([]Line, int) {
	slices.SortFunc(es, func(a, b *entry) int {
		if a.count != b.count {
			return b.count - a.count
		}
		return a.order - b.order
	})
	more := 0
	if len(es) > maxShown {
		more = len(es) - maxShown
		es = es[:maxShown]
	}
	lines := make([]Line, len(es))
	for i, e := range es {
		lines[i] = Line{Count: e.count, Message: e.msg}
	}
	return lines, more
}
