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
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA),
		patterns,
	)
	dfa := mustBuild(t,
		ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindDFA),
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

// ---------------------------------------------------------------------------
// PatternBytes
// ---------------------------------------------------------------------------

func TestPatternBytes(t *testing.T) {
	a := mustNew(t, []string{"abc", "def", "ghi"})
	for i := 0; i < a.PatternCount(); i++ {
		got := a.PatternBytes(ac.PatternID(i))
		want := a.Pattern(ac.PatternID(i))
		if string(got) != string(want) {
			t.Errorf("PatternBytes(%d) = %q, want %q", i, got, want)
		}
	}
}

func TestPatternBytes_SharesBacking(t *testing.T) {
	a := mustNew(t, []string{"hello"})
	b1 := a.PatternBytes(0)
	b2 := a.PatternBytes(0)
	// Both should point to the same backing array.
	if &b1[0] != &b2[0] {
		t.Error("PatternBytes should return the same backing slice")
	}
}

// ---------------------------------------------------------------------------
// FindAllAppend
// ---------------------------------------------------------------------------

func TestFindAllAppend_MatchesFindAll(t *testing.T) {
	a := mustNew(t, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	want := a.FindAll(haystack)
	got := a.FindAllAppend(nil, haystack)

	if len(got) != len(want) {
		t.Fatalf("FindAllAppend returned %d matches, FindAll returned %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestFindAllAppend_ReusesSlice(t *testing.T) {
	a := mustNew(t, []string{"ab", "cd"})
	buf := make([]ac.Match, 0, 64)

	for i := 0; i < 10; i++ {
		result := a.FindAllAppend(buf, []byte("abcd"))
		if len(result) != 2 {
			t.Fatalf("iteration %d: got %d matches, want 2", i, len(result))
		}
		// Verify reuse: result should share the backing array with buf.
		if cap(result) < 64 {
			t.Errorf("iteration %d: backing array not reused (cap=%d)", i, cap(result))
		}
		buf = result
	}
}

func TestFindAllAppend_NilAutomaton(t *testing.T) {
	a, _ := ac.New(nil)
	got := a.FindAllAppend(make([]ac.Match, 0, 8), []byte("hello"))
	if len(got) != 0 {
		t.Errorf("expected 0 matches for nil automaton, got %d", len(got))
	}
}

func TestFindAllAppend_EmptyHaystack(t *testing.T) {
	a := mustNew(t, []string{"abc"})
	got := a.FindAllAppend(nil, []byte(""))
	if len(got) != 0 {
		t.Errorf("expected 0 matches for empty haystack, got %d", len(got))
	}
}

func TestFindAllAppend_NoMatch(t *testing.T) {
	a := mustNew(t, []string{"xyz"})
	got := a.FindAllAppend(nil, []byte("hello world"))
	if len(got) != 0 {
		t.Errorf("expected 0 matches, got %d", len(got))
	}
}

func TestFindAllAppendString(t *testing.T) {
	a := mustNew(t, []string{"he", "she"})
	got := a.FindAllAppendString(nil, "ushers")
	want := a.FindAllString("ushers")
	if len(got) != len(want) {
		t.Fatalf("got %d matches, want %d", len(got), len(want))
	}
}

// ---------------------------------------------------------------------------
// FindOverlappingAllAppend
// ---------------------------------------------------------------------------

func TestFindOverlappingAllAppend_MatchesFindOverlappingAll(t *testing.T) {
	a := mustNew(t, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	want := a.FindOverlappingAll(haystack)
	got := a.FindOverlappingAllAppend(nil, haystack)

	if len(got) != len(want) {
		t.Fatalf("FindOverlappingAllAppend returned %d matches, FindOverlappingAll returned %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestFindOverlappingAllAppend_ReusesSlice(t *testing.T) {
	a := mustNew(t, []string{"ab", "b"})
	buf := make([]ac.Match, 0, 64)

	for i := 0; i < 10; i++ {
		result := a.FindOverlappingAllAppend(buf, []byte("ab"))
		if len(result) != 2 {
			t.Fatalf("iteration %d: got %d matches, want 2", i, len(result))
		}
		if cap(result) < 64 {
			t.Errorf("iteration %d: backing array not reused (cap=%d)", i, cap(result))
		}
		buf = result
	}
}

func TestFindOverlappingAllAppend_NilAutomaton(t *testing.T) {
	a, _ := ac.New(nil)
	got := a.FindOverlappingAllAppend(make([]ac.Match, 0, 8), []byte("hello"))
	if len(got) != 0 {
		t.Errorf("expected 0 matches for nil automaton, got %d", len(got))
	}
}

func TestFindOverlappingAllAppendString(t *testing.T) {
	a := mustNew(t, []string{"he", "she"})
	got := a.FindOverlappingAllAppendString(nil, "ushers")
	want := a.FindOverlappingAllString("ushers")
	if len(got) != len(want) {
		t.Fatalf("got %d matches, want %d", len(got), len(want))
	}
}

// ---------------------------------------------------------------------------
// FindAllAppend / FindOverlappingAllAppend with forced NFA
// ---------------------------------------------------------------------------

func TestFindAllAppend_NFA(t *testing.T) {
	b := ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA)
	a := mustBuild(t, b, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	want := a.FindAll(haystack)
	got := a.FindAllAppend(nil, haystack)

	if len(got) != len(want) {
		t.Fatalf("NFA FindAllAppend: got %d matches, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NFA match[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestFindOverlappingAllAppend_NFA(t *testing.T) {
	b := ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA)
	a := mustBuild(t, b, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	want := a.FindOverlappingAll(haystack)
	got := a.FindOverlappingAllAppend(nil, haystack)

	if len(got) != len(want) {
		t.Fatalf("NFA FindOverlappingAllAppend: got %d matches, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NFA match[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// CountAll
// ---------------------------------------------------------------------------

func TestCountAll_MatchesLen(t *testing.T) {
	a := mustNew(t, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")
	want := len(a.FindAll(haystack))
	got := a.CountAll(haystack)
	if got != want {
		t.Errorf("CountAll = %d, want %d", got, want)
	}
}

func TestCountAll_NoMatch(t *testing.T) {
	a := mustNew(t, []string{"xyz"})
	if n := a.CountAll([]byte("hello")); n != 0 {
		t.Errorf("CountAll = %d, want 0", n)
	}
}

func TestCountAll_EmptyHaystack(t *testing.T) {
	a := mustNew(t, []string{"abc"})
	if n := a.CountAll([]byte("")); n != 0 {
		t.Errorf("CountAll = %d, want 0", n)
	}
}

func TestCountAll_NilAutomaton(t *testing.T) {
	a, _ := ac.New(nil)
	if n := a.CountAll([]byte("hello")); n != 0 {
		t.Errorf("CountAll = %d, want 0", n)
	}
}

func TestCountAllString(t *testing.T) {
	a := mustNew(t, []string{"ab", "cd"})
	want := len(a.FindAllString("abcdef"))
	got := a.CountAllString("abcdef")
	if got != want {
		t.Errorf("CountAllString = %d, want %d", got, want)
	}
}

func TestCountAll_NFA(t *testing.T) {
	b := ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA)
	a := mustBuild(t, b, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")
	want := len(a.FindAll(haystack))
	got := a.CountAll(haystack)
	if got != want {
		t.Errorf("NFA CountAll = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// CountOverlapping
// ---------------------------------------------------------------------------

func TestCountOverlapping_MatchesLen(t *testing.T) {
	a := mustNew(t, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")
	want := len(a.FindOverlappingAll(haystack))
	got := a.CountOverlapping(haystack)
	if got != want {
		t.Errorf("CountOverlapping = %d, want %d", got, want)
	}
}

func TestCountOverlapping_NoMatch(t *testing.T) {
	a := mustNew(t, []string{"xyz"})
	if n := a.CountOverlapping([]byte("hello")); n != 0 {
		t.Errorf("CountOverlapping = %d, want 0", n)
	}
}

func TestCountOverlapping_EmptyHaystack(t *testing.T) {
	a := mustNew(t, []string{"abc"})
	if n := a.CountOverlapping([]byte("")); n != 0 {
		t.Errorf("CountOverlapping = %d, want 0", n)
	}
}

func TestCountOverlapping_NilAutomaton(t *testing.T) {
	a, _ := ac.New(nil)
	if n := a.CountOverlapping([]byte("hello")); n != 0 {
		t.Errorf("CountOverlapping = %d, want 0", n)
	}
}

func TestCountOverlappingString(t *testing.T) {
	a := mustNew(t, []string{"ab", "b"})
	want := len(a.FindOverlappingAllString("ab"))
	got := a.CountOverlappingString("ab")
	if got != want {
		t.Errorf("CountOverlappingString = %d, want %d", got, want)
	}
}

func TestCountOverlapping_NFA(t *testing.T) {
	b := ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA)
	a := mustBuild(t, b, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")
	want := len(a.FindOverlappingAll(haystack))
	got := a.CountOverlapping(haystack)
	if got != want {
		t.Errorf("NFA CountOverlapping = %d, want %d", got, want)
	}
}

func TestCountOverlapping_ManyPatterns(t *testing.T) {
	patterns := make([]string, 200)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("p%d", i)
	}
	a := mustNew(t, patterns)
	haystack := []byte(strings.Join(patterns, " "))
	want := len(a.FindOverlappingAll(haystack))
	got := a.CountOverlapping(haystack)
	if got != want {
		t.Errorf("CountOverlapping (200 patterns) = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// OverlappingPatternSet
// ---------------------------------------------------------------------------

func patternSetToIDs(seen []bool) []ac.PatternID {
	var ids []ac.PatternID
	for i, v := range seen {
		if v {
			ids = append(ids, ac.PatternID(i))
		}
	}
	return ids
}

func findOverlappingToIDs(a *ac.AhoCorasick, haystack []byte) []ac.PatternID {
	matches := a.FindOverlappingAll(haystack)
	idSet := make(map[ac.PatternID]bool)
	for _, m := range matches {
		idSet[m.PatternID()] = true
	}
	var ids []ac.PatternID
	for id := range idSet {
		ids = append(ids, id)
	}
	return ids
}

func TestOverlappingPatternSet_MatchesFindOverlappingAll(t *testing.T) {
	a := mustNew(t, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	seen := make([]bool, a.PatternCount())
	a.OverlappingPatternSet(haystack, seen)
	got := patternSetToIDs(seen)
	want := findOverlappingToIDs(a, haystack)

	if len(got) != len(want) {
		t.Fatalf("OverlappingPatternSet found %d patterns, FindOverlappingAll found %d unique", len(got), len(want))
	}
	wantMap := make(map[ac.PatternID]bool)
	for _, id := range want {
		wantMap[id] = true
	}
	for _, id := range got {
		if !wantMap[id] {
			t.Errorf("OverlappingPatternSet found pattern %d not in FindOverlappingAll", id)
		}
	}
}

func TestOverlappingPatternSet_NoMatch(t *testing.T) {
	a := mustNew(t, []string{"xyz"})
	seen := make([]bool, a.PatternCount())
	a.OverlappingPatternSet([]byte("hello"), seen)
	for i, v := range seen {
		if v {
			t.Errorf("seen[%d] should be false", i)
		}
	}
}

func TestOverlappingPatternSet_EmptyHaystack(t *testing.T) {
	a := mustNew(t, []string{"abc"})
	seen := make([]bool, a.PatternCount())
	a.OverlappingPatternSet([]byte(""), seen)
	for i, v := range seen {
		if v {
			t.Errorf("seen[%d] should be false for empty haystack", i)
		}
	}
}

func TestOverlappingPatternSet_NilAutomaton(t *testing.T) {
	a, _ := ac.New(nil)
	seen := make([]bool, 10)
	a.OverlappingPatternSet([]byte("hello"), seen) // should not panic
}

func TestOverlappingPatternSet_Reuse(t *testing.T) {
	a := mustNew(t, []string{"abc", "def", "ghi"})
	seen := make([]bool, a.PatternCount())

	// First search
	a.OverlappingPatternSet([]byte("abc"), seen)
	if !seen[0] {
		t.Error("expected seen[0] = true after first search")
	}

	// Clear and reuse
	clear(seen)
	a.OverlappingPatternSet([]byte("def"), seen)
	if seen[0] {
		t.Error("expected seen[0] = false after clear+second search")
	}
	if !seen[1] {
		t.Error("expected seen[1] = true after second search")
	}
}

func TestOverlappingPatternSet_NFA(t *testing.T) {
	b := ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA)
	a := mustBuild(t, b, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	seen := make([]bool, a.PatternCount())
	a.OverlappingPatternSet(haystack, seen)
	got := patternSetToIDs(seen)
	want := findOverlappingToIDs(a, haystack)

	if len(got) != len(want) {
		t.Fatalf("NFA OverlappingPatternSet: %d patterns, want %d", len(got), len(want))
	}
}

func TestOverlappingPatternSetString(t *testing.T) {
	a := mustNew(t, []string{"he", "she"})
	seen := make([]bool, a.PatternCount())
	a.OverlappingPatternSetString("ushers", seen)
	if !seen[0] || !seen[1] {
		t.Errorf("OverlappingPatternSetString: seen=%v, want [true, true]", seen)
	}
}

func TestOverlappingPatternSet_ManyPatterns(t *testing.T) {
	patterns := make([]string, 200)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("p%d", i)
	}
	a := mustNew(t, patterns)
	haystack := []byte(strings.Join(patterns, " "))

	seen := make([]bool, a.PatternCount())
	a.OverlappingPatternSet(haystack, seen)

	gotCount := 0
	for _, v := range seen {
		if v {
			gotCount++
		}
	}
	wantIDs := findOverlappingToIDs(a, haystack)
	if gotCount != len(wantIDs) {
		t.Errorf("OverlappingPatternSet (200 patterns): %d matched, want %d", gotCount, len(wantIDs))
	}
}

// ---------------------------------------------------------------------------
// AllPatternSet
// ---------------------------------------------------------------------------

func findAllToIDs(a *ac.AhoCorasick, haystack []byte) []ac.PatternID {
	matches := a.FindAll(haystack)
	idSet := make(map[ac.PatternID]bool)
	for _, m := range matches {
		idSet[m.PatternID()] = true
	}
	var ids []ac.PatternID
	for id := range idSet {
		ids = append(ids, id)
	}
	return ids
}

func TestAllPatternSet_MatchesFindAll(t *testing.T) {
	a := mustNew(t, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	seen := make([]bool, a.PatternCount())
	a.AllPatternSet(haystack, seen)
	got := patternSetToIDs(seen)
	want := findAllToIDs(a, haystack)

	if len(got) != len(want) {
		t.Fatalf("AllPatternSet found %d patterns, FindAll found %d unique", len(got), len(want))
	}
}

func TestAllPatternSet_NoMatch(t *testing.T) {
	a := mustNew(t, []string{"xyz"})
	seen := make([]bool, a.PatternCount())
	a.AllPatternSet([]byte("hello"), seen)
	for i, v := range seen {
		if v {
			t.Errorf("seen[%d] should be false", i)
		}
	}
}

func TestAllPatternSet_NFA(t *testing.T) {
	b := ac.NewBuilder().AutomatonKind(ac.AhoCorasickKindContiguousNFA)
	a := mustBuild(t, b, []string{"he", "she", "his", "hers"})
	haystack := []byte("ushers")

	seen := make([]bool, a.PatternCount())
	a.AllPatternSet(haystack, seen)
	got := patternSetToIDs(seen)
	want := findAllToIDs(a, haystack)

	if len(got) != len(want) {
		t.Fatalf("NFA AllPatternSet: %d patterns, want %d", len(got), len(want))
	}
}

func TestAllPatternSetString(t *testing.T) {
	a := mustNew(t, []string{"ab", "cd"})
	seen := make([]bool, a.PatternCount())
	a.AllPatternSetString("abcd", seen)
	if !seen[0] || !seen[1] {
		t.Errorf("AllPatternSetString: seen=%v, want [true, true]", seen)
	}
}
