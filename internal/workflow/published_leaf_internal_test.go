package workflow

// The one step of #132's identity that can fail, isolated so its failure is
// testable.
//
// # Contract
//
// `publishedLeafHash(assembled, at)` returns the hash of the included leaf whose
// span is EXACTLY `at`, and an error when there is no such leaf. The caller owns
// the arithmetic that produces `at` — the original offset, plus every earlier
// replacement's length delta, plus the candidate's own leading whitespace, with
// the length of its trimmed text — and this owns only "find that leaf or fail".
//
// Splitting it that way is what makes the failure reachable in a test. The
// arithmetic is exercised end to end by
// `TestTheSecondTargetsHashAccountsForTheFirstReplacementsShift`, which asserts
// each target's exact leaf hash and its exact offset in the published document.
//
// # Why it must fail rather than guess
//
// `executionGate.SpliceableIntoOriginal` already checks that each candidate lands
// as one leaf in the ORIGINAL document, but the published document carries every
// target's replacement and offsets shift. A per-candidate verdict does not
// establish the combined mapping, so if the leaf is not where the arithmetic says
// it is, the identity is unknown — and an unknown identity recorded as a
// publication is worse than a failed run, because the screens would then hold a
// hash of bytes nobody published.
//
// # Unresolved
//
// Nothing here forces `Execute` to route through this function. The count
// invariant in `publication_test.go`
// (`TestEveryChangedTargetHasExactlyOnePublicationEntry`) is what pins that every
// changed target gets exactly one entry, and I could not construct a candidate
// that passes the per-candidate splice gate and then fails to map in the combined
// document — adjacent replacements are separated by the original blank line, and a
// candidate carrying its own blank line is already refused as two leaves. Whether
// such a candidate exists is unmeasured.

import (
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
)

// A span that is exactly an included leaf hashes that leaf's bytes.
func TestThePublishedLeafHashIsTheLeafsOwnBytes(t *testing.T) {
	const body = "First paragraph, long enough to be read as prose.\n\n" +
		"Second paragraph, also long enough to be read as prose.\n"
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) != 2 {
		t.Fatalf("the fixture admitted %d leaves; this test needs two", len(leaves))
	}

	for i, leaf := range leaves {
		got, err := publishedLeafHash(doc, leaf.Span)
		if err != nil {
			t.Fatalf("leaf %d: publishedLeafHash: %v", i, err)
		}
		span := doc.Raw()[leaf.Span.Offset : leaf.Span.Offset+leaf.Span.Length]
		if want := identity.HashBytes(span); got != want {
			t.Errorf("leaf %d hashed %.12s, want %.12s — the hash is over bytes other "+
				"than the leaf's own span", i, got, want)
		}
	}
}

// A span that is not a leaf is an error, not a hash of whatever is there.
//
// Four ways to miss, because each is a different arithmetic mistake and three of
// them read real bytes: an offset shifted by a few, a length that is short, a
// span covering both paragraphs, and one past the end of the document. A function
// that hashed `raw[at]` without checking would answer all four.
func TestAPublishedLeafThatIsNotThereIsAnError(t *testing.T) {
	const body = "First paragraph, long enough to be read as prose.\n\n" +
		"Second paragraph, also long enough to be read as prose.\n"
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) != 2 {
		t.Fatalf("the fixture admitted %d leaves; this test needs two", len(leaves))
	}
	first, second := leaves[0].Span, leaves[1].Span

	for _, c := range []struct {
		name string
		at   text.Span
	}{
		{"an offset a few bytes early", text.Span{Offset: first.Offset + 3, Length: first.Length}},
		{"a length a few bytes short", text.Span{Offset: first.Offset, Length: first.Length - 3}},
		{
			"a span covering both paragraphs",
			text.Span{Offset: first.Offset, Length: second.Offset + second.Length - first.Offset},
		},
		{"past the end of the document", text.Span{Offset: len(doc.Raw()) + 1, Length: 4}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := publishedLeafHash(doc, c.at)
			if err == nil {
				t.Fatalf("a span that is not an included leaf hashed to %.12s; an "+
					"identity nobody published would be recorded as one", got)
			}
			if got != "" {
				t.Errorf("the failure returned %q as well as an error; a caller that "+
					"checks only one of them records a hash of the wrong bytes", got)
			}
			// The error names the span, because the arithmetic that produced it is
			// in the caller and "no such leaf" alone cannot be debugged.
			if !strings.Contains(err.Error(), "leaf") {
				t.Errorf("the error does not say what went wrong: %v", err)
			}
		})
	}
}

// An excluded leaf is not a published paragraph either.
//
// A fenced code block is a leaf the structure pass excludes, so its span exists
// and is not something this tool publishes. An implementation scanning all leaves
// rather than the included ones would hash it.
func TestAnExcludedLeafIsNotAPublishedParagraph(t *testing.T) {
	const body = "First paragraph, long enough to be read as prose.\n\n" +
		"```\nnot prose\n```\n"
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	structure := doc.Structure(text.DefaultStructureOptions())
	var excluded *text.Node
	for _, leaf := range structure.Leaves() {
		if !leaf.Included {
			excluded = leaf
			break
		}
	}
	if excluded == nil {
		t.Skip("the fixture admitted no excluded leaf, so there is nothing to confuse")
	}

	if got, err := publishedLeafHash(doc, excluded.Span); err == nil {
		t.Errorf("an EXCLUDED leaf hashed to %.12s; only included leaves are paragraphs "+
			"this tool publishes", got)
	}
}
