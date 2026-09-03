package store_test

// #83, at the boundary that refuses the artifact.
//
// The store's rule is `(verdict == separated) == (Low < High)`, in Go and again
// as a schema CHECK. It is right: the verdict is a stored witness that the two
// boundaries are ordered, and an artifact whose verdict and numbers disagree is
// incoherent whatever produced it.
//
// What produced it is two writers that each derived the verdict from a
// DIFFERENT question:
//
//	internal/workflow  verdict = separated iff the bootstrap intervals shipped
//	internal/store     verdict = separated iff the bands calibrated
//
// Neither of those is "are the boundaries ordered". Shippability and band
// emission are properties of confidence intervals and error rates; ordering is a
// property of two numbers. They coincide often enough that the suite stayed
// green and rarely enough that `hapax eval` on a real corpus exits 3 with
// `store: invalid: threshold verdict` — an operational failure standing in for
// "these two corpora do not separate".
//
// So both writers take the verdict from one derivation, and these tests pin the
// pairing rather than either writer's internals: whatever eval produces, the
// store accepts.

import (
	"context"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/eval/evaltest"
	"github.com/fissible/hapax/internal/store"
)

// ---------------------------------------------------------------------------
// The pairing
// ---------------------------------------------------------------------------

// Every ordering a calibration can produce is storable when the verdict comes
// from the single derivation. The inverted row is the one that fails today.
func TestTheDerivedVerdictIsAlwaysStorable(t *testing.T) {
	for _, c := range []struct {
		name      string
		low, high float64
	}{
		{"ordered", 0.10, 0.80},
		{"equal", 0.40, 0.40},
		{"inverted", 0.80, 0.10},
		{"both zero", 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			ids := seedEveryArtifact(t, s)
			threshold := store.Threshold{
				ID: fakeID("threshold", c.name), ProfileID: ids.Profile, ReferenceID: ids.Reference,
				PopulationID: fakeID("population", c.name),
				Low:          c.low, High: c.high,
				Verdict: eval.VerdictFor(c.low, c.high),
			}
			if err := s.PutThreshold(context.Background(), threshold); err != nil {
				t.Fatalf("the store refused a threshold whose verdict it derived itself: %v", err)
			}
			got, err := s.LoadThreshold(context.Background(), threshold.ID)
			if err != nil {
				t.Fatalf("LoadThreshold: %v", err)
			}
			if got.Verdict != threshold.Verdict {
				t.Errorf("verdict came back %q, was stored as %q", got.Verdict, threshold.Verdict)
			}
		})
	}
}

// And the rule still refuses an incoherent pairing, so the fix is not "the
// store stopped checking". A verdict written by hand against the wrong numbers
// is exactly the artifact this constraint exists to catch.
func TestAnIncoherentVerdictIsStillRefused(t *testing.T) {
	for _, c := range []struct {
		name      string
		low, high float64
		verdict   eval.ThresholdVerdict
	}{
		{"separated but not ordered", 0.80, 0.10, eval.VerdictSeparated},
		{"separated but equal", 0.40, 0.40, eval.VerdictSeparated},
		{"ordered but incompatible", 0.10, 0.80, eval.VerdictPairIncompatible},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			ids := seedEveryArtifact(t, s)
			err := s.PutThreshold(context.Background(), store.Threshold{
				ID: fakeID("threshold", c.name), ProfileID: ids.Profile, ReferenceID: ids.Reference,
				PopulationID: fakeID("population", c.name),
				Low:          c.low, High: c.high, Verdict: c.verdict,
			})
			if err == nil {
				t.Errorf("the store accepted verdict %q beside Low = %v and High = %v",
					c.verdict, c.low, c.high)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The release path
// ---------------------------------------------------------------------------

// PutRelease writes a threshold of its own, from the calibration rather than
// from eval's own verdict, and it is the second place the two questions were
// confused. A release whose populations sit the wrong way round is what a real
// corpus produced, and it must persist as a completed adverse measurement
// rather than fail.
func TestAReleaseWhosePopulationsAreInvertedIsStillStorable(t *testing.T) {
	s := newStore(t)
	ids := seedEveryArtifact(t, s)

	// The author population sits ENTIRELY ABOVE the distractors: every held-out
	// document of the author's measures further from the profile than any
	// stranger's does. Nothing about that is a fault — it is a measurement, and
	// it is what fifty documents against four hundred strangers produced.
	release := evaltest.ReleaseAroundUnchecked(t, ids.Profile, ids.Reference, 0.80, 0.10)
	if release.Shippable {
		t.Fatal("the inverted fixture shipped; it is meant not to")
	}

	if err := s.PutRelease(context.Background(), release, "", store.LeaveHead); err != nil {
		t.Fatalf("the store refused an inverted release: %v", err)
	}

	// And the threshold it wrote agrees with its own numbers.
	got, err := s.LoadThreshold(context.Background(), release.Calibration.ThresholdsID)
	if err != nil {
		t.Fatalf("LoadThreshold: %v", err)
	}
	if want := eval.VerdictFor(got.Low, got.High); got.Verdict != want {
		t.Errorf("stored verdict %q beside Low = %v and High = %v, want %q",
			got.Verdict, got.Low, got.High, want)
	}
}

// A shippable release still records a separated verdict, so the change does not
// simply make every threshold pair-incompatible.
func TestAShippableReleaseStillRecordsASeparatedVerdict(t *testing.T) {
	s := newStore(t)
	ids := seedEveryArtifact(t, s)
	release := evaltest.ShippableRelease(t, ids.Profile, ids.Reference)

	if err := s.PutRelease(context.Background(), release, "", store.LeaveHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}

	got, err := s.LoadThreshold(context.Background(), release.Calibration.ThresholdsID)
	if err != nil {
		t.Fatalf("LoadThreshold: %v", err)
	}
	if got.Verdict != eval.VerdictSeparated {
		t.Errorf("a shippable release recorded verdict %q", got.Verdict)
	}
	if !(got.Low < got.High) {
		t.Errorf("a shippable release recorded Low = %v and High = %v", got.Low, got.High)
	}
}

// ---------------------------------------------------------------------------
// Fail closed at the boundary
// ---------------------------------------------------------------------------

// A CALIBRATED calibration whose boundaries are the wrong way round is refused
// on the way in.
//
// eval will not produce one once the sort is gone. The store's job is not to
// trust that: it is the last place a wrong answer can be stopped before it
// reaches a user's paragraph, and a persisted row can arrive from an older
// build, a hand edit, or a partial write. Calibration.Band classifies against
// these two numbers, so a calibrated row with Low >= High reports IN-RANGE for
// a paragraph measured between the boundaries — the tool saying "this sounds
// like you" about prose its own measurement puts in the middle.
//
// # Why this is not already covered by the verdict rule
//
// Today these rows are refused, but by the THRESHOLD verdict check, and only
// because that check currently derives the verdict from the wrong question.
// Once the verdict is derived from the ordering, an inverted pair is coherent
// by that rule — pair-incompatible beside Low > High — and this row would be
// accepted. The check has to exist at the calibration, on its own terms.
//
// # Only the calibrated cases are here
//
// An UNCALIBRATED calibration may carry any boundaries, including none: it
// makes no claim, and refusing it would refuse the honest record of an
// evaluation that could not calibrate — which is the outcome #83 exists to let
// through. That direction is covered by
// TestAReleaseWhosePopulationsAreInvertedIsStillStorable above, which builds a
// genuinely uncalibrated inverted release through eval's own constructors
// rather than by mutating a shippable one. Mutation cannot express it: setting
// Calibrated to false on a shippable release trips the evaluation-result gate,
// so such a row would be refused for a reason that has nothing to do with this.
func TestACalibratedCalibrationMustHaveOrderedBoundaries(t *testing.T) {
	for _, c := range []struct {
		name      string
		low, high float64
		want      bool
	}{
		{"ordered", 0.10, 0.80, true},
		{"inverted", 0.80, 0.10, false},
		{"equal", 0.40, 0.40, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			ids := seedEveryArtifact(t, s)
			release := evaltest.ShippableRelease(t, ids.Profile, ids.Reference)
			if !release.Calibration.Calibrated {
				t.Fatal("the fixture is not calibrated; this test needs one that is")
			}
			release.Calibration.Low, release.Calibration.High = c.low, c.high

			err := s.PutRelease(context.Background(), release, "", store.LeaveHead)

			if c.want && err != nil {
				t.Errorf("the store refused a calibrated calibration with Low = %v and High = %v: %v",
					c.low, c.high, err)
			}
			if !c.want && err == nil {
				t.Errorf("the store accepted a CALIBRATED calibration with Low = %v and "+
					"High = %v; Calibration.Band would report in-range for a paragraph "+
					"measured between them", c.low, c.high)
			}
		})
	}
}
