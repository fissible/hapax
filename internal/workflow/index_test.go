package workflow_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// The three outcomes
// ---------------------------------------------------------------------------

// A corpus large enough for both a profile and a reference indexes completely.
// Nothing about it is adverse — and every qualification check still reports
// not-performed, because none is implemented. Those are two different claims and
// the result has to make both.
func TestAFullCorpusIndexesAndStillReportsEveryCheckNotPerformed(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 60)
	requireComposition(t, root, 51, 6, 3)
	// Six calibrate documents of ten paragraphs each. If the declared minimum
	// ever rises above that, this fixture stops exercising a full index and
	// would quietly become a test of the middle mode.
	if deviation.DefaultMinSegments() > 60 {
		t.Fatalf("the declared minimum is %d; this fixture supplies 60 calibrate segments",
			deviation.DefaultMinSegments())
	}

	result := indexed(t, indexRequest(root))

	if result.Mode != workflow.IndexProfileAndReference {
		t.Errorf("mode = %q, want %q", result.Mode, workflow.IndexProfileAndReference)
	}
	if result.Adverse || result.Adversity != "" {
		t.Errorf("a complete index reported adverse=%v adversity=%q", result.Adverse, result.Adversity)
	}
	if result.SnapshotID == "" || result.ProfileID == "" || result.ReferenceID == "" {
		t.Errorf("incomplete identities: %+v", result)
	}
	if result.StorePath != defaultStorePath(root) {
		t.Errorf("store = %q, want %q", result.StorePath, defaultStorePath(root))
	}

	// Every declared check by NAME, not merely the right number of them: a
	// duplicate would satisfy a count while a declared check went unreported.
	reported := map[string]string{}
	for _, check := range result.Checks {
		if _, twice := reported[check.Name]; twice {
			t.Errorf("%s was reported twice", check.Name)
		}
		reported[check.Name] = check.State
	}
	for _, name := range workflow.CheckNames() {
		state, present := reported[name]
		if !present {
			t.Errorf("%s was not reported", name)
			continue
		}
		if state != string(corpus.CheckNotPerformed) {
			t.Errorf("%s = %q; this slice implements no qualification", name, state)
		}
	}
	for name := range reported {
		if !contains(workflow.CheckNames(), name) {
			t.Errorf("%s was reported and is not declared", name)
		}
	}
}

// A corpus too small to fit a profile is adverse and COMPLETED: the snapshot is
// persisted, because "indexed, nothing fits yet" has to leave something to look
// at, and because an operational failure would commit nothing at all.
func TestACorpusTooSmallForAProfileIsAdverseAndStillPersisted(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 2)
	requireComposition(t, root, 2, 0, 0)

	result := indexed(t, indexRequest(root))

	if result.Mode != workflow.IndexSnapshotOnly {
		t.Errorf("mode = %q, want %q", result.Mode, workflow.IndexSnapshotOnly)
	}
	if !result.Adverse || result.Adversity != workflow.AdversityCorpusTooSmall {
		t.Errorf("adverse=%v adversity=%q, want a corpus-too-small adversity", result.Adverse, result.Adversity)
	}
	if result.SnapshotID == "" {
		t.Error("nothing was persisted; the snapshot is what an adverse index leaves behind")
	}
	if result.ProfileID != "" || result.ReferenceID != "" {
		t.Errorf("a profile or reference was claimed: %+v", result)
	}
	// And the database says so too, not just the return value.
	if _, err := openStore(t, defaultStorePath(root)).Snapshot(ctx(), result.SnapshotID); err != nil {
		t.Errorf("the snapshot it reported is not in the store: %v", err)
	}
	if heads, _ := persistedProfiles(t, defaultStorePath(root)); heads != 0 {
		t.Errorf("%d profile heads for a corpus that fits none", heads)
	}
}

// And a corpus that fits a profile but cannot fill a reference is the middle
// mode: the profile is kept and headed, the reference is not invented.
func TestACorpusThatCannotFillAReferenceKeepsItsProfile(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 3)
	requireComposition(t, root, 3, 0, 0)

	result := indexed(t, indexRequest(root))

	if result.Mode != workflow.IndexProfile {
		t.Errorf("mode = %q, want %q", result.Mode, workflow.IndexProfile)
	}
	if !result.Adverse || result.Adversity != workflow.AdversityReferenceTooSmall {
		t.Errorf("adverse=%v adversity=%q, want a reference-too-small adversity", result.Adverse, result.Adversity)
	}
	if result.ProfileID == "" {
		t.Error("the profile that fitted was not kept")
	}
	if result.ReferenceID != "" {
		t.Error("a reference was claimed over no calibrate documents")
	}
	heads, references := persistedProfiles(t, defaultStorePath(root))
	if heads != 1 {
		t.Errorf("%d heads; the profile that fitted should be the register's head", heads)
	}
	if references != 0 {
		t.Errorf("%d references persisted", references)
	}
}

// The declared reference minimum is the one that decides, not a number the
// workflow reached for. Pinned from BOTH sides of the boundary the fixture
// actually supplies — a minimum equal to the available segments must build a
// reference and one segment more must not. Asserting only the refusing side
// would still pass if segmentation changed and the corpus supplied fewer than
// anyone thought.
func TestTheReferenceMinimumDecidesWhetherAReferenceIsBuilt(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 60)
	requireComposition(t, root, 51, 6, 3)

	// Counted from the persisted graph rather than taken from the result. If
	// the boundary were pinned against a number the workflow derived from the
	// same expression that decides the mode, the test would be comparing that
	// expression with itself.
	first := indexed(t, indexRequest(root))
	available := vectoredLeaves(t, defaultStorePath(root), first.SnapshotID, corpus.Calibrate)
	if available == 0 {
		t.Fatal("the fixture supplies no calibrate segments; there is no boundary to pin")
	}
	if first.CalibrateSegments != available {
		t.Errorf("the result reports %d calibrate segments and the graph holds %d",
			first.CalibrateSegments, available)
	}

	exact, err := workflow.New(profile.DefaultRequirements(), available).Index(ctx(), indexRequest(corpusOf(t, 60)))
	if err != nil {
		t.Fatalf("Index at exactly %d: %v", available, err)
	}
	if exact.Mode != workflow.IndexProfileAndReference || exact.ReferenceID == "" {
		t.Errorf("a minimum of %d over %d segments built no reference: mode %q",
			available, available, exact.Mode)
	}

	strict, err := workflow.New(profile.DefaultRequirements(), available+1).Index(ctx(), indexRequest(corpusOf(t, 60)))
	if err != nil {
		t.Fatalf("Index at %d: %v", available+1, err)
	}
	if strict.Mode != workflow.IndexProfile {
		t.Errorf("mode = %q with a minimum of %d over %d segments, want %q",
			strict.Mode, available+1, available, workflow.IndexProfile)
	}
	if strict.ReferenceID != "" {
		t.Error("a reference was built below the minimum it was given")
	}
}

// Adversity and the flag are one fact, not two that can disagree.
func TestAdversityAndItsFlagCannotDisagree(t *testing.T) {
	t.Parallel()
	for _, size := range []int{2, 3, 60} {
		result := indexed(t, indexRequest(corpusOf(t, size)))
		if result.Adverse != (result.Adversity != "") {
			t.Errorf("%d documents: adverse=%v with adversity %q", size, result.Adverse, result.Adversity)
		}
		if result.Adversity != "" && !contains(workflow.Adversities(), result.Adversity) {
			t.Errorf("%d documents: adversity %q is outside the declared vocabulary", size, result.Adversity)
		}
	}
}

// ---------------------------------------------------------------------------
// Where the store goes
// ---------------------------------------------------------------------------

// The database lives under the corpus root, and the corpus does not index it:
// corpus.Walk skips dot-prefixed entries, so a second index over the same root
// must not discover the first one's database as a document.
func TestTheStoreLivesUnderTheRootAndIsNotIndexedByIt(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 60)
	first := indexed(t, indexRequest(root))
	if _, err := os.Stat(defaultStorePath(root)); err != nil {
		t.Fatalf("no database at the declared path: %v", err)
	}

	second := indexed(t, indexRequest(root))
	if second.Documents != first.Documents {
		t.Errorf("the second index saw %d documents where the first saw %d; it found its own store",
			second.Documents, first.Documents)
	}
	if second.SnapshotID != first.SnapshotID {
		t.Errorf("the same corpus produced two snapshot identities, %q then %q",
			first.SnapshotID, second.SnapshotID)
	}
}

// An explicit --store is an exact file, and creating directories for it would
// turn a typo into a directory somewhere the user did not ask for.
func TestAnExplicitStorePathCreatesNoDirectory(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 60)
	absent := filepath.Join(t.TempDir(), "no", "such", "dir", "hapax.sqlite3")

	request := indexRequest(root)
	request.StorePath = absent
	if _, err := workflow.Default().Index(ctx(), request); err == nil {
		t.Fatal("indexed into a directory that does not exist")
	}
	if _, err := os.Stat(filepath.Dir(absent)); !os.IsNotExist(err) {
		t.Errorf("the parent directory was created: %v", err)
	}
	// And the corpus root did not quietly get one either.
	if _, err := os.Stat(filepath.Join(root, ".hapax")); !os.IsNotExist(err) {
		t.Errorf("an override still created the default store: %v", err)
	}
}

// An explicit --store that IS reachable is used, and the default is not created
// beside it.
func TestAnExplicitStoreIsTheOneWritten(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 60)
	elsewhere := filepath.Join(t.TempDir(), "chosen.sqlite3")

	request := indexRequest(root)
	request.StorePath = elsewhere
	result := indexed(t, request)

	if result.StorePath != elsewhere {
		t.Errorf("store = %q, want %q", result.StorePath, elsewhere)
	}
	if _, err := os.Stat(elsewhere); err != nil {
		t.Errorf("nothing was written to the named store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".hapax")); !os.IsNotExist(err) {
		t.Error("the default store was created as well as the named one")
	}
}

// ---------------------------------------------------------------------------
// One floor, not two
// ---------------------------------------------------------------------------

// #55: ingest hardcoded profile.DefaultRequirements() for the paragraph floor
// while profile.Build took its caller's, so a non-default floor would persist
// vectors chosen under one rule and fit a profile under another. A2a does not
// restructure that, but it must not be able to happen.
//
// Compared against the DATABASE, not against a second counter in the same
// result: two numbers the workflow derived from profile.Build would agree with
// each other while the persisted graph disagreed with both. The fixture puts
// half its paragraphs below the raised floor and half above, so the two answers
// differ by a factor of two if the floors diverge.
func TestOneParagraphFloorGovernsBothTheGraphAndTheProfile(t *testing.T) {
	t.Parallel()
	root := mixedCorpusOf(t, 60)
	requireComposition(t, root, 50, 3, 7)

	requirements := profile.DefaultRequirements()
	requirements.MinParagraphLexicalTokens = 12
	requireStraddle(t, root, requirements.MinParagraphLexicalTokens)

	runner := workflow.New(requirements, deviation.DefaultMinSegments())
	result, err := runner.Index(ctx(), indexRequest(root))
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if result.ProfileID == "" {
		t.Fatal("no profile was fitted; this test needs one to compare against")
	}

	stored := vectoredTrainNodes(t, defaultStorePath(root), result.SnapshotID)
	if stored == 0 || result.TrainParagraphs == 0 {
		t.Fatalf("nothing cleared the floor, so agreement is vacuous: %d stored, %d counted",
			stored, result.TrainParagraphs)
	}
	if stored != result.TrainParagraphs {
		t.Errorf("the database holds %d train vectors and the profile counted %d paragraphs; "+
			"two floors were applied", stored, result.TrainParagraphs)
	}

	// And the profile the store holds records the floor it was fitted under, so
	// a workflow that vectorised at twelve and fitted at one cannot satisfy the
	// comparison above by reporting the graph's count as the profile's.
	bundle := persistedBundle(t, defaultStorePath(root), "essays")
	if bundle.Profile.MinParagraphLexicalTokens != requirements.MinParagraphLexicalTokens {
		t.Errorf("the stored profile was fitted at a floor of %d, not %d",
			bundle.Profile.MinParagraphLexicalTokens, requirements.MinParagraphLexicalTokens)
	}
	// A feature defined on every paragraph is summarised over exactly the
	// population the graph kept. None may exceed it, and at least one must
	// reach it, or the profile was fitted over a different set of paragraphs.
	reached := false
	for _, statistic := range bundle.Profile.Stats {
		if statistic.N > stored {
			t.Errorf("%s is summarised over %d observations and the graph kept %d",
				statistic.Feature, statistic.N, stored)
		}
		if statistic.N == stored {
			reached = true
		}
	}
	if !reached {
		t.Errorf("no statistic covers all %d kept paragraphs; the profile saw a different population", stored)
	}

	// And the raised floor excluded something, or agreement above would prove
	// nothing: at the default floor of one, every short paragraph counts too.
	loose := mixedCorpusOf(t, 60)
	relaxed := workflow.New(profile.DefaultRequirements(), deviation.DefaultMinSegments())
	looseResult, err := relaxed.Index(ctx(), indexRequest(loose))
	if err != nil {
		t.Fatalf("Index at the default floor: %v", err)
	}
	atDefault := vectoredTrainNodes(t, defaultStorePath(loose), looseResult.SnapshotID)
	if atDefault <= stored {
		t.Errorf("raising the floor to %d excluded nothing: %d train vectors against %d",
			requirements.MinParagraphLexicalTokens, stored, atDefault)
	}
}

// The declared defaults are the ones a real invocation uses. A runner whose
// defaults were anything else would make every test above a statement about the
// test's own configuration.
func TestTheDefaultRunnerUsesTheDeclaredMinimums(t *testing.T) {
	t.Parallel()
	runner := workflow.Default()
	if runner.Requirements != profile.DefaultRequirements() {
		t.Errorf("requirements = %+v, want %+v", runner.Requirements, profile.DefaultRequirements())
	}
	if runner.MinSegments != deviation.DefaultMinSegments() {
		t.Errorf("min segments = %d, want %d", runner.MinSegments, deviation.DefaultMinSegments())
	}
}

// ---------------------------------------------------------------------------
// Failures that are not outcomes
// ---------------------------------------------------------------------------

// A corpus root that is not there is an operational failure, not an adverse
// result: nothing was measured, so there is nothing to report about it.
func TestAMissingCorpusRootIsAFailureAndNotAnAdverseResult(t *testing.T) {
	t.Parallel()
	request := indexRequest(filepath.Join(t.TempDir(), "absent"))
	result, err := workflow.Default().Index(ctx(), request)
	if err == nil {
		t.Fatal("indexed a corpus root that does not exist")
	}
	if errors.Is(err, profile.ErrCorpusTooSmall) {
		t.Errorf("a missing root was classified as an insufficient corpus: %v", err)
	}
	if result.SnapshotID != "" {
		t.Errorf("a failed index reported a snapshot: %+v", result)
	}
}

// The register names the profile head, so it cannot be defaulted: two unrelated
// corpora sharing an invented default would overwrite each other's head.
func TestIndexRequiresARegister(t *testing.T) {
	t.Parallel()
	request := indexRequest(corpusOf(t, 60))
	request.Register = ""
	if _, err := workflow.Default().Index(ctx(), request); err == nil {
		t.Error("indexed without a register")
	}
}

func contains[T comparable](values []T, wanted T) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// The declared bootstrap is what a real invocation uses. Most tests here run a
// cheap one, so without this the suite would be measuring its own convenience:
// a Default that quietly resampled twenty-four times would satisfy every other
// assertion in the package.
func TestTheDefaultRunnerUsesTheDeclaredBootstrap(t *testing.T) {
	t.Parallel()
	runner := workflow.Default()
	if runner.Discrimination != eval.DefaultDiscrimination() {
		t.Errorf("discrimination spec = %+v, want %+v", runner.Discrimination, eval.DefaultDiscrimination())
	}
	if runner.BandFloor != eval.DefaultBandFloor() {
		t.Errorf("band floor = %+v, want %+v", runner.BandFloor, eval.DefaultBandFloor())
	}
	if runner.Bootstrap != eval.DefaultBootstrap() {
		t.Errorf("bootstrap = %+v, want %+v", runner.Bootstrap, eval.DefaultBootstrap())
	}
	// And the cheap runner the harness uses differs in the resample counts and
	// in NOTHING else, or a test passing here would say nothing about a real run.
	cheap := cheapRunner()
	if cheap.Requirements != runner.Requirements || cheap.MinSegments != runner.MinSegments {
		t.Error("the cheap runner relaxed something other than the resample count")
	}
	cheap.Discrimination.Resamples = runner.Discrimination.Resamples
	cheap.BandFloor.Resamples = runner.BandFloor.Resamples
	cheap.Bootstrap.Resamples = runner.Bootstrap.Resamples
	if cheap.Discrimination != runner.Discrimination || cheap.BandFloor != runner.BandFloor ||
		cheap.Bootstrap != runner.Bootstrap {
		t.Error("the cheap runner changed a spec field other than Resamples")
	}
}
