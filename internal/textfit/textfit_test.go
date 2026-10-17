package textfit

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Truncate leaves text that fits alone, and cuts longer text back to a
// rune boundary, never through a multi-byte rune, with a notice of the
// bytes kept.
func TestTruncate(t *testing.T) {
	if got, cut := Truncate("héllo", 6); got != "héllo" || cut {
		t.Fatalf("fits: %q %v", got, cut)
	}
	for _, tc := range []struct {
		s    string
		n    int
		keep string
	}{
		{"abcdef", 3, "abc"},
		{"aé", 2, "a"},   // n falls inside é
		{"日本語", 4, "日"},  // n falls inside 本
		{"日本語", 6, "日本"}, // n falls on a boundary
		{"abc", 0, ""},
		{"abc", -5, ""},
	} {
		got, cut := Truncate(tc.s, tc.n)
		kept, notice, ok := strings.Cut(got, "\n[truncated: ")
		if !cut || !ok || kept != tc.keep || !utf8.ValidString(got) || len(notice)+len("\n[truncated: ") > Reserve {
			t.Fatalf("Truncate(%q, %d) = %q %v, want %q kept", tc.s, tc.n, got, cut, tc.keep)
		}
		if want := "showing " + strconv.Itoa(len(tc.keep)) + " of " + strconv.Itoa(len(tc.s)) + " bytes]"; notice != want {
			t.Fatalf("Truncate(%q, %d) notice %q, want %q", tc.s, tc.n, notice, want)
		}
	}
}

// Fit gives a piece its whole length when it fits an even share, and
// hands the room short pieces leave to the longer ones, never going over
// the budget.
func TestFit(t *testing.T) {
	for _, tc := range []struct {
		lens   []int
		budget int
		want   []int
	}{
		{[]int{10, 20, 30}, 100, []int{10, 20, 30}},
		{[]int{10, 100, 100}, 100, []int{10, 45, 45}},
		{[]int{100, 5, 100}, 61, []int{28, 5, 28}},
		{[]int{50, 50}, 0, []int{0, 0}},
		{[]int{50, 50}, -1, []int{0, 0}},
		{nil, 10, []int{}},
	} {
		got := Fit(tc.lens, tc.budget)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("Fit(%v, %d) = %v, want %v", tc.lens, tc.budget, got, tc.want)
		}
		sum := 0
		for _, n := range got {
			sum += n
		}
		if sum > max(tc.budget, 0) {
			t.Fatalf("Fit(%v, %d) = %v, over budget", tc.lens, tc.budget, got)
		}
	}
}
