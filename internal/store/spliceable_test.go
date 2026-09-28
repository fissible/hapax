package store_test

import (
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
)

// #115 adds `not-spliceable` to the rejection vocabulary, so `rewrite_attempt` is
// rebuilt a fifth time — SQLite cannot alter a CHECK in place.
//
// Two things are already covered elsewhere, and an earlier version of this
// comment claimed the opposite about both.
//
// Data preservation across the rebuild is NOT covered by #107's migration test —
// that seeds at the version where `rewrite_attempt_overgrown_script` does not yet
// exist — so `splice_migration_internal_test.go` in this package supplies it. This
// comment used to assert the coverage that sibling file opens by denying.
//
// Agreement between the CHECK and the Go vocabulary IS covered:
// `declaredVocabularies()` derives the rejection set from
// `rewrite.RejectionCodes()`, so `TestEveryDeclaredEnumValueIsAcceptedByTheSchema`
// fails the moment a code is declared in Go and forgotten in the migration — it is
// in this slice's own red list for that reason. I filed #120 claiming that check
// did not exist and closed it as invalid; the sentence citing #120 as an open
// problem was left behind.
//
// So one thing is left for THIS file: that the new code survives the Go write
// path, PutRewriteAttempt through LoadRewriteAttempt.
//
// An earlier draft also required a splice refusal to carry no script evidence.
// That is false — every gate is consulted whatever the first one says, so the
// scripts are measured and belong in the record — and singling this code out
// would have been arbitrary anyway, since `tells-worse` and `not-improved` can
// carry them too.

// The new code round-trips, so the CHECK admits what RejectionCodes() declares.
func TestASpliceRefusalRoundTrips(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.Preserved, attempt.PreserveIdentifiers = true, nil
	attempt.IntroducedScripts, attempt.OvergrownScripts = nil, nil
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotSpliceable

	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if got.Rejection != rewrite.RejectionNotSpliceable {
		t.Errorf("rejection = %q, want %q", got.Rejection, rewrite.RejectionNotSpliceable)
	}
}
