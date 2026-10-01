package store

// #134's migration has no history to recover, and deliberately invents none.
//
// # Contract
//
// The migration creates the evidence table empty. It does NOT derive rows from
// `accepted=1`, because that is the equation #134 is about: an accepted attempt
// may never have reached a file, and a run that failed after an acceptance
// leaves one behind.
//
// # Consequence, and why it is recorded rather than inferred
//
// A store written before this migration holds accepted attempts and no
// publication evidence, so paragraphs published by those runs stop being
// screened. That is a real loss, and the direction is chosen: an unrecorded
// publication means the screen fails to EXCLUDE, where a backfilled one would
// exclude the author's own prose from their own corpus.
//
// The condition cannot be read off the evidence table later — a fresh store is
// also empty, and a subsequent publication does not repair the gap — so the
// migration records it at the one moment it is knowable.
//
// # Evidence
//
// Reproduced in internal/workflow: a provider that answers once and then fails
// returns `provider down` and zero bytes, and `PublishedParagraphs` holds one
// entry. Planning an author document carrying that same paragraph then reports
// `already-rewritten=1`.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/identity"
)

// publicationMigration is the INDEX of the migration this file covers, so
// truncating the list produces the schema as it stood immediately before it.
const publicationMigration = 12

// A store that already held accepted attempts is marked as having a gap.
func TestTheMigrationRecordsThatEarlierRunsLeftNoPublicationEvidence(t *testing.T) {
	for _, c := range []struct {
		name     string
		accepted bool
		want     bool
	}{
		// An accepted attempt may have been published, and the store cannot say.
		{"an accepted attempt", true, true},
		// A refused one cannot have been, so there is nothing to disclose.
		{"only refused attempts", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := migrateWithSeededAttempt(t, c.accepted)
			after, err := Open(path)
			if err != nil {
				t.Fatalf("migrating forward: %v", err)
			}
			defer after.Close()

			gap, err := after.PublicationEvidenceGap(context.Background())
			if err != nil {
				t.Fatalf("PublicationEvidenceGap: %v", err)
			}
			if gap != c.want {
				t.Errorf("PublicationEvidenceGap = %v, want %v", gap, c.want)
			}

			// Exactly one marker row when there is one to write, so a migration
			// inserting per accepted attempt is caught.
			var markers int
			if err := after.db.QueryRowContext(context.Background(),
				"SELECT count(*) FROM publication_evidence_gap").Scan(&markers); err != nil {
				t.Fatalf("counting publication_evidence_gap: %v", err)
			}
			if want := map[bool]int{true: 1, false: 0}[c.want]; markers != want {
				t.Errorf("%d marker rows, want %d", markers, want)
			}

			// The table is created and left EMPTY either way. A migration that
			// backfilled from accepted=1 would satisfy the gap assertion above
			// and fail here, which is the half that matters.
			var rows int
			if err := after.db.QueryRowContext(context.Background(),
				"SELECT count(*) FROM published_paragraph").Scan(&rows); err != nil {
				t.Fatalf("counting published_paragraph: %v", err)
			}
			if rows != 0 {
				t.Errorf("the migration recorded %d publications; it cannot know of any", rows)
			}
		})
	}
}

// A store created after this migration has no gap to disclose.
func TestAFreshStoreHasNoPublicationEvidenceGap(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hapax.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	gap, err := s.PublicationEvidenceGap(context.Background())
	if err != nil {
		t.Fatalf("PublicationEvidenceGap: %v", err)
	}
	if gap {
		t.Error("a store with no history at all reports a historical gap")
	}
}

// An accepted attempt recorded AFTER the migration is not a historical gap.
//
// The pair to the test above, and the one that separates reading the marker from
// recomputing `EXISTS(SELECT 1 FROM rewrite_attempt WHERE accepted=1)`. A store
// created after this migration accounts for everything it ever published, so an
// acceptance in it discloses nothing.
func TestAnAttemptAcceptedAfterTheMigrationIsNotAGap(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hapax.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	seedAttemptsAt(t, s, 2, true)

	gap, err := s.PublicationEvidenceGap(context.Background())
	if err != nil {
		t.Fatalf("PublicationEvidenceGap: %v", err)
	}
	if gap {
		t.Error("an acceptance in a store built after the migration reports a " +
			"historical gap; the condition is being recomputed from the attempt " +
			"table rather than read from what the migration recorded")
	}
}

// And a recorded gap survives the attempts that caused it being deleted.
//
// The same discrimination from the other side: a store that is pruned, or whose
// old attempts are otherwise gone, still cannot account for what those runs
// published. Reopening is included because the answer must come from the
// database rather than from something computed at open time.
func TestARecordedGapSurvivesTheAttemptsBeingDeleted(t *testing.T) {
	path := migrateWithSeededAttempt(t, true)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	if _, err := first.db.ExecContext(context.Background(),
		"DELETE FROM rewrite_attempt"); err != nil {
		t.Fatalf("deleting the attempts: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close()
	gap, err := again.PublicationEvidenceGap(context.Background())
	if err != nil {
		t.Fatalf("PublicationEvidenceGap: %v", err)
	}
	if !gap {
		t.Error("deleting the old attempts cleared the disclosure; what those runs " +
			"published is no better accounted for than it was")
	}
}

// Publishing after the migration does not clear the gap.
//
// The disclosure is about runs this store can no longer account for, and a later
// publication says nothing about those. An implementation deriving the gap from
// "is the evidence table empty" passes both tests above and fails this one.
func TestARecordedPublicationDoesNotClearTheGap(t *testing.T) {
	path := migrateWithSeededAttempt(t, true)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	defer s.Close()

	if err := s.RecordPublication(context.Background(), Publication{
		InvocationID: identity.HashBytes([]byte("a later invocation")),
		Paragraphs: []PublishedParagraph{
			{NodeID: identity.HashBytes([]byte("a later node")),
				ParagraphHash: identity.HashBytes([]byte("a later paragraph"))},
		},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	gap, err := s.PublicationEvidenceGap(context.Background())
	if err != nil {
		t.Fatalf("PublicationEvidenceGap: %v", err)
	}
	if !gap {
		t.Error("recording a new publication cleared a gap it cannot have repaired")
	}
}

// migrateWithSeededAttempt opens a store at the version immediately before the
// publication migration, seeds one attempt, and returns its path unmigrated.
func migrateWithSeededAttempt(t *testing.T, accepted bool) string {
	t.Helper()
	if len(migrations) <= publicationMigration {
		t.Fatalf("the migration list has %d entries; the publication migration is "+
			"migrations[%d]", len(migrations), publicationMigration)
	}
	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:publicationMigration]

	path := filepath.Join(t.TempDir(), "hapax.db")
	before, err := Open(path)
	if err != nil {
		t.Fatalf("opening at the pre-migration version: %v", err)
	}
	wantVersion := publicationMigration - 1
	version, err := before.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != wantVersion {
		t.Fatalf("the truncated list produced version %d, want %d", version, wantVersion)
	}
	// TWO attempts, so "exactly one marker" distinguishes one per migration from
	// one per accepted attempt.
	seedAttemptsAt(t, before, 2, accepted)
	if err := before.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	migrations = full
	return path
}

// seedAttemptsAt writes the graph an attempt needs and `n` attempts against it,
// with raw SQL at the schema the store is CURRENTLY at.
//
// Not `seedAttemptGraph`: that one targets the schema its own migration test
// truncates to, and its `rewrite_attempt_identifier` insert names no `node_id`.
// Run against this migration's schema it fails with
// `NOT NULL constraint failed: rewrite_attempt_identifier.node_id`. No identifier
// rows are written here, because nothing about publication evidence depends on
// them.
func seedAttemptsAt(t *testing.T, s *Store, n int, accepted bool) {
	t.Helper()
	ctx := context.Background()
	snapshotID := identity.HashBytes([]byte("snapshot"))
	documentID := identity.HashInputs(map[string]string{"snapshot": snapshotID, "path": "a.md"})
	profileID := identity.HashBytes([]byte("profile"))
	invocation := identity.HashBytes([]byte("invocation"))

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-09-30T00:00:00Z')",
			[]any{snapshotID, identity.HashBytes([]byte("policy"))}},
		{`INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language)
			VALUES (?,?,'a.md',?,'essays','draft','eligible','not-performed')`,
			[]any{documentID, snapshotID, identity.HashBytes([]byte("content"))}},
		{`INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,
			feature_set_version,min_paragraph_lexical_tokens)
			VALUES (?,?,'essays','paragraph','sample',?,1,1)`,
			[]any{profileID, snapshotID, identity.HashBytes([]byte("manifest"))}},
	} {
		if _, err := s.db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seeding %q: %v", statement.sql, err)
		}
	}

	rejection, flag := "not-improved", 0
	if accepted {
		rejection, flag = "", 1
	}
	for i := range n {
		node := identity.HashInputs(map[string]string{
			"document": documentID, "ordinal": fmt.Sprint(i),
		})
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
				VALUES (?,?,?,'leaf','paragraph','document',?,12,1,'')`,
			node, documentID, i, i*12); err != nil {
			t.Fatalf("seeding node %d: %v", i, err)
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
				current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
				preserved,tells_comparison,tells_comparable,accepted,rejection)
				VALUES (?,0,?,'ollama',?,?,?,1.5,0.5,'drifting','in-range',1,0,1,?,?)`,
			invocation, profileID, node,
			identity.HashBytes([]byte(fmt.Sprintf("current %d", i))),
			identity.HashBytes([]byte(fmt.Sprintf("candidate %d", i))),
			flag, rejection); err != nil {
			t.Fatalf("seeding attempt %d: %v", i, err)
		}
	}
}
