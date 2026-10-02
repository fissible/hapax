package store_test

// #143. The expansion gate's evidence persists: the original and candidate
// lexical counts and the bound that was applied.
//
// # Contract
//
// A refusal discards the prose, so the counts are the only durable trace of how
// long the candidate was. The bound is stored PER ROW rather than read from the
// constant at load time, because a later change to `ExpansionCeiling` must not
// silently restate what an old decision was measured against — which is also why
// the fixtures below use a bound that is NOT today's constant.
//
// The database-level tests go through raw SQL on purpose. Asserting that
// `PutRewriteAttempt` returns an error proves only that something refused;
// application validation would satisfy it while the schema admitted the row. The
// claim here is that the DATABASE refuses, so the writes bypass the API.

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// openStoreAt is newStore with the path kept, so a test can reach the same
// database through raw SQL afterwards.
func openStoreAt(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hapax.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The new rejection code round-trips, which it cannot do until the column's
// vocabulary admits it, and the bound comes back off the COLUMN rather than from
// the constant.
func TestTheExpandedRejectionIsStoredAndLoaded(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionExpanded
	attempt.Splice = rewrite.SpliceNotRecorded
	// A HISTORICAL bound, deliberately not today's constant: 20 -> 26 exceeds
	// 1.25 and would be inside 1.5, so a loader returning `ExpansionCeiling`
	// rather than reading the column cannot pass, and neither can a constraint
	// that checks against the constant.
	attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens = 20, 26
	attempt.ExpansionCeiling = 1.25

	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if got.Rejection != rewrite.RejectionExpanded {
		t.Errorf("rejection stored %q and loaded %q",
			rewrite.RejectionExpanded, got.Rejection)
	}
	if got.OriginalLexicalTokens != 20 || got.CandidateLexicalTokens != 26 {
		t.Errorf("counts loaded %d -> %d, want 20 -> 26",
			got.OriginalLexicalTokens, got.CandidateLexicalTokens)
	}
	if got.ExpansionCeiling != 1.25 {
		t.Errorf("ceiling loaded %v, want the STORED 1.25 and not the constant %v",
			got.ExpansionCeiling, rewrite.ExpansionCeiling)
	}
}

// The counts and the bound persist on EVERY attempt, not only an expansion
// refusal, because they are measurements rather than an explanation of one
// outcome. A column written only on the refusing path is a column no other path
// can be audited against.
func TestTheCountsAndBoundPersistOnAnAcceptedAttempt(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.Accepted, attempt.Rejection = true, rewrite.RejectionNone
	attempt.Preserved, attempt.PreserveIdentifiers = true, nil
	attempt.Splice = rewrite.SpliceIntact
	attempt.CurrentDistance, attempt.CandidateDistance = 1.4, 1.2
	attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens = 40, 44
	attempt.ExpansionCeiling = 1.25

	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if got.OriginalLexicalTokens != 40 || got.CandidateLexicalTokens != 44 {
		t.Errorf("counts loaded %d -> %d, want 40 -> 44",
			got.OriginalLexicalTokens, got.CandidateLexicalTokens)
	}
	if got.ExpansionCeiling != 1.25 {
		t.Errorf("ceiling loaded %v, want the stored 1.25", got.ExpansionCeiling)
	}
}

// The DATABASE refuses a count that cannot be a count. Written through raw SQL,
// because an API-level error would not establish the schema's own guarantee.
func TestTheDatabaseRefusesANegativeLexicalCount(t *testing.T) {
	for _, c := range []struct {
		name                string
		original, candidate int
	}{
		{"negative original", -1, 10},
		{"negative candidate", 10, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, path := openStoreAt(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotImproved
			attempt.Splice = rewrite.SpliceNotIntact
			attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens = 10, 10
			attempt.ExpansionCeiling = 1.25
			if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
				t.Fatalf("seeding a valid row: %v", err)
			}

			db := rawDB(t, path)
			// A VALID update on the same two columns first. It must SUCCEED,
			// which is what establishes that the columns exist and the statement
			// is well formed — otherwise the refusal below could be a missing
			// column rather than the constraint, and the test would pass
			// vacuously against an unimplemented schema.
			if _, err := db.Exec(
				`UPDATE rewrite_attempt SET original_lexical_tokens=?, candidate_lexical_tokens=?`,
				11, 12,
			); err != nil {
				t.Fatalf("a valid update of the two count columns failed: %v", err)
			}

			if _, err := db.Exec(
				`UPDATE rewrite_attempt SET original_lexical_tokens=?, candidate_lexical_tokens=?`,
				c.original, c.candidate,
			); err == nil {
				t.Errorf("the database accepted counts of %d/%d",
					c.original, c.candidate)
			}
		})
	}
}

// An `expanded` refusal must be consistent with its own evidence: the candidate
// has to exceed the bound THE ROW RECORDS. A constraint written against today's
// constant would make historical rows at another bound look contradictory, so the
// implication is checked against `expansion_ceiling` and not against 1.5.
//
// Only that direction. The reverse would be wrong: an over-bound candidate can be
// refused for preservation instead, and such a row is correct.
func TestTheDatabaseRefusesAnExpandedRowWhoseCountsAreWithinItsOwnBound(t *testing.T) {
	for _, c := range []struct {
		name      string
		candidate int
	}{
		{"inside the bound", 25},
		{"exactly on the bound", 30},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, path := openStoreAt(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.Accepted, attempt.Rejection = false, rewrite.RejectionExpanded
			attempt.Splice = rewrite.SpliceNotRecorded
			// 20 -> 31 at 1.5 is a VALID expanded row: it really does exceed.
			attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens = 20, 31
			attempt.ExpansionCeiling = 1.5
			if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
				t.Fatalf("seeding a valid expanded row: %v", err)
			}

			db := rawDB(t, path)
			// A valid move first — still above the bound — so the refusal below
			// cannot be a missing column or a malformed statement.
			if _, err := db.Exec(
				`UPDATE rewrite_attempt SET candidate_lexical_tokens=?`, 32,
			); err != nil {
				t.Fatalf("a valid update to 32 tokens failed: %v", err)
			}
			if _, err := db.Exec(
				`UPDATE rewrite_attempt SET candidate_lexical_tokens=?`, 31,
			); err != nil {
				t.Fatalf("restoring 31 tokens failed: %v", err)
			}

			// Now move ONLY the candidate count to a value inside the row's own
			// bound. Nothing else changes, so a failure can only be the
			// contradiction.
			if _, err := db.Exec(
				`UPDATE rewrite_attempt SET candidate_lexical_tokens=?`, c.candidate,
			); err == nil {
				t.Errorf("the database accepted an `expanded` row recording 20 -> %d "+
					"against its own ceiling of 1.5", c.candidate)
			}

			// And the seeded row is intact, so the rejected update changed nothing.
			got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
			if err != nil {
				t.Fatalf("LoadRewriteAttempt after the refused update: %v", err)
			}
			if got.CandidateLexicalTokens != 31 {
				t.Errorf("the row now records a candidate of %d; the refused update "+
					"was partially applied", got.CandidateLexicalTokens)
			}
		})
	}
}

// The implication is ONE-WAY, and this is the case that keeps it so.
//
// An over-bound candidate can be refused for preservation instead, and such a row
// is correct and must be storable. Without this, a constraint enforcing
// equivalence between `expanded` and exceeding the bound would pass every other
// test here while silently making a whole class of honest rows unwritable.
func TestAnOverBoundRowRefusedForPreservationIsStorable(t *testing.T) {
	s, path := openStoreAt(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotPreserved
	attempt.Splice = rewrite.SpliceNotRecorded
	// 20 -> 31 EXCEEDS the recorded 1.5, and the rejection is not `expanded`.
	attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens = 20, 31
	attempt.ExpansionCeiling = 1.5

	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("an over-bound row refused for preservation was rejected: %v — the "+
			"implication must not be enforced in reverse", err)
	}
	got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if got.Rejection != rewrite.RejectionNotPreserved {
		t.Errorf("rejection loaded %q, want %q", got.Rejection, rewrite.RejectionNotPreserved)
	}
	if got.CandidateLexicalTokens != 31 {
		t.Errorf("candidate count loaded %d, want 31", got.CandidateLexicalTokens)
	}

	// And raw SQL agrees: the schema itself admits the row, so the acceptance
	// above is not the API declining to validate.
	if _, err := rawDB(t, path).Exec(
		`UPDATE rewrite_attempt SET candidate_lexical_tokens=?`, 40,
	); err != nil {
		t.Errorf("the schema refused to raise an over-bound non-expanded row to 40 "+
			"tokens: %v", err)
	}
}

// The evidence is IMMUTABLE under replay, like every other field of an attempt.
//
// `sameAttempt` decides whether a re-recorded attempt is the same one. Removing
// any ONE of the three new fields from that comparison passed the whole store
// suite: a second write changing only that field returned success and quietly
// kept the old evidence. So each is changed on its own here.
func TestTheExpansionEvidenceIsImmutableUnderReplay(t *testing.T) {
	seed := func(t *testing.T) (*store.Store, store.RewriteAttempt) {
		t.Helper()
		s := newStore(t)
		snapshot, prof := seededProfile(t, s)
		attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
		attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotImproved
		attempt.Splice = rewrite.SpliceNotIntact
		attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens = 20, 26
		attempt.ExpansionCeiling = 1.25
		if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
			t.Fatalf("seeding: %v", err)
		}
		return s, attempt
	}

	t.Run("an identical replay succeeds", func(t *testing.T) {
		s, attempt := seed(t)
		if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
			t.Errorf("replaying the identical attempt: %v", err)
		}
	})

	for _, c := range []struct {
		name   string
		change func(*store.RewriteAttempt)
	}{
		{"the original count", func(a *store.RewriteAttempt) { a.OriginalLexicalTokens = 21 }},
		{"the candidate count", func(a *store.RewriteAttempt) { a.CandidateLexicalTokens = 27 }},
		{"the bound", func(a *store.RewriteAttempt) { a.ExpansionCeiling = 1.5 }},
	} {
		t.Run("changing "+c.name+" conflicts", func(t *testing.T) {
			s, attempt := seed(t)
			changed := attempt
			c.change(&changed)

			if err := s.PutRewriteAttempt(ctx(), changed); !errors.Is(err, store.ErrConflict) {
				t.Errorf("re-recording with %s changed gave %v, want %v",
					c.name, err, store.ErrConflict)
			}
			// And the stored evidence is the ORIGINAL, so a conflict that wrote
			// anyway would still be caught.
			got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
			if err != nil {
				t.Fatalf("LoadRewriteAttempt: %v", err)
			}
			if got.OriginalLexicalTokens != 20 || got.CandidateLexicalTokens != 26 ||
				got.ExpansionCeiling != 1.25 {
				t.Errorf("stored evidence is now %d -> %d at %v, want 20 -> 26 at 1.25",
					got.OriginalLexicalTokens, got.CandidateLexicalTokens, got.ExpansionCeiling)
			}
		})
	}
}

// The constraint uses the row's OWN bound, including one ABOVE today's constant.
//
// Every other case here records a bound at or below 1.5, which a constraint
// written as `min(expansion_ceiling, 1.5)` satisfies. This row's bound is 2.0, so
// 35 tokens against an original of 20 exceeds today's constant and is INSIDE the
// recorded bound — the only shape that separates the two constraints.
func TestTheConstraintUsesARecordedBoundAboveTheCurrentConstant(t *testing.T) {
	s, path := openStoreAt(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionExpanded
	attempt.Splice = rewrite.SpliceNotRecorded
	// 41 > 40 = 2.0 x 20, so the seeded row is a valid `expanded` row.
	attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens = 20, 41
	attempt.ExpansionCeiling = 2.0
	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("seeding an expanded row at a bound of 2.0: %v", err)
	}

	db := rawDB(t, path)
	// Valid first: still above 40.
	if _, err := db.Exec(`UPDATE rewrite_attempt SET candidate_lexical_tokens=?`, 45); err != nil {
		t.Fatalf("a valid update to 45 tokens failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE rewrite_attempt SET candidate_lexical_tokens=?`, 41); err != nil {
		t.Fatalf("restoring 41 tokens failed: %v", err)
	}

	// 35 is above 1.5 x 20 = 30 but below 2.0 x 20 = 40, so it contradicts the
	// bound this row records while satisfying today's constant.
	if _, err := db.Exec(`UPDATE rewrite_attempt SET candidate_lexical_tokens=?`, 35); err == nil {
		t.Error("the database accepted an `expanded` row recording 20 -> 35 against " +
			"its own ceiling of 2.0; the constraint is reading the current constant " +
			"rather than the stored bound")
	}
}
