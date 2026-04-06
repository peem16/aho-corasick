package ahocorasick

// ---------------------------------------------------------------------------
// DFA — full 256-wide transition table
// ---------------------------------------------------------------------------
//
// Memory layout:
//   trans[s*256 + b] = next state for state s on byte b
//
// This gives O(1) transition lookup with sequential memory access
// in the search loop, which is very cache-friendly.
//
// Construction cost: O(|NFA_states| × 256) time and memory.
// Search cost:       O(n) — no failure link traversal needed.

// dfa wraps a pre-computed, dense transition table built from an n.
//
// Outputs are stored in a flat buffer to reduce per-state allocations and
// improve cache locality when iterating over matches.
type dfa struct {
	trans     []stateID   // flat: numStates * 256 entries
	outBuf    []PatternID // flat output buffer: all pattern IDs concatenated
	outBase   []int32     // per-state base index into outBuf; -1 = no match
	outLen    []int32     // per-state output count
	matchKind MatchKind
	numStates int
	alphabet  [256]byte
	useAlpha  bool
}

// ---------------------------------------------------------------------------
// automaton interface
// ---------------------------------------------------------------------------

func (d *dfa) startState() stateID { return startStateID }

func (d *dfa) isDead(s stateID) bool { return s == deadStateID }

func (d *dfa) matchKindOf() MatchKind { return d.matchKind }

func (d *dfa) isMatch(s stateID) bool { return d.outBase[s] >= 0 }

func (d *dfa) matches(s stateID) []PatternID {
	base := d.outBase[s]
	if base < 0 {
		return nil
	}
	return d.outBuf[base : base+d.outLen[s]]
}

// nextState returns the next state from s on byte b.
// The transition table already encodes failure links, so no loop is needed.
//
//go:nosplit
func (d *dfa) nextState(s stateID, b byte) stateID {
	if d.useAlpha {
		b = d.alphabet[b]
	}
	// BCE hint: if s is valid, s*256+255 is within bounds.
	idx := int(s)<<8 | int(b)
	return d.trans[idx]
}

// ---------------------------------------------------------------------------
// DFA construction from NFA
// ---------------------------------------------------------------------------

// buildDFA converts a fully-built NFA into a DFA by precomputing all
// 256 transitions for every state, following failure links eagerly.
//
// This is NOT subset-construction: the DFA has the same number of states
// as the NFA.  For each (state, byte) pair, we walk the failure chain
// until we find a defined goto or reach the start state.
func buildDFA(n *nfa) *dfa {
	numStates := len(n.states)
	d := &dfa{
		trans:     make([]stateID, numStates*256),
		outBase:   make([]int32, numStates),
		outLen:    make([]int32, numStates),
		matchKind: n.matchKind,
		numStates: numStates,
		alphabet:  n.alphabet,
		useAlpha:  n.useAlpha,
	}

	// Initialise all states as non-matching.
	for s := range d.outBase {
		d.outBase[s] = -1
	}

	// Populate outputs into a flat buffer to reduce allocations and
	// improve cache locality — avoids one heap object per matching state.
	numOut := 0
	for s := 0; s < numStates; s++ {
		numOut += len(n.matches(stateID(s)))
	}
	d.outBuf = make([]PatternID, 0, numOut)
	for s := 0; s < numStates; s++ {
		outs := n.matches(stateID(s))
		if len(outs) == 0 {
			continue
		}
		d.outBase[s] = int32(len(d.outBuf))
		d.outLen[s] = int32(len(outs))
		d.outBuf = append(d.outBuf, outs...)
	}

	// Populate transitions using failure-link inheritance in BFS order.
	// For each state s with failure link f:
	//   DFA[s][b] = DFA[f][b]  for bytes where s has no direct transition
	//   DFA[s][b] = child      for bytes where s has a direct trie transition
	// Since f is always at lesser depth, BFS ensures f is already computed.
	// This is O(S × 256) instead of O(S × 256 × depth).

	transBuf := n.transBuf
	transBase := n.transBase
	transLen := n.transLen

	// Dead state (0): all transitions stay at 0 (zeroed by make).

	// Start state: copy from NFA's precomputed startTrans.
	startBase := int(startStateID) << 8
	for b := 0; b < 256; b++ {
		d.trans[startBase|b] = n.startTrans[b]
	}

	// BFS from start's children.
	visited := make([]bool, numStates)
	visited[deadStateID] = true
	visited[startStateID] = true

	queue := make([]stateID, 0, numStates-2)

	// Enqueue start state's trie children.
	stBase := int(transBase[startStateID])
	stLen := int(transLen[startStateID])
	for i := 0; i < stLen; i++ {
		child := transBuf[stBase+i].next
		if child != deadStateID && !visited[child] {
			visited[child] = true
			queue = append(queue, child)
		}
	}

	for qi := 0; qi < len(queue); qi++ {
		s := queue[qi]
		sBase := int(s) << 8
		fail := n.states[s].fail
		failBase := int(fail) << 8

		// Copy failure state's DFA row (O(256) memcpy).
		copy(d.trans[sBase:sBase+256], d.trans[failBase:failBase+256])

		// Overwrite with s's direct NFA transitions and enqueue children.
		tb := int(transBase[s])
		tl := int(transLen[s])
		for i := 0; i < tl; i++ {
			tr := &transBuf[tb+i]
			d.trans[sBase|int(tr.b)] = tr.next
			if tr.next != deadStateID && !visited[tr.next] {
				visited[tr.next] = true
				queue = append(queue, tr.next)
			}
		}
	}

	return d
}
