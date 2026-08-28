// Package profile builds provenance-carrying, paragraph-unit feature profiles.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/text"
)

const profileSchemaVersion = 1

// Unit identifies the source unit from which each feature vector was made.
type Unit string

const (
	// UnitDocument identifies profiles persisted before paragraph measurement.
	UnitDocument Unit = "document"
	// UnitParagraph identifies profiles built from included text leaves.
	UnitParagraph Unit = "paragraph"
)

// VarianceConvention identifies how profile variances are calculated.
type VarianceConvention string

const (
	// SampleVariance uses the n-1 denominator.
	SampleVariance VarianceConvention = "sample"
)

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
		return nil, errors.New("profile requires at least one eligible document")
	}
	if len(snap.Documents) < req.MinDocuments {
		return nil, fmt.Errorf("profile requires at least %d snapshot documents; snapshot has %d", req.MinDocuments, len(snap.Documents))
	}
	train := make([]corpus.Document, 0, len(eligible))
	for _, document := range eligible {
		if document.Split == corpus.Train {
			train = append(train, document)
		}
	}
	if len(train) == 0 {
		return nil, errors.New("profile requires at least one train document")
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
		for _, leaf := range admitted.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
			tokens, err := admitted.RunTokens(leaf)
			if err != nil {
				return nil, fmt.Errorf("read paragraph in snapshot document %q: %w", document.Path, err)
			}
			vector := features.Extract(tokens)
			if vector.LexicalTokens < req.MinParagraphLexicalTokens {
				paragraphsBelowFloor++
				continue
			}
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
		return nil, fmt.Errorf("profile requires at least %d paragraphs; train documents have %d", req.MinParagraphs, paragraphs)
	}

	p := &Profile{
		SnapshotID:            snap.ID,
		Register:              snap.Policy.Register,
		Split:                 corpus.Train,
		Unit:                  UnitParagraph,
		ProductionReady:       false,
		NotProductionReason:   "profile minimums are declared, not derived",
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
	p.ID = hashInputs(p.IdentityInputs())
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
	path := filepath.Join(root, filepath.FromSlash(document.Path))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot document %q: %w", document.Path, err)
	}
	admitted, err := text.Admit(raw)
	if err != nil {
		return nil, fmt.Errorf("admit snapshot document %q: %w", document.Path, err)
	}
	// corpus.ContentHash is the hash of admitted raw bytes for admitted documents,
	// so this comparison intentionally verifies that same BOM-stripped representation.
	digest := hashBytes(admitted.Raw())
	if digest != document.ContentHash {
		return nil, fmt.Errorf("snapshot document %q changed since the snapshot", document.Path)
	}
	return admitted, nil
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

func hashInputs(inputs map[string]string) string {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		parts = append(parts, key, inputs[key])
	}
	return hashBytes(frame(parts...))
}

func frame(parts ...string) []byte {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(strconv.Itoa(len(part)))
		builder.WriteByte(':')
		builder.WriteString(part)
	}
	return []byte(builder.String())
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
