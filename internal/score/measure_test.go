package score_test

import (
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/score"
)

// ADR 0005: without a release, score emits raw distance and per-feature deltas
// but no band. Score cannot serve that — it requires a release to band with —
// and giving it a nullable one would put the absent artifact back on a path
// nobody tests, which is how A2b's no-reference bug escaped as a
// library-internal error.
//
// So Measure is the raw entry point, and it is TOTAL: there is no release to
// pass and therefore none to forget.
func TestMeasureProducesDistancesAndNoBands(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)

	report, err := score.Measure([]byte(draft), mustFit(t, prof), ref)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if len(report.Segments) == 0 {
		t.Fatal("measured nothing")
	}

	measured := 0
	for i, segment := range report.Segments {
		if segment.Band.Defined {
			t.Errorf("segment %d carries a band with nothing calibrated: %+v", i, segment.Band)
		}
		// An absent band that does not say why renders as a hole. The reason is
		// what the reader acts on.
		if segment.Band.Reason != eval.ReasonUncalibrated {
			t.Errorf("segment %d has no band and gives reason %q, want %q",
				i, segment.Band.Reason, eval.ReasonUncalibrated)
		}
		if segment.Band.Band != "" {
			t.Errorf("segment %d names band %q", i, segment.Band.Band)
		}
		if segment.Distance.Defined {
			measured++
			// The deltas are the whole point of measuring without a band.
			if len(segment.Features) == 0 {
				t.Errorf("segment %d has a distance and no per-feature deltas", i)
			}
		}
	}
	if measured == 0 {
		t.Error("no segment yielded a defined distance; there is nothing to report")
	}
}

// And Measure agrees with Score about everything that is not the band. A raw
// report that computed distances differently would make the uncalibrated
// output incomparable with the calibrated one, which is the opposite of what
// emitting it is for.
func TestMeasureAgreesWithScoreOnEverythingButTheBand(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)
	release := testRelease(t, ref)
	fitted := mustFit(t, prof)

	banded, err := score.Score([]byte(draft), fitted, ref, release)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	raw, err := score.Measure([]byte(draft), fitted, ref)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if len(raw.Segments) != len(banded.Segments) {
		t.Fatalf("measured %d segments and scored %d", len(raw.Segments), len(banded.Segments))
	}
	// Compared as whole values with only the band set aside, so a delta whose
	// direction or reason drifted between the two paths is caught. Checking two
	// fields would let the part a reader acts on diverge unnoticed.
	for i := range raw.Segments {
		rawSegment, bandedSegment := raw.Segments[i], banded.Segments[i]
		rawSegment.Band, bandedSegment.Band = eval.BandOutcome{}, eval.BandOutcome{}
		if !reflect.DeepEqual(rawSegment, bandedSegment) {
			t.Errorf("segment %d differs outside its band:\n raw    %+v\n banded %+v",
				i, rawSegment, bandedSegment)
		}
	}
	if raw.ParagraphsBelowFloor != banded.ParagraphsBelowFloor {
		t.Errorf("below-floor count differs: %d and %d", raw.ParagraphsBelowFloor, banded.ParagraphsBelowFloor)
	}
}

// Measure still refuses what Score refuses about its inputs, minus the release.
func TestMeasureRefusesMismatchedArtifacts(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)

	t.Run("no reference", func(t *testing.T) {
		if _, err := score.Measure([]byte(draft), mustFit(t, prof), nil); err == nil {
			t.Error("measured against no reference")
		}
	})
	t.Run("a reference belonging to another profile", func(t *testing.T) {
		other := *ref
		other.ProfileID = "0f4a2c9b8e7d6a5b4c3d2e1f00112233445566778899aabbccddeeff001122334"
		if _, err := score.Measure([]byte(draft), mustFit(t, prof), &other); err == nil {
			t.Error("measured against another profile's reference")
		}
	})
}
