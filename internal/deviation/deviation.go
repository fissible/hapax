// Package deviation standardizes feature values and ranks them against a
// Calibrate reference distribution.
package deviation

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
)

// Algorithm identifies this reference and rank-transform contract.
const Algorithm = "length-aware-standardization-empirical-normal-v1"

var (
	// ErrMissingInput reports a required input that was not provided.
	ErrMissingInput = errors.New("missing input")
	// ErrProfileUnit reports a profile that was not fitted at paragraph scale.
	ErrProfileUnit = errors.New("profile unit must be paragraph")
	// ErrManifestMismatch reports artifacts that do not contain exactly the live manifest.
	ErrManifestMismatch = errors.New("feature manifest mismatch")
	// ErrReferenceSplit reports a reference that is not wholly Calibrate data.
	ErrReferenceSplit = errors.New("reference must use calibrate split")
	// ErrUnknownSplit reports an unrecorded or unsupported corpus split.
	ErrUnknownSplit = errors.New("unknown split")
	// ErrInvalidRequirements reports invalid reference construction requirements.
	ErrInvalidRequirements = errors.New("invalid requirements")
	// ErrReferenceTooSmall reports fewer segments than the declared reference minimum.
	ErrReferenceTooSmall = errors.New("reference too small")
	// ErrProfileMismatch reports artifacts built from different profiles.
	ErrProfileMismatch = errors.New("profile mismatch")
	// ErrMalformedInput reports a persisted numeric invariant violation.
	ErrMalformedInput = errors.New("malformed input")
)

// Reason identifies why a standardized value or deviation is undefined.
type Reason string

const (
	// ReasonFeatureUndefined means the feature was not measurable on the segment.
	ReasonFeatureUndefined Reason = "feature-undefined"
	// ReasonSamplingVarianceUndefined means its segment sampling variance is unavailable.
	ReasonSamplingVarianceUndefined Reason = "sampling-variance-undefined"
	// ReasonProfileUndefined means the profile lacks sufficient observations.
	ReasonProfileUndefined Reason = "profile-undefined"
	// ReasonProfileVarianceUndefined means the profile lacks a variance estimate.
	ReasonProfileVarianceUndefined Reason = "profile-variance-undefined"
	// ReasonZeroVariance means the combined variance is zero.
	ReasonZeroVariance Reason = "zero-variance"
	// ReasonReferenceTooSmall means the feature has too few Calibrate values.
	ReasonReferenceTooSmall Reason = "reference-too-small"
	// ReasonInsufficientEvidence means no manifest tier met its availability minimum.
	ReasonInsufficientEvidence Reason = "insufficient-evidence"
)

// Standardized is one length-aware standardized feature value.
type Standardized struct {
	Feature features.ID
	Value   float64
	Defined bool
	Reason  Reason
}

// Standardization is a manifest-order standardized feature vector.
type Standardization struct {
	ProfileID, FeatureManifestDigest string
	Split                            corpus.Split
	LexicalTokens                    int
	Values                           []Standardized
}

// Deviation is one normal-quantile transformed standardized value.
type Deviation struct {
	Feature features.ID
	Value   float64
	Defined bool
	Reason  Reason
}

// Deviations is a manifest-order transformed vector with its provenance.
type Deviations struct {
	ProfileID, ReferenceID, FeatureManifestDigest string
	Split                                         corpus.Split
	Values                                        []Deviation
}

// Reference is a Calibrate distribution of standardized values per feature.
type Reference struct {
	ID, ProfileID, FeatureManifestDigest, Algorithm string
	Split                                           corpus.Split
	MinSegments, Segments                           int
	values                                          map[features.ID][]float64
}

// Standardize applies the length-aware standardization to every manifest feature.
func Standardize(v features.Vector, p *profile.Profile, split corpus.Split) (Standardization, error) {
	if !knownSplit(split) {
		return Standardization{}, fmt.Errorf("standardize: %w: %q", ErrUnknownSplit, split)
	}
	if p == nil {
		return Standardization{}, fmt.Errorf("standardize: %w", ErrMissingInput)
	}
	if p.Unit != profile.UnitParagraph {
		return Standardization{}, fmt.Errorf("standardize: %w", ErrProfileUnit)
	}
	if v.SetVersion != features.SetVersion || p.FeatureSetVersion != features.SetVersion || p.FeatureManifestDigest != features.ManifestDigest() {
		return Standardization{}, fmt.Errorf("standardize: %w", ErrManifestMismatch)
	}
	vector, ok := manifestMap(v.Values, func(value features.FeatureValue) features.ID { return value.ID }, features.Definitions())
	if !ok {
		return Standardization{}, fmt.Errorf("standardize: %w", ErrManifestMismatch)
	}
	stats, ok := manifestMap(p.Stats, func(stat profile.Stats) features.ID { return stat.Feature }, features.Definitions())
	if !ok {
		return Standardization{}, fmt.Errorf("standardize: %w", ErrManifestMismatch)
	}
	out := Standardization{ProfileID: p.ID, FeatureManifestDigest: p.FeatureManifestDigest, Split: split, LexicalTokens: v.LexicalTokens, Values: make([]Standardized, 0, len(features.Definitions()))}
	for _, definition := range features.Definitions() {
		fv, st := vector[definition.ID], stats[definition.ID]
		value := Standardized{Feature: definition.ID}
		switch {
		case !fv.Defined:
			value.Reason = ReasonFeatureUndefined
		case !fv.SamplingVarianceDefined:
			value.Reason = ReasonSamplingVarianceUndefined
		case !st.Defined:
			value.Reason = ReasonProfileUndefined
		case !st.VarianceDefined:
			value.Reason = ReasonProfileVarianceUndefined
		case !finite(fv.Value) || !finite(fv.SamplingVariance) || fv.SamplingVariance < 0 || !finite(st.Mean) || !finite(st.Variance) || st.Variance < 0:
			return Standardization{}, fmt.Errorf("standardize %s: %w", definition.ID, ErrMalformedInput)
		default:
			variance := st.Variance + fv.SamplingVariance
			if variance == 0 {
				value.Reason = ReasonZeroVariance
			} else {
				value.Value = (fv.Value - st.Mean) / math.Sqrt(variance)
				value.Defined = true
			}
		}
		out.Values = append(out.Values, value)
	}
	return out, nil
}

// BuildReference builds a per-feature Calibrate distribution.
func BuildReference(p *profile.Profile, split corpus.Split, segments []Standardization, minSegments int) (*Reference, error) {
	if !knownSplit(split) {
		return nil, fmt.Errorf("build reference: %w: %q", ErrUnknownSplit, split)
	}
	if p == nil {
		return nil, fmt.Errorf("build reference: %w", ErrMissingInput)
	}
	if p.Unit != profile.UnitParagraph {
		return nil, fmt.Errorf("build reference: %w", ErrProfileUnit)
	}
	if split != corpus.Calibrate {
		return nil, fmt.Errorf("build reference: %w", ErrReferenceSplit)
	}
	if minSegments <= 0 {
		return nil, fmt.Errorf("build reference: %w", ErrInvalidRequirements)
	}
	if len(segments) < minSegments {
		return nil, fmt.Errorf("build reference: %w", ErrReferenceTooSmall)
	}
	values := make(map[features.ID][]float64, len(features.Definitions()))
	for _, segment := range segments {
		if !knownSplit(segment.Split) {
			return nil, fmt.Errorf("build reference: %w: %q", ErrUnknownSplit, segment.Split)
		}
		if segment.Split != corpus.Calibrate {
			return nil, fmt.Errorf("build reference: %w", ErrReferenceSplit)
		}
		if segment.ProfileID != p.ID {
			return nil, fmt.Errorf("build reference: %w", ErrProfileMismatch)
		}
		if segment.FeatureManifestDigest != p.FeatureManifestDigest {
			return nil, fmt.Errorf("build reference: %w", ErrManifestMismatch)
		}
		entries, ok := manifestMap(segment.Values, func(value Standardized) features.ID { return value.Feature }, features.Definitions())
		if !ok {
			return nil, fmt.Errorf("build reference: %w", ErrManifestMismatch)
		}
		for _, d := range features.Definitions() {
			if entry := entries[d.ID]; entry.Defined {
				if !finite(entry.Value) {
					return nil, fmt.Errorf("build reference %s: %w", d.ID, ErrMalformedInput)
				}
				values[d.ID] = append(values[d.ID], entry.Value)
			}
		}
	}
	for id := range values {
		sort.Float64s(values[id])
	}
	r := &Reference{ProfileID: p.ID, FeatureManifestDigest: p.FeatureManifestDigest, Algorithm: Algorithm, Split: split, MinSegments: minSegments, Segments: len(segments), values: values}
	r.ID = r.identity()
	return r, nil
}

// Size returns the number of defined Calibrate values for a feature.
func (r *Reference) Size(id features.ID) int {
	if r == nil {
		return 0
	}
	return len(r.values[id])
}

// Available reports whether a feature meets the reference minimum.
func (r *Reference) Available(id features.ID) bool { return r != nil && r.Size(id) >= r.MinSegments }

// Cap returns the maximum absolute transformed deviation for an available feature.
func (r *Reference) Cap(id features.ID) (float64, bool) {
	if !r.Available(id) {
		return 0, false
	}
	n := float64(r.Size(id))
	return quantile(1 - 1/(2*(n+1))), true
}

// Transform ranks a standardized query against this reference.
func (r *Reference) Transform(query Standardization) (Deviations, error) {
	if r == nil {
		return Deviations{}, fmt.Errorf("transform: %w", ErrMissingInput)
	}
	if !knownSplit(query.Split) {
		return Deviations{}, fmt.Errorf("transform: %w: %q", ErrUnknownSplit, query.Split)
	}
	if query.ProfileID != r.ProfileID {
		return Deviations{}, fmt.Errorf("transform: %w", ErrProfileMismatch)
	}
	if query.FeatureManifestDigest != r.FeatureManifestDigest {
		return Deviations{}, fmt.Errorf("transform: %w", ErrManifestMismatch)
	}
	entries, ok := manifestMap(query.Values, func(value Standardized) features.ID { return value.Feature }, features.Definitions())
	if !ok {
		return Deviations{}, fmt.Errorf("transform: %w", ErrManifestMismatch)
	}
	out := Deviations{ProfileID: r.ProfileID, ReferenceID: r.ID, FeatureManifestDigest: r.FeatureManifestDigest, Split: query.Split, Values: make([]Deviation, 0, len(features.Definitions()))}
	for _, definition := range features.Definitions() {
		s := entries[definition.ID]
		d := Deviation{Feature: definition.ID}
		if !s.Defined {
			if s.Reason == "" {
				return Deviations{}, fmt.Errorf("transform %s: %w", definition.ID, ErrMalformedInput)
			}
			d.Reason = s.Reason
		} else if !finite(s.Value) {
			return Deviations{}, fmt.Errorf("transform %s: %w", definition.ID, ErrMalformedInput)
		} else if !r.Available(definition.ID) {
			d.Reason = ReasonReferenceTooSmall
		} else {
			refs := r.values[definition.ID]
			lower := sort.SearchFloat64s(refs, s.Value)
			upper := sort.Search(len(refs), func(i int) bool { return refs[i] > s.Value })
			u := (float64(lower) + float64(upper-lower)/2 + .5) / float64(len(refs)+1)
			d.Value = quantile(u)
			d.Defined = true
		}
		out.Values = append(out.Values, d)
	}
	return out, nil
}

func knownSplit(s corpus.Split) bool {
	return s == corpus.Train || s == corpus.Calibrate || s == corpus.Test
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func quantile(u float64) float64 { return math.Sqrt2 * math.Erfinv(2*u-1) }

// manifestMap validates that values contain every definition exactly once.
// It rejects duplicate or unknown value IDs and duplicate or empty manifest IDs.
func manifestMap[T any](values []T, id func(T) features.ID, definitions []features.Definition) (map[features.ID]T, bool) {
	manifest := make(map[features.ID]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.ID == "" {
			return nil, false
		}
		if _, exists := manifest[definition.ID]; exists {
			return nil, false
		}
		manifest[definition.ID] = struct{}{}
	}
	if len(values) != len(manifest) {
		return nil, false
	}

	out := make(map[features.ID]T, len(values))
	for _, value := range values {
		feature := id(value)
		if _, inManifest := manifest[feature]; !inManifest {
			return nil, false
		}
		if _, duplicate := out[feature]; duplicate {
			return nil, false
		}
		out[feature] = value
	}
	return out, true
}

func (r *Reference) identity() string {
	parts := []string{"algorithm", r.Algorithm, "profile-id", r.ProfileID, "manifest-digest", r.FeatureManifestDigest, "split", string(r.Split), "min-segments", strconv.Itoa(r.MinSegments), "segments", strconv.Itoa(r.Segments)}
	for _, d := range features.Definitions() {
		values := r.values[d.ID]
		parts = append(parts, "feature", string(d.ID), "count", strconv.Itoa(len(values)))
		for _, v := range values {
			// -0 and +0 rank identically but would otherwise hash differently.
			if v == 0 {
				v = 0
			}
			parts = append(parts, strconv.FormatFloat(v, 'g', -1, 64))
		}
	}
	return identity.HashBytes(identity.Frame(parts...))
}
