package heal

// PatternRegistry is an ordered list of HealPatterns. The registry walks the
// list and returns the first pattern whose Matches predicate returns true.
// Order matters when two patterns could match the same failure cluster — the
// more specific pattern MUST be registered first.
type PatternRegistry struct {
	patterns []HealPattern
}

// New builds a registry with the given patterns in registration order.
func New(patterns ...HealPattern) *PatternRegistry {
	return &PatternRegistry{
		patterns: append([]HealPattern(nil), patterns...),
	}
}

// Default returns the canonical registry with all built-in patterns
// registered in priority order. Add new patterns here after they have a
// human-reviewed PR.
func Default() *PatternRegistry {
	return New(
		NewMissingFixture(),
		NewMissingSeedUpdate(),
		NewRemovedExport(),
	)
}

// Patterns returns a copy of the registered patterns in registration order.
// Used by tests and doc generation; the slice is safe to mutate.
func (r *PatternRegistry) Patterns() []HealPattern {
	if r == nil {
		return nil
	}
	out := make([]HealPattern, len(r.patterns))
	copy(out, r.patterns)
	return out
}

// Match returns the first pattern whose Matches predicate accepts the
// failure cluster. Returns (nil, false) when nothing matches.
func (r *PatternRegistry) Match(failures []BaselineFailure) (HealPattern, bool) {
	if r == nil || len(failures) == 0 {
		return nil, false
	}
	for _, p := range r.patterns {
		if p.Matches(failures) {
			return p, true
		}
	}
	return nil, false
}
