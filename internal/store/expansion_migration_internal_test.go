package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
)

// Historical counts and bounds were not recorded; zero backfills mean unknown.
func TestAddingTheExpansionColumnsKeepsTheAttemptsAlreadyStored(t *testing.T) {
	const expansionMigration = 14
	full := migrations
	t.Cleanup(func() { migrations = full })
	if len(full) <= expansionMigration {
		t.Fatalf("missing migration %d", expansionMigration)
	}
	migrations = full[:expansionMigration]
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hapax.db")
	before, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = before.Close() })
	if version, err := before.SchemaVersion(ctx); err != nil || version != 13 {
		t.Fatalf("pre-migration version = %d, error = %v", version, err)
	}
	seeded := seedAttemptAtSpliceSchema(t, before,
		attemptShape{accepted: true},
		attemptShape{rejection: rewrite.RejectionNotSpliceable},
		attemptShape{rejection: rewrite.RejectionNotImproved},
	)
	for i, splice := range []rewrite.SpliceOutcome{rewrite.SpliceIntact, rewrite.SpliceNotIntact, rewrite.SpliceNotRecorded} {
		seeded[i].Splice = splice
		if _, err := before.db.ExecContext(ctx, `UPDATE rewrite_attempt SET splice=? WHERE attempt_index=?`, splice, i); err != nil {
			t.Fatal(err)
		}
	}
	// Distinct child evidence catches a rebuild that drops or swaps a child.
	seeded[2].Preserved = false
	seeded[2].PreserveIdentifiers = []string{"preserve-v1:number:lost:3d4c981bf761d9b8"}
	seeded[2].IntroducedScripts = []string{"Greek", "Han"}
	seeded[2].OvergrownScripts = []string{"Cyrillic"}
	if _, err := before.db.ExecContext(ctx, `UPDATE rewrite_attempt SET preserved=0 WHERE attempt_index=2`); err != nil {
		t.Fatal(err)
	}
	for _, child := range []struct {
		table, column string
		values        []string
	}{
		{"rewrite_attempt_identifier", "identifier", seeded[2].PreserveIdentifiers},
		{"rewrite_attempt_script", "script", seeded[2].IntroducedScripts},
		{"rewrite_attempt_overgrown_script", "script", seeded[2].OvergrownScripts},
	} {
		for ordinal, value := range child.values {
			if _, err := before.db.ExecContext(ctx,
				"INSERT INTO "+child.table+" (invocation_id,node_id,attempt_index,ordinal,"+child.column+") VALUES (?,?,2,?,?)",
				seeded[2].InvocationID, seeded[2].NodeID, ordinal, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := before.Close(); err != nil {
		t.Fatal(err)
	}
	migrations = full
	after, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	defer after.Close()
	for _, want := range seeded {
		got, err := after.LoadRewriteAttempt(ctx, want.InvocationID, want.NodeID, want.Index)
		if err != nil {
			t.Fatalf("loading migrated attempt %d: %v", want.Index, err)
		}
		if got.OriginalLexicalTokens != 0 || got.CandidateLexicalTokens != 0 || got.ExpansionCeiling != 0 {
			t.Errorf("attempt %d backfill = %d -> %d at %v, want all zero",
				want.Index, got.OriginalLexicalTokens, got.CandidateLexicalTokens, got.ExpansionCeiling)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("migrated attempt:\n%+v\nwant:\n%+v", got, want)
		}
	}
}
