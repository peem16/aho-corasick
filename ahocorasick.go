package ahocorasick

// AhoCorasick is the primary type for multi-pattern string search.
// Build one with New or AhoCorasickBuilder.Build, then reuse it
// freely — it is safe for concurrent use after construction.
type AhoCorasick struct {
	imp       automaton   // *NFA or *DFA
	pf        *prefilter
	matchKind MatchKind
	kind      AhoCorasickKind
	patterns  [][]byte // deep copy of original patterns
	patCount  int
}

// New builds an AhoCorasick automaton with default settings
// (MatchKindStandard, auto kind, prefilter enabled).
func New(patterns [][]byte) (*AhoCorasick, error) {
	return NewBuilder().Build(patterns)
}

// NewString is a convenience wrapper for string patterns.
func NewString(patterns []string) (*AhoCorasick, error) {
	bs := make([][]byte, len(patterns))
	for i, p := range patterns {
		bs[i] = []byte(p)
	}
	return New(bs)
}

// PatternCount returns the number of patterns in the automaton.
func (ac *AhoCorasick) PatternCount() int { return ac.patCount }

// Pattern returns the i-th pattern (a copy — safe to modify).
func (ac *AhoCorasick) Pattern(i PatternID) []byte {
	cp := make([]byte, len(ac.patterns[i]))
	copy(cp, ac.patterns[i])
	return cp
}

// MatchKind returns the match semantics used by this automaton.
func (ac *AhoCorasick) MatchKind() MatchKind { return ac.matchKind }

// Kind returns the automaton representation kind.
func (ac *AhoCorasick) Kind() AhoCorasickKind { return ac.kind }

// ---------------------------------------------------------------------------
// IsMatch
// ---------------------------------------------------------------------------

// IsMatch reports whether haystack contains at least one match.
func (ac *AhoCorasick) IsMatch(haystack []byte) bool {
	_, ok := ac.Find(haystack)
	return ok
}

// IsMatchString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) IsMatchString(haystack string) bool {
	return ac.IsMatch([]byte(haystack))
}

// ---------------------------------------------------------------------------
// Find — first match only
// ---------------------------------------------------------------------------

// Find returns the first match in haystack, if any.
func (ac *AhoCorasick) Find(haystack []byte) (Match, bool) {
	if ac.imp == nil {
		return Match{}, false
	}
	return ac.findFrom(haystack, 0, startStateID)
}

// FindString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindString(haystack string) (Match, bool) {
	return ac.Find([]byte(haystack))
}

// findFrom searches haystack starting at position pos with the automaton
// in state cur.  This is the shared implementation used by Find and FindIter.
func (ac *AhoCorasick) findFrom(haystack []byte, pos int, cur stateID) (Match, bool) {
	switch ac.matchKind {
	case MatchKindStandard:
		return ac.findStandard(haystack, pos, cur)
	case MatchKindLeftmostFirst:
		return ac.findLeftmostFirst(haystack, pos)
	case MatchKindLeftmostLongest:
		return ac.findLeftmostLongest(haystack, pos)
	}
	return Match{}, false
}

// ---------------------------------------------------------------------------
// Standard search
// ---------------------------------------------------------------------------

func (ac *AhoCorasick) findStandard(haystack []byte, pos int, state stateID) (Match, bool) {
	imp := ac.imp
	pf := ac.pf
	n := len(haystack)

	// Check for a match at the current position before consuming any byte.
	// This handles empty patterns and resuming an iterator at a match state.
	if imp.isMatch(state) {
		ms := imp.matches(state)
		pid := ms[0]
		patLen := len(ac.patterns[pid])
		start := pos - patLen
		if start < 0 {
			start = 0
		}
		return Match{id: pid, start: start, end: pos}, true
	}

	if n == 0 || pos >= n {
		return Match{}, false
	}

	// BCE hint: tells the compiler n-1 is a valid index.
	_ = haystack[n-1]

	for pos < n {
		// Prefilter: skip ahead when at start state.
		if pf.enabled && state == startStateID {
			next := pf.next(haystack, pos)
			if next < 0 {
				return Match{}, false
			}
			pos = next
		}

		b := haystack[pos]
		pos++
		state = imp.nextState(state, b)

		if imp.isMatch(state) {
			ms := imp.matches(state)
			pid := ms[0]
			patLen := len(ac.patterns[pid])
			return Match{id: pid, start: pos - patLen, end: pos}, true
		}
	}

	return Match{}, false
}

// ---------------------------------------------------------------------------
// LeftmostFirst search
// ---------------------------------------------------------------------------
//
// The leftmost-first algorithm scans left to right, tracking the "current
// match" (the leftmost, earliest-pattern match found so far) and continuing
// until we can guarantee no better match starts at the same or earlier position.

func (ac *AhoCorasick) findLeftmostFirst(haystack []byte, pos int) (Match, bool) {
	imp := ac.imp
	n := len(haystack)

	if n == 0 {
		if imp.isMatch(startStateID) {
			ms := imp.matches(startStateID)
			return Match{id: ms[0], start: 0, end: 0}, true
		}
		return Match{}, false
	}

	_ = haystack[n-1]

	state := startStateID
	// matchStart: position where the current candidate match started.
	// We need it to compute the match's start offset.
	matchStart := -1
	var best Match
	hasBest := false

	for pos < n {
		b := haystack[pos]
		pos++
		state = imp.nextState(state, b)

		if imp.isDead(state) {
			if hasBest {
				return best, true
			}
			// Reset to start state and continue.
			state = startStateID
			matchStart = -1
			continue
		}

		if imp.isMatch(state) {
			ms := imp.matches(state)
			pid := ms[0] // lowest PatternID = LeftmostFirst
			patLen := len(ac.patterns[pid])
			start := pos - patLen
			if !hasBest || start < best.start || (start == best.start && pid < best.id) {
				best = Match{id: pid, start: start, end: pos}
				hasBest = true
				matchStart = start
			}
		} else if hasBest && matchStart >= 0 {
			// We had a match; if we've moved past its start, check if we
			// can still find a longer one.  For LeftmostFirst we keep the
			// earliest-pattern match, so once we have one we can stop
			// as soon as the dead state is reached or haystack ends.
			_ = matchStart
		}
	}

	return best, hasBest
}

// ---------------------------------------------------------------------------
// LeftmostLongest search
// ---------------------------------------------------------------------------

func (ac *AhoCorasick) findLeftmostLongest(haystack []byte, pos int) (Match, bool) {
	imp := ac.imp
	n := len(haystack)

	if n == 0 {
		if imp.isMatch(startStateID) {
			ms := imp.matches(startStateID)
			// Pick longest pattern at start state.
			best := ms[0]
			bestLen := len(ac.patterns[best])
			for _, pid := range ms[1:] {
				if l := len(ac.patterns[pid]); l > bestLen {
					bestLen = l
					best = pid
				}
			}
			return Match{id: best, start: 0, end: 0}, true
		}
		return Match{}, false
	}

	_ = haystack[n-1]

	state := startStateID
	var best Match
	hasBest := false

	for pos < n {
		b := haystack[pos]
		pos++
		state = imp.nextState(state, b)

		if imp.isDead(state) {
			if hasBest {
				return best, true
			}
			state = startStateID
			continue
		}

		if imp.isMatch(state) {
			ms := imp.matches(state)
			// Find the longest match among outputs.
			pid := ms[0]
			patLen := len(ac.patterns[pid])
			for _, p := range ms[1:] {
				if l := len(ac.patterns[p]); l > patLen {
					patLen = l
					pid = p
				}
			}
			start := pos - patLen
			if !hasBest || start < best.start || (start == best.start && patLen > best.end-best.start) {
				best = Match{id: pid, start: start, end: pos}
				hasBest = true
			}
		}
	}

	return best, hasBest
}

// ---------------------------------------------------------------------------
// FindAll — collect all matches
// ---------------------------------------------------------------------------

// FindAll returns all non-overlapping matches.
func (ac *AhoCorasick) FindAll(haystack []byte) []Match {
	var out []Match
	it := ac.FindIter(haystack)
	for {
		m, ok := it.Next()
		if !ok {
			break
		}
		out = append(out, m)
	}
	it.Close()
	return out
}

// FindAllString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindAllString(haystack string) []Match {
	return ac.FindAll([]byte(haystack))
}

// ---------------------------------------------------------------------------
// Iterators
// ---------------------------------------------------------------------------

// FindIter returns an iterator over non-overlapping matches.
// Call it.Close() when done to return the iterator to the pool.
func (ac *AhoCorasick) FindIter(haystack []byte) *FindIter {
	return newFindIter(ac, haystack)
}

// FindIterString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindIterString(haystack string) *FindIter {
	return ac.FindIter([]byte(haystack))
}

// FindOverlappingIter returns an iterator over all matches including
// overlapping ones.  Only meaningful for MatchKindStandard.
func (ac *AhoCorasick) FindOverlappingIter(haystack []byte) *FindOverlappingIter {
	return newFindOverlappingIter(ac, haystack)
}

// FindOverlappingIterString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindOverlappingIterString(haystack string) *FindOverlappingIter {
	return ac.FindOverlappingIter([]byte(haystack))
}
