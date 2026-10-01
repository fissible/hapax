package store_test

// #109. The corpus screen needs the whole set of paragraphs this tool published;
// #111's `ProducedByRewrite` answers about a bounded list.
//
// They are two shapes of one question and both callers are real. A plan asks
// about one draft's paragraphs — a handful, known in advance. A corpus screen
// must test every paragraph of every document, and `ProducedByRewrite` binds one
// parameter per hash against the SQLite variable ceiling recorded in its own doc
// comment, so it cannot be the one that answers here.
//
// What this returns is the same fact under the same rule, and the rule changed:
// only PUBLISHED paragraphs, recorded by `Store.RecordPublication` after bytes
// became visible. It used to be "only accepted candidates", which #134 refuted —
// a run that accepted and then failed published nothing, and an acceptance a
// later one superseded never left the process. The contract is stated once, in
// `publication_test.go`.
//
// Unscoped by register and by profile, deliberately. Text this tool published is
// this tool's text whatever register the draft was in, and a byte-identical
// paragraph is a real match — so a `letters` corpus is screened against
// candidates published for `essays` drafts, and should be.

import (
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// The set is the published paragraphs and nothing else.
//
// Four ways of being wrong, each different: a refused candidate, the text a run
// started from, a candidate accepted and never published, and something this
// store has never seen at all.
func TestThePublishedSetHoldsOnlyPublishedParagraphs(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}

	accepted := attemptFixture(prof.ID, nodes[0].ID)
	accepted.CurrentHash = identity.HashBytes([]byte("what the author wrote"))
	accepted.CandidateHash = identity.HashBytes([]byte("what hapax published"))
	accepted.Accepted, accepted.Rejection = true, ""
	if err := s.PutRewriteAttempt(ctx(), accepted); err != nil {
		t.Fatalf("PutRewriteAttempt(accepted): %v", err)
	}
	refused := attemptFixture(prof.ID, nodes[1].ID)
	refused.CurrentHash = identity.HashBytes([]byte("what the author wrote"))
	refused.CandidateHash = identity.HashBytes([]byte("what hapax refused"))
	refused.Accepted, refused.Rejection = false, rewrite.RejectionNotImproved
	if err := s.PutRewriteAttempt(ctx(), refused); err != nil {
		t.Fatalf("PutRewriteAttempt(refused): %v", err)
	}
	stranded := identity.HashBytes([]byte("what a failed run accepted"))
	unpublished := attemptFixture(prof.ID, nodes[1].ID)
	unpublished.Index = 1
	unpublished.CandidateHash = stranded
	unpublished.Accepted, unpublished.Rejection = true, ""
	if err := s.PutRewriteAttempt(ctx(), unpublished); err != nil {
		t.Fatalf("PutRewriteAttempt(unpublished): %v", err)
	}
	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: accepted.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[0].ID, ParagraphHash: accepted.CandidateHash},
		},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	got, err := s.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}

	for _, c := range []struct {
		name, hash string
		want       bool
	}{
		{"a published paragraph", accepted.CandidateHash, true},
		{"a candidate that was refused", refused.CandidateHash, false},
		{"the text a run started from", accepted.CurrentHash, false},
		{"a candidate accepted and never published", stranded, false},
		{"a hash the store never saw", identity.HashBytes([]byte("never seen")), false},
	} {
		if got[c.hash] != c.want {
			t.Errorf("%s: in the published set = %v, want %v", c.name, got[c.hash], c.want)
		}
	}
	if len(got) != 1 {
		t.Errorf("the set holds %d hashes, want 1: %v", len(got), got)
	}
}

// An empty store answers with an empty set, not nil and not an error.
//
// This is the state of every first index: `Index` walks before `commit` opens,
// and `commit` is what creates the store at all. The screen must be able to say
// "performed, nothing published" rather than "not performed", and it cannot do
// that from a nil it has to special-case.
func TestAnEmptyStoreHasAnEmptyPublishedSet(t *testing.T) {
	s := newStore(t)

	got, err := s.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}
	if got == nil {
		t.Error("an empty store returned a nil set; a caller then cannot tell it from " +
			"a screen it never ran")
	}
	if len(got) != 0 {
		t.Errorf("an empty store returned %d hashes: %v", len(got), got)
	}
}

// One text published twice is one member.
//
// Two invocations publishing the same paragraph leave two rows, because the key
// is the invocation and the target. The screen tests membership, so a set that
// counted rows would be answering a different question — and would grow without
// bound on a store that is rewritten often.
func TestTextPublishedTwiceIsOneMember(t *testing.T) {
	s := newStore(t)
	// The profile is no longer needed: publication evidence names an invocation and
	// a node, and no attempt is built here any more.
	snapshot, _ := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	// Guarded like its sibling: without this, the same fixture shrinking gives
	// one clear failure there and an index-out-of-range panic here.
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}
	shared := identity.HashBytes([]byte("published twice"))

	for i, node := range []string{nodes[0].ID, nodes[1].ID} {
		if err := s.RecordPublication(ctx(), store.Publication{
			InvocationID: identity.HashBytes([]byte{byte('a' + i)}),
			Paragraphs:   []store.PublishedParagraph{{NodeID: node, ParagraphHash: shared}},
		}); err != nil {
			t.Fatalf("RecordPublication(%d): %v", i, err)
		}
	}

	got, err := s.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}
	if !got[shared] {
		t.Fatal("the twice-published text is not in the set")
	}
	if len(got) != 1 {
		t.Errorf("the set holds %d hashes for one distinct text: %v", len(got), got)
	}
}
