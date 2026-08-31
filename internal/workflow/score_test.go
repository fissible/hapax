package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// What a score is
// ---------------------------------------------------------------------------

// A draft measured against a calibrated release: every admitted paragraph gets a
// distance and a band, and the bands come from the release the head names.
func TestScoringADraftAgainstACalibratedRelease(t *testing.T) {
	root, release := calibratedStore(t)
	draft := writeDraft(t, root, authorLikeDraft)

	result := scored(t, scoreRequest(root, draft))

	if !result.Calibrated {
		t.Fatalf("scored against a release and called itself uncalibrated: %+v", result)
	}
	if result.ReleaseID != release {
		t.Errorf("release = %q, want the head %q", result.ReleaseID, release)
	}
	if result.Refusal != "" {
		t.Errorf("refused with %q", result.Refusal)
	}
	if len(result.Segments) == 0 {
		t.Fatal("scored no segments")
	}
	banded := 0
	for i, segment := range result.Segments {
		if segment.Distance.Defined && !segment.Band.Defined {
			t.Errorf("segment %d has a distance and no band, against a calibrated release", i)
		}
		if segment.Band.Defined {
			banded++
			if !contains(workflow.Bands(), segment.Band.Band) {
				t.Errorf("segment %d is in band %q, which is not one of them", i, segment.Band.Band)
			}
		}
	}
	if banded == 0 {
		t.Error("nothing was banded; there is no score here")
	}
}

// The per-feature deltas are the part a person acts on, so they are present
// wherever a distance is, banded or not.
func TestEveryMeasuredSegmentCarriesItsDeltas(t *testing.T) {
	root, _ := calibratedStore(t)
	result := scored(t, scoreRequest(root, writeDraft(t, root, authorLikeDraft)))

	for i, segment := range result.Segments {
		if !segment.Distance.Defined {
			continue
		}
		if len(segment.Features) == 0 {
			t.Errorf("segment %d has a distance and no deltas", i)
		}
		for _, delta := range segment.Features {
			if delta.Feature == "" {
				t.Errorf("segment %d carries a delta for no feature", i)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Refusing the band, not the measurement
// ---------------------------------------------------------------------------

// ADR 0005 and DESIGN agree once you notice a refusal carries a document: with
// no release, score still measures. The refusal is the BAND.
func TestAnUncalibratedProfileStillMeasures(t *testing.T) {
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, authorLikeDraft)

	result := scored(t, scoreRequest(root, draft))

	if result.Refusal != workflow.RefusalUncalibrated {
		t.Fatalf("refusal = %q, want %q", result.Refusal, workflow.RefusalUncalibrated)
	}
	if result.Calibrated {
		t.Error("calibrated with no release")
	}
	if result.ReleaseID != "" {
		t.Errorf("named a release %q; an absent head is not a release", result.ReleaseID)
	}
	// The measurement happened. Refusing it as well would make the refusal
	// useless to the person who asked.
	measured := 0
	for i, segment := range result.Segments {
		if segment.Band.Defined {
			t.Errorf("segment %d was banded with nothing calibrated", i)
		}
		if segment.Distance.Defined {
			measured++
			if len(segment.Features) == 0 {
				t.Errorf("segment %d measured a distance and reported no deltas", i)
			}
			// Exactly uncalibrated, so the workflow and the CLI cannot disagree
			// about what an absent band means.
			if segment.Band.Reason != "uncalibrated" {
				t.Errorf("segment %d gives reason %q for its absent band", i, segment.Band.Reason)
			}
		}
	}
	if measured == 0 {
		t.Error("refused and measured nothing; the payload is empty")
	}
}

// The refusals score can make, and which of them follow a measurement.
func TestTheRefusalsScoreCanMake(t *testing.T) {
	for _, c := range []struct {
		name     string
		store    func(*testing.T) string
		refusal  string
		measured bool
	}{
		{
			name:    "nothing indexed at all",
			store:   func(t *testing.T) string { return emptyStore(t) },
			refusal: workflow.RefusalNoProfile,
		},
		{
			name:    "a profile with nothing to transform against",
			store:   profileOnlyStore,
			refusal: workflow.RefusalNoReference,
		},
		{
			name:    "a profile whose reference nothing designates",
			store:   twoReferenceStore,
			refusal: workflow.RefusalAmbiguousReference,
		},
		{
			name:     "a profile and a reference and no release",
			store:    uncalibratedStore,
			refusal:  workflow.RefusalUncalibrated,
			measured: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := c.store(t)
			result := scored(t, scoreRequest(root, writeDraft(t, root, authorLikeDraft)))
			if result.Refusal != c.refusal {
				t.Fatalf("refusal = %q, want %q", result.Refusal, c.refusal)
			}
			if !contains(workflow.Refusals(), result.Refusal) {
				t.Errorf("%q is outside the declared vocabulary", result.Refusal)
			}
			// Only the uncalibrated refusal follows a measurement. The others
			// refuse a precondition, and reporting segments for them would be
			// claiming to have measured something.
			if measured := len(result.Segments) > 0; measured != c.measured {
				t.Errorf("segments=%d for a %q refusal, want measured=%v",
					len(result.Segments), c.refusal, c.measured)
			}
		})
	}
}

// A draft with nothing to measure is insufficient-evidence, which is the one
// refusal that FOLLOWS an attempted measurement rather than replacing it.
func TestADraftWithNothingAboveTheFloorIsInsufficientEvidence(t *testing.T) {
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, "Too short.\n")

	result := scored(t, scoreRequest(root, draft))

	if result.Refusal != workflow.RefusalInsufficientEvidence {
		t.Errorf("refusal = %q, want %q", result.Refusal, workflow.RefusalInsufficientEvidence)
	}
	// It got as far as admitting the draft, so it can say what it found.
	if result.ParagraphsBelowFloor == 0 {
		t.Error("nothing was below the floor, and nothing was scored either")
	}
	// And it measured nothing, which is what the refusal means. A result
	// carrying segments while calling itself insufficient-evidence would be
	// labelling a measurement as an absence of one.
	if len(result.Segments) != 0 {
		t.Errorf("%d segments on an insufficient-evidence refusal", len(result.Segments))
	}
}

// ---------------------------------------------------------------------------
// Failures that are not outcomes
// ---------------------------------------------------------------------------

func TestAMissingDraftIsAFailure(t *testing.T) {
	root, _ := calibratedStore(t)
	request := scoreRequest(root, filepath.Join(t.TempDir(), "absent.md"))
	if _, err := workflow.Default().Score(ctx(), request); err == nil {
		t.Error("scored a draft that is not there")
	}
}

// score takes a draft operand but no corpus, so like profile and eval it finds
// the store rather than being told where it is.
func TestScoreDiscoversTheStore(t *testing.T) {
	root, _ := calibratedStore(t)
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	draft := writeDraft(t, root, authorLikeDraft)

	result, err := workflow.Default().Score(ctx(), workflow.ScoreRequest{
		StartDir: deep, Register: "essays", Path: draft,
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if result.StorePath != defaultStorePath(root) {
		t.Errorf("store = %q, want the ancestor's %q", result.StorePath, defaultStorePath(root))
	}
}
