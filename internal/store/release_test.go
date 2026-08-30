package store_test

import (
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

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
	if got := rowsIn(t, openRaw(t, s), "eval_result"); got != 0 {
		t.Errorf("%d legacy releases survived the migration", got)
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

// setEmitted moves a claiming report between emitted and not, keeping the
// bound-versus-target relation that decides it.
func setEmitted(report *store.BandReport, emitted bool) {
	report.Emitted = emitted
	if emitted {
		report.ErrorBound, report.Reason = report.Target/2, ""
	} else {
		report.ErrorBound, report.Reason = report.Target*2, "error-bound-exceeds-target"
	}
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
