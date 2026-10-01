package workflow_test

// #132. Three components hash "the paragraph" and two of them mean different
// bytes than the third.
//
//	rewrite.go:443     CandidateHash = H(candidate)             the RAW provider string
//	workflow.go:966    draftLeaf.hash = H(admitted[leaf.Span])  the PLAN's leaf span
//	corpus.go:360      H(raw[leaf.Span])                        the SCREEN's leaf span
//
// #115 deliberately tolerates surrounding whitespace, so a candidate ending in a
// newline is accepted and recorded under the hash of the string WITH it, while
// both screens look up the leaf span WITHOUT it.
//
// # Evidence, measured end to end in this package
//
// One scripted candidate, one character changed, `result.Bytes` written back and
// the draft re-planned:
//
//	candidate                 round trip          recorded hash in the published leaves
//	clean                     already-rewritten   true
//	candidate + "\n"          TARGET              false
//	candidate + " "           TARGET              false
//
// No author edit is needed, and a trailing newline is the likeliest thing a
// language model emits.
//
// # Contract
//
// A published paragraph's identity is the hash of its leaf span in the
// RE-ADMITTED ASSEMBLED document: `H(admit(result.Bytes).Raw()[leaf.Span])`. That
// is already what the plan and the corpus screen compute, so the three are made
// to agree by adopting the two that already agreed.
//
// `rewrite_attempt.candidate_hash` stays the RAW provider string. The audit
// record should say what the provider returned, and that is a different question
// from what was published.
//
// # Why the FINAL document and not the splice gate's
//
// `executionGate.SpliceableIntoOriginal` already assembles its candidate,
// re-admits it and locates the resulting leaf — but it does that for ONE
// candidate spliced into the ORIGINAL document. The published document carries
// every target's replacement, so offsets shift. Measured on `targetStore`'s
// draft: `improvesOne` is 3 bytes shorter than `paragraphOne`, so the second
// target moves from offset 200 to 197, and `lengthensOne` is 16 bytes longer, so
// it moves to 216. A per-candidate verdict cannot see either.
//
// # Unresolved
//
// I could not construct a candidate that passes the per-candidate splice gate and
// then fails to map in the combined document — adjacent replacements are
// separated by the original blank line, and a candidate carrying its own blank
// line is already refused as two leaves. So the guard is specified as a COUNT
// invariant (`TestEveryChangedTargetHasExactlyOnePublicationEntry`) rather than
// exercised by a fixture, and whether such a candidate exists is unmeasured.

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
	"github.com/fissible/hapax/internal/workflow"
)

// publishedLeaves hashes every included leaf of an assembled document the way
// the plan and the corpus screen do, so the assertions below compare the
// recorded evidence against an independent computation rather than against the
// implementation's own.
func publishedLeaves(t *testing.T, raw []byte) map[string]int {
	t.Helper()
	doc, err := text.Admit(raw)
	if err != nil {
		t.Fatalf("admit the assembled document: %v", err)
	}
	out := map[string]int{}
	for _, leaf := range doc.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		span := doc.Raw()[leaf.Span.Offset : leaf.Span.Offset+leaf.Span.Length]
		out[identity.HashBytes(span)] = leaf.Span.Offset
	}
	return out
}

// hashOf returns the recorded publication hash for one target node.
func hashOf(t *testing.T, publication workflow.Publication, nodeID string) string {
	t.Helper()
	for _, paragraph := range publication.Paragraphs {
		if paragraph.NodeID == nodeID {
			return paragraph.ParagraphHash
		}
	}
	t.Fatalf("the publication evidence names no paragraph for node %.12s: %+v",
		nodeID, publication.Paragraphs)
	return ""
}

// The published hash is the leaf's, whatever whitespace the provider wrapped it in.
//
// The four rows are the measurement above plus a leading space, and the clean row
// is kept because it is the one case where the raw candidate hash and the leaf
// hash coincide: without it an implementation could record the raw hash and the
// other three rows would be the only thing failing, which is correct but leaves
// nothing asserting the clean case was not broken on the way past.
//
// The second assertion is the discriminator. For every row that carries
// whitespace, the RAW candidate hash must NOT be what was recorded — otherwise an
// implementation that kept `H(candidate)` satisfies "the hash is in the published
// leaves" by accident whenever the provider happens to be tidy.
func TestThePublishedHashIsTheLeafOfTheAssembledDocument(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, suffix, prefix string
	}{
		{name: "a clean candidate"},
		{name: "a trailing newline", suffix: "\n"},
		{name: "a trailing space", suffix: " "},
		{name: "a leading space", prefix: " "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, draft := targetStore(t)
			requireCandidates(t, root)
			plan := planned(t, planRequest(root, draft))
			target := targetsOf(plan)[0]

			offered := c.prefix + improvesOne + c.suffix
			provider := newProvider(t, map[string][]string{paragraphOne: {offered}})
			runner, _ := executingRunner(&arm{provider: provider}, nil)
			result := executed(t, runner, executeRequest(plan, localChoice()))
			if result.Improved != 1 {
				t.Fatalf("the candidate was not accepted (improved=%d); the whitespace "+
					"tolerance #115 relies on has changed and this fixture no longer "+
					"reaches the question: %+v", result.Improved, result.Outcomes)
			}

			recorded := hashOf(t, result.Publication, target.NodeID)
			leaves := publishedLeaves(t, result.Bytes)
			if _, found := leaves[recorded]; !found {
				t.Errorf("the recorded hash is not a leaf of the published bytes; the " +
					"screens look up leaf spans and would never match it")
			}
			raw := identity.HashBytes([]byte(offered))
			if c.prefix+c.suffix != "" && recorded == raw {
				t.Errorf("the recorded hash is H(the provider's string), which includes " +
					"the surrounding whitespace the published leaf does not")
			}
			// And the AUDIT record still says what the provider returned. The
			// cheapest wrong fix is to trim the candidate before hashing it, which
			// makes the two agree by destroying the thing the audit table is for.
			attempt := storedAttempt(t, root, result.InvocationID, target.NodeID, 0)
			if attempt.CandidateHash != raw {
				t.Errorf("the attempt records %.12s, want H(the provider's exact string) "+
					"%.12s — the audit record answers what was RETURNED, not what was "+
					"published", attempt.CandidateHash, raw)
			}
		})
	}
}

// Publishing and re-planning reports already-rewritten, whatever the whitespace.
//
// The end-to-end property, and the one that actually broke: it is what #111's
// guard and #109's screen both ask. Every assertion above is about one hash; this
// is about the two components agreeing.
func TestPublishingAndReplanningReportsAlreadyRewritten(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, suffix string }{
		{name: "a clean candidate"},
		{name: "a trailing newline", suffix: "\n"},
		{name: "a trailing space", suffix: " "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, draft := targetStore(t)
			requireCandidates(t, root)
			plan := planned(t, planRequest(root, draft))

			provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne + c.suffix}})
			runner, _ := executingRunner(&arm{provider: provider}, nil)
			result := executed(t, runner, executeRequest(plan, localChoice()))
			if result.Improved != 1 {
				t.Fatalf("improved=%d; the fixture must accept exactly one candidate", result.Improved)
			}

			// Nothing is recorded by the run itself. An implementation that wrote
			// the evidence at the end of a successful `Execute` passes every other
			// assertion here, because the explicit call below is idempotent — so
			// the empty screen BEFORE it is the only thing that separates them.
			db := openStore(t, defaultStorePath(root))
			before, err := db.PublishedParagraphs(ctx())
			if err != nil {
				t.Fatalf("PublishedParagraphs before publishing: %v", err)
			}
			if len(before) != 0 {
				t.Fatalf("Execute recorded %d published paragraphs on its own; nothing "+
					"had been written to the file yet: %v", len(before), before)
			}

			// What `cli` does, in the order `cli` does it: the bytes become
			// visible, and then the evidence is recorded.
			if err := os.WriteFile(draft, result.Bytes, 0o644); err != nil {
				t.Fatalf("publish: %v", err)
			}
			if err := runner.RecordPublication(ctx(), result.Publication); err != nil {
				t.Fatalf("RecordPublication: %v", err)
			}
			after, err := db.PublishedParagraphs(ctx())
			if err != nil {
				t.Fatalf("PublishedParagraphs after recording: %v", err)
			}
			if len(after) != 1 {
				t.Fatalf("%d published paragraphs after recording one, want 1: %v",
					len(after), after)
			}

			again := planned(t, planRequest(root, draft))
			if got := dispositionOf(t, again, 0); got != workflow.DispositionAlreadyRewritten {
				t.Errorf("the published paragraph re-plans as %q, want %q — the plan and "+
					"the loop do not agree on which bytes were published", got,
					workflow.DispositionAlreadyRewritten)
			}
			if again.ParagraphsAlreadyRewritten != 1 {
				t.Errorf("already-rewritten=%d, want 1", again.ParagraphsAlreadyRewritten)
			}
		})
	}
}

// The second target's hash accounts for the first replacement's shift.
//
// `lengthensOne` is 16 bytes longer than `paragraphOne`, so the second target
// moves from offset 200 to 216 in the published document. An implementation
// reading the ORIGINAL offset lands 16 bytes early, inside the document, and
// produces a hash of real bytes that are not the paragraph — which is why this
// fixture LENGTHENS rather than shortening: `improvesOne` is 3 bytes shorter and
// the wrong read falls off the end, where any bounds check turns a wrong answer
// into an error and the mistake is not the one under test.
func TestTheSecondTargetsHashAccountsForTheFirstReplacementsShift(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))
	targets := targetsOf(plan)
	if len(targets) != 2 {
		t.Fatalf("the draft planned %d targets and this test needs two", len(targets))
	}
	if len(lengthensOne) <= len(paragraphOne) {
		t.Fatalf("lengthensOne is %d bytes against paragraphOne's %d; this test needs it "+
			"LONGER or the wrong read lands outside the document",
			len(lengthensOne), len(paragraphOne))
	}

	provider := newProvider(t, map[string][]string{
		paragraphOne: {lengthensOne},
		// Trailing whitespace on the second as well, so neither target can be
		// satisfied by the raw candidate hash.
		paragraphTwo: {improvesTwo + "\n"},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))
	if result.Improved != 2 {
		t.Fatalf("improved=%d; this test needs both targets rewritten: %+v",
			result.Improved, result.Outcomes)
	}

	// The EXACT hash per target, not merely "a published leaf". Asserting
	// membership alone is satisfied by giving both targets H(lengthensOne): it is
	// a leaf of the published bytes, and its offset is not the second target's
	// original one either.
	leaves := publishedLeaves(t, result.Bytes)
	shift := len(lengthensOne) - len(paragraphOne)
	for i, c := range []struct {
		published  string
		wantOffset int
	}{
		{lengthensOne, targets[0].Offset},
		{improvesTwo, targets[1].Offset + shift},
	} {
		want := identity.HashBytes([]byte(c.published))
		if got := hashOf(t, result.Publication, targets[i].NodeID); got != want {
			t.Errorf("target %d records %.12s, want the hash of the paragraph it published",
				i, got)
			continue
		}
		offset, found := leaves[want]
		if !found {
			t.Errorf("target %d: that hash is not a leaf of the published bytes", i)
			continue
		}
		if offset != c.wantOffset {
			t.Errorf("target %d: its published leaf sits at %d, want %d — the first "+
				"replacement moved it by %+d", i, offset, c.wantOffset, shift)
		}
	}
}

// Every changed target has exactly one publication entry, and nothing else does.
//
// The count invariant stands in for the mapping guard: if a leaf cannot be
// located in the published document, the implementation must fail rather than
// return publishable bytes with evidence that is short. An unchanged target
// contributes nothing, so the count is the number of changed ones and not the
// number of targets.
func TestEveryChangedTargetHasExactlyOnePublicationEntry(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		replies map[string][]string
		want    int
	}{
		{"neither target changes", map[string][]string{}, 0},
		{"only the first changes", map[string][]string{paragraphOne: {improvesOne}}, 1},
		// The mirror, because "one of two" is satisfied by evidence naming
		// whichever target the implementation happens to visit first.
		{"only the second changes", map[string][]string{paragraphTwo: {improvesTwo}}, 1},
		{"both change", map[string][]string{
			paragraphOne: {improvesOne}, paragraphTwo: {improvesTwo},
		}, 2},
		{"a refused candidate changes nothing", map[string][]string{paragraphOne: {matchesOne}}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, draft := targetStore(t)
			requireCandidates(t, root)
			plan := planned(t, planRequest(root, draft))
			provider := newProvider(t, c.replies)
			runner, _ := executingRunner(&arm{provider: provider}, nil)
			result := executed(t, runner, executeRequest(plan, localChoice()))

			if result.Improved != c.want {
				t.Fatalf("improved=%d, want %d; the fixture is not producing the case "+
					"this row names: %+v", result.Improved, c.want, result.Outcomes)
			}
			if got := len(result.Publication.Paragraphs); got != c.want {
				t.Errorf("%d publication entries for %d changed targets", got, c.want)
			}
			// WHICH targets, not how many. A count plus uniqueness is satisfied by
			// evidence naming the wrong node, which is the mistake the shift test
			// is about in a form this fixture can also reach.
			changed := map[string]bool{}
			for _, outcome := range result.Outcomes {
				if outcome.Changed {
					changed[outcome.NodeID] = true
				}
			}
			recorded := map[string]bool{}
			for _, paragraph := range result.Publication.Paragraphs {
				if recorded[paragraph.NodeID] {
					t.Errorf("node %.12s appears twice in the evidence", paragraph.NodeID)
				}
				recorded[paragraph.NodeID] = true
				if !changed[paragraph.NodeID] {
					t.Errorf("the evidence names node %.12s, which did not change",
						paragraph.NodeID)
				}
			}
			for node := range changed {
				if !recorded[node] {
					t.Errorf("node %.12s changed and is absent from the evidence", node)
				}
			}
		})
	}
}

// Only the LAST accepted candidate is published.
//
// ADR 0006's loop advances `current` on acceptance, so a target with two
// acceptances published one paragraph and recorded two attempts. #134's second
// consequence, and the one I previously dismissed as harmless.
func TestOnlyTheFinalAcceptedCandidateIsPublished(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))
	target := targetsOf(plan)[0]

	// Two candidates for one target, each better than the last. Measured against
	// this fixture's release: paragraphOne 1.338914, approachesOne 0.949697,
	// improvesOne 0.558017.
	provider := newProvider(t, map[string][]string{
		paragraphOne:  {approachesOne},
		approachesOne: {improvesOne},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	request := executeRequest(plan, localChoice())
	request.Attempts = 2
	result := executed(t, runner, request)

	outcome := outcomeAt(t, result, target.Index)
	if len(outcome.Rejections) != 2 || outcome.Rejections[0] != "" || outcome.Rejections[1] != "" {
		t.Fatalf("the target recorded %v; this test needs TWO acceptances or supersession "+
			"never happens", outcome.Rejections)
	}
	if len(result.Publication.Paragraphs) != 1 {
		t.Fatalf("two acceptances produced %d publication entries, want 1: %+v",
			len(result.Publication.Paragraphs), result.Publication.Paragraphs)
	}

	recorded := hashOf(t, result.Publication, target.NodeID)
	if want := identity.HashBytes([]byte(improvesOne)); recorded != want {
		t.Errorf("the published hash is not the LAST accepted candidate's")
	}
	if superseded := identity.HashBytes([]byte(approachesOne)); recorded == superseded {
		t.Error("the published hash is the FIRST acceptance's, which never left the process")
	}
}

// A run that fails after an acceptance carries no publication evidence.
//
// Measured: the provider answers once and then fails, `Execute` returns
// `provider down` and zero bytes. The accepted attempt is in the audit table,
// which is correct — a decision was made. Nothing was published, and the screens
// must not say otherwise.
func TestARunThatFailsAfterAnAcceptanceCarriesNoPublicationEvidence(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, repeatedDraft())
	plan := planned(t, planRequest(root, draft))
	if len(targetsOf(plan)) != 2 {
		t.Fatalf("the repeated draft planned %d targets and this test needs two",
			len(targetsOf(plan)))
	}

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne, improvesOne}})
	provider.before = func(_ *testing.T, call int) {
		if call == 2 {
			provider.err = errors.New("provider down")
		}
	}
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result, err := runner.Execute(ctx(), executeRequest(plan, localChoice()))
	if err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("Execute returned %v; this test needs the run to fail after an acceptance", err)
	}
	// The returned result carries neither bytes nor evidence. Discarding it would
	// leave an implementation free to hand back evidence beside an error, which
	// `cli` would then record after publishing nothing.
	if len(result.Bytes) != 0 {
		t.Errorf("a failed run returned %d publishable bytes", len(result.Bytes))
	}
	if len(result.Publication.Paragraphs) != 0 {
		t.Errorf("a failed run returned publication evidence: %+v", result.Publication)
	}

	// The screens see nothing, because nothing recorded a publication.
	db := openStore(t, defaultStorePath(root))
	published, err := db.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}
	if len(published) != 0 {
		t.Errorf("a failed run that wrote no bytes left %d paragraphs in the published "+
			"set: %v", len(published), published)
	}
	// And the acceptance IS still in the audit table, because a decision was
	// made. Without this, deleting the attempt recording satisfies the assertion
	// above and loses the audit trail instead of fixing the screen.
	if got := countAttempts(t, root); got == 0 {
		t.Error("the failed run recorded no attempt at all; the accepted decision " +
			"belongs in the audit table whether or not anything was published")
	}
}

// And an author document holding that text is still the author's.
//
// The consequence that makes the case above matter rather than being untidy: the
// screen's job is to keep this tool's output out of the corpus, and here it was
// removing prose the author wrote. Reproduced before the fix as
// `already-rewritten=1`.
func TestAnUnpublishedAcceptanceDoesNotScreenAnAuthorsOwnParagraph(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, repeatedDraft())
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne, improvesOne}})
	provider.before = func(_ *testing.T, call int) {
		if call == 2 {
			provider.err = errors.New("provider down")
		}
	}
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	if _, err := runner.Execute(ctx(), executeRequest(plan, localChoice())); err == nil {
		t.Fatal("the run must fail after an acceptance or this test asserts nothing")
	}

	// The author's own draft, whose first paragraph happens to be byte-identical
	// to what the failed run accepted.
	authors := writeDraftBytes(t, root, improvesOne+"\n\n"+paragraphTwo+"\n\n")
	again := planned(t, planRequest(root, authors))

	if got := dispositionOf(t, again, 0); got == workflow.DispositionAlreadyRewritten {
		t.Error("the author's own paragraph is reported as this tool's output; nothing " +
			"was ever published")
	}
	if again.ParagraphsAlreadyRewritten != 0 {
		t.Errorf("already-rewritten=%d, want 0", again.ParagraphsAlreadyRewritten)
	}
}

// The hashes are in STRIPPED coordinates on a draft carrying a byte order mark.
//
// `text.Admit` strips a leading BOM and `assemble.Assemble` puts it back, so the
// published FILE's offsets are three bytes past the admitted document's. The
// screens re-admit, so the evidence has to be in admitted coordinates — the same
// hazard `admittedBytes` records for the plan, one component along.
func TestThePublishedHashesAreInStrippedCoordinates(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, "\ufeff"+executableDraft())
	plan := planned(t, planRequest(root, draft))
	target := targetsOf(plan)[0]

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne + "\n"}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))
	if result.Improved != 1 {
		t.Fatalf("improved=%d on a BOM'd draft: %+v", result.Improved, result.Outcomes)
	}
	if !strings.HasPrefix(string(result.Bytes), "\ufeff") {
		t.Fatal("the assembled bytes lost the byte order mark, so this fixture no longer " +
			"distinguishes the two coordinate systems")
	}

	recorded := hashOf(t, result.Publication, target.NodeID)
	if _, found := publishedLeaves(t, result.Bytes)[recorded]; !found {
		t.Error("the recorded hash is not a leaf of the re-admitted document; it is in " +
			"file coordinates, three bytes off")
	}
}

// The evidence names the store the run resolved, not a path a caller supplies.
//
// `cli` hands this value straight back, so the binding is what stops a second
// database being written. Without it the composition root would have to
// reconstruct the store path, and `--store` resolution lives here.
func TestThePublicationEvidenceNamesTheResolvedStore(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if want := defaultStorePath(root); result.Publication.StorePath != want {
		t.Errorf("the evidence names store %q, want the resolved %q",
			result.Publication.StorePath, want)
	}
	if result.Publication.InvocationID != result.InvocationID {
		t.Errorf("the evidence names invocation %q and the run was %q",
			result.Publication.InvocationID, result.InvocationID)
	}
}

// And recording reaches the store the request named, not the default.
//
// The test above uses the default path, so an implementation that ignored the
// carried value and recomputed the default would satisfy it. This runs against an
// explicit store and then looks in THAT database, and in the default one.
func TestRecordingReachesTheStoreTheRequestNamed(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.db")
	copyFile(t, defaultStorePath(root), elsewhere)
	request := planRequest(root, draft)
	request.StorePath = elsewhere
	plan := planned(t, request)

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne + "\n"}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))
	if result.Improved != 1 {
		t.Fatalf("improved=%d against the copied store: %+v", result.Improved, result.Outcomes)
	}
	if result.Publication.StorePath != elsewhere {
		t.Fatalf("the evidence names %q, want the requested %q",
			result.Publication.StorePath, elsewhere)
	}
	if err := runner.RecordPublication(ctx(), result.Publication); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	named, err := openStore(t, elsewhere).PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs(elsewhere): %v", err)
	}
	if len(named) != 1 {
		t.Errorf("the named store holds %d published paragraphs, want 1: %v", len(named), named)
	}
	untouched, err := openStore(t, defaultStorePath(root)).PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs(default): %v", err)
	}
	if len(untouched) != 0 {
		t.Errorf("the DEFAULT store holds %d published paragraphs; the evidence went to "+
			"a database the request did not name: %v", len(untouched), untouched)
	}
}

// Index reports the gap the store holds, and invents none.
//
// Both halves, because each is a separate way to be wrong: hardcoding false
// passes the clean row and hardcoding true passes the marked one. The marker is
// written by the migration, which `migrations` being unexported puts out of reach
// here — so the marked store is produced by inserting the row the migration
// writes, which is the state a migrated store is in. How the row gets there is
// `internal/store`'s
// `TestTheMigrationRecordsThatEarlierRunsLeftNoPublicationEvidence`.
func TestIndexReportsThePublicationEvidenceGapTheStoreHolds(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		marked bool
	}{
		{"a store built from scratch", false},
		{"a store carrying the migration's marker", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := variedCorpus(t)
			// An index first, so there is a store to mark.
			indexed(t, indexRequest(root))
			if c.marked {
				markPublicationEvidenceGap(t, defaultStorePath(root))
			}

			result := indexed(t, indexRequest(root))

			if result.PublicationEvidenceGap != c.marked {
				t.Errorf("PublicationEvidenceGap = %v, want %v",
					result.PublicationEvidenceGap, c.marked)
			}
		})
	}
}

// markPublicationEvidenceGap writes the row the migration writes, with plain
// database/sql, because what is being set up is a store STATE rather than a
// migration history.
func markPublicationEvidenceGap(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx(),
		"INSERT INTO publication_evidence_gap (noticed_at) VALUES ('2026-09-30T00:00:00Z')"); err != nil {
		t.Fatalf("mark the gap: %v", err)
	}
}

// Every published paragraph survives `Rewrite` and reaches storage.
//
// `already_rewritten_test.go`'s handoff test drives `Runner.Rewrite` with ONE
// improvement, and every multi-paragraph assertion here calls `Execute` directly —
// so either handoff could discard the second paragraph's evidence while the file
// holds both. Reproduced as a mutation by codex: `Paragraphs = Paragraphs[:1]`
// before recording passed every cli test while the fixture carried one entry.
func TestRewriteCarriesEveryPublishedParagraphThroughToStorage(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	runner, invocation := executingRunner(&arm{provider: newProvider(t, map[string][]string{
		paragraphOne: {lengthensOne}, paragraphTwo: {improvesTwo + "\n"},
	})}, nil)

	outcome, err := runner.Rewrite(ctx(), workflow.RewriteInput{
		StorePath: defaultStorePath(root), CorpusRoot: root, Register: "essays",
		Path: draft, Choice: localChoice(),
	})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	report := outcome.Report()
	if report.Improved != 2 {
		t.Fatalf("Improved = %d, want 2; this test needs both paragraphs rewritten: %+v",
			report.Improved, report.Outcomes)
	}

	// The changed targets in SOURCE order, which for this draft is paragraphOne
	// then paragraphTwo.
	var changed []workflow.TargetOutcome
	for _, target := range report.Outcomes {
		if target.Changed {
			changed = append(changed, target)
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Index < changed[j].Index })
	if len(changed) != 2 {
		t.Fatalf("%d outcomes report a change, want 2", len(changed))
	}
	want := map[string]string{
		invocation + "/" + changed[0].NodeID: identity.HashBytes([]byte(lengthensOne)),
		invocation + "/" + changed[1].NodeID: identity.HashBytes([]byte(improvesTwo)),
	}

	evidence := outcome.Publication()
	got := map[string]string{}
	for _, paragraph := range evidence.Paragraphs {
		got[evidence.InvocationID+"/"+paragraph.NodeID] = paragraph.ParagraphHash
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Rewrite returned evidence\n%v\nwant\n%v", got, want)
	}

	if err := runner.RecordPublication(ctx(), evidence); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}
	if rows := storedPublicationRows(t, defaultStorePath(root)); !reflect.DeepEqual(rows, want) {
		t.Errorf("published_paragraph holds\n%v\nwant\n%v", rows, want)
	}
}

// The runner forwards the WHOLE batch, and forwards the store's failure.
//
// Every other call through `Runner.RecordPublication` supplies one paragraph, and
// the multi-row and atomicity cases go straight to `Store.RecordPublication`, so
// three broken runners survive all of them: one forwarding `Paragraphs[:1]`, one
// recording each paragraph in its own store call and losing atomicity, and one
// discarding the store's error and returning nil — which would make the real cli
// report success after the evidence was lost.
//
// Three cases over the same two-target run, two of which install the trigger the
// store's own atomicity test uses so the failure is the database's and arrives
// while a write is already in flight.
//
// EACH paragraph gets its own refusal case, with its own store. Refusing only one
// of them is satisfied by a runner that records paragraph by paragraph and
// iterates BACKWARDS: it meets the refused paragraph first, errors, and leaves
// nothing — while a failure on the other paragraph would leave partial evidence.
// Rejecting by hash makes the expectation independent of insertion order; it does
// not make the test exercise rollback, and an earlier version of this comment
// claimed that it did.
func TestTheRunnerForwardsTheWholeBatchAndItsFailure(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		// refuse is the index of the paragraph the trigger rejects, or -1 for the
		// success case.
		refuse  int
		wantRow int
	}{
		{"both paragraphs are recorded", -1, 2},
		{"the first row refused leaves neither", 0, 0},
		{"the second row refused leaves neither", 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, draft := targetStore(t)
			requireCandidates(t, root)
			plan := planned(t, planRequest(root, draft))
			provider := newProvider(t, map[string][]string{
				paragraphOne: {lengthensOne}, paragraphTwo: {improvesTwo + "\n"},
			})
			runner, invocation := executingRunner(&arm{provider: provider}, nil)
			result := executed(t, runner, executeRequest(plan, localChoice()))
			if result.Improved != 2 || len(result.Publication.Paragraphs) != 2 {
				t.Fatalf("improved=%d with %d published paragraphs; this test needs two "+
					"of each: %+v", result.Improved, len(result.Publication.Paragraphs),
					result.Outcomes)
			}
			if c.refuse >= 0 {
				refusePublicationOf(t, defaultStorePath(root),
					result.Publication.Paragraphs[c.refuse].ParagraphHash)
			}

			err := runner.RecordPublication(ctx(), result.Publication)

			if c.refuse >= 0 {
				if err == nil {
					t.Error("the store refused a row and RecordPublication returned nil; " +
						"cli would report success with the evidence lost")
				}
			} else if err != nil {
				t.Fatalf("RecordPublication: %v", err)
			}

			rows := storedPublicationRows(t, defaultStorePath(root))
			if len(rows) != c.wantRow {
				t.Fatalf("published_paragraph holds %d rows, want %d: %v",
					len(rows), c.wantRow, rows)
			}
			if c.wantRow == 0 {
				return
			}
			// Both rows, complete, so forwarding only the first paragraph fails
			// here rather than somewhere downstream.
			want := map[string]string{}
			for _, paragraph := range result.Publication.Paragraphs {
				want[invocation+"/"+paragraph.NodeID] = paragraph.ParagraphHash
			}
			if !reflect.DeepEqual(rows, want) {
				t.Errorf("published_paragraph holds\n%v\nwant\n%v", rows, want)
			}
		})
	}
}

// refusePublicationOf installs a trigger that rejects one paragraph hash, so a
// batch fails at the database with a write already in flight. The trigger is the
// test's, not the schema's.
func refusePublicationOf(t *testing.T, path, hash string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx(),
		`CREATE TRIGGER refuse_one BEFORE INSERT ON published_paragraph
			WHEN new.paragraph_hash = '`+hash+`'
			BEGIN SELECT RAISE(ABORT, 'refused by the test'); END`); err != nil {
		t.Fatalf("install the trigger: %v", err)
	}
}

// storedPublicationRows reads the evidence table keyed by invocation and node, so
// an assertion can be about the identities rather than only about a hash.
func storedPublicationRows(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx(),
		"SELECT invocation_id, node_id, paragraph_hash FROM published_paragraph")
	if err != nil {
		t.Fatalf("reading published_paragraph: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var invocation, node, hash string
		if err := rows.Scan(&invocation, &node, &hash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[invocation+"/"+node] = hash
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// copyFile duplicates a store so a run can be pointed at one that is not the
// default and still has a release to work against.
func copyFile(t *testing.T, from, to string) {
	t.Helper()
	body, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	if err := os.WriteFile(to, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", to, err)
	}
}
