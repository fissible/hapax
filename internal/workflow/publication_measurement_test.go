package workflow_test

// #133. The splice gate establishes that a candidate lands as one included leaf,
// at the replaced span, in the same containers. It does NOT establish that the
// candidate means the same thing in the document as it did in isolation: it
// compares no excisions and no token interpretation.
//
// # Evidence, measured here
//
// A candidate wrapping most of its prose in a REFERENCE-style image, with
// `[ref]: /image.png` defined elsewhere in the draft, and a short tail left
// outside the image so the leaf stays `paragraph` rather than becoming `image`:
//
//	              excisions   tokens   lexical
//	isolation         0         37       31
//	in document       1          6        5
//
// Through the real workflow, before this slice:
//
//	in isolation   distance 1.338914 -> 0.804930, improves, preserved, one segment
//	the loop       improved=1, state="improved"            PUBLISHED
//	rescored       one scoreable segment left — the UNTOUCHED paragraph, at 0.530873
//	               skipped, below the floor: 1              the one it "improved"
//
// So the tool measured an improvement on a 31-token paragraph, published a
// 5-token paragraph, and the paragraph it reported improving is unmeasurable in
// its own output. The issue cites 0.729584 for the distance; this fixture's
// wording measures 0.804930, and the number here is the one this file reproduces.
//
// # Contract: the invariant is stated once, about the assembled document
//
// Before publication, scoring the ASSEMBLED document must reproduce what the loop
// measured. Not a per-candidate proxy: the splice gate assembles into the ORIGINAL
// document, and a candidate can be interpreted identically there and differently
// once its neighbours are replaced — the gap #132's tests leave explicitly
// undemonstrated for the structural case.
//
// On failure the run REFUSES with `publication-measurement-mismatch`: no
// publishable bytes, no publication evidence, and the recorded attempts kept.
// Provider spend buys attempts, not permission to publish an unsupported result.
// The refusal names the targets it found, by the node the plan named.
//
// # What is compared, and what that is not
//
// Two weaker predicates were considered and rejected. "Still scoreable and still
// improved" passes a paragraph whose prose the context changed, as long as the
// number lands somewhere good. "Segment counts and floor outcomes unchanged"
// conceals a substitution and conceals two offsetting losses.
//
// What is compared is the ordered token INTERPRETATION — each token's text, class
// and flags — against the last ACCEPTED candidate, which is not always the last
// attempt, plus the measurement that interpretation produced: a defined distance,
// the same contributing features, and the applicable band.
//
// # One coordinate hazard, measured
//
// Score reports carry FILE offsets and admitted-leaf spans carry stripped ones. On
// a draft with a byte order mark they differ by exactly three:
//
//	no BOM     segment 0 offset=0    admitted leaf offset=0    delta=0
//	with BOM   segment 0 offset=3    admitted leaf offset=0    delta=3
//	with BOM   segment 1 offset=153  admitted leaf offset=150  delta=3
//
// Any comparison between the two has to normalize that or it is off by three on
// every BOM'd document.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
	"github.com/fissible/hapax/internal/workflow"
)

// refImageCandidate improves in isolation and is reinterpreted in the document.
//
// The tail outside the image is load-bearing: without it `imageOnly` makes the
// leaf `role=image`, which the splice gate already refuses on containers, and the
// fixture would be testing that instead.
const refImageCandidate = "![A paragraph of ordinary prose that runs on past a single sentence so " +
	"the structure pass reads it as prose rather than as a heading][ref] and it says a thing."

// referenceDraft defines both references the candidates here resolve against.
//
// Two definitions, one draft: a link reference definition is not an included leaf,
// so neither changes the paragraph count or the targets, and every fixture in this
// file can share one draft shape.
func referenceDraft() string {
	return paragraphOne + "\n\n" + paragraphTwo + "\n\n[ref]: /image.png\n[!]: /other.png\n"
}

// A candidate the document reinterprets is refused, and nothing is published.
//
// The anchor. Every assertion here is about the run REFUSING rather than about the
// candidate being scored differently, because the defect was that it published.
func TestACandidateTheDocumentReinterpretsIsRefused(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, referenceDraft())
	plan := planned(t, planRequest(root, draft))

	// The fixture's own premise, asserted before it is relied on: in ISOLATION
	// this candidate improves and passes preserve, so nothing but the
	// reinterpretation can be what stops it.
	isolated := judge(t, root, paragraphOne, refImageCandidate)
	if !isolated.improves || !isolated.preserved || isolated.segments != 1 {
		t.Fatalf("in isolation the candidate measures improves=%v preserved=%v segments=%d; "+
			"the fixture no longer reaches the question", isolated.improves,
			isolated.preserved, isolated.segments)
	}

	provider := newProvider(t, map[string][]string{paragraphOne: {refImageCandidate}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != workflow.RefusalPublicationMeasurementMismatch {
		t.Fatalf("the run refused with %q, want %q — it published a paragraph it never "+
			"scored", result.Refusal, workflow.RefusalPublicationMeasurementMismatch)
	}
	if len(result.Bytes) != 0 {
		t.Errorf("%d publishable bytes beside a refusal", len(result.Bytes))
	}
	if len(result.Publication.Paragraphs) != 0 {
		t.Errorf("publication evidence beside a refusal: %+v", result.Publication.Paragraphs)
	}
	// The attempts STAND. A refusal at publication is not a claim that the loop
	// did not run, and the audit record is what says a candidate was accepted
	// locally and then refused globally.
	if countAttempts(t, root) == 0 {
		t.Error("the refusal discarded the attempt records; the loop's decisions " +
			"happened and belong in the audit table")
	}
	// EXACTLY the node the plan named, and nothing else. A substring check admits
	// extra identities, duplicates, and prose wrapped around the hash.
	target := targetsOf(plan)[0]
	if !reflect.DeepEqual(result.MeasurementMismatches, []string{target.NodeID}) {
		t.Errorf("the refusal reports %v, want exactly [%s]",
			result.MeasurementMismatches, target.NodeID)
	}
	// The acceptance EVIDENCE survives, not merely some attempt. `countAttempts > 0`
	// is satisfied by keeping a rejected attempt while losing or rewriting the
	// accepted one, which is the record that says the loop decided this candidate
	// was good and the document disagreed.
	stored := storedAttempt(t, root, result.InvocationID, target.NodeID, 0)
	if !stored.Accepted {
		t.Error("the stored attempt is no longer accepted; the loop's local decision " +
			"is what the audit table is for")
	}
	if stored.CandidateHash != identity.HashBytes([]byte(refImageCandidate)) {
		t.Errorf("the stored attempt records candidate %.12s, not the one the provider "+
			"returned", stored.CandidateHash)
	}
}

// One mismatch refuses the WHOLE run, including the clean replacement beside it.
//
// The refusal is about the document, so a run with one good replacement and one
// reinterpreted one publishes nothing at all. Without this, an implementation
// could drop the offending replacement and publish the rest — which would be a
// different product decision, made silently.
func TestOneMismatchRefusesTheWholeRunIncludingTheCleanReplacement(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, referenceDraft())
	plan := planned(t, planRequest(root, draft))
	targets := targetsOf(plan)
	if len(targets) != 2 {
		t.Fatalf("the draft planned %d targets and this test needs two", len(targets))
	}

	provider := newProvider(t, map[string][]string{
		paragraphOne: {improvesOne},        // clean
		paragraphTwo: {reinterpretedTwo()}, // reinterpreted
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	// BOTH targets must have been accepted, or the whole-run claim is untested.
	// An earlier version of this fixture used a candidate measuring exactly what
	// `paragraphTwo` measures — 0.5308734172437329 either way — so the loop refused
	// it as not-improved and only the clean replacement was ever accepted.
	for i, outcome := range result.Outcomes {
		if !outcome.Changed {
			t.Fatalf("target %d was not accepted (%v); this test needs a clean "+
				"replacement AND a reinterpreted one", i, outcome.Rejections)
		}
	}

	if result.Refusal != workflow.RefusalPublicationMeasurementMismatch {
		t.Fatalf("the run refused with %q, want %q", result.Refusal,
			workflow.RefusalPublicationMeasurementMismatch)
	}
	if len(result.Bytes) != 0 {
		t.Errorf("%d publishable bytes; the clean replacement must not publish on its own",
			len(result.Bytes))
	}
	if len(result.Publication.Paragraphs) != 0 {
		t.Errorf("publication evidence for a refused run: %+v", result.Publication.Paragraphs)
	}
	// The mismatch names the SECOND target only, after the first one's acceptance
	// shifted it.
	if !reflect.DeepEqual(result.MeasurementMismatches, []string{targets[1].NodeID}) {
		t.Errorf("mismatches = %v, want exactly [%s]", result.MeasurementMismatches,
			targets[1].NodeID)
	}
}

// TWO reinterpreted targets name BOTH, so returning at the first fails.
//
// Every other refusal here has exactly one mismatch, and a checker that returns
// as soon as it finds one passes the whole suite — measured by codex. Both
// candidates are accepted by the loop, so the run reaches the publication check
// with two replacements that do not reproduce.
func TestTwoReinterpretedTargetsAreBothNamed(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, referenceDraft())
	plan := planned(t, planRequest(root, draft))
	targets := targetsOf(plan)
	if len(targets) != 2 {
		t.Fatalf("the draft planned %d targets and this test needs two", len(targets))
	}

	provider := newProvider(t, map[string][]string{
		paragraphOne: {refImageCandidate},
		paragraphTwo: {reinterpretedTwo()},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	// Both accepted, or there is only one mismatch to find and this test is the
	// single-mismatch case again.
	for i, outcome := range result.Outcomes {
		if !outcome.Changed {
			t.Fatalf("target %d was not accepted (%v); this test needs BOTH reinterpreted "+
				"candidates accepted", i, outcome.Rejections)
		}
	}

	if result.Refusal != workflow.RefusalPublicationMeasurementMismatch {
		t.Fatalf("the run refused with %q, want %q", result.Refusal,
			workflow.RefusalPublicationMeasurementMismatch)
	}
	want := []string{targets[0].NodeID, targets[1].NodeID}
	if !reflect.DeepEqual(result.MeasurementMismatches, want) {
		t.Errorf("mismatches = %v, want BOTH nodes %v — a check that returns at the "+
			"first one it finds reports a single target and the user never learns the "+
			"second paragraph is wrong too", result.MeasurementMismatches, want)
	}
	if len(result.Bytes) != 0 || len(result.Publication.Paragraphs) != 0 {
		t.Errorf("%d bytes and %d publication entries beside a refusal",
			len(result.Bytes), len(result.Publication.Paragraphs))
	}
}

// reinterpretedTwo is paragraphTwo's reinterpreted candidate, so a mismatch can
// sit at a LATER target whose span an earlier acceptance has already moved.
//
// Measured: 0.5308734172437329 to 0.4845778534366120, preserved, tells unchanged,
// so the loop accepts it. The obvious construction — wrapping the leading words
// the way `scoreableReinterpretation` does — measures EXACTLY what the original
// does and is refused as not-improved, which is how this test came to assert a
// whole-run refusal on a run with only one acceptance.
func reinterpretedTwo() string { return "![?][!] " + improvesTwo }

// A BOM'd draft refuses too.
//
// `TestABOMdDraftIsNotAMismatch` pins the direction that must not refuse; this
// pins the other. Skipping the check entirely on BOM'd documents passes that one
// and fails this one, which is the cheapest way to make the coordinate problem go
// away and the wrong one.
func TestABOMdDraftStillRefusesAReinterpretation(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, "\ufeff"+referenceDraft())
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{paragraphOne: {refImageCandidate}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != workflow.RefusalPublicationMeasurementMismatch {
		t.Errorf("a BOM'd draft refused with %q, want %q — the check is skipping "+
			"documents it cannot align", result.Refusal,
			workflow.RefusalPublicationMeasurementMismatch)
	}
}

// An ordinary candidate still publishes.
//
// The other half, and the one that stops this slice from being a check that
// refuses everything: the same draft, with a candidate the document interprets
// exactly as it was scored.
func TestACandidateTheDocumentInterpretsIdenticallyIsPublished(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, referenceDraft())
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != "" {
		t.Fatalf("an ordinary improving candidate was refused as %q", result.Refusal)
	}
	if result.Improved != 1 {
		t.Fatalf("improved=%d, want 1: %+v", result.Improved, result.Outcomes)
	}
	if len(result.Bytes) == 0 {
		t.Error("nothing to publish after an acceptance")
	}
	if len(result.Publication.Paragraphs) != 1 {
		t.Errorf("%d publication entries for one changed target",
			len(result.Publication.Paragraphs))
	}
	if len(result.MeasurementMismatches) != 0 {
		t.Errorf("a clean run reports mismatches: %v", result.MeasurementMismatches)
	}
}

// Caught even when the published paragraph still scores WELL.
//
// This is the predicate "still scoreable and still improved" admits, and the
// anchor above does not refute it: the anchor's candidate falls below the floor,
// so a check that only refuses unscoreable results passes both. Measured, this one
// stays comfortably scoreable and still improving:
//
//	original                  1.338914
//	candidate in isolation    0.729584     29 lexical tokens
//	candidate in context      0.628054     26 lexical tokens
//
// Constructed by codex in review. The published paragraph is better by the number
// and is still not the paragraph whose improvement was reported.
func TestCaughtEvenWhenThePublishedParagraphStillScoresWell(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, referenceDraft())
	plan := planned(t, planRequest(root, draft))

	// The premises, asserted before they are relied on.
	isolated := judge(t, root, paragraphOne, scoreableReinterpretation())
	if !isolated.improves || !isolated.preserved {
		t.Fatalf("in isolation: improves=%v preserved=%v", isolated.improves, isolated.preserved)
	}
	// And in CONTEXT it is still scoreable and still improving, which is the whole
	// point of this case: `inContext > 0` proved neither.
	// writeProbe, not writeDraftBytes: the latter writes `root/draft.md`, which is
	// the path the plan was made against, so scoring a premise through it changes
	// the draft underneath the run and `Execute` correctly refuses as `stale-draft`.
	assembled := writeProbe(t, strings.Replace(referenceDraft(), paragraphOne,
		scoreableReinterpretation(), 1))
	inContext := scored(t, workflow.ScoreRequest{
		StorePath: defaultStorePath(root), Register: "essays", Path: assembled,
	})
	if len(inContext.Segments) == 0 {
		t.Fatal("the reinterpreted paragraph is not scoreable in context; this fixture " +
			"is the anchor's below-floor case again")
	}
	if !inContext.Segments[0].Distance.Defined {
		t.Fatal("the in-context distance is undefined; this fixture must stay scoreable")
	}
	if inContext.Segments[0].Distance.Value >= isolated.currentDistance {
		t.Fatalf("in context the paragraph measures %.6f against the original's %.6f; "+
			"this fixture needs it still IMPROVING or it cannot refute \"still scoreable "+
			"and still improved\"", inContext.Segments[0].Distance.Value,
			isolated.currentDistance)
	}

	provider := newProvider(t, map[string][]string{paragraphOne: {scoreableReinterpretation()}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != workflow.RefusalPublicationMeasurementMismatch {
		t.Errorf("a candidate the document reinterprets into DIFFERENT prose that still "+
			"scores well was refused as %q, want %q", result.Refusal,
			workflow.RefusalPublicationMeasurementMismatch)
	}
}

// And caught when the lexical count and the SCORE are both identical.
//
// The case that refutes every LEXICAL-count-based and score-based predicate at
// once. Not every count-based one: the total token count does distinguish 38 from
// 31, and an earlier version of this comment claimed more than the numbers show.
// Measured: 28 lexical tokens in both contexts, distance 0.5580165378445097 in
// both, the same feature deltas and the same band — and the ordered token stream
// shrinks from 38 to 31.
//
// Constructed by codex after I had claimed it could not be built. It can, and it
// is the reason the comparison is over the token interpretation rather than over
// anything derived from it.
func TestCaughtWhenTheCountAndTheScoreAreBothUnchanged(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root,
		paragraphOne+"\n\n"+paragraphTwo+"\n\n[!]: /image.png\n")
	plan := planned(t, planRequest(root, draft))

	candidate := streamPreservingReinterpretation()
	// The premises that make this case the one it claims to be: the lexical count
	// does not change, and NEITHER DOES THE MEASUREMENT — distance, contributing
	// features, feature deltas and band. Asserting only the count would let fixture
	// drift turn this back into the count test while the comment still claimed
	// otherwise.
	isolation := firstLeafLexical(t, candidate)
	inContextLexical := firstLeafLexical(t, candidate+"\n\n[!]: /other.png\n")
	if isolation != inContextLexical {
		t.Fatalf("lexical tokens are %d in isolation and %d in context; this fixture "+
			"needs them EQUAL or it is just the count test again",
			isolation, inContextLexical)
	}
	// Separate probe paths: `writeDraftBytes` would put both at `root/draft.md`,
	// where they would overwrite each other AND stale the planned draft.
	alone := writeProbe(t, candidate+"\n")
	withRef := writeProbe(t, candidate+"\n\n[!]: /other.png\n")
	scoredAlone := scored(t, workflow.ScoreRequest{
		StorePath: defaultStorePath(root), Register: "essays", Path: alone,
	})
	scoredWithRef := scored(t, workflow.ScoreRequest{
		StorePath: defaultStorePath(root), Register: "essays", Path: withRef,
	})
	if len(scoredAlone.Segments) != 1 || len(scoredWithRef.Segments) != 1 {
		t.Fatalf("the candidate measures %d segments alone and %d in context; this "+
			"fixture needs one each", len(scoredAlone.Segments), len(scoredWithRef.Segments))
	}
	a, b := scoredAlone.Segments[0], scoredWithRef.Segments[0]
	if a.Distance.Value != b.Distance.Value || a.Distance.Defined != b.Distance.Defined {
		t.Fatalf("the distance is %v/%.16f alone and %v/%.16f in context; this fixture "+
			"needs them IDENTICAL", a.Distance.Defined, a.Distance.Value,
			b.Distance.Defined, b.Distance.Value)
	}
	if !reflect.DeepEqual(a.Features, b.Features) {
		t.Fatal("the feature deltas differ; this fixture needs the whole measurement equal")
	}
	// The CONTRIBUTING feature set is not asserted here, because
	// `workflow.MeasuredDistance` does not carry it — the projection to this layer
	// keeps Value, Defined, Reason and Partial and drops `deviation.Distance.Features`.
	// It is compared where it exists, in `checkPublication`, which works on a
	// `score.Report`.
	if a.Band != b.Band {
		t.Fatalf("the band is %+v alone and %+v in context", a.Band, b.Band)
	}

	provider := newProvider(t, map[string][]string{paragraphOne: {candidate}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != workflow.RefusalPublicationMeasurementMismatch {
		t.Errorf("a candidate whose count and score are unchanged and whose token stream "+
			"is not was refused as %q, want %q", result.Refusal,
			workflow.RefusalPublicationMeasurementMismatch)
	}
}

// firstLeafLexical counts the lexical tokens of a body's FIRST included leaf.
//
// The first leaf rather than the only one, because two callers hand it a whole
// draft: the paragraph under test is the first, and a reference definition
// elsewhere is not an included leaf at all.
func firstLeafLexical(t *testing.T, body string) int {
	t.Helper()
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) == 0 {
		t.Fatalf("the body admitted no included leaf: %.40q", body)
	}
	tokens, err := doc.RunTokens(leaves[0])
	if err != nil {
		t.Fatalf("RunTokens: %v", err)
	}
	n := 0
	for _, token := range tokens {
		if token.Lexical {
			n++
		}
	}
	return n
}

// scoreableReinterpretation stays above the floor and still improves in context.
func scoreableReinterpretation() string {
	return strings.Replace(improvesOne, "A paragraph", "![A paragraph][ref]", 1)
}

// streamPreservingReinterpretation keeps the lexical count and the distance and
// changes the token stream.
func streamPreservingReinterpretation() string { return "![?][!] " + improvesOne }

// A neighbour whose span merely SHIFTED is not a mismatch.
//
// Every replacement moves the paragraphs after it, so the check has to compare
// untouched leaves at their translated spans. Comparing at the original offsets
// would report every run with a length-changing replacement as a mismatch, which
// is this slice's most likely way to be wrong in the refusing direction.
//
// `lengthensOne` is 16 bytes longer than the paragraph it replaces, so the second
// paragraph moves — measured at +16 in #132's fixtures.
func TestAnUntouchedNeighbourThatMerelyShiftedIsNotAMismatch(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))
	if len(lengthensOne) <= len(paragraphOne) {
		t.Fatalf("lengthensOne is %d bytes against %d; this test needs the later "+
			"paragraphs to MOVE", len(lengthensOne), len(paragraphOne))
	}

	// Only the first target is answered, so the second is an untouched neighbour
	// whose span shifts by exactly the first replacement's delta.
	provider := newProvider(t, map[string][]string{paragraphOne: {lengthensOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != "" {
		t.Fatalf("a run whose only change SHIFTED its neighbour was refused as %q",
			result.Refusal)
	}
	if result.Improved != 1 {
		t.Fatalf("improved=%d, want 1: %+v", result.Improved, result.Outcomes)
	}
	if len(result.MeasurementMismatches) != 0 {
		t.Errorf("the shifted neighbour is reported as a mismatch: %v",
			result.MeasurementMismatches)
	}
}

// The check compares against the LAST ACCEPTED candidate, not the last attempt.
//
// ADR 0006's loop keeps going after an acceptance, so the final attempt is often a
// refusal — and the text in the document is the last ACCEPTED one. A check
// comparing against `Attempts[len-1]` measures the wrong thing and would refuse
// every multi-attempt run that ended on a refusal, which is most of them.
func TestTheComparisonUsesTheLastAcceptedCandidateNotTheLastAttempt(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))
	target := targetsOf(plan)[0]

	// Accept, accept, then refuse. TWO acceptances, because with one a checker
	// that kept the FIRST accepted measurement passes — the first is also the
	// last. `matchesOne` measures exactly what the ORIGINAL paragraph measures,
	// not what `improvesOne` does, so against the second acceptance it is worse
	// and is refused as not-improved while `current` stays put.
	provider := newProvider(t, map[string][]string{
		paragraphOne:  {approachesOne},
		approachesOne: {improvesOne},
		improvesOne:   {matchesOne},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	request := executeRequest(plan, localChoice())
	request.Attempts = 3
	result := executed(t, runner, request)

	outcome := outcomeAt(t, result, target.Index)
	if len(outcome.Rejections) != 3 || outcome.Rejections[0] != "" ||
		outcome.Rejections[1] != "" || outcome.Rejections[2] == "" {
		t.Fatalf("the target recorded %v; this test needs TWO acceptances then a refusal",
			outcome.Rejections)
	}
	if result.Refusal != "" {
		t.Fatalf("a run that accepted and then refused was refused at publication as %q — "+
			"the comparison is against the last ATTEMPT rather than the last accepted "+
			"candidate", result.Refusal)
	}
	if result.Improved != 1 {
		t.Fatalf("improved=%d, want 1", result.Improved)
	}
	// And the published bytes hold the ACCEPTED text, not the refused one.
	if !strings.Contains(string(result.Bytes), improvesOne) {
		t.Error("the published bytes do not hold the accepted candidate")
	}
	if strings.Contains(string(result.Bytes), matchesOne) {
		t.Error("the published bytes hold the REFUSED candidate")
	}
}

// A BOM'd draft is compared in one coordinate system.
//
// Measured: score reports carry file offsets and admitted spans carry stripped
// ones, differing by exactly 3 on a BOM'd document. An implementation comparing
// the two without normalizing is off by three on every such draft, and the
// direction of the error — reporting a mismatch for a document that has none — is
// the one that refuses honest work.
func TestABOMdDraftIsNotAMismatch(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, "\ufeff"+paragraphOne+"\n\n"+paragraphTwo+"\n")
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != "" {
		t.Fatalf("a BOM'd draft was refused as %q; the comparison is mixing file and "+
			"stripped coordinates", result.Refusal)
	}
	if result.Improved != 1 {
		t.Fatalf("improved=%d on a BOM'd draft: %+v", result.Improved, result.Outcomes)
	}
	if !strings.HasPrefix(string(result.Bytes), "\ufeff") {
		t.Error("the published bytes lost the byte order mark")
	}
	if len(result.MeasurementMismatches) != 0 {
		t.Errorf("a BOM'd draft reports mismatches: %v", result.MeasurementMismatches)
	}
}

// The refusal is in the declared vocabulary.
func TestThePublicationMeasurementRefusalIsDeclared(t *testing.T) {
	t.Parallel()
	if workflow.RefusalPublicationMeasurementMismatch != "publication-measurement-mismatch" {
		t.Errorf("the refusal is spelled %q", workflow.RefusalPublicationMeasurementMismatch)
	}
	seen := 0
	for _, refusal := range workflow.Refusals() {
		if refusal == workflow.RefusalPublicationMeasurementMismatch {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the refusal appears %d times in Refusals(): %v", seen, workflow.Refusals())
	}
}
