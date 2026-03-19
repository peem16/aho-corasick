package ahocorasick

import "sort"

// ---------------------------------------------------------------------------
// Rune-based Aho-Corasick NFA
// ---------------------------------------------------------------------------

// runeNFATrans is a single (rune → state) transition entry.
// Kept sorted by r so we can binary-search in the sparse fallback path.
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
// Uses an NFA with a compact rune alphabet and precomputed dense transition
// tables for the start state and shallow states (depth ≤ denseDepth).
// This eliminates binary search and failure-link traversal for the most
// frequently visited states, achieving O(1) per-rune transitions in the
// common case.
type RuneAhoCorasick struct {
	states    []nfaState     // reuse existing (fail + outputIdx)
	transBuf  []runeNFATrans // all transitions concatenated (sparse fallback)
	transBase []int32        // per-state offset into transBuf
	transLen  []int32        // per-state transition count
	outputs   []PatternID    // all output pattern IDs, concatenated
	outLen    []int32        // per-state output count
	patterns  [][]rune       // deep copy of original patterns
	patLens   []int32        // cached pattern lengths
	patCount  int

	// Compact rune alphabet: maps runes in [minRune, maxRune] to
	// 1-based indices (0 = rune not in any pattern).
	runeTable []uint16
	minRune   rune
	maxRune   rune
	alphaSize int // number of distinct runes + 1 (0 reserved for "not in alphabet")

	// Dense transition tables (precomputed including failure links).
	startDense []stateID // length = alphaSize; startDense[alpha] = next state from start
	denseTrans []stateID // concatenated dense tables for shallow states
	denseIdx   []int32   // per-state index into denseTrans; -1 = use sparse lookup
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

const runeDefaultDenseDepth = 5

func buildRuneNFA(patterns [][]rune) *RuneAhoCorasick {
	ra := &RuneAhoCorasick{}

	// Temporary per-state transitions (will be flattened later).
	tmpTrans := make([][]runeNFATrans, 2)

	// Track trie depth per state during construction for dense table building.
	tmpDepths := make([]uint16, 2)

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
		for depth, r := range pat {
			next, ok := runeLookupTmp(tmpTrans[cur], r)
			if !ok {
				newID := stateID(len(ra.states))
				ra.states = append(ra.states, nfaState{outputIdx: -1})
				tmpDepths = append(tmpDepths, uint16(depth+1))
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

	// ---- Phase 5: build compact rune alphabet ----
	ra.buildRuneAlphabet(patterns)

	// ---- Phase 6: build start state dense table ----
	ra.buildStartDense()

	// ---- Phase 7: build dense tables for shallow states ----
	ra.buildRuneDenseTrans(runeDefaultDenseDepth, tmpDepths)

	// ---- Phase 8: deep copy patterns & cache lengths ----
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

// buildRuneAlphabet collects all unique runes from patterns and builds a
// compact mapping from rune to 1-based index. Index 0 means "rune not in
// any pattern" and allows O(1) short-circuit to start state.
func (ra *RuneAhoCorasick) buildRuneAlphabet(patterns [][]rune) {
	seen := make(map[rune]bool)
	for _, pat := range patterns {
		for _, r := range pat {
			seen[r] = true
		}
	}
	if len(seen) == 0 {
		ra.alphaSize = 1
		return
	}

	// Find min/max rune range.
	first := true
	for r := range seen {
		if first || r < ra.minRune {
			ra.minRune = r
		}
		if first || r > ra.maxRune {
			ra.maxRune = r
		}
		first = false
	}

	rangeSize := int(ra.maxRune-ra.minRune) + 1
	ra.runeTable = make([]uint16, rangeSize)
	idx := uint16(1) // 0 = not in alphabet
	// Assign indices in sorted order for determinism.
	sorted := make([]rune, 0, len(seen))
	for r := range seen {
		sorted = append(sorted, r)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, r := range sorted {
		ra.runeTable[r-ra.minRune] = idx
		idx++
	}
	ra.alphaSize = int(idx) // 0..idx-1
}

// buildStartDense precomputes the dense transition table for the start state.
// For each rune in the alphabet: if start has a transition, use it; else stay at start.
func (ra *RuneAhoCorasick) buildStartDense() {
	ra.startDense = make([]stateID, ra.alphaSize)
	// Index 0 (not-in-alphabet) → stay at start.
	ra.startDense[0] = startStateID
	for alpha := 1; alpha < ra.alphaSize; alpha++ {
		ra.startDense[alpha] = startStateID // default
	}

	// Fill from actual transitions.
	base := int(ra.transBase[startStateID])
	length := int(ra.transLen[startStateID])
	for i := 0; i < length; i++ {
		tr := ra.transBuf[base+i]
		alpha := ra.runeToAlpha(tr.r)
		if alpha > 0 {
			ra.startDense[alpha] = tr.next
		}
	}
}

// buildRuneDenseTrans precomputes dense transition tables for shallow states.
// For each state at depth ≤ maxDepth and each alphabet rune, the table stores
// the result of following transitions + failure links (precomputed O(1) lookup).
func (ra *RuneAhoCorasick) buildRuneDenseTrans(maxDepth int, depths []uint16) {
	numStates := stateID(len(ra.states))
	ra.denseIdx = make([]int32, numStates)
	for i := range ra.denseIdx {
		ra.denseIdx[i] = -1
	}

	if ra.alphaSize <= 1 {
		return
	}

	md := uint16(maxDepth)

	// Adaptive: reduce maxDepth if memory would exceed budget.
	const maxDenseBytes = 8 << 20 // 8MB
	const maxTrackedDepth = 256
	var cumByDepth [maxTrackedDepth + 1]int32
	for s := stateID(2); s < numStates; s++ {
		d := depths[s]
		if d <= maxTrackedDepth {
			cumByDepth[d]++
		}
	}
	for d := 1; d <= maxTrackedDepth; d++ {
		cumByDepth[d] += cumByDepth[d-1]
	}
	for md > 0 && int(cumByDepth[md])*ra.alphaSize*4 > maxDenseBytes {
		md--
	}

	denseCount := int(cumByDepth[md])
	if denseCount == 0 {
		return
	}

	ra.denseTrans = make([]stateID, denseCount*ra.alphaSize)
	idx := int32(0)
	for s := stateID(2); s < numStates; s++ {
		if depths[s] > md {
			continue
		}
		ra.denseIdx[s] = idx
		tableBase := int(idx) * ra.alphaSize

		// For each alphabet index, simulate nextState(s, rune) including failure links.
		for alpha := 0; alpha < ra.alphaSize; alpha++ {
			if alpha == 0 {
				// Not-in-alphabet rune: walk failure links to start, stay at start.
				ra.denseTrans[tableBase] = startStateID
				continue
			}

			// Find the rune for this alpha index by scanning runeTable.
			// This is build-time only, so O(n) is fine.
			r := ra.alphaToRune(uint16(alpha))

			cur := s
			for {
				if cur == deadStateID {
					ra.denseTrans[tableBase+alpha] = deadStateID
					break
				}
				if next, ok := ra.lookupSparse(cur, r); ok {
					ra.denseTrans[tableBase+alpha] = next
					break
				}
				if cur == startStateID {
					ra.denseTrans[tableBase+alpha] = startStateID
					break
				}
				fail := ra.states[cur].fail
				if fail == startStateID {
					ra.denseTrans[tableBase+alpha] = ra.startDense[alpha]
					break
				}
				cur = fail
			}
		}
		idx++
	}
}

// runeToAlpha maps a rune to its compact alphabet index (0 = not in alphabet).
//
//go:nosplit
func (ra *RuneAhoCorasick) runeToAlpha(r rune) uint16 {
	if r < ra.minRune || r > ra.maxRune {
		return 0
	}
	return ra.runeTable[r-ra.minRune]
}

// alphaToRune returns the rune for a given 1-based alpha index (build-time only).
func (ra *RuneAhoCorasick) alphaToRune(alpha uint16) rune {
	for i, a := range ra.runeTable {
		if a == alpha {
			return ra.minRune + rune(i)
		}
	}
	return 0
}

// lookupSparse performs a binary search for rune r in the flattened transition
// buffer of state s.
//
//go:nosplit
func (ra *RuneAhoCorasick) lookupSparse(s stateID, r rune) (stateID, bool) {
	base := int(ra.transBase[s])
	length := int(ra.transLen[s])
	tr := ra.transBuf[base : base+length]
	lo, hi := 0, length
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if tr[mid].r < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < length && tr[lo].r == r {
		return tr[lo].next, true
	}
	return 0, false
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
	outputs := ra.outputs
	outLens := ra.outLen
	n := len(haystack)

	// Dense table locals.
	startDense := ra.startDense
	denseTrans := ra.denseTrans
	denseIdx := ra.denseIdx
	alphaSize := ra.alphaSize
	runeTable := ra.runeTable
	minRune := ra.minRune
	maxRune := ra.maxRune

	// Sparse fallback locals.
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen

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

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

		// Map rune to compact alphabet index.
		alpha := uint16(0)
		if r >= minRune && r <= maxRune {
			alpha = runeTable[r-minRune]
		}

		if alpha == 0 {
			// Rune not in any pattern → go to start (no pattern can match through this rune).
			state = startStateID
		} else if state == startStateID {
			// O(1) dense lookup for start state.
			state = startDense[alpha]
		} else if di := denseIdx[state]; di >= 0 {
			// O(1) dense lookup for shallow state.
			state = denseTrans[int(di)*alphaSize+int(alpha)]
		} else {
			// Sparse fallback: binary search + failure links for deep states.
			state = ra.nextStateSparse(state, r, alpha, startDense, transBuf, transBase, transLen)
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

// nextStateSparse is the fallback path for deep states without dense tables.
// It walks failure links and uses binary search for transition lookups.
//
//go:nosplit
func (ra *RuneAhoCorasick) nextStateSparse(
	state stateID, r rune, alpha uint16,
	startDense []stateID,
	transBuf []runeNFATrans, transBase, transLen []int32,
) stateID {
	for {
		if state == deadStateID {
			return deadStateID
		}

		tbase := int(transBase[state])
		tlen := int(transLen[state])
		tr := transBuf[tbase : tbase+tlen]

		// Try transition at current state.
		found := false
		if tlen <= 8 {
			for i := 0; i < tlen; i++ {
				if tr[i].r == r {
					return tr[i].next
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
				return tr[lo].next
			}
		}

		if found {
			// unreachable but helps compiler
			break
		}

		if state == startStateID {
			return startStateID
		}

		fail := ra.states[state].fail
		if fail == startStateID {
			return startDense[alpha]
		}

		// If failure state has a dense table, use it.
		if di := ra.denseIdx[fail]; di >= 0 {
			return ra.denseTrans[int(di)*ra.alphaSize+int(alpha)]
		}
		state = fail
	}
	return startStateID
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
	outputs := ra.outputs
	outLens := ra.outLen
	patLens := ra.patLens
	n := len(haystack)
	out := dst

	startDense := ra.startDense
	denseTrans := ra.denseTrans
	denseIdx := ra.denseIdx
	alphaSize := ra.alphaSize
	runeTable := ra.runeTable
	minRune := ra.minRune
	maxRune := ra.maxRune
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen

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

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

		alpha := uint16(0)
		if r >= minRune && r <= maxRune {
			alpha = runeTable[r-minRune]
		}

		if alpha == 0 {
			state = startStateID
		} else if state == startStateID {
			state = startDense[alpha]
		} else if di := denseIdx[state]; di >= 0 {
			state = denseTrans[int(di)*alphaSize+int(alpha)]
		} else {
			state = ra.nextStateSparse(state, r, alpha, startDense, transBuf, transBase, transLen)
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
	n := len(haystack)

	startDense := ra.startDense
	denseTrans := ra.denseTrans
	denseIdx := ra.denseIdx
	alphaSize := ra.alphaSize
	runeTable := ra.runeTable
	minRune := ra.minRune
	maxRune := ra.maxRune
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen

	state := startStateID

	if states[state].outputIdx >= 0 {
		return true
	}

	if n == 0 {
		return false
	}

	_ = haystack[n-1]

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

		alpha := uint16(0)
		if r >= minRune && r <= maxRune {
			alpha = runeTable[r-minRune]
		}

		if alpha == 0 {
			state = startStateID
		} else if state == startStateID {
			state = startDense[alpha]
		} else if di := denseIdx[state]; di >= 0 {
			state = denseTrans[int(di)*alphaSize+int(alpha)]
		} else {
			state = ra.nextStateSparse(state, r, alpha, startDense, transBuf, transBase, transLen)
		}

		if states[state].outputIdx >= 0 {
			return true
		}
	}

	return false
}
