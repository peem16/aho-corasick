// Package ahocorasick provides a high-performance implementation of the
// Aho-Corasick multi-pattern string search algorithm.
//
// The API mirrors the Rust aho-corasick crate (https://docs.rs/aho-corasick).
// It supports Standard, LeftmostFirst, and LeftmostLongest match semantics,
// NFA and DFA automaton backends, ASCII case-insensitive matching, and
// overlapping match iteration.
package ahocorasick

// PatternID is the index of a pattern in the original pattern slice
// provided to the builder. It is always < PatternCount().
type PatternID uint32

// Match represents a single match found in a haystack.
// Every match contains the PatternID that matched and the
// [Start, End) byte offsets within the haystack.
type Match struct {
	id    PatternID
	start int
	end   int
}

// PatternID returns the index of the pattern that matched.
func (m Match) PatternID() PatternID { return m.id }

// Start returns the starting byte offset of this match (inclusive).
func (m Match) Start() int { return m.start }

// End returns the ending byte offset of this match (exclusive).
func (m Match) End() int { return m.end }

// Bytes returns the matching slice of haystack without copying.
func (m Match) Bytes(haystack []byte) []byte { return haystack[m.start:m.end] }

// IsEmpty reports whether the match is zero-length.
func (m Match) IsEmpty() bool { return m.start == m.end }

// MatchKind controls the match semantics of the Aho-Corasick automaton.
type MatchKind uint8

const (
	// MatchKindStandard reports matches as soon as they are found.
	// This is the only mode that supports overlapping matches and
	// stream searching.  Corresponds to the classical Aho-Corasick
	// textbook semantics.
	MatchKindStandard MatchKind = iota

	// MatchKindLeftmostFirst reports the leftmost match, preferring
	// the pattern with the smallest index when multiple patterns
	// match at the same leftmost position.  Mimics Perl / PCRE
	// alternation semantics (pat1|pat2|…).
	MatchKindLeftmostFirst

	// MatchKindLeftmostLongest reports the leftmost match, preferring
	// the longest match when multiple patterns match at the same
	// leftmost position.  Mimics POSIX alternation semantics.
	MatchKindLeftmostLongest
)

// AhoCorasickKind selects the internal automaton representation.
type AhoCorasickKind uint8

const (
	// AhoCorasickKindAuto lets the library choose the best representation
	// based on the number of patterns and match kind.
	AhoCorasickKindAuto AhoCorasickKind = iota

	// AhoCorasickKindNoncontiguousNFA uses a sparse NFA with per-state
	// transition slices.  Lowest memory usage for many patterns.
	AhoCorasickKindNoncontiguousNFA

	// AhoCorasickKindContiguousNFA is the same sparse NFA but with
	// states packed into a single contiguous array for better cache
	// behaviour during traversal.
	AhoCorasickKindContiguousNFA

	// AhoCorasickKindDFA builds a full DFA with a 256-entry transition
	// table per state.  Fastest search — O(n) with no failure-link
	// traversal — at the cost of higher memory usage.
	AhoCorasickKindDFA
)

// stateID is the internal state identifier used by all automata.
// 0 is reserved as the dead state; 1 is the start state.
type stateID uint32

const (
	deadStateID  stateID = 0
	startStateID stateID = 1
)

// automaton is the internal interface satisfied by both the NFA and DFA.
type automaton interface {
	// startState returns the initial state ID (always startStateID).
	startState() stateID
	// nextState returns the state reached from s on byte b.
	// It must follow failure links internally so the caller does not need to.
	nextState(s stateID, b byte) stateID
	// isMatch reports whether state s has at least one pattern output.
	isMatch(s stateID) bool
	// matches returns all pattern IDs output at state s.
	// The returned slice must not be modified by the caller.
	matches(s stateID) []PatternID
	// isDead reports whether s is the dead state (only meaningful for
	// leftmost search termination).
	isDead(s stateID) bool
	// matchKind returns the semantics this automaton was built with.
	matchKindOf() MatchKind
}
