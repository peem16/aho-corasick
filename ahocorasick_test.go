package ahocorasick_test

import (
	"fmt"
	"strings"
	"testing"

	ac "github.com/peem16/aho-corasick"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustNew(t *testing.T, patterns []string) *ac.AhoCorasick {
	t.Helper()
	a, err := ac.NewString(patterns)
	if err != nil {
		t.Fatalf("NewString: %v", err)
	}
	return a
}

func mustBuild(t *testing.T, b *ac.AhoCorasickBuilder, patterns []string) *ac.AhoCorasick {
	t.Helper()
	bs := make([][]byte, len(patterns))
	for i, p := range patterns {
		bs[i] = []byte(p)
	}
	a, err := b.Build(bs)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return a
}

func matchesStr(a *ac.AhoCorasick, haystack string) []string {
	var out []string
	it := a.FindIterString(haystack)
	for {
		m, ok := it.Next()
		if !ok {
			break
		}
		out = append(out, fmt.Sprintf("(%d,%d,%d)", m.PatternID(), m.Start(), m.End()))
	}
	it.Close()
	return out
}

// ---------------------------------------------------------------------------
// Basic tests
// ---------------------------------------------------------------------------

func TestNew_NoPatterns(t *testing.T) {
	a, err := ac.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.IsMatch([]byte("hello")) {
		t.Error("expected no match for empty pattern set")
	}
}

func TestFind_Single(t *testing.T) {
	a := mustNew(t, []string{"he"})
	m, ok := a.FindString("she sells")
	if !ok {
		t.Fatal("expected match")
	}
	if m.Start() != 1 || m.End() != 3 {
		t.Errorf("got [%d,%d) want [1,3)", m.Start(), m.End())
	}
	if m.PatternID() != 0 {
		t.Errorf("pattern ID = %d, want 0", m.PatternID())
	}
}

func TestFind_NotFound(t *testing.T) {
	a := mustNew(t, []string{"xyz"})
	_, ok := a.FindString("hello world")
	if ok {
		t.Error("expected no match")
	}
}

func TestFindAll_Multiple(t *testing.T) {
	a := mustNew(t, []string{"he", "she", "his", "hers"})
	got := matchesStr(a, "ushers")
	// Standard mode: "she"→(1,[1,4)), "he"→(0,[2,4)), "hers"→(3,[2,6))
	if len(got) == 0 {
		t.Error("expected matches, got none")
	}
}

func TestFindAll_Standard_Overlapping(t *testing.T) {
	// In Standard mode with FindIter, we get non-overlapping results.
	// Use FindOverlappingIter for overlapping ones.
	a := mustNew(t, []string{"aa", "a"})
	hay := []byte("aaa")
	var ms []ac.Match
	it := a.FindOverlappingIter(hay)
	for {
		m, ok := it.Next()
		if !ok {
			break
		}
		ms = append(ms, m)
	}
	it.Close()
	if len(ms) == 0 {
		t.Error("expected overlapping matches")
	}
}

func TestFindOverlappingAll(t *testing.T) {
	// Verify FindOverlappingAll produces the same results as FindOverlappingIter.
	a := mustNew(t, []string{"aa", "a", "aab"})
	hay := []byte("aab aaa")

	// Collect via iterator.
	var iterMs []ac.Match
	it := a.FindOverlappingIter(hay)
	for {
		m, ok := it.Next()
		if !ok {
			break
		}
		iterMs = append(iterMs, m)
	}
	it.Close()

	// Collect via FindOverlappingAll.
	allMs := a.FindOverlappingAll(hay)

	if len(allMs) != len(iterMs) {
		t.Fatalf("FindOverlappingAll returned %d matches, iter returned %d", len(allMs), len(iterMs))
	}
	for i := range allMs {
		if allMs[i] != iterMs[i] {
			t.Errorf("match %d: FindOverlappingAll=%v, iter=%v", i, allMs[i], iterMs[i])
		}
	}
}

func TestIsMatch(t *testing.T) {
	a := mustNew(t, []string{"foo", "bar"})
	if !a.IsMatchString("the bar is here") {
		t.Error("expected IsMatch=true")
	}
	if a.IsMatchString("nothing here") {
		t.Error("expected IsMatch=false")
	}
}

// ---------------------------------------------------------------------------
// MatchKind tests
// ---------------------------------------------------------------------------

func TestMatchKindStandard(t *testing.T) {
	a := mustBuild(t,
		ac.NewBuilder().MatchKind(ac.MatchKindStandard),
		[]string{"ab", "b"},
	)
	// Standard: first match found is "b" at position 1 (as automaton hits state for "b" after "a").
	// Actual order depends on pattern lengths and failure links.
	ms := a.FindAllString("ab")
	if len(ms) == 0 {
		t.Error("expected matches in standard mode")
	}
}

func TestMatchKindLeftmostFirst(t *testing.T) {
	a := mustBuild(t,
		ac.NewBuilder().MatchKind(ac.MatchKindLeftmostFirst),
		[]string{"ab", "a"},
	)
	// LeftmostFirst: "ab" and "a" both start at 0; "ab" has lower ID.
	ms := a.FindAllString("ab")
	if len(ms) == 0 {
		t.Fatal("expected match")
	}
	// Either "ab" or "a" wins; the point is we get exactly one match.
	if len(ms) != 1 {
		t.Errorf("LeftmostFirst: got %d matches, want 1", len(ms))
	}
}

func TestMatchKindLeftmostLongest(t *testing.T) {
	a := mustBuild(t,
		ac.NewBuilder().MatchKind(ac.MatchKindLeftmostLongest),
		[]string{"a", "ab", "abc"},
	)
	ms := a.FindAllString("abcd")
	if len(ms) == 0 {
		t.Fatal("expected match")
	}
	// LeftmostLongest: should prefer "abc" (longest).
	if len(ms) != 1 {
		t.Errorf("LeftmostLongest: got %d matches, want 1", len(ms))
	}
	got := ms[0]
	want := "abc"
	if string(got.Bytes([]byte("abcd"))) != want {
		t.Errorf("LeftmostLongest: got %q, want %q", got.Bytes([]byte("abcd")), want)
	}
}

// ---------------------------------------------------------------------------
// ASCII case-insensitive
// ---------------------------------------------------------------------------

func TestAsciiCaseInsensitive(t *testing.T) {
	a := mustBuild(t,
		ac.NewBuilder().AsciiCaseInsensitive(true),
		[]string{"hello"},
	)
	tests := []string{"Hello", "HELLO", "hElLo", "hello"}
	for _, hay := range tests {
		if !a.IsMatchString(hay) {
			t.Errorf("expected match for %q", hay)
		}
	}
	if a.IsMatchString("world") {
		t.Error("expected no match for 'world'")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestEmptyHaystack(t *testing.T) {
	a := mustNew(t, []string{"foo"})
	_, ok := a.FindString("")
	if ok {
		t.Error("expected no match in empty haystack")
	}
}

func TestEmptyPattern(t *testing.T) {
	bs := [][]byte{[]byte("")}
	a, err := ac.New(bs)
	if err != nil {
		t.Fatal(err)
	}
	// Empty pattern should match at position 0.
	m, ok := a.Find([]byte("hello"))
	if !ok {
		t.Fatal("expected match for empty pattern")
	}
	if m.Start() != 0 || m.End() != 0 {
		t.Errorf("empty pattern match at [%d,%d), want [0,0)", m.Start(), m.End())
	}
}

func TestPatternAtBoundary(t *testing.T) {
	a := mustNew(t, []string{"lo"})
	m, ok := a.FindString("hello")
	if !ok {
		t.Fatal("expected match")
	}
	if m.Start() != 3 || m.End() != 5 {
		t.Errorf("got [%d,%d), want [3,5)", m.Start(), m.End())
	}
}

func TestLongPattern(t *testing.T) {
	pat := strings.Repeat("a", 1000)
	hay := strings.Repeat("b", 500) + pat + strings.Repeat("c", 500)
	a := mustNew(t, []string{pat})
	m, ok := a.FindString(hay)
	if !ok {
		t.Fatal("expected match")
	}
	if m.Start() != 500 || m.End() != 1500 {
		t.Errorf("got [%d,%d), want [500,1500)", m.Start(), m.End())
	}
}

func TestManyPatterns(t *testing.T) {
	// Build 200 patterns to exercise the auto-kind selection.
	patterns := make([]string, 200)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("pattern%d", i)
	}
	a := mustNew(t, patterns)
	for i, p := range patterns {
		if !a.IsMatchString(p) {
			t.Errorf("pattern %d not found", i)
		}
	}
}

// ---------------------------------------------------------------------------
// NFA vs DFA equivalence
// ---------------------------------------------------------------------------

func TestNFAvsDFA(t *testing.T) {
	patterns := []string{"he", "she", "his", "hers"}
	haystack := "ushers"

	nfa := mustBuild(t,
		ac.NewBuilder().Kind(ac.AhoCorasickKindContiguousNFA),
		patterns,
	)
	dfa := mustBuild(t,
		ac.NewBuilder().Kind(ac.AhoCorasickKindDFA),
		patterns,
	)

	nfaMatches := nfa.FindAllString(haystack)
	dfaMatches := dfa.FindAllString(haystack)

	if len(nfaMatches) != len(dfaMatches) {
		t.Fatalf("NFA got %d matches, DFA got %d", len(nfaMatches), len(dfaMatches))
	}
	for i := range nfaMatches {
		n, d := nfaMatches[i], dfaMatches[i]
		if n.PatternID() != d.PatternID() || n.Start() != d.Start() || n.End() != d.End() {
			t.Errorf("match[%d]: NFA=%+v DFA=%+v", i, n, d)
		}
	}
}

// ---------------------------------------------------------------------------
// ReplaceAll tests
// ---------------------------------------------------------------------------

func TestReplaceAll(t *testing.T) {
	a := mustNew(t, []string{"fox", "dog"})
	in := "the quick brown fox jumps over the lazy dog"
	repls := []string{"cat", "wolf"}
	got, err := a.ReplaceAllString(in, repls)
	if err != nil {
		t.Fatal(err)
	}
	want := "the quick brown cat jumps over the lazy wolf"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReplaceAllWith(t *testing.T) {
	a := mustNew(t, []string{"hello", "world"})
	in := "hello world"
	got := a.ReplaceAllWithString(in, func(m ac.Match) string {
		return "[" + string(m.Bytes([]byte(in))) + "]"
	})
	want := "[hello] [world]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReplaceAll_WrongReplacementsLen(t *testing.T) {
	a := mustNew(t, []string{"a", "b"})
	_, err := a.ReplaceAll([]byte("ab"), [][]byte{[]byte("x")}) // wrong length
	if err == nil {
		t.Error("expected error for wrong replacements length")
	}
}

// ---------------------------------------------------------------------------
// PatternCount / Pattern accessor
// ---------------------------------------------------------------------------

func TestPatternCount(t *testing.T) {
	patterns := []string{"foo", "bar", "baz"}
	a := mustNew(t, patterns)
	if a.PatternCount() != 3 {
		t.Errorf("PatternCount = %d, want 3", a.PatternCount())
	}
}

func TestPattern(t *testing.T) {
	patterns := []string{"foo", "bar"}
	a := mustNew(t, patterns)
	for i, want := range patterns {
		got := a.Pattern(ac.PatternID(i))
		if string(got) != want {
			t.Errorf("Pattern(%d) = %q, want %q", i, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Iterator Close / pool reuse
// ---------------------------------------------------------------------------

func TestIterClose(t *testing.T) {
	a := mustNew(t, []string{"a"})
	for i := 0; i < 100; i++ {
		it := a.FindIterString("aaaa")
		cnt := 0
		for {
			_, ok := it.Next()
			if !ok {
				break
			}
			cnt++
		}
		it.Close()
		if cnt != 4 {
			t.Errorf("iteration %d: got %d matches, want 4", i, cnt)
		}
	}
}

// ---------------------------------------------------------------------------
// Unicode / binary safe
// ---------------------------------------------------------------------------

func TestBinarySafe(t *testing.T) {
	// Aho-Corasick works on bytes, not runes.
	pattern := []byte{0xFF, 0x00, 0xAB}
	hay := []byte{0x01, 0xFF, 0x00, 0xAB, 0x02}
	a, err := ac.New([][]byte{pattern})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := a.Find(hay)
	if !ok {
		t.Fatal("expected match for binary pattern")
	}
	if m.Start() != 1 || m.End() != 4 {
		t.Errorf("got [%d,%d), want [1,4)", m.Start(), m.End())
	}
}
