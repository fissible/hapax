// Package profile builds provenance-carrying, paragraph-unit feature profiles.
package profile

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/snapshot"
	"github.com/fissible/hapax/internal/text"
)

const profileSchemaVersion = 1

// ErrCorpusTooSmall distinguishes an adverse corpus outcome from an I/O failure.
var ErrCorpusTooSmall = errors.New("profile: corpus too small")

const notReadyMinimumsNotDerived = "profile minimums are declared, not derived"

func NotReadyReasons() []string { return []string{notReadyMinimumsNotDerived} }

func DefaultRequirements() Requirements {
	return Requirements{MinDocuments: 3, MinParagraphs: 30, MinObservationsPerFeature: 30, MinParagraphLexicalTokens: 1}
}

// Unit identifies the source unit from which each feature vector was made.
type Unit string

const (
	// UnitDocument identifies profiles persisted before paragraph measurement.
	UnitDocument Unit = "document"
	// UnitParagraph identifies profiles built from included text leaves.
	UnitParagraph Unit = "paragraph"
)

func Units() []Unit { return []Unit{UnitDocument, UnitParagraph} }

// VarianceConvention identifies how profile variances are calculated.
type VarianceConvention string

const (
	// SampleVariance uses the n-1 denominator.
	SampleVariance VarianceConvention = "sample"
)

func VarianceConventions() []VarianceConvention { return []VarianceConvention{SampleVariance} }

const (
	// MADMedianV1 trims values beyond a configured number of scaled median absolute deviations.
	MADMedianV1 = "mad-median-v1"
	// NoTrimming records that no outlier handling was requested.
	NoTrimming = "none"
)

// Requirements are the explicit build-time floors and optional trimming rule.
type Requirements struct {
	MinDocuments              int
	MinParagraphs             int
	MinObservationsPerFeature int
	MinParagraphLexicalTokens int
	OutlierMADs               float64
}

// Stats are one feature's distribution statistics and accounting counts.
type Stats struct {
	Feature                  features.ID
	N, Undefined, Excluded   int
	Mean, Variance           float64
	Defined, VarianceDefined bool
	MinObservations          int
	MinObservationsDerived   bool
}

// Profile is a statistical artifact only. Calibration and score policy belong to eval.
type Profile struct {
	ID, SnapshotID, Register string
	Split                    corpus.Split
	Unit                     Unit
	ProductionReady          bool
	NotProductionReason      string
	FeatureSetVersion        int
	FeatureManifestDigest    string
	SchemaVersion            int
	VarianceConvention       VarianceConvention
	OutlierAlgorithm         string
	Requirements             Requirements
	Documents                int
	Paragraphs               int
	ParagraphsBelowFloor     int
	ParagraphFloorDerived    bool
	Stats                    []Stats
}

// Paragraphs is the paragraph-scale feature population admitted by a lexical
// token floor. Keeping this path shared prevents profile fitting and evaluation
// from silently adopting different definitions of a paragraph.
type Paragraphs struct {
	Vectors    []features.Vector
	BelowFloor int
}

// ParagraphLeaf ties an admitted feature vector to the caller-owned tree node.
type ParagraphLeaf struct {
	Node   *text.Node
	Vector features.Vector
}

func ParagraphLeaves(doc *text.Document, root *text.Node, minLexicalTokens int) ([]ParagraphLeaf, int, error) {
	if doc == nil || root == nil {
		return nil, 0, errors.New("paragraph document and root must not be nil")
	}
	var leaves []ParagraphLeaf
	below := 0
	for _, node := range root.IncludedLeaves() {
		tokens, err := doc.RunTokens(node)
		if err != nil {
			return nil, 0, fmt.Errorf("read paragraph: %w", err)
		}
		vector := features.Extract(tokens)
		if vector.LexicalTokens < minLexicalTokens {
			below++
			continue
		}
		leaves = append(leaves, ParagraphLeaf{Node: node, Vector: vector})
	}
	return leaves, below, nil
}

// ParagraphVectors extracts vectors for included paragraph leaves that meet
// minLexicalTokens. It reports excluded leaves separately so callers can retain
// their own accounting without reimplementing the admission rule.
func ParagraphVectors(doc *text.Document, minLexicalTokens int) (Paragraphs, error) {
	if doc == nil {
		return Paragraphs{}, errors.New("paragraph document must not be nil")
	}

	leaves, below, err := ParagraphLeaves(doc, doc.Structure(text.DefaultStructureOptions()), minLexicalTokens)
	if err != nil {
		return Paragraphs{}, err
	}
	paragraphs := Paragraphs{BelowFloor: below}
	for _, leaf := range leaves {
		paragraphs.Vectors = append(paragraphs.Vectors, leaf.Vector)
	}
	return paragraphs, nil
}

// Build calculates train-split paragraph statistics for a snapshot.
func Build(root string, snap *corpus.Snapshot, req Requirements) (*Profile, error) {
	if err := validateRequirements(req); err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, errors.New("profile snapshot must not be nil")
	}

	eligible := snap.Eligible()
	if len(eligible) == 0 {
		return nil, fmt.Errorf("%w: profile requires at least one eligible document", ErrCorpusTooSmall)
	}
	if len(snap.Documents) < req.MinDocuments {
		return nil, fmt.Errorf("%w: profile requires at least %d snapshot documents; snapshot has %d", ErrCorpusTooSmall, req.MinDocuments, len(snap.Documents))
	}
	train := make([]corpus.Document, 0, len(eligible))
	for _, document := range eligible {
		if document.Split == corpus.Train {
			train = append(train, document)
		}
	}
	if len(train) == 0 {
		// A vanished eligible file is an operational failure even when the split
		// would otherwise be insufficient; do not classify I/O as corpus size.
		for _, document := range eligible {
			if _, err := readVerified(root, document); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%w: profile requires at least one train document", ErrCorpusTooSmall)
	}

	values := make(map[features.ID][]float64, len(features.Definitions()))
	undefined := make(map[features.ID]int, len(features.Definitions()))
	paragraphs := 0
	paragraphsBelowFloor := 0
	for _, document := range train {
		admitted, err := readVerified(root, document)
		if err != nil {
			return nil, err
		}
		paragraphVectors, err := ParagraphVectors(admitted, req.MinParagraphLexicalTokens)
		if err != nil {
			return nil, fmt.Errorf("read paragraphs in snapshot document %q: %w", document.Path, err)
		}
		paragraphsBelowFloor += paragraphVectors.BelowFloor
		for _, vector := range paragraphVectors.Vectors {
			paragraphs++
			for _, definition := range features.Definitions() {
				value, ok := vector.Get(definition.ID)
				if !ok || !value.Defined {
					undefined[definition.ID]++
					continue
				}
				values[definition.ID] = append(values[definition.ID], value.Value)
			}
		}
	}
	if paragraphs < req.MinParagraphs {
		return nil, fmt.Errorf("%w: profile requires at least %d paragraphs; train documents have %d", ErrCorpusTooSmall, req.MinParagraphs, paragraphs)
	}

	p := &Profile{
		SnapshotID:            snap.ID,
		Register:              snap.Policy.Register,
		Split:                 corpus.Train,
		Unit:                  UnitParagraph,
		ProductionReady:       false,
		NotProductionReason:   notReadyMinimumsNotDerived,
		FeatureSetVersion:     features.SetVersion,
		FeatureManifestDigest: features.ManifestDigest(),
		SchemaVersion:         profileSchemaVersion,
		VarianceConvention:    SampleVariance,
		OutlierAlgorithm:      NoTrimming,
		Requirements:          req,
		Documents:             len(train),
		Paragraphs:            paragraphs,
		ParagraphsBelowFloor:  paragraphsBelowFloor,
		ParagraphFloorDerived: false,
		Stats:                 make([]Stats, 0, len(features.Definitions())),
	}
	if req.OutlierMADs > 0 {
		p.OutlierAlgorithm = MADMedianV1
	}
	for _, definition := range features.Definitions() {
		kept, excluded := trim(values[definition.ID], req.OutlierMADs)
		p.Stats = append(p.Stats, makeStats(definition.ID, kept, undefined[definition.ID], excluded, req.MinObservationsPerFeature))
	}
	p.ID = identity.HashInputs(p.IdentityInputs())
	return p, nil
}

// Stat finds feature statistics by feature ID.
func (p *Profile) Stat(id features.ID) (Stats, bool) {
	for _, stat := range p.Stats {
		if stat.Feature == id {
			return stat, true
		}
	}
	return Stats{}, false
}

// RebindSnapshot associates the profile with its persisted snapshot and
// recomputes the profile identity from the package's complete identity rule.
func (p *Profile) RebindSnapshot(snapshotID string) {
	p.SnapshotID = snapshotID
	p.ID = identity.HashInputs(p.IdentityInputs())
}

// IdentityInputs returns the complete reviewable cache identity inputs.
func (p *Profile) IdentityInputs() map[string]string {
	return map[string]string{
		"feature-manifest-digest":      p.FeatureManifestDigest,
		"min-documents":                strconv.Itoa(p.Requirements.MinDocuments),
		"min-observations-per-feature": strconv.Itoa(p.Requirements.MinObservationsPerFeature),
		"min-paragraph-lexical-tokens": strconv.Itoa(p.Requirements.MinParagraphLexicalTokens),
		"min-paragraphs":               strconv.Itoa(p.Requirements.MinParagraphs),
		"outlier-algorithm":            p.OutlierAlgorithm,
		"outlier-mads":                 strconv.FormatFloat(p.Requirements.OutlierMADs, 'g', -1, 64),
		"profile-schema-version":       strconv.Itoa(p.SchemaVersion),
		"register":                     p.Register,
		"snapshot-id":                  p.SnapshotID,
		"split":                        string(p.Split),
		"unit":                         string(p.Unit),
		"variance-convention":          string(p.VarianceConvention),
	}
}

func validateRequirements(req Requirements) error {
	if req.MinDocuments <= 0 {
		return errors.New("minimum documents must be positive")
	}
	if req.MinParagraphs <= 0 {
		return errors.New("minimum paragraphs must be positive")
	}
	if req.MinObservationsPerFeature <= 0 {
		return errors.New("minimum observations per feature must be positive")
	}
	if req.MinParagraphLexicalTokens <= 0 {
		return errors.New("minimum paragraph lexical tokens must be positive")
	}
	if req.OutlierMADs < 0 {
		return errors.New("outlier MADs must not be negative")
	}
	return nil
}

func readVerified(root string, document corpus.Document) (*text.Document, error) {
	return snapshot.ReadVerified(root, document.Path, document.ContentHash)
}

func makeStats(id features.ID, values []float64, undefined, excluded, minimum int) Stats {
	stat := Stats{
		Feature:         id,
		N:               len(values),
		Undefined:       undefined,
		Excluded:        excluded,
		MinObservations: minimum,
	}
	if stat.N < minimum {
		return stat
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	stat.Mean = sum / float64(stat.N)
	stat.Defined = true
	if stat.N < 2 {
		return stat
	}
	var squares float64
	for _, value := range values {
		delta := value - stat.Mean
		squares += delta * delta
	}
	stat.Variance = squares / float64(stat.N-1)
	stat.VarianceDefined = true
	return stat
}

func trim(values []float64, madLimit float64) ([]float64, int) {
	if madLimit == 0 || len(values) == 0 {
		return append([]float64(nil), values...), 0
	}
	center := median(values)
	deviations := make([]float64, len(values))
	for i, value := range values {
		deviations[i] = math.Abs(value - center)
	}
	// Scale raw MAD to its normal-distribution-consistent sigma estimate.
	mad := 1.4826 * median(deviations)
	if mad == 0 {
		return append([]float64(nil), values...), 0
	}
	kept := make([]float64, 0, len(values))
	var excluded int
	for _, value := range values {
		if math.Abs(value-center) > madLimit*mad {
			excluded++
			continue
		}
		kept = append(kept, value)
	}
	return kept, excluded
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 != 0 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}
