package text_test

// #107. #91's guard is a FIRST-OCCURRENCE check. It refuses a script the
// paragraph does not use, and says nothing about a script it already uses, so
// one pre-existing character disarms it:
//
//	The author 著 never draws it.                 22 letters, Han=1
//	作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。   24 letters, Han=24
//
// Nothing is introduced — Han was already there — so #91 accepts the second as
// a rewrite of the first.
//
// # Two designs measured and discarded first
//
// **An order statistic.** "The set of scripts holding the most letters must not
// grow." Measured against #91's own incident: the candidate is Latin=86 Han=21,
// so Latin still holds the plurality and it is ACCEPTED. Both failure cases
// preserve rank order, so no constant-free order statistic can see them — they
// are magnitude failures.
//
// **A corpus reference.** "No script may exceed what the author's corpus
// predicts for a text of this length", with the corpus-wide ratio as the
// prediction. It refuses every LENGTHENING rewrite, analytically: a candidate
// written wholly in one script has count == length, and the corpus ratio is
// below one whenever the corpus holds a single letter of anything else, so the
// bound falls below the length and the rule reduces to "never longer than the
// current paragraph". Measured on the incident's own 52-letter paragraph, 53
// letters is refused at bound 52.999910 — and #91's diagnosis was that the bad
// candidate won on LENGTH, so this forbids exactly the direction the distance
// measure pulls toward.
//
// # The rule
//
// A script's count may grow only in proportion to the paragraph's growth:
//
//	count(candidate, s)  <=  max( count(original, s),  share(original, s) * letters(candidate) )
//
// Counts rather than shares, because a rewrite that shortens the Latin while
// touching no Han raises Han's SHARE and would otherwise be refused for a change
// it did not make. One Han letter stays one Han letter however the Latin moves.
//
// The `max` with the original count is what lets a paragraph that already
// carries a foreign script be rewritten at all: it may keep what it has. Without
// it every candidate that merely preserves the existing letters is refused,
// which is the systematic-refusal cost #91 flagged.
//
// # Against the ORIGINAL, not the current text
//
// `Overgrown` takes the paragraph the loop started from. Anchoring on the
// advancing `current` lets the bound ratchet, because the carried-over term is
// absolute while the proportional term is length-relative — grow to bank an
// absolute count, then shrink to shrink the denominator:
//
//	start      Han=1  Latin=51   (52 letters, 1.9%)
//	attempt 1  L=520, bound 10 -> take 10 Han   (1.9%)   accepted
//	attempt 2  L=52,  bound 10 -> take 10 Han   (19.2%)  accepted
//
// Both steps satisfy the rule against their own predecessor. Two accepted
// attempts inside one invocation, and the share has multiplied tenfold. The
// original is a fixed anchor, and it trivially satisfies its own bound, so
// nothing is lost by using it.
//
// # It subsumes #91, and that is deliberate
//
// A script absent from the original has count 0 and share 0, so its bound is 0
// and any appearance is overgrowth. #91's rule is the special case where the
// original count is exactly zero. Both are kept: the introduction has its own
// reason code because "a script that was never here" is a more useful thing to
// tell a writer than "a script grew", and the arithmetic agreeing with it is
// asserted below rather than assumed.

import (
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// Count
// ---------------------------------------------------------------------------

// The raw counts, which `Share` and `Letters` could only reconstruct lossily.
// Every value measured, not counted by hand.
func TestCountReportsLettersPerScript(t *testing.T) {
	for _, c := range []struct {
		name, input string
		want        map[string]int
	}{
		{
			name:  "one script",
			input: "The argument turns on a distinction the author never draws.",
			want:  map[string]int{"Latin": 49, "Han": 0, "Greek": 0},
		},
		{
			name:  "a single foreign character",
			input: "The author 著 never draws it.",
			want:  map[string]int{"Latin": 21, "Han": 1},
		},
		{
			name:  "three scripts at once",
			input: "The author, автор 著者, never draws it.",
			want:  map[string]int{"Latin": 21, "Cyrillic": 5, "Han": 2},
		},
		{
			// Punctuation and digits are Common, counted in neither the
			// numerator nor the denominator.
			name:  "no letters at all",
			input: "1979 — (42) !!",
			want:  map[string]int{"Latin": 0, "Han": 0},
		},
		{
			// MULTI-LINE, because a mutation truncating input at the first
			// newline has passed a whole package in this project before.
			name:  "spanning a line break",
			input: "The author never draws it,\nand the reader never asks.",
			want:  map[string]int{"Latin": 42},
		},
		{
			// And a blank line, with letters either side and the count being
			// the sum rather than either half.
			name:  "spanning a blank line",
			input: "漢字漢字漢\n\n字漢字 ab",
			want:  map[string]int{"Han": 8, "Latin": 2},
		},
		{
			name:  "an undeclared name counts zero rather than panicking",
			input: "The author never draws it.",
			want:  map[string]int{"Tengwar": 0, "": 0, "Common": 0, "Inherited": 0},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			set := text.Scripts(c.input)
			for name, want := range c.want {
				if got := set.Count(name); got != want {
					t.Errorf("Count(%q) = %d, want %d", name, got, want)
				}
			}
		})
	}
}

// Count agrees with the two accessors already exposed.
//
// Share is a ratio and Letters is a total, so `Share*Letters` is the count up to
// floating-point error. Asserting the relation catches a Count that reads a
// different map or forgets to normalize, without re-deriving the arithmetic.
func TestCountAgreesWithShareAndLetters(t *testing.T) {
	for _, input := range []string{
		"The argument turns on a distinction the author never draws.",
		"The author 著 never draws it.",
		"The author, автор 著者, never draws it.",
		"αβγδ ab 漢",
		"漢字漢字漢\n\n字漢字 ab",
		"1979 — (42) !!",
		"",
	} {
		set := text.Scripts(input)
		summed := 0
		for _, name := range set.Names() {
			count := set.Count(name)
			if count <= 0 {
				t.Errorf("%q: Names() lists %s but Count is %d", input, name, count)
			}
			summed += count
			// Share is count/letters, so count must be share*letters.
			if want := set.Share(name) * float64(set.Letters()); absDiff(float64(count), want) > 1e-9 {
				t.Errorf("%q: Count(%s) = %d, but Share*Letters = %v", input, name, count, want)
			}
		}
		// And nothing is counted that Names does not list.
		if summed != set.Letters() {
			t.Errorf("%q: the listed counts sum to %d, Letters() is %d",
				input, summed, set.Letters())
		}
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// The zero value answers rather than panicking: its count map is nil.
func TestTheZeroScriptSetCountsZero(t *testing.T) {
	var zero text.ScriptSet
	if got := zero.Count("Latin"); got != 0 {
		t.Errorf("the zero value counts %d Latin letters", got)
	}
	if got := zero.Letters(); got != 0 {
		t.Errorf("the zero value has %d letters", got)
	}
}

// ---------------------------------------------------------------------------
// Overgrown
// ---------------------------------------------------------------------------

// The rule itself, as a table of measured cases.
//
// The receiver is the CANDIDATE and the argument is the ORIGINAL, matching
// `Introduced`, so the call reads the way the gate asks the question.
func TestOvergrownNamesTheScriptsThatGrewOutOfProportion(t *testing.T) {
	for _, c := range []struct {
		name, original, candidate string
		want                      []string
	}{
		{
			name:      "an ordinary rewrite moves no script",
			original:  "The argument turns on a distinction the author never draws.",
			candidate: "The argument rests on a distinction the author never makes.",
			want:      nil,
		},
		{
			// The whole point: a longer rewrite in the SAME script is fine.
			// The corpus-reference design refused this, which is why it died.
			name:      "lengthening in the same script is not growth",
			original:  "The author never draws it.",
			candidate: "The author never draws it, and the reader never thinks to ask him why that is.",
			want:      nil,
		},
		{
			// #107 itself. Han=1 of 22 gives a bound of max(1, 1/22*24) = 1.09,
			// and the candidate has 24.
			name:      "the script that was one character becomes the paragraph",
			original:  "The author 著 never draws it.",
			candidate: "作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。",
			want:      []string{"Han"},
		},
		{
			// Han=1 of 33 against a 22-letter candidate gives max(1, 0.67) = 1,
			// so keeping the one character is admissible however much Latin
			// went away. This is the case a SHARE rule refuses wrongly.
			name:      "keeping one character while the rest shortens",
			original:  "The author 著 never draws it at all, he said.",
			candidate: "The author 著 never draws it.",
			want:      nil,
		},
		{
			name:      "removing a script is not growing it",
			original:  "The author 著 never draws it at all, he said.",
			candidate: "The argument turns on a distinction the author never draws.",
			want:      nil,
		},
		{
			// Symmetric in the scripts: it protects whatever the paragraph was
			// written in. A Greek-dominant original rewritten mostly into Latin
			// overgrows LATIN, and picks up Cyrillic from zero at the same time.
			// Greek bound max(4, 16) = 16 against 0; Han max(1, 4) = 4 against 2.
			name:      "the dominant script of the original can itself overgrow",
			original:  "αβγδ ab 漢",
			candidate: "The author, автор 著者, never draws it.",
			want:      []string{"Cyrillic", "Latin"},
		},
		{
			// Absent from the original means bound zero, so any appearance is
			// growth. #91's rule is this case; see the subsumption test below.
			name:      "a script absent from the original has a bound of zero",
			original:  "The argument turns on a distinction the author never draws.",
			candidate: "The argument turns 漢 on a distinction.",
			want:      []string{"Han"},
		},
		{
			// A letterless original gives every script a share of zero, so
			// prose is growth. This is the hole the discarded order-statistic
			// design had: an empty set is a subset of everything.
			name:      "a letterless original admits no letters",
			original:  "1979 — (42) !!",
			candidate: "The argument turns on a distinction.",
			want:      []string{"Latin"},
		},
		{
			name:      "a letterless candidate grows nothing",
			original:  "The argument turns on a distinction the author never draws.",
			candidate: "1979 — (42) !!",
			want:      nil,
		},
		{
			// MULTI-LINE on both sides.
			name:      "across a line break and a blank line",
			original:  "The author never draws it,\nand the reader never asks.",
			candidate: "漢字漢字漢\n\n字漢字 ab",
			want:      []string{"Han"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := text.Scripts(c.candidate).Overgrown(text.Scripts(c.original))
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Overgrown() = %v, want %v", got, c.want)
			}
		})
	}
}

// Overgrown is exactly the bound, checked against the bound rather than against
// a second implementation of it.
//
// A method that used shares instead of counts, or compared against the
// candidate's own share, or dropped the `max`, disagrees here on some pair.
func TestOvergrownIsExactlyTheProportionalBound(t *testing.T) {
	texts := []string{
		"The argument turns on a distinction the author never draws.",
		"The author 著 never draws it.",
		"The author 著 never draws it at all, he said.",
		"作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。",
		"abc 漢字漢",
		"αβγδ ab 漢",
		"The author, автор 著者, never draws it.",
		"1979 — (42) !!",
		"漢字漢字漢\n\n字漢字 ab",
	}
	for _, original := range texts {
		for _, candidate := range texts {
			originalSet, candidateSet := text.Scripts(original), text.Scripts(candidate)

			var want []string
			for _, name := range candidateSet.Names() {
				bound := float64(originalSet.Count(name))
				if proportional := originalSet.Share(name) * float64(candidateSet.Letters()); proportional > bound {
					bound = proportional
				}
				if float64(candidateSet.Count(name)) > bound {
					want = append(want, name)
				}
			}
			sort.Strings(want)

			got := candidateSet.Overgrown(originalSet)
			if len(got) == 0 && len(want) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q -> %q: Overgrown() = %v, want %v", original, candidate, got, want)
			}
		}
	}
}

// Anything introduced is also overgrown.
//
// The two rules are kept separate for the sake of the reason a writer reads,
// not because they are independent: a script absent from the original has a
// bound of zero, so #91's rule is the zero case of this one. Asserting the
// containment keeps them from drifting apart — a carve-out excluding absent
// scripts from the bound would break it, and so would a bound that treated an
// absent script as unconstrained.
func TestEverythingIntroducedIsAlsoOvergrown(t *testing.T) {
	texts := []string{
		"The argument turns on a distinction the author never draws.",
		"The author 著 never draws it.",
		"作者從不畫它,著者亦然。",
		"abc 漢字漢",
		"αβγδ ab 漢",
		"The author, автор 著者, never draws it.",
		"1979 — (42) !!",
	}
	for _, original := range texts {
		for _, candidate := range texts {
			originalSet, candidateSet := text.Scripts(original), text.Scripts(candidate)
			overgrown := map[string]bool{}
			for _, name := range candidateSet.Overgrown(originalSet) {
				overgrown[name] = true
			}
			for _, name := range candidateSet.Introduced(originalSet) {
				if !overgrown[name] {
					t.Errorf("%q -> %q: %s is introduced but not overgrown; overgrown = %v",
						original, candidate, name, candidateSet.Overgrown(originalSet))
				}
			}
		}
	}
}

// The original is a fixed anchor, so growing then shrinking gains nothing.
//
// This is the ladder that killed anchoring on the advancing `current`: the
// carried-over term is absolute and the proportional term is length-relative,
// so a long candidate banks an absolute count that a later short candidate
// spends at a much higher share. Measured against the ORIGINAL both steps are
// judged on the same anchor, and the second is refused.
func TestGrowingThenShrinkingDoesNotEscapeTheBound(t *testing.T) {
	original := text.Scripts("The author 著 never draws it.") // Han=1, 22 letters

	// A long candidate may carry proportionally more Han: 1/22 * 220 = 10.
	long := "The author 著著著著著著 never draws it, and the reader never thinks to ask him why that is, nor does he ever say, and the argument turns on a distinction he does not draw at all here."
	longSet := text.Scripts(long)
	if longSet.Count("Han") == 0 || longSet.Letters() < 150 {
		t.Fatalf("the long fixture is Han=%d of %d letters; it must be long and carry Han",
			longSet.Count("Han"), longSet.Letters())
	}
	if got := longSet.Overgrown(original); len(got) != 0 {
		t.Fatalf("the long fixture must be ADMISSIBLE or this test proves nothing: %v", got)
	}

	// Now the short one carrying the same absolute count. Against the long
	// candidate it would pass; against the original it must not.
	short := "The author 著著著著著著 never draws."
	shortSet := text.Scripts(short)
	if shortSet.Count("Han") != longSet.Count("Han") {
		t.Fatalf("the two fixtures must carry the SAME Han count: %d and %d",
			shortSet.Count("Han"), longSet.Count("Han"))
	}
	if got := shortSet.Overgrown(longSet); len(got) != 0 {
		t.Fatalf("against the long candidate the short one is admissible, which is the "+
			"ladder; got %v", got)
	}
	if got := shortSet.Overgrown(original); !reflect.DeepEqual(got, []string{"Han"}) {
		t.Errorf("against the original the short one gives %v, want [Han]", got)
	}
}

// Sorted, so the record is stable and two equal answers compare equal.
func TestOvergrownIsSorted(t *testing.T) {
	got := text.Scripts("автор 著者 abc αβγ").Overgrown(text.Scripts("x"))
	if !sort.StringsAreSorted(got) {
		t.Errorf("Overgrown() = %v, which is not sorted", got)
	}
	if len(got) < 3 {
		t.Errorf("Overgrown() = %v; this fixture should overgrow several scripts", got)
	}
}

// Neither set is disturbed by asking, and asking twice answers the same.
//
// The gate asks about several candidates against one original, so a method that
// mutated either side would answer differently the second time.
func TestOvergrownChangesNeitherSet(t *testing.T) {
	original := text.Scripts("The author 著 never draws it.")
	candidate := text.Scripts("作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。")

	originalHan, candidateHan := original.Count("Han"), candidate.Count("Han")
	originalLetters, candidateLetters := original.Letters(), candidate.Letters()

	first := candidate.Overgrown(original)
	second := candidate.Overgrown(original)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("asked twice, answered %v then %v", first, second)
	}
	if original.Count("Han") != originalHan || candidate.Count("Han") != candidateHan ||
		original.Letters() != originalLetters || candidate.Letters() != candidateLetters {
		t.Error("asking changed one of the sets")
	}
}

// The zero value on either side.
func TestOvergrownAgainstTheZeroScriptSet(t *testing.T) {
	var zero text.ScriptSet
	prose := text.Scripts("The argument turns on a distinction.")

	// From nothing, any letter is growth.
	if got := prose.Overgrown(zero); !reflect.DeepEqual(got, []string{"Latin"}) {
		t.Errorf("against the zero value, prose gives %v, want [Latin]", got)
	}
	// And nothing grows from anything.
	if got := zero.Overgrown(prose); len(got) != 0 {
		t.Errorf("the zero value overgrows %v", got)
	}
}
