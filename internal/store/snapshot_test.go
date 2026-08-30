package store_test

import (
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

// A snapshot, its documents, their nodes and those nodes' vectors are one
// transaction. A partial graph is indistinguishable from a small corpus, which
// is the failure this rule exists to make impossible rather than unlikely.

func TestASnapshotRoundTrips(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(
		document("essays/a.md", hashA, node(0, 0, 12), node(1, 14, 20)),
		document("essays/b.md", hashB, node(0, 0, 9)),
	)
	mustPutSnapshot(t, s, write)

	got, err := s.Snapshot(ctx(), write.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if want := withDerivedIDs(write); !reflect.DeepEqual(got, want) {
		t.Errorf("round trip differs:\n%+v\nwant\n%+v", got, want)
	}
	// And the caller's aggregate is untouched: a write that filled in IDs would
	// be modifying data it was only given to read.
	if write.Documents[0].ID != "" || write.Documents[0].Nodes[0].ID != "" {
		t.Error("PutSnapshot wrote derived identities into the caller's aggregate")
	}
}

// Nothing partial is ever readable: a write that fails on its last node leaves
// no snapshot at all, not a snapshot with fewer nodes.
func TestAFailedAggregateWriteLeavesNothing(t *testing.T) {
	s := newStore(t)
	bad := document("essays/b.md", hashB, node(0, 0, 9))
	bad.Nodes = append(bad.Nodes, store.Node{Ordinal: 1, Kind: "not-a-kind", Offset: 10, Length: 2})
	write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12)), bad)

	if err := s.PutSnapshot(ctx(), write); err == nil {
		t.Fatal("accepted a node with an unknown kind")
	}
	if _, err := s.Snapshot(ctx(), write.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound — a partial graph survived", err)
	}
}

// Writing the same aggregate twice is safe; writing a different one under the
// same identity is corruption, not an update.
func TestRewritingAnIdenticalSnapshotSucceedsAndADifferentOneConflicts(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12)))
	mustPutSnapshot(t, s, write)

	if err := s.PutSnapshot(ctx(), write); err != nil {
		t.Errorf("an identical rewrite failed: %v", err)
	}

	// Membership is only part of the content. A node span or a vector value can
	// differ while every path and content hash is identical, and a store that
	// overwrote those under the same identity would lose the original silently.
	for _, c := range []struct {
		name    string
		changed store.SnapshotWrite
	}{
		{"different document hash", snapshotWrite(document("essays/a.md", hashB, node(0, 0, 12)))},
		{"different node span", snapshotWrite(document("essays/a.md", hashA, node(0, 0, 20)))},
		{"an extra node", snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12), node(1, 14, 6)))},
		{"a vector where there was none", func() store.SnapshotWrite {
			leaf := node(0, 0, 12)
			leaf.Vector = &features.Vector{SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: vectorValues()}
			return snapshotWrite(document("essays/a.md", hashA, leaf))
		}()},
	} {
		t.Run(c.name, func(t *testing.T) {
			changed := c.changed
			changed.ID = write.ID
			if err := s.PutSnapshot(ctx(), changed); !errors.Is(err, store.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict", err)
			}
			// And the original must be untouched: a conflict that half-applied
			// would be worse than one that overwrote.
			got, err := s.Snapshot(ctx(), write.ID)
			if err != nil {
				t.Fatalf("Snapshot after conflict: %v", err)
			}
			if want := withDerivedIDs(write); !reflect.DeepEqual(got, want) {
				t.Errorf("the stored aggregate changed after a refused write:\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// Derived, not surrogate. Comparing two fresh databases is not enough — both
// would assign 1 to their first row and agree. The preimages are declared in
// DESIGN, so the expected values are computed here from them.
func TestDocumentAndNodeIdentitiesAreTheDeclaredDigests(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12), node(1, 14, 6)))
	mustPutSnapshot(t, s, write)

	got, err := s.Snapshot(ctx(), write.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	wantDocument := identity.HashInputs(map[string]string{"snapshot": write.ID, "path": "essays/a.md"})
	if got.Documents[0].ID != wantDocument {
		t.Errorf("document ID = %q, want %q", got.Documents[0].ID, wantDocument)
	}
	for i, leaf := range got.Documents[0].Nodes {
		want := identity.HashInputs(map[string]string{"document": wantDocument, "ordinal": strconv.Itoa(i)})
		if leaf.ID != want {
			t.Errorf("node %d ID = %q, want %q", i, leaf.ID, want)
		}
	}
}

// And a stored ID that disagrees with its own preimage is corruption, not a
// row to be read and trusted.
func TestAStoredIdentityThatDisagreesWithItsKeyIsCorrupt(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12)))
	mustPutSnapshot(t, s, write)

	db := openRaw(t, s)
	if _, err := db.Exec("UPDATE document SET path = 'essays/renamed.md'"); err != nil {
		t.Fatalf("corrupting: %v", err)
	}
	if got, err := s.Snapshot(ctx(), write.ID); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt (read %+v)", err, got)
	}
}

// A feature vector belongs to a node and a manifest. Reading it back must give
// the same values, including which were undefined and why.
func TestAFeatureVectorRoundTripsWithItsUndefinedValues(t *testing.T) {
	s := newStore(t)
	leaf := node(0, 0, 12)
	leaf.Vector = &features.Vector{
		SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7,
		Values: vectorValues(),
	}
	write := snapshotWrite(document("essays/a.md", hashA, leaf))
	mustPutSnapshot(t, s, write)

	got, err := s.Snapshot(ctx(), write.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !reflect.DeepEqual(got.Documents[0].Nodes[0].Vector, leaf.Vector) {
		t.Errorf("vector round trip differs:\n%+v\nwant\n%+v", got.Documents[0].Nodes[0].Vector, leaf.Vector)
	}
}

// Corruption must never be able to present as insufficient evidence, which is a
// verdict this system emits and would therefore be believed.
func TestCorruptionIsNotInsufficientEvidence(t *testing.T) {
	// Every mutation here must be one the SCHEMA CANNOT PREVENT — otherwise the
	// write-side CHECK rejects it and the read-side rule is untestable. An
	// earlier version used role = 'sonnet' and document_id = 'ffff', both of
	// which the constraints refuse, so the two suites contradicted each other.
	// What a CHECK cannot express is a cross-column derivation: an ID that no
	// longer matches its own preimage, or a well-formed reference to something
	// that is not there.
	for _, c := range []struct {
		name    string
		corrupt string
	}{
		{"a non-finite float", "UPDATE feature_value SET value = 'NaN'"},
		{"a well-formed reference to a document that does not exist",
			"UPDATE node SET document_id = '" + identity.HashBytes([]byte("no such document")) + "'"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			leaf := node(0, 0, 12)
			leaf.Vector = &features.Vector{SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: vectorValues()}
			write := snapshotWrite(document("essays/a.md", hashA, leaf))
			mustPutSnapshot(t, s, write)

			db := openRaw(t, s)
			if _, err := db.Exec(c.corrupt); err != nil {
				t.Fatalf("corrupting: %v", err)
			}

			got, err := s.Snapshot(ctx(), write.ID)
			if err == nil {
				t.Fatalf("read a corrupt snapshot as valid: %+v", got)
			}
			if !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}

// Moving a node beneath a DIFFERENT REAL document must be rejected as a
// parent mismatch, not merely produce a smaller tree — the node's derived ID no
// longer agrees with the document it now sits under.
func TestANodeMovedBeneathAnotherDocumentIsCorrupt(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(
		document("essays/a.md", hashA, node(0, 0, 12)),
		document("essays/b.md", hashB, node(0, 0, 9)),
	)
	mustPutSnapshot(t, s, write)

	other := identity.HashInputs(map[string]string{"snapshot": write.ID, "path": "essays/b.md"})
	first := identity.HashInputs(map[string]string{"snapshot": write.ID, "path": "essays/a.md"})
	db := openRaw(t, s)
	if _, err := db.Exec("UPDATE node SET document_id = ? WHERE document_id = ?", other, first); err != nil {
		t.Fatalf("corrupting: %v", err)
	}

	got, err := s.Snapshot(ctx(), write.ID)
	if err == nil {
		t.Fatalf("read a tree whose node sits under the wrong document: %+v", got)
	}
	if !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// (8) The vector's feature closure: exactly the manifest, once each, in order.
func TestAVectorMustCoverTheManifestExactly(t *testing.T) {
	for _, c := range []struct {
		name    string
		corrupt string
	}{
		{"a missing feature", "DELETE FROM feature_value WHERE feature = (SELECT feature FROM feature_value LIMIT 1)"},
		{"an unknown feature", "UPDATE feature_value SET feature = 'invented' WHERE rowid = (SELECT min(rowid) FROM feature_value)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			leaf := node(0, 0, 12)
			leaf.Vector = &features.Vector{SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: vectorValues()}
			write := snapshotWrite(document("essays/a.md", hashA, leaf))
			mustPutSnapshot(t, s, write)

			db := openRaw(t, s)
			if _, err := db.Exec(c.corrupt); err != nil {
				t.Fatalf("corrupting: %v", err)
			}
			if got, err := s.Snapshot(ctx(), write.ID); !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt (read %+v)", err, got)
			}
		})
	}
}

// Node fields are validated on the way in, not discovered on the way out.
func TestNodeFieldsAreValidatedOnWrite(t *testing.T) {
	for _, c := range []struct {
		name   string
		break_ func(n *store.Node)
	}{
		{"unknown kind", func(n *store.Node) { n.Kind = "stanza" }},
		{"unknown role", func(n *store.Node) { n.Role = "sonnet" }},
		{"unknown container", func(n *store.Node) { n.Containers = []text.ContainerKind{"chapter"} }},
		{"negative offset", func(n *store.Node) { n.Offset = -1 }},
		{"negative length", func(n *store.Node) { n.Length = -1 }},
		{"zero length", func(n *store.Node) { n.Length = 0 }},
		{"negative ordinal", func(n *store.Node) { n.Ordinal = -1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			leaf := node(0, 0, 12)
			c.break_(&leaf)
			if err := newStore(t).PutSnapshot(ctx(), snapshotWrite(document("essays/a.md", hashA, leaf))); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Ordinals are the node's identity within a document, so a gap or a repeat is
// not a smaller document — it is a document that cannot be addressed.
func TestNodeOrdinalsMustBeContiguousFromZero(t *testing.T) {
	for _, c := range []struct {
		name  string
		nodes []store.Node
	}{
		{"a gap", []store.Node{node(0, 0, 4), node(2, 6, 4)}},
		{"a repeat", []store.Node{node(0, 0, 4), node(0, 6, 4)}},
		{"not starting at zero", []store.Node{node(1, 0, 4)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := newStore(t).PutSnapshot(ctx(), snapshotWrite(document("essays/a.md", hashA, c.nodes...))); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Two writers racing on the same identity: the loser rereads inside its own
// transaction and succeeds, because an identical retry is safe. Returning
// ErrConflict on the collision itself would fail it.
func TestConcurrentIdenticalWritersBothSucceed(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12)))

	// Separate Store handles and a start barrier, so this races on SQLite rather
	// than on one process's mutex.
	stores := make([]*store.Store, 8)
	for i := range stores {
		opened, err := store.Open(s.Path())
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		t.Cleanup(func() { _ = opened.Close() })
		stores[i] = opened
	}
	start := make(chan struct{})
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(w *store.Store) {
			<-start
			errs <- w.PutSnapshot(ctx(), write)
		}(stores[i])
	}
	close(start)
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Errorf("writer %d failed on an identical write: %v", i, err)
		}
	}
	got, err := s.Snapshot(ctx(), write.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got.Documents) != 1 || len(got.Documents[0].Nodes) != 1 {
		t.Errorf("eight identical writers produced %d documents", len(got.Documents))
	}
}

func vectorValues() []features.FeatureValue {
	out := make([]features.FeatureValue, 0, len(features.Definitions()))
	for i, definition := range features.Definitions() {
		value := features.FeatureValue{ID: definition.ID}
		// Leave one undefined, so the round trip covers both states.
		if i != 2 {
			value.Value, value.Defined = float64(i)+0.5, true
			value.SamplingVariance, value.SamplingVarianceDefined = 0.25, true
		}
		out = append(out, value)
	}
	return out
}

var (
	_ = corpus.Train
	_ = text.KindLeaf
)

// The snapshot identity is verified, not trusted. corpus computes it, but a
// store that accepted any ID could not enforce read integrity for the one
// artifact everything else hangs from.
func TestASnapshotIdentityThatDoesNotMatchItsMembershipIsRefused(t *testing.T) {
	write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12)))
	write.ID = identity.HashInputs(map[string]string{"snapshot": "something-else"})

	if err := newStore(t).PutSnapshot(ctx(), write); err == nil {
		t.Error("accepted a snapshot whose ID does not hash its membership")
	}
}

// And membership edited underneath a stored snapshot is corruption on read.
func TestMembershipEditedUnderAStoredSnapshotIsCorrupt(t *testing.T) {
	s := newStore(t)
	write := snapshotWrite(
		document("essays/a.md", hashA, node(0, 0, 12)),
		document("essays/b.md", hashB, node(0, 0, 9)),
	)
	mustPutSnapshot(t, s, write)

	db := openRaw(t, s)
	if _, err := db.Exec("DELETE FROM node WHERE document_id = ?",
		identity.HashInputs(map[string]string{"snapshot": write.ID, "path": "essays/b.md"})); err != nil {
		t.Fatalf("corrupting: %v", err)
	}
	if _, err := db.Exec("DELETE FROM document WHERE path = 'essays/b.md'"); err != nil {
		t.Fatalf("corrupting: %v", err)
	}
	if got, err := s.Snapshot(ctx(), write.ID); !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt (read %+v)", err, got)
	}
}

// Foreign-key ENFORCEMENT is a property of the connection, and this slice
// exposes no operation that could violate one — PutSnapshot validates before it
// inserts. So what is testable here is that the constraints are DECLARED, which
// the schema test does. Enforcement gets its observable consequence with the
// cascade Prune relies on, in the slice that adds it. Recording the gap rather
// than writing a test that proves nothing while appearing to.

// Duplicates and ordering, which the missing/unknown cases do not cover.
func TestAVectorRejectsDuplicateFeaturesAndIsOrderIndependent(t *testing.T) {
	t.Run("duplicate on write", func(t *testing.T) {
		leaf := node(0, 0, 12)
		values := vectorValues()
		values = append(values, values[0])
		leaf.Vector = &features.Vector{SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: values}
		if err := newStore(t).PutSnapshot(ctx(), snapshotWrite(document("essays/a.md", hashA, leaf))); err == nil {
			t.Error("accepted a vector naming one feature twice")
		}
	})

	t.Run("read is manifest order regardless of row order", func(t *testing.T) {
		s := newStore(t)
		leaf := node(0, 0, 12)
		leaf.Vector = &features.Vector{SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: vectorValues()}
		write := snapshotWrite(document("essays/a.md", hashA, leaf))
		mustPutSnapshot(t, s, write)

		// Actually reverse the rows. Inserting in manifest order and reading
		// back in manifest order would pass with no ordering at all.
		db := openRaw(t, s)
		if _, err := db.Exec(
			"CREATE TEMP TABLE reversed AS SELECT * FROM feature_value ORDER BY feature DESC",
		); err != nil {
			t.Fatalf("reordering: %v", err)
		}
		if _, err := db.Exec("DELETE FROM feature_value"); err != nil {
			t.Fatalf("reordering: %v", err)
		}
		if _, err := db.Exec("INSERT INTO feature_value SELECT * FROM reversed"); err != nil {
			t.Fatalf("reordering: %v", err)
		}

		got, err := s.Snapshot(ctx(), write.ID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		for i, value := range got.Documents[0].Nodes[0].Vector.Values {
			if value.ID != features.Definitions()[i].ID {
				t.Errorf("value %d is %q, want manifest order %q", i, value.ID, features.Definitions()[i].ID)
			}
		}
	})
}

// The remaining textual columns this slice writes.
func TestTheRemainingTextualColumnsHaveGrammars(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(w *store.SnapshotWrite)
	}{
		{"a policy digest that is not hex", func(w *store.SnapshotWrite) { w.PolicyDigest = "policy" }},
		{"an unknown admission", func(w *store.SnapshotWrite) { w.Documents[0].Admission = "maybe" }},
		{"a language that is not a tag", func(w *store.SnapshotWrite) { w.Documents[0].Language = "English, mostly" }},
		{"an unknown exclusion", func(w *store.SnapshotWrite) { w.Documents[0].Nodes[0].Exclusion = "felt wrong" }},
		{"a feature set version that is not the manifest's", func(w *store.SnapshotWrite) {
			w.Documents[0].Nodes[0].Vector = &features.Vector{SetVersion: features.SetVersion + 1, Values: vectorValues()}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			write := snapshotWrite(document("essays/a.md", hashA, node(0, 0, 12)))
			c.mutate(&write)
			write.ID = snapshotID(write.PolicyDigest, write.Documents)
			if err := newStore(t).PutSnapshot(ctx(), write); err == nil {
				t.Error("accepted")
			}
		})
	}
}
