// Package features_test defines the contract for feature extraction.
//
// SCOPE. This slice extracts part of the candidate feature manifest recorded in
// docs/DESIGN.md Section 2, from a token stream, and nothing else. It does not
// derive minimum sample sizes or tiers, transform values, build profiles, or
// score. Section 2 requires tier membership and minimums to be MEASURED against
// a declared reference population, so every definition records its tier as
// PROVISIONAL and no minimum is claimed at all.
//
// Deferred from the manifest, with reasons:
//
//   - sentence-length mean and variance need sentence segmentation (text slice
//     2c, absent);
//   - contraction RATE needs a denominator of contractible opportunities, which
//     requires detecting unrealized forms ("do not") via a bidirectional
//     lexicon. The tokenizer's per-token Contraction flag is a numerator only,
//     and a rate built from it alone would measure verbosity, not preference;
//   - word-length DISTRIBUTION. The mean implemented here is a summary of it,
//     not a replacement, and the manifest records both.
package features_test

import (
	"math"
	"testing"

	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/text"
)

// wantSetVersion pins the manifest version from docs/DESIGN.md Section 2.
//
// It does NOT force a bump, and an earlier comment here wrongly claimed it did:
// changing the computation and regenerating the golden values below, with both
// numbers left alone, passes. Nothing in-repo can prevent that.
//
// What the pinned golden values buy is narrower still: a change that alters an
// output FOR THE GOLDEN VECTOR cannot be made silently, because the diff shows a
// changed number rather than a changed line of arithmetic. A change affecting
// only inputs outside goldenText leaves every pinned expectation intact and is
// not caught here. Cache identity is the content hash described in Section 2,
// not this integer.
const wantSetVersion = 1

const goldenText = "The Constitution, which was drafted in 1787, provides that the several States shall retain their powers; it does so because the framers feared concentration: a fear well founded."

// Derived independently, in a separate script, from a dump of the already
// merged and separately tested tokenizer — not from this package. 27 of the 33
// tokens are lexical and their lengths sum to 142.
var goldenValues = map[features.ID]float64{
	features.WordLengthMean:   142.0 / 27.0,
	features.CommaDensity:     2.0 / 27.0,
	features.SemicolonDensity: 1.0 / 27.0,
	features.ColonDensity:     1.0 / 27.0,
	features.FunctionWordRate: 14.0 / 27.0,
	features.ClauseMarkerRate: 3.0 / 27.0,
}

func extract(t *testing.T, src string) features.Vector {
	t.Helper()
	doc, err := text.Admit([]byte(src))
	if err != nil {
		t.Fatalf("Admit(%q): %v", src, err)
	}
	return features.Extract(doc.Tokens())
}

func value(t *testing.T, v features.Vector, id features.ID) float64 {
	t.Helper()
	fv, ok := v.Get(id)
	if !ok {
		t.Fatalf("vector has no entry for %q", id)
	}
	if !fv.Defined {
		t.Fatalf("%s is undefined", id)
	}
	return fv.Value
}

func assertClose(t *testing.T, got, want float64, id features.ID) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("%s = %v, want %v", id, got, want)
	}
}

// ---------------------------------------------------------------------------
// The manifest
// ---------------------------------------------------------------------------

func TestDefinitionsAreWellFormed(t *testing.T) {
	defs := features.Definitions()
	if len(defs) != len(goldenValues) {
		t.Fatalf("%d definitions, %d pinned golden values — every feature needs a pinned expectation", len(defs), len(goldenValues))
	}

	seen := map[features.ID]bool{}
	for _, d := range defs {
		if d.ID == "" {
			t.Error("definition with empty ID")
		}
		if seen[d.ID] {
			t.Errorf("duplicate feature ID %q", d.ID)
		}
		seen[d.ID] = true
		if d.Description == "" {
			t.Errorf("%s has no description", d.ID)
		}
		// A boolean cannot say WHICH tier is claimed, so the definition carries
		// both the claim and its provisional status.
		if d.CandidateTier != features.TierA {
			t.Errorf("%s claims tier %q; every feature in this slice is a Tier A candidate", d.ID, d.CandidateTier)
		}
		if !d.TierProvisional {
			t.Errorf("%s does not record its tier as provisional; no derivation has run", d.ID)
		}
	}
}

// The aggregate function-word rate collapses the Tier B function-word
// distribution into one ratio, which may measure content density rather than
// identity. It is a candidate for the derivation to rule on, and must say so.
func TestAggregateFunctionWordRateIsMarkedUnvalidated(t *testing.T) {
	for _, d := range features.Definitions() {
		if d.ID != features.FunctionWordRate {
			continue
		}
		if !d.Unvalidated {
			t.Error("FunctionWordRate is not marked Unvalidated; it has not earned a Tier A place from the design")
		}
		return
	}
	t.Fatal("no definition for FunctionWordRate")
}

func TestDefinitionOrderIsStable(t *testing.T) {
	first := features.Definitions()
	for i := 0; i < 3; i++ {
		again := features.Definitions()
		if len(again) != len(first) {
			t.Fatalf("definition count changed: %d then %d", len(first), len(again))
		}
		for j := range first {
			if first[j].ID != again[j].ID {
				t.Fatalf("definition %d changed from %q to %q", j, first[j].ID, again[j].ID)
			}
		}
	}
}

// Exported slices must not alias package state, or one caller can silently
// corrupt every subsequent extraction.
func TestExportedSlicesAreDefensiveCopies(t *testing.T) {
	defs := features.Definitions()
	if len(defs) == 0 {
		t.Fatal("no definitions")
	}
	original := defs[0].ID
	defs[0].ID = "tampered"
	if again := features.Definitions(); again[0].ID != original {
		t.Errorf("Definitions() returns package state; caller mutation changed %q to %q", original, again[0].ID)
	}

	fw := features.FunctionWords()
	firstWord := fw[0]
	fw[0] = "tampered"
	if again := features.FunctionWords(); again[0] != firstWord {
		t.Error("FunctionWords() returns package state")
	}

	cm := features.ClauseMarkers()
	firstMarker := cm[0]
	cm[0] = "tampered"
	if again := features.ClauseMarkers(); again[0] != firstMarker {
		t.Error("ClauseMarkers() returns package state")
	}
}

func TestVectorCoversExactlyTheDefinitions(t *testing.T) {
	v := extract(t, "The quick brown fox, which jumped, was tired.")
	defs := features.Definitions()
	if len(v.Values) != len(defs) {
		t.Fatalf("vector holds %d values, %d definitions", len(v.Values), len(defs))
	}
	for i, d := range defs {
		if v.Values[i].ID != d.ID {
			t.Errorf("value %d is %q, want %q — vectors are positional", i, v.Values[i].ID, d.ID)
		}
	}
}

func TestFeatureSetVersionIsPinned(t *testing.T) {
	if got := features.SetVersion; got != wantSetVersion {
		t.Errorf("SetVersion = %d, want %d.\n\nIf the feature set changed deliberately, bump wantSetVersion "+
			"and regenerate the golden vector IN THE SAME COMMIT.", got, wantSetVersion)
	}
	if v := extract(t, "anything"); v.SetVersion != features.SetVersion {
		t.Errorf("vector reports SetVersion %d, package reports %d", v.SetVersion, features.SetVersion)
	}
}

// ---------------------------------------------------------------------------
// Vocabularies, pinned exactly
// ---------------------------------------------------------------------------

func TestFunctionWordListIsPinned(t *testing.T) {
	fw := features.FunctionWords()
	if len(fw) != 147 {
		t.Errorf("function word list has %d entries, want 147", len(fw))
	}
	assertNoDuplicates(t, "function words", fw)
	assertAllLowerCase(t, "function words", fw)

	assertMembership(t, "function words", fw,
		// Closed-class members that must be present, including the modals a
		// frequency-derived list tends to omit.
		[]string{"the", "of", "and", "to", "a", "in", "that", "it", "is", "was",
			"may", "might", "must", "will", "shall", "can", "could", "would",
			"either", "neither", "upon", "within", "without", "among", "whose",
			// Subordinating conjunctions: closed-class, and their absence was
			// inconsistent while because/since/while/until/whether/if were present.
			"although", "though", "whereas", "unless"},
		// Register-sensitive adverbs deliberately excluded: they track subject
		// matter and emphasis rather than grammar.
		[]string{"again", "further", "more", "most", "only", "same", "very"})
}

func TestClauseMarkerListIsPinned(t *testing.T) {
	cm := features.ClauseMarkers()
	if len(cm) != 20 {
		t.Errorf("clause marker list has %d entries, want 20", len(cm))
	}
	assertNoDuplicates(t, "clause markers", cm)
	assertAllLowerCase(t, "clause markers", cm)
	assertMembership(t, "clause markers", cm,
		[]string{"that", "which", "who", "because", "although", "while", "since", "unless", "whether", "if"},
		[]string{"the", "and", "of", "very"})
}

// The two vocabularies overlap by design. That is not double counting — they
// are separate dimensions — but it is residual correlation that Section 2
// requires to be measured before any weighting is fitted, so it is pinned here
// rather than left implicit.
func TestVocabularyOverlapIsPinned(t *testing.T) {
	fw := map[string]bool{}
	for _, w := range features.FunctionWords() {
		fw[w] = true
	}
	overlap := 0
	for _, m := range features.ClauseMarkers() {
		if fw[m] {
			overlap++
		}
	}
	// Every clause marker is also a function word. That is expected: the marker
	// list is drawn from closed-class subordinators and relativisers, which are
	// function words by definition. Total overlap makes the residual correlation
	// Section 2 requires to be measured maximal rather than incidental.
	if overlap != len(features.ClauseMarkers()) {
		t.Errorf("vocabulary overlap is %d of %d markers, want total overlap; a partial overlap means the function-word list is missing closed-class subordinators", overlap, len(features.ClauseMarkers()))
	}
}

func assertNoDuplicates(t *testing.T, label string, words []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, w := range words {
		if seen[w] {
			t.Errorf("%s: duplicate entry %q", label, w)
		}
		seen[w] = true
	}
}

func assertAllLowerCase(t *testing.T, label string, words []string) {
	t.Helper()
	for _, w := range words {
		for _, r := range w {
			if r >= 'A' && r <= 'Z' {
				t.Errorf("%s: %q is not lower-case; matching is case-folded", label, w)
				break
			}
		}
	}
}

func assertMembership(t *testing.T, label string, words, mustHave, mustNotHave []string) {
	t.Helper()
	set := map[string]bool{}
	for _, w := range words {
		set[w] = true
	}
	for _, w := range mustHave {
		if !set[w] {
			t.Errorf("%s: %q is missing", label, w)
		}
	}
	for _, w := range mustNotHave {
		if set[w] {
			t.Errorf("%s: %q is present but deliberately excluded", label, w)
		}
	}
}

// ---------------------------------------------------------------------------
// Extraction reads its argument
// ---------------------------------------------------------------------------

// Built by hand rather than routed through the tokenizer, so this proves
// Extract uses the tokens it is given. Passing the same text through the
// tokenizer twice would prove neither that nor determinism.
func TestExtractUsesTheTokensItIsGiven(t *testing.T) {
	toks := []text.Token{
		{Text: "the", Class: text.Word, Lexical: true},
		{Text: ",", Class: text.Punctuation},
		{Text: "because", Class: text.Word, Lexical: true},
		{Text: "elephants", Class: text.Word, Lexical: true},
		{Text: "42", Class: text.Number},
	}
	v := features.Extract(toks)

	if v.Tokens != 5 {
		t.Errorf("Tokens = %d, want 5", v.Tokens)
	}
	if v.LexicalTokens != 3 {
		t.Errorf("LexicalTokens = %d, want 3", v.LexicalTokens)
	}
	// the(3) + because(7) + elephants(9) = 19 over 3
	assertClose(t, value(t, v, features.WordLengthMean), 19.0/3.0, features.WordLengthMean)
	assertClose(t, value(t, v, features.CommaDensity), 1.0/3.0, features.CommaDensity)
	assertClose(t, value(t, v, features.FunctionWordRate), 2.0/3.0, features.FunctionWordRate)
	assertClose(t, value(t, v, features.ClauseMarkerRate), 1.0/3.0, features.ClauseMarkerRate)
}

// Token.Lexical is the authority for what counts as a word, not Token.Class.
// A synthetic token that disagrees with itself must follow Lexical, so the rule
// is unambiguous for any future producer of tokens.
func TestLexicalFlagIsAuthoritativeOverClass(t *testing.T) {
	v := features.Extract([]text.Token{
		{Text: "aaaa", Class: text.Word, Lexical: false},
		{Text: "bb", Class: text.Number, Lexical: true},
	})
	if v.LexicalTokens != 1 {
		t.Fatalf("LexicalTokens = %d, want 1 — Lexical governs", v.LexicalTokens)
	}
	assertClose(t, value(t, v, features.WordLengthMean), 2, features.WordLengthMean)
}

// ---------------------------------------------------------------------------
// Individual features
// ---------------------------------------------------------------------------

func TestWordLengthUsesLexicalTokensOnly(t *testing.T) {
	assertClose(t, value(t, extract(t, "aa bbb cccc"), features.WordLengthMean), 3, features.WordLengthMean)
	assertClose(t, value(t, extract(t, "aa bbb cccc 12345 £"), features.WordLengthMean), 3, features.WordLengthMean)
	assertClose(t, value(t, extract(t, "aa, bbb; cccc."), features.WordLengthMean), 3, features.WordLengthMean)
}

// Length counts NFC characters, not bytes: accented prose must not read as
// using longer words. Escapes are used because literal accented forms get
// silently composed in source.
func TestWordLengthCountsCharactersNotBytes(t *testing.T) {
	const composed = "café"    // 4 characters, 5 bytes
	const decomposed = "café" // 4 characters after NFC, 6 bytes
	assertClose(t, value(t, extract(t, composed), features.WordLengthMean), 4, features.WordLengthMean)
	assertClose(t, value(t, extract(t, decomposed), features.WordLengthMean), 4, features.WordLengthMean)
}

func TestNonASCIILexicalTokensAreCounted(t *testing.T) {
	// Greek "logos", five characters.
	assertClose(t, value(t, extract(t, "λόγος"), features.WordLengthMean), 5, features.WordLengthMean)
}

func TestPunctuationDensitiesAreCountedSeparately(t *testing.T) {
	v := extract(t, "one; two: three, four")
	assertClose(t, value(t, v, features.CommaDensity), 0.25, features.CommaDensity)
	assertClose(t, value(t, v, features.SemicolonDensity), 0.25, features.SemicolonDensity)
	assertClose(t, value(t, v, features.ColonDensity), 0.25, features.ColonDensity)
}

// A density is a count per lexical token and is unbounded above. Treating it as
// a proportion would make a range check wrong for half the feature set.
func TestDensitiesMayExceedOne(t *testing.T) {
	if got := value(t, extract(t, "word,,,"), features.CommaDensity); got != 3 {
		t.Errorf("CommaDensity = %v for one word and three commas, want 3", got)
	}
}

func TestFunctionWordRate(t *testing.T) {
	assertClose(t, value(t, extract(t, "the cat sat on the mat"), features.FunctionWordRate), 0.5, features.FunctionWordRate)
	assertClose(t, value(t, extract(t, "cats chase mice"), features.FunctionWordRate), 0, features.FunctionWordRate)
}

func TestVocabularyMatchingIsCaseFolded(t *testing.T) {
	lower := value(t, extract(t, "the cat sat on the mat because it ran"), features.FunctionWordRate)
	upper := value(t, extract(t, "The cat sat On The mat BECAUSE It ran"), features.FunctionWordRate)
	assertClose(t, upper, lower, features.FunctionWordRate)

	lowerCM := value(t, extract(t, "the book that she read because it was short"), features.ClauseMarkerRate)
	upperCM := value(t, extract(t, "The book That she read BECAUSE it was short"), features.ClauseMarkerRate)
	assertClose(t, upperCM, lowerCM, features.ClauseMarkerRate)
}

func TestClauseMarkerRate(t *testing.T) {
	assertClose(t, value(t, extract(t, "the book that she read because it was short"), features.ClauseMarkerRate), 2.0/9.0, features.ClauseMarkerRate)
}

// Rates are memberships over lexical tokens and cannot exceed 1.
func TestRatesAreBounded(t *testing.T) {
	v := extract(t, goldenText)
	for _, id := range []features.ID{features.FunctionWordRate, features.ClauseMarkerRate} {
		got := value(t, v, id)
		if got < 0 || got > 1 {
			t.Errorf("%s = %v, outside [0,1]", id, got)
		}
	}
	assertClose(t, value(t, extract(t, "the of and to a"), features.FunctionWordRate), 1, features.FunctionWordRate)
}

// ---------------------------------------------------------------------------
// Undefined values
// ---------------------------------------------------------------------------

// A value over zero lexical tokens is undefined. It is marked, never encoded as
// a number: NaN is refused by encoding/json, compares unequal to itself, and has
// no canonical bit pattern for hashing — and these values are persisted and
// keyed per Section 2's cache identity rules.
func TestValuesOverNoLexicalTokensAreUndefined(t *testing.T) {
	for _, src := range []string{"", "   ", ",,,", "123 456", "£ €"} {
		t.Run(src, func(t *testing.T) {
			v := extract(t, src)
			for _, d := range features.Definitions() {
				fv, ok := v.Get(d.ID)
				if !ok {
					t.Fatalf("vector has no entry for %q", d.ID)
				}
				if fv.Defined {
					t.Errorf("%s reports Defined with no lexical tokens (value %v)", d.ID, fv.Value)
				}
				if math.IsNaN(fv.Value) {
					t.Errorf("%s carries NaN; undefined must be signalled by Defined, not by the number", d.ID)
				}
			}
		})
	}
}

func TestVectorCarriesCounts(t *testing.T) {
	v := extract(t, "one, two three")
	if v.LexicalTokens != 3 {
		t.Errorf("LexicalTokens = %d, want 3", v.LexicalTokens)
	}
	if v.Tokens != 4 {
		t.Errorf("Tokens = %d, want 4", v.Tokens)
	}
	empty := extract(t, "")
	if empty.LexicalTokens != 0 || empty.Tokens != 0 {
		t.Errorf("empty document reports Tokens=%d LexicalTokens=%d, want 0/0", empty.Tokens, empty.LexicalTokens)
	}
}

// ---------------------------------------------------------------------------
// Golden vector and determinism
// ---------------------------------------------------------------------------

func TestGoldenVector(t *testing.T) {
	v := extract(t, goldenText)
	if v.Tokens != 33 {
		t.Errorf("Tokens = %d, want 33", v.Tokens)
	}
	if v.LexicalTokens != 27 {
		t.Errorf("LexicalTokens = %d, want 27", v.LexicalTokens)
	}
	for id, want := range goldenValues {
		assertClose(t, value(t, v, id), want, id)
	}
	for _, d := range features.Definitions() {
		if _, ok := goldenValues[d.ID]; !ok {
			t.Errorf("feature %q has no pinned golden value; add one and bump wantSetVersion", d.ID)
		}
	}
}

func TestExtractionIsDeterministic(t *testing.T) {
	first := extract(t, goldenText)
	for i := 0; i < 3; i++ {
		again := extract(t, goldenText)
		for j := range first.Values {
			a, b := first.Values[j], again.Values[j]
			if a.ID != b.ID || a.Defined != b.Defined || a.Value != b.Value {
				t.Fatalf("run %d: %s changed from %+v to %+v", i, a.ID, a, b)
			}
		}
	}
}
