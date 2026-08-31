package store_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/store"
)

// An audit record keyed by (invocation_id, attempt_index) cannot hold one run
// over two paragraphs.
//
// rewrite.Loop.Rewrite is per-segment: it is handed one paragraph, and its
// Attempt.Index counts attempts WITHIN that paragraph, from zero, every time.
// A composition root that rewrites three paragraphs in one invocation therefore
// produces three attempts numbered zero, which under the old key were one row
// written three times with different payloads — ErrConflict on the second.
//
// Every test that reached this table used a single node, so the whole suite
// passed. This file is what the suite was missing, and the key now carries the
// node the attempt was about.

func TestOneInvocationRecordsEveryParagraphItRewrote(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two paragraphs", len(nodes))
	}

	first := attemptFixture(prof.ID, nodes[0].ID)
	second := attemptFixture(prof.ID, nodes[1].ID)
	second.InvocationID = first.InvocationID
	if first.Index != second.Index {
		t.Fatalf("the fixtures differ at index %d and %d; the collision under test "+
			"is two paragraphs sharing one attempt number", first.Index, second.Index)
	}
	// Distinguishable payloads: identical ones would be accepted by the
	// idempotent replay path and prove nothing about the key.
	second.CurrentHash = identity.HashBytes([]byte("the second paragraph"))

	if err := s.PutRewriteAttempt(ctx(), first); err != nil {
		t.Fatalf("first paragraph: %v", err)
	}
	if err := s.PutRewriteAttempt(ctx(), second); err != nil {
		t.Fatalf("second paragraph of the same invocation: %v", err)
	}

	for _, want := range []store.RewriteAttempt{first, second} {
		got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.NodeID, want.Index)
		if err != nil {
			t.Fatalf("LoadRewriteAttempt(%s): %v", want.NodeID, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("attempt for node %s =\n%+v\nwant\n%+v", want.NodeID, got, want)
		}
	}
}

// Loading is by the whole key. Naming an invocation and a node that never
// appeared together is a miss, not somebody else's row.
func TestAnAttemptIsFoundOnlyUnderItsOwnNode(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}

	written := attemptFixture(prof.ID, nodes[0].ID)
	if err := s.PutRewriteAttempt(ctx(), written); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}

	_, err := s.LoadRewriteAttempt(ctx(), written.InvocationID, nodes[1].ID, written.Index)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("loading a node this invocation never touched gave %v, want ErrNotFound", err)
	}
}

// The idempotent replay survives the wider key: it is what lets a caller record
// the same decision twice without the store deciding that is a conflict.
func TestReplayingTheSameAttemptIsStillAcceptedAndDiffersFromAConflict(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)

	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Errorf("an identical replay was refused: %v", err)
	}

	changed := attempt
	changed.CandidateHash = identity.HashBytes([]byte("a different candidate"))
	if err := s.PutRewriteAttempt(ctx(), changed); !errors.Is(err, store.ErrConflict) {
		t.Errorf("rewriting a stored decision gave %v, want ErrConflict", err)
	}
}

// The identifier child table is keyed on the same triple. Under the old key,
// two paragraphs' identifiers landed in one ordinal sequence — so a preserve
// verdict about one paragraph could be read back as a verdict about another.
func TestPreserveIdentifiersBelongToOneParagraphEach(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}

	first := attemptFixture(prof.ID, nodes[0].ID)
	first.PreserveIdentifiers = []string{identifierFor(preserve.ClassNumber, preserve.DirectionLost, "1979")}
	second := attemptFixture(prof.ID, nodes[1].ID)
	second.InvocationID = first.InvocationID
	second.PreserveIdentifiers = []string{identifierFor(preserve.ClassURL, preserve.DirectionInvented, "example.com")}

	if err := s.PutRewriteAttempt(ctx(), first); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.PutRewriteAttempt(ctx(), second); err != nil {
		t.Fatalf("second: %v", err)
	}

	for _, want := range []store.RewriteAttempt{first, second} {
		got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.NodeID, want.Index)
		if err != nil {
			t.Fatalf("LoadRewriteAttempt(%s): %v", want.NodeID, err)
		}
		if !reflect.DeepEqual(got.PreserveIdentifiers, want.PreserveIdentifiers) {
			t.Errorf("node %s carries identifiers %v, want %v",
				want.NodeID, got.PreserveIdentifiers, want.PreserveIdentifiers)
		}
	}
}

// Deleting one paragraph's node takes that paragraph's attempt and its
// identifiers, and leaves the other paragraph's alone. The cascade is per
// attempt, not per invocation.
func TestDroppingOneParagraphsNodeLeavesTheOtherAttemptStanding(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}

	first := attemptFixture(prof.ID, nodes[0].ID)
	second := attemptFixture(prof.ID, nodes[1].ID)
	second.InvocationID = first.InvocationID
	second.CurrentHash = identity.HashBytes([]byte("the second paragraph"))
	for _, attempt := range []store.RewriteAttempt{first, second} {
		if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
			t.Fatalf("PutRewriteAttempt(%s): %v", attempt.NodeID, err)
		}
	}

	raw := openRaw(t, s)
	if _, err := raw.Exec("PRAGMA foreign_keys=1"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM node WHERE node_id=?", first.NodeID); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	if _, err := s.LoadRewriteAttempt(ctx(), first.InvocationID, first.NodeID, first.Index); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the deleted node's attempt gave %v, want ErrNotFound", err)
	}
	if _, err := s.LoadRewriteAttempt(ctx(), second.InvocationID, second.NodeID, second.Index); err != nil {
		t.Errorf("the surviving node's attempt went with it: %v", err)
	}

	var orphans int
	if err := raw.QueryRow("SELECT count(*) FROM rewrite_attempt_identifier WHERE node_id=?",
		first.NodeID).Scan(&orphans); err != nil {
		t.Fatalf("count identifiers: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d identifier rows outlived the attempt they belonged to", orphans)
	}
}
