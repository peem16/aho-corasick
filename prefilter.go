package ahocorasick

import "bytes"

// ---------------------------------------------------------------------------
// Prefilter — SIMD-accelerated candidate detection
// ---------------------------------------------------------------------------
//
// The prefilter works by scanning ahead in the haystack for positions that
// could possibly be the start of any pattern match.  It does this by
// maintaining a set of "first bytes" — the first byte of every pattern.
//
// When the automaton is at the start state and the current position does
// not look like the start of any pattern, we skip forward to the next
// candidate position using bytes.IndexByte (which uses AVX2 on amd64).
//
// The prefilter is only active when the automaton is at the start state
// (i.e., no partial match is in progress), and only when the number of
// distinct first bytes is small (≤ 3 by default, matching the Rust crate
// heuristic).  For more patterns the cost of the prefilter exceeds its
// benefit.

const maxPrefilterBytes = 16  // maximum distinct first bytes to prefilter on
const maxPrefilterPatterns = 100000 // disable prefilter above this pattern count

// prefilter holds the precomputed acceleration data.
type prefilter struct {
	enabled bool
	// firstSet[b] is true when b is the first byte of at least one pattern.
	firstSet [256]bool
	// Cached distinct first bytes for SIMD fast path (up to 5).
	cachedBytes [5]byte
	distinct    int
}

// newPrefilter builds a prefilter for the given patterns.
// Returns a disabled prefilter when the heuristic deems it unprofitable.
func newPrefilter(patterns [][]byte, alphabet [256]byte, useAlpha bool) *prefilter {
	pf := &prefilter{}

	if len(patterns) > maxPrefilterPatterns {
		return pf // disabled
	}

	var firsts [256]bool
	for _, pat := range patterns {
		if len(pat) == 0 {
			// Empty pattern matches everywhere; prefilter useless.
			return pf
		}
		b := pat[0]
		if useAlpha {
			// Normalize to find the canonical byte.
			norm := alphabet[b]
			// Add ALL raw bytes that normalize to this canonical byte,
			// so the prefilter correctly finds e.g. both 'h' and 'H'.
			for raw := 0; raw < 256; raw++ {
				if alphabet[byte(raw)] == norm {
					firsts[byte(raw)] = true
				}
			}
		} else {
			firsts[b] = true
		}
	}

	count := 0
	var bs [maxPrefilterBytes]byte
	for b := 0; b < 256; b++ {
		if firsts[byte(b)] {
			if count < maxPrefilterBytes {
				bs[count] = byte(b)
			}
			count++
		}
	}

	if count == 0 || count > maxPrefilterBytes {
		return pf // disabled
	}

	pf.enabled = true
	pf.firstSet = firsts
	pf.distinct = count
	for i := 0; i < count && i < len(pf.cachedBytes); i++ {
		pf.cachedBytes[i] = bs[i]
	}
	return pf
}

// next returns the next position in haystack[pos:] that could be the start
// of a match, or -1 if no such position exists.
// The caller must only invoke next when the automaton is at startStateID.
//
//go:nosplit
func (pf *prefilter) next(haystack []byte, pos int) int {
	if pos >= len(haystack) {
		return -1
	}
	sub := haystack[pos:]
	cb := &pf.cachedBytes
	switch pf.distinct {
	case 1:
		i := bytes.IndexByte(sub, cb[0])
		if i < 0 {
			return -1
		}
		return pos + i
	case 2:
		i := indexByteTwo(sub, cb[0], cb[1])
		if i < 0 {
			return -1
		}
		return pos + i
	case 3:
		i := indexByteThree(sub, cb[0], cb[1], cb[2])
		if i < 0 {
			return -1
		}
		return pos + i
	case 4:
		i := indexByteFour(sub, cb[0], cb[1], cb[2], cb[3])
		if i < 0 {
			return -1
		}
		return pos + i
	case 5:
		i := indexByteFive(sub, cb[0], cb[1], cb[2], cb[3], cb[4])
		if i < 0 {
			return -1
		}
		return pos + i
	default:
		// Bitmap scan for 6+ distinct first bytes.
		fs := &pf.firstSet
		for i, b := range sub {
			if fs[b] {
				return pos + i
			}
		}
		return -1
	}
}

// indexByteTwo finds the first occurrence of b0 or b1 in s.
// Uses SIMD-accelerated bytes.IndexByte for each byte and returns the minimum.
func indexByteTwo(s []byte, b0, b1 byte) int {
	i0 := bytes.IndexByte(s, b0)
	i1 := bytes.IndexByte(s, b1)
	if i0 < 0 {
		return i1
	}
	if i1 < 0 {
		return i0
	}
	if i0 < i1 {
		return i0
	}
	return i1
}

// indexByteThree finds the first occurrence of b0, b1, or b2 in s.
// Uses SIMD-accelerated bytes.IndexByte for each byte and returns the minimum.
func indexByteThree(s []byte, b0, b1, b2 byte) int {
	i0 := bytes.IndexByte(s, b0)
	i1 := bytes.IndexByte(s, b1)
	i2 := bytes.IndexByte(s, b2)
	min := i0
	if min < 0 || (i1 >= 0 && i1 < min) {
		min = i1
	}
	if min < 0 || (i2 >= 0 && i2 < min) {
		min = i2
	}
	return min
}

// indexByteFour finds the first occurrence of any of 4 bytes in s.
func indexByteFour(s []byte, b0, b1, b2, b3 byte) int {
	i0 := bytes.IndexByte(s, b0)
	i1 := bytes.IndexByte(s, b1)
	i2 := bytes.IndexByte(s, b2)
	i3 := bytes.IndexByte(s, b3)
	min := i0
	if min < 0 || (i1 >= 0 && i1 < min) {
		min = i1
	}
	if min < 0 || (i2 >= 0 && i2 < min) {
		min = i2
	}
	if min < 0 || (i3 >= 0 && i3 < min) {
		min = i3
	}
	return min
}

// indexByteFive finds the first occurrence of any of 5 bytes in s.
func indexByteFive(s []byte, b0, b1, b2, b3, b4 byte) int {
	i0 := bytes.IndexByte(s, b0)
	i1 := bytes.IndexByte(s, b1)
	i2 := bytes.IndexByte(s, b2)
	i3 := bytes.IndexByte(s, b3)
	i4 := bytes.IndexByte(s, b4)
	min := i0
	if min < 0 || (i1 >= 0 && i1 < min) {
		min = i1
	}
	if min < 0 || (i2 >= 0 && i2 < min) {
		min = i2
	}
	if min < 0 || (i3 >= 0 && i3 < min) {
		min = i3
	}
	if min < 0 || (i4 >= 0 && i4 < min) {
		min = i4
	}
	return min
}
