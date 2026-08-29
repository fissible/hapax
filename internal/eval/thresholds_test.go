package eval_test

// Band thresholds: two declared error targets, two quantiles, ordered before use.
//
// DESIGN Section 2 "Bands: two error targets, and a corrected crossing rule"
// gives two quantities and three regions:
//
//	A = Q_author(1 - p_author)        above this, author segments are rare
//	D = Q_distractor(p_distractor)    below this, distractor segments are rare
//
//	t_low = min(A, D)    t_high = max(A, D)
//
//	in range : d <= t_low     not you : d >= t_high     drifting : between
//
// Each bound comes from the distribution whose error it controls. Calling a
// segment `in range` claims it is the author's, so the error is a DISTRACTOR
// landing there, bounded from the distractor distances. Calling it `not you`
// claims the opposite, so the error is the AUTHOR landing there, bounded from
// the author distances.
//
// # Why the pair is ordered, which REVIEW Round 9 corrected
//
// An earlier draft assigned t_low = D and t_high = A unconditionally, and
// declared the two targets jointly unsatisfiable when t_low >= t_high. That rule
// was wrong in the direction that matters. D > A is the WELL-SEPARATED case: when
// the author's distances are small and the distractors' are large, the author's
// upper quantile sits below the distractors' lower quantile. Measured on
// synthetic populations at the v1 targets, the refusal fired on clean separation
// and did not fire on heavy overlap — the profile that discriminates best emitted
// no bands, and the one that barely discriminates emitted them.
//
// Ordering the pair removes the case, and both targets still hold by monotonicity
// alone, since min(A,D) <= D and max(A,D) >= A:
//
//	P(d_distractor <= min(A,D)) <= P(d_distractor <= D) = p_distractor
//	P(d_author     >= max(A,D)) <= P(d_author     >= A) = p_author
//
// Where the distributions overlap, A > D and this reproduces the old assignment
// exactly. Where they separate, A < D, the thresholds take their values from the
// opposite distributions, both achieved rates fall BELOW target, and `drifting`
// spans the gap in which neither population has mass — the honest label for a
// segment unlike both.
//
// This is not the unconditional swap Section 2 warns against. Swapping only when
// A < D is safe precisely because that inequality is what makes both bounds
// slack. The condition is the proof.
//
// # The minimums here are derived, not declared
//
// Thresholds are chosen from observed distances, so on n observations the
// smallest achievable non-zero error rate is 1/n, and a threshold meeting target
// p exists only when 1/n <= p. That forces ceil(1/p_author) author distances and
// ceil(1/p_distractor) distractor distances — 20 and 10 at the v1 targets of 0.05
// and 0.10. Below either count no qualifying threshold exists at all, and the
// outcome is no bands rather than a boundary extrapolated past the data.
//
// Sample size is necessary but not sufficient: a population whose distances are
// all equal has no qualifying threshold at any n, because every candidate carries
// the whole population in its tail.
//
// # What this slice is not
//
// Clustered bootstrap confidence intervals, the per-band minimum held-out counts
// and observed-rate check of ADR 0005's band calibration floor, and the AUC
// discrimination gate are all separate. This slice produces the boundaries and
// assigns a band; it makes no claim that a band is calibrated.

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func near(got, want float64) bool { return math.Abs(got-want) < 1e-12 }

// testSource names what a calibration was run on and cannot infer from the
// distances themselves.
//
// The distractor pool is required because Section 2 reports calibration figures
// per (profile, distractor pool) pair: a figure computed against a mismatched
// pool measures genre rather than authorship.
//
// The cohort is required because the Calibrate SPLIT is not the identity of the
// held-out documents in it. Two different calibration cohorts can produce the
// same boundaries, and an artifact that cannot tell them apart lets stale
// calibration evidence be reused under a new corpus.
//
// Neither is checked when banding: both describe how the boundaries were drawn,
// not the segment being scored.
func testSource() eval.Source {
	return eval.Source{Cohort: "calibrate-cohort-under-test", DistractorPool: "distractor-pool-under-test"}
}

// scored builds a defined distance carrying the provenance a threshold artifact
// binds itself to.
func scored(class eval.Class, value float64) eval.ClassedDistance {
	return eval.ClassedDistance{
		Class: class,
		Distance: deviation.Distance{
			ProfileID:             "profile-under-test",
			ReferenceID:           "reference-under-test",
			FeatureManifestDigest: features.ManifestDigest(),
			Split:                 corpus.Calibrate,
			Value:                 value,
			Defined:               true,
			Features:              []features.ID{features.WordLengthMean},
			ScoredTiers:           []features.Tier{features.TierA},
			WeightScheme:          deviation.WeightSchemeUniform,
			Algorithm:             deviation.DistanceAlgorithm,
		},
	}
}

// unscored builds an insufficient-evidence distance, which carries no number.
func unscored(class eval.Class) eval.ClassedDistance {
	out := scored(class, 0)
	out.Distance.Value = 0
	out.Distance.Defined = false
	out.Distance.Reason = deviation.ReasonInsufficientEvidence
	out.Distance.Features = nil
	out.Distance.ScoredTiers = nil
	return out
}

// population builds n distances of one class over an inclusive integer range.
func population(class eval.Class, from, to int) []eval.ClassedDistance {
	out := make([]eval.ClassedDistance, 0, to-from+1)
	for v := from; v <= to; v++ {
		out = append(out, scored(class, float64(v)))
	}
	return out
}

// overlapping is the case where the two distributions share ground: author
// distances 1..20, distractor 5..14.
//
//	A = 20  (one of twenty author distances is >= 20, so 0.05)
//	D = 5   (one of ten distractor distances is <= 5, so 0.10)
//	A > D, so t_low = 5 and t_high = 20, which is what the old rule also gave.
func overlapping() []eval.ClassedDistance {
	return append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 5, 14)...)
}

// separated is the case the old rule refused: author 1..20, distractor 30..39.
//
//	A = 20, D = 30. A < D, so t_low = 20 and t_high = 30.
//	The old assignment gave t_low = 30 >= t_high = 20 and emitted no bands.
func separated() []eval.ClassedDistance {
	return append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 30, 39)...)
}

func calibrate(t *testing.T, in []eval.ClassedDistance) *eval.Thresholds {
	t.Helper()
	got, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	return got
}

func bandOf(t *testing.T, th *eval.Thresholds, value float64) eval.BandOutcome {
	t.Helper()
	got, err := th.Band(scored(eval.ClassAuthor, value).Distance)
	if err != nil {
		t.Fatalf("Band(%v): %v", value, err)
	}
	return got
}

// ---------------------------------------------------------------------------
// The declared targets
// ---------------------------------------------------------------------------

// The two targets are asymmetric because the errors are: telling someone their
// own prose is not theirs is the more damaging one, so p_author is the tighter
// bound. Both are declared stand-ins, and both are asserted against their
// literal values because they enter the threshold artifact's identity — changing
// either must fail a test first.
func TestDeclaredErrorTargets(t *testing.T) {
	got := eval.DefaultTargets()

	if !near(got.Author, 0.05) {
		t.Errorf("p_author = %v, want 0.05", got.Author)
	}
	if !near(got.Distractor, 0.10) {
		t.Errorf("p_distractor = %v, want 0.10", got.Distractor)
	}
	if !(got.Author < got.Distractor) {
		t.Errorf("p_author %v is not tighter than p_distractor %v; the asymmetry is the point", got.Author, got.Distractor)
	}
}

// ---------------------------------------------------------------------------
// The quantiles, on overlapping distributions
// ---------------------------------------------------------------------------

// Author 1..20 and distractor 5..14, hand-checkable:
//
//	A: 20 is the smallest author value whose tail is within 0.05, since exactly
//	   one of twenty is >= 20. At 19 the tail is 2/20 = 0.10, over target.
//	D: 5 is the largest distractor value whose lower tail is within 0.10, since
//	   exactly one of ten is <= 5. At 6 it is 2/10 = 0.20, over target.
//
// A > D here, so the ordered pair reproduces the earlier unconditional
// assignment exactly and the achieved rates sit on their targets.
func TestThresholdsOnOverlappingDistributions(t *testing.T) {
	got := calibrate(t, overlapping())

	if !near(got.Low, 5) {
		t.Errorf("t_low = %v, want 5", got.Low)
	}
	if !near(got.High, 20) {
		t.Errorf("t_high = %v, want 20", got.High)
	}
	if got.Separated {
		t.Errorf("overlapping distributions reported as separated")
	}
	if !near(got.AchievedAuthor, 0.05) {
		t.Errorf("achieved author error = %v, want 0.05", got.AchievedAuthor)
	}
	if !near(got.AchievedDistractor, 0.10) {
		t.Errorf("achieved distractor error = %v, want 0.10", got.AchievedDistractor)
	}
}

// ---------------------------------------------------------------------------
// The quantiles, on separated distributions — the Round 9 correction
// ---------------------------------------------------------------------------

// The case the old rule refused. Author 1..20, distractor 30..39, so A = 20 and
// D = 30. The old assignment gave t_low = D = 30 and t_high = A = 20, saw
// t_low >= t_high, and emitted no bands — on the cleanest separation available.
//
// Ordered: t_low = 20, t_high = 30, `drifting` spans the empty gap between the
// populations, and both achieved rates are zero rather than merely within target.
func TestThresholdsOnSeparatedDistributions(t *testing.T) {
	got := calibrate(t, separated())

	if !near(got.Low, 20) {
		t.Errorf("t_low = %v, want 20", got.Low)
	}
	if !near(got.High, 30) {
		t.Errorf("t_high = %v, want 30", got.High)
	}
	if got.Low >= got.High {
		t.Fatalf("t_low %v is not below t_high %v on cleanly separated populations", got.Low, got.High)
	}
	if !got.Separated {
		t.Errorf("cleanly separated distributions not reported as separated")
	}
	if !near(got.AchievedAuthor, 0) {
		t.Errorf("achieved author error = %v, want 0", got.AchievedAuthor)
	}
	if !near(got.AchievedDistractor, 0) {
		t.Errorf("achieved distractor error = %v, want 0", got.AchievedDistractor)
	}
}

// The property the whole correction rests on, asserted directly rather than
// inferred from the two cases above: whatever the populations, neither achieved
// rate may exceed its declared target.
func TestAchievedRatesNeverExceedTheirTargets(t *testing.T) {
	cases := []struct {
		name string
		in   []eval.ClassedDistance
	}{
		{name: "overlapping", in: overlapping()},
		{name: "separated", in: separated()},
		{name: "author above distractor", in: append(population(eval.ClassAuthor, 30, 49), population(eval.ClassDistractor, 1, 10)...)},
		{name: "interleaved", in: append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 2, 11)...)},
		{name: "identical ranges", in: append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 1, 10)...)},
	}

	targets := eval.DefaultTargets()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := calibrate(t, c.in)
			if got.AchievedAuthor > targets.Author+1e-12 {
				t.Errorf("achieved author error %v exceeds the target %v", got.AchievedAuthor, targets.Author)
			}
			if got.AchievedDistractor > targets.Distractor+1e-12 {
				t.Errorf("achieved distractor error %v exceeds the target %v", got.AchievedDistractor, targets.Distractor)
			}
			if got.Low > got.High {
				t.Errorf("t_low %v is above t_high %v", got.Low, got.High)
			}
		})
	}
}

// The achieved rates are recomputed from the populations that produced them,
// rather than assumed equal to the targets. Section 2 requires them reported
// next to their targets for exactly this reason.
func TestAchievedRatesAreMeasuredNotAssumed(t *testing.T) {
	got := calibrate(t, separated())

	if near(got.AchievedAuthor, got.Targets.Author) {
		t.Errorf("achieved author error equals the target %v on populations where it should be 0; it was assumed, not measured", got.Targets.Author)
	}
	if near(got.AchievedDistractor, got.Targets.Distractor) {
		t.Errorf("achieved distractor error equals the target %v on populations where it should be 0; it was assumed, not measured", got.Targets.Distractor)
	}
}

// A tightest-that-respects-its-target rule must not be read as
// tightest-full-stop. Taking the opposite extreme drives each error to zero and
// collapses the band; taking a looser value than necessary wastes the target.
// Author 1..20 at p_author = 0.05 admits exactly one qualifying value, 20.
func TestThresholdsAreTheTightestValueRespectingTheirTarget(t *testing.T) {
	got := calibrate(t, overlapping())

	// t_high = 20 exactly: 19 would give an author error of 0.10, over target,
	// and 21 is not a value the population contains.
	if !near(got.High, 20) {
		t.Fatalf("t_high = %v, want 20", got.High)
	}
	// t_low = 5 exactly: 6 would give a distractor error of 0.20, over target,
	// and anything below 5 would push the achieved rate to 0 and shrink the
	// `in range` region for nothing.
	if !near(got.Low, 5) {
		t.Fatalf("t_low = %v, want 5", got.Low)
	}
	if got.AchievedDistractor == 0 {
		t.Errorf("achieved distractor error is 0 where the target allows 0.10; the threshold was taken to the wrong extreme")
	}
}

// Thresholds are values the populations actually contained. Section 2 forbids
// boundary randomization, and an interpolated boundary is a number no segment
// produced.
func TestThresholdsAreObservedValues(t *testing.T) {
	got := calibrate(t, overlapping())

	observed := make(map[float64]bool)
	for _, in := range overlapping() {
		observed[in.Distance.Value] = true
	}
	if !observed[got.Low] {
		t.Errorf("t_low = %v is not a value any segment produced", got.Low)
	}
	if !observed[got.High] {
		t.Errorf("t_high = %v is not a value any segment produced", got.High)
	}
}

// Calibration is deterministic: same input, same artifact, every time.
func TestCalibrationIsDeterministic(t *testing.T) {
	first := calibrate(t, overlapping())
	second := calibrate(t, overlapping())

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two calibrations of the same population differ:\n%+v\n%+v", first, second)
	}
}

// The order distances arrive in is not information. A calibration that depended
// on it would produce a different artifact for the same population.
func TestCalibrationIgnoresInputOrder(t *testing.T) {
	forward := overlapping()
	backward := make([]eval.ClassedDistance, len(forward))
	for i := range forward {
		backward[len(forward)-1-i] = forward[i]
	}

	if !reflect.DeepEqual(calibrate(t, forward), calibrate(t, backward)) {
		t.Errorf("input order changed the threshold artifact")
	}
}

// ---------------------------------------------------------------------------
// The sample sizes the targets force
// ---------------------------------------------------------------------------

// At p_author = 0.05 a threshold with a non-zero tail needs twenty author
// distances, and at p_distractor = 0.10 it needs ten distractors. One short of
// either and no qualifying value exists, so the outcome is no thresholds rather
// than a boundary beyond the data. Both sides of both floors are checked.
func TestMinimumSampleSizesAreDerivedFromTheTargets(t *testing.T) {
	t.Run("one author short", func(t *testing.T) {
		in := append(population(eval.ClassAuthor, 1, 19), population(eval.ClassDistractor, 5, 14)...)
		if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrTooFewAuthorDistances) {
			t.Errorf("err = %v, want %v", err, eval.ErrTooFewAuthorDistances)
		}
	})

	t.Run("exactly enough authors", func(t *testing.T) {
		in := append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 5, 14)...)
		if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); err != nil {
			t.Errorf("twenty author distances at a target of 0.05 were refused: %v", err)
		}
	})

	t.Run("one distractor short", func(t *testing.T) {
		in := append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 5, 13)...)
		if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrTooFewDistractorDistances) {
			t.Errorf("err = %v, want %v", err, eval.ErrTooFewDistractorDistances)
		}
	})

	t.Run("exactly enough distractors", func(t *testing.T) {
		in := append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 5, 14)...)
		if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); err != nil {
			t.Errorf("ten distractor distances at a target of 0.10 were refused: %v", err)
		}
	})
}

// The floor follows the target rather than a constant. At a looser p_author of
// 0.25 four author distances suffice; at 0.05 they do not. A hardcoded twenty
// passes the test above and fails this one.
func TestMinimumSampleSizesFollowTheTargets(t *testing.T) {
	loose := eval.Targets{Author: 0.25, Distractor: 0.5}
	in := append(population(eval.ClassAuthor, 1, 4), population(eval.ClassDistractor, 5, 6)...)

	if _, err := eval.Calibrate(in, testSource(), loose); err != nil {
		t.Errorf("four author and two distractor distances at targets of 0.25 and 0.5 were refused: %v", err)
	}
	if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); err == nil {
		t.Errorf("the same population was accepted at the much tighter default targets")
	}
}

// Enough observations is necessary, not sufficient. Twenty identical author
// distances contain no value whose tail is within 0.05, because every candidate
// carries the whole population with it.
func TestADegeneratePopulationHasNoQualifyingThreshold(t *testing.T) {
	identical := make([]eval.ClassedDistance, 0, 20)
	for i := 0; i < 20; i++ {
		identical = append(identical, scored(eval.ClassAuthor, 7))
	}
	in := append(identical, population(eval.ClassDistractor, 30, 39)...)

	if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrNoQualifyingThreshold) {
		t.Errorf("err = %v, want %v", err, eval.ErrNoQualifyingThreshold)
	}
}

// The same degeneracy on the other side, because a guard written for one
// population is easy to leave off the other.
func TestADegenerateDistractorPopulationIsAlsoRefused(t *testing.T) {
	identical := make([]eval.ClassedDistance, 0, 10)
	for i := 0; i < 10; i++ {
		identical = append(identical, scored(eval.ClassDistractor, 7))
	}
	in := append(population(eval.ClassAuthor, 1, 20), identical...)

	if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrNoQualifyingThreshold) {
		t.Errorf("err = %v, want %v", err, eval.ErrNoQualifyingThreshold)
	}
}

// An unscoreable segment carries no number and cannot inform a boundary. It
// counts toward neither population, so twenty author distances of which five are
// undefined is fifteen, and below the floor.
func TestUndefinedDistancesDoNotCountTowardTheMinimums(t *testing.T) {
	in := population(eval.ClassAuthor, 1, 15)
	for i := 0; i < 5; i++ {
		in = append(in, unscored(eval.ClassAuthor))
	}
	in = append(in, population(eval.ClassDistractor, 5, 14)...)

	if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrTooFewAuthorDistances) {
		t.Errorf("err = %v, want %v; five undefined distances were counted as observations", err, eval.ErrTooFewAuthorDistances)
	}
}

// And they do not move a boundary they are excluded from. Adding undefined
// distances to a population that already calibrates must leave the artifact's
// numbers untouched.
func TestUndefinedDistancesDoNotMoveTheThresholds(t *testing.T) {
	base := calibrate(t, overlapping())

	padded := overlapping()
	for i := 0; i < 8; i++ {
		padded = append(padded, unscored(eval.ClassAuthor), unscored(eval.ClassDistractor))
	}
	got := calibrate(t, padded)

	if !near(got.Low, base.Low) || !near(got.High, base.High) {
		t.Errorf("thresholds moved from (%v, %v) to (%v, %v) when undefined distances were added", base.Low, base.High, got.Low, got.High)
	}
	if got.AuthorDistances != base.AuthorDistances || got.DistractorDistances != base.DistractorDistances {
		t.Errorf("counts moved from (%d, %d) to (%d, %d)", base.AuthorDistances, base.DistractorDistances, got.AuthorDistances, got.DistractorDistances)
	}
}

// ---------------------------------------------------------------------------
// Band assignment
// ---------------------------------------------------------------------------

// The three regions on the separated case, where t_low = 20 and t_high = 30.
func TestBandAssignment(t *testing.T) {
	th := calibrate(t, separated())

	cases := []struct {
		name  string
		value float64
		want  eval.Band
	}{
		{name: "far below t_low", value: 1, want: eval.BandInRange},
		{name: "just below t_low", value: 19.5, want: eval.BandInRange},
		{name: "exactly t_low", value: 20, want: eval.BandInRange},
		{name: "between", value: 25, want: eval.BandDrifting},
		{name: "just below t_high", value: 29.5, want: eval.BandDrifting},
		{name: "exactly t_high", value: 30, want: eval.BandNotYou},
		{name: "far above t_high", value: 100, want: eval.BandNotYou},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bandOf(t, th, c.value)
			if !got.Defined {
				t.Fatalf("no band: %v", got.Reason)
			}
			if got.Band != c.want {
				t.Errorf("band = %q, want %q", got.Band, c.want)
			}
			if !near(got.Distance, c.value) {
				t.Errorf("distance = %v, want %v", got.Distance, c.value)
			}
		})
	}
}

// Both boundaries are closed, on the side that names them. A boundary tested
// only from one side leaves the other undefined by omission.
func TestBandBoundariesAreClosed(t *testing.T) {
	th := calibrate(t, separated())

	if got := bandOf(t, th, th.Low); got.Band != eval.BandInRange {
		t.Errorf("a distance exactly at t_low banded %q, want %q", got.Band, eval.BandInRange)
	}
	if got := bandOf(t, th, th.High); got.Band != eval.BandNotYou {
		t.Errorf("a distance exactly at t_high banded %q, want %q", got.Band, eval.BandNotYou)
	}
	if got := bandOf(t, th, math.Nextafter(th.Low, math.Inf(1))); got.Band != eval.BandDrifting {
		t.Errorf("a distance one ulp above t_low banded %q, want %q", got.Band, eval.BandDrifting)
	}
	if got := bandOf(t, th, math.Nextafter(th.High, math.Inf(-1))); got.Band != eval.BandDrifting {
		t.Errorf("a distance one ulp below t_high banded %q, want %q", got.Band, eval.BandDrifting)
	}
}

// When A equals D the two boundaries coincide, `drifting` is empty, and a
// distance sitting exactly on the boundary satisfies both region tests. Section 2
// breaks that tie toward `in range`, away from the more damaging error.
//
// Author 1..20 gives A = 20; distractor 20..29 gives D = 20.
func TestACoincidentBoundaryResolvesToInRange(t *testing.T) {
	th := calibrate(t, append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 20, 29)...))

	if !near(th.Low, 20) || !near(th.High, 20) {
		t.Fatalf("thresholds = (%v, %v), want both 20 for this population", th.Low, th.High)
	}
	if got := bandOf(t, th, 20); got.Band != eval.BandInRange {
		t.Errorf("a distance on a coincident boundary banded %q, want %q", got.Band, eval.BandInRange)
	}
	if got := bandOf(t, th, 19); got.Band != eval.BandInRange {
		t.Errorf("band = %q below a coincident boundary, want %q", got.Band, eval.BandInRange)
	}
	if got := bandOf(t, th, 21); got.Band != eval.BandNotYou {
		t.Errorf("band = %q above a coincident boundary, want %q", got.Band, eval.BandNotYou)
	}
}

// Banding is not a calibration, and it imposes nothing about the split. The
// boundaries are ESTIMATED on Calibrate, but Section 2 reports figures on Test,
// and `score` bands a user's draft, which belongs to no split at all. A Band
// that required Calibrate would make the scoring path unusable.
//
// Added by consensus after the first implementation shared one validator
// between Calibrate and Band: the helper above always set Calibrate, so no test
// could reach this.
func TestBandDoesNotRequireACalibrationSplit(t *testing.T) {
	th := calibrate(t, separated())

	for _, split := range []corpus.Split{corpus.Test, corpus.Train, corpus.Calibrate, ""} {
		name := string(split)
		if name == "" {
			name = "no split at all"
		}
		t.Run(name, func(t *testing.T) {
			in := scored(eval.ClassAuthor, 25).Distance
			in.Split = split

			got, err := th.Band(in)
			if err != nil {
				t.Fatalf("Band refused a %q-split distance: %v", split, err)
			}
			if !got.Defined {
				t.Fatalf("no band: %v", got.Reason)
			}
			if got.Band != eval.BandDrifting {
				t.Errorf("band = %q, want %q", got.Band, eval.BandDrifting)
			}
		})
	}
}

// A segment with no distance gets no band. ADR 0006 requires this: absence of
// measurement is never treated as a result, and `in range` would be the worst
// possible default for something that was never scored.
func TestAnUnscoreableSegmentGetsNoBand(t *testing.T) {
	th := calibrate(t, separated())

	got, err := th.Band(unscored(eval.ClassAuthor).Distance)
	if err != nil {
		t.Fatalf("Band: %v", err)
	}
	if got.Defined {
		t.Fatalf("an insufficient-evidence distance banded as %q", got.Band)
	}
	if got.Band != "" {
		t.Errorf("band = %q, want empty", got.Band)
	}
	if got.Reason != deviation.ReasonInsufficientEvidence {
		t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonInsufficientEvidence)
	}
}

// The band names are asserted against their literal values because they are
// user-facing labels and reach the reported record.
func TestBandNames(t *testing.T) {
	if eval.BandInRange != "in-range" {
		t.Errorf("BandInRange = %q, want %q", eval.BandInRange, "in-range")
	}
	if eval.BandDrifting != "drifting" {
		t.Errorf("BandDrifting = %q, want %q", eval.BandDrifting, "drifting")
	}
	if eval.BandNotYou != "not-you" {
		t.Errorf("BandNotYou = %q, want %q", eval.BandNotYou, "not-you")
	}
}

// ---------------------------------------------------------------------------
// What the artifact is bound to
// ---------------------------------------------------------------------------

// Round 8 established that any proper subset of the manifest's tiers carries its
// own thresholds. The binding is enforced here: a distance scored over a
// different tier subset is not drawn from the distribution these boundaries
// describe, and banding it against them is the same error as comparing two
// distances over different feature sets.
func TestBandRefusesADistanceFromAnotherCalibration(t *testing.T) {
	th := calibrate(t, separated())

	otherTier := scored(eval.ClassAuthor, 25).Distance
	otherTier.ScoredTiers = []features.Tier{"B"}

	otherProfile := scored(eval.ClassAuthor, 25).Distance
	otherProfile.ProfileID = "another-profile"

	otherManifest := scored(eval.ClassAuthor, 25).Distance
	otherManifest.FeatureManifestDigest = "another-digest"

	otherWeighting := scored(eval.ClassAuthor, 25).Distance
	otherWeighting.WeightScheme = "expert-v1"

	otherReference := scored(eval.ClassAuthor, 25).Distance
	otherReference.ReferenceID = "another-reference"

	otherAlgorithm := scored(eval.ClassAuthor, 25).Distance
	otherAlgorithm.Algorithm = "distance-median-v2"

	cases := []struct {
		name string
		in   deviation.Distance
		want error
	}{
		{name: "another tier subset", in: otherTier, want: eval.ErrTierMismatch},
		{name: "another profile", in: otherProfile, want: eval.ErrProfileMismatch},
		{name: "another feature manifest", in: otherManifest, want: eval.ErrManifestMismatch},
		{name: "another weighting scheme", in: otherWeighting, want: eval.ErrWeightingMismatch},
		// The reference is the ECDF every deviation was ranked against, so a
		// distance computed against a different one is on a different scale.
		{name: "another reference", in: otherReference, want: eval.ErrReferenceMismatch},
		{name: "another distance algorithm", in: otherAlgorithm, want: eval.ErrAlgorithmMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := th.Band(c.in); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// The artifact records everything it was calibrated against, so a report can
// name it and a cache cannot serve one calibration for another.
func TestThresholdsCarryTheirProvenance(t *testing.T) {
	got := calibrate(t, separated())

	if got.ProfileID != "profile-under-test" {
		t.Errorf("ProfileID = %q", got.ProfileID)
	}
	if got.FeatureManifestDigest != features.ManifestDigest() {
		t.Errorf("FeatureManifestDigest = %q", got.FeatureManifestDigest)
	}
	if got.WeightScheme != deviation.WeightSchemeUniform {
		t.Errorf("WeightScheme = %q, want %q", got.WeightScheme, deviation.WeightSchemeUniform)
	}
	if got.ReferenceID != "reference-under-test" {
		t.Errorf("ReferenceID = %q", got.ReferenceID)
	}
	if got.DistanceAlgorithm != deviation.DistanceAlgorithm {
		t.Errorf("DistanceAlgorithm = %q, want %q", got.DistanceAlgorithm, deviation.DistanceAlgorithm)
	}
	if want := []features.Tier{features.TierA}; !reflect.DeepEqual(got.ScoredTiers, want) {
		t.Errorf("ScoredTiers = %v, want %v", got.ScoredTiers, want)
	}
	if got.Split != corpus.Calibrate {
		t.Errorf("Split = %q, want %q", got.Split, corpus.Calibrate)
	}
	if got.Targets != eval.DefaultTargets() {
		t.Errorf("Targets = %+v, want %+v", got.Targets, eval.DefaultTargets())
	}
	if got.AuthorDistances != 20 || got.DistractorDistances != 10 {
		t.Errorf("counts = (%d, %d), want (20, 10)", got.AuthorDistances, got.DistractorDistances)
	}
	if got.Source != testSource() {
		t.Errorf("Source = %+v, want %+v", got.Source, testSource())
	}
	if got.Algorithm != eval.ThresholdAlgorithm {
		t.Errorf("Algorithm = %q, want %q", got.Algorithm, eval.ThresholdAlgorithm)
	}
	if eval.ThresholdAlgorithm != "band-ordered-quantile-v1" {
		t.Errorf("ThresholdAlgorithm = %q, want %q", eval.ThresholdAlgorithm, "band-ordered-quantile-v1")
	}
}

// Thresholds are estimated on Calibrate. Train is excluded because the profile
// was fitted there, and Test because reported figures come from Test — a
// boundary fitted on Test contaminates the numbers it is then judged by.
func TestCalibrationAdmitsOnlyTheCalibrateSplit(t *testing.T) {
	for _, split := range []corpus.Split{corpus.Train, corpus.Test} {
		t.Run(string(split), func(t *testing.T) {
			in := separated()
			for i := range in {
				in[i].Distance.Split = split
			}
			if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrCalibrationSplit) {
				t.Errorf("err = %v, want %v", err, eval.ErrCalibrationSplit)
			}
		})
	}

	t.Run("one segment from another split", func(t *testing.T) {
		in := separated()
		in[3].Distance.Split = corpus.Test
		if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrCalibrationSplit) {
			t.Errorf("err = %v, want %v", err, eval.ErrCalibrationSplit)
		}
	})
}

// Every distance in a calibration must come from the same profile, manifest,
// weighting and tier subset. A population assembled from two calibrations
// describes no single distribution.
func TestCalibrationRefusesAMixedPopulation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*deviation.Distance)
		want   error
	}{
		{name: "another profile", mutate: func(d *deviation.Distance) { d.ProfileID = "another-profile" }, want: eval.ErrProfileMismatch},
		{name: "another manifest", mutate: func(d *deviation.Distance) { d.FeatureManifestDigest = "another-digest" }, want: eval.ErrManifestMismatch},
		{name: "another weighting", mutate: func(d *deviation.Distance) { d.WeightScheme = "expert-v1" }, want: eval.ErrWeightingMismatch},
		{name: "another tier subset", mutate: func(d *deviation.Distance) { d.ScoredTiers = []features.Tier{"B"} }, want: eval.ErrTierMismatch},
		{name: "another reference", mutate: func(d *deviation.Distance) { d.ReferenceID = "another-reference" }, want: eval.ErrReferenceMismatch},
		{name: "another distance algorithm", mutate: func(d *deviation.Distance) { d.Algorithm = "distance-median-v2" }, want: eval.ErrAlgorithmMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := separated()
			c.mutate(&in[7].Distance)
			if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The threshold identity
// ---------------------------------------------------------------------------

// A threshold artifact is content-addressed like every other artifact here:
// anything that can change a band changes the ID.
func TestThresholdIdentityChangesWithItsInputs(t *testing.T) {
	base := calibrate(t, separated())

	if same := calibrate(t, separated()); base.ID != same.ID {
		t.Errorf("identical inputs produced different IDs: %q and %q", base.ID, same.ID)
	}

	looser, err := eval.Calibrate(separated(), testSource(), eval.Targets{Author: 0.10, Distractor: 0.20})
	if err != nil {
		t.Fatalf("Calibrate at looser targets: %v", err)
	}
	if looser.ID == base.ID {
		t.Errorf("different error targets produced the same ID %q", base.ID)
	}

	moved := calibrate(t, append(population(eval.ClassAuthor, 1, 20), population(eval.ClassDistractor, 31, 40)...))
	if moved.ID == base.ID {
		t.Errorf("a moved distractor population produced the same ID %q", base.ID)
	}
}

// Numbers alone do not identify a calibration. Two artifacts with identical
// thresholds, identical targets and identical populations are different
// artifacts if anything they were calibrated against differs — otherwise a cache
// serves one author's boundaries for another's, or a pool's for a different
// pool's. Every recorded binding is mutated in turn.
func TestThresholdIdentityCoversEveryBinding(t *testing.T) {
	base := calibrate(t, separated())

	cases := []struct {
		name   string
		mutate func(*deviation.Distance)
	}{
		{name: "the profile", mutate: func(d *deviation.Distance) { d.ProfileID = "another-profile" }},
		{name: "the reference", mutate: func(d *deviation.Distance) { d.ReferenceID = "another-reference" }},
		{name: "the feature manifest", mutate: func(d *deviation.Distance) { d.FeatureManifestDigest = "another-digest" }},
		{name: "the weighting scheme", mutate: func(d *deviation.Distance) { d.WeightScheme = "expert-v1" }},
		{name: "the distance algorithm", mutate: func(d *deviation.Distance) { d.Algorithm = "distance-median-v2" }},
		{name: "the tier subset", mutate: func(d *deviation.Distance) { d.ScoredTiers = []features.Tier{"B"} }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := separated()
			for i := range in {
				c.mutate(&in[i].Distance)
			}
			moved, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
			if err != nil {
				t.Fatalf("Calibrate: %v", err)
			}
			if !near(moved.Low, base.Low) || !near(moved.High, base.High) {
				t.Fatalf("this case must change only the binding, but the thresholds moved from (%v, %v) to (%v, %v)", base.Low, base.High, moved.Low, moved.High)
			}
			if moved.ID == base.ID {
				t.Errorf("changing %s left the ID at %q", c.name, base.ID)
			}
		})
	}

	sources := []struct {
		name string
		in   eval.Source
	}{
		{name: "the distractor pool", in: eval.Source{Cohort: testSource().Cohort, DistractorPool: "another-distractor-pool"}},
		{name: "the calibration cohort", in: eval.Source{Cohort: "another-cohort", DistractorPool: testSource().DistractorPool}},
	}
	for _, c := range sources {
		t.Run(c.name, func(t *testing.T) {
			moved, err := eval.Calibrate(separated(), c.in, eval.DefaultTargets())
			if err != nil {
				t.Fatalf("Calibrate: %v", err)
			}
			if !near(moved.Low, base.Low) || !near(moved.High, base.High) {
				t.Fatalf("the thresholds moved; this case must change only %s", c.name)
			}
			if moved.ID == base.ID {
				t.Errorf("changing %s left the ID at %q", c.name, base.ID)
			}
		})
	}

	// Two valid target pairs that select the SAME observed bounds. On author
	// 1..20 and distractor 30..39, both (0.05, 0.10) and (0.06, 0.15) pick
	// A = 20 and D = 30, and both achieved rates are zero either way. Only the
	// declared targets differ — so an identity that omitted them would collide
	// here, where the earlier looser-targets case cannot show it because that
	// one also moves the numbers.
	t.Run("the targets alone", func(t *testing.T) {
		other, err := eval.Calibrate(separated(), testSource(), eval.Targets{Author: 0.06, Distractor: 0.15})
		if err != nil {
			t.Fatalf("Calibrate: %v", err)
		}
		if !near(other.Low, base.Low) || !near(other.High, base.High) {
			t.Fatalf("thresholds moved from (%v, %v) to (%v, %v); this case needs them identical", base.Low, base.High, other.Low, other.High)
		}
		if !near(other.AchievedAuthor, base.AchievedAuthor) || !near(other.AchievedDistractor, base.AchievedDistractor) {
			t.Fatalf("achieved rates moved; this case needs them identical")
		}
		if other.ID == base.ID {
			t.Errorf("two different declared target pairs producing identical bounds share the ID %q", base.ID)
		}
	})

	// The populations themselves are in the identity, not merely the bounds they
	// produced. One author distance changed from 5 to 6 leaves A = 20 and every
	// reported number untouched, and is still different evidence.
	t.Run("the population behind identical bounds", func(t *testing.T) {
		altered := separated()
		altered[4].Distance.Value = 6
		other, err := eval.Calibrate(altered, testSource(), eval.DefaultTargets())
		if err != nil {
			t.Fatalf("Calibrate: %v", err)
		}
		if !near(other.Low, base.Low) || !near(other.High, base.High) {
			t.Fatalf("thresholds moved; this case needs them identical")
		}
		if !near(other.AchievedAuthor, base.AchievedAuthor) || !near(other.AchievedDistractor, base.AchievedDistractor) {
			t.Fatalf("achieved rates moved from (%v, %v) to (%v, %v); this case needs every reported number identical", base.AchievedAuthor, base.AchievedDistractor, other.AchievedAuthor, other.AchievedDistractor)
		}
		if other.AuthorDistances != base.AuthorDistances || other.DistractorDistances != base.DistractorDistances {
			t.Fatalf("counts moved; this case needs every reported number identical")
		}
		if other.ID == base.ID {
			t.Errorf("two different populations producing identical bounds share the ID %q", base.ID)
		}
	})
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

func TestCalibrateRefusesBadInput(t *testing.T) {
	cases := []struct {
		name    string
		in      []eval.ClassedDistance
		targets eval.Targets
		want    error
	}{
		{name: "no distances", in: nil, targets: eval.DefaultTargets(), want: eval.ErrMissingInput},
		{name: "no author distances", in: population(eval.ClassDistractor, 5, 14), targets: eval.DefaultTargets(), want: eval.ErrTooFewAuthorDistances},
		{name: "no distractor distances", in: population(eval.ClassAuthor, 1, 20), targets: eval.DefaultTargets(), want: eval.ErrTooFewDistractorDistances},
		// Each bound is checked on both targets: a guard written for one is
		// easy to leave off the other, and both must lie strictly in (0, 1).
		{name: "a zero author target", in: separated(), targets: eval.Targets{Author: 0, Distractor: 0.10}, want: eval.ErrInvalidTargets},
		{name: "a zero distractor target", in: separated(), targets: eval.Targets{Author: 0.05, Distractor: 0}, want: eval.ErrInvalidTargets},
		{name: "a negative author target", in: separated(), targets: eval.Targets{Author: -0.05, Distractor: 0.10}, want: eval.ErrInvalidTargets},
		{name: "a negative distractor target", in: separated(), targets: eval.Targets{Author: 0.05, Distractor: -0.10}, want: eval.ErrInvalidTargets},
		{name: "an author target of one", in: separated(), targets: eval.Targets{Author: 1, Distractor: 0.10}, want: eval.ErrInvalidTargets},
		{name: "a distractor target of one", in: separated(), targets: eval.Targets{Author: 0.05, Distractor: 1}, want: eval.ErrInvalidTargets},
		{name: "an author target above one", in: separated(), targets: eval.Targets{Author: 1.5, Distractor: 0.10}, want: eval.ErrInvalidTargets},
		{name: "a distractor target above one", in: separated(), targets: eval.Targets{Author: 0.05, Distractor: 1.5}, want: eval.ErrInvalidTargets},
		{name: "a NaN target", in: separated(), targets: eval.Targets{Author: math.NaN(), Distractor: 0.10}, want: eval.ErrInvalidTargets},
		{name: "an unknown class", in: append(separated(), eval.ClassedDistance{Class: "witness", Distance: scored(eval.ClassAuthor, 5).Distance}), targets: eval.DefaultTargets(), want: eval.ErrUnknownClass},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := eval.Calibrate(c.in, testSource(), c.targets); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}

	// Both halves of the source are required, and each is checked: a guard
	// written for one is easy to leave off the other.
	t.Run("an unnamed calibration cohort", func(t *testing.T) {
		if _, err := eval.Calibrate(separated(), eval.Source{DistractorPool: testSource().DistractorPool}, eval.DefaultTargets()); !errors.Is(err, eval.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, eval.ErrMissingInput)
		}
	})

	t.Run("an unnamed distractor pool", func(t *testing.T) {
		if _, err := eval.Calibrate(separated(), eval.Source{Cohort: testSource().Cohort}, eval.DefaultTargets()); !errors.Is(err, eval.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, eval.ErrMissingInput)
		}
	})
}

// The same non-finite rule as everywhere else: a defined distance that is NaN or
// infinite is a corrupt artifact, and one NaN in a sorted population silently
// destroys the ordering every quantile depends on.
func TestCalibrateRefusesNonFiniteDistances(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		in := separated()
		in[4].Distance.Value = bad
		if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrMalformedInput) {
			t.Errorf("value %v: err = %v, want %v", bad, err, eval.ErrMalformedInput)
		}
	}
}

// A negative distance cannot arise from a mean of absolute deviations, so one
// appearing in a persisted population is corruption rather than an extreme.
func TestCalibrateRefusesANegativeDistance(t *testing.T) {
	in := separated()
	in[4].Distance.Value = -1

	if _, err := eval.Calibrate(in, testSource(), eval.DefaultTargets()); !errors.Is(err, eval.ErrMalformedInput) {
		t.Errorf("err = %v, want %v", err, eval.ErrMalformedInput)
	}
}

func TestBandRefusesBadInput(t *testing.T) {
	th := calibrate(t, separated())

	malformed := func(v float64) deviation.Distance {
		out := scored(eval.ClassAuthor, 25).Distance
		out.Value = v
		return out
	}

	cases := []struct {
		name string
		in   deviation.Distance
		want error
	}{
		{name: "a NaN distance", in: malformed(math.NaN()), want: eval.ErrMalformedInput},
		{name: "a positive infinity", in: malformed(math.Inf(1)), want: eval.ErrMalformedInput},
		{name: "a negative infinity", in: malformed(math.Inf(-1)), want: eval.ErrMalformedInput},
		{name: "a negative distance", in: malformed(-1), want: eval.ErrMalformedInput},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := th.Band(c.in); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}

	t.Run("a nil artifact", func(t *testing.T) {
		var none *eval.Thresholds
		if _, err := none.Band(scored(eval.ClassAuthor, 25).Distance); !errors.Is(err, eval.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, eval.ErrMissingInput)
		}
	})
}
