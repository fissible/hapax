// Package features extracts the versioned candidate feature manifest from text tokens.
package features

import (
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// ID identifies a feature in the manifest.
type ID string

// Tier is a candidate feature tier.
type Tier string

// Sampling identifies the observation model used for a feature's sampling variance.
type Sampling string

const (
	// TierA contains features intended for paragraph-scale measurement.
	TierA Tier = "A"

	// SetVersion is the version of the candidate feature manifest.
	SetVersion = 1

	WordLengthMean   ID = "word_length_mean"
	CommaDensity     ID = "comma_density"
	SemicolonDensity ID = "semicolon_density"
	ColonDensity     ID = "colon_density"
	FunctionWordRate ID = "function_word_rate"
	ClauseMarkerRate ID = "clause_marker_rate"

	SamplingRate    Sampling = "rate"
	SamplingDensity Sampling = "density"
	SamplingMean    Sampling = "mean"
)

func Tiers() []Tier { return []Tier{TierA} }

// SamplingModel records the versioned assumptions shared by sampling families.
type SamplingModel struct {
	Version           string
	DensityDispersion float64
}

// Manifest is the complete, versioned input to feature extraction and sampling.
type Manifest struct {
	Definitions   []Definition
	FunctionWords []string
	ClauseMarkers []string
	Sampling      SamplingModel
}

// CurrentSamplingModel returns the declared, versioned sampling assumptions.
func CurrentSamplingModel() SamplingModel {
	return SamplingModel{Version: "sampling-variance-v1", DensityDispersion: 1}
}

// Definition describes one candidate feature in the manifest.
type Definition struct {
	ID              ID
	CandidateTier   Tier
	TierProvisional bool
	Unvalidated     bool
	Description     string
	Sampling        Sampling
}

// FeatureValue is an extracted feature and whether it is defined for the input.
type FeatureValue struct {
	ID                      ID
	Value                   float64
	Defined                 bool
	SamplingVariance        float64
	SamplingVarianceDefined bool
}

// Vector contains the extracted candidate feature values and token counts.
type Vector struct {
	SetVersion    int
	Tokens        int
	LexicalTokens int
	Values        []FeatureValue
}

// Get returns the value for id.
func (v Vector) Get(id ID) (FeatureValue, bool) {
	for _, value := range v.Values {
		if value.ID == id {
			return value, true
		}
	}
	return FeatureValue{}, false
}

var definitions = []Definition{
	{ID: WordLengthMean, CandidateTier: TierA, TierProvisional: true, Description: "Mean NFC character length of lexical tokens.", Sampling: SamplingMean},
	{ID: CommaDensity, CandidateTier: TierA, TierProvisional: true, Description: "Comma count per lexical token.", Sampling: SamplingDensity},
	{ID: SemicolonDensity, CandidateTier: TierA, TierProvisional: true, Description: "Semicolon count per lexical token.", Sampling: SamplingDensity},
	{ID: ColonDensity, CandidateTier: TierA, TierProvisional: true, Description: "Colon count per lexical token.", Sampling: SamplingDensity},
	{ID: FunctionWordRate, CandidateTier: TierA, TierProvisional: true, Unvalidated: true, Description: "Function-word membership rate among lexical tokens.", Sampling: SamplingRate},
	{ID: ClauseMarkerRate, CandidateTier: TierA, TierProvisional: true, Description: "Surface clause-marker membership rate among lexical tokens.", Sampling: SamplingRate},
}

var functionWords = []string{
	"a", "about", "above", "across", "after", "against", "all", "although", "am", "among", "an", "and", "any", "are", "as", "at", "be", "because", "been", "before", "being", "below", "beneath", "beside", "between", "beyond", "both", "but", "by", "can", "cannot", "could", "did", "do", "does", "doing", "done", "down", "during", "each", "either", "few", "for", "from", "had", "has", "have", "having", "he", "her", "hers", "herself", "him", "himself", "his", "how", "i", "if", "in", "into", "is", "it", "its", "itself", "may", "me", "might", "must", "my", "myself", "neither", "nor", "not", "of", "off", "on", "once", "one", "or", "other", "ought", "our", "ours", "ourselves", "out", "over", "own", "per", "shall", "she", "should", "since", "so", "some", "such", "than", "that", "the", "their", "theirs", "them", "themselves", "then", "there", "these", "they", "this", "those", "though", "through", "throughout", "to", "too", "toward", "towards", "under", "unless", "until", "unto", "up", "upon", "us", "was", "we", "were", "what", "when", "whence", "where", "whereas", "whether", "which", "while", "who", "whom", "whose", "why", "will", "with", "within", "without", "would", "you", "your", "yours", "yourself", "yourselves",
}

var clauseMarkers = []string{
	"that", "which", "who", "whom", "whose", "because", "although", "though", "while", "whereas", "since", "unless", "until", "whether", "if", "when", "where", "after", "before", "as",
}

var (
	fold            = cases.Fold()
	functionWordSet = vocabularySet(functionWords)
	clauseMarkerSet = vocabularySet(clauseMarkers)
)

// Definitions returns the manifest in its stable positional order.
func Definitions() []Definition { return append([]Definition(nil), definitions...) }

// CurrentManifest returns a deep copy of the manifest currently in force.
func CurrentManifest() Manifest {
	return Manifest{
		Definitions:   Definitions(),
		FunctionWords: FunctionWords(),
		ClauseMarkers: ClauseMarkers(),
		Sampling:      CurrentSamplingModel(),
	}
}

// ManifestDigest returns the identity of the current feature manifest and model.
func ManifestDigest() string { return DigestOf(CurrentManifest()) }

// DigestOf returns the stable digest of every field in m.
func DigestOf(m Manifest) string {
	parts := []string{"manifest", "definitions", strconv.Itoa(len(m.Definitions))}
	for _, definition := range m.Definitions {
		parts = append(parts,
			"definition",
			string(definition.ID),
			string(definition.CandidateTier),
			strconv.FormatBool(definition.TierProvisional),
			strconv.FormatBool(definition.Unvalidated),
			definition.Description,
			string(definition.Sampling),
		)
	}
	parts = appendVocabulary(parts, "function-words", m.FunctionWords)
	parts = appendVocabulary(parts, "clause-markers", m.ClauseMarkers)
	parts = append(parts,
		"sampling-model",
		m.Sampling.Version,
		strconv.FormatFloat(m.Sampling.DensityDispersion, 'g', -1, 64),
	)
	return identity.HashBytes(identity.Frame(parts...))
}

func appendVocabulary(parts []string, domain string, words []string) []string {
	canonical := canonicalVocabulary(words)
	return append(parts, append([]string{domain, strconv.Itoa(len(canonical))}, canonical...)...)
}

func canonicalVocabulary(words []string) []string {
	set := vocabularySet(words)
	canonical := make([]string, 0, len(set))
	for word := range set {
		canonical = append(canonical, word)
	}
	sort.Strings(canonical)
	return canonical
}

// FunctionWords returns the versioned function-word vocabulary.
func FunctionWords() []string { return append([]string(nil), functionWords...) }

// ClauseMarkers returns the versioned clause-marker vocabulary.
func ClauseMarkers() []string { return append([]string(nil), clauseMarkers...) }

// Extract computes the manifest values from tokens. It performs no sampling,
// transformation, profiling, or scoring.
func Extract(tokens []text.Token) Vector {
	v := Vector{
		SetVersion: SetVersion,
		Tokens:     len(tokens),
		Values:     make([]FeatureValue, len(definitions)),
	}
	for i, definition := range definitions {
		v.Values[i] = FeatureValue{ID: definition.ID}
	}

	var wordLength, commas, semicolons, colons, functionWords, clauseMarkers int
	wordLengths := make([]float64, 0, len(tokens))
	for _, token := range tokens {
		switch token.Text {
		case ",":
			commas++
		case ";":
			semicolons++
		case ":":
			colons++
		}

		if !token.Lexical {
			continue
		}
		v.LexicalTokens++
		length := utf8.RuneCountInString(norm.NFC.String(token.Text))
		wordLength += length
		wordLengths = append(wordLengths, float64(length))

		word := fold.String(token.Text)
		if functionWordSet[word] {
			functionWords++
		}
		if clauseMarkerSet[word] {
			clauseMarkers++
		}
	}

	if v.LexicalTokens == 0 {
		return v
	}

	denominator := float64(v.LexicalTokens)
	values := map[ID]float64{
		WordLengthMean:   float64(wordLength) / denominator,
		CommaDensity:     float64(commas) / denominator,
		SemicolonDensity: float64(semicolons) / denominator,
		ColonDensity:     float64(colons) / denominator,
		FunctionWordRate: float64(functionWords) / denominator,
		ClauseMarkerRate: float64(clauseMarkers) / denominator,
	}
	meanSamplingVariance, meanSamplingVarianceDefined := sampleMeanVariance(wordLengths)
	model := CurrentSamplingModel()
	for i, definition := range definitions {
		value := values[definition.ID]
		featureValue := FeatureValue{ID: definition.ID, Value: value, Defined: true}
		switch definition.Sampling {
		case SamplingRate:
			featureValue.SamplingVariance = value * (1 - value) / denominator
			featureValue.SamplingVarianceDefined = true
		case SamplingDensity:
			featureValue.SamplingVariance = model.DensityDispersion * value / denominator
			featureValue.SamplingVarianceDefined = true
		case SamplingMean:
			featureValue.SamplingVariance = meanSamplingVariance
			featureValue.SamplingVarianceDefined = meanSamplingVarianceDefined
		}
		v.Values[i] = featureValue
	}
	return v
}

func sampleMeanVariance(values []float64) (float64, bool) {
	if len(values) < 2 {
		return 0, false
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	mean := sum / float64(len(values))
	var squares float64
	for _, value := range values {
		delta := value - mean
		squares += delta * delta
	}
	return squares / float64(len(values)-1) / float64(len(values)), true
}

func vocabularySet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[fold.String(word)] = true
	}
	return set
}
