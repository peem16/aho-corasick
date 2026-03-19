package ahocorasick

import (
	"sort"
	"unsafe"
)

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

// outputFlag is the high bit of a denseTrans entry. When set, it indicates
// that the target state has at least one pattern output, allowing the hot
// path to skip the output check (a random memory load) in the common
// no-match case.
const outputFlag stateID = 1 << 31

// RuneAhoCorasick is an Aho-Corasick automaton that operates on rune
// (Unicode code point) sequences instead of byte sequences.
//
// This is beneficial for scripts like Thai, CJK, etc. where each character
// is 3+ bytes in UTF-8. A byte-based automaton traverses 3x more transitions
// per character compared to a rune-based one.
//
// Uses an NFA with a compact rune alphabet and precomputed dense transition
// tables for the start state and shallow states (depth ≤ denseDepth).
// Dense table entries encode both the target state and a hasOutput flag
// in a single uint32, eliminating a separate memory load for the output
// check in the common (no-match) case.
type RuneAhoCorasick struct {
	states    []nfaState     // reuse existing (fail + outputIdx) — used during build & sparse fallback
	transBuf  []runeNFATrans // all transitions concatenated (sparse fallback)
	transBase []int32        // per-state offset into transBuf
	transLen  []int32        // per-state transition count
	outputs   []PatternID    // all output pattern IDs, concatenated
	outLen    []int32        // per-state output count
	outputOff []int32        // per-state output offset into outputs[] (-1 = no output)
	patterns  [][]rune       // deep copy of original patterns
	patLens   []int32        // cached pattern lengths
	patCount  int

	// Compact rune alphabet: maps runes in [minRune, minRune+runeTableLen)
	// to 1-based indices (0 = rune not in any pattern).
	runeTable    []uint16
	minRune      uint32 // stored as uint32 for branchless subtraction
	runeTableLen uint32 // = maxRune - minRune + 1 (for single unsigned bounds check)
	alphaSize    int    // number of distinct runes + 1 (0 reserved for "not in alphabet")

	// Unified dense transition table. Start state and shallow states share
	// the same denseTrans array. Each entry encodes:
	//   - bits [30:0]: target stateID
	//   - bit  [31]:   1 if target state has pattern output (outputFlag)
	// denseBase[s] is the pre-multiplied offset (idx * alphaSize) for O(1)
	// lookup: denseTrans[denseBase[s] + alpha].
	// denseBase[s] = -1 means the state uses the sparse fallback.
	denseTrans []stateID // concatenated dense tables, each of length alphaSize
	denseBase  []int32   // per-state pre-multiplied base into denseTrans; -1 = sparse
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

	// ---- Phase 6: build unified dense tables (start + shallow states) ----
	ra.buildUnifiedDenseTrans(runeDefaultDenseDepth, tmpDepths)

	// ---- Phase 7: deep copy patterns & cache lengths ----
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
	ra.outputOff = make([]int32, numStates)

	total := 0
	for _, outs := range tmp {
		total += len(outs)
	}
	ra.outputs = make([]PatternID, 0, total)

	for s := 0; s < numStates; s++ {
		outs := tmp[s]
		if len(outs) == 0 {
			ra.states[s].outputIdx = -1
			ra.outputOff[s] = -1
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
		off := int32(len(ra.outputs))
		ra.states[s].outputIdx = off
		ra.outputOff[s] = off
		ra.outLen[s] = int32(len(outs))
		ra.outputs = append(ra.outputs, outs...)
	}
}

// buildRuneAlphabet collects all unique runes from patterns and builds a
// compact mapping from rune to 1-based index.
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

	first := true
	var minR, maxR rune
	for r := range seen {
		if first || r < minR {
			minR = r
		}
		if first || r > maxR {
			maxR = r
		}
		first = false
	}

	ra.minRune = uint32(minR)
	rangeSize := int(maxR-minR) + 1
	ra.runeTable = make([]uint16, rangeSize)
	ra.runeTableLen = uint32(rangeSize)
	idx := uint16(1)
	sorted := make([]rune, 0, len(seen))
	for r := range seen {
		sorted = append(sorted, r)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, r := range sorted {
		ra.runeTable[r-minR] = idx
		idx++
	}
	ra.alphaSize = int(idx)
}

// buildUnifiedDenseTrans builds a single denseTrans array that includes
// the start state and all shallow states. Each entry encodes the target
// stateID in bits [30:0] and a hasOutput flag in bit [31].
func (ra *RuneAhoCorasick) buildUnifiedDenseTrans(maxDepth int, depths []uint16) {
	numStates := stateID(len(ra.states))
	ra.denseBase = make([]int32, numStates)
	for i := range ra.denseBase {
		ra.denseBase[i] = -1
	}

	if ra.alphaSize <= 1 {
		return
	}

	md := uint16(maxDepth)

	const maxDenseBytes = 8 << 20
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
	for md > 0 && int(1+cumByDepth[md])*ra.alphaSize*4 > maxDenseBytes {
		md--
	}

	denseCount := 1 + int(cumByDepth[md]) // +1 for start state
	ra.denseTrans = make([]stateID, denseCount*ra.alphaSize)

	// Helper: encode a target state with its output flag.
	encode := func(target stateID) stateID {
		if ra.outputOff[target] >= 0 {
			return target | outputFlag
		}
		return target
	}

	// --- Slot 0: start state ---
	ra.denseBase[startStateID] = 0
	for alpha := 0; alpha < ra.alphaSize; alpha++ {
		ra.denseTrans[alpha] = encode(startStateID)
	}
	base := int(ra.transBase[startStateID])
	length := int(ra.transLen[startStateID])
	for i := 0; i < length; i++ {
		tr := ra.transBuf[base+i]
		off := uint32(tr.r) - ra.minRune
		if off < ra.runeTableLen {
			alpha := ra.runeTable[off]
			if alpha > 0 {
				ra.denseTrans[int(alpha)] = encode(tr.next)
			}
		}
	}

	// --- Slots 1..N: shallow states ---
	idx := int32(1)
	for s := stateID(2); s < numStates; s++ {
		if depths[s] > md {
			continue
		}
		tableBase := int(idx) * ra.alphaSize
		ra.denseBase[s] = int32(tableBase)

		for alpha := 0; alpha < ra.alphaSize; alpha++ {
			if alpha == 0 {
				ra.denseTrans[tableBase] = encode(startStateID)
				continue
			}

			r := ra.alphaToRune(uint16(alpha))
			cur := s
			for {
				if cur == deadStateID {
					ra.denseTrans[tableBase+alpha] = encode(deadStateID)
					break
				}
				if next, ok := ra.lookupSparse(cur, r); ok {
					ra.denseTrans[tableBase+alpha] = encode(next)
					break
				}
				if cur == startStateID {
					ra.denseTrans[tableBase+alpha] = encode(startStateID)
					break
				}
				fail := ra.states[cur].fail
				if fail == startStateID {
					// Use start state's dense table (slot 0), already encoded.
					ra.denseTrans[tableBase+alpha] = ra.denseTrans[alpha]
					break
				}
				cur = fail
			}
		}
		idx++
	}
}

// alphaToRune returns the rune for a given 1-based alpha index (build-time only).
func (ra *RuneAhoCorasick) alphaToRune(alpha uint16) rune {
	for i, a := range ra.runeTable {
		if a == alpha {
			return rune(ra.minRune) + rune(i)
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

	n := len(haystack)
	if n == 0 {
		// Only check empty-pattern output at start.
		if ra.outputOff[startStateID] >= 0 {
			ra.drainOutputs(startStateID, seen)
		}
		return
	}

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	denseTrans := ra.denseTrans
	denseBase := ra.denseBase
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune

	// Unsafe base pointers for bounds-check-free access in the hot loop.
	// All indices are guaranteed valid by construction (build phase).
	rtPtr := unsafe.Pointer(unsafe.SliceData(runeTable))
	dtPtr := unsafe.Pointer(unsafe.SliceData(denseTrans))
	dbPtr := unsafe.Pointer(unsafe.SliceData(denseBase))
	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))

	// Sparse fallback locals.
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen

	state := startStateID

	// Check for empty-pattern match at start.
	if outputOff[startStateID] >= 0 {
		ra.drainOutputs(startStateID, seen)
	}

	for pos := 0; pos < n; pos++ {
		// Load rune without bounds check.
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(pos)*4))

		// Map rune to compact alphabet index (single unsigned comparison).
		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}

		// Dense table lookup (no bounds check).
		db := *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4))
		if db >= 0 {
			// O(1) transition: denseBase[state] + alpha → denseTrans entry.
			raw := *(*stateID)(unsafe.Add(dtPtr, uintptr(db+alpha)*4))
			state = raw &^ outputFlag
			if raw >= outputFlag {
				// Target state has output — drain it.
				obase := outputOff[state]
				ol := outLen[state]
				for i := int32(0); i < ol; i++ {
					seen[outputs[obase+i]] = true
				}
			}
		} else if alpha == 0 {
			state = startStateID
		} else {
			state = ra.nextStateSparse(state, rune(r), uint16(alpha), denseTrans, transBuf, transBase, transLen)
			if outputOff[state] >= 0 {
				obase := outputOff[state]
				ol := outLen[state]
				for i := int32(0); i < ol; i++ {
					seen[outputs[obase+i]] = true
				}
			}
		}
	}
}

// drainOutputs writes all pattern IDs at state s into seen.
func (ra *RuneAhoCorasick) drainOutputs(s stateID, seen []bool) {
	obase := ra.outputOff[s]
	ol := ra.outLen[s]
	for i := int32(0); i < ol; i++ {
		seen[ra.outputs[obase+i]] = true
	}
}

// nextStateSparse is the fallback path for deep states without dense tables.
//
//go:nosplit
func (ra *RuneAhoCorasick) nextStateSparse(
	state stateID, r rune, alpha uint16,
	denseTrans []stateID,
	transBuf []runeNFATrans, transBase, transLen []int32,
) stateID {
	for {
		if state == deadStateID {
			return deadStateID
		}

		tbase := int(transBase[state])
		tlen := int(transLen[state])
		tr := transBuf[tbase : tbase+tlen]

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

		if state == startStateID {
			return startStateID
		}

		fail := ra.states[state].fail
		if db := ra.denseBase[fail]; db >= 0 {
			raw := denseTrans[int(db)+int(alpha)]
			return raw &^ outputFlag
		}
		state = fail
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
func (ra *RuneAhoCorasick) FindOverlappingAllAppend(dst []RuneMatch, haystack []rune) []RuneMatch {
	if ra == nil || len(ra.states) == 0 {
		return dst
	}

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	patLens := ra.patLens
	n := len(haystack)
	out := dst

	denseTrans := ra.denseTrans
	denseBase := ra.denseBase
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen

	state := startStateID

	if outputOff[startStateID] >= 0 {
		obase := outputOff[startStateID]
		ol := outLen[startStateID]
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

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(runeTable[off])
		}

		hasOut := false
		if db := denseBase[state]; db >= 0 {
			raw := denseTrans[int(db)+int(alpha)]
			state = raw &^ outputFlag
			hasOut = raw >= outputFlag
		} else if alpha == 0 {
			state = startStateID
		} else {
			state = ra.nextStateSparse(state, r, uint16(alpha), denseTrans, transBuf, transBase, transLen)
			hasOut = outputOff[state] >= 0
		}

		if hasOut {
			obase := outputOff[state]
			ol := outLen[state]
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

	n := len(haystack)
	outputOff := ra.outputOff
	denseTrans := ra.denseTrans
	denseBase := ra.denseBase
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune
	transBuf := ra.transBuf
	transBase := ra.transBase
	transLen := ra.transLen

	state := startStateID

	if outputOff[startStateID] >= 0 {
		return true
	}

	if n == 0 {
		return false
	}

	_ = haystack[n-1]

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(runeTable[off])
		}

		if db := denseBase[state]; db >= 0 {
			raw := denseTrans[int(db)+int(alpha)]
			state = raw &^ outputFlag
			if raw >= outputFlag {
				return true
			}
		} else if alpha == 0 {
			state = startStateID
		} else {
			state = ra.nextStateSparse(state, r, uint16(alpha), denseTrans, transBuf, transBase, transLen)
			if outputOff[state] >= 0 {
				return true
			}
		}
	}

	return false
}
