// Package ahocorasick implements high-performance multi-pattern string matching
// using the Aho-Corasick algorithm. The API mirrors the Rust aho-corasick crate.
//
// # Quick start
//
//	ac, err := ahocorasick.NewString([]string{"he", "she", "his", "hers"})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for _, m := range ac.FindAllString("ushers") {
//	    fmt.Printf("pattern %d: %q\n", m.PatternID(), "ushers"[m.Start():m.End()])
//	}
//
// # Automaton backends
//
// Two backends are available via [AhoCorasickKind]:
//   - [AhoCorasickKindDFA]: full 256-entry transition table per state; O(1)
//     byte lookup, fastest search throughput.
//   - [AhoCorasickKindContiguousNFA]: sparse transitions; lower memory usage,
//     suitable for large pattern sets.
//
// [AhoCorasickKindAuto] (the default) selects DFA when the pattern count is
// ≤10 or leftmost semantics are used, and NFA otherwise.
//
// # Match semantics
//
// Three modes are available via [MatchKind]:
//   - [MatchKindStandard]: classical Aho-Corasick; reports the earliest match
//     at each position. Supports overlapping search via [AhoCorasick.FindOverlappingIter].
//   - [MatchKindLeftmostFirst]: leftmost start position wins; among ties the
//     lowest pattern index wins (Perl-like semantics).
//   - [MatchKindLeftmostLongest]: leftmost start position wins; among ties the
//     longest match wins (POSIX-like semantics).
//
// # Concurrency
//
// [AhoCorasick] is immutable after construction and safe for concurrent use
// without additional synchronisation. Iterator objects ([FindIter],
// [FindOverlappingIter]) are single-use per goroutine; call Close when done so
// the underlying state is returned to a sync.Pool.
package ahocorasick
