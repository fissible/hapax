package deviation_test

// The distance `d` — a uniformly weighted robust mean of transformed
// deviations, Manhattan in transformed space, the same form as Burrows' Delta
// generalized beyond function words.
//
//	d = (1/k) * sum |deviation_i|   over the k features a segment makes available
//
// # Absolute values, and why the mean is over k and not over the manifest
//
// Delta averages |z|. Averaging signed deviations would let a feature above the
// author's usual cancel one below it, so a segment wrong in both directions
// would score as typical. That is not a subtle failure; it is the statistic
// measuring something else entirely.
//
// A feature that is undefined on the segment, or whose reference is too small,
// is excluded from the numerator AND the denominator. Averaging it in as a zero
// would read as "exactly typical" — the single most misleading value available
// for something that was never measured.
//
// # z_max is struck (REVIEW Round 8)
//
// Winsorization at a fixed z_max was specified when deviations were unbounded
// standardized values, where one broken feature really could dominate. Round 7
// made them bounded by construction: the plotting position caps every deviation
// at Phi^-1(1 - 1/2m), so under uniform weights a single feature's largest
// possible share of d is its own cap over k.
//
// The arithmetic settled it. A conventional z_max = 3 does not bind until a
// feature carries 370 reference values, against an illustrative reference size
// of thirty; set low enough to bind at realistic sizes — 2.0 binds from
// twenty-one — it discards evidence the reference does support, and does so with
// one flat constant while the existing bound already scales per feature with the
// evidence behind it. So there is no winsorization step here, and no z_max.
//
// # Tiers come from the manifest
//
// ADR 0003 tiers features by the sample size they need. `TierB` is not a
// declared constant: the manifest holds six Tier A features and nothing else,
// and an empty tier is one whose minimum can never be met.
//
// So the tier set is read off the manifest. Today that resolves to one tier and
// there is nothing to blend. The day a Tier B feature is added it resolves to
// two, with the per-tier minimum and the partial-score rule already in force.
//
// A tier's minimum is a MAJORITY of its manifest features — stated as a
// proportion rather than a count so it does not silently weaken as the manifest
// grows. Six features require four. No tier meeting its minimum is insufficient
// evidence: no d, no band, and per ADR 0006 `rewrite` passes the segment through
// untouched.
//
// # d carries the features that produced it
//
// ADR 0006's acceptance loop compares d(candidate) <= d(current) - epsilon. A
// rewrite can change which features are available — a candidate long enough to
// define one the original was not, or short enough to lose one. Two means over
// different feature sets are not on the same scale, and comparing them would let
// the loop accept a rewrite that moved only the denominator. The contributing
// set travels with the number so that comparison can be refused.

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// deviationsOf builds a well-formed Deviations carrying one value per manifest
// feature, in manifest order. Fewer values than the manifest leaves the
// remainder undefined for a stated reason.
func deviationsOf(values ...float64) deviation.Deviations {
	order := manifestOrder()
	out := deviation.Deviations{
		ProfileID:             "profile-under-test",
		ReferenceID:           "reference-under-test",
		FeatureManifestDigest: features.ManifestDigest(),
		Split:                 corpus.Test,
		Values:                make([]deviation.Deviation, 0, len(order)),
	}
	for i, id := range order {
		if i < len(values) {
			out.Values = append(out.Values, deviation.Deviation{
				Feature: id,
				Value:   values[i],
				Defined: true,
			})
			continue
		}
		out.Values = append(out.Values, deviation.Deviation{
			Feature: id,
			Reason:  deviation.ReasonReferenceTooSmall,
		})
	}
	return out
}

func distanceOf(t *testing.T, d deviation.Deviations) deviation.Distance {
	t.Helper()
	got, err := d.Distance()
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	return got
}

// tierOf finds one tier's availability accounting.
func tierOf(t *testing.T, d deviation.Distance, tier features.Tier) deviation.TierAvailability {
	t.Helper()
	for _, got := range d.Tiers {
		if got.Tier == tier {
			return got
		}
	}
	t.Fatalf("distance has no accounting for tier %q", tier)
	return deviation.TierAvailability{}
}

// manifestTiers is the distinct tier set the manifest declares, in the order
// features first mention them.
func manifestTiers() []features.Tier {
	seen := make(map[features.Tier]bool)
	out := []features.Tier{}
	for _, definition := range features.Definitions() {
		if !seen[definition.CandidateTier] {
			seen[definition.CandidateTier] = true
			out = append(out, definition.CandidateTier)
		}
	}
	return out
}

// tierFeatures counts the manifest features in one tier.
func tierFeatures(tier features.Tier) int {
	n := 0
	for _, definition := range features.Definitions() {
		if definition.CandidateTier == tier {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// The arithmetic
// ---------------------------------------------------------------------------

// Six deviations, all defined:
//
//	|0.5| + |-1.5| + |0.25| + |-0.75| + |1.0| + |-0.5| = 4.5
//	d = 4.5 / 6 = 0.75
func TestDistanceIsTheMeanOfAbsoluteDeviations(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, -1.5, 0.25, -0.75, 1.0, -0.5))

	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if !closeTo(got.Value, 0.75) {
		t.Errorf("d = %v, want 0.75", got.Value)
	}
}

// The absolute value is the whole difference between Delta's statistic and a
// signed mean. Three features above the author's usual and three below, each by
// the same amount, is a segment that departs on every feature — not a typical
// one. A signed mean returns exactly 0 here.
func TestDistanceDoesNotLetSignsCancel(t *testing.T) {
	got := distanceOf(t, deviationsOf(1, -1, 1, -1, 1, -1))

	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if got.Value == 0 {
		t.Fatalf("d = 0 for a segment that departs on all six features; the signs cancelled")
	}
	if !closeTo(got.Value, 1.0) {
		t.Errorf("d = %v, want 1.0", got.Value)
	}
}

// Four of six defined: |0.5| + |1.5| + |0.5| + |1.5| = 4.0 over k = 4, so
// d = 1.0. Averaging the two undefined features in as zeros gives 4.0/6 =
// 0.6666..., which is both wrong and lower — an unmeasured feature would make a
// segment look closer to the author.
func TestDistanceExcludesUndefinedFeaturesFromTheDenominator(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5))

	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if closeTo(got.Value, 4.0/6.0) {
		t.Fatalf("d = %v, which is the sum divided by the whole manifest; undefined features were averaged in as zeros", got.Value)
	}
	if !closeTo(got.Value, 1.0) {
		t.Errorf("d = %v, want 1.0", got.Value)
	}
}

// d is a mean of absolute values, so it cannot be negative, and it is zero only
// when every contributing deviation is zero.
func TestDistanceIsZeroOnlyWhenEveryDeviationIsZero(t *testing.T) {
	zero := distanceOf(t, deviationsOf(0, 0, 0, 0, 0, 0))
	if !zero.Defined {
		t.Fatalf("undefined: %v", zero.Reason)
	}
	if zero.Value != 0 {
		t.Errorf("d = %v for six zero deviations, want exactly 0", zero.Value)
	}

	one := distanceOf(t, deviationsOf(0, 0, 0, 0, 0, -0.6))
	if one.Value <= 0 {
		t.Errorf("d = %v with one nonzero deviation, want positive", one.Value)
	}
	if !closeTo(one.Value, 0.1) {
		t.Errorf("d = %v, want 0.6/6 = 0.1", one.Value)
	}
}

// ---------------------------------------------------------------------------
// Tier availability
// ---------------------------------------------------------------------------

// The tier set is read off the manifest rather than enumerated in scoring code.
// Both halves are asserted: the derivation, so a manifest that grows a tier is
// covered without a code change, and the concrete v1 position, so a manifest
// change cannot quietly alter what `d` means without this test noticing.
func TestTiersComeFromTheManifest(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5, 0.5, 1.5))

	tiers := make([]features.Tier, 0, len(got.Tiers))
	for _, tier := range got.Tiers {
		tiers = append(tiers, tier.Tier)
	}
	if want := manifestTiers(); !reflect.DeepEqual(tiers, want) {
		t.Errorf("tiers = %v, want the manifest's %v", tiers, want)
	}

	// The v1 position, asserted concretely.
	if len(tiers) != 1 || tiers[0] != features.TierA {
		t.Fatalf("tiers = %v; v1 declares exactly one tier, A", tiers)
	}
	a := tierOf(t, got, features.TierA)
	if a.Manifest != 6 {
		t.Errorf("tier A has %d manifest features, want 6", a.Manifest)
	}
	if a.Required != 4 {
		t.Errorf("tier A requires %d features, want a majority of six, which is 4", a.Required)
	}
}

// A majority, not half. Three of six is not a majority and must not be treated
// as one — the difference is exactly the case an off-by-one produces, and it
// decides whether a segment is scored at all.
func TestATierMinimumIsAMajorityOfItsManifestFeatures(t *testing.T) {
	n := tierFeatures(features.TierA)
	want := n/2 + 1

	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5, 0.5, 1.5))
	a := tierOf(t, got, features.TierA)
	if a.Required != want {
		t.Errorf("required = %d, want %d for %d manifest features", a.Required, want, n)
	}
	if a.Required*2 <= n {
		t.Errorf("required = %d is not a majority of %d", a.Required, n)
	}
}

// Exactly at the minimum is scored; one below is not. A floor tested only from
// far away is a floor whose position is unknown.
func TestTierMinimumIsMetAtExactlyTheMinimum(t *testing.T) {
	atMinimum := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5))
	a := tierOf(t, atMinimum, features.TierA)
	if !a.Met {
		t.Errorf("four available features of a required four did not meet the minimum")
	}
	if a.Available != 4 {
		t.Errorf("available = %d, want 4", a.Available)
	}
	if !atMinimum.Defined {
		t.Errorf("distance at exactly the minimum is undefined: %v", atMinimum.Reason)
	}

	belowMinimum := distanceOf(t, deviationsOf(0.5, 1.5, 0.5))
	b := tierOf(t, belowMinimum, features.TierA)
	if b.Met {
		t.Errorf("three available features of a required four met the minimum")
	}
	if b.Available != 3 {
		t.Errorf("available = %d, want 3", b.Available)
	}
}

// No tier meeting its minimum is insufficient evidence: no number at all, with
// the reason stated. ADR 0006 turns on this — a segment with no d cannot be
// improved on d, so rewrite passes it through untouched. A zero here would read
// as a perfect score.
func TestNoTierMeetingItsMinimumIsInsufficientEvidence(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5))

	if got.Defined {
		t.Fatalf("d is defined as %v with three of a required four features", got.Value)
	}
	if got.Reason != deviation.ReasonInsufficientEvidence {
		t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonInsufficientEvidence)
	}
	if got.Value != 0 {
		t.Errorf("an undefined d carries %v; it must be zero, never a sentinel", got.Value)
	}
	if len(got.Features) != 0 {
		t.Errorf("an undefined d lists %v as contributing features; want none", got.Features)
	}
	if len(got.ScoredTiers) != 0 {
		t.Errorf("an undefined d lists %v as scored tiers; want none", got.ScoredTiers)
	}
}

// Not one feature available is the same verdict, reached from the other end of
// the range. A guard written for "too few" can still divide by zero at none.
func TestNoAvailableFeaturesIsInsufficientEvidence(t *testing.T) {
	got := distanceOf(t, deviationsOf())

	if got.Defined {
		t.Fatalf("d is defined as %v with no available features", got.Value)
	}
	if got.Reason != deviation.ReasonInsufficientEvidence {
		t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonInsufficientEvidence)
	}
	if math.IsNaN(got.Value) || math.IsInf(got.Value, 0) {
		t.Errorf("d = %v; a zero denominator became a non-finite number", got.Value)
	}
}

// Every tier is accounted for whether or not it was scored, so a report can say
// why a tier did not contribute rather than leaving it absent and unexplained.
func TestEveryTierIsAccountedForEvenWhenUnscored(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5))

	if len(got.Tiers) != len(manifestTiers()) {
		t.Fatalf("got %d tier rows, want %d", len(got.Tiers), len(manifestTiers()))
	}
	a := tierOf(t, got, features.TierA)
	if a.Met {
		t.Errorf("tier A is marked met below its minimum")
	}
	if a.Manifest != 6 || a.Required != 4 || a.Available != 3 {
		t.Errorf("tier A accounting = manifest %d, required %d, available %d; want 6, 4, 3", a.Manifest, a.Required, a.Available)
	}
}

// ---------------------------------------------------------------------------
// The scored tier set, and partial scores
// ---------------------------------------------------------------------------

// v1's manifest declares one tier, so a scored distance covers all of them and
// is not partial. Flagging it partial would claim it is missing a tier that does
// not exist.
func TestAFullyScoredDistanceIsNotPartial(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5, 0.5, 1.5))

	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if got.Partial {
		t.Errorf("a distance over every tier in the manifest is flagged partial")
	}
	if want := manifestTiers(); !reflect.DeepEqual(got.ScoredTiers, want) {
		t.Errorf("scored tiers = %v, want %v", got.ScoredTiers, want)
	}
}

// Partial is a property of the TIER set, not the feature set. Four of six
// features is a thinner measurement, but it is still the whole of tier A, and
// it shares thresholds with any other tier-A score. Conflating the two would
// demand a separate threshold artifact for every distinct feature subset.
func TestPartialTracksTiersNotFeatures(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5))

	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if got.Partial {
		t.Errorf("four of six tier-A features is flagged partial; partial is about tiers, not feature counts")
	}
	if len(got.Features) != 4 {
		t.Errorf("contributing features = %d, want 4", len(got.Features))
	}
}

// ---------------------------------------------------------------------------
// The contributing feature set
// ---------------------------------------------------------------------------

// The set is exact and in manifest order, so a later comparison can test it for
// equality rather than for size.
func TestDistanceListsItsContributingFeaturesInManifestOrder(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5))

	order := manifestOrder()
	want := order[:4]
	if !reflect.DeepEqual(got.Features, want) {
		t.Errorf("features = %v, want %v", got.Features, want)
	}
}

// The finding this field exists for: two distances that are numerically equal
// but rest on different features. ADR 0006's loop compares d(candidate) against
// d(current), and without the contributing set it cannot tell that the
// comparison is meaningless.
func TestEqualDistancesOverDifferentFeaturesAreDistinguishable(t *testing.T) {
	first := deviationsOf(1, 1, 1, 1)

	// The same four |deviations|, on a different four features.
	second := deviationsOf()
	order := manifestOrder()
	for i := range second.Values {
		if i < 2 {
			continue
		}
		second.Values[i] = deviation.Deviation{Feature: order[i], Value: 1, Defined: true}
	}

	a := distanceOf(t, first)
	b := distanceOf(t, second)

	if !a.Defined || !b.Defined {
		t.Fatalf("undefined: %v and %v", a.Reason, b.Reason)
	}
	if !closeTo(a.Value, b.Value) {
		t.Fatalf("the two distances are %v and %v; this test needs them equal to be meaningful", a.Value, b.Value)
	}
	if reflect.DeepEqual(a.Features, b.Features) {
		t.Errorf("two distances over different feature sets report the same contributing set %v", a.Features)
	}
}

// ---------------------------------------------------------------------------
// Provenance and the declared weighting
// ---------------------------------------------------------------------------

// The distance inherits its lineage from the deviations, so a report can name
// the profile, the reference and the split behind the number.
func TestDistanceCarriesItsProvenance(t *testing.T) {
	source := deviationsOf(0.5, 1.5, 0.5, 1.5, 0.5, 1.5)
	got := distanceOf(t, source)

	if got.ProfileID != source.ProfileID {
		t.Errorf("ProfileID = %q, want %q", got.ProfileID, source.ProfileID)
	}
	if got.ReferenceID != source.ReferenceID {
		t.Errorf("ReferenceID = %q, want %q", got.ReferenceID, source.ReferenceID)
	}
	if got.FeatureManifestDigest != source.FeatureManifestDigest {
		t.Errorf("FeatureManifestDigest = %q, want %q", got.FeatureManifestDigest, source.FeatureManifestDigest)
	}
	if got.Split != source.Split {
		t.Errorf("Split = %q, want %q", got.Split, source.Split)
	}
}

// Section 2 requires the weighting scheme to be recorded and versioned rather
// than inferred from the code, because uniform, expert, inverse-redundancy and
// learned weights are materially different models. A fitted scheme must not be
// servable from a cache built under this one.
func TestDistanceRecordsTheDeclaredWeighting(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5, 0.5, 1.5))

	if got.WeightScheme != deviation.WeightSchemeUniform {
		t.Errorf("WeightScheme = %q, want %q", got.WeightScheme, deviation.WeightSchemeUniform)
	}
	if got.Algorithm != deviation.DistanceAlgorithm {
		t.Errorf("Algorithm = %q, want %q", got.Algorithm, deviation.DistanceAlgorithm)
	}

	// The constants are asserted against their literal values as well, because
	// comparing a field to the constant it was set from proves only that one
	// assignment happened. These strings go into the scoring cache identity, so
	// changing either must be a deliberate act that fails a test first. Scheme
	// and version travel as one versioned identifier, matching every other
	// algorithm name in this repository.
	if deviation.WeightSchemeUniform != "uniform-v1" {
		t.Errorf("WeightSchemeUniform = %q, want %q", deviation.WeightSchemeUniform, "uniform-v1")
	}
	if deviation.DistanceAlgorithm != "distance-uniform-mean-v1" {
		t.Errorf("DistanceAlgorithm = %q, want %q", deviation.DistanceAlgorithm, "distance-uniform-mean-v1")
	}
}

// An unscored distance still says how it was computed and where it came from.
// A refusal with no provenance cannot be reported or cached.
func TestAnUndefinedDistanceStillCarriesItsProvenance(t *testing.T) {
	source := deviationsOf(0.5)
	got := distanceOf(t, source)

	if got.Defined {
		t.Fatalf("d is defined as %v", got.Value)
	}
	if got.ProfileID != source.ProfileID || got.ReferenceID != source.ReferenceID {
		t.Errorf("provenance lost on an undefined distance: %q, %q", got.ProfileID, got.ReferenceID)
	}
	if got.WeightScheme != deviation.WeightSchemeUniform || got.Algorithm != deviation.DistanceAlgorithm {
		t.Errorf("method lost on an undefined distance: %q, %q", got.WeightScheme, got.Algorithm)
	}
}

// ---------------------------------------------------------------------------
// Malformed and mismatched input
// ---------------------------------------------------------------------------

func TestDistanceRejectsMalformedDeviations(t *testing.T) {
	order := manifestOrder()

	missing := deviationsOf(1, 1, 1, 1, 1, 1)
	missing.Values = missing.Values[1:]

	duplicate := deviationsOf(1, 1, 1, 1, 1, 1)
	duplicate.Values = append(duplicate.Values, deviation.Deviation{Feature: order[0], Value: 2, Defined: true})

	foreign := deviationsOf(1, 1, 1, 1, 1, 1)
	foreign.Values[0].Feature = "not_a_manifest_feature"

	unknownSplit := deviationsOf(1, 1, 1, 1, 1, 1)
	unknownSplit.Split = "holdout"

	noSplit := deviationsOf(1, 1, 1, 1, 1, 1)
	noSplit.Split = ""

	noReference := deviationsOf(1, 1, 1, 1, 1, 1)
	noReference.ReferenceID = ""

	noProfile := deviationsOf(1, 1, 1, 1, 1, 1)
	noProfile.ProfileID = ""

	noDigest := deviationsOf(1, 1, 1, 1, 1, 1)
	noDigest.FeatureManifestDigest = ""

	staleDigest := deviationsOf(1, 1, 1, 1, 1, 1)
	staleDigest.FeatureManifestDigest = "a-digest-from-another-manifest"

	definedWithReason := deviationsOf(1, 1, 1, 1, 1, 1)
	definedWithReason.Values[2].Reason = deviation.ReasonZeroVariance

	cases := []struct {
		name string
		in   deviation.Deviations
		want error
	}{
		{name: "a missing manifest feature", in: missing, want: deviation.ErrManifestMismatch},
		{name: "a duplicated manifest feature", in: duplicate, want: deviation.ErrManifestMismatch},
		{name: "a feature outside the manifest", in: foreign, want: deviation.ErrManifestMismatch},
		{name: "an unknown split", in: unknownSplit, want: deviation.ErrUnknownSplit},
		{name: "no split recorded", in: noSplit, want: deviation.ErrUnknownSplit},
		{name: "no reference recorded", in: noReference, want: deviation.ErrMissingInput},
		{name: "no profile recorded", in: noProfile, want: deviation.ErrMissingInput},
		{name: "no manifest digest recorded", in: noDigest, want: deviation.ErrMissingInput},
		// Unlike BuildReference, which checks the digest only for agreement,
		// Distance reads the live manifest to group features into tiers. A
		// deviation record measured under another manifest cannot be tiered
		// against this one, so here currency is required.
		{name: "a digest from another manifest", in: staleDigest, want: deviation.ErrManifestMismatch},
		// A defined value carrying a reason is self-contradictory: Reason exists
		// to say why there is no value.
		{name: "a defined deviation carrying a reason", in: definedWithReason, want: deviation.ErrMalformedInput},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.in.Distance(); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// The same non-finite rule as everywhere else in this package: a defined value
// that is NaN or infinite is a corrupt artifact, not an extreme measurement. It
// would otherwise poison the mean silently — one NaN makes d NaN, and one
// infinity makes it infinite, in both cases without any guard firing.
func TestDistanceRejectsNonFiniteDeviations(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		in := deviationsOf(1, 1, 1, 1, 1, 1)
		in.Values[2].Value = bad
		if _, err := in.Distance(); !errors.Is(err, deviation.ErrMalformedInput) {
			t.Errorf("value %v: err = %v, want %v", bad, err, deviation.ErrMalformedInput)
		}
	}
}

// An undefined deviation carrying no reason is a malformed artifact, matching
// Transform. The point of the Reason type is that nothing is undefined without
// saying why.
func TestDistanceRejectsAnUndefinedDeviationWithNoReason(t *testing.T) {
	in := deviationsOf(1, 1, 1, 1, 1, 1)
	in.Values[3] = deviation.Deviation{Feature: in.Values[3].Feature}

	if _, err := in.Distance(); !errors.Is(err, deviation.ErrMalformedInput) {
		t.Errorf("err = %v, want %v", err, deviation.ErrMalformedInput)
	}
}

// An undefined deviation's value is not asked to be finite: it carries a zero it
// never justified, and the non-finite check applies to defined values only.
func TestUndefinedDeviationsAreNotCheckedForFiniteness(t *testing.T) {
	in := deviationsOf(1, 1, 1, 1)
	in.Values[5].Value = math.NaN()

	got, err := in.Distance()
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if !closeTo(got.Value, 1.0) {
		t.Errorf("d = %v, want 1.0", got.Value)
	}
}

// ---------------------------------------------------------------------------
// End to end, on real extracted text
// ---------------------------------------------------------------------------

// Every test above builds deviations literally. This one runs prose through the
// whole chain — Admit, Extract, Standardize, BuildReference, Transform, Distance
// — so a change anywhere upstream cannot leave this arithmetic looking healthy.
//
// d is a mean of absolute deviations, each bounded by its feature's reference
// cap, so d cannot exceed the largest of those caps.
func TestDistanceEndToEndOnRealText(t *testing.T) {
	admit := func(src string) features.Vector {
		t.Helper()
		doc, err := text.Admit([]byte(src))
		if err != nil {
			t.Fatalf("Admit(%q): %v", src, err)
		}
		return features.Extract(doc.Tokens())
	}

	p := profileWith()
	calibrate := []string{
		"The argument rests on a distinction that the record does not support, and the record is all we have.",
		"It is not that the claim is false; it is that nothing in the material would tell us either way.",
		"Every reading of the passage runs into the same wall, which is that the author never says it.",
		"We can grant the premise and still find that the conclusion does not follow from it.",
		"There is a version of this argument that works, but it is not the one on the page.",
	}

	segments := make([]deviation.Standardization, 0, len(calibrate))
	for _, src := range calibrate {
		s, err := deviation.Standardize(admit(src), p, corpus.Calibrate)
		if err != nil {
			t.Fatalf("Standardize(%q): %v", src, err)
		}
		segments = append(segments, s)
	}

	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}

	query, err := deviation.Standardize(admit("Whether the passage supports the reading is a question the passage itself cannot settle."), p, corpus.Test)
	if err != nil {
		t.Fatalf("Standardize the query: %v", err)
	}
	deviations, err := ref.Transform(query)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	got, err := deviations.Distance()
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if !got.Defined {
		t.Fatalf("d is undefined on real text: %v", got.Reason)
	}
	if math.IsNaN(got.Value) || math.IsInf(got.Value, 0) {
		t.Fatalf("d = %v on real text", got.Value)
	}
	if got.Value < 0 {
		t.Errorf("d = %v, which is negative", got.Value)
	}

	var largest float64
	for _, id := range got.Features {
		bound, ok := ref.Cap(id)
		if !ok {
			t.Fatalf("%s contributed to d but has no reference cap", id)
		}
		if bound > largest {
			largest = bound
		}
	}
	if got.Value > largest+1e-12 {
		t.Errorf("d = %v exceeds the largest contributing feature's cap %v", got.Value, largest)
	}
	if got.Split != corpus.Test {
		t.Errorf("Split = %q, want %q", got.Split, corpus.Test)
	}
	if got.ReferenceID != ref.ID {
		t.Errorf("ReferenceID = %q, want %q", got.ReferenceID, ref.ID)
	}
}

// Insufficient evidence, reached through the real chain rather than by
// constructing the refusal directly.
//
// Three of the six manifest features are given an undefined profile statistic.
// Standardize marks those undefined, so no Calibrate segment contributes a value
// for them, so their references are empty and their deviations come back
// undefined. Three available features against a required four is below tier A's
// majority, and the whole segment is unscoreable — which is what ADR 0006 needs
// in order to pass it through untouched rather than call an absent measurement
// an improvement.
//
// The stats are written with N below MinObservations to mirror what
// profile.Build produces, but what this test exercises is the Defined flag, not
// the derivation that sets it. profile.Build owns that relationship and tests it
// there; asserting it here would be asserting another package's arithmetic
// through a fixture.
func TestInsufficientEvidenceEndToEnd(t *testing.T) {
	admit := func(src string) features.Vector {
		t.Helper()
		doc, err := text.Admit([]byte(src))
		if err != nil {
			t.Fatalf("Admit(%q): %v", src, err)
		}
		return features.Extract(doc.Tokens())
	}

	unfit := []features.ID{features.SemicolonDensity, features.ColonDensity, features.ClauseMarkerRate}
	stats := make([]profile.Stats, 0, len(unfit))
	for _, id := range unfit {
		stats = append(stats, profile.Stats{Feature: id, N: 3, MinObservations: 20})
	}
	p := profileWith(stats...)

	calibrate := []string{
		"The argument rests on a distinction that the record does not support, and the record is all we have.",
		"It is not that the claim is false; it is that nothing in the material would tell us either way.",
		"Every reading of the passage runs into the same wall, which is that the author never says it.",
		"We can grant the premise and still find that the conclusion does not follow from it.",
	}
	segments := make([]deviation.Standardization, 0, len(calibrate))
	for _, src := range calibrate {
		s, err := deviation.Standardize(admit(src), p, corpus.Calibrate)
		if err != nil {
			t.Fatalf("Standardize(%q): %v", src, err)
		}
		segments = append(segments, s)
	}

	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	for _, id := range unfit {
		if ref.Size(id) != 0 {
			t.Fatalf("%s has %d reference values; this test needs it to have none", id, ref.Size(id))
		}
	}

	query, err := deviation.Standardize(admit("Whether the passage supports the reading is a question the passage itself cannot settle."), p, corpus.Test)
	if err != nil {
		t.Fatalf("Standardize the query: %v", err)
	}
	deviations, err := ref.Transform(query)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	got, err := deviations.Distance()
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if got.Defined {
		t.Fatalf("d = %v with three of a required four features available", got.Value)
	}
	if got.Reason != deviation.ReasonInsufficientEvidence {
		t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonInsufficientEvidence)
	}
	a := tierOf(t, got, features.TierA)
	if a.Available != 3 {
		t.Errorf("available = %d, want 3", a.Available)
	}
	if a.Met {
		t.Errorf("tier A is marked met with three of a required four")
	}
}

// ---------------------------------------------------------------------------
// No winsorization: the strike of z_max, frozen
// ---------------------------------------------------------------------------

// Every other fixture in this file has magnitude at most 1.5, so an
// implementation that still clipped at the obsolete conventional z_max = 3.0
// would pass all of them. This one does not:
//
//	|4.0| + |-5.0| + |3.5| + |-6.0| + |4.5| + |-1.0| = 24.0, d = 4.0
//
// Clipping at 3.0 first gives (3 + 3 + 3 + 3 + 3 + 1)/6 = 2.6666..., a visibly
// different number. Deviations of this size are not hypothetical: the rank cap
// is Phi^-1(1 - 1/2m), which passes 3.0 once a feature carries 370 reference
// values, and a large Calibrate split reaches that.
func TestDistanceDoesNotWinsorize(t *testing.T) {
	got := distanceOf(t, deviationsOf(4.0, -5.0, 3.5, -6.0, 4.5, -1.0))

	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if closeTo(got.Value, 16.0/6.0) {
		t.Fatalf("d = %v, which is the mean after clipping at 3.0; z_max is struck", got.Value)
	}
	if !closeTo(got.Value, 4.0) {
		t.Errorf("d = %v, want 4.0", got.Value)
	}
}

// ---------------------------------------------------------------------------
// Uniform weights, as behavior rather than as a label
// ---------------------------------------------------------------------------

// Recording WeightScheme says what the model claims; this says the arithmetic
// agrees. The same lone deviation is placed at each manifest position in turn
// and must produce the same d every time. Any per-feature weighting — expert,
// inverse-redundancy, or a stray coefficient — gives a different answer in at
// least one position, and a single-position test cannot see it.
func TestUniformWeightsGiveEveryFeatureEqualInfluence(t *testing.T) {
	order := manifestOrder()

	for position, id := range order {
		t.Run(string(id), func(t *testing.T) {
			in := deviationsOf()
			for i := range in.Values {
				in.Values[i] = deviation.Deviation{Feature: order[i], Value: 0, Defined: true}
			}
			in.Values[position].Value = -0.6

			got := distanceOf(t, in)
			if !got.Defined {
				t.Fatalf("undefined: %v", got.Reason)
			}
			if !closeTo(got.Value, 0.1) {
				t.Errorf("d = %v with the sole deviation at position %d, want 0.6/6 = 0.1", got.Value, position)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The tier plan, over manifests this one is not
// ---------------------------------------------------------------------------

// v1's manifest declares one tier of six features, so every assertion made
// through Distance is consistent with an implementation that hardcodes "tier A,
// six features, four required". TierPlan is the same derivation as a pure
// function over an arbitrary manifest, which is where the general rule can
// actually be exercised.
//
// The scoring policy over several tiers is exercised through DistanceOver, which
// is this same computation against an explicit manifest. Distance is
// DistanceOver against the live one. TierPlan alone would prove only the
// accounting: an implementation could report a correct plan and still average in
// deviations from a tier that missed its minimum.

func synthetic(tier features.Tier, ids ...features.ID) []features.Definition {
	out := make([]features.Definition, 0, len(ids))
	for _, id := range ids {
		out = append(out, features.Definition{ID: id, CandidateTier: tier})
	}
	return out
}

func availabilityOf(t *testing.T, plan []deviation.TierAvailability, tier features.Tier) deviation.TierAvailability {
	t.Helper()
	for _, got := range plan {
		if got.Tier == tier {
			return got
		}
	}
	t.Fatalf("plan has no row for tier %q", tier)
	return deviation.TierAvailability{}
}

// The majority rule at sizes the live manifest will never have. n/2 rounded down
// plus one: two of three, three of five, four of seven — never half.
func TestTierMinimumIsAMajorityAtEverySize(t *testing.T) {
	cases := []struct{ manifest, required int }{
		{1, 1}, {2, 2}, {3, 2}, {4, 3}, {5, 3}, {6, 4}, {7, 4}, {8, 5}, {20, 11},
	}

	for _, c := range cases {
		ids := make([]features.ID, 0, c.manifest)
		for i := 0; i < c.manifest; i++ {
			ids = append(ids, features.ID("f"+string(rune('a'+i%26))+string(rune('0'+i/26))))
		}
		plan := deviation.TierPlan(synthetic("X", ids...), nil)
		row := availabilityOf(t, plan, "X")

		if row.Manifest != c.manifest {
			t.Errorf("manifest = %d, want %d", row.Manifest, c.manifest)
		}
		if row.Required != c.required {
			t.Errorf("%d features require %d, want %d", c.manifest, row.Required, c.required)
		}
		if row.Required*2 <= c.manifest {
			t.Errorf("%d of %d is not a majority", row.Required, c.manifest)
		}
	}
}

// Tiers appear in the order the manifest first mentions them, and each counts
// only its own features.
func TestTierPlanGroupsByTierInManifestOrder(t *testing.T) {
	definitions := append(
		synthetic("A", "a1", "a2", "a3"),
		synthetic("B", "b1", "b2")...,
	)
	plan := deviation.TierPlan(definitions, nil)

	tiers := make([]features.Tier, 0, len(plan))
	for _, row := range plan {
		tiers = append(tiers, row.Tier)
	}
	if want := []features.Tier{"A", "B"}; !reflect.DeepEqual(tiers, want) {
		t.Fatalf("tiers = %v, want %v", tiers, want)
	}
	if a := availabilityOf(t, plan, "A"); a.Manifest != 3 || a.Required != 2 {
		t.Errorf("tier A = manifest %d, required %d; want 3, 2", a.Manifest, a.Required)
	}
	if b := availabilityOf(t, plan, "B"); b.Manifest != 2 || b.Required != 2 {
		t.Errorf("tier B = manifest %d, required %d; want 2, 2", b.Manifest, b.Required)
	}
}

// Every combination of tiers meeting and missing their minimums, which the live
// one-tier manifest cannot produce.
func TestTierPlanAcrossMultipleTiers(t *testing.T) {
	definitions := append(
		synthetic("A", "a1", "a2", "a3"),
		synthetic("B", "b1", "b2")...,
	)

	cases := []struct {
		name      string
		available []features.ID
		metA      bool
		metB      bool
	}{
		{name: "both tiers met", available: []features.ID{"a1", "a2", "b1", "b2"}, metA: true, metB: true},
		{name: "only A met", available: []features.ID{"a1", "a2", "a3", "b1"}, metA: true, metB: false},
		{name: "only B met", available: []features.ID{"a1", "b1", "b2"}, metA: false, metB: true},
		{name: "neither met", available: []features.ID{"a1", "b1"}, metA: false, metB: false},
		{name: "nothing available", available: nil, metA: false, metB: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			available := make(map[features.ID]bool, len(c.available))
			for _, id := range c.available {
				available[id] = true
			}
			plan := deviation.TierPlan(definitions, available)

			if got := availabilityOf(t, plan, "A"); got.Met != c.metA {
				t.Errorf("tier A met = %v (available %d of a required %d), want %v", got.Met, got.Available, got.Required, c.metA)
			}
			if got := availabilityOf(t, plan, "B"); got.Met != c.metB {
				t.Errorf("tier B met = %v (available %d of a required %d), want %v", got.Met, got.Available, got.Required, c.metB)
			}
		})
	}
}

// A feature available but absent from the manifest is not counted toward any
// tier. Counting it would let a stale artifact inflate a tier past its minimum.
func TestTierPlanIgnoresFeaturesOutsideTheManifest(t *testing.T) {
	definitions := synthetic("A", "a1", "a2", "a3")
	plan := deviation.TierPlan(definitions, map[features.ID]bool{"a1": true, "z9": true})

	got := availabilityOf(t, plan, "A")
	if got.Available != 1 {
		t.Errorf("available = %d, want 1; a feature outside the manifest was counted", got.Available)
	}
	if got.Met {
		t.Errorf("tier A is met on one available feature of a required two")
	}
}

// Distance's own accounting is TierPlan over the live manifest. Asserting the
// two agree is what stops Distance growing a second, divergent derivation.
func TestDistanceTierAccountingIsTheManifestTierPlan(t *testing.T) {
	got := distanceOf(t, deviationsOf(0.5, 1.5, 0.5, 1.5))

	available := make(map[features.ID]bool, len(got.Features))
	for _, id := range got.Features {
		available[id] = true
	}
	want := deviation.TierPlan(features.Definitions(), available)

	if !reflect.DeepEqual(got.Tiers, want) {
		t.Errorf("distance tiers = %+v, want the manifest plan %+v", got.Tiers, want)
	}
}

// ---------------------------------------------------------------------------
// The scoring policy across tiers
// ---------------------------------------------------------------------------

// syntheticDeviations builds a deviation record over an arbitrary feature list,
// so the tier rules can be exercised on manifests the live one is not.
func syntheticDeviations(defined map[features.ID]float64, order ...features.ID) deviation.Deviations {
	out := deviation.Deviations{
		ProfileID:             "profile-under-test",
		ReferenceID:           "reference-under-test",
		FeatureManifestDigest: "a-synthetic-manifest-digest",
		Split:                 corpus.Test,
		Values:                make([]deviation.Deviation, 0, len(order)),
	}
	for _, id := range order {
		if v, ok := defined[id]; ok {
			out.Values = append(out.Values, deviation.Deviation{Feature: id, Value: v, Defined: true})
			continue
		}
		out.Values = append(out.Values, deviation.Deviation{Feature: id, Reason: deviation.ReasonReferenceTooSmall})
	}
	return out
}

// Tier A holds a1, a2, a3 and requires two; tier B holds b1, b2 and requires
// two. Each case is chosen so that including a feature from an unmet tier gives
// a visibly different number rather than a near miss.
func TestDistanceOverAcrossTiers(t *testing.T) {
	order := []features.ID{"a1", "a2", "a3", "b1", "b2"}
	definitions := append(synthetic("A", "a1", "a2", "a3"), synthetic("B", "b1", "b2")...)

	cases := []struct {
		name        string
		defined     map[features.ID]float64
		wantValue   float64
		wantDefined bool
		wantPartial bool
		wantTiers   []features.Tier
		wantFeature []features.ID
		// availA and availB are the defined features in each tier, asserted as
		// literals so the row comparison below is not the only witness.
		availA, availB int
		// wrongValue is what including every defined deviation regardless of
		// tier would produce.
		wrongValue float64
	}{
		{
			name:        "both tiers met",
			availA:      2,
			availB:      2,
			defined:     map[features.ID]float64{"a1": 1, "a2": 1, "b1": 2, "b2": 2},
			wantValue:   1.5, // (1+1+2+2)/4
			wantDefined: true,
			wantPartial: false,
			wantTiers:   []features.Tier{"A", "B"},
			wantFeature: []features.ID{"a1", "a2", "b1", "b2"},
			wrongValue:  1.5,
		},
		{
			name:        "only tier A met",
			availA:      3,
			availB:      1,
			defined:     map[features.ID]float64{"a1": 1, "a2": 1, "a3": 1, "b1": 4},
			wantValue:   1.0, // (1+1+1)/3, with b1 excluded although defined
			wantDefined: true,
			wantPartial: true,
			wantTiers:   []features.Tier{"A"},
			wantFeature: []features.ID{"a1", "a2", "a3"},
			wrongValue:  1.75, // (1+1+1+4)/4
		},
		{
			name:        "only tier B met",
			availA:      1,
			availB:      2,
			defined:     map[features.ID]float64{"a1": 9, "b1": 0.5, "b2": 1.5},
			wantValue:   1.0, // (0.5+1.5)/2, with a1 excluded although defined
			wantDefined: true,
			wantPartial: true,
			wantTiers:   []features.Tier{"B"},
			wantFeature: []features.ID{"b1", "b2"},
			wrongValue:  11.0 / 3.0,
		},
		{
			name:        "neither tier met",
			availA:      1,
			availB:      1,
			defined:     map[features.ID]float64{"a1": 1, "b1": 1},
			wantDefined: false,
			wantPartial: false,
			wantTiers:   nil,
			wantFeature: nil,
			wrongValue:  1.0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := syntheticDeviations(c.defined, order...).DistanceOver(definitions)
			if err != nil {
				t.Fatalf("DistanceOver: %v", err)
			}

			if got.Defined != c.wantDefined {
				t.Fatalf("Defined = %v (reason %q), want %v", got.Defined, got.Reason, c.wantDefined)
			}
			if !c.wantDefined {
				if got.Reason != deviation.ReasonInsufficientEvidence {
					t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonInsufficientEvidence)
				}
				if got.Value != 0 {
					t.Errorf("an undefined d carries %v, want 0", got.Value)
				}
			} else {
				if c.wantValue != c.wrongValue && closeTo(got.Value, c.wrongValue) {
					t.Fatalf("d = %v, which is the mean over every defined deviation; features from a tier that missed its minimum were included", got.Value)
				}
				if !closeTo(got.Value, c.wantValue) {
					t.Errorf("d = %v, want %v", got.Value, c.wantValue)
				}
			}

			if got.Partial != c.wantPartial {
				t.Errorf("Partial = %v, want %v", got.Partial, c.wantPartial)
			}
			if len(got.ScoredTiers) != len(c.wantTiers) || (len(c.wantTiers) > 0 && !reflect.DeepEqual(got.ScoredTiers, c.wantTiers)) {
				t.Errorf("scored tiers = %v, want %v", got.ScoredTiers, c.wantTiers)
			}
			if len(got.Features) != len(c.wantFeature) || (len(c.wantFeature) > 0 && !reflect.DeepEqual(got.Features, c.wantFeature)) {
				t.Errorf("features = %v, want %v", got.Features, c.wantFeature)
			}

			// Every tier is accounted for whether or not it scored, and the
			// rows are compared in full: a count alone would accept duplicate
			// or fabricated rows. TierPlan is the oracle here and is itself
			// tested against literal expectations over this same manifest.
			available := make(map[features.ID]bool, len(c.defined))
			for id := range c.defined {
				available[id] = true
			}
			if want := deviation.TierPlan(definitions, available); !reflect.DeepEqual(got.Tiers, want) {
				t.Errorf("tier rows = %+v, want %+v", got.Tiers, want)
			}
			if row := availabilityOf(t, got.Tiers, "A"); row.Available != c.availA {
				t.Errorf("tier A available = %d, want %d", row.Available, c.availA)
			}
			if row := availabilityOf(t, got.Tiers, "B"); row.Available != c.availB {
				t.Errorf("tier B available = %d, want %d", row.Available, c.availB)
			}
		})
	}
}

// An insufficient-evidence verdict is not a partial score. Partial means "scored
// on some of the tiers"; no score at all is a different outcome and a consumer
// that treated the two alike would report a band for a segment that has none.
func TestInsufficientEvidenceIsNotPartial(t *testing.T) {
	definitions := append(synthetic("A", "a1", "a2", "a3"), synthetic("B", "b1", "b2")...)
	got, err := syntheticDeviations(map[features.ID]float64{"a1": 1}, "a1", "a2", "a3", "b1", "b2").DistanceOver(definitions)
	if err != nil {
		t.Fatalf("DistanceOver: %v", err)
	}

	if got.Defined {
		t.Fatalf("d is defined as %v", got.Value)
	}
	if got.Partial {
		t.Errorf("an unscoreable segment is flagged as a partial score")
	}
}

// Distance is DistanceOver against the live manifest, and must not become a
// second implementation of the same rules.
func TestDistanceIsDistanceOverTheLiveManifest(t *testing.T) {
	in := deviationsOf(0.5, -1.5, 0.25, -0.75, 1.0, -0.5)

	direct := distanceOf(t, in)
	over, err := in.DistanceOver(features.Definitions())
	if err != nil {
		t.Fatalf("DistanceOver: %v", err)
	}
	if !reflect.DeepEqual(direct, over) {
		t.Errorf("Distance() = %+v, DistanceOver(live manifest) = %+v", direct, over)
	}
}

// DistanceOver checks the record against the manifest it is given. Distance adds
// the currency check, because it is the entry point that reads the live manifest.
func TestDistanceOverChecksAgainstTheManifestItIsGiven(t *testing.T) {
	definitions := synthetic("A", "a1", "a2", "a3")

	t.Run("a feature outside the given manifest", func(t *testing.T) {
		in := syntheticDeviations(map[features.ID]float64{"a1": 1, "a2": 1, "z9": 1}, "a1", "a2", "z9")
		if _, err := in.DistanceOver(definitions); !errors.Is(err, deviation.ErrManifestMismatch) {
			t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
		}
	})

	t.Run("a feature missing from the record", func(t *testing.T) {
		in := syntheticDeviations(map[features.ID]float64{"a1": 1, "a2": 1}, "a1", "a2")
		if _, err := in.DistanceOver(definitions); !errors.Is(err, deviation.ErrManifestMismatch) {
			t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
		}
	})

	t.Run("no definitions at all", func(t *testing.T) {
		in := syntheticDeviations(map[features.ID]float64{"a1": 1}, "a1")
		if _, err := in.DistanceOver(nil); !errors.Is(err, deviation.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, deviation.ErrMissingInput)
		}
	})

	// Validation is shared, so every guard Distance applies must fire here too.
	// An implementation that validated only at the live-manifest entry point
	// would leave DistanceOver collapsing duplicates and averaging in NaN.
	t.Run("shared validation", func(t *testing.T) {
		complete := func() deviation.Deviations {
			return syntheticDeviations(map[features.ID]float64{"a1": 1, "a2": 1, "a3": 1}, "a1", "a2", "a3")
		}

		duplicate := complete()
		duplicate.Values = append(duplicate.Values, deviation.Deviation{Feature: "a1", Value: 9, Defined: true})

		nonFinite := complete()
		nonFinite.Values[1].Value = math.NaN()

		infinite := complete()
		infinite.Values[2].Value = math.Inf(-1)

		positiveInfinite := complete()
		positiveInfinite.Values[0].Value = math.Inf(1)

		definedWithReason := complete()
		definedWithReason.Values[0].Reason = deviation.ReasonZeroVariance

		undefinedWithoutReason := complete()
		undefinedWithoutReason.Values[1] = deviation.Deviation{Feature: "a2"}

		unknownSplit := complete()
		unknownSplit.Split = "holdout"

		noSplit := complete()
		noSplit.Split = ""

		noReference := complete()
		noReference.ReferenceID = ""

		noProfile := complete()
		noProfile.ProfileID = ""

		noDigest := complete()
		noDigest.FeatureManifestDigest = ""

		cases := []struct {
			name string
			in   deviation.Deviations
			want error
		}{
			{name: "a duplicated feature", in: duplicate, want: deviation.ErrManifestMismatch},
			{name: "a NaN value", in: nonFinite, want: deviation.ErrMalformedInput},
			{name: "a negative infinity", in: infinite, want: deviation.ErrMalformedInput},
			{name: "a positive infinity", in: positiveInfinite, want: deviation.ErrMalformedInput},
			{name: "a defined value carrying a reason", in: definedWithReason, want: deviation.ErrMalformedInput},
			{name: "an undefined value with no reason", in: undefinedWithoutReason, want: deviation.ErrMalformedInput},
			{name: "an unknown split", in: unknownSplit, want: deviation.ErrUnknownSplit},
			{name: "no split recorded", in: noSplit, want: deviation.ErrUnknownSplit},
			{name: "no reference recorded", in: noReference, want: deviation.ErrMissingInput},
			{name: "no profile recorded", in: noProfile, want: deviation.ErrMissingInput},
			{name: "no manifest digest recorded", in: noDigest, want: deviation.ErrMissingInput},
		}

		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if _, err := c.in.DistanceOver(definitions); !errors.Is(err, c.want) {
					t.Errorf("err = %v, want %v", err, c.want)
				}
			})
		}
	})

	// A synthetic digest is not the live one, and DistanceOver must not care:
	// it was handed the manifest to check against.
	t.Run("a digest that is not the live manifest's", func(t *testing.T) {
		in := syntheticDeviations(map[features.ID]float64{"a1": 1, "a2": 1, "a3": 1}, "a1", "a2", "a3")
		if _, err := in.DistanceOver(definitions); err != nil {
			t.Errorf("DistanceOver refused a synthetic manifest over its digest: %v", err)
		}
	})
}
