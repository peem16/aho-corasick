package ahocorasick

// AhoCorasick is the primary type for multi-pattern string search.
// Build one with New or AhoCorasickBuilder.Build, then reuse it
// freely — it is safe for concurrent use after construction.
type AhoCorasick struct {
	imp       automaton // *NFA or *DFA
	nfa       *NFA      // non-nil when imp is *NFA; avoids repeated type assertions
	dfa       *DFA      // non-nil when imp is *DFA; avoids repeated type assertions
	pf        *prefilter
	matchKind MatchKind
	kind      AhoCorasickKind
	patterns  [][]byte // deep copy of original patterns
	patLens   []int32  // cached pattern lengths for hot-path access
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
		if ac.dfa != nil {
			return ac.findLeftmostFirstDFA(ac.dfa, haystack, pos)
		}
		return ac.findLeftmostFirst(haystack, pos)
	case MatchKindLeftmostLongest:
		if ac.dfa != nil {
			return ac.findLeftmostLongestDFA(ac.dfa, haystack, pos)
		}
		return ac.findLeftmostLongest(haystack, pos)
	}
	return Match{}, false
}

// ---------------------------------------------------------------------------
// Standard search
// ---------------------------------------------------------------------------

func (ac *AhoCorasick) findStandard(haystack []byte, pos int, state stateID) (Match, bool) {
	// Use cached pointers to avoid repeated type assertions in hot loop.
	if ac.nfa != nil {
		return ac.findStandardNFA(ac.nfa, haystack, pos, state)
	}
	if ac.dfa != nil {
		return ac.findStandardDFA(ac.dfa, haystack, pos, state)
	}
	return ac.findStandardGeneric(haystack, pos, state)
}

func (ac *AhoCorasick) findStandardGeneric(haystack []byte, pos int, state stateID) (Match, bool) {
	imp := ac.imp
	pf := ac.pf
	n := len(haystack)

	// Check for a match at the current position before consuming any byte.
	// This handles empty patterns and resuming an iterator at a match state.
	if imp.isMatch(state) {
		ms := imp.matches(state)
		pid := ms[0]
		patLen := int(ac.patLens[pid])
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
			patLen := int(ac.patLens[pid])
			return Match{id: pid, start: pos - patLen, end: pos}, true
		}
	}

	return Match{}, false
}

// findStandardDFA is an inlined DFA path for findStandard that eliminates
// interface dispatch and allows the compiler to register-allocate the hot
// transition table pointer.
func (ac *AhoCorasick) findStandardDFA(dfa *DFA, haystack []byte, pos int, state stateID) (Match, bool) {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	useAlpha := dfa.useAlpha

	// Check for a match at the current state before consuming any byte.
	if outBase[state] >= 0 {
		pid := outBuf[outBase[state]]
		patLen := int(patLens[pid])
		start := pos - patLen
		if start < 0 {
			start = 0
		}
		return Match{id: pid, start: start, end: pos}, true
	}

	if n == 0 || pos >= n {
		return Match{}, false
	}

	// BCE hint.
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
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if outBase[state] >= 0 {
			pid := outBuf[outBase[state]]
			patLen := int(patLens[pid])
			return Match{id: pid, start: pos - patLen, end: pos}, true
		}
	}

	return Match{}, false
}

// findStandardNFA is an inlined NFA path for findStandard that eliminates
// interface dispatch, binary search on shallow states (via dense tables),
// and enables the compiler to register-allocate NFA slice headers.
func (ac *AhoCorasick) findStandardNFA(nfa *NFA, haystack []byte, pos int, state stateID) (Match, bool) {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)

	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	outputs := nfa.outputs
	useAlpha := nfa.useAlpha

	// Check for a match at the current position before consuming any byte.
	if states[state].outputIdx >= 0 {
		pid := outputs[states[state].outputIdx]
		patLen := int(patLens[pid])
		start := pos - patLen
		if start < 0 {
			start = 0
		}
		return Match{id: pid, start: start, end: pos}, true
	}

	if n == 0 || pos >= n {
		return Match{}, false
	}

	_ = haystack[n-1]

	for pos < n {
		if pf.enabled && state == startStateID {
			next := pf.next(haystack, pos)
			if next < 0 {
				return Match{}, false
			}
			pos = next
		}

		b := haystack[pos]
		pos++

		// ---- inlined nextState ----
		if useAlpha {
			b = nfa.alphabet[b]
		}
		if state == startStateID {
			state = startTrans[b]
		} else if di := denseIdx[state]; di >= 0 {
			state = denseTrans[int(di)<<8|int(b)]
		} else {
			for {
				if state == deadStateID {
					break
				}
				tbase := int(transBase[state])
				tlen := int(transLen[state])
				tr := transBuf[tbase : tbase+tlen]
				found := false
				if tlen <= 8 {
					for i := 0; i < tlen; i++ {
						if tr[i].b == b {
							state = tr[i].next
							found = true
							break
						}
						if tr[i].b > b {
							break
						}
					}
				} else {
					lo, hi := 0, tlen
					for lo < hi {
						mid := int(uint(lo+hi) >> 1)
						if tr[mid].b < b {
							lo = mid + 1
						} else {
							hi = mid
						}
					}
					if lo < tlen && tr[lo].b == b {
						state = tr[lo].next
						found = true
					}
				}
				if found {
					break
				}
				fail := states[state].fail
				if fail == startStateID {
					state = startTrans[b]
					break
				}
				// Check if failure state has a dense table — O(1) resolve.
				if di := denseIdx[fail]; di >= 0 {
					state = denseTrans[int(di)<<8|int(b)]
					break
				}
				state = fail
			}
		}
		// ---- end inlined nextState ----

		if states[state].outputIdx >= 0 {
			pid := outputs[states[state].outputIdx]
			patLen := int(patLens[pid])
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

// findLeftmostFirstDFA is an inlined DFA path that eliminates interface dispatch.
func (ac *AhoCorasick) findLeftmostFirstDFA(dfa *DFA, haystack []byte, pos int) (Match, bool) {
	patLens := ac.patLens
	n := len(haystack)
	if n == 0 {
		if dfa.outBase[startStateID] >= 0 {
			pid := dfa.outBuf[dfa.outBase[startStateID]]
			return Match{id: pid, start: 0, end: 0}, true
		}
		return Match{}, false
	}

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	useAlpha := dfa.useAlpha

	_ = haystack[n-1]

	state := startStateID
	matchStart := -1
	var best Match
	hasBest := false

	for pos < n {
		b := haystack[pos]
		pos++
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if state == deadStateID {
			if hasBest {
				return best, true
			}
			state = startStateID
			matchStart = -1
			continue
		}

		if base := outBase[state]; base >= 0 {
			pid := outBuf[base] // lowest PatternID = LeftmostFirst
			patLen := int(patLens[pid])
			start := pos - patLen
			if !hasBest || start < best.start || (start == best.start && pid < best.id) {
				best = Match{id: pid, start: start, end: pos}
				hasBest = true
				matchStart = start
			}
		} else if hasBest && matchStart >= 0 {
			_ = matchStart
		}
	}

	return best, hasBest
}

func (ac *AhoCorasick) findLeftmostFirst(haystack []byte, pos int) (Match, bool) {
	if dfa, ok := ac.imp.(*DFA); ok {
		return ac.findLeftmostFirstDFA(dfa, haystack, pos)
	}
	if nfa, ok := ac.imp.(*NFA); ok {
		return ac.findLeftmostFirstNFA(nfa, haystack, pos)
	}
	return ac.findLeftmostFirstGeneric(haystack, pos)
}

func (ac *AhoCorasick) findLeftmostFirstGeneric(haystack []byte, pos int) (Match, bool) {
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
			state = startStateID
			matchStart = -1
			continue
		}

		if imp.isMatch(state) {
			ms := imp.matches(state)
			pid := ms[0]
			patLen := int(ac.patLens[pid])
			start := pos - patLen
			if !hasBest || start < best.start || (start == best.start && pid < best.id) {
				best = Match{id: pid, start: start, end: pos}
				hasBest = true
				matchStart = start
			}
		} else if hasBest && matchStart >= 0 {
			_ = matchStart
		}
	}

	return best, hasBest
}

// findLeftmostFirstNFA is an inlined NFA path that eliminates interface
// dispatch and enables register allocation of NFA fields.
func (ac *AhoCorasick) findLeftmostFirstNFA(nfa *NFA, haystack []byte, pos int) (Match, bool) {
	patLens := ac.patLens
	n := len(haystack)

	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	outputs := nfa.outputs
	useAlpha := nfa.useAlpha

	if n == 0 {
		if states[startStateID].outputIdx >= 0 {
			pid := outputs[states[startStateID].outputIdx]
			return Match{id: pid, start: 0, end: 0}, true
		}
		return Match{}, false
	}

	_ = haystack[n-1]

	state := startStateID
	matchStart := -1
	var best Match
	hasBest := false

	for pos < n {
		b := haystack[pos]
		pos++

		// ---- inlined nextState(state, b) ----
		if useAlpha {
			b = nfa.alphabet[b]
		}
		if state == startStateID {
			state = startTrans[b]
		} else if di := denseIdx[state]; di >= 0 {
			state = denseTrans[int(di)<<8|int(b)]
		} else {
			for {
				if state == deadStateID {
					break
				}
				tbase := int(transBase[state])
				tlen := int(transLen[state])
				tr := transBuf[tbase : tbase+tlen]
				found := false
				if tlen <= 8 {
					for i := 0; i < tlen; i++ {
						if tr[i].b == b {
							state = tr[i].next
							found = true
							break
						}
						if tr[i].b > b {
							break
						}
					}
				} else {
					lo, hi := 0, tlen
					for lo < hi {
						mid := int(uint(lo+hi) >> 1)
						if tr[mid].b < b {
							lo = mid + 1
						} else {
							hi = mid
						}
					}
					if lo < tlen && tr[lo].b == b {
						state = tr[lo].next
						found = true
					}
				}
				if found {
					break
				}
				fail := states[state].fail
				if fail == startStateID {
					state = startTrans[b]
					break
				}
				if di := denseIdx[fail]; di >= 0 {
					state = denseTrans[int(di)<<8|int(b)]
					break
				}
				state = fail
			}
		}
		// ---- end inlined nextState ----

		if state == deadStateID {
			if hasBest {
				return best, true
			}
			state = startStateID
			matchStart = -1
			continue
		}

		if states[state].outputIdx >= 0 {
			pid := outputs[states[state].outputIdx]
			patLen := int(patLens[pid])
			start := pos - patLen
			if !hasBest || start < best.start || (start == best.start && pid < best.id) {
				best = Match{id: pid, start: start, end: pos}
				hasBest = true
				matchStart = start
			}
		} else if hasBest && matchStart >= 0 {
			_ = matchStart
		}
	}

	return best, hasBest
}

// ---------------------------------------------------------------------------
// LeftmostLongest search
// ---------------------------------------------------------------------------

// findLeftmostLongestDFA is an inlined DFA path for leftmost-longest search.
func (ac *AhoCorasick) findLeftmostLongestDFA(dfa *DFA, haystack []byte, pos int) (Match, bool) {
	patLens := ac.patLens
	n := len(haystack)
	if n == 0 {
		if base := dfa.outBase[startStateID]; base >= 0 {
			olen := dfa.outLen[startStateID]
			pid := dfa.outBuf[base]
			bestLen := int(patLens[pid])
			for i := int32(1); i < olen; i++ {
				p := dfa.outBuf[base+i]
				if l := int(patLens[p]); l > bestLen {
					bestLen = l
					pid = p
				}
			}
			return Match{id: pid, start: 0, end: 0}, true
		}
		return Match{}, false
	}

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	outLen := dfa.outLen
	useAlpha := dfa.useAlpha

	_ = haystack[n-1]

	state := startStateID
	var best Match
	hasBest := false

	for pos < n {
		b := haystack[pos]
		pos++
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if state == deadStateID {
			if hasBest {
				return best, true
			}
			state = startStateID
			continue
		}

		if base := outBase[state]; base >= 0 {
			olen := outLen[state]
			pid := outBuf[base]
			patLen := int(patLens[pid])
			for i := int32(1); i < olen; i++ {
				p := outBuf[base+i]
				if l := int(patLens[p]); l > patLen {
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

func (ac *AhoCorasick) findLeftmostLongest(haystack []byte, pos int) (Match, bool) {
	if dfa, ok := ac.imp.(*DFA); ok {
		return ac.findLeftmostLongestDFA(dfa, haystack, pos)
	}
	if nfa, ok := ac.imp.(*NFA); ok {
		return ac.findLeftmostLongestNFA(nfa, haystack, pos)
	}
	return ac.findLeftmostLongestGeneric(haystack, pos)
}

func (ac *AhoCorasick) findLeftmostLongestGeneric(haystack []byte, pos int) (Match, bool) {
	imp := ac.imp
	n := len(haystack)

	if n == 0 {
		if imp.isMatch(startStateID) {
			ms := imp.matches(startStateID)
			best := ms[0]
			bestLen := int(ac.patLens[best])
			for _, pid := range ms[1:] {
				if l := int(ac.patLens[pid]); l > bestLen {
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
			pid := ms[0]
			patLen := int(ac.patLens[pid])
			for _, p := range ms[1:] {
				if l := int(ac.patLens[p]); l > patLen {
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

// findLeftmostLongestNFA is an inlined NFA path that eliminates interface
// dispatch and enables register allocation of NFA fields.
func (ac *AhoCorasick) findLeftmostLongestNFA(nfa *NFA, haystack []byte, pos int) (Match, bool) {
	patLens := ac.patLens
	n := len(haystack)

	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	outputs := nfa.outputs
	outLen := nfa.outLen
	useAlpha := nfa.useAlpha

	if n == 0 {
		if states[startStateID].outputIdx >= 0 {
			oIdx := states[startStateID].outputIdx
			oLen := outLen[startStateID]
			ms := outputs[oIdx : int32(oIdx)+oLen]
			best := ms[0]
			bestLen := int(patLens[best])
			for _, pid := range ms[1:] {
				if l := int(patLens[pid]); l > bestLen {
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

		// ---- inlined nextState(state, b) ----
		if useAlpha {
			b = nfa.alphabet[b]
		}
		if state == startStateID {
			state = startTrans[b]
		} else if di := denseIdx[state]; di >= 0 {
			state = denseTrans[int(di)<<8|int(b)]
		} else {
			for {
				if state == deadStateID {
					break
				}
				tbase := int(transBase[state])
				tlen := int(transLen[state])
				tr := transBuf[tbase : tbase+tlen]
				found := false
				if tlen <= 8 {
					for i := 0; i < tlen; i++ {
						if tr[i].b == b {
							state = tr[i].next
							found = true
							break
						}
						if tr[i].b > b {
							break
						}
					}
				} else {
					lo, hi := 0, tlen
					for lo < hi {
						mid := int(uint(lo+hi) >> 1)
						if tr[mid].b < b {
							lo = mid + 1
						} else {
							hi = mid
						}
					}
					if lo < tlen && tr[lo].b == b {
						state = tr[lo].next
						found = true
					}
				}
				if found {
					break
				}
				fail := states[state].fail
				if fail == startStateID {
					state = startTrans[b]
					break
				}
				if di := denseIdx[fail]; di >= 0 {
					state = denseTrans[int(di)<<8|int(b)]
					break
				}
				state = fail
			}
		}
		// ---- end inlined nextState ----

		if state == deadStateID {
			if hasBest {
				return best, true
			}
			state = startStateID
			continue
		}

		if states[state].outputIdx >= 0 {
			oIdx := states[state].outputIdx
			oLen := outLen[state]
			ms := outputs[oIdx : int32(oIdx)+oLen]
			pid := ms[0]
			patLen := int(patLens[pid])
			for _, p := range ms[1:] {
				if l := int(patLens[p]); l > patLen {
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
	if ac.imp == nil {
		return nil
	}
	// Use specialized inlined loops to avoid per-match iterator overhead.
	if ac.matchKind == MatchKindStandard {
		if ac.dfa != nil {
			return ac.findAllStandardDFA(ac.dfa, haystack)
		}
		if ac.nfa != nil {
			return ac.findAllStandardNFA(ac.nfa, haystack)
		}
	}
	// Fallback: use iterator for leftmost semantics.
	out := make([]Match, 0, 16)
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

// findAllStandardDFA collects all Standard non-overlapping matches using the
// DFA in a single tight loop, avoiding per-match iterator dispatch overhead.
func (ac *AhoCorasick) findAllStandardDFA(dfa *DFA, haystack []byte) []Match {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)
	if n == 0 {
		// Check for empty-pattern match at start state.
		if dfa.outBase[startStateID] >= 0 {
			pid := dfa.outBuf[dfa.outBase[startStateID]]
			return []Match{{id: pid, start: 0, end: 0}}
		}
		return nil
	}

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	useAlpha := dfa.useAlpha

	out := make([]Match, 0, 16)
	state := startStateID
	pos := 0

	// Check for empty-pattern match at start.
	if outBase[state] >= 0 {
		pid := outBuf[outBase[state]]
		out = append(out, Match{id: pid, start: 0, end: 0})
		pos = 1
		state = startStateID
	}

	_ = haystack[n-1] // BCE hint

	for pos < n {
		if pf.enabled && state == startStateID {
			next := pf.next(haystack, pos)
			if next < 0 {
				break
			}
			pos = next
		}

		b := haystack[pos]
		pos++
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if outBase[state] >= 0 {
			pid := outBuf[outBase[state]]
			patLen := int(patLens[pid])
			out = append(out, Match{id: pid, start: pos - patLen, end: pos})
			// Non-overlapping: reset to start.
			state = startStateID
		}
	}

	return out
}

// findAllStandardNFA collects all Standard non-overlapping matches using the
// NFA in a single tight loop, avoiding per-match iterator dispatch overhead.
func (ac *AhoCorasick) findAllStandardNFA(nfa *NFA, haystack []byte) []Match {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)

	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	outputs := nfa.outputs
	useAlpha := nfa.useAlpha

	if n == 0 {
		if states[startStateID].outputIdx >= 0 {
			pid := outputs[states[startStateID].outputIdx]
			return []Match{{id: pid, start: 0, end: 0}}
		}
		return nil
	}

	out := make([]Match, 0, 16)
	state := startStateID
	pos := 0

	// Check for empty-pattern match at start.
	if states[state].outputIdx >= 0 {
		pid := outputs[states[state].outputIdx]
		out = append(out, Match{id: pid, start: 0, end: 0})
		pos = 1
		state = startStateID
	}

	_ = haystack[n-1]

	for pos < n {
		if pf.enabled && state == startStateID {
			next := pf.next(haystack, pos)
			if next < 0 {
				break
			}
			pos = next
		}

		b := haystack[pos]
		pos++

		// ---- inlined nextState ----
		if useAlpha {
			b = nfa.alphabet[b]
		}
		if state == startStateID {
			state = startTrans[b]
		} else if di := denseIdx[state]; di >= 0 {
			state = denseTrans[int(di)<<8|int(b)]
		} else {
			for {
				if state == deadStateID {
					break
				}
				tbase := int(transBase[state])
				tlen := int(transLen[state])
				tr := transBuf[tbase : tbase+tlen]
				found := false
				if tlen <= 8 {
					for i := 0; i < tlen; i++ {
						if tr[i].b == b {
							state = tr[i].next
							found = true
							break
						}
						if tr[i].b > b {
							break
						}
					}
				} else {
					lo, hi := 0, tlen
					for lo < hi {
						mid := int(uint(lo+hi) >> 1)
						if tr[mid].b < b {
							lo = mid + 1
						} else {
							hi = mid
						}
					}
					if lo < tlen && tr[lo].b == b {
						state = tr[lo].next
						found = true
					}
				}
				if found {
					break
				}
				fail := states[state].fail
				if fail == startStateID {
					state = startTrans[b]
					break
				}
				if di := denseIdx[fail]; di >= 0 {
					state = denseTrans[int(di)<<8|int(b)]
					break
				}
				state = fail
			}
		}
		// ---- end inlined nextState ----

		if states[state].outputIdx >= 0 {
			pid := outputs[states[state].outputIdx]
			patLen := int(patLens[pid])
			out = append(out, Match{id: pid, start: pos - patLen, end: pos})
			state = startStateID
		}
	}

	return out
}

// FindAllString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindAllString(haystack string) []Match {
	return ac.FindAll([]byte(haystack))
}

// ---------------------------------------------------------------------------
// FindOverlappingAll — collect all overlapping matches
// ---------------------------------------------------------------------------

// FindOverlappingAll returns all matches including overlapping ones.
// Only meaningful for MatchKindStandard.
func (ac *AhoCorasick) FindOverlappingAll(haystack []byte) []Match {
	if ac.imp == nil {
		return nil
	}
	if ac.dfa != nil {
		return ac.findOverlappingAllDFA(ac.dfa, haystack)
	}
	if ac.nfa != nil {
		return ac.findOverlappingAllNFA(ac.nfa, haystack)
	}
	// Fallback: use iterator.
	out := make([]Match, 0, 16)
	it := ac.FindOverlappingIter(haystack)
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

// FindOverlappingAllString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindOverlappingAllString(haystack string) []Match {
	return ac.FindOverlappingAll([]byte(haystack))
}

// findOverlappingAllDFA collects all overlapping matches using the DFA
// in a single tight loop, avoiding per-match iterator dispatch overhead.
func (ac *AhoCorasick) findOverlappingAllDFA(dfa *DFA, haystack []byte) []Match {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	outLen := dfa.outLen
	useAlpha := dfa.useAlpha

	out := make([]Match, 0, 16)
	state := startStateID

	// Check for empty-pattern match at start.
	if outBase[state] >= 0 {
		base := outBase[state]
		ol := outLen[state]
		for i := int32(0); i < ol; i++ {
			pid := outBuf[base+i]
			out = append(out, Match{id: pid, start: 0, end: 0})
		}
	}

	if n == 0 {
		return out
	}

	_ = haystack[n-1] // BCE hint

	for pos := 0; pos < n; pos++ {
		if pf.enabled && state == startStateID {
			next := pf.next(haystack, pos)
			if next < 0 {
				break
			}
			pos = next
		}

		b := haystack[pos]
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if outBase[state] >= 0 {
			base := outBase[state]
			ol := outLen[state]
			end := pos + 1
			for i := int32(0); i < ol; i++ {
				pid := outBuf[base+i]
				patLen := int(patLens[pid])
				out = append(out, Match{id: pid, start: end - patLen, end: end})
			}
		}
	}

	return out
}

// findOverlappingAllNFA collects all overlapping matches using the NFA
// in a single tight loop, avoiding per-match iterator dispatch overhead.
func (ac *AhoCorasick) findOverlappingAllNFA(nfa *NFA, haystack []byte) []Match {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)

	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	outputs := nfa.outputs
	outLen := nfa.outLen
	useAlpha := nfa.useAlpha

	out := make([]Match, 0, 16)
	state := startStateID

	// Check for empty-pattern match at start.
	if states[state].outputIdx >= 0 {
		obase := states[state].outputIdx
		ol := outLen[state]
		for i := int32(0); i < ol; i++ {
			pid := outputs[int32(obase)+i]
			out = append(out, Match{id: pid, start: 0, end: 0})
		}
	}

	if n == 0 {
		return out
	}

	_ = haystack[n-1]

	for pos := 0; pos < n; pos++ {
		if pf.enabled && state == startStateID {
			next := pf.next(haystack, pos)
			if next < 0 {
				break
			}
			pos = next
		}

		b := haystack[pos]

		// ---- inlined nextState ----
		if useAlpha {
			b = nfa.alphabet[b]
		}
		if state == startStateID {
			state = startTrans[b]
		} else if di := denseIdx[state]; di >= 0 {
			state = denseTrans[int(di)<<8|int(b)]
		} else {
			for {
				if state == deadStateID {
					break
				}
				tbase := int(transBase[state])
				tlen := int(transLen[state])
				tr := transBuf[tbase : tbase+tlen]
				found := false
				if tlen <= 8 {
					for i := 0; i < tlen; i++ {
						if tr[i].b == b {
							state = tr[i].next
							found = true
							break
						}
						if tr[i].b > b {
							break
						}
					}
				} else {
					lo, hi := 0, tlen
					for lo < hi {
						mid := int(uint(lo+hi) >> 1)
						if tr[mid].b < b {
							lo = mid + 1
						} else {
							hi = mid
						}
					}
					if lo < tlen && tr[lo].b == b {
						state = tr[lo].next
						found = true
					}
				}
				if found {
					break
				}
				fail := states[state].fail
				if fail == startStateID {
					state = startTrans[b]
					break
				}
				if di := denseIdx[fail]; di >= 0 {
					state = denseTrans[int(di)<<8|int(b)]
					break
				}
				state = fail
			}
		}
		// ---- end inlined nextState ----

		if states[state].outputIdx >= 0 {
			obase := states[state].outputIdx
			ol := outLen[state]
			end := pos + 1
			for i := int32(0); i < ol; i++ {
				pid := outputs[int32(obase)+i]
				patLen := int(patLens[pid])
				out = append(out, Match{id: pid, start: end - patLen, end: end})
			}
		}
	}

	return out
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
