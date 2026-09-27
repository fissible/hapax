// Package assemble splices accepted paragraph replacements into a document's
// original bytes.
package assemble

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/fissible/hapax/internal/text"
)

var (
	// ErrMissingInput reports a required document that was not provided.
	ErrMissingInput = errors.New("assemble missing input")
	// ErrNotALeaf reports a span that is not exactly an included leaf.
	ErrNotALeaf = errors.New("assemble span is not an included leaf")
	// ErrUnordered reports replacements not supplied in source order.
	ErrUnordered = errors.New("assemble replacements are unordered")
	// ErrOverlap reports replacements that name overlapping source bytes.
	ErrOverlap = errors.New("assemble replacements overlap")
	// ErrExcision reports a leaf whose raw span contains omitted source bytes.
	ErrExcision = errors.New("assemble leaf contains excisions")
	// ErrInvalidText reports an empty or invalid UTF-8 replacement.
	ErrInvalidText = errors.New("assemble invalid replacement text")
)

// Replacement substitutes Text for one included leaf's raw byte Span.
type Replacement struct {
	Span text.Span
	Text string
}

// Assemble returns the original document bytes with replacements spliced in.
// Replacements must name included leaves exactly, in source order. Any refusal
// returns nil so callers cannot mistake a partial rewrite for usable output.
func Assemble(doc *text.Document, replacements []Replacement) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("%w: document", ErrMissingInput)
	}

	if err := validate(doc, replacements); err != nil {
		return nil, err
	}

	raw := doc.Raw()
	output := make([]byte, 0, assembledLength(raw, replacements, doc.HadBOM()))
	if doc.HadBOM() {
		output = append(output, 0xef, 0xbb, 0xbf)
	}
	at := 0
	for _, replacement := range replacements {
		output = append(output, raw[at:replacement.Span.Offset]...)
		output = append(output, replacement.Text...)
		at = replacement.Span.Offset + replacement.Span.Length
	}
	output = append(output, raw[at:]...)
	return output, nil
}

func validate(doc *text.Document, replacements []Replacement) error {
	if len(replacements) == 0 {
		return nil
	}

	for index, replacement := range replacements {
		if replacement.Text == "" || !utf8.ValidString(replacement.Text) {
			return fmt.Errorf("%w: replacement %d", ErrInvalidText, index)
		}
		if index == 0 {
			continue
		}

		previous := replacements[index-1].Span
		if replacement.Span.Offset < previous.Offset {
			return fmt.Errorf("%w: replacement %d starts before replacement %d", ErrUnordered, index, index-1)
		}
		if replacement.Span.Offset < previous.Offset+previous.Length {
			return fmt.Errorf("%w: replacements %d and %d", ErrOverlap, index-1, index)
		}
	}

	leaves := make(map[text.Span]*text.Node)
	for _, leaf := range doc.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		leaves[leaf.Span] = leaf
	}
	for index, replacement := range replacements {
		leaf, ok := leaves[replacement.Span]
		if !ok {
			return fmt.Errorf("%w: replacement %d", ErrNotALeaf, index)
		}
		if len(leaf.Excisions) != 0 {
			return fmt.Errorf("%w: replacement %d", ErrExcision, index)
		}
	}

	return nil
}

func assembledLength(raw []byte, replacements []Replacement, hadBOM bool) int {
	length := len(raw)
	for _, replacement := range replacements {
		length += len(replacement.Text) - replacement.Span.Length
	}
	if hadBOM {
		length += 3
	}
	return length
}
