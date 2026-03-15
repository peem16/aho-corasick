package ahocorasick

import "fmt"

// ---------------------------------------------------------------------------
// ReplaceAll / ReplaceAllWith
// ---------------------------------------------------------------------------

// ReplaceAll replaces each non-overlapping match in haystack with the
// corresponding replacement byte slice.  replacements must have length
// equal to PatternCount(); replacements[i] is used for pattern i.
//
// If a replacement slice is nil, the matched bytes are removed.
func (ac *AhoCorasick) ReplaceAll(haystack []byte, replacements [][]byte) ([]byte, error) {
	if len(replacements) != ac.patCount {
		return nil, fmt.Errorf(
			"aho-corasick: ReplaceAll: replacements length %d != pattern count %d",
			len(replacements), ac.patCount,
		)
	}
	return ac.ReplaceAllWith(haystack, func(m Match) []byte {
		return replacements[m.PatternID()]
	}), nil
}

// ReplaceAllString is a convenience wrapper for string inputs.
func (ac *AhoCorasick) ReplaceAllString(haystack string, replacements []string) (string, error) {
	repls := make([][]byte, len(replacements))
	for i, r := range replacements {
		repls[i] = []byte(r)
	}
	out, err := ac.ReplaceAll([]byte(haystack), repls)
	return string(out), err
}

// ReplaceAllWith replaces each non-overlapping match by calling f and
// appending its return value.  If f returns nil, the match is removed.
func (ac *AhoCorasick) ReplaceAllWith(haystack []byte, f func(Match) []byte) []byte {
	if ac.imp == nil || len(haystack) == 0 {
		cp := make([]byte, len(haystack))
		copy(cp, haystack)
		return cp
	}

	// Pre-allocate output with extra headroom for replacements.
	// Adding 25% avoids re-allocation when replacements are slightly
	// larger than the matched text.
	cap := len(haystack) + len(haystack)/4
	out := make([]byte, 0, cap)
	prev := 0 // end of last match in haystack

	it := ac.FindIter(haystack)
	for {
		m, ok := it.Next()
		if !ok {
			break
		}
		// Append bytes before this match.
		out = append(out, haystack[prev:m.start]...)
		// Append replacement.
		if repl := f(m); repl != nil {
			out = append(out, repl...)
		}
		prev = m.end
	}
	it.Close()

	// Append remaining bytes after last match.
	out = append(out, haystack[prev:]...)
	return out
}

// ReplaceAllWithString is a convenience wrapper for string inputs/outputs.
func (ac *AhoCorasick) ReplaceAllWithString(haystack string, f func(Match) string) string {
	return string(ac.ReplaceAllWith([]byte(haystack), func(m Match) []byte {
		return []byte(f(m))
	}))
}
