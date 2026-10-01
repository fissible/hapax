package text_test

// #107. #91's guard is a FIRST-OCCURRENCE check, so one pre-existing character
// disarms it: a paragraph carrying a single Han letter can come back largely
// Han while introducing nothing.
//
// # Contract
//
// Stated once, on `ScriptSet.Exceeding`. Everything below reproduces it; none of
// it restates it. The declared thresholds and the argument for them are on
// `rewrite.ScriptCeiling`.
//
// # Evidence: three constant-free designs, each REFUTED on a case below
//
// Each is a refuted candidate and not a proof that no corpus-derived bound
// exists. #136 carries what nobody tried.
//
// **The plurality script set** — "the set of scripts holding the most letters
// must not grow." Measured against the incident-DERIVED fixture the table below
// uses, 52 Latin + 1 Han to 68 + 18: Latin still holds the plurality, so the
// candidate is accepted while Han goes from 0.0189 to 0.2093. DERIVED, not the
// incident: #91's reported pair is 52 Latin to 92 Latin + 21 Han, or 0.1858,
// asserted with exact counts in scripts_test.go. The fixtures here add one Han
// letter to the original, which is what disarms #91's introduction guard and is
// the whole of #107. That refutes THIS rule on THIS witness. It says
// nothing about order statistics in general — an ordered count vector does see
// magnitude — and it does not generalize across the fixtures either: the other
// incident-shaped case below, 21 Latin + 1 Han to 24 Han, moves the plurality
// from Latin to Han instead of preserving rank order, and no rule over that
// statistic was measured.
//
// **A corpus reference** — "no script may exceed what the author's corpus
// predicts for a text of this length." A monoscript candidate has count equal to
// its length, and the corpus ratio is below one whenever the corpus holds a
// single letter of anything else, so the bound falls below the length. Measured:
// 52 to 53 Latin letters refused at bound 52.999910.
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
// A share comparison with a TOLERANCE was not refuted, only measured to keep a
// boundary: refusing any increase above two percentage points against 96 Latin
// and 4 Greek, a candidate of 94 and 6 sits at exactly 0.020000 and passes while
// 93 and 6 sits at 0.020606 and fails.
//
// # Decision
//
// A FIXED ceiling, in place of the two PROPORTIONAL bounds above — the corpus
// reference and the paragraph's own share — which scale with the text and were
// refuted on lengthening. It also sees magnitude, which the plurality rule did
// not. It is still a threshold on a ratio, and `ScriptSet.Exceeding` records what
// that costs.
//
// # Unresolved
//
// The declared values, the rejection rate on legitimate rewrites, and whether
// proportional growth should be exempt: #136. What the three refutations do NOT
// rule out: per-paragraph maxima, conditional distributions, and the tolerance
// measured above.

import (
	"reflect"
	"sort"
	"strings"
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
			// The case that killed the previous design, which refused a
			// lengthening rewrite for its length alone. Latin is the only script
			// in either text, so the original's own share exempts it. Nothing
			// here says a longer rewrite is admissible in general: 96 Latin + 4
			// Greek to 96 + 6 puts Greek at 0.0588 and names it.
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
			// INCIDENT-DERIVED, not the incident: one Han letter is added to the
			// original so #91's guard is disarmed, and Han 1 of 53 at 0.0189
			// becomes 18 of 86 at 0.2093. The reported pair itself, 52 Latin to
			// 92 Latin + 21 Han, is asserted in scripts_test.go.
			name:      "an incident-derived rewrite",
			original:  "So I asked an AI to attack it. Not to review it politely. To break it 著.",
			candidate: "And hence I posed a challenge to the AI may it not just offer a courteous assessment 質疑它能否不僅以禮貌的態度來審視我們",
			want:      []string{"Han"},
		},
		{
			// #107's own example: 1 Han letter of 22, or 4.55%, becomes the
			// whole paragraph.
			name:      "one Han letter becomes the paragraph",
			original:  "The author 著 never draws it.",
			candidate: "作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。",
			want:      []string{"Han"},
		},
		{
			// ESTABLISHED. Han is 3 letters of 6 in the original, at 0.5000, so
			// the predicate's first line exempts it from THIS guard however far
			// it grows. #91's introduction guard is separate, has its own anchor,
			// and this row asserts nothing about it.
			name:      "a script established in the original is exempt",
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
			// for a script it shrank, which is the failure that ruled out shares
			// in the first place. Here the count arm is silent because the count
			// fell and the share arm is silent because 5.56% is under
			// establishment; both arms are needed, and the takeover row below is
			// named on a count that did not grow.
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
			original:  "The author 著著著著著著著著著著著著著著著著著著著著著 never draws it at all and the reader never once asks him why that is so and he does not ever say so",
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
			// growing a script in the band needs the rest of the paragraph to
			// grow with it. Measured, the escape: 60 Latin + 20 Greek + 20 Han to
			// 960 + 21 + 21 grows both counts and names neither, both shares
			// having fallen to 0.0210.
			name: "a rewrite of a paragraph with two scripts in the band",
			// Greek 15 of 66 is 22.7% and Han 11 of 66 is 16.7% — both over the
			// ceiling, both under establishment — and the candidate grows each
			// by one letter.
			original:  "abcdefghijklmnopqrstuvwxyzabcdefghijklmn αβγδεζηικλμνξοπ 著者作家文筆漢字語文書",
			candidate: "abcdefghijklmnopqrstuvwxyzabcdefghijklmn αβγδεζηικλμνξοπρ 著者作家文筆漢字語文書物",
			want:      []string{"Greek", "Han"},
		},
		{
			// THE SHARE ARM'S OWN BOUNDARY, and the operator that decides a
			// rung of #111. Han is 15 of 90 in the original — 16.67%, under
			// establishment — and 15 of 60 in the candidate, exactly 25.0000%
			// on an unchanged count. Written exclusive, this is admitted and
			// then established as the next anchor, from which any share is
			// free. Inclusive, it is refused, which matches `established`.
			name:      "landing exactly on the establishment threshold",
			original:  "著著著著著著著著著著著著著著著 abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvw",
			candidate: "著著著著著著著著著著著著著著著 abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrs",
			want:      []string{"Han"},
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
//
// SCOPE, because the name used to claim more than this reaches: one anchor is
// passed to both functions and establishment is held above one, so what is
// established is containment at a ceiling of zero WITH EQUAL ANCHORS and
// nothing established. Production passes different anchors — introduction reads
// `current`, growth reads `original` — and there containment fails in both
// directions; `TestNeitherGuardContainsTheOtherWithProductionAnchors` below
// holds that case.
func TestAtCeilingZeroAndOneAnchorGrowthRefusesEveryIntroduction(t *testing.T) {
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

// Neither guard contains the other once production's anchors are used.
//
// The containment above holds only WITH EQUAL ANCHORS at a ceiling of zero.
// Production passes `original` to growth and the advancing `current` to
// introduction, and then each guard refuses something the other permits. Three
// measured cases, in both directions: two where introduction refuses and growth
// does not, and #107's own incident, where growth refuses and introduction does
// not.
//
// Every text is built from repeated letters so the counts are exactly the ones
// the comments name.
func TestNeitherGuardContainsTheOtherWithProductionAnchors(t *testing.T) {
	const established, ceiling = 0.25, 0.05
	build := func(latin, greek, han int) text.ScriptSet {
		return text.Scripts(strings.Repeat("a", latin) + " " +
			strings.Repeat("α", greek) + " " + strings.Repeat("著", han))
	}
	for _, c := range []struct {
		name                          string
		original, current, candidate  text.ScriptSet
		ceiling                       float64
		wantExceeding, wantIntroduced []string
	}{
		{
			// The ceiling. One Han letter in a 70-letter paragraph is 0.0143,
			// under the ceiling, and introduced.
			name:           "introduction refuses under the ceiling",
			original:       build(69, 0, 0),
			current:        build(69, 0, 0),
			candidate:      build(69, 0, 1),
			ceiling:        ceiling,
			wantIntroduced: []string{"Han"},
		},
		{
			// The anchor, which no ceiling fixes: at a ceiling of ZERO growth
			// still exempts Greek, because Greek is established in the original
			// at 0.3000 — and it is absent from current.
			name:           "introduction refuses what the original established",
			original:       build(70, 30, 0),
			current:        build(100, 0, 0),
			candidate:      build(70, 30, 0),
			ceiling:        0,
			wantIntroduced: []string{"Greek"},
		},
		{
			// #107 itself, at the incident-DERIVED counts: Han is 1 of 53 in the
			// original and in current, so nothing is introduced, and the
			// candidate takes it to 0.2093.
			name:          "growth refuses what introduction permits",
			original:      build(52, 0, 1),
			current:       build(52, 0, 1),
			candidate:     build(68, 0, 18),
			ceiling:       ceiling,
			wantExceeding: []string{"Han"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.candidate.Exceeding(c.original, established, c.ceiling)
			if len(got) != 0 || len(c.wantExceeding) != 0 {
				if !reflect.DeepEqual(got, c.wantExceeding) {
					t.Errorf("Exceeding(original) = %v, want %v", got, c.wantExceeding)
				}
			}
			if got := c.candidate.Introduced(c.current); len(got) != 0 || len(c.wantIntroduced) != 0 {
				if !reflect.DeepEqual(got, c.wantIntroduced) {
					t.Errorf("Introduced(current) = %v, want %v", got, c.wantIntroduced)
				}
			}
		})
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
					candidateSet.Share(name) >= established
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
