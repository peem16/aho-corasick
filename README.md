# aho-corasick

High-performance multi-pattern string matching in Go using the [Aho-Corasick algorithm](https://en.wikipedia.org/wiki/Aho%E2%80%93Corasick_algorithm). API mirrors the Rust [`aho-corasick`](https://docs.rs/aho-corasick) crate.

```
go get github.com/peem16/aho-corasick
```

## Features at a glance

| Feature | Description |
|---|---|
| Multi-pattern search | Find many patterns simultaneously in a single O(n) pass |
| Three match semantics | Standard, LeftmostFirst, LeftmostLongest |
| Overlapping matches | Report every match including ones that share bytes |
| NFA & DFA backends | Auto-selected; DFA for fastest search, NFA to save memory |
| SIMD prefilter | `bytes.IndexByte` acceleration skips non-candidate bytes |
| ASCII case-insensitive | Built into the automaton — no haystack copy needed |
| Replace API | `ReplaceAll` / `ReplaceAllWith` with per-pattern replacements |
| Zero-alloc iteration | Iterator pool via `sync.Pool`; `0 B/op` in hot search loops |
| Concurrency-safe | Automaton is read-only after construction; share freely |

---

## Quick start

```go
import "github.com/peem16/aho-corasick"

ac, err := ahocorasick.NewString([]string{"he", "she", "his", "hers"})
if err != nil {
    log.Fatal(err)
}

// Check if any pattern exists
fmt.Println(ac.IsMatchString("ushers")) // true

// Find the first match
m, ok := ac.FindString("ushers")
if ok {
    fmt.Printf("pattern %d: %q at [%d, %d)\n", m.PatternID(), m.Bytes([]byte("ushers")), m.Start(), m.End())
}

// Find all non-overlapping matches
for _, m := range ac.FindAllString("ushers") {
    fmt.Printf("pattern %d at [%d, %d)\n", m.PatternID(), m.Start(), m.End())
}
```

---

## Match semantics

Three modes control what counts as a "match" when patterns overlap.

### Standard

Classical Aho-Corasick: report every match as soon as the automaton reaches a terminal state. The only mode that supports overlapping matches.

```go
ac, _ := ahocorasick.NewBuilder().
    MatchKind(ahocorasick.MatchKindStandard).
    Build([][]byte{[]byte("ab"), []byte("b")})

// "ab" → matches "ab" (pattern 0) and "b" (pattern 1)
for _, m := range ac.FindAll([]byte("ab")) {
    fmt.Println(m) // [0,2) and [1,2)
}
```

### LeftmostFirst

Leftmost match wins; when multiple patterns start at the same position, the one with the **lower index** wins. Behaves like Perl / PCRE `pat0|pat1|…` alternation.

```go
ac, _ := ahocorasick.NewBuilder().
    MatchKind(ahocorasick.MatchKindLeftmostFirst).
    BuildString([]string{"he", "hers"})

m, _ := ac.FindString("hers")
// returns "he" (pattern 0) — lower index wins over "hers" (pattern 1)
```

### LeftmostLongest

Leftmost match wins; when multiple patterns start at the same position, the **longest** one wins. Behaves like POSIX alternation.

```go
ac, _ := ahocorasick.NewBuilder().
    MatchKind(ahocorasick.MatchKindLeftmostLongest).
    BuildString([]string{"he", "hers"})

m, _ := ac.FindString("hers")
// returns "hers" (pattern 1) — longer match wins
```

---

## Overlapping matches

Use `FindOverlappingIter` (only available with `MatchKindStandard`) to get every match including those that share bytes.

```go
ac, _ := ahocorasick.NewString([]string{"a", "aa", "aaa"})

it := ac.FindOverlappingIterString("aaa")
defer it.Close()
for {
    m, ok := it.Next()
    if !ok {
        break
    }
    fmt.Printf("%q at [%d,%d)\n", m.Bytes([]byte("aaa")), m.Start(), m.End())
}
// "a"   at [0,1)
// "aa"  at [0,2)
// "a"   at [1,2)
// "aaa" at [0,3)
// "aa"  at [1,3)
// "a"   at [2,3)
```

---

## Zero-allocation iteration

`FindIter` returns an iterator from an internal `sync.Pool`. Call `Close()` to return it to the pool. This keeps the hot search path at `0 allocs/op`.

```go
it := ac.FindIterString(text)
defer it.Close() // return to pool

for {
    m, ok := it.Next()
    if !ok {
        break
    }
    process(m)
}
```

`FindAll` does this internally and is the simplest option when you need the full list.

---

## Replace

```go
ac, _ := ahocorasick.NewString([]string{"foo", "bar"})

// Fixed replacements per pattern ID
out, _ := ac.ReplaceAllString("foo and bar", []string{"baz", "qux"})
fmt.Println(out) // "baz and qux"

// Dynamic replacement via callback
out2 := ac.ReplaceAllWithString("foo and bar", func(m ahocorasick.Match) string {
    return strings.ToUpper(m.Bytes([]byte("foo and bar")))
})
fmt.Println(out2) // "FOO and BAR"
```

Return `nil` from the callback (or use a `nil` replacement slice entry) to delete the matched bytes.

---

## ASCII case-insensitive matching

Case folding is built into the automaton at build time — no `strings.ToLower` on the haystack.

```go
ac, _ := ahocorasick.NewBuilder().
    AsciiCaseInsensitive(true).
    BuildString([]string{"Hello", "WORLD"})

fmt.Println(ac.IsMatchString("hello world")) // true
fmt.Println(ac.IsMatchString("HELLO WORLD")) // true
```

Non-ASCII bytes are matched exactly.

---

## Choosing a backend

The `Kind` option controls memory/speed trade-off.

| Kind | Memory | Speed | When to use |
|---|---|---|---|
| `Auto` (default) | — | — | Let the library decide |
| `DFA` | `O(states × 256)` | Fastest — O(1) per byte | ≤10 patterns or leftmost semantics |
| `ContiguousNFA` | `O(states × avg_fan_out)` | Fast — binary search per byte | Many patterns |
| `NoncontiguousNFA` | Lowest | Slowest | Extremely large pattern sets |

```go
ac, _ := ahocorasick.NewBuilder().
    Kind(ahocorasick.AhoCorasickKindDFA).
    BuildString(patterns)
```

`Auto` picks DFA for ≤10 patterns or any leftmost `MatchKind`, and `ContiguousNFA` otherwise.

---

## Builder reference

```go
ahocorasick.NewBuilder().
    MatchKind(ahocorasick.MatchKindLeftmostLongest). // default: Standard
    Kind(ahocorasick.AhoCorasickKindDFA).            // default: Auto
    AsciiCaseInsensitive(true).                      // default: false
    Prefilter(false).                                // default: true
    DenseDepth(3).                                   // default: 2
    Build(patterns)
```

`Prefilter(false)` disables the SIMD byte-skip heuristic. Useful when nearly every position in the haystack is a match candidate and the prefilter overhead outweighs the benefit.

`DenseDepth(d)` — NFA states at depth ≤ d use a 256-entry dense row (faster transitions); deeper states use sorted sparse lists (less memory).

---

## Match fields

```go
m.PatternID()         // uint32 — index in the original patterns slice
m.Start()             // int — start byte offset (inclusive)
m.End()               // int — end byte offset (exclusive)
m.Bytes(haystack)     // []byte — zero-copy slice of the matching region
m.IsEmpty()           // bool — true for zero-length matches
```

---

## Benchmarks

Measured on an amd64 machine with a 1 MB haystack:

```
BenchmarkFind_DFA_Small_1KB       0 B/op   0 allocs   ~21,500 MB/s
BenchmarkFind_NFA_Small_1KB       0 B/op   0 allocs   ~19,400 MB/s
BenchmarkFindAll_WithPrefilter                         ~479 MB/s
BenchmarkFindAll_NoPrefilter                           ~155 MB/s
BenchmarkReplaceAll_1MB                                ~453 MB/s
```

---

## License

MIT
