package text_test

// #91. An accepted rewrite switched language mid-paragraph and nothing noticed.
//
// The real run, against a local 7B model:
//
//	before: So I asked an AI to attack it. Not to review it politely. To break it.
//	after:  And hence, I posed a challenge to the AI: may it not just offer a
//	        courteous assessment of the work, but also strive to质疑它能否…
//
// Exit 0, `rewrite ok`, `claim=closer-by-distance`. `preserve` covers numbers,
// entities, negations, URLs and quotes; `tells` is 22 English regexes; neither
// looks at script. The distance fell from 1.6570 to 0.8289 because the
// candidate won on LENGTH and sentence structure against a profile fitted on
// long explanatory prose — the script switch was simply not penalised.
//
// DESIGN assigns an "English-only language gate" to component 0. This is the
// measurement that gate needs.
//
// # Scripts, not languages
//
// Measured before designing anything:
//
//	#91 before                    100.0% Latin
//	#91 after                      81.4% Latin, 18.6% Han   (113 letters, 21 Han)
//	english with a French quote   100.0% Latin
//	english with a German noun    100.0% Latin
//	turkish                       100.0% Latin
//
// Lines three to five are the point: `é`, `ü`, `ç` and `ı` are all
// `unicode.Latin`, so French, German and Turkish prose cannot be refused by a
// script rule however strict. That is what makes this measurable
// deterministically, with no model and no word lists, and why this package
// reports SCRIPTS and leaves "language" to its consumer.
//
// # Three decisions, each forced by a measurement
//
// **Counting happens after NFC.** Decomposed Hangul jamo are LETTERS, not
// marks, so skipping marks cannot fix them. Measured: `A한` is 2 letters and
// 50% Hangul, while the canonically equivalent `A\u1112\u1161\u11ab` is 4
// letters and 75% — the same text, two answers. Normalizing first makes them
// agree.
//
// **`Common` and `Inherited` are not evidence, and are not counted at all.**
// Measured: `ー` (U+30FC) and Arabic tatweel (U+0640) are letters whose script
// is `Common`, so `コンピューター` would otherwise report a `Common` script
// alongside Katakana — and a rewrite adding a long-vowel mark would "introduce"
// it. So would mathematical bold `A` (U+1D400). None of those is a new writing
// system. They are excluded from the numerator AND the denominator, because a
// character carrying no script evidence is not evidence.
//
// **Only letters count.** Digits, punctuation, spaces and symbols carry no
// script evidence — a paragraph of arithmetic is not a change of language — and
// including them would make the proportion move with how much punctuation a
// sentence happened to use. Marks are skipped too: a mark's script is already
// implied by the letter it modifies. Measured, this matters for more than
// combining acutes — an Arabic-Indic digit carries script `Arabic`, and a
// Hebrew point and a Devanagari vowel sign are marks carrying `Hebrew` and
// `Devanagari` rather than `Inherited`, so a rule that counted script-bearing
// marks or digits would find scripts in text that has none of their letters.
//
// # The rule is RELATIVE
//
// `Introduced(current)` names the scripts the candidate uses that the current
// text does not — not an allow-list. English in and Han out is refused; Han in
// and Han out is allowed, so a writer whose paragraph is Han can have it
// rewritten; Turkish out of English is not an introduction at all. One rule, no
// per-language configuration, nothing for a bilingual user to switch off, and
// no contradiction with #94 when per-language profiles arrive.
//
// There is deliberately NO threshold here. One Han letter among a thousand
// Latin ones is still an introduction, and whether that should be refused is
// `rewrite`'s decision. Recording that here because the reviewer proved a
// sub-1% threshold could hide inside this function unnoticed.
//
// # A limit worth stating
//
// Set difference cannot see a language change within one script. Measured,
// Japanese `犬が好きです` is Han and Hiragana while `イヌが好きです` adds
// Katakana — so an ordinary Japanese rewrite DOES report an introduction, and a
// consumer that refused every introduction would be wrong about Japanese. In
// the other direction, a Japanese paragraph rewritten into Chinese using only
// already-present Han reports nothing. Both are limits of the measurement, not
// defects in it, and both are why the threshold belongs to the policy.

import (
	"math"
	"sort"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/fissible/hapax/internal/text"
)

const (
	englishBefore = "So I asked an AI to attack it. Not to review it politely. To break it."
	mixedAfter    = "And hence, I posed a challenge to the AI: may it not just offer a courteous " +
		"assessment of the work, but also strive to质疑它能否不仅以礼貌的态度来审视我们的工作。"
	pureHan = "质疑它能否不仅以礼貌的态度来审视我们的工作"
)

// Exact counts and exact shares.
//
// An earlier version asserted only `Share > 0`, which a function returning 1
// for every present script passes — and so does one returning NaN, since
// `NaN <= 0` is false. Every share here is compared to a computed fraction and
// checked finite.
func TestScriptsCountsLettersAndSharesExactly(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      string
		letters int
		counts  map[string]int
	}{
		{"english", englishBefore, 52, map[string]int{"Latin": 52}},
		// A TITLECASE letter, category Lt. `unicode.IsLetter` includes it, and
		// adding an `unicode.Is(unicode.Lt, r)` exclusion passed every other
		// assertion while reporting zero letters for it.
		{"titlecase", "\u01c5", 1, map[string]int{"Latin": 1}},
		// The real candidate: 113 letters of which 21 are Han, so 18.6%. An
		// earlier comment of mine said "22 of 108", which was wrong and was
		// never asserted.
		{"english and han", mixedAfter, 113, map[string]int{"Latin": 92, "Han": 21}},
		{"pure han", pureHan, 21, map[string]int{"Han": 21}},
		// INTERLEAVED, so a script is revisited after another intervenes. No
		// other exact-share fixture returns to a script, so a mutant resetting
		// a count on re-entry passed the whole package — reporting Latin at
		// 1/3 here instead of 2/3.
		{"interleaved", "A\u674eB", 3, map[string]int{"Latin": 2, "Han": 1}},
		{"interleaved three times", "A\u674eB\u674eC\u674e", 6,
			map[string]int{"Latin": 3, "Han": 3}},
		// MULTILINE. The only newline fixture was letterless, so truncating at
		// the first newline passed the whole package — and hard-wrapped prose
		// is the common case, not an edge one. This is the third issue in a row
		// where a single-line fixture set hid a truncation.
		{"across a newline", "A\n\u674e", 2, map[string]int{"Latin": 1, "Han": 1}},
		{"across a blank line", "plain\n\nplainer", 12, map[string]int{"Latin": 12}},
		// Past 255 of one script, because `uint8` counters passed the entire
		// package: 256 Han letters wrapped to zero, so the share was 0, the
		// names were [Latin] and nothing was introduced.
		{"more than a byte of one script", "A" + manyHan(256), 257,
			map[string]int{"Latin": 1, "Han": 256}},
		{"turkish", "Gerçekten çok güzel bir gün oldu ve herkes bundan memnun kaldı sanırım.",
			59, map[string]int{"Latin": 59}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := text.Scripts(c.in)

			if got.Letters() != c.letters {
				t.Fatalf("counted %d letters, want %d", got.Letters(), c.letters)
			}
			for name, n := range c.counts {
				assertShare(t, got, name, float64(n)/float64(c.letters))
			}
			// An ABSENT ordinary script is a finite zero. Returning NaN for
			// absent scripts, while keeping zero for Common, Inherited and
			// letterless input, passed every other assertion here.
			for _, absent := range []string{"Han", "Greek", "Cyrillic", "Armenian", "Thai"} {
				if _, present := c.counts[absent]; present {
					continue
				}
				assertShare(t, got, absent, 0)
			}
			// The shares of the named scripts account for every counted letter,
			// so nothing is double-counted and nothing is missing.
			var total float64
			for _, name := range got.Names() {
				total += got.Share(name)
			}
			if math.Abs(total-1) > 1e-9 {
				t.Errorf("shares sum to %.6f over %v, want 1", total, got.Names())
			}
		})
	}
}

// Every script Unicode knows, not a handful.
//
// Restricting recognition to Latin, Han, Greek and Cyrillic passed the first
// version of this file, and so did ignoring every code point above U+FFFF.
// `unicode.Scripts` has 163 entries; the implementation must not carry its own
// shorter list.
func TestEveryScriptIsRecognisedIncludingAboveTheBasicPlane(t *testing.T) {
	for _, c := range []struct {
		script string
		in     string
	}{
		{"Hiragana", "ひらがな"},
		{"Katakana", "カタカナ"},
		{"Hangul", "한국어"},
		{"Arabic", "العربية"},
		{"Devanagari", "देवनागरी"},
		{"Hebrew", "עברית"},
		{"Thai", "ภาษาไทย"},
		{"Greek", "Ελληνικά"},
		{"Cyrillic", "Кириллица"},
		// Supplementary plane: CJK extension B. Ignoring everything above
		// U+FFFF passed the first version.
		{"Han", "𠀀𠀁"},
	} {
		t.Run(c.script, func(t *testing.T) {
			got := text.Scripts(c.in)
			if got.Letters() == 0 {
				t.Fatalf("%q counted no letters", c.in)
			}
			assertShare(t, got, c.script, 1)
		})
	}
}

// Every script Unicode defines is recognised, derived from `unicode.Scripts`
// rather than from a list written here.
//
// The explicit table above exercises ten scripts, and restricting recognition
// to exactly those ten passed — Armenian then returned zero letters. So this
// walks all 163 entries, picks a representative letter from each after NFC, and
// requires it to be attributed to that script.
//
// Scripts with no eligible letter are skipped rather than failed: Braille is
// symbols, and some entries are entirely marks or digits. The count of scripts
// actually checked is asserted, so a future Unicode table that stopped
// producing letters cannot quietly empty this test.
func TestRecognitionIsDerivedFromUnicodeAndNotAList(t *testing.T) {
	checked := 0
	for name, table := range unicode.Scripts {
		if name == "Common" || name == "Inherited" {
			continue
		}
		r, ok := representativeLetter(table)
		if !ok {
			continue
		}
		checked++
		got := text.Scripts(string(r))
		if got.Letters() != 1 {
			t.Errorf("%s: %q counted %d letters, want 1", name, string(r), got.Letters())
			continue
		}
		// Through the finite-checking helper: a mutant that counted the letter
		// but attributed only the eleven explicitly covered scripts returned
		// NaN here, and a bare tolerance comparison accepts it.
		assertShare(t, got, name, 1)
		// And the NAME, and the introduction. Keeping counts and shares correct
		// while restricting `Names()` to those eleven also passed, which meant
		// "A" to "Ա" reported no introduction at all.
		if names := got.Names(); !sameNames(names, []string{name}) {
			t.Errorf("%s: %q names %v, want [%s]", name, string(r), names, name)
		}
		if out := got.Introduced(text.Scripts("")); !sameNames(out, []string{name}) {
			t.Errorf("%s: %q introduces %v against letterless current, want [%s]",
				name, string(r), out, name)
		}
		// Against NONEMPTY current, both ways. Testing only letterless current
		// let a mutant return every name for the empty case while restricting
		// ordinary comparisons to eleven scripts, so "A" to "Ա" introduced
		// nothing; and a mutant restricting the CURRENT side made "Ա" to "Ա"
		// introduce Armenian.
		if name != "Latin" {
			if out := got.Introduced(text.Scripts("A")); !sameNames(out, []string{name}) {
				t.Errorf("%s: %q introduces %v against Latin current, want [%s]",
					name, string(r), out, name)
			}
		}
		if out := got.Introduced(got); len(out) != 0 {
			t.Errorf("%s: %q introduces %v against itself, want none",
				name, string(r), out)
		}
	}
	// Well over the ten the explicit table covers, so a short allow-list
	// cannot satisfy this.
	if checked < 100 {
		t.Errorf("checked %d scripts, want at least 100 of the %d Unicode defines",
			checked, len(unicode.Scripts))
	}
}

// representativeLetter finds one code point in the range that this contract
// counts: a letter, after NFC, that is not Common or Inherited.
func representativeLetter(table *unicode.RangeTable) (rune, bool) {
	for _, r16 := range table.R16 {
		for r := rune(r16.Lo); r <= rune(r16.Hi); r += rune(r16.Stride) {
			if eligible(r) {
				return r, true
			}
		}
	}
	for _, r32 := range table.R32 {
		for r := rune(r32.Lo); r <= rune(r32.Hi); r += rune(r32.Stride) {
			if eligible(r) {
				return r, true
			}
		}
	}
	return 0, false
}

func eligible(r rune) bool {
	if !unicode.IsLetter(r) || unicode.Is(unicode.Common, r) || unicode.Is(unicode.Inherited, r) {
		return false
	}
	// A code point that changes under NFC would be counted as whatever it
	// normalizes to, which is a different script's letter.
	return norm.NFC.String(string(r)) == string(r)
}

// Counting happens after NFC, so canonically equivalent text measures the same.
//
// Hangul is the case that forces this: its decomposed jamo are LETTERS, so
// skipping marks cannot reconcile them.
func TestCanonicallyEquivalentTextMeasuresTheSame(t *testing.T) {
	for _, c := range []struct {
		name                 string
		composed, decomposed string
		letters              int
		names                []string
		shares               map[string]float64
	}{
		{
			// Under NFC both are 2 letters, half Latin and half Hangul. Under
			// NFD both would be 4 and three-quarters Hangul.
			name: "hangul", composed: "A\uD55C", decomposed: "A\u1112\u1161\u11AB",
			letters: 2, names: []string{"Hangul", "Latin"},
			shares: map[string]float64{"Hangul": 0.5, "Latin": 0.5},
		},
		{
			// The combining-acute case, which skipping marks also handles.
			name: "latin acute", composed: "caf\u00e9 society", decomposed: "cafe\u0301 society",
			letters: 11, names: []string{"Latin"},
			shares: map[string]float64{"Latin": 1},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.composed == c.decomposed {
				t.Fatal("the two forms are identical, so this proves nothing")
			}
			a, b := text.Scripts(c.composed), text.Scripts(c.decomposed)

			// ABSOLUTE expectations on both forms, not merely agreement
			// between them. Normalizing to NFD instead of NFC makes them agree
			// at 4 letters and 75% Hangul, so an equality-only assertion
			// passed the wrong normalization.
			for _, form := range []struct {
				label string
				got   text.ScriptSet
			}{{"composed", a}, {"decomposed", b}} {
				if form.got.Letters() != c.letters {
					t.Errorf("%s counts %d letters, want %d",
						form.label, form.got.Letters(), c.letters)
				}
				for name, want := range c.shares {
					assertShare(t, form.got, name, want)
				}
				if !sameNames(form.got.Names(), c.names) {
					t.Errorf("%s names %v, want %v", form.label, form.got.Names(), c.names)
				}
			}
		})
	}
}

// Characters carrying no script evidence are not counted at all.
//
// `Common` and `Inherited` letters are the surprising half. Measured: `ー`
// (U+30FC) and Arabic tatweel (U+0640) are LETTERS whose script is `Common`,
// and so is mathematical bold `A` (U+1D400). Exposing them would report a
// `Common` script beside Katakana for an ordinary Japanese word, and make a
// rewrite that added a long-vowel mark an introduction.
func TestCharactersWithNoScriptEvidenceAreNotCounted(t *testing.T) {
	// Counts computed, not hand-counted. Two of them were wrong in an earlier
	// version — 21 for a string with 20 letters and 14 for one with 13 — and
	// both were false reds that would have rejected a correct implementation.
	for _, c := range []struct {
		name      string
		in        string
		letters   int
		wantNames []string
		shares    map[string]float64
	}{
		// A long-vowel mark adds no script and no letter.
		{"katakana with a prolonged sound mark", "\u30b3\u30f3\u30d4\u30e5\u30fc\u30bf\u30fc",
			5, []string{"Katakana"}, map[string]float64{"Katakana": 1}},
		// Tatweel is an Arabic-script elongation classified Common.
		{"arabic with tatweel", "\u0639\u0640\u0631\u0628\u064a",
			4, []string{"Arabic"}, map[string]float64{"Arabic": 1}},
		// Mathematical bold capital A is a letter, and not a writing system.
		{"mathematical bold letters", "\U0001D400\U0001D401 plain",
			5, []string{"Latin"}, map[string]float64{"Latin": 1}},
		// Digits carry a script but are not letters, so an Arabic-Indic digit
		// must not make Latin prose report Arabic.
		{"arabic-indic digits in english", "We counted \u0663\u0664 of them today",
			20, []string{"Latin"}, map[string]float64{"Latin": 1}},
		// Script-bearing MARKS are skipped too: a Hebrew point and a Devanagari
		// vowel sign carry Hebrew and Devanagari rather than Inherited.
		{"script-bearing marks alone", "plain text \u05b4 \u093e here",
			13, []string{"Latin"}, map[string]float64{"Latin": 1}},
		// LETTER-NUMBERS, category Nl, which `unicode.IsLetter` excludes. The
		// mirror of the titlecase case above: Lt must be counted and Nl must
		// not, and a predicate of `IsLetter(r) || Is(Nl, r)` passed everything.
		// U+3021 is a HAN-script number, so under that mutant "A〡" reported 2
		// letters, half Han, and introduced Han.
		{"han letter-number", "A\u3021", 1, []string{"Latin"},
			map[string]float64{"Latin": 1}},
		// And a Latin-script one, so the exclusion is by CATEGORY rather than
		// by script: U+2160 is the Roman numeral one.
		{"latin letter-number", "A\u2160", 1, []string{"Latin"},
			map[string]float64{"Latin": 1}},
		// MIXED, so the exclusion shows up in the PROPORTIONS and not only in
		// the names. A mutant that kept Letters() and Names() right but divided
		// by every Unicode letter reported 5/7 Katakana for the word above;
		// excluded properly, beside Latin, it is 5/13.
		{"katakana with a mark, beside latin",
			"\u30b3\u30f3\u30d4\u30e5\u30fc\u30bf\u30fc and plain",
			13, []string{"Katakana", "Latin"},
			map[string]float64{"Katakana": 5.0 / 13, "Latin": 8.0 / 13}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := text.Scripts(c.in)
			if got.Letters() != c.letters {
				t.Errorf("counted %d letters, want %d", got.Letters(), c.letters)
			}
			if !sameNames(got.Names(), c.wantNames) {
				t.Errorf("names %v, want %v", got.Names(), c.wantNames)
			}
			for name, want := range c.shares {
				assertShare(t, got, name, want)
			}
			for _, absent := range []string{"Common", "Inherited"} {
				if got.Share(absent) != 0 {
					t.Errorf("reports %q at %v, want none", absent, got.Share(absent))
				}
			}
		})
	}
}

// No non-letter category contributes, enumerated rather than listed.
//
// Three rounds running found one more numeric or mark category that a mutated
// predicate could include: Nl, then No, with Mn and Mc already covered by
// hand. Each fixture closed one category and left the rest open, so this walks
// `unicode.Categories` instead: for every category that is not a letter
// category, it finds a code point carrying a real script and requires it to
// count for nothing.
//
// Titlecase is the one letter category that must be INCLUDED, and is asserted
// separately — `Lt` is a letter and `unicode.IsLetter` says so.
func TestNoNonLetterCategoryContributes(t *testing.T) {
	checked := map[string]rune{}
	for name, table := range unicode.Categories {
		// Letter categories are the subject of the rest of this file. Single
		// letters "L" and the composite are skipped along with Lu/Ll/Lt/Lm/Lo.
		if name == "" || name[0] == 'L' {
			continue
		}
		r, ok := scriptBearing(table)
		if !ok {
			continue
		}
		checked[name] = r

		got := text.Scripts("A" + string(r))
		if got.Letters() != 1 {
			t.Errorf("category %s: %q counted %d letters beside one Latin, want 1",
				name, string(r), got.Letters())
			continue
		}
		assertShare(t, got, "Latin", 1)
		if !sameNames(got.Names(), []string{"Latin"}) {
			t.Errorf("category %s: %q gave names %v beside one Latin, want [Latin]",
				name, string(r), got.Names())
		}
		if out := got.Introduced(text.Scripts("A")); len(out) != 0 {
			t.Errorf("category %s: %q introduced %v, want none", name, string(r), out)
		}
	}
	// The categories this is known to reach. Asserted so a future Unicode
	// table that stopped yielding script-bearing code points in them cannot
	// quietly empty this test.
	for _, name := range []string{"Nd", "Nl", "No", "Mn", "Mc"} {
		if _, ok := checked[name]; !ok {
			t.Errorf("category %s contributed no script-bearing code point to check", name)
		}
	}
	if len(checked) < 5 {
		t.Errorf("checked %d non-letter categories, want at least 5", len(checked))
	}
}

// scriptBearing finds a code point in the table whose script is a real one, so
// the assertion is about the CATEGORY rather than about Common or Inherited.
func scriptBearing(table *unicode.RangeTable) (rune, bool) {
	hasScript := func(r rune) bool {
		if unicode.Is(unicode.Common, r) || unicode.Is(unicode.Inherited, r) {
			return false
		}
		if norm.NFC.String(string(r)) != string(r) {
			return false
		}
		for name, tab := range unicode.Scripts {
			if name != "Common" && name != "Inherited" && unicode.Is(tab, r) {
				return true
			}
		}
		return false
	}
	for _, r16 := range table.R16 {
		for r := rune(r16.Lo); r <= rune(r16.Hi); r += rune(r16.Stride) {
			if hasScript(r) {
				return r, true
			}
		}
	}
	for _, r32 := range table.R32 {
		for r := rune(r32.Lo); r <= rune(r32.Hi); r += rune(r32.Stride) {
			if hasScript(r) {
				return r, true
			}
		}
	}
	return 0, false
}

// The limits of this measurement, stated as fixtures so they are intentional.
//
// Each follows from "letters, after NFC, excluding Common and Inherited" and
// each would surprise someone. Recording them is the point; none is a defect
// to fix here, and a consumer refusing every introduction would be wrong about
// at least the first two.
//
// The exclusion of Common is a defensible SIMPLIFICATION rather than a claim
// that such characters carry no evidence at all — UAX #24's Script_Extensions
// describes how Common can mean "shared among a restricted set of scripts". It
// is excluded because resolving that contextually is a larger contract than
// this gate needs.
func TestTheKnownLimitsOfScriptEvidence(t *testing.T) {
	for _, c := range []struct {
		name               string
		current, candidate string
		want               []string
		note               string
		letters            int
		checked            bool
		shares             map[string]float64
	}{
		{
			name:    "the micro sign is Latin and Greek mu is Greek",
			current: "The gap was 5\u00b5m across", candidate: "The gap was 5\u03bcm across",
			want: []string{"Greek"},
			note: "an ordinary unit-notation substitution reads as a script change",
		},
		{
			name:    "a Hangul filler is a Hangul letter",
			current: "plain", candidate: "plain\u3164",
			want: []string{"Hangul"},
			note: "U+3164 survives NFC and is default-ignorable, yet is a letter",
		},
		{
			name:    "braille carries no letters at all",
			current: "hello", candidate: "\u2813\u2811\u2807\u2807\u2815",
			want: nil,
			note: "braille patterns are symbols, so a braille rewrite reports nothing",
			// Asserted, because a mutant counting script-bearing SYMBOLS toward
			// the letter total while attributing only letters passed the
			// introduction check alone, reporting five letters here.
			letters: 0,
			checked: true,
		},
		{
			// And beside Latin, so the total and the share are both visible.
			name:    "braille beside latin",
			current: "hello", candidate: "A\u2813\u2811\u2807\u2807\u2815",
			want:    nil,
			note:    "the braille cells contribute nothing, so this is wholly Latin",
			letters: 1,
			checked: true,
			shares:  map[string]float64{"Latin": 1},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			candidate := text.Scripts(c.candidate)
			if got := candidate.Introduced(text.Scripts(c.current)); !sameNames(got, c.want) {
				t.Errorf("introduced %v, want %v — %s", got, c.want, c.note)
			}
			if c.checked && candidate.Letters() != c.letters {
				t.Errorf("the candidate counts %d letters, want %d — %s",
					candidate.Letters(), c.letters, c.note)
			}
			for name, want := range c.shares {
				assertShare(t, candidate, name, want)
			}
		})
	}
}

// assertShare compares a share to an exact fraction and refuses a non-finite
// one. `math.Abs(NaN-x) > tolerance` is false, so a tolerance comparison alone
// accepts NaN — which a mutant returning NaN for some scripts exploited.
func assertShare(t *testing.T, got text.ScriptSet, name string, want float64) {
	t.Helper()
	share := got.Share(name)
	if math.IsNaN(share) || math.IsInf(share, 0) {
		t.Errorf("%q share is not finite: %v", name, share)
		return
	}
	if math.Abs(share-want) > 1e-9 {
		t.Errorf("%q share = %.6f, want %.6f", name, share, want)
	}
}

// Names reports exactly the scripts present, sorted, without duplicates.
//
// Its only assertion was once for letterless input, which a function returning
// nil unconditionally satisfies.
func TestNamesReportsExactlyWhatIsPresent(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want []string
	}{
		{"single script", englishBefore, []string{"Latin"}},
		{"two scripts", mixedAfter, []string{"Han", "Latin"}},
		{"three scripts", "Plain 质疑 and \u03a9\u03bc\u03b5 and \u041f\u0440\u0438 mixed",
			[]string{"Cyrillic", "Greek", "Han", "Latin"}},
		{"no letters", "123 — …", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := text.Scripts(c.in).Names()

			if len(got) != len(c.want) {
				t.Fatalf("names %v, want %v", got, c.want)
			}
			// Sorted, so a rejection reason reads the same every run. Map
			// iteration order in Go is deliberately random.
			if !sort.StringsAreSorted(got) {
				t.Errorf("names %v are not sorted", got)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("names %v, want %v", got, c.want)
				}
			}
			seen := map[string]bool{}
			for _, name := range got {
				if seen[name] {
					t.Errorf("%q appears twice in %v", name, got)
				}
				seen[name] = true
			}
		})
	}
}

// Introduced names the scripts the candidate adds, whatever their share.
func TestIntroducedNamesOnlyWhatTheCandidateAdds(t *testing.T) {
	for _, c := range []struct {
		name               string
		current, candidate string
		want               []string
	}{
		{"english rewritten with han", englishBefore, mixedAfter, []string{"Han"}},
		// A Han-script letter-NUMBER introduces nothing, because it is not a
		// letter. Under the Nl-counting mutant this introduced Han.
		{"a han letter-number introduces nothing", "A", "A\u3021", nil},
		// And across a newline, where truncating at the first one reported no
		// introduction at all.
		{"introduced on a later line", "A", "A\n\u674e", []string{"Han"}},
		{"han rewritten with han", pureHan, pureHan + "的问题", nil},
		{"han rewritten with english", pureHan, englishBefore, []string{"Latin"}},
		{"english rewritten with english", englishBefore,
			"I asked it to break the thing rather than to review it politely.", nil},
		// Accented Latin adds no script, so European prose is never an
		// introduction however different the language.
		{"english rewritten with turkish", englishBefore,
			"Gerçekten çok güzel bir gün oldu ve herkes bundan memnun kaldı.", nil},
		// No threshold: one Han letter among a thousand Latin ones is still an
		// introduction. A sub-1% suppression survived the first version.
		{"one han letter among a thousand latin", englishBefore,
			strings1000() + "李", []string{"Han"}},
		// And the CURRENT side: a script present in only one letter of a
		// thousand is still present, so nothing is introduced by using it.
		// A mutant treating a sub-1% current script as absent passed until
		// this direction existed.
		{"one han letter among a thousand, on the current side",
			strings1000() + "李", "李", nil},
		// THREE introductions at once. Returning after the first newly seen
		// script passed, because every other expected list here holds zero or
		// one name — which also left the ordering untested.
		{"three scripts introduced at once", "A",
			"A \u03a9 \u0416 \u674e", []string{"Cyrillic", "Greek", "Han"}},
		// MIXED CURRENT. Every current text was once single-script, so a mutant
		// treating only scripts above a 50% share as "already present" passed.
		{"mixed current, both retained", mixedAfter, mixedAfter, nil},
		{"mixed current, one dropped", mixedAfter, englishBefore, nil},
		{"mixed current, proportions changed", mixedAfter, pureHan + " tail", nil},
		{"mixed current, a third script added", mixedAfter,
			mixedAfter + " \u03a9\u03bc\u03b5\u03b3\u03b1", []string{"Greek"}},
		// Japanese, so the limits of set difference are intentional rather than
		// discovered later. Katakana IS introduced by an ordinary Japanese
		// rewrite; a Japanese-to-Chinese rewrite using only Han is not.
		{"japanese adds katakana", "\u72ac\u304c\u597d\u304d\u3067\u3059",
			"\u30a4\u30cc\u304c\u597d\u304d\u3067\u3059", []string{"Katakana"}},
		{"japanese narrowed to han", "\u72ac\u304c\u597d\u304d\u3067\u3059",
			"\u72ac\u597d", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := text.Scripts(c.candidate).Introduced(text.Scripts(c.current))

			if len(got) != len(c.want) {
				t.Fatalf("introduced %v, want %v", got, c.want)
			}
			if !sort.StringsAreSorted(got) {
				t.Errorf("introduced %v is not sorted", got)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("introduced %v, want %v", got, c.want)
				}
			}
		})
	}
}

// Introduced does not change either set it reads.
//
// A mutant that recorded the candidate's scripts into the CURRENT set passed
// the whole package, because no test compared twice against the same value:
// the first call returned [Han] and the second returned nothing. A
// measurement that changes when you read it twice is not a measurement.
func TestIntroducedLeavesBothSetsUnchanged(t *testing.T) {
	for _, c := range []struct {
		name               string
		current, candidate string
		want               []string
		currentNames       []string
		candidateNames     []string
	}{
		{
			name: "disjoint", current: "A", candidate: "\u674e",
			want:         []string{"Han"},
			currentNames: []string{"Latin"}, candidateNames: []string{"Han"},
		},
		{
			// OVERLAPPING, which the disjoint case cannot cover: a mutant
			// deleting the SHARED script from `current` passed the whole
			// package, returning [Han] and then [Han Latin] while current's
			// names emptied and its Latin share went to zero.
			name: "overlapping", current: "A", candidate: "A\u674e",
			want:         []string{"Han"},
			currentNames: []string{"Latin"}, candidateNames: []string{"Han", "Latin"},
		},
		{
			// And contained the other way, so the candidate is a subset.
			name: "candidate is a subset", current: "A\u674e", candidate: "A",
			want:         nil,
			currentNames: []string{"Han", "Latin"}, candidateNames: []string{"Latin"},
		},
		{
			// REPEATED letters and UNEQUAL counts on both sides. Every script
			// above occurs exactly once, so collapsing counts to presence flags
			// — setting every count to 1 — was invisible in the shares while
			// the introduced list stayed correct. Here current is Latin 2 / Han
			// 1 and the candidate Latin 3 / Han 2 / Greek 1, so a flag collapse
			// moves current's Latin share from 2/3 to 1/3 and the candidate's
			// from 1/2 to 1/6.
			name:    "repeated letters, unequal counts",
			current: "AA\u674e", candidate: "AAA\u674e\u674e\u03a9",
			want:           []string{"Greek"},
			currentNames:   []string{"Han", "Latin"},
			candidateNames: []string{"Greek", "Han", "Latin"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			current, candidate := text.Scripts(c.current), text.Scripts(c.candidate)

			snapshot := func(in text.ScriptSet) (int, map[string]float64) {
				shares := map[string]float64{}
				for _, name := range in.Names() {
					shares[name] = in.Share(name)
				}
				return in.Letters(), shares
			}
			currentLetters, currentShares := snapshot(current)
			candidateLetters, candidateShares := snapshot(candidate)

			// Three times, because a mutant writing into either side is only
			// visible on the second read.
			for attempt := 1; attempt <= 3; attempt++ {
				if got := candidate.Introduced(current); !sameNames(got, c.want) {
					t.Fatalf("attempt %d introduced %v, want %v", attempt, got, c.want)
				}
			}

			for _, side := range []struct {
				label   string
				got     text.ScriptSet
				letters int
				names   []string
				shares  map[string]float64
			}{
				{"current", current, currentLetters, c.currentNames, currentShares},
				{"candidate", candidate, candidateLetters, c.candidateNames, candidateShares},
			} {
				if side.got.Letters() != side.letters {
					t.Errorf("the %s set now counts %d letters, was %d",
						side.label, side.got.Letters(), side.letters)
				}
				if !sameNames(side.got.Names(), side.names) {
					t.Errorf("the %s set now names %v, want %v",
						side.label, side.got.Names(), side.names)
				}
				for name, want := range side.shares {
					assertShare(t, side.got, name, want)
				}
			}
		})
	}
}

// Letterless text reports nothing rather than dividing by zero.
func TestTextWithNoLettersReportsNoScripts(t *testing.T) {
	for _, in := range []string{"", "   ", "123 456", "— … !?", "\n\n"} {
		got := text.Scripts(in)
		if got.Letters() != 0 {
			t.Errorf("%q counts %d letters, want 0", in, got.Letters())
		}
		if share := got.Share("Latin"); share != 0 {
			t.Errorf("%q reports a Latin share of %v, want 0", in, share)
		}
		if len(got.Names()) != 0 {
			t.Errorf("%q names %v, want none", in, got.Names())
		}
		// And nothing is introduced relative to it, or by it.
		if out := text.Scripts(englishBefore).Introduced(got); len(out) != 1 || out[0] != "Latin" {
			t.Errorf("against letterless current, english introduces %v, want [Latin]", out)
		}
		if out := got.Introduced(text.Scripts(englishBefore)); len(out) != 0 {
			t.Errorf("letterless candidate introduces %v, want none", out)
		}
	}
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// manyHan is n Han letters, enough to overflow a byte-wide counter.
func manyHan(n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = '\u674e'
	}
	return string(out)
}

// strings1000 is a thousand Latin letters, so a proportional threshold inside
// Introduced has somewhere to hide and be caught.
func strings1000() string {
	out := make([]byte, 0, 1000)
	for i := 0; i < 1000; i++ {
		out = append(out, 'a')
	}
	return string(out)
}
