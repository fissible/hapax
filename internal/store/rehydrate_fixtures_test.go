package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

// The prose these tests rehydrate. Long enough that a span is a fragment of a
// document rather than the whole of it.
const (
	bodyA = "The first paragraph carries the span under test.\n\nA second paragraph follows it, so an offset means something.\n"
	bodyB = "A different document entirely, with its own admitted bytes.\n"
)

var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})

// admitted is the byte sequence corpus hashes and node offsets index into. It
// is NOT the file: text.Admit strips a leading BOM, so a file carrying one is
// three bytes longer than this and every offset would be shifted by three.
func admitted(t *testing.T, body string) []byte {
	t.Helper()
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return doc.Raw()
}

func admittedHash(t *testing.T, body string) string {
	t.Helper()
	return identity.HashBytes(admitted(t, body))
}

// corpusDocument writes a real file and returns the store.Document describing
// it, with the content hash and every span taken from the ADMITTED bytes.
func corpusDocument(t *testing.T, root, path, body string, spans ...text.Span) store.Document {
	t.Helper()
	onDisk := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(onDisk), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(onDisk, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	nodes := make([]store.Node, len(spans))
	for i, span := range spans {
		nodes[i] = node(i, span.Offset, span.Length)
	}
	return document(path, admittedHash(t, body), nodes...)
}

// corpusStore writes two documents, stores the snapshot over them, and returns
// the root and the snapshot with its derived IDs filled in.
func corpusStore(t *testing.T, s *store.Store) (string, store.SnapshotWrite) {
	t.Helper()
	root := t.TempDir()
	write := snapshotWrite(
		corpusDocument(t, root, "essays/a.md", bodyA,
			text.Span{Offset: 0, Length: 48},
			text.Span{Offset: 50, Length: 60},
		),
		corpusDocument(t, root, "essays/b.md", bodyB, text.Span{Offset: 2, Length: 20}),
	)
	mustPutSnapshot(t, s, write)
	return root, withDerivedIDs(write)
}

// rewrite replaces a document's file, leaving the stored record alone.
func rewriteFile(t *testing.T, root, path, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(body), 0o644); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
}

func removeFile(t *testing.T, root, path string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
}

// replaceWithDirectory makes a path unreadable in a way that does not depend on
// the process's privileges — a chmod 000 file is still readable as root, which
// CI may be.
func replaceWithDirectory(t *testing.T, root, path string) {
	t.Helper()
	onDisk := filepath.Join(root, filepath.FromSlash(path))
	if err := os.Remove(onDisk); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	if err := os.Mkdir(onDisk, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func outcomes(results []store.Rehydrated) []store.Outcome {
	out := make([]store.Outcome, len(results))
	for i, result := range results {
		out[i] = result.Outcome
	}
	return out
}
