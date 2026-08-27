package text_test

// Slice 2d — the structural tree.
//
// DESIGN Section 3 replaced an earlier flat segment-class model with a tree,
// because authored prose appears inside list items, footnotes, captions and
// definition descriptions, and because Markdown structures nest. The tree
// distinguishes two things:
//
//   - Containers — list, list item, table, footnote section, block quote,
//     definition list. Structure only; never a feature-bearing unit.
//   - Leaf text runs — each carrying a role, its own admission verdict, and a
//     machine-readable reason when excluded.
//
// # API decisions this suite pins that DESIGN Section 3 does NOT mandate
//
// Recorded here so the freeze is honest about which assertions are the design
// speaking and which are this package choosing:
//
//  1. The tree always has a ContainerDocument root, even for an empty document.
//  2. A container's span encloses every descendant. DESIGN keeps non-included
//     leaves "for spans, for rehydration"; rehydrating a whole container needs
//     this, but Section 3 never says it.
//  3. The front-matter leaf spans the body BETWEEN the fences, not the fences.
//  4. StructureVersion is distinct from ContractVersion, so a change to the
//     inclusion policy or the sententiality rule does not masquerade as a
//     tokenization change.
//  5. A leaf records the EVIDENCE for its verdict (Words, EndsTerminal) and not
//     only the verdict, which is what makes Reclassify possible without the
//     source bytes.
//  6. Node's field schema is frozen exactly, so no leaf field can hold a
//     reference to the Document — see TestNodeSchemaIsFrozen, which also
//     records the part of "without reparsing" no test can establish.
//  7. An HTML block yields leaves; there is no figure container.
//  8. The root's span is exactly the whole document, [0, len(Raw())).
//  9. NotExcluded is the zero value of ExclusionReason, so a container — which
//     has no verdict at all — carries it.
//  10. A container with no leaf descendant is not emitted. Structure that
//      encloses nothing records nothing, and permitting it lets an
//      implementation scatter empty containers through an otherwise valid tree.
//
// Parsing over the RAW admitted bytes rather than the NFC form is NOT in this
// list. It was a claimed API decision here until the slice-2d test review
// pointed out it contradicted Section 3, which required a normalized-to-raw
// offset map. Section 3 was wrong and has been amended; see REVIEW.md Round 4.
// It is now design, not a local choice.
//
// # Fixture validity
//
// Two fixtures below are deliberately outside CommonMark and GFM:
//
//   - The definition-list syntax ("Term\n: description") is neither CommonMark
//     nor GFM. It parses only with goldmark's separate definition-list
//     extension, which the implementation must enable explicitly.
//   - <figure>/<figcaption> is valid CommonMark only as an opaque raw HTML
//     block. No parser exposes it as a semantic figure, so caption extraction
//     is post-processing over the HTML block's bytes, not an extension.
//
// # The sententiality rule is declared, not derived
//
// DESIGN says whether a list item is prose is decided per item by sentential
// structure. Deciding that properly needs a finite-verb test, which needs POS
// tagging, which ADR 0001 rules out. v1 therefore uses a declared heuristic and
// — following Section 3's own treatment of sentence segmentation — measures its
// error rate against a hand-annotated fixture and publishes it. See
// TestSententialityRuleErrorRateIsPublished.

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/fissible/hapax/internal/text"
)

// mustAdmit lives in span_test.go.

// structure parses and ALWAYS applies the whole-tree invariant. Calling
// checkTree by hand at each site drifts; here it cannot.
func structure(t *testing.T, src string, opts text.StructureOptions) (*text.Document, *text.Node) {
	t.Helper()
	doc := mustAdmit(t, src)
	root := doc.Structure(opts)
	checkTree(t, doc, root, opts)
	return doc, root
}

func leafRuns(t *testing.T, root *text.Node) []*text.Node {
	t.Helper()
	if root.Kind != text.KindContainer {
		t.Fatalf("root Kind = %v, want KindContainer", root.Kind)
	}
	return root.Leaves()
}

func runText(t *testing.T, doc *text.Document, leaf *text.Node) string {
	t.Helper()
	got, err := doc.RunText(leaf)
	if err != nil {
		t.Fatalf("RunText(%+v) returned error: %v", leaf.Span, err)
	}
	return got
}

func resolve(t *testing.T, doc *text.Document, leaf *text.Node) string {
	t.Helper()
	got, err := doc.Resolve(leaf.Span)
	if err != nil {
		t.Fatalf("Resolve(%+v) returned error: %v", leaf.Span, err)
	}
	return got
}

func findRole(root *text.Node, role text.Role) []*text.Node {
	var out []*text.Node
	for _, l := range root.Leaves() {
		if l.Role == role {
			out = append(out, l)
		}
	}
	return out
}

func onlyRole(t *testing.T, root *text.Node, role text.Role) *text.Node {
	t.Helper()
	got := findRole(root, role)
	if len(got) != 1 {
		t.Fatalf("found %d leaves with role %q, want exactly 1", len(got), role)
	}
	return got[0]
}

func end(s text.Span) int { return s.Offset + s.Length }

var knownContainers = map[text.ContainerKind]bool{
	text.ContainerDocument:        true,
	text.ContainerBlockQuote:      true,
	text.ContainerList:            true,
	text.ContainerListItem:        true,
	text.ContainerTable:           true,
	text.ContainerFootnoteSection: true,
	text.ContainerFootnote:        true,
	text.ContainerDefinitionList:  true,
}

var knownRoles = map[text.Role]bool{
	text.RoleParagraph:             true,
	text.RoleHeading:               true,
	text.RoleCodeBlock:             true,
	text.RoleFrontMatter:           true,
	text.RoleTableCell:             true,
	text.RoleFootnote:              true,
	text.RoleCaption:               true,
	text.RoleImage:                 true,
	text.RoleHTMLBlock:             true,
	text.RoleDefinitionTerm:        true,
	text.RoleDefinitionDescription: true,
}

// ---------------------------------------------------------------------------
// Invariants that must hold on every leaf of every fixture
// ---------------------------------------------------------------------------

// checkTree asserts the contract that does not depend on which document is
// being parsed. Running it over every fixture is what stops an implementation
// from satisfying one test's letter while breaking the model.
func checkTree(t *testing.T, doc *text.Document, root *text.Node, opts text.StructureOptions) {
	t.Helper()

	// The zero value means the declared defaults, so the invariant must judge
	// it the same way the implementation does — otherwise this check rejects a
	// correct tree in TestZeroOptionsAreTheDeclaredDefaults.
	if opts == (text.StructureOptions{}) {
		opts = text.DefaultStructureOptions()
	}

	// Walking the real Children edges is what makes Containers falsifiable: a
	// leaf's self-reported path must equal the chain of container nodes that
	// actually encloses it. Without this, a flat root full of leaves with
	// fabricated paths satisfies every nesting test in the file.
	var walked []*text.Node
	var walk func(n *text.Node, path []text.ContainerKind)
	walk = func(n *text.Node, path []text.ContainerKind) {
		switch n.Kind {
		case text.KindContainer:
			if !knownContainers[n.Container] {
				t.Errorf("container node at %+v has unknown Container %q", n.Span, n.Container)
			}
			if n.Role != "" {
				t.Errorf("container %q carries Role %q; containers are structure, never feature-bearing", n.Container, n.Role)
			}
			if n.Included || n.Excisions != nil || n.Words != 0 || n.Sentential || n.EndsTerminal || n.Exclusion != text.NotExcluded {
				t.Errorf("container %q carries leaf verdict state: Included=%v Words=%d Sentential=%v EndsTerminal=%v Exclusion=%q",
					n.Container, n.Included, n.Words, n.Sentential, n.EndsTerminal, n.Exclusion)
			}
			if n.Containers != nil {
				t.Errorf("container %q carries a Containers path; that is a leaf field", n.Container)
			}
			path = append(append([]text.ContainerKind{}, path...), n.Container)
		case text.KindLeaf:
			walked = append(walked, n)
			if len(n.Children) != 0 {
				t.Errorf("leaf %q at %+v has %d children", n.Role, n.Span, len(n.Children))
			}
			if n.Container != "" {
				t.Errorf("leaf %q carries Container %q", n.Role, n.Container)
			}
			if !knownRoles[n.Role] {
				t.Errorf("leaf at %+v has unknown Role %q", n.Span, n.Role)
			}
			if !reflect.DeepEqual(n.Containers, path) {
				t.Errorf("leaf %q at %+v reports container path %v, but the tree encloses it in %v", n.Role, n.Span, n.Containers, path)
			}
		default:
			t.Errorf("node at %+v has invalid Kind %v", n.Span, n.Kind)
		}
		// Every node's span — container as well as leaf — must be in bounds and
		// grapheme-aligned. Enclosing its children is not enough on its own.
		if _, err := doc.Resolve(n.Span); err != nil {
			t.Errorf("node %q%q span %+v does not resolve: %v", n.Container, n.Role, n.Span, err)
		}
		// Siblings are ordered and disjoint. Containment alone lets every
		// container claim the whole document while the leaves stay correct, so
		// the tree stops representing the actual Markdown structure.
		siblingEnd := n.Span.Offset
		for _, c := range n.Children {
			if c.Span.Offset < n.Span.Offset || end(c.Span) > end(n.Span) {
				t.Errorf("child %+v escapes parent %+v", c.Span, n.Span)
			}
			if c.Span.Offset < siblingEnd {
				t.Errorf("child %+v overlaps or precedes its previous sibling ending at %d, under parent %+v", c.Span, siblingEnd, n.Span)
			}
			siblingEnd = end(c.Span)
			walk(c, path)
		}
	}

	// A container with no leaf beneath it carries no information — it is pure
	// structure enclosing nothing — so it is not emitted at all. Without this,
	// an implementation can scatter arbitrary empty containers through the tree
	// and satisfy everything else.
	var leafDescendants func(n *text.Node) int
	leafDescendants = func(n *text.Node) int {
		if n.Kind == text.KindLeaf {
			return 1
		}
		total := 0
		for _, c := range n.Children {
			total += leafDescendants(c)
		}
		if n != root && n.Kind == text.KindContainer {
			if total == 0 {
				t.Errorf("container %q at %+v encloses no leaf; empty containers are not emitted", n.Container, n.Span)
			}
			if n.Span.Length == 0 {
				t.Errorf("container %q has an empty span %+v", n.Container, n.Span)
			}
		}
		return total
	}
	leafDescendants(root)

	if root.Kind != text.KindContainer || root.Container != text.ContainerDocument {
		t.Fatalf("root Kind=%v Container=%q, want KindContainer/%q", root.Kind, root.Container, text.ContainerDocument)
	}
	if want := (text.Span{Offset: 0, Length: len(doc.Raw())}); root.Span != want {
		t.Errorf("root span = %+v, want the whole document %+v", root.Span, want)
	}
	walk(root, nil)

	// Leaves() must be the leaves of the real tree, in document order — not a
	// separately maintained list that could drift from it.
	if !reflect.DeepEqual(leafRuns(t, root), walked) {
		t.Errorf("Leaves() returned %d nodes; walking Children found %d", len(root.Leaves()), len(walked))
	}

	prevEnd := -1
	for i, l := range leafRuns(t, root) {
		if l.Kind != text.KindLeaf {
			t.Errorf("Leaves() returned a node with Kind %v at %+v", l.Kind, l.Span)
			continue
		}
		if l.Span.Length <= 0 {
			t.Errorf("leaf %d (%s) has empty span %+v", i, l.Role, l.Span)
		}
		if l.Span.Offset < prevEnd {
			t.Errorf("leaf %d (%s) at %+v overlaps the previous leaf ending at %d", i, l.Role, l.Span, prevEnd)
		}
		prevEnd = end(l.Span)

		resolve(t, doc, l) // resolvable, therefore grapheme-aligned

		// Excisions: non-empty, ordered, disjoint, contained, resolvable.
		cursor := l.Span.Offset
		for j, e := range l.Excisions {
			if e.Length <= 0 {
				t.Errorf("leaf %d excision %d is empty: %+v", i, j, e)
			}
			if e.Offset < cursor || end(e) > end(l.Span) {
				t.Errorf("leaf %d excision %d %+v is out of order or escapes %+v", i, j, e, l.Span)
			}
			if _, err := doc.Resolve(e); err != nil {
				t.Errorf("leaf %d excision %d %+v does not resolve: %v", i, j, e, err)
			}
			cursor = end(e)
		}

		// RunText is exactly the leaf's raw bytes minus the recorded excisions,
		// NFC-normalized. Without this, RunText could be computed by some other
		// route and the published Excisions could be decorative.
		var b strings.Builder
		at := l.Span.Offset
		for _, e := range l.Excisions {
			b.Write(doc.Raw()[at:e.Offset])
			at = end(e)
		}
		b.Write(doc.Raw()[at:end(l.Span)])
		if want := norm.NFC.String(b.String()); runText(t, doc, l) != want {
			t.Errorf("leaf %d (%s): RunText = %q, want raw-minus-excisions %q", i, l.Role, runText(t, doc, l), want)
		}

		// The verdict is a pure function of the recorded evidence and the
		// options. This is what makes Reclassify sound.
		// Zero words is never sentential: a run with nothing left after
		// excision carries no authored prose to measure.
		wantSentential := l.Words > 0 &&
			((l.EndsTerminal && l.Words >= opts.MinSententialWords) || l.Words >= opts.MinUnpunctuatedWords)
		if l.Sentential != wantSentential {
			t.Errorf("leaf %d (%s) %q: Sentential = %v, but Words=%d EndsTerminal=%v under %+v implies %v",
				i, l.Role, runText(t, doc, l), l.Sentential, l.Words, l.EndsTerminal, opts, wantSentential)
		}

		if l.Included != (l.Exclusion == text.NotExcluded) {
			t.Errorf("leaf %d (%s): Included=%v but Exclusion=%q", i, l.Role, l.Included, l.Exclusion)
		}

		// Precedence: role first, then the zero-word and sententiality rule,
		// then the block-quote policy. So a run with nothing left after excision
		// is excluded wherever it sits, and only a role exclusion outranks that.
		if l.Words == 0 && l.Exclusion != text.ExcludedByRole && l.Exclusion != text.ExcludedNotSentential {
			t.Errorf("leaf %d (%s) has no words left but Exclusion=%q, want %q", i, l.Role, l.Exclusion, text.ExcludedNotSentential)
		}
	}
}

// ---------------------------------------------------------------------------
// Provenance and options
// ---------------------------------------------------------------------------

func TestStructureVersionIsDeclared(t *testing.T) {
	if text.StructureVersion == "" {
		t.Fatal("StructureVersion is empty; the structure contract must declare provenance")
	}
	if text.StructureVersion == text.ContractVersion {
		t.Errorf("StructureVersion = ContractVersion = %q; they must version independently", text.StructureVersion)
	}
}

// The zero value must mean the declared defaults, not "every threshold is
// zero". StructureOptions{} with a zero MinSententialWords would make every
// run with terminal punctuation sentential, including a bare "Redis.".
func TestZeroOptionsAreTheDeclaredDefaults(t *testing.T) {
	const src = "- Redis.\n- The proxy terminates TLS before the request reaches the app.\n"

	_, zero := structure(t, src, text.StructureOptions{})
	_, explicit := structure(t, src, text.DefaultStructureOptions())

	if !reflect.DeepEqual(zero, explicit) {
		t.Error("StructureOptions{} did not produce the same tree as DefaultStructureOptions()")
	}
	runs := findRole(zero, text.RoleParagraph)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].Included {
		t.Error(`"Redis." was admitted under the zero-value options; the word minimum was not applied`)
	}
}

func TestDefaultsAreTheDesignDefaults(t *testing.T) {
	opts := text.DefaultStructureOptions()
	if opts.IncludeBlockQuotes {
		t.Error("IncludeBlockQuotes defaults to true; DESIGN says block quote content is configurable, default no")
	}
	if opts.MinSententialWords != 4 {
		t.Errorf("MinSententialWords = %d, want the declared default 4", opts.MinSententialWords)
	}
	if opts.MinUnpunctuatedWords != 8 {
		t.Errorf("MinUnpunctuatedWords = %d, want the declared default 8", opts.MinUnpunctuatedWords)
	}
}

// ---------------------------------------------------------------------------
// Tree shape
// ---------------------------------------------------------------------------

// The case the flat model could not represent: a quote containing a list
// containing prose. Asserted through the leaf's container path rather than by
// walking single-child links, since the nesting is the behavior and the arity
// of each level is not.
func TestContainersNest(t *testing.T) {
	const src = "> - A quoted list item that is a whole sentence.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	if root.Container != text.ContainerDocument {
		t.Fatalf("root Container = %q, want %q", root.Container, text.ContainerDocument)
	}

	leaf := onlyRole(t, root, text.RoleParagraph)
	want := []text.ContainerKind{
		text.ContainerDocument,
		text.ContainerBlockQuote,
		text.ContainerList,
		text.ContainerListItem,
	}
	if !reflect.DeepEqual(leaf.Containers, want) {
		t.Errorf("leaf container path = %v, want %v", leaf.Containers, want)
	}
	if got := runText(t, doc, leaf); got != "A quoted list item that is a whole sentence." {
		t.Errorf("leaf text = %q", got)
	}
}

// The inverse nesting: a list containing a block quote. DESIGN names both
// directions and only one of them was covered.
func TestListContainingBlockQuoteNestsTheOtherWay(t *testing.T) {
	const src = "- Introducing the quotation in a full sentence.\n\n  > Someone else said this first.\n"

	_, root := structure(t, src, text.DefaultStructureOptions())

	runs := findRole(root, text.RoleParagraph)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	quoted := runs[1]
	want := []text.ContainerKind{
		text.ContainerDocument,
		text.ContainerList,
		text.ContainerListItem,
		text.ContainerBlockQuote,
	}
	if !reflect.DeepEqual(quoted.Containers, want) {
		t.Errorf("quoted leaf container path = %v, want %v", quoted.Containers, want)
	}
	if quoted.Exclusion != text.ExcludedByBlockQuotePolicy {
		t.Errorf("quote inside a list: Exclusion = %q, want %q", quoted.Exclusion, text.ExcludedByBlockQuotePolicy)
	}
}

func TestFootnoteAndDefinitionListContainersExist(t *testing.T) {
	const src = "A claim that needs support.[^1]\n\n" +
		"[^1]: The support is published with the claim.\n\n" +
		"Term\n: A definition description that is a whole sentence.\n"

	_, root := structure(t, src, text.DefaultStructureOptions())

	// The exact path, not membership: membership alone admits an inverted
	// Document → Footnote → FootnoteSection tree.
	note := onlyRole(t, root, text.RoleFootnote)
	wantNote := []text.ContainerKind{text.ContainerDocument, text.ContainerFootnoteSection, text.ContainerFootnote}
	if !reflect.DeepEqual(note.Containers, wantNote) {
		t.Errorf("footnote leaf container path = %v, want %v", note.Containers, wantNote)
	}

	wantDef := []text.ContainerKind{text.ContainerDocument, text.ContainerDefinitionList}
	for _, role := range []text.Role{text.RoleDefinitionTerm, text.RoleDefinitionDescription} {
		leaf := onlyRole(t, root, role)
		if !reflect.DeepEqual(leaf.Containers, wantDef) {
			t.Errorf("%s container path = %v, want %v", role, leaf.Containers, wantDef)
		}
	}
}

func TestStructureIsDeterministic(t *testing.T) {
	doc := mustAdmit(t, kitchenSink)
	a := doc.Structure(text.DefaultStructureOptions())
	b := doc.Structure(text.DefaultStructureOptions())
	checkTree(t, doc, a, text.DefaultStructureOptions())
	checkTree(t, doc, b, text.DefaultStructureOptions())
	if !reflect.DeepEqual(a, b) {
		t.Error("two parses of the same bytes produced different trees")
	}
}

func TestEmptyDocumentYieldsBareRoot(t *testing.T) {
	_, root := structure(t, "", text.DefaultStructureOptions())
	if root == nil {
		t.Fatal("Structure returned nil for an empty document")
	}
	if root.Container != text.ContainerDocument {
		t.Errorf("root Container = %q, want %q", root.Container, text.ContainerDocument)
	}
	if runs := root.Leaves(); len(runs) != 0 {
		t.Errorf("empty document produced %d leaves", len(runs))
	}
	if root.Span.Length != 0 {
		t.Errorf("root span = %+v, want length 0", root.Span)
	}
}

// ---------------------------------------------------------------------------
// Spans are raw byte offsets
// ---------------------------------------------------------------------------

// Hand-computed rather than round-tripped: a round trip through Resolve would
// pass even if offsets were NFC positions consistently misapplied.
func TestLeafSpansAreRawByteOffsets(t *testing.T) {
	// '#'(0) ' '(1) 'T'(2) 'e'(3) 0xCC(4) 0x81(5) 't'(6) 'e'(7) '\r'(8) '\n'(9)
	// '\r'(10) '\n'(11) then the paragraph at 12. NFC would collapse bytes 3–5
	// to a two-byte precomposed rune, so a normalized offset differs here.
	const src = "# Te\u0301te\r\n\r\nBody sentence follows the heading here.\r\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	heading := onlyRole(t, root, text.RoleHeading)
	if got, want := heading.Span, (text.Span{Offset: 2, Length: 6}); got != want {
		t.Errorf("heading span = %+v, want %+v (raw bytes, marker and CRLF excluded)", got, want)
	}
	if got := resolve(t, doc, heading); got != "T\u00e9te" {
		t.Errorf("heading resolves to %q, want NFC %q", got, "T\u00e9te")
	}

	para := onlyRole(t, root, text.RoleParagraph)
	if got, want := para.Span, (text.Span{Offset: 12, Length: 39}); got != want {
		t.Errorf("paragraph span = %+v, want %+v", got, want)
	}
	if got := resolve(t, doc, para); got != "Body sentence follows the heading here." {
		t.Errorf("paragraph resolves to %q", got)
	}
}

func TestSoftBreaksKeepRawLineEndings(t *testing.T) {
	const src = "One line of the paragraph here.\r\nAnd its continuation line.\r\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)

	if got, want := resolve(t, doc, para), "One line of the paragraph here.\r\nAnd its continuation line."; got != want {
		t.Errorf("paragraph = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Inclusion policy — DESIGN Section 3's leaf-role table, row by row
// ---------------------------------------------------------------------------

func TestParagraphProseIsIncluded(t *testing.T) {
	_, root := structure(t, "The core case, a paragraph of prose.\n", text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)
	if want := []text.ContainerKind{text.ContainerDocument}; !reflect.DeepEqual(para.Containers, want) {
		t.Errorf("top-level paragraph container path = %v, want %v", para.Containers, want)
	}
	if !para.Included {
		t.Errorf("paragraph prose excluded (%s); it is the core included case", para.Exclusion)
	}
	if para.Exclusion != text.NotExcluded {
		t.Errorf("included leaf carries Exclusion %q, want %q", para.Exclusion, text.NotExcluded)
	}
}

func TestFragmentaryRolesAreExcludedButRecorded(t *testing.T) {
	const src = "# A Heading\n\nSome body prose to keep the parse honest.\n\n" +
		"Term\n: A definition description that is a whole sentence.\n"

	_, root := structure(t, src, text.DefaultStructureOptions())

	for _, role := range []text.Role{text.RoleHeading, text.RoleDefinitionTerm} {
		leaf := onlyRole(t, root, role)
		if leaf.Included {
			t.Errorf("%s is included; DESIGN excludes it as fragmentary and verbless", role)
		}
		if leaf.Exclusion != text.ExcludedByRole {
			t.Errorf("%s Exclusion = %q, want %q", role, leaf.Exclusion, text.ExcludedByRole)
		}
	}

	// A heading that reads as a full sentence is still excluded: the exclusion
	// is by role, and role outranks sententiality.
	_, root2 := structure(t, "# The proxy terminates TLS before the request reaches the app.\n", text.DefaultStructureOptions())
	h := onlyRole(t, root2, text.RoleHeading)
	if h.Included || h.Exclusion != text.ExcludedByRole {
		t.Errorf("sentential heading: Included=%v Exclusion=%q, want false/%q", h.Included, h.Exclusion, text.ExcludedByRole)
	}

	desc := onlyRole(t, root, text.RoleDefinitionDescription)
	if !desc.Included {
		t.Errorf("definition description excluded (%s); DESIGN includes it as authored prose", desc.Exclusion)
	}
}

// The load-bearing case: a list is a container, so inclusion is decided per
// item, never for the list.
func TestListItemProseIsDecidedPerItemNotPerContainer(t *testing.T) {
	const src = "" +
		"- Nginx\n" +
		"- The proxy terminates TLS before the request reaches the app.\n" +
		"- Redis\n" +
		"- Sessions live there so a restart does not sign everyone out.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	runs := findRole(root, text.RoleParagraph)
	if len(runs) != 4 {
		t.Fatalf("got %d list-item runs, want 4", len(runs))
	}
	for i, want := range []bool{false, true, false, true} {
		if runs[i].Included != want {
			t.Errorf("item %d (%q): Included = %v, want %v", i, runText(t, doc, runs[i]), runs[i].Included, want)
		}
		if !want && runs[i].Exclusion != text.ExcludedNotSentential {
			t.Errorf("item %d Exclusion = %q, want %q", i, runs[i].Exclusion, text.ExcludedNotSentential)
		}
	}
}

// The declared rule, at its edges. Without these an implementation of
// "two-or-more words ending in a period" passes every other test in the file.
func TestSententialityRuleBoundaries(t *testing.T) {
	cases := []struct {
		name string
		item string
		want bool
	}{
		// MinSententialWords is 4: terminal punctuation is not enough on its own.
		{"three words with a period", "Ship it now.", false},
		{"exactly four words with a period", "We ship it now.", true},
		{"question mark counts", "Why did it fail?", true},
		{"exclamation counts", "Do not ship it!", true},

		// MinUnpunctuatedWords is 8: real bullet prose often has no final stop.
		{"seven words unpunctuated", "Every rule declares its provenance and category", false},
		{"eight words unpunctuated", "Contamination screening still reports NotPerformed for every snapshot", true},

		// Closing delimiters are peeled before the terminal test, or a quoted
		// or parenthesized ending would read as unpunctuated.
		{"closing double quote", `He said, "Ship it."`, true},
		{"closing parenthesis", "The corpus is split three ways (train, calibrate, test).", true},
		{"emphasis closer", "*The rule is complete and correct.*", true},

		// A colon or comma is not a sentence terminator.
		{"colon ending", "The three splits are as follows:", false},
		// Seven words, deliberately: at eight the unpunctuated tier would admit
		// it regardless, and this case is about the comma not terminating.
		{"comma ending", "We admit the document, then parse it,", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, root := structure(t, "- "+c.item+"\n", text.DefaultStructureOptions())
			checkTree(t, doc, root, text.DefaultStructureOptions())
			leaf := onlyRole(t, root, text.RoleParagraph)
			if leaf.Sentential != c.want {
				t.Errorf("Sentential = %v, want %v (Words=%d EndsTerminal=%v)", leaf.Sentential, c.want, leaf.Words, leaf.EndsTerminal)
			}
			if leaf.Included != c.want {
				t.Errorf("Included = %v, want %v", leaf.Included, c.want)
			}
		})
	}
}

// The thresholds are options, so moving them must move the verdict. An
// implementation with the numbers hard-coded passes the boundary test above.
func TestSententialityThresholdsAreHonoured(t *testing.T) {
	const src = "- Ship it now.\n"

	opts := text.DefaultStructureOptions()
	opts.MinSententialWords = 3
	_, root := structure(t, src, opts)
	if leaf := onlyRole(t, root, text.RoleParagraph); !leaf.Sentential {
		t.Error("lowering MinSententialWords to 3 did not admit a three-word sentence")
	}

	opts = text.DefaultStructureOptions()
	opts.MinUnpunctuatedWords = 4
	_, root = structure(t, "- Every rule declares its provenance and category\n", opts)
	if leaf := onlyRole(t, root, text.RoleParagraph); !leaf.Sentential {
		t.Error("lowering MinUnpunctuatedWords to 4 did not admit a seven-word unpunctuated run")
	}
}

// Words and EndsTerminal describe what SURVIVES excision. A run whose words or
// whose final stop come only from code, an image or a footnote marker is not
// prose that an author wrote.
func TestSententialityIsJudgedAfterExcision(t *testing.T) {
	cases := []struct {
		name  string
		item  string
		words int
		want  bool
	}{
		{"only code spans", "`--verbose` `--quiet` `--json` `--porcelain` `--color` `--no-pager`", 0, false},
		{"code supplies the words", "See `func Structure(opts StructureOptions) *Node` here.", 2, false},
		{"image supplies the words", "![A scatter plot of AUC against corpus size](plot.png)", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, root := structure(t, "- "+c.item+"\n", text.DefaultStructureOptions())
			checkTree(t, doc, root, text.DefaultStructureOptions())
			leaf := root.Leaves()[0]
			if leaf.Words != c.words {
				t.Errorf("Words = %d, want %d (RunText = %q)", leaf.Words, c.words, runText(t, doc, leaf))
			}
			if leaf.Sentential != c.want {
				t.Errorf("Sentential = %v, want %v", leaf.Sentential, c.want)
			}
		})
	}
}

// A footnote marker cannot supply the terminal stop.
func TestFootnoteMarkerDoesNotSupplyTerminalPunctuation(t *testing.T) {
	const src = "A claim that needs some support[^1]\n\n[^1]: Published with the claim.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	para := findRole(root, text.RoleParagraph)[0]
	if para.EndsTerminal {
		t.Errorf("EndsTerminal = true for %q; the marker is not authored punctuation", runText(t, doc, para))
	}
}

func TestShortTopLevelParagraphIsStillIncluded(t *testing.T) {
	_, root := structure(t, "Nginx\n", text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)
	if !para.Included {
		t.Errorf("top-level paragraph excluded as %q; the per-item rule is for list items", para.Exclusion)
	}
}

// ---------------------------------------------------------------------------
// The declared rule's measured error rate
// ---------------------------------------------------------------------------

// sententialityFixture is hand-annotated by linguistic judgment — "is this a
// complete clause?" — independently of the declared rule. wantRule is what the
// declared rule produces. Where they disagree, the rule is wrong, and the
// disagreement is recorded rather than hidden.
// words and endsTerm are the evidence the leaf must report. Pinning only the
// verdict would let an implementation reach the right answer from wrong counts.
// Every count below was produced by the slice-2a tokenizer, not by hand.
var sententialityFixture = []struct {
	item     string
	words    int
	endsTerm bool
	isProse  bool // human annotation
	wantRule bool // declared heuristic's verdict
}{
	{"The proxy terminates TLS before the request reaches the app.", 10, true, true, true},
	{"Sessions live there so a restart does not sign everyone out.", 11, true, true, true},
	{"It caches nothing, which is the point.", 7, true, true, true},
	{`He said, "Ship it."`, 4, true, true, true},
	{"Run it twice", 3, false, true, false}, // MISS: imperative clause under the word floor
	{"*The rule is complete and correct.*", 6, true, true, true},
	{"Why does the segmenter fail differently on different authors?", 9, true, true, true},
	{"Do not normalize CRLF to LF!", 6, true, true, true},
	{"The corpus is split three ways (train, calibrate, test).", 9, true, true, true},
	{"Every rule declares its provenance and category", 7, false, true, false}, // MISS: unpunctuated clause, seven words
	{"Contamination screening still reports NotPerformed for every snapshot", 8, false, true, true},
	{"A profile fitted on the calibrate split leaks into the reported figures.", 12, true, true, true},
	{"The band is withheld rather than quietly relaxed.", 8, true, true, true},
	{"Offsets are raw byte offsets, never normalized ones.", 8, true, true, true},
	{"Nginx", 1, false, false, false},
	{"Redis", 1, false, false, false},
	{"Go 1.24", 1, false, false, false},
	{"Sentence length", 2, false, false, false},
	{"Mean word length per paragraph", 5, false, false, false},
	{"Type-token ratio and its length dependence", 6, false, false, false},
	{"TLS termination, session storage, and rate limiting", 7, false, false, false},
	{"MIT", 1, false, false, false},
	{"docs/DESIGN.md", 3, false, false, false},
	{"The versioned lexical-diversity contract and its sampling protocol parameters", 9, false, false, true}, // FALSE POSITIVE: long noun phrase
	{"Front matter", 2, false, false, false},
	{"In production only.", 3, true, false, false},
	{"In production only, never in the test suite.", 8, true, false, true}, // FALSE POSITIVE: punctuated fragment
	{"Abbreviations, initials, decimals, ellipses, quoted sentences", 6, false, false, false},
	{"Distractor corpus selection", 3, false, false, false},
	{"Hapax ratio", 2, false, false, false},
}

// The published ceiling. Declared before measurement, per DESIGN's rule that
// numerical targets are outputs rather than inputs: the measured rate is
// reported by this test and the ceiling is what makes a regression fail.
const sententialityErrorCeiling = 0.20

func TestSententialityRuleErrorRateIsPublished(t *testing.T) {
	var md strings.Builder
	for _, c := range sententialityFixture {
		md.WriteString("- " + c.item + "\n")
	}

	doc, root := structure(t, md.String(), text.DefaultStructureOptions())

	runs := findRole(root, text.RoleParagraph)
	if len(runs) != len(sententialityFixture) {
		t.Fatalf("parsed %d list items, want %d", len(runs), len(sententialityFixture))
	}

	errors := 0
	for i, c := range sententialityFixture {
		if runs[i].Words != c.words {
			t.Errorf("item %d %q: Words = %d, want %d (RunText = %q)", i, c.item, runs[i].Words, c.words, runText(t, doc, runs[i]))
		}
		if runs[i].EndsTerminal != c.endsTerm {
			t.Errorf("item %d %q: EndsTerminal = %v, want %v", i, c.item, runs[i].EndsTerminal, c.endsTerm)
		}
		if got := runText(t, doc, runs[i]); got != c.item {
			t.Errorf("item %d: RunText = %q, want %q", i, got, c.item)
		}
		if runs[i].Sentential != c.wantRule {
			t.Errorf("item %d %q: Sentential = %v, want %v (Words=%d EndsTerminal=%v)",
				i, c.item, runs[i].Sentential, c.wantRule, runs[i].Words, runs[i].EndsTerminal)
		}
		if c.wantRule != c.isProse {
			errors++
		}
	}

	rate := float64(errors) / float64(len(sententialityFixture))
	t.Logf("declared sententiality rule (%s): %d/%d disagreements with the annotation, error rate %.3f",
		text.StructureVersion, errors, len(sententialityFixture), rate)
	if rate > sententialityErrorCeiling {
		t.Errorf("error rate %.3f exceeds the published ceiling %.3f", rate, sententialityErrorCeiling)
	}
}

// ---------------------------------------------------------------------------
// Inline excision
// ---------------------------------------------------------------------------

func TestInlineCodeIsExcisedAndSurroundingProseRetained(t *testing.T) {
	const src = "Call `doc.Structure(opts)` before reading any leaf role.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)

	if len(para.Excisions) != 1 {
		t.Fatalf("got %d excisions, want 1: %+v", len(para.Excisions), para.Excisions)
	}
	// The excision covers the backticks too: a stray delimiter left behind
	// tokenizes as punctuation.
	if got, err := doc.Resolve(para.Excisions[0]); err != nil {
		t.Fatalf("excision span does not resolve: %v", err)
	} else if got != "`doc.Structure(opts)`" {
		t.Errorf("excision covers %q, want the full code span including delimiters", got)
	}
	if got, want := runText(t, doc, para), "Call  before reading any leaf role."; got != want {
		t.Errorf("RunText = %q, want %q", got, want)
	}
	if got := resolve(t, doc, para); got != strings.TrimSuffix(src, "\n") {
		t.Errorf("Resolve = %q; the leaf span must still cover the whole run", got)
	}
}

func TestEveryExcisedConstructIsIdentified(t *testing.T) {
	const src = "See `a` and ![alt text](plot.png) and the note[^1] in one run of prose.\n\n" +
		"[^1]: The note body.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	para := findRole(root, text.RoleParagraph)[0]
	want := []string{"`a`", "![alt text](plot.png)", "[^1]"}
	if len(para.Excisions) != len(want) {
		t.Fatalf("got %d excisions, want %d", len(para.Excisions), len(want))
	}
	for i, w := range want {
		got, err := doc.Resolve(para.Excisions[i])
		if err != nil {
			t.Fatalf("excision %d does not resolve: %v", i, err)
		}
		if got != w {
			t.Errorf("excision %d covers %q, want %q", i, got, w)
		}
	}
	if got, want := runText(t, doc, para), "See  and  and the note in one run of prose."; got != want {
		t.Errorf("RunText = %q, want %q", got, want)
	}
}

func TestMultipleCodeSpansAreEachExcised(t *testing.T) {
	const src = "Prefer `a` over `bb` and never `ccc` in prose that runs on.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)

	if len(para.Excisions) != 3 {
		t.Fatalf("got %d excisions, want 3", len(para.Excisions))
	}
	if got, want := runText(t, doc, para), "Prefer  over  and never  in prose that runs on."; got != want {
		t.Errorf("RunText = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Non-prose blocks
// ---------------------------------------------------------------------------

func TestCodeBlocksAndFrontMatterAreExcluded(t *testing.T) {
	const src = "---\ntitle: A Post\nauthor: Someone\n---\n\n" +
		"Real prose that belongs in the population.\n\n" +
		"```go\nfunc main() { println(\"not prose\") }\n```\n\n" +
		"    indented code is also not prose\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	fm := onlyRole(t, root, text.RoleFrontMatter)
	if fm.Included || fm.Exclusion != text.ExcludedByRole {
		t.Errorf("front matter: Included=%v Exclusion=%q, want false/%q", fm.Included, fm.Exclusion, text.ExcludedByRole)
	}
	if got := resolve(t, doc, fm); got != "title: A Post\nauthor: Someone" {
		t.Errorf("front matter span = %q; it must cover the body between the fences", got)
	}

	blocks := findRole(root, text.RoleCodeBlock)
	if len(blocks) != 2 {
		t.Fatalf("got %d code blocks, want 2 (fenced and indented)", len(blocks))
	}
	for _, b := range blocks {
		if b.Included || b.Exclusion != text.ExcludedByRole {
			t.Errorf("code block at %+v: Included=%v Exclusion=%q", b.Span, b.Included, b.Exclusion)
		}
	}

	if para := onlyRole(t, root, text.RoleParagraph); !para.Included {
		t.Errorf("the one real paragraph was excluded as %q", para.Exclusion)
	}
}

// Front matter shifts every offset after it; parsing the remainder without
// rebasing is the obvious implementation bug.
func TestOffsetsAfterFrontMatterAreRebased(t *testing.T) {
	// "---\n"=4 (0-3), "a: 1\n"=5 (4-8), "---\n"=4 (9-12), "\n"=1 (13), prose at 14.
	const src = "---\na: 1\n---\n\nProse after the front matter block.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)

	if got, want := para.Span, (text.Span{Offset: 14, Length: 35}); got != want {
		t.Errorf("paragraph span = %+v, want %+v", got, want)
	}
	if got := resolve(t, doc, para); got != "Prose after the front matter block." {
		t.Errorf("paragraph resolves to %q", got)
	}
}

// The rebasing must survive everything else that moves offsets at once: a
// stripped BOM, CRLF line endings, and a decomposed character inside the front
// matter body.
func TestRebasingSurvivesBOMCRLFAndDecomposedInput(t *testing.T) {
	// After the BOM is stripped:
	//   "---\r\n"      bytes  0..4   (5)
	//   "a: e\u0301\r\n"  bytes  5..12  (8: 'a' ':' ' ' 'e' 0xCC 0x81 '\r' '\n')
	//   "---\r\n"      bytes 13..17  (5)
	//   "\r\n"         bytes 18..19  (2)
	//   prose          byte  20, 35 bytes
	const src = "\ufeff---\r\na: e\u0301\r\n---\r\n\r\nProse after the front matter block.\r\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	if !doc.HadBOM() {
		t.Fatal("HadBOM() = false; the fixture starts with a BOM")
	}

	fm := onlyRole(t, root, text.RoleFrontMatter)
	if got, want := fm.Span, (text.Span{Offset: 5, Length: 6}); got != want {
		t.Errorf("front matter span = %+v, want %+v", got, want)
	}
	if got := resolve(t, doc, fm); got != "a: \u00e9" {
		t.Errorf("front matter resolves to %q, want %q", got, "a: \u00e9")
	}

	para := onlyRole(t, root, text.RoleParagraph)
	if got, want := para.Span, (text.Span{Offset: 20, Length: 35}); got != want {
		t.Errorf("paragraph span = %+v, want %+v", got, want)
	}
}

func TestFrontMatterOnlyAtStartOfDocument(t *testing.T) {
	const src = "Opening prose that precedes the rule.\n\n---\na: 1\n---\n"

	_, root := structure(t, src, text.DefaultStructureOptions())
	if got := findRole(root, text.RoleFrontMatter); len(got) != 0 {
		t.Errorf("found %d front-matter leaves in a document that opens with prose", len(got))
	}
}

func TestTableCellsAreExcludedAndTheTableIsAContainer(t *testing.T) {
	const src = "" +
		"| Feature | Tier |\n" +
		"|---|---|\n" +
		"| Sentence length is measured per paragraph. | A |\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	cells := findRole(root, text.RoleTableCell)
	if len(cells) == 0 {
		t.Fatal("table produced no cell leaves; cells must be recorded, not discarded")
	}
	// Rows are not modelled: cells hang directly off the table container.
	wantPath := []text.ContainerKind{text.ContainerDocument, text.ContainerTable}
	for _, c := range cells {
		if !reflect.DeepEqual(c.Containers, wantPath) {
			t.Errorf("cell %q container path = %v, want %v", runText(t, doc, c), c.Containers, wantPath)
		}
		if c.Included || c.Exclusion != text.ExcludedByRole {
			t.Errorf("cell %q: Included=%v Exclusion=%q, want false/%q — cells are excluded even when sentential",
				runText(t, doc, c), c.Included, c.Exclusion, text.ExcludedByRole)
		}
	}
}

// ---------------------------------------------------------------------------
// Footnotes and captions
// ---------------------------------------------------------------------------

func TestFootnoteBodyIsProseAndTheMarkerDoesNotSplitTheParagraph(t *testing.T) {
	const src = "The claim rests on measured error rates.[^1]\n\n" +
		"[^1]: The rate is published alongside the fixture it was measured on.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	paras := findRole(root, text.RoleParagraph)
	if len(paras) != 1 {
		t.Fatalf("got %d paragraph leaves, want 1; the footnote marker split the run", len(paras))
	}
	if got := resolve(t, doc, paras[0]); got != "The claim rests on measured error rates.[^1]" {
		t.Errorf("referring paragraph span = %q; the span covers the whole run, marker included", got)
	}
	if got, want := runText(t, doc, paras[0]), "The claim rests on measured error rates."; got != want {
		t.Errorf("RunText = %q, want %q (reference marker excised)", got, want)
	}

	note := onlyRole(t, root, text.RoleFootnote)
	if !note.Included {
		t.Errorf("footnote body excluded as %q; DESIGN includes it", note.Exclusion)
	}
	if got := runText(t, doc, note); got != "The rate is published alongside the fixture it was measured on." {
		t.Errorf("footnote text = %q", got)
	}
}

// Markdown has no caption construct, so v1 recognizes figcaption inside an HTML
// block. The rest of that block is recorded and excluded, never dropped.
func TestFigcaptionIsACaptionAndTheRestOfTheHTMLBlockIsRecorded(t *testing.T) {
	const src = "<figure>\n<img src=\"plot.png\" alt=\"AUC by author\">\n" +
		"<figcaption>Discrimination holds across every author in the pool.</figcaption>\n</figure>\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	caption := onlyRole(t, root, text.RoleCaption)
	if !caption.Included {
		t.Errorf("caption excluded as %q", caption.Exclusion)
	}
	if got := runText(t, doc, caption); got != "Discrimination holds across every author in the pool." {
		t.Errorf("caption text = %q; the span must cover the inner text only", got)
	}

	remnants := findRole(root, text.RoleHTMLBlock)
	if len(remnants) == 0 {
		t.Fatal("the non-caption HTML was dropped; DESIGN records non-included leaves rather than discarding them")
	}
	for _, l := range remnants {
		if l.Included || l.Exclusion != text.ExcludedByRole {
			t.Errorf("HTML remnant %q: Included=%v Exclusion=%q", runText(t, doc, l), l.Included, l.Exclusion)
		}
	}
}

func TestImageOnlyParagraphIsNotProse(t *testing.T) {
	const src = "![A scatter plot of AUC against corpus size](plot.png)\n\n" +
		"The plot shows the floor holding above two hundred documents.\n"

	_, root := structure(t, src, text.DefaultStructureOptions())

	img := onlyRole(t, root, text.RoleImage)
	if img.Included || img.Exclusion != text.ExcludedByRole {
		t.Errorf("image-only paragraph: Included=%v Exclusion=%q, want false/%q", img.Included, img.Exclusion, text.ExcludedByRole)
	}
	if got := onlyRole(t, root, text.RoleParagraph); !got.Included {
		t.Errorf("the following prose paragraph was excluded as %q", got.Exclusion)
	}
}

// Every image form CommonMark defines, inline and reference alike.
//
// Added after the implementation panicked on four of these. An image with empty
// alt text is ordinary — it is what a decorative image looks like — and a
// reference-style image is no rarer.
func TestImageFormsAreExcised(t *testing.T) {
	const refDef = "\n\n[ref]: /url\n"
	cases := []struct {
		name     string
		src      string
		excision string
		run      string
	}{
		{"inline with alt", "Before ![alt text](x.png) after the image here.\n", "![alt text](x.png)", "Before  after the image here."},
		{"empty alt text", "Before ![](x.png) after the image here.\n", "![](x.png)", "Before  after the image here."},
		{"whitespace alt text", "Before ![ ](x.png) after the image here.\n", "![ ](x.png)", "Before  after the image here."},
		{"title attribute", "Before ![alt](x.png \"a title\") after the image.\n", "![alt](x.png \"a title\")", "Before  after the image."},
		{"empty alt and title", "Before ![](x.png \"a title\") after the image.\n", "![](x.png \"a title\")", "Before  after the image."},
		{"full reference", "Before ![alt text][ref] after the image here." + refDef, "![alt text][ref]", "Before  after the image here."},
		{"collapsed reference", "Before ![ref][] after the image runs here." + refDef, "![ref][]", "Before  after the image runs here."},
		{"shortcut reference", "Before ![ref] after the image runs here." + refDef, "![ref]", "Before  after the image runs here."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, root := structure(t, c.src, text.DefaultStructureOptions())
			paras := findRole(root, text.RoleParagraph)
			if len(paras) != 1 {
				t.Fatalf("got %d paragraph leaves, want 1", len(paras))
			}
			para := paras[0]
			if len(para.Excisions) != 1 {
				t.Fatalf("got %d excisions, want 1: %+v", len(para.Excisions), para.Excisions)
			}
			if got, err := doc.Resolve(para.Excisions[0]); err != nil {
				t.Fatalf("excision does not resolve: %v", err)
			} else if got != c.excision {
				t.Errorf("excision covers %q, want %q", got, c.excision)
			}
			if got := runText(t, doc, para); got != c.run {
				t.Errorf("RunText = %q, want %q", got, c.run)
			}
		})
	}
}

// A paragraph of nothing but images is not prose however many it holds, so the
// image role cannot be conditioned on there being exactly one.
func TestParagraphOfSeveralImagesIsNotProse(t *testing.T) {
	doc, root := structure(t, "![](x.png)![](y.png)![](z.png)\n", text.DefaultStructureOptions())

	leaf := onlyRole(t, root, text.RoleImage)
	if len(leaf.Excisions) != 3 {
		t.Fatalf("got %d excisions, want 3: %+v", len(leaf.Excisions), leaf.Excisions)
	}
	if got := runText(t, doc, leaf); got != "" {
		t.Errorf("RunText = %q, want empty", got)
	}
	if leaf.Included || leaf.Exclusion != text.ExcludedByRole {
		t.Errorf("Included=%v Exclusion=%q, want false/%q", leaf.Included, leaf.Exclusion, text.ExcludedByRole)
	}
}

// Excision is driven by what the parser actually resolved. Markup that only
// resembles a construct is authored prose and stays byte for byte: an unmatched
// double backtick is not a code span, and a bracket with no footnote definition
// is not a reference.
func TestOnlyResolvedConstructsAreExcised(t *testing.T) {
	const src = "A `` b and ![](x.png) and [^ not a note] together here.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)

	if len(para.Excisions) != 1 {
		t.Fatalf("got %d excisions, want 1 (the image only): %+v", len(para.Excisions), para.Excisions)
	}
	if got, _ := doc.Resolve(para.Excisions[0]); got != "![](x.png)" {
		t.Errorf("excision covers %q, want the image", got)
	}
	if got, want := runText(t, doc, para), "A `` b and  and [^ not a note] together here."; got != want {
		t.Errorf("RunText = %q, want %q", got, want)
	}
}

// An image inside a code span is code, not an image: the parser resolves one
// code span and no Image node, so there is exactly one excision.
//
// Pinning this also settles a policy gap it exposed. A run with nothing left
// after excision has no authored prose in it, whatever its container, so it is
// not in the feature population — otherwise an empty paragraph would count as a
// paragraph observation and dilute every per-paragraph statistic. Zero words is
// therefore never sentential, and the top-level paragraph exemption does not
// override it.
func TestRunWithNoProseLeftIsNotInThePopulation(t *testing.T) {
	const src = "`code with ![](img.png) inside it`\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)

	if got := findRole(root, text.RoleImage); len(got) != 0 {
		t.Errorf("got %d image leaves; the parser resolves a code span here, not an image", len(got))
	}
	if len(para.Excisions) != 1 {
		t.Fatalf("got %d excisions, want 1 code span: %+v", len(para.Excisions), para.Excisions)
	}
	if got, _ := doc.Resolve(para.Excisions[0]); got != src[:len(src)-1] {
		t.Errorf("excision covers %q, want the whole code span", got)
	}
	if got := runText(t, doc, para); got != "" {
		t.Errorf("RunText = %q, want empty", got)
	}
	if para.Words != 0 {
		t.Errorf("Words = %d, want 0", para.Words)
	}
	if para.Sentential {
		t.Error("a run with no words left is sentential")
	}
	if para.Included || para.Exclusion != text.ExcludedNotSentential {
		t.Errorf("Included=%v Exclusion=%q, want false/%q", para.Included, para.Exclusion, text.ExcludedNotSentential)
	}
}

// Zero words outranks the block-quote policy, under either setting and after a
// reclassification. Nothing left after excision is nothing to measure, so it
// cannot become admissible by flipping a policy that is about whose words they
// are.
func TestZeroWordRuleOutranksTheBlockQuotePolicy(t *testing.T) {
	const src = "> `code and nothing else`\n"

	on := text.DefaultStructureOptions()
	on.IncludeBlockQuotes = true

	assert := func(t *testing.T, doc *text.Document, leaf *text.Node) {
		t.Helper()
		if got := runText(t, doc, leaf); got != "" {
			t.Errorf("RunText = %q, want empty", got)
		}
		if leaf.Words != 0 {
			t.Errorf("Words = %d, want 0", leaf.Words)
		}
		if leaf.Included {
			t.Error("a quoted run with no words left was admitted")
		}
		if leaf.Exclusion != text.ExcludedNotSentential {
			t.Errorf("Exclusion = %q, want %q", leaf.Exclusion, text.ExcludedNotSentential)
		}
	}

	for _, tc := range []struct {
		name string
		opts text.StructureOptions
	}{{"policy off", text.DefaultStructureOptions()}, {"policy on", on}} {
		t.Run(tc.name, func(t *testing.T) {
			doc, root := structure(t, src, tc.opts)
			assert(t, doc, onlyRole(t, root, text.RoleParagraph))
		})
	}

	doc, root := structure(t, src, text.DefaultStructureOptions())
	root.Reclassify(on)
	checkTree(t, doc, root, on)
	assert(t, doc, onlyRole(t, root, text.RoleParagraph))
}

// A construct wrapped across a line inside a container produces two overlapping
// reasons to excise the same bytes: the container's marker on the continuation
// line, and the construct that spans it. They must coalesce into one span.
//
// Added after all three of these crashed. A code span wrapped across a line in
// a quote or a list item is ordinary technical writing, not an edge case.
func TestExcisionsOverlappingAContainerMarkerAreMerged(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		excision string
		run      string
	}{
		{
			"code span across lines in a quote",
			"> Run `go test\n> ./...` before pushing here.\n",
			"`go test\n> ./...`",
			"Run  before pushing here.",
		},
		{
			"code span across lines in a list item",
			"- Run `go test\n  ./...` before pushing here.\n",
			"`go test\n  ./...`",
			"Run  before pushing here.",
		},
		{
			"image across lines in a quote",
			"> See ![alt\n> text](x.png) here now.\n",
			"![alt\n> text](x.png)",
			"See  here now.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, root := structure(t, c.src, text.DefaultStructureOptions())
			para := onlyRole(t, root, text.RoleParagraph)
			if len(para.Excisions) != 1 {
				t.Fatalf("got %d excisions, want 1 merged span: %+v", len(para.Excisions), para.Excisions)
			}
			if got, err := doc.Resolve(para.Excisions[0]); err != nil {
				t.Fatalf("merged excision does not resolve: %v", err)
			} else if got != c.excision {
				t.Errorf("excision covers %q, want %q", got, c.excision)
			}
			if got := runText(t, doc, para); got != c.run {
				t.Errorf("RunText = %q, want %q", got, c.run)
			}
		})
	}
}

// A block with no content lines has no bytes to record, so it yields no leaf.
//
// Found by the sweep: an empty fence was producing a zero-length leaf at offset
// 0, which sits BEFORE its preceding sibling and breaks document order for
// everything after it. A leaf that spans nothing is not a record of anything —
// the fence delimiters are markers, and markers are not leaves anywhere else in
// this contract either.
func TestEmptyBlockYieldsNoLeaf(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		roles []text.Role
		run   string // RunText of the sole surviving leaf, if there is one
	}{
		{"empty fence alone", "```\n```\n", nil, ""},
		{"unterminated empty fence", "```\n", nil, ""},
		{"prose then empty fence", "Some prose here.\n\n```\n```\n", []text.Role{text.RoleParagraph}, "Some prose here."},
		{"image then empty fence", "![](x.png)\n\n```\n```\n", []text.Role{text.RoleImage}, ""},
		{"fence with content is kept", "```\nstuff\n```\n", []text.Role{text.RoleCodeBlock}, "stuff"},
		{"empty heading", "#\n\nProse after the empty heading.\n", []text.Role{text.RoleParagraph}, "Prose after the empty heading."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, root := structure(t, c.src, text.DefaultStructureOptions())
			var got []text.Role
			for _, l := range root.Leaves() {
				got = append(got, l.Role)
			}
			if len(got) != len(c.roles) {
				t.Fatalf("got leaves %v, want %v", got, c.roles)
			}
			for i := range got {
				if got[i] != c.roles[i] {
					t.Errorf("leaf %d = %q, want %q", i, got[i], c.roles[i])
				}
			}
			// The surviving leaf must carry its content, so a fix cannot keep a
			// marker-only leaf and still pass on role alone.
			if len(c.roles) == 1 {
				if got := runText(t, doc, root.Leaves()[0]); got != c.run {
					t.Errorf("RunText = %q, want %q", got, c.run)
				}
			}
		})
	}
}

// An empty table cell is ordinary, and it must not become an empty leaf.
//
// The empty-block rule was first applied at the code-block and heading call
// sites rather than centrally, so cells kept producing a zero-length leaf at
// offset 0 — which sorts before every real cell and breaks the table's order.
// The rule is general: NO leaf has an empty span, whatever produced it.
func TestEmptyTableCellYieldsNoLeaf(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		cells []string
	}{
		{"empty header cell", "| a |  |\n|---|---|\n| c | d |\n", []string{"a", "c", "d"}},
		{"empty body cell", "| a | b |\n|---|---|\n| c |  |\n", []string{"a", "b", "c"}},
		{"empty first cell", "|  | b |\n|---|---|\n| c | d |\n", []string{"b", "c", "d"}},
		{"no empty cells", "| a | b |\n|---|---|\n| c | d |\n", []string{"a", "b", "c", "d"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, root := structure(t, c.src, text.DefaultStructureOptions())
			cells := findRole(root, text.RoleTableCell)
			if len(cells) != len(c.cells) {
				var got []string
				for _, l := range cells {
					got = append(got, runText(t, doc, l))
				}
				t.Fatalf("got cells %q, want %q", got, c.cells)
			}
			for i, want := range c.cells {
				if got := runText(t, doc, cells[i]); got != want {
					t.Errorf("cell %d = %q, want %q", i, got, want)
				}
			}
		})
	}
}

// An independent producer of the same defect, pinned so that a table-local fix
// cannot satisfy the amendment while leaving the invariant non-central. An
// empty figcaption yields no caption leaf, and the HTML either side of it is
// still recorded, in order.
func TestEmptyFigcaptionYieldsNoCaptionLeaf(t *testing.T) {
	const src = "<figure>\n<figcaption></figcaption>\n</figure>\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())

	if got := findRole(root, text.RoleCaption); len(got) != 0 {
		t.Errorf("got %d caption leaves, want none for an empty figcaption", len(got))
	}
	remnants := findRole(root, text.RoleHTMLBlock)
	want := []string{"<figure>\n<figcaption>", "</figcaption>\n</figure>"}
	if len(remnants) != len(want) {
		t.Fatalf("got %d HTML remnants, want %d", len(remnants), len(want))
	}
	for i, w := range want {
		if got := resolve(t, doc, remnants[i]); got != w {
			t.Errorf("remnant %d = %q, want %q", i, got, w)
		}
	}
}

// Every span this package emits must be grapheme-aligned, so a CRLF is never
// split. Snap exists for exactly this; block spans must go through it too.
//
// The expected runs are pinned as well as the alignment: checkTree alone would
// pass an implementation that silently dropped the block it could not align.
func TestSpansNeverSplitACRLF(t *testing.T) {
	inList := []text.ContainerKind{text.ContainerDocument, text.ContainerList, text.ContainerListItem}
	inQuote := []text.ContainerKind{text.ContainerDocument, text.ContainerBlockQuote}
	atRoot := []text.ContainerKind{text.ContainerDocument}

	cases := []struct {
		src   string
		runs  []string
		roles []text.Role
		paths [][]text.ContainerKind
	}{
		{
			"- Prose in an item.\r\n\r\n- Another item entirely.\r\n",
			[]string{"Prose in an item.", "Another item entirely."},
			[]text.Role{text.RoleParagraph, text.RoleParagraph},
			[][]text.ContainerKind{inList, inList},
		},
		{
			"> Quoted prose here.\r\n> Continued on a second line.\r\n",
			[]string{"Quoted prose here.\r\nContinued on a second line."},
			[]text.Role{text.RoleParagraph},
			[][]text.ContainerKind{inQuote},
		},
		{
			"```\r\nfenced\r\n```\r\n",
			[]string{"fenced"},
			[]text.Role{text.RoleCodeBlock},
			[][]text.ContainerKind{atRoot},
		},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			// structure() runs checkTree, which resolves every span and so
			// rejects any that is not on a grapheme boundary.
			doc, root := structure(t, c.src, text.DefaultStructureOptions())
			leaves := root.Leaves()
			if len(leaves) != len(c.runs) {
				t.Fatalf("got %d leaves, want %d", len(leaves), len(c.runs))
			}
			for i, want := range c.runs {
				if got := runText(t, doc, leaves[i]); got != want {
					t.Errorf("leaf %d RunText = %q, want %q", i, got, want)
				}
				if leaves[i].Role != c.roles[i] {
					t.Errorf("leaf %d Role = %q, want %q", i, leaves[i].Role, c.roles[i])
				}
				if !reflect.DeepEqual(leaves[i].Containers, c.paths[i]) {
					t.Errorf("leaf %d container path = %v, want %v", i, leaves[i].Containers, c.paths[i])
				}
			}
		})
	}
}

// The backstop, and deliberately shape-agnostic: it asserts only that Structure
// returns without panicking and that every leaf it emits has resolvable RunText,
// under a parse and again after Reclassify. It does NOT assert that raw bytes
// are preserved or that no excision was guessed — those are the contract for an
// unlocatable envelope, and where the right shape is known it is pinned in the
// tests above. This is the floor for forms not enumerated there.
func TestStructureNeverPanicsOnValidMarkdown(t *testing.T) {
	for _, src := range []string{
		"![](/url)\n",
		"![](/url \"title\")\n",
		"![alt][ref]\n\n[ref]: /url\n",
		"![ref]\n\n[ref]: /url\n",
		"[![nested image link](i.png)](https://example.test)\n",
		"![](x.png) `a` [^1] all at once here.\n\n[^1]: A body.\n",
		"> ![](x.png)\n> - ![](y.png)\n",
		"> `\n> `a`\n",
		"> ``\n> ``\n",
		"- `a\n- `b\n",
		"> A claim[^\n> 1] here.\n\n[^1]: A body.\n",
		"- ```\n  \r\n",
		"> - ```\n>   \r\n",
		"| ![](x.png) | `a` |\n|---|---|\n| b | c |\n",
		"***\n\n<!-- a comment -->\n\n    code\n",
	} {
		t.Run(src, func(t *testing.T) {
			doc := mustAdmit(t, src)
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Structure(%q) panicked: %v", src, r)
				}
			}()
			root := doc.Structure(text.DefaultStructureOptions())
			resolveAll := func(stage string) {
				for _, l := range root.Leaves() {
					if _, err := doc.RunText(l); err != nil {
						t.Errorf("RunText on %s leaf %s: %v", l.Role, stage, err)
					}
				}
			}
			resolveAll("after parse")
			root.Reclassify(text.DefaultStructureOptions())
			resolveAll("after Reclassify")
		})
	}
}

func TestImageAltTextIsNotProseInsideAParagraph(t *testing.T) {
	const src = "See ![the AUC scatter plot](plot.png) for the measured floor here.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	para := onlyRole(t, root, text.RoleParagraph)

	if got, want := runText(t, doc, para), "See  for the measured floor here."; got != want {
		t.Errorf("RunText = %q, want %q (the image excised, its alt text with it)", got, want)
	}
}

// ---------------------------------------------------------------------------
// Block-quote policy, and reclassification without reparsing
// ---------------------------------------------------------------------------

func TestBlockQuotePolicyIsRecordedSeparately(t *testing.T) {
	const src = "> A quoted sentence that someone else wrote first.\n\n" +
		"My own sentence, which is included either way.\n"

	_, root := structure(t, src, text.DefaultStructureOptions())

	runs := findRole(root, text.RoleParagraph)
	if len(runs) != 2 {
		t.Fatalf("got %d paragraph runs, want 2", len(runs))
	}
	quoted, mine := runs[0], runs[1]

	if quoted.Included {
		t.Error("block-quote content included under the default policy")
	}
	if quoted.Exclusion != text.ExcludedByBlockQuotePolicy {
		t.Errorf("quote Exclusion = %q, want %q — a policy exclusion must be distinguishable from a role exclusion", quoted.Exclusion, text.ExcludedByBlockQuotePolicy)
	}
	if !mine.Included {
		t.Errorf("unquoted prose excluded as %q", mine.Exclusion)
	}
}

// DESIGN keeps non-included leaves so "a policy change can be applied without
// reparsing". Calling Structure twice does not demonstrate that; Reclassify
// operating on an existing tree, with no access to the source, does.
func TestReclassifyAppliesAPolicyChangeWithoutReparsing(t *testing.T) {
	const src = "> A quoted sentence that someone else wrote first.\n\n" +
		"> - Redis\n\n" +
		"My own sentence, which is included either way.\n"

	on := text.DefaultStructureOptions()
	on.IncludeBlockQuotes = true

	doc, tree := structure(t, src, text.DefaultStructureOptions())
	_, fresh := structure(t, src, on)

	tree.Reclassify(on)
	if !reflect.DeepEqual(tree, fresh) {
		t.Error("Reclassify did not reproduce a fresh parse under the new options")
	}
	checkTree(t, doc, tree, on)

	// And back again: reclassification is not one-way.
	_, original := structure(t, src, text.DefaultStructureOptions())
	tree.Reclassify(text.DefaultStructureOptions())
	if !reflect.DeepEqual(tree, original) {
		t.Error("Reclassify back to the default options did not restore the original verdicts")
	}
}

// Reclassify must honour the word thresholds too. An implementation that only
// re-evaluates the block-quote flag passes the test above.
func TestReclassifyHonoursThresholdChanges(t *testing.T) {
	const src = "- Ship it now.\n- Every rule declares its provenance and category\n"

	lower := text.DefaultStructureOptions()
	lower.MinSententialWords = 3
	lower.MinUnpunctuatedWords = 7

	doc, tree := structure(t, src, text.DefaultStructureOptions())
	for i, l := range tree.Leaves() {
		if l.Included {
			t.Fatalf("leaf %d was admitted under the default thresholds", i)
		}
	}

	_, fresh := structure(t, src, lower)
	tree.Reclassify(lower)
	if !reflect.DeepEqual(tree, fresh) {
		t.Error("Reclassify with lowered thresholds did not reproduce a fresh parse")
	}
	checkTree(t, doc, tree, lower)
	for i, l := range tree.Leaves() {
		if !l.Included {
			t.Errorf("leaf %d still excluded (%s) after lowering both thresholds", i, l.Exclusion)
		}
	}

	// And back: raising the thresholds must exclude them again. An
	// implementation that only ever admits newly qualifying leaves passes
	// everything above.
	_, restored := structure(t, src, text.DefaultStructureOptions())
	tree.Reclassify(text.DefaultStructureOptions())
	if !reflect.DeepEqual(tree, restored) {
		t.Error("Reclassify back to the default thresholds did not reproduce a fresh default parse")
	}
	for i, l := range tree.Leaves() {
		if l.Included {
			t.Errorf("leaf %d still admitted after restoring the default thresholds", i)
		}
		if l.Exclusion != text.ExcludedNotSentential {
			t.Errorf("leaf %d Exclusion = %q, want %q", i, l.Exclusion, text.ExcludedNotSentential)
		}
	}
}

// Node's field schema is frozen, exactly. An earlier version of this test only
// asserted that no field was unexported, which the reviewer correctly called
// theatre: a *Document can be retained in an EXPORTED field just as easily.
//
// Pinning the exact set closes that. What it does NOT close, and no test can,
// is package-global state keyed by the node — Reclassify could consult a side
// table and reparse from there. That is a phase-4 code-review obligation, not
// something this suite can establish, and it is recorded here rather than
// papered over.
func TestNodeSchemaIsFrozen(t *testing.T) {
	want := map[string]string{
		"Kind":         "text.Kind",
		"Container":    "text.ContainerKind",
		"Role":         "text.Role",
		"Span":         "text.Span",
		"Containers":   "[]text.ContainerKind",
		"Children":     "[]*text.Node",
		"Excisions":    "[]text.Span",
		"Words":        "int",
		"EndsTerminal": "bool",
		"Sentential":   "bool",
		"Included":     "bool",
		"Exclusion":    "text.ExclusionReason",
	}

	typ := reflect.TypeOf(text.Node{})
	got := make(map[string]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			t.Errorf("Node.%s is unexported; a leaf carries only recorded evidence", f.Name)
			continue
		}
		got[f.Name] = f.Type.String()
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Node schema = %v,\nwant %v", got, want)
	}
}

// Under either policy, a bare label inside a quote is excluded for NOT being a
// sentence. Recording the policy reason instead would wrongly admit it the
// moment the policy is flipped on.
func TestSententialityOutranksTheBlockQuotePolicy(t *testing.T) {
	const src = "> - Redis\n"

	for _, tc := range []struct {
		name string
		opts text.StructureOptions
	}{
		{"policy off", text.DefaultStructureOptions()},
		{"policy on", func() text.StructureOptions {
			o := text.DefaultStructureOptions()
			o.IncludeBlockQuotes = true
			return o
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, root := structure(t, src, tc.opts)
			checkTree(t, doc, root, tc.opts)
			item := onlyRole(t, root, text.RoleParagraph)
			if item.Included {
				t.Error("a bare label inside a block quote was admitted")
			}
			if item.Exclusion != text.ExcludedNotSentential {
				t.Errorf("Exclusion = %q, want %q", item.Exclusion, text.ExcludedNotSentential)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The paragraph unit — what unblocks `profile`
// ---------------------------------------------------------------------------

func TestIncludedLeavesIsExactlyTheAdmittedPopulation(t *testing.T) {
	_, root := structure(t, kitchenSink, text.DefaultStructureOptions())

	var want []*text.Node
	for _, l := range root.Leaves() {
		if l.Included {
			want = append(want, l)
		}
	}
	got := root.IncludedLeaves()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IncludedLeaves returned %d leaves, want the %d leaves whose verdict is Included", len(got), len(want))
	}
	if len(got) == 0 {
		t.Fatal("the kitchen sink admitted no prose at all")
	}
}

// The population must be the authored prose in the fixture, named explicitly.
// A test that only checks internal consistency passes on an implementation
// that admits nothing.
func TestKitchenSinkAdmitsExactlyTheAuthoredProse(t *testing.T) {
	doc, root := structure(t, kitchenSink, text.DefaultStructureOptions())

	var got []string
	for _, l := range root.IncludedLeaves() {
		got = append(got, runText(t, doc, l))
	}
	want := []string{
		"An opening paragraph that states the claim plainly.",
		"A second paragraph carrying  in the middle of a sentence.",
		"The proxy terminates TLS before the request reaches the app.",
		"Sessions live there so a restart does not sign everyone out.",
		"Discrimination holds across every author in the pool.",
		"A closing paragraph that restates nothing at all.",
		"A trailing claim.",
		"The rate is published alongside the fixture it was measured on.",
	}
	if len(got) != len(want) {
		t.Fatalf("admitted %d runs, want %d:\ngot  %q\nwant %q", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("run %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The whole leaf sequence of the kitchen sink, pinned: role, exact container
// path and verdict, in document order.
//
// Asserting exact paths one role at a time is whack-a-mole — the reviewer found
// a fabricated container for headings only after footnotes, tables, definition
// lists and top-level paragraphs had each been pinned individually. This closes
// the class: every leaf the fixture produces is named, so no leaf can be
// wrapped in an invented container, given a wrong role, or quietly dropped.
func TestKitchenSinkLeafSequenceIsPinned(t *testing.T) {
	doc0 := []text.ContainerKind{text.ContainerDocument}
	inList := []text.ContainerKind{text.ContainerDocument, text.ContainerList, text.ContainerListItem}
	inQuote := []text.ContainerKind{text.ContainerDocument, text.ContainerBlockQuote}
	inTable := []text.ContainerKind{text.ContainerDocument, text.ContainerTable}
	inDefs := []text.ContainerKind{text.ContainerDocument, text.ContainerDefinitionList}
	inNote := []text.ContainerKind{text.ContainerDocument, text.ContainerFootnoteSection, text.ContainerFootnote}

	// text is the leaf's resolved RAW span, excisions included. Without it the
	// tuples repeat and an implementation can drop one table cell while
	// splitting another in two, keeping the same ordered role/path/verdict
	// sequence.
	want := []struct {
		role text.Role
		path []text.ContainerKind
		excl text.ExclusionReason
		text string
	}{
		{text.RoleFrontMatter, doc0, text.ExcludedByRole, "title: Fixture"},
		{text.RoleHeading, doc0, text.ExcludedByRole, "Heading Excluded"},
		{text.RoleParagraph, doc0, text.NotExcluded, "An opening paragraph that states the claim plainly."},
		{text.RoleParagraph, doc0, text.NotExcluded, "A second paragraph carrying `inline.Code()` in the middle of a sentence."},
		{text.RoleParagraph, inList, text.ExcludedNotSentential, "Nginx"},
		{text.RoleParagraph, inList, text.NotExcluded, "The proxy terminates TLS before the request reaches the app."},
		{text.RoleParagraph, inList, text.ExcludedNotSentential, "Redis"},
		{text.RoleParagraph, inList, text.NotExcluded, "Sessions live there so a restart does not sign everyone out."},
		{text.RoleParagraph, inQuote, text.ExcludedByBlockQuotePolicy, "A quotation belonging to somebody else entirely."},
		{text.RoleCodeBlock, doc0, text.ExcludedByRole, "func main() {}"},
		{text.RoleTableCell, inTable, text.ExcludedByRole, "Column"},
		{text.RoleTableCell, inTable, text.ExcludedByRole, "Tier"},
		{text.RoleTableCell, inTable, text.ExcludedByRole, "A cell that reads like a sentence."},
		{text.RoleTableCell, inTable, text.ExcludedByRole, "A"},
		// The caption splits its HTML block in two, so the remnants either side
		// of it are forced by the ordering and disjointness invariants.
		{text.RoleHTMLBlock, doc0, text.ExcludedByRole, "<figure>\n<figcaption>"},
		{text.RoleCaption, doc0, text.NotExcluded, "Discrimination holds across every author in the pool."},
		{text.RoleHTMLBlock, doc0, text.ExcludedByRole, "</figcaption>\n</figure>"},
		{text.RoleDefinitionTerm, inDefs, text.ExcludedByRole, "Term"},
		{text.RoleDefinitionDescription, inDefs, text.NotExcluded, "A closing paragraph that restates nothing at all."},
		{text.RoleParagraph, doc0, text.NotExcluded, "A trailing claim.[^1]"},
		{text.RoleFootnote, inNote, text.NotExcluded, "The rate is published alongside the fixture it was measured on."},
	}

	doc, root := structure(t, kitchenSink, text.DefaultStructureOptions())
	got := root.Leaves()
	if len(got) != len(want) {
		for i, l := range got {
			t.Logf("leaf %2d  %-24s %v  %q", i, l.Role, l.Containers, runText(t, doc, l))
		}
		t.Fatalf("kitchen sink produced %d leaves, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Role != w.role {
			t.Errorf("leaf %d (%q): Role = %q, want %q", i, runText(t, doc, got[i]), got[i].Role, w.role)
		}
		if !reflect.DeepEqual(got[i].Containers, w.path) {
			t.Errorf("leaf %d (%q): container path = %v, want %v", i, runText(t, doc, got[i]), got[i].Containers, w.path)
		}
		if got[i].Exclusion != w.excl {
			t.Errorf("leaf %d (%q): Exclusion = %q, want %q", i, runText(t, doc, got[i]), got[i].Exclusion, w.excl)
		}
		if got := resolve(t, doc, got[i]); got != w.text {
			t.Errorf("leaf %d spans %q, want %q", i, got, w.text)
		}
	}
}

// Every fixture construct at once, so the ordering, disjointness and population
// checks run against something that actually nests.
const kitchenSink = "---\ntitle: Fixture\n---\n\n" +
	"# Heading Excluded\n\n" +
	"An opening paragraph that states the claim plainly.\n\n" +
	"A second paragraph carrying `inline.Code()` in the middle of a sentence.\n\n" +
	"- Nginx\n" +
	"- The proxy terminates TLS before the request reaches the app.\n" +
	"- Redis\n" +
	"- Sessions live there so a restart does not sign everyone out.\n\n" +
	"> A quotation belonging to somebody else entirely.\n\n" +
	"```go\nfunc main() {}\n```\n\n" +
	"| Column | Tier |\n|---|---|\n| A cell that reads like a sentence. | A |\n\n" +
	"<figure>\n<figcaption>Discrimination holds across every author in the pool.</figcaption>\n</figure>\n\n" +
	"Term\n: A closing paragraph that restates nothing at all.\n\n" +
	"A trailing claim.[^1]\n\n" +
	"[^1]: The rate is published alongside the fixture it was measured on.\n"
