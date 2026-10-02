package workflow_test

// #126. `targetStore` and `settledStore` are built 107 times per package run, and
// each build re-crafts a release and then re-plans the draft to check the fixture's
// shape. Both are deterministic, so both can be done ONCE and the result copied.
//
// # Contract
//
// What a cached fixture owes its callers, and what these tests pin:
//
//   - the dispositions the helper's name promises — targets for `targetStore`,
//     in-range for `settledStore` — asserted as EXPLICIT values and not merely as
//     agreement between two builds, because agreement admits both being wrong;
//   - agreement with a build that does not go through the cache at all, on the
//     state the helper hands back BEFORE anything plans again. That includes the
//     exemplar selection the setup plan PERSISTED, which a cache that skipped the
//     plan would silently drop, and the corpus document by document, which a
//     cross-copy comparison cannot see when the damage is uniform;
//   - independence: a test that disturbs its store — in the DATABASE, not only in
//     the files — must not disturb another's, including a copy taken afterwards;
//   - lifetime: the template outlives the test that happened to build it, which a
//     `t.TempDir()` template does not;
//   - a copy check that rejects the wrong release, not merely a missing one.
//
// # Evidence: what the cost actually is, and what the ticket got wrong
//
// Measured on darwin, `-race`, which is what CI runs, with `-count=1` in a fresh
// process per run. The whole package is 425.3s locally; ubuntu CI reported 428.9s
// at feff482. Two similar TOTALS do not establish that component savings carry
// across platforms — they are reported together only so the local baseline is not
// mistaken for a different workload.
//
// The ticket blames "walks a corpus, indexes it". That is ALREADY cached —
// `variedTemplate` is a `sync.OnceValues` and `copyOfTemplate` copies it. The real
// per-fixture cost, after that copy:
//
//	copy 33ms | LoadProfileBundle 29ms | ReleaseAround 330ms | PutRelease 19ms
//	writeDraft 0.2ms | requireDispositions 2.20s || TOTAL ~2.6s
//
// So `requireDispositions` is 84% of it, and the ticket does not mention it. It is
// expensive because it runs a full plan.
//
// Counted by instrumenting each helper at 5fabca1, not by reading call sites:
// targetStore 101, settledStore 6, installRelease 114, requireDispositions 107,
// across the 222 tests the package held at that revision. The ticket says "roughly
// forty". This file adds six more tests, one of them deliberately non-parallel.
//
// # Consequence: the prize is smaller than the ticket implies
//
// Summed fixture latency is not wall time — most of the package's tests call
// `t.Parallel()` (204 of the 222 top-level tests at 5fabca1), at GOMAXPROCS 10. So the saving was measured rather than projected:
//
//	go test -race -timeout 30m ./internal/workflow -count=1        425.3s
//	the same with requireDispositions stubbed to a no-op           338.5s
//
// 86.8s, 20.4%. That number is what the NO-OP experiment removes, and it is not a
// caching ceiling: the stub also skips persisting the draft snapshot and exemplar
// selection that the real check leaves behind, so a correct cache has to keep that
// state and does strictly more work than the stub — how much of the 86.8s survives
// is unmeasured until a cache exists. 86.8s is 37% of the 235s of summed latency;
// applying
// the same ratio to the release-install component, 107 x 0.378s, ESTIMATES roughly
// 15s more. Where the remaining time goes is not measured here.
//
// The 7 direct `installRelease` calls are worth 2.6s summed, so they are excluded
// from scope — not because caching them was measured and found worthless, but
// because 2.6s does not justify the second kind of template it would need (a
// released store with no draft, so those callers keep exactly today's state).

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/eval/evaltest"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/workflow"
)

// The dispositions each helper promises, stated as values. If a fixture edit moves a
// distance across a release boundary this fails here, once, instead of making every
// downstream assertion vacuous.
func TestTheCachedFixturesHaveTheDispositionsTheirNamesPromise(t *testing.T) {
	t.Parallel()
	for _, c := range fixtureConfigurations() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, draft := c.build(t)
			c.requireShape(t, root, draft, c.name)
		})
	}
}

// A cached fixture must match one built the OLD way, without touching the cache.
//
// Compared on the state the helper hands back, read BEFORE either side is planned
// again. Planning the cached copy first would RECREATE the artifacts the setup plan
// persisted, so a cache that dropped them would look identical.
func TestACachedFixtureMatchesABuildThatNeverTouchesTheCache(t *testing.T) {
	// NOT parallel at this level: the target oracle is built once here and the
	// settled subtest needs its exemplar selection id, which is the id a settled
	// store would hold if it had been prepared from a planned TARGET template.
	// Selection identity derives from the selected corpus exemplars and the
	// profile, and the corpora are identical, so the oracle's id is exactly the one
	// such contamination would leave behind.
	configurations := fixtureConfigurations()
	targetConfiguration := configurations[0]
	if targetConfiguration.name != "targetStore" || !targetConfiguration.selectsExemplars {
		t.Fatalf("the first configuration is %q with selectsExemplars=%v; this test "+
			"needs the target one first to obtain a selection id",
			targetConfiguration.name, targetConfiguration.selectsExemplars)
	}
	_, _, targetOraclePlan := freshStore(t, targetConfiguration)
	if targetOraclePlan.ExemplarSelectionID == "" {
		t.Fatal("the target oracle selected no exemplars, so there is no id to " +
			"check for in the settled store")
	}

	for _, c := range configurations {
		t.Run(c.name, func(t *testing.T) {
			cachedRoot, cachedDraft := c.build(t)
			freshRoot, freshDraft, freshPlan := freshStore(t, c)

			if cachedRoot == freshRoot {
				t.Fatal("both builds returned the same root; they are not independent")
			}

			cached := fixtureIdentity(t, cachedRoot, cachedDraft)
			fresh := fixtureIdentity(t, freshRoot, freshDraft)

			if cached.profileID != fresh.profileID {
				t.Errorf("profile id: cached %s, fresh %s", cached.profileID, fresh.profileID)
			}
			if cached.referenceID != fresh.referenceID {
				t.Errorf("reference id: cached %s, fresh %s", cached.referenceID, fresh.referenceID)
			}
			if cached.releaseHead != fresh.releaseHead {
				t.Errorf("release head: cached %s, fresh %s", cached.releaseHead, fresh.releaseHead)
			}
			if cached.draftBody != fresh.draftBody {
				t.Error("the draft bodies differ")
			}
			if cached.profileID == "" || cached.referenceID == "" || cached.releaseHead == "" {
				t.Fatalf("the cached fixture has empty identity: %+v", cached)
			}
			if !reflect.DeepEqual(cached.profileHeads, fresh.profileHeads) {
				t.Errorf("profile heads differ:\n cached %v\n fresh  %v",
					cached.profileHeads, fresh.profileHeads)
			}
			if len(fresh.profileHeads) != 2 {
				t.Fatalf("the oracle has %d profile heads, want essays and letters; "+
					"comparing the map proves little with one register",
					len(fresh.profileHeads))
			}

			// The artifacts the setup plan PERSISTED, looked up by the ORACLE's ids
			// inside the CACHED store so that no second plan is needed — planning the
			// cached copy would recreate whatever was missing.
			//
			// The DRAFT SNAPSHOT is the load-bearing one. An earlier version checked
			// only the exemplar selection and claimed "a cache that planned something
			// else has a different id", which is FALSE: selection identity derives
			// from the selected corpus exemplars, so planning a different draft need
			// not change it. Two implementations passed on that basis — one deleting
			// the draft snapshot graph while keeping the selection, one planning
			// `repeatedDraft()` and then restoring `executableDraft()` without
			// replanning.
			requirePersistedPlan(t, cachedRoot, freshRoot, freshPlan, c.selectsExemplars)

			// A settled fixture must not carry the TARGET selection. Checking the
			// oracle's own plan recorded none proves nothing about the cached
			// store: an implementation that installed the target release, planned,
			// then restored the settled release leaves the selection loadable, and
			// that passed every test until this ran.
			if !c.selectsExemplars {
				// NOT FOUND specifically. `err != nil` would accept a selection that
				// is present but corrupt, and an implementation that left the row
				// behind while deleting its members produced exactly that and passed.
				_, err := openStore(t, defaultStorePath(cachedRoot)).
					LoadExemplarSelection(ctx(), targetOraclePlan.ExemplarSelectionID)
				switch {
				case err == nil:
					t.Errorf("the settled fixture carries exemplar selection %s, which "+
						"only a TARGET plan creates — it was prepared from a planned "+
						"target template", targetOraclePlan.ExemplarSelectionID)
				case !errors.Is(err, store.ErrNotFound):
					t.Errorf("looking for selection %s in the settled fixture gave %v, "+
						"want %v — the row is present in some other state",
						targetOraclePlan.ExemplarSelectionID, err, store.ErrNotFound)
				}
			}

			// The corpus itself, document by document. Cross-copy comparison cannot
			// see a template damaged UNIFORMLY — a fixture missing the same document
			// in every copy looks consistent and still plans — so this compares
			// against the oracle, the only side built from `writeVariedCorpusInto`
			// directly. Deleting a document from the SETTLED fixtures alone passed
			// every test until this ran for both configurations.
			cachedCorpus := corpusDocuments(t, cachedRoot)
			freshCorpus := corpusDocuments(t, freshRoot)
			if len(freshCorpus) == 0 {
				t.Fatal("the oracle's corpus has no documents, so comparing it proves nothing")
			}
			if !reflect.DeepEqual(cachedCorpus, freshCorpus) {
				missing, extra, differing := compareCorpora(cachedCorpus, freshCorpus)
				t.Errorf("the cached copy's corpus is not the oracle's: %d missing %v, "+
					"%d unexpected %v, %d with different bytes %v",
					len(missing), missing, len(extra), extra, len(differing), differing)
			}
		})
	}
}

// The two configurations must not be interchangeable.
//
// Not because the equality tests above would miss it — the independent target
// oracle rejects a settled release head — but because this states the property
// directly and does not depend on the oracle to do it.
func TestTheTwoCachedFixturesAreDifferentReleases(t *testing.T) {
	t.Parallel()
	targetRoot, targetDraft := targetStore(t)
	settledRoot, settledDraft := settledStore(t)

	target := fixtureIdentity(t, targetRoot, targetDraft)
	settled := fixtureIdentity(t, settledRoot, settledDraft)

	if target.releaseHead == settled.releaseHead {
		t.Fatalf("both fixtures carry release %s, so the centres (0.05, 5.0) and "+
			"(2.0, 8.0) produced the same release or one cache served both",
			target.releaseHead)
	}
	// Same corpus, so these must still agree — otherwise the difference above is
	// the corpus rather than the release.
	if target.profileID != settled.profileID {
		t.Errorf("profile ids differ, %s and %s: the two fixtures no longer share "+
			"a corpus, so comparing their releases proves nothing",
			target.profileID, settled.profileID)
	}
	if target.draftBody != settled.draftBody {
		t.Error("the drafts differ; both fixtures are supposed to use executableDraft")
	}
}

// Disturbing one copy must not disturb another — including one taken AFTERWARDS,
// which is what a cache gets wrong when it re-copies from a mutated original.
//
// The disturbances have to reach the DATABASE and the corpus FILES. Removing a
// document and truncating the draft leaves SQLite untouched, so a cache that gave
// every copy a distinct directory while sharing one store file would pass: that is a
// real mutant and it did pass an earlier version of this test.
func TestDisturbingOneCopyLeavesTheOthersIntact(t *testing.T) {
	t.Parallel()
	for _, c := range fixtureConfigurations() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			victimRoot, victimDraft := c.build(t)
			bystanderRoot, bystanderDraft := c.build(t)

			if victimRoot == bystanderRoot {
				t.Fatal("both fixtures share a root, so no test could disturb its own store")
			}
			expectedHead := fixtureIdentity(t, bystanderRoot, "").releaseHead
			deleted, deletedBody := removeOneCorpusDocument(t, victimRoot)
			// An in-place OVERWRITE as well as a deletion. Removing a directory entry
			// leaves a hard link's other names intact, so an implementation that
			// hard-linked the corpus while copying the database passed the deletion
			// check alone. Overwriting reaches the shared inode.
			overwritten, originalBody := overwriteOneCorpusDocument(t, victimRoot, deleted)

			// A COMMITTED database write, and proof it landed.
			moveTheHead(t, victimRoot, 3.0, 9.0)
			if moved := fixtureIdentity(t, victimRoot, "").releaseHead; moved == expectedHead {
				t.Fatalf("the victim's release head is still %s, so the database was "+
					"not disturbed and this cannot detect a shared store", expectedHead)
			}
			if err := os.WriteFile(victimDraft, []byte("disturbed\n"), 0o644); err != nil {
				t.Fatalf("truncating the victim draft: %v", err)
			}

			// The copy taken BEFORE, checked before anything else is constructed, so
			// a later construction cannot repair shared state first.
			requireUndisturbed(t, bystanderRoot, bystanderDraft, expectedHead, c,
				map[string]string{deleted: deletedBody, overwritten: originalBody},
				"the bystander copy")

			// And a copy taken AFTER the disturbance.
			laterRoot, laterDraft := c.build(t)
			if laterRoot == victimRoot {
				t.Fatal("the later copy reused the disturbed root")
			}
			requireUndisturbed(t, laterRoot, laterDraft, expectedHead, c,
				map[string]string{deleted: deletedBody, overwritten: originalBody},
				"a copy taken after the disturbance")
		})
	}
}

// The copy check must reject a copy that is not the expected template.
//
// This is the one test here with no passing implementation yet: it names the seam.
// `checkPreparedCopy` takes the destination root and the ids the TEMPLATE recorded,
// and returns an error. The ids come from the template's own metadata, so the check
// cannot be satisfied by reading the destination and agreeing with itself.
//
// A non-empty release head is not enough, which is why the wrong-head case is here:
// delivering the settled release to a target fixture would pass an emptiness test
// while silently changing what all 101 callers exercise.
func TestTheCopyCheckRejectsACopyThatIsNotTheTemplate(t *testing.T) {
	t.Parallel()
	targetRoot, targetDraft := targetStore(t)
	settledRoot, _ := settledStore(t)

	target := fixtureIdentity(t, targetRoot, targetDraft)
	settled := fixtureIdentity(t, settledRoot, "")
	if target.releaseHead == settled.releaseHead {
		t.Fatal("the two releases are identical, so no check could distinguish them")
	}
	if target.releaseHead == "" || settled.releaseHead == "" {
		t.Fatal("a release head is empty; then an emptiness check would suffice and " +
			"the wrong-head case below would not be the point")
	}

	if err := checkPreparedCopy(targetRoot, target.profileID, target.releaseHead); err != nil {
		t.Errorf("checkPreparedCopy on a correct copy: %v", err)
	}
	if err := checkPreparedCopy(targetRoot, target.profileID, settled.releaseHead); err == nil {
		t.Error("the settled release head was accepted for a target fixture; an " +
			"emptiness check would pass this, which is why identity is required")
	}
	if err := checkPreparedCopy(variedCorpus(t), target.profileID, target.releaseHead); err == nil {
		t.Error("a store with no release at all was accepted")
	}
	if err := checkPreparedCopy(targetRoot, "not-a-profile", target.releaseHead); err == nil {
		t.Error("a mismatched profile id was accepted")
	}
}

// The template must outlive the test that happened to build it.
//
// A cache built inside a `sync.Once` that calls `t.TempDir()` — directly or through
// `variedCorpus` — hands the template a TEST-OWNED directory. Go deletes it when
// that test finishes, so the first fixture works and every later one copies from a
// directory that no longer exists. Whether this bites depends on test ordering, so
// it is pinned rather than left to luck.
//
// The subtests are SEQUENTIAL and neither calls t.Parallel(), so the first has fully
// returned — and its cleanup has run — before the second builds anything.
func TestTheTemplateOutlivesTheTestThatBuiltIt(t *testing.T) {
	for _, c := range fixtureConfigurations() {
		t.Run(c.name, func(t *testing.T) {
			var firstRoot string
			t.Run("the test that may initialize the template", func(t *testing.T) {
				root, draft := c.build(t)
				firstRoot = root
				c.requireShape(t, root, draft, "the initializing subtest")
			})
			if firstRoot == "" {
				t.Fatal("the first subtest did not build a fixture")
			}

			t.Run("a later test, after the first one's cleanup", func(t *testing.T) {
				root, draft := c.build(t)
				if root == firstRoot {
					t.Fatal("the second fixture reused the first one's root, which " +
						"the first subtest's cleanup has already removed")
				}
				c.requireShape(t, root, draft, "a fixture built after the initializer finished")
			})
		})
	}
}

// ---------------------------------------------------------------------------
// The oracle
// ---------------------------------------------------------------------------

// fixtureConfiguration is one of the two prepared fixtures, so every contract in
// this file runs against both. Covering only `targetStore` let an implementation
// that damaged the SETTLED corpus pass everything.
type fixtureConfiguration struct {
	name                           string
	authorCentre, distractorCentre float64
	want                           []workflow.Disposition
	// settled planning returns BEFORE selecting exemplars, so only the target
	// configuration persists a selection.
	selectsExemplars bool
}

func fixtureConfigurations() []fixtureConfiguration {
	return []fixtureConfiguration{
		{
			name: "targetStore", authorCentre: 0.05, distractorCentre: 5.0,
			want: []workflow.Disposition{
				workflow.DispositionTarget, workflow.DispositionTarget},
			selectsExemplars: true,
		},
		{
			name: "settledStore", authorCentre: 2.0, distractorCentre: 8.0,
			want: []workflow.Disposition{
				workflow.DispositionInRange, workflow.DispositionInRange},
			selectsExemplars: false,
		},
	}
}

func (c fixtureConfiguration) build(t *testing.T) (root, draft string) {
	t.Helper()
	if c.name == "settledStore" {
		return settledStore(t)
	}
	return targetStore(t)
}

// requireShape fails unless planning gives the dispositions this configuration
// promises, and returns the plan so callers can keep its persisted artifact ids.
func (c fixtureConfiguration) requireShape(t *testing.T, root, draft, which string) workflow.RewritePlan {
	t.Helper()
	plan := planned(t, planRequest(root, draft))
	if plan.Refusal != "" {
		t.Fatalf("%s: planning refused %q", which, plan.Refusal)
	}
	if len(plan.Segments) != len(c.want) {
		t.Fatalf("%s: the plan has %d segments, want %d", which, len(plan.Segments), len(c.want))
	}
	for i, want := range c.want {
		if got := plan.Segments[i].Disposition; got != want {
			t.Errorf("%s: segment %d disposition = %q, want %q", which, i, got, want)
		}
	}
	return plan
}

// freshStore repeats the ORIGINAL construction sequence for one configuration, and
// deliberately starts from `writeVariedCorpusInto` and `workflow.Default().Index`
// rather than from `variedCorpus`.
//
// Bypassing `installRelease` is not enough: a cache installed one level down, inside
// `variedCorpus`, would put the oracle back through the thing it exists to check.
// What is duplicated here is the ORCHESTRATION — make a directory, write the corpus,
// index two registers — not the corpus generation, which stays shared so that both
// sides really are the same corpus.
//
// It also runs the original setup PLAN and returns it, because `requireDispositions`
// was never only a check: it persists the draft snapshot, and for a target fixture
// the exemplar selection too.
func freshStore(t *testing.T, c fixtureConfiguration) (root, draft string, plan workflow.RewritePlan) {
	t.Helper()
	root = t.TempDir()
	if err := writeVariedCorpusInto(root, 60); err != nil {
		t.Fatalf("writing the varied corpus: %v", err)
	}
	for _, register := range []string{"essays", "letters"} {
		if _, err := workflow.Default().Index(ctx(), workflow.IndexRequest{
			CorpusRoot: root, Register: register,
		}); err != nil {
			t.Fatalf("Index(%s): %v", register, err)
		}
	}

	opened := openStore(t, defaultStorePath(root))
	bundle, err := opened.LoadProfileBundle(ctx(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	if bundle.Reference.ID == "" {
		t.Fatal("the fresh corpus indexed no reference")
	}
	release := evaltest.ReleaseAround(t, bundle.Profile.ID, bundle.Reference.ID,
		c.authorCentre, c.distractorCentre)
	if !release.Shippable {
		t.Fatalf("the crafted release is not shippable (%s)", release.Reason)
	}
	if err := opened.PutRelease(ctx(), release, "", store.AdvanceHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}
	draft = writeDraft(t, root, executableDraft())

	return root, draft, c.requireShape(t, root, draft, "the freshly built "+c.name+" oracle")
}

// requirePersistedPlan checks the cached copy carries the artifacts the oracle's
// setup plan left behind, looked up BY THE ORACLE'S IDS so no second plan is needed.
func requirePersistedPlan(t *testing.T, cachedRoot, freshRoot string, plan workflow.RewritePlan, selectsExemplars bool) {
	t.Helper()
	if plan.DraftSnapshotID == "" {
		t.Fatal("the oracle's plan recorded no draft snapshot id, so there is " +
			"nothing to look for in the cached copy")
	}
	cached := openStore(t, defaultStorePath(cachedRoot))
	fresh := openStore(t, defaultStorePath(freshRoot))

	cachedSnapshot, err := cached.Snapshot(ctx(), plan.DraftSnapshotID)
	if err != nil {
		t.Fatalf("the cached copy does not carry draft snapshot %s: %v — the setup "+
			"plan's persisted state did not survive", plan.DraftSnapshotID, err)
	}
	freshSnapshot, err := fresh.Snapshot(ctx(), plan.DraftSnapshotID)
	if err != nil {
		t.Fatalf("loading the draft snapshot from the oracle's own store: %v", err)
	}
	if len(freshSnapshot.Documents) == 0 {
		t.Fatal("the oracle's draft snapshot holds no documents, so comparing the " +
			"graph proves nothing")
	}
	if cachedSnapshot.PolicyDigest != freshSnapshot.PolicyDigest {
		t.Errorf("draft snapshot policy digest: cached %s, fresh %s",
			cachedSnapshot.PolicyDigest, freshSnapshot.PolicyDigest)
	}
	if !reflect.DeepEqual(cachedSnapshot.Documents, freshSnapshot.Documents) {
		t.Errorf("the cached draft snapshot's documents differ from the oracle's:\n"+
			" cached %+v\n fresh  %+v", cachedSnapshot.Documents, freshSnapshot.Documents)
	}

	if !selectsExemplars {
		if plan.ExemplarSelectionID != "" {
			t.Fatalf("this configuration is declared not to select exemplars, and "+
				"the oracle's plan recorded selection %s", plan.ExemplarSelectionID)
		}
		return
	}
	if plan.ExemplarSelectionID == "" {
		t.Fatal("this configuration is declared to select exemplars and the " +
			"oracle's plan recorded none")
	}
	cachedSelection, err := cached.LoadExemplarSelection(ctx(), plan.ExemplarSelectionID)
	if err != nil {
		t.Fatalf("the cached copy does not carry exemplar selection %s: %v",
			plan.ExemplarSelectionID, err)
	}
	freshSelection, err := fresh.LoadExemplarSelection(ctx(), plan.ExemplarSelectionID)
	if err != nil {
		t.Fatalf("loading the selection from the oracle's own store: %v", err)
	}
	if len(freshSelection.Members) == 0 {
		t.Fatal("the oracle's selection has no members, so comparing them proves nothing")
	}
	if cachedSelection.CertificateID != freshSelection.CertificateID {
		t.Errorf("certificate id: cached %s, fresh %s",
			cachedSelection.CertificateID, freshSelection.CertificateID)
	}
	if !reflect.DeepEqual(cachedSelection.Members, freshSelection.Members) {
		t.Errorf("members differ:\n cached %v\n fresh  %v",
			cachedSelection.Members, freshSelection.Members)
	}
}

// fixtureState is the identity a copy must preserve, read without planning.
type fixtureState struct {
	profileID   string
	referenceID string
	releaseHead string
	draftBody   string
	// Both registers. The original construction and the oracle index `essays` AND
	// `letters`, and `anotherProfileID` consumes the second — so reading only
	// `essays` let an implementation that dropped the `letters` head pass.
	profileHeads map[string]string
}

// fixtureIdentity reads that state. An empty draft path skips the draft, for the
// cases that only compare stores.
func fixtureIdentity(t *testing.T, root, draft string) fixtureState {
	t.Helper()
	opened := openStore(t, defaultStorePath(root))
	bundle, err := opened.LoadProfileBundle(ctx(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	head, err := opened.ReleaseHead(ctx(), bundle.Profile.ID)
	if err != nil {
		t.Fatalf("ReleaseHead: %v", err)
	}
	heads, err := opened.ProfileHeads(ctx())
	if err != nil {
		t.Fatalf("ProfileHeads: %v", err)
	}
	// Both registers must LOAD, not merely appear in the head map.
	for _, register := range []string{"essays", "letters"} {
		if _, err := opened.LoadProfileBundle(ctx(), register); err != nil {
			t.Fatalf("LoadProfileBundle(%s): %v", register, err)
		}
	}
	state := fixtureState{
		profileID:    bundle.Profile.ID,
		referenceID:  bundle.Reference.ID,
		releaseHead:  head,
		profileHeads: heads,
	}
	if draft != "" {
		body, err := os.ReadFile(draft)
		if err != nil {
			t.Fatalf("reading the draft: %v", err)
		}
		state.draftBody = string(body)
	}
	return state
}

// requireUndisturbed checks a copy kept its release head, the exact bytes of the
// corpus documents the victim's disturbance touched, and its planning answer.
//
// Planning alone is not enough: an implementation that deleted a corpus document
// from every fixture still planned successfully.
func requireUndisturbed(t *testing.T, root, draft, wantHead string, c fixtureConfiguration, documents map[string]string, which string) {
	t.Helper()
	if head := fixtureIdentity(t, root, "").releaseHead; head != wantHead {
		t.Errorf("%s: release head = %s, want %s — the database is shared",
			which, head, wantHead)
	}
	for document, body := range documents {
		got, err := os.ReadFile(filepath.Join(root, document))
		if err != nil {
			t.Errorf("%s: corpus document %s is gone: %v", which, document, err)
			continue
		}
		if string(got) != body {
			t.Errorf("%s: corpus document %s holds different bytes — the corpus "+
				"files are shared, which a deletion alone cannot show because "+
				"unlinking one name leaves a hard link's others intact",
				which, document)
		}
	}
	c.requireShape(t, root, draft, which)
}

// overwriteOneCorpusDocument rewrites one corpus document IN PLACE and returns its
// relative path with the bytes it held. `avoid` is the document the caller already
// deleted, so the two disturbances land on different files.
//
// Overwriting is what reaches a shared inode: an implementation that hard-linked the
// corpus while copying the database independently passed every deletion check.
func overwriteOneCorpusDocument(t *testing.T, root, avoid string) (string, string) {
	t.Helper()
	for document, body := range corpusDocuments(t, root) {
		if document == avoid {
			continue
		}
		full := filepath.Join(root, document)
		if err := os.WriteFile(full, []byte("overwritten by the victim\n"), 0o644); err != nil {
			t.Fatalf("overwriting %s: %v", document, err)
		}
		// Proof the write landed, so a silently failing overwrite cannot make the
		// isolation assertions vacuous.
		after, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("re-reading %s: %v", document, err)
		}
		if string(after) == body {
			t.Fatalf("%s still holds its original bytes after the overwrite", document)
		}
		return document, body
	}
	t.Fatalf("the corpus has no document other than %s to overwrite", avoid)
	return "", ""
}

// removeOneCorpusDocument deletes a single indexed corpus file from under root and
// returns its path relative to root with the bytes it held, so callers can assert
// those bytes survive elsewhere. It fails rather than returning quietly if there is
// nothing to remove, since a no-op disturbance makes the isolation checks vacuous.
func removeOneCorpusDocument(t *testing.T, root string) (string, string) {
	t.Helper()
	var document string
	var body []byte
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if document != "" || entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		// The draft lives under root too; this must disturb the CORPUS.
		if filepath.Base(path) == "draft.md" {
			return nil
		}
		if body, err = os.ReadFile(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		document = relative
		return nil
	})
	if err != nil {
		t.Fatalf("walking the corpus: %v", err)
	}
	if document == "" {
		t.Fatal("the corpus has no .md document to remove, so this disturbance " +
			"would be a no-op and the isolation checks vacuous")
	}
	if len(body) == 0 {
		t.Fatalf("corpus document %s was empty, so comparing its bytes elsewhere "+
			"proves nothing", document)
	}
	return document, string(body)
}

// corpusDocuments maps each corpus document's path, relative to root, to its bytes.
// The draft is excluded: it is not corpus, and tests rewrite it.
func corpusDocuments(t *testing.T, root string) map[string]string {
	t.Helper()
	documents := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" || filepath.Base(path) == "draft.md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		documents[relative] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the corpus under %s: %v", root, err)
	}
	return documents
}

// compareCorpora names the difference, so a failure says which documents rather
// than dumping two corpora.
func compareCorpora(got, want map[string]string) (missing, extra, differing []string) {
	for path, body := range want {
		switch other, ok := got[path]; {
		case !ok:
			missing = append(missing, path)
		case other != body:
			differing = append(differing, path)
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			extra = append(extra, path)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(differing)
	return missing, extra, differing
}
