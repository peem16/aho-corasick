package ahocorasick

import (
	"sort"
)

// ---------------------------------------------------------------------------
// NFA state & transition types
// ---------------------------------------------------------------------------

// nfaTrans is a single (byte → state) transition entry.
// Kept sorted by b so we can binary-search in the hot path.
type nfaTrans struct {
	b    byte
	_    [3]byte  // padding — keeps stateID 4-byte aligned
	next stateID
}

// nfaState holds the per-state metadata for the NFA.
// Fields are ordered to keep the hottest search-path fields (fail, outputIdx)
// packed into 8 bytes so that more states fit per cache line.
// depth is intentionally excluded: it is only needed during build and is
// passed as a separate temporary slice to avoid polluting the search cache.
type nfaState struct {
	fail      stateID // failure (fall-back) link
	outputIdx int32   // index into NFA.outputBase; -1 = no match
}

// NFA is an Aho-Corasick Non-deterministic Finite Automaton.
//
// Memory layout
//   - states     : flat []nfaState  — O(S) where S = number of states
//   - transBuf   : flat []nfaTrans  — all transitions concatenated for cache locality
//   - transBase  : []int32          — per-state offset into transBuf
//   - transLen   : []int32          — per-state transition count
//   - outputs    : flat []PatternID — all output pattern lists concatenated
//   - outLen     : []int32          — per-state output count
//   - startTrans : [256]stateID     — precomputed dense table for start state
//   - denseTrans : []stateID        — precomputed dense tables for shallow states
//   - denseIdx   : []int32          — per-state index into denseTrans (-1 = sparse)
//
// States at depth ≤ denseDepth get precomputed 256-entry dense transition
// tables (like a partial DFA). This eliminates binary search and failure-link
// traversal for the most frequently visited states while keeping memory
// usage proportional to the number of shallow states, not all states.
//
// Note: nfaState deliberately omits the trie depth field.  Depth is only
// needed during construction (buildDenseTrans) and is passed as a temporary
// []uint16 to avoid inflating the hot struct and wasting cache capacity.
type NFA struct {
	states     []nfaState
	transBuf   []nfaTrans  // all transitions concatenated
	transBase  []int32     // transBase[stateID] = start index in transBuf
	transLen   []int32     // transLen[stateID] = number of transitions
	outputs    []PatternID // all output pattern IDs, concatenated
	outLen     []int32     // outLen[stateID] = number of outputs for that state
	startTrans [256]stateID // precomputed transitions from start state
	denseTrans []stateID   // dense tables for shallow states (256 entries each)
	denseIdx   []int32     // denseIdx[stateID] = base index in denseTrans; -1 = sparse
	matchKind  MatchKind
	// alphabet maps raw bytes to (possibly normalised) bytes.
	// Used for ASCII case-insensitive matching.
	alphabet [256]byte
	useAlpha bool // true when alphabet is non-identity
}

// ---------------------------------------------------------------------------
// automaton interface implementation
// ---------------------------------------------------------------------------

func (n *NFA) startState() stateID { return startStateID }

func (n *NFA) isDead(s stateID) bool { return s == deadStateID }

func (n *NFA) matchKindOf() MatchKind { return n.matchKind }

func (n *NFA) isMatch(s stateID) bool {
	return n.states[s].outputIdx >= 0
}

// matches returns the pattern IDs output at state s (possibly nil).
func (n *NFA) matches(s stateID) []PatternID {
	st := &n.states[s]
	if st.outputIdx < 0 {
		return nil
	}
	base := int32(st.outputIdx)
	length := n.outLen[s]
	return n.outputs[base : base+length]
}

// nextState walks one transition from state s on byte b, following
// failure links until a transition is found (or we reach start/dead).
//
//go:nosplit
func (n *NFA) nextState(s stateID, b byte) stateID {
	if n.useAlpha {
		b = n.alphabet[b]
	}
	// Fast path: start state uses precomputed dense table.
	if s == startStateID {
		return n.startTrans[b]
	}
	// Fast path: shallow states with precomputed dense tables.
	if di := n.denseIdx[s]; di >= 0 {
		return n.denseTrans[int(di)<<8|int(b)]
	}
	for {
		// Dead state is a sink — stays dead.
		if s == deadStateID {
			return deadStateID
		}
		if next, ok := n.lookup(s, b); ok {
			return next
		}
		// Failure link reached start → use dense table.
		if n.states[s].fail == startStateID {
			return n.startTrans[b]
		}
		s = n.states[s].fail
	}
}

// lookup performs a binary search for byte b in the flattened transition
// buffer of state s.
//
//go:nosplit
func (n *NFA) lookup(s stateID, b byte) (stateID, bool) {
	base := int(n.transBase[s])
	length := int(n.transLen[s])
	tr := n.transBuf[base : base+length]
	lo, hi := 0, length
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if tr[mid].b < b {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < length && tr[lo].b == b {
		return tr[lo].next, true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// NFA builder
// ---------------------------------------------------------------------------

// buildNFA constructs an Aho-Corasick NFA from patterns.
// patterns must not be empty (validated by the caller).
func buildNFA(patterns [][]byte, mk MatchKind, alphabet [256]byte, useAlpha bool, denseDepth int) *NFA {
	n := &NFA{
		matchKind: mk,
		alphabet:  alphabet,
		useAlpha:  useAlpha,
	}

	// During build we use a temporary [][]nfaTrans per state,
	// then flatten into contiguous transBuf at the end.
	tmpTrans := make([][]nfaTrans, 2)

	// tmpDepths tracks trie depth per state during construction.
	// Kept separate from nfaState to keep the hot struct small (8 bytes).
	tmpDepths := make([]uint16, 2)

	// ---- Phase 1: build trie (goto function) ----
	// Reserve state 0 (dead) and state 1 (start).
	n.states = make([]nfaState, 2)
	n.states[0].outputIdx = -1
	n.states[1].outputIdx = -1

	// We accumulate outputs in a temporary slice-of-slice, then flatten later.
	tmpOutputs := make([][]PatternID, 2) // indexed by stateID

	for pid, pat := range patterns {
		if len(pat) == 0 {
			// Empty pattern matches at every position; attach to start.
			tmpOutputs[startStateID] = append(tmpOutputs[startStateID], PatternID(pid))
			continue
		}
		cur := startStateID
		for depth, raw := range pat {
			b := raw
			if useAlpha {
				b = alphabet[b]
			}
			next, ok := lookupTmp(tmpTrans[cur], b)
			if !ok {
				// Allocate a new state.
				newID := stateID(len(n.states))
				n.states = append(n.states, nfaState{outputIdx: -1})
				tmpDepths = append(tmpDepths, uint16(depth+1))
				tmpTrans = append(tmpTrans, nil)
				tmpOutputs = append(tmpOutputs, nil)
				tmpTrans[cur] = addTransTmp(tmpTrans[cur], b, newID)
				next = newID
			}
			cur = next
		}
		tmpOutputs[cur] = append(tmpOutputs[cur], PatternID(pid))
	}

	// ---- Phase 2: build failure links (BFS from depth 1) ----
	// Also propagate output sets from failure states.
	queue := make([]stateID, 0, len(n.states))

	// Initialise depth-1 states: their failure link is start.
	for _, tr := range tmpTrans[startStateID] {
		child := tr.next
		n.states[child].fail = startStateID
		queue = append(queue, child)
		// Inherit outputs from start if start has outputs.
		if len(tmpOutputs[startStateID]) > 0 {
			tmpOutputs[child] = append(tmpOutputs[child], tmpOutputs[startStateID]...)
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, tr := range tmpTrans[cur] {
			b := tr.b
			child := tr.next

			// Walk failure links to find the longest proper suffix that
			// has a transition on b.
			fail := n.states[cur].fail
			for fail != startStateID {
				if _, ok := lookupTmp(tmpTrans[fail], b); ok {
					break
				}
				fail = n.states[fail].fail
			}
			if next, ok := lookupTmp(tmpTrans[fail], b); ok && next != child {
				n.states[child].fail = next
			} else {
				n.states[child].fail = startStateID
			}

			// Inherit outputs from failure state.
			failState := n.states[child].fail
			if len(tmpOutputs[failState]) > 0 {
				tmpOutputs[child] = append(tmpOutputs[child], tmpOutputs[failState]...)
			}

			queue = append(queue, child)
		}
	}

	// ---- Phase 3: handle leftmost semantics ----
	// For LeftmostFirst/LeftmostLongest we need to add dead-state
	// transitions so that the search stops extending once a match is
	// found and we've passed any possible longer/earlier match.
	if mk == MatchKindLeftmostFirst || mk == MatchKindLeftmostLongest {
		addDeadTransitions(n.states, tmpTrans, tmpOutputs)
	}

	// ---- Phase 4: flatten transitions into contiguous buffer ----
	n.flattenTransitions(tmpTrans)

	// ---- Phase 5: precompute start state dense transition table ----
	n.buildStartTrans()

	// ---- Phase 6: precompute dense tables for shallow states ----
	n.buildDenseTrans(uint16(denseDepth), tmpDepths)

	// ---- Phase 7: flatten output table ----
	n.flattenOutputs(tmpOutputs)

	return n
}

// lookupTmp performs a binary search for byte b in a temporary transition slice.
func lookupTmp(tr []nfaTrans, b byte) (stateID, bool) {
	lo, hi := 0, len(tr)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if tr[mid].b < b {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(tr) && tr[lo].b == b {
		return tr[lo].next, true
	}
	return 0, false
}

// addTransTmp inserts (b → next) into a sorted transition slice and returns it.
func addTransTmp(tr []nfaTrans, b byte, next stateID) []nfaTrans {
	lo, hi := 0, len(tr)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if tr[mid].b < b {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	tr = append(tr, nfaTrans{})
	copy(tr[lo+1:], tr[lo:])
	tr[lo] = nfaTrans{b: b, next: next}
	return tr
}

// addDeadTransitions modifies the trie so that states which are
// "in the middle" of matching a longer pattern don't report a shorter
// sub-match prematurely (LeftmostFirst) or continue past a finished
// match (LeftmostLongest).
//
// Optimized: O(256) merge instead of O(n²) repeated sorted insertion.
// Since existing transitions are already sorted, we merge-scan in one pass
// and build a complete 256-entry list directly.
func addDeadTransitions(states []nfaState, tmpTrans [][]nfaTrans, tmpOutputs [][]PatternID) {
	for s := stateID(1); int(s) < len(states); s++ {
		if len(tmpOutputs[s]) == 0 {
			continue
		}
		existing := tmpTrans[s]
		if len(existing) == 256 {
			// Already has transitions for every byte — nothing to add.
			continue
		}
		// Build a full 256-entry sorted transition list by merging existing
		// (sorted) transitions with dead-state fillers, in O(256) time.
		newTrans := make([]nfaTrans, 0, 256)
		ei := 0
		for b := 0; b < 256; b++ {
			bb := byte(b)
			if ei < len(existing) && existing[ei].b == bb {
				newTrans = append(newTrans, existing[ei])
				ei++
			} else {
				newTrans = append(newTrans, nfaTrans{b: bb, next: deadStateID})
			}
		}
		tmpTrans[s] = newTrans
	}
}

// flattenTransitions packs all per-state transition slices into a single
// contiguous buffer for better cache locality during search.
func (n *NFA) flattenTransitions(tmpTrans [][]nfaTrans) {
	numStates := len(n.states)
	n.transBase = make([]int32, numStates)
	n.transLen = make([]int32, numStates)

	total := 0
	for _, tr := range tmpTrans {
		total += len(tr)
	}
	n.transBuf = make([]nfaTrans, 0, total)

	for s := 0; s < numStates; s++ {
		tr := tmpTrans[s]
		n.transBase[s] = int32(len(n.transBuf))
		n.transLen[s] = int32(len(tr))
		n.transBuf = append(n.transBuf, tr...)
	}
}

// buildStartTrans precomputes the dense 256-entry transition table for the
// start state. This eliminates binary search and failure-link traversal for
// the most frequently visited state.
func (n *NFA) buildStartTrans() {
	// For each byte, compute what nextState(startStateID, b) would return.
	// Start state: if there's a direct transition, use it; otherwise stay at start.
	for b := 0; b < 256; b++ {
		bb := byte(b)
		if n.useAlpha {
			bb = n.alphabet[bb]
		}
		if next, ok := n.lookup(startStateID, bb); ok {
			n.startTrans[b] = next
		} else {
			n.startTrans[b] = startStateID
		}
	}
}

// buildDenseTrans precomputes 256-entry dense transition tables for states
// at depth ≤ maxDepth (excluding start state, which has its own table).
// This turns binary-search + failure-link traversal into a single O(1) lookup
// for the most frequently visited states.
func (n *NFA) buildDenseTrans(maxDepth uint16, depths []uint16) {
	numStates := stateID(len(n.states))
	n.denseIdx = make([]int32, numStates)
	for i := range n.denseIdx {
		n.denseIdx[i] = -1
	}

	// Adaptive: reduce maxDepth if memory would exceed 2MB budget.
	// O(S) single pass: count states per depth, then prefix-sum.
	const maxDenseBytes = 2 << 20 // 2MB
	const maxTrackedDepth = 256   // depths beyond this are never densified
	var cumByDepth [maxTrackedDepth + 1]int32
	for s := stateID(2); s < numStates; s++ {
		d := depths[s]
		if d <= maxTrackedDepth {
			cumByDepth[d]++
		}
	}
	// Convert to prefix sums: cumByDepth[d] = #states at depth ≤ d.
	for d := 1; d <= maxTrackedDepth; d++ {
		cumByDepth[d] += cumByDepth[d-1]
	}
	// Find the largest depth ≤ maxDepth that fits in budget.
	for maxDepth > 0 && int(cumByDepth[maxDepth])*256*4 > maxDenseBytes {
		maxDepth--
	}

	denseCount := int(cumByDepth[maxDepth])
	if denseCount == 0 {
		return
	}

	n.denseTrans = make([]stateID, denseCount*256)
	idx := int32(0)
	for s := stateID(2); s < numStates; s++ {
		if depths[s] > maxDepth {
			continue
		}
		n.denseIdx[s] = idx
		base := int(idx) << 8
		for b := 0; b < 256; b++ {
			bb := byte(b)
			if n.useAlpha {
				bb = n.alphabet[bb]
			}
			// Simulate nextState(s, bb) without the dense table shortcut.
			cur := s
			for {
				if cur == deadStateID {
					n.denseTrans[base+b] = deadStateID
					break
				}
				if next, ok := n.lookup(cur, bb); ok {
					n.denseTrans[base+b] = next
					break
				}
				if n.states[cur].fail == startStateID {
					n.denseTrans[base+b] = n.startTrans[b]
					break
				}
				cur = n.states[cur].fail
			}
		}
		idx++
	}
}

// flattenOutputs converts the per-state slice-of-slices into flat arrays.
func (n *NFA) flattenOutputs(tmp [][]PatternID) {
	numStates := len(n.states)
	n.outLen = make([]int32, numStates)

	// Count total outputs.
	total := 0
	for _, outs := range tmp {
		total += len(outs)
	}
	n.outputs = make([]PatternID, 0, total)

	for s := 0; s < numStates; s++ {
		outs := tmp[s]
		if len(outs) == 0 {
			n.states[s].outputIdx = -1
			continue
		}
		// Sort for determinism (LeftmostFirst expects lowest PatternID first).
		sort.Slice(outs, func(i, j int) bool { return outs[i] < outs[j] })
		n.states[s].outputIdx = int32(len(n.outputs))
		n.outLen[s] = int32(len(outs))
		n.outputs = append(n.outputs, outs...)
	}
}

// ---------------------------------------------------------------------------
// NFA search helpers used by ahocorasick.go
// ---------------------------------------------------------------------------

// stepNFA advances state s on byte b, applying the alphabet normalisation
// and following failure links.  Exported as a method so the search loops
// in ahocorasick.go can call it directly for readability.
//
//go:nosplit
func (n *NFA) step(s stateID, b byte) stateID {
	return n.nextState(s, b)
}

// firstMatchAt returns the first PatternID output at state s along with
// its start offset (end is the caller's responsibility).
func (n *NFA) firstMatchAt(s stateID) PatternID {
	return n.outputs[n.states[s].outputIdx]
}
