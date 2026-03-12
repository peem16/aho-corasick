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

// DFA wraps a pre-computed, dense transition table built from an NFA.
type DFA struct {
	trans     []stateID   // flat: numStates * 256 entries
	outputs   []PatternID // all output pattern IDs, concatenated (flat)
	outBase   []int32     // outBase[stateID] = start in outputs; -1 = no match
	outLen    []int32     // outLen[stateID] = number of outputs for that state
	matchKind MatchKind
	numStates int
	alphabet  [256]byte
	useAlpha  bool
}

// ---------------------------------------------------------------------------
// automaton interface
// ---------------------------------------------------------------------------

func (d *DFA) startState() stateID { return startStateID }

func (d *DFA) isDead(s stateID) bool { return s == deadStateID }

func (d *DFA) matchKindOf() MatchKind { return d.matchKind }

func (d *DFA) isMatch(s stateID) bool { return d.outBase[s] >= 0 }

func (d *DFA) matches(s stateID) []PatternID {
	base := d.outBase[s]
	if base < 0 {
		return nil
	}
	return d.outputs[base : base+d.outLen[s]]
}

// nextState returns the next state from s on byte b.
// The transition table already encodes failure links, so no loop is needed.
//
//go:nosplit
func (d *DFA) nextState(s stateID, b byte) stateID {
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
func buildDFA(nfa *NFA) *DFA {
	numStates := len(nfa.states)
	d := &DFA{
		trans:     make([]stateID, numStates*256),
		outBase:   make([]int32, numStates),
		outLen:    make([]int32, numStates),
		matchKind: nfa.matchKind,
		numStates: numStates,
		alphabet:  nfa.alphabet,
		useAlpha:  nfa.useAlpha,
	}

	// Initialise all outBase to -1 (no match).
	for s := range d.outBase {
		d.outBase[s] = -1
	}

	// Populate flat output table.
	// Count total outputs first to allocate once.
	total := 0
	for s := 0; s < numStates; s++ {
		total += len(nfa.matches(stateID(s)))
	}
	d.outputs = make([]PatternID, 0, total)

	for s := 0; s < numStates; s++ {
		outs := nfa.matches(stateID(s))
		if len(outs) == 0 {
			continue
		}
		d.outBase[s] = int32(len(d.outputs))
		d.outLen[s] = int32(len(outs))
		d.outputs = append(d.outputs, outs...)
	}

	// Populate transitions.
	// For every state s and every byte b, follow NFA failure links to
	// find the target state.
	for s := 0; s < numStates; s++ {
		base := s << 8 // s * 256
		for b := 0; b < 256; b++ {
			d.trans[base|b] = nfa.nextState(stateID(s), byte(b))
		}
	}

	return d
}
