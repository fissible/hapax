package eval_test

// The band calibration floor: ADR 0005's second release gate.
//
// A band is a claim, and this is where the claim is checked against held-out
// evidence. REVIEW Round 11 had to give the check something to do first — the
// design said a band's "observed rate must fall inside its declared confidence
// interval", which is vacuous, since a point estimate always lies inside an
// interval computed from it.
//
// # Only two bands claim anything
//
//	in range  says THIS IS THE AUTHOR, so its error is a DISTRACTOR landing
//	          there, and its target is p_distractor
//	not you   says the opposite, so its error is the AUTHOR landing there, and
//	          its target is p_author
//	drifting  asserts nothing, needs no evidence, and is the fallback
//
// A failed claiming band collapses into drifting. When both fail, everything
// would land in the band that means nothing, so the profile reports
// uncalibrated rather than dressing an absence as a result.
//
// # The rate is class-conditional, and measured on Test
//
// The gated quantity is P(distractor lands in range) over ALL held-out
// distractors — the same quantity the threshold target bounds, re-measured out
// of sample. Not the band's composition, which depends on how many distractors
// the user happened to supply and is a property of the pool rather than of the
// method.
//
// Thresholds are fitted on Calibrate to hit their targets there, so re-measuring
// on Calibrate would ask whether the fit fits. Test asks whether it holds.
//
// # The bound, and why it has a floor
//
// A bound rather than a point estimate: an observed zero on a handful of
// segments is not evidence of a small rate. It comes from the same clustered
// bootstrap as the threshold intervals, one-sided at 0.95.
//
// But a bootstrap degenerates exactly where this gate lives. The good case is a
// rate observed as zero, which resamples to zero every time — so the bootstrap
// reports an upper bound of 0 for a band that has merely never been wrong yet.
// That is the most over-confident answer available, and worst where the evidence
// is thinnest.
//
// So the bound is the GREATER of the bootstrap percentile and 3/c, where c is
// the number of CLUSTERS in the class whose error is bounded. A declared
// conservatism, not a theorem: never claim a tighter bound than a perfect sample
// of this many independent units could support.
//
// The denominator is clusters and not segments, and that is not cosmetic. A
// hundred error-free segments from one document are one independent observation,
// not a hundred, so 3/100 would claim 0.03 from evidence supporting nothing of
// the sort — and would smuggle back the independence assumption the clustered
// bootstrap exists to avoid, immediately after the bootstrap had avoided it.
//
// # Which makes the minimum count a consequence, not a second gate
//
// Since the bound is at least 3/c, a target of p cannot be cleared below
// ceil(3/p) CLUSTERS — 60 held-out author documents for `not you` and 30
// distractor clusters for `in range` at the v1 targets. That is a real cost and
// it is stated rather than softened: a band is a claim about an error rate, and
// there is no sample size below this at which such a claim can be made.
//
// # The calibration classifies; the thresholds only measure
//
// Thresholds answer a geometric question — which side of the boundaries a
// distance falls on. The calibration answers the one that matters: which label
// may be emitted. A consumer left to read the band reports and apply the
// thresholds itself could emit a label the gate refused, which is the outcome
// the whole gate exists to prevent.
//
// # What this slice is not
//
// The AUC discrimination gate is separate. This slice judges bands; it makes no
// claim that the profile discriminates.

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
)

// ---------------------------------------------------------------------------
// Populations
// ---------------------------------------------------------------------------

// held builds a Test-split distance. The gate runs on Test; the thresholds it
// judges were fitted on Calibrate.
func held(class eval.Class, value float64) eval.ClassedDistance {
	out := scored(class, value)
	out.Distance.Split = corpus.Test
	return out
}

func heldOut(class eval.Class, values []float64, documents int) []eval.ClassedDistance {
	out := make([]eval.ClassedDistance, 0, len(values))
	for i, v := range values {
		in := held(class, v)
		in.Document = label("doc", i%documents)
		out = append(out, in)
	}
	return out
}

// perDocument gives every segment its own document, so the cluster count equals
// the segment count.
func perDocument(class eval.Class, values []float64) []eval.ClassedDistance {
	return heldOut(class, values, len(values))
}

func joinSpans(spans ...[]float64) []float64 {
	out := []float64{}
	for _, s := range spans {
		out = append(out, s...)
	}
	return out
}

// thresholds are always the ones calibrated on the comfortable population:
// t_low = 96 and t_high = 205.
func calibrated(t *testing.T) *eval.Thresholds {
	t.Helper()
	th, err := eval.Calibrate(comfortable(), testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if th.Low != 96 || th.High != 205 {
		t.Fatalf("thresholds = (%v, %v), want (96, 205)", th.Low, th.High)
	}
	return th
}

// Every fixture gives each segment its own document, so the cluster count and
// the segment count coincide and the arithmetic below can be read directly.
// t_low is 96 and t_high is 205 throughout.

// clean has eighty author documents and forty distractor clusters, none of them
// ever wrong. Both bands are decided by the floor: 3/80 = 0.0375 against a
// target of 0.05, and 3/40 = 0.075 against 0.10.
func clean() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 80)),
		perDocument(eval.ClassDistractor, span(201, 240))...,
	)
}

// atTheMinimum sits both classes exactly on their derived cluster minimums:
// 3/60 = 0.05 and 3/30 = 0.10, each equal to its target. Both must be emitted,
// because the comparison is inclusive.
func atTheMinimum() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 60)),
		perDocument(eval.ClassDistractor, span(201, 230))...,
	)
}

// oneShort is one cluster below each minimum: 3/59 = 0.0508 and 3/29 = 0.1034,
// both just over target however clean the observation.
func oneShort() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 59)),
		perDocument(eval.ClassDistractor, span(201, 229))...,
	)
}

// leakyInRange puts eight of forty distractor clusters below t_low: an observed
// error of 0.20 and a bootstrap bound of 0.30, well above the 0.075 floor, so
// this case is decided by the bootstrap.
func leakyInRange() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 80)),
		perDocument(eval.ClassDistractor, joinSpans(span(50, 57), span(201, 232)))...,
	)
}

// leakyNotYou puts eight of eighty author clusters above t_high: an observed
// error of 0.10 and a bound of 0.1625, again above the floor.
func leakyNotYou() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, joinSpans(span(1, 72), span(300, 307))),
		perDocument(eval.ClassDistractor, span(201, 240))...,
	)
}

func bothLeak() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, joinSpans(span(1, 72), span(300, 307))),
		perDocument(eval.ClassDistractor, joinSpans(span(50, 57), span(201, 232)))...,
	)
}

func gateOf(t *testing.T, in []eval.ClassedDistance) eval.Calibration {
	t.Helper()
	got, err := calibrated(t).CalibrateBands(in, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	return got
}

func reportOf(t *testing.T, c eval.Calibration, band eval.Band) eval.BandReport {
	t.Helper()
	for _, got := range c.Bands {
		if got.Band == band {
			return got
		}
	}
	t.Fatalf("calibration has no report for band %q", band)
	return eval.BandReport{}
}

// ---------------------------------------------------------------------------
// The declared floor
// ---------------------------------------------------------------------------

func TestDeclaredBandFloor(t *testing.T) {
	got := eval.DefaultBandFloor()

	if got.Confidence != 0.95 {
		t.Errorf("confidence = %v, want 0.95", got.Confidence)
	}
	if got.Resamples != 2000 {
		t.Errorf("resamples = %d, want 2000", got.Resamples)
	}
	if got.Seed != 0x68617061785F7631 {
		t.Errorf("seed = %#x, want the declared %#x", got.Seed, uint64(0x68617061785F7631))
	}
	if eval.BandCalibrationAlgorithm != "band-error-bound-v1" {
		t.Errorf("BandCalibrationAlgorithm = %q", eval.BandCalibrationAlgorithm)
	}
}

// ---------------------------------------------------------------------------
// What each band claims
// ---------------------------------------------------------------------------

// Each claiming band is gated on the error of ITS OWN claim, against the target
// its threshold was built to respect. Getting these crossed would gate `in
// range` on the author rate, which its threshold never bounded.
func TestEachClaimingBandIsGatedOnItsOwnError(t *testing.T) {
	got := gateOf(t, clean())

	inRange := reportOf(t, got, eval.BandInRange)
	if inRange.Claims != eval.ClassDistractor {
		t.Errorf("in-range bounds the error of class %q, want %q", inRange.Claims, eval.ClassDistractor)
	}
	if inRange.Target != eval.DefaultTargets().Distractor {
		t.Errorf("in-range target = %v, want p_distractor %v", inRange.Target, eval.DefaultTargets().Distractor)
	}

	notYou := reportOf(t, got, eval.BandNotYou)
	if notYou.Claims != eval.ClassAuthor {
		t.Errorf("not-you bounds the error of class %q, want %q", notYou.Claims, eval.ClassAuthor)
	}
	if notYou.Target != eval.DefaultTargets().Author {
		t.Errorf("not-you target = %v, want p_author %v", notYou.Target, eval.DefaultTargets().Author)
	}
}

// drifting asserts nothing about authorship, so it has no error to bound and no
// evidence that could back it. It is the fallback, always available, and never
// refused.
func TestDriftingIsTheFallbackAndIsNotGated(t *testing.T) {
	for _, in := range [][]eval.ClassedDistance{clean(), leakyInRange(), leakyNotYou(), bothLeak()} {
		got := gateOf(t, in)
		drifting := reportOf(t, got, eval.BandDrifting)

		if !drifting.Emitted {
			t.Errorf("drifting was refused: %v", drifting.Reason)
		}
		if drifting.Claims != "" {
			t.Errorf("drifting claims the error of class %q; it claims nothing", drifting.Claims)
		}
		if drifting.Target != 0 || drifting.ErrorBound != 0 {
			t.Errorf("drifting carries a target of %v and a bound of %v; it has neither", drifting.Target, drifting.ErrorBound)
		}
	}
}

// Every band is reported whether or not it was emitted, in a stable order, so a
// reader can see why a label is missing rather than finding it absent.
func TestEveryBandIsReported(t *testing.T) {
	got := gateOf(t, bothLeak())

	order := make([]eval.Band, 0, len(got.Bands))
	for _, b := range got.Bands {
		order = append(order, b.Band)
	}
	want := []eval.Band{eval.BandInRange, eval.BandDrifting, eval.BandNotYou}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("bands = %v, want %v in ascending distance order", order, want)
	}
}

// ---------------------------------------------------------------------------
// The bound, and its floor
// ---------------------------------------------------------------------------

// Both halves of the bound are exercised here, which is why the exact values
// matter.
//
// On `clean` neither claiming band has ever been wrong, so every resample gives
// a rate of zero and the bootstrap percentile is 0. The reported bound is the
// rule-of-three floor instead: 3/80 = 0.0375 for the eighty author clusters and
// 3/40 = 0.075 for the forty distractor clusters. An implementation using the
// bootstrap alone reports 0 twice and passes every emission test in this file —
// this is the assertion that catches it.
func TestTheBoundIsFlooredAtTheRuleOfThree(t *testing.T) {
	got := gateOf(t, clean())

	notYou := reportOf(t, got, eval.BandNotYou)
	if notYou.ErrorRate != 0 {
		t.Fatalf("author error rate = %v on a clean population, want 0", notYou.ErrorRate)
	}
	if notYou.ErrorBound != 0.0375 {
		t.Errorf("author error bound = %v, want 3/80 = 0.0375; a bare bootstrap would report 0", notYou.ErrorBound)
	}

	inRange := reportOf(t, got, eval.BandInRange)
	if inRange.ErrorRate != 0 {
		t.Fatalf("distractor error rate = %v on a clean population, want 0", inRange.ErrorRate)
	}
	if inRange.ErrorBound != 0.075 {
		t.Errorf("distractor error bound = %v, want 3/40 = 0.075; a bare bootstrap would report 0", inRange.ErrorBound)
	}
}

// And the other half: where errors are observed, the bootstrap percentile
// exceeds the rule-of-three floor and is what the bound reports. An
// implementation using only the floor reports 0.0375 and 0.075 here too, and
// emits both bands.
func TestTheBoundIsTheBootstrapWhereItExceedsTheFloor(t *testing.T) {
	t.Run("in-range", func(t *testing.T) {
		got := reportOf(t, gateOf(t, leakyInRange()), eval.BandInRange)
		if got.ErrorRate != 0.20 {
			t.Fatalf("distractor error rate = %v, want 8/40 = 0.20", got.ErrorRate)
		}
		if got.ErrorBound != 0.30 {
			t.Errorf("distractor error bound = %v, want the bootstrap's 0.30", got.ErrorBound)
		}
		if got.ErrorBound <= 3.0/40.0 {
			t.Errorf("the bound %v did not exceed the rule-of-three floor; this case must be decided by the bootstrap", got.ErrorBound)
		}
	})

	t.Run("not-you", func(t *testing.T) {
		got := reportOf(t, gateOf(t, leakyNotYou()), eval.BandNotYou)
		if got.ErrorRate != 0.10 {
			t.Fatalf("author error rate = %v, want 8/80 = 0.10", got.ErrorRate)
		}
		if got.ErrorBound != 0.1625 {
			t.Errorf("author error bound = %v, want the bootstrap's 0.1625", got.ErrorBound)
		}
		if got.ErrorBound <= 3.0/80.0 {
			t.Errorf("the bound %v did not exceed the rule-of-three floor; this case must be decided by the bootstrap", got.ErrorBound)
		}
	})
}

// The bound is never below the observed rate. A bound under its own point
// estimate is not a bound.
func TestTheBoundIsNeverBelowTheObservedRate(t *testing.T) {
	for _, in := range [][]eval.ClassedDistance{clean(), oneShort(), leakyInRange(), leakyNotYou(), bothLeak()} {
		got := gateOf(t, in)
		for _, b := range got.Bands {
			if b.Claims == "" {
				continue
			}
			if b.ErrorBound < b.ErrorRate {
				t.Errorf("%s: bound %v is below the observed rate %v", b.Band, b.ErrorBound, b.ErrorRate)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

func TestBandEmission(t *testing.T) {
	cases := []struct {
		name            string
		in              []eval.ClassedDistance
		inRange, notYou bool
		calibrated      bool
	}{
		{name: "a clean population emits both", in: clean(), inRange: true, notYou: true, calibrated: true},
		{name: "distractors leaking into in-range", in: leakyInRange(), inRange: false, notYou: true, calibrated: true},
		{name: "authors leaking into not-you", in: leakyNotYou(), inRange: true, notYou: false, calibrated: true},
		{name: "both leaking", in: bothLeak(), inRange: false, notYou: false, calibrated: false},
		{name: "one cluster short on both sides", in: oneShort(), inRange: false, notYou: false, calibrated: false},
		{name: "exactly at both minimums", in: atTheMinimum(), inRange: true, notYou: true, calibrated: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := gateOf(t, c.in)

			if inRange := reportOf(t, got, eval.BandInRange); inRange.Emitted != c.inRange {
				t.Errorf("in-range emitted = %v (bound %v against target %v), want %v",
					inRange.Emitted, inRange.ErrorBound, inRange.Target, c.inRange)
			}
			if notYou := reportOf(t, got, eval.BandNotYou); notYou.Emitted != c.notYou {
				t.Errorf("not-you emitted = %v (bound %v against target %v), want %v",
					notYou.Emitted, notYou.ErrorBound, notYou.Target, c.notYou)
			}
			if got.Calibrated != c.calibrated {
				t.Errorf("calibrated = %v, want %v", got.Calibrated, c.calibrated)
			}
		})
	}
}

// A refused band says why, and an emitted one does not invent a reason.
func TestRefusedBandsStateAReason(t *testing.T) {
	failed := reportOf(t, gateOf(t, leakyInRange()), eval.BandInRange)
	if failed.Reason == "" {
		t.Errorf("a refused band states no reason")
	}

	passed := reportOf(t, gateOf(t, clean()), eval.BandNotYou)
	if passed.Reason != "" {
		t.Errorf("an emitted band carries the reason %q", passed.Reason)
	}
}

// Both claiming bands failing is not a band set with one member; it is the
// absence of a band set. Reporting it as `drifting everywhere` would dress an
// absence of evidence as a result, which is what ADR 0006 refuses.
func TestBothClaimingBandsFailingIsUncalibrated(t *testing.T) {
	got := gateOf(t, bothLeak())

	if got.Calibrated {
		t.Fatalf("calibrated with neither claiming band emitted")
	}
	if got.Reason == "" {
		t.Errorf("an uncalibrated result states no reason")
	}
	// drifting is still reported as available; it is the band that would be
	// applied, and saying so is not the same as calling the profile calibrated.
	if drifting := reportOf(t, got, eval.BandDrifting); !drifting.Emitted {
		t.Errorf("drifting is not emitted even as the fallback")
	}
}

// The floor at which a band flips, from both sides and on both classes. This is
// where the derived minimum actually lives: it is not a separate check, it is
// the point at which 3/c crosses the target.
func TestTheDerivedMinimumIsWhereTheBoundCrossesTheTarget(t *testing.T) {
	th := calibrated(t)

	at, err := th.CalibrateBands(atTheMinimum(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	inRange := reportOf(t, at, eval.BandInRange)
	if inRange.ClassClusters != 30 {
		t.Fatalf("in-range class clusters = %d, want 30", inRange.ClassClusters)
	}
	if inRange.ErrorBound != 0.10 {
		t.Errorf("in-range bound at thirty clusters = %v, want exactly 3/30 = 0.10", inRange.ErrorBound)
	}
	if !inRange.Emitted {
		t.Errorf("an in-range bound sitting exactly on its target was refused")
	}
	notYou := reportOf(t, at, eval.BandNotYou)
	if notYou.ClassClusters != 60 {
		t.Fatalf("not-you class clusters = %d, want 60", notYou.ClassClusters)
	}
	if notYou.ErrorBound != 0.05 {
		t.Errorf("not-you bound at sixty clusters = %v, want exactly 3/60 = 0.05", notYou.ErrorBound)
	}
	if !notYou.Emitted {
		t.Errorf("a not-you bound sitting exactly on its target was refused")
	}

	short, err := th.CalibrateBands(oneShort(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	if got := reportOf(t, short, eval.BandInRange); got.Emitted {
		t.Errorf("twenty-nine distractor clusters cleared 0.10 with a bound of %v", got.ErrorBound)
	}
	if got := reportOf(t, short, eval.BandNotYou); got.Emitted {
		t.Errorf("fifty-nine author clusters cleared 0.05 with a bound of %v", got.ErrorBound)
	}
}

// The denominator is the CLUSTER count, not the segment count, and the two are
// deliberately separated here. Eighty author segments in four documents is four
// independent observations, so the floor is 3/4 = 0.75, not 3/80 = 0.0375. An
// implementation dividing by segments emits the band; the design's whole reason
// for resampling clusters is that it must not.
func TestTheFloorCountsClustersNotSegments(t *testing.T) {
	th := calibrated(t)

	crowded := append(
		heldOut(eval.ClassAuthor, span(1, 80), 4),
		perDocument(eval.ClassDistractor, span(201, 240))...,
	)
	got, err := th.CalibrateBands(crowded, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}

	notYou := reportOf(t, got, eval.BandNotYou)
	if notYou.ClassSegments != 80 {
		t.Fatalf("class segments = %d, want 80", notYou.ClassSegments)
	}
	if notYou.ClassClusters != 4 {
		t.Fatalf("class clusters = %d, want 4", notYou.ClassClusters)
	}
	if notYou.ErrorBound == 0.0375 {
		t.Fatalf("bound = %v, which is 3/80; the floor divided by segments", notYou.ErrorBound)
	}
	if notYou.ErrorBound != 0.75 {
		t.Errorf("bound = %v, want 3/4 = 0.75", notYou.ErrorBound)
	}
	if notYou.Emitted {
		t.Errorf("not-you was emitted on four independent documents")
	}
}

// The derived minimum is reported so a user is told how much more writing a band
// needs, even though the bound is what decides.
func TestTheDerivedMinimumIsReported(t *testing.T) {
	got := gateOf(t, clean())

	if inRange := reportOf(t, got, eval.BandInRange); inRange.MinClassClusters != 30 {
		t.Errorf("in-range minimum = %d clusters, want ceil(3/0.10) = 30", inRange.MinClassClusters)
	}
	if notYou := reportOf(t, got, eval.BandNotYou); notYou.MinClassClusters != 60 {
		t.Errorf("not-you minimum = %d clusters, want ceil(3/0.05) = 60", notYou.MinClassClusters)
	}
}

// ---------------------------------------------------------------------------
// The rate is class-conditional, not the band's composition
// ---------------------------------------------------------------------------

// The gated quantity is the share of the CLASS that lands in the band, not the
// share of the BAND that came from the class. The two differ whenever the pool
// sizes differ, and only the first is a property of the method.
//
// Here the distractor pool is doubled while every distractor stays above t_high.
// The class-conditional error stays 0 and the class denominator doubles, so the
// bound falls. A composition-based rate would move with the pool size instead.
func TestTheRateIsClassConditional(t *testing.T) {
	th := calibrated(t)

	doubled := append(
		perDocument(eval.ClassAuthor, span(1, 80)),
		perDocument(eval.ClassDistractor, span(201, 280))...,
	)
	got, err := th.CalibrateBands(doubled, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}

	inRange := reportOf(t, got, eval.BandInRange)
	if inRange.ClassSegments != 80 {
		t.Errorf("class segments = %d, want 80 — the whole distractor class, not the band's occupancy", inRange.ClassSegments)
	}
	if inRange.ErrorRate != 0 {
		t.Errorf("distractor error rate = %v, want 0", inRange.ErrorRate)
	}
	if inRange.ErrorBound != 0.0375 {
		t.Errorf("bound = %v, want 3/80 = 0.0375", inRange.ErrorBound)
	}
}

// Occupancy is reported for both classes, because it tells a reader whether a
// label is ever reached — but a band is not refused for being unvisited, since
// occupancy by the other class is not evidence about the rate the band claims.
func TestOccupancyIsReportedAndNotGatedOn(t *testing.T) {
	got := gateOf(t, clean())

	// t_low is 96 and t_high is 205, so on the clean population in-range holds
	// all eighty authors and no distractors, distractors 201..204 drift, and
	// not-you holds the thirty-six distractors from 205 up.
	inRange := reportOf(t, got, eval.BandInRange)
	if inRange.AuthorSegments != 80 || inRange.DistractorSegments != 0 {
		t.Errorf("in-range occupancy = %d authors and %d distractors, want 80 and 0",
			inRange.AuthorSegments, inRange.DistractorSegments)
	}
	if !inRange.Emitted {
		t.Fatalf("in-range was refused: %v", inRange.Reason)
	}

	notYou := reportOf(t, got, eval.BandNotYou)
	if notYou.AuthorSegments != 0 || notYou.DistractorSegments != 36 {
		t.Errorf("not-you occupancy = %d authors and %d distractors, want 0 and 36",
			notYou.AuthorSegments, notYou.DistractorSegments)
	}
	// Emitted despite holding no author segments at all: its claim is about the
	// author rate, and zero authors landing there is the best possible evidence
	// for it, not an absence of evidence.
	if !notYou.Emitted {
		t.Errorf("not-you was refused for holding no author segments: %v", notYou.Reason)
	}
}

// ---------------------------------------------------------------------------
// Provenance and identity
// ---------------------------------------------------------------------------

func TestCalibrationCarriesItsProvenance(t *testing.T) {
	th := calibrated(t)
	got, err := th.CalibrateBands(clean(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}

	if got.ThresholdsID != th.ID {
		t.Errorf("ThresholdsID = %q, want %q", got.ThresholdsID, th.ID)
	}
	// ADR 0005 requires every result to name what produced it. A threshold ID is
	// a pointer to that provenance, not the provenance itself: a report has to be
	// readable without resolving it.
	if got.ProfileID != th.ProfileID {
		t.Errorf("ProfileID = %q, want %q", got.ProfileID, th.ProfileID)
	}
	if got.ReferenceID != th.ReferenceID {
		t.Errorf("ReferenceID = %q, want %q", got.ReferenceID, th.ReferenceID)
	}
	if got.FeatureManifestDigest != th.FeatureManifestDigest {
		t.Errorf("FeatureManifestDigest = %q, want %q", got.FeatureManifestDigest, th.FeatureManifestDigest)
	}
	if got.Algorithm != eval.BandCalibrationAlgorithm {
		t.Errorf("Algorithm = %q, want %q", got.Algorithm, eval.BandCalibrationAlgorithm)
	}
	if got.Split != corpus.Test {
		t.Errorf("Split = %q, want %q", got.Split, corpus.Test)
	}
	if got.Floor != eval.DefaultBandFloor() {
		t.Errorf("Floor = %+v, want %+v", got.Floor, eval.DefaultBandFloor())
	}
}

func TestCalibrationIsDeterministicAndOrderIndependent(t *testing.T) {
	base := gateOf(t, clean())

	if again := gateOf(t, clean()); !reflect.DeepEqual(base, again) {
		t.Errorf("two runs over the same population differ")
	}

	forward := clean()
	reversed := make([]eval.ClassedDistance, len(forward))
	for i := range forward {
		reversed[len(forward)-1-i] = forward[i]
	}
	if got := gateOf(t, reversed); !reflect.DeepEqual(base, got) {
		t.Errorf("reversing the population changed the calibration")
	}
}

// Anything that can change a band's fate changes the ID, including the cluster
// partition — the same collision the interval slice had to close, for the same
// reason: the bound comes from resampling clusters, so two partitions of one
// population are different evidence.
func TestCalibrationIdentityCoversItsInputs(t *testing.T) {
	th := calibrated(t)
	base, err := th.CalibrateBands(clean(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}

	t.Run("a changed population", func(t *testing.T) {
		moved, err := th.CalibrateBands(leakyInRange(), eval.DefaultBandFloor())
		if err != nil {
			t.Fatalf("CalibrateBands: %v", err)
		}
		if moved.ID == base.ID {
			t.Errorf("a different population produced the same ID %q", base.ID)
		}
	})

	// Every declared parameter, not only the one that is easiest to vary. The
	// seed and the resample count do not move the bound on this population, so
	// the identity is the only place their effect is visible — which makes it
	// the only place they can be checked.
	floors := []struct {
		name   string
		mutate func(*eval.BandFloor)
	}{
		{name: "a changed seed", mutate: func(f *eval.BandFloor) { f.Seed = 99 }},
		{name: "a changed resample count", mutate: func(f *eval.BandFloor) { f.Resamples = 500 }},
		{name: "a changed confidence", mutate: func(f *eval.BandFloor) { f.Confidence = 0.99 }},
	}
	for _, c := range floors {
		t.Run(c.name, func(t *testing.T) {
			floor := eval.DefaultBandFloor()
			c.mutate(&floor)
			moved, err := th.CalibrateBands(clean(), floor)
			if err != nil {
				t.Fatalf("CalibrateBands: %v", err)
			}
			if moved.ID == base.ID {
				t.Errorf("%s produced the same ID %q", c.name, base.ID)
			}
		})
	}

	// The same eighty author segments over forty documents, grouped round-robin
	// and grouped as contiguous pairs. Same distances, same cluster count, same
	// bounds — both are decided by the 3/40 floor — and different evidence, so
	// different artifacts.
	t.Run("a changed cluster partition", func(t *testing.T) {
		roundRobin := append(
			heldOut(eval.ClassAuthor, span(1, 80), 40),
			perDocument(eval.ClassDistractor, span(201, 240))...,
		)
		contiguous := make([]eval.ClassedDistance, 0, 120)
		for i, v := range span(1, 80) {
			in := held(eval.ClassAuthor, v)
			in.Document = label("doc", i/2)
			contiguous = append(contiguous, in)
		}
		contiguous = append(contiguous, perDocument(eval.ClassDistractor, span(201, 240))...)

		first, err := th.CalibrateBands(roundRobin, eval.DefaultBandFloor())
		if err != nil {
			t.Fatalf("CalibrateBands: %v", err)
		}
		second, err := th.CalibrateBands(contiguous, eval.DefaultBandFloor())
		if err != nil {
			t.Fatalf("CalibrateBands: %v", err)
		}

		if reportOf(t, first, eval.BandNotYou).ErrorBound != reportOf(t, second, eval.BandNotYou).ErrorBound {
			t.Fatalf("the two partitions give different bounds; this test needs them identical")
		}
		if first.ID == second.ID {
			t.Errorf("two cluster partitions of the same distances share the ID %q", first.ID)
		}
	})
}

// ---------------------------------------------------------------------------
// What the gate refuses
// ---------------------------------------------------------------------------

// The gate measures generalization, so it runs on Test. Calibrate is where the
// thresholds were fitted, and re-measuring there asks whether the fit fits;
// Train is excluded for the same reason it always is.
func TestTheGateAdmitsOnlyTheTestSplit(t *testing.T) {
	th := calibrated(t)

	for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, ""} {
		name := string(split)
		if name == "" {
			name = "no split at all"
		}
		t.Run(name, func(t *testing.T) {
			in := clean()
			for i := range in {
				in[i].Distance.Split = split
			}
			if _, err := th.CalibrateBands(in, eval.DefaultBandFloor()); !errors.Is(err, eval.ErrTestSplit) {
				t.Errorf("err = %v, want %v", err, eval.ErrTestSplit)
			}
		})
	}

	t.Run("one segment from another split", func(t *testing.T) {
		in := clean()
		in[5].Distance.Split = corpus.Calibrate
		if _, err := th.CalibrateBands(in, eval.DefaultBandFloor()); !errors.Is(err, eval.ErrTestSplit) {
			t.Errorf("err = %v, want %v", err, eval.ErrTestSplit)
		}
	})
}

// A held-out distance scored against different bindings is not on the scale
// these thresholds describe, so it cannot be banded against them.
func TestTheGateRefusesMismatchedBindings(t *testing.T) {
	th := calibrated(t)

	cases := []struct {
		name   string
		mutate func(*deviation.Distance)
		want   error
	}{
		{name: "another profile", mutate: func(d *deviation.Distance) { d.ProfileID = "another-profile" }, want: eval.ErrProfileMismatch},
		{name: "another reference", mutate: func(d *deviation.Distance) { d.ReferenceID = "another-reference" }, want: eval.ErrReferenceMismatch},
		{name: "another manifest", mutate: func(d *deviation.Distance) { d.FeatureManifestDigest = "another-digest" }, want: eval.ErrManifestMismatch},
		{name: "another weighting", mutate: func(d *deviation.Distance) { d.WeightScheme = "expert-v1" }, want: eval.ErrWeightingMismatch},
		{name: "another distance algorithm", mutate: func(d *deviation.Distance) { d.Algorithm = "distance-median-v2" }, want: eval.ErrAlgorithmMismatch},
		{name: "another tier subset", mutate: func(d *deviation.Distance) { d.ScoredTiers = []features.Tier{"B"} }, want: eval.ErrTierMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := clean()
			c.mutate(&in[9].Distance)
			if _, err := th.CalibrateBands(in, eval.DefaultBandFloor()); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// The bound is a clustered bootstrap, so it needs the same cluster labels the
// interval slice does, on both classes.
func TestTheGateRequiresClusterLabels(t *testing.T) {
	th := calibrated(t)

	for _, class := range []eval.Class{eval.ClassAuthor, eval.ClassDistractor} {
		t.Run(string(class), func(t *testing.T) {
			in := clean()
			for i := range in {
				if in[i].Class == class {
					in[i].Document = ""
					break
				}
			}
			if _, err := th.CalibrateBands(in, eval.DefaultBandFloor()); !errors.Is(err, eval.ErrMissingCluster) {
				t.Errorf("err = %v, want %v", err, eval.ErrMissingCluster)
			}
		})
	}

	t.Run("partial author labels on the distractor class", func(t *testing.T) {
		in := clean()
		for i := range in {
			if in[i].Class == eval.ClassDistractor && in[i].Document == "doc-00" {
				in[i].Author = "author-one"
			}
		}
		if _, err := th.CalibrateBands(in, eval.DefaultBandFloor()); !errors.Is(err, eval.ErrPartialAuthorLabels) {
			t.Errorf("err = %v, want %v", err, eval.ErrPartialAuthorLabels)
		}
	})
}

// A segment with no distance was never banded, so it is evidence about nothing
// and counts toward neither the class denominator nor any occupancy — on either
// class, since an exclusion written for one is easy to leave off the other.
func TestUndefinedDistancesAreExcluded(t *testing.T) {
	th := calibrated(t)
	base := gateOf(t, clean())

	for _, class := range []eval.Class{eval.ClassAuthor, eval.ClassDistractor} {
		t.Run(string(class), func(t *testing.T) {
			padded := clean()
			for i := 0; i < 12; i++ {
				none := held(class, 0)
				none.Distance.Value = 0
				none.Distance.Defined = false
				none.Distance.Reason = deviation.ReasonInsufficientEvidence
				none.Distance.Features = nil
				none.Distance.ScoredTiers = nil
				// Deliberately unlabelled: an unscoreable segment is excluded
				// before anything is clustered, so requiring a cluster label for
				// it would refuse a population over evidence that is not used.
				none.Document = ""
				padded = append(padded, none)
			}

			got, err := th.CalibrateBands(padded, eval.DefaultBandFloor())
			if err != nil {
				t.Fatalf("CalibrateBands: %v", err)
			}

			// The whole report, not just the denominator: the padding carries a
			// value of 0, which is below t_low, so an implementation that
			// filtered it out of the statistics but left it in the occupancy
			// counts would show twelve phantom segments sitting in in-range.
			if !reflect.DeepEqual(got.Bands, base.Bands) {
				t.Errorf("adding unscoreable %s segments changed the band reports:\n%+v\n%+v", class, base.Bands, got.Bands)
			}
			if got.Calibrated != base.Calibrated {
				t.Errorf("adding unscoreable %s segments changed the verdict from %v to %v", class, base.Calibrated, got.Calibrated)
			}
		})
	}
}

// A class with no held-out segments at all cannot support the band that bounds
// its error. Dividing by zero here would produce a NaN bound that compares false
// against every target and silently refuses, or worse, compares true.
func TestAClassWithNoHeldOutSegmentsRefusesItsBand(t *testing.T) {
	th := calibrated(t)

	authorsOnly := perDocument(eval.ClassAuthor, span(1, 80))
	got, err := th.CalibrateBands(authorsOnly, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}

	inRange := reportOf(t, got, eval.BandInRange)
	if inRange.Emitted {
		t.Errorf("in-range was emitted with no held-out distractors at all")
	}
	if math.IsNaN(inRange.ErrorBound) || math.IsInf(inRange.ErrorBound, 0) {
		t.Errorf("bound = %v with an empty class", inRange.ErrorBound)
	}
	// With no evidence the rate could be anything, so the bound is 1 — the
	// widest value there is, and one that fails every target below it. Reporting
	// 0 would be the same over-confidence the rule-of-three floor exists to stop.
	if inRange.ErrorBound != 1 {
		t.Errorf("bound = %v with an empty class, want 1", inRange.ErrorBound)
	}
	if inRange.ClassSegments != 0 || inRange.ClassClusters != 0 {
		t.Errorf("class segments = %d and clusters = %d, want 0 and 0", inRange.ClassSegments, inRange.ClassClusters)
	}
	if notYou := reportOf(t, got, eval.BandNotYou); !notYou.Emitted {
		t.Errorf("not-you was refused although its own class is intact: %v", notYou.Reason)
	}
}

func TestTheGateRefusesAnInvalidFloor(t *testing.T) {
	th := calibrated(t)

	cases := []struct {
		name   string
		mutate func(*eval.BandFloor)
	}{
		{name: "a zero confidence", mutate: func(f *eval.BandFloor) { f.Confidence = 0 }},
		{name: "a negative confidence", mutate: func(f *eval.BandFloor) { f.Confidence = -0.5 }},
		{name: "a confidence of one", mutate: func(f *eval.BandFloor) { f.Confidence = 1 }},
		{name: "a confidence above one", mutate: func(f *eval.BandFloor) { f.Confidence = 1.5 }},
		{name: "a NaN confidence", mutate: func(f *eval.BandFloor) { f.Confidence = math.NaN() }},
		{name: "zero resamples", mutate: func(f *eval.BandFloor) { f.Resamples = 0 }},
		{name: "negative resamples", mutate: func(f *eval.BandFloor) { f.Resamples = -1 }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			floor := eval.DefaultBandFloor()
			c.mutate(&floor)
			if _, err := th.CalibrateBands(clean(), floor); !errors.Is(err, eval.ErrInvalidBandFloor) {
				t.Errorf("err = %v, want %v", err, eval.ErrInvalidBandFloor)
			}
		})
	}

	t.Run("no distances", func(t *testing.T) {
		if _, err := th.CalibrateBands(nil, eval.DefaultBandFloor()); !errors.Is(err, eval.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, eval.ErrMissingInput)
		}
	})

	t.Run("a nil artifact", func(t *testing.T) {
		var none *eval.Thresholds
		if _, err := none.CalibrateBands(clean(), eval.DefaultBandFloor()); !errors.Is(err, eval.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, eval.ErrMissingInput)
		}
	})
}

// ---------------------------------------------------------------------------
// The calibration is what classifies
// ---------------------------------------------------------------------------

// A refused band must not be reachable. Thresholds.Band answers where a distance
// falls; Calibration.Band answers what may be said about it, which is the only
// question score and rewrite are allowed to ask.
func TestTheCalibrationCollapsesRefusedBands(t *testing.T) {
	th := calibrated(t)

	// A distance of 10 is below t_low, so the thresholds place it in-range.
	inRangeSide := held(eval.ClassAuthor, 10).Distance
	// A distance of 300 is above t_high, so the thresholds place it in not-you.
	notYouSide := held(eval.ClassAuthor, 300).Distance

	raw, err := th.Band(inRangeSide)
	if err != nil {
		t.Fatalf("Band: %v", err)
	}
	if raw.Band != eval.BandInRange {
		t.Fatalf("the thresholds place 10 in %q, want %q", raw.Band, eval.BandInRange)
	}

	t.Run("a refused in-range collapses to drifting", func(t *testing.T) {
		got := gateOf(t, leakyInRange())
		if reportOf(t, got, eval.BandInRange).Emitted {
			t.Fatalf("this fixture must refuse in-range")
		}
		out, err := got.Band(inRangeSide)
		if err != nil {
			t.Fatalf("Band: %v", err)
		}
		if !out.Defined {
			t.Fatalf("no band: %v", out.Reason)
		}
		if out.Band != eval.BandDrifting {
			t.Errorf("band = %q for a distance in a refused in-range, want %q", out.Band, eval.BandDrifting)
		}
	})

	t.Run("a refused not-you collapses to drifting", func(t *testing.T) {
		got := gateOf(t, leakyNotYou())
		if reportOf(t, got, eval.BandNotYou).Emitted {
			t.Fatalf("this fixture must refuse not-you")
		}
		out, err := got.Band(notYouSide)
		if err != nil {
			t.Fatalf("Band: %v", err)
		}
		if out.Band != eval.BandDrifting {
			t.Errorf("band = %q for a distance in a refused not-you, want %q", out.Band, eval.BandDrifting)
		}
	})

	t.Run("an emitted band is passed through", func(t *testing.T) {
		got := gateOf(t, clean())
		out, err := got.Band(inRangeSide)
		if err != nil {
			t.Fatalf("Band: %v", err)
		}
		if out.Band != eval.BandInRange {
			t.Errorf("band = %q where in-range was emitted, want %q", out.Band, eval.BandInRange)
		}
	})
}

// An uncalibrated profile emits no band at all. ADR 0006 turns on this: with no
// band and no comparable score, rewrite refuses rather than treating an absence
// of measurement as an improvement.
func TestAnUncalibratedProfileEmitsNoBand(t *testing.T) {
	got := gateOf(t, bothLeak())
	if got.Calibrated {
		t.Fatalf("this fixture must be uncalibrated")
	}

	for _, value := range []float64{10, 150, 300} {
		out, err := got.Band(held(eval.ClassAuthor, value).Distance)
		if err != nil {
			t.Fatalf("Band(%v): %v", value, err)
		}
		if out.Defined {
			t.Errorf("distance %v banded as %q against an uncalibrated profile", value, out.Band)
		}
		if out.Band != "" {
			t.Errorf("band = %q, want empty", out.Band)
		}
		if out.Reason == "" {
			t.Errorf("no reason given for refusing to band %v", value)
		}
	}
}

// An unscoreable segment gets no band from the calibration either, and keeps its
// own reason rather than being told the profile is uncalibrated.
func TestTheCalibrationPassesThroughInsufficientEvidence(t *testing.T) {
	got := gateOf(t, clean())

	none := held(eval.ClassAuthor, 0).Distance
	none.Value = 0
	none.Defined = false
	none.Reason = deviation.ReasonInsufficientEvidence
	none.Features = nil
	none.ScoredTiers = nil

	out, err := got.Band(none)
	if err != nil {
		t.Fatalf("Band: %v", err)
	}
	if out.Defined {
		t.Fatalf("an unscoreable distance banded as %q", out.Band)
	}
	if out.Reason != deviation.ReasonInsufficientEvidence {
		t.Errorf("reason = %q, want the distance's own %q", out.Reason, deviation.ReasonInsufficientEvidence)
	}
}

// The calibration applies the same binding checks the thresholds do, so a
// distance from another calibration cannot be banded through it either — and the
// whole table is exercised, since a guard that reaches only the first field is
// the usual way this goes wrong.
func TestTheCalibrationRefusesAMismatchedDistance(t *testing.T) {
	got := gateOf(t, clean())

	cases := []struct {
		name   string
		mutate func(*deviation.Distance)
		want   error
	}{
		{name: "another profile", mutate: func(d *deviation.Distance) { d.ProfileID = "another-profile" }, want: eval.ErrProfileMismatch},
		{name: "another reference", mutate: func(d *deviation.Distance) { d.ReferenceID = "another-reference" }, want: eval.ErrReferenceMismatch},
		{name: "another manifest", mutate: func(d *deviation.Distance) { d.FeatureManifestDigest = "another-digest" }, want: eval.ErrManifestMismatch},
		{name: "another weighting", mutate: func(d *deviation.Distance) { d.WeightScheme = "expert-v1" }, want: eval.ErrWeightingMismatch},
		{name: "another distance algorithm", mutate: func(d *deviation.Distance) { d.Algorithm = "distance-median-v2" }, want: eval.ErrAlgorithmMismatch},
		{name: "another tier subset", mutate: func(d *deviation.Distance) { d.ScoredTiers = []features.Tier{"B"} }, want: eval.ErrTierMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			foreign := held(eval.ClassAuthor, 10).Distance
			c.mutate(&foreign)
			if _, err := got.Band(foreign); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// A calibration is an artifact, so it has to survive being one. Every other
// artifact here — the profile, the reference, the thresholds, the intervals — is
// self-contained and content-addressed precisely so it can be stored and reused,
// and a calibration that classified through hidden state would not be.
//
// The failure this guards against is silent rather than loud: an implementation
// holding the boundaries in an unexported field decodes them as zero, and then
// every distance above zero satisfies d >= High and comes back `not you`, with
// no error anywhere.
//
// Added by consensus after the first implementation classified through an
// unexported copy of the thresholds.
func TestACalibrationSurvivesBeingPersisted(t *testing.T) {
	base := gateOf(t, clean())

	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var restored eval.Calibration
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if restored.ID != base.ID {
		t.Errorf("ID = %q, want %q", restored.ID, base.ID)
	}
	if restored.Calibrated != base.Calibrated {
		t.Errorf("Calibrated = %v, want %v", restored.Calibrated, base.Calibrated)
	}
	if !reflect.DeepEqual(restored.Bands, base.Bands) {
		t.Errorf("the band reports did not survive the round trip")
	}

	for _, value := range []float64{10, 96, 150, 205, 300} {
		distance := held(eval.ClassAuthor, value).Distance

		want, err := base.Band(distance)
		if err != nil {
			t.Fatalf("Band(%v) on the original: %v", value, err)
		}
		got, err := restored.Band(distance)
		if err != nil {
			t.Fatalf("Band(%v) on the restored calibration: %v", value, err)
		}
		if got != want {
			t.Errorf("distance %v bands as %+v after a round trip, want %+v", value, got, want)
		}
	}

	// And the bindings are checked from the restored artifact too, not waved
	// through because the hidden state is gone.
	foreign := held(eval.ClassAuthor, 10).Distance
	foreign.ProfileID = "another-profile"
	if _, err := restored.Band(foreign); !errors.Is(err, eval.ErrProfileMismatch) {
		t.Errorf("err = %v, want %v", err, eval.ErrProfileMismatch)
	}
}

// The boundaries a calibration classifies with are its own recorded fields, and
// they agree with the thresholds it was built from. A calibration whose
// boundaries disagreed with its ThresholdsID would be two artifacts wearing one
// name.
func TestACalibrationRecordsTheBoundariesItClassifiesWith(t *testing.T) {
	th := calibrated(t)
	got, err := th.CalibrateBands(clean(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}

	if got.Low != th.Low || got.High != th.High {
		t.Errorf("boundaries = (%v, %v), want the thresholds' own (%v, %v)", got.Low, got.High, th.Low, th.High)
	}
	if got.WeightScheme != th.WeightScheme {
		t.Errorf("WeightScheme = %q, want %q", got.WeightScheme, th.WeightScheme)
	}
	if got.DistanceAlgorithm != th.DistanceAlgorithm {
		t.Errorf("DistanceAlgorithm = %q, want %q", got.DistanceAlgorithm, th.DistanceAlgorithm)
	}
	if !reflect.DeepEqual(got.ScoredTiers, th.ScoredTiers) {
		t.Errorf("ScoredTiers = %v, want %v", got.ScoredTiers, th.ScoredTiers)
	}
}

// An uncalibrated profile refuses with a declared reason, not an inline string a
// consumer can only discover by reading the implementation.
func TestTheUncalibratedReasonIsDeclared(t *testing.T) {
	got := gateOf(t, bothLeak())
	if got.Calibrated {
		t.Fatalf("this fixture must be uncalibrated")
	}

	out, err := got.Band(held(eval.ClassAuthor, 10).Distance)
	if err != nil {
		t.Fatalf("Band: %v", err)
	}
	if out.Reason != eval.ReasonUncalibrated {
		t.Errorf("reason = %q, want %q", out.Reason, eval.ReasonUncalibrated)
	}
	if eval.ReasonUncalibrated != "uncalibrated" {
		t.Errorf("ReasonUncalibrated = %q, want %q", eval.ReasonUncalibrated, "uncalibrated")
	}
}

// ---------------------------------------------------------------------------
// The declared confidence is used, not merely recorded
// ---------------------------------------------------------------------------

// Three nested levels on one population, exact under the declared seed. This
// pins the percentile rank and proves the confidence reaches the computation.
//
// The seed and the resample count are deliberately NOT asserted to move the
// bound: the bound is an order statistic of a discrete distribution over a
// handful of achievable rates, so two seeds or two resample counts agreeing on
// it is ordinary rather than suspicious, and a test asserting otherwise would be
// flaky. Both reach the identity instead.
func TestTheConfidenceLevelReachesTheBound(t *testing.T) {
	th := calibrated(t)

	cases := []struct {
		confidence float64
		bound      float64
	}{
		{confidence: 0.50, bound: 0.20},
		{confidence: 0.95, bound: 0.30},
		{confidence: 0.99, bound: 0.35},
	}

	for _, c := range cases {
		t.Run(strconv.FormatFloat(c.confidence, 'f', -1, 64), func(t *testing.T) {
			floor := eval.DefaultBandFloor()
			floor.Confidence = c.confidence
			got, err := th.CalibrateBands(leakyInRange(), floor)
			if err != nil {
				t.Fatalf("CalibrateBands: %v", err)
			}
			inRange := reportOf(t, got, eval.BandInRange)
			if inRange.ErrorBound != c.bound {
				t.Errorf("bound at confidence %v = %v, want %v", c.confidence, inRange.ErrorBound, c.bound)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The other class, symmetrically
// ---------------------------------------------------------------------------

// The empty-class case on the author side, matching the distractor one. A guard
// written for one class is easy to leave off the other.
func TestAnEmptyAuthorClassRefusesNotYou(t *testing.T) {
	th := calibrated(t)

	distractorsOnly := perDocument(eval.ClassDistractor, span(201, 240))
	got, err := th.CalibrateBands(distractorsOnly, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}

	notYou := reportOf(t, got, eval.BandNotYou)
	if notYou.Emitted {
		t.Errorf("not-you was emitted with no held-out authors at all")
	}
	if notYou.ErrorBound != 1 {
		t.Errorf("bound = %v with an empty author class, want 1", notYou.ErrorBound)
	}
	if inRange := reportOf(t, got, eval.BandInRange); !inRange.Emitted {
		t.Errorf("in-range was refused although its own class is intact: %v", inRange.Reason)
	}
}
