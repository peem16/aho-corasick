package ahocorasick

import (
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

// Runes returns the matching slice of haystack without copying.
func (m RuneMatch) Runes(haystack []rune) []rune { return haystack[m.start:m.end] }

// daOutputFlag is the high bit of a daBase entry. When set, it indicates
// that the DA slot has at least one pattern output, allowing the hot
// path to skip the output check (a random memory load) in the common
// no-match case.
const daOutputFlag int32 = -1 << 31 // 0x80000000

// daUnused is the sentinel value for unused daCheck slots.
const daUnused int32 = -1

// findBaseMaxProbes caps how many free cells findBase scans before giving up and
// placing a node in fresh space at the end of the array. It bounds the per-node
// base search so construction stays near-linear at 100k+ patterns, at the cost
// of a few wasted slots. Tuned empirically (see BenchmarkNewRune_Huge).
const findBaseMaxProbes = 48

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
	// Layout: [base0, check0, fail0, outVecOff0, base1, check1, fail1, ...]
	// 16 bytes per slot. Loading check[t] prefetches base[t] in the same
	// cache line → saves one L3 miss per rune on the next iteration.
	// The 4th field is an offset into outVec, NOT into outputs (-1 = none).
	daVec []int32

	// Length-prefixed output lists for the vec scan paths (built by BuildVec):
	// outVec[off] = count, followed by count pattern IDs (same order as the
	// slot's outputs[] region). The drain reads count and pids from one
	// stream instead of touching the separate outLen array (a cold cache
	// line per matching state on large machines).
	outVec []PatternID
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

// RuneStats holds internal size diagnostics for a RuneAhoCorasick automaton.
type RuneStats struct {
	DASlots   int // total slots allocated in the double-array
	UsedSlots int // slots where daCheck != -1 (or root slot)
	AlphaSize int // compact alphabet size (including 0 = rune not in any pattern)
}

// Stats returns internal sizes for diagnostics.
func (ra *RuneAhoCorasick) Stats() RuneStats {
	if ra == nil {
		return RuneStats{}
	}
	var usedSlots int
	for i := range ra.daCheck {
		if ra.daCheck[i] != daUnused || int32(i) == ra.rootSlot {
			usedSlots++
		}
	}
	return RuneStats{
		DASlots:   len(ra.daBase),
		UsedSlots: usedSlots,
		AlphaSize: int(ra.alphaSize),
	}
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

// BuildVec creates the interleaved DA layout used by the vec scan paths
// (OverlappingPatternSetVecTrack, OverlappingBitsetVecTrack/Buf, and the
// FindOverlappingAllAppend fast path).
// Layout: 4 × int32 per slot = 16 bytes [base, check, fail, outVecOff].
// When the CPU loads check[t], base[t] is in the same cache line.
// On the next iteration (state = t), base[t] is already in L1 — one fewer
// L3 miss per rune compared to the SoA layout.
//
// It also builds outVec, a length-prefixed copy of each output slot's pattern
// IDs ([count, pid...]); the 4th daVec field points into it (-1 = no output),
// so vec drains read count and pids from a single stream instead of loading
// the separate outLen array.
//
// Memory: len(daBase) × 16 bytes for daVec, plus 4 bytes per output entry
// + 4 bytes per output-bearing slot for outVec. For 943K slots → ~14.4 MB
// of daVec.
func (ra *RuneAhoCorasick) BuildVec() {
	n := len(ra.daBase)
	v := make([]int32, n*4)
	// Slice all five sources to [:n] so the compiler proves equal length and
	// hoists their bounds checks out of the loop. daCheck/daFail/outputOff/
	// outLen are all built with len == len(daBase), so these never panic.
	base := ra.daBase[:n]
	check := ra.daCheck[:n]
	fail := ra.daFail[:n]
	outOff := ra.outputOff[:n]
	outLen := ra.outLen[:n]

	outVecSize := 0
	for i := 0; i < n; i++ {
		if outOff[i] >= 0 {
			outVecSize += 1 + int(outLen[i])
		}
	}
	outVec := make([]PatternID, 0, outVecSize)

	for i := 0; i < n; i++ {
		j := i * 4
		v[j] = base[i]
		v[j+1] = check[i]
		v[j+2] = fail[i]
		oo := outOff[i]
		if oo >= 0 {
			ol := outLen[i]
			v[j+3] = int32(len(outVec))
			outVec = append(outVec, PatternID(ol))
			outVec = append(outVec, ra.outputs[oo:oo+ol]...)
		} else {
			v[j+3] = -1
		}
	}
	// outVec is published before daVec: every outVec reader is gated on
	// daVec != nil, so a racy reader that has not yet observed daVec also
	// has no path to outVec. (BuildVec is still a mutator — call it before
	// sharing the machine across goroutines, like BuildDFA.)
	ra.outVec = outVec
	ra.daVec = v
}

// VecMemBytes returns the size in bytes of the tables built by BuildVec
// (interleaved DA + length-prefixed outputs), or 0 if not built.
func (ra *RuneAhoCorasick) VecMemBytes() int64 {
	if ra == nil || ra.daVec == nil {
		return 0
	}
	return int64(len(ra.daVec))*4 + int64(len(ra.outVec))*4
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
	outVec := ra.outVec
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

		// Check output flag (bit 31 of base). The flag guarantees the slot
		// has an outVec entry (offset >= 0), so no inner offset check.
		if *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) < 0 {
			// outVecOff = daVec[state*4 + 3]; outVec[ooff] = count, pids follow.
			ooff := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+12))
			for _, pid := range outVec[ooff+1 : ooff+1+int32(outVec[ooff])] {
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
// Bitset scan: output to []uint64 instead of []bool (8x smaller → fits L1)
// ---------------------------------------------------------------------------

// PatternBitsetWords returns the number of uint64 words needed for the bitset:
// ceil(PatternCount() / 64).
func (ra *RuneAhoCorasick) PatternBitsetWords() int {
	if ra == nil {
		return 0
	}
	return (ra.patCount + 63) / 64
}

// OverlappingBitsetTrack scans haystack and sets bits in the seen[] bitset.
// seen must have length >= PatternBitsetWords(). dirty tracks which word indices
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

	// Root outputs. setBit is inlined (not a closure) so the compiler can
	// keep dirty in a register and inline the append growth; a closure that
	// reassigns the captured dirty slice blocks both and escapes dirty.
	if ra.outputOff[root] >= 0 {
		obase := ra.outputOff[root]
		ol := ra.outLen[root]
		for i := int32(0); i < ol; i++ {
			pid := ra.outputs[obase+i]
			wi := int32(pid / 64)
			bit := uint64(1) << (pid % 64)
			if seen[wi]&bit == 0 {
				seen[wi] |= bit
				dirty = append(dirty, wi)
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
				pid := outputs[obase+i]
				wi := int32(pid / 64)
				bit := uint64(1) << (pid % 64)
				if seen[wi]&bit == 0 {
					seen[wi] |= bit
					dirty = append(dirty, wi)
				}
			}
		}
	}

	return dirty
}

// OverlappingBitsetVecTrack combines the Vec two-phase scan (alpha pre-conversion
// + interleaved DA) with bitset output ([]uint64 + dirty word tracking).
// Must call BuildVec() first. Falls back to OverlappingBitsetTrack if daVec is nil.
//
// For long haystacks (> 1024 runes) this allocates a temporary alpha buffer each
// call; use OverlappingBitsetVecTrackBuf to supply a reusable buffer instead.
func (ra *RuneAhoCorasick) OverlappingBitsetVecTrack(haystack []rune, seen []uint64, dirty []int32) []int32 {
	if ra == nil || ra.patCount == 0 {
		return dirty
	}
	if ra.daVec == nil {
		return ra.OverlappingBitsetTrack(haystack, seen, dirty)
	}
	n := len(haystack)
	var alphaBuf [1024]int32
	var alphas []int32
	if n <= 1024 {
		alphas = alphaBuf[:n]
	} else {
		alphas = make([]int32, n)
	}
	ra.fillAlphas(haystack, alphas)
	return ra.bitsetVecScan(alphas, seen, dirty)
}

// OverlappingBitsetVecTrackBuf is OverlappingBitsetVecTrack with a caller-supplied
// alpha scratch buffer, eliminating the per-call allocation for long haystacks.
// Pass the same scratch across calls; it grows as needed and the (possibly grown)
// slice is returned alongside dirty for reuse:
//
//	dirty, scratch = m.OverlappingBitsetVecTrackBuf(text, seen, dirty[:0], scratch)
func (ra *RuneAhoCorasick) OverlappingBitsetVecTrackBuf(haystack []rune, seen []uint64, dirty []int32, scratch []int32) ([]int32, []int32) {
	if ra == nil || ra.patCount == 0 {
		return dirty, scratch
	}
	if ra.daVec == nil {
		return ra.OverlappingBitsetTrack(haystack, seen, dirty), scratch
	}
	n := len(haystack)
	if cap(scratch) < n {
		scratch = make([]int32, n)
	} else {
		scratch = scratch[:n]
	}
	ra.fillAlphas(haystack, scratch)
	return ra.bitsetVecScan(scratch, seen, dirty), scratch
}

// fillAlphas writes the compact alpha index of each rune of haystack into
// alphas[:len(haystack)] (0 = rune not in any pattern). alphas may hold stale
// data from a reused buffer, so every slot is written.
func (ra *RuneAhoCorasick) fillAlphas(haystack []rune, alphas []int32) {
	n := len(haystack)
	rtPtr := unsafe.Pointer(unsafe.SliceData(ra.runeTable))
	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	minRune := ra.minRune
	rtLen := ra.runeTableLen
	for i := 0; i < n; i++ {
		a := int32(0)
		r := *(*rune)(unsafe.Add(haystackPtr, uintptr(i)*4))
		off := uint32(r) - minRune
		if off < rtLen {
			a = int32(*(*uint16)(unsafe.Add(rtPtr, uintptr(off)*2)))
		}
		alphas[i] = a
	}
}

// bitsetVecScan emits root outputs then runs the interleaved-DA scan over the
// pre-converted alphas, setting bits in seen and tracking dirty words.
func (ra *RuneAhoCorasick) bitsetVecScan(alphas []int32, seen []uint64, dirty []int32) []int32 {
	root := ra.rootSlot

	// Root outputs. setBit is inlined (see OverlappingBitsetTrack) to keep
	// dirty in a register and avoid escaping it through a closure capture.
	if ra.outputOff[root] >= 0 {
		obase := ra.outputOff[root]
		ol := ra.outLen[root]
		for i := int32(0); i < ol; i++ {
			pid := ra.outputs[obase+i]
			wi := int32(pid / 64)
			bit := uint64(1) << (pid % 64)
			if seen[wi]&bit == 0 {
				seen[wi] |= bit
				dirty = append(dirty, wi)
			}
		}
	}

	n := len(alphas)
	if n == 0 {
		return dirty
	}

	outVec := ra.outVec
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

		// base<0 (output flag) guarantees an outVec entry (offset >= 0).
		// outVec[ooff] = count, pids follow: count and pids come from one
		// stream, with no separate outLen load per matching state.
		if *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) < 0 {
			ooff := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+12))
			for _, pid := range outVec[ooff+1 : ooff+1+int32(outVec[ooff])] {
				wi := int32(pid / 64)
				bit := uint64(1) << (pid % 64)
				if seen[wi]&bit == 0 {
					seen[wi] |= bit
					dirty = append(dirty, wi)
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

// PatternRunes returns the i-th pattern as a direct reference — no copy is made.
// The caller must not modify the returned slice; use Pattern() for a safe copy.
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

	// Terminal trie node of each pattern. Full output sets are NOT propagated
	// through per-state slices during the failure-link BFS (that append-growth
	// churn dominated construction at 100k patterns); Phase 3 materializes
	// them directly into the flat ra.outputs array from these.
	termNode := make([]stateID, len(patterns))

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
			termNode[pid] = startStateID
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
				tmpTrans[cur] = runeAddTransTmp(tmpTrans[cur], r, newID)
				next = newID
			}
			cur = next
		}
		termNode[pid] = cur
	}

	// ---- Phase 2: failure links (BFS from depth 1) ----
	queue := make([]stateID, 0, len(states))

	for _, tr := range tmpTrans[startStateID] {
		child := tr.next
		states[child].fail = startStateID
		queue = append(queue, child)
	}

	for qi := 0; qi < len(queue); qi++ {
		cur := queue[qi]
		for _, tr := range tmpTrans[cur] {
			r := tr.r
			child := tr.next

			// Walk the fail chain with one lookup per node (root included);
			// the found node is not looked up a second time.
			fail := states[cur].fail
			var next stateID
			var ok bool
			for {
				next, ok = runeLookupTmp(tmpTrans[fail], r)
				if ok || fail == startStateID {
					break
				}
				fail = states[fail].fail
			}
			if ok && next != child {
				states[child].fail = next
			} else {
				states[child].fail = startStateID
			}

			queue = append(queue, child)
		}
	}

	// ---- Phase 3: materialize full output sets into the flat array ----
	// full(s) = own(s) ∪ full(fail(s)), where own(s) are the patterns whose
	// terminal node is s. Sizes first (fullLen), then each state's region of
	// ra.outputs is filled as a 2-way merge of its own list with the fail
	// state's already-materialized region. A pid never repeats along a fail
	// chain and both merge inputs are sorted ascending, so every region comes
	// out sorted — the same layout (and match emission order) the old
	// propagate-then-sort code produced, without its per-state slice churn.
	numTrieStates := len(states)

	// Own outputs via counting sort over termNode: stable and pid-ascending
	// within each bucket, i.e. every own list is already sorted.
	ownCount := make([]int32, numTrieStates)
	for _, tn := range termNode {
		ownCount[tn]++
	}
	ownOff := make([]int32, numTrieStates+1)
	{
		var acc int32
		for s := 0; s < numTrieStates; s++ {
			ownOff[s] = acc
			acc += ownCount[s]
		}
		ownOff[numTrieStates] = acc
	}
	ownPids := make([]PatternID, len(patterns))
	{
		cursor := make([]int32, numTrieStates)
		copy(cursor, ownOff[:numTrieStates])
		for pid, tn := range termNode {
			ownPids[cursor[tn]] = PatternID(pid)
			cursor[tn]++
		}
	}

	// Full set sizes in BFS order: a fail link always points to a strictly
	// shallower state, so fullLen[fail] is final before fullLen[s] is read.
	// State indices follow trie insertion order, NOT depth — iterating by
	// index here would read unfinished values.
	fullLen := make([]int32, numTrieStates)
	fullLen[startStateID] = ownCount[startStateID]
	for _, s := range queue {
		fullLen[s] = ownCount[s] + fullLen[states[s].fail]
	}

	// Offsets assigned in state-index order: reproduces the exact flat
	// layout of the old append-in-index-order flatten.
	trieOutOff := make([]int32, numTrieStates)
	trieOutLen := fullLen
	total := 0
	for s := 0; s < numTrieStates; s++ {
		if fullLen[s] == 0 {
			trieOutOff[s] = -1
			continue
		}
		trieOutOff[s] = int32(total)
		total += int(fullLen[s])
	}
	ra.outputs = make([]PatternID, total)

	// Materialize in BFS order (fail's region is always filled first).
	// Regions are disjoint slices of ra.outputs, so reading the fail
	// state's region while writing s's is safe.
	fillOutputs := func(s stateID) {
		fl := fullLen[s]
		if fl == 0 {
			return
		}
		dst := ra.outputs[trieOutOff[s] : trieOutOff[s]+fl]
		own := ownPids[ownOff[s]:ownOff[s+1]]
		f := states[s].fail
		if fullLen[f] == 0 {
			copy(dst, own)
			return
		}
		inh := ra.outputs[trieOutOff[f] : trieOutOff[f]+fullLen[f]]
		if len(own) == 0 {
			copy(dst, inh)
			return
		}
		i, j, k := 0, 0, 0
		for i < len(own) && j < len(inh) {
			if own[i] < inh[j] {
				dst[k] = own[i]
				i++
			} else {
				dst[k] = inh[j]
				j++
			}
			k++
		}
		k += copy(dst[k:], own[i:])
		copy(dst[k:], inh[j:])
	}
	fillOutputs(startStateID)
	for _, s := range queue {
		fillOutputs(s)
	}

	// ---- Phase 4: build compact rune alphabet ----
	ra.buildRuneAlphabet(patterns)

	// ---- Phase 5: build double-array trie ----
	ra.buildDoubleArray(states, tmpTrans, trieOutOff, trieOutLen)

	// ---- Phase 6: deep copy patterns & cache lengths ----
	// Patterns share a single rune backing array instead of one allocation per
	// pattern (N allocs → 1). Each ra.patterns[i] is capacity-bounded so a
	// caller appending to a PatternRunes() result cannot corrupt its neighbour.
	ra.patCount = len(patterns)
	totalRunes := 0
	for _, p := range patterns {
		totalRunes += len(p)
	}
	backing := make([]rune, totalRunes)
	ra.patterns = make([][]rune, len(patterns))
	ra.patLens = make([]int32, len(patterns))
	off := 0
	for i, p := range patterns {
		m := copy(backing[off:], p)
		ra.patterns[i] = backing[off : off+m : off+m]
		ra.patLens[i] = int32(m)
		off += m
	}

	return ra
}

// ---------------------------------------------------------------------------
// Builder helpers
// ---------------------------------------------------------------------------

func runeLookupTmp(tr []runeNFATrans, r rune) (stateID, bool) {
	// Most trie nodes have few children; a linear scan with an early sorted
	// exit beats binary search (no division, better branch prediction) for
	// short lists. Falls back to binary search for wide nodes (near the root).
	if len(tr) <= 8 {
		for i := 0; i < len(tr); i++ {
			if tr[i].r == r {
				return tr[i].next, true
			}
			if tr[i].r > r {
				return 0, false
			}
		}
		return 0, false
	}
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
	// Presence-array alphabet build: O(total + range) with a single allocation
	// (runeTable itself), no map and no sort. Beats both the old map[rune]bool
	// (allocation + hashing — costly for the many tiny per-campaign machines)
	// and a sort-all-runes approach (which sorts every duplicated rune — costly
	// for large pattern sets that share a small alphabet).
	minR, maxR := rune(0), rune(0)
	first := true
	for _, pat := range patterns {
		for _, r := range pat {
			if first {
				minR, maxR = r, r
				first = false
			} else if r < minR {
				minR = r
			} else if r > maxR {
				maxR = r
			}
		}
	}
	if first { // no runes in any pattern
		ra.alphaSize = 1
		return
	}

	ra.minRune = uint32(minR)
	rangeSize := int(maxR-minR) + 1
	ra.runeTable = make([]uint16, rangeSize)
	ra.runeTableLen = uint32(rangeSize)

	// Mark present runes (temporary marker), then renumber ascending so each
	// distinct rune gets a stable 1-based alphabet index in rune order.
	for _, pat := range patterns {
		for _, r := range pat {
			ra.runeTable[r-minR] = 1
		}
	}
	idx := uint16(1)
	for i := range ra.runeTable {
		if ra.runeTable[i] != 0 {
			ra.runeTable[i] = idx
			idx++
		}
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

// fillInt32 sets every element of s to v. It seeds the first element and then
// doubles the filled region with copy (memmove), which the runtime vectorizes —
// far faster than a scalar loop for the large -1/daUnused init arrays in
// double-array construction (no memset-to-nonzero primitive exists in Go).
func fillInt32(s []int32, v int32) {
	if len(s) == 0 {
		return
	}
	s[0] = v
	for i := 1; i < len(s); i *= 2 {
		copy(s[i:], s[:i])
	}
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
	// One scratch buffer serves every childrenOf call: the returned slice is
	// fully consumed within a single BFS iteration below (findBase + child
	// placement) and never retained, so reuse is safe and saves one heap
	// allocation per trie state (~hundreds of thousands at 100k patterns).
	childBuf := make([]alphaChild, 0, 64)
	childrenOf := func(trieState stateID) []alphaChild {
		trans := tmpTrans[trieState]
		children := childBuf[:0]
		for _, tr := range trans {
			a := runeToAlpha(tr.r)
			if a > 0 {
				children = append(children, alphaChild{alpha: a, trieState: tr.next})
			}
		}
		childBuf = children
		return children
	}

	// Initial DA size estimate. We'll grow as needed.
	daSize := int32(numTrieStates * 2)
	if daSize < int32(ra.alphaSize)*2 {
		daSize = int32(ra.alphaSize) * 2
	}
	ra.daBase = make([]int32, daSize)
	ra.daCheck = make([]int32, daSize)
	fillInt32(ra.daCheck, daUnused)

	// Map trie state → DA slot.
	daStateMap := make([]int32, numTrieStates)
	fillInt32(daStateMap, -1)

	// Root at DA slot 0.
	ra.rootSlot = 0
	daStateMap[startStateID] = 0
	ra.daCheck[0] = 0 // root's check = self (sentinel)

	// Doubly-linked free-cell list over slots [1, daSize). Slot 0 is the root,
	// never free. Invariant: a cell is in this list iff daCheck[cell]==daUnused.
	// findBase draws candidate bases only from free cells, so it skips the long
	// runs of occupied cells that make a plain linear probe O(n²) at scale —
	// the dominant construction cost at 100k+ patterns. prevFree/nextFree are
	// construction-only scratch (discarded), so the built machine is unchanged.
	prevFree := make([]int32, daSize)
	nextFree := make([]int32, daSize)
	var freeHead, freeTail int32 = -1, -1
	if daSize > 1 {
		for i := int32(1); i < daSize; i++ {
			prevFree[i] = i - 1
			nextFree[i] = i + 1
		}
		prevFree[1] = -1
		nextFree[daSize-1] = -1
		freeHead = 1
		freeTail = daSize - 1
	}

	// growDA grows the arrays if needed and splices the new cells onto the
	// free-list tail (keeping the list index-ordered).
	growDA := func(minSize int32) {
		old := int32(len(ra.daBase))
		if minSize <= old {
			return
		}
		newSize := old * 2
		if newSize < minSize {
			newSize = minSize
		}
		newBase := make([]int32, newSize)
		copy(newBase, ra.daBase)
		ra.daBase = newBase

		newCheck := make([]int32, newSize)
		fillInt32(newCheck[old:], daUnused)
		copy(newCheck, ra.daCheck)
		ra.daCheck = newCheck

		newPrev := make([]int32, newSize)
		copy(newPrev, prevFree)
		prevFree = newPrev
		newNext := make([]int32, newSize)
		copy(newNext, nextFree)
		nextFree = newNext

		for i := old; i < newSize; i++ {
			prevFree[i] = i - 1
			nextFree[i] = i + 1
		}
		nextFree[newSize-1] = -1
		if freeTail == -1 {
			freeHead = old
			prevFree[old] = -1
		} else {
			nextFree[freeTail] = old
			prevFree[old] = freeTail
		}
		freeTail = newSize - 1
	}

	// unlink removes a now-occupied cell from the free list.
	unlink := func(cell int32) {
		pr := prevFree[cell]
		nx := nextFree[cell]
		if pr != -1 {
			nextFree[pr] = nx
		} else {
			freeHead = nx
		}
		if nx != -1 {
			prevFree[nx] = pr
		} else {
			freeTail = pr
		}
	}

	// Bump allocator for capped placements (see findBase). Hands out cells from
	// an exclusive fresh region that is detached from the free list, so normal
	// findBase never collides with bump-placed nodes and bump-placed cells need
	// no unlink. Wastes the gaps between child alphas — the time-for-space part
	// of the cedar/darts trade.
	var bumpPtr, bumpEnd int32
	ensureBump := func(span int32) {
		if bumpEnd != 0 && bumpPtr+span <= bumpEnd {
			return
		}
		old := int32(len(ra.daBase))
		chunk := old / 2
		if chunk < span+1024 {
			chunk = span + 1024
		}
		savedTail := freeTail
		growDA(old + chunk)
		// Detach the freshly-appended block [old, len) from the free list.
		if savedTail == -1 {
			freeHead, freeTail = -1, -1
		} else {
			nextFree[savedTail] = -1
			freeTail = savedTail
		}
		bumpPtr, bumpEnd = old, int32(len(ra.daBase))
	}

	// findBase finds a base b such that daCheck[b+alpha]==daUnused for every
	// child alpha. Candidate bases come from the free list: each free cell p is
	// tried as the home of children[0] (b = p-firstAlpha, free by construction),
	// so only the remaining children are verified. The list is index-ordered, so
	// once p>=firstAlpha it only grows.
	//
	// To keep construction near-linear at 100k+ patterns, the free-cell probe is
	// capped (findBaseMaxProbes): a node that does not fit within the cap is
	// bump-allocated in exclusive fresh space instead of scanning the whole free
	// list. This is the cedar/darts time-for-space trade — it leaves a few empty
	// slots but turns the worst-case O(n²) probe into bounded work per node.
	// Returns (base, bumped); when bumped the caller must NOT unlink the children
	// (they are not in the free list).
	findBase := func(children []alphaChild) (int32, bool) {
		if len(children) == 0 {
			return int32(0), false
		}
		firstAlpha := children[0].alpha
		lastAlpha := children[len(children)-1].alpha

		p := freeHead
		for p != -1 && p < firstAlpha { // skip cells too small (negative base)
			p = nextFree[p]
		}
		for probes := 0; p != -1 && probes < findBaseMaxProbes; probes++ {
			b := p - firstAlpha
			growDA(b + lastAlpha + 1)
			ok := true
			for ci := 1; ci < len(children); ci++ {
				if ra.daCheck[b+children[ci].alpha] != daUnused {
					ok = false
					break
				}
			}
			if ok {
				return b, false
			}
			p = nextFree[p]
		}
		// Free list exhausted or probe cap hit: bump-allocate in fresh space.
		ensureBump(lastAlpha - firstAlpha + 1)
		b := bumpPtr - firstAlpha
		bumpPtr += lastAlpha - firstAlpha + 1
		return b, true
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

		b, bumped := findBase(children)
		ra.daBase[daSlot] = b

		for _, c := range children {
			childDASlot := b + c.alpha
			ra.daCheck[childDASlot] = daSlot
			if !bumped {
				unlink(childDASlot) // now occupied; remove from the free list
			}
			daStateMap[c.trieState] = childDASlot
			bfsQueue = append(bfsQueue, c.trieState)
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
	fillInt32(ra.outputOff, -1)

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
	return ra.FindOverlappingAllAppend(make([]RuneMatch, 0, 16), haystack)
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

	// Interleaved-DA fast path when BuildVec has been called: same prefetch
	// win as the bitset vec scan (check[t] pulls base[t] into L1), which
	// matters on large machines whose DA tables exceed cache.
	if ra.daVec != nil {
		return ra.findOverlappingAllVec(out, haystack)
	}

	daBase := ra.daBase
	daCheck := ra.daCheck
	daFail := ra.daFail
	runeTable := ra.runeTable
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune

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

		// DA transition with failure link following. base+alpha is always in
		// bounds thanks to the alphaSize padding appended at the end of
		// buildDoubleArray, so no length guard is needed (matches
		// OverlappingBitsetTrack).
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

		if *(*int32)(unsafe.Add(dbPtr, uintptr(state)*4)) < 0 { // output flag
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

// findOverlappingAllVec is the FindOverlappingAllAppend hot loop on the
// interleaved DA (daVec) with length-prefixed outputs (outVec). Single-phase:
// runes are converted to alphas inline (no alpha scratch buffer) so the
// Append API stays zero-alloc on haystacks of any length. The caller has
// already emitted root (empty-pattern) outputs and checked n > 0.
func (ra *RuneAhoCorasick) findOverlappingAllVec(out []RuneMatch, haystack []rune) []RuneMatch {
	n := len(haystack)
	rootSlot := ra.rootSlot
	outVec := ra.outVec
	patLens := ra.patLens
	runeTableLen := ra.runeTableLen
	minRune := ra.minRune

	haystackPtr := unsafe.Pointer(unsafe.SliceData(haystack))
	rtPtr := unsafe.Pointer(unsafe.SliceData(ra.runeTable))
	vecPtr := unsafe.Pointer(unsafe.SliceData(ra.daVec))

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

		// DA transition with fail chain on the interleaved layout: loading
		// check[t] (slot t, byte offset +4) brings base[t] into the same
		// cache line, saving one L3 miss per rune on large machines.
		for {
			base := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) & 0x7FFFFFFF
			t := base + alpha
			if *(*int32)(unsafe.Add(vecPtr, uintptr(t)*16+4)) == state {
				state = t
				break
			}
			if state == rootSlot {
				break
			}
			state = *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+8))
		}

		// base<0 (output flag) guarantees an outVec entry (offset >= 0).
		// outVec[ooff] = count, pids follow.
		if *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16)) < 0 {
			ooff := *(*int32)(unsafe.Add(vecPtr, uintptr(state)*16+12))
			end := pos + 1
			for _, pid := range outVec[ooff+1 : ooff+1+int32(outVec[ooff])] {
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
