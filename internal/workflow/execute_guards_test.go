package workflow_test

// Everything Execute refuses, errors on, or declines to spend, and the ordering
// that decides which of those a caller gets.
//
// The division these tests hold to: a refusal is a fact about the world that
// makes the run impossible — the draft moved, an exemplar will not rehydrate,
// the mode forbids the provider — and it carries a reason from a closed set. An
// error is a component failing or a precondition being violated. A gate
// rejection is neither: it is the tool working.

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// The invariant every refusal below is also checked against
// ---------------------------------------------------------------------------

// requireUnpublishable is the one rule B2b-2 will rely on: bytes may be written
// only when there is no error, no refusal, and the bytes exist. It is asserted
// at every refusal rather than once, because a single member set on one path is
// exactly how a partial document escapes.
func requireUnpublishable(t *testing.T, result workflow.ExecuteResult, want string) {
	t.Helper()
	if result.Refusal != want {
		t.Errorf("Refusal = %q, want %q", result.Refusal, want)
	}
	if result.Bytes != nil {
		t.Errorf("a %q refusal carries %d bytes; a refusal must never be publishable", want, len(result.Bytes))
	}
	declared := false
	for _, refusal := range workflow.Refusals() {
		if refusal == result.Refusal {
			declared = true
		}
	}
	if !declared {
		t.Errorf("%q is not in the declared refusal vocabulary %v", result.Refusal, workflow.Refusals())
	}
}

// ---------------------------------------------------------------------------
// Freshness
// ---------------------------------------------------------------------------

// A draft that changed between planning and execution is refused before a single
// provider call. Without this the persisted audit describes one draft while the
// bytes handed back describe another.
func TestADraftChangedBeforeTheLoopRefusesAndSpendsNothing(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))
	seeded := seedAttempt(t, root, plan)

	if err := os.WriteFile(draft, []byte("An entirely different draft, long enough to be admitted and measured on "+
		"its own terms rather than skipped by the floor.\n\n"), 0o644); err != nil {
		t.Fatalf("rewrite draft: %v", err)
	}

	local := &arm{provider: newProvider(t, map[string][]string{paragraphOne: {improvesOne}})}
	runner, _ := executingRunner(local, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	requireUnpublishable(t, result, workflow.RefusalStaleDraft)
	if calls := local.provider.(*scriptedProvider).calls; len(calls) != 0 {
		t.Errorf("%d provider calls were made against a draft that had moved", len(calls))
	}
	requireNothingWasSpent(t, root, seeded)
	// The provider was BUILT, because resolution is a declarative step that
	// precedes reading anything — building one costs nothing and sends nothing.
	// Pinned so the order of the two steps is a decision rather than an accident.
	if len(local.choices) != 1 {
		t.Errorf("the local arm was built %d times, want once before the draft was read", len(local.choices))
	}
}

// And a draft that changes DURING the run is refused too, because a long
// provider run is exactly when a file changes underneath. The attempts already
// made are real and already persisted, so the outcomes are still returned — what
// is withheld is the bytes.
func TestADraftChangedDuringTheLoopRefusesAndKeepsTheAudit(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {improvesOne},
		paragraphTwo: {improvesTwo},
	})
	provider.before = func(t *testing.T, call int) {
		if call != 1 {
			return
		}
		if err := os.WriteFile(draft, []byte(executableDraft()+
			"A third paragraph appended while the provider was thinking, long enough "+
			"to be admitted and to change what the file measures.\n\n"), 0o644); err != nil {
			t.Fatalf("append to draft: %v", err)
		}
	}
	runner, invocation := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	requireUnpublishable(t, result, workflow.RefusalStaleDraft)
	if len(result.Outcomes) == 0 {
		t.Fatal("no outcomes were returned; the attempts happened and are already in the store, " +
			"so suppressing them would make the result disagree with it")
	}
	stored := countAttempts(t, root)
	if stored == 0 {
		t.Fatal("no attempts reached the store, so this test did not reach the loop")
	}
	reported := 0
	for _, outcome := range result.Outcomes {
		reported += len(outcome.Rejections)
	}
	if reported != stored {
		t.Errorf("the result reports %d attempts and the store holds %d", reported, stored)
	}
	if _, err := openStore(t, defaultStorePath(root)).
		LoadRewriteAttempt(ctx(), invocation, outcomeAt(t, result, 0).NodeID, 0); err != nil {
		t.Errorf("the first attempt is not readable under the invocation the result names: %v", err)
	}
}

// Freshness is the content, not the timestamp. Rewriting the file with the same
// bytes moves its modification time and changes nothing that matters, and an
// implementation guarding on mtime or size would refuse a run it should not.
func TestFreshnessIsTheContentAndNotTheTimestamp(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	if err := os.WriteFile(draft, []byte(executableDraft()), 0o644); err != nil {
		t.Fatalf("rewrite draft with identical bytes: %v", err)
	}

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != "" {
		t.Errorf("Refusal = %q after a rewrite that changed no bytes", result.Refusal)
	}
	if result.Bytes == nil {
		t.Error("no bytes were returned for a draft that is unchanged")
	}
}

// Freshness is not file identity either. Publishing by staging and renaming is
// how #70 writes a database and how B2b-2 will write a destination, so a draft
// arriving through a rename with identical bytes is ordinary rather than
// suspicious — and an implementation guarding on inode would refuse it.
func TestFreshnessIsNotFileIdentity(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	staged := draft + ".staged"
	if err := os.WriteFile(staged, []byte(executableDraft()), 0o644); err != nil {
		t.Fatalf("stage: %v", err)
	}
	original, err := os.Stat(draft)
	if err != nil {
		t.Fatalf("stat draft: %v", err)
	}
	if err := os.Rename(staged, draft); err != nil {
		t.Fatalf("rename over the draft: %v", err)
	}
	replaced, err := os.Stat(draft)
	if err != nil {
		t.Fatalf("stat draft: %v", err)
	}
	if os.SameFile(original, replaced) {
		t.Fatal("the rename did not replace the file, so this test asserts nothing about identity")
	}

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))
	if result.Refusal != "" {
		t.Errorf("Refusal = %q after a rename that changed no bytes", result.Refusal)
	}
}

// A change that leaves the length alone is still a change. An implementation
// comparing sizes would miss it, and the result would be a document assembled
// against text nobody wrote.
func TestAChangeOfTheSameLengthIsStillStale(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))

	swapped := strings.Replace(executableDraft(), "A paragraph of ordinary prose", "A paragraph of ordinary PROSE", 1)
	if len(swapped) != len(executableDraft()) {
		t.Fatalf("the substitution changed the length, so this test would pass for the wrong reason")
	}
	if err := os.WriteFile(draft, []byte(swapped), 0o644); err != nil {
		t.Fatalf("rewrite draft: %v", err)
	}

	runner, _ := executingRunner(&arm{provider: newProvider(t, nil)}, nil)
	requireUnpublishable(t, executed(t, runner, executeRequest(plan, localChoice())), workflow.RefusalStaleDraft)
}

// A byte-order mark is the one edit the content hash cannot see: admission
// strips it before hashing, so a BOM appearing or disappearing mid-run leaves
// the draft "fresh" while assembly restores the state of the FIRST read — which
// would silently rewrite the file's first three bytes.
func TestABOMChangingMidRunIsStale(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, before, after string }{
		{"a BOM appears", executableDraft(), "\xef\xbb\xbf" + executableDraft()},
		{"a BOM disappears", "\xef\xbb\xbf" + executableDraft(), executableDraft()},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := installRelease(t, 0.05, 5.0)
			draft := writeDraftBytes(t, root, c.before)
			plan := planned(t, planRequest(root, draft))
			if plan.Targets == 0 {
				t.Fatalf("the fixture planned no targets, so the loop is never entered")
			}

			provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
			provider.before = func(t *testing.T, call int) {
				if call == 1 {
					if err := os.WriteFile(draft, []byte(c.after), 0o644); err != nil {
						t.Fatalf("swap the BOM: %v", err)
					}
				}
			}
			runner, _ := executingRunner(&arm{provider: provider}, nil)
			result := executed(t, runner, executeRequest(plan, localChoice()))

			// The precondition, asserted rather than described. Without it an
			// implementation hashing the RAW bytes would refuse too, and this
			// test would pass while the separate guard did not exist.
			if admittedHash(t, c.before) != admittedHash(t, c.after) {
				t.Fatalf("the admitted content hash differs across the BOM change, so the hash " +
					"catches it on its own and this test says nothing about a separate guard")
			}
			requireUnpublishable(t, result, workflow.RefusalStaleDraft)
		})
	}
}

// A BOM that is there throughout survives into the assembled bytes, and the
// spans are into the bytes after it. Byte ownership is the risk this slice
// carries and a BOM is where an off-by-three lives.
func TestABOMPresentThroughoutSurvivesAssembly(t *testing.T) {
	t.Parallel()
	root := installRelease(t, 0.05, 5.0)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, "\xef\xbb\xbf"+executableDraft())
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {improvesOne},
		paragraphTwo: {improvesTwo},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	want := spliced(t, draft, substitution{paragraphOne, improvesOne}, substitution{paragraphTwo, improvesTwo})
	if string(result.Bytes) != string(want) {
		t.Errorf("assembled bytes are\n%q\nwant\n%q", result.Bytes, want)
	}
	if len(result.Bytes) < 3 || string(result.Bytes[:3]) != "\xef\xbb\xbf" {
		t.Error("the assembled document lost its byte-order mark")
	}
}

// ---------------------------------------------------------------------------
// The exemplars
// ---------------------------------------------------------------------------

// The exemplars are in hand before the first provider call, and nothing reads
// them again afterwards. Proved by DELETING the corpus during the run rather
// than by counting reads: an implementation that rehydrates per segment cannot
// survive it, whereas a call count is satisfied by an implementation that reads
// and discards.
//
// What this does NOT prove is "exactly once" — two rehydrations before the first
// call would pass it. That is deliberate rather than an oversight. Proving a
// literal count needs a seam injected purely to be counted, and the property
// that matters is behavioural: the exemplars are fixed before anything is spent,
// and they do not change under the run. Together with the refusal test below —
// a corpus already gone refuses before a single call — that is the whole of what
// a caller can observe.
func TestTheExemplarsAreInHandBeforeTheFirstCallAndNotReadAgain(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {improvesOne},
		paragraphTwo: {improvesTwo},
	})
	provider.before = func(t *testing.T, call int) {
		if call == 1 {
			removeCorpusDocuments(t, root, draft)
		}
	}
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != "" {
		t.Fatalf("Refusal = %q; the exemplars were already in hand when the corpus went away", result.Refusal)
	}
	if len(provider.calls) < 2 {
		t.Fatalf("%d provider calls; the corpus went away after the first and the run must continue", len(provider.calls))
	}
	first := exemplarSection(t, promptAt(t, provider, 0))
	for i, call := range provider.calls[1:] {
		if got := exemplarSection(t, call.Prompt); got != first {
			t.Errorf("call %d carried different exemplars:\n%q\nwant\n%q", i+1, got, first)
		}
	}
}

// The exemplars a provider sees are the PERSISTED selection, in the persisted
// order. Order is load-bearing: the selection is an ordered artifact and a
// prompt that reorders it is a different prompt.
func TestTheExemplarsAreThePersistedSelectionInItsOrder(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))
	// The exemplars' documents gain a byte-order mark, which the content hash
	// cannot see but a raw offset can. So the prompt matching the rehydrated
	// text is a statement about WHERE the text came from, not only about its
	// order: anything reading the files itself lands three bytes late.
	giveExemplarsAByteOrderMark(t, root, plan)

	selection, err := openStore(t, defaultStorePath(root)).LoadExemplarSelection(ctx(), plan.ExemplarSelectionID)
	if err != nil {
		t.Fatalf("LoadExemplarSelection: %v", err)
	}
	if len(selection.Members) < 2 {
		t.Fatalf("the selection has %d members; order cannot be observed", len(selection.Members))
	}
	rehydrated, err := openStore(t, defaultStorePath(root)).Rehydrate(ctx(), root, selection.Members)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	provider := newProvider(t, nil)
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	executed(t, runner, executeRequest(plan, localChoice()))
	if len(provider.calls) == 0 {
		t.Fatal("the provider was never called")
	}

	section := exemplarSection(t, promptAt(t, provider, 0))
	at := 0
	for i, member := range rehydrated {
		if member.Outcome != store.OutcomeOK {
			t.Fatalf("exemplar %d did not rehydrate (%s), so the fixture cannot check the order", i, member.Outcome)
		}
		found := strings.Index(section[at:], member.Text)
		if found < 0 {
			t.Fatalf("exemplar %d does not appear in the prompt after exemplar %d; the persisted order was not kept", i, i-1)
		}
		at += found + len(member.Text)
	}
}

// A selected exemplar that will not rehydrate refuses before anything is spent.
// The refusal is stale-exemplars and not stale-draft: the draft is fine, the
// corpus moved.
func TestAnExemplarThatWillNotRehydrateRefusesBeforeAnythingIsSpent(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))
	permitted := exemplarPaths(t, root, plan.ExemplarSelectionID)
	before := storeState(t, root, plan.DraftSnapshotID, profileSnapshotID(t, root))
	seeded := seedAttempt(t, root, plan)
	removeCorpusDocuments(t, root, draft)

	local := &arm{provider: newProvider(t, map[string][]string{paragraphOne: {improvesOne}})}
	runner, _ := executingRunner(local, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	requireUnpublishable(t, result, workflow.RefusalStaleExemplars)
	// The refusal is a claim about the corpus, and the store must carry the
	// observation that justifies it — for those documents and no others.
	requireOnlyAuditRecordsWereWritten(t, root, before, permitted, wasUnreadable)
	if calls := local.provider.(*scriptedProvider).calls; len(calls) != 0 {
		t.Errorf("%d provider calls were made after the exemplars had gone", len(calls))
	}
	// And the earlier run's record is still there. "No attempts" would be
	// satisfied by an implementation that refused correctly and tidied up the
	// table on its way out.
	requireNothingWasSpent(t, root, seeded)
}

// ---------------------------------------------------------------------------
// Precedence, so that two refusals cannot both be true and the answer be a coin
// ---------------------------------------------------------------------------

// Several of these conditions can hold at once, and a caller reading a reason
// needs the same reason every time. The order is the design's: validate the
// plan, resolve the provider, read the draft, then the exemplars.
func TestTheOrderOfTheChecksIsPinned(t *testing.T) {
	t.Parallel()
	t.Run("an invalid plan beats an unknown provider", func(t *testing.T) {
		root, draft := targetStore(t)
		plan := planned(t, planRequest(root, draft))
		plan.Targets++

		local, cloud := &arm{provider: newProvider(t, nil)}, &arm{provider: newProvider(t, nil)}
		runner, _ := executingRunner(local, cloud)
		_, err := runner.Execute(ctx(), executeRequest(plan, workflow.ProviderChoice{Provider: "gpt"}))
		if err == nil {
			t.Fatal("a tampered plan with an unknown provider was executed")
		}
		if errors.Is(err, workflow.ErrUnknownProvider) {
			t.Error("the provider was resolved before the plan was validated")
		}
		if len(local.choices)+len(cloud.choices) != 0 {
			t.Error("an arm was built for a plan that does not validate")
		}
	})

	t.Run("local-only beats a draft that is not there", func(t *testing.T) {
		root, draft := targetStore(t)
		plan := planned(t, planRequest(root, draft))
		if err := os.Remove(draft); err != nil {
			t.Fatalf("remove draft: %v", err)
		}
		runner, _ := executingRunner(nil, &arm{provider: newProvider(t, nil)})
		request := executeRequest(plan, cloudChoice())
		request.Mode.LocalOnly = true
		requireUnpublishable(t, executed(t, runner, request), workflow.RefusalLocalOnlyForbidsProvider)
	})

	t.Run("an arm that will not build beats a draft that is not there", func(t *testing.T) {
		// The stated order is resolve the provider, then read the draft. The
		// stale-draft test shows a SUCCESSFUL arm is built first, which does not
		// establish what a caller observes when both would fail.
		root, draft := targetStore(t)
		plan := planned(t, planRequest(root, draft))
		if err := os.Remove(draft); err != nil {
			t.Fatalf("remove draft: %v", err)
		}
		runner, _ := executingRunner(&arm{err: errors.New("no endpoint")}, nil)
		_, err := runner.Execute(ctx(), executeRequest(plan, localChoice()))
		if err == nil {
			t.Fatal("a run with neither a provider nor a draft succeeded")
		}
		if errors.Is(err, os.ErrNotExist) {
			t.Error("the draft was read before the provider was resolved")
		}
		if !strings.Contains(err.Error(), "no endpoint") {
			t.Errorf("error = %v, want the arm's own failure", err)
		}
	})

	t.Run("a stale draft beats missing exemplars", func(t *testing.T) {
		root, draft := targetStore(t)
		plan := planned(t, planRequest(root, draft))
		removeCorpusDocuments(t, root, draft)
		if err := os.WriteFile(draft, []byte("Something else entirely, at enough length to be admitted "+
			"and measured on its own terms rather than skipped.\n\n"), 0o644); err != nil {
			t.Fatalf("rewrite draft: %v", err)
		}
		runner, _ := executingRunner(&arm{provider: newProvider(t, nil)}, nil)
		requireUnpublishable(t, executed(t, runner, executeRequest(plan, localChoice())), workflow.RefusalStaleDraft)
	})

	t.Run("nothing to change beats missing exemplars", func(t *testing.T) {
		// A settled plan never selected exemplars, so a corpus that has gone is
		// not its problem. An implementation that rehydrated unconditionally
		// would refuse a run that has nothing to rehydrate for.
		root, draft := settledStore(t)
		plan := planned(t, planRequest(root, draft))
		removeCorpusDocuments(t, root, draft)

		runner, _ := executingRunner(nil, nil)
		result := executed(t, runner, executeRequest(plan, localChoice()))
		if result.Refusal != "" {
			t.Errorf("Refusal = %q; a plan with nothing to change needs no exemplars", result.Refusal)
		}
		if string(result.Bytes) != executableDraft() {
			t.Error("the draft was not returned byte for byte")
		}
	})
}

// ---------------------------------------------------------------------------
// Provider resolution, which is target work
// ---------------------------------------------------------------------------

// local-only refuses the cloud arm, and does so before the draft is read. The
// draft is deleted, so an implementation that opened the file first would report
// an operational failure instead of the refusal the user is owed.
func TestLocalOnlyRefusesTheCloudArmBeforeReadingTheDraft(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))
	if err := os.Remove(draft); err != nil {
		t.Fatalf("remove draft: %v", err)
	}

	cloud := &arm{provider: newProvider(t, nil)}
	runner, _ := executingRunner(nil, cloud)
	request := executeRequest(plan, cloudChoice())
	request.Mode.LocalOnly = true
	result := executed(t, runner, request)

	requireUnpublishable(t, result, workflow.RefusalLocalOnlyForbidsProvider)
	if len(cloud.choices) != 0 {
		t.Errorf("the cloud arm was called %d times under local-only", len(cloud.choices))
	}
}

// An unknown provider is an invalid invocation rather than a refusal, and it is
// an error so cli can classify it as one. Neither arm runs.
func TestAnUnknownProviderIsAnErrorAndNeitherArmRuns(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))

	local, cloud := &arm{provider: newProvider(t, nil)}, &arm{provider: newProvider(t, nil)}
	runner, _ := executingRunner(local, cloud)
	_, err := runner.Execute(ctx(), executeRequest(plan, workflow.ProviderChoice{Provider: "gpt", Model: "x"}))
	if !errors.Is(err, workflow.ErrUnknownProvider) {
		t.Errorf("error = %v, want ErrUnknownProvider", err)
	}
	if len(local.choices)+len(cloud.choices) != 0 {
		t.Error("an arm was called for a provider neither of them owns")
	}
}

// A failing arm is not a reason to try the other one. Falling back would send a
// draft to a provider the user did not choose, which under local-only would send
// it off the machine.
func TestAFailingArmNeverFallsBackToTheOther(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))

	local := &arm{err: errors.New("no endpoint")}
	cloud := &arm{provider: newProvider(t, nil)}
	runner, _ := executingRunner(local, cloud)
	if _, err := runner.Execute(ctx(), executeRequest(plan, localChoice())); err == nil {
		t.Fatal("a failing provider arm was swallowed")
	}
	if len(cloud.choices) != 0 {
		t.Errorf("the cloud arm ran %d times after the local arm failed", len(cloud.choices))
	}
}

// A provider that fails mid-run is an operational failure, not a rejection. A
// loop that treated it as "no improvement" would report a paragraph as judged
// when nothing judged it.
func TestAProviderFailureIsAnErrorAndReturnsNoBytes(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, nil)
	provider.err = errors.New("provider unavailable")
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result, err := runner.Execute(ctx(), executeRequest(plan, localChoice()))
	if err == nil {
		t.Fatal("a provider failure was swallowed")
	}
	if result.Bytes != nil {
		t.Errorf("%d bytes were returned beside an error", len(result.Bytes))
	}
}

// ---------------------------------------------------------------------------
// Nothing to change
// ---------------------------------------------------------------------------

// A plan with no targets constructs no provider at all, and therefore succeeds
// under local-only even when a cloud provider is named. Nothing is sent, so
// refusing would refuse something that never happens. The bytes are the draft
// exactly, which is where B2b-2's exact copy comes from — through the same
// freshness check as a real rewrite rather than a bare copy that skips it.
func TestANoTargetPlanConstructsNoProviderAndCopiesExactly(t *testing.T) {
	t.Parallel()
	root, draft := settledStore(t)
	plan := planned(t, planRequest(root, draft))
	if plan.State != workflow.StateNothingToChange {
		t.Fatalf("the fixture planned %q and this test needs nothing-to-change", plan.State)
	}

	before := storeState(t, root, plan.DraftSnapshotID, profileSnapshotID(t, root))
	local, cloud := &arm{provider: newProvider(t, nil)}, &arm{provider: newProvider(t, nil)}
	runner, _ := executingRunner(local, cloud)
	request := executeRequest(plan, cloudChoice())
	request.Mode.LocalOnly = true
	result := executed(t, runner, request)

	if result.Refusal != "" {
		t.Errorf("Refusal = %q; no provider was needed and nothing was sent", result.Refusal)
	}
	// Nothing was rehydrated, so no read observation may have moved anywhere —
	// there is no permitted set at all on this path.
	requireOnlyAuditRecordsWereWritten(t, root, before, nil, notChecked)
	if len(local.choices)+len(cloud.choices) != 0 {
		t.Error("a provider was constructed for a plan with nothing to change")
	}
	if string(result.Bytes) != executableDraft() {
		t.Errorf("bytes are\n%q\nwant the draft byte for byte\n%q", result.Bytes, executableDraft())
	}
	if result.State != workflow.RewriteNoTargets || result.Targets != 0 || len(result.Outcomes) != 0 {
		t.Errorf("State = %q, Targets = %d, %d outcomes", result.State, result.Targets, len(result.Outcomes))
	}
}

// And it still takes the freshness check, which is the reason it goes through
// Execute rather than being copied by the caller.
func TestANoTargetPlanIsStillCheckedForFreshness(t *testing.T) {
	t.Parallel()
	root, draft := settledStore(t)
	plan := planned(t, planRequest(root, draft))
	if err := os.WriteFile(draft, []byte("Something else entirely, long enough to be admitted and measured "+
		"on its own terms rather than skipped.\n\n"), 0o644); err != nil {
		t.Fatalf("rewrite draft: %v", err)
	}

	runner, _ := executingRunner(nil, nil)
	requireUnpublishable(t, executed(t, runner, executeRequest(plan, localChoice())), workflow.RefusalStaleDraft)
}

// ---------------------------------------------------------------------------
// The plan is a capability, not trusted input
// ---------------------------------------------------------------------------

// RewritePlan is a public struct, so a caller can build one by hand. Without
// these checks a hand-built plan could have an attempt recorded against a node
// it has no business touching — a corpus node, a node from another document, or
// the same node twice. Each case is an error, and none of them reaches a
// provider or the store.
func TestAHandBuiltPlanCannotReachWhatItDoesNotOwn(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		tamper func(t *testing.T, root string, plan *workflow.RewritePlan)
	}{
		{"a refusal is executed", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			plan.Refusal = workflow.RefusalUncalibrated
		}},
		{"the state is not a planned one", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			plan.State = ""
		}},
		{"the target count disagrees with the dispositions", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			plan.Targets++
		}},
		{"a target names a node from the corpus", func(t *testing.T, root string, plan *workflow.RewritePlan) {
			plan.Segments[0].NodeID = aCorpusNode(t, root)
		}},
		{"a target's span disagrees with the stored node", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			plan.Segments[0].Length--
		}},
		{"a target names the draft's own heading", func(t *testing.T, root string, plan *workflow.RewritePlan) {
			// In the right document, and real — but never a paragraph, so it has
			// no vector, was never scored, and cannot be spliced.
			plan.Segments[0].NodeID = aDraftNodeWithoutAVector(t, root, plan.DraftSnapshotID)
		}},
		{"one node is targeted twice", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			plan.Segments[1].NodeID = plan.Segments[0].NodeID
			plan.Segments[1].Offset = plan.Segments[0].Offset
			plan.Segments[1].Length = plan.Segments[0].Length
		}},
		{"the draft snapshot is the profile's", func(t *testing.T, root string, plan *workflow.RewritePlan) {
			plan.DraftSnapshotID = profileSnapshotID(t, root)
		}},
		{"the selection order was changed", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			plan.ExemplarNodes[0], plan.ExemplarNodes[len(plan.ExemplarNodes)-1] =
				plan.ExemplarNodes[len(plan.ExemplarNodes)-1], plan.ExemplarNodes[0]
		}},
		{"the selection names a set that was never persisted", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			plan.ExemplarSelectionID = strings.Repeat("0", len(plan.ExemplarSelectionID))
		}},
		{"an exemplar was dropped from the list", func(_ *testing.T, _ string, plan *workflow.RewritePlan) {
			// Reordering catches an implementation that compares members as a
			// set; dropping one catches an implementation that compares only
			// the first, or only the length, or nothing at all.
			plan.ExemplarNodes = plan.ExemplarNodes[:len(plan.ExemplarNodes)-1]
		}},
		{"an exemplar was substituted for a corpus node", func(t *testing.T, root string, plan *workflow.RewritePlan) {
			plan.ExemplarNodes[len(plan.ExemplarNodes)-1] = aCorpusNode(t, root)
		}},
		{"the selection belongs to another profile", func(t *testing.T, root string, plan *workflow.RewritePlan) {
			// A real, loadable profile — the second register's head over the same
			// snapshot — so the rejection cannot come from a profile that simply
			// will not load. What is wrong is the BINDING: this plan's selection
			// was made for a different profile, and store's own validation says
			// only that the selection is internally consistent.
			plan.ProfileID = anotherProfileID(t, root)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, draft := targetStore(t)
			plan := planned(t, planRequest(root, draft))
			if len(plan.Segments) < 2 || len(plan.ExemplarNodes) < 2 {
				t.Fatalf("the fixture has %d segments and %d exemplars; some cases cannot be built",
					len(plan.Segments), len(plan.ExemplarNodes))
			}
			seeded := seedAttempt(t, root, plan)
			c.tamper(t, root, &plan)

			local := &arm{provider: newProvider(t, map[string][]string{paragraphOne: {improvesOne}})}
			runner, _ := executingRunner(local, nil)
			result, err := runner.Execute(ctx(), executeRequest(plan, localChoice()))
			if err == nil {
				t.Fatalf("the tampered plan was executed: refusal %q, %d bytes", result.Refusal, len(result.Bytes))
			}
			if result.Bytes != nil {
				t.Errorf("%d bytes were returned beside an error", len(result.Bytes))
			}
			if calls := local.provider.(*scriptedProvider).calls; len(calls) != 0 {
				t.Errorf("%d provider calls were made on a plan that does not validate", len(calls))
			}
			if len(local.choices) != 0 {
				t.Errorf("the local arm was built %d times for a plan that does not validate; "+
					"validation comes first", len(local.choices))
			}
			requireNothingWasSpent(t, root, seeded)
		})
	}
}

// ---------------------------------------------------------------------------
// The preflight, and the invariant it stands in for
// ---------------------------------------------------------------------------

// The loop rescores each target's raw span standing alone, while planning scored
// the whole document. If those ever disagreed, a paragraph the plan called a
// target would come back not-entered after the provider had been paid.
//
// They do not disagree today, and that was measured rather than assumed: an
// included leaf's raw span excludes its container's syntax, so a paragraph
// inside a list item or a table cell parses back to exactly one paragraph. This
// test asserts that invariant directly, over shapes chosen to break it, because
// it is what would notice if a structure-parser change ever made the preflight's
// error path live.
func TestEveryAdmittedLeafMeasuresTheSameAloneAsInPlace(t *testing.T) {
	t.Parallel()
	root := installRelease(t, 0.05, 5.0)
	for name, body := range map[string]string{
		"plain paragraphs": executableDraft(),
		"inside a list": "A lead-in paragraph of ordinary prose that runs on past a single sentence " +
			"so the structure pass reads it as prose rather than as a heading.\n\n" +
			"- A list item long enough to clear the lexical floor, carrying ordinary prose " +
			"past a single sentence so that it is admitted and measured.\n\n",
		"inside a table": "A lead-in paragraph of ordinary prose that runs on past a single sentence " +
			"so the structure pass reads it as prose rather than as a heading.\n\n" +
			"| heading one | heading two |\n| --- | --- |\n" +
			"| A table cell long enough to clear the lexical floor, with ordinary prose past a sentence | another cell |\n\n",
		"inside a quote": "A lead-in paragraph of ordinary prose that runs on past a single sentence " +
			"so the structure pass reads it as prose rather than as a heading.\n\n" +
			"> A quoted paragraph, long enough to clear the lexical floor, carrying ordinary " +
			"prose past a single sentence so that it is admitted and measured.\n\n",
		"a hash line inside a list item": "A lead-in paragraph of ordinary prose that runs on past a single " +
			"sentence so the structure pass reads it as prose rather than as a heading.\n\n" +
			"- A list item long enough to clear the lexical floor, carrying ordinary prose past a sentence\n" +
			"  # and a line that would be a heading if this span stood alone\n\n",
		"a setext underline inside a list item": "A lead-in paragraph of ordinary prose that runs on past a " +
			"single sentence so the structure pass reads it as prose rather than as a heading.\n\n" +
			"- A list item long enough to clear the lexical floor, carrying ordinary prose past a sentence\n" +
			"  ===\n\n",
		"after excluded leaves": "# A heading, which is not admitted\n\n" +
			"A paragraph of ordinary prose that runs on past a single sentence so the " +
			"structure pass reads it as prose rather than as a heading; it says a thing.\n\n" +
			"```\na code block, also not admitted\n```\n\n" +
			"A second paragraph doing likewise, at enough length to clear the floor and " +
			"be measured on its own terms rather than skipped.\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeDraftBytes(t, t.TempDir(), body)
			whole := scored(t, workflow.ScoreRequest{
				StorePath: defaultStorePath(root), Register: "essays", Path: path,
			})
			leaves := admittedLeaves(t, path, storedFloor(t, root))
			if len(leaves) != len(whole.Segments) {
				t.Fatalf("%d admitted leaves and %d scored segments", len(leaves), len(whole.Segments))
			}
			for i, leaf := range leaves {
				alone := scored(t, workflow.ScoreRequest{
					StorePath: defaultStorePath(root), Register: "essays", Path: writeProbe(t, leaf.Text),
				})
				if len(alone.Segments) != 1 {
					t.Fatalf("leaf %d measures %d segments standing alone; the loop requires exactly one, "+
						"and the preflight's error path is now reachable", i, len(alone.Segments))
				}
				if i >= len(whole.Segments) {
					t.Fatalf("leaf %d has no scored segment beside it", i)
				}
				if alone.Segments[0].Distance.Value != whole.Segments[i].Distance.Value {
					t.Errorf("leaf %d measures %v alone and %v in place",
						i, alone.Segments[0].Distance.Value, whole.Segments[i].Distance.Value)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Identity and cancellation
// ---------------------------------------------------------------------------

// Two runs of one plan get different invocations. A content-addressed identity
// would collide: PutRewriteAttempt refuses a differing record under an existing
// key, so re-running the same rewrite against a provider that answered
// differently would fail as an operational error the second time.
func TestTwoRunsOfOnePlanGetDifferentInvocations(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	// The default seam, not the fixed one the other tests install.
	first := workflow.Default()
	first.Providers.Local = (&arm{provider: newProvider(t, map[string][]string{paragraphOne: {improvesOne}})}).build
	second := workflow.Default()
	second.Providers.Local = (&arm{provider: newProvider(t, map[string][]string{paragraphOne: {matchesOne}})}).build

	a := executed(t, first, executeRequest(plan, localChoice()))
	b := executed(t, second, executeRequest(plan, localChoice()))

	if a.InvocationID == "" || b.InvocationID == "" {
		t.Fatalf("an invocation is empty: %q and %q", a.InvocationID, b.InvocationID)
	}
	if a.InvocationID == b.InvocationID {
		t.Error("two runs of one plan share an invocation, so the second run's attempts collide with the first's")
	}
	// And the second run's attempts really are there, which is the failure the
	// distinct identity prevents.
	if got := countAttempts(t, root); got < 2 {
		t.Errorf("the store holds %d attempts after two runs", got)
	}
}

// A cancelled run stops rather than finishing the remaining targets, and returns
// no bytes. Each persisted attempt is individually atomic, so what is left
// behind is a valid prefix — the store is consistent, not complete.
func TestACancelledRunStopsAndReturnsNoBytes(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	cancellable, cancel := context.WithCancel(context.Background())
	provider := newProvider(t, map[string][]string{
		paragraphOne: {improvesOne},
		paragraphTwo: {improvesTwo},
	})
	provider.before = func(_ *testing.T, call int) {
		if call == 1 {
			cancel()
		}
	}
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result, err := runner.Execute(cancellable, workflow.ExecuteRequest{Plan: plan, Choice: localChoice()})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if result.Bytes != nil {
		t.Errorf("%d bytes were returned from a cancelled run", len(result.Bytes))
	}
	if len(provider.calls) != 1 {
		t.Errorf("the provider was called %d times after cancellation", len(provider.calls))
	}
}

// The loop reporting a target it will not judge is a broken precondition rather
// than an ordinary non-improvement, and Execute must not quietly report it as
// none-improved. The preflight makes this unreachable in practice; the
// assertion is that no result ever carries the terminal reason that would mean
// it happened.
func TestNoOutcomeEverReportsALoopThatWasNeverEntered(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {matchesOne},
		paragraphTwo: {improvesTwo},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	// Without this the test passes against a result that carries no outcomes at
	// all, which is the vacuous reading of "no outcome reports it".
	if len(result.Outcomes) != len(targetsOf(plan)) {
		t.Fatalf("%d outcomes for %d targets", len(result.Outcomes), len(targetsOf(plan)))
	}
	for _, outcome := range result.Outcomes {
		if outcome.Terminal == string(rewrite.TerminalNotEntered) {
			t.Errorf("outcome for %s reports %q, which Execute must raise as an error",
				outcome.NodeID, rewrite.TerminalNotEntered)
		}
		declared := false
		for _, terminal := range workflow.Terminals() {
			if outcome.Terminal == terminal {
				declared = true
			}
		}
		if !declared {
			t.Errorf("outcome for %s reports terminal %q, which is not declared", outcome.NodeID, outcome.Terminal)
		}
	}
}

// ---------------------------------------------------------------------------
// Two runs at once, and an identity that cannot be minted
// ---------------------------------------------------------------------------

// Two executions of one plan running together must not collide. Attempts are
// keyed (invocation, node, attempt index), and the first two of those are the
// same for both runs unless the invocation really is distinct — a collision
// would surface as ErrConflict from the store, which is an operational failure
// on a run that did nothing wrong.
func TestTwoRunsOfOnePlanAtOnceDoNotCollide(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	results := make([]workflow.ExecuteResult, 2)
	errs := make([]error, 2)
	var waiting sync.WaitGroup
	for i := range results {
		waiting.Add(1)
		go func(i int) {
			defer waiting.Done()
			runner := workflow.Default()
			runner.Providers.Local = (&arm{provider: newProvider(t, map[string][]string{
				paragraphOne: {matchesOne, improvesOne},
				paragraphTwo: {improvesTwo},
			})}).build
			results[i], errs[i] = runner.Execute(ctx(), executeRequest(plan, localChoice()))
		}(i)
	}
	waiting.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if results[0].InvocationID == results[1].InvocationID {
		t.Fatal("two concurrent runs of one plan share an invocation")
	}
	reported := 0
	for _, result := range results {
		for _, outcome := range result.Outcomes {
			reported += len(outcome.Rejections)
		}
	}
	if got := countAttempts(t, root); got != reported {
		t.Errorf("the store holds %d attempts and the two runs report %d between them", got, reported)
	}
}

// An identity that cannot be minted is an operational failure, and nothing is
// spent. Failing closed matters: a run that proceeded with an empty invocation
// would have every attempt refused by the store as an invalid artifact — after
// the provider had been paid, which is precisely the defect B1 found.
func TestAnInvocationThatCannotBeMintedSpendsNothing(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))
	seeded := seedAttempt(t, root, plan)

	local := &arm{provider: newProvider(t, map[string][]string{paragraphOne: {improvesOne}})}
	runner, _ := executingRunner(local, nil)
	runner.NewInvocationID = func() (string, error) { return "", errors.New("no entropy") }

	result, err := runner.Execute(ctx(), executeRequest(plan, localChoice()))
	if err == nil {
		t.Fatal("a run with no invocation identity was executed")
	}
	if result.Bytes != nil {
		t.Errorf("%d bytes were returned beside an error", len(result.Bytes))
	}
	if calls := local.provider.(*scriptedProvider).calls; len(calls) != 0 {
		t.Errorf("%d provider calls were made without an invocation to record them under", len(calls))
	}
	requireNothingWasSpent(t, root, seeded)
}
