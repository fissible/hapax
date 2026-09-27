package workflow

import (
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// The gate that actually measures
// ---------------------------------------------------------------------------
//
// #91's policy is only a policy if something feeds it. `text.Scripts` and
// `ScriptSet.Introduced` landed as a separate slice and had NO production
// caller — which is precisely the condition that kept the incident live after
// the measurement existed. `executionGate.Language` is the one place that
// changes, and without these tests the whole acceptance policy can go green
// with `hapax rewrite` behaving exactly as it did on the day of the incident.
//
// These are internal tests because `executionGate` is unexported and the seam
// is the point: the assertion is that this gate reports what the measurement
// says, not that the loop refuses — the loop's half is `internal/rewrite`.

func TestTheExecutionGateReportsTheScriptsACandidateIntroduces(t *testing.T) {
	for _, c := range []struct {
		name               string
		current, candidate string
		want               []string
	}{
		{
			// The incident: English prose returned in Han.
			name:      "the reported incident",
			current:   "The argument turns on a distinction the author never draws.",
			candidate: "論点は著者が引かない区別にかかっている。",
			want:      []string{"Han", "Hiragana"},
		},
		{
			// A Japanese writer's paragraph gaining a script it did not use.
			// Hiragana and Han are already present; Katakana is not.
			name:      "one script added to a paragraph that already mixes two",
			current:   "犬が好きです。",
			candidate: "イヌが好きです。",
			want:      []string{"Katakana"},
		},
		{
			name:      "an ordinary rewrite introduces nothing",
			current:   "The argument turns on a distinction the author never draws.",
			candidate: "The argument rests on a distinction the author never makes.",
			want:      nil,
		},
		{
			// Dropping a script is not introducing one. The policy is about
			// what arrives, not about what leaves.
			name:      "a script removed is not a script introduced",
			current:   "The author, 著者, never draws it.",
			candidate: "The author never draws it.",
			want:      nil,
		},
		{
			// Punctuation, digits and spaces are Common, and Common is not a
			// script a text is written in. A candidate differing only in them
			// introduces nothing.
			name:      "common characters are not a script",
			current:   "The author never draws it",
			candidate: "The author (1979) never draws it — twice!",
			want:      nil,
		},
		{
			// THE LOW-SHARE CASE, and the one this file exists to pin.
			//
			// `internal/rewrite` specifies "any introduction, no threshold" as
			// a refusal rule — but a threshold would never be written there.
			// It would be written HERE, in the measurement, and every other
			// fixture in this table introduces at least 5.9% of the letters,
			// so a `Share(name) >= 0.05` filter passed the entire repository.
			//
			// Measured: 78 letters, one of them Han, 1.28%. Below any
			// threshold anyone would pick, and the policy's own example — "a
			// single foreign name is around 2%" is the argument the slice
			// makes for refusing to have a threshold at all.
			name:      "one foreign character is still an introduction",
			current:   "The quick brown fox jumps over the lazy dog again and again, and the author never draws it at all.",
			candidate: "The quick brown fox jumps over the lazy dog again and again, and the author 著 never draws it at all.",
			want:      []string{"Han"},
		},
		{
			name:      "two scripts at once, named in sorted order",
			current:   "The author never draws it.",
			candidate: "The author, автор 著者, never draws it.",
			want:      []string{"Cyrillic", "Han"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The original and the current coincide on a first attempt, which is
			// what these introduction cases are about; #107's growth anchor is
			// exercised separately.
			got, err := executionGate{}.Language(c.current, c.current, c.candidate)
			if err != nil {
				t.Fatalf("Language: %v", err)
			}
			if len(got.Introduced) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got.Introduced, c.want) {
				t.Errorf("Introduced = %v, want %v", got.Introduced, c.want)
			}
		})
	}
}

// The gate is not a stub.
//
// A `Language` method returning an empty verdict satisfies the interface, keeps
// the whole repository green, and leaves the binary behaving as it did before
// #91 was filed. This asserts the one thing a stub cannot do.
func TestTheExecutionGateIsNotAHardcodedEmptyVerdict(t *testing.T) {
	const only = "A paragraph written in one script only."
	got, err := executionGate{}.Language(only, only, only+" 著者.")
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(got.Introduced) == 0 {
		t.Error("the gate reports no introduction for a candidate that adds Han; " +
			"it is not consulting the measurement")
	}
}

// ---------------------------------------------------------------------------
// #107: the ceiling, and which argument establishment is judged against
// ---------------------------------------------------------------------------

// The gate reports the scripts that cross the declared ceiling.
func TestTheExecutionGateReportsScriptsCrossingTheCeiling(t *testing.T) {
	for _, c := range []struct {
		name, original, candidate string
		want                      []string
	}{
		{
			// #107 itself: Han was already present, so nothing is introduced.
			name:      "two percent becomes the paragraph",
			original:  "The author 著 never draws it.",
			candidate: "作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。",
			want:      []string{"Han"},
		},
		{
			// The case the two discarded designs refused. A ceiling does not
			// move with length, so lengthening is free.
			name:      "lengthening a monoscript paragraph",
			original:  "The author never draws it.",
			candidate: "The author never draws it, and the reader never thinks to ask him why.",
			want:      nil,
		},
		{
			// MULTI-SCRIPT lengthening, whose absence hid the previous design's
			// failure: its share of the original is below one, so every
			// proportional bound refused this.
			name:      "lengthening a paragraph carrying a foreign character",
			original:  "The author 著 never draws it.",
			candidate: "The author 著 never draws it, and the reader never thinks to ask why that is.",
			want:      nil,
		},
		{
			name:      "removing a script while lengthening",
			original:  "The author 著 never draws it at all, he said.",
			candidate: "The argument turns on a distinction the author never draws.",
			want:      nil,
		},
		{
			// Established: Han is half the original, so a bilingual paragraph
			// may be rewritten in either of its languages.
			name:      "a script already above the ceiling is unconstrained",
			original:  "abc 漢字漢",
			candidate: "作者從不畫它，著者亦然。",
			want:      nil,
		},
		{
			// Below the ceiling, so this rule is silent and #91's is not.
			name:      "one introduced character in a long paragraph",
			original:  "The argument turns on a distinction the author never draws at all in this paragraph.",
			candidate: "The argument turns 著 on a distinction the author never draws at all in this paragraph.",
			want:      nil,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := executionGate{}.Language(c.original, c.original, c.candidate)
			if err != nil {
				t.Fatalf("Language: %v", err)
			}
			if len(got.Overgrown) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got.Overgrown, c.want) {
				t.Errorf("Overgrown = %v, want %v", got.Overgrown, c.want)
			}
		})
	}
}

// The gate uses the DECLARED ceiling rather than one of its own.
//
// A gate that hardcoded a different number would pass the table above on most
// rows. This pins the boundary exactly: Han is 1 of 20 letters, which is
// `rewrite.ScriptCeiling` to the digit, and the bound is strict, so it does not
// cross.
func TestTheExecutionGateUsesTheDeclaredCeiling(t *testing.T) {
	if rewrite.ScriptCeiling != 0.05 {
		t.Fatalf("this test is written against a ceiling of 0.05; it is %v",
			rewrite.ScriptCeiling)
	}
	const onTheCeiling = "abcdefghijklmnopqrs著" // 20 letters, Han exactly 5%

	got, err := executionGate{}.Language("abcdefghijklmnopqrst", "abcdefghijklmnopqrst", onTheCeiling)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(got.Overgrown) != 0 {
		t.Errorf("Overgrown = %v at exactly the ceiling, want none — the bound is strict",
			got.Overgrown)
	}
	// One letter fewer of Latin puts Han above it: 1 of 19 is 5.26%.
	above := "abcdefghijklmnopqr著"
	got, err = executionGate{}.Language("abcdefghijklmnopqrst", "abcdefghijklmnopqrst", above)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if !reflect.DeepEqual(got.Overgrown, []string{"Han"}) {
		t.Errorf("Overgrown = %v just above the ceiling, want [Han]", got.Overgrown)
	}
}

// The gate uses the declared ESTABLISHMENT threshold, not a literal of its own.
//
// Its sibling got a boundary pair and this did not, so substituting the gate's
// threshold directly, the package accepted anything in (0.0455, 0.50] — a
// ten-fold range whose low end sits a thousandth above the ceiling. A policy
// change to the constant could also ship with the binary unchanged, because the
// call site could carry a literal.
//
// The pair: an original with Han at exactly 25% is established and the candidate
// is free; one letter more of Latin puts it at 23.81% and the same candidate is
// refused.
func TestTheExecutionGateUsesTheDeclaredEstablishmentThreshold(t *testing.T) {
	if rewrite.ScriptEstablished != 0.25 {
		t.Fatalf("this test is written against an establishment threshold of 0.25; "+
			"it is %v", rewrite.ScriptEstablished)
	}
	const atThreshold = "abcdefghijklmno漢字漢字漢"     // 20 letters, Han 5 = 25.00%
	const belowThreshold = "abcdefghijklmnop漢字漢字漢" // 21 letters, Han 5 = 23.81%
	const allHan = "作者從不畫它，著者亦然。"

	established, err := executionGate{}.Language(atThreshold, atThreshold, allHan)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(established.Overgrown) != 0 {
		t.Errorf("Overgrown = %v at exactly the establishment threshold, want none",
			established.Overgrown)
	}

	notYet, err := executionGate{}.Language(belowThreshold, belowThreshold, allHan)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if !reflect.DeepEqual(notYet.Overgrown, []string{"Han"}) {
		t.Errorf("Overgrown = %v just below the threshold, want [Han]", notYet.Overgrown)
	}
}

// Both thresholds are judged against the FIRST argument, not the second.
//
// The route the anchor closes is the COUNT condition. Raising establishment
// above the ceiling already closed the other one — a rewrite cannot make a
// script established, because anything over the ceiling is refused long before
// 25%. What remains is banking a count at exactly the ceiling and then
// shrinking, which doubles the share while the count stays flat.
func TestTheExecutionGateJudgesBothThresholdsFromTheOriginal(t *testing.T) {
	const origin = "abcdefghijklmnopqrst" // 20 Latin, Han 0
	const banked = "abcdefghijklmnopqrs著" // 20 letters, Han 1 = 5.00%
	const shrunk = "abcdefghi著"           // 10 letters, Han 1 = 10.00%

	// Rung one is admissible: exactly on the ceiling, and the bound is strict.
	first, err := executionGate{}.Language(origin, origin, banked)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(first.Overgrown) != 0 {
		t.Fatalf("rung one must be admissible or this test proves nothing: %v",
			first.Overgrown)
	}

	// Rung two, judged against rung one, is admissible because the COUNT did not
	// grow — one Han letter before and one after — even though the share doubled.
	ratchet, err := executionGate{}.Language(banked, banked, shrunk)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(ratchet.Overgrown) != 0 {
		t.Fatalf("against rung one the shrunk candidate must be admissible, or the "+
			"ladder this guards against does not exist: %v", ratchet.Overgrown)
	}

	// The real call: anchored on the original, with current at rung one. The
	// count grew from zero and the share is twice the ceiling.
	got, err := executionGate{}.Language(origin, banked, shrunk)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if !reflect.DeepEqual(got.Overgrown, []string{"Han"}) {
		t.Errorf("Overgrown = %v, want [Han] — both thresholds are judged from the "+
			"first argument, and judging them from the second admits this",
			got.Overgrown)
	}
}

// Introduction is still measured against the CURRENT text.
//
// The two facts use different anchors on purpose, so one call cannot be
// collapsed into a single comparison.
func TestTheExecutionGateMeasuresIntroductionFromTheCurrentText(t *testing.T) {
	const origin = "The argument turns on a distinction."
	const current = "The argument turns 著 on a distinction."
	const candidate = "The argument turns 著著著著著著著著 on a finer point."

	got, err := executionGate{}.Language(origin, current, candidate)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	// Han is in the current text, so it is not introduced...
	if len(got.Introduced) != 0 {
		t.Errorf("Introduced = %v, want none — Han is already in the current text",
			got.Introduced)
	}
	// ...but it is absent from the ORIGINAL and well over the ceiling.
	if !reflect.DeepEqual(got.Overgrown, []string{"Han"}) {
		t.Errorf("Overgrown = %v, want [Han]", got.Overgrown)
	}
}

// ---------------------------------------------------------------------------
// #116: the production preserve gate's argument order
// ---------------------------------------------------------------------------

// `preserve.Check` is symmetric in its VERDICT and asymmetric in its EVIDENCE.
// Swapping its arguments here refuses the same candidates and records every
// `lost` as an `invented` with a different digest — the decision survives and
// the audit trail lies. Measured, and it passed the whole repository, because the
// only assertion on the direction lived on a hand-copied duplicate of this body
// in `internal/rewrite`.
//
// So the claim is pinned on the production line, not on a copy of it.
func TestTheExecutionGatePreservesAgainstTheFirstArgument(t *testing.T) {
	// The laundering triple's endpoints: an entity that exists only by sentence
	// position in the first text and is absent from the second.
	const (
		original  = "Yesterday the market was busy and the sellers were loud."
		candidate = "the market was busy and the sellers were loud."
	)

	got, err := executionGate{}.Preserve(original, candidate)
	if err != nil {
		t.Fatalf("Preserve: %v", err)
	}
	if got.Preserved {
		t.Fatal("the candidate drops an item the original had; this fixture must be " +
			"refused or it proves nothing")
	}
	// LOST, not invented. The swap produces an equally unpreserved verdict with
	// an inverted identifier, so the verdict alone cannot tell them apart.
	const want = "preserve-v1:entity:lost:734e476d2f0f911f"
	if len(got.Identifiers) != 1 || got.Identifiers[0] != want {
		t.Errorf("Identifiers = %v, want [%s] — an argument-swapped gate records an "+
			"invention instead", got.Identifiers, want)
	}
}

// ---------------------------------------------------------------------------
// #115: would the candidate splice back as one leaf in the same place?
// ---------------------------------------------------------------------------
//
// The gate holds the document and the target span, so it can answer what the
// loop cannot: what this candidate BECOMES once put back where it came from.
//
// Every fixture below is measured against a real two-paragraph draft. The three
// conditions — exactly one included leaf, at exactly that span, in the same
// containers — are separated across fixtures, because a mutation dropping any one
// of them passed an earlier draft where they were not.
//
// `Leaves()` versus `IncludedLeaves()` IS separated, by the short-rewrite pair.
// An earlier draft claimed it could not be, on the grounds that every excluding
// shape is refused as `not-one-segment` first. That reasoning was wrong twice:
// these tests call the gate directly, so loop reachability is irrelevant to its
// contract, and the case below is reachable anyway.
//
// Measured, `"A rewritten paragraph of prose"` — five words, no terminal
// punctuation:
//
//	alone                 [paragraph inc=true  exc=""                      @0+30 [document]]
//	into a plain place    [paragraph inc=true  exc=""                      @0+30 [document]]
//	into a list place     [paragraph inc=FALSE exc="excluded-not-sentential" @2+30 [document list list-item]]
//
// In the list place the leaf exists at exactly the replaced span with exactly the
// original's containers, and is EXCLUDED. So span identity and containers both
// pass and only inclusion refuses it — `Leaves()` would accept. It is also the
// same BYTES getting different verdicts in different documents, which no other
// fixture here provides, and it defeats any string heuristic because the text
// carries no marker, no blank line and no fence.

const gateFirst = "A paragraph of ordinary prose that runs on past a single sentence so the " +
	"structure pass reads it as prose rather than as a heading; it says a thing."
const gateSecond = "A second paragraph doing likewise, at enough length to clear the floor and " +
	"be measured on its own terms rather than skipped."

// gateFor builds the gate a target would get: the document, and the span of the
// leaf whose raw bytes are want.
func gateFor(t *testing.T, body, want string) executionGate {
	t.Helper()
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	for _, leaf := range doc.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		if string(doc.Raw()[leaf.Span.Offset:leaf.Span.Offset+leaf.Span.Length]) == want {
			return executionGate{register: "essays", doc: doc, span: leaf.Span}
		}
	}
	t.Fatalf("no included leaf holds the wanted text")
	return executionGate{}
}

func TestTheExecutionGateJudgesWhetherACandidateSplicesBack(t *testing.T) {
	plain := gateFirst + "\n\n" + gateSecond + "\n\n"
	// A document whose first paragraph is a LIST ITEM, so a legitimate rewrite of
	// a nested paragraph can be shown to be admissible.
	nested := "- " + gateFirst + "\n\n" + gateSecond + "\n\n"

	for _, c := range []struct {
		name, body, target, candidate string
		want                          bool
	}{
		{
			name: "a plain rewrite in a plain place", body: plain, target: gateFirst,
			candidate: "A rewritten paragraph of ordinary prose that runs on past a single " +
				"sentence so the structure pass still reads it as prose here.",
			want: true,
		},
		{
			// TRAILING WHITESPACE. The re-parsed leaf excludes it, so the leaf is
			// exactly one byte shorter than the replacement text. A literal span
			// comparison refuses this, and it breaks a committed test in
			// `internal/assemble` that splices exactly such a replacement.
			name: "a rewrite with a trailing space", body: plain, target: gateFirst,
			candidate: "A rewritten paragraph of ordinary prose that runs on past a single " +
				"sentence so the structure pass still reads it as prose here. ",
			want: true,
		},
		{
			// CONTAINERS, and the case an implementation hardcoding `[document]`
			// would refuse. The original is `[document list list-item]` and the
			// candidate keeps it there, because the span excludes the marker.
			name: "a plain rewrite of a list item", body: nested, target: gateFirst,
			candidate: "A rewritten paragraph of ordinary prose that runs on past a single " +
				"sentence so the structure pass still reads it as prose here.",
			want: true,
		},
		{
			// CONTAINERS again, the other way: the original is top-level and the
			// candidate puts it inside a list. One included leaf, still
			// `role=paragraph`, so neither a leaf count nor a role check sees it.
			name: "a rewrite that becomes a list item", body: plain, target: gateFirst,
			candidate: "- A rewritten paragraph of ordinary prose that runs on past a single " +
				"sentence so the structure pass still reads it as prose here.",
			want: false,
		},
		{
			// LEAF COUNT. Measured: this scores as ONE segment, because only one
			// half clears the lexical floor, so the loop hands it here — and
			// spliced it is two leaves, the short one unscoreable forever.
			name: "a rewrite that splits in two", body: plain, target: gateFirst,
			candidate: "A rewritten paragraph here.\n\nAnd a second half that runs on past " +
				"a single sentence so the structure pass reads it as prose in its own right.",
			want: false,
		},
		{
			// INCLUSION, isolated. The leaf lands at exactly the replaced span
			// with exactly the original's containers, so span and containers both
			// pass; only `Included` refuses it. Paired with the row below, the
			// SAME bytes get opposite verdicts, so the verdict cannot be a
			// function of the candidate text.
			name: "a short rewrite in a plain place", body: plain, target: gateFirst,
			candidate: "A rewritten paragraph of prose",
			want:      true,
		},
		{
			name: "the same short rewrite in a list item", body: nested, target: gateFirst,
			candidate: "A rewritten paragraph of prose",
			want:      false,
		},
		{
			// ZERO leaves, and NON-LOCAL damage: an unterminated fence swallows the
			// rest of the document into one code block, so the second paragraph
			// disappears too. It also defeats a string heuristic that only looks
			// for a list marker or a blank line — this candidate has neither.
			name: "a rewrite opening an unterminated fence", body: plain, target: gateFirst,
			candidate: "```\nA rewritten paragraph of ordinary prose that runs on past a " +
				"single sentence so the structure pass reads it as code now.",
			want: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := gateFor(t, c.body, c.target).SpliceableIntoOriginal(c.candidate)
			if err != nil {
				t.Fatalf("SpliceableIntoOriginal: %v", err)
			}
			if got.Intact != c.want {
				t.Errorf("Intact = %v, want %v", got.Intact, c.want)
			}
		})
	}
}

// The check reads the document it was built with, not the candidate alone.
//
// Parsed in isolation a list-item rewrite reports `containers=[document]`, so an
// implementation that checked the candidate on its own would refuse every
// legitimate nested rewrite — the same defect as hardcoding `[document]`.
//
// An earlier version of this comment claimed the same candidate is admissible
// nested and refused plain. Its own assertions said the opposite, and no fixture
// anywhere in the slice had one text getting two verdicts — which is why a
// three-clause string heuristic survived. The short-rewrite pair in the table
// above is that fixture. What THIS test adds is the marked-up direction: a
// candidate carrying its own list marker is refused in both places, for two
// different reasons.
func TestTheSpliceVerdictDependsOnTheDocumentNotTheCandidateAlone(t *testing.T) {
	const candidate = "A rewritten paragraph of ordinary prose that runs on past a single " +
		"sentence so the structure pass still reads it as prose here."
	const listed = "- " + candidate

	plain := gateFirst + "\n\n" + gateSecond + "\n\n"
	nested := "- " + gateFirst + "\n\n" + gateSecond + "\n\n"

	// The bare candidate: admissible in both, because the span excludes any marker.
	for _, c := range []struct{ name, body string }{{"plain", plain}, {"nested", nested}} {
		got, err := gateFor(t, c.body, gateFirst).SpliceableIntoOriginal(candidate)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !got.Intact {
			t.Errorf("%s: a bare candidate should splice cleanly", c.name)
		}
	}

	// The marked-up candidate: refused in a plain place, because it ADDS a list.
	got, err := gateFor(t, plain, gateFirst).SpliceableIntoOriginal(listed)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	if got.Intact {
		t.Error("a candidate adding a list marker to a top-level paragraph should be refused")
	}

	// And in a nested place it is refused too, because it adds a SECOND list.
	// Same text, and the verdict turns on the document both times.
	got, err = gateFor(t, nested, gateFirst).SpliceableIntoOriginal(listed)
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	if got.Intact {
		t.Error("a candidate nesting a list inside a list item should be refused")
	}
}

// The gate re-parses the whole document once per candidate, so the cost is
// O(document) per attempt and the header's 89.6 ms figure needs an anchor.
//
// `Admit` is linear at roughly 62 MB/s; the cost is the goldmark tree build, and
// that is SUPERLINEAR — measured, 3.9× the bytes cost 37× the time. Targets grow
// linearly with document size while the parse grows faster, so at the default
// three attempts a 196 KB draft spends around two minutes purely re-parsing,
// against 45 ms for an 8 KB one.
//
// This benchmark exists so that figure is exercised rather than asserted, and so
// a `Structure` regression shows up here rather than in a user's slow run.
func BenchmarkSpliceGateOnARealisticDraft(b *testing.B) {
	var body []byte
	for len(body) < 200*1024 {
		body = append(body, []byte(gateFirst+"\n\n"+gateSecond+"\n\n")...)
	}
	doc, err := text.Admit(body)
	if err != nil {
		b.Fatalf("Admit: %v", err)
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) < 100 {
		b.Fatalf("the fixture admits %d leaves; it must be a realistic draft", len(leaves))
	}
	gate := executionGate{register: "essays", doc: doc, span: leaves[0].Span}
	const candidate = "A rewritten paragraph of ordinary prose that runs on past a single " +
		"sentence so the structure pass still reads it as prose here."

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := gate.SpliceableIntoOriginal(candidate); err != nil {
			b.Fatalf("SpliceableIntoOriginal: %v", err)
		}
	}
}
