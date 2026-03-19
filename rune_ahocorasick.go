package ahocorasick

import "sort"

// ---------------------------------------------------------------------------
// Rune-based Aho-Corasick NFA
// ---------------------------------------------------------------------------

// runeNFATrans is a single (rune → state) transition entry.
// Kept sorted by r so we can binary-search in the hot path.
type runeNFATrans struct {
	r    rune
	next stateID
}

// RuneMatch represents a single match found in a rune haystack.
type RuneMatch struct {
	id    PatternID
	start int // rune offset (inclusive)
	end   int // rune offset (exclusive)
}

// PatternID returns the index of the pattern that matched.
func (m RuneMatch) PatternID() PatternID { return m.id }

// Start returns the starting rune offset of this match (inclusive).
func (m RuneMatch) Start() int { return m.start }

// End returns the ending rune offset of this match (exclusive).
func (m RuneMatch) End() int { return m.end }

// RuneAhoCorasick is an Aho-Corasick automaton that operates on rune
// (Unicode code point) sequences instead of byte sequences.
//
// This is beneficial for scripts like Thai, CJK, etc. where each character
// is 3+ bytes in UTF-8. A byte-based automaton traverses 3x more transitions
// per character compared to a rune-based one.
//
// Only NFA mode is supported (no DFA conversion). Transitions use sorted
// binary search — no dense tables since the rune space is too large for
// 256-entry tables.
type RuneAhoCorasick struct {
	states    []nfaState      // reuse existing (fail + outputIdx)
	transBuf  []runeNFATrans  // all transitions concatenated
	transBase []int32         // per-state offset into transBuf
	transLen  []int32         // per-state transition count
	outputs   []PatternID     // all output pattern IDs, concatenated
	outLen    []int32         // per-state output count
	patterns  [][]rune        // deep copy of original patterns
	patLens   []int32         // cached pattern lengths
	patCount  int
}

// NewRune builds a rune-based Aho-Corasick automaton from rune patterns.
// Uses Standard (overlapping) match semantics only.
func NewRune(patterns [][]rune) (*RuneAhoCorasick, error) {
	if len(patterns) == 0 {
		return &RuneAhoCorasick{}, nil
	}
	return buildRuneNFA(patterns), nil
}

// PatternCount returns the number of patterns in the automaton.
func (ra *RuneAhoCorasick) PatternCount() int {
	if ra == nil {
		return 0
	}
	return ra.patCount
}

// Pattern returns the i-th pattern (a copy — safe to modify).
func (ra *RuneAhoCorasick) Pattern(id PatternID) []rune {
	cp := make([]rune, len(ra.patterns[id]))
	copy(cp, ra.patterns[id])
	return cp
}

// PatternRunes returns the i-th pattern without copying.
// The caller must not modify the returned slice.
func (ra *RuneAhoCorasick) PatternRunes(id PatternID) []rune {
	return ra.patterns[id]
}

// ---------------------------------------------------------------------------
// Builder
// ---------------------------------------------------------------------------

func buildRuneNFA(patterns [][]rune) *RuneAhoCorasick {
	ra := &RuneAhoCorasick{}

	// Temporary per-state transitions (will be flattened later).
	tmpTrans := make([][]runeNFATrans, 2)

	// ---- Phase 1: build trie ----
	ra.states = make([]nfaState, 2)
	ra.states[0].outputIdx = -1 // dead state
	ra.states[1].outputIdx = -1 // start state

	tmpOutputs := make([][]PatternID, 2)

	// Slab allocator for 1-entry transition slots.
	slabSize := 2
	for _, p := range patterns {
		slabSize += len(p)
	}
	transSlab := make([]runeNFATrans, slabSize)
	slabIdx := 2

	for pid, pat := range patterns {
		if len(pat) == 0 {
			tmpOutputs[startStateID] = append(tmpOutputs[startStateID], PatternID(pid))
			continue
		}
		cur := startStateID
		for _, r := range pat {
			next, ok := runeLookupTmp(tmpTrans[cur], r)
			if !ok {
				newID := stateID(len(ra.states))
				ra.states = append(ra.states, nfaState{outputIdx: -1})
				if slabIdx < len(transSlab) {
					tmpTrans = append(tmpTrans, transSlab[slabIdx:slabIdx:slabIdx+1])
					slabIdx++
				} else {
					tmpTrans = append(tmpTrans, nil)
				}
				tmpOutputs = append(tmpOutputs, nil)
				tmpTrans[cur] = runeAddTransTmp(tmpTrans[cur], r, newID)
				next = newID
			}
			cur = next
		}
		tmpOutputs[cur] = append(tmpOutputs[cur], PatternID(pid))
	}

	// ---- Phase 2: failure links (BFS from depth 1) ----
	queue := make([]stateID, 0, len(ra.states))

	for _, tr := range tmpTrans[startStateID] {
		child := tr.next
		ra.states[child].fail = startStateID
		queue = append(queue, child)
		if len(tmpOutputs[startStateID]) > 0 {
			tmpOutputs[child] = append(tmpOutputs[child], tmpOutputs[startStateID]...)
		}
	}

	for qi := 0; qi < len(queue); qi++ {
		cur := queue[qi]
		for _, tr := range tmpTrans[cur] {
			r := tr.r
			child := tr.next

			fail := ra.states[cur].fail
			for fail != startStateID {
				if _, ok := runeLookupTmp(tmpTrans[fail], r); ok {
					break
				}
				fail = ra.states[fail].fail
			}
			if next, ok := runeLookupTmp(tmpTrans[fail], r); ok && next != child {
				ra.states[child].fail = next
			} else {
				ra.states[child].fail = startStateID
			}

			failState := ra.states[child].fail
			if len(tmpOutputs[failState]) > 0 {
				tmpOutputs[child] = append(tmpOutputs[child], tmpOutputs[failState]...)
			}

			queue = append(queue, child)
		}
	}

	// ---- Phase 3: flatten transitions ----
	ra.flattenRuneTransitions(tmpTrans)

	// ---- Phase 4: flatten outputs ----
	ra.flattenRuneOutputs(tmpOutputs)

	// ---- Phase 5: deep copy patterns & cache lengths ----
	ra.patCount = len(patterns)
	ra.patterns = make([][]rune, len(patterns))
	ra.patLens = make([]int32, len(patterns))
	for i, p := range patterns {
		cp := make([]rune, len(p))
		copy(cp, p)
		ra.patterns[i] = cp
		ra.patLens[i] = int32(len(p))
	}

	return ra
}

// ---------------------------------------------------------------------------
// Builder helpers
// ---------------------------------------------------------------------------

func runeLookupTmp(tr []runeNFATrans, r rune) (stateID, bool) {
	lo, hi := 0, len(tr)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if tr[mid].r < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(tr) && tr[lo].r == r {
		return tr[lo].next, true
	}
	return 0, false
}

func runeAddTransTmp(tr []runeNFATrans, r rune, next stateID) []runeNFATrans {
	lo, hi := 0, len(tr)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if tr[mid].r < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	tr = append(tr, runeNFATrans{})
	copy(tr[lo+1:], tr[lo:])
	tr[lo] = runeNFATrans{r: r, next: next}
	return tr
}

func (ra *RuneAhoCorasick) flattenRuneTransitions(tmpTrans [][]runeNFATrans) {
	numStates := len(ra.states)
	ra.transBase = make([]int32, numStates)
	ra.transLen = make([]int32, numStates)

	total := 0
	for _, tr := range tmpTrans {
		total += len(tr)
	}
	ra.transBuf = make([]runeNFATrans, 0, total)

	for s := 0; s < numStates; s++ {
		tr := tmpTrans[s]
		ra.transBase[s] = int32(len(ra.transBuf))
		ra.transLen[s] = int32(len(tr))
		ra.transBuf = append(ra.transBuf, tr...)
	}
}

func (ra *RuneAhoCorasick) flattenRuneOutputs(tmp [][]PatternID) {
	numStates := len(ra.states)
	ra.outLen = make([]int32, numStates)

	total := 0
	for _, outs := range tmp {
		total += len(outs)
	}
	ra.outputs = make([]PatternID, 0, total)

	for s := 0; s < numStates; s++ {
		outs := tmp[s]
		if len(outs) == 0 {
			ra.states[s].outputIdx = -1
			continue
		}
		if len(outs) <= 8 {
			for i := 1; i < len(outs); i++ {
				key := outs[i]
				j := i - 1
				for j >= 0 && outs[j] > key {
					outs[j+1] = outs[j]
					j--
				}
				outs[j+1] = key
			}
		} else {
			sort.Slice(outs, func(i, j int) bool { return outs[i] < outs[j] })
		}
		ra.states[s].outputIdx = int32(len(ra.outputs))
		ra.outLen[s] = int32(len(outs))
		ra.outputs = append(ra.outputs, outs...)
	}
}

// ---------------------------------------------------------------------------
// Search: OverlappingPatternSet (hot path for per-campaign matching)
// ---------------------------------------------------------------------------

// OverlappingPatternSet sets seen[pid] = true for every pattern that appears
// in haystack. The seen slice must have length >= PatternCount().
// This is the zero-allocation hot path for per-campaign keyword matching.
func (ra *RuneAhoCorasick) OverlappingPatternSet(haystack []rune, seen []bool) {
	if ra == nil || len(ra.states) == 0 {
		return
	}

	states := ra.states
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen
	outputs := ra.outputs
	outLens := ra.outLen
	n := len(haystack)

	state := startStateID

	// Check for empty-pattern match at start.
	if states[state].outputIdx >= 0 {
		obase := states[state].outputIdx
		ol := outLens[state]
		for i := int32(0); i < ol; i++ {
			seen[outputs[obase+i]] = true
		}
	}

	if n == 0 {
		return
	}

	_ = haystack[n-1] // BCE hint

	// Cache start state transition slice for quick access.
	startBase := int(transBase[startStateID])
	startLen := int(transLen[startStateID])

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

		// Inlined nextState: follow transitions or failure links.
		for {
			if state == deadStateID {
				break
			}

			tbase := int(transBase[state])
			tlen := int(transLen[state])
			tr := transBuf[tbase : tbase+tlen]

			found := false
			if tlen <= 8 {
				// Linear scan for small transition sets.
				for i := 0; i < tlen; i++ {
					if tr[i].r == r {
						state = tr[i].next
						found = true
						break
					}
					if tr[i].r > r {
						break
					}
				}
			} else {
				// Binary search for larger transition sets.
				lo, hi := 0, tlen
				for lo < hi {
					mid := int(uint(lo+hi) >> 1)
					if tr[mid].r < r {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if lo < tlen && tr[lo].r == r {
					state = tr[lo].next
					found = true
				}
			}

			if found {
				break
			}

			// At start state with no match → stay at start.
			if state == startStateID {
				break
			}

			fail := states[state].fail
			if fail == startStateID {
				// Check start state transitions.
				str := transBuf[startBase : startBase+startLen]
				state = startStateID
				lo, hi := 0, startLen
				for lo < hi {
					mid := int(uint(lo+hi) >> 1)
					if str[mid].r < r {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if lo < startLen && str[lo].r == r {
					state = str[lo].next
				}
				break
			}
			state = fail
		}

		// Drain output chain.
		if states[state].outputIdx >= 0 {
			obase := states[state].outputIdx
			ol := outLens[state]
			for i := int32(0); i < ol; i++ {
				seen[outputs[obase+i]] = true
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Search: FindOverlappingAll
// ---------------------------------------------------------------------------

// FindOverlappingAll returns all overlapping matches in haystack with
// rune-based start/end positions.
func (ra *RuneAhoCorasick) FindOverlappingAll(haystack []rune) []RuneMatch {
	return ra.FindOverlappingAllAppend(nil, haystack)
}

// FindOverlappingAllAppend appends all overlapping matches to dst and returns it.
// Pass dst[:0] to reuse an existing buffer.
func (ra *RuneAhoCorasick) FindOverlappingAllAppend(dst []RuneMatch, haystack []rune) []RuneMatch {
	if ra == nil || len(ra.states) == 0 {
		return dst
	}

	states := ra.states
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen
	outputs := ra.outputs
	outLens := ra.outLen
	patLens := ra.patLens
	n := len(haystack)
	out := dst

	state := startStateID

	// Check for empty-pattern match at start.
	if states[state].outputIdx >= 0 {
		obase := states[state].outputIdx
		ol := outLens[state]
		for i := int32(0); i < ol; i++ {
			pid := outputs[obase+i]
			out = append(out, RuneMatch{id: pid, start: 0, end: 0})
		}
	}

	if n == 0 {
		return out
	}

	_ = haystack[n-1]

	// Cache start state transition slice for quick access.
	startBase := int(transBase[startStateID])
	startLen := int(transLen[startStateID])

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

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
					if tr[i].r == r {
						state = tr[i].next
						found = true
						break
					}
					if tr[i].r > r {
						break
					}
				}
			} else {
				lo, hi := 0, tlen
				for lo < hi {
					mid := int(uint(lo+hi) >> 1)
					if tr[mid].r < r {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if lo < tlen && tr[lo].r == r {
					state = tr[lo].next
					found = true
				}
			}

			if found {
				break
			}

			if state == startStateID {
				break
			}

			fail := states[state].fail
			if fail == startStateID {
				str := transBuf[startBase : startBase+startLen]
				state = startStateID
				lo, hi := 0, startLen
				for lo < hi {
					mid := int(uint(lo+hi) >> 1)
					if str[mid].r < r {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if lo < startLen && str[lo].r == r {
					state = str[lo].next
				}
				break
			}
			state = fail
		}

		if states[state].outputIdx >= 0 {
			obase := states[state].outputIdx
			ol := outLens[state]
			end := pos + 1
			for i := int32(0); i < ol; i++ {
				pid := outputs[obase+i]
				start := end - int(patLens[pid])
				out = append(out, RuneMatch{id: pid, start: start, end: end})
			}
		}
	}

	return out
}

// ---------------------------------------------------------------------------
// Search: IsMatch
// ---------------------------------------------------------------------------

// IsMatch reports whether haystack contains at least one match.
func (ra *RuneAhoCorasick) IsMatch(haystack []rune) bool {
	if ra == nil || len(ra.states) == 0 {
		return false
	}

	states := ra.states
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen
	n := len(haystack)

	state := startStateID

	if states[state].outputIdx >= 0 {
		return true
	}

	if n == 0 {
		return false
	}

	_ = haystack[n-1]

	startBase := int(transBase[startStateID])
	startLen := int(transLen[startStateID])

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

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
					if tr[i].r == r {
						state = tr[i].next
						found = true
						break
					}
					if tr[i].r > r {
						break
					}
				}
			} else {
				lo, hi := 0, tlen
				for lo < hi {
					mid := int(uint(lo+hi) >> 1)
					if tr[mid].r < r {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if lo < tlen && tr[lo].r == r {
					state = tr[lo].next
					found = true
				}
			}

			if found {
				break
			}

			if state == startStateID {
				break
			}

			fail := states[state].fail
			if fail == startStateID {
				str := transBuf[startBase : startBase+startLen]
				state = startStateID
				lo, hi := 0, startLen
				for lo < hi {
					mid := int(uint(lo+hi) >> 1)
					if str[mid].r < r {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if lo < startLen && str[lo].r == r {
					state = str[lo].next
				}
				break
			}
			state = fail
		}

		if states[state].outputIdx >= 0 {
			return true
		}
	}

	return false
}
