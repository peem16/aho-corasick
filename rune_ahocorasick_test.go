package ahocorasick

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func runesOf(ss ...string) [][]rune {
	out := make([][]rune, len(ss))
	for i, s := range ss {
		out[i] = []rune(s)
	}
	return out
}

func buildRune(t *testing.T, patterns ...string) *RuneAhoCorasick {
	t.Helper()
	ra, err := NewRune(runesOf(patterns...))
	if err != nil {
		t.Fatalf("NewRune: %v", err)
	}
	return ra
}

// ---------------------------------------------------------------------------
// Tests: OverlappingPatternSet
// ---------------------------------------------------------------------------

func TestRuneOverlappingPatternSet_Basic(t *testing.T) {
	ra := buildRune(t, "he", "she", "his", "hers")
	hay := []rune("ushers")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(hay, seen)

	want := map[string]bool{"he": true, "she": true, "hers": true}
	for i, s := range []string{"he", "she", "his", "hers"} {
		if seen[i] != want[s] {
			t.Errorf("pattern %q: seen=%v want=%v", s, seen[i], want[s])
		}
	}
}

func TestRuneOverlappingPatternSet_Thai(t *testing.T) {
	patterns := []string{"สวัสดี", "ครับ", "สวัส"}
	ra := buildRune(t, patterns...)
	hay := []rune("สวัสดีครับ")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(hay, seen)

	for i, p := range patterns {
		if !seen[i] {
			t.Errorf("pattern %q not matched", p)
		}
	}
}

func TestRuneOverlappingPatternSet_NoMatch(t *testing.T) {
	ra := buildRune(t, "foo", "bar")
	hay := []rune("baz")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(hay, seen)

	for i, s := range []string{"foo", "bar"} {
		if seen[i] {
			t.Errorf("pattern %q should not match", s)
		}
	}
}

func TestRuneOverlappingPatternSet_EmptyHaystack(t *testing.T) {
	ra := buildRune(t, "a")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(nil, seen)
	if seen[0] {
		t.Error("empty haystack should not match")
	}
}

func TestRuneOverlappingPatternSet_EmptyPattern(t *testing.T) {
	ra := buildRune(t, "", "a")
	hay := []rune("b")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(hay, seen)
	if !seen[0] {
		t.Error("empty pattern should always match")
	}
	if seen[1] {
		t.Error("pattern 'a' should not match 'b'")
	}
}

func TestRuneOverlappingPatternSet_NilReceiver(t *testing.T) {
	var ra *RuneAhoCorasick
	ra.OverlappingPatternSet([]rune("hello"), nil) // should not panic
}

func TestRuneOverlappingPatternSet_SingleChar(t *testing.T) {
	ra := buildRune(t, "a", "b", "c")
	hay := []rune("abc")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(hay, seen)
	for i := range seen {
		if !seen[i] {
			t.Errorf("pattern %d not matched", i)
		}
	}
}

func TestRuneOverlappingPatternSet_Overlapping(t *testing.T) {
	ra := buildRune(t, "abc", "bc", "c")
	hay := []rune("abc")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(hay, seen)
	for i, p := range []string{"abc", "bc", "c"} {
		if !seen[i] {
			t.Errorf("pattern %q not matched", p)
		}
	}
}

func TestRuneOverlappingPatternSet_ResetBetweenCalls(t *testing.T) {
	ra := buildRune(t, "foo", "bar")

	hay1 := []rune("foo")
	seen := make([]bool, ra.PatternCount())
	ra.OverlappingPatternSet(hay1, seen)
	if !seen[0] || seen[1] {
		t.Fatal("first call wrong")
	}

	// Reset and match different text.
	for i := range seen {
		seen[i] = false
	}
	hay2 := []rune("bar")
	ra.OverlappingPatternSet(hay2, seen)
	if seen[0] || !seen[1] {
		t.Fatal("second call wrong after reset")
	}
}

// ---------------------------------------------------------------------------
// Tests: FindOverlappingAll
// ---------------------------------------------------------------------------

func TestRuneFindOverlappingAll_Positions(t *testing.T) {
	ra := buildRune(t, "he", "she", "his", "hers")
	hay := []rune("ushers")
	matches := ra.FindOverlappingAll(hay)

	type hit struct {
		pat        string
		start, end int
	}
	want := map[hit]bool{
		{"she", 1, 4}: true,
		{"he", 2, 4}:  true,
		{"hers", 2, 6}: true,
	}

	got := make(map[hit]bool)
	for _, m := range matches {
		got[hit{
			pat:   string(ra.patterns[m.PatternID()]),
			start: m.Start(),
			end:   m.End(),
		}] = true
	}

	if len(got) != len(want) {
		t.Fatalf("got %d matches, want %d: %v", len(got), len(want), got)
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing match: %+v", w)
		}
	}
}

func TestRuneFindOverlappingAll_ThaiPositions(t *testing.T) {
	ra := buildRune(t, "สวัสดี", "ครับ")
	hay := []rune("สวัสดีครับ")
	matches := ra.FindOverlappingAll(hay)

	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}
	// "สวัสดี" starts at rune 0, ends at rune 6
	if matches[0].Start() != 0 || matches[0].End() != 6 {
		t.Errorf("match[0]: start=%d end=%d, want 0,6", matches[0].Start(), matches[0].End())
	}
	// "ครับ" starts at rune 6, ends at rune 10
	if matches[1].Start() != 6 || matches[1].End() != 10 {
		t.Errorf("match[1]: start=%d end=%d, want 6,10", matches[1].Start(), matches[1].End())
	}
}

func TestRuneFindOverlappingAll_AppendReuse(t *testing.T) {
	ra := buildRune(t, "a", "b")
	buf := make([]RuneMatch, 0, 16)
	buf = ra.FindOverlappingAllAppend(buf[:0], []rune("ab"))
	if len(buf) != 2 {
		t.Fatalf("got %d matches, want 2", len(buf))
	}
	if cap(buf) != 16 {
		t.Errorf("buffer was reallocated: cap=%d", cap(buf))
	}
}

// ---------------------------------------------------------------------------
// Tests: IsMatch
// ---------------------------------------------------------------------------

func TestRuneIsMatch(t *testing.T) {
	ra := buildRune(t, "hello", "world")
	if !ra.IsMatch([]rune("hello there")) {
		t.Error("should match 'hello'")
	}
	if !ra.IsMatch([]rune("the world")) {
		t.Error("should match 'world'")
	}
	if ra.IsMatch([]rune("nothing here")) {
		t.Error("should not match")
	}
}

func TestRuneIsMatch_NilReceiver(t *testing.T) {
	var ra *RuneAhoCorasick
	if ra.IsMatch([]rune("hello")) {
		t.Error("nil receiver should return false")
	}
}

// ---------------------------------------------------------------------------
// Tests: PatternCount / Pattern
// ---------------------------------------------------------------------------

func TestRunePatternCount(t *testing.T) {
	ra := buildRune(t, "a", "bb", "ccc")
	if ra.PatternCount() != 3 {
		t.Errorf("got %d, want 3", ra.PatternCount())
	}
}

func TestRunePattern(t *testing.T) {
	ra := buildRune(t, "hello", "สวัสดี")
	p0 := ra.Pattern(0)
	if string(p0) != "hello" {
		t.Errorf("pattern 0: %q", string(p0))
	}
	p1 := ra.Pattern(1)
	if string(p1) != "สวัสดี" {
		t.Errorf("pattern 1: %q", string(p1))
	}
	// Verify it's a copy.
	p0[0] = 'X'
	if string(ra.patterns[0]) != "hello" {
		t.Error("Pattern() should return a copy")
	}
}

func TestRuneEmptyPatterns(t *testing.T) {
	ra, err := NewRune(nil)
	if err != nil {
		t.Fatal(err)
	}
	if ra.PatternCount() != 0 {
		t.Errorf("got %d, want 0", ra.PatternCount())
	}
}

// ---------------------------------------------------------------------------
// Tests: Correctness — cross-validate PatternSet vs FindOverlappingAll
// ---------------------------------------------------------------------------

func TestRuneCrossValidate(t *testing.T) {
	patterns := []string{
		"he", "she", "his", "hers", "her",
		"สวัสดี", "ครับ", "สวัส", "ดี",
		"abc", "bc", "c", "ab",
	}
	ra := buildRune(t, patterns...)
	haystacks := []string{
		"ushers",
		"สวัสดีครับ",
		"abcdef",
		"xxhisherxx",
		"nothing",
	}
	for _, h := range haystacks {
		hay := []rune(h)
		seen := make([]bool, ra.PatternCount())
		ra.OverlappingPatternSet(hay, seen)

		matches := ra.FindOverlappingAll(hay)
		seenFromFind := make([]bool, ra.PatternCount())
		for _, m := range matches {
			seenFromFind[m.PatternID()] = true
		}

		for i := range seen {
			if seen[i] != seenFromFind[i] {
				t.Errorf("haystack=%q pattern=%q: PatternSet=%v Find=%v",
					h, patterns[i], seen[i], seenFromFind[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: Large pattern set
// ---------------------------------------------------------------------------

func TestRuneLargePatternSet(t *testing.T) {
	// Generate 1000 unique patterns.
	rng := rand.New(rand.NewSource(42))
	pats := make([]string, 1000)
	seen := make(map[string]bool)
	for i := 0; i < 1000; {
		n := rng.Intn(8) + 2
		var sb strings.Builder
		for j := 0; j < n; j++ {
			sb.WriteRune(rune('a' + rng.Intn(10)))
		}
		s := sb.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		pats[i] = s
		i++
	}

	ra := buildRune(t, pats...)
	if ra.PatternCount() != 1000 {
		t.Fatalf("got %d patterns", ra.PatternCount())
	}

	// Search for each pattern in itself — should always match.
	for i, p := range pats {
		s := make([]bool, ra.PatternCount())
		ra.OverlappingPatternSet([]rune(p), s)
		if !s[i] {
			t.Errorf("pattern %d (%q) not found in itself", i, p)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: Failure link correctness (patterns sharing prefixes/suffixes)
// ---------------------------------------------------------------------------

func TestRuneFailureLinks(t *testing.T) {
	// Classic test: overlapping suffixes.
	ra := buildRune(t, "abcab", "cab", "ab")
	hay := []rune("abcab")
	matches := ra.FindOverlappingAll(hay)

	found := map[string]bool{}
	for _, m := range matches {
		found[string(ra.patterns[m.PatternID()])] = true
	}

	for _, want := range []string{"abcab", "cab", "ab"} {
		if !found[want] {
			t.Errorf("pattern %q not found in %q", want, "abcab")
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkRuneOverlappingPatternSet_Thai(b *testing.B) {
	thaiPatterns := []string{
		"สวัสดี", "ครับ", "ค่ะ", "ขอบคุณ", "ประเทศไทย",
		"กรุงเทพ", "มหานคร", "ภาษาไทย", "คนไทย", "อาหาร",
		"ร้านอาหาร", "โรงแรม", "สนามบิน", "รถไฟ", "ตลาด",
		"วัด", "โรงเรียน", "มหาวิทยาลัย", "โรงพยาบาล", "ธนาคาร",
	}
	ra := func() *RuneAhoCorasick {
		r, _ := NewRune(runesOf(thaiPatterns...))
		return r
	}()
	hay := []rune("สวัสดีครับ วันนี้อากาศดี ไปร้านอาหารแถวตลาดกันไหม แล้วก็แวะวัดด้วย ขอบคุณค่ะ")
	seen := make([]bool, ra.PatternCount())

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for j := range seen {
			seen[j] = false
		}
		ra.OverlappingPatternSet(hay, seen)
	}
}

func BenchmarkRuneOverlappingPatternSet_VsByteNFA(b *testing.B) {
	thaiPatterns := []string{
		"สวัสดี", "ครับ", "ค่ะ", "ขอบคุณ", "ประเทศไทย",
		"กรุงเทพ", "มหานคร", "ภาษาไทย", "คนไทย", "อาหาร",
	}
	hayStr := "สวัสดีครับ วันนี้อากาศดี ไปร้านอาหารแถวตลาดกันไหม ขอบคุณค่ะ"

	b.Run("Rune", func(b *testing.B) {
		ra, _ := NewRune(runesOf(thaiPatterns...))
		hay := []rune(hayStr)
		seen := make([]bool, ra.PatternCount())
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for j := range seen {
				seen[j] = false
			}
			ra.OverlappingPatternSet(hay, seen)
		}
	})

	b.Run("Byte", func(b *testing.B) {
		bytePatterns := make([][]byte, len(thaiPatterns))
		for i, s := range thaiPatterns {
			bytePatterns[i] = []byte(s)
		}
		ac, _ := New(bytePatterns)
		hay := []byte(hayStr)
		seen := make([]bool, ac.PatternCount())
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for j := range seen {
				seen[j] = false
			}
			ac.OverlappingPatternSet(hay, seen)
		}
	})
}

func BenchmarkRuneOverlappingPatternSet_PerCampaignLoop(b *testing.B) {
	// Simulate per-campaign matching: many small machines, same text.
	nCampaigns := 100
	machines := make([]*RuneAhoCorasick, nCampaigns)
	rng := rand.New(rand.NewSource(42))

	thaiRunes := []rune("กขคงจฉชซฌญฎฏฐฑฒณดตถทธนบปผฝพฟภมยรลวศษสหฬอฮ")
	for i := 0; i < nCampaigns; i++ {
		nPats := rng.Intn(5) + 1
		pats := make([][]rune, nPats)
		for j := 0; j < nPats; j++ {
			pLen := rng.Intn(4) + 2
			p := make([]rune, pLen)
			for k := range p {
				p[k] = thaiRunes[rng.Intn(len(thaiRunes))]
			}
			pats[j] = p
		}
		m, _ := NewRune(pats)
		machines[i] = m
	}

	hay := []rune("สวัสดีครับ วันนี้อากาศดีมาก ไปร้านอาหารกันเถอะ ขอบคุณค่ะ")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, m := range machines {
			seen := make([]bool, m.PatternCount())
			m.OverlappingPatternSet(hay, seen)
		}
	}
}

func BenchmarkRuneFindOverlappingAll_Thai(b *testing.B) {
	thaiPatterns := []string{
		"สวัสดี", "ครับ", "ค่ะ", "ขอบคุณ", "ประเทศไทย",
		"กรุงเทพ", "มหานคร", "ภาษาไทย", "คนไทย", "อาหาร",
	}
	ra, _ := NewRune(runesOf(thaiPatterns...))
	hay := []rune("สวัสดีครับ วันนี้อากาศดี ไปร้านอาหารแถวตลาดกันไหม ขอบคุณค่ะ")

	b.ResetTimer()
	b.ReportAllocs()
	var buf []RuneMatch
	for i := 0; i < b.N; i++ {
		buf = ra.FindOverlappingAllAppend(buf[:0], hay)
	}
	_ = fmt.Sprintf("%d", len(buf)) // prevent dead code elimination
}

// ---------------------------------------------------------------------------
// OverlappingPatternSetTrack tests
// ---------------------------------------------------------------------------

func TestRuneOverlappingPatternSetTrack_Basic(t *testing.T) {
	ra := buildRune(t, "he", "she", "his", "hers")
	hay := []rune("ushers")
	seen := make([]bool, ra.PatternCount())
	var dirty []PatternID

	dirty = ra.OverlappingPatternSetTrack(hay, seen, dirty[:0])

	// Same results as OverlappingPatternSet.
	want := map[string]bool{"he": true, "she": true, "hers": true}
	for i, s := range []string{"he", "she", "his", "hers"} {
		if seen[i] != want[s] {
			t.Errorf("pattern %q: seen=%v want=%v", s, seen[i], want[s])
		}
	}

	// dirty should contain exactly the matched pattern IDs.
	if len(dirty) != 3 {
		t.Fatalf("dirty len=%d want 3", len(dirty))
	}

	// Clear via dirty list and verify all false.
	for _, id := range dirty {
		seen[id] = false
	}
	for i := range seen {
		if seen[i] {
			t.Errorf("seen[%d] still true after dirty clear", i)
		}
	}
}

func TestRuneOverlappingPatternSetTrack_NoMatch(t *testing.T) {
	ra := buildRune(t, "foo", "bar")
	hay := []rune("baz")
	seen := make([]bool, ra.PatternCount())
	dirty := ra.OverlappingPatternSetTrack(hay, seen, nil)

	if len(dirty) != 0 {
		t.Errorf("dirty len=%d want 0", len(dirty))
	}
}

func TestRuneOverlappingPatternSetTrack_NoDuplicates(t *testing.T) {
	// "ab" appears twice in "ababab" but dirty should have it once.
	ra := buildRune(t, "ab")
	hay := []rune("ababab")
	seen := make([]bool, ra.PatternCount())
	dirty := ra.OverlappingPatternSetTrack(hay, seen, nil)

	if len(dirty) != 1 {
		t.Errorf("dirty len=%d want 1 (no duplicates)", len(dirty))
	}
}
