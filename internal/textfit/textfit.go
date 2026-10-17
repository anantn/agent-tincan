// Package textfit cuts text to fit a message cap: Truncate cuts one piece
// with a notice, and Fit shares a byte budget across several pieces.
package textfit

import (
	"fmt"
	"slices"
	"unicode/utf8"
)

// Reserve is the room kept for a truncation notice on each piece of text
// that may be cut.
const Reserve = 64

// Truncate cuts s to at most n bytes on a rune boundary and appends a
// notice (at most Reserve bytes) when it cut anything.
func Truncate(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	n = max(n, 0)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + fmt.Sprintf("\n[truncated: showing %d of %d bytes]", n, len(s)), true
}

// Fit shares budget bytes across pieces of the given lengths: each gets
// its whole length when that fits in an even share, and the room it
// leaves goes to the longer ones.
func Fit(lens []int, budget int) []int {
	out := make([]int, len(lens))
	order := make([]int, len(lens))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return lens[a] - lens[b] })
	left := max(budget, 0)
	for k, i := range order {
		share := left / (len(order) - k)
		out[i] = min(lens[i], share)
		left -= out[i]
	}
	return out
}
