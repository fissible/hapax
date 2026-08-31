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
	// Distractors cluster by AUTHOR, not by document, so that one prolific
	// writer cannot dominate the comparison. A pool whose members all carry the
	// same author name is one cluster however many files it holds — which is
	// how this fixture first came back with a cap of minus two.
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

	// Thresholds come from the CALIBRATE population and both gates are then
	// measured over the TEST one, which is what makes their population
	// identities agree — NewRelease refuses a pair that measured different
	// populations, and building both from one split is the easy way to trip it.
	population := func(split corpus.Split) []eval.ClassedDistance {
		var out []eval.ClassedDistance
		for i := 0; i < 60; i++ {
			d := distance(eval.ClassAuthor, digest("author", strconv.Itoa(i)), "the-author", 0.10+float64(i)*0.001)
			d.Distance.Split = split
			out = append(out, d)
		}
		for i := 0; i < 30; i++ {
			d := distance(eval.ClassDistractor, digest("distractor", strconv.Itoa(i)),
				"other-writer-"+strconv.Itoa(i), 3.00+float64(i)*0.001)
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
	if !release.Shippable {
		t.Fatalf("the fixture did not ship: %s (auc=%.3f bound=%.3f cap=%.3f clusters=%d/%d min=%d calibrated=%v)",
			release.Reason, release.Discrimination.AUC, release.Discrimination.LowerBound,
			release.Discrimination.Cap, release.Discrimination.AuthorClusters,
			release.Discrimination.DistractorClusters, release.Discrimination.MinClusters,
			release.Calibration.Calibrated)
	}
	return release
}
