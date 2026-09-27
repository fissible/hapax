package store_test

import (
	"errors"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// #115 adds `not-spliceable` to the rejection vocabulary, so `rewrite_attempt` is
// rebuilt a fifth time — SQLite cannot alter a CHECK in place.
//
// Data preservation across that rebuild is already covered: #107's migration test
// truncates the list to before its own rebuild and migrates all the way forward,
// so every later rebuild's copy runs under it. What is NOT covered without this
// file is whether the new code can be stored at all, and whether the CHECK and
// the Go vocabulary still agree — two hand-maintained copies of one enum, now in
// five copy-pasted versions (#120).

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

// A splice refusal carries no script evidence.
//
// The scripts are measured by a different gate, and this one refuses for a reason
// that has nothing to do with them — so a record naming scripts under this code
// would be the audit trail inventing evidence. Both columns must be empty, and
// #107's accepted-implies-none clause does not cover this because the attempt is
// rejected, not accepted.
func TestASpliceRefusalNamesNoScripts(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	attempt := attemptFixture(prof.ID, nodeID)
	attempt.Preserved, attempt.PreserveIdentifiers = true, nil
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotSpliceable
	attempt.IntroducedScripts = scriptsOf(t, "Han")

	if err := s.PutRewriteAttempt(ctx(), attempt); err == nil {
		t.Error("a splice refusal naming an introduced script was accepted")
	} else if !errors.Is(err, store.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}
