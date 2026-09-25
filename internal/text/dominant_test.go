package text_test

// #107. #91's script guard is a FIRST-OCCURRENCE check, not a drift check.
//
// It refuses a candidate that brings in a script the paragraph lacks, and says
// nothing about proportion among scripts already present. Measured, with the
// rule #91 actually shipped:
//
//	The author 著 never draws it.      22 letters, Latin=21 Han=1   dominant Latin
//	作者從不畫它，著者亦然。              10 letters, Han=10           dominant Han
//
// Nothing is introduced between those two — Han was already there — so #91
// accepts the second as a rewrite of the first. One character of setup disarms
// the guard against the incident it was filed for.
//
// # Dominance, and why it needs no constant
//
// #91 refused a share threshold because the number would have no derivation.
// The same objection rules out "no script may gain more than X%". What survives
// it is an ORDER statistic: which script holds the most letters. That is
// scale-free, it is defined for every text, and nobody has to justify a number.
//
// `Dominant` returns the scripts holding the maximal count — a set, because a
// tie is a real state and collapsing it would mean inventing a tie-break rule,
// which is a constant wearing a different hat.
//
// # What it deliberately does not see
//
// Dominance ignores magnitude. Latin=51 Han=49 and Latin=100 Han=1 both report
// `[Latin]`, and a rewrite between those two states is unconstrained by this
// statistic. That is the cost of refusing to invent a threshold, and it is
// worth stating plainly rather than discovering later: this measures a change
// of which script the paragraph is mostly written in, not how emphatically.
//
// Everything else follows `Scripts`: letters only, after NFC, with Common and
// Inherited attributed to no script. So punctuation, digits and a long-vowel
// mark cannot move dominance, and the canonically equivalent spellings of one
// paragraph cannot disagree about it.

import (
	"reflect"
	"sort"
	"testing"
	"unicode"

	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// Dominant
// ---------------------------------------------------------------------------

// Every value below is MEASURED, not counted by hand. Hand-counted letters have
// been wrong twice in this project.
func TestDominantNamesTheScriptsHoldingTheMostLetters(t *testing.T) {
	for _, c := range []struct {
		name, input string
		want        []string
	}{
		{
			name:  "one script",
			input: "The argument turns on a distinction the author never draws.",
			want:  []string{"Latin"}, // 49 letters, all Latin
		},
		{
			// #107's own example. Han is PRESENT, and nowhere near dominant.
			name:  "a single foreign character does not take the plurality",
			input: "The author 著 never draws it.",
			want:  []string{"Latin"}, // 22 letters: Latin 21, Han 1
		},
		{
			name:  "the same paragraph gone over to Han",
			input: "作者從不畫它，著者亦然。",
			want:  []string{"Han"}, // 10 letters, all Han
		},
		{
			// The case #91 cannot see at all: Japanese into Chinese moves the
			// plurality from Hiragana to Han while introducing nothing, because
			// Japanese already uses Han.
			name:  "japanese is dominated by Hiragana, not Han",
			input: "犬が好きです。",
			want:  []string{"Hiragana"}, // 6 letters: Hiragana 4, Han 2
		},
		{
			name:  "the chinese rendering of it is dominated by Han",
			input: "我喜欢狗。",
			want:  []string{"Han"}, // 4 letters, all Han
		},
		{
			// A tie is REPORTED as a tie. Breaking it here would be a
			// tie-break rule, which is a constant by another name.
			name:  "an exact tie names both, sorted",
			input: "abc 漢字漢",
			want:  []string{"Han", "Latin"}, // 6 letters: 3 and 3
		},
		{
			name:  "a plurality need not be a majority",
			input: "αβγδ ab 漢",
			want:  []string{"Greek"}, // 7 letters: Greek 4, Latin 2, Han 1
		},
		{
			// No letters, no dominance. Not an error and not a script.
			name:  "a letterless paragraph has no dominant script",
			input: "1979 — (42) !!",
			want:  nil,
		},
		{
			// Punctuation and digits are Common and are counted in neither the
			// numerator nor the denominator, so they cannot move dominance.
			name:  "punctuation does not participate",
			input: "a!!! b??? c... (d) [e] {f} — g",
			want:  []string{"Latin"}, // 7 letters
		},
		{
			// MULTI-LINE, because a mutation truncating input at the first
			// newline has passed an entire package in this project before.
			name:  "a paragraph spanning a line break",
			input: "The author never draws it,\nand the reader never asks.",
			want:  []string{"Latin"}, // 42 letters
		},
		{
			// And one spanning a blank line, with letters on both sides of it
			// and the dominance decided by the sum rather than by either half.
			name:  "a paragraph spanning a blank line",
			input: "漢字漢字漢\n\n字漢字 ab",
			want:  []string{"Han"}, // 10 letters: Han 8, Latin 2
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := text.Scripts(c.input).Dominant()
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Dominant() = %v, want %v", got, c.want)
			}
		})
	}
}

// Canonically equivalent spellings agree about dominance.
//
// `Scripts` normalizes to NFC because decomposed Hangul jamo are LETTERS rather
// than marks, so the same paragraph written two ways would otherwise report
// different counts. Dominance inherits that, and it is worth an assertion of
// its own: a rewrite that changed only the normalization form must not read as
// a change of script.
func TestDominanceDoesNotDependOnNormalizationForm(t *testing.T) {
	composed := "A한"     // A + HANGUL SYLLABLE HAN
	decomposed := "A한" // A + the same syllable as jamo
	if composed == decomposed {
		t.Fatal("the two fixtures are the same string; this test proves nothing")
	}

	first := text.Scripts(composed).Dominant()
	second := text.Scripts(decomposed).Dominant()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("composed gives %v and decomposed gives %v", first, second)
	}
	// And the value itself, so that "both wrong in the same way" fails.
	if !reflect.DeepEqual(first, []string{"Hangul", "Latin"}) {
		t.Errorf("Dominant() = %v, want [Hangul Latin]; one letter each", first)
	}
}

// Properties that must hold for any input, checked against the counts rather
// than against a second implementation of the same arithmetic.
func TestDominanceAgreesWithTheCountsItIsDerivedFrom(t *testing.T) {
	for _, input := range []string{
		"The argument turns on a distinction the author never draws.",
		"The author 著 never draws it.",
		"abc 漢字漢",
		"αβγδ ab 漢",
		"犬が好きです。",
		"1979 — (42) !!",
		"漢字漢字漢\n\n字漢字 ab",
		"",
	} {
		set := text.Scripts(input)
		dominant := set.Dominant()

		if set.Letters() == 0 {
			if len(dominant) != 0 {
				t.Errorf("%q has no letters yet reports %v", input, dominant)
			}
			continue
		}
		if len(dominant) == 0 {
			t.Errorf("%q has %d letters and no dominant script", input, set.Letters())
			continue
		}
		// Sorted, so the record is stable and two equal sets compare equal.
		if !sort.StringsAreSorted(dominant) {
			t.Errorf("%q reports %v, which is not sorted", input, dominant)
		}
		// Every dominant script is present, and they all hold the same share.
		// Share is the only count this package exposes, so the check is
		// expressed in it rather than in a private field.
		top := set.Share(dominant[0])
		for _, name := range dominant {
			if set.Share(name) != top {
				t.Errorf("%q reports %v as dominant, but %s has share %v and %s has %v",
					input, dominant, dominant[0], top, name, set.Share(name))
			}
		}
		// And nothing present beats them.
		for _, name := range set.Names() {
			if set.Share(name) > top {
				t.Errorf("%q reports %v as dominant, but %s has a larger share %v",
					input, dominant, name, set.Share(name))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// NewlyDominant
// ---------------------------------------------------------------------------

// The rule the policy is built on: no script may BECOME dominant that was not
// already dominant.
//
// Stated as a set operation rather than "the plurality must not change" because
// ties are real. The dominant set may narrow — a paragraph that was exactly
// balanced can tip — but it may never grow or shift, and that is what closes
// the ladder a weaker rule leaves open:
//
//	current Latin=90 Han=10, dominant [Latin]
//	candidate A Latin=50 Han=50 — introduces nothing, and Latin is STILL
//	  dominant, so "the plurality must remain a plurality" accepts it
//	current advances to A, dominant [Han Latin]
//	candidate B Latin=2 Han=98 — introduces nothing, and [Han] is inside
//	  [Han Latin], so every rule accepts it
//
// Two accepted attempts inside one invocation, and the paragraph is in Han.
// Requiring the candidate's dominant set to be a SUBSET of the current's
// refuses rung one, because [Han Latin] is not inside [Latin].
func TestNewlyDominantNamesTheScriptsThatTookOver(t *testing.T) {
	for _, c := range []struct {
		name, current, candidate string
		want                     []string
	}{
		{
			name:      "an ordinary rewrite changes nothing",
			current:   "The argument turns on a distinction the author never draws.",
			candidate: "The argument rests on a distinction the author never makes.",
			want:      nil,
		},
		{
			// #107 itself: Han was already present, so #91 sees nothing.
			name:      "the script that was one character becomes the paragraph",
			current:   "The author 著 never draws it.",
			candidate: "作者從不畫它，著者亦然。",
			want:      []string{"Han"},
		},
		{
			name:      "japanese into chinese, introducing nothing",
			current:   "犬が好きです。",
			candidate: "我喜欢狗。",
			want:      []string{"Han"},
		},
		{
			// Rung one of the ladder. Latin stays dominant and Han JOINS it,
			// which is a broadening and must be named.
			name:      "a script joining the dominant set is newly dominant",
			current:   "The author 著 never draws it.",
			candidate: "abc 漢字漢",
			want:      []string{"Han"},
		},
		{
			// Narrowing from a genuine tie. Han was already dominant, so it
			// did not become dominant, and nothing is named.
			name:      "narrowing from a tie names nothing",
			current:   "abc 漢字漢",
			candidate: "作者從不畫它，著者亦然。",
			want:      nil,
		},
		{
			name:      "losing a script is not gaining one",
			current:   "abc 漢字漢",
			candidate: "The argument turns on a distinction the author never draws.",
			want:      nil,
		},
		{
			// A candidate with no letters has no dominant script, so there is
			// nothing to have taken over. Other guards own that rewrite.
			name:      "a letterless candidate names nothing",
			current:   "The argument turns on a distinction the author never draws.",
			candidate: "1979 — (42) !!",
			want:      nil,
		},
		{
			// The other direction: from nothing, anything is new.
			name:      "a letterless current means any dominance is new",
			current:   "1979 — (42) !!",
			candidate: "The argument turns on a distinction the author never draws.",
			want:      []string{"Latin"},
		},
		{
			name:      "two scripts can take over at once, named in sorted order",
			current:   "αβγδ ab 漢",
			candidate: "abc 漢字漢",
			want:      []string{"Han", "Latin"},
		},
		{
			// MULTI-LINE on both sides.
			name:      "across a line break and a blank line",
			current:   "The author never draws it,\nand the reader never asks.",
			candidate: "漢字漢字漢\n\n字漢字 ab",
			want:      []string{"Han"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := text.Scripts(c.candidate).NewlyDominant(text.Scripts(c.current))
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("NewlyDominant() = %v, want %v", got, c.want)
			}
		})
	}
}

// NewlyDominant is exactly the set difference of the two dominant sets.
//
// Asserted as a relation rather than by re-deriving the arithmetic, so a
// NewlyDominant that quietly used shares, or intersected with the scripts
// PRESENT rather than dominant, disagrees here.
func TestNewlyDominantIsTheDifferenceOfTheDominantSets(t *testing.T) {
	texts := []string{
		"The argument turns on a distinction the author never draws.",
		"The author 著 never draws it.",
		"作者從不畫它，著者亦然。",
		"abc 漢字漢",
		"αβγδ ab 漢",
		"犬が好きです。",
		"1979 — (42) !!",
		"漢字漢字漢\n\n字漢字 ab",
	}
	for _, current := range texts {
		for _, candidate := range texts {
			currentSet, candidateSet := text.Scripts(current), text.Scripts(candidate)
			was := map[string]bool{}
			for _, name := range currentSet.Dominant() {
				was[name] = true
			}
			var want []string
			for _, name := range candidateSet.Dominant() {
				if !was[name] {
					want = append(want, name)
				}
			}
			got := candidateSet.NewlyDominant(currentSet)
			if len(got) == 0 && len(want) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q -> %q: NewlyDominant() = %v, want %v",
					current, candidate, got, want)
			}
		}
	}
}

// Empty means admissible, and the rule is exactly that.
//
// The policy refuses when this is non-empty, so the two must agree: the set is
// empty precisely when the candidate's dominant set is contained in the
// current's.
func TestNewlyDominantIsEmptyExactlyWhenTheDominantSetDidNotGrow(t *testing.T) {
	texts := []string{
		"The argument turns on a distinction the author never draws.",
		"The author 著 never draws it.",
		"abc 漢字漢",
		"作者從不畫它，著者亦然。",
		"1979 — (42) !!",
	}
	for _, current := range texts {
		for _, candidate := range texts {
			currentSet, candidateSet := text.Scripts(current), text.Scripts(candidate)
			was := map[string]bool{}
			for _, name := range currentSet.Dominant() {
				was[name] = true
			}
			contained := true
			for _, name := range candidateSet.Dominant() {
				if !was[name] {
					contained = false
				}
			}
			if empty := len(candidateSet.NewlyDominant(currentSet)) == 0; empty != contained {
				t.Errorf("%q -> %q: NewlyDominant empty = %v, dominant set contained = %v",
					current, candidate, empty, contained)
			}
		}
	}
}

// Neither set is disturbed by asking.
//
// `Introduced` carries the same requirement and for the same reason: the gate
// asks about several candidates against one current, so a method that mutated
// its receiver or its argument would give a different answer the second time.
func TestNewlyDominantChangesNeitherSet(t *testing.T) {
	current := text.Scripts("The author 著 never draws it.")
	candidate := text.Scripts("作者從不畫它，著者亦然。")

	currentBefore := append([]string(nil), current.Dominant()...)
	candidateBefore := append([]string(nil), candidate.Dominant()...)
	currentLetters, candidateLetters := current.Letters(), candidate.Letters()

	first := candidate.NewlyDominant(current)
	second := candidate.NewlyDominant(current)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("asked twice, answered %v then %v", first, second)
	}
	if !reflect.DeepEqual(current.Dominant(), currentBefore) ||
		!reflect.DeepEqual(candidate.Dominant(), candidateBefore) {
		t.Errorf("dominance changed: current %v -> %v, candidate %v -> %v",
			currentBefore, current.Dominant(), candidateBefore, candidate.Dominant())
	}
	if current.Letters() != currentLetters || candidate.Letters() != candidateLetters {
		t.Errorf("letter counts changed: current %d -> %d, candidate %d -> %d",
			currentLetters, current.Letters(), candidateLetters, candidate.Letters())
	}
}

// The zero value answers rather than panicking.
//
// `ScriptSet`'s documented zero value is empty, and the gate can reach one
// through an empty string, so both methods have to cope with a nil count map.
func TestTheZeroScriptSetHasNoDominanceAndIntroducesNothing(t *testing.T) {
	var zero text.ScriptSet

	if got := zero.Dominant(); len(got) != 0 {
		t.Errorf("the zero value reports %v as dominant", got)
	}
	if got := zero.NewlyDominant(text.Scripts("The author never draws it.")); len(got) != 0 {
		t.Errorf("the zero value newly dominates %v", got)
	}
	if got := text.Scripts("The author never draws it.").NewlyDominant(zero); !reflect.DeepEqual(got, []string{"Latin"}) {
		t.Errorf("against the zero value, Latin gives %v, want [Latin]", got)
	}
}

// Dominance is drawn from the same table as the rest of the package.
//
// Generative rather than a fixture list: every script in this Go's tables must
// be reachable as a dominant one. A `Dominant` that consulted a hardcoded list
// of the scripts someone thought to name would pass every case above.
func TestEveryScriptInTheTablesCanBeDominant(t *testing.T) {
	checked := 0
	for name, table := range unicode.Scripts {
		if name == "Common" || name == "Inherited" {
			continue
		}
		var letter rune
		for _, r := range table.R16 {
			for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
				if unicode.IsLetter(c) {
					letter = c
					break
				}
			}
			if letter != 0 {
				break
			}
		}
		if letter == 0 {
			continue // a script with no 16-bit letters; the 32-bit planes are not walked here
		}
		checked++
		got := text.Scripts(string(letter)).Dominant()
		if !reflect.DeepEqual(got, []string{name}) {
			t.Errorf("a lone %s letter %q reports %v as dominant", name, letter, got)
		}
	}
	if checked < 100 {
		t.Errorf("only %d scripts were reachable; this test is not covering the tables", checked)
	}
}
