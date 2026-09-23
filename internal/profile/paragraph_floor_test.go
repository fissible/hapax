package profile_test

// #92 and #64, which are the same defect.
//
// hapax needs 30 observations per feature to FIT a profile and 30 segments in a
// reference to RANK against. It needs ONE lexical token to score a paragraph:
// `DefaultRequirements().MinParagraphLexicalTokens` is 1.
//
// Measured against a real 50-document profile:
//
//	idx 0  tokens=2   distance=1.1041  defined=true
//	idx 1  tokens=27  distance=0.7716  defined=true
//	idx 2  tokens=1   distance=1.5316  defined=true
//
// A one-token paragraph — "Yes." — scored 1.5316, the HIGHEST in the document,
// so `rewrite` offered it as the most promising target. The reference's own 30
// observations were satisfied, so a distance was produced.
//
// #64 recorded the same root cause from the other end — at a floor of 1,
// `paragraphs_below_floor` can never be non-zero — and named the decision:
// "Whether that is right is a stylometry question, not a plumbing one, and
// should be decided on evidence rather than to make a test pass." It also
// records that the default was once raised to 3 to make a fixture pass, and
// reverted in review. This is the evidence it asked for.
//
// # What this floor is, and what it is NOT
//
// It is an INTERIM SAFETY BOUND: ten lexical tokens, so that for a fixed
// denominator N >= 10, a one-count change moves a rate by 1/N <= 0.1.
//
// Stated with that precision because a looser version — "a rate cannot move in
// increments larger than 0.1" — is false: an arbitrary edit changes the
// denominator too and can move a rate by much more.
//
// Stated precisely, because a first version of this comment got it wrong. The
// manifest is not six rates. It is one MEAN (word_length_mean), three
// DENSITIES (comma, semicolon, colon), and two membership RATES (function_word,
// clause_marker). Only the two rates are bounded in [0,1]; a density is a count
// per lexical token and is unbounded above — `TestDensitiesMayExceedOne` in
// internal/features proves comma density is 3 for one word and three commas,
// and its comment says treating a density as a proportion "would make a range
// check wrong for half the feature set". I made that exact error and put it in
// a rationale.
//
// The classification still matters for describing the features honestly, but not
// for this arithmetic: the densities have the same fixed-denominator one-count
// resolution, and being unbounded does not change it.
//
// So what ten buys is a CHOSEN RESOLUTION CONSTRAINT, and nothing more. It is
// not a measured reliability threshold and provides no reliability guarantee for
// ANY feature.
//
// It is NOT the derivation DESIGN Section 2 asks for — "minimum segment size per
// tier ... as measured numbers, not claims". That is an empirical question about
// how distance reliability falls off with segment size, per feature tier, and it
// has not been answered. `ParagraphFloorDerived` therefore stays FALSE, which is
// what the existing TestParagraphFloorIsRecordedAsUnderived already asserts.
//
// Ten is a judgment with a statable rationale, and it is recorded as one. It is
// the third number proposed for this field; the first two were 1, which admits a
// one-word paragraph as a measurement, and 3, which was chosen to make a fixture
// pass. Anyone revisiting it should do Section 2's measurement rather than pick
// a fourth.
//
// # One floor, not two
//
// An earlier draft separated the floor that decides which paragraphs INFORM the
// profile from the floor that decides which can be MEASURED against it, and
// derived the second from the corpus. That was abandoned, and the corpus-derived
// rule was fragile anyway — five one-token paragraphs in a corpus of 1,970 long
// ones collapsed it back to 1.
//
// One floor is kept for a narrower reason than the one first given here. The
// original justification — that a vector too unreliable to score against is
// equally unreliable as an observation — does not hold: an observation unfit for
// an individual decision can still inform an aggregate, and individual scoring
// reliability and estimation of a population mean are different questions.
//
// The actual reason is that one floor keeps fitting, reference construction and
// scoring on the SAME admitted population, which is a property the rest of the
// system already depends on.
//
// Its cost, recorded rather than accepted silently: the profile now describes
// paragraphs of at least ten lexical tokens, and excludes short-form habits from
// its means and variances. That can move the statistics and can change corpus
// eligibility. Matching the admission rules also does not remove length-dependent
// noise among the paragraphs that survive.
//
// # Existing profiles keep the floor they were built with
//
// `score` and `rewrite` use the PERSISTED profile's floor, which is correct — a
// profile is the instrument it was fitted as, and silently overriding its stored
// floor at scoring time would measure against a population the profile never
// saw.
//
// So changing this default does not repair a profile already on disk. The real
// profile that produced the 1.5316 above will keep scoring "Yes." until it is
// re-indexed, and its dependent reference and calibration artifacts regenerated.
// That is a migration note, not a defect, and it is stated here because a user
// who upgrades and sees no change would otherwise be right to think the fix did
// not land.
//
// # What this does not fix
//
// A 26-word paragraph measured against a corpus of 62-word paragraphs is still a
// noisy estimate. This stops the absurd cases. Register mismatch stays until the
// corpus matches what is being written, and no floor repairs that.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

// The declared default, and the number itself.
func TestTheParagraphFloorIsTen(t *testing.T) {
	if got := profile.DefaultRequirements().MinParagraphLexicalTokens; got != 10 {
		t.Errorf("MinParagraphLexicalTokens = %d, want 10", got)
	}
}

// And it is still declared rather than derived. Section 2's per-tier
// measurement has not been done, and a profile that claimed otherwise would be
// asserting evidence that does not exist.
//
// This duplicates TestParagraphFloorIsRecordedAsUnderived deliberately: that
// test guards the flag, this one guards the pairing of the flag with the new
// value, so raising the floor cannot quietly come to mean "derived".
func TestRaisingTheFloorDoesNotMakeItDerived(t *testing.T) {
	built := floorProfile(t)
	if built.ParagraphFloorDerived {
		t.Error("the profile claims a derived paragraph floor; ten is a declared " +
			"interim bound and Section 2's measurement has not run")
	}
	if built.Requirements.MinParagraphLexicalTokens != 10 {
		t.Errorf("profile floor = %d, want 10", built.Requirements.MinParagraphLexicalTokens)
	}
}

// ---------------------------------------------------------------------------
// The behaviour that caused this
// ---------------------------------------------------------------------------

// A paragraph below the floor produces no vector, and is counted.
//
// The counting half is #64: at a floor of one, `paragraphs_below_floor` could
// never be non-zero, so it was a reported member that was always 0 — a
// measurement-shaped value that no input could move.
func TestAParagraphBelowTheFloorIsExcludedAndCounted(t *testing.T) {
	// One paragraph at nine tokens, one at twenty. Nine is below ten by one, so
	// this pins the boundary rather than an obviously tiny case.
	doc := admitted(t, paragraphOf(9)+paragraphOf(20))

	got, err := profile.ParagraphVectors(doc, profile.DefaultRequirements().MinParagraphLexicalTokens)
	if err != nil {
		t.Fatalf("ParagraphVectors: %v", err)
	}
	if len(got.Vectors) != 1 {
		t.Errorf("kept %d paragraphs, want 1", len(got.Vectors))
	}
	if got.BelowFloor != 1 {
		t.Errorf("below floor = %d, want 1 — #64's unreachable member is now reachable",
			got.BelowFloor)
	}
}

// Exactly at the floor is admitted. A floor is a minimum, and an off-by-one here
// silently discards a whole band of real paragraphs.
func TestAParagraphExactlyAtTheFloorIsAdmitted(t *testing.T) {
	doc := admitted(t, paragraphOf(10))

	got, err := profile.ParagraphVectors(doc, profile.DefaultRequirements().MinParagraphLexicalTokens)
	if err != nil {
		t.Fatalf("ParagraphVectors: %v", err)
	}
	if len(got.Vectors) != 1 {
		t.Errorf("a ten-token paragraph was excluded at a floor of ten")
	}
	if got.BelowFloor != 0 {
		t.Errorf("below floor = %d, want 0", got.BelowFloor)
	}
}

// The case that prompted the issue, at the size it actually occurred.
func TestTheOneTokenParagraphIsNotAMeasurementUnit(t *testing.T) {
	doc := admitted(t, "Yes.\n\n"+paragraphOf(20))

	got, err := profile.ParagraphVectors(doc, profile.DefaultRequirements().MinParagraphLexicalTokens)
	if err != nil {
		t.Fatalf("ParagraphVectors: %v", err)
	}
	if got.BelowFloor != 1 {
		t.Errorf("below floor = %d, want 1: \"Yes.\" is one lexical token and scored "+
			"1.5316 against a real profile", got.BelowFloor)
	}
	if len(got.Vectors) != 1 {
		t.Errorf("kept %d paragraphs, want only the twenty-token one", len(got.Vectors))
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// floorProfile is a real fitted profile at the declared defaults, since what is
// under test is what a real invocation produces.
func floorProfile(t *testing.T) *profile.Profile {
	t.Helper()
	req := profile.DefaultRequirements()
	// The paragraph and observation minimums are lowered so the fixture need not
	// be large; the FLOOR is the default, because it is the subject.
	req.MinDocuments, req.MinParagraphs, req.MinObservationsPerFeature = 1, 1, 1
	return build(t, multiParagraphCorpus(8), req)
}

// paragraphOf builds a paragraph with exactly n lexical tokens.
func paragraphOf(n int) string {
	words := make([]string, 0, n)
	for i := 0; i < n; i++ {
		words = append(words, fmt.Sprintf("word%d", i))
	}
	return strings.Join(words, " ") + ".\n\n"
}

func admitted(t *testing.T, body string) *text.Document {
	t.Helper()
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return doc
}
