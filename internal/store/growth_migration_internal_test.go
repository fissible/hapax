package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/rewrite"
)

// ---------------------------------------------------------------------------
// The fourth rewrite_attempt rebuild, and the child it carries over
// ---------------------------------------------------------------------------
//
// #107 adds `language-growth` to the rejection vocabulary, so `rewrite_attempt`
// is rebuilt again, and both existing children are rebuilt alongside it because
// their foreign keys name the table being dropped.
//
// `language_migration_internal_test.go` covers the parent's carry-over, and its
// identifier rows are seeded before the migration runs, so that child is covered
// too. It CANNOT cover `rewrite_attempt_script`: it seeds from version 7, where
// that table does not exist — migration 8 creates it — so any script row it
// writes is written after migration 9 has already run. Three mutations of
// migration 9's script carry-over survive the whole store package as a result:
// deleting the INSERT, transposing `attempt_index` with `ordinal`, and
// transposing `invocation_id` with `node_id`. All three are silent, because both
// hashes are hex-64 and both integers are non-negative, so no CHECK notices.
//
// Structurally the same shape as the cascade test that could not observe a
// loader leak because it deleted the row before loading: a test that looks like
// it covers the path and cannot.
//
// What it costs in production is specific. A writer who has been running #91,
// accumulated `language` refusals, and upgrades to #107 loses every recorded
// script name — and "a refusal discards the prose, so this is the only durable
// trace that the substitution happened" is the whole argument for the column.

// growthMigration is the index of #107's rebuild, so truncating the list leaves
// the schema as it stood after #91's — the version this upgrade starts from.
const growthMigration = 9

func TestAddingTheGrowthCodeKeepsTheScriptsAlreadyStored(t *testing.T) {
	if len(migrations) <= growthMigration {
		t.Fatalf("the migration list has %d entries; the growth rebuild is migrations[%d]",
			len(migrations), growthMigration)
	}
	wantVersion := growthMigration - 1

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:growthMigration]

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

	// A #91 refusal, with TWO script rows. Two matters for the same reason it
	// does in the seed next door: with one row at ordinal 0 on an attempt at
	// index 0, `attempt_index` and `ordinal` hold the same value and a
	// transposition between them is the identity. This attempt is at index 5.
	const refusedIndex = 5
	currentHash := identity.HashBytes([]byte("a paragraph that came back in Han"))
	candidateHash := identity.HashBytes([]byte("the candidate that did it"))
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
			current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
			preserved,tells_comparison,tells_comparable,accepted,rejection)
			VALUES (?,?,?,'ollama',?,?,?,0.9,0.4,'drifting','in-range',1,-1,1,0,'language')`,
			[]any{seeded.invocation, refusedIndex, seeded.profile, seeded.node,
				currentHash, candidateHash}},
		{`INSERT INTO rewrite_attempt_script (invocation_id,node_id,attempt_index,ordinal,script)
			VALUES (?,?,?,0,'Cyrillic')`,
			[]any{seeded.invocation, seeded.node, refusedIndex}},
		{`INSERT INTO rewrite_attempt_script (invocation_id,node_id,attempt_index,ordinal,script)
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

	// Through the loader, in the recorded order.
	got, err := after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.node, refusedIndex)
	if err != nil {
		t.Fatalf("the language refusal did not survive the rebuild: %v", err)
	}
	if !reflect.DeepEqual(got.IntroducedScripts, []string{"Cyrillic", "Han"}) {
		t.Errorf("the scripts came back as %v, want [Cyrillic Han]", got.IntroducedScripts)
	}
	if got.Rejection != rewrite.RejectionLanguage {
		t.Errorf("rejection = %q, want %q", got.Rejection, rewrite.RejectionLanguage)
	}
	// And #107's own column is empty on a record written before it existed,
	// rather than inheriting the other child's rows.
	if len(got.OvergrownScripts) != 0 {
		t.Errorf("a pre-#107 record came back with overgrown scripts %v",
			got.OvergrownScripts)
	}

	// And the identity columns themselves, because the loader would find the
	// rows under a transposed key just as readily if BOTH the write and the
	// read agreed on the wrong one. Read raw, keyed the way production keys it.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	rows, err := raw.Query(
		`SELECT ordinal, script FROM rewrite_attempt_script
		 WHERE invocation_id=? AND node_id=? AND attempt_index=? ORDER BY ordinal`,
		seeded.invocation, seeded.node, refusedIndex)
	if err != nil {
		t.Fatalf("reading the script rows: %v", err)
	}
	defer rows.Close()
	var ordinals []int
	var scripts []string
	for rows.Next() {
		var ordinal int
		var script string
		if err := rows.Scan(&ordinal, &script); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ordinals = append(ordinals, ordinal)
		scripts = append(scripts, script)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !reflect.DeepEqual(ordinals, []int{0, 1}) {
		t.Errorf("ordinals = %v, want [0 1]; the rebuild moved them", ordinals)
	}
	if !reflect.DeepEqual(scripts, []string{"Cyrillic", "Han"}) {
		t.Errorf("scripts = %v, want [Cyrillic Han]", scripts)
	}

	// The rest of the graph is still intact, so this is a carry-over test and
	// not a delete-everything test.
	if _, err := after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.node, 0); err != nil {
		t.Errorf("the seeded accepted attempt went with it: %v", err)
	}
	if _, err := after.LoadRewriteAttempt(ctx, seeded.invocation, seeded.secondNode, 0); err != nil {
		t.Errorf("the seeded rejected attempt went with it: %v", err)
	}

	// And #107's code is storable now, which is what the rebuild was for.
	if _, err := raw.Exec(`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,
		current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,
		preserved,tells_comparison,tells_comparable,accepted,rejection)
		VALUES (?,6,?,'ollama',?,?,?,0.9,0.4,'drifting','in-range',1,-1,1,0,'language-growth')`,
		seeded.invocation, seeded.profile, seeded.node,
		identity.HashBytes([]byte("c9")), identity.HashBytes([]byte("d9"))); err != nil {
		t.Errorf("a growth refusal was refused after the rebuild: %v", err)
	}
}
