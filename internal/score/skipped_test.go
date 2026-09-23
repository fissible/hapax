package score_test

// #98. Raising the paragraph floor in #92 broke something that worked by
// accident.
//
// At a floor of one essentially nothing was excluded, so a scored index WAS the
// paragraph a reader counted in their file. At ten it is not, and `score`
// reports only filtered indices and a count:
//
//	paragraphs_below_floor: 2
//	segments: [ { index: 0, lexical_tokens: 27, ... } ]
//
// A person scoring an eleven-paragraph post sees nine segments and a 2, with no
// way to tell WHICH two, and no way to map `index 0` back to their own text.
//
// Score and rewrite agree with each other, so `--paragraphs` still selects what
// `score` printed — that contract is intact and is not what this fixes. What is
// missing is the bridge from an index to the source.
//
// # Two additions
//
// Every scored segment carries its source span, and every SKIPPED paragraph is
// reported with its span, its token count, and the floor it failed. A count
// alone cannot be acted on; a span can be found in the file.
//
// # Why the count stays
//
// `ParagraphsBelowFloor` is also reported by `index` and `profile`, where the
// unit is a whole corpus and per-paragraph detail would be enormous. It stays
// there and stays here, and its agreement with the list is asserted over
// several shapes rather than assumed from one.
//
// # Spans are exact
//
// A paragraph leaf's span carries no trailing separator: measured, a leaf at
// offset 0 of a 64-character paragraph has length 64, and the next begins at 66,
// skipping the blank line. So these assertions compare BYTES with no trimming.
// An earlier version trimmed, which accepts a boundary that absorbs separator
// whitespace while still being disjoint.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/score"
)

// The fixture interleaves short and long paragraphs, because a mapping that is
// off by the NUMBER of skipped paragraphs is correct on a grouped fixture and
// wrong on this one.
//
// `café` is deliberate: it puts a multibyte rune before the later spans, so a
// byte offset and a character offset diverge and a rune-counting implementation
// cannot pass.
const (
	shortA = "Yes."
	longA  = "The café stores every record in a signed chain and verifies it on read."
	shortB = "It broke."
	longB  = "Verification points at the entry that changed, which is the whole point of the design."
)

// An inline-code pair, one either side of the floor.
//
// A paragraph's raw span covers its WHOLE source, backticked runs included,
// while its lexical count excludes them — structure.go records those runs as
// excisions and a token is counted only when it overlaps none of them. The two
// facts are independent, and a mapping that subtracted each excision's length
// from the reported span passed the whole suite: without a fixture containing
// an excision there was nothing to subtract.
//
// The counts here are MEASURED, not derived: 16 and 2.

// A WRAPPED paragraph, one either side of the floor.
//
// Every reported paragraph above is a single line, so truncating each span at
// its first newline preserved scoring and token counts and passed the whole
// suite. Measured, these are one leaf each: the survivor is 72 bytes across two
// lines and the skip is 12. Under the mutation they report 47 and 4.
//
// Hard-wrapped prose is the common case, not an edge one — a paragraph written
// in any editor that wraps at eighty columns has newlines in it.
const (
	wrapKeep = "The café stores every record in a signed chain\nand verifies it on read."
	wrapSkip = "Yes.\nIndeed."
)

const (
	codeKeep = "Run `hapax index --profile posts` before every score you take, " +
		"or the profile you are measuring against quietly goes stale."
	codeSkip = "Use `--json` here."
)

// para is a paragraph and what should be reported about it. The offset is
// filled in by draftOf.
type para struct {
	text   string
	tokens int
	offset int
}

// draftOf assembles paragraphs into a draft and records each one's exact raw
// offset as it goes.
//
// Offsets come from CONSTRUCTION, never from searching the result. The previous
// helper used strings.Index, which returns the FIRST occurrence — so a draft
// containing the same paragraph twice expected both reports to point at the
// first, and a mapping that resolved every span by searching for its own text
// passed the entire suite while reporting the second occurrence at the first
// one's offset. Repeated paragraphs are ordinary in real writing ("Yes." twice
// in a dialogue), so this is not a contrived shape.
func draftOf(paras ...para) (string, []para) {
	var b strings.Builder
	out := make([]para, len(paras))
	for i, pa := range paras {
		if i > 0 {
			b.WriteString("\n\n")
		}
		pa.offset = b.Len()
		out[i] = pa
		b.WriteString(pa.text)
	}
	b.WriteString("\n")
	return b.String(), out
}

// chunk is a piece of a draft written verbatim. Only chunks marked `report`
// are expected to appear in a score report; the rest are markup or spacing that
// exists to move the paragraphs around.
type chunk struct {
	text   string
	tokens int
	report bool
	sep    string // written after this chunk
}

// layoutOf assembles a draft from chunks, recording the offset of each reported
// paragraph as it is written.
//
// draftOf separates every paragraph with exactly one blank line, which makes
// each offset a running total of `length + 2`. A mapping that computed offsets
// that way — never consulting the source at all — passed the whole suite.
// Measured, a real draft does not look like that: a heading and a block quote
// are excluded from the report entirely and a stray blank line widens a gap, so
// the paragraphs do not land at accumulated lengths.
//
// The numbers move whenever a case is added to this fixture, so they are not
// quoted here — read them off a failure message, or off `pick(all, ...)`, which
// is where they are actually derived. (An earlier version of this comment
// quoted 13, 20, 116 and 127 from the two-exclusion draft that first
// demonstrated the point, and went stale the moment the fence and the
// non-sentential cases were added.)
// layoutOf writes chunks verbatim with LF. layoutOfEOL writes the same layout
// with any line ending, converting the separators AND the newlines inside a
// wrapped paragraph.
//
// Parameterized because every fixture used LF, so normalizing CRLF to LF before
// admission — `text.Admit(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n")))`
// — left the whole suite passing while every reported offset was wrong against
// the file on disk. Measured, spans are over the ORIGINAL bytes: the wrapped
// survivor is 73 bytes under CRLF and 72 under LF, with the same token count.
//
// This is the same defect class as reporting spans over an NFC copy, and it
// reaches a whole platform: a draft written on Windows, or checked out with
// `core.autocrlf`, is CRLF throughout.
func layoutOf(chunks ...chunk) (string, []para) {
	return layoutOfEOL("\n", chunks...)
}

func layoutOfEOL(eol string, chunks ...chunk) (string, []para) {
	var b strings.Builder
	var out []para
	for _, c := range chunks {
		text := strings.ReplaceAll(c.text, "\n", eol)
		if c.report {
			out = append(out, para{text: text, tokens: c.tokens, offset: b.Len()})
		}
		b.WriteString(text)
		b.WriteString(strings.ReplaceAll(c.sep, "\n", eol))
	}
	return b.String(), out
}

// pick selects paragraphs by position, preserving order, so a test names one
// side of the partition without restating any offset.
func pick(all []para, at ...int) []para {
	out := make([]para, 0, len(at))
	for _, i := range at {
		out = append(out, all[i])
	}
	return out
}

// interleavedDraft is the standard fixture: short and long alternating, so a
// mapping off by the NUMBER of skipped paragraphs is wrong here rather than
// coincidentally right.
func interleavedDraft() (string, []para) {
	return draftOf(
		para{text: shortA, tokens: 1},
		para{text: longA, tokens: 14},
		para{text: shortB, tokens: 2},
		para{text: longB, tokens: 15},
	)
}

func interleaved() string {
	raw, _ := interleavedDraft()
	return raw
}

// ---------------------------------------------------------------------------
// Scored segments
// ---------------------------------------------------------------------------

// Every scored segment carries its index, its exact span, and its size — as one
// tuple.
//
// The tuple matters. An earlier version checked spans in order without checking
// the index, so correct spans labelled with the ORIGINAL paragraph indices 1 and
// 3 passed. Indices are zero-based over SCORED paragraphs, which is what
// `--paragraphs` consumes.
func TestAScoredSegmentCarriesItsIndexSpanAndSize(t *testing.T) {
	raw, all := interleavedDraft()
	assertSegmentTuples(t, scoreInterleaved(t), raw, pick(all, 1, 3))
}

// ---------------------------------------------------------------------------
// Skipped paragraphs
// ---------------------------------------------------------------------------

// Every skipped paragraph is reported with its exact span and size, in document
// order, so it can be found in the file and the reason understood.
// assertSkippedTuples checks the reported skipped paragraphs against the exact
// paragraphs expected, as (offset, length, bytes, token count) tuples in
// document order.
//
// A tuple, and not a count plus an inequality. Replacing an all-skipped
// report's list with an equally sized slice of ZERO-VALUED entries satisfied
// both the cardinality check and `LexicalTokens < floor` (0 is below every
// floor), and the whole suite stayed green.
//
// The OFFSET is asserted, not just the bytes at it. Two identical paragraphs
// slice to the same text, so comparing text alone cannot tell them apart.
func assertSkippedTuples(t *testing.T, report score.Report, raw string, want []para) {
	t.Helper()
	if len(report.Skipped) != len(want) {
		t.Fatalf("reported %d skipped paragraphs, want %d", len(report.Skipped), len(want))
	}
	for i, w := range want {
		got := report.Skipped[i]
		assertSpan(t, "skipped", i, raw, got.Offset, got.Length, w)
		if got.LexicalTokens != w.tokens {
			t.Errorf("skipped %d has %d lexical tokens, want %d",
				i, got.LexicalTokens, w.tokens)
		}
	}
}

// assertSegmentTuples is the same assertion for the SURVIVORS, plus the index,
// which is zero-based over scored paragraphs and is what `--paragraphs` takes.
func assertSegmentTuples(t *testing.T, report score.Report, raw string, want []para) {
	t.Helper()
	if len(report.Segments) != len(want) {
		t.Fatalf("scored %d segments, want %d", len(report.Segments), len(want))
	}
	for i, w := range want {
		got := report.Segments[i]
		if got.Index != i {
			t.Errorf("segment %d reports index %d; indices are zero-based over "+
				"SCORED paragraphs", i, got.Index)
		}
		assertSpan(t, "segment", i, raw, got.Offset, got.Length, w)
		if got.LexicalTokens != w.tokens {
			t.Errorf("segment %d has %d lexical tokens, want %d",
				i, got.LexicalTokens, w.tokens)
		}
	}
}

// assertSpan checks a reported span against the offset recorded at construction
// AND the bytes it selects.
func assertSpan(t *testing.T, label string, i int, raw string, offset, length int, w para) {
	t.Helper()
	if offset != w.offset || length != len(w.text) {
		t.Errorf("%s %d spans [%d,+%d), want [%d,+%d) — %q",
			label, i, offset, length, w.offset, len(w.text), w.text)
		return
	}
	if raw[offset:offset+length] != w.text {
		t.Errorf("%s %d spans\n  %q\nwant exactly\n  %q",
			label, i, raw[offset:offset+length], w.text)
	}
}

func TestASkippedParagraphIsReportedWithItsSpanAndSize(t *testing.T) {
	raw, all := interleavedDraft()
	assertSkippedTuples(t, scoreInterleaved(t), raw, pick(all, 0, 2))
}

// Two identical paragraphs each report their OWN occurrence.
//
// Resolving a span by searching the draft for its own text returns the first
// match every time, which is indistinguishable from correct until the same
// paragraph appears twice. Under that mapping the second survivor was reported
// at the first one's offset and the whole suite stayed green.
func TestRepeatedParagraphsReportTheirOwnOccurrence(t *testing.T) {
	raw, all := draftOf(
		para{text: shortA, tokens: 1},
		para{text: longA, tokens: 14},
		para{text: shortA, tokens: 1},
		para{text: longA, tokens: 14},
	)
	report := scoreAtFloor(t, raw, floorUnderTest)

	assertSegmentTuples(t, report, raw, pick(all, 1, 3))
	assertSkippedTuples(t, report, raw, pick(all, 0, 2))
}

// Spans survive a draft whose paragraphs are NOT evenly spaced.
//
// Excluded markup between paragraphs, and a stray extra blank line, so the
// distance from one paragraph to the next is not its predecessor's length plus
// a fixed separator. The heading and the quote are measured facts: neither is
// an included leaf, so neither is reported and neither counts below the floor.
func TestSpansHoldWhenTheLayoutIsNotUniform(t *testing.T) {
	// Over line endings AND leading bytes, because a normalization applied
	// before the span is captured is invisible to a fixture that never carries
	// the thing being normalized away.
	//
	//   crlf                — CRLF to LF before admission
	//   leading blank lines — bytes.TrimLeft(source, " \t\r\n")
	//   byte order mark     — text.Admit already strips one leading UTF-8 BOM
	//   front matter        — the parser slices front matter off, parses the
	//                         body, then restores the coordinate base
	//
	// Each of the three passed the entire suite against LF-only, unprefixed
	// fixtures.
	//
	// The BOM case states a CONTRACT rather than only guarding a mutation.
	// Measured: `text.Admit` strips the BOM, so its spans are in stripped
	// coordinates and a reported offset of 0 does not select the first
	// paragraph of the file — it selects the BOM. #98 exists so a reader can
	// find a paragraph in THEIR file, so the reported offset is asserted in FILE
	// coordinates and the implementation has to add the BOM back. Leading blank
	// lines, by contrast, are already file coordinates and need nothing.
	for _, c := range []struct {
		name  string
		eol   string
		lead  []chunk
		noEOL bool
	}{
		{"lf", "\n", nil, false},
		{"crlf", "\r\n", nil, false},
		{"leading blank lines", "\n", []chunk{{sep: "\n\n"}}, false},
		{"byte order mark", "\n", []chunk{{text: "\ufeff"}}, false},
		{"front matter", "\n", []chunk{{text: "---\ntitle: Test\n---", sep: "\n\n"}}, false},
		// No trailing newline. Every fixture ended with one, so truncating each
		// reported length to `min(length, len(raw)-offset-1)` passed all three
		// suites — it only ever ate the newline. A file that does not end in one
		// loses a byte off its last paragraph.
		{"no trailing newline", "\n", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The excluded markup CONTAINS COPIES of the reported paragraphs.
			//
			// Without that, offsets can still be reconstructed by scanning forward —
			// `cursor + bytes.Index(source[cursor:], paragraph)`, advancing the cursor —
			// which passed all three suites including the repeated-paragraph tests. The
			// assumption it rests on is that the next textual occurrence belongs to the
			// next included leaf. A heading `# Yes.` and a quoted copy of the long
			// paragraph break it. Measured in the MINIMAL input that demonstrated
			// this — a heading copy, a quoted copy and the two paragraphs, nothing
			// else — the reported paragraphs sit at 8 and 91 while the copies sit
			// at 2 and 16. Those are the probe's coordinates, not this composite
			// fixture's; this one's move whenever a case is added.
			// A fenced code block holding copies too.
			//
			// The prefixed copies above are not at a line start, so a forward search
			// that accepts only line-start matches walked straight past them. Inside a
			// fence the copies ARE at line starts and are still excluded — by enclosing
			// structure, which is the one thing a scan of the raw bytes cannot see.
			// Measured, again in the minimal fence-only input: the copies sit at 4
			// and 10 and the reported paragraphs at 88 and 95. Coordinates of the
			// probe, not of this fixture.
			// And the third exclusion reason, ExcludedNotSentential.
			//
			// structure.go excludes a leaf for three different reasons, and the fixtures
			// covered only two: by role (heading, code block) and by block-quote policy.
			// A non-sentential list item and a paragraph that is nothing but inline code
			// — zero words once the excision is taken out — are excluded by the third.
			// A mapping that enumerated every leaf and admitted those reported them as
			// skipped paragraphs, which is a lie twice over: they were not measured, and
			// they did not fail the FLOOR. Measured: neither is an included leaf and
			// neither counts below the floor.
			chunks := append(append([]chunk(nil), c.lead...), []chunk{
				chunk{text: "```\n" + shortA + "\n\n" + longA + "\n```", sep: "\n\n"},
				chunk{text: "# " + shortA, sep: "\n\n"},
				chunk{text: "- " + shortA, sep: "\n\n"},
				chunk{text: "`--json`", sep: "\n\n"},
				chunk{text: shortA, tokens: 1, report: true, sep: "\n\n\n"},
				chunk{text: "> " + longA, sep: "\n\n"},
				chunk{text: longA, tokens: 14, report: true, sep: "\n\n"},
				chunk{text: shortB, tokens: 2, report: true, sep: "\n\n"},
				chunk{text: longB, tokens: 15, report: true, sep: "\n\n"},
				chunk{text: wrapSkip, tokens: 2, report: true, sep: "\n\n"},
				chunk{text: wrapKeep, tokens: 14, report: true, sep: "\n"},
			}...)
			raw, all := layoutOfEOL(c.eol, chunks...)
			if c.noEOL {
				raw = strings.TrimSuffix(raw, c.eol)
			}
			report := scoreAtFloor(t, raw, floorUnderTest)

			assertSegmentTuples(t, report, raw, pick(all, 1, 3, 5))
			assertSkippedTuples(t, report, raw, pick(all, 0, 2, 4))
		})
	}
}

// The shapes whose reported leaves are NOT paragraphs, and the prefixes that
// move a reported boundary. Offsets are computed from the pieces, never
// searched for.
func admittedRoleDrafts() []struct {
	name string
	raw  string
	keep []para
	skip []para
} {
	const termOne, termTwo = "Term\n: ", "Other\n: "
	defRaw := termOne + shortA + "\n\n" + termTwo + longA + "\n"
	defSkip := len(termOne)
	defKeep := defSkip + len(shortA) + 2 + len(termTwo)

	const figOpen, figClose = "<figure>\n<figcaption>", "</figcaption>\n</figure>"
	capRaw := figOpen + shortA + figClose + "\n\n" + figOpen + longA + figClose + "\n"
	capSkip := len(figOpen)
	capKeep := capSkip + len(shortA) + len(figClose) + 2 + len(figOpen)

	// A caption written over its own lines keeps the boundary whitespace: the
	// reported span is "\n  Yes.\n", not "Yes.". Trimming trailing whitespace
	// from every reported span passed the entire suite against the tight
	// captions above, which sit flush against their tags.
	wideSkip, wideKeep := "\n  "+shortA+"\n", "\n  "+longA+"\n"
	wideRaw := figOpen + wideSkip + figClose + "\n\n" + figOpen + wideKeep + figClose + "\n"
	wideSkipAt := len(figOpen)
	wideKeepAt := wideSkipAt + len(wideSkip) + len(figClose) + 2 + len(figOpen)

	const fnBody, fnOne, fnTwo = "A claim.[^1] And another.[^2]\n\n", "[^1]: ", "[^2]: "
	fnRaw := fnBody + fnOne + shortA + "\n\n" + fnTwo + longA + "\n"
	fnSkip := len(fnBody) + len(fnOne)
	fnKeep := fnSkip + len(shortA) + 2 + len(fnTwo)
	body := fnBody[:len(fnBody)-2]

	return []struct {
		name string
		raw  string
		keep []para
		skip []para
	}{
		{"definition description", defRaw,
			[]para{{text: longA, tokens: 14, offset: defKeep}},
			[]para{{text: shortA, tokens: 1, offset: defSkip}}},
		{"figure caption", capRaw,
			[]para{{text: longA, tokens: 14, offset: capKeep}},
			[]para{{text: shortA, tokens: 1, offset: capSkip}}},
		{"multiline figure caption", wideRaw,
			[]para{{text: wideKeep, tokens: 14, offset: wideKeepAt}},
			[]para{{text: wideSkip, tokens: 1, offset: wideSkipAt}}},
		// The referencing paragraph is a leaf too, and at four tokens it falls
		// below the floor — so this case also pins the ORDER of a mixed list.
		{"referenced footnote", fnRaw,
			[]para{{text: longA, tokens: 14, offset: fnKeep}},
			[]para{{text: body, tokens: 4, offset: 0}, {text: shortA, tokens: 1, offset: fnSkip}}},
	}
}

// A reported leaf is not always a paragraph.
//
// `ParagraphLeaves` consumes every INCLUDED leaf and `exclusionFor` rejects
// only some roles, so a definition description, a figure caption and a
// referenced footnote are all admitted, measured and scored exactly like
// paragraphs. Every other fixture in this file is RoleParagraph, so zeroing the
// span of any leaf whose role is not RoleParagraph passed all three suites —
// and so did zeroing RoleCaption alone, and RoleFootnote alone.
//
// Measured, not assumed. An earlier version of this comment recorded that
// footnotes "produce no leaf under the default options"; that was wrong, and it
// was wrong because the probe used an UNREFERENCED definition. A referenced one
// is admitted at [37,+4).
func TestEveryAdmittedRoleCarriesItsSpan(t *testing.T) {
	for _, c := range admittedRoleDrafts() {
		t.Run(c.name, func(t *testing.T) {
			report := scoreAtFloor(t, c.raw, floorUnderTest)
			assertSegmentTuples(t, report, c.raw, c.keep)
			assertSkippedTuples(t, report, c.raw, c.skip)
		})
	}
}

// Snap moves a reported boundary outward to a grapheme boundary.
//
// For a paragraph opening with a space followed by a combining acute, Goldmark
// starts its segment at byte 1 — INSIDE that grapheme — and `Snap` expands the
// span back to 0. Trimming leading whitespace from a reported span survives
// every other test here and reports [1,+6) instead of [0,+7).
//
// Measured: [0,+7) and [9,+75), one lexical token and fourteen. The reported
// bytes include the prefix, which is the point — a span is a grapheme-aligned
// range of the file, not the prose someone would have typed.
func TestSnapExpandsAReportedSpanToAGraphemeBoundary(t *testing.T) {
	const prefix = " \u0301"
	raw := prefix + shortA + "\n\n" + prefix + longA + "\n"
	keepAt := len(prefix) + len(shortA) + 2

	report := scoreAtFloor(t, raw, floorUnderTest)
	assertSegmentTuples(t, report, raw,
		[]para{{text: prefix + longA, tokens: 14, offset: keepAt}})
	assertSkippedTuples(t, report, raw,
		[]para{{text: prefix + shortA, tokens: 1, offset: 0}})
}

// Continuation indentation stays in the SPAN while leaving the COUNT.
//
// A list item wrapped over two lines carries a line gap. `lineGapExcisions`
// records the CONTINUATION INDENT as an excision — measured, {10,+2}, the two
// spaces only — so the indent does not enter the measured text while the
// newline does. The reported span must cover both, because those bytes lie in
// the file between the paragraph's first and last character.
//
// An earlier version of this comment said the excision covers the newline as
// well. It does not, and `RunText` returns "This is\nshort prose." — the
// assertions were right and the explanation was wrong.
//
// Two facts pulling opposite ways on the same paragraph, which is why a
// mutation subtracting each gap's length from the reported length passed
// everything: the inline-code fixture has an excision in the MIDDLE of a line,
// and nothing had one at the START of a continuation line. Measured, the
// excisions here are [10,+2) and [75,+2) — the indent spaces only, never the
// newline.
//
// Measured: [2,+22) at four tokens and [27,+74) at fourteen, one excision each.
func TestContinuationIndentationStaysInTheSpanButNotTheCount(t *testing.T) {
	const bullet = "- "
	const gapSkip = "This is\n  short prose."
	const gapKeep = "The café stores every record in a signed chain\n  and verifies it on read."
	raw := bullet + gapSkip + "\n" + bullet + gapKeep + "\n"
	skipAt := len(bullet)
	keepAt := skipAt + len(gapSkip) + 1 + len(bullet)

	report := scoreAtFloor(t, raw, floorUnderTest)
	assertSegmentTuples(t, report, raw, []para{{text: gapKeep, tokens: 14, offset: keepAt}})
	assertSkippedTuples(t, report, raw, []para{{text: gapSkip, tokens: 4, offset: skipAt}})
}

// An excision that decides ADMISSION, not just the count.
//
// The inline-code fixtures above never cross the floor when their excised words
// are counted, so a mutation that ignored excisions when deciding which
// paragraphs are skipped — while still REPORTING the right counts — passed the
// entire suite. The same shape as the numeric admission mutation, in the other
// condition.
//
// Measured: this paragraph is eight lexical tokens, and eleven if the three
// backticked words are counted. At a floor of ten it is skipped; under that
// mutation it is not.
func TestExcludedWordsDoNotAdmitAParagraph(t *testing.T) {
	const crossing = "We waited `alpha beta gamma` days for records and shipped fixes."
	raw := crossing + "\n\n" + longA + "\n"

	report := scoreAtFloor(t, raw, floorUnderTest)
	assertSegmentTuples(t, report, raw,
		[]para{{text: longA, tokens: 14, offset: len(crossing) + 2}})
	assertSkippedTuples(t, report, raw,
		[]para{{text: crossing, tokens: 8, offset: 0}})
}

// An excision can ADJOIN the words either side of it without merging them.
//
// The inline-code fixture separates its code from the prose with spaces, so
// recomputing a token count from `RunText(node)` — which returns the text with
// the excision removed — happened to give the same answer and passed the whole
// suite.
//
// Measured, `Prefix`+"`code`"+`suffix.` spans [0,+19) and counts TWO lexical
// tokens, while `RunText` returns "Prefixsuffix.", which counts one. The span
// and the count come from different places and this is where they disagree.
func TestAnExcisionBetweenTwoWordsDoesNotMergeThem(t *testing.T) {
	const adjoined = "Prefix`code`suffix."
	raw := adjoined + "\n\n" + longA + "\n"

	report := scoreAtFloor(t, raw, floorUnderTest)
	assertSegmentTuples(t, report, raw,
		[]para{{text: longA, tokens: 14, offset: len(adjoined) + 2}})
	assertSkippedTuples(t, report, raw,
		[]para{{text: adjoined, tokens: 2, offset: 0}})
}

// # The counting contract, and which fixture covers which part of it
//
// Read off the source rather than inferred:
//
//	count = document tokens that are LEXICAL, WHOLLY CONTAINED in the leaf span,
//	        and overlapping NO EXCISION
//
// Four conditions, and the fixtures below are organized by them:
//
//	lexical classification  the numeric cases, including one that crosses the
//	                        floor only when numbers are miscounted
//	excision overlap        inline code, an excision adjoining both neighbours,
//	                        and one whose excised words cross the floor
//	coordinates / envelope  the layout table (CRLF, leading blank lines, BOM,
//	                        front matter, no trailing newline), Snap, captions,
//	                        and the line-gap continuation indent
//
// The line-gap indent is listed under COORDINATES, not excision overlap. Its
// excision contains only spaces, so ignoring it entirely still yields four
// lexical tokens — it guards that the span keeps bytes the measurement drops,
// which is a span property. An earlier version of this map filed it under
// excision overlap and so claimed a guard that does not exist.
//
// The fourth, WHOLE-TOKEN CONTAINMENT, has no fixture here, and that is a
// deliberate decision rather than an oversight.
//
// It is reachable — but in current source only through #102, where a caption's
// boundaries are found by searching a `bytes.ToLower` copy and then applied to
// the original bytes. For a full block —
//
//	<figure>\n<figcaption>İ test</figcaption>\n</figure>\n
//
// the caption span comes back as [21,+6), "İ tes", and the token `test`
// straddles its end, so containment drops it and the caption measures one token
// instead of two. (The bare tag without the <figure> wrapper truncates the same
// way at [12,+6); an earlier version of this comment quoted the bare tag with
// the wrapped block's offset.)
//
// Asserting that here would freeze a bug into a contract: the test would have
// to encode the truncated span as correct, and would then FAIL when #102 is
// fixed. So the condition is recorded rather than guarded, and #102 carries the
// reproduction.
//
// If #102 is fixed and no other input reaches the condition, it becomes
// unfalsifiable by construction — which is worth stating in this file at that
// point, with the reason, rather than leaving a silent gap in the list above.

// A number is not a lexical token, and the reported count says so.
//
// The counting contract, read off the source rather than inferred: a paragraph's
// count is the number of document tokens that are LEXICAL, wholly contained in
// the leaf span, and overlapping no excision.
//
// This fixture exercises CLASSIFICATION. Including numbers in the skipped
// report's count — a one-token change, `token.Lexical || token.Class ==
// text.Number` — passed all three suites.
//
// Measured: "Wait 42 days." spans [0,+13) and counts TWO, not three.
//
// The middle paragraph is the one that matters, and an earlier version of this
// comment claimed something the fixture did not test. It asserted that a count
// including numbers "would change which side of the floor a paragraph lands
// on" — but both original paragraphs sat clear of the floor on their own sides
// (13 and 16 above, 2 and 3 below), so nothing crossed it. A mutation that
// counted numbers only when deciding ADMISSION, leaving the reported counts
// correct, passed every suite.
//
// "We waited 42 days for 7 records and shipped 3 fixes." is eight lexical
// tokens and eleven if the three numbers are counted. At a floor of ten it is
// skipped, and under that mutation it is not.
func TestANumberIsNotCountedAsALexicalToken(t *testing.T) {
	const shortNum = "Wait 42 days."
	const longNum = "We waited 42 days for the 7 records to reconcile and then shipped the 3 fixes."
	const crossing = "We waited 42 days for 7 records and shipped 3 fixes."
	raw := shortNum + "\n\n" + crossing + "\n\n" + longNum + "\n"
	crossAt := len(shortNum) + 2
	longAt := crossAt + len(crossing) + 2

	report := scoreAtFloor(t, raw, floorUnderTest)
	assertSegmentTuples(t, report, raw,
		[]para{{text: longNum, tokens: 13, offset: longAt}})
	assertSkippedTuples(t, report, raw, []para{
		{text: shortNum, tokens: 2, offset: 0},
		{text: crossing, tokens: 8, offset: crossAt},
	})
}

// A paragraph containing inline code reports its WHOLE raw span, while counting
// only the tokens outside the backticks.
func TestInlineCodeIsExcludedFromTheCountButNotFromTheSpan(t *testing.T) {
	raw, all := draftOf(
		para{text: codeSkip, tokens: 2},
		para{text: codeKeep, tokens: 16},
	)
	report := scoreAtFloor(t, raw, floorUnderTest)

	assertSegmentTuples(t, report, raw, pick(all, 1))
	assertSkippedTuples(t, report, raw, pick(all, 0))
}

// ---------------------------------------------------------------------------
// The floor, over more than one value
// ---------------------------------------------------------------------------

// The floor that was applied is reported, and it is the floor actually used.
//
// Exercised at several values, because a single case is satisfied by hard-coding
// the number — and the partitions differ at each, so the report is checked
// against what the floor should have done rather than only against itself.
func TestTheReportSaysWhichFloorWasAppliedAtSeveralFloors(t *testing.T) {
	// Named by text on both sides, so each case pins WHICH paragraphs landed
	// where rather than how many. At floor 20 nothing survives, and a count plus
	// the `tokens < floor` inequality is satisfied by four zero-valued entries.
	// Positions into interleavedDraft(): 0 "Yes." (1), 1 longA (14),
	// 2 "It broke." (2), 3 longB (15).
	for _, c := range []struct {
		floor    int
		segments []int
		skipped  []int
	}{
		{2, []int{1, 2, 3}, []int{0}},  // only "Yes." at one token falls below
		{3, []int{1, 3}, []int{0, 2}},  // "It broke." at two joins it
		{10, []int{1, 3}, []int{0, 2}}, // the shipped default
		{20, nil, []int{0, 1, 2, 3}},   // nothing clears
	} {
		t.Run(floorName(c.floor), func(t *testing.T) {
			raw, all := interleavedDraft()
			report := scoreAtFloor(t, raw, c.floor)

			if report.ParagraphFloor != c.floor {
				t.Errorf("report floor = %d, want %d", report.ParagraphFloor, c.floor)
			}
			assertSegmentTuples(t, report, raw, pick(all, c.segments...))
			assertSkippedTuples(t, report, raw, pick(all, c.skipped...))

			// And the partition agrees with the floor it reports.
			for _, s := range report.Skipped {
				if s.LexicalTokens >= report.ParagraphFloor {
					t.Errorf("a skipped paragraph has %d tokens, at or above the reported "+
						"floor of %d", s.LexicalTokens, report.ParagraphFloor)
				}
			}
			for _, s := range report.Segments {
				if s.LexicalTokens < report.ParagraphFloor {
					t.Errorf("a scored segment has %d tokens, below the reported floor of %d",
						s.LexicalTokens, report.ParagraphFloor)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Raw offsets, not normalized ones
// ---------------------------------------------------------------------------

// Reported spans index the RAW bytes, not an NFC copy of them.
//
// internal/text/structure.go states the contract directly: spans "remain valid
// raw offsets even when NFC changes byte length". A reporting mapping that
// takes its spans from the normalized form satisfies every other test in this
// file, because the precomposed `café` fixture normalizes to itself and the two
// forms are byte-identical.
//
// Written decomposed — `cafe` + U+0301 — the two disagree by one byte, and the
// mutation shows up as a survivor one byte short and every later paragraph one
// byte early. This is not a contrived input: macOS hands back decomposed text
// from a number of ordinary paths, so a user's own file can be in this form
// without their ever having chosen it. An offset that is one byte early points
// `--paragraphs` at the wrong bytes.
func TestReportedSpansIndexTheRawBytesNotANormalizedCopy(t *testing.T) {
	raw, all := draftOf(
		para{text: shortA, tokens: 1},
		para{text: decomposed(longA), tokens: 14},
		para{text: shortB, tokens: 2},
		para{text: longB, tokens: 15},
	)
	report := scoreAtFloor(t, raw, floorUnderTest)

	assertSegmentTuples(t, report, raw, pick(all, 1, 3))
	assertSkippedTuples(t, report, raw, pick(all, 0, 2))
}

// decomposed rewrites the fixture's only precomposed character into its
// combining form, which is one byte longer.
func decomposed(s string) string {
	return strings.ReplaceAll(s, "caf\u00e9", "cafe\u0301")
}

// ---------------------------------------------------------------------------
// The count and the list
// ---------------------------------------------------------------------------

// They are one fact, over every shape: some skipped, all skipped, none skipped.
func TestTheCountAlwaysEqualsTheList(t *testing.T) {
	for _, c := range []struct {
		name   string
		source string
		floor  int
	}{
		{"some skipped", interleaved(), 10},
		{"all skipped", interleaved(), 20},
		{"none skipped", longA + "\n\n" + longB + "\n", 10},
	} {
		t.Run(c.name, func(t *testing.T) {
			report := scoreAtFloor(t, c.source, c.floor)
			if report.ParagraphsBelowFloor != len(report.Skipped) {
				t.Errorf("paragraphs_below_floor = %d and %d paragraphs are listed",
					report.ParagraphsBelowFloor, len(report.Skipped))
			}
		})
	}
}

// A draft with nothing below the floor reports no skipped paragraphs and a zero
// count.
//
// This asserts the COUNT is zero and the list is empty. It does not require a
// non-nil slice: nothing in the Go contract needs one, and if the JSON envelope
// must carry `[]` rather than `null` that belongs in a serialization test, not
// here.
func TestADraftWithNothingSkippedReportsNone(t *testing.T) {
	report := scoreAtFloor(t, longA+"\n\n"+longB+"\n", 10)

	if report.ParagraphsBelowFloor != 0 {
		t.Errorf("paragraphs_below_floor = %d, want 0", report.ParagraphsBelowFloor)
	}
	if len(report.Skipped) != 0 {
		t.Errorf("reported %d skipped paragraphs, want none", len(report.Skipped))
	}
	if len(report.Segments) != 2 {
		t.Errorf("scored %d segments, want 2", len(report.Segments))
	}
}

// ---------------------------------------------------------------------------
// Measure reports the same
// ---------------------------------------------------------------------------

// Measure is the uncalibrated path, and workflow uses it whenever a profile has
// no release — which is every profile that has not been through eval. The new
// report fields must be there too, or the reporting works only for the
// calibrated case.
func TestMeasureReportsTheSameSkippedParagraphs(t *testing.T) {
	scoredReport := scoreInterleaved(t)
	measured := measureAtFloor(t, interleaved(), floorUnderTest)

	if measured.ParagraphFloor != scoredReport.ParagraphFloor {
		t.Errorf("Measure reports floor %d and Score reports %d",
			measured.ParagraphFloor, scoredReport.ParagraphFloor)
	}
	if measured.ParagraphsBelowFloor != scoredReport.ParagraphsBelowFloor {
		t.Errorf("Measure counts %d below floor and Score counts %d",
			measured.ParagraphsBelowFloor, scoredReport.ParagraphsBelowFloor)
	}
	if len(measured.Skipped) != len(scoredReport.Skipped) {
		t.Fatalf("Measure lists %d skipped and Score lists %d",
			len(measured.Skipped), len(scoredReport.Skipped))
	}
	for i := range measured.Skipped {
		if measured.Skipped[i] != scoredReport.Skipped[i] {
			t.Errorf("skipped %d differs: Measure %+v, Score %+v",
				i, measured.Skipped[i], scoredReport.Skipped[i])
		}
	}
	for i := range measured.Segments {
		if measured.Segments[i].Offset != scoredReport.Segments[i].Offset ||
			measured.Segments[i].Length != scoredReport.Segments[i].Length {
			t.Errorf("segment %d span differs between Measure and Score", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// floorUnderTest is stated here rather than taken from the defaults, so this
// file keeps testing the reporting contract when the default moves again.
const floorUnderTest = 10

func floorName(n int) string { return "floor " + strconv.Itoa(n) }

func scoreInterleaved(t *testing.T) score.Report {
	t.Helper()
	return scoreAtFloor(t, interleaved(), floorUnderTest)
}

func scoreAtFloor(t *testing.T, source string, floor int) score.Report {
	t.Helper()
	prof := withFloor(testProfile(), floor)
	ref := testReference(t, testProfile())
	got, err := score.Score([]byte(source), mustFit(t, prof), ref, testRelease(t, ref))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	return got
}

func measureAtFloor(t *testing.T, source string, floor int) score.Report {
	t.Helper()
	prof := withFloor(testProfile(), floor)
	ref := testReference(t, testProfile())
	got, err := score.Measure([]byte(source), mustFit(t, prof), ref)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	return got
}
