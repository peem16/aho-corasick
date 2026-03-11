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
	trans     []stateID    // flat: numStates * 256 entries
	outputs   [][]PatternID // match output per state (nil if no match)
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

func (d *DFA) isMatch(s stateID) bool { return d.outputs[s] != nil }

func (d *DFA) matches(s stateID) []PatternID { return d.outputs[s] }

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
		outputs:   make([][]PatternID, numStates),
		matchKind: nfa.matchKind,
		numStates: numStates,
		alphabet:  nfa.alphabet,
		useAlpha:  nfa.useAlpha,
	}

	// Populate outputs.
	for s := 0; s < numStates; s++ {
		if outs := nfa.matches(stateID(s)); len(outs) > 0 {
			// Make an independent copy so DFA doesn't alias NFA memory.
			cp := make([]PatternID, len(outs))
			copy(cp, outs)
			d.outputs[s] = cp
		}
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
