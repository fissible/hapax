package store_test

// #111. Every guard anchors on the paragraph the invocation started from, and
// `hapax rewrite --in-place` makes that the PREVIOUS run's output. Each step is
// individually admissible and the composition is not.
//
// Measured against the shipped script guard, ceiling 0.05 and established 0.25:
//
//	original     letters= 22 Han= 1 share=0.0455
//	after run 1  letters=132 Han= 6 share=0.0455   admissible
//	after run 2  letters= 25 Han= 6 share=0.2400   admissible against run 1's output
//	                                               REFUSED against the true original
//
// # Why this is a hash lookup and not a durable anchor
//
// The issue proposed recovering "the earliest recorded state of that paragraph".
// Two measured facts rule that out.
//
// No prose is stored — that is this package's central invariant, enforced column
// by column next door in `allowlist_test.go`. `document` holds a path and a
// content hash, `node` holds an offset and a length, and `rewrite_attempt` holds
// `current_hash` and `candidate_hash`. `preserve.Check` and `text.Scripts` need
// BYTES, which the store cannot give them without becoming the thing the
// allowlist exists to prevent.
//
// And `node_id` is not a durable paragraph identity:
//
//	snapshot.ID = H(policy, [path=content_hash, ...])   every document's hash
//	document.ID = H(snapshot.ID, path)
//	node.ID     = H(document.ID, ordinal)
//
// A change to ANY document in the corpus changes every node_id, so a paragraph
// cannot be followed across an in-place rewrite by id. The derivation above IS
// the proof; `TestAHashOutlivesTheNodeItWasRecordedAgainst` exercises it.
//
// What IS durable is the hash of the paragraph's own bytes: content-addressed,
// independent of corpus and snapshot state, and already recorded for every
// candidate this tool ever produced. So the question this answers is not "what
// did this paragraph used to say" but "is this paragraph text I wrote", which
// needs no prose and no stable id.
//
// # No index, deliberately
//
// `rewrite_attempt` carries no index on `candidate_hash`, so this is a table
// scan, once per plan. The table grows by one row per attempt per paragraph per
// invocation, and a plan already reads a snapshot, a profile, a reference and a
// release before it gets here. An index would be invisible against that, and is
// not declared here — which matters, because
// `allowlist_test.go` declares COLUMNS and says nothing about indexes:
// `TestNoIndexViewOrTriggerDerivesText` would permit one, so adding it later
// needs no amendment there and would go unremarked. Stated rather than left
// silent, because silence in this package reads as "not considered".
//
// # Only PUBLISHED paragraphs, which is not what this originally said
//
// It said "only accepted candidates", and #134 is that an accepted candidate is
// not a published paragraph: `RecordAttempt` fires per attempt inside the loop,
// so a run that accepted a candidate and then failed leaves one behind having
// written nothing, and a run with three acceptances published the last. The
// contract now lives on `Store.RecordPublication`, in `publication_test.go`, and
// is not restated here.
//
// What survives unchanged: a `current_hash` is the text a run STARTED from, which
// is the author's — or an earlier run's, in which case that earlier run's own
// publication carries the same hash. Counting it would refuse paragraphs this
// tool never emitted.

import (
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// An accepted candidate's hash is recognized; nothing else is.
//
// Table-driven over the four hashes one attempt puts in the store, because the
// three negatives are each a different way to be wrong: the input text, a
// candidate that was refused, and a hash the store never saw.
func TestOnlyAPublishedParagraphCountsAsRewriteOutput(t *testing.T) {
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

	// Accepted AND published, which is what the screen is about. Without this
	// the test asserts the equation #134 refuted.
	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: accepted.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[0].ID, ParagraphHash: accepted.CandidateHash},
		},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	// Accepted and never published: a run that failed after an acceptance, or an
	// acceptance a later one superseded.
	stranded := identity.HashBytes([]byte("what a failed run accepted"))
	unpublished := attemptFixture(prof.ID, nodes[1].ID)
	unpublished.Index = 1
	unpublished.CandidateHash = stranded
	unpublished.Accepted, unpublished.Rejection = true, ""
	if err := s.PutRewriteAttempt(ctx(), unpublished); err != nil {
		t.Fatalf("PutRewriteAttempt(unpublished): %v", err)
	}

	unknown := identity.HashBytes([]byte("a paragraph this store never saw"))

	got, err := s.ProducedByRewrite(ctx(), []string{
		accepted.CandidateHash, refused.CandidateHash, accepted.CurrentHash, stranded, unknown,
	})
	if err != nil {
		t.Fatalf("ProducedByRewrite: %v", err)
	}

	for _, c := range []struct {
		name, hash string
		want       bool
	}{
		{"a published paragraph", accepted.CandidateHash, true},
		{"a candidate that was refused, and so never published", refused.CandidateHash, false},
		{"the text a run started from", accepted.CurrentHash, false},
		{"a candidate that was accepted and never published", stranded, false},
		{"a hash the store never saw", unknown, false},
	} {
		if got[c.hash] != c.want {
			t.Errorf("%s: ProducedByRewrite = %v, want %v", c.name, got[c.hash], c.want)
		}
	}
}

// The answer covers every hash asked about and invents none.
//
// A map is not a list: an implementation returning only the matches would satisfy
// every assertion above, since a missing key reads as false. That is a defensible
// API but it is not this one, because a caller cannot then tell "no" from "I did
// not look" — and this caller asks about paragraphs it is about to decide the
// fate of.
func TestTheAnswerNamesEveryHashItWasAskedAbout(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	nodes := storedGraph(t, s).Documents[0].Nodes

	accepted := attemptFixture(prof.ID, nodes[0].ID)
	accepted.Accepted, accepted.Rejection = true, ""
	if err := s.PutRewriteAttempt(ctx(), accepted); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: accepted.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[0].ID, ParagraphHash: accepted.CandidateHash},
		},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}
	asked := []string{
		accepted.CandidateHash,
		identity.HashBytes([]byte("one")),
		identity.HashBytes([]byte("two")),
	}

	got, err := s.ProducedByRewrite(ctx(), asked)
	if err != nil {
		t.Fatalf("ProducedByRewrite: %v", err)
	}

	if len(got) != len(asked) {
		t.Errorf("the answer holds %d entries for %d hashes: %v", len(got), len(asked), got)
	}
	for _, hash := range asked {
		if _, present := got[hash]; !present {
			t.Errorf("no entry for %s", hash)
		}
	}
}

// Asking about nothing is not an error, and reads the database no more than it
// has to.
//
// The caller asks once per plan, and a draft whose every paragraph was filtered
// out earlier asks about none. An implementation that built `IN ()` would fail
// here rather than in a plan.
func TestAskingAboutNoHashesIsNotAnError(t *testing.T) {
	s := newStore(t)

	got, err := s.ProducedByRewrite(ctx(), nil)
	if err != nil {
		t.Fatalf("ProducedByRewrite(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ProducedByRewrite(nil) = %v, want empty", got)
	}
	if got, err = s.ProducedByRewrite(ctx(), []string{}); err != nil {
		t.Fatalf("ProducedByRewrite(empty): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ProducedByRewrite(empty) = %v, want empty", got)
	}
}

// A hash outlives the node identity it was recorded against.
//
// This is the property that makes a hash the right key, and the signature alone
// does not establish it — an earlier draft asserted only that the query takes no
// node argument, which the compiler already guarantees, and whose single
// assertion was case one of the test above repeated.
//
// A second snapshot holding one more document changes `snapshot.ID`, and every
// `document.ID` and `node.ID` under it, because the snapshot id is derived from
// every member's path and content hash. That is exactly what an edit to the
// corpus does between two rewrite runs. The recorded hash must still answer.
func TestAHashOutlivesTheNodeItWasRecordedAgainst(t *testing.T) {
	s := newStore(t)
	first, prof := seededProfile(t, s)

	accepted := attemptFixture(prof.ID, first.Documents[0].Nodes[0].ID)
	accepted.Accepted, accepted.Rejection = true, ""
	if err := s.PutRewriteAttempt(ctx(), accepted); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: accepted.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: first.Documents[0].Nodes[0].ID, ParagraphHash: accepted.CandidateHash},
		},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	// One more document, which renumbers everything beneath it.
	second := withDerivedIDs(snapshotWrite(
		document("essays/a.md", hashA, node(0, 0, 12), node(1, 12, 20)),
		document("essays/b.md", hashB, node(0, 0, 30)),
		document("essays/c.md", identity.HashBytes([]byte("a later document")), node(0, 0, 18)),
	))
	mustPutSnapshot(t, s, second)
	if second.Documents[0].Nodes[0].ID == first.Documents[0].Nodes[0].ID {
		t.Fatal("the second snapshot did not renumber the nodes, so this proves nothing")
	}

	got, err := s.ProducedByRewrite(ctx(), []string{accepted.CandidateHash})
	if err != nil {
		t.Fatalf("ProducedByRewrite: %v", err)
	}
	if !got[accepted.CandidateHash] {
		t.Error("a hash recorded before the corpus changed is no longer recognized; " +
			"the answer depends on node identity, which does not survive the rewrite " +
			"this exists to catch")
	}
}

// The record outlives a prune, which is the guard's actual lifetime.
//
// `rewrite_attempt.node_id` is `REFERENCES node(node_id) ON DELETE CASCADE` and
// `Index` prunes on every profile-mode write, so this guard survives only
// because `pruneConn` seeds `rewrite_attempt`'s profile_id and node_id as
// reachability roots. That is in a file this slice does not touch, nothing else
// pins it, and a future prune change would disable #111 with a green suite.
func TestAnAcceptedRecordSurvivesAPrune(t *testing.T) {
	s := newStore(t)
	first, prof := seededProfile(t, s)

	accepted := attemptFixture(prof.ID, first.Documents[0].Nodes[0].ID)
	accepted.Accepted, accepted.Rejection = true, ""
	if err := s.PutRewriteAttempt(ctx(), accepted); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: accepted.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: first.Documents[0].Nodes[0].ID, ParagraphHash: accepted.CandidateHash},
		},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}
	// A second snapshot nothing references, so the prune has something to take.
	// Without it the fixture cannot tell "the prune preserved the record" from
	// "the prune deleted nothing at all", which is most of what this test is for.
	mustPutSnapshot(t, s, snapshotWrite(
		document("essays/orphan.md", identity.HashBytes([]byte("orphan")), node(0, 0, 24)),
	))

	// Keeping NOTHING explicitly, which is the strongest form: the attempt's own
	// profile and node survive only as roots in their own right, and if they do
	// not, the cascade on node_id takes the record with them. Naming another
	// profile instead would refuse — Prune requires every kept id to exist.
	pruned, err := s.Prune(ctx(), nil)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if pruned.Snapshots == 0 {
		t.Fatal("the prune deleted no snapshot, so it never exercised the delete path")
	}

	got, e := s.ProducedByRewrite(ctx(), []string{accepted.CandidateHash})
	if e != nil {
		t.Fatalf("ProducedByRewrite after Prune: %v", e)
	}
	if !got[accepted.CandidateHash] {
		t.Error("the accepted record did not survive a prune, so the guard lapses " +
			"whenever an index runs")
	}
}
