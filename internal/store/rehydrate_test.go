package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// The structured reference
// ---------------------------------------------------------------------------

// A span reference carries the snapshot, the document, the expected content
// hash and a byte range — not an opaque string a caller has to parse.
func TestASpanIsTheStoredReference(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)
	_ = root
	want := store.Span{
		NodeID:      snapshot.Documents[0].Nodes[1].ID,
		DocumentID:  snapshot.Documents[0].ID,
		SnapshotID:  snapshot.ID,
		Path:        "essays/a.md",
		ContentHash: admittedHash(t, bodyA),
		Offset:      50,
		Length:      60,
	}

	got, err := s.Span(ctx(), want.NodeID)
	if err != nil {
		t.Fatalf("Span: %v", err)
	}
	if got != want {
		t.Errorf("span =\n%+v\nwant\n%+v", got, want)
	}
}

// Snapshot identity is location-independent, so the reference may not name a
// directory. Only Rehydrate joins a root, and only the one it was handed.
func TestASpanCarriesARelativePathAndNoRoot(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)

	got, err := s.Span(ctx(), snapshot.Documents[1].Nodes[0].ID)
	if err != nil {
		t.Fatalf("Span: %v", err)
	}
	if strings.HasPrefix(got.Path, "/") || strings.Contains(got.Path, root) {
		t.Errorf("path %q resolves against a root", got.Path)
	}
	if got.Path != "essays/b.md" {
		t.Errorf("path = %q, want the stored relative path", got.Path)
	}
}

func TestASpanForAnUnknownNodeIsNotFound(t *testing.T) {
	s := newStore(t)
	corpusStore(t, s)
	if _, err := s.Span(ctx(), identity.HashBytes([]byte("no such node"))); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// A stored reference the store cannot account for is damage it wrote, not an
// ordinary state, so it is ErrCorrupt on both paths that resolve one.
func TestAStoredReferenceThatDisagreesWithItselfIsCorrupt(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage string
	}{
		// A SECOND snapshot has to exist for this to move anything: pointing a
		// document at the snapshot it already belongs to is a no-op.
		{"a document moved beneath another snapshot",
			"UPDATE document SET snapshot_id = (SELECT id FROM snapshot WHERE id <> " +
				"(SELECT snapshot_id FROM document WHERE path = 'essays/a.md')) WHERE path = 'essays/a.md'"},
		{"a document renamed under its derived key",
			"UPDATE document SET path = 'essays/renamed.md' WHERE path = 'essays/a.md'"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			root, snapshot := corpusStore(t, s)
			elsewhere := snapshotWrite(corpusDocument(t, t.TempDir(), "other/a.md", bodyB,
				text.Span{Offset: 0, Length: 10}))
			mustPutSnapshot(t, s, elsewhere)
			nodeID := snapshot.Documents[0].Nodes[0].ID
			result, err := openRaw(t, s).Exec(c.damage)
			if err != nil {
				t.Fatalf("damaging: %v", err)
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				t.Fatalf("the damage changed %d rows; the case would be vacuous", affected)
			}
			if _, err := s.Span(ctx(), nodeID); !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("Span error = %v, want ErrCorrupt", err)
			}
			if _, err := s.Rehydrate(ctx(), root, []string{nodeID}); !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("Rehydrate error = %v, want ErrCorrupt", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rehydration
// ---------------------------------------------------------------------------

func TestRehydrationReturnsTheAdmittedBytesOfTheSpan(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)
	source := admitted(t, bodyA)

	results, err := s.Rehydrate(ctx(), root, []string{
		snapshot.Documents[0].Nodes[0].ID,
		snapshot.Documents[0].Nodes[1].ID,
	})
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	for i, want := range []string{string(source[0:48]), string(source[50:110])} {
		if results[i].Outcome != store.OutcomeOK {
			t.Fatalf("result %d outcome = %q, want ok", i, results[i].Outcome)
		}
		if results[i].Text != want {
			t.Errorf("result %d text = %q, want %q", i, results[i].Text, want)
		}
	}
}

// The stored hash is over ADMITTED bytes, and text.Admit strips a leading BOM.
// A file that gains or loses one has changed no admitted byte, so rehydration
// must not notice — and an implementation that hashed the file as read would
// report content-changed for a document nobody touched.
func TestRehydrationIsBlindToALeadingBOM(t *testing.T) {
	for _, c := range []struct{ name, stored, onDisk string }{
		{"a BOM appears", bodyA, utf8BOM + bodyA},
		{"a BOM disappears", utf8BOM + bodyA, bodyA},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			root := t.TempDir()
			write := snapshotWrite(corpusDocument(t, root, "essays/a.md", c.stored,
				text.Span{Offset: 0, Length: 48}))
			mustPutSnapshot(t, s, write)
			nodeID := withDerivedIDs(write).Documents[0].Nodes[0].ID
			rewriteFile(t, root, "essays/a.md", c.onDisk)

			results, err := s.Rehydrate(ctx(), root, []string{nodeID})
			if err != nil {
				t.Fatalf("Rehydrate: %v", err)
			}
			if results[0].Outcome != store.OutcomeOK {
				t.Errorf("outcome = %q, want ok", results[0].Outcome)
			}
			if want := string(admitted(t, c.stored)[0:48]); results[0].Text != want {
				t.Errorf("text = %q, want %q", results[0].Text, want)
			}
		})
	}
}

// The closed vocabulary, and what causes each value.
func TestTheOutcomeVocabularyMapsToItsCauses(t *testing.T) {
	for _, c := range []struct {
		name    string
		damage  func(t *testing.T, root string)
		want    store.Outcome
		hasText bool
	}{
		{"the file is still there", func(*testing.T, string) {}, store.OutcomeOK, true},
		{"the path does not resolve", func(t *testing.T, root string) {
			removeFile(t, root, "essays/a.md")
		}, store.OutcomeMissing, false},
		{"reading it fails", func(t *testing.T, root string) {
			replaceWithDirectory(t, root, "essays/a.md")
		}, store.OutcomeUnreadable, false},
		{"the admitted bytes hash differently", func(t *testing.T, root string) {
			rewriteFile(t, root, "essays/a.md", "Entirely different prose of a different length.\n")
		}, store.OutcomeContentChanged, false},
		{"it no longer admits at all", func(t *testing.T, root string) {
			rewriteFile(t, root, "essays/a.md", "Prose interrupted by \xff\xfe invalid bytes.\n")
		}, store.OutcomeContentChanged, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			root, snapshot := corpusStore(t, s)
			c.damage(t, root)

			results, err := s.Rehydrate(ctx(), root, []string{snapshot.Documents[0].Nodes[0].ID})
			if err != nil {
				t.Fatalf("Rehydrate: %v", err)
			}
			if results[0].Outcome != c.want {
				t.Errorf("outcome = %q, want %q", results[0].Outcome, c.want)
			}
			if hasText := results[0].Text != ""; hasText != c.hasText {
				t.Errorf("text = %q, want text: %v", results[0].Text, c.hasText)
			}
		})
	}
}

// Results line up with the request, so a caller can pair them without matching
// on node ID — and a shorter answer is impossible rather than merely unlikely.
func TestRehydrationAnswersInTheOrderItWasAsked(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)
	asked := []string{
		snapshot.Documents[1].Nodes[0].ID,
		snapshot.Documents[0].Nodes[1].ID,
		snapshot.Documents[0].Nodes[0].ID,
	}
	removeFile(t, root, "essays/b.md")

	results, err := s.Rehydrate(ctx(), root, asked)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if len(results) != len(asked) {
		t.Fatalf("%d results for %d requests", len(results), len(asked))
	}
	for i, nodeID := range asked {
		if results[i].NodeID != nodeID {
			t.Errorf("result %d is for %q, want %q", i, results[i].NodeID, nodeID)
		}
	}
	if got := outcomes(results); !reflect.DeepEqual(got, []store.Outcome{
		store.OutcomeMissing, store.OutcomeOK, store.OutcomeOK,
	}) {
		t.Errorf("outcomes = %v", got)
	}
}

// A node that is not stored is a programming error, not an ordinary state: the
// ordinary states are all about the USER's files. Returning a short slice would
// be exactly the silent reduction the exemplar contract forbids.
func TestRehydrationIsAllOrError(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)
	absent := identity.HashBytes([]byte("no such node"))

	results, err := s.Rehydrate(ctx(), root, []string{
		snapshot.Documents[0].Nodes[0].ID, absent,
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if results != nil {
		t.Errorf("results = %v, want nil alongside the error", results)
	}
}

func TestRehydratingNothingIsNotAnError(t *testing.T) {
	s := newStore(t)
	root, _ := corpusStore(t, s)
	results, err := s.Rehydrate(ctx(), root, nil)
	if err != nil || len(results) != 0 {
		t.Errorf("Rehydrate(nil) = %v, %v", results, err)
	}
}

// ---------------------------------------------------------------------------
// Absence is not deletion
// ---------------------------------------------------------------------------

// unavailable_at answers "could this be read at all", so only missing and
// unreadable set it. content-changed means the file WAS read.
func TestOnlyAnUnreadableDocumentIsMarkedUnavailable(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(t *testing.T, root string)
		marked bool
	}{
		{"missing", func(t *testing.T, root string) { removeFile(t, root, "essays/a.md") }, true},
		{"unreadable", func(t *testing.T, root string) { replaceWithDirectory(t, root, "essays/a.md") }, true},
		{"content changed", func(t *testing.T, root string) {
			rewriteFile(t, root, "essays/a.md", "Different prose entirely.\n")
		}, false},
		{"read cleanly", func(*testing.T, string) {}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			root, snapshot := corpusStore(t, s)
			c.damage(t, root)
			if _, err := s.Rehydrate(ctx(), root, []string{snapshot.Documents[0].Nodes[0].ID}); err != nil {
				t.Fatalf("Rehydrate: %v", err)
			}

			unavailable, err := s.Unavailable(ctx(), snapshot.ID)
			if err != nil {
				t.Fatalf("Unavailable: %v", err)
			}
			if _, marked := unavailable["essays/a.md"]; marked != c.marked {
				t.Errorf("marked = %v, want %v", marked, c.marked)
			}
			// The other document was never asked about and must be untouched.
			if _, marked := unavailable["essays/b.md"]; marked {
				t.Error("a document that was not rehydrated was marked")
			}
		})
	}
}

// Marking is not deletion: the artifact graph is all still there afterwards.
func TestAMarkedDocumentKeepsItsWholeGraph(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)
	removeFile(t, root, "essays/a.md")
	if _, err := s.Rehydrate(ctx(), root, []string{snapshot.Documents[0].Nodes[0].ID}); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	stored, err := s.Snapshot(ctx(), snapshot.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(stored.Documents) != len(snapshot.Documents) {
		t.Fatalf("%d documents, want %d", len(stored.Documents), len(snapshot.Documents))
	}
	for i, document := range stored.Documents {
		if len(document.Nodes) != len(snapshot.Documents[i].Nodes) {
			t.Errorf("document %q kept %d nodes, want %d",
				document.Path, len(document.Nodes), len(snapshot.Documents[i].Nodes))
		}
	}
}

// The mark is cleared the first time the document reads back cleanly.
func TestTheMarkIsClearedOnTheFirstCleanRead(t *testing.T) {
	s := newStore(t)
	root, snapshot := corpusStore(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID

	removeFile(t, root, "essays/a.md")
	if _, err := s.Rehydrate(ctx(), root, []string{nodeID}); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	unavailable, err := s.Unavailable(ctx(), snapshot.ID)
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, marked := unavailable["essays/a.md"]; !marked {
		t.Fatal("a missing document was not marked")
	}

	rewriteFile(t, root, "essays/a.md", bodyA)
	if _, err := s.Rehydrate(ctx(), root, []string{nodeID}); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if unavailable, err = s.Unavailable(ctx(), snapshot.ID); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, marked := unavailable["essays/a.md"]; marked {
		t.Error("the mark survived a clean read")
	}
}

// A store with nothing marked says so, rather than reporting an absent snapshot.
func TestUnavailableIsEmptyWhenNothingIsMarked(t *testing.T) {
	s := newStore(t)
	_, snapshot := corpusStore(t, s)
	unavailable, err := s.Unavailable(ctx(), snapshot.ID)
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if len(unavailable) != 0 {
		t.Errorf("unavailable = %v, want empty", unavailable)
	}
}

func TestUnavailableForAnUnknownSnapshotIsNotFound(t *testing.T) {
	s := newStore(t)
	corpusStore(t, s)
	_, err := s.Unavailable(ctx(), identity.HashBytes([]byte("no such snapshot")))
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// A mark records that the document could not be READ. A later read that
// succeeds but finds different content answers a different question, so it must
// not clear the mark — an implementation that cleared on any successful OS read
// would pass every other case here.
func TestAMarkSurvivesAReadThatFindsDifferentContent(t *testing.T) {
	for _, c := range []struct{ name, replacement string }{
		{"different prose", "Entirely different prose of a different length.\n"},
		{"bytes that no longer admit", "Prose interrupted by \xff\xfe invalid bytes.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			root, snapshot := corpusStore(t, s)
			nodeID := snapshot.Documents[0].Nodes[0].ID

			removeFile(t, root, "essays/a.md")
			if _, err := s.Rehydrate(ctx(), root, []string{nodeID}); err != nil {
				t.Fatalf("Rehydrate: %v", err)
			}
			rewriteFile(t, root, "essays/a.md", c.replacement)
			results, err := s.Rehydrate(ctx(), root, []string{nodeID})
			if err != nil {
				t.Fatalf("Rehydrate: %v", err)
			}
			if results[0].Outcome != store.OutcomeContentChanged {
				t.Fatalf("outcome = %q, want content-changed", results[0].Outcome)
			}

			unavailable, err := s.Unavailable(ctx(), snapshot.ID)
			if err != nil {
				t.Fatalf("Unavailable: %v", err)
			}
			if _, marked := unavailable["essays/a.md"]; !marked {
				t.Error("the mark was cleared by a read that found different content")
			}
		})
	}
}

// The narrowed rule made executable: a stored span the store itself cannot
// account for is damage, not one of the four ordinary states.
func TestAStoredSpanTheStoreCannotAccountForIsCorrupt(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage string
	}{
		{"a node whose ordinal no longer derives its key",
			"UPDATE node SET ordinal = 7 WHERE ordinal = 0"},
		{"a span running past the document it came from",
			"UPDATE node SET length = 100000 WHERE ordinal = 0"},
		{"a span starting past the document it came from",
			"UPDATE node SET offset = 100000 WHERE ordinal = 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			root, snapshot := corpusStore(t, s)
			nodeID := snapshot.Documents[0].Nodes[0].ID
			if _, err := openRaw(t, s).Exec(c.damage); err != nil {
				t.Fatalf("damaging: %v", err)
			}
			if _, err := s.Rehydrate(ctx(), root, []string{nodeID}); !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}
