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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
//
// Note what "nothing to measure" has to mean at the declared floor of one
// lexical token. Below-floor is UNREACHABLE there: prose with a word in it
// clears a floor of one, and text without one — digits, punctuation — is not
// admitted as a paragraph at all, so it is not below the floor either, it is
// absent. I first wrote this against a "Too short." draft, which measures
// perfectly well, and the implementation was changed to raise the default floor
// to three so the test would pass. That is a product-wide change to what counts
// as a paragraph, made to satisfy one fixture. The fixture was what was wrong.
//
// So ParagraphsBelowFloor is zero here and that is correct. It becomes non-zero
// only for a profile fitted at a raised floor, which #64 records.
func TestADraftWithNothingToMeasureIsInsufficientEvidence(t *testing.T) {
	t.Parallel()
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, "123 456.\n\n789 1011.\n\n")

	result := scored(t, scoreRequest(root, draft))

	if result.Refusal != workflow.RefusalInsufficientEvidence {
		t.Errorf("refusal = %q, want %q", result.Refusal, workflow.RefusalInsufficientEvidence)
	}
	// It measured nothing, which is what the refusal means. A result carrying
	// segments while calling itself insufficient-evidence would be labelling a
	// measurement as an absence of one.
	if len(result.Segments) != 0 {
		t.Errorf("%d segments on an insufficient-evidence refusal", len(result.Segments))
	}
	// And it got far enough to have admitted the draft, so it names it — along
	// with everything it resolved before the draft turned out to be the
	// problem. A refusal that dropped those would say the store was empty.
	if result.Path != draft {
		t.Errorf("path = %q, want %q", result.Path, draft)
	}
	if result.ProfileID == "" || result.ReferenceID == "" || result.ReleaseID == "" {
		t.Errorf("resolved a calibrated release and reported none of it: %+v", result)
	}
	if !result.Calibrated {
		t.Error("scored against a release and called itself uncalibrated")
	}
}

// An uncalibrated store scoring an unmeasurable draft has two things wrong at
// once. The draft is the nearer one: insufficient-evidence tells the reader to
// look at what they handed over, where uncalibrated would send them to run eval
// and leave them no better off.
func TestNothingToMeasureOutranksNothingToBandWith(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, "123 456.\n\n789 1011.\n\n")

	result := scored(t, scoreRequest(root, draft))

	if result.Refusal != workflow.RefusalInsufficientEvidence {
		t.Errorf("refusal = %q, want %q", result.Refusal, workflow.RefusalInsufficientEvidence)
	}
	if result.Calibrated {
		t.Error("calibrated with no release")
	}
}

// ---------------------------------------------------------------------------
// Failures that are not outcomes
// ---------------------------------------------------------------------------

func TestAMissingDraftIsAFailure(t *testing.T) {
	t.Parallel()
	root, _ := calibratedStore(t)
	request := scoreRequest(root, filepath.Join(t.TempDir(), "absent.md"))
	if _, err := workflow.Default().Score(ctx(), request); err == nil {
		t.Error("scored a draft that is not there")
	}
}

// score with no --profile resolves the SOLE head, exactly as profile does.
// Found by running the binary: every test here passed a register explicitly, so
// nothing exercised resolution, and `hapax score draft.md` came back no-profile
// against a store that had one — it was looking up the empty register.
func TestScoreWithNoRegisterSelectsTheSoleHead(t *testing.T) {
	t.Parallel()
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, authorLikeDraft)

	request := scoreRequest(root, draft)
	request.Register = ""
	result := scored(t, request)

	if result.Refusal != "" {
		t.Fatalf("refused with %q against a store holding one profile", result.Refusal)
	}
	if result.Selection != workflow.SelectedSoleHead {
		t.Errorf("selection = %q, want %q", result.Selection, workflow.SelectedSoleHead)
	}
	if !result.Calibrated {
		t.Error("resolved the head and did not score against its release")
	}
}

// And with several heads and none named it is the same correctable invocation
// profile makes, not a refusal and not a guess.
func TestScoreWithSeveralHeadsAndNoneNamedIsAmbiguous(t *testing.T) {
	t.Parallel()
	root := twoRegisterCorpus(t)
	draft := writeDraft(t, root, authorLikeDraft)

	request := scoreRequest(root, draft)
	request.Register = ""
	result := scored(t, request)

	if result.Selection != workflow.SelectionAmbiguous {
		t.Errorf("selection = %q, want %q", result.Selection, workflow.SelectionAmbiguous)
	}
	if len(result.Available) != 2 {
		t.Errorf("available = %v, want the two registers", result.Available)
	}
	if len(result.Segments) != 0 {
		t.Error("measured against a profile it had not chosen")
	}
}

// A register that does not exist is the same correctable invocation too.
func TestScoreWithAnUnknownRegisterListsWhatThereIs(t *testing.T) {
	t.Parallel()
	root, _ := calibratedStore(t)
	request := scoreRequest(root, writeDraft(t, root, authorLikeDraft))
	request.Register = "reviews"
	result := scored(t, request)

	if result.Selection != workflow.SelectionUnknownRegister {
		t.Errorf("selection = %q, want %q", result.Selection, workflow.SelectionUnknownRegister)
	}
	if len(result.Available) == 0 {
		t.Error("asked for a register that does not exist and offered none that do")
	}
}

// score takes a draft operand but no corpus, so like profile and eval it finds
// the store rather than being told where it is.
func TestScoreDiscoversTheStore(t *testing.T) {
	t.Parallel()
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
