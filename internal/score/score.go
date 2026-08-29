// Package score measures draft paragraphs against a fitted profile and release.
package score

import (
	"errors"
	"fmt"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

// Algorithm identifies the paragraph scoring contract.
const Algorithm = "score-paragraph-v1"

var (
	// ErrMissingInput reports a required profile or reference that was not provided.
	ErrMissingInput = errors.New("score missing input")
	// ErrInvalidRequirements reports malformed profile scoring requirements.
	ErrInvalidRequirements = errors.New("score invalid requirements")
	// ErrProfileMismatch reports artifacts built from different profiles.
	ErrProfileMismatch = errors.New("score profile mismatch")
	// ErrReferenceMismatch reports artifacts built from different references.
	ErrReferenceMismatch = errors.New("score reference mismatch")
)

// Direction identifies the signed direction of a defined feature deviation.
type Direction string

const (
	DirectionAbove   Direction = "above"
	DirectionBelow   Direction = "below"
	DirectionTypical Direction = "typical"
)

// FeatureDelta is one manifest-order transformed feature result.
type FeatureDelta struct {
	Feature   features.ID
	Deviation float64
	Defined   bool
	Reason    deviation.Reason
	Direction Direction
}

// Segment is one admitted draft paragraph and its score artifacts.
type Segment struct {
	Index, LexicalTokens int
	Distance             deviation.Distance
	Band                 eval.BandOutcome
	Features             []FeatureDelta
}

// Report is the complete score for a draft.
type Report struct {
	ProfileID, ReferenceID, ReleaseID, FeatureManifestDigest, Algorithm string
	Split                                                               corpus.Split
	Calibrated                                                          bool
	ParagraphsBelowFloor                                                int
	Segments                                                            []Segment
}

// Score measures every paragraph admitted under the profile's own floor.
func Score(source []byte, prof *profile.Profile, ref *deviation.Reference, release eval.Release) (Report, error) {
	if err := validate(prof, ref, release); err != nil {
		return Report{}, err
	}

	doc, err := text.Admit(source)
	if err != nil {
		return Report{}, fmt.Errorf("score admit draft: %w", err)
	}
	paragraphs, err := profile.ParagraphVectors(doc, prof.Requirements.MinParagraphLexicalTokens)
	if err != nil {
		return Report{}, fmt.Errorf("score paragraphs: %w", err)
	}

	report := Report{
		ProfileID: prof.ID, ReferenceID: ref.ID, ReleaseID: release.ID,
		FeatureManifestDigest: prof.FeatureManifestDigest, Algorithm: Algorithm,
		Split: corpus.Draft, Calibrated: release.Shippable,
		ParagraphsBelowFloor: paragraphs.BelowFloor,
		Segments:             make([]Segment, 0, len(paragraphs.Vectors)),
	}
	for index, vector := range paragraphs.Vectors {
		standardized, err := deviation.Standardize(vector, prof, corpus.Draft)
		if err != nil {
			return Report{}, fmt.Errorf("score paragraph %d: %w", index, err)
		}
		deviations, err := ref.Transform(standardized)
		if err != nil {
			return Report{}, fmt.Errorf("score paragraph %d: %w", index, err)
		}
		distance, err := deviations.Distance()
		if err != nil {
			return Report{}, fmt.Errorf("score paragraph %d: %w", index, err)
		}
		band, err := release.Band(distance)
		if err != nil {
			return Report{}, fmt.Errorf("score paragraph %d: %w", index, err)
		}
		report.Segments = append(report.Segments, Segment{
			Index: index, LexicalTokens: vector.LexicalTokens, Distance: distance, Band: band,
			Features: featureDeltas(deviations),
		})
	}
	return report, nil
}

func validate(prof *profile.Profile, ref *deviation.Reference, release eval.Release) error {
	if prof == nil {
		return fmt.Errorf("%w: profile", ErrMissingInput)
	}
	if ref == nil {
		return fmt.Errorf("%w: reference", ErrMissingInput)
	}
	if prof.Requirements.MinParagraphLexicalTokens <= 0 {
		return fmt.Errorf("%w: got %d, want > 0", ErrInvalidRequirements, prof.Requirements.MinParagraphLexicalTokens)
	}
	if prof.Unit != profile.UnitParagraph {
		return deviation.ErrProfileUnit
	}
	// A profile ID hashes all profile identity inputs, including its feature manifest
	// digest, so matching IDs make a separate manifest-digest comparison redundant.
	if ref.ProfileID != prof.ID {
		return fmt.Errorf("%w: got %q, want %q", ErrProfileMismatch, ref.ProfileID, prof.ID)
	}
	if release.Discrimination.ProfileID != prof.ID || release.Calibration.ProfileID != prof.ID {
		return fmt.Errorf("%w: got discrimination %q and calibration %q, want %q", ErrProfileMismatch, release.Discrimination.ProfileID, release.Calibration.ProfileID, prof.ID)
	}
	if release.Discrimination.ReferenceID != ref.ID || release.Calibration.ReferenceID != ref.ID {
		return fmt.Errorf("%w: got discrimination %q and calibration %q, want %q", ErrReferenceMismatch, release.Discrimination.ReferenceID, release.Calibration.ReferenceID, ref.ID)
	}
	return nil
}

func featureDeltas(deviations deviation.Deviations) []FeatureDelta {
	values := make(map[features.ID]deviation.Deviation, len(deviations.Values))
	for _, value := range deviations.Values {
		values[value.Feature] = value
	}
	out := make([]FeatureDelta, 0, len(features.Definitions()))
	for _, definition := range features.Definitions() {
		value := values[definition.ID]
		delta := FeatureDelta{Feature: definition.ID, Defined: value.Defined, Reason: value.Reason}
		if value.Defined {
			delta.Deviation = value.Value
			switch {
			case value.Value > 0:
				delta.Direction = DirectionAbove
			case value.Value < 0:
				delta.Direction = DirectionBelow
			default:
				delta.Direction = DirectionTypical
			}
		}
		out = append(out, delta)
	}
	return out
}
