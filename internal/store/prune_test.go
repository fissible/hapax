package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

// pruneFixture is the smallest graph that can tell the declared roots apart.
//
//	snapshot kept    <- profile kept       (reference, threshold, selection, head "essays")
//	snapshot audited <- profile audited    (reference, eval_result on it)
//	snapshot drafted <- no profile; the node a rewrite_attempt points at
//	snapshot orphan  <- profile orphan     (reference, threshold, selection, head "orphan")
//
// The rewrite attempt belongs to the KEPT profile but names a node in the
// drafted snapshot, which is legal: a rewrite operates on draft nodes that need
// not belong to the profile's own corpus.
type pruneFixture struct {
	KeptProfile, AuditedProfile, OrphanProfile store.Profile
	Kept, Audited, Drafted, Orphan             store.SnapshotWrite
	EvalResult                                 store.EvalResult
	Attempt                                    store.RewriteAttempt
}

func newPruneFixture(t *testing.T, s *store.Store) pruneFixture {
	t.Helper()
	root := t.TempDir()
	var f pruneFixture

	snapshotOf := func(name, body string, spans ...text.Span) store.SnapshotWrite {
		write := snapshotWrite(corpusDocument(t, root, name, body, spans...))
		mustPutSnapshot(t, s, write)
		return withDerivedIDs(write)
	}
	// The kept snapshot carries a feature vector, so a prune that quietly
	// dropped the children of what it kept would be visible.
	keptWrite := snapshotWrite(corpusDocument(t, root, "kept/a.md", bodyA,
		text.Span{Offset: 0, Length: 48}, text.Span{Offset: 50, Length: 60}))
	keptWrite.Documents[0].Nodes[0].Vector = &features.Vector{
		SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: vectorValues(),
	}
	mustPutSnapshot(t, s, keptWrite)
	f.Kept = withDerivedIDs(keptWrite)
	f.Audited = snapshotOf("audited/a.md", bodyB, text.Span{Offset: 0, Length: 20})
	f.Drafted = snapshotOf("drafted/a.md", bodyB, text.Span{Offset: 2, Length: 20})
	f.Orphan = snapshotOf("orphan/a.md", bodyA, text.Span{Offset: 0, Length: 48}, text.Span{Offset: 50, Length: 60})

	profileOn := func(snapshot store.SnapshotWrite, register, salt string) store.Profile {
		prof := profileFixture(snapshot.ID)
		prof.ID, prof.Register = fakeID("profile", salt), register
		if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err != nil {
			t.Fatalf("PutProfile %s: %v", salt, err)
		}
		return prof
	}
	f.KeptProfile = profileOn(f.Kept, "essays", "kept")
	f.AuditedProfile = profileOn(f.Audited, "audited", "audited")
	f.OrphanProfile = profileOn(f.Orphan, "orphan", "orphan")

	referenceOn := func(prof store.Profile) store.Reference {
		ref := referenceFixture(prof.ID)
		mustPutReference(t, s, ref)
		return ref
	}
	keptRef := referenceOn(f.KeptProfile)
	auditedRef := referenceOn(f.AuditedProfile)
	orphanRef := referenceOn(f.OrphanProfile)

	for prof, ref := range map[string]store.Reference{
		f.KeptProfile.ID: keptRef, f.OrphanProfile.ID: orphanRef,
	} {
		if err := s.PutThreshold(ctx(), thresholdFixture(prof, ref.ID)); err != nil {
			t.Fatalf("PutThreshold: %v", err)
		}
	}
	for prof, snapshot := range map[string]store.SnapshotWrite{
		f.KeptProfile.ID: f.Kept, f.OrphanProfile.ID: f.Orphan,
	} {
		selection := selectionFixture(prof, snapshot.Documents[0].Nodes[0].ID)
		if err := s.PutExemplarSelection(ctx(), selection); err != nil {
			t.Fatalf("PutExemplarSelection: %v", err)
		}
	}

	f.EvalResult = evalResultFixture(f.AuditedProfile.ID, auditedRef.ID)
	if err := s.PutEvalResult(ctx(), f.EvalResult); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}
	f.Attempt = attemptFixture(f.KeptProfile.ID, f.Drafted.Documents[0].Nodes[0].ID)
	if err := s.PutRewriteAttempt(ctx(), f.Attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	return f
}

func mustPrune(t *testing.T, s *store.Store, keep ...string) store.Pruned {
	t.Helper()
	pruned, err := s.Prune(ctx(), keep)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	return pruned
}

// ---------------------------------------------------------------------------

// Prune removes what is unreachable from the roots it was given, and nothing
// else. The orphan profile is the only one no root reaches.
func TestPruneRemovesOnlyWhatNoRootReaches(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	mustPrune(t, s, f.KeptProfile.ID)

	for _, kept := range []struct {
		name string
		load func() error
	}{
		{"the profile it was given", func() error { _, err := s.LoadProfile(ctx(), f.KeptProfile.ID); return err }},
		{"the profile an eval result names", func() error {
			_, err := s.LoadProfile(ctx(), f.AuditedProfile.ID)
			return err
		}},
		{"the snapshot the kept profile was built on", func() error {
			_, err := s.Snapshot(ctx(), f.Kept.ID)
			return err
		}},
		{"the snapshot an eval result reaches through its profile", func() error {
			_, err := s.Snapshot(ctx(), f.Audited.ID)
			return err
		}},
		{"the eval result", func() error { _, err := s.LoadEvalResult(ctx(), f.EvalResult.ID); return err }},
		{"the rewrite attempt", func() error {
			_, err := s.LoadRewriteAttempt(ctx(), f.Attempt.InvocationID, f.Attempt.Index)
			return err
		}},
	} {
		if err := kept.load(); err != nil {
			t.Errorf("%s was removed: %v", kept.name, err)
		}
	}

	if _, err := s.LoadProfile(ctx(), f.OrphanProfile.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the orphan profile survived: %v", err)
	}
	if _, err := s.Snapshot(ctx(), f.Orphan.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the orphan snapshot survived: %v", err)
	}
}

// The edge that closes the audit-retention hole. rewrite_attempt.node_id
// cascades on delete, and the drafted snapshot is reachable from NO profile —
// so without node -> document -> snapshot, Prune would delete that snapshot and
// the cascade would take the attempt with it.
func TestPruneKeepsTheSnapshotAnAuditRecordPointsInto(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	mustPrune(t, s, f.KeptProfile.ID)

	if _, err := s.Snapshot(ctx(), f.Drafted.ID); err != nil {
		t.Errorf("the drafted snapshot was removed: %v", err)
	}
	if _, err := s.LoadRewriteAttempt(ctx(), f.Attempt.InvocationID, f.Attempt.Index); err != nil {
		t.Errorf("the audit record went with it: %v", err)
	}
	span, err := s.Span(ctx(), f.Attempt.NodeID)
	if err != nil {
		t.Fatalf("the audited node was removed: %v", err)
	}
	if span.SnapshotID != f.Drafted.ID {
		t.Errorf("span snapshot = %q, want %q", span.SnapshotID, f.Drafted.ID)
	}
}

// Prune given nothing is not "delete everything": every eval result and every
// rewrite attempt is a root in its own right.
func TestPruneGivenNoProfilesStillKeepsEveryAuditedOne(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	mustPrune(t, s)

	for _, id := range []string{f.KeptProfile.ID, f.AuditedProfile.ID} {
		if _, err := s.LoadProfile(ctx(), id); err != nil {
			t.Errorf("profile %q was removed despite an audit root: %v", id, err)
		}
	}
	if _, err := s.LoadProfile(ctx(), f.OrphanProfile.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the orphan profile survived: %v", err)
	}
}

// Audit records are unconditional roots, so no call can remove one.
func TestPruneNeverRemovesAnAuditRecord(t *testing.T) {
	s := newStore(t)
	newPruneFixture(t, s)
	raw := openRaw(t, s)
	evals, attempts := rowsIn(t, raw, "eval_result"), rowsIn(t, raw, "rewrite_attempt")

	mustPrune(t, s)

	if got := rowsIn(t, raw, "eval_result"); got != evals {
		t.Errorf("eval_result rows = %d, want %d", got, evals)
	}
	if got := rowsIn(t, raw, "rewrite_attempt"); got != attempts {
		t.Errorf("rewrite_attempt rows = %d, want %d", got, attempts)
	}
}

// profile_head is deliberately not a root: a head nobody passed is a head cli
// chose not to keep, and it goes with its profile.
func TestAHeadGoesWithItsProfileAndOnlyWithIt(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	mustPrune(t, s, f.KeptProfile.ID)

	if head, err := s.ProfileHead(ctx(), f.KeptProfile.Register); err != nil || head != f.KeptProfile.ID {
		t.Errorf("kept head = %q, %v", head, err)
	}
	if _, err := s.ProfileHead(ctx(), f.OrphanProfile.Register); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the orphan head survived: %v", err)
	}
}

// The counts are what cli reports, so they must describe the removal rather
// than whatever RowsAffected happened to see through the cascades.
func TestPruneCountsTheParentRowsItRemoved(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	pruned := mustPrune(t, s, f.KeptProfile.ID)

	want := store.Pruned{
		Snapshots: 1, Documents: 1, Nodes: 2,
		Profiles: 1, References: 1, Thresholds: 1, Selections: 1, Heads: 1,
	}
	if pruned != want {
		t.Errorf("pruned =\n%+v\nwant\n%+v", pruned, want)
	}
}

func TestPruningNothingReportsNothing(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	mustPrune(t, s, f.KeptProfile.ID)

	if pruned := mustPrune(t, s, f.KeptProfile.ID); pruned != (store.Pruned{}) {
		t.Errorf("a second prune removed %+v", pruned)
	}
}

// A document marked unavailable keeps its whole graph — that is the point of
// marking rather than deleting, and Prune must not quietly finish the job.
func TestPruneKeepsAnUnavailableDocumentsGraph(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)
	prof := profileFixture(snapshot.ID)
	if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	removeFile(t, root, "essays/a.md")
	if _, err := s.Rehydrate(ctx(), root, []string{snapshot.Documents[0].Nodes[0].ID}); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	if pruned := mustPrune(t, s, prof.ID); pruned != (store.Pruned{}) {
		t.Errorf("prune removed %+v from a snapshot whose document is merely unavailable", pruned)
	}
	unavailable, err := s.Unavailable(ctx(), snapshot.ID)
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, marked := unavailable["essays/a.md"]; !marked {
		t.Error("the mark did not survive the prune")
	}
}

// Asking to keep something that is not there is a caller bug, and keeping
// nothing instead would be the destructive reading of it.
func TestPruneRefusesARootItCannotFind(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	absent := identity.HashBytes([]byte("no such profile"))

	if _, err := s.Prune(ctx(), []string{f.KeptProfile.ID, absent}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if _, err := s.LoadProfile(ctx(), f.OrphanProfile.ID); err != nil {
		t.Errorf("a refused prune still removed something: %v", err)
	}
}

func TestPruneRefusesARootThatIsNotAnIdentity(t *testing.T) {
	s := newStore(t)
	newPruneFixture(t, s)
	if _, err := s.Prune(ctx(), []string{"not-a-hash"}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

// Marking and deletion commit together, so a prune that cannot finish removes
// nothing at all rather than leaving a half-pruned graph.
func TestAPruneThatCannotFinishRemovesNothing(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Prune(cancelled, []string{f.KeptProfile.ID}); err == nil {
		t.Fatal("a cancelled prune reported success")
	}
	if _, err := s.LoadProfile(ctx(), f.OrphanProfile.ID); err != nil {
		t.Errorf("the orphan profile was removed anyway: %v", err)
	}
	if _, err := s.Snapshot(ctx(), f.Orphan.ID); err != nil {
		t.Errorf("the orphan snapshot was removed anyway: %v", err)
	}
}

// Keeping a profile means keeping its whole graph, not merely its own row. An
// implementation that retained profiles, snapshots and audit records while
// deleting their children would pass every test above.
func TestPruneKeepsTheChildrenOfWhatItKeeps(t *testing.T) {
	s := newStore(t)
	f := newPruneFixture(t, s)
	before := loadKeptGraph(t, s, f)

	mustPrune(t, s, f.KeptProfile.ID)

	if after := loadKeptGraph(t, s, f); !reflect.DeepEqual(after, before) {
		t.Errorf("the kept graph changed:\n%+v\nwant\n%+v", after, before)
	}
	raw := openRaw(t, s)
	for table, want := range map[string]int{
		"profile_stat":    len(f.KeptProfile.Stats) * 2, // the kept and the audited profile
		"reference_value": rowsIn(t, raw, "reference_value"),
	} {
		if got := rowsIn(t, raw, table); got < want {
			t.Errorf("%s has %d rows, want at least %d", table, got, want)
		}
	}
	if rowsIn(t, raw, "exemplar_member") == 0 {
		t.Error("the kept selection lost its members")
	}
	if rowsIn(t, raw, "feature_vector") == 0 || rowsIn(t, raw, "feature_value") == 0 {
		t.Error("the kept snapshot lost its feature vectors")
	}
	if rowsIn(t, raw, "rewrite_attempt_identifier") == 0 {
		t.Error("the retained audit record lost its identifiers")
	}
}

// loadKeptGraph is everything that must be byte-identical across a prune that
// was told to keep it.
func loadKeptGraph(t *testing.T, s *store.Store, f pruneFixture) []any {
	t.Helper()
	prof, err := s.LoadProfile(ctx(), f.KeptProfile.ID)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	ref, err := s.LoadReference(ctx(), referenceFixture(f.KeptProfile.ID).ID)
	if err != nil {
		t.Fatalf("LoadReference: %v", err)
	}
	threshold, err := s.LoadThreshold(ctx(), thresholdFixture(f.KeptProfile.ID, ref.ID).ID)
	if err != nil {
		t.Fatalf("LoadThreshold: %v", err)
	}
	selection, err := s.LoadExemplarSelection(ctx(), selectionFixture(f.KeptProfile.ID).ID)
	if err != nil {
		t.Fatalf("LoadExemplarSelection: %v", err)
	}
	snapshot, err := s.Snapshot(ctx(), f.Kept.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	attempt, err := s.LoadRewriteAttempt(ctx(), f.Attempt.InvocationID, f.Attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	return []any{prof, ref, threshold, selection, snapshot, attempt}
}
