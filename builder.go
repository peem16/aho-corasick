package ahocorasick

import "fmt"

// ---------------------------------------------------------------------------
// AhoCorasickBuilder — configurable automaton constructor
// ---------------------------------------------------------------------------

// AhoCorasickBuilder configures and builds an AhoCorasick automaton.
// All setter methods return the receiver for chaining.
//
// Defaults:
//   - MatchKind:            MatchKindStandard
//   - Kind:                 AhoCorasickKindAuto
//   - AsciiCaseInsensitive: false
//   - Prefilter:            true
//   - DenseDepth:           2
type AhoCorasickBuilder struct {
	matchKind            MatchKind
	kind                 AhoCorasickKind
	asciiCaseInsensitive bool
	prefilter            bool
	denseDepth           int
}

// NewBuilder returns a builder with sensible defaults.
func NewBuilder() *AhoCorasickBuilder {
	return &AhoCorasickBuilder{
		matchKind:  MatchKindStandard,
		kind:       AhoCorasickKindAuto,
		prefilter:  true,
		denseDepth: 2,
	}
}

// MatchKind sets the match semantics.
func (b *AhoCorasickBuilder) MatchKind(k MatchKind) *AhoCorasickBuilder {
	b.matchKind = k
	return b
}

// Kind forces a specific automaton representation.
// Use AhoCorasickKindAuto to let the library decide.
func (b *AhoCorasickBuilder) Kind(k AhoCorasickKind) *AhoCorasickBuilder {
	b.kind = k
	return b
}

// AsciiCaseInsensitive enables case-insensitive matching for ASCII bytes.
// Non-ASCII bytes are matched exactly.
func (b *AhoCorasickBuilder) AsciiCaseInsensitive(v bool) *AhoCorasickBuilder {
	b.asciiCaseInsensitive = v
	return b
}

// Prefilter controls the byte-prefilter acceleration heuristic.
// The prefilter uses SIMD-accelerated bytes.IndexByte to skip positions
// that cannot possibly start a match.  Disable if the workload has many
// matching positions (i.e., the prefilter rarely skips anything).
func (b *AhoCorasickBuilder) Prefilter(v bool) *AhoCorasickBuilder {
	b.prefilter = v
	return b
}

// DenseDepth sets the depth threshold for dense vs sparse state
// representation in the NFA.  States at depth ≤ DenseDepth will use a
// 256-entry dense table; deeper states use sorted sparse lists.
// The default of 2 is a good balance for most workloads.
func (b *AhoCorasickBuilder) DenseDepth(d int) *AhoCorasickBuilder {
	b.denseDepth = d
	return b
}

// Build constructs the AhoCorasick automaton from the given patterns.
// patterns may be nil or empty, in which case no matches will ever be found.
// Returns an error if any pattern is invalid (currently never for []byte).
func (b *AhoCorasickBuilder) Build(patterns [][]byte) (*AhoCorasick, error) {
	if len(patterns) == 0 {
		return &AhoCorasick{
			matchKind: b.matchKind,
			kind:      AhoCorasickKindAuto,
		}, nil
	}

	// Build alphabet (byte normalisation table).
	alphabet, useAlpha := buildAlphabet(b.asciiCaseInsensitive)

	// Build NFA (always — DFA is derived from NFA).
	nfa := buildNFA(patterns, b.matchKind, alphabet, useAlpha)

	// Decide automaton kind.
	kind := b.resolveKind(len(patterns))

	var imp automaton
	switch kind {
	case AhoCorasickKindDFA:
		imp = buildDFA(nfa)
	default:
		imp = nfa
	}

	// Build prefilter if enabled.
	var pf *prefilter
	if b.prefilter {
		pf = newPrefilter(patterns, alphabet, useAlpha)
	} else {
		pf = &prefilter{} // disabled
	}

	// Deep-copy patterns so the caller can safely reuse their slice.
	patsCopy := make([][]byte, len(patterns))
	patLens := make([]int32, len(patterns))
	for i, p := range patterns {
		cp := make([]byte, len(p))
		copy(cp, p)
		patsCopy[i] = cp
		patLens[i] = int32(len(p))
	}

	return &AhoCorasick{
		imp:       imp,
		pf:        pf,
		matchKind: b.matchKind,
		kind:      kind,
		patterns:  patsCopy,
		patLens:   patLens,
		patCount:  len(patterns),
	}, nil
}

// resolveKind picks the concrete automaton kind when AhoCorasickKindAuto.
func (b *AhoCorasickBuilder) resolveKind(numPatterns int) AhoCorasickKind {
	if b.kind != AhoCorasickKindAuto {
		return b.kind
	}
	// Heuristic: use DFA for leftmost semantics or small pattern sets,
	// use NFA for larger sets to save memory.
	if b.matchKind != MatchKindStandard || numPatterns <= 10 {
		return AhoCorasickKindDFA
	}
	return AhoCorasickKindContiguousNFA
}

// buildAlphabet constructs the byte normalisation table.
// When caseInsensitive is true, upper-case ASCII letters are mapped to
// their lower-case equivalents.
func buildAlphabet(caseInsensitive bool) ([256]byte, bool) {
	var alpha [256]byte
	for i := range alpha {
		alpha[i] = byte(i)
	}
	if !caseInsensitive {
		return alpha, false
	}
	for b := byte('A'); b <= 'Z'; b++ {
		alpha[b] = b + ('a' - 'A')
	}
	return alpha, true
}

// ---------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------

func validatePatterns(patterns [][]byte) error {
	for i, p := range patterns {
		if p == nil {
			return fmt.Errorf("aho-corasick: pattern %d is nil", i)
		}
	}
	return nil
}
