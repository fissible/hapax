package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/store"
)

// score needs a fitted profile, a reference and a release that all describe each
// other. Assembling that in the workflow would mean reconstructing profile facts
// and choosing a reference, and LoadProfileBundle already shows how that goes
// wrong: it picks with ORDER BY id over content-derived hashes, which is
// selection by coincidence — #62.
//
// LoadScoringBundle is the one read that returns a coherent set, decoded to the
// domain types rather than the storage ones.
func TestAScoringBundleIsCoherentOrItIsNotReturned(t *testing.T) {
	s := newStore(t)
	ids := seedEveryArtifact(t, s)

	bundle, err := s.LoadScoringBundle(ctx(), seededRegister)
	if err != nil {
		t.Fatalf("LoadScoringBundle: %v", err)
	}

	if !bundle.Calibrated {
		t.Fatalf("the seeded graph has a release head and the bundle says it is not calibrated")
	}
	if bundle.Fitted.ID != ids.Profile {
		t.Errorf("profile = %q, want the head %q", bundle.Fitted.ID, ids.Profile)
	}
	// The reference the RELEASE names, not whichever sorts first.
	if bundle.Reference.ID != bundle.Release.Calibration.ReferenceID {
		t.Errorf("reference = %q and the release calibrated against %q",
			bundle.Reference.ID, bundle.Release.Calibration.ReferenceID)
	}
	if bundle.Release.ID != ids.EvalResult {
		t.Errorf("release = %q, want the head %q", bundle.Release.ID, ids.EvalResult)
	}
	// And the release is the DOMAIN artifact, reconstructed through
	// eval.NewRelease rather than handed over as storage rows: its gates have
	// to agree with the flags the store persisted separately.
	if bundle.Release.Shippable != bundle.Release.Discrimination.Discriminates && bundle.Release.Calibration.Calibrated {
		t.Errorf("the reconstructed release disagrees with its own gates: %+v", bundle.Release)
	}
}

// A profile with no release head is the UNCALIBRATED path, not an error and not
// a synthesised release. The head advances only for a shippable release, so its
// absence is exactly what "nothing has been calibrated yet" looks like.
func TestAProfileWithNoReleaseHeadIsARawBundle(t *testing.T) {
	s := newStore(t)
	ids := seedProfileAndReference(t, s)

	bundle, err := s.LoadScoringBundle(ctx(), seededRegister)
	if err != nil {
		t.Fatalf("LoadScoringBundle: %v", err)
	}
	if bundle.Calibrated {
		t.Error("called itself calibrated with no release head")
	}
	if bundle.Release.ID != "" {
		t.Errorf("invented a release %q", bundle.Release.ID)
	}
	if bundle.Fitted.ID != ids.Profile || bundle.Reference.ID != ids.Reference {
		t.Errorf("the raw bundle lost its profile or reference: %+v", bundle)
	}
}

// The two cannot disagree. A bundle claiming calibration with no release, or a
// release on one that says it is raw, is the state a caller would dereference on
// a path nobody tested.
func TestCalibrationAndTheReleaseCannotDisagree(t *testing.T) {
	s := newStore(t)
	seedEveryArtifact(t, s)
	bundle, err := s.LoadScoringBundle(ctx(), seededRegister)
	if err != nil {
		t.Fatalf("LoadScoringBundle: %v", err)
	}
	if bundle.Calibrated != (bundle.Release.ID != "") {
		t.Errorf("calibrated=%v with release %q", bundle.Calibrated, bundle.Release.ID)
	}
}

// Zero references is nothing to transform against. One is the raw path. Several
// is a choice nobody made — a release names its reference, and without one there
// is no provenance-bearing selector, so guessing by hash order is how a score
// gets computed against a reference the user never designated.
func TestARawBundleRequiresExactlyOneReference(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		s := newStore(t)
		seedProfileOnly(t, s)
		if _, err := s.LoadScoringBundle(ctx(), seededRegister); !errors.Is(err, store.ErrNoReference) {
			t.Errorf("err = %v, want ErrNoReference", err)
		}
	})
	t.Run("several", func(t *testing.T) {
		s := newStore(t)
		seedProfileAndTwoReferences(t, s)
		if _, err := s.LoadScoringBundle(ctx(), seededRegister); !errors.Is(err, store.ErrAmbiguousReference) {
			t.Errorf("err = %v, want ErrAmbiguousReference", err)
		}
	})
}

// No head at all is the ordinary first-run state, and the same not-found every
// other head lookup gives rather than a bundle-shaped emptiness.
func TestAScoringBundleForNoProfileIsNotFound(t *testing.T) {
	if _, err := newStore(t).LoadScoringBundle(ctx(), "essays"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A release that cannot be reconstructed is corruption, not a raw bundle. The
// difference matters: raw means nothing was calibrated, corrupt means something
// was and the database no longer describes it.
func TestAReleaseThatWillNotReconstructIsCorrupt(t *testing.T) {
	s := newStore(t)
	seedEveryArtifact(t, s)
	relaxEnum(t, s, "eval_result", "calibration_split")
	if _, err := openRaw(t, s).Exec("UPDATE eval_result SET calibration_split = 'draft'"); err != nil {
		t.Fatalf("damaging: %v", err)
	}
	if _, err := s.LoadScoringBundle(ctx(), seededRegister); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("err = %v, want ErrCorrupt", err)
	}
}

// ---------------------------------------------------------------------------
// Both directions of one codec
// ---------------------------------------------------------------------------

// A2b left the WRITE direction hand-rolled in internal/workflow — an
// eval.Release flattened into a store.EvalResult by a function there — while the
// read direction is here. Two directions of one codec in two packages is how
// they diverge, and the divergence would show up as a release that scores
// differently from the one that was evaluated.
//
// So the write side moves here too, and the round trip is the test. It is worth
// more than either direction checked alone: a field dropped on the way in and
// defaulted on the way out passes both halves separately.
func TestAReleaseSurvivesTheRoundTrip(t *testing.T) {
	s := newStore(t)
	ids := seedProfileAndReference(t, s)
	written := shippableRelease(t, ids.Profile, ids.Reference)

	if err := s.PutRelease(ctx(), written, "", store.AdvanceHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}
	bundle, err := s.LoadScoringBundle(ctx(), seededRegister)
	if err != nil {
		t.Fatalf("LoadScoringBundle: %v", err)
	}

	if !bundle.Calibrated {
		t.Fatal("wrote a release and read back a raw bundle")
	}
	if !reflect.DeepEqual(bundle.Release, written) {
		t.Errorf("the release did not survive:\n wrote %+v\n read  %+v", written, bundle.Release)
	}
}

// And the same release twice is a replay, while a different one at the same
// identity is a conflict — the rule every other artifact here follows.
func TestRewritingAReleaseIsAReplayOrAConflict(t *testing.T) {
	s := newStore(t)
	ids := seedProfileAndReference(t, s)
	written := shippableRelease(t, ids.Profile, ids.Reference)

	if err := s.PutRelease(ctx(), written, "", store.AdvanceHead); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.PutRelease(ctx(), written, "", store.AdvanceHead); err != nil {
		t.Errorf("an identical rewrite was refused: %v", err)
	}

	different := written
	different.Shippable = !written.Shippable
	if err := s.PutRelease(ctx(), different, "", store.AdvanceHead); !errors.Is(err, store.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// A release naming a profile or reference the store does not hold is refused
// rather than written, because a release is a claim ABOUT those artifacts.
func TestAReleaseMustNameArtifactsTheStoreHolds(t *testing.T) {
	s := newStore(t)
	ids := seedProfileAndReference(t, s)

	t.Run("an unknown profile", func(t *testing.T) {
		stranger := shippableRelease(t, fakeID("profile", "elsewhere"), ids.Reference)
		if err := s.PutRelease(ctx(), stranger, "", store.LeaveHead); err == nil {
			t.Error("wrote a release about a profile that is not here")
		}
	})
	t.Run("an unknown reference", func(t *testing.T) {
		stranger := shippableRelease(t, ids.Profile, fakeID("reference", "elsewhere"))
		if err := s.PutRelease(ctx(), stranger, "", store.LeaveHead); err == nil {
			t.Error("wrote a release about a reference that is not here")
		}
	})
}
