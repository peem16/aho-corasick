# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run all tests
go test ./...

# Run tests with race detector
go test -race ./...

# Run a single test
go test -run TestName ./...

# Run benchmarks
go test -bench=. -benchmem ./...

# Run a specific benchmark
go test -bench=BenchmarkFind_DFA -benchmem ./...

# Vet
go vet ./...
```

## Architecture

Package `ahocorasick` implements multi-pattern string matching using the Aho-Corasick algorithm, mirroring the Rust `aho-corasick` crate API.

### Automaton backends

Two backends implement the internal `automaton` interface (`match.go`):

- **NFA** (`nfa.go`): Sparse per-state transitions stored as sorted `[]nfaTrans` slices. BFS constructs the trie, failure links, and output sets. Binary search per byte on state transition.
- **DFA** (`dfa.go`): Full 256-entry transition table per state. `trans[stateID*256 + byte]` enables O(1) lookup with no branch. Built by expanding NFA transitions via failure links — same state count as NFA (no subset construction blowup).

`Auto` (default) selects DFA for ≤10 patterns or leftmost semantics; NFA otherwise.

### Search flow

`AhoCorasick.Find*` methods (`ahocorasick.go`) call into the backend. The prefilter (`prefilter.go`) uses `bytes.IndexByte` (SIMD on amd64) to skip ahead to candidate positions when ≤3 distinct pattern-first-bytes exist; disabled for >100 patterns. Iterators (`iter.go`) use `sync.Pool` to achieve zero allocations per `Next()` call — callers must call `Close()` when done.

### Match semantics (MatchKind)

- `Standard`: reports earliest match at each position; supports overlapping via `FindOverlappingIter`
- `LeftmostFirst`: earliest start, lowest pattern index wins (Perl semantics)
- `LeftmostLongest`: earliest start, longest match wins (POSIX semantics)

Dead states in the DFA drive leftmost semantics: once a dead state is reached, the accumulated candidate match is emitted.

### Key design choices

- DFA hot loop uses `state<<8 | byte` indexing (no multiply) with a bounds-check-elimination hint (`_ = haystack[n-1]`) before the loop.
- Case-insensitive matching uses a 256-byte `alphabet` normalization table applied once per byte — no `bytes.ToLower` on the haystack.
- `Match` is a small value type (PatternID + start + end) passed by value to avoid heap allocation.
- All automaton state is immutable after construction — safe for concurrent use without locks.
- Output sets are flattened into a single `[]PatternID` slice with per-state base+length indexing to avoid per-state allocations.
