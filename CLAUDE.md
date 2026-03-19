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

### Rune-based NFA (`rune_ahocorasick.go`)

A separate `RuneAhoCorasick` type operates on `[]rune` instead of `[]byte`. Designed for scripts where each character is 3+ bytes in UTF-8 (Thai, CJK, etc.) — reduces transitions per character from 3x (byte) to 1x (rune).

**Constructor**: `NewRune(patterns [][]rune) (*RuneAhoCorasick, error)`

**Public API** (mirrors byte-based where applicable):
- `OverlappingPatternSet(haystack []rune, seen []bool)` — hot path, zero-alloc bitmap
- `FindOverlappingAll(haystack []rune) []RuneMatch` — collect matches with rune positions
- `FindOverlappingAllAppend(dst []RuneMatch, haystack []rune) []RuneMatch` — append variant for buffer reuse
- `IsMatch(haystack []rune) bool` — short-circuit on first match
- `PatternCount() int`, `Pattern(id PatternID) []rune`, `PatternRunes(id PatternID) []rune`

**Key internals**:
- **Compact rune alphabet**: `runeTable []uint16` maps `(rune - minRune)` → 1-based alphabet index (0 = not in any pattern). Only runes that appear in patterns get an index.
- **Unified dense transition table**: `denseTrans []stateID` covers start state + shallow states (depth ≤ 5). `denseBase []int32` holds pre-multiplied offsets (`stateIdx * alphaSize`). States without dense tables have `denseBase[s] = -1` and fall back to sorted binary search on `transBuf`.
- **Output flag (bit 31)**: Each `denseTrans` entry encodes `targetState | outputFlag`. The hot path skips the output drain when the flag is clear — avoids a random memory load in the common no-match case.
- **`unsafe.Pointer` hot loop**: `OverlappingPatternSet` uses `unsafe.Add` for haystack, runeTable, denseTrans, and denseBase access to eliminate Go bounds checks.
- **Separate `outputOff []int32`**: Output offsets stored separately from `nfaState` for better cache-line utilization.
- **NFA-only**: No DFA variant — rune alphabet is too large for full dense DFA tables.

**Performance** (Thai text, 10 patterns, Intel i7-14700KF):
| Variant | ns/op | vs Byte |
|---------|-------|---------|
| Byte NFA | ~584 | baseline |
| Rune v1 (sparse only) | ~509 | 1.1x |
| Rune v2 (dense tables) | ~247 | 2.3x |
| Rune v3 (unified dense) | ~213 | 2.7x |
| Rune v4 (unsafe + output flag) | ~196 | 3.0x |

### Key design choices

- DFA hot loop uses `state<<8 | byte` indexing (no multiply) with a bounds-check-elimination hint (`_ = haystack[n-1]`) before the loop.
- Case-insensitive matching uses a 256-byte `alphabet` normalization table applied once per byte — no `bytes.ToLower` on the haystack.
- `Match` is a small value type (PatternID + start + end) passed by value to avoid heap allocation.
- All automaton state is immutable after construction — safe for concurrent use without locks.
- Output sets are flattened into a single `[]PatternID` slice with per-state base+length indexing to avoid per-state allocations.

## Primary consumer

This library is used by `github.com/wisesight/zocialeye` for keyword matching across ~2639 campaigns per message. The `Peem16Machine` wrapper in `match/campaignformatch/` holds an `*AhoCorasick` (byte) or `*RuneAhoCorasick` (rune) plus a `KeywordToID map[string]PatternID` for condition checking.

## Files overview

| File | Purpose |
|------|---------|
| `ahocorasick.go` | Public `AhoCorasick` type, `New()`, all `Find*`/`Replace*` methods |
| `nfa.go` | NFA builder (trie → failure links → dense tables → flatten), `buildNFA` |
| `dfa.go` | DFA builder, full 256-entry transition tables |
| `rune_ahocorasick.go` | `RuneAhoCorasick` type, `NewRune()`, rune-based search methods |
| `match.go` | `PatternID`, `stateID`, `Match`, `MatchKind` types |
| `builder.go` | `Builder` with options (match kind, ASCII case insensitive, etc.) |
| `prefilter.go` | SIMD-accelerated `bytes.IndexByte` prefilter for ≤3 start bytes |
| `iter.go` | Iterator types with `sync.Pool` for zero-alloc iteration |
| `replace.go` | `Replace*` methods |
| `doc.go` | Package documentation |
