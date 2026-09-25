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

// ---------------------------------------------------------------------------
// The third rewrite_attempt rebuild
// ---------------------------------------------------------------------------
//
// #91 adds `language` to the rejection vocabulary, and SQLite cannot alter a
// CHECK in place, so `rewrite_attempt` is rebuilt for the third time. Each of
// the previous two rebuilds has a test that data already stored survives it;
// this one had none, because no test in the frozen set called for one and every
// other store test starts from a fresh database at the latest version.
//
// The failure it guards against is quiet and total: the rebuild's INSERT names
// sixteen columns positionally, so a wrong order silently transposes fields
// across every attempt in the file, and a missing one drops a column's values.
// Nothing else would notice.

// languageMigration is the index of the rebuild this file covers, so truncating
// the list produces the schema as it stood immediately before it.
const languageMigration = 8

type seededLanguageAttempt struct {
	invocation, node, secondNode, profile string
	accepted, rejected                    RewriteAttempt
}

func TestAddingTheLanguageCodeKeepsTheAttemptsAlreadyStored(t *testing.T) {
	if len(migrations) <= languageMigration {
		t.Fatalf("the migration list has %d entries; the language rebuild is migrations[%d]",
			len(migrations), languageMigration)
	}
	wantVersion := languageMigration - 1

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:languageMigration]

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
	seeded := seedLanguageAttemptGraph(t, before)
	if err := before.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	migrations = full
	after, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	defer after.Close()

	ctx := context.Background()

	// Both records, whole. Checking a sample of the columns checks nothing: a
	// transposition corrupts one specific field and which one is not
	// predictable. The rejected attempt carries a preserve identifier, so the
	// existing child table's rows are covered by the same comparison.
	got, err := after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.node, 0)
	if err != nil {
		t.Fatalf("the accepted attempt did not survive the rebuild: %v", err)
	}
	if !reflect.DeepEqual(got, seeded.accepted) {
		t.Errorf("the accepted attempt came back as\n%+v\nand was stored as\n%+v", got, seeded.accepted)
	}
	got, err = after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.secondNode, 0)
	if err != nil {
		t.Fatalf("the rejected attempt did not survive the rebuild: %v", err)
	}
	if !reflect.DeepEqual(got, seeded.rejected) {
		t.Errorf("the rejected attempt came back as\n%+v\nand was stored as\n%+v", got, seeded.rejected)
	}

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()

	// The point of the rebuild: `language` is now a storable rejection. Under
	// version 8 this row was refused by the CHECK.
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
		current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
		preserved,tells_comparison,tells_comparable,accepted,rejection)
		VALUES (?,1,?,'ollama',?,?,?,0.9,0.4,'drifting','in-range',1,-1,1,0,'language')`,
		seeded.invocation, seeded.profile, seeded.node,
		identity.HashBytes([]byte("c3")), identity.HashBytes([]byte("d3"))); err != nil {
		t.Fatalf("a language refusal was refused after the rebuild: %v", err)
	}

	// And the half of the vocabulary that must NOT have been relaxed. Dropping
	// the CHECK entirely is the simplest way to make the row above insertable,
	// and it passes every assertion before this one.
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
		current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
		preserved,tells_comparison,tells_comparable,accepted,rejection)
		VALUES (?,2,?,'ollama',?,?,?,0.9,0.4,'drifting','in-range',1,-1,1,0,'script-introduced')`,
		seeded.invocation, seeded.profile, seeded.node,
		identity.HashBytes([]byte("c4")), identity.HashBytes([]byte("d4"))); err == nil {
		t.Error("an undeclared rejection code was stored; the rebuild relaxed the vocabulary " +
			"instead of extending it")
	}

	// The new child table cascades with its parent, on a database that was
	// MIGRATED rather than created at the current version — the frozen test
	// covers the fresh-database path only.
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt_script (invocation_id,node_id,attempt_index,ordinal,script)
		VALUES (?,?,1,0,'Han')`, seeded.invocation, seeded.node); err != nil {
		t.Fatalf("recording a script against a migrated database: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM node WHERE node_id=?", seeded.node); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	var orphaned int
	if err := raw.QueryRow("SELECT count(*) FROM rewrite_attempt_script").Scan(&orphaned); err != nil {
		t.Fatalf("counting script rows: %v", err)
	}
	if orphaned != 0 {
		t.Errorf("%d script rows outlived their attempt after a migration; the rebuilt "+
			"parent and the new child are not connected", orphaned)
	}
}

func seedLanguageAttemptGraph(t *testing.T, s *Store) seededLanguageAttempt {
	t.Helper()
	ctx := context.Background()
	out := seededLanguageAttempt{
		invocation: identity.HashBytes([]byte("language-invocation")),
		profile:    identity.HashBytes([]byte("language-profile")),
	}
	snapshotID := identity.HashBytes([]byte("language-snapshot"))
	documentID := identity.HashInputs(map[string]string{"snapshot": snapshotID, "path": "a.md"})
	out.node = identity.HashInputs(map[string]string{"document": documentID, "ordinal": "0"})
	out.secondNode = identity.HashInputs(map[string]string{"document": documentID, "ordinal": "1"})
	acceptedCurrent := identity.HashBytes([]byte("language-current"))
	acceptedCandidate := identity.HashBytes([]byte("language-candidate"))
	rejectedCurrent := identity.HashBytes([]byte("language-current-2"))
	rejectedCandidate := identity.HashBytes([]byte("language-candidate-2"))
	identifier := "preserve-v1:negation:lost:0123456789abcdef"

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-09-24T00:00:00Z')",
			[]any{snapshotID, identity.HashBytes([]byte("language-policy"))}},
		{`INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language)
			VALUES (?,?,'a.md',?,'essays','draft','eligible','not-performed')`,
			[]any{documentID, snapshotID, identity.HashBytes([]byte("language-content"))}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,0,'leaf','paragraph','document',0,12,1,'')`, []any{out.node, documentID}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,1,'leaf','paragraph','document',12,12,1,'')`, []any{out.secondNode, documentID}},
		{`INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,
			feature_set_version,min_paragraph_lexical_tokens)
			VALUES (?,?,'essays','paragraph','sample',?,1,1)`,
			[]any{out.profile, snapshotID, identity.HashBytes([]byte("language-manifest"))}},
		{`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
			current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
			preserved,tells_comparison,tells_comparable,accepted,rejection)
			VALUES (?,0,?,'anthropic',?,?,?,1.2,0.4,'drifting','in-range',1,-1,1,1,'')`,
			[]any{out.invocation, out.profile, out.node, acceptedCurrent, acceptedCandidate}},
		{`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
			current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
			preserved,tells_comparison,tells_comparable,accepted,rejection)
			VALUES (?,0,?,'ollama',?,?,?,1.2,1.4,'drifting','not-you',0,2,1,0,'not-preserved')`,
			[]any{out.invocation, out.profile, out.secondNode, rejectedCurrent, rejectedCandidate}},
		{`INSERT INTO rewrite_attempt_identifier (invocation_id,node_id,attempt_index,ordinal,identifier)
			VALUES (?,?,0,0,?)`, []any{out.invocation, out.secondNode, identifier}},
	} {
		if _, err := s.db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seeding %q: %v", statement.sql, err)
		}
	}

	// Spelled out rather than read back through the loader that is about to be
	// migrated: an expectation derived from the thing under test is not one.
	out.accepted = RewriteAttempt{
		InvocationID: out.invocation, Index: 0, ProfileID: out.profile,
		ProviderID: llm.ProviderAnthropic, NodeID: out.node,
		CurrentHash: acceptedCurrent, CandidateHash: acceptedCandidate,
		CurrentDistance: 1.2, CandidateDistance: 0.4,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandInRange,
		Preserved: true, TellsComparison: -1, TellsComparable: true,
		Accepted: true, Rejection: rewrite.RejectionNone,
	}
	out.rejected = RewriteAttempt{
		InvocationID: out.invocation, Index: 0, ProfileID: out.profile,
		ProviderID: llm.ProviderOllama, NodeID: out.secondNode,
		CurrentHash: rejectedCurrent, CandidateHash: rejectedCandidate,
		CurrentDistance: 1.2, CandidateDistance: 1.4,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandNotYou,
		Preserved: false, PreserveIdentifiers: []string{identifier},
		TellsComparison: 2, TellsComparable: true,
		Accepted: false, Rejection: rewrite.RejectionNotPreserved,
	}
	return out
}
