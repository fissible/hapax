package workflow

import (
	"context"
	"fmt"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

// Publication carries evidence to record only after the assembled bytes become
// visible. StorePath is the store resolved by the rewrite workflow.
type Publication struct {
	StorePath, InvocationID string
	Paragraphs              []PublishedParagraph
}

type PublishedParagraph struct {
	NodeID, ParagraphHash string
}

func (o RewriteOutcome) Publication() Publication {
	p := o.publication
	p.Paragraphs = append([]PublishedParagraph(nil), p.Paragraphs...)
	return p
}

// WithPublication retains its own copy, just as NewRewriteOutcome does for bytes.
func (o RewriteOutcome) WithPublication(p Publication) RewriteOutcome {
	p.Paragraphs = append([]PublishedParagraph(nil), p.Paragraphs...)
	o.publication = p
	return o
}

// RecordPublication forwards the complete batch to the resolved store. The
// caller must have successfully published the corresponding document first.
func (r *Runner) RecordPublication(ctx context.Context, p Publication) error {
	if len(p.Paragraphs) == 0 {
		return nil
	}
	s, err := store.Open(p.StorePath)
	if err != nil {
		return err
	}
	defer s.Close()
	evidence := store.Publication{InvocationID: p.InvocationID, Paragraphs: make([]store.PublishedParagraph, len(p.Paragraphs))}
	for i, paragraph := range p.Paragraphs {
		evidence.Paragraphs[i] = store.PublishedParagraph{NodeID: paragraph.NodeID, ParagraphHash: paragraph.ParagraphHash}
	}
	return s.RecordPublication(ctx, evidence)
}

// publishedLeafHash refuses an unknown identity instead of hashing arbitrary
// bytes at a guessed span. Coordinates belong to the re-admitted final document.
func publishedLeafHash(assembled *text.Document, at text.Span) (string, error) {
	var leaves []*text.Node
	if assembled != nil {
		leaves = assembled.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	}
	return leafHashAt(assembled, leaves, at)
}

// leafHashAt uses included leaves resolved from assembled so publication of
// multiple replacements needs only one structure pass over the final document.
func leafHashAt(assembled *text.Document, leaves []*text.Node, at text.Span) (string, error) {
	for _, leaf := range leaves {
		if leaf.Span == at {
			return identity.HashBytes(assembled.Raw()[at.Offset : at.Offset+at.Length]), nil
		}
	}
	return "", fmt.Errorf("published span %d+%d is not an included leaf", at.Offset, at.Length)
}
