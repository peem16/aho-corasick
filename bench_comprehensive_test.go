package ahocorasick_test

import (
	"fmt"
	"strings"
	"testing"

	ac "github.com/peem16/aho-corasick"
)

// ---------------------------------------------------------------------------
// Additional helpers (reuses smallPatterns, mediumPatterns, haystackOf,
// buildAC, hay1MB, hay1MB_medium declared in bench_test.go)
// ---------------------------------------------------------------------------

// haystackWithMatchCount builds a haystack that produces ~targetMatches total
// matches. Each cycle embeds every pattern once, separated by 200-byte filler.
func haystackWithMatchCount(targetMatches int, patterns []string) []byte {
	filler := strings.Repeat("X", 200)
	numCycles := targetMatches / len(patterns)
	var sb strings.Builder
	for i := 0; i < numCycles; i++ {
		for _, p := range patterns {
			sb.WriteString(filler)
			sb.WriteString(p)
		}
	}
	return []byte(sb.String())
}

// haystackOfCI builds a haystack of ~size bytes embedding uppercase variants
// of patterns, for use with AsciiCaseInsensitive benchmarks.
func haystackOfCI(size int, patterns []string) []byte {
	upper := make([]string, len(patterns))
	for i, p := range patterns {
		upper[i] = strings.ToUpper(p)
	}
	return haystackOf(size, upper)
}

// xlPatterns generates N patterns of the form "patternNNN".
func xlPatterns(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("pattern%d", i)
	}
	return out
}

var (
	hayScale100   = haystackWithMatchCount(100, smallPatterns)
	hayScale1000  = haystackWithMatchCount(1000, smallPatterns)
	hayScale10000 = haystackWithMatchCount(10000, smallPatterns)

	hay1MB_ci        = haystackOfCI(1024*1024, smallPatterns)
	hay1MB_medium_ci = haystackOfCI(1024*1024, mediumPatterns)

	// Large pattern sets for high-pattern-count benchmarks.
	patterns1000  = xlPatterns(1000)
	patterns5000  = xlPatterns(5000)
	patterns10000 = xlPatterns(10000)
	hay1MB_1000   = haystackOf(1024*1024, patterns1000)
	hay1MB_5000   = haystackOf(1024*1024, patterns5000)
	hay1MB_10000  = haystackOf(1024*1024, patterns10000)
)

// ---------------------------------------------------------------------------
// Group A: FindOverlappingIter — NFA + DFA, small + medium patterns, 1MB
// ---------------------------------------------------------------------------

func BenchmarkFindOverlapping_NFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA).MatchKind(ac.MatchKindStandard))
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindOverlappingIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindOverlapping_DFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA).MatchKind(ac.MatchKindStandard))
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindOverlappingIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindOverlapping_NFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA).MatchKind(ac.MatchKindStandard))
	hay := hay1MB_medium
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindOverlappingIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindOverlapping_DFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA).MatchKind(ac.MatchKindStandard))
	hay := hay1MB_medium
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindOverlappingIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

// ---------------------------------------------------------------------------
// Group B: FindIter throughput — 1MB haystacks (NFA + DFA, small + medium)
// ---------------------------------------------------------------------------

func BenchmarkFindIter_NFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindIter_DFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA))
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindIter_NFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hay1MB_medium
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindIter_DFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA))
	hay := hay1MB_medium
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

// ---------------------------------------------------------------------------
// Group C: ReplaceAllWith (callback) — small + medium patterns, 1MB
// ---------------------------------------------------------------------------

func BenchmarkReplaceAllWith_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.ReplaceAllWith(hay, func(m ac.Match) []byte {
			src := m.Bytes(hay)
			out := make([]byte, 0, len(src)+2)
			out = append(out, '[')
			out = append(out, src...)
			out = append(out, ']')
			return out
		})
	}
}

func BenchmarkReplaceAllWith_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns, ac.NewBuilder())
	hay := hay1MB_medium
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.ReplaceAllWith(hay, func(m ac.Match) []byte {
			src := m.Bytes(hay)
			out := make([]byte, 0, len(src)+2)
			out = append(out, '[')
			out = append(out, src...)
			out = append(out, ']')
			return out
		})
	}
}

// ---------------------------------------------------------------------------
// Group D: AsciiCaseInsensitive — NFA + DFA, small + medium patterns, 1MB
// Haystack embeds uppercase variants; patterns are lowercase.
// ---------------------------------------------------------------------------

func BenchmarkCaseInsensitive_NFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA).AsciiCaseInsensitive(true))
	hay := hay1MB_ci
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkCaseInsensitive_DFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA).AsciiCaseInsensitive(true))
	hay := hay1MB_ci
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkCaseInsensitive_NFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA).AsciiCaseInsensitive(true))
	hay := hay1MB_medium_ci
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkCaseInsensitive_DFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA).AsciiCaseInsensitive(true))
	hay := hay1MB_medium_ci
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

// ---------------------------------------------------------------------------
// Group E: Scaling — match density 100 / 1000 / 10000 matches, DFA + NFA
// Shows how throughput changes as the number of matches in the haystack grows.
// ---------------------------------------------------------------------------

func BenchmarkScaling_DFA_Small_100Matches(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA))
	hay := hayScale100
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkScaling_DFA_Small_1000Matches(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA))
	hay := hayScale1000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkScaling_DFA_Small_10000Matches(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA))
	hay := hayScale10000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkScaling_NFA_Small_100Matches(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hayScale100
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkScaling_NFA_Small_1000Matches(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hayScale1000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkScaling_NFA_Small_10000Matches(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hayScale10000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

// ---------------------------------------------------------------------------
// Group F: High pattern count — FindOverlappingIter with 1000/5000/10000 patterns
// ---------------------------------------------------------------------------

func BenchmarkFindOverlapping_NFA_1000Patterns_1MB(b *testing.B) {
	a := buildAC(b, patterns1000,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA).MatchKind(ac.MatchKindStandard))
	hay := hay1MB_1000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindOverlappingIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindOverlapping_NFA_5000Patterns_1MB(b *testing.B) {
	a := buildAC(b, patterns5000,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA).MatchKind(ac.MatchKindStandard))
	hay := hay1MB_5000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindOverlappingIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindOverlapping_NFA_10000Patterns_1MB(b *testing.B) {
	a := buildAC(b, patterns10000,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA).MatchKind(ac.MatchKindStandard))
	hay := hay1MB_10000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		it := a.FindOverlappingIter(hay)
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
		}
		it.Close()
	}
}

func BenchmarkFindAll_NFA_1000Patterns_1MB(b *testing.B) {
	a := buildAC(b, patterns1000,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hay1MB_1000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkFindAll_NFA_5000Patterns_1MB(b *testing.B) {
	a := buildAC(b, patterns5000,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hay1MB_5000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkFindAll_NFA_10000Patterns_1MB(b *testing.B) {
	a := buildAC(b, patterns10000,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA))
	hay := hay1MB_10000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

// ---------------------------------------------------------------------------
// Diverse first-byte benchmarks — tests wider prefilter (4+ distinct bytes)
// ---------------------------------------------------------------------------

// diversePatterns generates N patterns with diverse first bytes.
// Patterns cycle through lowercase letters: "a_word0", "b_word1", ..., "z_word25", "a_word26", ...
func diversePatterns(n int) []string {
	out := make([]string, n)
	for i := range out {
		ch := byte('a') + byte(i%26)
		out[i] = fmt.Sprintf("%c_word%d", ch, i)
	}
	return out
}

// fewFirstBytePatterns generates N patterns cycling through 4 distinct first bytes.
// Tests the SIMD-accelerated prefilter for 4 distinct first bytes.
func fewFirstBytePatterns(n int) []string {
	prefixes := []byte{'a', 'b', 'c', 'd'}
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%c_item%d", prefixes[i%len(prefixes)], i)
	}
	return out
}

var (
	diversePats1000         = diversePatterns(1000)
	diversePats5000         = diversePatterns(5000)
	fewFirstBytePats1000    = fewFirstBytePatterns(1000)
	hay1MB_diverse1000      = haystackOf(1<<20, diversePats1000)
	hay1MB_diverse5000      = haystackOf(1<<20, diversePats5000)
	hay1MB_fewFirstByte1000 = haystackOf(1<<20, fewFirstBytePats1000)
)

func BenchmarkFindAll_Auto_1000DiversePatterns_1MB(b *testing.B) {
	a := buildAC(b, diversePats1000, ac.NewBuilder())
	hay := hay1MB_diverse1000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkFindAll_Auto_5000DiversePatterns_1MB(b *testing.B) {
	a := buildAC(b, diversePats5000, ac.NewBuilder())
	hay := hay1MB_diverse5000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkFindAll_Auto_1000FewFirstByte_1MB(b *testing.B) {
	a := buildAC(b, fewFirstBytePats1000, ac.NewBuilder())
	hay := hay1MB_fewFirstByte1000
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}
