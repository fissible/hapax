package deviation_test

// The per-feature deviation: length-aware standardization, then an
// empirical-CDF rank transform of that quantity.
//
// DESIGN Section 2 names two corrections and REVIEW Round 5 fixed the order
// they compose in, because the orderings are different estimators:
//
//  1. Standardize against the profile, with a denominator that combines the
//     profile variance with the feature's sampling variance AT THE OBSERVED
//     SEGMENT LENGTH. Dividing by the profile SD alone understates short-segment
//     noise and manufactures confident deviations.
//
//  2. Rank THAT quantity against the author's Calibrate distribution. Ranking
//     the raw feature value instead would drop correction 1 entirely: segment
//     length would never enter, and a short segment could reach an extreme
//     percentile on sampling noise alone.
//
// REVIEW Round 7 then settled what the transform returns. An empirical-CDF rank
// is a percentile in [0,1], and two later rules presuppose otherwise: `z_max`
// winsorization is vacuous on a bounded percentile, and `d` is declared to be
// "Manhattan in transformed space, the same form as Burrows' Delta", which
// averages |z|. So the rank is mapped back through the normal quantile
// function.
//
// # The plotting position, and why it is a declared choice
//
// An empirical CDF over n reference values returns 0 below all of them and 1
// above, and the normal quantile is infinite at both. At thirty reference
// segments that is one segment in thirty per tail, not an edge case.
//
// The segment is therefore ranked within the reference PLUS ITSELF — m = n + 1
// values — at the (i - 1/2)/m position, ties taking their midrank. Ranking
// against the n reference values alone cannot be made symmetric: n points leave
// n+1 gaps, so any position over n is short a cell at one end.
//
// Written out, with L values below the query and T equal to it:
//
//	u = (L + T/2 + 1/2) / (n + 1)
//	deviation = Phi^-1(u)
//
// The consequence is declared rather than discovered: |deviation| is capped at
// Phi^-1(1 - 1/2m), about 2.14 at thirty reference values, 2.58 at a hundred,
// and only 1.69 at ten. At small reference sizes that cap, not `z_max`, is the
// operative limit — which is a further reason the minimum reference size is a
// real published figure.
//
// # The sign is kept
//
// `d` takes absolute values, which invites discarding the sign here. Below the
// author's usual comma density and above it are different facts, `rewrite`
// needs the direction, and it cannot be recovered later.
//
// # The reference split
//
// Built on Calibrate. Train is excluded because the profile was fitted on it
// and ranks against it would be optimistic; Test is excluded because reported
// figures come from Test and a reference fitted there contaminates them.

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

func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-12
}

// stat is a fully defined profile statistic.
func stat(id features.ID, mean, variance float64) profile.Stats {
	return profile.Stats{
		Feature:         id,
		N:               50,
		Mean:            mean,
		Variance:        variance,
		Defined:         true,
		VarianceDefined: true,
		MinObservations: 20,
	}
}

// value is a fully defined extracted feature value.
func value(id features.ID, v, samplingVariance float64) features.FeatureValue {
	return features.FeatureValue{
		ID:                      id,
		Value:                   v,
		Defined:                 true,
		SamplingVariance:        samplingVariance,
		SamplingVarianceDefined: true,
	}
}

// profileWith builds a paragraph-unit profile whose named features carry the
// given statistics and whose remaining features are defined but inert.
func profileWith(stats ...profile.Stats) *profile.Profile {
	named := make(map[features.ID]bool, len(stats))
	for _, s := range stats {
		named[s.Feature] = true
	}
	all := append([]profile.Stats(nil), stats...)
	for _, definition := range features.Definitions() {
		if !named[definition.ID] {
			all = append(all, stat(definition.ID, 1, 1))
		}
	}
	return &profile.Profile{
		ID:                    "profile-under-test",
		SnapshotID:            "snapshot-under-test",
		Split:                 corpus.Train,
		Unit:                  profile.UnitParagraph,
		FeatureSetVersion:     features.SetVersion,
		FeatureManifestDigest: features.ManifestDigest(),
		VarianceConvention:    profile.SampleVariance,
		// A real profile always carries a positive paragraph floor; this
		// fixture omitted it because Standardize never reads it, and the union
		// let an unfitted profile through without anyone noticing. Projecting
		// to Fitted refuses a floor of zero, which is how it surfaced.
		Requirements: profile.Requirements{MinParagraphLexicalTokens: 1},
		Stats:        all,
	}
}

// vectorWith builds a vector whose named features carry the given values and
// whose remaining features are defined but inert.
func vectorWith(lexicalTokens int, values ...features.FeatureValue) features.Vector {
	named := make(map[features.ID]bool, len(values))
	for _, v := range values {
		named[v.ID] = true
	}
	all := append([]features.FeatureValue(nil), values...)
	for _, definition := range features.Definitions() {
		if !named[definition.ID] {
			all = append(all, value(definition.ID, 1, 1))
		}
	}
	return features.Vector{
		SetVersion:    features.SetVersion,
		Tokens:        lexicalTokens,
		LexicalTokens: lexicalTokens,
		Values:        all,
	}
}

func standardize(t *testing.T, v features.Vector, p *profile.Profile) deviation.Standardization {
	t.Helper()
	s, err := deviation.Standardize(v, mustFit(t, p), corpus.Calibrate)
	if err != nil {
		t.Fatalf("Standardize: %v", err)
	}
	return s
}

func standardizedOf(t *testing.T, s deviation.Standardization, id features.ID) deviation.Standardized {
	t.Helper()
	for _, got := range s.Values {
		if got.Feature == id {
			return got
		}
	}
	t.Fatalf("standardization has no entry for %q", id)
	return deviation.Standardized{}
}

func deviationOf(t *testing.T, d deviation.Deviations, id features.ID) deviation.Deviation {
	t.Helper()
	for _, got := range d.Values {
		if got.Feature == id {
			return got
		}
	}
	t.Fatalf("deviations have no entry for %q", id)
	return deviation.Deviation{}
}

// referenceOf builds a Calibrate reference whose FunctionWordRate values are
// exactly the given standardized quantities. Every other feature is given the
// same count of distinct values so per-feature availability is not accidentally
// under-supplied.
func referenceOf(t *testing.T, p *profile.Profile, minSegments int, functionWordRate ...float64) *deviation.Reference {
	t.Helper()
	segments := make([]deviation.Standardization, 0, len(functionWordRate))
	for i, z := range functionWordRate {
		values := []deviation.Standardized{{
			Feature: features.FunctionWordRate,
			Value:   z,
			Defined: true,
		}}
		for _, definition := range features.Definitions() {
			if definition.ID == features.FunctionWordRate {
				continue
			}
			values = append(values, deviation.Standardized{
				Feature: definition.ID,
				Value:   float64(i),
				Defined: true,
			})
		}
		segments = append(segments, deviation.Standardization{
			ProfileID:             p.ID,
			FeatureManifestDigest: p.FeatureManifestDigest,
			Split:                 corpus.Calibrate,
			LexicalTokens:         50,
			Values:                values,
		})
	}
	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, minSegments)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	return ref
}

// queryOf is a standardization carrying one chosen FunctionWordRate deviation.
func queryOf(p *profile.Profile, z float64) deviation.Standardization {
	values := []deviation.Standardized{{
		Feature: features.FunctionWordRate,
		Value:   z,
		Defined: true,
	}}
	for _, definition := range features.Definitions() {
		if definition.ID == features.FunctionWordRate {
			continue
		}
		values = append(values, deviation.Standardized{
			Feature: definition.ID,
			Value:   0,
			Defined: true,
		})
	}
	return deviation.Standardization{
		ProfileID:             p.ID,
		FeatureManifestDigest: p.FeatureManifestDigest,
		Split:                 corpus.Test,
		LexicalTokens:         50,
		Values:                values,
	}
}

func transform(t *testing.T, ref *deviation.Reference, s deviation.Standardization) deviation.Deviations {
	t.Helper()
	d, err := ref.Transform(s)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	return d
}

func manifestOrder() []features.ID {
	order := make([]features.ID, 0, len(features.Definitions()))
	for _, definition := range features.Definitions() {
		order = append(order, definition.ID)
	}
	return order
}

// ---------------------------------------------------------------------------
// Correction 1 — the length-aware denominator
// ---------------------------------------------------------------------------

// The arithmetic, spelled out so a reader can check it without running
// anything. A rate at profile mean 0.40 with profile variance 0.0100, observed
// at 0.50 over 25 lexical tokens:
//
//	sampling variance = p(1-p)/n = 0.5*0.5/25 = 0.0100
//	denominator       = sqrt(0.0100 + 0.0100) = 0.14142135623730951
//	z                 = 0.10 / 0.14142135623730951 = 0.7071067811865476
//
// Dividing by the profile SD alone would give exactly 1.0. That is the whole
// correction, and this test is the one that distinguishes them.
func TestStandardizeCombinesProfileAndSamplingVariance(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0.0100))
	v := vectorWith(25, value(features.FunctionWordRate, 0.50, 0.0100))

	got := standardizedOf(t, standardize(t, v, p), features.FunctionWordRate)
	if !got.Defined {
		t.Fatalf("standardized value is undefined: %v", got.Reason)
	}
	const want = 0.7071067811865476
	if !closeTo(got.Value, want) {
		t.Errorf("z = %v, want %v", got.Value, want)
	}
	if closeTo(got.Value, 1.0) {
		t.Errorf("z = %v, which is the profile-SD-only answer; the sampling variance is not in the denominator", got.Value)
	}
}

// The same raw departure from the profile mean, measured over four times the
// text, is stronger evidence. A 25-token segment gives 0.7071067811865476 and a
// 100-token segment gives 0.894427190999916 — the sampling term shrinks as 1/n
// while the profile term does not.
//
// An implementation that ignored segment length would return the same number
// twice, so this asserts both values exactly rather than merely that they
// differ.
func TestStandardizeUsesTheObservedSegmentLength(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0.0100))

	short := standardizedOf(t, standardize(t, vectorWith(25, value(features.FunctionWordRate, 0.50, 0.0100)), p), features.FunctionWordRate)
	long := standardizedOf(t, standardize(t, vectorWith(100, value(features.FunctionWordRate, 0.50, 0.0025)), p), features.FunctionWordRate)

	const wantShort = 0.7071067811865476
	const wantLong = 0.894427190999916
	if !closeTo(short.Value, wantShort) {
		t.Errorf("25-token z = %v, want %v", short.Value, wantShort)
	}
	if !closeTo(long.Value, wantLong) {
		t.Errorf("100-token z = %v, want %v", long.Value, wantLong)
	}
	if !(math.Abs(long.Value) > math.Abs(short.Value)) {
		t.Errorf("the longer segment produced the weaker deviation: %v vs %v", long.Value, short.Value)
	}
}

// The denominator is a variance sum under a square root, not a sum of standard
// deviations. sqrt(0.01) + sqrt(0.01) = 0.2 would give z = 0.5; the correct
// sqrt(0.01 + 0.01) gives 0.7071067811865476. Both are plausible-looking code.
func TestStandardizeAddsVariancesNotStandardDeviations(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0.0100))
	v := vectorWith(25, value(features.FunctionWordRate, 0.50, 0.0100))

	got := standardizedOf(t, standardize(t, v, p), features.FunctionWordRate)
	if closeTo(got.Value, 0.5) {
		t.Fatalf("z = %v, which is what adding standard deviations gives", got.Value)
	}
	if !closeTo(got.Value, 0.7071067811865476) {
		t.Errorf("z = %v, want 0.7071067811865476", got.Value)
	}
}

// Each sampling family reaches the same denominator, so each is checked with
// its own hand-computed value.
func TestStandardizeAcrossSamplingFamilies(t *testing.T) {
	cases := []struct {
		name          string
		id            features.ID
		mean, varce   float64
		observed, sv  float64
		lexicalTokens int
		want          float64
	}{
		{
			// density: lambda/n = 0.08/50 = 0.0016; sqrt(0.0004+0.0016) = 0.0447213595499958
			name: "density", id: features.CommaDensity,
			mean: 0.05, varce: 0.0004, observed: 0.08, sv: 0.0016, lexicalTokens: 50,
			want: 0.6708203932499369,
		},
		{
			// mean: the within-segment sample variance over n; sqrt(0.25+0.09) = 0.5830951894845301
			name: "mean", id: features.WordLengthMean,
			mean: 4.5, varce: 0.25, observed: 5.0, sv: 0.09, lexicalTokens: 40,
			want: 0.8574929257125443,
		},
		{
			// rate below the profile mean: p(1-p)/n = 0.3*0.7/25 = 0.0084
			name: "rate below the mean", id: features.ClauseMarkerRate,
			mean: 0.40, varce: 0.0100, observed: 0.30, sv: 0.0084, lexicalTokens: 25,
			want: -0.7372097807744857,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := profileWith(stat(c.id, c.mean, c.varce))
			v := vectorWith(c.lexicalTokens, value(c.id, c.observed, c.sv))
			got := standardizedOf(t, standardize(t, v, p), c.id)
			if !got.Defined {
				t.Fatalf("undefined: %v", got.Reason)
			}
			if !closeTo(got.Value, c.want) {
				t.Errorf("z = %v, want %v", got.Value, c.want)
			}
		})
	}
}

// The sign says which side of the author's usual the segment falls on, and
// nothing downstream can recover it once it is gone.
func TestStandardizeKeepsTheSign(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0.0100))

	above := standardizedOf(t, standardize(t, vectorWith(25, value(features.FunctionWordRate, 0.50, 0.0100)), p), features.FunctionWordRate)
	below := standardizedOf(t, standardize(t, vectorWith(25, value(features.FunctionWordRate, 0.30, 0.0084)), p), features.FunctionWordRate)

	if above.Value <= 0 {
		t.Errorf("a rate above the profile mean standardized to %v; want positive", above.Value)
	}
	if below.Value >= 0 {
		t.Errorf("a rate below the profile mean standardized to %v; want negative", below.Value)
	}
}

// A feature exactly at the profile mean is not evidence in either direction.
func TestStandardizeAtTheProfileMeanIsZero(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0.0100))
	v := vectorWith(25, value(features.FunctionWordRate, 0.40, 0.0096))

	got := standardizedOf(t, standardize(t, v, p), features.FunctionWordRate)
	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if got.Value != 0 {
		t.Errorf("z = %v at the profile mean, want exactly 0", got.Value)
	}
}

// ---------------------------------------------------------------------------
// Correction 1 — what is undefined, and why
// ---------------------------------------------------------------------------

// Each way a standardization can fail to exist carries its own reason. A single
// undefined flag would make a profile too thin to fit indistinguishable from a
// segment too short to measure, and those call for different action.
func TestStandardizeMarksEachUndefinedCause(t *testing.T) {
	const id = features.FunctionWordRate

	undefinedFeature := vectorWith(25)
	for i := range undefinedFeature.Values {
		if undefinedFeature.Values[i].ID == id {
			undefinedFeature.Values[i] = features.FeatureValue{ID: id, SamplingVarianceDefined: true}
		}
	}

	undefinedSampling := vectorWith(25)
	for i := range undefinedSampling.Values {
		if undefinedSampling.Values[i].ID == id {
			undefinedSampling.Values[i] = features.FeatureValue{ID: id, Value: 0.5, Defined: true}
		}
	}

	cases := []struct {
		name   string
		p      *profile.Profile
		v      features.Vector
		reason deviation.Reason
	}{
		{
			name:   "the feature is undefined on the segment",
			p:      profileWith(stat(id, 0.4, 0.01)),
			v:      undefinedFeature,
			reason: deviation.ReasonFeatureUndefined,
		},
		{
			name:   "the segment sampling variance is undefined",
			p:      profileWith(stat(id, 0.4, 0.01)),
			v:      undefinedSampling,
			reason: deviation.ReasonSamplingVarianceUndefined,
		},
		{
			name:   "the profile statistic is undefined",
			p:      profileWith(profile.Stats{Feature: id, N: 3, MinObservations: 20}),
			v:      vectorWith(25, value(id, 0.5, 0.01)),
			reason: deviation.ReasonProfileUndefined,
		},
		{
			name:   "the profile variance is undefined",
			p:      profileWith(profile.Stats{Feature: id, N: 1, Mean: 0.4, Defined: true, MinObservations: 1}),
			v:      vectorWith(25, value(id, 0.5, 0.01)),
			reason: deviation.ReasonProfileVarianceUndefined,
		},
		{
			// A feature that is zero everywhere: the profile saw no spread and
			// the segment shows none either. 0/0 is not a large deviation and
			// is not a small one.
			name:   "the combined variance is zero",
			p:      profileWith(stat(id, 0, 0)),
			v:      vectorWith(25, value(id, 0, 0)),
			reason: deviation.ReasonZeroVariance,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := standardizedOf(t, standardize(t, c.v, c.p), id)
			if got.Defined {
				t.Fatalf("value is defined as %v; want undefined", got.Value)
			}
			if got.Reason != c.reason {
				t.Errorf("reason = %q, want %q", got.Reason, c.reason)
			}
			if got.Value != 0 {
				t.Errorf("an undefined value carries %v; it must be zero, never a sentinel", got.Value)
			}
		})
	}
}

// When several causes hold at once, the reported reason is the first one a
// reader would reach checking the inputs in order: the segment value, the
// segment sampling variance, the profile statistic, the profile variance, then
// the combined variance. Leaving this unstated would let the reason depend on
// the order an implementation happened to write its guards in, which is a
// silent difference in what a user is told to do about it.
func TestStandardizeUndefinedCausesHaveADeclaredPrecedence(t *testing.T) {
	const id = features.FunctionWordRate

	segmentValueMissing := vectorWith(25)
	segmentSamplingMissing := vectorWith(25)
	for i := range segmentValueMissing.Values {
		if segmentValueMissing.Values[i].ID == id {
			// Nothing about this feature is defined on the segment.
			segmentValueMissing.Values[i] = features.FeatureValue{ID: id}
		}
	}
	for i := range segmentSamplingMissing.Values {
		if segmentSamplingMissing.Values[i].ID == id {
			segmentSamplingMissing.Values[i] = features.FeatureValue{ID: id, Value: 0.5, Defined: true}
		}
	}

	unfitProfile := profileWith(profile.Stats{Feature: id, N: 3, MinObservations: 20})
	noVarianceProfile := profileWith(profile.Stats{Feature: id, N: 1, Mean: 0.4, Defined: true, MinObservations: 1})

	cases := []struct {
		name   string
		p      *profile.Profile
		v      features.Vector
		reason deviation.Reason
	}{
		{
			name:   "every cause at once reports the segment value",
			p:      unfitProfile,
			v:      segmentValueMissing,
			reason: deviation.ReasonFeatureUndefined,
		},
		{
			name:   "a missing sampling variance outranks an unfit profile",
			p:      unfitProfile,
			v:      segmentSamplingMissing,
			reason: deviation.ReasonSamplingVarianceUndefined,
		},
		{
			name:   "an unfit profile outranks a missing profile variance",
			p:      unfitProfile,
			v:      vectorWith(25, value(id, 0.5, 0.01)),
			reason: deviation.ReasonProfileUndefined,
		},
		{
			name:   "a missing profile variance outranks the zero-variance guard",
			p:      noVarianceProfile,
			v:      vectorWith(25, value(id, 0.5, 0)),
			reason: deviation.ReasonProfileVarianceUndefined,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := standardizedOf(t, standardize(t, c.v, c.p), id)
			if got.Defined {
				t.Fatalf("value is defined as %v; want undefined", got.Value)
			}
			if got.Reason != c.reason {
				t.Errorf("reason = %q, want %q", got.Reason, c.reason)
			}
		})
	}
}

// A zero denominator must not become an infinity, and an undefined input must
// not become a NaN. Both are persisted and hashed per Section 2's cache
// identity rules: encoding/json refuses NaN, NaN compares unequal to itself,
// and hashing needs a canonical bit pattern.
func TestStandardizeNeverProducesNaNOrInfinity(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0, 0))
	v := vectorWith(25, value(features.FunctionWordRate, 0.5, 0))

	for _, got := range standardize(t, v, p).Values {
		if math.IsNaN(got.Value) || math.IsInf(got.Value, 0) {
			t.Errorf("%s standardized to %v, which cannot be persisted or hashed", got.Feature, got.Value)
		}
	}
}

// A nonzero numerator over a zero denominator is the case an implementation is
// most likely to let through as +Inf: the profile saw no spread, so any
// departure looks infinitely surprising. It is not evidence, it is a profile
// that cannot support the comparison.
func TestStandardizeZeroVarianceWithANonzeroNumerator(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0))
	v := vectorWith(25, value(features.FunctionWordRate, 0.50, 0))

	got := standardizedOf(t, standardize(t, v, p), features.FunctionWordRate)
	if got.Defined {
		t.Fatalf("value is defined as %v; want undefined", got.Value)
	}
	if got.Reason != deviation.ReasonZeroVariance {
		t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonZeroVariance)
	}
}

// A sampling variance of zero is a real answer, not a missing one: a segment
// with no commas has a zero count, and the profile variance still stands behind
// the comparison. It must not be confused with the zero-denominator case.
func TestStandardizeZeroSamplingVarianceAloneIsDefined(t *testing.T) {
	p := profileWith(stat(features.CommaDensity, 0.05, 0.0004))
	v := vectorWith(40, value(features.CommaDensity, 0, 0))

	got := standardizedOf(t, standardize(t, v, p), features.CommaDensity)
	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	// (0 - 0.05) / sqrt(0.0004) = -2.5
	if !closeTo(got.Value, -2.5) {
		t.Errorf("z = %v, want -2.5", got.Value)
	}
}

// ---------------------------------------------------------------------------
// Correction 1 — shape and provenance
// ---------------------------------------------------------------------------

// Every manifest feature is present exactly once, in manifest order. Iterating
// only what came back would let a silently dropped feature pass every other
// test in this file.
func TestStandardizeCoversTheWholeManifestInOrder(t *testing.T) {
	p := profileWith()
	// The named feature is placed first in the vector, so the input order is
	// not the manifest order and echoing it back cannot pass.
	v := vectorWith(25, value(features.ClauseMarkerRate, 0.1, 0.01))

	got := standardize(t, v, p)
	order := make([]features.ID, 0, len(got.Values))
	for _, s := range got.Values {
		order = append(order, s.Feature)
	}
	if want := manifestOrder(); !reflect.DeepEqual(order, want) {
		t.Errorf("features = %v, want the manifest order %v", order, want)
	}
}

// The standardization records which profile it came from, so a later transform
// can refuse a mismatched pair rather than silently produce a number from two
// unrelated fits.
func TestStandardizeCarriesItsProvenance(t *testing.T) {
	p := profileWith()
	v := vectorWith(37)

	got := standardize(t, v, p)
	if got.ProfileID != p.ID {
		t.Errorf("ProfileID = %q, want %q", got.ProfileID, p.ID)
	}
	if got.FeatureManifestDigest != p.FeatureManifestDigest {
		t.Errorf("FeatureManifestDigest = %q, want %q", got.FeatureManifestDigest, p.FeatureManifestDigest)
	}
	if got.LexicalTokens != 37 {
		t.Errorf("LexicalTokens = %d, want 37", got.LexicalTokens)
	}
	if got.Split != corpus.Calibrate {
		t.Errorf("Split = %q, want %q", got.Split, corpus.Calibrate)
	}
}

// Narrowing Standardize to the fitted projection took three of this test's
// cases away from it: a nil profile, a document-unit profile and one fitted
// under a different manifest can no longer be PASSED here, because
// profile.Fitted() refuses to project any of them. Those rejections did not
// disappear — they moved to where the projection is made, and profile covers all
// three there, along with an empty identity, an absent floor and no statistics.
//
// It would overstate it to call a bad Fitted unrepresentable. The fields are
// exported, so a caller can still hand-build an invalid one; what is protected
// is the PROJECTION PATH, which is how every real caller obtains one. Go does
// not make the value unconstructable and this test should not imply that it
// does.
//
// What is left here is what Standardize still owns: the split, and the vector.
func TestStandardizeRejectsBadInput(t *testing.T) {
	fitted := mustFit(t, profileWith())

	for _, c := range []struct {
		name  string
		split corpus.Split
		v     features.Vector
		want  error
	}{
		{name: "a split it does not know", split: "someday", v: vectorWith(25), want: deviation.ErrUnknownSplit},
		{
			name: "a vector from a different feature set", split: corpus.Calibrate,
			v: staleVector(), want: deviation.ErrManifestMismatch,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := deviation.Standardize(c.v, fitted, c.split)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// staleVector is a vector built under a feature set this binary does not carry.
func staleVector() features.Vector {
	v := vectorWith(25)
	v.SetVersion = features.SetVersion + 1
	return v
}

// A vector extracted under a different feature-set version is not comparable to
// this profile's statistics, whatever the digest says.
func TestStandardizeRejectsAMismatchedFeatureSetVersion(t *testing.T) {
	p := profileWith()
	v := vectorWith(25)
	v.SetVersion = features.SetVersion + 1

	if _, err := deviation.Standardize(v, mustFit(t, p), corpus.Calibrate); !errors.Is(err, deviation.ErrManifestMismatch) {
		t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
	}
}

// A vector missing a manifest feature entirely is a different failure from a
// feature that is present and undefined, and it must not be papered over.
func TestStandardizeRejectsAVectorMissingAManifestFeature(t *testing.T) {
	p := profileWith()
	v := vectorWith(25)
	v.Values = v.Values[1:]

	if _, err := deviation.Standardize(v, mustFit(t, p), corpus.Calibrate); !errors.Is(err, deviation.ErrManifestMismatch) {
		t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
	}
}

// A profile missing a manifest feature likewise. Both directions of the same
// asymmetry, because a guard written on one side is easy to leave off the
// other.
func TestStandardizeRejectsAProfileMissingAManifestFeature(t *testing.T) {
	p := profileWith()
	p.Stats = p.Stats[1:]
	v := vectorWith(25)

	if _, err := deviation.Standardize(v, mustFit(t, p), corpus.Calibrate); !errors.Is(err, deviation.ErrManifestMismatch) {
		t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
	}
}

// ---------------------------------------------------------------------------
// The reference distribution
// ---------------------------------------------------------------------------

// Train is excluded because the profile was fitted on it; Test is excluded
// because reported figures come from Test. Only Calibrate is admitted, and the
// guard is checked on every split rather than on the one that happens to be
// wrong.
func TestBuildReferenceAdmitsOnlyCalibrate(t *testing.T) {
	p := profileWith()
	segments := referenceSegments(p, 4)

	for _, split := range []corpus.Split{corpus.Train, corpus.Test} {
		t.Run(string(split), func(t *testing.T) {
			if _, err := deviation.BuildReference(p, split, segments, 3); !errors.Is(err, deviation.ErrReferenceSplit) {
				t.Errorf("err = %v, want %v", err, deviation.ErrReferenceSplit)
			}
		})
	}

	t.Run(string(corpus.Calibrate), func(t *testing.T) {
		ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3)
		if err != nil {
			t.Fatalf("BuildReference on calibrate: %v", err)
		}
		if ref.Split != corpus.Calibrate {
			t.Errorf("Split = %q, want %q", ref.Split, corpus.Calibrate)
		}
	})
}

// referenceSegments produces n fully defined standardized segments.
func referenceSegments(p *profile.Profile, n int) []deviation.Standardization {
	segments := make([]deviation.Standardization, 0, n)
	for i := 0; i < n; i++ {
		values := make([]deviation.Standardized, 0, len(features.Definitions()))
		for _, definition := range features.Definitions() {
			values = append(values, deviation.Standardized{
				Feature: definition.ID,
				Value:   float64(i),
				Defined: true,
			})
		}
		segments = append(segments, deviation.Standardization{
			ProfileID:             p.ID,
			FeatureManifestDigest: p.FeatureManifestDigest,
			Split:                 corpus.Calibrate,
			LexicalTokens:         50,
			Values:                values,
		})
	}
	return segments
}

func TestBuildReferenceRejectsBadInput(t *testing.T) {
	p := profileWith()

	foreign := referenceSegments(p, 4)
	foreign[2].ProfileID = "another-profile"

	staleDigest := referenceSegments(p, 4)
	staleDigest[1].FeatureManifestDigest = "a-different-digest"

	cases := []struct {
		name        string
		p           *profile.Profile
		segments    []deviation.Standardization
		minSegments int
		want        error
	}{
		{name: "no profile", p: nil, segments: referenceSegments(p, 4), minSegments: 3, want: deviation.ErrMissingInput},
		{name: "no segments", p: p, segments: nil, minSegments: 3, want: deviation.ErrReferenceTooSmall},
		{name: "a zero minimum", p: p, segments: referenceSegments(p, 4), minSegments: 0, want: deviation.ErrInvalidRequirements},
		{name: "a negative minimum", p: p, segments: referenceSegments(p, 4), minSegments: -1, want: deviation.ErrInvalidRequirements},
		{name: "fewer segments than the minimum", p: p, segments: referenceSegments(p, 2), minSegments: 3, want: deviation.ErrReferenceTooSmall},
		{name: "a segment from another profile", p: p, segments: foreign, minSegments: 3, want: deviation.ErrProfileMismatch},
		{name: "a segment under another manifest", p: p, segments: staleDigest, minSegments: 3, want: deviation.ErrManifestMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := deviation.BuildReference(c.p, corpus.Calibrate, c.segments, c.minSegments); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// A reference at exactly the minimum is admitted. Off-by-one on a floor is
// silent and changes which authors can be scored at all.
func TestBuildReferenceAdmitsExactlyTheMinimum(t *testing.T) {
	p := profileWith()
	ref, err := deviation.BuildReference(p, corpus.Calibrate, referenceSegments(p, 3), 3)
	if err != nil {
		t.Fatalf("BuildReference at exactly the minimum: %v", err)
	}
	if ref.Segments != 3 {
		t.Errorf("Segments = %d, want 3", ref.Segments)
	}
}

// The reference size is per feature, because a feature undefined in some
// Calibrate segments has fewer values behind it than the segment count
// suggests. Reporting the segment count as every feature's reference size would
// overstate the evidence behind exactly the features that have least.
func TestReferenceSizeIsPerFeature(t *testing.T) {
	p := profileWith()
	segments := referenceSegments(p, 6)
	for i := 0; i < 4; i++ {
		for j := range segments[i].Values {
			if segments[i].Values[j].Feature == features.CommaDensity {
				segments[i].Values[j] = deviation.Standardized{
					Feature: features.CommaDensity,
					Reason:  deviation.ReasonFeatureUndefined,
				}
			}
		}
	}

	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 2)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	if got := ref.Size(features.CommaDensity); got != 2 {
		t.Errorf("comma_density reference size = %d, want 2", got)
	}
	if got := ref.Size(features.FunctionWordRate); got != 6 {
		t.Errorf("function_word_rate reference size = %d, want 6", got)
	}
	if ref.Segments != 6 {
		t.Errorf("Segments = %d, want 6", ref.Segments)
	}
}

// A feature with fewer defined reference values than the minimum is
// unavailable, and the whole reference is not thereby invalid: the other
// features still carry evidence.
func TestReferenceAvailabilityIsPerFeature(t *testing.T) {
	p := profileWith()
	segments := referenceSegments(p, 6)
	for i := 0; i < 5; i++ {
		for j := range segments[i].Values {
			if segments[i].Values[j].Feature == features.CommaDensity {
				segments[i].Values[j] = deviation.Standardized{
					Feature: features.CommaDensity,
					Reason:  deviation.ReasonFeatureUndefined,
				}
			}
		}
	}

	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	if ref.Available(features.CommaDensity) {
		t.Errorf("comma_density is available on one defined value against a minimum of 3")
	}
	if !ref.Available(features.FunctionWordRate) {
		t.Errorf("function_word_rate is unavailable on six defined values against a minimum of 3")
	}
}

// The cap is a property of the reference size, and it is published rather than
// discovered. n reference values give m = n+1 and a cap of Phi^-1(1 - 1/2m).
func TestReferenceCapFollowsTheReferenceSize(t *testing.T) {
	p := profileWith()

	cases := []struct {
		size int
		want float64
	}{
		{size: 3, want: 1.1503493803760081},  // Phi^-1(3.5/4)  = Phi^-1(0.875)
		{size: 4, want: 1.2815515655446006},  // Phi^-1(4.5/5)  = Phi^-1(0.9)
		{size: 30, want: 2.1411981209720180}, // Phi^-1(30.5/31)
	}

	for _, c := range cases {
		ref, err := deviation.BuildReference(p, corpus.Calibrate, referenceSegments(p, c.size), 2)
		if err != nil {
			t.Fatalf("BuildReference at %d: %v", c.size, err)
		}
		got, ok := ref.Cap(features.FunctionWordRate)
		if !ok {
			t.Fatalf("no cap at reference size %d", c.size)
		}
		if !closeTo(got, c.want) {
			t.Errorf("cap at %d reference values = %v, want %v", c.size, got, c.want)
		}
	}
}

func TestReferenceCapIsAbsentForAnUnavailableFeature(t *testing.T) {
	p := profileWith()
	segments := referenceSegments(p, 4)
	for i := range segments {
		for j := range segments[i].Values {
			if segments[i].Values[j].Feature == features.CommaDensity {
				segments[i].Values[j] = deviation.Standardized{
					Feature: features.CommaDensity,
					Reason:  deviation.ReasonFeatureUndefined,
				}
			}
		}
	}

	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	if got, ok := ref.Cap(features.CommaDensity); ok {
		t.Errorf("cap = %v for a feature with no reference values; want absent", got)
	}
}

// ---------------------------------------------------------------------------
// The reference identity
// ---------------------------------------------------------------------------

// A reference is a scoring input, so it is content-addressed like every other
// artifact in this project: anything that can change a deviation changes the
// ID.
func TestReferenceIdentityChangesWithItsInputs(t *testing.T) {
	p := profileWith()
	base := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	same := referenceOf(t, p, 2, -1, 0, 0.8, 1)
	if base.ID != same.ID {
		t.Errorf("identical inputs produced different IDs: %q and %q", base.ID, same.ID)
	}

	cases := []struct {
		name string
		ref  *deviation.Reference
	}{
		{name: "a changed reference value", ref: referenceOf(t, p, 2, -1, 0, 0.9, 1)},
		{name: "an added reference value", ref: referenceOf(t, p, 2, -1, 0, 0.8, 1, 1.5)},
		{name: "a changed minimum", ref: referenceOf(t, p, 3, -1, 0, 0.8, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.ref.ID == base.ID {
				t.Errorf("%s did not change the reference ID %q", c.name, base.ID)
			}
		})
	}
}

// The reference is a distribution, not a sequence. Presenting the same Calibrate
// segments in a different order must not produce a different artifact, or the
// cache would miss on every rebuild.
func TestReferenceIdentityIgnoresSegmentOrder(t *testing.T) {
	p := profileWith()
	ascending := referenceOf(t, p, 2, -1, 0, 0.8, 1)
	shuffled := referenceOf(t, p, 2, 0.8, 1, -1, 0)

	if ascending.ID != shuffled.ID {
		t.Errorf("segment order changed the reference ID: %q and %q", ascending.ID, shuffled.ID)
	}
}

// A profile identity is not a substitute for the reference's own: two
// references built from the same profile on different Calibrate segments are
// different artifacts.
func TestReferenceIdentityIsNotTheProfileIdentity(t *testing.T) {
	p := profileWith()
	one := referenceOf(t, p, 2, -1, 0, 0.8, 1)
	two := referenceOf(t, p, 2, -2, 0, 0.4, 3)

	if one.ID == two.ID {
		t.Errorf("two different references share the ID %q", one.ID)
	}
	if one.ID == p.ID || two.ID == p.ID {
		t.Errorf("the reference ID is the profile ID %q", p.ID)
	}
}

func TestReferenceRecordsItsAlgorithm(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)
	if ref.Algorithm != deviation.Algorithm {
		t.Errorf("Algorithm = %q, want %q", ref.Algorithm, deviation.Algorithm)
	}
}

// ---------------------------------------------------------------------------
// Correction 2 — the rank transform
// ---------------------------------------------------------------------------

// The arithmetic, against the reference {-1, 0, 0.8, 1} with n = 4, m = 5:
//
//	query  L  T   u = (L + T/2 + 1/2)/5   Phi^-1(u)
//	-5.0   0  0   0.1                     -1.2815515655446006
//	 0.0   1  1   0.4                     -0.2533471031357998
//	 0.5   2  0   0.5                      0.0
//	 0.9   3  0   0.7                      0.5244005127080410
//	 5.0   4  0   0.9                      1.2815515655446006
func TestTransformAgainstAHandComputedReference(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	cases := []struct {
		name string
		z    float64
		want float64
	}{
		{name: "below every reference value", z: -5.0, want: -1.2815515655446006},
		{name: "tied with one reference value", z: 0.0, want: -0.2533471031357998},
		{name: "between two reference values", z: 0.5, want: 0.0},
		{name: "just below the largest", z: 0.9, want: 0.5244005127080410},
		{name: "above every reference value", z: 5.0, want: 1.2815515655446006},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deviationOf(t, transform(t, ref, queryOf(p, c.z)), features.FunctionWordRate)
			if !got.Defined {
				t.Fatalf("undefined: %v", got.Reason)
			}
			if !closeTo(got.Value, c.want) {
				t.Errorf("deviation = %v, want %v", got.Value, c.want)
			}
		})
	}
}

// The plotting position is symmetric: a segment below everything and a segment
// above everything are equally surprising, in opposite directions. Ranking
// against the n reference values alone cannot achieve this, because n points
// leave n+1 gaps and any position over n is short a cell at one end.
func TestTransformIsSymmetricInBothTails(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	low := deviationOf(t, transform(t, ref, queryOf(p, -100)), features.FunctionWordRate)
	high := deviationOf(t, transform(t, ref, queryOf(p, 100)), features.FunctionWordRate)

	if !closeTo(low.Value, -high.Value) {
		t.Errorf("tails are asymmetric: %v below and %v above", low.Value, high.Value)
	}
}

// Ties take their midrank. A query equal to every reference value sits exactly
// at the centre of the distribution, which is the only defensible answer and
// the one a naive "count of values less than" would miss.
func TestTransformTiesTakeTheirMidrank(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, 2, 2, 2)

	got := deviationOf(t, transform(t, ref, queryOf(p, 2)), features.FunctionWordRate)
	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if got.Value != 0 {
		t.Errorf("deviation = %v for a query equal to every reference value, want exactly 0", got.Value)
	}
}

// The transform is monotone in the standardized value: a segment further from
// the author's usual cannot rank as less surprising.
func TestTransformIsMonotone(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -2, -1, 0, 1, 2)

	previous := math.Inf(-1)
	for _, z := range []float64{-9, -1.5, -0.5, 0, 0.5, 1.5, 9} {
		got := deviationOf(t, transform(t, ref, queryOf(p, z)), features.FunctionWordRate)
		if got.Value < previous {
			t.Errorf("z = %v gave %v, below the previous %v", z, got.Value, previous)
		}
		previous = got.Value
	}
}

// The cap is reached, not merely approached: a query far beyond the reference
// lands exactly on Phi^-1(1 - 1/2m), and nothing exceeds it.
func TestTransformIsBoundedByTheReferenceCap(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	bound, ok := ref.Cap(features.FunctionWordRate)
	if !ok {
		t.Fatalf("no cap for an available feature")
	}
	for _, z := range []float64{1e6, -1e6, 1e300, -1e300} {
		got := deviationOf(t, transform(t, ref, queryOf(p, z)), features.FunctionWordRate)
		if math.Abs(got.Value) > bound+1e-12 {
			t.Errorf("z = %v gave %v, beyond the cap %v", z, got.Value, bound)
		}
		if !closeTo(math.Abs(got.Value), bound) {
			t.Errorf("z = %v gave %v, want the cap %v exactly", z, got.Value, bound)
		}
	}
}

// Nothing reaches an infinity, whatever the input. The empirical CDF returns 0
// below all reference values and 1 above, and the normal quantile is infinite
// at both — which is what the plotting position exists to prevent.
func TestTransformNeverProducesNaNOrInfinity(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	for _, z := range []float64{-1e308, -5, 0, 5, 1e308} {
		for _, got := range transform(t, ref, queryOf(p, z)).Values {
			if math.IsNaN(got.Value) || math.IsInf(got.Value, 0) {
				t.Errorf("z = %v: %s transformed to %v, which cannot be persisted or hashed", z, got.Feature, got.Value)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The composition, in order
// ---------------------------------------------------------------------------

// The finding of REVIEW Round 5, as a test.
//
// Two segments carry the same raw function-word rate, 0.50, one over 25 lexical
// tokens and one over 100. Ranking raw values would place them identically,
// because they ARE identical. Ranking the length-aware standardization places
// them apart, because the longer segment's departure is better evidenced:
//
//	25 tokens  -> z = 0.7071067811865476 -> L = 2 -> u = 0.5 -> 0.0
//	100 tokens -> z = 0.894427190999916  -> L = 3 -> u = 0.7 -> 0.5244005127080410
//
// The reference {-1, 0, 0.8, 1} is chosen so that 0.8 falls between the two
// standardized values. An implementation that ranked raw values, or that
// standardized by the profile SD alone, returns the same number twice here.
func TestTheCorrectionsComposeInOrder(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0.0100))
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	short := transform(t, ref, standardize(t, vectorWith(25, value(features.FunctionWordRate, 0.50, 0.0100)), p))
	long := transform(t, ref, standardize(t, vectorWith(100, value(features.FunctionWordRate, 0.50, 0.0025)), p))

	gotShort := deviationOf(t, short, features.FunctionWordRate)
	gotLong := deviationOf(t, long, features.FunctionWordRate)

	if !gotShort.Defined || !gotLong.Defined {
		t.Fatalf("undefined: %v and %v", gotShort.Reason, gotLong.Reason)
	}
	if closeTo(gotShort.Value, gotLong.Value) {
		t.Fatalf("the same raw rate over 25 and 100 tokens both transformed to %v; the rank was applied to the raw value, not to the standardization", gotShort.Value)
	}
	if !closeTo(gotShort.Value, 0.0) {
		t.Errorf("25-token deviation = %v, want 0.0", gotShort.Value)
	}
	if !closeTo(gotLong.Value, 0.5244005127080410) {
		t.Errorf("100-token deviation = %v, want 0.5244005127080410", gotLong.Value)
	}
}

// The sign survives the transform. A segment below the author's usual rate must
// come out negative, and one above positive, or `rewrite` cannot tell a reader
// which way to move.
func TestTransformKeepsTheSign(t *testing.T) {
	p := profileWith(stat(features.FunctionWordRate, 0.40, 0.0100))
	ref := referenceOf(t, p, 2, -1, -0.5, 0.5, 1)

	below := deviationOf(t, transform(t, ref, standardize(t, vectorWith(25, value(features.FunctionWordRate, 0.30, 0.0084)), p)), features.FunctionWordRate)
	above := deviationOf(t, transform(t, ref, standardize(t, vectorWith(25, value(features.FunctionWordRate, 0.50, 0.0100)), p)), features.FunctionWordRate)

	if below.Value >= 0 {
		t.Errorf("a rate below the author's usual transformed to %v; want negative", below.Value)
	}
	if above.Value <= 0 {
		t.Errorf("a rate above the author's usual transformed to %v; want positive", above.Value)
	}
}

// ---------------------------------------------------------------------------
// Correction 2 — what is undefined, and why
// ---------------------------------------------------------------------------

// An undefined standardization cannot be ranked, and the reason it was
// undefined is the useful one — not a fresh reason invented at the transform.
func TestTransformPropagatesAnUndefinedStandardization(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	query := queryOf(p, 0)
	for i := range query.Values {
		if query.Values[i].Feature == features.FunctionWordRate {
			query.Values[i] = deviation.Standardized{
				Feature: features.FunctionWordRate,
				Reason:  deviation.ReasonSamplingVarianceUndefined,
			}
		}
	}

	got := deviationOf(t, transform(t, ref, query), features.FunctionWordRate)
	if got.Defined {
		t.Fatalf("value is defined as %v; want undefined", got.Value)
	}
	if got.Reason != deviation.ReasonSamplingVarianceUndefined {
		t.Errorf("reason = %q, want the standardization's own %q", got.Reason, deviation.ReasonSamplingVarianceUndefined)
	}
	if got.Value != 0 {
		t.Errorf("an undefined deviation carries %v; it must be zero, never a sentinel", got.Value)
	}
}

// A feature whose reference is too small is unavailable, not zero. Returning 0
// would read as "exactly typical" — the single most misleading answer available
// for a feature about which nothing is known.
func TestTransformMarksAFeatureWithTooSmallAReference(t *testing.T) {
	p := profileWith()
	segments := referenceSegments(p, 6)
	for i := 0; i < 5; i++ {
		for j := range segments[i].Values {
			if segments[i].Values[j].Feature == features.CommaDensity {
				segments[i].Values[j] = deviation.Standardized{
					Feature: features.CommaDensity,
					Reason:  deviation.ReasonFeatureUndefined,
				}
			}
		}
	}
	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}

	got := deviationOf(t, transform(t, ref, queryOf(p, 0)), features.CommaDensity)
	if got.Defined {
		t.Fatalf("comma_density is defined as %v against a one-value reference", got.Value)
	}
	if got.Reason != deviation.ReasonReferenceTooSmall {
		t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonReferenceTooSmall)
	}
	if got.Value != 0 {
		t.Errorf("an undefined deviation carries %v; it must be zero, never a sentinel", got.Value)
	}
	if available := deviationOf(t, transform(t, ref, queryOf(p, 0)), features.FunctionWordRate); !available.Defined {
		t.Errorf("function_word_rate is undefined (%v) although its own reference is sufficient", available.Reason)
	}
}

// ---------------------------------------------------------------------------
// Correction 2 — shape and provenance
// ---------------------------------------------------------------------------

func TestTransformCoversTheWholeManifestInOrder(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	got := transform(t, ref, queryOf(p, 0))
	order := make([]features.ID, 0, len(got.Values))
	for _, d := range got.Values {
		order = append(order, d.Feature)
	}
	if want := manifestOrder(); !reflect.DeepEqual(order, want) {
		t.Errorf("features = %v, want the manifest order %v", order, want)
	}
}

func TestTransformCarriesItsProvenance(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	got := transform(t, ref, queryOf(p, 0))
	if got.ProfileID != p.ID {
		t.Errorf("ProfileID = %q, want %q", got.ProfileID, p.ID)
	}
	if got.ReferenceID != ref.ID {
		t.Errorf("ReferenceID = %q, want %q", got.ReferenceID, ref.ID)
	}
	if got.FeatureManifestDigest != p.FeatureManifestDigest {
		t.Errorf("FeatureManifestDigest = %q, want %q", got.FeatureManifestDigest, p.FeatureManifestDigest)
	}
	if got.Split != corpus.Test {
		t.Errorf("Split = %q, want the query's own %q", got.Split, corpus.Test)
	}
}

// A standardization fitted against one profile ranked against another's
// reference is a number with no meaning. Both mismatches are refused, and both
// directions of the manifest check are exercised.
func TestTransformRejectsAMismatchedStandardization(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	foreign := queryOf(p, 0)
	foreign.ProfileID = "another-profile"

	stale := queryOf(p, 0)
	stale.FeatureManifestDigest = "a-different-digest"

	missing := queryOf(p, 0)
	missing.Values = missing.Values[1:]

	cases := []struct {
		name  string
		query deviation.Standardization
		want  error
	}{
		{name: "another profile", query: foreign, want: deviation.ErrProfileMismatch},
		{name: "another manifest", query: stale, want: deviation.ErrManifestMismatch},
		{name: "a missing manifest feature", query: missing, want: deviation.ErrManifestMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ref.Transform(c.query); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Against real extracted text
// ---------------------------------------------------------------------------

// Every test above builds vectors literally, which is the right unit boundary
// but proves nothing about the real extractor's output flowing through. This
// one runs actual prose through text.Admit and features.Extract, so a change in
// what Extract produces cannot pass unnoticed here.
func TestStandardizeAndTransformRealExtractedText(t *testing.T) {
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
		s, err := deviation.Standardize(admit(src), mustFit(t, p), corpus.Calibrate)
		if err != nil {
			t.Fatalf("Standardize(%q): %v", src, err)
		}
		segments = append(segments, s)
	}

	ref, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}

	query, err := deviation.Standardize(admit("Whether the passage supports the reading is a question the passage itself cannot settle."), mustFit(t, p), corpus.Test)
	if err != nil {
		t.Fatalf("Standardize the query: %v", err)
	}
	got, err := ref.Transform(query)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}

	if len(got.Values) != len(features.Definitions()) {
		t.Fatalf("got %d deviations, want %d", len(got.Values), len(features.Definitions()))
	}
	bound, ok := ref.Cap(features.FunctionWordRate)
	if !ok {
		t.Fatalf("no cap for function_word_rate")
	}
	for _, d := range got.Values {
		if math.IsNaN(d.Value) || math.IsInf(d.Value, 0) {
			t.Errorf("%s transformed to %v on real text", d.Feature, d.Value)
		}
		if d.Defined && math.Abs(d.Value) > bound+1e-12 {
			t.Errorf("%s transformed to %v, beyond the five-value cap %v", d.Feature, d.Value, bound)
		}
	}
}

// ---------------------------------------------------------------------------
// The split boundary is recorded, not assumed
// ---------------------------------------------------------------------------

// A reference declared to be built on Calibrate but assembled from whatever the
// caller passed enforces nothing: the argument is a label and the segments carry
// no record of where they came from. Each standardization therefore carries its
// own split, set where the split is actually known, and the reference refuses
// any segment that does not come from the split it claims.
//
// This does not let the package verify a split it is never told. It does let it
// refuse to accept a claim nobody made, which is the difference between an
// unchecked argument and a checked field.
func TestBuildReferenceRefusesSegmentsFromAnotherSplit(t *testing.T) {
	p := profileWith()

	fromTrain := referenceSegments(p, 4)
	for i := range fromTrain {
		fromTrain[i].Split = corpus.Train
	}

	mixed := referenceSegments(p, 4)
	mixed[2].Split = corpus.Test

	unlabelled := referenceSegments(p, 4)
	unlabelled[1].Split = ""

	cases := []struct {
		name     string
		segments []deviation.Standardization
	}{
		{name: "every segment from train", segments: fromTrain},
		{name: "one segment from test", segments: mixed},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := deviation.BuildReference(p, corpus.Calibrate, c.segments, 3); !errors.Is(err, deviation.ErrReferenceSplit) {
				t.Errorf("err = %v, want %v", err, deviation.ErrReferenceSplit)
			}
		})
	}

	// A segment carrying no split at all is not "from the wrong split", it is a
	// standardization that never recorded where it came from.
	if _, err := deviation.BuildReference(p, corpus.Calibrate, unlabelled, 3); !errors.Is(err, deviation.ErrUnknownSplit) {
		t.Errorf("unlabelled segment: err = %v, want %v", err, deviation.ErrUnknownSplit)
	}
}

// An unrecorded split is not a default. Standardizing without one would put the
// burden back on a caller to remember, which is what the field exists to remove.
func TestStandardizeRefusesAnUnrecordedSplit(t *testing.T) {
	if _, err := deviation.Standardize(vectorWith(25), mustFit(t, profileWith()), ""); !errors.Is(err, deviation.ErrUnknownSplit) {
		t.Errorf("err = %v, want %v", err, deviation.ErrUnknownSplit)
	}
}

// The query being transformed is not restricted to a split: reported figures
// come from Test, but a caller may legitimately score a segment from anywhere.
// The restriction is on what may become a reference, not on what may be ranked.
func TestTransformAcceptsAQueryFromAnySplit(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Test} {
		t.Run(string(split), func(t *testing.T) {
			query := queryOf(p, 0.5)
			query.Split = split
			got := deviationOf(t, transform(t, ref, query), features.FunctionWordRate)
			if !got.Defined {
				t.Fatalf("undefined: %v", got.Reason)
			}
			if !closeTo(got.Value, 0.0) {
				t.Errorf("deviation = %v, want 0.0", got.Value)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Exactly once, not merely at least once
// ---------------------------------------------------------------------------

// The suite refuses inputs missing a manifest feature. The symmetric condition
// is a feature appearing twice, which a first-match lookup discards in silence:
// the second entry is never read, so a vector carrying two different values for
// the same feature produces a number with no stated basis.
func TestDuplicateManifestEntriesAreRefused(t *testing.T) {
	t.Run("in a vector", func(t *testing.T) {
		v := vectorWith(25)
		v.Values = append(v.Values, value(features.FunctionWordRate, 0.9, 0.01))
		if _, err := deviation.Standardize(v, mustFit(t, profileWith()), corpus.Calibrate); !errors.Is(err, deviation.ErrManifestMismatch) {
			t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
		}
	})

	t.Run("in a profile", func(t *testing.T) {
		p := profileWith()
		p.Stats = append(p.Stats, stat(features.FunctionWordRate, 0.9, 0.02))
		if _, err := deviation.Standardize(vectorWith(25), mustFit(t, p), corpus.Calibrate); !errors.Is(err, deviation.ErrManifestMismatch) {
			t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
		}
	})

	t.Run("in a reference segment", func(t *testing.T) {
		p := profileWith()
		segments := referenceSegments(p, 4)
		segments[1].Values = append(segments[1].Values, deviation.Standardized{
			Feature: features.FunctionWordRate,
			Value:   9,
			Defined: true,
		})
		if _, err := deviation.BuildReference(p, corpus.Calibrate, segments, 3); !errors.Is(err, deviation.ErrManifestMismatch) {
			t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
		}
	})

	t.Run("in a transform query", func(t *testing.T) {
		p := profileWith()
		ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)
		query := queryOf(p, 0.5)
		query.Values = append(query.Values, deviation.Standardized{
			Feature: features.FunctionWordRate,
			Value:   9,
			Defined: true,
		})
		if _, err := ref.Transform(query); !errors.Is(err, deviation.ErrManifestMismatch) {
			t.Errorf("err = %v, want %v", err, deviation.ErrManifestMismatch)
		}
	})
}

// ---------------------------------------------------------------------------
// Each feature is ranked against its own distribution
// ---------------------------------------------------------------------------

// Every rank expectation above is on function_word_rate, so an implementation
// that built one distribution and ranked every feature against it would pass
// them all. Here two features are ranked in the SAME call against deliberately
// different references:
//
//	function_word_rate: reference {-1, 0, 0.8, 1}, query 0.5
//	                    L = 2, T = 0, u = 2.5/5 = 0.5  -> 0.0
//	comma_density:      reference {0, 1, 2, 3},    query 0
//	                    L = 0, T = 1, u = 1.0/5 = 0.2  -> -0.8416212335729142
//
// One distribution serving both cannot produce this pair.
func TestEachFeatureIsRankedAgainstItsOwnReference(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	got := transform(t, ref, queryOf(p, 0.5))

	rate := deviationOf(t, got, features.FunctionWordRate)
	density := deviationOf(t, got, features.CommaDensity)
	if !rate.Defined || !density.Defined {
		t.Fatalf("undefined: %v and %v", rate.Reason, density.Reason)
	}
	if !closeTo(rate.Value, 0.0) {
		t.Errorf("function_word_rate = %v, want 0.0", rate.Value)
	}
	if !closeTo(density.Value, -0.8416212335729142) {
		t.Errorf("comma_density = %v, want -0.8416212335729142", density.Value)
	}
}

// The reference identity must move when any feature's distribution moves, not
// only the one the rest of this file exercises.
func TestReferenceIdentityFollowsEveryFeature(t *testing.T) {
	p := profileWith()

	base, err := deviation.BuildReference(p, corpus.Calibrate, referenceSegments(p, 4), 2)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}

	for _, id := range manifestOrder() {
		t.Run(string(id), func(t *testing.T) {
			segments := referenceSegments(p, 4)
			for j := range segments[2].Values {
				if segments[2].Values[j].Feature == id {
					segments[2].Values[j].Value = 41.5
				}
			}
			moved, err := deviation.BuildReference(p, corpus.Calibrate, segments, 2)
			if err != nil {
				t.Fatalf("BuildReference: %v", err)
			}
			if moved.ID == base.ID {
				t.Errorf("moving %s left the reference ID at %q", id, base.ID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Ties, generally
// ---------------------------------------------------------------------------

// A query tied with one reference value and a query tied with all of them are
// both satisfiable by special cases. A partial multiway tie is not:
//
//	reference {0, 0, 0, 1}, query 0
//	L = 0, T = 3, n = 4, u = (0 + 1.5 + 0.5)/5 = 0.4 -> -0.2533471031357998
//
// The result is below zero because three of four reference values sit at the
// query and the fourth is above it, so the query is in the lower half of the
// distribution — which counting only strictly-less values would miss entirely.
func TestTransformHandlesAPartialMultiwayTie(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, 0, 0, 0, 1)

	got := deviationOf(t, transform(t, ref, queryOf(p, 0)), features.FunctionWordRate)
	if !got.Defined {
		t.Fatalf("undefined: %v", got.Reason)
	}
	if !closeTo(got.Value, -0.2533471031357998) {
		t.Errorf("deviation = %v, want -0.2533471031357998", got.Value)
	}
}

// ---------------------------------------------------------------------------
// The declared minimum survives construction
// ---------------------------------------------------------------------------

// DESIGN Section 2 makes the minimum reference size a published figure. A
// reference that consumed its minimum and kept only the derived availability
// cannot publish it, and a report would have to take the number on trust from
// whatever called BuildReference.
func TestReferenceRecordsItsDeclaredMinimum(t *testing.T) {
	p := profileWith()
	ref, err := deviation.BuildReference(p, corpus.Calibrate, referenceSegments(p, 8), 5)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	if ref.MinSegments != 5 {
		t.Errorf("MinSegments = %d, want 5", ref.MinSegments)
	}
	if ref.Segments != 8 {
		t.Errorf("Segments = %d, want 8", ref.Segments)
	}
	if ref.ProfileID != p.ID {
		t.Errorf("ProfileID = %q, want %q", ref.ProfileID, p.ID)
	}
	if ref.FeatureManifestDigest != p.FeatureManifestDigest {
		t.Errorf("FeatureManifestDigest = %q, want %q", ref.FeatureManifestDigest, p.FeatureManifestDigest)
	}
}

// ---------------------------------------------------------------------------
// The split survives the transform
// ---------------------------------------------------------------------------

// Reported figures come from Test, so a consumer of a deviation record has to
// be able to tell which split produced it. Dropping the split at the transform
// makes a Test result indistinguishable from a Train one in exactly the artifact
// a report is built from.
func TestTransformCarriesTheQuerySplit(t *testing.T) {
	p := profileWith()
	ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)

	for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Test} {
		t.Run(string(split), func(t *testing.T) {
			query := queryOf(p, 0.5)
			query.Split = split
			if got := transform(t, ref, query); got.Split != split {
				t.Errorf("Split = %q, want %q", got.Split, split)
			}
		})
	}
}

// An unrecorded split is refused, but so is an invented one. Accepting any
// non-empty label would leave the contract satisfied by a caller typing
// anything at all, which is the state the field was added to end.
func TestStandardizeRefusesAnUnknownSplit(t *testing.T) {
	for _, split := range []corpus.Split{"holdout", "validation", "CALIBRATE", " calibrate"} {
		t.Run(string(split), func(t *testing.T) {
			if _, err := deviation.Standardize(vectorWith(25), mustFit(t, profileWith()), split); !errors.Is(err, deviation.ErrUnknownSplit) {
				t.Errorf("err = %v, want %v", err, deviation.ErrUnknownSplit)
			}
		})
	}
}

func TestStandardizeAcceptsEveryDeclaredSplit(t *testing.T) {
	for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Test} {
		t.Run(string(split), func(t *testing.T) {
			got, err := deviation.Standardize(vectorWith(25), mustFit(t, profileWith()), split)
			if err != nil {
				t.Fatalf("Standardize: %v", err)
			}
			if got.Split != split {
				t.Errorf("Split = %q, want %q", got.Split, split)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The reference is tied to its profile
// ---------------------------------------------------------------------------

// Two references over identical standardized distributions but fitted against
// different profiles are different artifacts: the standardized values mean
// different things because they were measured against different means and
// variances. A hash over the values alone would conflate them and serve one
// author's reference for another's.
func TestReferenceIdentityIncludesTheProfile(t *testing.T) {
	one := profileWith()
	two := profileWith()
	two.ID = "a-different-profile"

	refOne, err := deviation.BuildReference(one, corpus.Calibrate, referenceSegments(one, 4), 2)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	refTwo, err := deviation.BuildReference(two, corpus.Calibrate, referenceSegments(two, 4), 2)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}

	if refOne.ID == refTwo.ID {
		t.Errorf("references over identical values but different profiles share the ID %q", refOne.ID)
	}
}

// ---------------------------------------------------------------------------
// Malformed persisted inputs
// ---------------------------------------------------------------------------

// Section 2 says a value is never NaN and never a sentinel, so a NaN, an
// infinity or a negative variance arriving from a persisted artifact is not an
// edge case to absorb — it is a violated invariant, and the artifact is corrupt.
// Absorbing it as "undefined" would let a corrupt profile score text and report
// a clean-looking number.
func TestMalformedNumbersAreRefused(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)

	withValue := func(id features.ID, v features.FeatureValue) features.Vector {
		out := vectorWith(25)
		for i := range out.Values {
			if out.Values[i].ID == id {
				out.Values[i] = v
			}
		}
		return out
	}
	withStat := func(id features.ID, st profile.Stats) *profile.Profile {
		out := profileWith()
		for i := range out.Stats {
			if out.Stats[i].Feature == id {
				out.Stats[i] = st
			}
		}
		return out
	}

	const id = features.FunctionWordRate

	// Every non-finite value in every numeric position, rather than one
	// representative each: an implementation that guards NaN and +Inf but lets
	// -Inf through is the exact shape of bug a representative sample misses.
	t.Run("standardize", func(t *testing.T) {
		positions := []struct {
			name string
			with func(float64) (features.Vector, *profile.Profile)
		}{
			{name: "feature value", with: func(bad float64) (features.Vector, *profile.Profile) {
				return withValue(id, value(id, bad, 0.01)), profileWith()
			}},
			{name: "sampling variance", with: func(bad float64) (features.Vector, *profile.Profile) {
				return withValue(id, value(id, 0.5, bad)), profileWith()
			}},
			{name: "profile mean", with: func(bad float64) (features.Vector, *profile.Profile) {
				return vectorWith(25), withStat(id, stat(id, bad, 0.01))
			}},
			{name: "profile variance", with: func(bad float64) (features.Vector, *profile.Profile) {
				return vectorWith(25), withStat(id, stat(id, 0.4, bad))
			}},
		}
		bad := []struct {
			name  string
			value float64
		}{
			{name: "NaN", value: nan},
			{name: "positive infinity", value: inf},
			{name: "negative infinity", value: math.Inf(-1)},
		}

		for _, position := range positions {
			for _, b := range bad {
				t.Run(position.name+"/"+b.name, func(t *testing.T) {
					v, p := position.with(b.value)
					if _, err := deviation.Standardize(v, mustFit(t, p), corpus.Calibrate); !errors.Is(err, deviation.ErrMalformedInput) {
						t.Errorf("err = %v, want %v", err, deviation.ErrMalformedInput)
					}
				})
			}
		}

		// A negative variance is finite and still impossible.
		t.Run("negative sampling variance", func(t *testing.T) {
			if _, err := deviation.Standardize(withValue(id, value(id, 0.5, -0.01)), mustFit(t, profileWith()), corpus.Calibrate); !errors.Is(err, deviation.ErrMalformedInput) {
				t.Errorf("err = %v, want %v", err, deviation.ErrMalformedInput)
			}
		})
		t.Run("negative profile variance", func(t *testing.T) {
			if _, err := deviation.Standardize(vectorWith(25), mustFit(t, withStat(id, stat(id, 0.4, -0.01))), corpus.Calibrate); !errors.Is(err, deviation.ErrMalformedInput) {
				t.Errorf("err = %v, want %v", err, deviation.ErrMalformedInput)
			}
		})
	})

	t.Run("build reference", func(t *testing.T) {
		for _, bad := range []float64{nan, inf, math.Inf(-1)} {
			segments := referenceSegments(profileWith(), 4)
			for j := range segments[1].Values {
				if segments[1].Values[j].Feature == id {
					segments[1].Values[j].Value = bad
				}
			}
			if _, err := deviation.BuildReference(profileWith(), corpus.Calibrate, segments, 2); !errors.Is(err, deviation.ErrMalformedInput) {
				t.Errorf("reference value %v: err = %v, want %v", bad, err, deviation.ErrMalformedInput)
			}
		}
	})

	t.Run("transform", func(t *testing.T) {
		p := profileWith()
		ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)
		for _, bad := range []float64{nan, inf, math.Inf(-1)} {
			if _, err := ref.Transform(queryOf(p, bad)); !errors.Is(err, deviation.ErrMalformedInput) {
				t.Errorf("query %v: err = %v, want %v", bad, err, deviation.ErrMalformedInput)
			}
		}
	})
}

// An undefined value carries a zero it is not asked to justify, so the finiteness
// check applies to defined values only. A profile statistic below its minimum
// carries a zero mean and zero variance and must still standardize to "undefined
// for the stated reason", not to a malformed-input error.
func TestMalformedChecksApplyOnlyToDefinedValues(t *testing.T) {
	const id = features.FunctionWordRate
	p := profileWith(profile.Stats{Feature: id, N: 3, MinObservations: 20})
	v := vectorWith(25)
	for i := range v.Values {
		if v.Values[i].ID == id {
			v.Values[i] = features.FeatureValue{ID: id, SamplingVarianceDefined: true}
		}
	}

	got := standardizedOf(t, standardize(t, v, p), id)
	if got.Defined {
		t.Fatalf("value is defined as %v; want undefined", got.Value)
	}
	if got.Reason != deviation.ReasonFeatureUndefined {
		t.Errorf("reason = %q, want %q", got.Reason, deviation.ReasonFeatureUndefined)
	}
}

// The split contract holds at every entry point, not only where it is first
// recorded. A persisted standardization or a direct call could otherwise carry
// an invented label all the way into a deviation artifact.
func TestUnknownSplitsAreRefusedAtEveryEntryPoint(t *testing.T) {
	p := profileWith()
	unknown := []corpus.Split{"", "holdout", "CALIBRATE"}

	t.Run("build reference", func(t *testing.T) {
		for _, split := range unknown {
			t.Run(string(split), func(t *testing.T) {
				if _, err := deviation.BuildReference(p, split, referenceSegments(p, 4), 3); !errors.Is(err, deviation.ErrUnknownSplit) {
					t.Errorf("err = %v, want %v", err, deviation.ErrUnknownSplit)
				}
			})
		}
	})

	t.Run("transform", func(t *testing.T) {
		ref := referenceOf(t, p, 2, -1, 0, 0.8, 1)
		for _, split := range unknown {
			t.Run(string(split), func(t *testing.T) {
				query := queryOf(p, 0.5)
				query.Split = split
				if _, err := ref.Transform(query); !errors.Is(err, deviation.ErrUnknownSplit) {
					t.Errorf("err = %v, want %v", err, deviation.ErrUnknownSplit)
				}
			})
		}
	})
}

// The manifest digest is part of the reference identity for the same reason it
// is part of the profile's: the same standardized number means something
// different under a different feature manifest, and a cache must not serve one
// for the other. Same profile ID, same values, different digest.
//
// This is also why BuildReference checks the digest for AGREEMENT and not for
// currency, where Standardize checks both. Standardize measures against the live
// manifest, so a profile fitted under another one is not comparable to what it
// is about to compute. BuildReference only assembles values that were already
// measured; requiring them to match the running binary's manifest would make the
// digest a constant within any one process, and a constant cannot distinguish a
// cache written under one manifest from a cache read under the next.
func TestReferenceIdentityIncludesTheManifestDigest(t *testing.T) {
	one := profileWith()
	two := profileWith()
	two.FeatureManifestDigest = "a-different-but-internally-consistent-digest"

	refOne, err := deviation.BuildReference(one, corpus.Calibrate, referenceSegments(one, 4), 2)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	refTwo, err := deviation.BuildReference(two, corpus.Calibrate, referenceSegments(two, 4), 2)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}

	if refOne.ProfileID != refTwo.ProfileID {
		t.Fatalf("the two references have different profile IDs; the test cannot isolate the digest")
	}
	if refOne.ID == refTwo.ID {
		t.Errorf("references under different feature manifests share the ID %q", refOne.ID)
	}
}

// mustFit projects a built profile the way store and score do, so these tests
// exercise Standardize through the same narrow input production uses rather
// than through a union kept alive for their convenience.
func mustFit(t *testing.T, p *profile.Profile) profile.Fitted {
	t.Helper()
	fitted, err := p.Fitted()
	if err != nil {
		t.Fatalf("Fitted: %v", err)
	}
	return fitted
}
