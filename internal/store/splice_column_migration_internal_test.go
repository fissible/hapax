package store

// #135 adds a column rather than rebuilding the table for the sixth time.
//
// # Decision
//
// `ALTER TABLE rewrite_attempt ADD COLUMN splice TEXT NOT NULL DEFAULT ''
// CHECK(splice IN (...))`. Measured against the driver this project uses before
// the choice was made: the add succeeds, the CHECK refuses a value outside the
// vocabulary afterwards, and an existing row reads back as `''`.
//
// The five earlier `rewrite_attempt` rebuilds — migrations 5, 8, 9, 10 and 11 —
// were not all the same kind of change: migration 5 changed the PRIMARY KEY, and
// the others widened a CHECK, which SQLite genuinely cannot do in place. Adding a
// new column is neither, and an earlier draft of this comment claimed all five
// were CHECK widenings, which was wrong on the count and on the reason.
//
// A rebuild is also not free of risk this repo has already paid for: migration 10
// dropped a table with foreign keys on and silently emptied a child, and migration
// 11's note records the same hazard. One ADD COLUMN has no such failure mode.
//
// # What the default means
//
// `''` is "no verdict recorded", and for a migrated row that is the honest answer:
// the gate may well have run and its result was discarded, which is the defect
// being fixed. The migration does NOT derive a verdict from `rejection` — a row
// refused as `not-spliceable` did have the gate say no, but a row refused as
// `not-improved` says nothing either way, and backfilling the first while leaving
// the second would make the column mean two different things by row.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
)

// spliceColumnMigration is the INDEX of the migration this file covers, so
// truncating the list produces the schema as it stood immediately before it.
const spliceColumnMigration = 12

// The attempts already stored survive, and carry no verdict.
func TestAddingTheSpliceColumnKeepsTheAttemptsAlreadyStored(t *testing.T) {
	full := migrations
	t.Cleanup(func() { migrations = full })
	if len(migrations) <= spliceColumnMigration {
		t.Fatalf("the migration list has %d entries; the splice column is migrations[%d]",
			len(migrations), spliceColumnMigration)
	}
	migrations = full[:spliceColumnMigration]

	path := filepath.Join(t.TempDir(), "hapax.db")
	before, err := Open(path)
	if err != nil {
		t.Fatalf("opening at the pre-migration version: %v", err)
	}
	if version, err := before.SchemaVersion(context.Background()); err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	} else if want := spliceColumnMigration - 1; version != want {
		t.Fatalf("the truncated list produced version %d, want %d", version, want)
	}
	seeded := seedAttemptAtSpliceSchema(t, before)[0]
	if err := before.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	migrations = full
	after, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	defer after.Close()

	got, err := after.LoadRewriteAttempt(context.Background(), seeded.InvocationID, seeded.NodeID, 0)
	if err != nil {
		t.Fatalf("the attempt stored before the migration no longer loads: %v", err)
	}
	// Every other field unchanged, compared whole rather than sampled: an
	// ALTER TABLE cannot transpose columns the way a rebuild's positional INSERT
	// can, and asserting it anyway is what would catch the migration being
	// rewritten as a rebuild later.
	want := seeded
	if got.Splice != "" {
		t.Errorf("a row written before the column existed carries splice %q, want \"\" — "+
			"nothing can know what the gate said about it", got.Splice)
	}
	got.Splice = want.Splice
	if !sameAttempt(got, want) {
		t.Errorf("the migrated attempt is\n%+v\nand was\n%+v", got, want)
	}
}

// NO verdict is inferred from a historical rejection.
//
// The three shapes that could tempt an inference, seeded before the migration: a
// row that was ACCEPTED (so the gate must have said yes), one refused as
// `not-spliceable` (so it must have said no), and one refused for an unrelated
// reason (so it says nothing). All three must come out empty.
//
// Measured by codex as a surviving mutant: appending
// `UPDATE rewrite_attempt SET splice='not-intact' WHERE rejection='not-spliceable'`
// to the migration passed the whole suite. It is a TRUE inference for that row and
// it is still wrong to make, because it leaves the column meaning "what the gate
// said" on some rows and "nothing is known" on others, and no reader can tell which
// from the row itself.
func TestTheMigrationInfersNoVerdictFromAHistoricalRejection(t *testing.T) {
	full := migrations
	t.Cleanup(func() { migrations = full })
	if len(migrations) <= spliceColumnMigration {
		t.Fatalf("the migration list has %d entries; the splice column is migrations[%d]",
			len(migrations), spliceColumnMigration)
	}
	migrations = full[:spliceColumnMigration]

	path := filepath.Join(t.TempDir(), "hapax.db")
	before, err := Open(path)
	if err != nil {
		t.Fatalf("opening at the pre-migration version: %v", err)
	}
	seeded := seedAttemptAtSpliceSchema(t, before,
		attemptShape{accepted: true},
		attemptShape{rejection: rewrite.RejectionNotSpliceable},
		attemptShape{rejection: rewrite.RejectionNotImproved},
	)
	if err := before.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	migrations = full
	after, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	defer after.Close()

	for i, want := range seeded {
		got, err := after.LoadRewriteAttempt(context.Background(),
			want.InvocationID, want.NodeID, i)
		if err != nil {
			t.Fatalf("attempt %d no longer loads: %v", i, err)
		}
		if got.Splice != "" {
			t.Errorf("the attempt refused as %q (accepted=%v) carries splice %q, want \"\" "+
				"— the migration inferred a verdict it cannot know",
				want.Rejection, want.Accepted, got.Splice)
		}
	}
}

// The CHECK is enforced after the column is added in place.
//
// This is the property the whole decision rests on: SQLite permits ADD COLUMN with
// a CHECK, and it would be a silent hole if the constraint were accepted and then
// not applied. Written with raw SQL, because the Go validation would refuse the
// value before the database saw it and the question here is what the DATABASE does.
func TestTheSpliceCheckIsEnforcedAfterTheColumnIsAdded(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hapax.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	seeded := seedAttemptAtSpliceSchema(t, s)[0]

	_, err = s.db.ExecContext(context.Background(),
		"UPDATE rewrite_attempt SET splice='spliceable' WHERE invocation_id=?", seeded.InvocationID)

	if err == nil {
		t.Error("the database accepted a splice value outside the vocabulary; the CHECK " +
			"was declared on an added column and is not being applied")
	}
}

// seedAttemptAtSpliceSchema writes the graph an attempt needs and ONE attempt,
// with raw SQL at the schema the store is currently at, and returns the record as
// written.
//
// Not `seedAttemptGraph`: that one targets the schema its own migration test
// truncates to, and its `rewrite_attempt_identifier` insert names no `node_id`.
// Against this migration's schema it fails with
// `NOT NULL constraint failed: rewrite_attempt_identifier.node_id`. That is the
// second slice in a row to trip over it — #134's migration test needed its own
// seeder for exactly the same reason — so the rule is simply that
// `seedAttemptGraph` belongs to migration 4 and nothing else may call it.
//
// No identifier rows at all here: nothing about the splice column depends on them.
func seedAttemptAtSpliceSchema(t *testing.T, s *Store, shapes ...attemptShape) []RewriteAttempt {
	t.Helper()
	ctx := context.Background()
	if len(shapes) == 0 {
		shapes = []attemptShape{{accepted: false, rejection: rewrite.RejectionNotImproved}}
	}
	snapshotID := identity.HashBytes([]byte("snapshot"))
	documentID := identity.HashInputs(map[string]string{"snapshot": snapshotID, "path": "a.md"})
	node := identity.HashInputs(map[string]string{"document": documentID, "ordinal": "0"})
	profileID := identity.HashBytes([]byte("profile"))
	invocation := identity.HashBytes([]byte("invocation"))

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-10-01T00:00:00Z')",
			[]any{snapshotID, identity.HashBytes([]byte("policy"))}},
		{`INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language)
			VALUES (?,?,'a.md',?,'essays','draft','eligible','not-performed')`,
			[]any{documentID, snapshotID, identity.HashBytes([]byte("content"))}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,0,'leaf','paragraph','document',0,12,1,'')`, []any{node, documentID}},
		{`INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,
			feature_set_version,min_paragraph_lexical_tokens)
			VALUES (?,?,'essays','paragraph','sample',?,1,1)`,
			[]any{profileID, snapshotID, identity.HashBytes([]byte("manifest"))}},
	} {
		if _, err := s.db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seeding %q: %v", statement.sql, err)
		}
	}

	var written []RewriteAttempt
	for i, shape := range shapes {
		want := RewriteAttempt{
			InvocationID: invocation, Index: i,
			ProfileID: profileID, ProviderID: llm.ProviderOllama, NodeID: node,
			CurrentHash:     identity.HashBytes([]byte(fmt.Sprintf("current %d", i))),
			CandidateHash:   identity.HashBytes([]byte(fmt.Sprintf("candidate %d", i))),
			CurrentDistance: 1.2, CandidateDistance: 1.4,
			CurrentBand: eval.BandDrifting, CandidateBand: eval.BandNotYou,
			Preserved: true, TellsComparison: -1, TellsComparable: true,
			Accepted: shape.accepted, Rejection: shape.rejection,
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
				current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
				preserved,tells_comparison,tells_comparable,accepted,rejection)
				VALUES (?,?,?,'ollama',?,?,?,1.2,1.4,'drifting','not-you',1,-1,1,?,?)`,
			invocation, i, profileID, node, want.CurrentHash, want.CandidateHash,
			boolInt(shape.accepted), string(shape.rejection)); err != nil {
			t.Fatalf("seeding attempt %d: %v", i, err)
		}
		written = append(written, want)
	}
	return written
}

// attemptShape is the only thing the backfill question turns on: whether a
// historical row was accepted, and which code refused it.
type attemptShape struct {
	accepted  bool
	rejection rewrite.RejectionCode
}
