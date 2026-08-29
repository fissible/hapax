package eval_test

// The discrimination gate: ADR 0005's third and last release gate.
//
// ADR 0005 names "a predeclared minimum AUC" and declares no minimum, no
// orientation and no tie rule. REVIEW Round 12 supplies all three, and the
// middle one is the dangerous omission.
//
// # Orientation
//
// `d` is a DISTANCE — lower means closer to the author — so
//
//	AUC = P(d_author < d_distractor) + 0.5 * P(d_author == d_distractor)
//
// over all author x distractor pairs. An implementation reaching for the
// conventional "probability the positive scores higher" computes 1 - AUC and
// reports 0.15 for a profile that separates cleanly: low enough to read as a
// failure, high enough not to read as a bug, and nothing in the arithmetic
// objects. The inverted fixture below exists for exactly that.
//
// # Ties
//
// Half, the Mann-Whitney convention. Not a formality: `d` is a mean of six
// rank-transformed deviations that are themselves capped by the reference size,
// so exact ties are ordinary. Dropping them inflates AUC; counting them as wins
// inflates it further.
//
// # A bound, and the same degeneracy mirrored
//
// The gate is on the one-sided LOWER confidence bound, from the same clustered
// bootstrap, at 0.95. AUC is a paired statistic, so each resample draws clusters
// from both classes — independently, from their own streams — and recomputes AUC
// on the resampled pair.
//
// The band floor found that a rate observed as zero resamples to zero and gives
// an over-confident upper bound. This is the same failure from the other end:
// perfect separation resamples to 1.0 every time, so the bootstrap reports a
// lower bound of 1.0 for a profile that has merely never misordered a pair yet.
// The bound is therefore the LESSER of the bootstrap percentile and 1 - 3/c,
// where c is min(author clusters, distractor clusters).
//
// # The floor is 0.80, and it is a judgement
//
// Unlike the band minimums nothing derives it. What informs it: this tool's
// output drives EDITS TO THE USER'S OWN WRITING, and ADR 0006's loop accepts a
// rewrite whenever `d` improves — so a barely-discriminating `d` turns that loop
// into noise-driven vandalism. The bar belongs above "better than a coin".
//
// It may well not be met by v1's six Tier A features at paragraph scale. That is
// the designed behaviour, not a number to relax afterwards.
//
// The floor implies its own cluster minimum: clearing 0.80 needs 3/c <= 0.20, so
// at least fifteen clusters per class. Less demanding than the band gate's thirty
// and sixty, so in practice the band gate binds first.
//
// # The gates compose in one direction
//
// Discrimination is prior: below its floor no band is emitted whatever the
// band-level evidence says. The reverse does not hold. The release verdict owns
// that composition rather than leaving it to a caller to remember.

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
)

// ---------------------------------------------------------------------------
// Populations
// ---------------------------------------------------------------------------

// Every fixture gives each segment its own document, so the cluster count and
// the segment count coincide.

// discriminating separates perfectly: eighty author clusters below every one of
// forty distractor clusters. AUC is 1.0 and the bound is the cap, 1 - 3/40.
//
// This is the same population the band floor tests call clean(); the two names
// exist because the two gates ask different questions of it.
func discriminating() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 80)),
		perDocument(eval.ClassDistractor, span(201, 240))...,
	)
}

// tooFewClusters separates just as perfectly on a tenth of the evidence: ten
// distractor clusters cap the bound at 1 - 3/10 = 0.70, below the floor.
func tooFewClusters() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 20)),
		perDocument(eval.ClassDistractor, span(201, 210))...,
	)
}

// inverted is discriminating with the classes exchanged: the author's distances
// are all ABOVE the distractors'. AUC is 0. An implementation with the
// comparison the wrong way round reports 1.0 here and passes.
func inverted() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(201, 240)),
		perDocument(eval.ClassDistractor, span(1, 80))...,
	)
}

// allTied gives every author and every distractor the same distance, so every
// pair ties and AUC is exactly 0.5. Dropping ties leaves nothing to divide by;
// counting them as wins gives 1.0.
func allTied() []eval.ClassedDistance {
	author := make([]float64, 80)
	for i := range author {
		author[i] = 100
	}
	distractor := make([]float64, 40)
	for i := range distractor {
		distractor[i] = 100
	}
	return append(
		perDocument(eval.ClassAuthor, author),
		perDocument(eval.ClassDistractor, distractor)...,
	)
}

// boundBelowFloor is the case that separates a bound from a point estimate: AUC
// is 0.84984375, comfortably over the floor, and the lower bound is 0.79234375,
// under it.
func boundBelowFloor() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 80)),
		perDocument(eval.ClassDistractor, span(50, 89))...,
	)
}

// justAboveFloor is its neighbour, one step further apart: AUC 0.859375 and a
// bound of 0.80296875, which clears.
func justAboveFloor() []eval.ClassedDistance {
	return append(
		perDocument(eval.ClassAuthor, span(1, 80)),
		perDocument(eval.ClassDistractor, span(51, 90))...,
	)
}

func discriminationOf(t *testing.T, in []eval.ClassedDistance) eval.Discrimination {
	t.Helper()
	got, err := eval.Discriminate(in, eval.DefaultDiscrimination())
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// The declared spec
// ---------------------------------------------------------------------------

func TestDeclaredDiscriminationSpec(t *testing.T) {
	got := eval.DefaultDiscrimination()

	if got.Floor != 0.80 {
		t.Errorf("floor = %v, want 0.80", got.Floor)
	}
	if got.Confidence != 0.95 {
		t.Errorf("confidence = %v, want 0.95", got.Confidence)
	}
	if got.Resamples != 2000 {
		t.Errorf("resamples = %d, want 2000", got.Resamples)
	}
	if got.Seed != 0x68617061785F7631 {
		t.Errorf("seed = %#x, want the declared %#x", got.Seed, uint64(0x68617061785F7631))
	}
	if eval.DiscriminationAlgorithm != "clustered-auc-lower-bound-v1" {
		t.Errorf("DiscriminationAlgorithm = %q", eval.DiscriminationAlgorithm)
	}
}

// ---------------------------------------------------------------------------
// Orientation
// ---------------------------------------------------------------------------

// The assertion this whole file is built around. Two populations, identical in
// every respect except which class holds the low distances.
//
// A profile whose author segments sit below every distractor is the best
// possible discriminator and must score 1.0. Exchange the classes and it must
// score 0.0 — not 1.0, which is what a comparison written the conventional way
// round produces, and which would let a profile that identifies the author's
// prose as maximally unlike the author pass the release gate.
func TestOrientationFollowsTheDistance(t *testing.T) {
	right := discriminationOf(t, discriminating())
	wrong := discriminationOf(t, inverted())

	if right.AUC != 1 {
		t.Errorf("AUC = %v where every author distance is below every distractor, want 1", right.AUC)
	}
	if wrong.AUC != 0 {
		t.Errorf("AUC = %v where every author distance is above every distractor, want 0; a comparison the wrong way round reports 1", wrong.AUC)
	}
	if !right.Discriminates {
		t.Errorf("a perfectly separating profile does not discriminate: %v", right.Reason)
	}
	if wrong.Discriminates {
		t.Errorf("a profile that scores the author as maximally unlike the author passed the gate")
	}
}

// ---------------------------------------------------------------------------
// Ties
// ---------------------------------------------------------------------------

// Every pair ties, so every pair contributes a half and AUC is exactly 0.5 —
// chance, which is the honest answer for a profile that cannot tell the two
// classes apart at all.
//
// Dropping ties would leave no pairs to average and no defined answer. Counting
// them as wins would give 1.0 and pass the gate on a profile with no information
// whatsoever, which is the worst outcome available here.
func TestTiesCountAsHalf(t *testing.T) {
	got := discriminationOf(t, allTied())

	if got.AUC != 0.5 {
		t.Errorf("AUC = %v where every pair ties, want exactly 0.5", got.AUC)
	}
	if math.IsNaN(got.AUC) {
		t.Errorf("AUC is NaN; ties were dropped and nothing was left to divide by")
	}
	if got.Discriminates {
		t.Errorf("a profile whose every pair ties passed the gate")
	}
}

// ---------------------------------------------------------------------------
// The bound
// ---------------------------------------------------------------------------

// Both halves of the bound, exactly, under the declared seed.
//
// On the perfectly separating population every resample is also perfect, so the
// bootstrap percentile is 1.0 and the reported bound is the cap instead:
// 1 - 3/40 = 0.925. An implementation using the bootstrap alone reports 1.0 and
// still passes the gate, which is why the exact value matters rather than the
// verdict.
func TestTheBoundIsCappedAtTheRuleOfThree(t *testing.T) {
	got := discriminationOf(t, discriminating())

	if got.AUC != 1 {
		t.Fatalf("AUC = %v, want 1", got.AUC)
	}
	if got.Cap != 0.925 {
		t.Errorf("cap = %v, want 1 - 3/40 = 0.925", got.Cap)
	}
	if got.LowerBound != 0.925 {
		t.Errorf("lower bound = %v, want the cap 0.925; a bare bootstrap reports 1", got.LowerBound)
	}
}

// And the other half: where the classes overlap the bootstrap percentile falls
// below the cap and is what the bound reports.
func TestTheBoundIsTheBootstrapWhereItFallsBelowTheCap(t *testing.T) {
	got := discriminationOf(t, justAboveFloor())

	if got.AUC != 0.859375 {
		t.Fatalf("AUC = %v, want 0.859375", got.AUC)
	}
	if got.LowerBound != 0.80296875 {
		t.Errorf("lower bound = %v, want the bootstrap's 0.80296875", got.LowerBound)
	}
	if got.LowerBound >= got.Cap {
		t.Errorf("the bound %v did not fall below the cap %v; this case must be decided by the bootstrap", got.LowerBound, got.Cap)
	}
}

// The bound is never above the point estimate. A lower bound over its own
// estimate is not a bound.
func TestTheBoundIsNeverAboveTheObservedAUC(t *testing.T) {
	for _, in := range [][]eval.ClassedDistance{
		discriminating(), tooFewClusters(), inverted(), allTied(), boundBelowFloor(), justAboveFloor(),
	} {
		got := discriminationOf(t, in)
		if got.LowerBound > got.AUC {
			t.Errorf("bound %v is above the observed AUC %v", got.LowerBound, got.AUC)
		}
		if math.IsNaN(got.LowerBound) || math.IsInf(got.LowerBound, 0) {
			t.Errorf("bound = %v", got.LowerBound)
		}
	}
}

// ---------------------------------------------------------------------------
// The gate is on the bound
// ---------------------------------------------------------------------------

// The pair that separates a bound from a point estimate. Both populations have
// an AUC over the floor; only one has a bound over it, and only that one passes.
//
// An implementation gating on the point estimate passes both.
func TestTheGateIsOnTheBoundNotThePointEstimate(t *testing.T) {
	below := discriminationOf(t, boundBelowFloor())
	above := discriminationOf(t, justAboveFloor())

	if below.AUC != 0.84984375 {
		t.Fatalf("AUC = %v, want 0.84984375", below.AUC)
	}
	if below.AUC < eval.DefaultDiscrimination().Floor {
		t.Fatalf("this fixture needs an AUC above the floor; it is %v", below.AUC)
	}
	if below.LowerBound != 0.79234375 {
		t.Fatalf("lower bound = %v, want 0.79234375", below.LowerBound)
	}
	if below.Discriminates {
		t.Errorf("a bound of %v passed a floor of %v", below.LowerBound, eval.DefaultDiscrimination().Floor)
	}
	if below.Reason == "" {
		t.Errorf("a failed gate states no reason")
	}

	if !above.Discriminates {
		t.Errorf("a bound of %v failed a floor of %v: %v", above.LowerBound, eval.DefaultDiscrimination().Floor, above.Reason)
	}
	if above.Reason != "" {
		t.Errorf("a passing gate carries the reason %q", above.Reason)
	}
}

// The comparison is inclusive: a bound sitting exactly on the floor passes. The
// floor is set to the fixture's own bound to reach the equality branch, which no
// other test does.
func TestTheFloorIsInclusive(t *testing.T) {
	spec := eval.DefaultDiscrimination()
	spec.Floor = 0.79234375

	got, err := eval.Discriminate(boundBelowFloor(), spec)
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	if got.LowerBound != spec.Floor {
		t.Fatalf("bound = %v, want the floor %v exactly", got.LowerBound, spec.Floor)
	}
	if !got.Discriminates {
		t.Errorf("a bound sitting exactly on the floor was refused")
	}
}

// The floor is a parameter, not a constant: the same population flips verdict
// when the declared floor moves across its bound. An implementation hardcoding
// 0.80 passes every test above and fails this one.
func TestTheFloorIsTheDeclaredOne(t *testing.T) {
	spec := eval.DefaultDiscrimination()
	spec.Floor = 0.60

	got, err := eval.Discriminate(boundBelowFloor(), spec)
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	if !got.Discriminates {
		t.Errorf("a bound of %v failed a floor of 0.60", got.LowerBound)
	}
}

// ---------------------------------------------------------------------------
// The cluster minimum the floor implies
// ---------------------------------------------------------------------------

// Perfect separation on too little evidence. Ten distractor clusters cap the
// bound at 1 - 3/10 = 0.70, so no observation however clean can clear a floor of
// 0.80. This is where the implied minimum lives: it is not a separate check, it
// is the point at which the cap crosses the floor.
func TestPerfectSeparationOnTooFewClustersStillFails(t *testing.T) {
	got := discriminationOf(t, tooFewClusters())

	if got.AUC != 1 {
		t.Fatalf("AUC = %v, want 1", got.AUC)
	}
	if got.Cap != 0.70 {
		t.Errorf("cap = %v, want 1 - 3/10 = 0.70", got.Cap)
	}
	if got.LowerBound != 0.70 {
		t.Errorf("lower bound = %v, want the cap 0.70", got.LowerBound)
	}
	if got.Discriminates {
		t.Errorf("perfect separation on ten distractor clusters cleared a floor of 0.80")
	}
}

// The cap follows the SMALLER class, since that is what limits the evidence.
// Eighty author clusters against forty distractor clusters caps at 3/40, not
// 3/80.
func TestTheCapFollowsTheSmallerClass(t *testing.T) {
	got := discriminationOf(t, discriminating())

	if got.AuthorClusters != 80 || got.DistractorClusters != 40 {
		t.Fatalf("clusters = %d and %d, want 80 and 40", got.AuthorClusters, got.DistractorClusters)
	}
	if got.Cap == 1-3.0/80.0 {
		t.Fatalf("cap = %v, which follows the larger class", got.Cap)
	}
	if got.Cap != 1-3.0/40.0 {
		t.Errorf("cap = %v, want 1 - 3/40", got.Cap)
	}
}

// The implied minimum is reported so a user is told what the gate needs.
func TestTheImpliedClusterMinimumIsReported(t *testing.T) {
	got := discriminationOf(t, discriminating())

	// 3/c <= 1 - 0.80 gives c >= 15.
	if got.MinClusters != 15 {
		t.Errorf("minimum clusters = %d, want ceil(3/(1-0.80)) = 15", got.MinClusters)
	}
}

// ---------------------------------------------------------------------------
// Reproducibility and provenance
// ---------------------------------------------------------------------------

func TestDiscriminationIsDeterministicAndOrderIndependent(t *testing.T) {
	base := discriminationOf(t, justAboveFloor())

	if again := discriminationOf(t, justAboveFloor()); !reflect.DeepEqual(base, again) {
		t.Errorf("two runs over the same population differ")
	}

	forward := justAboveFloor()
	reversed := make([]eval.ClassedDistance, len(forward))
	for i := range forward {
		reversed[len(forward)-1-i] = forward[i]
	}
	if got := discriminationOf(t, reversed); !reflect.DeepEqual(base, got) {
		t.Errorf("reversing the population changed the result")
	}
}

func TestDiscriminationCarriesItsProvenance(t *testing.T) {
	got := discriminationOf(t, discriminating())

	if got.ProfileID != "profile-under-test" {
		t.Errorf("ProfileID = %q", got.ProfileID)
	}
	if got.ReferenceID != "reference-under-test" {
		t.Errorf("ReferenceID = %q", got.ReferenceID)
	}
	if got.FeatureManifestDigest != features.ManifestDigest() {
		t.Errorf("FeatureManifestDigest = %q", got.FeatureManifestDigest)
	}
	if got.WeightScheme != deviation.WeightSchemeUniform {
		t.Errorf("WeightScheme = %q", got.WeightScheme)
	}
	if got.DistanceAlgorithm != deviation.DistanceAlgorithm {
		t.Errorf("DistanceAlgorithm = %q", got.DistanceAlgorithm)
	}
	if want := []features.Tier{features.TierA}; !reflect.DeepEqual(got.ScoredTiers, want) {
		t.Errorf("ScoredTiers = %v, want %v", got.ScoredTiers, want)
	}
	if got.Split != corpus.Test {
		t.Errorf("Split = %q, want %q", got.Split, corpus.Test)
	}
	if got.Algorithm != eval.DiscriminationAlgorithm {
		t.Errorf("Algorithm = %q, want %q", got.Algorithm, eval.DiscriminationAlgorithm)
	}
	if got.Clustering != eval.ClusterByDocument {
		t.Errorf("Clustering = %q, want %q", got.Clustering, eval.ClusterByDocument)
	}
	if got.AuthorSegments != 80 || got.DistractorSegments != 40 {
		t.Errorf("segments = %d and %d, want 80 and 40", got.AuthorSegments, got.DistractorSegments)
	}
}

// Anything that can change the verdict changes the ID, including the cluster
// partition — the bound comes from resampling clusters, so two partitions of one
// population are different evidence.
func TestDiscriminationIdentityCoversItsInputs(t *testing.T) {
	base := discriminationOf(t, discriminating())

	t.Run("a changed population", func(t *testing.T) {
		if moved := discriminationOf(t, justAboveFloor()); moved.ID == base.ID {
			t.Errorf("a different population produced the same ID %q", base.ID)
		}
	})

	specs := []struct {
		name   string
		mutate func(*eval.DiscriminationSpec)
	}{
		{name: "a changed floor", mutate: func(s *eval.DiscriminationSpec) { s.Floor = 0.60 }},
		{name: "a changed confidence", mutate: func(s *eval.DiscriminationSpec) { s.Confidence = 0.99 }},
		{name: "a changed resample count", mutate: func(s *eval.DiscriminationSpec) { s.Resamples = 500 }},
		{name: "a changed seed", mutate: func(s *eval.DiscriminationSpec) { s.Seed = 99 }},
	}
	for _, c := range specs {
		t.Run(c.name, func(t *testing.T) {
			spec := eval.DefaultDiscrimination()
			c.mutate(&spec)
			moved, err := eval.Discriminate(discriminating(), spec)
			if err != nil {
				t.Fatalf("Discriminate: %v", err)
			}
			if moved.ID == base.ID {
				t.Errorf("%s produced the same ID %q", c.name, base.ID)
			}
		})
	}

	// Turning on author labels changes the artifact, and the reason is worth
	// stating because it is not the one it first appears to be.
	//
	// The clustering MODE cannot be isolated from the membership. A cluster's
	// membership record includes each member's author, and the mode turns on the
	// DISTRACTOR members alone: document-and-author requires every distractor to
	// carry an author, while the author class clusters by document either way and
	// its members may be unlabelled in both. So a population in document mode has
	// at least one distractor with no author and one in document-and-author mode
	// has none — identical distractor membership already implies identical mode.
	// The mode is a function of that membership, so hashing it separately is
	// redundant rather than load-bearing, and no test can show otherwise. What is
	// asserted here is the true and useful thing: the change reaches the identity.
	t.Run("author labels turned on", func(t *testing.T) {
		labelled := discriminating()
		for i := range labelled {
			if labelled[i].Class == eval.ClassDistractor {
				labelled[i].Author = labelled[i].Document
			}
		}

		moved := discriminationOf(t, labelled)
		if moved.Clustering != eval.ClusterByDocumentAndAuthor {
			t.Fatalf("clustering = %q, want %q", moved.Clustering, eval.ClusterByDocumentAndAuthor)
		}
		if moved.DistractorClusters != base.DistractorClusters {
			t.Errorf("cluster count moved from %d to %d; labelling each document with its own author should not regroup anything",
				base.DistractorClusters, moved.DistractorClusters)
		}
		if moved.ID == base.ID {
			t.Errorf("labelling the distractors left the ID at %q", base.ID)
		}
	})

	// The same eighty author segments over forty documents, grouped round-robin
	// and as contiguous pairs: same distances, same cluster count, same verdict,
	// different evidence.
	t.Run("a changed cluster partition", func(t *testing.T) {
		roundRobin := append(
			heldOut(eval.ClassAuthor, span(1, 80), 40),
			perDocument(eval.ClassDistractor, span(201, 240))...,
		)
		contiguous := make([]eval.ClassedDistance, 0, 120)
		for i, v := range span(1, 80) {
			in := held(eval.ClassAuthor, v)
			in.Document = label("doc", i/2)
			contiguous = append(contiguous, in)
		}
		contiguous = append(contiguous, perDocument(eval.ClassDistractor, span(201, 240))...)

		first := discriminationOf(t, roundRobin)
		second := discriminationOf(t, contiguous)
		if first.ID == second.ID {
			t.Errorf("two cluster partitions of the same distances share the ID %q", first.ID)
		}
	})
}

// ---------------------------------------------------------------------------
// What the gate refuses
// ---------------------------------------------------------------------------

// Discrimination is a reported figure, so it comes from Test.
func TestDiscriminationAdmitsOnlyTheTestSplit(t *testing.T) {
	for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, ""} {
		name := string(split)
		if name == "" {
			name = "no split at all"
		}
		t.Run(name, func(t *testing.T) {
			in := discriminating()
			for i := range in {
				in[i].Distance.Split = split
			}
			if _, err := eval.Discriminate(in, eval.DefaultDiscrimination()); !errors.Is(err, eval.ErrTestSplit) {
				t.Errorf("err = %v, want %v", err, eval.ErrTestSplit)
			}
		})
	}

	// One contaminant is enough, on either side.
	for _, class := range []eval.Class{eval.ClassAuthor, eval.ClassDistractor} {
		t.Run("one "+string(class)+" segment from another split", func(t *testing.T) {
			in := discriminating()
			for i := range in {
				if in[i].Class == class {
					in[i].Distance.Split = corpus.Calibrate
					break
				}
			}
			if _, err := eval.Discriminate(in, eval.DefaultDiscrimination()); !errors.Is(err, eval.ErrTestSplit) {
				t.Errorf("err = %v, want %v", err, eval.ErrTestSplit)
			}
		})
	}
}

func TestDiscriminationRefusesBadInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*deviation.Distance)
		want   error
	}{
		{name: "another profile", mutate: func(d *deviation.Distance) { d.ProfileID = "another-profile" }, want: eval.ErrProfileMismatch},
		{name: "another reference", mutate: func(d *deviation.Distance) { d.ReferenceID = "another-reference" }, want: eval.ErrReferenceMismatch},
		{name: "another manifest", mutate: func(d *deviation.Distance) { d.FeatureManifestDigest = "another-digest" }, want: eval.ErrManifestMismatch},
		{name: "another weighting", mutate: func(d *deviation.Distance) { d.WeightScheme = "expert-v1" }, want: eval.ErrWeightingMismatch},
		{name: "another distance algorithm", mutate: func(d *deviation.Distance) { d.Algorithm = "distance-median-v2" }, want: eval.ErrAlgorithmMismatch},
		{name: "another tier subset", mutate: func(d *deviation.Distance) { d.ScoredTiers = []features.Tier{"B"} }, want: eval.ErrTierMismatch},
	}

	// Injected on both sides: a guard reached only through the first class is the
	// usual way this goes wrong.
	for _, c := range cases {
		for _, class := range []eval.Class{eval.ClassAuthor, eval.ClassDistractor} {
			t.Run(c.name+" on "+string(class), func(t *testing.T) {
				in := discriminating()
				for i := range in {
					if in[i].Class == class {
						c.mutate(&in[i].Distance)
						break
					}
				}
				if _, err := eval.Discriminate(in, eval.DefaultDiscrimination()); !errors.Is(err, c.want) {
					t.Errorf("err = %v, want %v", err, c.want)
				}
			})
		}
	}

	// AUC is defined over pairs, so either class being empty leaves nothing to
	// pair. Both directions.
	for _, only := range []eval.Class{eval.ClassAuthor, eval.ClassDistractor} {
		t.Run("only "+string(only)+" segments", func(t *testing.T) {
			in := []eval.ClassedDistance{}
			for _, d := range discriminating() {
				if d.Class == only {
					in = append(in, d)
				}
			}
			if _, err := eval.Discriminate(in, eval.DefaultDiscrimination()); !errors.Is(err, eval.ErrMissingInput) {
				t.Errorf("err = %v, want %v; AUC over no pairs is not a number", err, eval.ErrMissingInput)
			}
		})
	}

	t.Run("no distances", func(t *testing.T) {
		if _, err := eval.Discriminate(nil, eval.DefaultDiscrimination()); !errors.Is(err, eval.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, eval.ErrMissingInput)
		}
	})

	specs := []struct {
		name   string
		mutate func(*eval.DiscriminationSpec)
	}{
		{name: "a floor of zero", mutate: func(s *eval.DiscriminationSpec) { s.Floor = 0 }},
		{name: "a negative floor", mutate: func(s *eval.DiscriminationSpec) { s.Floor = -0.1 }},
		{name: "a floor above one", mutate: func(s *eval.DiscriminationSpec) { s.Floor = 1.5 }},
		{name: "a NaN floor", mutate: func(s *eval.DiscriminationSpec) { s.Floor = math.NaN() }},
		{name: "a zero confidence", mutate: func(s *eval.DiscriminationSpec) { s.Confidence = 0 }},
		{name: "a confidence of one", mutate: func(s *eval.DiscriminationSpec) { s.Confidence = 1 }},
		{name: "a NaN confidence", mutate: func(s *eval.DiscriminationSpec) { s.Confidence = math.NaN() }},
		{name: "zero resamples", mutate: func(s *eval.DiscriminationSpec) { s.Resamples = 0 }},
		{name: "negative resamples", mutate: func(s *eval.DiscriminationSpec) { s.Resamples = -1 }},
	}
	for _, c := range specs {
		t.Run(c.name, func(t *testing.T) {
			spec := eval.DefaultDiscrimination()
			c.mutate(&spec)
			if _, err := eval.Discriminate(discriminating(), spec); !errors.Is(err, eval.ErrInvalidDiscrimination) {
				t.Errorf("err = %v, want %v", err, eval.ErrInvalidDiscrimination)
			}
		})
	}

	// A floor of exactly one is valid: it demands perfect separation, which the
	// cap then makes unreachable — stringent, not nonsensical.
	t.Run("a floor of one", func(t *testing.T) {
		spec := eval.DefaultDiscrimination()
		spec.Floor = 1
		got, err := eval.Discriminate(discriminating(), spec)
		if err != nil {
			t.Fatalf("a floor of 1 was refused: %v", err)
		}
		if got.Discriminates {
			t.Errorf("a floor of 1 was cleared by a bound of %v", got.LowerBound)
		}
	})
}

// An unscoreable segment carries no distance, so it is in no pair and counts
// toward neither class — on either side.
func TestUnscoreableSegmentsAreExcludedFromAUC(t *testing.T) {
	base := discriminationOf(t, justAboveFloor())

	for _, class := range []eval.Class{eval.ClassAuthor, eval.ClassDistractor} {
		t.Run(string(class), func(t *testing.T) {
			padded := justAboveFloor()
			for i := 0; i < 12; i++ {
				none := held(class, 0)
				none.Distance.Value = 0
				none.Distance.Defined = false
				none.Distance.Reason = deviation.ReasonInsufficientEvidence
				none.Distance.Features = nil
				none.Distance.ScoredTiers = nil
				none.Document = ""
				padded = append(padded, none)
			}

			got := discriminationOf(t, padded)
			if got.AUC != base.AUC || got.LowerBound != base.LowerBound {
				t.Errorf("unscoreable %s segments moved AUC from %v to %v and the bound from %v to %v",
					class, base.AUC, got.AUC, base.LowerBound, got.LowerBound)
			}
			if got.AuthorSegments != base.AuthorSegments || got.DistractorSegments != base.DistractorSegments {
				t.Errorf("unscoreable %s segments changed the counts", class)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The release verdict: the two gates composed
// ---------------------------------------------------------------------------

func releaseOf(t *testing.T, in []eval.ClassedDistance) eval.Release {
	t.Helper()
	th := calibrated(t)
	calibration, err := th.CalibrateBands(in, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	discrimination, err := eval.Discriminate(in, eval.DefaultDiscrimination())
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	got, err := eval.NewRelease(discrimination, calibration)
	if err != nil {
		t.Fatalf("NewRelease: %v", err)
	}
	return got
}

// Discrimination is prior. Below its floor no band is emitted whatever the
// band-level evidence says, because a band is a label on a distance that has
// been shown not to carry information.
//
// The boundBelowFloor population sits every distractor below t_low, so its
// in-range band is refused outright while not-you holds — the calibration is
// still usable, by its own lights. Discrimination fails, and the release must
// refuse regardless of what the surviving band would have said.
func TestDiscriminationIsPriorToTheBands(t *testing.T) {
	got := releaseOf(t, boundBelowFloor())

	if got.Discrimination.Discriminates {
		t.Fatalf("this fixture must fail discrimination")
	}
	// The band gate's own verdict must survive, or this test would pass against
	// a calibration that emitted nothing for unrelated reasons.
	if !got.Calibration.Calibrated {
		t.Fatalf("this fixture must keep a band by the band gate's own lights")
	}
	if got.Shippable {
		t.Errorf("shippable below the discrimination floor")
	}
	if got.Reason == "" {
		t.Errorf("an unshippable release states no reason")
	}

	for _, value := range []float64{10, 150, 300} {
		out, err := got.Band(held(eval.ClassAuthor, value).Distance)
		if err != nil {
			t.Fatalf("Band(%v): %v", value, err)
		}
		if out.Defined {
			t.Errorf("distance %v banded as %q below the discrimination floor", value, out.Band)
		}
		if out.Reason != eval.ReasonUncalibrated {
			t.Errorf("reason = %q, want %q", out.Reason, eval.ReasonUncalibrated)
		}
	}
}

// The reverse does not hold: a band can fail while discrimination passes, and
// the profile stays usable for the bands that hold.
func TestBandsCanFailWhileDiscriminationPasses(t *testing.T) {
	got := releaseOf(t, leakyInRange())

	if !got.Discrimination.Discriminates {
		t.Fatalf("this fixture must pass discrimination; the bound is %v", got.Discrimination.LowerBound)
	}
	if !got.Calibration.Calibrated {
		t.Fatalf("this fixture must keep one claiming band")
	}
	if !got.Shippable {
		t.Errorf("not shippable although discrimination passes and a band holds: %v", got.Reason)
	}

	// The refused band still collapses to drifting, exactly as the calibration
	// alone would have it.
	out, err := got.Band(held(eval.ClassAuthor, 10).Distance)
	if err != nil {
		t.Fatalf("Band: %v", err)
	}
	if out.Band != eval.BandDrifting {
		t.Errorf("band = %q for a distance in a refused in-range, want %q", out.Band, eval.BandDrifting)
	}
}

// Both gates passing is the only shippable state.
func TestAReleaseIsShippableOnlyWhenBothGatesPass(t *testing.T) {
	got := releaseOf(t, clean())

	if !got.Discrimination.Discriminates {
		t.Fatalf("this fixture must pass discrimination; the bound is %v", got.Discrimination.LowerBound)
	}
	if !got.Calibration.Calibrated {
		t.Fatalf("this fixture must be calibrated")
	}
	if !got.Shippable {
		t.Errorf("not shippable with both gates passing: %v", got.Reason)
	}
	if got.Reason != "" {
		t.Errorf("a shippable release carries the reason %q", got.Reason)
	}

	out, err := got.Band(held(eval.ClassAuthor, 10).Distance)
	if err != nil {
		t.Fatalf("Band: %v", err)
	}
	if out.Band != eval.BandInRange {
		t.Errorf("band = %q, want %q", out.Band, eval.BandInRange)
	}
}

// A release assembled from two artifacts that do not describe the same thing is
// not a verdict about anything.
func TestNewReleaseRefusesMismatchedArtifacts(t *testing.T) {
	th := calibrated(t)
	calibration, err := th.CalibrateBands(clean(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	discrimination, err := eval.Discriminate(clean(), eval.DefaultDiscrimination())
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*eval.Discrimination)
		want   error
	}{
		{name: "another profile", mutate: func(d *eval.Discrimination) { d.ProfileID = "another-profile" }, want: eval.ErrProfileMismatch},
		{name: "another reference", mutate: func(d *eval.Discrimination) { d.ReferenceID = "another-reference" }, want: eval.ErrReferenceMismatch},
		{name: "another manifest", mutate: func(d *eval.Discrimination) { d.FeatureManifestDigest = "another-digest" }, want: eval.ErrManifestMismatch},
		{name: "another weighting", mutate: func(d *eval.Discrimination) { d.WeightScheme = "expert-v1" }, want: eval.ErrWeightingMismatch},
		{name: "another distance algorithm", mutate: func(d *eval.Discrimination) { d.DistanceAlgorithm = "distance-median-v2" }, want: eval.ErrAlgorithmMismatch},
		{name: "another tier subset", mutate: func(d *eval.Discrimination) { d.ScoredTiers = []features.Tier{"B"} }, want: eval.ErrTierMismatch},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			foreign := discrimination
			c.mutate(&foreign)
			if _, err := eval.NewRelease(foreign, calibration); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// Matching profile metadata is not the same as being about the same evidence.
// Two artifacts can agree on the profile, the reference, the manifest, the
// weighting, the algorithm and the tier subset and still have been computed from
// different held-out populations — or from the same segments under different
// cluster partitions, which is different evidence for the same reason it is
// different evidence everywhere else in this design.
//
// So both artifacts record the population they were computed from, and a release
// refuses to compose two that disagree.
func TestNewReleaseRefusesArtifactsFromDifferentPopulations(t *testing.T) {
	th := calibrated(t)

	calibration, err := th.CalibrateBands(clean(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	matching, err := eval.Discriminate(clean(), eval.DefaultDiscrimination())
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	if matching.PopulationID != calibration.PopulationID {
		t.Fatalf("two artifacts over the same population disagree on it: %q and %q",
			matching.PopulationID, calibration.PopulationID)
	}
	if _, err := eval.NewRelease(matching, calibration); err != nil {
		t.Fatalf("NewRelease over one population: %v", err)
	}

	t.Run("a different held-out population", func(t *testing.T) {
		other, err := eval.Discriminate(justAboveFloor(), eval.DefaultDiscrimination())
		if err != nil {
			t.Fatalf("Discriminate: %v", err)
		}
		if _, err := eval.NewRelease(other, calibration); !errors.Is(err, eval.ErrPopulationMismatch) {
			t.Errorf("err = %v, want %v", err, eval.ErrPopulationMismatch)
		}
	})

	t.Run("the same segments under a different partition", func(t *testing.T) {
		blocked := make([]eval.ClassedDistance, 0, 120)
		for i, v := range span(1, 80) {
			in := held(eval.ClassAuthor, v)
			in.Document = label("doc", i/2)
			blocked = append(blocked, in)
		}
		blocked = append(blocked, perDocument(eval.ClassDistractor, span(201, 240))...)

		repartitioned, err := eval.Discriminate(blocked, eval.DefaultDiscrimination())
		if err != nil {
			t.Fatalf("Discriminate: %v", err)
		}
		if repartitioned.PopulationID == calibration.PopulationID {
			t.Fatalf("two partitions of the same segments share a population ID")
		}
		if _, err := eval.NewRelease(repartitioned, calibration); !errors.Is(err, eval.ErrPopulationMismatch) {
			t.Errorf("err = %v, want %v", err, eval.ErrPopulationMismatch)
		}
	})
}

// A release is an artifact too, and the lesson from the calibration slice
// applies unchanged: one that classified through state which did not survive
// storage would be silently wrong when read back.
func TestAReleaseSurvivesBeingPersisted(t *testing.T) {
	base := releaseOf(t, clean())

	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var restored eval.Release
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if restored.ID != base.ID {
		t.Errorf("ID = %q, want %q", restored.ID, base.ID)
	}
	if restored.Shippable != base.Shippable {
		t.Errorf("Shippable = %v, want %v", restored.Shippable, base.Shippable)
	}

	for _, value := range []float64{10, 96, 150, 205, 300} {
		distance := held(eval.ClassAuthor, value).Distance
		want, err := base.Band(distance)
		if err != nil {
			t.Fatalf("Band(%v) on the original: %v", value, err)
		}
		got, err := restored.Band(distance)
		if err != nil {
			t.Fatalf("Band(%v) on the restored release: %v", value, err)
		}
		if got != want {
			t.Errorf("distance %v bands as %+v after a round trip, want %+v", value, got, want)
		}
	}
}

// The release names both artifacts it composed, so a report can be traced to the
// two gates that produced it.
func TestAReleaseNamesBothGates(t *testing.T) {
	got := releaseOf(t, clean())

	if got.Discrimination.ID == "" || got.Calibration.ID == "" {
		t.Errorf("the release does not name both artifacts: %q and %q", got.Discrimination.ID, got.Calibration.ID)
	}
	if got.ID == got.Discrimination.ID || got.ID == got.Calibration.ID {
		t.Errorf("the release ID is one of its parts")
	}
}

// A release ID that did not move with its parts would let a cache serve one
// verdict for another. Both inputs are varied in turn, each time leaving the
// population — and therefore the composition itself — valid.
func TestAReleaseIdentityBindsBothGates(t *testing.T) {
	th := calibrated(t)

	calibration, err := th.CalibrateBands(clean(), eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	discrimination, err := eval.Discriminate(clean(), eval.DefaultDiscrimination())
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	base, err := eval.NewRelease(discrimination, calibration)
	if err != nil {
		t.Fatalf("NewRelease: %v", err)
	}

	t.Run("a changed discrimination", func(t *testing.T) {
		spec := eval.DefaultDiscrimination()
		spec.Seed = spec.Seed + 7
		moved, err := eval.Discriminate(clean(), spec)
		if err != nil {
			t.Fatalf("Discriminate: %v", err)
		}
		if moved.ID == discrimination.ID {
			t.Fatalf("this case needs the discrimination artifact to change")
		}
		got, err := eval.NewRelease(moved, calibration)
		if err != nil {
			t.Fatalf("NewRelease: %v", err)
		}
		if got.ID == base.ID {
			t.Errorf("a changed discrimination left the release ID at %q", base.ID)
		}
	})

	t.Run("a changed calibration", func(t *testing.T) {
		floor := eval.DefaultBandFloor()
		floor.Seed = floor.Seed + 7
		moved, err := th.CalibrateBands(clean(), floor)
		if err != nil {
			t.Fatalf("CalibrateBands: %v", err)
		}
		if moved.ID == calibration.ID {
			t.Fatalf("this case needs the calibration artifact to change")
		}
		got, err := eval.NewRelease(discrimination, moved)
		if err != nil {
			t.Fatalf("NewRelease: %v", err)
		}
		if got.ID == base.ID {
			t.Errorf("a changed calibration left the release ID at %q", base.ID)
		}
	})
}

// ---------------------------------------------------------------------------
// Clusters, not segments
// ---------------------------------------------------------------------------

// Every other fixture here puts one segment in each document, which makes
// cluster resampling and segment resampling the same thing. This one does not.
//
// The same eighty author segments are grouped into twenty clusters two ways —
// round-robin, and as contiguous blocks of four. AUC is 0.859375 either way,
// the cluster count is twenty either way, and the cap is 1 - 3/20 = 0.85 either
// way. The bounds are not the same at all:
//
//	round-robin  0.82171875, which clears the floor
//	contiguous   0.766875,   which does not
//
// So the partition alone flips the release verdict. An implementation resampling
// segments gives one answer for both, and an implementation ignoring the
// clustering entirely gives the eighty-singleton answer of 0.80296875 for both.
func TestAUCResamplingFollowsTheClusters(t *testing.T) {
	distractors := perDocument(eval.ClassDistractor, span(51, 90))

	roundRobin := append(heldOut(eval.ClassAuthor, span(1, 80), 20), distractors...)
	contiguous := make([]eval.ClassedDistance, 0, 120)
	for i, v := range span(1, 80) {
		in := held(eval.ClassAuthor, v)
		in.Document = label("doc", i/4)
		contiguous = append(contiguous, in)
	}
	contiguous = append(contiguous, distractors...)

	spread := discriminationOf(t, roundRobin)
	blocked := discriminationOf(t, contiguous)

	if spread.AUC != blocked.AUC {
		t.Fatalf("the two partitions give different AUCs, %v and %v; this test needs them identical", spread.AUC, blocked.AUC)
	}
	if spread.AuthorClusters != 20 || blocked.AuthorClusters != 20 {
		t.Fatalf("author clusters = %d and %d, want 20 in both", spread.AuthorClusters, blocked.AuthorClusters)
	}
	if spread.Cap != 0.85 || blocked.Cap != 0.85 {
		t.Fatalf("caps = %v and %v, want 1 - 3/20 = 0.85 in both", spread.Cap, blocked.Cap)
	}

	if spread.LowerBound != 0.82171875 {
		t.Errorf("round-robin bound = %v, want 0.82171875", spread.LowerBound)
	}
	if blocked.LowerBound != 0.766875 {
		t.Errorf("contiguous bound = %v, want 0.766875", blocked.LowerBound)
	}
	if spread.LowerBound == blocked.LowerBound {
		t.Fatalf("both partitions gave %v; the resampling did not follow the clusters", spread.LowerBound)
	}
	if !spread.Discriminates || blocked.Discriminates {
		t.Errorf("verdicts = %v and %v; the partition alone should decide this pair", spread.Discriminates, blocked.Discriminates)
	}
}

// ---------------------------------------------------------------------------
// The declared parameters reach the computation
// ---------------------------------------------------------------------------

// AUC over three thousand pairs is fine-grained enough that the seed and the
// resample count move the bound measurably — unlike the band gate's error rate,
// where they do not and the identity is the only witness. So all three are
// asserted behaviourally here, with exact values.
func TestTheDeclaredParametersReachTheBound(t *testing.T) {
	in := justAboveFloor()

	cases := []struct {
		name   string
		mutate func(*eval.DiscriminationSpec)
		bound  float64
	}{
		{name: "the declared spec", mutate: func(*eval.DiscriminationSpec) {}, bound: 0.80296875},
		{name: "a wider confidence", mutate: func(s *eval.DiscriminationSpec) { s.Confidence = 0.99 }, bound: 0.7790625},
		{name: "a narrower confidence", mutate: func(s *eval.DiscriminationSpec) { s.Confidence = 0.50 }, bound: 0.8609375},
		{name: "another seed", mutate: func(s *eval.DiscriminationSpec) { s.Seed = eval.DefaultDiscrimination().Seed + 7 }, bound: 0.8028125},
		{name: "fewer resamples", mutate: func(s *eval.DiscriminationSpec) { s.Resamples = 500 }, bound: 0.80875},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := eval.DefaultDiscrimination()
			c.mutate(&spec)
			got, err := eval.Discriminate(in, spec)
			if err != nil {
				t.Fatalf("Discriminate: %v", err)
			}
			if got.LowerBound != c.bound {
				t.Errorf("bound = %v, want %v", got.LowerBound, c.bound)
			}
			if got.Spec != spec {
				t.Errorf("the returned spec is %+v, want %+v", got.Spec, spec)
			}
		})
	}
}

// A wider confidence cannot give a tighter bound. The exact values above pin the
// rank; this pins the direction, which is what a mirrored percentile index would
// get wrong while still producing three distinct numbers.
func TestAWiderConfidenceGivesALowerBound(t *testing.T) {
	in := justAboveFloor()

	bounds := make([]float64, 0, 3)
	for _, confidence := range []float64{0.50, 0.95, 0.99} {
		spec := eval.DefaultDiscrimination()
		spec.Confidence = confidence
		got, err := eval.Discriminate(in, spec)
		if err != nil {
			t.Fatalf("Discriminate at %v: %v", confidence, err)
		}
		bounds = append(bounds, got.LowerBound)
	}

	if !(bounds[0] > bounds[1] && bounds[1] > bounds[2]) {
		t.Errorf("bounds at 0.50, 0.95 and 0.99 are %v; a wider confidence must give a lower bound", bounds)
	}
}
