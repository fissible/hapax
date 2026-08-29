package eval

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
)

// DiscriminationAlgorithm identifies the declared clustered AUC gate.
const DiscriminationAlgorithm = "clustered-auc-lower-bound-v1"

// DiscriminationSpec declares a reproducible one-sided AUC bound.
type DiscriminationSpec struct {
	Floor      float64
	Confidence float64
	Resamples  int
	Seed       uint64
}

// Discrimination records the held-out evidence that distances distinguish the
// author from the distractor population.
type Discrimination struct {
	ID, PopulationID                                                               string
	ProfileID, ReferenceID, FeatureManifestDigest, WeightScheme, DistanceAlgorithm string
	ScoredTiers                                                                    []features.Tier
	Split                                                                          corpus.Split
	Spec                                                                           DiscriminationSpec
	Algorithm                                                                      string
	Clustering                                                                     Clustering
	AUC, LowerBound, Cap                                                           float64
	AuthorSegments, DistractorSegments                                             int
	AuthorClusters, DistractorClusters, MinClusters                                int
	Discriminates                                                                  bool
	Reason                                                                         string
}

// Release composes the discrimination and band-calibration gates.
type Release struct {
	ID             string
	Discrimination Discrimination
	Calibration    Calibration
	Shippable      bool
	Reason         string
}

var ErrInvalidDiscrimination = errors.New("eval discrimination invalid specification")

// DefaultDiscrimination returns the v1 declared AUC gate parameters.
func DefaultDiscrimination() DiscriminationSpec {
	return DiscriminationSpec{Floor: 0.80, Confidence: 0.95, Resamples: 2000, Seed: 0x68617061785F7631}
}

// Discriminate evaluates the test population using a clustered AUC lower bound.
func Discriminate(distances []ClassedDistance, spec DiscriminationSpec) (Discrimination, error) {
	if len(distances) == 0 {
		return Discrimination{}, fmt.Errorf("discriminate: %w", ErrMissingInput)
	}
	if !validDiscrimination(spec) {
		return Discrimination{}, fmt.Errorf("discriminate: %w", ErrInvalidDiscrimination)
	}

	defined := make([]ClassedDistance, 0, len(distances))
	var binding deviation.Distance
	haveBinding := false
	for _, item := range distances {
		if item.Class != ClassAuthor && item.Class != ClassDistractor {
			return Discrimination{}, fmt.Errorf("%w: %q", ErrUnknownClass, item.Class)
		}
		if item.Distance.Split != corpus.Test {
			return Discrimination{}, fmt.Errorf("%w: %q", ErrTestSplit, item.Distance.Split)
		}
		if !item.Distance.Defined {
			continue
		}
		if err := validDistance(item.Distance); err != nil {
			return Discrimination{}, err
		}
		if !haveBinding {
			binding, haveBinding = item.Distance, true
		} else if err := sameBinding(binding, item.Distance); err != nil {
			return Discrimination{}, err
		}
		defined = append(defined, item)
	}

	author, distractor, clustering, err := bootstrapClusters(defined)
	if err != nil {
		return Discrimination{}, err
	}
	if len(author) == 0 || len(distractor) == 0 {
		return Discrimination{}, fmt.Errorf("discriminate: %w", ErrMissingInput)
	}

	observedAUC := auc(clusterItems(author), clusterItems(distractor))
	resampled := make([]float64, spec.Resamples)
	authorRandom := splitMix64{state: spec.Seed + 1}
	distractorRandom := splitMix64{state: spec.Seed + 2}
	for i := range resampled {
		resampled[i] = auc(
			appendDrawnClusters(nil, author, &authorRandom),
			appendDrawnClusters(nil, distractor, &distractorRandom),
		)
	}
	sort.Float64s(resampled)
	index := int(math.Floor((1 - spec.Confidence) * float64(len(resampled))))
	if index < 0 {
		index = 0
	}
	bootstrap := resampled[index]
	clusters := min(len(author), len(distractor))
	cap := 1 - 3/float64(clusters)

	minClusters := 0
	if spec.Floor < 1 {
		// The declared decimal floor 0.80 is mathematically 4/5. Account for
		// its binary representation landing infinitesimally above 15.
		minClusters = int(math.Ceil(3/(1-spec.Floor) - 1e-12))
	}
	out := Discrimination{
		ProfileID: binding.ProfileID, ReferenceID: binding.ReferenceID,
		FeatureManifestDigest: binding.FeatureManifestDigest, WeightScheme: binding.WeightScheme,
		DistanceAlgorithm: binding.Algorithm, ScoredTiers: append([]features.Tier(nil), binding.ScoredTiers...),
		Split: corpus.Test, Spec: spec, Algorithm: DiscriminationAlgorithm, Clustering: clustering,
		AUC: observedAUC, LowerBound: math.Min(bootstrap, cap), Cap: cap,
		AuthorSegments: clusterSegments(author), DistractorSegments: clusterSegments(distractor),
		AuthorClusters: len(author), DistractorClusters: len(distractor), MinClusters: minClusters,
	}
	out.Discriminates = out.LowerBound >= spec.Floor
	if !out.Discriminates {
		out.Reason = "lower-bound-below-floor"
	}
	out.PopulationID = clusteredPopulationID(author, distractor, clustering)
	out.ID = discriminationID(&out, author, distractor)
	return out, nil
}

func validDiscrimination(spec DiscriminationSpec) bool {
	return spec.Floor > 0 && spec.Floor <= 1 && finite(spec.Floor) &&
		spec.Confidence > 0 && spec.Confidence < 1 && finite(spec.Confidence) && spec.Resamples > 0
}

func clusterItems(clusters []bootstrapCluster) []ClassedDistance {
	var out []ClassedDistance
	for _, cluster := range clusters {
		out = append(out, cluster.items...)
	}
	return out
}

func auc(author, distractor []ClassedDistance) float64 {
	wins := 0.0
	for _, left := range author {
		for _, right := range distractor {
			switch {
			case left.Distance.Value < right.Distance.Value:
				wins++
			case left.Distance.Value == right.Distance.Value:
				wins += 0.5
			}
		}
	}
	return wins / float64(len(author)*len(distractor))
}

func discriminationID(discrimination *Discrimination, author, distractor []bootstrapCluster) string {
	return identity.HashInputs(map[string]string{
		"algorithm": DiscriminationAlgorithm, "author-clusters": strconv.Itoa(discrimination.AuthorClusters),
		"author-membership": clusterMembershipID(author), "clustering": string(discrimination.Clustering),
		"confidence": numberID(discrimination.Spec.Confidence), "distractor-clusters": strconv.Itoa(discrimination.DistractorClusters),
		"distractor-membership": clusterMembershipID(distractor), "floor": numberID(discrimination.Spec.Floor),
		"resamples": strconv.Itoa(discrimination.Spec.Resamples), "seed": strconv.FormatUint(discrimination.Spec.Seed, 10),
	})
}

// NewRelease composes compatible gates for the same held-out population.
func NewRelease(discrimination Discrimination, calibration Calibration) (Release, error) {
	want := deviation.Distance{ProfileID: calibration.ProfileID, ReferenceID: calibration.ReferenceID,
		FeatureManifestDigest: calibration.FeatureManifestDigest, WeightScheme: calibration.WeightScheme,
		Algorithm: calibration.DistanceAlgorithm, ScoredTiers: calibration.ScoredTiers}
	got := deviation.Distance{ProfileID: discrimination.ProfileID, ReferenceID: discrimination.ReferenceID,
		FeatureManifestDigest: discrimination.FeatureManifestDigest, WeightScheme: discrimination.WeightScheme,
		Algorithm: discrimination.DistanceAlgorithm, ScoredTiers: discrimination.ScoredTiers}
	if err := sameBinding(want, got); err != nil {
		return Release{}, err
	}
	if discrimination.PopulationID != calibration.PopulationID {
		return Release{}, fmt.Errorf("release: %w", ErrPopulationMismatch)
	}
	out := Release{Discrimination: discrimination, Calibration: calibration}
	out.Shippable = discrimination.Discriminates && calibration.Calibrated
	if !out.Shippable {
		if !discrimination.Discriminates {
			out.Reason = "discrimination-failed"
		} else {
			out.Reason = string(ReasonUncalibrated)
		}
	}
	out.ID = identity.HashInputs(map[string]string{"calibration-id": calibration.ID, "discrimination-id": discrimination.ID})
	return out, nil
}

// Band delegates distance validation and band assignment to calibration, then
// suppresses claims when the discrimination gate did not pass.
func (r Release) Band(distance deviation.Distance) (BandOutcome, error) {
	out, err := r.Calibration.Band(distance)
	if err != nil {
		return BandOutcome{}, err
	}
	if out.Defined && !r.Discrimination.Discriminates {
		return BandOutcome{Distance: distance.Value, Reason: ReasonUncalibrated}, nil
	}
	return out, nil
}
