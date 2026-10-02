package rewrite_test

// #137. `Epsilon` was declared with the claim that it "rejects ties while
// remaining below the resolution of a score", and the second half is false.
//
// The old argument computed the finest expressible change as `2.5/((n+1)*k)` —
// 0.0134 at a reference of thirty with the shipped manifest's six features. That
// approximates a SINGLE feature's rank step. The distance is a MEAN over k
// features, so changes in different features partly cancel and the total can be
// far finer than one step.
//
// # The witness, through the real Transform and Distance
//
// A reference of thirty values per feature and two query vectors whose per-feature
// ranks differ in both directions:
//
//	current     0.51465835887477385
//	candidate   0.51465835874341492
//	improvement 1.31358923738389421e-10   positive
//
// The improvement is real, it is about 10^8 times finer than the change the old
// argument called the finest expressible, and the loop rejects it. So the claim
// was not imprecise — it was wrong about which quantity it described.
//
// # What is true
//
// Epsilon's job is rejecting ties, and it is load-bearing for that. Acceptance is
// `candidate <= current - Epsilon`: the comparison is not strict, so at a tolerance
// of zero a tie would be ACCEPTED and `current` would advance without improving.
// ADR 0006's monotonicity needs exactly this and nothing about resolution.
//
// The tolerance also rejects small positive improvements. Not every improvement
// smaller than Epsilon, though — acceptance is governed by the ROUNDED threshold
// `current - Epsilon`, and at current = 1.0 that threshold is an improvement of
// 9.9999997171806854e-10, under Epsilon and accepted. Declining small improvements
// is a policy choice, and these tests do not argue it is the right one; they record
// that it is a choice rather than a consequence of the score's resolution.
//
// # What is still unmeasured
//
// How often a REAL rewrite lands inside the tolerance. The witness shows the
// arithmetic admits such improvements; it says nothing about their frequency in
// prose, which would need the maintainer's corpus and a provider. `EpsilonDerived`
// records that the value is not derived from a measurement — pinned beside the
// other declared figures in `rewrite_test.go`, not here — and #137 stays open for
// the measurement rather than being closed by this slice.
//
// The quantitative picture — an enumeration of achievable scores and the
// distribution of gaps between them — is in docs/DESIGN.md with its assumptions,
// because it depends on the summation order `Distance` uses and on the reference
// having distinct values. One witness belongs here; millions of enumerated scores
// do not.

import (
	"fmt"
	"math"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// An improvement the score can actually produce, inside the tolerance, rejected
// by the real loop.
//
// `TestAcceptanceTurnsOnTheDistance` already covers an improvement just under the
// tolerance, and `TestImprovementIsStillStrictWhenUncalibrated` one inside it —
// both with INVENTED scores (0.50-5e-10, 0.5-Epsilon/2). Neither establishes that
// the score can produce such a pair; they assume the premise #137 says is false.
//
// So this case does two things the others cannot do separately: it derives the
// pair from the real `Reference.Transform` and `Deviations.Distance`, and it sends
// those measured scores through `Loop.Rewrite`. Asserting the comparison inline
// instead would only restate the operator — it would stay green if the loop
// dropped Epsilon entirely.
//
// The two vectors move ranks in both directions, which is the cancellation the old
// argument missed: it reasoned about one feature's rank step and the distance is a
// mean over six.
func TestAnAchievableImprovementInsideTheToleranceIsRejected(t *testing.T) {
	reference := epsilonReference(t, 30)
	// Query values landing between reference values, so each feature's rank is
	// unambiguous.
	current := epsilonDistance(t, reference, []float64{8.5, 8.5, 16.5, 20.5, 21.5, 23.5})
	candidate := epsilonDistance(t, reference, []float64{2.5, 3.5, 10.5, 14.5, 17.5, 17.5})

	improvement := current - candidate
	if improvement <= 0 {
		t.Fatalf("the fixture's candidate does not improve: current %.17f candidate %.17f",
			current, candidate)
	}
	if improvement >= rewrite.Epsilon {
		t.Fatalf("the improvement is %.6e, which is NOT inside Epsilon %.6e — this "+
			"fixture no longer demonstrates the claim it exists for",
			improvement, rewrite.Epsilon)
	}
	// The old argument's figure, so the comparison is on the record rather than in
	// prose: 2.5/((n+1)*k) at n=30 with the shipped manifest.
	claimed := 2.5 / float64((30+1)*len(features.Definitions()))
	if improvement >= claimed {
		t.Errorf("the improvement %.6e is not finer than the old argument's claimed "+
			"finest expressible change %.9f, so the witness does not refute it",
			improvement, claimed)
	}

	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(current), better: scored(candidate)},
		[]string{better}, passingGate())

	got := run(t, loop)

	if got.Changed {
		t.Fatalf("an achievable improvement of %.6e was accepted; the tolerance is %.6e",
			improvement, rewrite.Epsilon)
	}
	if got.Text != original {
		t.Errorf("text = %q, want the original unchanged", got.Text)
	}
	if len(store.attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	attempt := store.attempts[0]
	if attempt.Accepted {
		t.Error("the stored attempt is marked accepted")
	}
	if attempt.Rejection != rewrite.RejectionNotImproved {
		t.Errorf("rejection = %q, want %q", attempt.Rejection, rewrite.RejectionNotImproved)
	}
	// The measured pair reached the decision, rather than some other scores.
	if attempt.CurrentDistance != current || attempt.CandidateDistance != candidate {
		t.Errorf("stored distances %.17f -> %.17f, want the measured %.17f -> %.17f",
			attempt.CurrentDistance, attempt.CandidateDistance, current, candidate)
	}
}

// The boundary itself. Acceptance is `candidate <= current - Epsilon`, so the
// COMPUTED float64 threshold is accepted and its next representable neighbor above
// is not.
//
// The existing table covers clearly-above and clearly-below; equality at the
// threshold was uncovered, which let a `>` -> `>=` change in the loop's comparison
// pass the whole package.
//
// Two values of current, because the rule is a comparison against a ROUNDED
// threshold and not a subtraction. At 0.50 the two are indistinguishable. At 1.0
// the threshold rounds to 0.99999999900000003, whose distance from current is
// 9.9999997171806854e-10 — strictly LESS than Epsilon, and accepted. So rewriting
// the rule as `current-candidate < Epsilon` rejects a candidate production accepts,
// and that rearrangement survives the entire package when only 0.50 is exercised.
func TestAcceptanceIncludesTheComputedThreshold(t *testing.T) {
	for _, currentDistance := range []float64{0.50, 1.0} {
		threshold := currentDistance - rewrite.Epsilon

		cases := []struct {
			name      string
			candidate float64
			accepted  bool
		}{
			{"one ulp below the threshold", math.Nextafter(threshold, math.Inf(-1)), true},
			{"exactly the threshold", threshold, true},
			{"one ulp above the threshold", math.Nextafter(threshold, math.Inf(1)), false},
		}

		for _, c := range cases {
			t.Run(fmt.Sprintf("current %.2f/%s", currentDistance, c.name), func(t *testing.T) {
				loop, _, _, _, store := loopOver(t,
					map[string]score.Report{
						original: scored(currentDistance),
						better:   scored(c.candidate),
					},
					[]string{better}, passingGate())

				got := run(t, loop)

				if got.Changed != c.accepted {
					t.Fatalf("changed = %v, want %v (%.17f -> %.17f, threshold %.17f)",
						got.Changed, c.accepted, currentDistance, c.candidate, threshold)
				}
				if len(store.attempts) == 0 {
					t.Fatal("no attempt was recorded")
				}
				want := rewrite.RejectionNone
				if !c.accepted {
					want = rewrite.RejectionNotImproved
				}
				if code := store.attempts[0].Rejection; code != want {
					t.Errorf("rejection = %q, want %q", code, want)
				}
			})
		}
	}
}

// epsilonReference builds a reference of n DISTINCT values 0..n-1 for every
// manifest feature. Distinctness is what makes each feature's rank positions
// known exactly; a reference with ties would place a query value differently.
func epsilonReference(t *testing.T, n int) *deviation.Reference {
	t.Helper()
	values := make(map[features.ID][]float64, len(features.Definitions()))
	for _, definition := range features.Definitions() {
		column := make([]float64, n)
		for i := range column {
			column[i] = float64(i)
		}
		values[definition.ID] = column
	}
	return &deviation.Reference{
		ID: "epsilon-reference", ProfileID: "epsilon-profile",
		FeatureManifestDigest: features.ManifestDigest(),
		Algorithm:             deviation.Algorithm,
		Split:                 corpus.Train, MinSegments: 1, Segments: n,
		Values: values,
	}
}

// epsilonDistance scores one query vector through the real transform, taking the
// values in manifest order.
//
// The length check pins the feature COUNT, not the manifest's identity: six
// different features, or the same six reordered, would still be scored. That is
// enough, because the caller asserts the improvement is positive and inside
// Epsilon — a manifest change that broke the demonstration fails there, with the
// length check only giving a clearer message for the commonest cause.
func epsilonDistance(t *testing.T, reference *deviation.Reference, perFeature []float64) float64 {
	t.Helper()
	definitions := features.Definitions()
	if len(perFeature) != len(definitions) {
		t.Fatalf("the fixture supplies %d values and the manifest declares %d features; "+
			"this witness was chosen for the shipped manifest and has to be rechosen if "+
			"the feature set changes", len(perFeature), len(definitions))
	}
	values := make([]deviation.Standardized, 0, len(definitions))
	for i, definition := range definitions {
		values = append(values, deviation.Standardized{
			Feature: definition.ID, Value: perFeature[i], Defined: true,
		})
	}
	deviations, err := reference.Transform(deviation.Standardization{
		ProfileID:             reference.ProfileID,
		FeatureManifestDigest: features.ManifestDigest(),
		Split:                 corpus.Train, LexicalTokens: 40, Values: values,
	})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	distance, err := deviations.Distance()
	if err != nil {
		t.Fatalf("Distance: %v", err)
	}
	if !distance.Defined {
		t.Fatalf("the witness scored undefined: %s", distance.Reason)
	}
	return distance.Value
}
