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

// BandCalibrationAlgorithm identifies the declared held-out band gate.
const BandCalibrationAlgorithm = "band-error-bound-v1"

func CalibrationReasons() []string { return []string{"", "no-claiming-band-emitted"} }
func BandReportReasons() []string {
	return []string{"", "empty-error-class", "error-bound-exceeds-target"}
}

// ReasonUncalibrated identifies a profile whose held-out calibration emitted no claiming bands.
const ReasonUncalibrated deviation.Reason = "uncalibrated"

// BandFloor declares the reproducible one-sided clustered-bootstrap bound.
type BandFloor struct {
	Confidence float64
	Resamples  int
	Seed       uint64
}

// BandReport records the evidence and decision for one ordered band.
type BandReport struct {
	Band                                           Band
	Claims                                         Class
	Target, ErrorRate, ErrorBound                  float64
	ClassSegments, ClassClusters, MinClassClusters int
	AuthorSegments, DistractorSegments             int
	Emitted                                        bool
	Reason                                         string
}

// Calibration is the authoritative, held-out classification artifact.
type Calibration struct {
	ID, ThresholdsID, PopulationID                                                 string
	Low, High                                                                      float64
	ProfileID, ReferenceID, FeatureManifestDigest, WeightScheme, DistanceAlgorithm string
	ScoredTiers                                                                    []features.Tier
	Split                                                                          corpus.Split
	Floor                                                                          BandFloor
	Algorithm                                                                      string
	Bands                                                                          []BandReport
	Calibrated                                                                     bool
	Reason                                                                         string
}

var (
	// ErrTestSplit reports a distance outside the held-out Test split.
	ErrTestSplit = errors.New("eval band calibration test split mismatch")
	// ErrInvalidBandFloor reports an invalid confidence or resample count.
	ErrInvalidBandFloor = errors.New("eval band calibration invalid floor")
)

// DefaultBandFloor returns the v1 declared band calibration parameters.
func DefaultBandFloor() BandFloor {
	return BandFloor{Confidence: 0.95, Resamples: 2000, Seed: 0x68617061785F7631}
}

// CalibrateBands measures claiming-band error on Test and decides what labels
// this calibration may emit.
func (t *Thresholds) CalibrateBands(distances []ClassedDistance, floor BandFloor) (Calibration, error) {
	if t == nil || len(distances) == 0 {
		return Calibration{}, fmt.Errorf("calibrate bands: %w", ErrMissingInput)
	}
	if !validBandFloor(floor) {
		return Calibration{}, fmt.Errorf("calibrate bands: %w", ErrInvalidBandFloor)
	}

	defined := make([]ClassedDistance, 0, len(distances))
	for _, item := range distances {
		if item.Class != ClassAuthor && item.Class != ClassDistractor {
			return Calibration{}, fmt.Errorf("%w: %q", ErrUnknownClass, item.Class)
		}
		if item.Distance.Split != corpus.Test {
			return Calibration{}, fmt.Errorf("%w: %q", ErrTestSplit, item.Distance.Split)
		}
		if item.Distance.Defined {
			if err := validDistance(item.Distance); err != nil {
				return Calibration{}, err
			}
			if err := sameThresholdBinding(t, item.Distance); err != nil {
				return Calibration{}, err
			}
			defined = append(defined, item)
		}
	}

	// Unscoreable distances are omitted before cluster validation: their absent
	// document label is not evidence about either class.
	author, distractor, clustering, err := bootstrapClusters(defined)
	if err != nil {
		return Calibration{}, err
	}
	inRange := bandReport(BandInRange, ClassDistractor, t.Targets.Distractor, distractor, floor, func(value float64) bool {
		return value <= t.Low
	})
	inRange.AuthorSegments, inRange.DistractorSegments = occupancy(defined, t, BandInRange)
	drifting := BandReport{Band: BandDrifting, Emitted: true}
	drifting.AuthorSegments, drifting.DistractorSegments = occupancy(defined, t, BandDrifting)
	notYou := bandReport(BandNotYou, ClassAuthor, t.Targets.Author, author, floor, func(value float64) bool {
		return value >= t.High
	})
	notYou.AuthorSegments, notYou.DistractorSegments = occupancy(defined, t, BandNotYou)

	out := Calibration{
		ThresholdsID: t.ID, ProfileID: t.ProfileID, ReferenceID: t.ReferenceID,
		FeatureManifestDigest: t.FeatureManifestDigest, WeightScheme: t.WeightScheme,
		DistanceAlgorithm: t.DistanceAlgorithm, ScoredTiers: append([]features.Tier(nil), t.ScoredTiers...),
		Low: t.Low, High: t.High, Split: corpus.Test, Floor: floor,
		Algorithm: BandCalibrationAlgorithm, Bands: []BandReport{inRange, drifting, notYou},
	}
	out.Calibrated = inRange.Emitted || notYou.Emitted
	if !out.Calibrated {
		out.Reason = "no-claiming-band-emitted"
	}
	out.PopulationID = clusteredPopulationID(author, distractor, clustering)
	out.ID = bandCalibrationID(&out, author, distractor, clustering)
	return out, nil
}

// clusteredPopulationID identifies the held-out population and its independence
// partition. It is deliberately shared by both release gates.
func clusteredPopulationID(author, distractor []bootstrapCluster, clustering Clustering) string {
	return identity.HashInputs(map[string]string{
		"author-membership":     clusterMembershipID(author),
		"clustering":            string(clustering),
		"distractor-membership": clusterMembershipID(distractor),
	})
}

func validBandFloor(floor BandFloor) bool {
	return floor.Resamples > 0 && floor.Confidence > 0 && floor.Confidence < 1 && finite(floor.Confidence)
}

func clusterSegments(clusters []bootstrapCluster) int {
	segments := 0
	for _, cluster := range clusters {
		segments += len(cluster.items)
	}
	return segments
}

func occupancy(distances []ClassedDistance, thresholds *Thresholds, band Band) (author, distractor int) {
	for _, item := range distances {
		actual := BandDrifting
		if item.Distance.Value <= thresholds.Low {
			actual = BandInRange
		} else if item.Distance.Value >= thresholds.High {
			actual = BandNotYou
		}
		if actual != band {
			continue
		}
		if item.Class == ClassAuthor {
			author++
		} else {
			distractor++
		}
	}
	return author, distractor
}

func bandReport(band Band, claims Class, target float64, clusters []bootstrapCluster, floor BandFloor, wrong func(float64) bool) BandReport {
	report := BandReport{
		Band: band, Claims: claims, Target: target, ClassClusters: len(clusters),
		ClassSegments: clusterSegments(clusters), MinClassClusters: int(math.Ceil(3 / target)),
	}
	if len(clusters) == 0 {
		report.ErrorBound = 1
		report.Reason = "empty-error-class"
		return report
	}

	errors := 0
	for _, cluster := range clusters {
		for _, item := range cluster.items {
			if wrong(item.Distance.Value) {
				errors++
			}
		}
	}
	report.ErrorRate = float64(errors) / float64(report.ClassSegments)
	report.ErrorBound = math.Max(oneSidedBound(clusters, floor, claims, wrong), 3/float64(len(clusters)))
	report.Emitted = report.ErrorBound <= target
	if !report.Emitted {
		report.Reason = "error-bound-exceeds-target"
	}
	return report
}

func oneSidedBound(clusters []bootstrapCluster, floor BandFloor, class Class, wrong func(float64) bool) float64 {
	offset := uint64(1)
	if class == ClassDistractor {
		offset = 2
	}
	random := splitMix64{state: floor.Seed + offset}
	rates := make([]float64, floor.Resamples)
	for resample := range rates {
		segments, errors := 0, 0
		for range clusters {
			cluster := clusters[random.next()%uint64(len(clusters))]
			segments += len(cluster.items)
			for _, item := range cluster.items {
				if wrong(item.Distance.Value) {
					errors++
				}
			}
		}
		rates[resample] = float64(errors) / float64(segments)
	}
	sort.Float64s(rates)
	index := int(math.Floor(floor.Confidence * float64(len(rates))))
	if index >= len(rates) {
		index = len(rates) - 1
	}
	return rates[index]
}

func bandCalibrationID(calibration *Calibration, author, distractor []bootstrapCluster, clustering Clustering) string {
	return identity.HashInputs(map[string]string{
		"algorithm": BandCalibrationAlgorithm, "author-clusters": strconv.Itoa(len(author)),
		"author-membership": clusterMembershipID(author), "clustering": string(clustering),
		"confidence": numberID(calibration.Floor.Confidence), "distractor-clusters": strconv.Itoa(len(distractor)),
		"distractor-membership": clusterMembershipID(distractor), "resamples": strconv.Itoa(calibration.Floor.Resamples),
		"seed": strconv.FormatUint(calibration.Floor.Seed, 10), "thresholds-id": calibration.ThresholdsID,
	})
}

// Band assigns a compatible distance to the labels this calibration may emit.
func (c Calibration) Band(distance deviation.Distance) (BandOutcome, error) {
	if !distance.Defined {
		return BandOutcome{Distance: distance.Value, Reason: distance.Reason}, nil
	}
	if err := validDistance(distance); err != nil {
		return BandOutcome{}, err
	}
	if err := sameBandCalibrationBinding(c, distance); err != nil {
		return BandOutcome{}, err
	}
	if c.Calibrated && c.Low > c.High {
		return BandOutcome{}, ErrNotSeparated
	}
	geometric := BandOutcome{Distance: distance.Value, Defined: true}
	if distance.Value <= c.Low {
		geometric.Band = BandInRange
	} else if distance.Value >= c.High {
		geometric.Band = BandNotYou
	} else {
		geometric.Band = BandDrifting
	}
	if !c.Calibrated {
		return BandOutcome{Distance: distance.Value, Reason: ReasonUncalibrated}, nil
	}
	for _, report := range c.Bands {
		if report.Band == geometric.Band && !report.Emitted {
			geometric.Band = BandDrifting
			break
		}
	}
	return geometric, nil
}

func sameBandCalibrationBinding(calibration Calibration, distance deviation.Distance) error {
	want := deviation.Distance{
		ProfileID: calibration.ProfileID, ReferenceID: calibration.ReferenceID,
		FeatureManifestDigest: calibration.FeatureManifestDigest, WeightScheme: calibration.WeightScheme,
		Algorithm: calibration.DistanceAlgorithm, ScoredTiers: calibration.ScoredTiers,
	}
	return sameBinding(want, distance)
}
