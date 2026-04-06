package ahocorasick

// AhoCorasick is the primary type for multi-pattern string search.
// Build one with New or AhoCorasickBuilder.Build, then reuse it
// freely — it is safe for concurrent use after construction.
type AhoCorasick struct {
	imp       automaton // *nfa or *dfa
	nfa       *nfa      // non-nil when imp is *nfa; avoids repeated type assertions
	dfa       *dfa      // non-nil when imp is *dfa; avoids repeated type assertions
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

// PatternBytes returns the i-th pattern as a direct reference — no copy is made.
// The caller must not modify the returned slice; use Pattern() for a safe copy.
func (ac *AhoCorasick) PatternBytes(i PatternID) []byte {
	return ac.patterns[i]
}

// MatchKind returns the match semantics used by this automaton.
func (ac *AhoCorasick) MatchKind() MatchKind { return ac.matchKind }

// AutomatonKind returns the automaton representation kind (NFA or DFA).
func (ac *AhoCorasick) AutomatonKind() AhoCorasickKind { return ac.kind }

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
func (ac *AhoCorasick) findStandardDFA(dfa *dfa, haystack []byte, pos int, state stateID) (Match, bool) {
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
func (ac *AhoCorasick) findStandardNFA(nfa *nfa, haystack []byte, pos int, state stateID) (Match, bool) {
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
func (ac *AhoCorasick) findLeftmostFirstDFA(dfa *dfa, haystack []byte, pos int) (Match, bool) {
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
	if dfa, ok := ac.imp.(*dfa); ok {
		return ac.findLeftmostFirstDFA(dfa, haystack, pos)
	}
	if nfa, ok := ac.imp.(*nfa); ok {
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
func (ac *AhoCorasick) findLeftmostFirstNFA(nfa *nfa, haystack []byte, pos int) (Match, bool) {
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
func (ac *AhoCorasick) findLeftmostLongestDFA(dfa *dfa, haystack []byte, pos int) (Match, bool) {
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
	if dfa, ok := ac.imp.(*dfa); ok {
		return ac.findLeftmostLongestDFA(dfa, haystack, pos)
	}
	if nfa, ok := ac.imp.(*nfa); ok {
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
func (ac *AhoCorasick) findLeftmostLongestNFA(nfa *nfa, haystack []byte, pos int) (Match, bool) {
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
	return ac.FindAllAppend(make([]Match, 0, 16), haystack)
}

// FindAllAppend appends all non-overlapping matches to dst and returns the
// extended slice. This allows callers to reuse the match slice across calls,
// eliminating per-call allocation overhead in hot loops.
func (ac *AhoCorasick) FindAllAppend(dst []Match, haystack []byte) []Match {
	if ac.imp == nil {
		return dst[:0]
	}
	// Use specialized inlined loops to avoid per-match iterator overhead.
	if ac.matchKind == MatchKindStandard {
		if ac.dfa != nil {
			return ac.findAllStandardDFAAppend(ac.dfa, dst, haystack)
		}
		if ac.nfa != nil {
			return ac.findAllStandardNFAAppend(ac.nfa, dst, haystack)
		}
	}
	// Fallback: use iterator for leftmost semantics.
	out := dst[:0]
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

// FindAllAppendString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindAllAppendString(dst []Match, haystack string) []Match {
	return ac.FindAllAppend(dst, []byte(haystack))
}

// findAllStandardDFAAppend collects all Standard non-overlapping matches using
// the DFA in a single tight loop, appending to dst.
func (ac *AhoCorasick) findAllStandardDFAAppend(dfa *dfa, dst []Match, haystack []byte) []Match {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)
	out := dst[:0]

	if n == 0 {
		// Check for empty-pattern match at start state.
		if dfa.outBase[startStateID] >= 0 {
			pid := dfa.outBuf[dfa.outBase[startStateID]]
			return append(out, Match{id: pid, start: 0, end: 0})
		}
		return out
	}

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	useAlpha := dfa.useAlpha

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

// findAllStandardNFAAppend collects all Standard non-overlapping matches using
// the NFA in a single tight loop, appending to dst.
func (ac *AhoCorasick) findAllStandardNFAAppend(nfa *nfa, dst []Match, haystack []byte) []Match {
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

	out := dst[:0]

	if n == 0 {
		if states[startStateID].outputIdx >= 0 {
			pid := outputs[states[startStateID].outputIdx]
			return append(out, Match{id: pid, start: 0, end: 0})
		}
		return out
	}

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
	return ac.FindOverlappingAllAppend(make([]Match, 0, 16), haystack)
}

// FindOverlappingAllAppend appends all overlapping matches to dst and returns
// the extended slice. This allows callers to reuse the match slice across
// calls, eliminating per-call allocation overhead in hot loops.
// Only meaningful for MatchKindStandard.
func (ac *AhoCorasick) FindOverlappingAllAppend(dst []Match, haystack []byte) []Match {
	if ac.imp == nil {
		return dst[:0]
	}
	if ac.dfa != nil {
		return ac.findOverlappingAllDFAAppend(ac.dfa, dst, haystack)
	}
	if ac.nfa != nil {
		return ac.findOverlappingAllNFAAppend(ac.nfa, dst, haystack)
	}
	// Fallback: use iterator.
	out := dst[:0]
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

// FindOverlappingAllAppendString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) FindOverlappingAllAppendString(dst []Match, haystack string) []Match {
	return ac.FindOverlappingAllAppend(dst, []byte(haystack))
}

// findOverlappingAllDFAAppend collects all overlapping matches using the DFA
// in a single tight loop, appending to dst.
func (ac *AhoCorasick) findOverlappingAllDFAAppend(dfa *dfa, dst []Match, haystack []byte) []Match {
	pf := ac.pf
	patLens := ac.patLens
	n := len(haystack)

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	outLen := dfa.outLen
	useAlpha := dfa.useAlpha

	out := dst[:0]
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

// findOverlappingAllNFAAppend collects all overlapping matches using the NFA
// in a single tight loop, appending to dst.
func (ac *AhoCorasick) findOverlappingAllNFAAppend(nfa *nfa, dst []Match, haystack []byte) []Match {
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

	out := dst[:0]
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
// CountAll / CountOverlapping — zero-allocation match counting
// ---------------------------------------------------------------------------

// CountAll returns the number of non-overlapping matches without allocating
// a result slice. Only meaningful for MatchKindStandard.
func (ac *AhoCorasick) CountAll(haystack []byte) int {
	if ac.imp == nil {
		return 0
	}
	if ac.matchKind == MatchKindStandard {
		if ac.dfa != nil {
			return ac.countAllStandardDFA(ac.dfa, haystack)
		}
		if ac.nfa != nil {
			return ac.countAllStandardNFA(ac.nfa, haystack)
		}
	}
	// Fallback: use iterator.
	count := 0
	it := ac.FindIter(haystack)
	for {
		_, ok := it.Next()
		if !ok {
			break
		}
		count++
	}
	it.Close()
	return count
}

// CountAllString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) CountAllString(haystack string) int {
	return ac.CountAll([]byte(haystack))
}

// countAllStandardDFA counts non-overlapping matches using the DFA.
func (ac *AhoCorasick) countAllStandardDFA(dfa *dfa, haystack []byte) int {
	pf := ac.pf
	n := len(haystack)
	count := 0

	if n == 0 {
		if dfa.outBase[startStateID] >= 0 {
			return 1
		}
		return 0
	}

	trans := dfa.trans
	outBase := dfa.outBase
	useAlpha := dfa.useAlpha

	state := startStateID
	pos := 0

	if outBase[state] >= 0 {
		count++
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
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if outBase[state] >= 0 {
			count++
			state = startStateID
		}
	}

	return count
}

// countAllStandardNFA counts non-overlapping matches using the NFA.
func (ac *AhoCorasick) countAllStandardNFA(nfa *nfa, haystack []byte) int {
	pf := ac.pf
	n := len(haystack)

	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	useAlpha := nfa.useAlpha

	count := 0

	if n == 0 {
		if states[startStateID].outputIdx >= 0 {
			return 1
		}
		return 0
	}

	state := startStateID
	pos := 0

	if states[state].outputIdx >= 0 {
		count++
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
			count++
			state = startStateID
		}
	}

	return count
}

// CountOverlapping returns the total number of overlapping matches without
// allocating a result slice. Only meaningful for MatchKindStandard.
func (ac *AhoCorasick) CountOverlapping(haystack []byte) int {
	if ac.imp == nil {
		return 0
	}
	if ac.dfa != nil {
		return ac.countOverlappingDFA(ac.dfa, haystack)
	}
	if ac.nfa != nil {
		return ac.countOverlappingNFA(ac.nfa, haystack)
	}
	// Fallback: use iterator.
	count := 0
	it := ac.FindOverlappingIter(haystack)
	for {
		_, ok := it.Next()
		if !ok {
			break
		}
		count++
	}
	it.Close()
	return count
}

// CountOverlappingString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) CountOverlappingString(haystack string) int {
	return ac.CountOverlapping([]byte(haystack))
}

// countOverlappingDFA counts all overlapping matches using the DFA.
func (ac *AhoCorasick) countOverlappingDFA(dfa *dfa, haystack []byte) int {
	pf := ac.pf
	n := len(haystack)

	trans := dfa.trans
	outBase := dfa.outBase
	outLen := dfa.outLen
	useAlpha := dfa.useAlpha

	count := 0
	state := startStateID

	if outBase[state] >= 0 {
		count += int(outLen[state])
	}

	if n == 0 {
		return count
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
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if outBase[state] >= 0 {
			count += int(outLen[state])
		}
	}

	return count
}

// countOverlappingNFA counts all overlapping matches using the NFA.
func (ac *AhoCorasick) countOverlappingNFA(nfa *nfa, haystack []byte) int {
	pf := ac.pf
	n := len(haystack)

	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	outLen := nfa.outLen
	useAlpha := nfa.useAlpha

	count := 0
	state := startStateID

	if states[state].outputIdx >= 0 {
		count += int(outLen[state])
	}

	if n == 0 {
		return count
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
			count += int(outLen[state])
		}
	}

	return count
}

// ---------------------------------------------------------------------------
// OverlappingPatternSet / AllPatternSet — zero-allocation pattern set marking
// ---------------------------------------------------------------------------

// OverlappingPatternSet marks seen[patternID] = true for every overlapping
// match found in haystack. The caller must provide a seen slice with length
// >= ac.PatternCount(). Use clear(seen) to reset between calls.
// This is the most allocation-efficient way to determine which patterns
// matched, as it creates no Match structs and automatically deduplicates.
// Only meaningful for MatchKindStandard.
func (ac *AhoCorasick) OverlappingPatternSet(haystack []byte, seen []bool) {
	if ac.imp == nil {
		return
	}
	if ac.dfa != nil {
		ac.overlappingPatternSetDFA(ac.dfa, haystack, seen)
		return
	}
	if ac.nfa != nil {
		ac.overlappingPatternSetNFA(ac.nfa, haystack, seen)
		return
	}
	// Fallback: use iterator.
	it := ac.FindOverlappingIter(haystack)
	for {
		m, ok := it.Next()
		if !ok {
			break
		}
		seen[m.PatternID()] = true
	}
	it.Close()
}

// OverlappingPatternSetString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) OverlappingPatternSetString(haystack string, seen []bool) {
	ac.OverlappingPatternSet([]byte(haystack), seen)
}

// overlappingPatternSetDFA marks matched patterns using the DFA.
func (ac *AhoCorasick) overlappingPatternSetDFA(dfa *dfa, haystack []byte, seen []bool) {
	pf := ac.pf
	n := len(haystack)

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	outLen := dfa.outLen
	useAlpha := dfa.useAlpha

	state := startStateID

	// Check for empty-pattern match at start.
	if outBase[state] >= 0 {
		base := outBase[state]
		ol := outLen[state]
		for i := int32(0); i < ol; i++ {
			seen[outBuf[base+i]] = true
		}
	}

	if n == 0 {
		return
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
			for i := int32(0); i < ol; i++ {
				seen[outBuf[base+i]] = true
			}
		}
	}
}

// overlappingPatternSetNFA marks matched patterns using the NFA.
func (ac *AhoCorasick) overlappingPatternSetNFA(nfa *nfa, haystack []byte, seen []bool) {
	pf := ac.pf
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

	state := startStateID

	// Check for empty-pattern match at start.
	if states[state].outputIdx >= 0 {
		obase := states[state].outputIdx
		ol := outLen[state]
		for i := int32(0); i < ol; i++ {
			seen[outputs[int32(obase)+i]] = true
		}
	}

	if n == 0 {
		return
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
			for i := int32(0); i < ol; i++ {
				seen[outputs[int32(obase)+i]] = true
			}
		}
	}
}

// AllPatternSet marks seen[patternID] = true for every non-overlapping match
// found in haystack. The caller must provide a seen slice with length
// >= ac.PatternCount(). Only meaningful for MatchKindStandard.
func (ac *AhoCorasick) AllPatternSet(haystack []byte, seen []bool) {
	if ac.imp == nil {
		return
	}
	if ac.matchKind == MatchKindStandard {
		if ac.dfa != nil {
			ac.allPatternSetDFA(ac.dfa, haystack, seen)
			return
		}
		if ac.nfa != nil {
			ac.allPatternSetNFA(ac.nfa, haystack, seen)
			return
		}
	}
	// Fallback: use iterator.
	it := ac.FindIter(haystack)
	for {
		m, ok := it.Next()
		if !ok {
			break
		}
		seen[m.PatternID()] = true
	}
	it.Close()
}

// AllPatternSetString is a convenience wrapper for string haystacks.
func (ac *AhoCorasick) AllPatternSetString(haystack string, seen []bool) {
	ac.AllPatternSet([]byte(haystack), seen)
}

// allPatternSetDFA marks non-overlapping matched patterns using the DFA.
func (ac *AhoCorasick) allPatternSetDFA(dfa *dfa, haystack []byte, seen []bool) {
	pf := ac.pf
	n := len(haystack)

	if n == 0 {
		if dfa.outBase[startStateID] >= 0 {
			seen[dfa.outBuf[dfa.outBase[startStateID]]] = true
		}
		return
	}

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	useAlpha := dfa.useAlpha

	state := startStateID
	pos := 0

	if outBase[state] >= 0 {
		seen[outBuf[outBase[state]]] = true
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
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if outBase[state] >= 0 {
			seen[outBuf[outBase[state]]] = true
			state = startStateID
		}
	}
}

// allPatternSetNFA marks non-overlapping matched patterns using the NFA.
func (ac *AhoCorasick) allPatternSetNFA(nfa *nfa, haystack []byte, seen []bool) {
	pf := ac.pf
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
			seen[outputs[states[startStateID].outputIdx]] = true
		}
		return
	}

	state := startStateID
	pos := 0

	if states[state].outputIdx >= 0 {
		seen[outputs[states[state].outputIdx]] = true
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
			seen[outputs[states[state].outputIdx]] = true
			state = startStateID
		}
	}
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
