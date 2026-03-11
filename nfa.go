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
// Fields are ordered to minimise padding and keep the hottest ones
// (fail, outputIdx) in the first cache-word.
type nfaState struct {
	fail      stateID // failure (fall-back) link
	outputIdx int32   // index into NFA.outputBase; -1 = no match
	depth     uint16  // depth in the trie (used for denseDepth threshold)
	_         [2]byte // pad to 16 bytes
}

// NFA is an Aho-Corasick Non-deterministic Finite Automaton.
//
// Memory layout
//   - states  : flat []nfaState  — O(S) where S = number of states
//   - trans   : [][]nfaTrans     — sparse; sorted per state for binary search
//   - outputs : flat []PatternID — all output pattern lists concatenated
//   - outBase : []int32          — per-state start index into outputs; length = outputs[i]
//
// This "outputs-flat" layout avoids a pointer per match state and keeps
// the output lists contiguous in memory.
type NFA struct {
	states    []nfaState
	trans     [][]nfaTrans
	outputs   []PatternID // all output pattern IDs, concatenated
	outBase   []int32     // outBase[stateID] = start in outputs; outLen[stateID] = count
	outLen    []int32     // outLen[stateID] = number of outputs for that state
	matchKind MatchKind
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
	base := st.outputIdx
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
	for {
		// Dead state is a sink — stays dead.
		if s == deadStateID {
			return deadStateID
		}
		if next, ok := n.lookup(s, b); ok {
			return next
		}
		if s == startStateID {
			return startStateID
		}
		s = n.states[s].fail
	}
}

// lookup performs a binary search for byte b in the transition list of state s.
//
//go:nosplit
func (n *NFA) lookup(s stateID, b byte) (stateID, bool) {
	tr := n.trans[s]
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

// ---------------------------------------------------------------------------
// NFA builder
// ---------------------------------------------------------------------------

// buildNFA constructs an Aho-Corasick NFA from patterns.
// patterns must not be empty (validated by the caller).
func buildNFA(patterns [][]byte, mk MatchKind, alphabet [256]byte, useAlpha bool) *NFA {
	n := &NFA{
		matchKind: mk,
		alphabet:  alphabet,
		useAlpha:  useAlpha,
	}

	// ---- Phase 1: build trie (goto function) ----
	// Reserve state 0 (dead) and state 1 (start).
	n.states = make([]nfaState, 2)
	n.trans = make([][]nfaTrans, 2)
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
			next, ok := n.lookup(cur, b)
			if !ok {
				// Allocate a new state.
				newID := stateID(len(n.states))
				n.states = append(n.states, nfaState{
					outputIdx: -1,
					depth:     uint16(depth + 1),
				})
				n.trans = append(n.trans, nil)
				tmpOutputs = append(tmpOutputs, nil)
				n.addTrans(cur, b, newID)
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
	for _, tr := range n.trans[startStateID] {
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

		for _, tr := range n.trans[cur] {
			b := tr.b
			child := tr.next

			// Walk failure links to find the longest proper suffix that
			// has a transition on b.
			fail := n.states[cur].fail
			for fail != startStateID {
				if _, ok := n.lookup(fail, b); ok {
					break
				}
				fail = n.states[fail].fail
			}
			if next, ok := n.lookup(fail, b); ok && next != child {
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
		n.addDeadTransitions(tmpOutputs)
	}

	// ---- Phase 4: flatten output table ----
	n.flattenOutputs(tmpOutputs)

	return n
}

// addTrans inserts (b → next) into the sorted transition list of state s.
func (n *NFA) addTrans(s stateID, b byte, next stateID) {
	tr := n.trans[s]
	// Binary search for insertion point.
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
	n.trans[s] = tr
}

// addDeadTransitions modifies the trie so that states which are
// "in the middle" of matching a longer pattern don't report a shorter
// sub-match prematurely (LeftmostFirst) or continue past a finished
// match (LeftmostLongest).
//
// Strategy: for every state that has an output AND is not a dead state,
// replace transitions that lead away from the match with dead-state
// transitions.  This causes the search loop to terminate cleanly.
func (n *NFA) addDeadTransitions(tmpOutputs [][]PatternID) {
	// Walk all states; if a state has outputs, redirect all transitions
	// from its failure path that would cause a "shorter match" to the dead state.
	// A simpler, correct approach: once we enter a match state, any transition
	// that doesn't continue toward a longer match should go to dead.
	//
	// We implement this by computing, for each state with output,
	// whether there exist byte extensions that lead to longer matches.
	// Bytes that don't lead to longer matches get dead-state transitions.
	for s := stateID(1); int(s) < len(n.states); s++ {
		if len(tmpOutputs[s]) == 0 {
			continue
		}
		// For every byte 0-255 not already in n.trans[s], if following failure
		// links from s doesn't lead to a longer match state, add a dead transition.
		present := make(map[byte]bool, len(n.trans[s]))
		for _, tr := range n.trans[s] {
			present[tr.b] = true
		}
		for b := 0; b < 256; b++ {
			if present[byte(b)] {
				continue
			}
			// Does the failure chain from s on byte b eventually reach
			// another match state (longer match)?  For leftmost semantics,
			// if not, we want to terminate here.
			// Simple conservative approach: add dead transition for all
			// undefined bytes at a match state.
			n.addTrans(s, byte(b), deadStateID)
		}
	}
}

// flattenOutputs converts the per-state slice-of-slices into flat arrays.
func (n *NFA) flattenOutputs(tmp [][]PatternID) {
	numStates := len(n.states)
	n.outBase = make([]int32, numStates)
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
		n.outBase[s] = int32(len(n.outputs))
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
