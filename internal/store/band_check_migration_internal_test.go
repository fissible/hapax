package store

// #81 relaxes rewrite_attempt's accepted/band CHECK, which SQLite cannot alter
// in place, so it is another table rebuild — and a rebuild is where migrations
// lose data.
//
// The green suite proves the schema a FRESH database ends up with. It says
// nothing about the upgrade path, because every other test opens a store at the
// newest version and never travels through 6. That gap is not hypothetical here:
// earlier in this same project a rebuild copied migration 0's definition of
// document.language instead of migration 2's, silently reverting a closed set to
// a grammar, and only the vocabulary suite caught it.
//
// So this seeds a database at version 6, migrates it forward, and checks the
// rows rather than the DDL.

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

// bandCheckMigration is the INDEX in the migration list of the statement that
// relaxes the accepted/band CHECK. Named rather than derived from the list's
// length, for the reason the attempt-key test gives: len(migrations)-1 would
// make this test stop before whatever the newest migration happens to be, so it
// would pass green against the ones that already exist and say nothing about the
// one this slice adds.
const bandCheckMigration = 7

type seededBandAttempt struct {
	invocation, node, secondNode, profile string
	accepted, rejected                    RewriteAttempt
}

func TestRelaxingTheBandCheckKeepsTheAttemptsAlreadyStored(t *testing.T) {
	if len(migrations) <= bandCheckMigration {
		t.Fatalf("the migration list has %d entries; the band-check rebuild is migrations[%d]",
			len(migrations), bandCheckMigration)
	}
	wantVersion := bandCheckMigration - 1

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:bandCheckMigration]

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
	seeded := seedBandAttemptGraph(t, before)
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

	// Both records, whole. A rebuild that copies columns in the wrong order
	// corrupts a specific field, and which field is not predictable, so checking
	// a sample of them checks nothing. The rejected one carries a preserve
	// identifier, so the child table's rows are checked by the same comparison.
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

	// The point of the rebuild: an accepted attempt with NO band is now storable.
	// Under version 6 this row was refused, which is what made #81's explicit
	// selection die with `store: invalid: rewrite attempt decision state`.
	third := identity.HashInputs(map[string]string{"document": seeded.node, "ordinal": "2"})
	if _, err := raw.Exec(`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
		SELECT ?, document_id, 2,'leaf','paragraph','document',24,12,1,'' FROM node WHERE node_id=?`,
		third, seeded.node); err != nil {
		t.Fatalf("seeding a third node: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
		current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
		preserved,tells_comparison,tells_comparable,accepted,rejection)
		VALUES (?,0,?,'ollama',?,?,?,0.9,0.4,'','',1,-1,1,1,'')`,
		seeded.invocation, seeded.profile, third,
		identity.HashBytes([]byte("c3")), identity.HashBytes([]byte("d3"))); err != nil {
		t.Fatalf("an accepted attempt with no band was refused after the rebuild: %v", err)
	}

	// And the half of the rule that survives: the two bands must still agree on
	// presence. A rebuild that dropped the CHECK entirely — the simplest way to
	// make the row above insertable — passes every assertion before this one.
	fourth := identity.HashInputs(map[string]string{"document": seeded.node, "ordinal": "3"})
	if _, err := raw.Exec(`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
		SELECT ?, document_id, 3,'leaf','paragraph','document',36,12,1,'' FROM node WHERE node_id=?`,
		fourth, seeded.node); err != nil {
		t.Fatalf("seeding a fourth node: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
		current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
		preserved,tells_comparison,tells_comparable,accepted,rejection)
		VALUES (?,0,?,'ollama',?,?,?,0.9,0.4,'drifting','',1,-1,1,1,'')`,
		seeded.invocation, seeded.profile, fourth,
		identity.HashBytes([]byte("c4")), identity.HashBytes([]byte("d4"))); err == nil {
		t.Error("an accepted attempt with one band and not the other was stored; the bands must agree on presence")
	}

	// The child table's cascade survives the rebuild. Both tables were recreated,
	// so the foreign key could have been dropped or left pointing at the
	// temporary name, and nothing above would notice.
	if _, err := raw.Exec("DELETE FROM rewrite_attempt WHERE node_id=?", seeded.secondNode); err != nil {
		t.Fatalf("deleting the rejected attempt: %v", err)
	}
	var orphans int
	if err := raw.QueryRow("SELECT count(*) FROM rewrite_attempt_identifier WHERE node_id=?",
		seeded.secondNode).Scan(&orphans); err != nil {
		t.Fatalf("counting identifiers: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d preserve identifiers outlived their attempt; the cascade did not survive the rebuild", orphans)
	}
}

// seedBandAttemptGraph writes one accepted and one rejected attempt at version 6.
// The accepted one carries both bands, because that is the only accepted shape
// version 6 permits — which is the whole reason the rebuild exists.
func seedBandAttemptGraph(t *testing.T, s *Store) seededBandAttempt {
	t.Helper()
	ctx := context.Background()
	out := seededBandAttempt{
		invocation: identity.HashBytes([]byte("band-invocation")),
		profile:    identity.HashBytes([]byte("band-profile")),
	}
	snapshotID := identity.HashBytes([]byte("band-snapshot"))
	documentID := identity.HashInputs(map[string]string{"snapshot": snapshotID, "path": "a.md"})
	out.node = identity.HashInputs(map[string]string{"document": documentID, "ordinal": "0"})
	out.secondNode = identity.HashInputs(map[string]string{"document": documentID, "ordinal": "1"})
	acceptedCurrent := identity.HashBytes([]byte("band-current"))
	acceptedCandidate := identity.HashBytes([]byte("band-candidate"))
	rejectedCurrent := identity.HashBytes([]byte("band-current-2"))
	rejectedCandidate := identity.HashBytes([]byte("band-candidate-2"))
	identifier := "preserve-v1:negation:lost:0123456789abcdef"

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-09-02T00:00:00Z')",
			[]any{snapshotID, identity.HashBytes([]byte("band-policy"))}},
		{`INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language)
			VALUES (?,?,'a.md',?,'essays','draft','eligible','not-performed')`,
			[]any{documentID, snapshotID, identity.HashBytes([]byte("band-content"))}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,0,'leaf','paragraph','document',0,12,1,'')`, []any{out.node, documentID}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,1,'leaf','paragraph','document',12,12,1,'')`, []any{out.secondNode, documentID}},
		{`INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,
			feature_set_version,min_paragraph_lexical_tokens)
			VALUES (?,?,'essays','paragraph','sample',?,1,1)`,
			[]any{out.profile, snapshotID, identity.HashBytes([]byte("band-manifest"))}},
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
		Preserved: true, PreserveIdentifiers: nil,
		TellsComparison: -1, TellsComparable: true,
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
