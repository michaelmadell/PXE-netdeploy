package ui

import (
	"testing"
	"unicode/utf8"
)

func TestBar(t *testing.T) {
	for _, c := range []struct {
		pct, width int
		blocks     bool
		want       string
	}{
		{0, 4, true, "░░░░"},
		{100, 4, true, "████"},
		{50, 4, true, "██░░"},
		{8, 4, true, "░░░░"},  // 0.96 of a third: nothing yet
		{9, 4, true, "▒░░░"},  // 1.08 thirds
		{60, 4, true, "██▒░"}, // 7.2 thirds: 2 cells and 1/3
		{99, 4, true, "███▓"},
		{150, 4, true, "████"},
		{-5, 4, true, "░░░░"},
		{50, 4, false, "[##..]"},
	} {
		got := Bar(c.pct, c.width, c.blocks)
		if got != c.want {
			t.Errorf("Bar(%d, %d, %v) = %q, want %q", c.pct, c.width, c.blocks, got, c.want)
		}
		if c.blocks && utf8.RuneCountInString(got) != c.width {
			t.Errorf("Bar(%d): %d characters", c.pct, utf8.RuneCountInString(got))
		}
	}
}
