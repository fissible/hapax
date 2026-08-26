package text_test

import (
	"math"
	"testing"

	"github.com/fissible/hapax/internal/text"
)

// Grapheme clusters whose internal structure a code-point-based snapper would
// happily split. Each is exactly one user-perceived character.
//
// byteLen is stated independently of the implementation so a mistyped fixture
// fails loudly instead of quietly weakening every assertion built on it.
var clusters = map[string]struct {
	text    string
	byteLen int
}{
	// Written with explicit escapes: a literal decomposed sequence in source can be
	// silently composed by an editor or tool, which would leave the fixture no longer
	// testing what its name claims.
	"zwj family":       {"\U0001F469\u200D\U0001F467", 11}, // woman + ZWJ + girl
	"regional flag":    {"\U0001F1FA\U0001F1F8", 8},        // two regional indicators
	"skin tone":        {"\U0001F44D\U0001F3FD", 8},        // thumbs up + skin-tone modifier
	"keycap":           {"1\uFE0F\u20E3", 7},               // '1' + VS16 + combining keycap
	"hangul jamo":      {"\u1100\u1161\u11A8", 9},          // choseong + jungseong + jongseong
	"combining stack":  {"a\u0301\u0327", 5},               // 'a' + acute + cedilla
	"composed base":    {"\u00E9\u0301", 4},                // precomposed e-acute + further mark
	"variation select": {"\u2764\uFE0F", 6},                // heart + VS16
}

func TestClusterFixturesHaveExpectedByteLengths(t *testing.T) {
	for name, c := range clusters {
		if got := len(c.text); got != c.byteLen {
			t.Errorf("fixture %q is %d bytes, expected %d — fixture is wrong, not the implementation", name, got, c.byteLen)
		}
	}
}

// Every span that starts or ends strictly inside a cluster must snap to exactly
// the cluster's bounds — not merely to something containing them. Asserting
// containment alone would be satisfied by an implementation that expands every
// span to the whole document.
func TestSnapNeverSplitsGraphemeClusters(t *testing.T) {
	for name, c := range clusters {
		t.Run(name, func(t *testing.T) {
			const prefix, suffix = "X", "Y"
			doc := mustAdmit(t, prefix+c.text+suffix)

			lo := len(prefix)
			hi := lo + c.byteLen
			want := text.Span{Offset: lo, Length: c.byteLen}

			for cut := lo + 1; cut < hi; cut++ {
				// A one-byte span sitting inside the cluster.
				in := text.Span{Offset: cut, Length: 1}
				if got := doc.Snap(in); got != want {
					t.Errorf("Snap(%+v) = %+v, want %+v (whole cluster, nothing more)", in, got, want)
				}
				// A span from the cluster start to an interior cut.
				in = text.Span{Offset: lo, Length: cut - lo}
				if got := doc.Snap(in); got != want {
					t.Errorf("Snap(%+v) = %+v, want %+v", in, got, want)
				}
				// A zero-length span at an interior position.
				in = text.Span{Offset: cut, Length: 0}
				if got := doc.Snap(in); got != want {
					t.Errorf("Snap(%+v) = %+v, want %+v", in, got, want)
				}
			}

			// The exact cluster bounds are already valid and must not move.
			if got := doc.Snap(want); got != want {
				t.Errorf("Snap(%+v) = %+v, want unchanged", want, got)
			}
			// Neighbours must not be swallowed: the prefix alone is a valid span.
			pre := text.Span{Offset: 0, Length: lo}
			if got := doc.Snap(pre); got != pre {
				t.Errorf("Snap(%+v) = %+v, want unchanged (prefix is its own cluster)", pre, got)
			}
		})
	}
}

// Offset+Length can overflow. Arithmetic saturates rather than wrapping, and
// the exact resulting span is asserted — an implementation that returns an empty
// in-bounds span would satisfy a bounds-only check while losing the whole range.
func TestSnapHandlesIntegerOverflow(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	n := len(doc.Raw()) // 7; valid boundaries are 0, 1, 4, 6, 7

	cases := []struct {
		name string
		in   text.Span
		want text.Span
	}{
		// start saturates past the end; empty span at the document end
		{"max offset, max length", text.Span{Offset: math.MaxInt, Length: math.MaxInt}, text.Span{Offset: n, Length: 0}},
		// start valid, end saturates past the end: covers the rest of the document
		{"valid offset, max length", text.Span{Offset: 1, Length: math.MaxInt}, text.Span{Offset: 1, Length: n - 1}},
		// start saturates below zero, end still below zero
		{"min offset, unit length", text.Span{Offset: math.MinInt, Length: 1}, text.Span{Offset: 0, Length: 0}},
		{"min offset, min length", text.Span{Offset: math.MinInt, Length: math.MinInt}, text.Span{Offset: 0, Length: 0}},
		// MaxInt+MinInt is -1: end falls before start, so the span is empty at the clamped start
		{"max offset, min length", text.Span{Offset: math.MaxInt, Length: math.MinInt}, text.Span{Offset: n, Length: 0}},
		{"zero offset, max length", text.Span{Offset: 0, Length: math.MaxInt}, text.Span{Offset: 0, Length: n}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := doc.Snap(c.in)
			if got != c.want {
				t.Errorf("Snap(%+v) = %+v, want %+v", c.in, got, c.want)
			}
			if again := doc.Snap(got); again != got {
				t.Errorf("Snap not idempotent after overflow: %+v then %+v", got, again)
			}
		})
	}
}
