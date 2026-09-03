package eval_test

// #83. `hapax eval` on a real corpus and a real distractor pool exits 3 with
// `store: invalid: threshold verdict` — an operational failure — when what
// actually happened is that the two populations do not separate, which is a
// measurement and should be reported as one.
//
// # Where the contradiction is
//
// Calibrate derives two boundaries: `a`, the author threshold, and `d`, the
// distractor threshold. It records `Separated: a < d`, which is the real
// question — the author's distances must sit BELOW the distractors' for the
// bands to mean anything. Then it sorts them:
//
//	out.Low, out.High = math.Min(a, d), math.Max(a, d)
//
// so `Low < High` says only that the two boundaries differ. When a > d the two
// disagree: Separated is false and Low < High is true. The store's rule —
// `(verdict == separated) == (Low < High)` — then refuses the artifact.
//
// # Why the store is right and Calibrate is wrong
//
// Band reads `<= Low` as in-range and `>= High` as not-you. Under a > d the sort
// makes Low the DISTRACTOR threshold, so "in-range" would name the region where
// the author's own distances have already been exceeded. The bands are inverted,
// silently, and the store's constraint is the only thing that notices.
//
// Removing the sort makes Separated and the ordering the same fact:
//
//	a < d  ->  Low < High   separated
//	a == d ->  Low == High  not separated
//	a > d  ->  Low > High   not separated
//
// # And a threshold that did not separate must not band
//
// With Low >= High the two branches of Band overlap, so every distance is both
// in-range and not-you. Today that is unreachable only because a non-separating
// release should not ship — an argument about a caller, not a property of the
// type. Band refuses instead.

import (
	"errors"
	"strconv"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
)

// orderingDistance is one calibration distance at a chosen value.
func orderingDistance(value float64) deviation.Distance {
	return deviation.Distance{
		Defined: true, Value: value, Split: corpus.Calibrate,
		ProfileID: "profile-83", ReferenceID: "reference-83", FeatureManifestDigest: "manifest-83",
		WeightScheme: deviation.WeightSchemeUniform, Algorithm: deviation.DistanceAlgorithm,
		ScoredTiers: []features.Tier{features.TierA},
	}
}

// populations builds a calibration set whose author distances sit around one
// centre and whose distractor distances sit around another. Forty of each, well
// above the derived minimums, so the thresholds are chosen from real quantiles
// rather than from a population too small to have any.
func populations(authorCentre, distractorCentre float64) []eval.ClassedDistance {
	var out []eval.ClassedDistance
	for i := 0; i < 40; i++ {
		step := float64(i) / 1000
		out = append(out,
			eval.ClassedDistance{Class: eval.ClassAuthor, Document: "a", Author: "",
				Distance: orderingDistance(authorCentre + step)},
			eval.ClassedDistance{Class: eval.ClassDistractor, Document: "d", Author: "",
				Distance: orderingDistance(distractorCentre + step)})
	}
	return out
}

func orderingCalibration(t *testing.T, distances []eval.ClassedDistance) *eval.Thresholds {
	t.Helper()
	got, err := eval.Calibrate(distances,
		eval.Source{Cohort: "cohort-83", DistractorPool: "pool-83"}, eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// The invariant
// ---------------------------------------------------------------------------

// Separated and the ordering are one fact, in every direction the populations
// can take. This is the assertion the store's constraint already encodes and
// that Calibrate can currently contradict.
func TestSeparationAndOrderingNeverDisagree(t *testing.T) {
	for _, c := range []struct {
		name                           string
		authorCentre, distractorCentre float64
	}{
		{"author below distractor", 0.1, 0.8},
		{"author above distractor", 0.8, 0.1},
		{"same centre", 0.4, 0.4},
		{"author just below", 0.40, 0.41},
		{"author just above", 0.41, 0.40},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := orderingCalibration(t, populations(c.authorCentre, c.distractorCentre))

			if got.Separated != (got.Low < got.High) {
				t.Errorf("Separated = %v with Low = %v and High = %v; the store's rule is "+
					"(verdict == separated) == (Low < High) and this artifact cannot satisfy it",
					got.Separated, got.Low, got.High)
			}
		})
	}
}

// Low is the AUTHOR's threshold and High is the DISTRACTORS', whichever way
// round the numbers come out. Sorting them is what let the two disagree, and it
// also silently swaps what the bands mean.
func TestTheBoundariesAreNotSorted(t *testing.T) {
	inverted := orderingCalibration(t, populations(0.8, 0.1))

	if inverted.Low <= inverted.High {
		t.Fatalf("Low = %v and High = %v; with the author population entirely above the "+
			"distractors, the author threshold must still be reported as Low",
			inverted.Low, inverted.High)
	}
	if inverted.Separated {
		t.Error("a threshold whose author boundary sits above its distractor boundary called itself separated")
	}

	// And the ordinary direction is unchanged, so the fix is not simply an
	// inversion of the old behaviour.
	ordinary := orderingCalibration(t, populations(0.1, 0.8))
	if !(ordinary.Low < ordinary.High) || !ordinary.Separated {
		t.Errorf("an author population below the distractors gave Low = %v, High = %v, separated = %v",
			ordinary.Low, ordinary.High, ordinary.Separated)
	}
}

// The achieved rates are BAND error rates, measured against the band boundary
// each class can be misread by: an author segment is misread when it lands in
// not-you (>= High), a distractor when it lands in in-range (<= Low).
//
// Range and finiteness would not prove this. Two rates that stayed swapped are
// both in [0,1] and both finite, so the exact values are asserted against
// populations whose quantiles are computable by hand.
func TestTheAchievedRatesAreMeasuredAgainstTheBandBoundaries(t *testing.T) {
	// Author 1..20 at a 5% target gives A = 20; distractor 5..14 at 10% gives
	// D = 5. The pair is inverted, so Low = 20 and High = 5.
	got := orderingCalibration(t, integerPopulations(1, 20, 5, 14))
	if !near(got.Low, 20) || !near(got.High, 5) {
		t.Fatalf("Low = %v, High = %v; want the author boundary 20 and the distractor boundary 5",
			got.Low, got.High)
	}

	// Sixteen of the twenty author distances (5..20) sit at or above High = 5.
	if !near(got.AchievedAuthor, 0.80) {
		t.Errorf("achieved author = %v, want 0.80", got.AchievedAuthor)
	}
	// Every distractor (5..14) sits at or below Low = 20.
	if !near(got.AchievedDistractor, 1.0) {
		t.Errorf("achieved distractor = %v, want 1.0", got.AchievedDistractor)
	}

	// And the ordinary direction, where the two are not equal to each other, so
	// an implementation that returned the same number for both would be caught.
	// Author 1..20 gives A = 20; distractor 30..39 gives D = 30. Low = 20,
	// High = 30. No author distance reaches 30 and no distractor falls to 20.
	ordinary := orderingCalibration(t, integerPopulations(1, 20, 30, 39))
	if !near(ordinary.Low, 20) || !near(ordinary.High, 30) {
		t.Fatalf("Low = %v, High = %v; want 20 and 30", ordinary.Low, ordinary.High)
	}
	if !near(ordinary.AchievedAuthor, 0) {
		t.Errorf("achieved author = %v, want 0", ordinary.AchievedAuthor)
	}
	if !near(ordinary.AchievedDistractor, 0) {
		t.Errorf("achieved distractor = %v, want 0", ordinary.AchievedDistractor)
	}
}

// integerPopulations builds calibration distances over two integer ranges, so
// the quantiles the thresholds land on can be computed by hand rather than
// read off the implementation.
func integerPopulations(authorLow, authorHigh, distractorLow, distractorHigh int) []eval.ClassedDistance {
	var out []eval.ClassedDistance
	for v := authorLow; v <= authorHigh; v++ {
		out = append(out, eval.ClassedDistance{Class: eval.ClassAuthor, Document: "a", Author: "",
			Distance: orderingDistance(float64(v))})
	}
	for v := distractorLow; v <= distractorHigh; v++ {
		out = append(out, eval.ClassedDistance{Class: eval.ClassDistractor, Document: "d", Author: "",
			Distance: orderingDistance(float64(v))})
	}
	return out
}

// ---------------------------------------------------------------------------
// The single derivation
// ---------------------------------------------------------------------------

// Two places write a threshold's verdict — internal/workflow when eval persists
// one, and internal/store when a release is put. Both derived it from a
// DIFFERENT question than the store's rule asks: one from whether the bootstrap
// intervals were shippable, the other from whether the bands calibrated. Neither
// is "are the boundaries ordered", so both could write an artifact their own
// validation refuses.
//
// One exported derivation, so the two cannot drift apart again and a test can
// pin the rule once.
func TestTheVerdictIsDerivedFromTheOrdering(t *testing.T) {
	for _, c := range []struct {
		name      string
		low, high float64
		want      eval.ThresholdVerdict
	}{
		{"ordered", 0.1, 0.8, eval.VerdictSeparated},
		{"equal", 0.4, 0.4, eval.VerdictPairIncompatible},
		{"inverted", 0.8, 0.1, eval.VerdictPairIncompatible},
		{"both zero", 0, 0, eval.VerdictPairIncompatible},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := eval.VerdictFor(c.low, c.high); got != c.want {
				t.Errorf("VerdictFor(%v, %v) = %q, want %q", c.low, c.high, got, c.want)
			}
		})
	}
}

// And whatever Calibrate produces, the derivation agrees with it. This is the
// pairing the store checks, asserted against real Calibrate output rather than
// against hand-written numbers.
func TestTheDerivationAgreesWithEveryCalibration(t *testing.T) {
	for _, centres := range [][2]float64{{0.1, 0.8}, {0.8, 0.1}, {0.4, 0.4}} {
		got := orderingCalibration(t, populations(centres[0], centres[1]))
		verdict := eval.VerdictFor(got.Low, got.High)
		if (verdict == eval.VerdictSeparated) != got.Separated {
			t.Errorf("centres %v: VerdictFor said %q and the threshold says separated = %v",
				centres, verdict, got.Separated)
		}
	}
}

// ---------------------------------------------------------------------------
// Banding against a threshold that did not separate
// ---------------------------------------------------------------------------

// With Low >= High the two branches overlap and every distance satisfies both,
// so the first one wins and the answer is an artifact of the code's order. A
// threshold that did not separate has no bands to assign, and says so.
func TestANonSeparatedThresholdRefusesToBand(t *testing.T) {
	got := orderingCalibration(t, populations(0.8, 0.1))
	if got.Separated {
		t.Fatal("the fixture separated; it is meant not to")
	}

	outcome, err := got.Band(orderingBandDistance(got, 0.5))

	if !errors.Is(err, eval.ErrNotSeparated) {
		t.Fatalf("Band returned (%+v, %v), want %v", outcome, err, eval.ErrNotSeparated)
	}
	if outcome.Band != "" {
		t.Errorf("a refused band still named %q", outcome.Band)
	}
	if outcome.Defined {
		t.Error("a refused band called itself defined")
	}
}

// A separated threshold bands exactly as it did before. The refusal above must
// not be reachable on the path that actually ships.
func TestASeparatedThresholdStillBands(t *testing.T) {
	got := orderingCalibration(t, populations(0.1, 0.8))
	if !got.Separated {
		t.Fatal("the fixture did not separate; it is meant to")
	}
	for _, c := range []struct {
		name  string
		value float64
		want  eval.Band
	}{
		{"below the low boundary", got.Low - 0.05, eval.BandInRange},
		{"between", (got.Low + got.High) / 2, eval.BandDrifting},
		{"above the high boundary", got.High + 0.05, eval.BandNotYou},
	} {
		t.Run(c.name, func(t *testing.T) {
			outcome, err := got.Band(orderingBandDistance(got, c.value))
			if err != nil {
				t.Fatalf("Band: %v", err)
			}
			if outcome.Band != c.want {
				t.Errorf("band = %q, want %q", outcome.Band, c.want)
			}
			if !outcome.Defined {
				t.Error("a banded distance is not defined")
			}
		})
	}
}

// An undefined distance is still not an error, separated or not: it is a
// measurement that did not happen, and Band has always reported that rather
// than failing. Checked on the non-separated threshold, because the new refusal
// must not swallow it.
func TestAnUndefinedDistanceIsStillNotAnError(t *testing.T) {
	for _, c := range []struct {
		name    string
		centres [2]float64
	}{
		{"separated", [2]float64{0.1, 0.8}},
		{"not separated", [2]float64{0.8, 0.1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := orderingCalibration(t, populations(c.centres[0], c.centres[1]))
			outcome, err := got.Band(deviation.Distance{
				Defined: false, Reason: deviation.ReasonInsufficientEvidence,
			})
			if err != nil {
				t.Fatalf("Band on an undefined distance: %v", err)
			}
			if outcome.Defined || outcome.Band != "" {
				t.Errorf("an undefined distance was banded as %q", outcome.Band)
			}
		})
	}
}

// orderingBandDistance is a Test-split distance bound to the threshold under
// test, which is what Band requires of anything it is asked to assign.
func orderingBandDistance(t *eval.Thresholds, value float64) deviation.Distance {
	return deviation.Distance{
		Defined: true, Value: value, Split: corpus.Test,
		ProfileID: t.ProfileID, ReferenceID: t.ReferenceID,
		FeatureManifestDigest: t.FeatureManifestDigest,
		WeightScheme:          t.WeightScheme, Algorithm: t.DistanceAlgorithm,
		ScoredTiers: t.ScoredTiers,
	}
}

// ---------------------------------------------------------------------------
// The path that actually decides whether a release ships
// ---------------------------------------------------------------------------

// CalibrateBands does NOT go through Band. It applies the same geometric
// predicates itself, so an implementation can pass every Band assertion above
// and still emit a claiming band off a threshold whose boundaries are inverted
// — and a claiming band is what makes a release calibrated, and calibration is
// half of shippable.
//
// # The fixture has to be able to emit
//
// A first version of this used twenty author and ten distractor documents and
// passed against the unfixed code. It proved nothing: the band floor's hard 3/n
// bound needs ceil(3/0.05) = 60 author clusters and ceil(3/0.10) = 30 distractor
// clusters before either band can be emitted at all, so those fixtures were
// refused for a reason that has nothing to do with separation.
//
// So: a hundred of each, with SPARSE TAILS, which is what puts the two
// boundaries far apart. Ninety-nine author distances at 10 and one at 20 puts
// the author boundary at 20; one distractor at 5 and ninety-nine at 15 puts the
// distractor boundary at 5. Inverted — and under the old sort the code sees
// (5, 20), a wide and apparently healthy pair, over which BOTH claiming bands
// emit at 1% observed error. That is the shape this test has to refuse.
func TestANonSeparatedThresholdEmitsNoClaimingBand(t *testing.T) {
	for _, c := range []struct {
		name                                                   string
		authorBulk, authorTail, distractorTail, distractorBulk float64
	}{
		{"inverted with sparse tails", 10, 20, 5, 15},
		{"inverted and further apart", 10, 30, 2, 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			thresholds := orderingCalibration(t,
				sparsePopulations(c.authorBulk, c.authorTail, c.distractorTail, c.distractorBulk))
			if thresholds.Separated {
				t.Fatalf("this fixture separated; Low = %v, High = %v", thresholds.Low, thresholds.High)
			}

			held := sparseHeldOut(c.authorBulk, c.authorTail, c.distractorTail, c.distractorBulk)
			calibration, err := thresholds.CalibrateBands(held, eval.DefaultBandFloor())
			if err != nil {
				t.Fatalf("CalibrateBands: %v", err)
			}

			if calibration.Calibrated {
				t.Error("a threshold whose author boundary sits at or above its distractor " +
					"boundary produced a CALIBRATED result")
			}
			for _, band := range calibration.Bands {
				if band.Claims != "" && band.Emitted {
					t.Errorf("band %q emitted a claim about %q off a threshold that did not "+
						"separate, at an observed error rate of %v", band.Band, band.Claims, band.ErrorRate)
				}
			}

			discrimination, err := eval.Discriminate(held, eval.DefaultDiscrimination())
			if err != nil {
				t.Fatalf("Discriminate: %v", err)
			}
			release, err := eval.NewRelease(discrimination, calibration)
			if err != nil {
				t.Fatalf("NewRelease: %v", err)
			}
			if release.Shippable {
				t.Error("a release built on a threshold that did not separate called itself shippable")
			}
		})
	}
}

// sparsePopulations builds a hundred author and a hundred distractor distances,
// each a bulk with a single outlier, so the chosen quantiles sit far from the
// mass. Enough of them that the band floor is not what refuses the bands.
func sparsePopulations(authorBulk, authorTail, distractorTail, distractorBulk float64) []eval.ClassedDistance {
	var out []eval.ClassedDistance
	for i := 0; i < 99; i++ {
		out = append(out, eval.ClassedDistance{Class: eval.ClassAuthor, Document: "a", Author: "",
			Distance: orderingDistance(authorBulk)})
	}
	out = append(out, eval.ClassedDistance{Class: eval.ClassAuthor, Document: "a", Author: "",
		Distance: orderingDistance(authorTail)})
	out = append(out, eval.ClassedDistance{Class: eval.ClassDistractor, Document: "d", Author: "",
		Distance: orderingDistance(distractorTail)})
	for i := 0; i < 99; i++ {
		out = append(out, eval.ClassedDistance{Class: eval.ClassDistractor, Document: "d", Author: "",
			Distance: orderingDistance(distractorBulk)})
	}
	return out
}

// sparseHeldOut is the same shape on the Test split, one document per distance
// so each is its own cluster and the band floor's 3/n bound is satisfied.
func sparseHeldOut(authorBulk, authorTail, distractorTail, distractorBulk float64) []eval.ClassedDistance {
	out := sparsePopulations(authorBulk, authorTail, distractorTail, distractorBulk)
	for i := range out {
		out[i].Distance.Split = corpus.Test
		out[i].Document = string(out[i].Class) + "-" + strconv.Itoa(i)
	}
	return out
}

// heldOut is the Test-split counterpart of integerPopulations. CalibrateBands
// measures error rates over the held-out split, not the one the thresholds were
// fitted on.
func orderingHeldOut(authorLow, authorHigh, distractorLow, distractorHigh int) []eval.ClassedDistance {
	out := integerPopulations(authorLow, authorHigh, distractorLow, distractorHigh)
	for i := range out {
		out[i].Distance.Split = corpus.Test
		out[i].Document = string(out[i].Class) + "-" + strconv.Itoa(i)
	}
	return out
}

// ---------------------------------------------------------------------------
// The third geometric consumer
// ---------------------------------------------------------------------------

// Bootstrap decides Actionable from `Low.Upper < High.Lower` and never consults
// Separated either. Under the sort, each resample's Low is min(A, D) and its
// High is max(A, D), so an inverted population produces two intervals that are
// disjoint THE WRONG WAY ROUND — and Actionable comes out true, Shippable comes
// out true, and that is the value `hapax eval` was deriving the stored verdict
// from.
//
// This is the third place the same mistake lives, after CalibrateBands and Band,
// and the one that made the bad release ship rather than merely fail to persist.
// Removing the sort fixes it without Bootstrap changing at all: with Low the
// author boundary and High the distractor one, an inverted pair has
// Low.Upper > High.Lower and the intervals correctly overlap.
func TestTheBootstrapRefusesInvertedIntervals(t *testing.T) {
	inverted := orderingCalibration(t, sparsePopulations(10, 20, 5, 15))
	if inverted.Separated {
		t.Fatal("the inverted fixture separated; it is meant not to")
	}

	intervals, err := inverted.Bootstrap(sparsePopulations(10, 20, 5, 15), eval.DefaultBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if intervals.Actionable {
		t.Errorf("the bootstrap called an inverted pair actionable: author interval [%v, %v], "+
			"distractor interval [%v, %v]",
			intervals.Low.Lower, intervals.Low.Upper, intervals.High.Lower, intervals.High.Upper)
	}
	if intervals.Shippable {
		t.Error("the bootstrap called an inverted pair shippable; this is the value eval " +
			"derived the stored verdict from, and it is why the bad release persisted")
	}
	if intervals.Reason != "overlapping-intervals" {
		t.Errorf("reason = %q, want overlapping-intervals", intervals.Reason)
	}

	// And a genuinely separated pair is still actionable, so the fix is not
	// simply making the bootstrap refuse everything.
	separatedPair := orderingCalibration(t, sparsePopulations(1, 10, 30, 40))
	if !separatedPair.Separated {
		t.Fatalf("the separated fixture did not separate: Low = %v, High = %v",
			separatedPair.Low, separatedPair.High)
	}
	ordinary, err := separatedPair.Bootstrap(sparsePopulations(1, 10, 30, 40), eval.DefaultBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !ordinary.Actionable {
		t.Errorf("a separated pair was called unactionable: author interval [%v, %v], "+
			"distractor interval [%v, %v]",
			ordinary.Low.Lower, ordinary.Low.Upper, ordinary.High.Lower, ordinary.High.Upper)
	}
}

// ---------------------------------------------------------------------------
// The fourth consumer, and the one scoring actually reaches
// ---------------------------------------------------------------------------

// Calibration.Band repeats the same geometric classification against its own
// Low and High, and it is what Release.Band — and therefore `hapax score` —
// reaches. A calibration that says Calibrated with Low >= High classifies
// almost every distance as in-range, because `distance <= Low` catches
// everything below the AUTHOR boundary once that boundary is the higher of the
// two.
//
// After the fix that state is unreachable through Calibrate and CalibrateBands.
// It remains reachable through a persisted row: an older store, a hand edit, a
// corrupted write. This is the last place a wrong answer could still be given
// to a user about their own paragraph, so it fails closed rather than trusting
// the producer.
func TestACalibrationThatDidNotSeparateRefusesToBand(t *testing.T) {
	separatedPair := orderingCalibration(t, sparsePopulations(1, 10, 30, 40))
	held := sparseHeldOut(1, 10, 30, 40)
	calibration, err := separatedPair.CalibrateBands(held, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	if !calibration.Calibrated {
		t.Fatalf("the fixture is not calibrated (%s); this test needs one that is", calibration.Reason)
	}

	// The distance that is actually misclassified. With the boundaries the right
	// way round (Low = 10, High = 30) a value of 20 sits between them and bands
	// as drifting. Swap them and `20 <= Low` is true, so it comes back IN-RANGE
	// — the tool telling a person a paragraph sounds like them when the
	// measurement puts it in the middle.
	//
	// A value above both boundaries would NOT show this: 100 bands as not-you
	// either way round, and an earlier version of this test used one and proved
	// less than its comment claimed.
	far := deviation.Distance{
		Defined: true, Value: 20, Split: corpus.Test,
		ProfileID: calibration.ProfileID, ReferenceID: calibration.ReferenceID,
		FeatureManifestDigest: calibration.FeatureManifestDigest,
		WeightScheme:          calibration.WeightScheme, Algorithm: calibration.DistanceAlgorithm,
		ScoredTiers: calibration.ScoredTiers,
	}
	outcome, err := calibration.Band(far)
	if err != nil {
		t.Fatalf("Band on a separated calibration: %v", err)
	}
	if outcome.Band != eval.BandDrifting {
		t.Fatalf("the fixture bands 20 as %q with Low = %v and High = %v; this test needs drifting",
			outcome.Band, calibration.Low, calibration.High)
	}

	// Now the state a persisted row could carry: still calibrated, boundaries
	// the wrong way round.
	inverted := calibration
	inverted.Low, inverted.High = calibration.High, calibration.Low

	outcome, err = inverted.Band(far)

	if !errors.Is(err, eval.ErrNotSeparated) {
		t.Errorf("Band on a calibrated calibration whose Low (%v) is not below its High (%v) "+
			"returned (%+v, %v), want %v — this is the path `hapax score` reaches, and "+
			"without the refusal it reports in-range for a paragraph measured between "+
			"the two boundaries",
			inverted.Low, inverted.High, outcome, err, eval.ErrNotSeparated)
	}
	if outcome.Band == eval.BandInRange {
		t.Error("a calibration whose boundaries are inverted called a drifting distance in-range")
	}
}
