// Package preserve compares the deterministic surface proxies protected by ADR 0006.
package preserve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/text"
	"golang.org/x/text/cases"
)

// Version identifies this gate and its declared vocabularies.
const Version = "preserve-v1"

// Class identifies a protected surface proxy.
type Class string

const (
	ClassNumber   Class = "number"
	ClassEntity   Class = "entity"
	ClassNegation Class = "negation"
	ClassURL      Class = "url"
	ClassQuote    Class = "quote"
)

// Direction identifies whether an item was removed or added.
type Direction string

const (
	DirectionLost     Direction = "lost"
	DirectionInvented Direction = "invented"
)

// Difference is one surface item whose multiplicity differs between texts.
type Difference struct {
	Class     Class
	Item      string
	Direction Direction
}

// Result is the deterministic preservation decision and its explanatory differences.
type Result struct {
	Preserved   bool
	Differences []Difference
}

var classes = []Class{ClassNumber, ClassEntity, ClassNegation, ClassURL, ClassQuote}

var directions = []Direction{DirectionLost, DirectionInvented}

var negations = []string{
	"cannot", "neither", "never", "no", "nobody", "none", "nor", "not", "nothing", "nowhere", "without",
	"aren't", "aren’t", "can't", "can’t", "couldn't", "couldn’t", "didn't", "didn’t", "doesn't", "doesn’t", "don't", "don’t",
	"hadn't", "hadn’t", "hasn't", "hasn’t", "haven't", "haven’t", "isn't", "isn’t", "shouldn't", "shouldn’t",
	"wasn't", "wasn’t", "weren't", "weren’t", "won't", "won’t", "wouldn't", "wouldn’t",
}

var (
	fold            = cases.Fold()
	functionWordSet = vocabularySet(features.FunctionWords())
	negationSet     = vocabularySet(negations)
)

// Negations returns the closed, versioned negation vocabulary.
func Negations() []string { return append([]string(nil), negations...) }

// Identifiers returns non-reversible audit identifiers for the result differences.
func (r Result) Identifiers() []string {
	identifiers := make([]string, len(r.Differences))
	for i, difference := range r.Differences {
		preimage := strings.Join([]string{Version, string(difference.Class), string(difference.Direction), difference.Item}, "\x00")
		digest := sha256.Sum256([]byte(preimage))
		identifiers[i] = Version + ":" + string(difference.Class) + ":" + string(difference.Direction) + ":" + hex.EncodeToString(digest[:])[:16]
	}
	return identifiers
}

// ValidIdentifier reports whether identifier has the exact, non-reversible
// audit identifier grammar produced by Result.Identifiers.
func ValidIdentifier(identifier string) bool {
	parts := strings.Split(identifier, ":")
	if len(parts) != 4 || parts[0] != Version {
		return false
	}
	if !declaredClass(Class(parts[1])) || !declaredDirection(Direction(parts[2])) {
		return false
	}
	if len(parts[3]) != 16 {
		return false
	}
	for _, c := range parts[3] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func declaredClass(class Class) bool {
	for _, declared := range classes {
		if class == declared {
			return true
		}
	}
	return false
}

func declaredDirection(direction Direction) bool {
	for _, declared := range directions {
		if direction == declared {
			return true
		}
	}
	return false
}

// Check compares protected surface forms in current and candidate.
func Check(current, candidate string) (Result, error) {
	currentDoc, err := text.Admit([]byte(current))
	if err != nil {
		return Result{}, fmt.Errorf("admit current text: %w", err)
	}
	candidateDoc, err := text.Admit([]byte(candidate))
	if err != nil {
		return Result{}, fmt.Errorf("admit candidate text: %w", err)
	}

	currentItems := extract(currentDoc)
	candidateItems := extract(candidateDoc)
	differences := make([]Difference, 0)
	for _, class := range classes {
		differences = append(differences, differencesFor(class, currentItems[class], candidateItems[class])...)
	}
	return Result{Preserved: len(differences) == 0, Differences: differences}, nil
}

func differencesFor(class Class, current, candidate map[string]int) []Difference {
	items := make(map[string]bool, len(current)+len(candidate))
	for item := range current {
		items[item] = true
	}
	for item := range candidate {
		items[item] = true
	}

	lost, invented := make([]string, 0), make([]string, 0)
	for item := range items {
		if current[item] > candidate[item] {
			lost = append(lost, item)
		}
		if candidate[item] > current[item] {
			invented = append(invented, item)
		}
	}
	sort.Strings(lost)
	sort.Strings(invented)
	differences := make([]Difference, 0, len(lost)+len(invented))
	for _, item := range lost {
		differences = append(differences, Difference{Class: class, Item: item, Direction: DirectionLost})
	}
	for _, item := range invented {
		differences = append(differences, Difference{Class: class, Item: item, Direction: DirectionInvented})
	}
	return differences
}

func extract(doc *text.Document) map[Class]map[string]int {
	items := make(map[Class]map[string]int, len(classes))
	for _, class := range classes {
		items[class] = make(map[string]int)
	}
	for _, token := range doc.Tokens() {
		if token.Class == text.Number {
			items[ClassNumber][token.Text]++
		}
		if token.Lexical {
			folded := fold.String(token.Text)
			if negationSet[folded] {
				items[ClassNegation][folded]++
			}
			first, _ := utf8.DecodeRuneInString(token.Text)
			if unicode.IsUpper(first) && !functionWordSet[folded] {
				items[ClassEntity][token.Text]++
			}
		}
	}
	for _, url := range urls(string(doc.Raw())) {
		items[ClassURL][url]++
	}
	for _, quote := range quotes(string(doc.Raw())) {
		items[ClassQuote][quote]++
	}
	return items
}

func vocabularySet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[fold.String(word)] = true
	}
	return set
}

func urls(source string) []string {
	var found []string
	for offset := 0; offset < len(source); {
		start := nextURLStart(source, offset)
		if start < 0 {
			break
		}
		end := start
		for end < len(source) {
			r, size := utf8.DecodeRuneInString(source[end:])
			if unicode.IsSpace(r) {
				break
			}
			end += size
		}
		item := strings.TrimRight(source[start:end], ".,;:!?)]}\"'”’")
		if item != "" {
			found = append(found, item)
		}
		offset = end
	}
	return found
}

func nextURLStart(source string, offset int) int {
	best := -1
	for _, prefix := range []string{"http://", "https://", "www."} {
		if index := strings.Index(source[offset:], prefix); index >= 0 {
			index += offset
			if best < 0 || index < best {
				best = index
			}
		}
	}
	return best
}

func quotes(source string) []string {
	var found []string
	for offset := 0; offset < len(source); {
		r, size := utf8.DecodeRuneInString(source[offset:])
		var close rune
		switch r {
		case '"':
			close = '"'
		case '“':
			close = '”'
		default:
			offset += size
			continue
		}
		end := offset + size
		for end < len(source) {
			next, nextSize := utf8.DecodeRuneInString(source[end:])
			end += nextSize
			if next == close {
				found = append(found, source[offset:end])
				offset = end
				break
			}
		}
		if end >= len(source) {
			break
		}
	}
	return found
}
