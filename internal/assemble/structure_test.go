package assemble_test

// #115. `Assemble` validates what it is GIVEN and never what it PRODUCES.
//
// `validate` checks each replacement against the input document: the span must
// be an included leaf exactly, with no excisions, in order, non-overlapping. It
// says nothing about what the replacement text becomes once spliced and
// re-parsed, so a candidate can change the document's STRUCTURE while the caller
// is told its prose improved.
//
// # Three of the five shapes originally filed are already refused
//
// The loop scores a candidate in isolation before any gate runs, so a candidate
// admitting to zero scoreable segments is refused as `not-one-segment` long
// before assembly. Measured end to end through the Runner:
//
//	list prefix  improved=1 rejections=[]                published=true
//	blank line   improved=1 rejections=[]                published=true
//	blockquote   improved=0 rejections=[not-one-segment] published=false
//
// A `> ` prefix, a `## ` prefix and a below-floor candidate all admit to zero
// segments and never reach `Assemble`. The issue was filed claiming otherwise,
// from a measurement of the splice alone.
//
// # What the two survivors actually do
//
// Both are still paragraphs, so "the paragraph stops being a paragraph" is the
// wrong description. Measured structure of each candidate alone:
//
//	plain       leaf 0 role=paragraph containers=[document]                span=0+106
//	list item   leaf 0 role=paragraph containers=[document list list-item] span=2+112
//	blank line  leaf 0 role=paragraph containers=[document]                span=0+27
//	            leaf 1 role=paragraph containers=[document]                span=29+101
//
// The `- ` prefix moves the paragraph INSIDE A LIST and shifts its span past the
// marker. The blank line SPLITS one paragraph into two — and only one half
// clears the lexical floor, so the document's admitted count is unchanged while
// its included count rises. The short half is unscoreable and invisible to every
// future run: the tool silently orphaned text it was asked to improve.
//
// # The check
//
// After splicing, the replaced span must correspond to exactly one included leaf,
// occupying exactly that span, with the same containers as the leaf it replaced.
//
// All three conditions are load-bearing and each survivor fails a different one:
// the list case fails span identity AND containers, the blank-line case fails
// leaf count. A plain rewrite satisfies all three exactly. Derivable, and no
// constant — this is a statement about the document, not a threshold.
//
// Role is deliberately NOT part of the check: both survivors keep
// `role=paragraph`, so a role comparison would pass them both and read as
// coverage it does not provide.

import (
	"errors"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/assemble"
	"github.com/fissible/hapax/internal/text"
)

const (
	// Two paragraphs, both admitted, so a replacement of the first has a
	// neighbour that must survive untouched.
	firstParagraph = "A paragraph of ordinary prose that runs on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing."
	secondParagraph = "A second paragraph doing likewise, at enough length to clear the floor and " +
		"be measured on its own terms rather than skipped."
)

func twoParagraphs() string { return firstParagraph + "\n\n" + secondParagraph + "\n\n" }

// admitted parses a body and returns its included leaves, so a test can ask what
// the document became rather than trusting a count.
func admitted(t *testing.T, body []byte) []*text.Node {
	t.Helper()
	doc, err := text.Admit(body)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
}

// A replacement that re-parses as one paragraph in the same place is spliced.
//
// The admissible half, first, because a check that refused everything would
// satisfy every assertion below.
func TestAPlainReplacementIsSpliced(t *testing.T) {
	doc, err := text.Admit([]byte(twoParagraphs()))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	const plain = "A rewritten paragraph of ordinary prose that runs on past a single " +
		"sentence so the structure pass still reads it as prose here."

	got, err := assemble.Assemble(doc, []assemble.Replacement{
		{Span: spanOf(t, doc, firstParagraph), Text: plain},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(string(got), plain) {
		t.Error("the replacement is not in the output")
	}
	// And the document still has the same two paragraphs, in the same
	// containers, so the check is not merely counting.
	leaves := admitted(t, got)
	if len(leaves) != 2 {
		t.Fatalf("the output has %d included leaves, want 2", len(leaves))
	}
	for i, leaf := range leaves {
		if len(leaf.Containers) != 1 || leaf.Containers[0] != text.ContainerDocument {
			t.Errorf("leaf %d is in %v, want [document]", i, leaf.Containers)
		}
	}
}

// A replacement that splits into two paragraphs is refused.
//
// Measured: the candidate scores as ONE segment, because only one of its halves
// clears the lexical floor, so the loop accepts it and hands it here. Spliced, it
// becomes two included leaves and the short half is permanently unscoreable.
func TestAReplacementThatSplitsIntoTwoParagraphsIsRefused(t *testing.T) {
	doc, err := text.Admit([]byte(twoParagraphs()))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	const split = "A rewritten paragraph here.\n\nAnd a second half that runs on past a " +
		"single sentence so the structure pass reads it as prose in its own right."

	got, err := assemble.Assemble(doc, []assemble.Replacement{
		{Span: spanOf(t, doc, firstParagraph), Text: split},
	})

	if !errors.Is(err, assemble.ErrNotOneLeaf) {
		t.Errorf("err = %v, want %v", err, assemble.ErrNotOneLeaf)
	}
	// Nil output, so a caller cannot mistake a structural change for usable
	// bytes — the same rule every other refusal here follows.
	if got != nil {
		t.Errorf("a refusal returned %d bytes", len(got))
	}
}

// A replacement that lands inside a list is refused.
//
// The subtler survivor: it keeps `role=paragraph` and produces exactly one
// included leaf, so neither a role check nor a leaf count catches it. What
// changes is the CONTAINERS — `[document list list-item]` — and the span, which
// starts two bytes later because the leaf excludes the marker.
func TestAReplacementThatBecomesAListItemIsRefused(t *testing.T) {
	doc, err := text.Admit([]byte(twoParagraphs()))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	const listed = "- A rewritten paragraph of ordinary prose that runs on past a single " +
		"sentence so the structure pass still reads it as prose here."

	// The premise, asserted so the fixture cannot quietly stop being one: alone,
	// this text is a single paragraph leaf, inside a list, offset past the marker.
	alone := admitted(t, []byte(listed))
	if len(alone) != 1 {
		t.Fatalf("the fixture admits %d leaves alone, want 1 — it must NOT be caught "+
			"by a leaf count", len(alone))
	}
	if alone[0].Role != text.RoleParagraph {
		t.Fatalf("the fixture's role is %q, want paragraph — it must NOT be caught by "+
			"a role check", alone[0].Role)
	}
	if len(alone[0].Containers) < 2 {
		t.Fatalf("the fixture is in %v; it must be nested for this test to mean "+
			"anything", alone[0].Containers)
	}

	got, err := assemble.Assemble(doc, []assemble.Replacement{
		{Span: spanOf(t, doc, firstParagraph), Text: listed},
	})

	if !errors.Is(err, assemble.ErrNotOneLeaf) {
		t.Errorf("err = %v, want %v", err, assemble.ErrNotOneLeaf)
	}
	if got != nil {
		t.Errorf("a refusal returned %d bytes", len(got))
	}
}

// Every replacement is checked, not only the first.
//
// A check that examined `replacements[0]` and stopped would pass both refusals
// above. The admissible replacement is first here so the loop has to reach the
// second.
func TestEveryReplacementIsCheckedNotOnlyTheFirst(t *testing.T) {
	doc, err := text.Admit([]byte(twoParagraphs()))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	const plain = "A rewritten paragraph of ordinary prose that runs on past a single " +
		"sentence so the structure pass still reads it as prose here."
	const listed = "- A second rewritten paragraph that runs on past a single sentence so " +
		"the structure pass still reads it as prose in a list."

	got, err := assemble.Assemble(doc, []assemble.Replacement{
		{Span: spanOf(t, doc, firstParagraph), Text: plain},
		{Span: spanOf(t, doc, secondParagraph), Text: listed},
	})

	if !errors.Is(err, assemble.ErrNotOneLeaf) {
		t.Errorf("err = %v, want %v — the SECOND replacement is the bad one", err,
			assemble.ErrNotOneLeaf)
	}
	if got != nil {
		t.Errorf("a refusal returned %d bytes", len(got))
	}
}

// Two admissible replacements at once, so the offset arithmetic is exercised.
//
// The second replacement's position in the OUTPUT is not its position in the
// input: it shifts by the length delta of every earlier replacement. A check
// that re-parsed the output but looked for the input's spans would pass the
// single-replacement cases and fail here — or worse, pass here by accident when
// the deltas happen to cancel, which is why the two replacements deliberately
// differ in length from what they replace.
func TestTwoAdmissibleReplacementsAreBothChecked(t *testing.T) {
	doc, err := text.Admit([]byte(twoParagraphs()))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	// Shorter than the first paragraph, so everything after it shifts left.
	const shorter = "A rewritten paragraph of ordinary prose that runs past one sentence " +
		"so the pass reads it as prose."
	// Longer than the second, so the delta does not cancel.
	const longer = "A second rewritten paragraph doing likewise, at more than enough length " +
		"to clear the floor and be measured on its own terms rather than being skipped here."
	if len(shorter) >= len(firstParagraph) || len(longer) <= len(secondParagraph) {
		t.Fatalf("the fixtures must change length in OPPOSITE directions or the "+
			"offset arithmetic is not under test: %d vs %d, %d vs %d",
			len(shorter), len(firstParagraph), len(longer), len(secondParagraph))
	}

	got, err := assemble.Assemble(doc, []assemble.Replacement{
		{Span: spanOf(t, doc, firstParagraph), Text: shorter},
		{Span: spanOf(t, doc, secondParagraph), Text: longer},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	leaves := admitted(t, got)
	if len(leaves) != 2 {
		t.Fatalf("the output has %d included leaves, want 2", len(leaves))
	}
	if !strings.Contains(string(got), shorter) || !strings.Contains(string(got), longer) {
		t.Error("both replacements should be in the output")
	}
}

// A document with no replacements is returned unchanged and unchecked.
//
// `Assemble(doc, nil)` is how the workflow emits an unmodified draft, so the new
// check must not run over a document it did not change — a corpus file that
// already contains a list would otherwise be refused on the way out.
func TestADocumentWithNoReplacementsIsNotStructurallyChecked(t *testing.T) {
	const withList = "A paragraph of ordinary prose that runs on past a single sentence so " +
		"the structure pass reads it as prose here.\n\n" +
		"- A list item that also runs on past a single sentence so the pass reads it as " +
		"prose inside a list.\n\n"
	doc, err := text.Admit([]byte(withList))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}

	got, err := assemble.Assemble(doc, nil)
	if err != nil {
		t.Fatalf("Assemble with no replacements: %v", err)
	}
	if string(got) != withList {
		t.Error("a document with no replacements came back changed")
	}
}

// The refusal is a declared error, spelled so a caller can match it.
func TestErrNotOneLeafIsDeclared(t *testing.T) {
	if assemble.ErrNotOneLeaf == nil {
		t.Fatal("ErrNotOneLeaf is nil")
	}
	// The message names what went wrong rather than restating the package.
	if got := assemble.ErrNotOneLeaf.Error(); !strings.Contains(got, "assemble") ||
		!strings.Contains(got, "leaf") {
		t.Errorf("ErrNotOneLeaf = %q; it should name the package and the condition", got)
	}
	// Distinct from the error that checks the INPUT's leaves, because the two
	// mean different things to a caller: one is a bad request, the other is a
	// candidate that would damage the document.
	if errors.Is(assemble.ErrNotOneLeaf, assemble.ErrNotALeaf) ||
		errors.Is(assemble.ErrNotALeaf, assemble.ErrNotOneLeaf) {
		t.Error("ErrNotOneLeaf and ErrNotALeaf match each other; a bad span and a " +
			"damaging replacement are different failures")
	}
}
