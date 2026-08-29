package assemble_test

// Splices accepted replacements back into the original bytes. The loop decides
// per paragraph what the text should be; this turns those decisions into a
// document, and the whole contract is about what it refuses to do.
//
// # Why a span must equal an included leaf's span
//
// Nothing in this system produces a verdict for an arbitrary byte range. score
// measures paragraphs, the loop decides per paragraph, and a sub-span has no
// measurement attached to it — so splicing one would write text no gate ever
// saw. It also disposes of grapheme splitting for free, since leaf spans are
// already boundary-aligned.
//
// # Why an excision refuses the whole leaf
//
// A leaf's run text DROPS its excisions. The paragraph
//
//	A paragraph with `code` inline and a footnote.[^1]
//
// reaches the loop as "A paragraph with  inline and a footnote." — the code
// span and the reference are not in the string it rewrote. Splicing that
// rewrite over the leaf's raw span would delete the user's code and their
// footnote, silently, and nothing downstream would notice.
//
// Measured against the real structure parser rather than assumed: bold, links,
// em-dashes and wrapped lines produce ZERO excisions and stay rewritable. Only
// inline code spans and footnote references produce them.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/assemble"
	"github.com/fissible/hapax/internal/text"
	"golang.org/x/text/unicode/norm"
)

func admit(t *testing.T, source string) *text.Document {
	t.Helper()
	doc, err := text.Admit([]byte(source))
	if err != nil {
		t.Fatalf("Admit(%q): %v", source, err)
	}
	return doc
}

// spanOf finds the included leaf whose run text is exactly want. Tests name
// leaves by their prose so a fixture edit cannot silently retarget a span.
func spanOf(t *testing.T, doc *text.Document, want string) text.Span {
	t.Helper()
	for _, leaf := range doc.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		run, err := doc.RunText(leaf)
		if err != nil {
			t.Fatalf("RunText: %v", err)
		}
		if run == want {
			return leaf.Span
		}
	}
	t.Fatalf("no included leaf with run text %q", want)
	return text.Span{}
}

// anyLeafSpan finds a leaf by run text whether or not it is included, so tests
// can name a heading or a code block in order to be refused one.
func anyLeafSpan(t *testing.T, doc *text.Document, want string) text.Span {
	t.Helper()
	for _, leaf := range doc.Structure(text.DefaultStructureOptions()).Leaves() {
		run, err := doc.RunText(leaf)
		if err != nil {
			t.Fatalf("RunText: %v", err)
		}
		if run == want {
			return leaf.Span
		}
	}
	t.Fatalf("no leaf with run text %q", want)
	return text.Span{}
}

func mustAssemble(t *testing.T, doc *text.Document, replacements ...assemble.Replacement) []byte {
	t.Helper()
	got, err := assemble.Assemble(doc, replacements)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return got
}

// mustRefuse requires a refusal AND no output, which is the all-or-nothing half
// of the contract. A component returning partial bytes beside an error would
// satisfy a bare error assertion.
func mustRefuse(t *testing.T, doc *text.Document, want error, replacements ...assemble.Replacement) {
	t.Helper()
	got, err := assemble.Assemble(doc, replacements)
	if err == nil {
		t.Fatalf("accepted, want %v; produced %q", want, got)
	}
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}
	if got != nil {
		t.Errorf("refused but still produced %d bytes: %q", len(got), got)
	}
}

const doc3 = "First paragraph here.\n\nSecond paragraph here.\n\nThird paragraph here.\n"

// ---------------------------------------------------------------------------
// The identity case, and byte exactness
// ---------------------------------------------------------------------------

func TestNoReplacementsReturnsTheOriginalBytes(t *testing.T) {
	doc := admit(t, doc3)
	if got := mustAssemble(t, doc); !bytes.Equal(got, []byte(doc3)) {
		t.Errorf("assembled %q, want the original %q", got, doc3)
	}
}

// A replacement identical to what is already there must also be a no-op, which
// separates "applied the replacement" from "happened to copy the input".
func TestReplacingATextWithItselfChangesNothing(t *testing.T) {
	doc := admit(t, doc3)
	got := mustAssemble(t, doc, assemble.Replacement{
		Span: spanOf(t, doc, "Second paragraph here."),
		Text: "Second paragraph here.",
	})
	if !bytes.Equal(got, []byte(doc3)) {
		t.Errorf("assembled %q, want %q", got, doc3)
	}
}

func TestOneReplacementLeavesEveryOtherByteAlone(t *testing.T) {
	doc := admit(t, doc3)
	got := mustAssemble(t, doc, assemble.Replacement{
		Span: spanOf(t, doc, "Second paragraph here."),
		Text: "A wholly different second paragraph.",
	})
	want := "First paragraph here.\n\nA wholly different second paragraph.\n\nThird paragraph here.\n"
	if string(got) != want {
		t.Errorf("assembled\n%q\nwant\n%q", got, want)
	}
}

// Blank lines, trailing spaces and CRLF are bytes like any other. text.Admit
// deliberately does not normalize line endings, because doing so would shift
// every offset in the file, so neither may this.
func TestUntouchedBytesSurviveExactly(t *testing.T) {
	source := "First paragraph here.\r\n\r\nSecond paragraph here.\r\n\r\n\r\n   \r\nThird paragraph here.\r\n"
	doc := admit(t, source)
	got := mustAssemble(t, doc, assemble.Replacement{
		Span: spanOf(t, doc, "Second paragraph here."),
		Text: "Replaced.",
	})
	// The whole document, byte for byte. Counting CRLFs and checking a prefix
	// leaves every other untouched byte unasserted.
	want := "First paragraph here.\r\n\r\nReplaced.\r\n\r\n\r\n   \r\nThird paragraph here.\r\n"
	if string(got) != want {
		t.Errorf("assembled\n%q\nwant\n%q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Offsets do not drift
// ---------------------------------------------------------------------------

// The bug this component exists to not have. Spans address the ORIGINAL bytes,
// so an implementation that splices into a buffer it is simultaneously growing
// will place every replacement after the first at the wrong offset. Both
// directions, because a shorter replacement drifts the other way and a single
// same-length fixture would hide the whole class.
func TestLaterSpansAreNotShiftedByEarlierReplacements(t *testing.T) {
	for _, c := range []struct {
		name, first, second, third string
	}{
		{"all longer", strings.Repeat("long ", 20), strings.Repeat("longer ", 20), strings.Repeat("longest ", 20)},
		{"all shorter", "a.", "b.", "c."},
		{"grow then shrink", strings.Repeat("much longer text ", 10), "x.", strings.Repeat("also much longer ", 10)},
		{"shrink then grow", "y.", strings.Repeat("considerably longer ", 10), "z."},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := admit(t, doc3)
			got := mustAssemble(t, doc,
				assemble.Replacement{Span: spanOf(t, doc, "First paragraph here."), Text: c.first},
				assemble.Replacement{Span: spanOf(t, doc, "Second paragraph here."), Text: c.second},
				assemble.Replacement{Span: spanOf(t, doc, "Third paragraph here."), Text: c.third},
			)
			want := c.first + "\n\n" + c.second + "\n\n" + c.third + "\n"
			if string(got) != want {
				t.Errorf("assembled\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// Every drift fixture above is ASCII, where a byte offset and a rune index are
// the same number — so a rune-indexed implementation passes all of them. Span
// addresses BYTES. Put multibyte text in the leaves that come first and the two
// diverge: an implementation counting runes lands short by one byte for every
// two-byte rune it passed, and by two for every three-byte one.
func TestOffsetsAreBytesNotRunes(t *testing.T) {
	// The first paragraph carries 2-byte (e\u0301 is 1+2), 3-byte (\u2014) and
	// 4-byte (\U0001F600) runes, so byte offsets run well ahead of rune indices
	// by the time the later leaves start.
	const source = "H\u00e9llo \u2014 a na\u00efve first paragraph \U0001F600 with wide runes.\n\n" +
		"Second paragraph here.\n\nThird paragraph here.\n"
	doc := admit(t, source)

	first := spanOf(t, doc, "H\u00e9llo \u2014 a na\u00efve first paragraph \U0001F600 with wide runes.")
	if first.Length == len([]rune("H\u00e9llo \u2014 a na\u00efve first paragraph \U0001F600 with wide runes.")) {
		t.Fatal("the fixture is all single-byte; it cannot tell bytes from runes")
	}

	got := mustAssemble(t, doc,
		assemble.Replacement{Span: spanOf(t, doc, "Second paragraph here."), Text: "Deuxi\u00e8me \u2014 replaced."},
		assemble.Replacement{Span: spanOf(t, doc, "Third paragraph here."), Text: "Troisi\u00e8me."},
	)
	want := "H\u00e9llo \u2014 a na\u00efve first paragraph \U0001F600 with wide runes.\n\n" +
		"Deuxi\u00e8me \u2014 replaced.\n\nTroisi\u00e8me."
	if string(got) != want+"\n" {
		t.Errorf("assembled\n%q\nwant\n%q", got, want+"\n")
	}
}

// Building from doc.Normalized() rather than doc.Raw() passes every fixture
// above, because they are all already in NFC. It must not: the document on disk
// is the author's bytes, and silently composing their combining sequences is a
// change they did not ask for. RunText returns NFC while Raw keeps the source
// form, so the two also differ in LENGTH here — an assembler working from the
// normalized string shifts every later span as well as rewriting the text.
func TestUntouchedSourceBytesAreNotNormalized(t *testing.T) {
	// Decomposed: e + U+0301, i + U+0308.
	const nfd = "Cafe\u0301 re\u0301sume\u0301 \u2014 a nai\u0308ve first paragraph."
	if norm.NFC.String(nfd) == nfd {
		t.Fatal("the fixture is already in NFC; it could not detect normalization")
	}
	source := nfd + "\n\nSecond paragraph here.\n\nThird paragraph here.\n"

	doc := admit(t, source)
	if doc.Normalized() == string(doc.Raw()) {
		t.Fatal("Normalized and Raw agree on this fixture; it could not detect the substitution")
	}

	got := mustAssemble(t, doc, assemble.Replacement{
		Span: spanOf(t, doc, "Second paragraph here."), Text: "Replaced.",
	})
	want := nfd + "\n\nReplaced.\n\nThird paragraph here.\n"
	if string(got) != want {
		t.Errorf("assembled\n%q\nwant\n%q", got, want)
	}
}

// Replacing only the last leaf must not disturb what precedes it, and replacing
// only the first must not disturb what follows.
func TestReplacingOneEndLeavesTheOtherIntact(t *testing.T) {
	for _, c := range []struct{ name, target, want string }{
		{"first", "First paragraph here.", "Changed.\n\nSecond paragraph here.\n\nThird paragraph here.\n"},
		{"last", "Third paragraph here.", "First paragraph here.\n\nSecond paragraph here.\n\nChanged.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := admit(t, doc3)
			got := mustAssemble(t, doc, assemble.Replacement{Span: spanOf(t, doc, c.target), Text: "Changed."})
			if string(got) != c.want {
				t.Errorf("assembled\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A span must name an included leaf, exactly
// ---------------------------------------------------------------------------

// A range inside a leaf has no verdict attached to it, so it is refused rather
// than spliced. Every offset here is a real grapheme boundary, so this is about
// the span having no meaning, not about it being malformed.
func TestASpanInsideALeafIsRefused(t *testing.T) {
	doc := admit(t, doc3)
	leaf := spanOf(t, doc, "Second paragraph here.")
	for _, c := range []struct {
		name string
		span text.Span
	}{
		{"prefix", text.Span{Offset: leaf.Offset, Length: leaf.Length - 1}},
		{"suffix", text.Span{Offset: leaf.Offset + 1, Length: leaf.Length - 1}},
		{"middle", text.Span{Offset: leaf.Offset + 1, Length: leaf.Length - 2}},
		{"one byte past the end", text.Span{Offset: leaf.Offset, Length: leaf.Length + 1}},
		{"one byte before the start", text.Span{Offset: leaf.Offset - 1, Length: leaf.Length + 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, doc, assemble.ErrNotALeaf, assemble.Replacement{Span: c.span, Text: "Replaced."})
		})
	}
}

// A leaf the structure pass excluded was never scored and never rewritten, so
// naming one is refused even though it is a real leaf with a real span.
func TestAnExcludedLeafCannotBeReplaced(t *testing.T) {
	source := "# A heading\n\n> A quoted paragraph\n> across two lines.\n\nA normal paragraph here.\n\n```\ncode block\n```\n"
	doc := admit(t, source)
	for _, c := range []struct{ name, run string }{
		{"heading", "A heading"},
		{"block-quoted paragraph", "A quoted paragraph\nacross two lines."},
		{"code block", "code block"},
	} {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, doc, assemble.ErrNotALeaf,
				assemble.Replacement{Span: anyLeafSpan(t, doc, c.run), Text: "Replaced."})
		})
	}
}

// A span pointing outside the document entirely.
func TestASpanOutsideTheDocumentIsRefused(t *testing.T) {
	doc := admit(t, doc3)
	for _, c := range []struct {
		name string
		span text.Span
	}{
		{"past the end", text.Span{Offset: len(doc3) + 10, Length: 4}},
		{"negative offset", text.Span{Offset: -1, Length: 4}},
		{"negative length", text.Span{Offset: 0, Length: -1}},
		{"zero length", text.Span{Offset: 0, Length: 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			mustRefuse(t, doc, assemble.ErrNotALeaf, assemble.Replacement{Span: c.span, Text: "Replaced."})
		})
	}
}

// ---------------------------------------------------------------------------
// Ordering and overlap
// ---------------------------------------------------------------------------

func TestUnorderedSpansAreRefused(t *testing.T) {
	doc := admit(t, doc3)
	mustRefuse(t, doc, assemble.ErrUnordered,
		assemble.Replacement{Span: spanOf(t, doc, "Third paragraph here."), Text: "C."},
		assemble.Replacement{Span: spanOf(t, doc, "First paragraph here."), Text: "A."},
	)
}

// Leaves never overlap each other, so the only reachable overlap is the same
// leaf named twice — two decisions about one paragraph, with no defined result.
// Refused rather than resolved by precedence.
func TestTheSameLeafNamedTwiceIsRefused(t *testing.T) {
	doc := admit(t, doc3)
	span := spanOf(t, doc, "Second paragraph here.")
	mustRefuse(t, doc, assemble.ErrOverlap,
		assemble.Replacement{Span: span, Text: "One rewrite."},
		assemble.Replacement{Span: span, Text: "A different rewrite."},
	)
}

// ---------------------------------------------------------------------------
// Excisions
// ---------------------------------------------------------------------------

// The measured cases. Bold, links, em-dashes and a wrapped line carry no
// excisions and must stay rewritable — if this half fails the gate has become
// uselessly strict, which no refusal test would reveal.
func TestLeavesWithoutExcisionsStayRewritable(t *testing.T) {
	for _, c := range []struct{ name, source, run string }{
		{"plain", "A plain paragraph here.\n", "A plain paragraph here."},
		{"wrapped", "A paragraph that happens\nto wrap across lines.\n", "A paragraph that happens\nto wrap across lines."},
		{"bold", "A paragraph with **bold** in it.\n", "A paragraph with **bold** in it."},
		{"link", "A paragraph with a [link](https://example.com) in it.\n", "A paragraph with a [link](https://example.com) in it."},
		{"punctuation", "A paragraph — with dashes — and \"quotes\".\n", "A paragraph — with dashes — and \"quotes\"."},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := admit(t, c.source)
			got := mustAssemble(t, doc, assemble.Replacement{Span: spanOf(t, doc, c.run), Text: "Rewritten."})
			if want := "Rewritten.\n"; string(got) != want {
				t.Errorf("assembled %q, want %q", got, want)
			}
		})
	}
}

// And the two that do carry them. The run text the loop rewrote does not
// contain the code span or the reference, so splicing over their bytes would
// delete them.
func TestALeafWithAnExcisionIsRefused(t *testing.T) {
	for _, c := range []struct{ name, source, run string }{
		{"inline code", "A paragraph with `code` inline.\n", "A paragraph with  inline."},
		{"footnote reference", "A paragraph with a footnote.[^1]\n\n[^1]: The note.\n", "A paragraph with a footnote."},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := admit(t, c.source)
			mustRefuse(t, doc, assemble.ErrExcision,
				assemble.Replacement{Span: spanOf(t, doc, c.run), Text: "Rewritten."})
		})
	}
}

// The refusal is what protects the content, so this states what would be lost
// and then requires the refusal in the same test: the excised bytes are inside
// the leaf's raw span, and absent from the run text the loop rewrote.
func TestTheExcisedBytesAreTheOnesAtRisk(t *testing.T) {
	doc := admit(t, "A paragraph with `code` inline.\n")
	const run = "A paragraph with  inline."
	span := spanOf(t, doc, run)

	raw := string(doc.Raw()[span.Offset : span.Offset+span.Length])
	if !strings.Contains(raw, "`code`") {
		t.Fatalf("the fixture is wrong: raw leaf %q does not contain the code span", raw)
	}
	if strings.Contains(run, "`code`") {
		t.Fatal("the run text still contains the code span; the premise of the rule is gone")
	}
	mustRefuse(t, doc, assemble.ErrExcision, assemble.Replacement{Span: span, Text: "Rewritten."})
}

// ---------------------------------------------------------------------------
// All or nothing
// ---------------------------------------------------------------------------

// A file half in the author's voice and half in the model's is worse than an
// error. One good replacement beside one bad one must produce no bytes at all —
// not the good one applied, not the original returned as a consolation.
func TestOneBadReplacementDiscardsTheGoodOnes(t *testing.T) {
	source := "First paragraph here.\n\nA paragraph with `code` inline.\n\nThird paragraph here.\n"
	doc := admit(t, source)
	good := assemble.Replacement{Span: spanOf(t, doc, "First paragraph here."), Text: "Rewritten first."}
	bad := assemble.Replacement{Span: spanOf(t, doc, "A paragraph with  inline."), Text: "Rewritten second."}
	third := assemble.Replacement{Span: spanOf(t, doc, "Third paragraph here."), Text: "Rewritten third."}

	got, err := assemble.Assemble(doc, []assemble.Replacement{good, bad, third})
	if err == nil {
		t.Fatalf("accepted a batch containing a refused replacement: %q", got)
	}
	if got != nil {
		t.Errorf("produced %d bytes despite refusing: %q", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// The output is itself a document
// ---------------------------------------------------------------------------

// Whatever comes out must be admissible, or the tool has written a file it
// cannot read back. Invalid UTF-8 in a replacement is the way that breaks.
func TestInvalidUTF8InAReplacementIsRefused(t *testing.T) {
	doc := admit(t, doc3)
	mustRefuse(t, doc, assemble.ErrInvalidText, assemble.Replacement{
		Span: spanOf(t, doc, "Second paragraph here."),
		Text: "valid then \xff\xfe invalid",
	})
}

// An empty replacement deletes a paragraph and leaves its blank lines behind.
// The acceptance loop compares distances between real texts and can never
// propose it, so it is a caller error rather than a decision to honour.
func TestAnEmptyReplacementIsRefused(t *testing.T) {
	doc := admit(t, doc3)
	mustRefuse(t, doc, assemble.ErrInvalidText,
		assemble.Replacement{Span: spanOf(t, doc, "Second paragraph here."), Text: ""})
}

func TestTheOutputCanBeAdmittedAgain(t *testing.T) {
	doc := admit(t, doc3)
	got := mustAssemble(t, doc,
		assemble.Replacement{Span: spanOf(t, doc, "First paragraph here."), Text: "A rewritten first paragraph — with punctuation."},
		assemble.Replacement{Span: spanOf(t, doc, "Third paragraph here."), Text: "A rewritten third paragraph."},
	)
	again, err := text.Admit(got)
	if err != nil {
		t.Fatalf("the assembled document is not admissible: %v", err)
	}
	if leaves := len(again.Structure(text.DefaultStructureOptions()).IncludedLeaves()); leaves != 3 {
		t.Errorf("the assembled document has %d included leaves, want 3", leaves)
	}
}

// Non-ASCII replacement text survives byte for byte, with no normalization
// applied on the way out.
func TestNonASCIIReplacementTextIsNotNormalized(t *testing.T) {
	doc := admit(t, doc3)
	// Genuinely decomposed: e + U+0301 COMBINING ACUTE, which NFC collapses to a
	// single precomposed rune. Written precomposed, this test would pass against
	// an implementation that normalizes on the way out.
	const decomposed = "Cafe\u0301 re\u0301sume\u0301 \u2014 nai\u0308ve."
	got := mustAssemble(t, doc, assemble.Replacement{
		Span: spanOf(t, doc, "Second paragraph here."), Text: decomposed,
	})
	if norm.NFC.String(decomposed) == decomposed {
		t.Fatal("the fixture is already in NFC; it could not detect normalization")
	}
	if !bytes.Contains(got, []byte(decomposed)) {
		t.Errorf("the decomposed form did not survive: %q", got)
	}
}

// ---------------------------------------------------------------------------
// The BOM
// ---------------------------------------------------------------------------

// Admit strips a leading BOM and every offset is relative to the stripped
// bytes. Assembling from those alone would rewrite the file's encoding preamble
// as a side effect of editing its prose, on every BOM-carrying file.
func TestAStrippedBOMIsPutBack(t *testing.T) {
	const bom = "\ufeff"
	doc := admit(t, bom+doc3)
	if !doc.HadBOM() {
		t.Fatal("the fixture did not carry a BOM")
	}

	unchanged := mustAssemble(t, doc)
	if want := bom + doc3; string(unchanged) != want {
		t.Errorf("with no replacements, assembled %q, want %q", unchanged, want)
	}

	// Two replacements and the whole expected document. Checking only for a
	// leading BOM would pass an implementation that prepends it and then
	// resolves stripped-coordinate spans against the BOM-prefixed buffer,
	// placing every replacement three bytes early.
	rewritten := mustAssemble(t, doc,
		assemble.Replacement{Span: spanOf(t, doc, "First paragraph here."), Text: "One."},
		assemble.Replacement{Span: spanOf(t, doc, "Third paragraph here."), Text: "Three."},
	)
	want := bom + "One.\n\nSecond paragraph here.\n\nThree.\n"
	if string(rewritten) != want {
		t.Errorf("assembled\n%q\nwant\n%q", rewritten, want)
	}
}

// And a document without one does not acquire it.
func TestADocumentWithoutABOMDoesNotGainOne(t *testing.T) {
	doc := admit(t, doc3)
	got := mustAssemble(t, doc, assemble.Replacement{
		Span: spanOf(t, doc, "Second paragraph here."), Text: "Replaced.",
	})
	if strings.HasPrefix(string(got), "\ufeff") {
		t.Errorf("a BOM appeared from nowhere: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Housekeeping
// ---------------------------------------------------------------------------

func TestANilDocumentIsRefused(t *testing.T) {
	got, err := assemble.Assemble(nil, nil)
	if err == nil {
		t.Fatalf("accepted a nil document: %q", got)
	}
	if !errors.Is(err, assemble.ErrMissingInput) {
		t.Errorf("error = %v, want %v", err, assemble.ErrMissingInput)
	}
	if got != nil {
		t.Errorf("produced %d bytes for a nil document", len(got))
	}
}

func TestAssemblyIsDeterministic(t *testing.T) {
	doc := admit(t, doc3)
	replacements := []assemble.Replacement{
		{Span: spanOf(t, doc, "First paragraph here."), Text: "One."},
		{Span: spanOf(t, doc, "Third paragraph here."), Text: "Three."},
	}
	first := mustAssemble(t, doc, replacements...)
	for i := 0; i < 5; i++ {
		if again := mustAssemble(t, doc, replacements...); !bytes.Equal(first, again) {
			t.Fatalf("run %d differs:\n%q\n%q", i, first, again)
		}
	}
}

// The caller's slice is theirs; assembling must not sort or otherwise reorder
// it in place, which an implementation that sorts to check ordering would do.
func TestTheCallersSliceIsNotModified(t *testing.T) {
	doc := admit(t, doc3)
	replacements := []assemble.Replacement{
		{Span: spanOf(t, doc, "Third paragraph here."), Text: "C."},
		{Span: spanOf(t, doc, "First paragraph here."), Text: "A."},
	}
	before := append([]assemble.Replacement(nil), replacements...)
	_, _ = assemble.Assemble(doc, replacements)
	for i := range before {
		if replacements[i] != before[i] {
			t.Errorf("replacement %d was modified: %v -> %v", i, before[i], replacements[i])
		}
	}
}

// All-or-nothing has to hold across the BOM boundary too: a refusal must return
// nothing, not a buffer that already has the BOM prepended to it.
func TestARefusalOnABOMDocumentReturnsNothing(t *testing.T) {
	doc := admit(t, "\ufeffFirst paragraph here.\n\nA paragraph with `code` inline.\n\nThird paragraph here.\n")
	if !doc.HadBOM() {
		t.Fatal("the fixture did not carry a BOM")
	}
	mustRefuse(t, doc, assemble.ErrExcision,
		assemble.Replacement{Span: spanOf(t, doc, "First paragraph here."), Text: "One."},
		assemble.Replacement{Span: spanOf(t, doc, "A paragraph with  inline."), Text: "Two."},
	)
}
