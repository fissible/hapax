// Package features extracts the versioned candidate feature manifest from text tokens.
package features

import (
	"strings"
	"unicode/utf8"

	"github.com/fissible/hapax/internal/text"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// ID identifies a feature in the manifest.
type ID string

// Tier is a candidate feature tier.
type Tier string

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
)

// Definition describes one candidate feature in the manifest.
type Definition struct {
	ID              ID
	CandidateTier   Tier
	TierProvisional bool
	Unvalidated     bool
	Description     string
}

// FeatureValue is an extracted feature and whether it is defined for the input.
type FeatureValue struct {
	ID      ID
	Value   float64
	Defined bool
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
	{ID: WordLengthMean, CandidateTier: TierA, TierProvisional: true, Description: "Mean NFC character length of lexical tokens."},
	{ID: CommaDensity, CandidateTier: TierA, TierProvisional: true, Description: "Comma count per lexical token."},
	{ID: SemicolonDensity, CandidateTier: TierA, TierProvisional: true, Description: "Semicolon count per lexical token."},
	{ID: ColonDensity, CandidateTier: TierA, TierProvisional: true, Description: "Colon count per lexical token."},
	{ID: FunctionWordRate, CandidateTier: TierA, TierProvisional: true, Unvalidated: true, Description: "Function-word membership rate among lexical tokens."},
	{ID: ClauseMarkerRate, CandidateTier: TierA, TierProvisional: true, Description: "Surface clause-marker membership rate among lexical tokens."},
}

var functionWords = []string{
	"a", "about", "above", "across", "after", "against", "all", "am", "among", "an", "and", "any", "are", "as", "at", "be", "because", "been", "before", "being", "below", "beneath", "beside", "between", "beyond", "both", "but", "by", "can", "cannot", "could", "did", "do", "does", "doing", "done", "down", "during", "each", "either", "few", "for", "from", "had", "has", "have", "having", "he", "her", "hers", "herself", "him", "himself", "his", "how", "i", "if", "in", "into", "is", "it", "its", "itself", "may", "me", "might", "must", "my", "myself", "neither", "nor", "not", "of", "off", "on", "once", "one", "or", "other", "ought", "our", "ours", "ourselves", "out", "over", "own", "per", "shall", "she", "should", "since", "so", "some", "such", "than", "that", "the", "their", "theirs", "them", "themselves", "then", "there", "these", "they", "this", "those", "through", "throughout", "to", "too", "toward", "towards", "under", "until", "unto", "up", "upon", "us", "was", "we", "were", "what", "when", "whence", "where", "whether", "which", "while", "who", "whom", "whose", "why", "will", "with", "within", "without", "would", "you", "your", "yours", "yourself", "yourselves",
}

var clauseMarkers = []string{
	"that", "which", "who", "whom", "whose", "because", "although", "though", "while", "whereas", "since", "unless", "until", "whether", "if", "when", "where", "after", "before", "as",
}

var (
	functionWordSet = vocabularySet(functionWords)
	clauseMarkerSet = vocabularySet(clauseMarkers)
	fold            = cases.Fold()
)

// Definitions returns the manifest in its stable positional order.
func Definitions() []Definition { return append([]Definition(nil), definitions...) }

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
		wordLength += utf8.RuneCountInString(norm.NFC.String(token.Text))

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
	v.Values[0] = FeatureValue{ID: WordLengthMean, Value: float64(wordLength) / denominator, Defined: true}
	v.Values[1] = FeatureValue{ID: CommaDensity, Value: float64(commas) / denominator, Defined: true}
	v.Values[2] = FeatureValue{ID: SemicolonDensity, Value: float64(semicolons) / denominator, Defined: true}
	v.Values[3] = FeatureValue{ID: ColonDensity, Value: float64(colons) / denominator, Defined: true}
	v.Values[4] = FeatureValue{ID: FunctionWordRate, Value: float64(functionWords) / denominator, Defined: true}
	v.Values[5] = FeatureValue{ID: ClauseMarkerRate, Value: float64(clauseMarkers) / denominator, Defined: true}
	return v
}

func vocabularySet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[strings.ToLower(word)] = true
	}
	return set
}
