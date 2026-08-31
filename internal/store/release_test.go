package store_test

import (
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
)

// seededRelease writes a snapshot, profile, reference, threshold and release,
// and returns the release with the profile it belongs to.
func seededRelease(t *testing.T, s *store.Store) (store.Profile, store.Reference, store.EvalResult) {
	t.Helper()
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	if err := s.PutThreshold(ctx(), thresholdFixture(prof.ID, ref.ID)); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	release := evalResultFixture(prof.ID, ref.ID)
	if err := s.PutEvalResult(ctx(), release, store.AdvanceHead); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}
	return prof, ref, release
}

// ---------------------------------------------------------------------------
// The release itself
// ---------------------------------------------------------------------------

// The whole release, field for field. score is a separate invocation from eval,
// so everything Calibration.Band consults has to survive the round trip.
func TestAReleaseRoundTrips(t *testing.T) {
	s := newStore(t)
	_, _, want := seededRelease(t, s)

	got, err := s.LoadEvalResult(ctx(), want.ID)
	if err != nil {
		t.Fatalf("LoadEvalResult: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("release =\n%+v\nwant\n%+v", got, want)
	}
}

// The three reports are what the whole slice exists for: Calibration.Band
// downgrades a band to drifting only when it FINDS a report saying that band
// was not emitted, so a lost report silently restores a refused claim.
func TestTheBandReportsSurviveInOrder(t *testing.T) {
	s := newStore(t)
	_, _, release := seededRelease(t, s)

	got, err := s.LoadEvalResult(ctx(), release.ID)
	if err != nil {
		t.Fatalf("LoadEvalResult: %v", err)
	}
	if !reflect.DeepEqual(got.Calibration.Bands, bandReports()) {
		t.Errorf("bands =\n%+v\nwant\n%+v", got.Calibration.Bands, bandReports())
	}
}

// Exactly one report per band. A missing one is not a smaller calibration.
func TestACalibrationMustCarryEveryBandExactlyOnce(t *testing.T) {
	for _, c := range []struct {
		name  string
		bands func() []store.BandReport
	}{
		{"a missing report", func() []store.BandReport { return bandReports()[1:] }},
		{"a duplicated band", func() []store.BandReport {
			reports := bandReports()
			return append(reports, reports[0])
		}},
		{"no reports at all", func() []store.BandReport { return nil }},
		{"a band outside the vocabulary", func() []store.BandReport {
			reports := bandReports()
			reports[1].Band = "sideways"
			return reports
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			if err := s.PutThreshold(ctx(), thresholdFixture(prof.ID, ref.ID)); err != nil {
				t.Fatalf("PutThreshold: %v", err)
			}
			release := evalResultFixture(prof.ID, ref.ID)
			release.Calibration.Bands = c.bands()
			if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// drifting is the one that claims nothing: always emitted, no claiming class,
// no error gate. It is where a distance lands when neither claim holds.
func TestTheDriftingReportClaimsNothing(t *testing.T) {
	for _, c := range []struct {
		name  string
		alter func(*store.BandReport)
	}{
		{"not emitted", func(r *store.BandReport) { r.Emitted = false }},
		{"claiming a class", func(r *store.BandReport) { r.Claims = eval.ClassAuthor }},
		{"carrying a target", func(r *store.BandReport) { r.Target = 0.1 }},
		{"carrying an error bound", func(r *store.BandReport) { r.ErrorBound = 0.1 }},
		{"carrying a reason", func(r *store.BandReport) { r.Reason = "empty-error-class" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			c.alter(&release.Calibration.Bands[1])
			if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// A claiming report's emission is its decision, not a flag beside it: an
// over-bound report that claimed emission would ship a band the calibration
// refused.
func TestAClaimingReportsEmissionIsItsErrorBound(t *testing.T) {
	for _, c := range []struct {
		name       string
		target     float64
		bound      float64
		emitted    bool
		reason     string
		acceptable bool
	}{
		{"within its target", 0.10, 0.08, true, "", true},
		{"exactly at its target", 0.10, 0.10, true, "", true},
		{"over its target", 0.10, 0.12, false, "error-bound-exceeds-target", true},
		{"an empty error class", 0.10, 1, false, "empty-error-class", true},
		{"over its target yet emitted", 0.10, 0.12, true, "", false},
		{"within its target yet not emitted", 0.10, 0.08, false, "error-bound-exceeds-target", false},
		{"emitted with a reason", 0.10, 0.08, true, "error-bound-exceeds-target", false},
		{"unemitted with no reason", 0.10, 0.12, false, "", false},
		{"a target of one", 1, 1, true, "", false},
		{"a target of zero", 0, 0, true, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			report := &release.Calibration.Bands[0]
			report.Target, report.ErrorBound = c.target, c.bound
			report.Emitted, report.Reason = c.emitted, c.reason
			if c.target > 0 {
				report.MinClassClusters = int(math.Ceil(3 / c.target))
			}
			release.Calibration.Calibrated = c.emitted || release.Calibration.Bands[2].Emitted
			err := s.PutEvalResult(ctx(), release, store.LeaveHead)
			if c.acceptable && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.acceptable && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// The bound has a floor of 3/ClassClusters, so a report claiming a tighter one
// than its own cluster count allows describes a measurement eval cannot make.
func TestAReportsBoundRespectsItsClusterCount(t *testing.T) {
	s := newStore(t)
	release := releaseFor(t, s)
	report := &release.Calibration.Bands[0]
	report.ErrorBound = 3/float64(report.ClassClusters) - 0.001
	if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
		t.Error("accepted")
	}
}

// An empty error class is the one case with no clusters, and eval returns a
// bound of exactly 1 there rather than dividing by zero.
func TestAnEmptyErrorClassBoundsAtOne(t *testing.T) {
	for _, c := range []struct {
		name       string
		clusters   int
		bound      float64
		acceptable bool
	}{
		{"no clusters, bound of one", 0, 1, true},
		{"no clusters, a tighter bound", 0, 0.5, false},
		{"clusters, a bound of one", 40, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			report := &release.Calibration.Bands[0]
			report.ClassClusters, report.ErrorBound = c.clusters, c.bound
			if c.clusters == 0 {
				report.ClassSegments, report.ErrorRate = 0, 0
				report.Reason = "empty-error-class"
			} else {
				report.Reason = "error-bound-exceeds-target"
			}
			report.Emitted = false
			release.Calibration.Calibrated = release.Calibration.Bands[2].Emitted
			err := s.PutEvalResult(ctx(), release, store.LeaveHead)
			if c.acceptable && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.acceptable && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Non-finite and out-of-range figures are not measurements.
func TestAReportsFiguresMustBeMeasurements(t *testing.T) {
	for _, c := range []struct {
		name  string
		alter func(*store.BandReport)
	}{
		{"a target that is not a number", func(r *store.BandReport) { r.Target = math.NaN() }},
		{"an infinite bound", func(r *store.BandReport) { r.ErrorBound = math.Inf(1) }},
		{"a negative rate", func(r *store.BandReport) { r.ErrorRate = -0.1 }},
		{"a rate above one", func(r *store.BandReport) { r.ErrorRate = 1.5 }},
		{"a bound above one", func(r *store.BandReport) { r.ErrorBound = 1.5 }},
		{"a negative segment count", func(r *store.BandReport) { r.ClassSegments = -1 }},
		{"a negative cluster count", func(r *store.BandReport) { r.ClassClusters = -1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			c.alter(&release.Calibration.Bands[0])
			if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Both gates measure the held-out Test split, unconditionally, in eval itself.
// Each is moved on its own: a validator that checked only one would pass if
// they always moved together.
func TestEachGateMeasuresTheHeldOutSplit(t *testing.T) {
	for _, gate := range []struct {
		name  string
		alter func(*store.EvalResult, corpus.Split)
	}{
		{"discrimination", func(r *store.EvalResult, split corpus.Split) { r.Discrimination.Split = split }},
		{"calibration", func(r *store.EvalResult, split corpus.Split) { r.Calibration.Split = split }},
	} {
		for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Draft} {
			t.Run(gate.name+"/"+string(split), func(t *testing.T) {
				s := newStore(t)
				release := releaseFor(t, s)
				gate.alter(&release, split)
				if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
					t.Error("accepted")
				}
			})
		}
	}
}

// The minimum cluster count is derived from the target, so it is checked rather
// than believed.
func TestTheMinimumClusterCountIsDerivedFromTheTarget(t *testing.T) {
	s := newStore(t)
	release := releaseFor(t, s)
	release.Calibration.Bands[0].MinClassClusters++
	if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
		t.Error("accepted")
	}
}

// Which class each band claims is fixed by what the band means, not chosen.
func TestEachClaimingBandClaimsItsOwnClass(t *testing.T) {
	for _, c := range []struct {
		name  string
		index int
		class eval.Class
	}{
		{"in-range claiming the author", 0, eval.ClassAuthor},
		{"not-you claiming the distractor", 2, eval.ClassDistractor},
		{"in-range claiming nothing", 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			release.Calibration.Bands[c.index].Claims = c.class
			if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Calibrated is whether either claiming band was emitted. A row claiming
// otherwise would ship a release whose classifier only ever says drifting.
func TestCalibrationIsWhetherAClaimingBandWasEmitted(t *testing.T) {
	for _, c := range []struct {
		name            string
		inRange, notYou bool
		calibrated      bool
		reason          string
		acceptable      bool
	}{
		{"both emitted", true, true, true, "", true},
		{"one emitted", true, false, true, "", true},
		{"neither emitted", false, false, false, "no-claiming-band-emitted", true},
		{"neither emitted yet calibrated", false, false, true, "", false},
		{"emitted yet uncalibrated", true, true, false, "no-claiming-band-emitted", false},
		{"calibrated with a reason", true, true, true, "no-claiming-band-emitted", false},
		{"uncalibrated with no reason", false, false, false, "", false},
		{"uncalibrated with the wrong reason", false, false, false, "something-else", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			setEmitted(&release.Calibration.Bands[0], c.inRange)
			setEmitted(&release.Calibration.Bands[2], c.notYou)
			release.Calibration.Calibrated, release.Calibration.Reason = c.calibrated, c.reason
			release.Shippable = release.Discrimination.Discriminates && c.calibrated
			if !release.Shippable {
				release.Reason = eval.ReleaseReasonUncalibrated
			}
			release.ID = releaseID(release.Calibration.ID, release.Discrimination.ID)
			err := s.PutEvalResult(ctx(), release, store.LeaveHead)
			if c.acceptable && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.acceptable && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// What the release is checked against
// ---------------------------------------------------------------------------

// The release ID is recomputed from the two component IDs. That catches a
// tampered RELEASE id — it cannot catch a tampered component id, whose
// membership preimage this schema does not hold, and the tests do not pretend
// otherwise.
func TestTheReleaseIdentityIsRecomputedFromItsGates(t *testing.T) {
	s := newStore(t)
	release := releaseFor(t, s)
	release.ID = identity.HashBytes([]byte("an identity of its own"))
	if err := s.PutEvalResult(ctx(), release, store.LeaveHead); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

func TestAStoredReleaseWhoseIdentityWasChangedIsCorrupt(t *testing.T) {
	s := newStore(t)
	_, _, release := seededRelease(t, s)
	elsewhere := identity.HashBytes([]byte("elsewhere"))
	if _, err := openRaw(t, s).Exec("UPDATE eval_result SET discrimination_id = ?", elsewhere); err != nil {
		t.Fatalf("damaging: %v", err)
	}
	if _, err := s.LoadEvalResult(ctx(), release.ID); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// Both gates measure the same held-out population, which is what NewRelease
// requires. It is NOT the threshold's population: thresholds are fitted on the
// Calibrate split and band calibration's population is built on Test.
func TestBothGatesMeasureTheSamePopulation(t *testing.T) {
	s := newStore(t)
	release := releaseFor(t, s)
	release.Calibration.PopulationID = identity.HashBytes([]byte("another population"))
	if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
		t.Error("accepted")
	}
}

// And both carry the same binding, because Band rejects a distance that
// contradicts any of it.
func TestBothGatesCarryTheSameBinding(t *testing.T) {
	for _, c := range []struct {
		name  string
		alter func(*store.Binding)
	}{
		{"a different manifest", func(b *store.Binding) { b.ManifestDigest = identity.HashBytes([]byte("other")) }},
		{"a different weight scheme", func(b *store.Binding) { b.WeightScheme = "weighted-v1" }},
		{"a different distance algorithm", func(b *store.Binding) { b.DistanceAlgorithm = "distance-v2" }},
		{"different scored tiers", func(b *store.Binding) { b.ScoredTiers = nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			c.alter(&release.Calibration.Binding)
			if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// A binding that is well formed but not this binary's manifest would let a
// release fitted under one feature contract be read under another.
func TestAForeignManifestIsRefused(t *testing.T) {
	s := newStore(t)
	release := releaseFor(t, s)
	foreign := identity.HashBytes([]byte("a manifest from another version"))
	release.Discrimination.Binding.ManifestDigest = foreign
	release.Calibration.Binding.ManifestDigest = foreign
	if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
		t.Error("accepted")
	}
}

// Scored tiers are canonical: sorted, without duplicates, and every member from
// the vocabulary features declares.
func TestScoredTiersAreCanonical(t *testing.T) {
	for _, c := range []struct {
		name  string
		tiers []features.Tier
	}{
		{"a duplicate", []features.Tier{features.TierA, features.TierA}},
		{"an undeclared tier", []features.Tier{"Z"}},
		{"empty", nil},
		// Sortedness is implemented and cannot be exercised: features declares
		// one tier, so every list of declared tiers is trivially sorted. Named
		// rather than covered by a case that would prove nothing.
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			release.Discrimination.Binding.ScoredTiers = c.tiers
			release.Calibration.Binding.ScoredTiers = c.tiers
			if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// The calibration's bounds are its threshold's. That is the whole of what the
// persisted threshold can prove — it holds no binding fields — so this is
// bounds and parentage, not provenance, and it is described as such.
func TestACalibrationsBoundsAreItsThresholds(t *testing.T) {
	for _, c := range []struct {
		name  string
		alter func(*store.Calibration)
	}{
		{"a different low bound", func(x *store.Calibration) { x.Low = 0.41 }},
		{"a different high bound", func(x *store.Calibration) { x.High = 0.91 }},
		{"a threshold that is not there", func(x *store.Calibration) {
			x.ThresholdsID = identity.HashBytes([]byte("no such threshold"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			c.alter(&release.Calibration)
			if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The head, and the chain that has no arbitrary row in it
// ---------------------------------------------------------------------------

func TestTheReleaseHeadAdvancesWithTheReleaseItNames(t *testing.T) {
	s := newStore(t)
	prof, _, release := seededRelease(t, s)

	head, err := s.ReleaseHead(ctx(), prof.ID)
	if err != nil {
		t.Fatalf("ReleaseHead: %v", err)
	}
	if head != release.ID {
		t.Errorf("head = %q, want %q", head, release.ID)
	}
}

func TestAReleaseWrittenWithoutAdvancingLeavesNoHead(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	if err := s.PutThreshold(ctx(), thresholdFixture(prof.ID, ref.ID)); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	if err := s.PutEvalResult(ctx(), evalResultFixture(prof.ID, ref.ID), store.LeaveHead); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}
	if _, err := s.ReleaseHead(ctx(), prof.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// A refused release leaves no head, because a head naming a release that was
// never written is the window writing them together closes.
func TestARefusedReleaseLeavesNoHead(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	if err := s.PutThreshold(ctx(), thresholdFixture(prof.ID, ref.ID)); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	release := evalResultFixture(prof.ID, ref.ID)
	release.Calibration.Bands = release.Calibration.Bands[1:]

	if err := s.PutEvalResult(ctx(), release, store.AdvanceHead); err == nil {
		t.Fatal("accepted an incomplete calibration")
	}
	if _, err := s.ReleaseHead(ctx(), prof.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("head = %v, want ErrNotFound", err)
	}
}

// Two independent foreign keys would permit a head whose profile and whose
// release belong to different profiles. They do not prove that relationship.
func TestAHeadMayNotNameAnotherProfilesRelease(t *testing.T) {
	s := newStore(t)
	snapshot, first := seededProfile(t, s)
	firstRef := referenceFixture(first.ID)
	mustPutReference(t, s, firstRef)
	if err := s.PutThreshold(ctx(), thresholdFixture(first.ID, firstRef.ID)); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	release := evalResultFixture(first.ID, firstRef.ID)
	if err := s.PutEvalResult(ctx(), release, store.AdvanceHead); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}

	second := profileFixture(snapshot.ID)
	second.ID, second.Register = fakeID("profile", "second"), "letters"
	mustPutProfile(t, s, second)

	if _, err := openRaw(t, s).Exec("UPDATE release_head SET profile_id = ?", second.ID); err != nil {
		t.Fatalf("damaging: %v", err)
	}
	if _, err := s.ReleaseHead(ctx(), second.ID); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// cli must be able to count and name the registers, not only fetch one by name:
// --profile is required when more than one head exists, and an unknown one is
// answered by listing the ones that do.
func TestProfileHeadsNamesEveryRegister(t *testing.T) {
	s := newStore(t)
	snapshot, first := seededProfile(t, s)
	second := profileFixture(snapshot.ID)
	second.ID, second.Register = fakeID("profile", "letters"), "letters"

	if err := s.PutProfile(ctx(), first, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	if err := s.PutProfile(ctx(), second, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	heads, err := s.ProfileHeads(ctx())
	if err != nil {
		t.Fatalf("ProfileHeads: %v", err)
	}
	want := map[string]string{first.Register: first.ID, second.Register: second.ID}
	if !reflect.DeepEqual(heads, want) {
		t.Errorf("heads =\n%v\nwant\n%v", heads, want)
	}
}

func TestProfileHeadsIsEmptyBeforeAnythingIsIndexed(t *testing.T) {
	heads, err := newStore(t).ProfileHeads(ctx())
	if err != nil {
		t.Fatalf("ProfileHeads: %v", err)
	}
	if len(heads) != 0 {
		t.Errorf("heads = %v, want empty", heads)
	}
}

// The chain register -> profile -> release -> reference, resolved in one call
// so cli never picks a row.
func TestTheBundleResolvesTheWholeChain(t *testing.T) {
	s := newStore(t)
	prof, ref, release := seededRelease(t, s)

	bundle, err := s.LoadProfileBundle(ctx(), prof.Register)
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	if !bundle.Evaluated {
		t.Error("the bundle reports no evaluation")
	}
	if !reflect.DeepEqual(bundle.Profile, prof) {
		t.Errorf("profile =\n%+v\nwant\n%+v", bundle.Profile, prof)
	}
	if !reflect.DeepEqual(bundle.Reference, ref) {
		t.Errorf("reference =\n%+v\nwant\n%+v", bundle.Reference, ref)
	}
	if !reflect.DeepEqual(bundle.Release, release) {
		t.Errorf("release =\n%+v\nwant\n%+v", bundle.Release, release)
	}
}

// A profile that has never been evaluated is NOT "no profile". cli has to tell
// them apart to refuse with uncalibrated rather than no-profile.
func TestAnUnevaluatedProfileIsABundleWithoutARelease(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	bundle, err := s.LoadProfileBundle(ctx(), prof.Register)
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	if bundle.Evaluated {
		t.Error("a profile with no release reports as evaluated")
	}
	if !reflect.DeepEqual(bundle.Release, store.EvalResult{}) {
		t.Errorf("release = %+v, want the zero value", bundle.Release)
	}
	if !reflect.DeepEqual(bundle.Profile, prof) {
		t.Error("the profile did not survive")
	}
}

func TestABundleForAnUnknownRegisterIsNotFound(t *testing.T) {
	s := newStore(t)
	seededProfile(t, s)
	if _, err := s.LoadProfileBundle(ctx(), "letters"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// The migration
// ---------------------------------------------------------------------------

// A row from before the release contract cannot reconstruct one, and a nullable
// default would make a corrupted release look valid. Migration 1 removes them.
func TestMigrationOneRemovesReleasesThatPredateTheContract(t *testing.T) {
	migrations := store.Migrations()
	if len(migrations) < 2 {
		t.Fatalf("%d migrations; the release contract is appended, not amended into 0", len(migrations))
	}

	path := filepath.Join(t.TempDir(), "hapax.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Version 0 only, with its ledger row, as a database written before this
	// binary existed.
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatalf("applying migration 0: %v", err)
	}
	if _, err := db.Exec("INSERT INTO migration (version, checksum, applied_at) VALUES (0, ?, '2026-01-01T00:00:00Z')",
		identity.HashBytes([]byte(migrations[0]))); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO eval_result
		(id,profile_id,reference_id,auc,lower_bound,cap,author_segments,distractor_segments,
		 author_clusters,distractor_clusters,discriminates,calibrated,shippable,reason)
		VALUES (?,?,?,0.8,0.7,2.5,10,10,2,2,1,1,1,'')`,
		identity.HashBytes([]byte("legacy")), identity.HashBytes([]byte("p")), identity.HashBytes([]byte("r"))); err != nil {
		t.Fatalf("seeding a legacy row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	version, err := s.SchemaVersion(ctx())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if want := len(migrations) - 1; version != want {
		t.Errorf("schema version = %d, want %d", version, want)
	}
	raw := openRaw(t, s)
	var checksum string
	if err := raw.QueryRow("SELECT checksum FROM migration WHERE version = 1").Scan(&checksum); err != nil {
		t.Fatalf("migration 1 has no ledger row: %v", err)
	}
	if want := identity.HashBytes([]byte(migrations[1])); checksum != want {
		t.Errorf("ledger checksum = %q, want %q", checksum, want)
	}
	if got := rowsIn(t, raw, "eval_result"); got != 0 {
		t.Errorf("%d legacy releases survived the migration", got)
	}
	if got := rowsIn(t, raw, "calibration_band"); got != 0 {
		t.Errorf("calibration_band was not created by migration 1")
	}
}

// And a release written under THIS contract survives being reopened. Deleting
// eval_result on every open would satisfy the test above.
func TestReopeningDoesNotRemoveACurrentRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, _, release := seededRelease(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	if _, err := second.LoadEvalResult(ctx(), release.ID); err != nil {
		t.Errorf("a current release did not survive a reopen: %v", err)
	}
}

// ---------------------------------------------------------------------------

// releaseFor is a valid release over a freshly seeded profile, ready to be
// damaged one field at a time.
func releaseFor(t *testing.T, s *store.Store) store.EvalResult {
	t.Helper()
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	if err := s.PutThreshold(ctx(), thresholdFixture(prof.ID, ref.ID)); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	return evalResultFixture(prof.ID, ref.ID)
}

// setDiscriminates moves the discrimination gate between passing and failing,
// keeping the bound-versus-floor relation that decides it.
func setDiscriminates(gate *store.Discrimination, discriminates bool) {
	gate.Discriminates = discriminates
	if discriminates {
		gate.LowerBound, gate.Reason = gate.Floor+0.06, ""
	} else {
		gate.LowerBound, gate.Reason = gate.Floor-0.05, "lower-bound-below-floor"
	}
}

// The discrimination gate's verdict is its lower bound against its floor, the
// same way the calibration's is its band reports. A row claiming otherwise
// would ship a release on evidence that did not reach the floor.
func TestDiscriminationIsItsLowerBoundAgainstItsFloor(t *testing.T) {
	for _, c := range []struct {
		name          string
		bound, floor  float64
		discriminates bool
		reason        string
		acceptable    bool
	}{
		{"above the floor", 0.71, 0.65, true, "", true},
		{"exactly at the floor", 0.65, 0.65, true, "", true},
		{"below the floor", 0.60, 0.65, false, "lower-bound-below-floor", true},
		{"below the floor yet discriminating", 0.60, 0.65, true, "", false},
		{"above the floor yet not", 0.71, 0.65, false, "lower-bound-below-floor", false},
		{"discriminating with a reason", 0.71, 0.65, true, "lower-bound-below-floor", false},
		{"failing with no reason", 0.60, 0.65, false, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			release.Discrimination.LowerBound = c.bound
			release.Discrimination.Floor = c.floor
			release.Discrimination.Discriminates = c.discriminates
			release.Discrimination.Reason = c.reason
			release.Shippable = c.discriminates && release.Calibration.Calibrated
			if !release.Shippable {
				release.Reason = eval.ReleaseReasonDiscriminationFailed
			}
			err := s.PutEvalResult(ctx(), release, store.LeaveHead)
			if c.acceptable && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.acceptable && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// And on the way out.
func TestAStoredDiscriminationThatContradictsItsFloorIsCorrupt(t *testing.T) {
	s := newStore(t)
	_, _, release := seededRelease(t, s)
	if _, err := openRaw(t, s).Exec("UPDATE eval_result SET discrimination_floor = 0.95"); err != nil {
		t.Fatalf("damaging: %v", err)
	}
	if _, err := s.LoadEvalResult(ctx(), release.ID); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// setEmitted moves a claiming report between emitted and not, keeping the
// bound-versus-target relation that decides it.
func setEmitted(report *store.BandReport, emitted bool) {
	report.Emitted = emitted
	if !emitted {
		report.ErrorBound, report.Reason = report.Target*2, "error-bound-exceeds-target"
		return
	}
	// An emitted bound is at or under its target AND at or above the floor its
	// own cluster count imposes. Target/2 satisfied the first and broke the
	// second: with 40 clusters the floor is 0.075 and half of 0.10 is 0.05.
	report.ErrorBound, report.Reason = math.Max(3/float64(report.ClassClusters), report.Target*0.8), ""
}

// The vocabularies cli and store validate against are the ones eval declares.
func TestTheReleaseVocabulariesAreEvalsOwn(t *testing.T) {
	for name, values := range map[string][]string{
		"clustering":            stringsOf(eval.Clusterings()),
		"discrimination reason": stringsOf(eval.DiscriminationReasons()),
		"calibration reason":    stringsOf(eval.CalibrationReasons()),
		"band report reason":    stringsOf(eval.BandReportReasons()),
		"class":                 stringsOf(eval.Classes()),
		"tier":                  stringsOf(features.Tiers()),
	} {
		if len(values) == 0 {
			t.Errorf("%s declares no vocabulary", name)
		}
		sorted := append([]string(nil), values...)
		sort.Strings(sorted)
		for i := 1; i < len(sorted); i++ {
			if sorted[i] == sorted[i-1] {
				t.Errorf("%s declares %q twice", name, sorted[i])
			}
		}
	}
}

// A threshold belongs to the calibration's own profile and reference. An FK
// proves the threshold exists, not that it is this calibration's.
func TestACalibrationsThresholdIsItsOwnProfilesAndReferences(t *testing.T) {
	setUp := func(t *testing.T) (*store.Store, store.Profile, store.Reference, store.Threshold) {
		t.Helper()
		s := newStore(t)
		snapshot, prof := seededProfile(t, s)
		ref := referenceFixture(prof.ID)
		mustPutReference(t, s, ref)
		if err := s.PutThreshold(ctx(), thresholdFixture(prof.ID, ref.ID)); err != nil {
			t.Fatalf("PutThreshold: %v", err)
		}
		other := profileFixture(snapshot.ID)
		other.ID, other.Register = fakeID("profile", "other"), "letters"
		mustPutProfile(t, s, other)
		otherRef := referenceFixture(other.ID)
		mustPutReference(t, s, otherRef)
		foreign := thresholdFixture(other.ID, otherRef.ID)
		if err := s.PutThreshold(ctx(), foreign); err != nil {
			t.Fatalf("PutThreshold: %v", err)
		}
		return s, prof, ref, foreign
	}

	t.Run("another profile's threshold", func(t *testing.T) {
		s, prof, ref, foreign := setUp(t)
		release := evalResultFixture(prof.ID, ref.ID)
		release.Calibration.ThresholdsID = foreign.ID
		release.ID = releaseID(release.Calibration.ID, release.Discrimination.ID)
		if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
			t.Error("accepted")
		}
	})
	// Differing in BOTH profile and reference would pass an implementation that
	// checked only the profile, so here is one that differs in reference alone.
	t.Run("the same profile's other reference", func(t *testing.T) {
		s, prof, ref, _ := setUp(t)
		sibling := referenceFixture(prof.ID)
		sibling.ID, sibling.MinSegments = fakeID("reference", "sibling"), 40
		mustPutReference(t, s, sibling)
		siblingThreshold := thresholdFixture(prof.ID, sibling.ID)
		siblingThreshold.ID = fakeID("threshold", "sibling")
		if err := s.PutThreshold(ctx(), siblingThreshold); err != nil {
			t.Fatalf("PutThreshold: %v", err)
		}

		release := evalResultFixture(prof.ID, ref.ID)
		release.Calibration.ThresholdsID = siblingThreshold.ID
		release.ID = releaseID(release.Calibration.ID, release.Discrimination.ID)
		if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
			t.Error("accepted a threshold belonging to another of this profile's references")
		}
	})
	t.Run("and a stored one from another profile is corrupt", func(t *testing.T) {
		s, prof, ref, foreign := setUp(t)
		release := evalResultFixture(prof.ID, ref.ID)
		if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err != nil {
			t.Fatalf("PutEvalResult: %v", err)
		}
		if _, err := openRaw(t, s).Exec("UPDATE eval_result SET calibration_thresholds_id = ?", foreign.ID); err != nil {
			t.Fatalf("damaging: %v", err)
		}
		if _, err := s.LoadEvalResult(ctx(), release.ID); !errors.Is(err, store.ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
	})
	// And one from the same profile's OTHER reference. A reader checking only
	// the profile would reject the case above and accept this one.
	t.Run("and a stored one from another reference is corrupt", func(t *testing.T) {
		s, prof, ref, _ := setUp(t)
		sibling := referenceFixture(prof.ID)
		sibling.ID, sibling.MinSegments = fakeID("reference", "sibling"), 40
		mustPutReference(t, s, sibling)
		siblingThreshold := thresholdFixture(prof.ID, sibling.ID)
		siblingThreshold.ID = fakeID("threshold", "sibling")
		if err := s.PutThreshold(ctx(), siblingThreshold); err != nil {
			t.Fatalf("PutThreshold: %v", err)
		}
		release := evalResultFixture(prof.ID, ref.ID)
		if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err != nil {
			t.Fatalf("PutEvalResult: %v", err)
		}
		if _, err := openRaw(t, s).Exec(
			"UPDATE eval_result SET calibration_thresholds_id = ?", siblingThreshold.ID); err != nil {
			t.Fatalf("damaging: %v", err)
		}
		if _, err := s.LoadEvalResult(ctx(), release.ID); !errors.Is(err, store.ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
	})
}

// Damage the schema cannot express, on the way out. Each of these is something
// Calibration.Band consults, so a reader that trusted the row would classify a
// draft against a release the calibration never made.
func TestDerivedReleaseStateIsCheckedOnRead(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage string
	}{
		{"a calibration claiming to be calibrated with nothing emitted",
			"UPDATE calibration_band SET emitted = 0, reason = 'error-bound-exceeds-target' WHERE band <> 'drifting'"},
		{"a band report removed", "DELETE FROM calibration_band WHERE band = 'not-you'"},
		{"a report emitted over its target", "UPDATE calibration_band SET error_bound = 0.9 WHERE band = 'in-range'"},
		{"a minimum cluster count that is not derived",
			"UPDATE calibration_band SET min_class_clusters = 1 WHERE band = 'in-range'"},
		{"the gates measuring different populations",
			"UPDATE eval_result SET calibration_population_id = discrimination_id"},
		{"the gates carrying different weight schemes",
			"UPDATE eval_result SET calibration_weight_scheme = 'weighted-v1'"},
		{"the gates carrying different manifests",
			"UPDATE eval_result SET calibration_manifest_digest = discrimination_population_id"},
		{"the gates carrying different distance algorithms",
			"UPDATE eval_result SET calibration_distance_algorithm = 'distance-other-v1'"},
		{"the gates carrying different scored tiers",
			"UPDATE eval_result SET discrimination_scored_tiers = ''"},
		{"a calibration low bound that is not its threshold's",
			"UPDATE eval_result SET calibration_low = 0.41"},
		{"a calibration high bound that is not its threshold's",
			"UPDATE eval_result SET calibration_high = 0.91"},
		{"a discrimination measuring a split of its own",
			"UPDATE eval_result SET discrimination_split = 'calibrate'"},
		{"a calibration measuring a split of its own",
			"UPDATE eval_result SET calibration_split = 'calibrate'"},
		// shippable = discriminates AND calibrated is already a CHECK from
		// slice 2a, so it is refused by the database and never reaches a
		// reader. It is asserted in
		// TestTheDatabaseItselfRefusesTheContradictionsItCanExpress.
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			_, _, release := seededRelease(t, s)
			result, err := openRaw(t, s).Exec(c.damage)
			if err != nil {
				t.Fatalf("damaging: %v", err)
			}
			if affected, _ := result.RowsAffected(); affected == 0 {
				t.Fatalf("the damage changed no row; the case would be vacuous")
			}
			if _, err := s.LoadEvalResult(ctx(), release.ID); !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}

// Advancing the profile head does not carry the old release with it: the new
// profile has no release until it is evaluated, which is what lets cli refuse
// with uncalibrated rather than scoring against the previous profile's bands.
func TestAdvancingTheProfileHeadLeavesTheNewProfileUnevaluated(t *testing.T) {
	s := newStore(t)
	snapshot, first := seededProfile(t, s)
	firstRef := referenceFixture(first.ID)
	mustPutReference(t, s, firstRef)
	if err := s.PutThreshold(ctx(), thresholdFixture(first.ID, firstRef.ID)); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	if err := s.PutProfile(ctx(), first, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	if err := s.PutEvalResult(ctx(), evalResultFixture(first.ID, firstRef.ID), store.AdvanceHead); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}

	second := profileFixture(snapshot.ID)
	second.ID = fakeID("profile", "second")
	second.MinParagraphLexicalTokens = 55
	if err := s.PutProfile(ctx(), second, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	bundle, err := s.LoadProfileBundle(ctx(), second.Register)
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	if bundle.Profile.ID != second.ID {
		t.Errorf("bundle profile = %q, want the advanced head %q", bundle.Profile.ID, second.ID)
	}
	if bundle.Evaluated {
		t.Error("the new profile inherited the old profile's release")
	}
	// And the old release is still there, reachable by its own profile.
	if _, err := s.ReleaseHead(ctx(), first.ID); err != nil {
		t.Errorf("the previous profile lost its release head: %v", err)
	}
}

// A refused release leaves the head where it was, rather than clearing or
// advancing it: the write and the head move together or not at all.
func TestARefusedReleaseLeavesTheExistingHeadAlone(t *testing.T) {
	s := newStore(t)
	prof, ref, first := seededRelease(t, s)

	broken := evalResultFixture(prof.ID, ref.ID)
	broken.Calibration.ID = fakeID("calibration", "second")
	broken.ID = releaseID(broken.Calibration.ID, broken.Discrimination.ID)
	broken.Calibration.Bands[0].Emitted = true
	broken.Calibration.Bands[0].ErrorBound = 0.9
	if err := s.PutEvalResult(ctx(), broken, store.AdvanceHead); err == nil {
		t.Fatal("accepted a report emitted over its target")
	}

	head, err := s.ReleaseHead(ctx(), prof.ID)
	if err != nil {
		t.Fatalf("ReleaseHead: %v", err)
	}
	if head != first.ID {
		t.Errorf("head = %q, want the release that was already there, %q", head, first.ID)
	}
}

// empty-error-class is the reason eval gives when a claiming class had NO
// clusters at all, and it is the one case that bypasses the bound-versus-target
// comparison. Accepting it beside a populated class would let a report skip
// that comparison while claiming to have measured something.
func TestAnEmptyErrorClassHasNoClusters(t *testing.T) {
	for _, c := range []struct {
		name       string
		clusters   int
		segments   int
		acceptable bool
	}{
		{"no clusters", 0, 0, true},
		{"clusters after all", 40, 400, false},
		{"segments without clusters", 0, 400, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			report := &release.Calibration.Bands[0]
			report.Emitted, report.Reason = false, "empty-error-class"
			report.ErrorBound, report.ErrorRate = 1, 0
			report.ClassClusters, report.ClassSegments = c.clusters, c.segments
			release.Calibration.Calibrated = release.Calibration.Bands[2].Emitted
			err := s.PutEvalResult(ctx(), release, store.LeaveHead)
			if c.acceptable && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.acceptable && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// The reports arrive in the order eval emits them, and the store must not
// depend on that order to know which band is which. A permuted write that is
// accepted and then reads back as corrupt is the worst of both: it stores
// something no reader will take.
func TestBandReportsAreIdentifiedByBandAndNotByPosition(t *testing.T) {
	for _, c := range []struct {
		name  string
		order []int
	}{
		{"drifting first", []int{1, 0, 2}},
		{"reversed", []int{2, 1, 0}},
		{"claiming bands swapped", []int{2, 1, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			release := releaseFor(t, s)
			canonical := release.Calibration.Bands
			permuted := make([]store.BandReport, len(canonical))
			for to, from := range c.order {
				permuted[to] = canonical[from]
			}
			release.Calibration.Bands = permuted

			err := s.PutEvalResult(ctx(), release, store.LeaveHead)
			if err != nil {
				// Refusing a non-canonical order is a fine answer.
				return
			}
			// Accepting it is also fine — but then it must read back, and read
			// back as the same calibration. What must not happen is a write the
			// store's own reader calls corrupt.
			got, err := s.LoadEvalResult(ctx(), release.ID)
			if err != nil {
				t.Fatalf("a write the store accepted reads back as %v", err)
			}
			if !reflect.DeepEqual(got.Calibration.Bands, canonical) {
				t.Errorf("bands =\n%+v\nwant the canonical order\n%+v", got.Calibration.Bands, canonical)
			}
		})
	}
}

// And the derived calibration is the same whatever order the reports arrived
// in: drifting is always emitted, so a derivation that read it as a claiming
// band would call every calibration calibrated.
func TestCalibrationIsDerivedFromTheBandsAndNotTheirOrder(t *testing.T) {
	s := newStore(t)
	release := releaseFor(t, s)
	// Neither claiming band emitted, so the calibration is not calibrated —
	// however the three reports are ordered.
	setEmitted(&release.Calibration.Bands[0], false)
	setEmitted(&release.Calibration.Bands[2], false)
	release.Calibration.Calibrated, release.Calibration.Reason = true, ""
	release.Shippable = true
	release.Calibration.Bands[0], release.Calibration.Bands[1] = release.Calibration.Bands[1], release.Calibration.Bands[0]

	if err := s.PutEvalResult(ctx(), release, store.LeaveHead); err == nil {
		t.Error("a calibration claiming to be calibrated with no claiming band emitted was accepted")
	}
}
