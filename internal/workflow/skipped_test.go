package workflow_test

// #98's integration half: the reporting surface must survive the workflow
// mapping, and the indices it publishes must be the ones `--paragraphs`
// consumes.
//
// Codex's finding on #92 was that a correct `profile` and a broken workflow
// mapping both pass a package-level suite. The same applies here, and more
// sharply: this slice's entire point is what a user is shown.

import (
	"os"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/workflow"
)

// reportingDraft interleaves short and long paragraphs so a mapping off by the
// number of skipped paragraphs is wrong rather than coincidentally right.
const (
	skipOne = "Yes."
	keepOne = "The café stores every record in a signed chain and verifies it on read."
	skipTwo = "It broke."
	keepTwo = "Verification points at the entry that changed, which is the whole point of it."
)

// draftOf assembles paragraphs and records each one's exact raw offset as it
// goes, so an expectation is never recovered by searching the result.
//
// strings.Index returns the FIRST occurrence, so a draft containing the same
// paragraph twice expects both reports to point at the first — and a mapping
// that resolved every span by searching for its own text passed the whole
// suite while reporting the second occurrence at the first one's offset.
//
// It also keeps offsets honest ACROSS fixtures: skipTwo sits at 80 in the
// four-paragraph draft and at 6 in the two-paragraph one, so a shared
// expectation list would be wrong in one of them.

// A WRAPPED paragraph, one either side of the floor.
//
// Every reported paragraph in these fixtures is a single line, so truncating each span at
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

func draftOf(paras ...paragraphWant) (string, []paragraphWant) {
	var b strings.Builder
	out := make([]paragraphWant, len(paras))
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

// chunk is a piece of a draft written verbatim. Only chunks marked `report` are
// expected in a score result; the rest are markup or spacing.
type chunk struct {
	text   string
	tokens int
	report bool
	sep    string
}

// layoutOf assembles a draft whose paragraphs are NOT evenly spaced, so an
// offset cannot be reconstructed as a running total of `length + 2`. A mapping
// that computed them that way, never consulting the source, passed the whole
// suite against draftOf's uniform fixtures.
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
func layoutOf(chunks ...chunk) (string, []paragraphWant) {
	return layoutOfEOL("\n", chunks...)
}

func layoutOfEOL(eol string, chunks ...chunk) (string, []paragraphWant) {
	var b strings.Builder
	var out []paragraphWant
	for _, c := range chunks {
		text := strings.ReplaceAll(c.text, "\n", eol)
		if c.report {
			out = append(out, paragraphWant{text: text, tokens: c.tokens, offset: b.Len()})
		}
		b.WriteString(text)
		b.WriteString(strings.ReplaceAll(c.sep, "\n", eol))
	}
	return b.String(), out
}

// pick selects paragraphs by position, preserving order.
func pick(all []paragraphWant, at ...int) []paragraphWant {
	out := make([]paragraphWant, 0, len(at))
	for _, i := range at {
		out = append(out, all[i])
	}
	return out
}

func reportingParas() (string, []paragraphWant) {
	return draftOf(
		paragraphWant{text: skipOne, tokens: 1},
		paragraphWant{text: keepOne, tokens: 14},
		paragraphWant{text: skipTwo, tokens: 2},
		paragraphWant{text: keepTwo, tokens: 14},
	)
}

func reportingDraft() string {
	raw, _ := reportingParas()
	return raw
}

// assertReportingSurface checks the WHOLE of #98's new surface on one result:
// the floor that was applied, the count agreeing with the list, every skipped
// paragraph's tuple and every survivor's tuple.
//
// One helper called under both calibration states, rather than an assertion per
// fixture. Four separate mutations proved that a check written against a single
// fixture guards only that fixture — including `out.Skipped = nil` and
// `out.ParagraphFloor = 0` applied when `bundle.Calibrated`, which passed the
// entire workflow suite because the calibrated test asserted selection only.
// paragraphWant is a paragraph expected on one side of the partition, named by
// its own text so the assertion checks BYTES rather than a remembered offset.
type paragraphWant struct {
	text   string
	tokens int
	offset int
}

// Positions into reportingParas(): 0 skipOne, 1 keepOne, 2 skipTwo, 3 keepTwo.
var (
	survivorsAboveTen = []int{1, 3}
	skippedBelowTen   = []int{0, 2}
	everyParagraph    = []int{0, 1, 2, 3}
)

func assertReportingSurface(t *testing.T, result workflow.ScoreResult, raw []byte,
	wantFloor int, wantSegments, wantSkipped []paragraphWant) {
	t.Helper()

	// Pinned against the floor the CALLER says this corpus was indexed at, not
	// against today's default — substituting the default passes whenever the two
	// happen to agree, and a mutation keyed on `floor != 10` is then invisible.
	if result.ParagraphFloor != wantFloor {
		t.Errorf("result floor = %d, want the persisted %d", result.ParagraphFloor, wantFloor)
	}
	if result.ParagraphsBelowFloor != len(result.Skipped) {
		t.Errorf("count %d disagrees with the list of %d",
			result.ParagraphsBelowFloor, len(result.Skipped))
	}

	// The OFFSET is checked, not only the bytes at it: two identical paragraphs
	// slice to the same text, so comparing text alone cannot tell them apart.
	spans := func(label string, i int, offset, length int, want paragraphWant) {
		t.Helper()
		if offset < 0 || offset+length > len(raw) {
			t.Fatalf("%s %d spans [%d,+%d), outside a draft of %d bytes",
				label, i, offset, length, len(raw))
		}
		if offset != want.offset || length != len(want.text) {
			t.Errorf("%s %d spans [%d,+%d), want [%d,+%d) — %q",
				label, i, offset, length, want.offset, len(want.text), want.text)
			return
		}
		if string(raw[offset:offset+length]) != want.text {
			t.Errorf("%s %d spans\n  %q\nwant exactly\n  %q",
				label, i, raw[offset:offset+length], want.text)
		}
	}

	if len(result.Segments) != len(wantSegments) {
		t.Fatalf("scored %d segments, want %d", len(result.Segments), len(wantSegments))
	}
	for i, want := range wantSegments {
		got := result.Segments[i]
		// Indices are zero-based over SCORED paragraphs, so a survivor's index is
		// its position in this list, not in the document.
		if got.Index != i {
			t.Errorf("segment %d reports index %d", i, got.Index)
		}
		spans("segment", i, got.Offset, got.Length, want)
		if got.LexicalTokens != want.tokens {
			t.Errorf("segment %d has %d lexical tokens, want %d",
				i, got.LexicalTokens, want.tokens)
		}
	}

	if len(result.Skipped) != len(wantSkipped) {
		t.Fatalf("workflow reported %d skipped paragraphs, want %d",
			len(result.Skipped), len(wantSkipped))
	}
	for i, want := range wantSkipped {
		got := result.Skipped[i]
		spans("skipped", i, got.Offset, got.Length, want)
		// The size too. Dropping every lexical count from the mapping passed a
		// version of this test that checked only bytes.
		if got.LexicalTokens != want.tokens {
			t.Errorf("skipped %d has %d lexical tokens, want %d",
				i, got.LexicalTokens, want.tokens)
		}
	}
}

// Score's result carries the skipped paragraphs and their spans through the
// workflow, not just the count — on an UNCALIBRATED profile, which is the state
// every profile is in until eval has run.
func TestScoreResultCarriesTheSkippedParagraphs(t *testing.T) {
	t.Parallel()
	root := indexedCorpus(t)
	draft := writeDraft(t, root, reportingDraft())

	result := scored(t, scoreRequest(root, draft))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	_, all := reportingParas()
	assertReportingSurface(t, result, raw, indexedFloor,
		pick(all, survivorsAboveTen...), pick(all, skippedBelowTen...))
}

// And the same surface survives on a CALIBRATED profile.
//
// Its own test rather than a loop, because the two fixtures are built
// differently and a shared store would hide which one broke.
func TestACalibratedScoreCarriesTheSameReportingSurface(t *testing.T) {
	t.Parallel()
	root := bandedStore(t, "drifting")
	draft := writeDraft(t, root, reportingDraft())

	result := scored(t, scoreRequest(root, draft))
	if !result.Calibrated {
		t.Fatalf("bandedStore produced an uncalibrated result; this test guards the " +
			"calibrated path and would otherwise duplicate the uncalibrated one")
	}
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	_, all := reportingParas()
	assertReportingSurface(t, result, raw, indexedFloor,
		pick(all, survivorsAboveTen...), pick(all, skippedBelowTen...))
}

// And the reported index is the one `--paragraphs` selects.
//
// This is the contract the whole slice exists for: a person reads an index off
// score, passes it to rewrite, and gets the paragraph they were looking at. It
// is asserted by SELECTING each reported index in turn and checking the target's
// exact bytes, and that the other survivor is not targeted.
func TestEveryReportedIndexSelectsTheParagraphItNames(t *testing.T) {
	t.Parallel()
	root := bandedStore(t, "drifting")
	draft := writeDraft(t, root, reportingDraft())

	result := scored(t, scoreRequest(root, draft))
	if len(result.Segments) != 2 {
		t.Fatalf("scored %d segments, want 2", len(result.Segments))
	}
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}

	// The reported tuples are established FIRST, against the draft, before any
	// of them is trusted enough to select with.
	//
	// Without this the test is vacuous under a mapping that copies one segment's
	// index and span onto all the others: every iteration then selects paragraph
	// zero, the "other survivor" check skips entries sharing an index, and
	// everything passes. Codex applied exactly that mutation and the whole
	// workflow suite stayed green.
	_, all := reportingParas()
	for i, want := range pick(all, survivorsAboveTen...) {
		got := result.Segments[i]
		if got.Index != i {
			t.Fatalf("segment %d reports index %d", i, got.Index)
		}
		if got.Offset != want.offset || got.Length != len(want.text) {
			t.Fatalf("segment %d spans [%d,+%d), want [%d,+%d) — %q",
				i, got.Offset, got.Length, want.offset, len(want.text), want.text)
		}
		if string(raw[got.Offset:got.Offset+got.Length]) != want.text {
			t.Fatalf("segment %d spans\n  %q\nwant exactly\n  %q",
				i, string(raw[got.Offset:got.Offset+got.Length]), want.text)
		}
	}

	for _, segment := range result.Segments {
		// What score says this index is.
		reported := string(raw[segment.Offset : segment.Offset+segment.Length])

		request := planRequest(root, draft)
		request.Paragraphs = []int{segment.Index}
		plan := planned(t, request)

		if plan.Refusal != "" {
			t.Fatalf("selecting index %d refused: %q", segment.Index, plan.Refusal)
		}
		var targets []string
		for _, planned := range plan.Segments {
			if planned.Disposition != workflow.DispositionTarget {
				continue
			}
			targets = append(targets, string(raw[planned.Offset:planned.Offset+planned.Length]))
		}
		if len(targets) != 1 {
			t.Fatalf("selecting index %d planned %d targets, want 1", segment.Index, len(targets))
		}
		if targets[0] != reported {
			t.Errorf("index %d was reported as\n  %q\nbut selecting it targets\n  %q",
				segment.Index, reported, targets[0])
		}
		// And the other survivor is untouched.
		for _, other := range result.Segments {
			if other.Index == segment.Index {
				continue
			}
			otherText := string(raw[other.Offset : other.Offset+other.Length])
			if strings.Contains(targets[0], otherText) {
				t.Errorf("selecting index %d also spans the paragraph at index %d",
					segment.Index, other.Index)
			}
		}
	}
}

// indexedFloor is the floor these fixtures index at. Stated rather than taken
// from the defaults, so substituting the default is a detectable change.
const indexedFloor = 10

// And the reported floor follows the PERSISTED profile across values, not the
// runner's current default.
func TestTheReportedFloorIsThePersistedOne(t *testing.T) {
	t.Parallel()
	// The full tuple assertion at each floor, not a count plus `tokens < floor`.
	// Replacing the list with equally sized ZERO-VALUED entries whenever the
	// persisted floor is not 10 left all three suites green: the count still
	// agreed and 0 is below every floor.
	for _, c := range []struct {
		floor    int
		segments []int
		skipped  []int
	}{
		{1, everyParagraph, nil},
		{12, survivorsAboveTen, skippedBelowTen},
	} {
		t.Run(floorLabel(c.floor), func(t *testing.T) {
			t.Parallel()
			requirements := profile.DefaultRequirements()
			requirements.MinParagraphLexicalTokens = c.floor
			root := indexedCorpusWith(t, requirements)
			draft := writeDraft(t, root, reportingDraft())

			result := scored(t, scoreRequest(root, draft))
			raw, err := os.ReadFile(draft)
			if err != nil {
				t.Fatalf("read the draft: %v", err)
			}
			_, all := reportingParas()
			assertReportingSurface(t, result, raw, c.floor,
				pick(all, c.segments...), pick(all, c.skipped...))

			for _, s := range result.Skipped {
				if s.LexicalTokens >= c.floor {
					t.Errorf("a skipped paragraph has %d tokens, at or above the floor of %d",
						s.LexicalTokens, c.floor)
				}
			}
		})
	}
}

// The same, through the workflow, on a draft that is not evenly spaced.
//
// A heading and a block quote sit between the paragraphs and are excluded from
// the report entirely, and one gap carries a stray blank line.
func TestSpansHoldThroughTheWorkflowWhenTheLayoutIsNotUniform(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			root := indexedCorpus(t)
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
				chunk{text: "```\n" + skipOne + "\n\n" + keepOne + "\n```", sep: "\n\n"},
				chunk{text: "# " + skipOne, sep: "\n\n"},
				chunk{text: "- " + skipOne, sep: "\n\n"},
				chunk{text: "`--json`", sep: "\n\n"},
				chunk{text: skipOne, tokens: 1, report: true, sep: "\n\n\n"},
				chunk{text: "> " + keepOne, sep: "\n\n"},
				chunk{text: keepOne, tokens: 14, report: true, sep: "\n\n"},
				chunk{text: skipTwo, tokens: 2, report: true, sep: "\n\n"},
				chunk{text: keepTwo, tokens: 14, report: true, sep: "\n\n"},
				chunk{text: wrapSkip, tokens: 2, report: true, sep: "\n\n"},
				chunk{text: wrapKeep, tokens: 14, report: true, sep: "\n"},
			}...)
			body, all := layoutOfEOL(c.eol, chunks...)
			if c.noEOL {
				body = strings.TrimSuffix(body, c.eol)
			}
			draft := writeDraft(t, root, body)

			result := scored(t, scoreRequest(root, draft))
			raw, err := os.ReadFile(draft)
			if err != nil {
				t.Fatalf("read the draft: %v", err)
			}
			assertReportingSurface(t, result, raw, indexedFloor,
				pick(all, 1, 3, 5), pick(all, 0, 2, 4))
		})
	}
}

// The same through the workflow: a reported leaf is not always a paragraph, and
// a reported boundary is not always where Goldmark put it.
//
// A definition description, a figure caption and a referenced footnote are all
// admitted and scored like paragraphs, and `Snap` expands a span outward to a
// grapheme boundary. Zeroing RoleCaption spans alone, or RoleFootnote alone, or
// trimming leading whitespace, each passed all three suites — every other
// fixture here is a plain paragraph starting on a clean boundary.
func TestEveryAdmittedRoleSurvivesTheWorkflow(t *testing.T) {
	t.Parallel()

	const termOne, termTwo = "Term\n: ", "Other\n: "
	const figOpen, figClose = "<figure>\n<figcaption>", "</figcaption>\n</figure>"
	const fnBody, fnOne, fnTwo = "A claim.[^1] And another.[^2]\n\n", "[^1]: ", "[^2]: "
	const snapPrefix = " \u0301"

	defSkip := len(termOne)
	capSkip := len(figOpen)
	// A caption over its own lines keeps the boundary whitespace, so the span is
	// "\n  Yes.\n". Trimming trailing whitespace passed against tight captions.
	wideSkip, wideKeep := "\n  "+skipOne+"\n", "\n  "+keepOne+"\n"
	fnSkip := len(fnBody) + len(fnOne)

	for _, c := range []struct {
		name string
		body string
		keep []paragraphWant
		skip []paragraphWant
	}{
		{
			"definition description",
			termOne + skipOne + "\n\n" + termTwo + keepOne + "\n",
			[]paragraphWant{{text: keepOne, tokens: 14,
				offset: defSkip + len(skipOne) + 2 + len(termTwo)}},
			[]paragraphWant{{text: skipOne, tokens: 1, offset: defSkip}},
		},
		{
			"figure caption",
			figOpen + skipOne + figClose + "\n\n" + figOpen + keepOne + figClose + "\n",
			[]paragraphWant{{text: keepOne, tokens: 14,
				offset: capSkip + len(skipOne) + len(figClose) + 2 + len(figOpen)}},
			[]paragraphWant{{text: skipOne, tokens: 1, offset: capSkip}},
		},
		{
			"multiline figure caption",
			figOpen + wideSkip + figClose + "\n\n" + figOpen + wideKeep + figClose + "\n",
			[]paragraphWant{{text: wideKeep, tokens: 14,
				offset: capSkip + len(wideSkip) + len(figClose) + 2 + len(figOpen)}},
			[]paragraphWant{{text: wideSkip, tokens: 1, offset: capSkip}},
		},
		{
			// The referencing paragraph is a leaf too and falls below the floor,
			// so this case pins the order of a mixed skipped list as well.
			"referenced footnote",
			fnBody + fnOne + skipOne + "\n\n" + fnTwo + keepOne + "\n",
			[]paragraphWant{{text: keepOne, tokens: 14,
				offset: fnSkip + len(skipOne) + 2 + len(fnTwo)}},
			[]paragraphWant{
				{text: fnBody[:len(fnBody)-2], tokens: 4, offset: 0},
				{text: skipOne, tokens: 1, offset: fnSkip},
			},
		},
		{
			"grapheme boundary",
			snapPrefix + skipOne + "\n\n" + snapPrefix + keepOne + "\n",
			[]paragraphWant{{text: snapPrefix + keepOne, tokens: 14,
				offset: len(snapPrefix) + len(skipOne) + 2}},
			[]paragraphWant{{text: snapPrefix + skipOne, tokens: 1, offset: 0}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := indexedCorpus(t)
			draft := writeDraft(t, root, c.body)
			result := scored(t, scoreRequest(root, draft))
			raw, err := os.ReadFile(draft)
			if err != nil {
				t.Fatalf("read the draft: %v", err)
			}
			assertReportingSurface(t, result, raw, indexedFloor, c.keep, c.skip)
		})
	}
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
// The same through the workflow. Two facts pulling opposite ways on the same paragraph, which is why a
// mutation subtracting each gap's length from the reported length passed
// everything: the inline-code fixture has an excision in the MIDDLE of a line,
// and nothing had one at the START of a continuation line. Measured, the
// excisions here are [10,+2) and [75,+2) — the indent spaces only, never the
// newline.
//
// Measured: [2,+22) at four tokens and [27,+74) at fourteen, one excision each.
func TestContinuationIndentationSurvivesTheWorkflow(t *testing.T) {
	t.Parallel()
	const bullet = "- "
	const gapSkip = "This is\n  short prose."
	const gapKeep = "The café stores every record in a signed chain\n  and verifies it on read."
	body := bullet + gapSkip + "\n" + bullet + gapKeep + "\n"
	skipAt := len(bullet)
	keepAt := skipAt + len(gapSkip) + 1 + len(bullet)

	root := indexedCorpus(t)
	draft := writeDraft(t, root, body)
	result := scored(t, scoreRequest(root, draft))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	assertReportingSurface(t, result, raw, indexedFloor,
		[]paragraphWant{{text: gapKeep, tokens: 14, offset: keepAt}},
		[]paragraphWant{{text: gapSkip, tokens: 4, offset: skipAt}})
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
func TestExcludedWordsDoNotAdmitAParagraphThroughTheWorkflow(t *testing.T) {
	t.Parallel()
	const crossing = "We waited `alpha beta gamma` days for records and shipped fixes."
	body := crossing + "\n\n" + keepOne + "\n"

	root := indexedCorpus(t)
	draft := writeDraft(t, root, body)
	result := scored(t, scoreRequest(root, draft))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	assertReportingSurface(t, result, raw, indexedFloor,
		[]paragraphWant{{text: keepOne, tokens: 14, offset: len(crossing) + 2}},
		[]paragraphWant{{text: crossing, tokens: 8, offset: 0}})
}

// An excision can adjoin the words either side of it without merging them.
//
// Measured: `Prefix`+"`code`"+`suffix.` spans [0,+19) and counts TWO lexical
// tokens, while `RunText` returns "Prefixsuffix." and counts one. Recomputing
// the count from RunText passed the whole suite, because every other
// inline-code fixture puts spaces around the code.
func TestAnExcisionBetweenTwoWordsSurvivesTheWorkflow(t *testing.T) {
	t.Parallel()
	const adjoined = "Prefix`code`suffix."
	body := adjoined + "\n\n" + keepOne + "\n"

	root := indexedCorpus(t)
	draft := writeDraft(t, root, body)
	result := scored(t, scoreRequest(root, draft))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	assertReportingSurface(t, result, raw, indexedFloor,
		[]paragraphWant{{text: keepOne, tokens: 14, offset: len(adjoined) + 2}},
		[]paragraphWant{{text: adjoined, tokens: 2, offset: 0}})
}

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
func TestANumberIsNotCountedAsALexicalTokenThroughTheWorkflow(t *testing.T) {
	t.Parallel()
	const shortNum = "Wait 42 days."
	const longNum = "We waited 42 days for the 7 records to reconcile and then shipped the 3 fixes."
	const crossing = "We waited 42 days for 7 records and shipped 3 fixes."
	body := shortNum + "\n\n" + crossing + "\n\n" + longNum + "\n"
	crossAt := len(shortNum) + 2
	longAt := crossAt + len(crossing) + 2

	root := indexedCorpus(t)
	draft := writeDraft(t, root, body)
	result := scored(t, scoreRequest(root, draft))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	assertReportingSurface(t, result, raw, indexedFloor,
		[]paragraphWant{{text: longNum, tokens: 13, offset: longAt}},
		[]paragraphWant{
			{text: shortNum, tokens: 2, offset: 0},
			{text: crossing, tokens: 8, offset: crossAt},
		})
}

// A draft where NOTHING clears the floor still reports the floor and the list.
//
// The zero-survivor case has its own path: `score` has no measurement to return
// and refuses with insufficient-evidence. Clearing `ParagraphFloor` and
// `Skipped` on that path left all three suites green, because every other test
// in this file has at least one survivor. It is also the case a user is most
// likely to hit — a short draft against a floor of ten — and the one where
// being told WHICH paragraphs were skipped matters most, since the answer is
// "all of them" and the report is otherwise empty.
func TestAnAllSkippedScoreStillReportsTheFloorAndTheList(t *testing.T) {
	t.Parallel()
	root := indexedCorpus(t)
	// Its own draft, and its own offsets: skipTwo sits at 6 here and at 80 in
	// the four-paragraph fixture. Reusing that fixture's expectations would
	// assert the wrong offset and pass only because nothing checked it.
	body, all := draftOf(
		paragraphWant{text: skipOne, tokens: 1},
		paragraphWant{text: skipTwo, tokens: 2},
	)
	draft := writeDraft(t, root, body)

	result := scored(t, scoreRequest(root, draft))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	assertReportingSurface(t, result, raw, indexedFloor, nil, all)
}

func floorLabel(n int) string {
	if n == 1 {
		return "floor one"
	}
	return "floor twelve"
}
