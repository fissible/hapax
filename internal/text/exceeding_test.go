package text_test

// #107. #91's guard is a FIRST-OCCURRENCE check, so one pre-existing character
// disarms it: a paragraph carrying a single Han letter can come back largely
// Han while introducing nothing.
//
// # Three constant-free designs died first, and the last two died the same way
//
// **An order statistic** — "the set of scripts holding the most letters must not
// grow." Measured against #91's own incident: the candidate is Latin=68 Han=18,
// so Latin still holds the plurality and it is accepted. Both failure cases
// preserve rank order, and no order statistic sees magnitude.
//
// **A corpus reference** — "no script may exceed what the author's corpus
// predicts for a text of this length." A monoscript candidate has count equal to
// its length, and the corpus ratio is below one whenever the corpus holds a
// single letter of anything else, so the bound falls below the length and every
// LENGTHENING rewrite is refused. Measured: 52 to 53 Latin letters refused at
// bound 52.999910.
//
// **The paragraph's own share** — the same bound with `share(original)` in place
// of the corpus ratio. It dies identically, and the algebra says why: holding
// the other scripts constant while the dominant one grows from c to c', the
// bound is (c/L)(c' + L - c), and overgrowth occurs iff c' > c for ANY
// multi-script original. Measured:
//
//	orig(22)[Han=1 Lat=21]  cand(30)[Han=1 Lat=29]  -> refused [Latin]
//	orig(33)[Han=1 Lat=32]  cand(49)[Lat=49]        -> refused [Latin]
//
// The second removes the foreign character and is still refused. That design's
// fixtures could not see it because every admissible-lengthening case used a
// MONOSCRIPT original, where the share is exactly 1.0 and the bound equals the
// length — one incidental value shared across the whole fixture set.
//
// # Why a constant is now declared rather than avoided
//
// Order statistics cannot see magnitude. Any rule comparing a script's share
// before and after has knife-edge jitter, because holding one script constant
// while another grows moves both shares. Any rule comparing counts against a
// proportional bound reduces to the share rule. So this takes a CEILING, which
// is not a comparison and therefore does not jitter, and the number is declared
// undeliverable the way #92 declared the paragraph floor.
//
// The ceiling is a PARAMETER here rather than a constant, because the
// measurement should not own the policy. The declared value lives beside the
// rejection code it produces.
//
// # Evidence for the value chosen by the caller
//
// Measured over the maintainer's corpus through the real admission path, with
// hapax's own output files excluded (#109): of 1959 admitted paragraphs, TWO
// contain any non-Latin script at all, at 0.1608% and 0.3831%. #91's incident
// is 18 Han letters of 86, or 20.9%. Any ceiling between about 1% and 15%
// separates every observed legitimate use from the incident by more than an
// order of magnitude in both directions.
//
// # The rule
//
// A script already at or above the ceiling in the ORIGINAL is established, and
// is not constrained — a bilingual paragraph may be rewritten in either of its
// languages. Every other script, present or absent, may not exceed the ceiling
// in the candidate.
//
// At a ceiling of zero this degenerates to #91's rule exactly, which is asserted
// below rather than assumed. At a ceiling of one it admits everything. Those two
// boundaries are what make it a generalisation of the shipped guard rather than
// a second unrelated one.

import (
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// Count
// ---------------------------------------------------------------------------

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
			// THE TRUNCATING PAIR. `int(Share(name) * Letters())` is a
			// plausible implementation that passes every other assertion here,
			// and it is wrong: 15/22 round-trips through float64 to 14. Without
			// this fixture the whole package accepts it.
			name:  "a count the float round-trip truncates",
			input: "abcdefghijklmno漢字漢字漢字漢",
			want:  map[string]int{"Latin": 15, "Han": 7},
		},
		{
			// And a second, so one arithmetic accident is not the only witness.
			name:  "another truncating pair",
			input: "abcdefghijklm漢字漢字漢字漢字漢字",
			want:  map[string]int{"Latin": 13, "Han": 10},
		},
		{
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

// The counts and the two existing accessors agree, and the counts sum to the
// total. Asserted as a relation so a Count reading a different map disagrees.
func TestCountAgreesWithShareAndLetters(t *testing.T) {
	for _, input := range []string{
		"The argument turns on a distinction the author never draws.",
		"The author, автор 著者, never draws it.",
		"abcdefghijklmno漢字漢字漢字漢",
		"abcdefghijklm漢字漢字漢字漢字漢字",
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
			if want := set.Share(name) * float64(set.Letters()); absDiff(float64(count), want) > 1e-9 {
				t.Errorf("%q: Count(%s) = %d, Share*Letters = %v", input, name, count, want)
			}
		}
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

func TestTheZeroScriptSetCountsZero(t *testing.T) {
	var zero text.ScriptSet
	if got := zero.Count("Latin"); got != 0 {
		t.Errorf("the zero value counts %d Latin letters", got)
	}
}

// ---------------------------------------------------------------------------
// Exceeding
// ---------------------------------------------------------------------------

// The rule, as measured cases at the ceiling the policy declares.
func TestExceedingNamesTheScriptsThatCrossTheCeiling(t *testing.T) {
	const ceiling = 0.05
	for _, c := range []struct {
		name, original, candidate string
		want                      []string
	}{
		{
			name:      "an ordinary rewrite crosses nothing",
			original:  "The argument turns on a distinction the author never draws.",
			candidate: "The argument rests on a distinction the author never makes.",
			want:      nil,
		},
		{
			// The case that killed the previous design. A longer rewrite is
			// admissible whether or not the original is multi-script, because a
			// ceiling does not move with length.
			name:      "lengthening a monoscript paragraph",
			original:  "The author never draws it.",
			candidate: "The author never draws it, and the reader never thinks to ask him why.",
			want:      nil,
		},
		{
			// MULTI-SCRIPT lengthening, which the previous design refused. This
			// fixture is the one whose absence hid that failure.
			name:      "lengthening a paragraph that carries a foreign character",
			original:  "The author 著 never draws it.",
			candidate: "The author 著 never draws it, and the reader never thinks to ask why that is.",
			want:      nil,
		},
		{
			// And the mirror: dropping the foreign character while lengthening.
			// The previous design refused this too.
			name:      "removing a script while lengthening",
			original:  "The author 著 never draws it at all, he said.",
			candidate: "The argument turns on a distinction the author never draws.",
			want:      nil,
		},
		{
			// #91's incident: Han 1 of 53 becomes 18 of 86, or 20.9%.
			name:      "the reported incident",
			original:  "So I asked an AI to attack it. Not to review it politely. To break it 著.",
			candidate: "And hence I posed a challenge to the AI may it not just offer a courteous assessment 質疑它能否不僅以禮貌的態度來審視我們",
			want:      []string{"Han"},
		},
		{
			// #107's own example: 1 of 22 becomes the whole paragraph.
			name:      "two percent becomes the paragraph",
			original:  "The author 著 never draws it.",
			candidate: "作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。",
			want:      []string{"Han"},
		},
		{
			// ESTABLISHED. Han is half the original, so it is above the ceiling
			// and unconstrained: a bilingual paragraph may be rewritten in
			// either of its languages.
			name:      "a script already above the ceiling is unconstrained",
			original:  "abc 漢字漢",
			candidate: "作者從不畫它，著者亦然。",
			want:      nil,
		},
		{
			// A single introduced character in a long paragraph is BELOW the
			// ceiling, so this rule does not catch it — #91's does. The two are
			// complementary rather than nested at a non-zero ceiling, which is
			// why both are kept.
			name:      "one introduced character in a long paragraph",
			original:  "The argument turns on a distinction the author never draws at all in this paragraph.",
			candidate: "The argument turns 著 on a distinction the author never draws at all in this paragraph.",
			want:      nil,
		},
		{
			name:      "a letterless candidate crosses nothing",
			original:  "The argument turns on a distinction the author never draws.",
			candidate: "1979 — (42) !!",
			want:      nil,
		},
		{
			// A letterless original establishes nothing, so any script that
			// takes more than the ceiling of the candidate crosses it.
			name:      "a letterless original establishes nothing",
			original:  "1979 — (42) !!",
			candidate: "The argument turns on a distinction.",
			want:      []string{"Latin"},
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
			got := text.Scripts(c.candidate).Exceeding(text.Scripts(c.original), ceiling)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Exceeding() = %v, want %v", got, c.want)
			}
		})
	}
}

// The ceiling is a parameter, and the answer moves with it.
//
// A measurement that baked in the policy's value would pass every case above.
// The same pair is judged differently at three ceilings, and the boundary
// values are exact: Han is 7 of 22 letters, so 31.8%.
func TestExceedingRespondsToTheCeilingItIsGiven(t *testing.T) {
	original := text.Scripts("abcdefghijklmnopqrstu")   // Latin 21, no Han
	candidate := text.Scripts("abcdefghijklmno漢字漢字漢字漢") // Latin 15, Han 7 of 22

	for _, c := range []struct {
		ceiling float64
		want    []string
	}{
		{0.01, []string{"Han"}},
		{0.20, []string{"Han"}},
		{0.30, []string{"Han"}},
		{0.32, nil}, // just above 7/22
		{0.50, nil},
	} {
		got := candidate.Exceeding(original, c.ceiling)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("at ceiling %v, Exceeding() = %v, want %v", c.ceiling, got, c.want)
		}
	}
}

// The ceiling is a strict bound: a script sitting exactly on it has not crossed
// it. Asserted because `>` and `>=` are the same on every inexact fixture.
func TestAScriptExactlyOnTheCeilingHasNotCrossedIt(t *testing.T) {
	original := text.Scripts("abcdefghijklmnopqrstu")
	// Han 5 of 20 letters is exactly one quarter.
	candidate := text.Scripts("abcdefghijklmno漢字漢字漢")
	if got := candidate.Count("Han"); got != 5 || candidate.Letters() != 20 {
		t.Fatalf("the fixture is Han=%d of %d letters; it must be exactly 5 of 20",
			got, candidate.Letters())
	}

	if got := candidate.Exceeding(original, 0.25); len(got) != 0 {
		t.Errorf("at a ceiling of exactly its own share, Exceeding() = %v, want none", got)
	}
	if got := candidate.Exceeding(original, 0.24); !reflect.DeepEqual(got, []string{"Han"}) {
		t.Errorf("just below, Exceeding() = %v, want [Han]", got)
	}
}

// At a ceiling of zero this is exactly #91's rule.
//
// The two guards are then one guard at two settings rather than two unrelated
// ones, and a drift between them shows up here. At a ceiling of one nothing can
// cross, because no share exceeds one.
func TestTheCeilingBoundariesAgreeWithTheShippedGuard(t *testing.T) {
	texts := []string{
		"The argument turns on a distinction the author never draws.",
		"The author 著 never draws it.",
		"作者從不畫它，著者亦然。",
		"abc 漢字漢",
		"αβγδ ab 漢",
		"The author, автор 著者, never draws it.",
		"1979 — (42) !!",
	}
	for _, original := range texts {
		for _, candidate := range texts {
			originalSet, candidateSet := text.Scripts(original), text.Scripts(candidate)

			atZero := candidateSet.Exceeding(originalSet, 0)
			introduced := candidateSet.Introduced(originalSet)
			if len(atZero) != 0 || len(introduced) != 0 {
				if !reflect.DeepEqual(atZero, introduced) {
					t.Errorf("%q -> %q: at ceiling 0 Exceeding() = %v but Introduced() = %v",
						original, candidate, atZero, introduced)
				}
			}
			if got := candidateSet.Exceeding(originalSet, 1); len(got) != 0 {
				t.Errorf("%q -> %q: at ceiling 1, Exceeding() = %v, want none",
					original, candidate, got)
			}
		}
	}
}

// Exceeding is exactly the rule, checked against the shares rather than against
// a second implementation of it.
func TestExceedingIsExactlyTheDeclaredRule(t *testing.T) {
	const ceiling = 0.05
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
				established := originalSet.Count(name) > 0 && originalSet.Share(name) >= ceiling
				if !established && candidateSet.Share(name) > ceiling {
					want = append(want, name)
				}
			}
			sort.Strings(want)

			got := candidateSet.Exceeding(originalSet, ceiling)
			if len(got) == 0 && len(want) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q -> %q: Exceeding() = %v, want %v", original, candidate, got, want)
			}
		}
	}
}

// Sorted, so the record is stable and two equal answers compare equal.
func TestExceedingIsSorted(t *testing.T) {
	got := text.Scripts("автор 著者 αβγ").Exceeding(text.Scripts("x"), 0.05)
	if !sort.StringsAreSorted(got) {
		t.Errorf("Exceeding() = %v, which is not sorted", got)
	}
	if len(got) < 3 {
		t.Errorf("Exceeding() = %v; this fixture should cross on several scripts", got)
	}
}

// Neither set is disturbed by asking, and asking twice answers the same. The
// gate asks about several candidates against one original.
func TestExceedingChangesNeitherSet(t *testing.T) {
	original := text.Scripts("The author 著 never draws it.")
	candidate := text.Scripts("作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。")
	originalHan, candidateHan := original.Count("Han"), candidate.Count("Han")
	originalLetters, candidateLetters := original.Letters(), candidate.Letters()

	first := candidate.Exceeding(original, 0.05)
	second := candidate.Exceeding(original, 0.05)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("asked twice, answered %v then %v", first, second)
	}
	if original.Count("Han") != originalHan || candidate.Count("Han") != candidateHan ||
		original.Letters() != originalLetters || candidate.Letters() != candidateLetters {
		t.Error("asking changed one of the sets")
	}
}

// The zero value on either side, and a ceiling outside the unit interval.
//
// The method is total: the gate can reach an empty set through an empty string,
// and a nonsensical ceiling must produce an answer rather than a panic.
func TestExceedingIsTotal(t *testing.T) {
	var zero text.ScriptSet
	prose := text.Scripts("The argument turns on a distinction.")

	if got := prose.Exceeding(zero, 0.05); !reflect.DeepEqual(got, []string{"Latin"}) {
		t.Errorf("against the zero value, prose gives %v, want [Latin]", got)
	}
	if got := zero.Exceeding(prose, 0.05); len(got) != 0 {
		t.Errorf("the zero value crosses %v", got)
	}
	for _, ceiling := range []float64{-1, 2} {
		if got := prose.Exceeding(zero, ceiling); ceiling < 0 && len(got) == 0 {
			t.Errorf("at ceiling %v, Exceeding() = %v; a negative ceiling admits nothing",
				ceiling, got)
		} else if ceiling > 1 && len(got) != 0 {
			t.Errorf("at ceiling %v, Exceeding() = %v, want none", ceiling, got)
		}
	}
}
