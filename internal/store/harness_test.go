package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

var (
	hashA = identity.HashBytes([]byte("document a"))
	hashB = identity.HashBytes([]byte("document b"))
)

func ctx() context.Context { return context.Background() }

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hapax.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// openRaw is a second connection, for asserting things about the schema that no
// API should expose.
func openRaw(t *testing.T, s *store.Store) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", s.Path())
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// corpusRoot writes files a span reference can be rehydrated against.
func corpusRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func node(ordinal int, offset, length int) store.Node {
	return store.Node{
		Ordinal: ordinal, Kind: text.KindLeaf, Role: text.RoleParagraph,
		Containers: []text.ContainerKind{text.ContainerDocument},
		Offset:     offset, Length: length, Included: true,
	}
}

func document(path, hash string, nodes ...store.Node) store.Document {
	return store.Document{
		Path: path, ContentHash: hash, Register: "essays",
		Split: corpus.Train, Admission: corpus.Eligible, Language: "en",
		Nodes: nodes,
	}
}

// snapshotWrite computes the declared snapshot identity from its own membership,
// so the fixture cannot hand the store an ID unrelated to what it contains.
func snapshotWrite(documents ...store.Document) store.SnapshotWrite {
	policy := identity.HashInputs(map[string]string{"policy": "under-test"})
	return store.SnapshotWrite{ID: snapshotID(policy, documents), PolicyDigest: policy, Documents: documents}
}

func snapshotID(policyDigest string, documents []store.Document) string {
	members := make([]string, 0, len(documents))
	for _, doc := range documents {
		members = append(members, doc.Path+"="+doc.ContentHash)
	}
	sort.Strings(members)
	return identity.HashInputs(map[string]string{
		"policy": policyDigest, "documents": string(identity.Frame(members...)),
	})
}

func mustPutSnapshot(t *testing.T, s *store.Store, write store.SnapshotWrite) {
	t.Helper()
	if err := s.PutSnapshot(ctx(), write); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Grammar probes, used by the allowlist table
// ---------------------------------------------------------------------------

func writeDocumentPath(path string) func(*testing.T) error {
	return func(t *testing.T) error {
		return newStore(t).PutSnapshot(ctx(), snapshotWrite(document(path, hashA, node(0, 0, 4))))
	}
}

func writeDocumentHash(hash string) func(*testing.T) error {
	return func(t *testing.T) error {
		return newStore(t).PutSnapshot(ctx(), snapshotWrite(document("a.md", hash, node(0, 0, 4))))
	}
}

func writeDocumentRegister(register string) func(*testing.T) error {
	return func(t *testing.T) error {
		doc := document("a.md", hashA, node(0, 0, 4))
		doc.Register = register
		return newStore(t).PutSnapshot(ctx(), snapshotWrite(doc))
	}
}

func writeDocumentSplit(split string) func(*testing.T) error {
	return func(t *testing.T) error {
		doc := document("a.md", hashA, node(0, 0, 4))
		doc.Split = corpus.Split(split)
		return newStore(t).PutSnapshot(ctx(), snapshotWrite(doc))
	}
}

// ---------------------------------------------------------------------------
// Concurrency helpers
// ---------------------------------------------------------------------------

const shortDeadline = 250 * time.Millisecond

func ctxBackground() context.Context { return context.Background() }

func shortContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), shortDeadline)
}

// concurrentWrite is the one aggregate every writer process writes, so an
// identical write is what races.
func concurrentWrite() store.SnapshotWrite {
	policy := identity.HashInputs(map[string]string{"policy": "concurrent"})
	documents := []store.Document{{
		Path: "essays/a.md", ContentHash: hashA, Register: "essays",
		Split: corpus.Train, Admission: corpus.Eligible, Language: "en",
		Nodes: []store.Node{{
			Ordinal: 0, Kind: text.KindLeaf, Role: text.RoleParagraph,
			Containers: []text.ContainerKind{text.ContainerDocument},
			Offset:     0, Length: 12, Included: true,
		}},
	}}
	return store.SnapshotWrite{ID: snapshotID(policy, documents), PolicyDigest: policy, Documents: documents}
}

// sortFKs makes the grouped foreign-key comparison order-independent without
// losing the grouping itself.
func sortFKs[T any](fks []T) {
	sort.Slice(fks, func(i, j int) bool {
		return fmt.Sprintf("%+v", fks[i]) < fmt.Sprintf("%+v", fks[j])
	})
}

// withDerivedIDs is what a round trip should equal. PutSnapshot does not fill
// these in — it must not modify its caller's aggregate — so the expectation is
// built here from the declared preimages rather than read out of the input.
func withDerivedIDs(write store.SnapshotWrite) store.SnapshotWrite {
	out := write
	out.Documents = make([]store.Document, len(write.Documents))
	for i, doc := range write.Documents {
		doc.ID = identity.HashInputs(map[string]string{"snapshot": write.ID, "path": doc.Path})
		nodes := make([]store.Node, len(doc.Nodes))
		for j, leaf := range doc.Nodes {
			leaf.ID = identity.HashInputs(map[string]string{
				"document": doc.ID, "ordinal": strconv.Itoa(leaf.Ordinal),
			})
			nodes[j] = leaf
		}
		doc.Nodes = nodes
		out.Documents[i] = doc
	}
	return out
}
