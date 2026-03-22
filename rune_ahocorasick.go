package ahocorasick

import (
	"sort"
	"unsafe"
)

// ---------------------------------------------------------------------------
// Rune-based Aho-Corasick NFA with Double-Array Trie
// ---------------------------------------------------------------------------

// runeNFATrans is a single (rune → state) transition entry (build-time only).
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

// daOutputFlag is the high bit of a daBase entry. When set, it indicates
// that the DA slot has at least one pattern output, allowing the hot
// path to skip the output check (a random memory load) in the common
// no-match case.
const daOutputFlag int32 = -1 << 31 // 0x80000000

// daUnused is the sentinel value for unused daCheck slots.
const daUnused int32 = -1

// RuneAhoCorasick is an Aho-Corasick automaton that operates on rune
// (Unicode code point) sequences instead of byte sequences.
//
// Uses a double-array trie for compact O(1) state transitions. The compact
// rune alphabet maps runes to small 1-based indices, keeping the double-array
// small and cache-friendly. Failure links are followed on transition miss.
//
// Memory: ~8 bytes per DA slot (base + check). A typical 50-state machine
// with ~200 transitions uses ~300-500 DA slots = 2-4 KB, compared to ~40 KB
// for the previous dense-table approach. This 10-15x reduction means each
// machine fits in L1 cache, critical for per-campaign matching with 2639+
// cold machines.
type RuneAhoCorasick struct {
	// Double-array trie. Transition: t = (daBase[state] & 0x7FFFFFFF) + alpha;
	// if daCheck[t] == state, next state = t. Else follow daFail[state].
	// daBase high bit (bit 31) = output flag (1 = this slot has pattern output).
	daBase  []int32 // per-slot base offset; bit 31 = output flag
	daCheck []int32 // per-slot parent slot; -1 = unused
	daFail  []int32 // per-slot failure link → DA slot

	// Outputs (indexed by DA slot).
	outputs   []PatternID // all output pattern IDs, concatenated
	outLen    []int32     // per-slot output count
	outputOff []int32     // per-slot output offset into outputs[] (-1 = no output)

	// Compact rune alphabet: maps runes in [minRune, minRune+runeTableLen)
	// to 1-based indices (0 = rune not in any pattern).
	runeTable    []uint16
	minRune      uint32 // stored as uint32 for branchless subtraction
	runeTableLen uint32 // = maxRune - minRune + 1 (for single unsigned bounds check)
	alphaSize    int32  // number of distinct runes + 1 (0 reserved for "not in alphabet")

	// Root DA slot (always 0).
	rootSlot int32

	// Patterns.
	patterns [][]rune
	patLens  []int32
	patCount int

	// DFA precomputed table (optional, built by BuildDFA).
	// dfaNext[slot * alphaSize + alpha] = next DA slot.
	// Eliminates failure-link following in the scan hot path.
	dfaNext []int32

	// Interleaved DA (optional, built by BuildVec).
	// Layout: [base0, check0, fail0, outOff0, base1, check1, fail1, outOff1, ...]
	// 16 bytes per slot. Loading check[t] prefetches base[t] in the same
	// cache line → saves one L3 miss per rune on the next iteration.
	daVec []int32
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

// Stats returns internal sizes for diagnostics.
//   - DASlots: total slots allocated in the double-array
//   - UsedSlots: slots where daCheck != -1 (or root)
//   - AlphaSize: compact alphabet size (including 0 = unknown)
func (ra *RuneAhoCorasick) Stats() (daSlots, usedSlots int, alphaSize int) {
	if ra == nil {
		return
	}
	daSlots = len(ra.daBase)
	alphaSize = int(ra.alphaSize)
	for i := range ra.daCheck {
		if ra.daCheck[i] != daUnused || int32(i) == ra.rootSlot {
			usedSlots++
		}
	}
	return
}

// BuildDFA precomputes a flat DFA transition table that eliminates
// failure-link following in the scan hot path. After calling BuildDFA,
// use OverlappingPatternSetDFA / OverlappingPatternSetDFATrack.
//
// Memory: len(daBase) × alphaSize × 4 bytes. For 149K-pattern unified
// machines this can be hundreds of MB; call Stats() first to estimate.
func (ra *RuneAhoCorasick) BuildDFA() {
	numSlots := int32(len(ra.daBase))
	alphaSize := ra.alphaSize
	root := ra.rootSlot

	tbl := make([]int32, int64(numSlots)*int64(alphaSize))

	for s := int32(0); s < numSlots; s++ {
		if ra.daCheck[s] == daUnused && s != root {
			continue
		}
		row := int64(s) * int64(alphaSize)
		for a := int32(1); a < alphaSize; a++ {
			// Follow fail chain to resolve this transition.
			state := s
			for {
				base := ra.daBase[state] & 0x7FFFFFFF
				t := base + a
				if t < numSlots && ra.daCheck[t] == state {
					tbl[row+int64(a)] = t
					break
				}
				if state == root {
					tbl[row+int64(a)] = root
					break
				}
				state = ra.daFail[state]
			}
		}
	}

	ra.dfaNext = tbl
}

// DFAMemBytes returns the memory used by the DFA table, or 0 if not built.
func (ra *RuneAhoCorasick) DFAMemBytes() int64 {
	if ra == nil || ra.dfaNext == nil {
		return 0
	}
	return int64(len(ra.dfaNext)) * 4
}

// OverlappingPatternSetDFA is like OverlappingPatternSet but uses the
// precomputed DFA table. One memory load per input rune (no fail loops).
// Must call BuildDFA() first.
func (ra *RuneAhoCorasick) OverlappingPatternSetDFA(haystack []rune, seen []bool) {
	if ra == nil || ra.dfaNext == nil || ra.patCount == 0 {
		return
	}

	n := len(haystack)
	root := ra.rootSlot
	alphaSize := int64(ra.alphaSize)

	// Root outputs.
	if ra.outputOff[root] >= 0 {
		obase := ra.outputOff[root]
		ol := ra.outLen[root]
		for i := int32(0); i < ol; i++ {
			seen[ra.outputs[obase+i]] = true
		}
	}
	if n == 0 {
		return
	}

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune
	daBase := ra.daBase
	dfaNext := ra.dfaNext

	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	rtPtr := unsafe.Pointer(unsafe.SliceData(runeTable))
	dfaPtr := unsafe.Pointer(unsafe.SliceData(dfaNext))
	dbPtr := unsafe.Pointer(unsafe.SliceData(daBase))

	state := root

	for pos := 0; pos < n; pos++ {
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(pos)*4))

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}

		if alpha == 0 {
			state = root
			continue
		}

		// Single lookup — no fail chain.
		state = *(*int32)(unsafe.Add(dfaPtr, uintptr(int64(state)*alphaSize+int64(alpha))*4))

		if *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) < 0 {
			obase := outputOff[state]
			ol := outLen[state]
			for i := int32(0); i < ol; i++ {
				seen[outputs[obase+i]] = true
			}
		}
	}
}

// OverlappingPatternSetDFATrack is OverlappingPatternSetDFA + dirty tracking.
func (ra *RuneAhoCorasick) OverlappingPatternSetDFATrack(haystack []rune, seen []bool, dirty []PatternID) []PatternID {
	if ra == nil || ra.dfaNext == nil || ra.patCount == 0 {
		return dirty
	}

	n := len(haystack)
	root := ra.rootSlot
	alphaSize := int64(ra.alphaSize)

	if ra.outputOff[root] >= 0 {
		obase := ra.outputOff[root]
		ol := ra.outLen[root]
		for i := int32(0); i < ol; i++ {
			pid := ra.outputs[obase+i]
			if !seen[pid] {
				seen[pid] = true
				dirty = append(dirty, pid)
			}
		}
	}
	if n == 0 {
		return dirty
	}

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune
	daBase := ra.daBase
	dfaNext := ra.dfaNext

	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	rtPtr := unsafe.Pointer(unsafe.SliceData(runeTable))
	dfaPtr := unsafe.Pointer(unsafe.SliceData(dfaNext))
	dbPtr := unsafe.Pointer(unsafe.SliceData(daBase))

	state := root

	for pos := 0; pos < n; pos++ {
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(pos)*4))

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}

		if alpha == 0 {
			state = root
			continue
		}

		state = *(*int32)(unsafe.Add(dfaPtr, uintptr(int64(state)*alphaSize+int64(alpha))*4))

		if *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) < 0 {
			obase := outputOff[state]
			ol := outLen[state]
			for i := int32(0); i < ol; i++ {
				pid := outputs[obase+i]
				if !seen[pid] {
					seen[pid] = true
					dirty = append(dirty, pid)
				}
			}
		}
	}

	return dirty
}

// ---------------------------------------------------------------------------
// Vectorized scan: interleaved DA (AoS) + alpha pre-conversion
// ---------------------------------------------------------------------------

// BuildVec creates the interleaved DA layout used by OverlappingPatternSetVecTrack.
// Layout: 4 × int32 per slot = 16 bytes [base, check, fail, outputOff].
// When the CPU loads check[t], base[t] is in the same cache line.
// On the next iteration (state = t), base[t] is already in L1 — one fewer
// L3 miss per rune compared to the SoA layout.
//
// Memory: len(daBase) × 16 bytes. For 943K slots → 14.4 MB.
func (ra *RuneAhoCorasick) BuildVec() {
	n := int32(len(ra.daBase))
	v := make([]int32, n*4)
	for i := int32(0); i < n; i++ {
		off := i * 4
		v[off+0] = ra.daBase[i]
		v[off+1] = ra.daCheck[i]
		v[off+2] = ra.daFail[i]
		v[off+3] = ra.outputOff[i]
	}
	ra.daVec = v
}

// VecMemBytes returns the interleaved DA table size in bytes, or 0 if not built.
func (ra *RuneAhoCorasick) VecMemBytes() int64 {
	if ra == nil || ra.daVec == nil {
		return 0
	}
	return int64(len(ra.daVec)) * 4
}

// OverlappingPatternSetVecTrack uses the interleaved DA layout for
// cache-friendly scanning. Two-phase approach:
//  1. Pre-convert all runes to alpha values (tight loop, runeTable in L1).
//  2. Scan the alpha array using interleaved DA (check[t] prefetches base[t]).
//
// Must call BuildVec() first.
func (ra *RuneAhoCorasick) OverlappingPatternSetVecTrack(haystack []rune, seen []bool, dirty []PatternID) []PatternID {
	if ra == nil || ra.daVec == nil || ra.patCount == 0 {
		return dirty
	}

	n := len(haystack)
	root := ra.rootSlot

	// Root outputs.
	if ra.outputOff[root] >= 0 {
		obase := ra.outputOff[root]
		ol := ra.outLen[root]
		for i := int32(0); i < ol; i++ {
			pid := ra.outputs[obase+i]
			if !seen[pid] {
				seen[pid] = true
				dirty = append(dirty, pid)
			}
		}
	}
	if n == 0 {
		return dirty
	}

	// ---- Phase 1: Rune → alpha pre-conversion ----
	// Stack buffer for small texts (≤ 1024 runes = 4 KB on stack).
	var alphaBuf [1024]int32
	var alphas []int32
	if n <= 1024 {
		alphas = alphaBuf[:n]
	} else {
		alphas = make([]int32, n)
	}

	rtPtr := unsafe.Pointer(unsafe.SliceData(ra.runeTable))
	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	minRune := ra.minRune
	rtLen := ra.runeTableLen

	for i := 0; i < n; i++ {
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(i)*4))
		off := uint32(r) - minRune
		if off < rtLen {
			alphas[i] = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}
		// else alphas[i] stays 0 (zero-initialized).
	}

	// ---- Phase 2: Interleaved DA scan ----
	outputs := ra.outputs
	outLen := ra.outLen
	daVec := ra.daVec
	vecPtr := unsafe.Pointer(unsafe.SliceData(daVec))

	state := root

	for i := 0; i < n; i++ {
		alpha := alphas[i]
		if alpha == 0 {
			state = root
			continue
		}

		// DA transition with fail chain. Each slot is 16 bytes in vecPtr.
		for {
			// base = daVec[state*4 + 0] & 0x7FFFFFFF
			base := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) & 0x7FFFFFFF
			t := base + alpha
			// check = daVec[t*4 + 1] — loading this brings base[t] into cache too!
			if *(*int32)(unsafe.Add(vecPtr, uintptr(t)*16+4)) == state {
				state = t
				break
			}
			if state == root {
				break
			}
			// fail = daVec[state*4 + 2]
			state = *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+8))
		}

		// Check output flag (bit 31 of base).
		if *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) < 0 {
			// outputOff = daVec[state*4 + 3]
			ooff := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+12))
			if ooff >= 0 {
				ol := outLen[state]
				for j := int32(0); j < ol; j++ {
					pid := outputs[ooff+j]
					if !seen[pid] {
						seen[pid] = true
						dirty = append(dirty, pid)
					}
				}
			}
		}
	}

	return dirty
}

// ---------------------------------------------------------------------------
// Bitset scan: output to []uint64 instead of []bool (8x smaller → fits L1)
// ---------------------------------------------------------------------------

// BitsetWords returns the number of uint64 words needed for the bitset:
// ceil(PatternCount() / 64).
func (ra *RuneAhoCorasick) BitsetWords() int {
	if ra == nil {
		return 0
	}
	return (ra.patCount + 63) / 64
}

// OverlappingBitsetTrack scans haystack and sets bits in the seen[] bitset.
// seen must have length >= BitsetWords(). dirty tracks which word indices
// were modified, for efficient clearing:
//
//	dirty = machine.OverlappingBitsetTrack(text, seen, dirty[:0])
//	// ... evaluate conditions using bitwise ops ...
//	for _, wi := range dirty { seen[wi] = 0 }
func (ra *RuneAhoCorasick) OverlappingBitsetTrack(haystack []rune, seen []uint64, dirty []int32) []int32 {
	if ra == nil || ra.patCount == 0 && len(ra.daBase) == 0 {
		return dirty
	}

	n := len(haystack)
	root := ra.rootSlot

	// Helper: set bit and track dirty word.
	setBit := func(pid PatternID) {
		wi := int32(pid / 64)
		bit := uint64(1) << (pid % 64)
		if seen[wi]&bit == 0 {
			seen[wi] |= bit
			// Track dirty word (may add duplicates — caller deduplicates by zeroing).
			dirty = append(dirty, wi)
		}
	}

	// Root outputs.
	if ra.outputOff[root] >= 0 {
		obase := ra.outputOff[root]
		ol := ra.outLen[root]
		for i := int32(0); i < ol; i++ {
			setBit(ra.outputs[obase+i])
		}
	}
	if n == 0 {
		return dirty
	}

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune
	daBase := ra.daBase
	daCheck := ra.daCheck
	daFail := ra.daFail

	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	rtPtr := unsafe.Pointer(unsafe.SliceData(runeTable))
	dbPtr := unsafe.Pointer(unsafe.SliceData(daBase))
	dcPtr := unsafe.Pointer(unsafe.SliceData(daCheck))
	dfPtr := unsafe.Pointer(unsafe.SliceData(daFail))

	state := root

	for pos := 0; pos < n; pos++ {
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(pos)*4))

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}

		if alpha == 0 {
			state = root
			continue
		}

		for {
			base := *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) & 0x7FFFFFFF
			t := base + alpha
			if *(*int32)(unsafe.Add(dcPtr, uintptr(t)*4)) == state {
				state = t
				break
			}
			if state == root {
				break
			}
			state = *(*int32)(unsafe.Add(dfPtr, uintptr(state)*4))
		}

		if *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) < 0 {
			obase := outputOff[state]
			ol := outLen[state]
			for i := int32(0); i < ol; i++ {
				setBit(outputs[obase+i])
			}
		}
	}

	return dirty
}

// OverlappingBitsetVecTrack combines the Vec two-phase scan (alpha pre-conversion
// + interleaved DA) with bitset output ([]uint64 + dirty word tracking).
// Must call BuildVec() first. Falls back to OverlappingBitsetTrack if daVec is nil.
func (ra *RuneAhoCorasick) OverlappingBitsetVecTrack(haystack []rune, seen []uint64, dirty []int32) []int32 {
	if ra == nil || ra.patCount == 0 {
		return dirty
	}
	if ra.daVec == nil {
		return ra.OverlappingBitsetTrack(haystack, seen, dirty)
	}

	n := len(haystack)
	root := ra.rootSlot

	// Helper: set bit and track dirty word.
	setBit := func(pid PatternID) {
		wi := int32(pid / 64)
		bit := uint64(1) << (pid % 64)
		if seen[wi]&bit == 0 {
			seen[wi] |= bit
			dirty = append(dirty, wi)
		}
	}

	// Root outputs.
	if ra.outputOff[root] >= 0 {
		obase := ra.outputOff[root]
		ol := ra.outLen[root]
		for i := int32(0); i < ol; i++ {
			setBit(ra.outputs[obase+i])
		}
	}
	if n == 0 {
		return dirty
	}

	// ---- Phase 1: Rune → alpha pre-conversion ----
	var alphaBuf [1024]int32
	var alphas []int32
	if n <= 1024 {
		alphas = alphaBuf[:n]
	} else {
		alphas = make([]int32, n)
	}

	rtPtr := unsafe.Pointer(unsafe.SliceData(ra.runeTable))
	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	minRune := ra.minRune
	rtLen := ra.runeTableLen

	for i := 0; i < n; i++ {
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(i)*4))
		off := uint32(r) - minRune
		if off < rtLen {
			alphas[i] = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}
	}

	// ---- Phase 2: Interleaved DA scan with bitset output ----
	outputs := ra.outputs
	outLen := ra.outLen
	daVec := ra.daVec
	vecPtr := unsafe.Pointer(unsafe.SliceData(daVec))

	state := root

	for i := 0; i < n; i++ {
		alpha := alphas[i]
		if alpha == 0 {
			state = root
			continue
		}

		for {
			base := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) & 0x7FFFFFFF
			t := base + alpha
			if *(*int32)(unsafe.Add(vecPtr, uintptr(t)*16+4)) == state {
				state = t
				break
			}
			if state == root {
				break
			}
			state = *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+8))
		}

		if *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) < 0 {
			ooff := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+12))
			if ooff >= 0 {
				ol := outLen[state]
				for j := int32(0); j < ol; j++ {
					setBit(outputs[ooff+j])
				}
			}
		}
	}

	return dirty
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

	// Temporary per-state transitions.
	tmpTrans := make([][]runeNFATrans, 2)

	states := make([]tmpState, 2)
	states[0].outputIdx = -1 // dead state
	states[1].outputIdx = -1 // start state

	tmpOutputs := make([][]PatternID, 2)

	// Slab allocator for 1-entry transition slots.
	slabSize := 2
	for _, p := range patterns {
		slabSize += len(p)
	}
	transSlab := make([]runeNFATrans, slabSize)
	slabIdx := 2

	// ---- Phase 1: build trie ----
	for pid, pat := range patterns {
		if len(pat) == 0 {
			tmpOutputs[startStateID] = append(tmpOutputs[startStateID], PatternID(pid))
			continue
		}
		cur := startStateID
		for _, r := range pat {
			next, ok := runeLookupTmp(tmpTrans[cur], r)
			if !ok {
				newID := stateID(len(states))
				states = append(states, tmpState{outputIdx: -1})
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
	queue := make([]stateID, 0, len(states))

	for _, tr := range tmpTrans[startStateID] {
		child := tr.next
		states[child].fail = startStateID
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

			fail := states[cur].fail
			for fail != startStateID {
				if _, ok := runeLookupTmp(tmpTrans[fail], r); ok {
					break
				}
				fail = states[fail].fail
			}
			if next, ok := runeLookupTmp(tmpTrans[fail], r); ok && next != child {
				states[child].fail = next
			} else {
				states[child].fail = startStateID
			}

			failState := states[child].fail
			if len(tmpOutputs[failState]) > 0 {
				tmpOutputs[child] = append(tmpOutputs[child], tmpOutputs[failState]...)
			}

			queue = append(queue, child)
		}
	}

	// ---- Phase 3: flatten outputs (indexed by trie state) ----
	numTrieStates := len(states)
	trieOutOff := make([]int32, numTrieStates)
	trieOutLen := make([]int32, numTrieStates)
	{
		total := 0
		for _, outs := range tmpOutputs {
			total += len(outs)
		}
		ra.outputs = make([]PatternID, 0, total)

		for s := 0; s < numTrieStates; s++ {
			outs := tmpOutputs[s]
			if len(outs) == 0 {
				trieOutOff[s] = -1
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
			trieOutOff[s] = int32(len(ra.outputs))
			trieOutLen[s] = int32(len(outs))
			ra.outputs = append(ra.outputs, outs...)
		}
	}

	// ---- Phase 4: build compact rune alphabet ----
	ra.buildRuneAlphabet(patterns)

	// ---- Phase 5: build double-array trie ----
	ra.buildDoubleArray(states, tmpTrans, trieOutOff, trieOutLen)

	// ---- Phase 6: deep copy patterns & cache lengths ----
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
	ra.alphaSize = int32(idx)
}

// ---------------------------------------------------------------------------
// Double-Array Construction
// ---------------------------------------------------------------------------

type tmpState struct {
	fail      stateID
	outputIdx int32
}

// buildDoubleArray constructs the double-array trie from the temporary trie.
// It maps trie state IDs to DA slot indices, sets up failure links and outputs.
func (ra *RuneAhoCorasick) buildDoubleArray(
	states []tmpState,
	tmpTrans [][]runeNFATrans,
	trieOutOff []int32,
	trieOutLen []int32,
) {
	numTrieStates := len(states)
	if numTrieStates <= 2 && len(tmpTrans[startStateID]) == 0 {
		// Only dead + start states with no transitions.
		// Handle empty-pattern-only case.
		ra.daBase = []int32{0}
		ra.daCheck = []int32{daUnused}
		ra.daFail = []int32{0}
		ra.outputOff = []int32{trieOutOff[startStateID]}
		ra.outLen = []int32{trieOutLen[startStateID]}
		ra.rootSlot = 0
		return
	}

	// Build a rune→alpha lookup for transitions.
	runeToAlpha := func(r rune) int32 {
		off := uint32(r) - ra.minRune
		if off < ra.runeTableLen {
			return int32(ra.runeTable[off])
		}
		return 0
	}

	// Collect children as (alpha, trieChildState) for each trie state.
	type alphaChild struct {
		alpha    int32
		trieState stateID
	}
	childrenOf := func(trieState stateID) []alphaChild {
		trans := tmpTrans[trieState]
		children := make([]alphaChild, 0, len(trans))
		for _, tr := range trans {
			a := runeToAlpha(tr.r)
			if a > 0 {
				children = append(children, alphaChild{alpha: a, trieState: tr.next})
			}
		}
		return children
	}

	// Initial DA size estimate. We'll grow as needed.
	daSize := int32(numTrieStates * 2)
	if daSize < int32(ra.alphaSize)*2 {
		daSize = int32(ra.alphaSize) * 2
	}
	ra.daBase = make([]int32, daSize)
	ra.daCheck = make([]int32, daSize)
	for i := range ra.daCheck {
		ra.daCheck[i] = daUnused
	}

	// Map trie state → DA slot.
	daStateMap := make([]int32, numTrieStates)
	for i := range daStateMap {
		daStateMap[i] = -1
	}

	// Root at DA slot 0.
	ra.rootSlot = 0
	daStateMap[startStateID] = 0
	ra.daCheck[0] = 0 // root's check = self (sentinel)

	nextCheckPos := int32(1)

	// growDA grows the arrays if needed.
	growDA := func(minSize int32) {
		if minSize <= int32(len(ra.daBase)) {
			return
		}
		newSize := int32(len(ra.daBase)) * 2
		if newSize < minSize {
			newSize = minSize
		}
		newBase := make([]int32, newSize)
		copy(newBase, ra.daBase)
		ra.daBase = newBase

		newCheck := make([]int32, newSize)
		for i := int32(len(ra.daCheck)); i < newSize; i++ {
			newCheck[i] = daUnused
		}
		copy(newCheck, ra.daCheck)
		ra.daCheck = newCheck
	}

	// findBase finds a base value b such that daCheck[b + alpha] == daUnused
	// for all alphas in children.
	findBase := func(children []alphaChild) int32 {
		if len(children) == 0 {
			return int32(0)
		}
		firstAlpha := children[0].alpha
		pos := nextCheckPos
		if pos < firstAlpha {
			pos = firstAlpha
		}

		for {
			b := pos - firstAlpha
			maxSlot := b + children[len(children)-1].alpha
			growDA(maxSlot + 1)

			ok := true
			for _, c := range children {
				if ra.daCheck[b+c.alpha] != daUnused {
					ok = false
					break
				}
			}
			if ok {
				return b
			}
			pos++
		}
	}

	// BFS through trie states, placing them in the double-array.
	bfsQueue := make([]stateID, 0, numTrieStates)
	bfsQueue = append(bfsQueue, startStateID)

	for qi := 0; qi < len(bfsQueue); qi++ {
		trieState := bfsQueue[qi]
		daSlot := daStateMap[trieState]
		children := childrenOf(trieState)

		if len(children) == 0 {
			ra.daBase[daSlot] = 0 // leaf node, base=0 (no children)
			continue
		}

		b := findBase(children)
		ra.daBase[daSlot] = b

		for _, c := range children {
			childDASlot := b + c.alpha
			ra.daCheck[childDASlot] = daSlot
			daStateMap[c.trieState] = childDASlot
			bfsQueue = append(bfsQueue, c.trieState)
		}

		// Advance nextCheckPos past dense regions.
		for nextCheckPos < int32(len(ra.daCheck)) && ra.daCheck[nextCheckPos] != daUnused {
			nextCheckPos++
		}
	}

	// Trim DA arrays to actual used size.
	usedSize := int32(0)
	for i := int32(len(ra.daCheck)) - 1; i >= 0; i-- {
		if ra.daCheck[i] != daUnused || i == 0 {
			usedSize = i + 1
			break
		}
	}
	// Add padding for safety (base + alphaSize could overflow).
	usedSize += ra.alphaSize
	if usedSize > int32(len(ra.daBase)) {
		growDA(usedSize)
	}
	ra.daBase = ra.daBase[:usedSize]
	ra.daCheck = ra.daCheck[:usedSize]

	// Build failure links and outputs indexed by DA slot.
	ra.daFail = make([]int32, usedSize)
	ra.outputOff = make([]int32, usedSize)
	ra.outLen = make([]int32, usedSize)
	for i := range ra.outputOff {
		ra.outputOff[i] = -1
	}

	for trieState := 0; trieState < numTrieStates; trieState++ {
		daSlot := daStateMap[trieState]
		if daSlot < 0 {
			continue
		}

		// Map failure link.
		failTrieState := states[trieState].fail
		failDASlot := daStateMap[failTrieState]
		if failDASlot < 0 {
			failDASlot = ra.rootSlot
		}
		ra.daFail[daSlot] = failDASlot

		// Map outputs.
		if trieOutOff[trieState] >= 0 {
			ra.outputOff[daSlot] = trieOutOff[trieState]
			ra.outLen[daSlot] = trieOutLen[trieState]
			// Set output flag in base.
			ra.daBase[daSlot] |= daOutputFlag
		}
	}
}

// ---------------------------------------------------------------------------
// Search: OverlappingPatternSet (hot path for per-campaign matching)
// ---------------------------------------------------------------------------

// OverlappingPatternSet sets seen[pid] = true for every pattern that appears
// in haystack. The seen slice must have length >= PatternCount().
// This is the zero-allocation hot path for per-campaign keyword matching.
func (ra *RuneAhoCorasick) OverlappingPatternSet(haystack []rune, seen []bool) {
	if ra == nil || ra.patCount == 0 && len(ra.daBase) == 0 {
		return
	}

	n := len(haystack)
	rootSlot := ra.rootSlot

	// Check for empty-pattern output at start.
	if ra.outputOff[rootSlot] >= 0 {
		obase := ra.outputOff[rootSlot]
		ol := ra.outLen[rootSlot]
		for i := int32(0); i < ol; i++ {
			seen[ra.outputs[obase+i]] = true
		}
	}

	if n == 0 {
		return
	}

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune

	daBase := ra.daBase
	daCheck := ra.daCheck
	daFail := ra.daFail

	// Unsafe base pointers for bounds-check-free access.
	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	rtPtr := unsafe.Pointer(unsafe.SliceData(runeTable))
	dbPtr := unsafe.Pointer(unsafe.SliceData(daBase))
	dcPtr := unsafe.Pointer(unsafe.SliceData(daCheck))
	dfPtr := unsafe.Pointer(unsafe.SliceData(daFail))

	state := rootSlot

	for pos := 0; pos < n; pos++ {
		// Load rune without bounds check.
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(pos)*4))

		// Map rune to compact alphabet index.
		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}

		if alpha == 0 {
			// Rune not in any pattern's alphabet → reset to root.
			state = rootSlot
			continue
		}

		// Double-array transition with failure link following.
		for {
			base := *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) & 0x7FFFFFFF
			t := base + alpha
			if *(*int32)(unsafe.Add(dcPtr, uintptr(t)*4)) == state {
				state = t
				break
			}
			if state == rootSlot {
				break // stay at root
			}
			state = *(*int32)(unsafe.Add(dfPtr, uintptr(state)*4))
		}

		// Check output flag (high bit of daBase).
		if *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) < 0 {
			obase := outputOff[state]
			ol := outLen[state]
			for i := int32(0); i < ol; i++ {
				seen[outputs[obase+i]] = true
			}
		}
	}
}

// OverlappingPatternSetTrack is like OverlappingPatternSet but also appends
// the IDs of matched patterns to dirty, returning the updated slice.
// This lets callers clear only the entries that were set, instead of
// resetting the entire seen[] array — critical when PatternCount() is large
// (e.g. 149K patterns in a unified machine) but only a few hundred match.
//
// Usage:
//
//	dirty = machine.OverlappingPatternSetTrack(text, seen, dirty[:0])
//	// ... use seen[] ...
//	for _, id := range dirty { seen[id] = false }
func (ra *RuneAhoCorasick) OverlappingPatternSetTrack(haystack []rune, seen []bool, dirty []PatternID) []PatternID {
	if ra == nil || ra.patCount == 0 && len(ra.daBase) == 0 {
		return dirty
	}

	n := len(haystack)
	rootSlot := ra.rootSlot

	// Check for empty-pattern output at start.
	if ra.outputOff[rootSlot] >= 0 {
		obase := ra.outputOff[rootSlot]
		ol := ra.outLen[rootSlot]
		for i := int32(0); i < ol; i++ {
			pid := ra.outputs[obase+i]
			if !seen[pid] {
				seen[pid] = true
				dirty = append(dirty, pid)
			}
		}
	}

	if n == 0 {
		return dirty
	}

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune

	daBase := ra.daBase
	daCheck := ra.daCheck
	daFail := ra.daFail

	// Unsafe base pointers for bounds-check-free access.
	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	rtPtr := unsafe.Pointer(unsafe.SliceData(runeTable))
	dbPtr := unsafe.Pointer(unsafe.SliceData(daBase))
	dcPtr := unsafe.Pointer(unsafe.SliceData(daCheck))
	dfPtr := unsafe.Pointer(unsafe.SliceData(daFail))

	state := rootSlot

	for pos := 0; pos < n; pos++ {
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(pos)*4))

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}

		if alpha == 0 {
			state = rootSlot
			continue
		}

		for {
			base := *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) & 0x7FFFFFFF
			t := base + alpha
			if *(*int32)(unsafe.Add(dcPtr, uintptr(t)*4)) == state {
				state = t
				break
			}
			if state == rootSlot {
				break
			}
			state = *(*int32)(unsafe.Add(dfPtr, uintptr(state)*4))
		}

		if *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) < 0 {
			obase := outputOff[state]
			ol := outLen[state]
			for i := int32(0); i < ol; i++ {
				pid := outputs[obase+i]
				if !seen[pid] {
					seen[pid] = true
					dirty = append(dirty, pid)
				}
			}
		}
	}

	return dirty
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
	if ra == nil || ra.patCount == 0 && len(ra.daBase) == 0 {
		return dst
	}

	n := len(haystack)
	out := dst
	rootSlot := ra.rootSlot

	outputs := ra.outputs
	outLen := ra.outLen
	outputOff := ra.outputOff
	patLens := ra.patLens

	// Empty-pattern output at start.
	if outputOff[rootSlot] >= 0 {
		obase := outputOff[rootSlot]
		ol := outLen[rootSlot]
		for i := int32(0); i < ol; i++ {
			pid := outputs[obase+i]
			out = append(out, RuneMatch{id: pid, start: 0, end: 0})
		}
	}

	if n == 0 {
		return out
	}

	daBase := ra.daBase
	daCheck := ra.daCheck
	daFail := ra.daFail
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune

	state := rootSlot

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(runeTable[off])
		}

		if alpha == 0 {
			state = rootSlot
			continue
		}

		// DA transition with failure link following.
		for {
			base := daBase[state] & 0x7FFFFFFF
			t := base + alpha
			if t < int32(len(daCheck)) && daCheck[t] == state {
				state = t
				break
			}
			if state == rootSlot {
				break
			}
			state = daFail[state]
		}

		if daBase[state] < 0 { // output flag
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
	if ra == nil || ra.patCount == 0 && len(ra.daBase) == 0 {
		return false
	}

	n := len(haystack)
	rootSlot := ra.rootSlot

	if ra.outputOff[rootSlot] >= 0 {
		return true
	}
	if n == 0 {
		return false
	}

	daBase := ra.daBase
	daCheck := ra.daCheck
	daFail := ra.daFail
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune

	state := rootSlot

	for pos := 0; pos < n; pos++ {
		r := haystack[pos]

		off := uint32(r) - minRune
		alpha := int32(0)
		if off < runeTableLen {
			alpha = int32(runeTable[off])
		}

		if alpha == 0 {
			state = rootSlot
			continue
		}

		for {
			base := daBase[state] & 0x7FFFFFFF
			t := base + alpha
			if t < int32(len(daCheck)) && daCheck[t] == state {
				state = t
				break
			}
			if state == rootSlot {
				break
			}
			state = daFail[state]
		}

		if daBase[state] < 0 {
			return true
		}
	}

	return false
}
