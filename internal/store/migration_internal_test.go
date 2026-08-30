package store

import (
	"context"
	"path/filepath"
	"testing"
)

// The one test in this package rather than store_test, because the contract it
// checks is internal: a migration's DDL and its ledger row are one transaction.
// Every other test drives the public API. Nothing is exported for this — the
// seam is the unexported applyMigration the implementation needs anyway.
//
// A non-atomic implementation passes every external test while leaving a
// permanently half-migrated database: the DDL applied, the ledger row missing,
// and the next open refusing forever.
func TestAFailedMigrationRollsBackItsDDL(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hapax.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Version 0 already exists, so the ledger insert violates the primary key
	// AFTER the DDL has run. If the two are not one transaction, probe survives.
	err = s.applyMigration(context.Background(), 0, "CREATE TABLE probe (x TEXT NOT NULL)")
	if err == nil {
		t.Fatal("a duplicate ledger version was accepted")
	}

	var name string
	queryErr := s.db.QueryRowContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'probe'").Scan(&name)
	if queryErr == nil {
		t.Error("the DDL survived a failed migration; it is not in the ledger's transaction")
	}
}
