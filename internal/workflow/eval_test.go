package workflow_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// What eval measures, and what it measures it from
// ---------------------------------------------------------------------------

// The held-out author segments are already in the graph index wrote. Evaluating
// re-read files would measure whatever is on disk NOW rather than what the
// profile was fitted against — so changing the corpus after indexing must not
// change what eval reports. Nothing else in the suite would notice the
// difference, because both paths produce plausible numbers.
func TestEvalMeasuresTheIndexedGraphAndNotTheFilesOnDisk(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	distractors := distractorCorpus(t, 20)

	before := evaluated(t, evalRequest(root, distractors))

	// Rewrite every author document. The corpus on disk is now a different
	// corpus; the indexed graph is not.
	for i := 0; i < 60; i++ {
		path := filepath.Join(root, fmt.Sprintf("doc%03d.md", i))
		if err := os.WriteFile(path, []byte("Entirely different prose that says nothing it used to say.\n\nAnd a second paragraph of it as well, also quite different.\n\n"), 0o644); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
	}

	after := evaluated(t, evalRequest(root, distractors))
	if after.ReleaseID != before.ReleaseID {
		t.Errorf("rewriting the corpus changed the release, %q then %q; eval read the disk",
			before.ReleaseID, after.ReleaseID)
	}
	if after.AuthorSegments != before.AuthorSegments {
		t.Errorf("author segments %d then %d", before.AuthorSegments, after.AuthorSegments)
	}
	// The identity alone is not enough: an implementation that re-walked the
	// corpus but took its identity from the stored snapshot would produce the
	// same release ID over different numbers. The whole measurement has to be
	// equal, compared as a value so a figure added later is covered.
	if !reflect.DeepEqual(after.Discrimination, before.Discrimination) {
		t.Errorf("the discrimination moved:\n before %+v\n after  %+v",
			before.Discrimination, after.Discrimination)
	}
	if !reflect.DeepEqual(after.Calibration, before.Calibration) {
		t.Errorf("the calibration moved:\n before %+v\n after  %+v",
			before.Calibration, after.Calibration)
	}
	if after.DistractorSegments != before.DistractorSegments || after.Split != before.Split {
		t.Errorf("the population moved: %d/%q then %d/%q",
			before.DistractorSegments, before.Split, after.DistractorSegments, after.Split)
	}
}

// THE WHOLE POOL IS DISTRACTOR MATERIAL. There is nothing to hold out of it —
// every member is "not the author" — so splitting it the way the author's corpus
// is split throws away most of the comparison. Nothing in this suite noticed
// until the numbers were looked at: twenty distractor documents were yielding
// six segments, which is one document's worth, and the discrimination bound came
// back at MINUS TWO because the cap is 1 - 3/clusters and one cluster makes it
// negative. The AUC was a perfect 1.000 the whole time.
func TestEveryDistractorContributesToTheComparison(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	const members = 20
	distractors := distractorCorpus(t, members)

	result := evaluated(t, evalRequest(root, distractors))

	if result.DistractorMembers != members {
		t.Errorf("the pool holds %d of %d files", result.DistractorMembers, members)
	}
	// Six paragraphs each, all above the floor, and none of them held out.
	if want := members * 6; result.DistractorSegments != want {
		t.Errorf("%d distractor segments over %d members; want %d — the pool is being "+
			"split as if part of it were held out", result.DistractorSegments, members, want)
	}
	// And the clusters are documents, so a pool of twenty is twenty of them.
	if result.DistractorClusters != members {
		t.Errorf("%d distractor clusters over %d members", result.DistractorClusters, members)
	}
}

// The declared floor is unreachable below a certain number of clusters, and that
// is arithmetic rather than luck: the bound is capped at 1 - 3/clusters, so a
// floor of 0.80 needs fifteen clusters per class before it can be cleared at
// all. A fixture that cannot supply them cannot ship however separable its prose
// is — which is why the cap is reported and not just the bound.
func TestTheBoundIsCappedByTheNumberOfClusters(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	result := evaluated(t, evalRequest(root, distractorCorpus(t, 20)))

	clusters := result.Discrimination.AuthorClusters
	if other := result.Discrimination.DistractorClusters; other < clusters {
		clusters = other
	}
	if clusters == 0 {
		t.Fatal("no clusters at all")
	}
	if want := 1 - 3/float64(clusters); result.Discrimination.Cap != want {
		t.Errorf("cap = %v over %d clusters, want %v", result.Discrimination.Cap, clusters, want)
	}
	if result.Discrimination.LowerBound > result.Discrimination.Cap {
		t.Errorf("bound %v exceeds its own cap %v", result.Discrimination.LowerBound, result.Discrimination.Cap)
	}
	if result.Discrimination.MinClusters != 15 {
		t.Errorf("min clusters = %d for a floor of %v, want 15",
			result.Discrimination.MinClusters, result.Discrimination.Floor)
	}
}

// A pool is other people's writing, and what reaches the database is its
// identity and its members' content hashes. Adding one file changes the pool,
// and therefore the release.
func TestTheDistractorPoolIdentifiesTheReleaseItCalibrated(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	distractors := distractorCorpus(t, 20)

	first := evaluated(t, evalRequest(root, distractors))
	if first.DistractorPoolID == "" {
		t.Fatal("the release names no distractor pool")
	}

	writeDistractor(t, distractors, 999)
	second := evaluated(t, evalRequest(root, distractors))

	if second.DistractorPoolID == first.DistractorPoolID {
		t.Error("adding a distractor did not change the pool it was calibrated against")
	}
	if second.ReleaseID == first.ReleaseID {
		t.Error("a different pool produced the same release")
	}
}

// The same pool, unchanged, is the same release. A rerun that produced a new
// identity every time would make the head churn and the audit record meaningless.
func TestARerunOverTheSameEvidenceIsTheSameRelease(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	distractors := distractorCorpus(t, 20)

	first := evaluated(t, evalRequest(root, distractors))
	second := evaluated(t, evalRequest(root, distractors))
	if second.ReleaseID != first.ReleaseID {
		t.Errorf("two runs over identical evidence produced %q and %q", first.ReleaseID, second.ReleaseID)
	}
	if second.DistractorPoolID != first.DistractorPoolID {
		t.Error("the same directory produced two pool identities")
	}
}

// ---------------------------------------------------------------------------
// The head, and what may move it
// ---------------------------------------------------------------------------

// The head moves if and only if the release ships. Asserted as a BICONDITIONAL
// rather than by requiring a shippable outcome, because whether synthetic prose
// clears a real discrimination floor is a property of the fixture and not of
// the rule — and a test that skips when it does not is a test that can quietly
// never run.
func TestTheHeadMovesExactlyWhenTheReleaseShips(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	result := evaluated(t, evalRequest(root, distractorCorpus(t, 20)))

	head := releaseHeadOrEmpty(t, defaultStorePath(root), result.ProfileID)
	switch {
	case result.Shippable && head != result.ReleaseID:
		t.Errorf("a shippable release %q is not the head; the head is %q", result.ReleaseID, head)
	case !result.Shippable && head == result.ReleaseID:
		t.Errorf("an unshippable release %q became the head: %s", result.ReleaseID, result.Reason)
	}
	// Either way it was persisted, because an adverse result is evidence.
	if result.ReleaseID == "" {
		t.Error("nothing was persisted")
	}
}

// And an adverse evaluation does not CLEAR a head that already exists.
//
// The head that stands is deliberately an adverse release rather than a
// shippable one, because the invariant is about not clearing, not about what is
// being preserved — and a shippable release is expensive to reach honestly. The
// calibration gate needs ceil(3/p) clusters per band: sixty held-out author
// documents at p_author = 0.05 and thirty distractors at p_distractor = 0.10.
// Sixty held-out documents is a corpus of about seven hundred, which is a slow
// fixture to pay for one assertion about whether a head moved.
func TestAnAdverseEvaluationDoesNotClearAnExistingHead(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))

	// A first evaluation, persisted but not shipped.
	first := evaluated(t, evalRequest(root, distractorCorpus(t, 20)))
	if first.Shippable {
		t.Fatal("this fixture is not meant to ship; the test below assumes it did not")
	}
	if first.ReleaseID == "" {
		t.Fatal("nothing was persisted to make a head of")
	}
	// Installed directly, so the head exists whatever the measurement said.
	installReleaseHead(t, defaultStorePath(root), first.ReleaseID)

	// A second, worse evaluation: no distractors at all.
	adverse := evaluated(t, evalRequest(root, ""))
	if adverse.Shippable {
		t.Fatal("an evaluation with no distractors called itself shippable")
	}
	if adverse.ReleaseID == first.ReleaseID {
		t.Error("the second result was written at the first one's identity")
	}
	if head := releaseHeadOrEmpty(t, defaultStorePath(root), adverse.ProfileID); head != first.ReleaseID {
		t.Errorf("head = %q, was %q; an adverse rerun moved a head it did not earn",
			head, first.ReleaseID)
	}
}

// Omitting --distractors is a declared outcome rather than a usage error: ADR
// 0005 says eval reports uncalibrated without them. It completes and is adverse.
func TestNoDistractorsIsACompletedUncalibratedMeasurement(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))

	result := evaluated(t, evalRequest(root, ""))
	if result.Shippable {
		t.Error("shippable with nothing to discriminate against")
	}
	if !result.Adverse {
		t.Error("not adverse, having measured nothing")
	}
	if result.Reason == "" {
		t.Error("adverse with no reason")
	}
	if result.DistractorPoolID != "" {
		t.Errorf("named a pool %q it was not given", result.DistractorPoolID)
	}
}

// ---------------------------------------------------------------------------
// Failures that are not outcomes
// ---------------------------------------------------------------------------

// Nothing indexed yet is the ordinary first-run state and the same refusal
// profile makes: an unmet precondition, not a failure.
func TestEvaluatingWithNoProfileIsARefusalAndNotAFailure(t *testing.T) {
	root := corpusOf(t, 2)
	indexed(t, indexRequest(root)) // too small to fit a profile: a store with no head

	result, err := workflow.Default().Eval(ctx(), evalRequest(root, distractorCorpus(t, 20)))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if result.Selection != workflow.SelectionNoProfile {
		t.Errorf("selection = %q, want %q", result.Selection, workflow.SelectionNoProfile)
	}
	if result.ReleaseID != "" {
		t.Errorf("wrote a release against no profile: %q", result.ReleaseID)
	}
}

// A distractor directory that is not there is operational: nothing was
// measured, so there is nothing to report about it.
func TestAMissingDistractorDirectoryIsAFailure(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))

	request := evalRequest(root, filepath.Join(t.TempDir(), "absent"))
	if _, err := workflow.Default().Eval(ctx(), request); err == nil {
		t.Error("evaluated against a distractor directory that does not exist")
	}
}

// Two members with identical content would be one cluster pretending to be two,
// and the bootstrap groups by content hash because a pool keeps no paths.
func TestADistractorPoolRefusesDuplicateContent(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	distractors := distractorCorpus(t, 20)

	// Same bytes under a different name.
	body, err := os.ReadFile(filepath.Join(distractors, "other000.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distractors, "copy.md"), body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result := evaluated(t, evalRequest(root, distractors))
	pool := storedPool(t, defaultStorePath(root), result.DistractorPoolID)

	seen := map[string]bool{}
	for _, hash := range pool {
		if seen[hash] {
			t.Errorf("the pool holds %q twice", hash)
		}
		seen[hash] = true
	}
	// Twenty-one files, twenty distinct contents. A pool of twenty-one would
	// mean the copy was admitted; a pool of nineteen would mean both were
	// dropped rather than one.
	if len(pool) != 20 {
		t.Errorf("the pool holds %d members over 21 files with 20 distinct contents", len(pool))
	}
	if result.DistractorMembers != len(pool) {
		t.Errorf("the result reports %d members and the store holds %d",
			result.DistractorMembers, len(pool))
	}
}

// The release names the pool it was calibrated against, in the DATABASE and not
// only in the return value. A release whose evidence cannot be identified after
// the fact is not an audit record.
func TestThePersistedReleaseNamesItsPool(t *testing.T) {
	root := corpusOf(t, 60)
	indexed(t, indexRequest(root))
	result := evaluated(t, evalRequest(root, distractorCorpus(t, 20)))
	if result.ReleaseID == "" {
		t.Fatal("nothing was persisted")
	}

	stored := storedRelease(t, defaultStorePath(root), result.ReleaseID)
	if stored.DistractorPoolID != result.DistractorPoolID {
		t.Errorf("the stored release names pool %q and the run used %q",
			stored.DistractorPoolID, result.DistractorPoolID)
	}
	if _, err := openStore(t, defaultStorePath(root)).LoadDistractorPool(ctx(), stored.DistractorPoolID); err != nil {
		t.Errorf("the pool the release names is not in the store: %v", err)
	}
}

// What a shippable release actually costs, measured rather than assumed, and
// recorded here because it is the reason no test above asserts one end to end.
//
// The discrimination bound is capped at 1 - 3/clusters, so a floor of 0.80 needs
// fifteen clusters per class. The calibration gate is stricter: ceil(3/target)
// clusters per band, which is sixty author clusters at p_author = 0.05 and
// thirty distractor clusters at p_distractor = 0.10. Held-out documents are
// about eight per cent of a corpus, so sixty of them is roughly seven hundred
// documents.
//
// The prose was never the problem. At sixty documents the AUC was a perfect
// 1.000 and the bound was minus two, because twenty distractors were being
// split down to one cluster.
