package text_test

// #102. A figure caption is truncated when its text lowercases to fewer bytes.
//
// `htmlLeafNodes` locates the `<figcaption>` tags by searching a
// `bytes.ToLower` COPY of the block and then applies those offsets to the
// ORIGINAL bytes. Lower-casing is not length-preserving, so every offset after
// a shrinking character is short by the difference.
//
// Measured, the two that shrink in ordinary prose:
//
//	U+0130 İ  2 bytes -> 1   (LATIN CAPITAL LETTER I WITH DOT ABOVE)
//	U+1E9E ẞ  3 bytes -> 2   (LATIN CAPITAL LETTER SHARP S)
//
// Both are real letters in Turkish and German respectively, not contrived
// inputs.
//
// # Why this is not only a wrong byte count
//
// The lost bytes come off the END of the caption, so a word CAN straddle the
// span's end. `RunTokens` counts a token only when it is WHOLLY CONTAINED in
// the leaf span, so a straddling word is dropped and the caption measures one
// lexical token fewer than it has, changing its feature vector and its
// distance.
//
// Whether that happens depends on what the lost bytes are. Measured: "İ test"
// loses its final `t` and drops to one lexical token, while "İ test." loses
// only the period and keeps both. So the consequence is real but conditional,
// and these fixtures demonstrate it for the unpunctuated case rather than
// establishing it for every affected caption.
//
// It is the same family as the `strings.ToLower` versus `cases.Fold`
// distinction DESIGN already records for the function-word test: a
// transformation applied for comparison must not be used to index the thing it
// was derived from.
//
// # What these tests pin
//
// That the three leaves `htmlLeafNodes` produces — the HTML before the caption,
// the caption itself, and the HTML after it — all carry spans into the original
// bytes. The caption is the one that matters for measurement, but the offsets
// are shared, so a shrinking character anywhere in the block can move any of
// them. Both fixtures assert all three leaves through one `assertTiling`
// helper — spans, bytes, and that they tile with no gap or overlap.
//
// Offsets are computed from the pieces that build each fixture rather than
// written as literals, so a fixture edit cannot silently make an assertion
// describe a different input.
//
// # Guarding the behaviour, not only the bug
//
// Two plausible REPAIRS would fix every offset here and break something else,
// so the table carries fixtures for both:
//
//	searching the raw bytes      loses `<FIGCAPTION>`; ToLower is there to make
//	                             the match case-insensitive
//	a regex using `.`            loses multiline captions, since `.` excludes
//	                             newlines
//	a regex using `[^<]*`        loses captions containing inline markup
//	a greedy regex               runs the first caption through the second
//	                             figure when both sit in one HTML block
//	trimming the capture         hands the caption's boundary whitespace to the
//	                             HTML leaves; it still tiles, and is still wrong
//	flipping to ToUpper          moves the same defect onto dotless ı, which
//	                             shrinks upward instead of downward
//	compensating by prefix delta still indexes raw bytes with transformed
//	                             coordinates; survives every fixture whose
//	                             shrinking letter is not last
//	searching doc.Resolve(span)  NFC coordinates, which differ from raw ones
//	detecting shrinking runes    handles cancellation, misses pure expansion
//	normalizing CRLF first       truncates a caption containing a CRLF
//	compensating the close with   folds transformations occurring AFTER the
//	the whole-block delta         closing tag into the caption's boundary
//	mapping against the document  mixes document and block coordinates, so
//	rather than the block         anything before the block shifts the caption
//	a length-check fallback      never fires when a loss and a gain cancel
//
// Both were found by asking what a fix would break rather than what the bug
// does, which is a different question and the one these fixtures answer.

import (
	"testing"

	"github.com/fissible/hapax/internal/text"
)

const (
	figOpen  = "<figure>\n<figcaption>"
	figClose = "</figcaption>\n</figure>"
)

// caption assembles a figure block whose tags carry the given spelling, and
// returns the source with the exact spans each of the three leaves must report.
//
// The tag spelling is a parameter because `bytes.ToLower` is there to make the
// match CASE-INSENSITIVE, and every fixture here once used lowercase. A repair
// that simply searched the raw bytes would have fixed every offset and silently
// stopped recognizing `<FIGCAPTION>` — passing the whole package while breaking
// the behaviour the buggy line exists to provide. Measured: all three spellings
// are recognized today, and all three are truncated identically.
func caption(tag, body string) (src, lead, trail string, offset int) {
	lead = "<figure>\n<" + tag + ">"
	trail = "</" + tag + ">\n</figure>"
	return lead + body + trail + "\n", lead, trail, len(lead)
}

// assertTiling checks that the three leaves cover the block exactly: the HTML
// before the caption, the caption, and the HTML after it, with no gap and no
// overlap, each selecting the bytes it should.
//
// One function rather than assertions at each call site, because the call sites
// are where this keeps going wrong. Twice now a mutation survived by moving a
// boundary that only ONE fixture happened to check — the trailing extent, then
// the leading one — while a comment claimed both were pinned. A caller cannot
// check one and skip the other here.
func assertTiling(t *testing.T, src string, root, caption *text.Node, lead, trail string) {
	t.Helper()
	var before, after *text.Node
	for _, node := range allLeaves(root) {
		if node.Role != text.RoleHTMLBlock {
			continue
		}
		if node.Span.Offset < caption.Span.Offset {
			before = node
		} else {
			after = node
		}
	}
	if before == nil || after == nil {
		t.Fatalf("want an HTML leaf either side of the caption; before=%v after=%v",
			before != nil, after != nil)
	}
	for _, c := range []struct {
		name string
		node *text.Node
		want string
	}{
		{"leading", before, lead},
		{"trailing", after, trail},
	} {
		if got := c.node.Span.Length; got != len(c.want) {
			t.Errorf("the %s HTML leaf is %d bytes, want %d", c.name, got, len(c.want))
			continue
		}
		if got := src[c.node.Span.Offset : c.node.Span.Offset+c.node.Span.Length]; got != c.want {
			t.Errorf("the %s HTML leaf spans\n  %q\nwant exactly\n  %q", c.name, got, c.want)
		}
	}
	// And they tile: no gap, no overlap.
	if got, want := before.Span.Offset+before.Span.Length, caption.Span.Offset; got != want {
		t.Errorf("the leading HTML leaf ends at %d, want %d — where the caption begins",
			got, want)
	}
	if got, want := after.Span.Offset, caption.Span.Offset+caption.Span.Length; got != want {
		t.Errorf("the trailing HTML leaf starts at %d, want %d — where the caption ends",
			got, want)
	}
}

// A caption whose text shrinks under lower-casing keeps its whole span.
//
// Under the defect the reported length is short by one byte per İ, so the
// caption's final character falls outside the span it claims.
func TestACaptionKeepsItsLastByteWhenItsTextShrinksWhenLowercased(t *testing.T) {
	// `resolved` is what `Resolve` should return: the NFC form, which is NOT
	// always the raw body. Empty means "same as body"; the decomposed row sets
	// it, because asserting the raw bytes there would reject a CORRECT
	// implementation.
	for _, c := range []struct {
		name     string
		tag      string
		body     string
		resolved string
	}{
		{"dotted capital I", "figcaption", "İ test", ""},
		{"capital sharp s", "figcaption", "ẞ test", ""},
		{"several shrinking letters", "figcaption", "İİẞ test", ""},
		// Uppercase and mixed-case tags, which is what ToLower is FOR. Without
		// these a repair that searched raw bytes would fix every offset and
		// stop recognizing these spellings, passing the whole package.
		{"uppercase tag", "FIGCAPTION", "İ test", ""},
		{"mixed-case tag", "FigCaption", "İ test", ""},
		// A caption written over two lines. A repair using a regex with `.`
		// would correct every offset above and silently stop recognizing these,
		// because `.` excludes newlines — measured, "plain\ntest" is a caption
		// today and disappears entirely under that repair.
		{"multiline body", "figcaption", "İ test\nand more", ""},
		{"multiline ascii", "figcaption", "plain\ntest", ""},
		// Inline markup inside the caption. A repair matching `[^<]*` between the
		// tags would correct every offset above and stop recognizing these —
		// measured, "plain <em>test</em>" is a caption today at [21,+19).
		{"inline markup", "figcaption", "İ plain <em>test</em>", ""},
		{"inline markup ascii", "figcaption", "plain <em>test</em>", ""},
		// Whitespace at the caption boundaries, which belongs to the CAPTION.
		// A repair that trimmed it — `\s*(.*?)\s*` — tiles perfectly and still
		// reassigns those bytes to the HTML leaves. Measured, "\nplain test\n"
		// keeps its span [21,+12) today.
		{"boundary whitespace", "figcaption", "\nİ test\n", ""},
		{"boundary whitespace ascii", "figcaption", "\nplain test\n", ""},
		// Dotless ı, which shrinks under UPPER-casing rather than lower. Correct
		// today at [21,+7); it breaks if the repair merely flips the case
		// direction instead of fixing the indexing. Measured:
		//
		//	İ  raw 2  lower 1  upper 2
		//	ẞ  raw 3  lower 2  upper 3
		//	ı  raw 2  lower 2  upper 1
		//
		// So no single case direction is safe, which is the point: the fix has
		// to stop indexing one buffer with another's offsets, not pick a better
		// transformation.
		// Expansion with NO shrink anywhere. U+023A grows by a byte, so the
		// boundary OVERSHOOTS instead of falling short — measured, the caption
		// spans "U+023A test<", swallowing the `<` of the closing tag. A repair
		// that detects shrinking runes and falls back would handle
		// cancellation and miss this entirely.
		{"expansion only", "figcaption", "\u023a test", ""},
		// CRLF inside the caption, correct today at 11 bytes and two words. A
		// repair that normalized line endings before searching would truncate
		// it.
		{"crlf body", "figcaption", "plain\r\ntest", ""},
		{"dotless i", "figcaption", "ı test", ""},
		// The control: ASCII behaviour must not change while the Unicode cases
		// are repaired. (An earlier comment justified it as catching an
		// implementation that stopped producing captions altogether. That is
		// false — the Unicode cases already fail at onlyRole in that case.)
		// Decomposed text. A repair that searched `doc.Resolve(span)` would fold
		// ASCII correctly and still use NFC coordinates, which differ from raw
		// ones. Measured, this caption is [21,+8) raw today and resolves to the
		// PRECOMPOSED form — so the raw bytes and the resolved text are
		// deliberately different expectations here.
		{"decomposed", "figcaption", "e\u0301 test", "\u00e9 test"},
		{"ascii only", "figcaption", "plain test", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			src, lead, trail, offset := caption(c.tag, c.body)

			doc, root := structure(t, src, text.DefaultStructureOptions())
			leaf := onlyRole(t, root, text.RoleCaption)

			if got, want := leaf.Span, (text.Span{Offset: offset, Length: len(c.body)}); got != want {
				t.Errorf("caption span = %+v, want %+v", got, want)
			}
			if got := src[leaf.Span.Offset : leaf.Span.Offset+leaf.Span.Length]; got != c.body {
				t.Errorf("caption span selects\n  %q\nwant exactly\n  %q", got, c.body)
			}
			wantResolved := c.resolved
			if wantResolved == "" {
				wantResolved = c.body
			}
			if got := resolve(t, doc, leaf); got != wantResolved {
				t.Errorf("caption resolves to %q, want %q", got, wantResolved)
			}

			assertTiling(t, src, root, leaf, lead, trail)
		})
	}
}

// Two figures in one HTML block: the first caption ends at the FIRST closing
// tag.
//
// Goldmark puts adjacent figures in a single HTML block, and `htmlLeafNodes`
// extracts only the first caption — the second is left inside the trailing HTML
// leaf. That is the current contract, and it is worth pinning because a repair
// using a greedy `(?is)<figcaption>(.*)</figcaption>` passes every other test
// here while extending the first caption through the second figure: measured by
// the reviewer, length 72 instead of 13 and words 8 instead of 2.
//
// The trailing remainder is asserted whole, so the second figure's bytes have
// to still be there.
func TestOnlyTheFirstCaptionIsExtractedFromABlockWithTwoFigures(t *testing.T) {
	const lead = "<figure>\n<figcaption>"
	const body = "İ first"
	const rest = "</figcaption>\n</figure>\n<figure>\n<figcaption>second one</figcaption>\n</figure>"
	src := lead + body + rest + "\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	leaf := onlyRole(t, root, text.RoleCaption)

	if got, want := leaf.Span, (text.Span{Offset: len(lead), Length: len(body)}); got != want {
		t.Errorf("first caption span = %+v, want %+v", got, want)
	}
	if got := resolve(t, doc, leaf); got != body {
		t.Errorf("first caption resolves to %q, want %q", got, body)
	}
	assertTiling(t, src, root, leaf, lead, rest)
}

// The inputs that PANIC rather than truncate, and the ones that defeat a
// length-check repair.
//
// # Why these crash
//
// When a transformed offset lands anywhere that is not a GRAPHEME boundary,
// structure.go's invariant fires:
//
//	text: structurally invalid leaf: span is not a grapheme boundary
//
// So this is a CRASH on valid Markdown, not only a wrong measurement.
//
// The condition is NOT "the caption ends in a shrinking letter", which an
// earlier version of this comment claimed. Measured, all of these panic today:
//
//	<figcaption>test I-dot</figcaption>        the shrink is last
//	<figcaption>I-dot test e-acute</…>         the shrink is not last; the
//	                                           boundary lands inside a stable rune
//	data-note="I-dot" … <figcaption>test e-acute</…>
//	                                           the shrink is in the ATTRIBUTE and
//	                                           the caption is pure ASCII
//
// What matters is only whether the mis-set boundary lands somewhere that is not
// a grapheme boundary — which is WIDER than "inside a rune". Measured,
// "U+0130 U+0130 test e U+0301" ends at 32 instead of 34, which is a perfectly
// valid rune boundary sitting between the `e` and its combining acute. It
// splits a grapheme, and it panics. An earlier version of this comment said
// "mid-rune", which is the narrower and wrong condition.
//
// # Cancellation
//
// Measured: U+0130 is 2 bytes and lowercases to 1; U+023A is 2 and lowercases
// to 3. One loses a byte, one gains one.
//
// A repair that checks `len(lower) != len(raw)` and falls back when they differ
// looks reasonable and passes every other fixture here — but with one of each
// the totals match, the fallback never runs, and the defect survives.
//
// # The leading leaf crashes too
//
// The same offsets build all three leaves, so the block BEFORE `<figcaption>`
// can end off a grapheme boundary and panic before the caption is built. The trailing
// leaf is the exception: its end is `len(raw)`, so it has no independently
// transformed end of its own.
//
// # Its own function, and no recover
//
// A panic aborts the test binary, so `-run` must be able to exclude these for a
// clean baseline of the table-driven cases. There is deliberately no `recover`:
// recovering would weaken this to "does not crash" when what it must assert is
// the right span.
func TestTheSpansThatLandMidRune(t *testing.T) {
	const noteOpen = "<figure data-note=\""
	// `resolved` as in the table above: NFC, not the raw body. Omitting it here
	// made the decomposed row below reject a CORRECT implementation — I added
	// the field to the other table and not this one, in the same edit.
	for _, c := range []struct {
		name     string
		lead     string
		body     string
		resolved string
	}{
		{"shrink at the caption's end", figOpen, "test \u0130", ""},
		{"shrink at the end, three bytes", figOpen, "test \u1e9e", ""},
		{"caption is only a shrinking letter", figOpen, "\u0130", ""},
		{"boundary lands in a stable rune", figOpen, "\u0130 test \u00e9", ""},
		// The shrink is in the ATTRIBUTE; the caption carries no shrinking
		// letter of its own. (An earlier name called this caption "ascii",
		// which it is not — it ends in U+00E9, and that is the rune the bad
		// boundary lands inside.)
		{"shrink in the attribute only",
			noteOpen + "\u0130\">\n<figcaption>", "test \u00e9", ""},
		// A loss and a gain cancel, so the block's lowered length equals its raw
		// length and a length-check repair never fires.
		{"a loss and a gain cancel",
			noteOpen + "\u023a\">\n<figcaption>", "\u0130 test", ""},
		{"cancelling, and the leading leaf ends off a boundary",
			noteOpen + "\u023a\">\n<figcaption>", "\u00e9 \u0130 test", ""},
		// The boundary lands at a valid RUNE boundary that splits a GRAPHEME:
		// between `e` and its combining acute. Measured, end 32 instead of 34.
		{"boundary splits a grapheme, not a rune", figOpen, "\u0130\u0130 test e\u0301", "\u0130\u0130 test \u00e9"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := c.lead + c.body + figClose + "\n"

			doc, root := structure(t, src, text.DefaultStructureOptions())
			leaf := onlyRole(t, root, text.RoleCaption)

			if got, want := leaf.Span, (text.Span{Offset: len(c.lead), Length: len(c.body)}); got != want {
				t.Errorf("caption span = %+v, want %+v", got, want)
			}
			wantResolved := c.resolved
			if wantResolved == "" {
				wantResolved = c.body
			}
			if got := resolve(t, doc, leaf); got != wantResolved {
				t.Errorf("caption resolves to %q, want %q", got, wantResolved)
			}
			assertTiling(t, src, root, leaf, c.lead, figClose)
		})
	}
}

// Case-changing Unicode AFTER the closing tag must not affect the caption.
//
// Every other fixture keeps its trailing content ASCII, so a repair that maps
// the opening boundary correctly and compensates the CLOSING one with the
// whole-block delta — `captionEnd += len(raw) - len(lower)` — passes the entire
// package. It folds transformations that occur after the closing tag into the
// caption's own boundary, which has nothing to do with them.
//
// Both directions are covered, because the compensation is signed: a trailing
// letter that grows and one that shrinks push the boundary opposite ways.
// Measured, the caption is [21,+7) in both cases and the trailing content is 38
// bytes either way.
func TestTrailingContentDoesNotMoveTheCaptionBoundary(t *testing.T) {
	const body = "\u0130 test"
	for _, c := range []struct {
		name  string
		trail string
	}{
		{"trailing letter grows", "</figcaption>\n<img alt=\"\u023a\">\n</figure>"},
		{"trailing letter shrinks", "</figcaption>\n<img alt=\"\u0130\">\n</figure>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := figOpen + body + c.trail + "\n"

			doc, root := structure(t, src, text.DefaultStructureOptions())
			leaf := onlyRole(t, root, text.RoleCaption)

			if got, want := leaf.Span, (text.Span{Offset: len(figOpen), Length: len(body)}); got != want {
				t.Errorf("caption span = %+v, want %+v", got, want)
			}
			if got := resolve(t, doc, leaf); got != body {
				t.Errorf("caption resolves to %q, want %q", got, body)
			}
			assertTiling(t, src, root, leaf, figOpen, c.trail)
		})
	}
}

// Content BEFORE the HTML block does not move the caption.
//
// Every other fixture starts its block at document offset zero, so a repair
// that maps lowered offsets against the whole document — `span.Offset +
// blockOffset`, mixing raw and transformed coordinates — passes the entire
// package. Confusing document and block coordinates is an ordinary mistake, and
// a document with a paragraph before a figure is ordinary input.
//
// Both directions again, since the error is signed. Measured, all three of
// these are CORRECT today: the caption body is ASCII here, so the defect does
// not reach it and these are behaviour controls rather than regressions.
func TestContentBeforeTheBlockDoesNotMoveTheCaption(t *testing.T) {
	const body = "plain test"
	for _, c := range []struct {
		name string
		pre  string
	}{
		{"preceded by a shrinking letter", "\u0130\n\n"},
		{"preceded by a growing letter", "\u023a\n\n"},
		{"preceded by ascii", "hello\n\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := c.pre + figOpen + body + figClose + "\n"

			doc, root := structure(t, src, text.DefaultStructureOptions())
			leaf := onlyRole(t, root, text.RoleCaption)

			want := text.Span{Offset: len(c.pre) + len(figOpen), Length: len(body)}
			if got := leaf.Span; got != want {
				t.Errorf("caption span = %+v, want %+v", got, want)
			}
			if got := resolve(t, doc, leaf); got != body {
				t.Errorf("caption resolves to %q, want %q", got, body)
			}
			// The leading HTML leaf here is the figure's opening tags only; the
			// preceding paragraph is a separate leaf and not part of the block.
			assertTiling(t, src, root, leaf, figOpen, figClose)
		})
	}
}

// The words survive, which is the measurement consequence.
//
// A truncated span drops its straddling final word under the whole-token
// containment rule, so the caption measures one lexical token fewer than it
// has.
//
// `Node.Words` is asserted as well as `RunTokens`, because they are derived at
// different times: Words is counted while the leaf is BUILT, RunTokens reads
// the span afterwards. A fix that repaired the spans without rebuilding the
// leaves left Words at 1 while RunTokens returned 2, and passed a version of
// this test that checked only the tokens.
//
// Words feeds `Sentential` and the `Words == 0` exclusion, but neither flips
// for this fixture: measured, "İ test" is Sentential=false and Included=true
// both before and after the fix, and only Words changes. It is the COUNT that
// is wrong here, not the admission — Sentential gates inclusion inside list
// items, which this caption is not in.
func TestATruncatedCaptionWouldLoseItsLastWord(t *testing.T) {
	const body = "İ test"
	src, _, _, _ := caption("figcaption", body)

	doc, root := structure(t, src, text.DefaultStructureOptions())
	leaf := onlyRole(t, root, text.RoleCaption)

	tokens, err := doc.RunTokens(leaf)
	if err != nil {
		t.Fatalf("RunTokens: %v", err)
	}
	var words []string
	for _, token := range tokens {
		if token.Lexical {
			words = append(words, token.Text)
		}
	}
	if len(words) != 2 {
		t.Fatalf("caption %q has %d lexical tokens (%v), want 2 — a short span drops "+
			"the straddling word", body, len(words), words)
	}
	if words[1] != "test" {
		t.Errorf("second lexical token is %q, want %q", words[1], "test")
	}
	if leaf.Words != 2 {
		t.Errorf("leaf.Words = %d, want 2 — counted when the leaf is built, so a span "+
			"repaired afterwards leaves this stale", leaf.Words)
	}
}

// A shrinking character BEFORE the caption moves every leaf, not just the
// caption.
//
// The offsets are applied to the leading HTML block and the trailing one as
// well, so this pins that the block preceding `<figcaption>` still covers its
// own bytes. Asserting only the caption would leave a fix that corrected one
// offset and not the others.
func TestAShrinkingCharacterBeforeTheCaptionDoesNotMoveTheOtherLeaves(t *testing.T) {
	const lead = "<figure data-note=\"\u0130\">\n<figcaption>"
	const body = "caption text"
	src := lead + body + figClose + "\n"

	_, root := structure(t, src, text.DefaultStructureOptions())

	leaf := onlyRole(t, root, text.RoleCaption)
	if got, want := leaf.Span, (text.Span{Offset: len(lead), Length: len(body)}); got != want {
		t.Fatalf("caption span = %+v, want %+v", got, want)
	}

	// The three leaves must TILE the block: the HTML before the caption ends
	// exactly where the caption starts, and the HTML after it starts exactly
	// where the caption ends. Under the defect every boundary is one byte
	// early, so the leading block stops mid-tag and the trailing block begins
	// inside the caption's last character.
	assertTiling(t, src, root, leaf, lead, figClose)
}

// allLeaves walks every leaf, included or not, because the HTML blocks around a
// caption are excluded by role and so never reach IncludedLeaves.
func allLeaves(root *text.Node) []*text.Node {
	var out []*text.Node
	var walk func(*text.Node)
	walk = func(n *text.Node) {
		if n == nil {
			return
		}
		if n.Kind == text.KindLeaf {
			out = append(out, n)
			return
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	return out
}
