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
// A script at or above `established` in the ORIGINAL is one of the languages
// that paragraph is written in, and is not constrained — a bilingual paragraph
// may be rewritten in either of its languages. Every other script, present or
// absent, may not exceed `ceiling` of the candidate WHILE USING MORE LETTERS OF
// IT than the original did.
//
// The two thresholds are separate parameters because they answer two questions
// and only one has evidence. The corpus says how much of a script a candidate
// may contain; it says nothing about how much a paragraph must already hold for
// that script to be its own. One number for both put the line one quotation
// wide — at 5%, sixteen CJK letters establish Han in half the corpus's
// paragraphs, after which the guard is off at any share.
//
// The count condition is what keeps a SHORTENING rewrite admissible. A ceiling
// alone reintroduces the failure that ruled out shares in the first place:
// measured, a 200-letter paragraph with 6 Greek letters cut to 70 letters with
// FOUR is 5.71%, over the ceiling, and refused for a script it shrank.
//
// On its own, though, that condition is a carryover term, and a carryover term
// is what let a candidate spend a banked count at any share. Measured: a
// paragraph at Han 21 of 107, which is 19.6% and the shape #91's incident LEFT
// IN THE FILE, becomes Han 21 of 21 by deleting every Latin letter, and the
// count never grew. So the condition is disjunctive — a script may keep its
// letters, but may not take over the paragraph:
//
//	refuse  iff  not established
//	        and  share(candidate) > ceiling
//	        and  ( count grew  or  share(candidate) > established )
//
// The relation to #91 is CONTAINMENT, not equality. An introduced script has an
// original count of zero, so it is never established and its count always grew:
// everything #91 refuses, this refuses too at a ceiling of zero. The converse is
// false, and deliberately so — growth without introduction is the whole subject
// of #107, so at any ceiling this names scripts #91 does not.
//
// An earlier draft claimed the two coincide exactly at a ceiling of zero. That
// was true of the rule before the count condition and false after it, and the
// claim outlived the change. Third time in this issue that a rule change left a
// claim behind, so: the header is re-derived with the fixtures, not after them.

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
	const established, ceiling = 0.25, 0.05
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
			// SHORTENING, with the script's own letters REMOVED. Greek is 2 of
			// 49 in the original and 1 of 18 in the candidate: 5.56%, over the
			// ceiling, on one fewer Greek letter. A ceiling alone refuses this
			// for a script it shrank, which is the failure that ruled out
			// shares in the first place, so the rule also requires the count to
			// have grown.
			name:      "shortening past the ceiling while removing the script",
			original:  "The author never draws it at all and the reader never asks αβ",
			candidate: "The author draws α now",
			want:      nil,
		},
		{
			// DELETION, at an equal count. Han is 21 of 107 in the original —
			// 19.6%, the shape #91's incident left in the file — and 21 of 21 in
			// the candidate, which deleted every Latin letter. The count never
			// grew, so the count arm alone admits it; the share arm refuses it.
			name:      "taking over the paragraph by deleting the rest",
			original:  "The author 著著著著著著著著著著著著著著著著著著著著著 never draws it at all and the reader never once asks him why that is so",
			candidate: "著著著著著著著著著著著著著著著著著著著著著",
			want:      []string{"Han"},
		},
		{
			// The count arm's own boundary: the CANDIDATE is over the ceiling
			// at 2 of 23, or 8.7%, the counts are equal at 2, and 8.7% is under
			// establishment so the share arm is silent too. Admissible — and
			// without this row `>` and `>=` on the count are indistinguishable.
			name:      "equal counts over the ceiling but under establishment",
			original:  "The author 著著 never draws it at all and the reader never once asks him why",
			candidate: "The author 著著 never draws it",
			want:      nil,
		},
		{
			// TWO sub-established scripts. Neither Greek nor Han is established
			// at 25%, both are far over the 5% ceiling, and a real rewrite grows
			// both — so both are refused. This is the cost of an absolute
			// establishment share, recorded here rather than discovered later:
			// a paragraph mixing scripts in the band between the two thresholds
			// cannot grow either of them.
			name: "a rewrite of a paragraph with two scripts in the band",
			// Greek 15 of 66 is 22.7% and Han 11 of 66 is 16.7% — both over the
			// ceiling, both under establishment — and the candidate grows each
			// by one letter.
			original:  "abcdefghijklmnopqrstuvwxyzabcdefghijklmn αβγδεζηικλμνξοπ 著者作家文筆漢字語文書",
			candidate: "abcdefghijklmnopqrstuvwxyzabcdefghijklmn αβγδεζηικλμνξοπρ 著者作家文筆漢字語文書物",
			want:      []string{"Greek", "Han"},
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
			got := text.Scripts(c.candidate).Exceeding(text.Scripts(c.original), established, ceiling)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Exceeding() = %v, want %v", got, c.want)
			}
		})
	}
}

// A script exactly ON the establishment threshold is established.
//
// The partner of the ceiling boundary below, and the one no other fixture
// covers: nothing in the whole-rule cross-check puts a script exactly on the
// threshold on the ORIGINAL side, so `>=` survived as `>` there. The two
// comparisons are independent decisions and each needs its own witness.
func TestAScriptExactlyOnTheEstablishmentThresholdIsEstablished(t *testing.T) {
	// Han is 5 of 20 letters, which is exactly a quarter.
	original := text.Scripts("abcdefghijklmno漢字漢字漢")
	if original.Count("Han") != 5 || original.Letters() != 20 {
		t.Fatalf("the fixture is Han=%d of %d letters; it must be exactly 5 of 20",
			original.Count("Han"), original.Letters())
	}
	candidate := text.Scripts("作者從不畫它，著者亦然。") // 10 letters, all Han

	// At exactly the threshold it is established, so the ceiling does not apply
	// however much Han the candidate uses.
	if got := candidate.Exceeding(original, 0.25, 0.05); len(got) != 0 {
		t.Errorf("at an establishment threshold of exactly its own share, "+
			"Exceeding() = %v, want none", got)
	}
	// A hair above, and it is not established.
	if got := candidate.Exceeding(original, 0.26, 0.05); !reflect.DeepEqual(got, []string{"Han"}) {
		t.Errorf("just above the threshold, Exceeding() = %v, want [Han]", got)
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

	const established = 0.25
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
		got := candidate.Exceeding(original, established, c.ceiling)
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
	const established = 0.25
	original := text.Scripts("abcdefghijklmnopqrstu")
	// Han 5 of 20 letters is exactly one quarter.
	candidate := text.Scripts("abcdefghijklmno漢字漢字漢")
	if got := candidate.Count("Han"); got != 5 || candidate.Letters() != 20 {
		t.Fatalf("the fixture is Han=%d of %d letters; it must be exactly 5 of 20",
			got, candidate.Letters())
	}

	if got := candidate.Exceeding(original, established, 0.25); len(got) != 0 {
		t.Errorf("at a ceiling of exactly its own share, Exceeding() = %v, want none", got)
	}
	if got := candidate.Exceeding(original, established, 0.24); !reflect.DeepEqual(got, []string{"Han"}) {
		t.Errorf("just below, Exceeding() = %v, want [Han]", got)
	}
}

// Everything #91 refuses, this refuses too — one-sided.
//
// An introduced script has an original count of zero, so `grew` and
// `!established` are automatic and only the ceiling can excuse it; at a ceiling
// of zero nothing is excused. The converse does NOT hold and must not be
// asserted: growth without introduction is what #107 exists for.
//
// At a ceiling of one nothing crosses, because no share exceeds one.
func TestEverythingTheShippedGuardRefusesThisRefusesToo(t *testing.T) {
	// Establishment is held ABOVE one so nothing is ever established here; the
	// boundary being probed is the ceiling's.
	const established = 1.1
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

			atZero := map[string]bool{}
			for _, name := range candidateSet.Exceeding(originalSet, established, 0) {
				atZero[name] = true
			}
			for _, name := range candidateSet.Introduced(originalSet) {
				if !atZero[name] {
					t.Errorf("%q -> %q: %s is introduced but not named at ceiling 0: %v",
						original, candidate, name,
						candidateSet.Exceeding(originalSet, established, 0))
				}
			}
			if got := candidateSet.Exceeding(originalSet, established, 1); len(got) != 0 {
				t.Errorf("%q -> %q: at ceiling 1, Exceeding() = %v, want none",
					original, candidate, got)
			}
		}
	}
}

// Exceeding is exactly the rule, checked against the shares rather than against
// a second implementation of it.
func TestExceedingIsExactlyTheDeclaredRule(t *testing.T) {
	const established, ceiling = 0.25, 0.05
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
				// Three conditions, written separately because they are three
				// decisions: establishment against its own threshold, the
				// ceiling against the candidate, and growth in absolute letters.
				isEstablished := originalSet.Count(name) > 0 &&
					originalSet.Share(name) >= established
				overCeiling := candidateSet.Share(name) > ceiling
				// Disjunctive: growing the count, OR taking over the
				// paragraph without growing it. The second arm is what stops a
				// candidate spending a banked count by deleting everything else.
				grew := candidateSet.Count(name) > originalSet.Count(name) ||
					candidateSet.Share(name) > established
				if !isEstablished && overCeiling && grew {
					want = append(want, name)
				}
			}
			sort.Strings(want)

			got := candidateSet.Exceeding(originalSet, established, ceiling)
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
	got := text.Scripts("автор 著者 αβγ").Exceeding(text.Scripts("x"), 0.25, 0.05)
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
	const established = 0.25
	original := text.Scripts("The author 著 never draws it.")
	candidate := text.Scripts("作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。")
	originalHan, candidateHan := original.Count("Han"), candidate.Count("Han")
	originalLetters, candidateLetters := original.Letters(), candidate.Letters()

	first := candidate.Exceeding(original, established, 0.05)
	second := candidate.Exceeding(original, established, 0.05)

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
	const established = 0.25
	var zero text.ScriptSet
	prose := text.Scripts("The argument turns on a distinction.")

	if got := prose.Exceeding(zero, established, 0.05); !reflect.DeepEqual(got, []string{"Latin"}) {
		t.Errorf("against the zero value, prose gives %v, want [Latin]", got)
	}
	if got := zero.Exceeding(prose, established, 0.05); len(got) != 0 {
		t.Errorf("the zero value crosses %v", got)
	}
	for _, ceiling := range []float64{-1, 2} {
		if got := prose.Exceeding(zero, established, ceiling); ceiling < 0 && len(got) == 0 {
			t.Errorf("at ceiling %v, Exceeding() = %v; a negative ceiling admits nothing",
				ceiling, got)
		} else if ceiling > 1 && len(got) != 0 {
			t.Errorf("at ceiling %v, Exceeding() = %v, want none", ceiling, got)
		}
	}
}
