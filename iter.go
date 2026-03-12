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
	haystack []byte
	pos      int
	state    stateID
	done     bool
}

// newFindIter acquires a FindIter from the pool and initialises it.
func newFindIter(ac *AhoCorasick, haystack []byte) *FindIter {
	it := findIterPool.Get().(*FindIter)
	it.ac = ac
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

// Close returns the iterator to the pool.  The caller must not use the
// iterator after calling Close.
func (it *FindIter) Close() {
	it.ac = nil
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
	ac         *AhoCorasick
	haystack   []byte
	pos        int     // current byte position in haystack
	state      stateID // current automaton state
	matchIdx   int     // index into current state's match list
	done       bool
}

// newFindOverlappingIter acquires a FindOverlappingIter from the pool.
func newFindOverlappingIter(ac *AhoCorasick, haystack []byte) *FindOverlappingIter {
	it := findOverlappingIterPool.Get().(*FindOverlappingIter)
	it.ac = ac
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

	// Fast path: dispatch directly to NFA to avoid interface overhead.
	if nfa, ok := it.ac.imp.(*NFA); ok {
		return it.nextNFA(nfa)
	}
	return it.nextGeneric()
}

// nextNFA is the specialized hot path for NFA overlapping iteration.
// It avoids interface dispatch and accesses NFA fields directly.
func (it *FindOverlappingIter) nextNFA(nfa *NFA) (Match, bool) {
	hay := it.haystack
	patLens := it.ac.patLens

	// Drain remaining matches from the current state.
	if it.matchIdx > 0 {
		st := &nfa.states[it.state]
		if st.outputIdx >= 0 {
			base := int32(st.outputIdx)
			length := nfa.outLen[it.state]
			if int32(it.matchIdx) < length {
				pid := nfa.outputs[base+int32(it.matchIdx)]
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

	for pos < n {
		b := hay[pos]
		pos++
		state = nfa.nextState(state, b)

		if nfa.states[state].outputIdx >= 0 {
			base := int32(nfa.states[state].outputIdx)
			pid := nfa.outputs[base]
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
	it.haystack = nil
	findOverlappingIterPool.Put(it)
}

// patternLen returns the byte length of pattern pid in ac.
// Uses the cached int32 array to avoid slice header indirection.
func patternLen(ac *AhoCorasick, pid PatternID) int {
	return int(ac.patLens[pid])
}
