package deviation

import (
	"fmt"
	"math"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
)

// WeightSchemeUniform identifies the v1 uniform feature weighting scheme.
const WeightSchemeUniform = "uniform-v1"

// DistanceAlgorithm identifies the v1 uniformly weighted mean distance.
const DistanceAlgorithm = "distance-uniform-mean-v1"

// TierAvailability records one manifest tier's availability and scoring minimum.
type TierAvailability struct {
	Tier                          features.Tier
	Manifest, Available, Required int
	Met                           bool
}

// Distance is the uniformly weighted mean absolute transformed deviation.
type Distance struct {
	ProfileID, ReferenceID, FeatureManifestDigest string
	Split                                         corpus.Split
	Value                                         float64
	Defined                                       bool
	Reason                                        Reason
	Features                                      []features.ID
	Tiers                                         []TierAvailability
	ScoredTiers                                   []features.Tier
	Partial                                       bool
	WeightScheme, Algorithm                       string
}

// TierPlan derives availability and majority requirements for every manifest tier.
// definitions must contain each manifest feature exactly once.
func TierPlan(definitions []features.Definition, available map[features.ID]bool) []TierAvailability {
	var rows []TierAvailability
	positions := make(map[features.Tier]int)
	for _, definition := range definitions {
		position, ok := positions[definition.CandidateTier]
		if !ok {
			position = len(rows)
			positions[definition.CandidateTier] = position
			rows = append(rows, TierAvailability{Tier: definition.CandidateTier})
		}
		rows[position].Manifest++
		if available[definition.ID] {
			rows[position].Available++
		}
	}
	for i := range rows {
		rows[i].Required = rows[i].Manifest/2 + 1
		rows[i].Met = rows[i].Available >= rows[i].Required
	}
	return rows
}

// Distance computes d against the current feature manifest.
func (d Deviations) Distance() (Distance, error) {
	if d.FeatureManifestDigest == "" {
		return Distance{}, fmt.Errorf("distance: %w: manifest digest", ErrMissingInput)
	}
	if d.FeatureManifestDigest != features.ManifestDigest() {
		return Distance{}, fmt.Errorf("distance: %w", ErrManifestMismatch)
	}
	return d.DistanceOver(features.Definitions())
}

// DistanceOver computes d against the supplied feature manifest.
func (d Deviations) DistanceOver(definitions []features.Definition) (Distance, error) {
	if len(definitions) == 0 {
		return Distance{}, fmt.Errorf("distance: %w: definitions", ErrMissingInput)
	}
	if err := validateDistanceInput(d, definitions); err != nil {
		return Distance{}, err
	}

	available := make(map[features.ID]bool, len(d.Values))
	values := make(map[features.ID]Deviation, len(d.Values))
	for _, value := range d.Values {
		values[value.Feature] = value
		available[value.Feature] = value.Defined
	}

	out := Distance{
		ProfileID:             d.ProfileID,
		ReferenceID:           d.ReferenceID,
		FeatureManifestDigest: d.FeatureManifestDigest,
		Split:                 d.Split,
		Tiers:                 TierPlan(definitions, available),
		WeightScheme:          WeightSchemeUniform,
		Algorithm:             DistanceAlgorithm,
	}
	for _, tier := range out.Tiers {
		if tier.Met {
			out.ScoredTiers = append(out.ScoredTiers, tier.Tier)
		}
	}
	if len(out.ScoredTiers) == 0 {
		out.Reason = ReasonInsufficientEvidence
		return out, nil
	}

	scored := make(map[features.Tier]bool, len(out.ScoredTiers))
	for _, tier := range out.ScoredTiers {
		scored[tier] = true
	}
	for _, definition := range definitions {
		value := values[definition.ID]
		if !scored[definition.CandidateTier] || !value.Defined {
			continue
		}
		out.Value += math.Abs(value.Value)
		out.Features = append(out.Features, definition.ID)
	}
	// A met tier has Available >= Required >= 1, so at least one feature was scored.
	out.Value /= float64(len(out.Features))
	out.Defined = true
	out.Partial = len(out.ScoredTiers) < len(out.Tiers)
	return out, nil
}

func validateDistanceInput(d Deviations, definitions []features.Definition) error {
	if d.ProfileID == "" {
		return fmt.Errorf("distance: %w: profile ID", ErrMissingInput)
	}
	if d.ReferenceID == "" {
		return fmt.Errorf("distance: %w: reference ID", ErrMissingInput)
	}
	if d.FeatureManifestDigest == "" {
		return fmt.Errorf("distance: %w: manifest digest", ErrMissingInput)
	}
	if !knownSplit(d.Split) {
		return fmt.Errorf("distance: %w: %q", ErrUnknownSplit, d.Split)
	}

	if _, ok := manifestMap(d.Values, func(value Deviation) features.ID { return value.Feature }, definitions); !ok {
		return fmt.Errorf("distance: %w", ErrManifestMismatch)
	}
	for _, value := range d.Values {
		if value.Defined {
			if value.Reason != "" || !finite(value.Value) {
				return fmt.Errorf("distance %s: %w", value.Feature, ErrMalformedInput)
			}
		} else if value.Reason == "" {
			return fmt.Errorf("distance %s: %w", value.Feature, ErrMalformedInput)
		}
	}
	return nil
}
