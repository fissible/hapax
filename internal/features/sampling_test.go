package features_test

// Per-feature sampling variance at the observed segment length.
//
// DESIGN Section 2's first transform correction says the deviation denominator
// "combines profile variance with the length-dependent sampling variance of the
// feature at the observed length". That is not one formula, and the amendment
// recorded in REVIEW Round 5 says so: the manifest distinguishes three families
// and each feature declares its own.
//
//	rate    — a bounded membership proportion. Binomial: p(1-p)/n
//	density — an unbounded per-token count. Conditional count model, with the
//	          lexical-token count as exposure: lambda/n
//	mean    — a mean of a per-token quantity. s^2/n, where s^2 is the SAMPLE
//	          variance of that quantity within the segment
//
// The density model is a WORKING assumption, not an empirical claim, and is
// recorded as such. Punctuation is syntax-constrained, plausibly zero-inflated
// and overdispersed relative to Poisson, and the numerator counts commas across
// all tokens while the denominator counts lexical ones. Treating it as
// quasi-Poisson with lexical exposure is defensible as a starting point and is
// subject to the same later calibration as every other declared parameter.
//
// The family is part of the feature manifest, so changing a feature's sampling
// model changes the manifest digest and therefore every profile identity
// derived from it. That is deliberate: a deviation computed under a different
// model is a different number, and a cache must not serve one for the other.
//
// # Why the sample variance, and what it costs
//
// The mean family uses the n-1 denominator, matching the profile's declared
// SampleVariance convention. It is therefore UNDEFINED at n = 1: one word
// carries no information about the spread of word lengths. A rate or density at
// n = 1 is defined, because the binomial and count models need no second
// observation.
//
// # Zero variance is a real answer, not a missing one
//
// A segment whose words are all the same length has zero within-segment
// variance; a segment with no commas has zero count. In both cases the sampling
// variance is legitimately 0 and the value is defined. It does not imply
// infinite confidence, because the deviation denominator adds the profile
// variance to it — the sampling term contributing nothing is exactly right when
// the segment itself shows no spread.

import (
	"math"
	"testing"

	"github.com/fissible/hapax/internal/features"
)

// extract() and value() live in features_test.go.

func samplingVariance(t *testing.T, v features.Vector, id features.ID) features.FeatureValue {
	t.Helper()
	fv, ok := v.Get(id)
	if !ok {
		t.Fatalf("no value for %q", id)
	}
	return fv
}

func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 1e-12
}

// ---------------------------------------------------------------------------
// The manifest
// ---------------------------------------------------------------------------

// Every feature declares a family, and the three are distinct values. A blank
// family would silently select whichever model an implementation defaulted to.
func TestEveryFeatureDeclaresASamplingFamily(t *testing.T) {
	want := map[features.ID]features.Sampling{
		features.WordLengthMean:   features.SamplingMean,
		features.CommaDensity:     features.SamplingDensity,
		features.SemicolonDensity: features.SamplingDensity,
		features.ColonDensity:     features.SamplingDensity,
		features.FunctionWordRate: features.SamplingRate,
		features.ClauseMarkerRate: features.SamplingRate,
	}
	definitions := features.Definitions()
	if len(definitions) != len(want) {
		t.Fatalf("%d definitions, %d expectations", len(definitions), len(want))
	}
	for _, d := range definitions {
		expected, ok := want[d.ID]
		if !ok {
			t.Errorf("%s has no expected sampling family", d.ID)
			continue
		}
		if d.Sampling != expected {
			t.Errorf("%s declares family %q, want %q", d.ID, d.Sampling, expected)
		}
	}

	distinct := map[features.Sampling]bool{
		features.SamplingRate: true, features.SamplingDensity: true, features.SamplingMean: true,
	}
	if len(distinct) != 3 {
		t.Error("the three sampling families are not three distinct values")
	}
	for _, family := range []features.Sampling{features.SamplingRate, features.SamplingDensity, features.SamplingMean} {
		if family == "" {
			t.Error("a sampling family is the empty string, which cannot be distinguished from an undeclared one")
		}
	}
}

// The rate and density families are genuinely different models, and the naming
// in DESIGN corrects an earlier inversion. A membership rate is bounded in
// [0,1]; a per-token density is not. Pinning them apart stops the manifest
// drifting back.
func TestRatesAndDensitiesAreDifferentFamilies(t *testing.T) {
	byID := map[features.ID]features.Definition{}
	for _, d := range features.Definitions() {
		byID[d.ID] = d
	}
	if byID[features.FunctionWordRate].Sampling == byID[features.CommaDensity].Sampling {
		t.Error("a bounded membership rate and an unbounded per-token density share a sampling family")
	}
}

// ---------------------------------------------------------------------------
// The numbers, computed by hand
// ---------------------------------------------------------------------------

// One segment, every family, every number worked out by hand.
//
//	"a, bb ccc dddd" — 4 lexical tokens of lengths 1, 2, 3, 4, and one comma.
//
//	word_length_mean   mean 2.5; sample variance ((1.5)^2+(0.5)^2+(0.5)^2+(1.5)^2)/3
//	                   = 5/3; sampling variance (5/3)/4 = 5/12
//	comma_density      lambda 1/4; sampling variance (1/4)/4 = 1/16
//	semicolon_density  lambda 0;   sampling variance 0
//	colon_density      lambda 0;   sampling variance 0
//	function_word_rate p 1/4 ("a" is a function word); p(1-p)/n
//	                   = (0.25)(0.75)/4 = 3/64
//	clause_marker_rate p 0;     sampling variance 0
func TestSamplingVarianceAgainstHandComputedValues(t *testing.T) {
	v := extract(t, "a, bb ccc dddd")
	if v.LexicalTokens != 4 {
		t.Fatalf("fixture has %d lexical tokens, want 4", v.LexicalTokens)
	}

	for id, want := range map[features.ID]float64{
		features.WordLengthMean:   5.0 / 12.0,
		features.CommaDensity:     1.0 / 16.0,
		features.SemicolonDensity: 0,
		features.ColonDensity:     0,
		features.FunctionWordRate: 3.0 / 64.0,
		features.ClauseMarkerRate: 0,
	} {
		got := samplingVariance(t, v, id)
		if !got.SamplingVarianceDefined {
			t.Errorf("%s: sampling variance undefined at 4 lexical tokens", id)
			continue
		}
		if !closeTo(got.SamplingVariance, want) {
			t.Errorf("%s: sampling variance = %v, want %v", id, got.SamplingVariance, want)
		}
	}
}

// The mean family needs a second observation; the others do not. A single word
// says nothing about the spread of word lengths, and reporting 0 there would
// claim certainty the segment cannot support.
func TestTheMeanFamilyIsUndefinedAtOneToken(t *testing.T) {
	v := extract(t, "the")
	if v.LexicalTokens != 1 {
		t.Fatalf("fixture has %d lexical tokens, want 1", v.LexicalTokens)
	}

	mean := samplingVariance(t, v, features.WordLengthMean)
	if !mean.Defined {
		t.Error("word_length_mean itself is undefined at one token; the mean of one word is that word")
	}
	if mean.SamplingVarianceDefined {
		t.Errorf("word_length_mean sampling variance is defined at one token as %v; the sample variance needs two", mean.SamplingVariance)
	}
	if mean.SamplingVariance != 0 {
		t.Errorf("an undefined sampling variance carries the value %v; it must be zero, never a sentinel", mean.SamplingVariance)
	}

	// p = 1 exactly: every lexical token is a function word. The binomial
	// variance is legitimately 0 and defined.
	rate := samplingVariance(t, v, features.FunctionWordRate)
	if !closeTo(rate.Value, 1) {
		t.Fatalf("function_word_rate = %v, want 1", rate.Value)
	}
	if !rate.SamplingVarianceDefined {
		t.Error("a rate's sampling variance is undefined at one token; the binomial model needs no second observation")
	}
	if rate.SamplingVariance != 0 {
		t.Errorf("p(1-p)/n at p=1 is %v, want 0", rate.SamplingVariance)
	}

	density := samplingVariance(t, v, features.CommaDensity)
	if !density.SamplingVarianceDefined {
		t.Error("a density's sampling variance is undefined at one token")
	}
	if density.SamplingVariance != 0 {
		t.Errorf("lambda/n at lambda=0 is %v, want 0", density.SamplingVariance)
	}
}

// Zero spread is an answer. All four words the same length gives a within-
// segment variance of exactly 0, and p = 1 gives a binomial variance of exactly
// 0 — both defined, neither a missing value.
func TestZeroVarianceIsDefined(t *testing.T) {
	v := extract(t, "a a a a")
	if v.LexicalTokens != 4 {
		t.Fatalf("fixture has %d lexical tokens, want 4", v.LexicalTokens)
	}

	for _, id := range []features.ID{features.WordLengthMean, features.FunctionWordRate} {
		got := samplingVariance(t, v, id)
		if !got.SamplingVarianceDefined {
			t.Errorf("%s: sampling variance undefined where the segment simply has no spread", id)
		}
		if got.SamplingVariance != 0 {
			t.Errorf("%s: sampling variance = %v, want exactly 0", id, got.SamplingVariance)
		}
	}
}

// No lexical tokens: the value is undefined, and so is anything derived from
// it. Section 2 is explicit that undefined is carried as a flag and never as a
// sentinel number or NaN, because these values are persisted and hashed.
func TestNothingIsDefinedWithoutLexicalTokens(t *testing.T) {
	v := extract(t, ", ; :")
	if v.LexicalTokens != 0 {
		t.Fatalf("fixture has %d lexical tokens, want 0", v.LexicalTokens)
	}

	for _, d := range features.Definitions() {
		got := samplingVariance(t, v, d.ID)
		if got.Defined {
			t.Errorf("%s is defined over zero lexical tokens", d.ID)
		}
		if got.SamplingVarianceDefined {
			t.Errorf("%s has a defined sampling variance over zero lexical tokens", d.ID)
		}
		if math.IsNaN(got.SamplingVariance) || math.IsInf(got.SamplingVariance, 0) {
			t.Errorf("%s sampling variance is %v; encoding/json refuses NaN and hashing needs a canonical bit pattern", d.ID, got.SamplingVariance)
		}
		if got.SamplingVariance != 0 {
			t.Errorf("%s undefined sampling variance carries %v, want 0", d.ID, got.SamplingVariance)
		}
	}
}

// Sampling variance falls as the segment lengthens — the whole point of
// correction 1, since a 40-token segment carries more measurement noise than a
// 400-token one and a denominator built from the profile SD alone treats them
// alike.
//
// Asserted as EXACT values at both lengths rather than "smaller", because the
// three families fall at different rates and "smaller" would accept any of
// them. The word-length case is the one that catches a plausible wrong answer:
// quadrupling the segment does NOT quarter its sampling variance, because the
// n-1 denominator of the sample variance changes too. 5/12 goes to 1/12, not to
// 5/48.
func TestSamplingVarianceAtTwoLengths(t *testing.T) {
	const unit = "a, bb ccc dddd"
	short := extract(t, unit)
	long := extract(t, unit+" "+unit+" "+unit+" "+unit)

	if short.LexicalTokens != 4 || long.LexicalTokens != 16 {
		t.Fatalf("fixtures have %d and %d lexical tokens, want 4 and 16", short.LexicalTokens, long.LexicalTokens)
	}

	for id, want := range map[features.ID][2]float64{
		// n-1 in the numerator's denominator: 5/3 over 4, then 4/3 over 16.
		features.WordLengthMean: {5.0 / 12.0, 1.0 / 12.0},
		// lambda unchanged at 1/4, so lambda/n quarters.
		features.CommaDensity: {1.0 / 16.0, 1.0 / 64.0},
		// p unchanged at 1/4, so p(1-p)/n quarters.
		features.FunctionWordRate: {3.0 / 64.0, 3.0 / 256.0},
	} {
		s, l := samplingVariance(t, short, id), samplingVariance(t, long, id)

		// The two fixtures must agree on the VALUE, or the comparison is not
		// about length alone.
		if !closeTo(s.Value, l.Value) {
			t.Fatalf("%s: the fixtures differ in value (%v and %v)", id, s.Value, l.Value)
		}
		if !closeTo(s.SamplingVariance, want[0]) {
			t.Errorf("%s at 4 tokens: sampling variance = %v, want %v", id, s.SamplingVariance, want[0])
		}
		if !closeTo(l.SamplingVariance, want[1]) {
			t.Errorf("%s at 16 tokens: sampling variance = %v, want %v", id, l.SamplingVariance, want[1])
		}
	}
}

// The declared family is part of the manifest, so changing a feature's sampling
// model must change the manifest digest — and therefore every profile identity
// derived from it. A deviation computed under a different model is a different
// number, and a cache must not serve one for the other.
//
// The digest belongs to the package that owns the manifest. `profile` consumed
// its own copy of this computation, which could not see a field it did not know
// about.
func TestTheManifestDigestCoversTheSamplingFamily(t *testing.T) {
	definitions := features.Definitions()
	if features.ManifestDigest() != features.DigestOf(definitions) {
		t.Fatal("ManifestDigest is not the digest of the current manifest")
	}

	for i := range definitions {
		altered := features.Definitions()
		switch altered[i].Sampling {
		case features.SamplingRate:
			altered[i].Sampling = features.SamplingDensity
		default:
			altered[i].Sampling = features.SamplingRate
		}
		if features.DigestOf(altered) == features.ManifestDigest() {
			t.Errorf("changing %s's sampling family did not change the manifest digest", altered[i].ID)
		}
	}

	// And the digest still covers what it covered before.
	altered := features.Definitions()
	altered[0].Description = altered[0].Description + " (reworded)"
	if features.DigestOf(altered) == features.ManifestDigest() {
		t.Error("changing a description did not change the manifest digest")
	}
}
