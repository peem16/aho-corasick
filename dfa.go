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
	outputs   []PatternID // all output pattern IDs, concatenated
	outputIdx []int32     // outputIdx[stateID] = base index into outputs; -1 = no match
	outLen    []int32     // outLen[stateID] = number of outputs for that state
	matchKind MatchKind
	numStates int
}

// ---------------------------------------------------------------------------
// automaton interface
// ---------------------------------------------------------------------------

func (d *DFA) startState() stateID { return startStateID }

func (d *DFA) isDead(s stateID) bool { return s == deadStateID }

func (d *DFA) matchKindOf() MatchKind { return d.matchKind }

func (d *DFA) isMatch(s stateID) bool { return d.outputIdx[s] >= 0 }

func (d *DFA) matches(s stateID) []PatternID {
	idx := d.outputIdx[s]
	if idx < 0 {
		return nil
	}
	return d.outputs[idx : idx+d.outLen[s]]
}

// nextState returns the next state from s on byte b.
// The transition table already encodes failure links and alphabet
// normalisation (baked in at build time), so no loop or runtime
// byte mapping is needed.
//
//go:nosplit
func (d *DFA) nextState(s stateID, b byte) stateID {
	return d.trans[int(s)<<8|int(b)]
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
		outputIdx: make([]int32, numStates),
		outLen:    make([]int32, numStates),
		matchKind: nfa.matchKind,
		numStates: numStates,
	}

	// Flatten outputs into a contiguous buffer (like NFA).
	totalOuts := 0
	for s := 0; s < numStates; s++ {
		if outs := nfa.matches(stateID(s)); len(outs) > 0 {
			totalOuts += len(outs)
		}
	}
	d.outputs = make([]PatternID, 0, totalOuts)
	for s := 0; s < numStates; s++ {
		outs := nfa.matches(stateID(s))
		if len(outs) == 0 {
			d.outputIdx[s] = -1
			continue
		}
		d.outputIdx[s] = int32(len(d.outputs))
		d.outLen[s] = int32(len(outs))
		// NFA outputs are already sorted by flattenOutputs; append directly.
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
