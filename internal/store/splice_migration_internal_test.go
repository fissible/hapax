package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/rewrite"
)

// ---------------------------------------------------------------------------
// The fifth rewrite_attempt rebuild, and the child #107 created
// ---------------------------------------------------------------------------
//
// #115 adds `not-spliceable` to the rejection vocabulary, so `rewrite_attempt` is
// rebuilt again and all three children are carried over with it.
//
// I claimed #107's migration test covered this. It does not, and the gap is the
// same trap #107's own header documents one table over: that test seeds at the
// version BEFORE #107's rebuild, which is the version at which
// `rewrite_attempt_overgrown_script` does not yet exist — #107's migration creates
// it as its last statement. So no row can be in that table before this
// migration's carry-over runs, and three mutations of that carry-over — deleting
// the INSERT, transposing `attempt_index` with `ordinal`, transposing
// `invocation_id` with `node_id` — all pass the store package without this file.
//
// Worse, a genuinely buggy fifth rebuild that loses EVERY overgrown row leaves
// the suite green while the parent and the other two children carry over intact.
//
// Two rows at a NON-ZERO attempt index, for the reason #107 documented: with one
// row at ordinal 0 on an attempt at index 0, `attempt_index` and `ordinal` hold
// the same value and a transposition between them is the identity.
//
// # The trap this test exists to catch, for whoever writes the migration
//
// #107's `CREATE TABLE rewrite_attempt_overgrown_script` runs AFTER its rename, so
// its foreign key names `rewrite_attempt`. Copied verbatim into a rebuild, that
// key points at the table about to be dropped, while the two `_new` children point
// at `rewrite_attempt_new`. Foreign keys are on from version 7 onward, so
// `DROP TABLE rewrite_attempt` cascades and empties the new child — bisected, the
// INSERT lands two rows and the DROP takes them to zero while the other two
// children stay at two. Retarget the key to `rewrite_attempt_new`.
//
// Without this test that loss is silent and the suite is green. With it, the
// failure is `ordinals = [], want [0 1]`.

// spliceMigration is the index of #115's rebuild, so truncating the list leaves
// the schema as it stood after #107's — the version this upgrade starts from, and
// the first at which the overgrown table exists.
const spliceMigration = 10

func TestAddingTheSpliceCodeKeepsTheOvergrownScriptsAlreadyStored(t *testing.T) {
	if len(migrations) <= spliceMigration {
		t.Fatalf("the migration list has %d entries; the splice rebuild is migrations[%d]",
			len(migrations), spliceMigration)
	}
	wantVersion := spliceMigration - 1

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:spliceMigration]

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

	const refusedIndex = 4
	currentHash := identity.HashBytes([]byte("a paragraph whose script grew"))
	candidateHash := identity.HashBytes([]byte("the candidate that grew it"))
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
			current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
			preserved,tells_comparison,tells_comparable,accepted,rejection)
			VALUES (?,?,?,'ollama',?,?,?,0.9,0.4,'drifting','in-range',1,-1,1,0,'language-growth')`,
			[]any{seeded.invocation, refusedIndex, seeded.profile, seeded.node,
				currentHash, candidateHash}},
		{`INSERT INTO rewrite_attempt_overgrown_script (invocation_id,node_id,attempt_index,ordinal,script)
			VALUES (?,?,?,0,'Cyrillic')`,
			[]any{seeded.invocation, seeded.node, refusedIndex}},
		{`INSERT INTO rewrite_attempt_overgrown_script (invocation_id,node_id,attempt_index,ordinal,script)
			VALUES (?,?,?,1,'Han')`,
			[]any{seeded.invocation, seeded.node, refusedIndex}},
	} {
		if _, err := before.db.ExecContext(context.Background(), statement.sql, statement.args...); err != nil {
			t.Fatalf("seeding %q: %v", statement.sql, err)
		}
	}
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
	got, err := after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.node, refusedIndex)
	if err != nil {
		t.Fatalf("the growth refusal did not survive the rebuild: %v", err)
	}
	if !reflect.DeepEqual(got.OvergrownScripts, []string{"Cyrillic", "Han"}) {
		t.Errorf("the overgrown scripts came back as %v, want [Cyrillic Han]",
			got.OvergrownScripts)
	}
	if got.Rejection != rewrite.RejectionLanguageGrowth {
		t.Errorf("rejection = %q, want %q", got.Rejection, rewrite.RejectionLanguageGrowth)
	}

	// The identity columns themselves, keyed the way production keys them, so a
	// transposition that the loader would find just as readily under the wrong
	// key is still caught.
	var ordinals []int
	rows, err := after.db.QueryContext(ctx,
		`SELECT ordinal FROM rewrite_attempt_overgrown_script
		 WHERE invocation_id=? AND node_id=? AND attempt_index=? ORDER BY ordinal`,
		seeded.invocation, seeded.node, refusedIndex)
	if err != nil {
		t.Fatalf("reading the overgrown rows: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal int
		if err := rows.Scan(&ordinal); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ordinals = append(ordinals, ordinal)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !reflect.DeepEqual(ordinals, []int{0, 1}) {
		t.Errorf("ordinals = %v, want [0 1]; the rebuild moved them", ordinals)
	}

	// And the rest of the graph, so this is a carry-over test rather than a
	// delete-everything test.
	if _, err := after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.node, 0); err != nil {
		t.Errorf("the seeded accepted attempt went with it: %v", err)
	}
	if _, err := after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.secondNode, 0); err != nil {
		t.Errorf("the seeded rejected attempt went with it: %v", err)
	}
}
