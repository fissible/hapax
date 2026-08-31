package workflow_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/exemplar"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/workflow"
)

// Plan is everything hapax rewrite decides before it has spoken to a provider:
// which paragraphs need rewriting, what node each of them is, and which
// exemplars the author's own writing supplies. It is separated from the loop
// because all of it is decidable offline, and because the alternative — finding
// out that the audit record cannot be written after three provider calls have
// been paid for — is the shape of failure this project keeps meeting.

// ---------------------------------------------------------------------------
// The draft becomes nodes, because the audit record cannot reference anything else
// ---------------------------------------------------------------------------

// store.PutRewriteAttempt refuses a node_id that is not in the node table, and a
// draft has never been in the store: hapax score reads the file and persists
// nothing. So every attempt of every rewrite would have been refused as an
// invalid artifact, after the provider had already been paid.
func TestPlanningPutsTheDraftsParagraphsInTheStore(t *testing.T) {
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, planRequest(root, draft))

	if len(plan.Segments) == 0 {
		t.Fatal("the draft produced no segments")
	}
	assertSegmentsResolveIntoTheDraftSnapshot(t, root, draft, plan)
}

// The draft is a draft. It is not part of the author's corpus, it was never
// walked, and labelling it train or test would put the user's unfinished writing
// into a population something else measures against.
func TestTheDraftIsStoredAsADraft(t *testing.T) {
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, planRequest(root, draft))
	written, err := openStore(t, defaultStorePath(root)).Snapshot(ctx(), plan.DraftSnapshotID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(written.Documents) != 1 {
		t.Fatalf("the draft snapshot holds %d documents, want exactly the draft", len(written.Documents))
	}
	if got := written.Documents[0].Split; got != corpus.Draft {
		t.Errorf("the draft is stored in the %q split", got)
	}
}

// The hazard this guards is the one A2b met and A2c had to state: a segment
// index is a position in the ADMITTED sequence and a node ordinal is a position
// in ALL leaves. A plan that hands the loop the wrong node splices the rewrite
// over the wrong paragraph, and every band in the output still looks right.
//
// Checked against the draft's own bytes rather than against score. Both reach
// their sequence through profile.ParagraphLeaves, so comparing them to each
// other lets one shared ordinal mistake agree with itself — every segment could
// name some other valid paragraph and the comparison would still pass.
func TestEachPlannedSegmentSpansTheParagraphItClaims(t *testing.T) {
	root, _ := calibratedStore(t)
	// Headings and a code block between the paragraphs, so the admitted sequence
	// and the leaf sequence cannot agree by accident.
	draft := writeDraft(t, root, interleavedDraft)

	plan := planned(t, planRequest(root, draft))
	want := admittedLeaves(t, draft, storedFloor(t, root))
	if len(want) < 3 {
		t.Fatalf("the fixture admits %d paragraphs; this test needs at least three so a "+
			"shifted sequence cannot look like the right one", len(want))
	}
	if len(plan.Segments) != len(want) {
		t.Fatalf("planned %d segments over %d admitted paragraphs", len(plan.Segments), len(want))
	}

	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	opened := openStore(t, defaultStorePath(root))
	for i, segment := range plan.Segments {
		if segment.Index != i {
			t.Errorf("position %d carries index %d", i, segment.Index)
		}
		if segment.Offset < 0 || segment.Length <= 0 || segment.Offset+segment.Length > len(raw) {
			t.Fatalf("segment %d spans %d+%d, outside a %d byte draft",
				i, segment.Offset, segment.Length, len(raw))
		}
		if got := string(raw[segment.Offset : segment.Offset+segment.Length]); got != want[i].Text {
			t.Errorf("segment %d spans\n%q\nwant\n%q", i, got, want[i].Text)
		}
		// And the stored node agrees with the span the plan reported, so the
		// audit record will name the same bytes the loop was given.
		span, err := opened.Span(ctx(), segment.NodeID)
		if err != nil {
			t.Fatalf("segment %d node %s: %v", i, segment.NodeID, err)
		}
		if span.Offset != segment.Offset || span.Length != segment.Length {
			t.Errorf("segment %d is planned at %d+%d and stored at %d+%d",
				i, segment.Offset, segment.Length, span.Offset, span.Length)
		}
	}

	// Distinct nodes: an implementation that resolved every segment to the same
	// leaf would satisfy a per-segment check that only looked at one of them.
	seen := map[string]int{}
	for i, segment := range plan.Segments {
		if first, repeated := seen[segment.NodeID]; repeated {
			t.Errorf("segments %d and %d name the same node %s", first, i, segment.NodeID)
		}
		seen[segment.NodeID] = i
	}
}

// The plan and score do have to agree, because they are two readings of one
// draft and the user sees both. Asserted separately from the mapping above, so
// a failure says which of the two things went wrong.
func TestThePlanAndTheScoreAgreeAboutTheDraft(t *testing.T) {
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, interleavedDraft)

	plan := planned(t, planRequest(root, draft))
	report := scored(t, scoreRequest(root, draft))

	if len(plan.Segments) != len(report.Segments) {
		t.Fatalf("planned %d segments, scored %d", len(plan.Segments), len(report.Segments))
	}
	if plan.ParagraphsBelowFloor != report.ParagraphsBelowFloor {
		t.Errorf("plan counts %d paragraphs below the floor, score counts %d",
			plan.ParagraphsBelowFloor, report.ParagraphsBelowFloor)
	}
	for i := range plan.Segments {
		if plan.Segments[i].Band.Band != report.Segments[i].Band.Band {
			t.Errorf("segment %d is banded %q in the plan and %q in the score",
				i, plan.Segments[i].Band.Band, report.Segments[i].Band.Band)
		}
		if plan.Segments[i].LexicalTokens != report.Segments[i].LexicalTokens {
			t.Errorf("segment %d has %d lexical tokens in the plan and %d in the score",
				i, plan.Segments[i].LexicalTokens, report.Segments[i].LexicalTokens)
		}
	}
}

// Running the same command twice is an ordinary thing to do, and it is how the
// UNIQUE-constraint failure in store.Index reached a release. The draft's
// snapshot identity is content-derived, so the second run must replay it rather
// than insert it again.
func TestPlanningTheSameDraftTwiceIsTheSamePlan(t *testing.T) {
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	first := planned(t, planRequest(root, draft))
	second := planned(t, planRequest(root, draft))

	if first.DraftSnapshotID != second.DraftSnapshotID {
		t.Errorf("the same draft produced snapshots %s and %s", first.DraftSnapshotID, second.DraftSnapshotID)
	}
	if !reflect.DeepEqual(first.Segments, second.Segments) {
		t.Errorf("segments differ between runs:\n%+v\n%+v", first.Segments, second.Segments)
	}
	if first.ExemplarSelectionID != second.ExemplarSelectionID {
		t.Errorf("exemplars selected as %s then %s", first.ExemplarSelectionID, second.ExemplarSelectionID)
	}
}

// Indexing a draft must not disturb the corpus it is being measured against.
// store.Index prunes everything unreachable from the profile heads whenever it
// advances one; a draft is reachable from no head at all.
func TestPlanningADraftLeavesTheProfileAndItsCorpusAlone(t *testing.T) {
	root, releaseID := calibratedStore(t)
	path := defaultStorePath(root)
	head := profileHead(t, root)
	bundle := persistedBundle(t, path, "essays")
	corpusNodes := vectoredLeaves(t, path, bundle.Profile.SnapshotID, corpus.Train)

	draft := writeDraft(t, root, twoParagraphs)
	plan := planned(t, planRequest(root, draft))

	if got := profileHead(t, root); got != head {
		t.Errorf("the profile head moved from %s to %s", head, got)
	}
	if got := releaseHead(t, path, head); got != releaseID {
		t.Errorf("the release head moved from %s to %s", releaseID, got)
	}
	if got := vectoredLeaves(t, path, bundle.Profile.SnapshotID, corpus.Train); got != corpusNodes {
		t.Errorf("the corpus snapshot has %d train paragraphs after planning, had %d", got, corpusNodes)
	}
	if plan.DraftSnapshotID == bundle.Profile.SnapshotID {
		t.Error("the draft was written into the profile's own snapshot")
	}

	// The draft is reachable from no profile head, so the next ordinary index —
	// which prunes whatever its heads cannot reach — reclaims it. That is the
	// intended lifecycle: a draft is not corpus, and it survives only for as long
	// as an audit record points into it, which is B2's business.
	indexed(t, indexRequest(root))
	if _, err := openStore(t, path).Snapshot(ctx(), plan.DraftSnapshotID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the draft snapshot survived a reindex with %v; nothing points at it", err)
	}
}

// The snapshot is the draft the caller named, not merely some one-document
// snapshot in the draft split. An implementation that indexed the wrong file, or
// a stale copy of the right one, satisfies every count and every split check.
func TestTheStoredDraftIsTheFileThatWasAsked(t *testing.T) {
	root, _ := calibratedStore(t)
	draft := writeDraft(t, root, interleavedDraft)

	plan := planned(t, planRequest(root, draft))
	written, err := openStore(t, defaultStorePath(root)).Snapshot(ctx(), plan.DraftSnapshotID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(written.Documents) != 1 {
		t.Fatalf("the draft snapshot holds %d documents", len(written.Documents))
	}
	document := written.Documents[0]

	if want := admittedContentHash(t, draft); document.ContentHash != want {
		t.Errorf("the stored draft hashes %s; the file on disk hashes %s", document.ContentHash, want)
	}
	if base := filepath.Base(draft); document.Path != base {
		t.Errorf("the stored draft is at path %q, want %q relative to its own root", document.Path, base)
	}
	// Eligible, or it has no nodes at all and nothing can be rewritten.
	if document.Admission != corpus.Eligible {
		t.Errorf("the draft was admitted as %q", document.Admission)
	}
}

// ---------------------------------------------------------------------------
// Which paragraphs are targets
// ---------------------------------------------------------------------------

// The loop is for paragraphs that read as somebody else. A paragraph already in
// range is not improved by being rewritten, and sending it costs the user tokens
// to be told what they already knew.
func TestOnlyDriftingAndNotYouParagraphsAreTargets(t *testing.T) {
	for _, want := range []string{"in-range", "drifting", "not-you"} {
		t.Run(want, func(t *testing.T) {
			root := bandedStore(t, want)
			draft := writeDraft(t, root, twoParagraphs)

			plan := planned(t, planRequest(root, draft))
			if len(plan.Segments) == 0 {
				t.Fatal("no segments")
			}
			for _, segment := range plan.Segments {
				if segment.Band.Band != want {
					t.Fatalf("segment %d banded %q; the fixture was built to make every "+
						"paragraph %q and this test proves nothing otherwise",
						segment.Index, segment.Band.Band, want)
				}
				// The exact disposition, not merely "not a target": any declared
				// non-target value would otherwise satisfy the in-range case.
				expected := workflow.DispositionTarget
				if want == "in-range" {
					expected = workflow.DispositionInRange
				}
				if segment.Disposition != expected {
					t.Errorf("segment %d banded %q has disposition %q, want %q",
						segment.Index, segment.Band.Band, segment.Disposition, expected)
				}
			}
			if want == "in-range" && plan.Targets != 0 {
				t.Errorf("%d targets among in-range paragraphs", plan.Targets)
			}
			if want != "in-range" && plan.Targets != len(plan.Segments) {
				t.Errorf("%d targets among %d %s paragraphs", plan.Targets, len(plan.Segments), want)
			}
		})
	}
}

// assemble refuses a replacement whose leaf span contains excisions, because a
// leaf's run text DROPS them: rewriting `A line with `+"`code`"+` in it` and
// splicing the result over the raw span deletes the user's code silently.
// Discovering that after the provider has been paid is the wrong order, and
// recording it as a rewrite rejection would put an attempt that never happened
// into the audit record.
func TestAParagraphAssembleCannotSpliceIsNotATarget(t *testing.T) {
	// Both excision constructs the DESIGN table measured, because they reach the
	// refusal by different routes through the structure parser and an
	// implementation can get one without the other.
	for name, body := range map[string]string{"inline code": excisionDraft, "footnote reference": footnoteDraft} {
		t.Run(name, func(t *testing.T) { assertExcisionsAreNotTargets(t, body) })
	}
}

func assertExcisionsAreNotTargets(t *testing.T, body string) {
	t.Helper()
	root := bandedStore(t, "not-you")
	draft := writeDraft(t, root, body)

	plan := planned(t, planRequest(root, draft))
	want := admittedLeaves(t, draft, storedFloor(t, root))
	if len(plan.Segments) != len(want) {
		t.Fatalf("planned %d segments over %d admitted paragraphs", len(plan.Segments), len(want))
	}

	// Per paragraph, not "some paragraph got each disposition": an
	// implementation that swapped the two would satisfy a pair of counts.
	excised, plain := 0, 0
	for i, segment := range plan.Segments {
		expected := workflow.DispositionTarget
		if want[i].HasExcisions {
			expected = workflow.DispositionContainsExcisions
			excised++
		} else {
			plain++
		}
		if segment.Disposition != expected {
			t.Errorf("segment %d has excisions=%v and disposition %q, want %q",
				i, want[i].HasExcisions, segment.Disposition, expected)
		}
		// Excluded from rewriting is not excluded from measurement: the user is
		// entitled to know the paragraph reads as somebody else even though
		// hapax will not touch it.
		if segment.Band.Band != "not-you" {
			t.Errorf("segment %d is banded %q", i, segment.Band.Band)
		}
	}
	if excised == 0 {
		t.Fatalf("the fixture produced no paragraph with excisions over %d segments; "+
			"this test proves nothing", len(plan.Segments))
	}
	if plain == 0 {
		t.Fatal("the fixture produced no ordinary target, so an implementation that " +
			"refused every paragraph would pass")
	}
	if plan.Targets != plain {
		t.Errorf("%d targets, %d of them spliceable", plan.Targets, plain)
	}
}

// The vocabulary is closed and is asserted by name. Checking only that observed
// values are members of Dispositions() proves nothing: an implementation that
// declared one disposition and used it everywhere would pass.
func TestTheDispositionVocabularyIsExactlyThis(t *testing.T) {
	want := []workflow.Disposition{
		workflow.DispositionTarget,
		workflow.DispositionInRange,
		workflow.DispositionUnmeasurable,
		workflow.DispositionContainsExcisions,
	}
	got := workflow.Dispositions()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dispositions = %v, want %v", got, want)
	}
	seen := map[workflow.Disposition]bool{}
	for _, disposition := range got {
		if seen[disposition] {
			t.Errorf("%q is declared twice", disposition)
		}
		seen[disposition] = true
	}

	root := bandedStore(t, "not-you")
	for _, body := range []string{twoParagraphs, excisionDraft, footnoteDraft, interleavedDraft} {
		plan := planned(t, planRequest(root, writeDraft(t, root, body)))
		for _, segment := range plan.Segments {
			if !seen[segment.Disposition] {
				t.Errorf("segment %d carries undeclared disposition %q", segment.Index, segment.Disposition)
			}
		}
	}
}

// The plan's states are closed and asserted by name, like the dispositions.
// DESIGN's rewrite RESULT states — all-improved, some-not-improved — are B2's
// and are deliberately not these: a plan has not run anything yet, so it can
// only say whether there is anything to run.
func TestThePlanStateVocabularyIsExactlyThis(t *testing.T) {
	want := []workflow.PlanState{workflow.StateNothingToChange, workflow.StateTargetsPlanned}
	if got := workflow.PlanStates(); !reflect.DeepEqual(got, want) {
		t.Errorf("plan states = %v, want %v", got, want)
	}
	// A named type rather than a string, so B2's result states — all-improved and
	// some-not-improved, which are outcomes of a loop that has run — cannot be
	// assigned into this field by a later slice reaching for the nearest name.
	if kind := reflect.TypeOf(workflow.StateNothingToChange).Name(); kind != "PlanState" {
		t.Errorf("the plan states are of type %q; a bare string lets a result state in", kind)
	}
}

// ---------------------------------------------------------------------------
// Nothing to change
// ---------------------------------------------------------------------------

// DESIGN: rewrite exits 0 both when nothing needed changing and when everything
// that did was improved, and which of those happened is a named state rather
// than an exit code. The first of those must cost nothing: no exemplars
// selected, nothing rehydrated, and — once B2 exists — no provider and no
// socket.
func TestADraftWithNothingToChangeSelectsNoExemplars(t *testing.T) {
	root := bandedStore(t, "in-range")
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, planRequest(root, draft))

	if plan.Targets != 0 {
		t.Fatalf("%d targets in a fixture built to have none", plan.Targets)
	}
	if plan.State != workflow.StateNothingToChange {
		t.Errorf("state = %q, want %q", plan.State, workflow.StateNothingToChange)
	}
	if plan.ExemplarSelectionID != "" {
		t.Errorf("exemplars %s were selected for a draft with nothing to rewrite", plan.ExemplarSelectionID)
	}
	if len(plan.ExemplarNodes) != 0 {
		t.Errorf("exemplar nodes %v were selected for a draft with nothing to rewrite", plan.ExemplarNodes)
	}

	if plan.ExemplarCertificateID != "" {
		t.Errorf("certificate %s was recorded for a draft with nothing to rewrite", plan.ExemplarCertificateID)
	}
	// The draft IS indexed here, unlike the insufficient-evidence case: every
	// segment the plan reports names a node, and a plan with nothing to change
	// still reports the segments it measured. Asserted the same way as the
	// target-bearing path, because a non-empty DraftSnapshotID does not prove the
	// no-op branch stored anything or that its node IDs resolve into it.
	assertSegmentsResolveIntoTheDraftSnapshot(t, root, draft, plan)

	var selections int
	if err := openRawStore(t, defaultStorePath(root)).QueryRow(
		"SELECT count(*) FROM exemplar_selection").Scan(&selections); err != nil {
		t.Fatalf("count selections: %v", err)
	}
	if selections != 0 {
		t.Errorf("%d exemplar selections were persisted for a draft with nothing to rewrite", selections)
	}
}

// A refusal is not an answer about the draft, so it must not carry one. Without
// this a refused plan reporting nothing-to-change passes — and "nothing to
// change" is precisely the wrong thing to tell someone whose profile cannot band
// anything at all.
func assertRefusedPlanClaimsNothing(t *testing.T, plan workflow.RewritePlan) {
	t.Helper()
	if plan.Refusal == "" {
		t.Fatal("this assertion is for refused plans and this one was not refused")
	}
	if plan.State != "" {
		t.Errorf("a plan refused with %q also claims state %q", plan.Refusal, plan.State)
	}
	if plan.Targets != 0 {
		t.Errorf("a refused plan names %d targets", plan.Targets)
	}
	if len(plan.Segments) != 0 {
		t.Errorf("a refused plan reports %d segments", len(plan.Segments))
	}
	if plan.ExemplarSelectionID != "" || plan.ExemplarCertificateID != "" || len(plan.ExemplarNodes) != 0 {
		t.Errorf("a refused plan selected exemplars: %s / %s / %v",
			plan.ExemplarSelectionID, plan.ExemplarCertificateID, plan.ExemplarNodes)
	}
}

// ---------------------------------------------------------------------------
// Exemplars, chosen before anything is read
// ---------------------------------------------------------------------------

// DESIGN requires the exemplar set to be fixed BEFORE rehydration is attempted,
// with no silent substitution and no silent reduction — otherwise which
// exemplars the author gets depends on which of their files happen to be
// readable today, and the same profile produces a different prompt tomorrow.
//
// It is achievable because exemplar.Select never reads Candidate.Text: density,
// strata and medoids all come from the stored vectors. This test is what says
// so. The corpus is deleted after indexing, so an implementation that rehydrates
// in order to select cannot pass, and no assertion about call counts is needed.
func TestExemplarsAreSelectedFromTheStoreWithTheCorpusGone(t *testing.T) {
	root := bandedStore(t, "not-you")
	draft := writeDraft(t, root, twoParagraphs)
	// Deleted BEFORE the first plan, not between two of them. Planning twice and
	// comparing would be satisfied by an implementation that read the files on
	// the first run and reloaded its own persisted answer on the second.
	removeCorpusDocuments(t, root, draft)

	plan := planned(t, planRequest(root, draft))

	if plan.ExemplarSelectionID == "" {
		t.Fatal("no exemplars were selected with the corpus off disk")
	}
	if len(plan.ExemplarNodes) != exemplar.DefaultConfig().N {
		t.Fatalf("selected %d exemplars, want %d", len(plan.ExemplarNodes), exemplar.DefaultConfig().N)
	}

	// And they are the ones exemplar.Select itself picks from the same persisted
	// metadata. Without this the test says only that SOMETHING was selected
	// without reading a file — a fixed three nodes would pass.
	want := selectFromStore(t, root)
	if !reflect.DeepEqual(plan.ExemplarNodes, want.nodes) {
		t.Errorf("selected %v, exemplar.Select over the same metadata selects %v",
			plan.ExemplarNodes, want.nodes)
	}
	if plan.ExemplarSelectionID != want.selectionID {
		t.Errorf("selection ID %s, want %s", plan.ExemplarSelectionID, want.selectionID)
	}
}

// The selection is persisted, in order, as the nodes it names — so B2 can
// rehydrate exactly those and refuse when it cannot, rather than choosing again
// from whatever survived.
func TestThePersistedSelectionNamesTheProfilesOwnNodesInOrder(t *testing.T) {
	root := bandedStore(t, "not-you")
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, planRequest(root, draft))
	if plan.ExemplarSelectionID == "" {
		t.Fatal("no exemplars were selected for a draft with targets")
	}
	if plan.State != workflow.StateTargetsPlanned {
		t.Errorf("state = %q with %d targets, want %q", plan.State, plan.Targets, workflow.StateTargetsPlanned)
	}

	opened := openStore(t, defaultStorePath(root))
	stored, err := opened.LoadExemplarSelection(ctx(), plan.ExemplarSelectionID)
	if err != nil {
		t.Fatalf("LoadExemplarSelection: %v", err)
	}
	if !reflect.DeepEqual(stored.Members, plan.ExemplarNodes) {
		t.Errorf("stored members %v, plan reports %v", stored.Members, plan.ExemplarNodes)
	}
	if stored.N != len(stored.Members) {
		t.Errorf("N = %d over %d members", stored.N, len(stored.Members))
	}
	if stored.ProfileID != plan.ProfileID {
		t.Errorf("the selection belongs to profile %s, the plan to %s", stored.ProfileID, plan.ProfileID)
	}
	if stored.CertificateID == "" {
		t.Fatal("the selection carries no certificate")
	}
	// The plan reports the certificate it actually persisted. Without this an
	// empty or invented certificate ID in the plan passes, and the certificate is
	// the only record of WHY these three exemplars and not three others.
	if plan.ExemplarCertificateID != stored.CertificateID {
		t.Errorf("the plan names certificate %s, the store holds %s",
			plan.ExemplarCertificateID, stored.CertificateID)
	}

	// Members of the PROFILE's snapshot, not the draft's: an implementation
	// that selected from whatever it had just indexed would offer the user
	// their own draft as an example of their own voice.
	bundle := persistedBundle(t, defaultStorePath(root), "essays")
	for _, member := range stored.Members {
		span, err := opened.Span(ctx(), member)
		if err != nil {
			t.Fatalf("member %s: %v", member, err)
		}
		if span.SnapshotID != bundle.Profile.SnapshotID {
			t.Errorf("member %s belongs to snapshot %s, not the profile's %s",
				member, span.SnapshotID, bundle.Profile.SnapshotID)
		}
	}
}

// ---------------------------------------------------------------------------
// Finding the store, rather than being told where it is
// ---------------------------------------------------------------------------

// Every other test here passes --store, and that is exactly how eval shipped
// unable to discover its own store in A2b: the fake and the fixtures both knew
// the path, so nothing exercised the search.
//
// The corpus root comes with it. The store lives at <root>/.hapax/hapax.sqlite3,
// so a discovered store names its own corpus, and B2 needs that root to
// rehydrate the exemplars from.
func TestPlanningFindsTheStoreAndItsCorpusFromAWorkingDirectory(t *testing.T) {
	root := bandedStore(t, "not-you")
	draft := writeDraft(t, root, twoParagraphs)
	told := planned(t, planRequest(root, draft))

	for _, from := range []struct {
		name, dir string
	}{
		{"the corpus root itself", root},
		{"a subdirectory of it", mustMkdir(t, filepath.Join(root, "notes", "deeper"))},
	} {
		t.Run(from.name, func(t *testing.T) {
			found, err := workflow.Default().Plan(ctx(), workflow.RewriteRequest{
				StartDir: from.dir, Register: "essays", Path: draft,
			})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if found.Refusal != "" {
				t.Fatalf("refused with %q rather than finding the store above %s", found.Refusal, from.dir)
			}
			if found.CorpusRoot != root {
				t.Errorf("derived corpus root %q, want %q", found.CorpusRoot, root)
			}
			// The WHOLE plan, not the store path and a count of segments. A
			// discovered plan that lost its state, its profile binding, its
			// exemplars or its certificate would satisfy anything narrower, and
			// discovery is the path a person actually takes.
			if !reflect.DeepEqual(found, told) {
				t.Errorf("the discovered plan is\n%+v\nand being told the path gives\n%+v", found, told)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Refusals, which happen before any of the above
// ---------------------------------------------------------------------------

// A profile with no shipped release cannot band anything, so there is no such
// thing as a paragraph that needs rewriting. Refusing here rather than after
// indexing the draft is the difference between a refusal and a side effect.
func TestAnUncalibratedProfileIsRefusedBeforeTheDraftIsTouched(t *testing.T) {
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, planRequest(root, draft))

	if plan.Refusal != workflow.RefusalUncalibrated {
		t.Fatalf("refusal = %q, want %q", plan.Refusal, workflow.RefusalUncalibrated)
	}
	assertRefusedPlanClaimsNothing(t, plan)
	if plan.DraftSnapshotID != "" {
		t.Errorf("a refused plan indexed the draft as %s", plan.DraftSnapshotID)
	}
	var snapshots int
	if err := openRawStore(t, defaultStorePath(root)).QueryRow(
		"SELECT count(*) FROM document WHERE split='draft'").Scan(&snapshots); err != nil {
		t.Fatalf("count draft documents: %v", err)
	}
	if snapshots != 0 {
		t.Errorf("%d draft documents were written by a refused plan", snapshots)
	}
}

// insufficient-evidence is the one refusal in this vocabulary that is NOT a fact
// about the store. Everything has resolved — the profile, the reference and the
// release are all known — and it is the draft that cannot be measured. So the
// plan keeps what it resolved and says what is missing, rather than presenting
// itself as an empty store; a reader told "no profile" would go and index one,
// which would not help them.
//
// It also writes nothing. There are no segments, so there are no nodes to name
// and nothing an audit record could ever point at.
func TestADraftWithNothingMeasurableRefusesAndKeepsWhatItResolved(t *testing.T) {
	root, releaseID := calibratedStore(t)
	// Digits and punctuation: admitted as text, but carrying no lexical tokens,
	// so no paragraph clears the floor and there is nothing to measure.
	draft := writeDraft(t, root, "123 456.\n\n789 1011.\n\n")

	before := census(t, root)
	plan := planned(t, planRequest(root, draft))

	if plan.Refusal != workflow.RefusalInsufficientEvidence {
		t.Fatalf("refusal = %q, want %q", plan.Refusal, workflow.RefusalInsufficientEvidence)
	}
	if len(plan.Segments) != 0 || plan.Targets != 0 {
		t.Errorf("%d segments and %d targets in a draft with nothing measurable",
			len(plan.Segments), plan.Targets)
	}
	if plan.State != "" {
		t.Errorf("a refused plan claims state %q", plan.State)
	}

	// The provenance a store refusal cannot carry, and this one must.
	bundle := persistedBundle(t, defaultStorePath(root), "essays")
	if plan.ProfileID != bundle.Profile.ID {
		t.Errorf("profile = %q, want the resolved %q", plan.ProfileID, bundle.Profile.ID)
	}
	if plan.ReferenceID != bundle.Reference.ID {
		t.Errorf("reference = %q, want the resolved %q", plan.ReferenceID, bundle.Reference.ID)
	}
	if plan.ReleaseID != releaseID {
		t.Errorf("release = %q, want the resolved %q", plan.ReleaseID, releaseID)
	}
	if plan.Path != draft {
		t.Errorf("path = %q, want %q", plan.Path, draft)
	}

	// Nothing written: no draft snapshot, because no node of it could ever be
	// named, and no exemplars, because nothing is going to be rewritten.
	if plan.DraftSnapshotID != "" {
		t.Errorf("indexed the draft as %s with no measurable paragraph in it", plan.DraftSnapshotID)
	}
	if plan.ExemplarSelectionID != "" || len(plan.ExemplarNodes) != 0 {
		t.Errorf("selected exemplars %s / %v for a draft it will not rewrite",
			plan.ExemplarSelectionID, plan.ExemplarNodes)
	}
	// Every table, not the three a test author thought to name: an empty or
	// wrongly classified snapshot written alongside an empty DraftSnapshotID
	// satisfies any shorter list.
	assertUnchanged(t, before, census(t, root))
}

// The refusals that ARE facts about the store, where the plan has resolved
// nothing and so can claim nothing.
func TestPlanRefusesEverythingScoreRefuses(t *testing.T) {
	for _, row := range []struct {
		name   string
		root   func(*testing.T) string
		reason string
	}{
		{"no store", emptyStore, workflow.RefusalNoProfile},
		{"no reference", profileOnlyStore, workflow.RefusalNoReference},
		{"two references", twoReferenceStore, workflow.RefusalAmbiguousReference},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := row.root(t)
			draft := writeDraft(t, root, twoParagraphs)
			plan := planned(t, planRequest(root, draft))
			if plan.Refusal != row.reason {
				t.Errorf("refusal = %q, want %q", plan.Refusal, row.reason)
			}
			assertRefusedPlanClaimsNothing(t, plan)
		})
	}
}
