package eval

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
)

// ThresholdAlgorithm identifies the ordered observed-quantile band contract.
const ThresholdAlgorithm = "band-ordered-quantile-v1"

// Band identifies the result of assigning a defined distance to thresholds.
type Band string

const (
	// BandInRange identifies distances at or below the lower threshold.
	BandInRange Band = "in-range"
	// BandDrifting identifies distances strictly between the thresholds.
	BandDrifting Band = "drifting"
	// BandNotYou identifies distances at or above the upper threshold.
	BandNotYou Band = "not-you"
)

// Targets declares the tolerated author and distractor error rates.
type Targets struct {
	Author, Distractor float64
}

// Source names the held-out calibration cohort and distractor pool.
type Source struct {
	Cohort, DistractorPool string
}

// ClassedDistance pairs one distance with its calibration population.
type ClassedDistance struct {
	Class    Class
	Document string
	Author   string
	Distance deviation.Distance
}

// Thresholds records observed boundaries and every calibration binding.
type Thresholds struct {
	ID                                                          string
	Low, High, AchievedAuthor, AchievedDistractor               float64
	Separated                                                   bool
	ProfileID, ReferenceID, FeatureManifestDigest, WeightScheme string
	DistanceAlgorithm                                           string
	ScoredTiers                                                 []features.Tier
	Split                                                       corpus.Split
	Targets                                                     Targets
	AuthorDistances, DistractorDistances                        int
	Source                                                      Source
	Algorithm                                                   string
}

// BandOutcome is the result of assigning a distance to a calibrated band.
type BandOutcome struct {
	Band     Band
	Distance float64
	Defined  bool
	Reason   deviation.Reason
}

var (
	// ErrInvalidTargets reports targets outside the open unit interval.
	ErrInvalidTargets = errors.New("eval thresholds invalid targets")
	// ErrTooFewAuthorDistances reports an author population below its derived floor.
	ErrTooFewAuthorDistances = errors.New("eval thresholds too few author distances")
	// ErrTooFewDistractorDistances reports a distractor population below its derived floor.
	ErrTooFewDistractorDistances = errors.New("eval thresholds too few distractor distances")
	// ErrNoQualifyingThreshold reports a population without an observed valid boundary.
	ErrNoQualifyingThreshold = errors.New("eval thresholds no qualifying threshold")
	// ErrUnknownClass reports a distance assigned to an unsupported population.
	ErrUnknownClass = errors.New("eval thresholds unknown class")
	// ErrCalibrationSplit reports a distance not drawn from the Calibrate split.
	ErrCalibrationSplit = errors.New("eval thresholds calibration split mismatch")
	// ErrReferenceMismatch reports distances ranked against different references.
	ErrReferenceMismatch = errors.New("eval thresholds reference mismatch")
	// ErrManifestMismatch reports distances using different feature manifests.
	ErrManifestMismatch = errors.New("eval thresholds feature manifest mismatch")
	// ErrWeightingMismatch reports distances using different weighting schemes.
	ErrWeightingMismatch = errors.New("eval thresholds weighting mismatch")
	// ErrAlgorithmMismatch reports distances using different distance algorithms.
	ErrAlgorithmMismatch = errors.New("eval thresholds distance algorithm mismatch")
	// ErrTierMismatch reports distances scored over different tier subsets.
	ErrTierMismatch = errors.New("eval thresholds tier subset mismatch")
	// ErrMalformedInput reports an invalid persisted distance value.
	ErrMalformedInput = errors.New("eval thresholds malformed input")
)

// DefaultTargets returns the v1 tolerated author and distractor error rates.
func DefaultTargets() Targets { return Targets{Author: 0.05, Distractor: 0.10} }

// Calibrate derives ordered bands from defined Calibrate distances only.
func Calibrate(distances []ClassedDistance, source Source, targets Targets) (*Thresholds, error) {
	if len(distances) == 0 {
		return nil, fmt.Errorf("calibrate: %w: distances", ErrMissingInput)
	}
	if source.Cohort == "" {
		return nil, fmt.Errorf("calibrate: %w: calibration cohort", ErrMissingInput)
	}
	if source.DistractorPool == "" {
		return nil, fmt.Errorf("calibrate: %w: distractor pool", ErrMissingInput)
	}
	if !validTarget(targets.Author) {
		return nil, fmt.Errorf("calibrate: %w: author target", ErrInvalidTargets)
	}
	if !validTarget(targets.Distractor) {
		return nil, fmt.Errorf("calibrate: %w: distractor target", ErrInvalidTargets)
	}

	var author, distractor []float64
	var binding deviation.Distance
	haveBinding := false
	for _, item := range distances {
		if item.Class != ClassAuthor && item.Class != ClassDistractor {
			return nil, fmt.Errorf("%w: %q", ErrUnknownClass, item.Class)
		}
		if err := validCalibrationDistance(item.Distance); err != nil {
			return nil, err
		}
		if item.Distance.Defined && !haveBinding {
			binding, haveBinding = item.Distance, true
		}
		if item.Distance.Defined {
			if err := validDistance(item.Distance); err != nil {
				return nil, err
			}
		}
	}
	for _, item := range distances {
		if haveBinding {
			if err := sameCalibrationBinding(binding, item.Distance); err != nil {
				return nil, err
			}
		}
		if !item.Distance.Defined {
			continue
		}
		if item.Class == ClassAuthor {
			author = append(author, item.Distance.Value)
		} else {
			distractor = append(distractor, item.Distance.Value)
		}
	}

	if len(author) < minimum(targets.Author) {
		return nil, fmt.Errorf("%w: got %d, need %d", ErrTooFewAuthorDistances, len(author), minimum(targets.Author))
	}
	if len(distractor) < minimum(targets.Distractor) {
		return nil, fmt.Errorf("%w: got %d, need %d", ErrTooFewDistractorDistances, len(distractor), minimum(targets.Distractor))
	}
	sort.Float64s(author)
	sort.Float64s(distractor)
	a, ok := authorThreshold(author, targets.Author)
	if !ok {
		return nil, fmt.Errorf("calibrate: %w: author population", ErrNoQualifyingThreshold)
	}
	d, ok := distractorThreshold(distractor, targets.Distractor)
	if !ok {
		return nil, fmt.Errorf("calibrate: %w: distractor population", ErrNoQualifyingThreshold)
	}

	out := &Thresholds{
		ProfileID: binding.ProfileID, ReferenceID: binding.ReferenceID,
		FeatureManifestDigest: binding.FeatureManifestDigest, WeightScheme: binding.WeightScheme,
		DistanceAlgorithm: binding.Algorithm, ScoredTiers: append([]features.Tier(nil), binding.ScoredTiers...),
		Split: corpus.Calibrate, Targets: targets, AuthorDistances: len(author),
		DistractorDistances: len(distractor), Source: source, Algorithm: ThresholdAlgorithm,
		Separated: a < d,
	}
	out.Low, out.High = math.Min(a, d), math.Max(a, d)
	out.AchievedAuthor = upperRate(author, out.High)
	out.AchievedDistractor = lowerRate(distractor, out.Low)
	out.ID = thresholdID(out, author, distractor)
	return out, nil
}

// Band assigns a compatible defined distance to an ordered band.
func (t *Thresholds) Band(distance deviation.Distance) (BandOutcome, error) {
	if t == nil {
		return BandOutcome{}, fmt.Errorf("band: %w: thresholds", ErrMissingInput)
	}
	if !distance.Defined {
		return BandOutcome{Distance: distance.Value, Reason: distance.Reason}, nil
	}
	if err := validDistance(distance); err != nil {
		return BandOutcome{}, err
	}
	if err := sameThresholdBinding(t, distance); err != nil {
		return BandOutcome{}, err
	}
	out := BandOutcome{Distance: distance.Value, Defined: true}
	if distance.Value <= t.Low {
		out.Band = BandInRange
	} else if distance.Value >= t.High {
		out.Band = BandNotYou
	} else {
		out.Band = BandDrifting
	}
	return out, nil
}

func validTarget(target float64) bool { return target > 0 && target < 1 && finite(target) }

func minimum(target float64) int { return int(math.Ceil(1 / target)) }

// validCalibrationDistance enforces the split reserved for threshold fitting.
func validCalibrationDistance(distance deviation.Distance) error {
	if distance.Split != corpus.Calibrate {
		return fmt.Errorf("%w: %q", ErrCalibrationSplit, distance.Split)
	}
	return nil
}

// validDistance rejects malformed values for distances that claim a value.
func validDistance(distance deviation.Distance) error {
	if distance.Value < 0 || !finite(distance.Value) {
		return fmt.Errorf("%w: distance value", ErrMalformedInput)
	}
	return nil
}

// sameCalibrationBinding checks every recorded calibration item's provenance.
// Undefined distances report no scored tiers, so the absent tier list cannot
// contradict the binding; their other provenance remains mandatory.
func sameCalibrationBinding(want, got deviation.Distance) error {
	if !got.Defined {
		got.ScoredTiers = want.ScoredTiers
	}
	return sameBinding(want, got)
}

func sameBinding(want, got deviation.Distance) error {
	if got.ProfileID != want.ProfileID {
		return fmt.Errorf("%w: got %q, want %q", ErrProfileMismatch, got.ProfileID, want.ProfileID)
	}
	if got.ReferenceID != want.ReferenceID {
		return fmt.Errorf("%w: got %q, want %q", ErrReferenceMismatch, got.ReferenceID, want.ReferenceID)
	}
	if got.FeatureManifestDigest != want.FeatureManifestDigest {
		return fmt.Errorf("%w: got %q, want %q", ErrManifestMismatch, got.FeatureManifestDigest, want.FeatureManifestDigest)
	}
	if got.WeightScheme != want.WeightScheme {
		return fmt.Errorf("%w: got %q, want %q", ErrWeightingMismatch, got.WeightScheme, want.WeightScheme)
	}
	if got.Algorithm != want.Algorithm {
		return fmt.Errorf("%w: got %q, want %q", ErrAlgorithmMismatch, got.Algorithm, want.Algorithm)
	}
	if !sameTiers(want.ScoredTiers, got.ScoredTiers) {
		return fmt.Errorf("%w", ErrTierMismatch)
	}
	return nil
}

func sameThresholdBinding(thresholds *Thresholds, distance deviation.Distance) error {
	want := deviation.Distance{
		ProfileID: thresholds.ProfileID, ReferenceID: thresholds.ReferenceID,
		FeatureManifestDigest: thresholds.FeatureManifestDigest, WeightScheme: thresholds.WeightScheme,
		Algorithm: thresholds.DistanceAlgorithm, ScoredTiers: thresholds.ScoredTiers,
	}
	return sameBinding(want, distance)
}

func authorThreshold(values []float64, target float64) (float64, bool) {
	for _, value := range values {
		if upperRate(values, value) <= target {
			return value, true
		}
	}
	return 0, false
}

func distractorThreshold(values []float64, target float64) (float64, bool) {
	for index := len(values) - 1; index >= 0; index-- {
		if lowerRate(values, values[index]) <= target {
			return values[index], true
		}
	}
	return 0, false
}

func upperRate(values []float64, threshold float64) float64 {
	return float64(len(values)-sort.SearchFloat64s(values, threshold)) / float64(len(values))
}

func lowerRate(values []float64, threshold float64) float64 {
	return float64(sort.Search(len(values), func(index int) bool { return values[index] > threshold })) / float64(len(values))
}

func canonicalTiers(tiers []features.Tier) []features.Tier {
	out := append([]features.Tier(nil), tiers...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameTiers(left, right []features.Tier) bool {
	left, right = canonicalTiers(left), canonicalTiers(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func thresholdID(thresholds *Thresholds, author, distractor []float64) string {
	return identity.HashInputs(map[string]string{
		"algorithm": ThresholdAlgorithm, "author-distances": populationID(author),
		"author-target": numberID(thresholds.Targets.Author), "calibration-cohort": thresholds.Source.Cohort,
		"distractor-distances": populationID(distractor), "distractor-pool": thresholds.Source.DistractorPool,
		"distractor-target": numberID(thresholds.Targets.Distractor), "distance-algorithm": thresholds.DistanceAlgorithm,
		"feature-manifest-digest": thresholds.FeatureManifestDigest, "profile-id": thresholds.ProfileID,
		"reference-id": thresholds.ReferenceID, "scored-tiers": tiersID(thresholds.ScoredTiers),
		"split": string(thresholds.Split), "weight-scheme": thresholds.WeightScheme,
	})
}

func populationID(values []float64) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = numberID(value)
	}
	return strings.Join(parts, ",")
}

func tiersID(tiers []features.Tier) string {
	parts := make([]string, len(tiers))
	for index, tier := range canonicalTiers(tiers) {
		parts[index] = string(tier)
	}
	return strings.Join(parts, ",")
}

func numberID(value float64) string {
	// -0 and +0 are the same number but would otherwise hash differently.
	if value == 0 {
		value = 0
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
