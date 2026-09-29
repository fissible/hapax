package store

// #109 puts `rejected-tool-output` in the admission vocabulary, which SQLite
// cannot add to a CHECK in place, so `document` is rebuilt for the second time.
//
// Every previous rebuild in this package has a test that rows already stored
// survive it — migration 6's document split, and three of `rewrite_attempt`.
// This one shipped without, because the frozen set answered the migration
// question with the vocabulary tripwire (`TestEveryDeclaredEnumValueIsAccepted\
// ByTheSchema` names the value the moment it is declared) and that tripwire
// tests the SCHEMA, not the data. Added afterwards, by consensus, for the
// reason `band_check_migration_internal_test.go` gives about its own rebuild:
// "the green suite proves the schema a FRESH database ends up with. It says
// nothing about the upgrade path."
//
// Two specific ways this rebuild could be quietly wrong:
//
//   - The INSERT names nine columns positionally, so a wrong order transposes
//     fields across every document in the file. Measured by mutation: swapping
//     `split` and `admission` is caught, but by the rebuilt table's own CHECKs
//     rather than by the comparison below — the seeded row gives those four
//     short lowercase columns DISTINCT values from four different vocabularies,
//     so no transposition among them produces a legal row. The comparison is
//     what covers a transposition the CHECKs would accept.
//   - `node.document_id REFERENCES document(document_id) ON DELETE CASCADE`, so
//     `DROP TABLE document` with foreign keys ON empties `node` — and every
//     vector and feature value hanging off it. That is #115's migration 10
//     defect in a different table, and it leaves a green suite behind. This is
//     the load-bearing half: measured by mutation, running the rebuild as a
//     plain batch instead of through `applyTableRebuildMigration` leaves 0 of 1
//     nodes, and nothing else in the suite notices.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/identity"
)

// admissionMigration is the INDEX of the rebuild this file covers, so
// truncating the list produces the schema as it stood immediately before it.
const admissionMigration = 11

func TestWideningTheAdmissionCheckKeepsTheDocumentsAlreadyStored(t *testing.T) {
	if len(migrations) <= admissionMigration {
		t.Fatalf("the migration list has %d entries; the admission rebuild is migrations[%d]",
			len(migrations), admissionMigration)
	}
	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:admissionMigration]

	path := filepath.Join(t.TempDir(), "hapax.db")
	before, err := Open(path)
	if err != nil {
		t.Fatalf("opening at the pre-rebuild version: %v", err)
	}
	wantVersion := admissionMigration - 1
	version, err := before.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != wantVersion {
		t.Fatalf("the truncated list produced version %d, want %d", version, wantVersion)
	}
	seeded := seedDocumentGraph(t, path)
	if err := before.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	migrations = full
	after, err := Open(path)
	if err != nil {
		t.Fatalf("migrating forward: %v", err)
	}
	defer after.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()

	// Every column of the document, in one comparison. Sampling proves nothing:
	// a transposition corrupts one specific field and which one is not
	// predictable.
	var got seededDocument
	err = raw.QueryRow(`SELECT document_id,snapshot_id,path,content_hash,register,split,admission,language
		FROM document`).Scan(&got.id, &got.snapshot, &got.path, &got.contentHash,
		&got.register, &got.split, &got.admission, &got.language)
	if err != nil {
		t.Fatalf("the document did not survive the rebuild: %v", err)
	}
	if got != seeded.document {
		t.Errorf("the document came back as\n%+v\nand was stored as\n%+v", got, seeded.document)
	}

	// And the child that cascades off it. A rebuild with foreign keys left on
	// takes the node with the table it drops, and nothing above notices.
	var nodes int
	if err := raw.QueryRow("SELECT count(*) FROM node WHERE document_id=?", seeded.document.id).
		Scan(&nodes); err != nil {
		t.Fatalf("counting nodes: %v", err)
	}
	if nodes != 1 {
		t.Errorf("%d nodes survived the rebuild, want 1 — the cascade took them", nodes)
	}

	// The point of the rebuild: the widened CHECK accepts the new value, and
	// still refuses one nobody declared.
	if _, err := raw.Exec("UPDATE document SET admission=? WHERE document_id=?",
		string(corpus.RejectedToolOutput), seeded.document.id); err != nil {
		t.Errorf("the rebuilt CHECK refuses %q: %v", corpus.RejectedToolOutput, err)
	}
	if _, err := raw.Exec("UPDATE document SET admission='rejected-vibes' WHERE document_id=?",
		seeded.document.id); err == nil {
		t.Error("the rebuilt CHECK accepts an undeclared admission")
	}
}

type seededDocument struct {
	id, snapshot, path, contentHash, register, split, admission, language string
}

type seededDocumentGraph struct{ document seededDocument }

// seedDocumentGraph writes one snapshot, one document and one node directly,
// because the typed writers target the current schema and this database is one
// version behind it.
//
// The document's short lowercase columns are given DISTINCT values, so a
// transposition among `register`, `split`, `admission` and `language` cannot
// come back looking right.
func seedDocumentGraph(t *testing.T, path string) seededDocumentGraph {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open for seeding: %v", err)
	}
	defer db.Close()

	doc := seededDocument{
		id:          identity.HashBytes([]byte("document")),
		snapshot:    identity.HashBytes([]byte("snapshot")),
		path:        "essays/already-stored.md",
		contentHash: identity.HashBytes([]byte("content")),
		register:    "letters",
		split:       string(corpus.Calibrate),
		admission:   string(corpus.RejectedDuplicate),
		language:    string(corpus.CheckSkippedByPolicy),
	}
	node := identity.HashBytes([]byte("node"))

	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,'2026-09-28T00:00:00Z')",
			[]any{doc.snapshot, identity.HashBytes([]byte("policy"))}},
		{`INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language)
			VALUES (?,?,?,?,?,?,?,?)`,
			[]any{doc.id, doc.snapshot, doc.path, doc.contentHash, doc.register, doc.split,
				doc.admission, doc.language}},
		{`INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion)
			VALUES (?,?,0,'leaf','paragraph','document',0,42,1,'')`,
			[]any{node, doc.id}},
	} {
		if _, err := db.Exec(statement.sql, statement.args...); err != nil {
			t.Fatalf("seeding %q: %v", strings.SplitN(statement.sql, " ", 4)[2], err)
		}
	}
	return seededDocumentGraph{document: doc}
}
