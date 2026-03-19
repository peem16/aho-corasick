package ahocorasick_test

import (
	"fmt"
	"strings"
	"testing"

	ac "github.com/peem16/aho-corasick"
)

// ---------------------------------------------------------------------------
// Benchmark helpers
// ---------------------------------------------------------------------------

func buildAC(b *testing.B, patterns []string, bldr *ac.AhoCorasickBuilder) *ac.AhoCorasick {
	b.Helper()
	bs := make([][]byte, len(patterns))
	for i, p := range patterns {
		bs[i] = []byte(p)
	}
	a, err := bldr.Build(bs)
	if err != nil {
		b.Fatal(err)
	}
	return a
}

// haystackOf generates a haystack of size bytes that contains each pattern
// once every ~interval bytes.
func haystackOf(size int, patterns []string) []byte {
	hay := strings.Builder{}
	hay.Grow(size)
	filler := "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	i := 0
	for hay.Len() < size {
		hay.WriteString(filler)
		hay.WriteString(patterns[i%len(patterns)])
		i++
	}
	return []byte(hay.String()[:size])
}

var smallPatterns = []string{"he", "she", "his", "hers", "hershey"}
var mediumPatterns = func() []string {
	out := make([]string, 50)
	for i := range out {
		out[i] = fmt.Sprintf("keyword%d", i)
	}
	return out
}()
var largePatterns = func() []string {
	out := make([]string, 200)
	for i := range out {
		out[i] = fmt.Sprintf("longerkeyword%d", i)
	}
	return out
}()
var xlPatterns1000 = func() []string {
	out := make([]string, 1000)
	for i := range out {
		out[i] = fmt.Sprintf("pattern%d", i)
	}
	return out
}()
var xlPatterns5000 = func() []string {
	out := make([]string, 5000)
	for i := range out {
		out[i] = fmt.Sprintf("pattern%d", i)
	}
	return out
}()
var xlPatterns10000 = func() []string {
	out := make([]string, 10000)
	for i := range out {
		out[i] = fmt.Sprintf("pattern%d", i)
	}
	return out
}()

// ---------------------------------------------------------------------------
// Build benchmarks
// ---------------------------------------------------------------------------

func BenchmarkBuild_NFA_Small(b *testing.B) {
	bs := make([][]byte, len(smallPatterns))
	for i, p := range smallPatterns {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA)
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

func BenchmarkBuild_DFA_Small(b *testing.B) {
	bs := make([][]byte, len(smallPatterns))
	for i, p := range smallPatterns {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder().Kind(ac.AhoCorasickKindDFA)
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

func BenchmarkBuild_NFA_Large(b *testing.B) {
	bs := make([][]byte, len(largePatterns))
	for i, p := range largePatterns {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA)
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

func BenchmarkBuild_NFA_1000(b *testing.B) {
	bs := make([][]byte, len(patterns1000))
	for i, p := range patterns1000 {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA)
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

func BenchmarkBuild_NFA_5000(b *testing.B) {
	bs := make([][]byte, len(patterns5000))
	for i, p := range patterns5000 {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA)
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

func BenchmarkBuild_NFA_LeftmostFirst_1000(b *testing.B) {
	bs := make([][]byte, len(patterns1000))
	for i, p := range patterns1000 {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA).MatchKind(ac.MatchKindLeftmostFirst)
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

func BenchmarkBuild_DFA_1000(b *testing.B) {
	bs := make([][]byte, len(patterns1000))
	for i, p := range patterns1000 {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder().Kind(ac.AhoCorasickKindDFA)
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

// ---------------------------------------------------------------------------
// Search benchmarks — small patterns, 1 KB haystack
// ---------------------------------------------------------------------------

var hay1KB = haystackOf(1024, smallPatterns)
var hay1MB = haystackOf(1024*1024, smallPatterns)
var hay1MB_medium = haystackOf(1024*1024, mediumPatterns)
var hay1MB_large = haystackOf(1024*1024, largePatterns)
var hay1MB_xl1000 = haystackOf(1024*1024, xlPatterns1000)
var hay1MB_xl5000 = haystackOf(1024*1024, xlPatterns5000)
var hay1MB_xl10000 = haystackOf(1024*1024, xlPatterns10000)

func BenchmarkFind_NFA_Small_1KB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA))
	b.SetBytes(int64(len(hay1KB)))
	for b.Loop() {
		_, _ = a.Find(hay1KB)
	}
}

func BenchmarkFind_DFA_Small_1KB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindDFA))
	b.SetBytes(int64(len(hay1KB)))
	for b.Loop() {
		_, _ = a.Find(hay1KB)
	}
}

func BenchmarkFindAll_NFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.FindAll(hay1MB)
	}
}

func BenchmarkFindAll_DFA_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindDFA))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.FindAll(hay1MB)
	}
}

// ---------------------------------------------------------------------------
// Search benchmarks — medium patterns, 1 MB haystack
// ---------------------------------------------------------------------------

func BenchmarkFindAll_NFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA))
	b.SetBytes(int64(len(hay1MB_medium)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_medium)
	}
}

func BenchmarkFindAll_DFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindDFA))
	b.SetBytes(int64(len(hay1MB_medium)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_medium)
	}
}

// ---------------------------------------------------------------------------
// Search benchmarks — large patterns, 1 MB haystack
// ---------------------------------------------------------------------------

func BenchmarkFindAll_NFA_Large_1MB(b *testing.B) {
	a := buildAC(b, largePatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA))
	b.SetBytes(int64(len(hay1MB_large)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_large)
	}
}

func BenchmarkFindAll_DFA_Large_1MB(b *testing.B) {
	a := buildAC(b, largePatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindDFA))
	b.SetBytes(int64(len(hay1MB_large)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_large)
	}
}

// ---------------------------------------------------------------------------
// Prefilter comparison
// ---------------------------------------------------------------------------

func BenchmarkFindAll_WithPrefilter_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().Prefilter(true))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.FindAll(hay1MB)
	}
}

func BenchmarkFindAll_NoPrefilter_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().Prefilter(false))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.FindAll(hay1MB)
	}
}

// ---------------------------------------------------------------------------
// ReplaceAll benchmark
// ---------------------------------------------------------------------------

func BenchmarkReplaceAll_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	repls := make([][]byte, len(smallPatterns))
	for i, p := range smallPatterns {
		repls[i] = []byte("[" + p + "]")
	}
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_, _ = a.ReplaceAll(hay1MB, repls)
	}
}

// ---------------------------------------------------------------------------
// Iterator pool benchmark
// ---------------------------------------------------------------------------

func BenchmarkFindIter_Pool_Reuse(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1KB
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
// IsMatch benchmark
// ---------------------------------------------------------------------------

func BenchmarkIsMatch_DFA_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().Kind(ac.AhoCorasickKindDFA))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.IsMatch(hay1MB)
	}
}

// ---------------------------------------------------------------------------
// MatchKind benchmarks
// ---------------------------------------------------------------------------

func BenchmarkMatchKind_Standard_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().MatchKind(ac.MatchKindStandard))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.FindAll(hay1MB)
	}
}

func BenchmarkMatchKind_LeftmostFirst_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().MatchKind(ac.MatchKindLeftmostFirst))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.FindAll(hay1MB)
	}
}

func BenchmarkMatchKind_LeftmostLongest_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder().MatchKind(ac.MatchKindLeftmostLongest))
	b.SetBytes(int64(len(hay1MB)))
	for b.Loop() {
		_ = a.FindAll(hay1MB)
	}
}

// ---------------------------------------------------------------------------
// High pattern-count benchmarks (Auto kind — exercises resolveKind heuristic)
// ---------------------------------------------------------------------------

func BenchmarkFindAll_Auto_1000Patterns_1MB(b *testing.B) {
	a := buildAC(b, xlPatterns1000, ac.NewBuilder())
	b.SetBytes(int64(len(hay1MB_xl1000)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_xl1000)
	}
}

func BenchmarkFindAll_DFA_1000Patterns_1MB(b *testing.B) {
	a := buildAC(b, xlPatterns1000, ac.NewBuilder().Kind(ac.AhoCorasickKindDFA))
	b.SetBytes(int64(len(hay1MB_xl1000)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_xl1000)
	}
}

func BenchmarkFindAll_Auto_5000Patterns_1MB(b *testing.B) {
	a := buildAC(b, xlPatterns5000, ac.NewBuilder())
	b.SetBytes(int64(len(hay1MB_xl5000)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_xl5000)
	}
}

func BenchmarkFindAll_Auto_10000Patterns_1MB(b *testing.B) {
	a := buildAC(b, xlPatterns10000, ac.NewBuilder())
	b.SetBytes(int64(len(hay1MB_xl10000)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_xl10000)
	}
}

func BenchmarkBuild_Auto_1000Patterns(b *testing.B) {
	bs := make([][]byte, len(xlPatterns1000))
	for i, p := range xlPatterns1000 {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder()
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

func BenchmarkBuild_Auto_10000Patterns(b *testing.B) {
	bs := make([][]byte, len(xlPatterns10000))
	for i, p := range xlPatterns10000 {
		bs[i] = []byte(p)
	}
	bldr := ac.NewBuilder()
	for b.Loop() {
		_, _ = bldr.Build(bs)
	}
}

// NFA Leftmost benchmarks — medium patterns (>10 → Auto selects NFA)
func BenchmarkMatchKind_LeftmostFirst_NFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns, ac.NewBuilder().MatchKind(ac.MatchKindLeftmostFirst))
	b.SetBytes(int64(len(hay1MB_medium)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_medium)
	}
}

func BenchmarkMatchKind_LeftmostLongest_NFA_Medium_1MB(b *testing.B) {
	a := buildAC(b, mediumPatterns, ac.NewBuilder().MatchKind(ac.MatchKindLeftmostLongest))
	b.SetBytes(int64(len(hay1MB_medium)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_medium)
	}
}

func BenchmarkMatchKind_LeftmostFirst_NFA_Large_1MB(b *testing.B) {
	a := buildAC(b, largePatterns, ac.NewBuilder().MatchKind(ac.MatchKindLeftmostFirst))
	b.SetBytes(int64(len(hay1MB_large)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_large)
	}
}

func BenchmarkMatchKind_LeftmostLongest_NFA_Large_1MB(b *testing.B) {
	a := buildAC(b, largePatterns, ac.NewBuilder().MatchKind(ac.MatchKindLeftmostLongest))
	b.SetBytes(int64(len(hay1MB_large)))
	for b.Loop() {
		_ = a.FindAll(hay1MB_large)
	}
}

// ---------------------------------------------------------------------------
// Append variant benchmarks — demonstrate zero-allocation reuse
// ---------------------------------------------------------------------------

func BenchmarkFindAllAppend_Reuse_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	buf := make([]ac.Match, 0, 256)
	for b.Loop() {
		buf = a.FindAllAppend(buf[:0], hay)
	}
}

func BenchmarkFindAllAppend_Fresh_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindAll(hay)
	}
}

func BenchmarkFindOverlappingAllAppend_Reuse_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	buf := make([]ac.Match, 0, 256)
	for b.Loop() {
		buf = a.FindOverlappingAllAppend(buf[:0], hay)
	}
}

func BenchmarkFindOverlappingAllAppend_Fresh_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.FindOverlappingAll(hay)
	}
}

// Per-campaign loop simulation: many machines, short haystack, reuse buf
func BenchmarkFindOverlappingAllAppend_PerCampaignLoop(b *testing.B) {
	// Simulate 100 campaign machines with different patterns
	machines := make([]*ac.AhoCorasick, 100)
	for i := range machines {
		pats := make([]string, 5)
		for j := range pats {
			pats[j] = fmt.Sprintf("campaign%d_keyword%d", i, j)
		}
		machines[i] = buildAC(b, pats, ac.NewBuilder())
	}
	hay := []byte(strings.Repeat("some text with campaign50_keyword2 embedded in it ", 10))
	b.SetBytes(int64(len(hay)) * int64(len(machines)))

	buf := make([]ac.Match, 0, 64)
	for b.Loop() {
		for _, m := range machines {
			buf = m.FindOverlappingAllAppend(buf[:0], hay)
		}
	}
}

// Same loop without reuse for comparison
func BenchmarkFindOverlappingAll_PerCampaignLoop(b *testing.B) {
	machines := make([]*ac.AhoCorasick, 100)
	for i := range machines {
		pats := make([]string, 5)
		for j := range pats {
			pats[j] = fmt.Sprintf("campaign%d_keyword%d", i, j)
		}
		machines[i] = buildAC(b, pats, ac.NewBuilder())
	}
	hay := []byte(strings.Repeat("some text with campaign50_keyword2 embedded in it ", 10))
	b.SetBytes(int64(len(hay)) * int64(len(machines)))

	for b.Loop() {
		for _, m := range machines {
			_ = m.FindOverlappingAll(hay)
		}
	}
}

// ---------------------------------------------------------------------------
// Count benchmarks — zero allocation
// ---------------------------------------------------------------------------

func BenchmarkCountOverlapping_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.CountOverlapping(hay)
	}
}

func BenchmarkCountAll_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.CountAll(hay)
	}
}

func BenchmarkCountOverlapping_Large_1MB(b *testing.B) {
	a := buildAC(b, largePatterns, ac.NewBuilder())
	hay := hay1MB_large
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		_ = a.CountOverlapping(hay)
	}
}

// PatternBytes benchmark
func BenchmarkPatternBytes_vs_Pattern(b *testing.B) {
	a := buildAC(b, largePatterns, ac.NewBuilder())
	n := a.PatternCount()

	b.Run("Pattern_Copy", func(b *testing.B) {
		for b.Loop() {
			for i := 0; i < n; i++ {
				_ = a.Pattern(ac.PatternID(i))
			}
		}
	})
	b.Run("PatternBytes_ZeroCopy", func(b *testing.B) {
		for b.Loop() {
			for i := 0; i < n; i++ {
				_ = a.PatternBytes(ac.PatternID(i))
			}
		}
	})
}

// ---------------------------------------------------------------------------
// PatternSet benchmarks — zero-allocation pattern marking
// ---------------------------------------------------------------------------

func BenchmarkOverlappingPatternSet_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	seen := make([]bool, a.PatternCount())
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		clear(seen)
		a.OverlappingPatternSet(hay, seen)
	}
}

func BenchmarkOverlappingPatternSet_Large_1MB(b *testing.B) {
	a := buildAC(b, largePatterns, ac.NewBuilder())
	hay := hay1MB_large
	seen := make([]bool, a.PatternCount())
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		clear(seen)
		a.OverlappingPatternSet(hay, seen)
	}
}

// Per-campaign simulation: OverlappingPatternSet vs FindOverlappingAll vs FindOverlappingAllAppend
func BenchmarkPerCampaign_OverlappingPatternSet(b *testing.B) {
	machines := make([]*ac.AhoCorasick, 100)
	maxPat := 0
	for i := range machines {
		pats := make([]string, 5)
		for j := range pats {
			pats[j] = fmt.Sprintf("campaign%d_keyword%d", i, j)
		}
		machines[i] = buildAC(b, pats, ac.NewBuilder())
		if machines[i].PatternCount() > maxPat {
			maxPat = machines[i].PatternCount()
		}
	}
	hay := []byte(strings.Repeat("some text with campaign50_keyword2 embedded in it ", 10))
	b.SetBytes(int64(len(hay)) * int64(len(machines)))

	seen := make([]bool, maxPat)
	for b.Loop() {
		for _, m := range machines {
			clear(seen[:m.PatternCount()])
			m.OverlappingPatternSet(hay, seen)
		}
	}
}

func BenchmarkPerCampaign_FindOverlappingAllAppend(b *testing.B) {
	machines := make([]*ac.AhoCorasick, 100)
	for i := range machines {
		pats := make([]string, 5)
		for j := range pats {
			pats[j] = fmt.Sprintf("campaign%d_keyword%d", i, j)
		}
		machines[i] = buildAC(b, pats, ac.NewBuilder())
	}
	hay := []byte(strings.Repeat("some text with campaign50_keyword2 embedded in it ", 10))
	b.SetBytes(int64(len(hay)) * int64(len(machines)))

	buf := make([]ac.Match, 0, 64)
	for b.Loop() {
		for _, m := range machines {
			buf = m.FindOverlappingAllAppend(buf[:0], hay)
		}
	}
}

func BenchmarkPerCampaign_FindOverlappingAll(b *testing.B) {
	machines := make([]*ac.AhoCorasick, 100)
	for i := range machines {
		pats := make([]string, 5)
		for j := range pats {
			pats[j] = fmt.Sprintf("campaign%d_keyword%d", i, j)
		}
		machines[i] = buildAC(b, pats, ac.NewBuilder())
	}
	hay := []byte(strings.Repeat("some text with campaign50_keyword2 embedded in it ", 10))
	b.SetBytes(int64(len(hay)) * int64(len(machines)))

	for b.Loop() {
		for _, m := range machines {
			_ = m.FindOverlappingAll(hay)
		}
	}
}

func BenchmarkAllPatternSet_Small_1MB(b *testing.B) {
	a := buildAC(b, smallPatterns, ac.NewBuilder())
	hay := hay1MB
	seen := make([]bool, a.PatternCount())
	b.SetBytes(int64(len(hay)))
	for b.Loop() {
		clear(seen)
		a.AllPatternSet(hay, seen)
	}
}
