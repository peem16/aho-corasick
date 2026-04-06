package ahocorasick

import "sync"

// ---------------------------------------------------------------------------
// FindIter — non-overlapping match iterator
// ---------------------------------------------------------------------------
//
// FindIter implements a zero-allocation iterator over non-overlapping matches.
// Iterator state is pooled with sync.Pool to avoid heap pressure when callers
// create many short-lived iterators.
//
// Usage:
//
//	it := ac.FindIter(haystack)
//	for {
//	    m, ok := it.Next()
//	    if !ok { break }
//	    // use m
//	}
//	it.Close() // return to pool

var findIterPool = sync.Pool{
	New: func() any { return &FindIter{} },
}

// FindIter iterates over non-overlapping matches in order of their end
// position (left to right).
type FindIter struct {
	ac       *AhoCorasick
	nfa      *nfa    // cached NFA pointer; nil when using DFA
	dfa      *dfa    // cached DFA pointer; nil when using NFA
	haystack []byte
	pos      int
	state    stateID
	done     bool
}

// newFindIter acquires a FindIter from the pool and initialises it.
func newFindIter(ac *AhoCorasick, haystack []byte) *FindIter {
	it := findIterPool.Get().(*FindIter)
	it.ac = ac
	it.nfa, _ = ac.imp.(*nfa)
	it.dfa, _ = ac.imp.(*dfa)
	it.haystack = haystack
	it.pos = 0
	it.state = startStateID
	it.done = false
	return it
}

// Next advances the iterator and returns the next match.
// Returns (Match{}, false) when no more matches exist.
func (it *FindIter) Next() (Match, bool) {
	if it.done {
		return Match{}, false
	}
	if it.ac.matchKind == MatchKindStandard {
		if it.nfa != nil {
			return it.nextNFA()
		}
		if it.dfa != nil {
			return it.nextDFA()
		}
	}
	m, ok := it.ac.findFrom(it.haystack, it.pos, it.state)
	if !ok {
		it.done = true
		return Match{}, false
	}
	// Advance past this match so next call continues after it.
	it.pos = m.end
	if m.end == m.start {
		// Zero-length match: advance by one to avoid infinite loop.
		it.pos = m.end + 1
	}
	it.state = startStateID
	return m, true
}

// nextNFA is a specialized inlined NFA path for FindIter that eliminates
// function-call overhead and allows the compiler to register-allocate
// NFA slice headers. Same approach as FindOverlappingIter.nextNFA.
func (it *FindIter) nextNFA() (Match, bool) {
	nfa := it.nfa
	hay := it.haystack
	patLens := it.ac.patLens

	// Cache NFA slice fields as locals for register allocation.
	states := nfa.states
	transBuf := nfa.transBuf
	transBase := nfa.transBase
	transLen := nfa.transLen
	startTrans := &nfa.startTrans
	denseTrans := nfa.denseTrans
	denseIdx := nfa.denseIdx
	outputs := nfa.outputs
	useAlpha := nfa.useAlpha
	pf := it.ac.pf

	pos := it.pos
	state := it.state
	n := len(hay)

	// Check for a match at the current position before consuming any byte.
	// This handles empty patterns and resuming at a match state.
	if states[state].outputIdx >= 0 {
		pid := outputs[states[state].outputIdx]
		patLen := int(patLens[pid])
		start := pos - patLen
		if start < 0 {
			start = 0
		}
		m := Match{id: pid, start: start, end: pos}
		// Advance past this match.
		it.pos = m.end
		if m.end == m.start {
			it.pos = m.end + 1
		}
		it.state = startStateID
		return m, true
	}

	if n == 0 || pos >= n {
		it.done = true
		return Match{}, false
	}

	// BCE hint.
	_ = hay[n-1]

	for pos < n {
		// Prefilter: skip ahead when at start state.
		if pf.enabled && state == startStateID {
			next := pf.next(hay, pos)
			if next < 0 {
				it.pos = n
				it.state = startStateID
				it.done = true
				return Match{}, false
			}
			pos = next
		}

		b := hay[pos]
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

		if states[state].outputIdx >= 0 {
			pid := outputs[states[state].outputIdx]
			patLen := int(patLens[pid])
			m := Match{id: pid, start: pos - patLen, end: pos}
			// Advance past this match.
			it.pos = m.end
			if m.end == m.start {
				it.pos = m.end + 1
			}
			it.state = startStateID
			return m, true
		}
	}

	it.pos = pos
	it.state = state
	it.done = true
	return Match{}, false
}

// nextDFA is a specialized inlined DFA path for FindIter that eliminates
// interface dispatch. The DFA transition table gives O(1) per-byte lookup.
func (it *FindIter) nextDFA() (Match, bool) {
	dfa := it.dfa
	hay := it.haystack
	patLens := it.ac.patLens
	pf := it.ac.pf

	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf

	pos := it.pos
	state := it.state
	n := len(hay)

	// Check for a match at the current position.
	if outBase[state] >= 0 {
		pid := outBuf[outBase[state]]
		patLen := int(patLens[pid])
		start := pos - patLen
		if start < 0 {
			start = 0
		}
		m := Match{id: pid, start: start, end: pos}
		it.pos = m.end
		if m.end == m.start {
			it.pos = m.end + 1
		}
		it.state = startStateID
		return m, true
	}

	if n == 0 || pos >= n {
		it.done = true
		return Match{}, false
	}

	_ = hay[n-1] // BCE hint

	for pos < n {
		if pf.enabled && state == startStateID {
			next := pf.next(hay, pos)
			if next < 0 {
				it.pos = n
				it.state = startStateID
				it.done = true
				return Match{}, false
			}
			pos = next
		}

		b := hay[pos]
		pos++
		state = trans[int(state)<<8|int(b)]

		if outBase[state] >= 0 {
			pid := outBuf[outBase[state]]
			patLen := int(patLens[pid])
			m := Match{id: pid, start: pos - patLen, end: pos}
			it.pos = m.end
			if m.end == m.start {
				it.pos = m.end + 1
			}
			it.state = startStateID
			return m, true
		}
	}

	it.pos = pos
	it.state = state
	it.done = true
	return Match{}, false
}

// Close returns the iterator to the pool.  The caller must not use the
// iterator after calling Close.
func (it *FindIter) Close() {
	it.ac = nil
	it.nfa = nil
	it.dfa = nil
	it.haystack = nil
	findIterPool.Put(it)
}

// ---------------------------------------------------------------------------
// FindOverlappingIter — overlapping match iterator (Standard mode only)
// ---------------------------------------------------------------------------

var findOverlappingIterPool = sync.Pool{
	New: func() any { return &FindOverlappingIter{} },
}

// FindOverlappingIter iterates over all matches including overlapping ones.
// This is only meaningful when the automaton was built with MatchKindStandard.
// For Leftmost* semantics it behaves identically to FindIter.
type FindOverlappingIter struct {
	ac       *AhoCorasick
	nfa      *nfa // cached NFA pointer; nil when using DFA
	dfa      *dfa // cached DFA pointer; nil when using NFA
	haystack []byte
	pos      int     // current byte position in haystack
	state    stateID // current automaton state
	matchIdx int     // index into current state's match list
	done     bool
}

// newFindOverlappingIter acquires a FindOverlappingIter from the pool.
func newFindOverlappingIter(ac *AhoCorasick, haystack []byte) *FindOverlappingIter {
	it := findOverlappingIterPool.Get().(*FindOverlappingIter)
	it.ac = ac
	it.nfa = ac.nfa // use cached pointer; nil if DFA
	it.dfa = ac.dfa // use cached pointer; nil if NFA
	it.haystack = haystack
	it.pos = 0
	it.state = startStateID
	it.matchIdx = 0
	it.done = false
	return it
}

// Next returns the next overlapping match.
// Returns (Match{}, false) when exhausted.
func (it *FindOverlappingIter) Next() (Match, bool) {
	if it.done {
		return Match{}, false
	}
	if it.nfa != nil {
		return it.nextNFA()
	}
	if it.dfa != nil {
		return it.nextDFA()
	}
	return it.nextGeneric()
}

// nextNFA is the specialized hot path for NFA overlapping iteration.
// All NFA field accesses and the nextState/lookup logic are fully inlined
// to eliminate function-call overhead and allow the compiler to keep
// slice headers and hot variables in registers.
func (it *FindOverlappingIter) nextNFA() (Match, bool) {
	nfa := it.nfa
	hay := it.haystack
	patLens := it.ac.patLens

	// Cache NFA slice fields as locals so the compiler can register-allocate them.
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
	pf := it.ac.pf

	// Drain remaining matches from the current state.
	if it.matchIdx > 0 {
		st := &states[it.state]
		if st.outputIdx >= 0 {
			obase := int32(st.outputIdx)
			olen := outLen[it.state]
			if int32(it.matchIdx) < olen {
				pid := outputs[obase+int32(it.matchIdx)]
				m := Match{
					id:    pid,
					start: it.pos - int(patLens[pid]),
					end:   it.pos,
				}
				it.matchIdx++
				return m, true
			}
		}
		it.matchIdx = 0
	}

	pos := it.pos
	state := it.state
	n := len(hay)
	if pos >= n {
		it.done = true
		return Match{}, false
	}

	// BCE hint: tells the compiler hay[n-1] is in bounds.
	_ = hay[n-1]

	for pos < n {
		// Prefilter: skip ahead when at start state.
		if pf.enabled && state == startStateID {
			next := pf.next(hay, pos)
			if next < 0 {
				it.pos = n
				it.state = startStateID
				it.done = true
				return Match{}, false
			}
			pos = next
		}

		b := hay[pos]
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
				// Inlined lookup in flattened transition buffer.
				tbase := int(transBase[state])
				tlen := int(transLen[state])
				tr := transBuf[tbase : tbase+tlen]
				found := false
				if tlen <= 8 {
					// Linear scan for small transition counts.
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
				// Failure link: if it points to start, use dense table.
				fail := states[state].fail
				if fail == startStateID {
					state = startTrans[b]
					break
				}
				// Check if failure state has a dense table — O(1) instead
				// of continuing binary search.
				if di := denseIdx[fail]; di >= 0 {
					state = denseTrans[int(di)<<8|int(b)]
					break
				}
				state = fail
			}
		}
		// ---- end inlined nextState ----

		if states[state].outputIdx >= 0 {
			obase := int32(states[state].outputIdx)
			pid := outputs[obase]
			m := Match{
				id:    pid,
				start: pos - int(patLens[pid]),
				end:   pos,
			}
			it.pos = pos
			it.state = state
			it.matchIdx = 1
			return m, true
		}
	}

	it.pos = pos
	it.state = state
	it.done = true
	return Match{}, false
}

// nextDFA is the specialized hot path for DFA overlapping iteration.
// All DFA field accesses are inlined to eliminate interface dispatch and
// allow the compiler to keep the transition table pointer in a register.
func (it *FindOverlappingIter) nextDFA() (Match, bool) {
	dfa := it.dfa
	hay := it.haystack
	patLens := it.ac.patLens
	pf := it.ac.pf

	// Cache DFA fields as locals for register allocation.
	trans := dfa.trans
	outBase := dfa.outBase
	outBuf := dfa.outBuf
	outLen := dfa.outLen
	useAlpha := dfa.useAlpha

	// Drain remaining matches from the current state.
	if it.matchIdx > 0 {
		base := outBase[it.state]
		if base >= 0 {
			olen := outLen[it.state]
			if int32(it.matchIdx) < olen {
				pid := outBuf[base+int32(it.matchIdx)]
				m := Match{
					id:    pid,
					start: it.pos - int(patLens[pid]),
					end:   it.pos,
				}
				it.matchIdx++
				return m, true
			}
		}
		it.matchIdx = 0
	}

	pos := it.pos
	state := it.state
	n := len(hay)
	if pos >= n {
		it.done = true
		return Match{}, false
	}

	// BCE hint.
	_ = hay[n-1]

	for pos < n {
		// Prefilter: skip ahead when at start state.
		if pf.enabled && state == startStateID {
			next := pf.next(hay, pos)
			if next < 0 {
				it.pos = n
				it.state = startStateID
				it.done = true
				return Match{}, false
			}
			pos = next
		}

		b := hay[pos]
		pos++
		if useAlpha {
			b = dfa.alphabet[b]
		}
		state = trans[int(state)<<8|int(b)]

		if base := outBase[state]; base >= 0 {
			pid := outBuf[base]
			m := Match{
				id:    pid,
				start: pos - int(patLens[pid]),
				end:   pos,
			}
			it.pos = pos
			it.state = state
			it.matchIdx = 1
			return m, true
		}
	}

	it.pos = pos
	it.state = state
	it.done = true
	return Match{}, false
}

// nextGeneric is the fallback path using the automaton interface.
func (it *FindOverlappingIter) nextGeneric() (Match, bool) {
	imp := it.ac.imp
	hay := it.haystack

	// First drain any remaining matches from the current state.
	if it.matchIdx > 0 {
		ms := imp.matches(it.state)
		if it.matchIdx < len(ms) {
			m := Match{
				id:    ms[it.matchIdx],
				start: it.pos - patternLen(it.ac, ms[it.matchIdx]),
				end:   it.pos,
			}
			it.matchIdx++
			return m, true
		}
		it.matchIdx = 0
	}

	for it.pos < len(hay) {
		b := hay[it.pos]
		it.pos++
		it.state = imp.nextState(it.state, b)

		if imp.isMatch(it.state) {
			ms := imp.matches(it.state)
			if len(ms) > 0 {
				m := Match{
					id:    ms[0],
					start: it.pos - patternLen(it.ac, ms[0]),
					end:   it.pos,
				}
				it.matchIdx = 1
				return m, true
			}
		}
	}

	it.done = true
	return Match{}, false
}

// Close returns the iterator to the pool.
func (it *FindOverlappingIter) Close() {
	it.ac = nil
	it.nfa = nil
	it.dfa = nil
	it.haystack = nil
	findOverlappingIterPool.Put(it)
}

// patternLen returns the byte length of pattern pid in ac.
// Uses the cached int32 array to avoid slice header indirection.
func patternLen(ac *AhoCorasick, pid PatternID) int {
	return int(ac.patLens[pid])
}
