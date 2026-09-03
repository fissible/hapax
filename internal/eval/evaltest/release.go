// Package evaltest builds evaluation artifacts that are expensive to reach
// honestly, for the packages that need one to test something else.
//
// It exists because a SHIPPABLE release cannot be produced from a small corpus:
// the discrimination bound is capped at 1 - 3/clusters and the calibration gate
// wants ceil(3/target) clusters per band, so a real one needs sixty held-out
// author documents and thirty distractors — a corpus of about seven hundred.
// Crafted distances reach the same place for the cost of some numbers.
//
// Both internal/store and internal/workflow need one, and a fixture this
// particular is not something to keep two copies of.
package evaltest

import (
	"strconv"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
)

// digest is a stable stand-in identity, shaped like the content-derived ones the
// store requires.
func digest(parts ...string) string {
	return identity.HashInputs(map[string]string{"parts": strings.Join(parts, "/")})
}

// ShippableRelease builds a release through eval's own constructors rather than
// by hand, so what the store's codec has to reconstruct is a real domain
// artifact and not a literal that happens to look like one.
//
// The cluster counts are not arbitrary. The discrimination bound is capped at
// 1 - 3/clusters and the calibration gate wants ceil(3/target) clusters per
// band, which is sixty author clusters at p_author = 0.05 and thirty distractor
// clusters at p_distractor = 0.10. A fixture short of those cannot ship however
// cleanly its distances separate — the distances here are just numbers, so they
// are cheap where seven hundred documents would not be.
func ShippableRelease(t *testing.T, profileID, referenceID string) eval.Release {
	t.Helper()
	return ReleaseAround(t, profileID, referenceID, 0.10, 3.00)
}

// ReleaseAround is ShippableRelease with the two populations placed where the
// caller needs them, so a test can decide which band a measured distance falls
// in. The boundaries are quantiles of these populations rather than either
// argument, so a caller that depends on where they land must read Low and High
// off the result and say so — see the callers in internal/workflow.
//
// It exists because band membership is otherwise an accident of whatever the
// fixture corpus happens to measure, and a test asserting "this paragraph is a
// rewrite target" against an accident is asserting nothing.
func ReleaseAround(t *testing.T, profileID, referenceID string, authorCenter, distractorCenter float64) eval.Release {
	t.Helper()
	release := ReleaseAroundUnchecked(t, profileID, referenceID, authorCenter, distractorCenter)
	if !release.Shippable {
		t.Fatalf("the fixture did not ship: %s (auc=%.3f bound=%.3f cap=%.3f clusters=%d/%d min=%d calibrated=%v)",
			release.Reason, release.Discrimination.AUC, release.Discrimination.LowerBound,
			release.Discrimination.Cap, release.Discrimination.AuthorClusters,
			release.Discrimination.DistractorClusters, release.Discrimination.MinClusters,
			release.Calibration.Calibrated)
	}
	return release
}

// ReleaseAroundUnchecked is ReleaseAround without the shippability assertion,
// for the cases a shippable fixture cannot express: populations that overlap,
// or that sit the wrong way round entirely. #83 is about what the store does
// with exactly those, and ReleaseAround cannot build one because it fails the
// test rather than returning it.
func ReleaseAroundUnchecked(t *testing.T, profileID, referenceID string, authorCenter, distractorCenter float64) eval.Release {
	t.Helper()
	// Distractors cluster by AUTHOR when every one carries a name, and by
	// DOCUMENT when none does. This fixture leaves the author empty on purpose,
	// for two reasons found the hard way. Giving thirty distractors one name
	// makes a single cluster and a cap of minus two behind a perfect AUC. And
	// giving them thirty DIFFERENT names switches the clustering mode, which
	// store refuses outright — its validation admits ClusterByDocument and
	// nothing else, so an author-clustered release cannot be persisted at all.
	//
	// Which is what hapax produces anyway: its pools carry no author, because
	// they carry no identifying metadata whatsoever. #63.
	distance := func(class eval.Class, document, author string, value float64) eval.ClassedDistance {
		return eval.ClassedDistance{
			Class: class, Document: document, Author: author,
			Distance: deviation.Distance{
				ProfileID: profileID, ReferenceID: referenceID,
				FeatureManifestDigest: features.ManifestDigest(),
				Split:                 corpus.Test, Value: value, Defined: true,
				ScoredTiers:  []features.Tier{features.TierA},
				WeightScheme: deviation.WeightSchemeUniform, Algorithm: deviation.DistanceAlgorithm,
			},
		}
	}

	// The spread is PROPORTIONAL to each centre, not a fixed 0.001 step. A fixed
	// step put the two populations on top of each other whenever the centres were
	// closer together than sixty steps — at 1e-4 and 1e-3 the author distances ran
	// from 0.0001 to 0.0591 and straight through the distractors, the AUC came out
	// near 0.26, and the fixture died on the discrimination floor instead of
	// producing a release. Scaling by the centre keeps the separation the caller
	// asked for at any magnitude.
	spread := func(centre float64, i int) float64 { return centre * (1 + float64(i)*0.001) }

	// Thresholds come from the CALIBRATE population and both gates are then
	// measured over the TEST one, which is what makes their population
	// identities agree — NewRelease refuses a pair that measured different
	// populations, and building both from one split is the easy way to trip it.
	population := func(split corpus.Split) []eval.ClassedDistance {
		var out []eval.ClassedDistance
		for i := 0; i < 60; i++ {
			d := distance(eval.ClassAuthor, digest("author", strconv.Itoa(i)), "", spread(authorCenter, i))
			d.Distance.Split = split
			out = append(out, d)
		}
		for i := 0; i < 30; i++ {
			d := distance(eval.ClassDistractor, digest("distractor", strconv.Itoa(i)),
				"", spread(distractorCenter, i))
			d.Distance.Split = split
			out = append(out, d)
		}
		return out
	}
	held, calibrating := population(corpus.Test), population(corpus.Calibrate)

	thresholds, err := eval.Calibrate(calibrating,
		eval.Source{Cohort: digest("cohort", profileID), DistractorPool: digest("pool", profileID)},
		eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	calibration, err := thresholds.CalibrateBands(held, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	discrimination, err := eval.Discriminate(held, eval.DefaultDiscrimination())
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	release, err := eval.NewRelease(discrimination, calibration)
	if err != nil {
		t.Fatalf("NewRelease: %v", err)
	}
	return release
}
