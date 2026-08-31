package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
)

// SQLite cannot loosen a primary key, so widening rewrite_attempt's identity to
// include the node is a table rebuild — and a rebuild is where migrations lose
// data. The schema rule is "migrated forward only", which is a promise about
// rows and not just about DDL, so the migration is checked against a database
// that already has some.
//
// No store in existence can actually have rewrite_attempt rows: the command that
// writes them has never shipped. That is a reason the migration is cheap to get
// right, not a reason to skip the test — a drop-and-recreate would pass every
// other test in this package, and the next audit table to be widened would
// inherit the pattern.
//
// This is in-package because the seam it needs is: build a database at the
// version BEFORE the rebuild. The migration list is the only thing that decides
// that, so the test shortens it. No store test runs in parallel, and the list is
// restored before the assertions that depend on it.
// attemptKeyMigration is the INDEX in the migration list of the statement that
// rebuilds rewrite_attempt. The ledger is zero-based — migrations[0] is recorded
// as version 0 and SchemaVersion is max(version) — so applying migrations[:4]
// leaves the schema at version 3.
//
// Named rather than derived from the list's length: len(migrations)-1 would have
// made this test stop before whatever the newest migration happens to be, so it
// would have passed green against the four that already exist and said nothing
// about the one this slice adds.
const attemptKeyMigration = 4

func TestWideningTheAttemptKeyKeepsTheAttemptsAlreadyStored(t *testing.T) {
	if len(migrations) <= attemptKeyMigration {
		t.Fatalf("the migration list has %d entries; the attempt-key rebuild is migrations[%d]",
			len(migrations), attemptKeyMigration)
	}
	wantVersion := attemptKeyMigration - 1

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:attemptKeyMigration]

	path := filepath.Join(t.TempDir(), "hapax.db")
	before, err := Open(path)
	if err != nil {
		t.Fatalf("opening at version %d: %v", wantVersion, err)
	}
	version, err := before.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != wantVersion {
		t.Fatalf("the truncated list produced version %d, want %d", version, wantVersion)
	}

	seeded := seedAttemptGraph(t, before)
	if err := before.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	migrations = full
	after, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	defer after.Close()

	got, err := after.LoadRewriteAttempt(context.Background(), seeded.invocation, seeded.node, 0)
	if err != nil {
		t.Fatalf("the attempt did not survive the rebuild: %v", err)
	}
	// The WHOLE record, not a sample of it. A rebuild that copies columns in the
	// wrong order, or drops one, corrupts a specific field — and which field is
	// not predictable, so checking two of them checks nothing.
	if !reflect.DeepEqual(got, seeded.want) {
		t.Errorf("the attempt came back as\n%+v\nand was stored as\n%+v", got, seeded.want)
	}

	// A rebuild that copied rows but kept the old key would satisfy everything
	// above. The point of the new key is that (invocation_id, attempt_index) no
	// longer has to be unique, so a second paragraph at attempt zero must now be
	// accepted — through a raw connection, because what is under test here is the
	// schema and not the write path the store_test file covers.
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
		current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
		preserved,tells_comparison,tells_comparable,accepted,rejection)
		SELECT invocation_id,attempt_index,profile_id,provider_id,?,current_hash,candidate_hash,
		current_distance,candidate_distance,current_band,candidate_band,preserved,tells_comparison,
		tells_comparable,accepted,rejection FROM rewrite_attempt WHERE node_id=?`,
		seeded.secondNode, seeded.node); err != nil {
		t.Fatalf("a second paragraph at attempt 0 was still refused: %v", err)
	}

	// The child table has to have been rebuilt too. Under the old key its
	// (invocation_id, attempt_index, ordinal) primary key makes the second
	// paragraph's first identifier collide with the first paragraph's, and its
	// foreign key points at a parent key that no longer identifies one row.
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt_identifier
		(invocation_id,attempt_index,node_id,ordinal,identifier) VALUES (?,0,?,0,?)`,
		seeded.invocation, seeded.secondNode, "preserve-v1:url:invented:fedcba9876543210"); err != nil {
		t.Fatalf("the identifier table kept the old key: %v", err)
	}

	// And the cascade follows the whole triple: deleting one paragraph's node
	// takes that paragraph's attempt and its identifiers, and leaves the other
	// paragraph's standing. A child still keyed on the old pair would lose both.
	if _, err := raw.Exec("DELETE FROM node WHERE node_id=?", seeded.node); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	for _, row := range []struct {
		node string
		want int
	}{{seeded.node, 0}, {seeded.secondNode, 1}} {
		var attempts, identifiers int
		if err := raw.QueryRow("SELECT count(*) FROM rewrite_attempt WHERE node_id=?", row.node).Scan(&attempts); err != nil {
			t.Fatalf("count attempts: %v", err)
		}
		if err := raw.QueryRow("SELECT count(*) FROM rewrite_attempt_identifier WHERE node_id=?", row.node).Scan(&identifiers); err != nil {
			t.Fatalf("count identifiers: %v", err)
		}
		if attempts != row.want || identifiers != row.want {
			t.Errorf("node %s has %d attempts and %d identifiers after its node was deleted, want %d of each",
				row.node, attempts, identifiers, row.want)
		}
	}
}

type seededAttempt struct {
	invocation, node, secondNode string
	// want is the whole record as it was written, so the migration can be checked
	// field for field rather than by sampling two columns.
	want RewriteAttempt
}

// seedAttemptGraph writes the minimum graph an attempt needs — snapshot,
// document, two nodes, profile — and one attempt with one identifier, using raw
// SQL because the Go API at this version is the thing being migrated away from.
func seedAttemptGraph(t *testing.T, s *Store) seededAttempt {
	t.Helper()
	ctx := context.Background()
	out := seededAttempt{invocation: identity.HashBytes([]byte("invocation"))}
	currentHash := identity.HashBytes([]byte("current"))
	candidateHash := identity.HashBytes([]byte("candidate"))
	identifier := "preserve-v1:number:lost:0123456789abcdef"
	snapshotID := identity.HashBytes([]byte("snapshot"))
	documentID := identity.HashInputs(map[string]string{"snapshot": snapshotID, "path": "a.md"})
	out.node = identity.HashInputs(map[string]string{"document": documentID, "ordinal": "0"})
	out.secondNode = identity.HashInputs(map[string]string{"document": documentID, "ordinal": "1"})
	profileID := identity.HashBytes([]byte("profile"))

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-08-31T00:00:00Z')",
			[]any{snapshotID, identity.HashBytes([]byte("policy"))}},
		{`INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language)
			VALUES (?,?,'a.md',?,'essays','draft','eligible','not-performed')`,
			[]any{documentID, snapshotID, identity.HashBytes([]byte("content"))}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,0,'leaf','paragraph','document',0,12,1,'')`, []any{out.node, documentID}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,1,'leaf','paragraph','document',12,12,1,'')`, []any{out.secondNode, documentID}},
		{`INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,
			feature_set_version,min_paragraph_lexical_tokens)
			VALUES (?,?,'essays','paragraph','sample',?,1,1)`,
			[]any{profileID, snapshotID, identity.HashBytes([]byte("manifest"))}},
		{`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
			current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
			preserved,tells_comparison,tells_comparable,accepted,rejection)
			VALUES (?,0,?,'ollama',?,?,?,1.2,1.4,'drifting','not-you',0,2,1,0,'not-preserved')`,
			[]any{out.invocation, profileID, out.node, currentHash, candidateHash}},
		{"INSERT INTO rewrite_attempt_identifier (invocation_id,attempt_index,ordinal,identifier) VALUES (?,0,0,?)",
			[]any{out.invocation, identifier}},
	} {
		if _, err := s.db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seeding %q: %v", statement.sql, err)
		}
	}

	// Spelled out rather than read back through the loader that is about to be
	// migrated: an expectation derived from the thing under test is not one.
	out.want = RewriteAttempt{
		InvocationID: out.invocation, Index: 0, ProfileID: profileID,
		ProviderID: llm.ProviderOllama, NodeID: out.node,
		CurrentHash: currentHash, CandidateHash: candidateHash,
		CurrentDistance: 1.2, CandidateDistance: 1.4,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandNotYou,
		Preserved: false, PreserveIdentifiers: []string{identifier},
		TellsComparison: 2, TellsComparable: true,
		Accepted: false, Rejection: rewrite.RejectionNotPreserved,
	}
	return out
}
