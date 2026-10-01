package store_test

// #134. Both screens equate an accepted ATTEMPT with a published paragraph, and
// `RecordAttempt` fires per attempt inside the loop — before later attempts,
// before the freshness check, before assembly, and before anything reaches a
// filesystem.
//
// # Evidence
//
// Measured end to end in internal/workflow, with a provider that answers once
// and then fails: `Execute` returns `provider down` and zero bytes, and
// `PublishedParagraphs` holds one entry. Planning an AUTHOR document whose
// paragraph is byte-identical to that candidate then reports
// `already-rewritten=1`. Nothing was written anywhere, and the paragraph the
// screen excluded was the author's own.
//
// The second case needs no failure: ADR 0006's loop advances `current` on
// acceptance, so a run with three acceptances published the last one and
// recorded three.
//
// # Contract
//
// `RecordPublication` is the only thing that puts a paragraph in either screen's
// answer. An accepted attempt by itself puts nothing there, and
// `rewrite_attempt` stays what its name says: the record of a decision.
//
// # What a row claims, and what it does not
//
// A row says this tool published these paragraph bytes under this invocation,
// for this target node. It does NOT say the tool authored them: a paragraph
// byte-identical to prose the author already had matches, and the match is real,
// because the screen's question is about bytes. That limit predates this slice
// and is not closed by it — what is closed is excluding text that was accepted
// and never published.
//
// # Decision: no foreign keys, on either column
//
// `node_id` is recorded, not referenced. The identity chain is
// `snapshot.ID = H(policy, [path=content_hash…])` then
// `document.ID = H(snapshot.ID, path)` then `node.ID = H(document.ID, ordinal)`,
// so a change to ANY document in the corpus changes every node id — and the next
// `index` after a publication is exactly when that happens. A cascading
// reference would delete the evidence at the moment it first mattered, which is
// the property `TestAHashOutlivesTheNodeItWasRecordedAgainst` already states for
// the hash next door.
//
// # Unresolved
//
// Retention. Nothing prunes publication evidence, so it grows by one row per
// changed paragraph per invocation forever. That is a deliberate omission rather
// than an oversight — a pruning rule would be a policy about how long this tool
// remembers what it published, and there is no measurement behind any particular
// answer.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
)

// publishedHash is prose this tool emitted, hashed the way a published
// paragraph is.
func publishedHash(body string) string { return identity.HashBytes([]byte(body)) }

// Recording the same publication twice is one row and not an error.
//
// Codex's requirement, and the reason is the retry: `cli` records after the
// bytes are visible, so a caller that publishes, fails to record, and is run
// again must not be refused for the attempt that already landed.
func TestRecordingTheSamePublicationTwiceIsOneMember(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	attempt := attemptFixture(prof.ID, nodes[0].ID)

	evidence := store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs:   []store.PublishedParagraph{{NodeID: nodes[0].ID, ParagraphHash: publishedHash("twice")}},
	}
	for i := range 2 {
		if err := s.RecordPublication(ctx(), evidence); err != nil {
			t.Fatalf("RecordPublication call %d: %v", i+1, err)
		}
	}

	if got := publicationRows(t, s); got != 1 {
		t.Errorf("recording the same publication twice left %d rows, want 1", got)
	}
}

// Conflicting evidence for one target is refused, and the first row stands.
//
// Two different hashes for the same invocation and node cannot both be what was
// published. Overwriting would make the evidence depend on call order; refusing
// says the caller is wrong.
func TestConflictingEvidenceForOneTargetIsRefused(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	attempt := attemptFixture(prof.ID, nodes[0].ID)
	first := publishedHash("what was published")

	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs:   []store.PublishedParagraph{{NodeID: nodes[0].ID, ParagraphHash: first}},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[0].ID, ParagraphHash: publishedHash("something else")},
		},
	})
	if err == nil {
		t.Fatal("two different hashes for one invocation and node were both accepted")
	}

	set, err := s.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}
	if !set[first] || len(set) != 1 {
		t.Errorf("the refused call disturbed the evidence already recorded: %v", set)
	}
}

// A batch that cannot be recorded records none of it.
//
// The run published one document, so its evidence is one fact. Recording the
// first paragraph and failing on the second would leave a screen that excludes
// half a publication, which is worse than one that excludes none of it: the
// caller is told the record failed and the store disagrees.
func TestABatchThatCannotBeRecordedRecordsNoneOfIt(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}
	attempt := attemptFixture(prof.ID, nodes[0].ID)
	good := publishedHash("the first paragraph")

	err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[0].ID, ParagraphHash: good},
			// Not a hash. The schema's own constraint is what refuses it, which
			// is why this fixture does not need a seam to inject a failure.
			{NodeID: nodes[1].ID, ParagraphHash: "not a hash"},
		},
	})
	if err == nil {
		t.Fatal("a batch holding an invalid hash was recorded")
	}

	if got := publicationRows(t, s); got != 0 {
		t.Errorf("a refused batch left %d rows; the first paragraph of it was kept", got)
	}
	set, err := s.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}
	if set[good] {
		t.Error("the valid half of a refused batch is in the published set")
	}
}

// Two nodes under one invocation, and one node under two invocations.
//
// The composite key, from both sides. `TestTextPublishedTwiceIsOneMember` varies
// the invocation AND the node at once, so uniqueness on either column alone
// satisfies it; neither row here can be dropped by a key that is too narrow.
// Repeating the whole batch afterwards is the retry `cli` performs.
func TestOneInvocationCanPublishSeveralParagraphs(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}
	first := attemptFixture(prof.ID, nodes[0].ID).InvocationID
	second := identity.HashBytes([]byte("a second invocation"))
	one, two := publishedHash("paragraph one"), publishedHash("paragraph two")

	batch := store.Publication{
		InvocationID: first,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[0].ID, ParagraphHash: one},
			{NodeID: nodes[1].ID, ParagraphHash: two},
		},
	}
	// The same NODE again, under a different invocation: a second `--in-place`
	// run over a paragraph the first one already rewrote.
	again := store.Publication{
		InvocationID: second,
		Paragraphs:   []store.PublishedParagraph{{NodeID: nodes[0].ID, ParagraphHash: two}},
	}
	for _, evidence := range []store.Publication{batch, again, batch, again} {
		if err := s.RecordPublication(ctx(), evidence); err != nil {
			t.Fatalf("RecordPublication: %v", err)
		}
	}

	if got := publicationRows(t, s); got != 3 {
		t.Errorf("%d rows for two nodes under one invocation plus one under another, "+
			"want 3 — a key on the invocation or the node alone loses one", got)
	}
	// The identities, read back, so a key that is wide enough but populated
	// wrongly is still caught.
	want := map[string]string{
		first + "/" + nodes[0].ID:  one,
		first + "/" + nodes[1].ID:  two,
		second + "/" + nodes[0].ID: two,
	}
	if got := publicationIdentities(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("the rows are\n%v\nwant\n%v", got, want)
	}
}

// A batch that fails on its SECOND row leaves neither.
//
// `TestABatchThatCannotBeRecordedRecordsNoneOfIt` is satisfied by validating
// every hash before inserting anything, which leaves a non-transactional loop
// broken for any failure the caller cannot pre-check. The failure here is the
// database's: a trigger refuses one otherwise-valid row, so nothing about the
// batch is detectably wrong until a write is already in flight.
//
// The trigger is the test's, not the schema's. What is required is the resulting
// STATE, not any particular transaction implementation.
func TestABatchThatFailsMidWayLeavesNothingBehind(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}
	attempt := attemptFixture(prof.ID, nodes[0].ID)
	good, refused := publishedHash("the first paragraph"), publishedHash("the refused one")

	db := openRaw(t, s)
	if _, err := db.Exec(`CREATE TRIGGER refuse_one BEFORE INSERT ON published_paragraph
		WHEN new.paragraph_hash = '` + refused + `'
		BEGIN SELECT RAISE(ABORT, 'refused by the test'); END`); err != nil {
		t.Fatalf("install the trigger: %v", err)
	}

	err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[0].ID, ParagraphHash: good},
			{NodeID: nodes[1].ID, ParagraphHash: refused},
		},
	})
	if err == nil {
		t.Fatal("a batch whose second row the database refused was recorded")
	}
	if got := publicationRows(t, s); got != 0 {
		t.Errorf("a failed batch left %d rows; the first row was committed before the "+
			"second was attempted", got)
	}
}

// A batch that conflicts with evidence already stored changes nothing.
//
// The same shape with no trigger: the first row is new and valid, the second
// contradicts a row from an earlier call. Both the new row and the standing one
// have to be exactly as they were.
func TestABatchConflictingWithStoredEvidenceChangesNothing(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}
	attempt := attemptFixture(prof.ID, nodes[0].ID)
	standing := publishedHash("what was published")
	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs:   []store.PublishedParagraph{{NodeID: nodes[0].ID, ParagraphHash: standing}},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	fresh := publishedHash("a paragraph nothing has recorded")
	err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: nodes[1].ID, ParagraphHash: fresh},
			{NodeID: nodes[0].ID, ParagraphHash: publishedHash("something else")},
		},
	})
	if err == nil {
		t.Fatal("a batch contradicting stored evidence was accepted")
	}

	want := map[string]string{attempt.InvocationID + "/" + nodes[0].ID: standing}
	if got := publicationIdentities(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("the rows are\n%v\nwant only the standing one\n%v", got, want)
	}
}

// Publication evidence outlives the node identity it names.
//
// The companion of `TestAHashOutlivesTheNodeItWasRecordedAgainst`, and the
// reason this table has no foreign key: a second snapshot holding one more
// document changes `snapshot.ID`, and every `document.ID` and `node.ID` under
// it, which is exactly what the next `index` after a publication does. A
// cascading reference would delete the evidence at the moment the screen first
// needs it.
func TestPublicationEvidenceOutlivesTheNodeIdentityItNames(t *testing.T) {
	s := newStore(t)
	first, prof := seededProfile(t, s)
	nodes := first.Documents[0].Nodes
	attempt := attemptFixture(prof.ID, nodes[0].ID)
	hash := publishedHash("published, then re-indexed")

	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs:   []store.PublishedParagraph{{NodeID: nodes[0].ID, ParagraphHash: hash}},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}

	second := withDerivedIDs(snapshotWrite(
		document("essays/a.md", hashA, node(0, 0, 12), node(1, 12, 20)),
		document("essays/b.md", hashB, node(0, 0, 30)),
		document("essays/c.md", identity.HashBytes([]byte("a later document")), node(0, 0, 18)),
	))
	mustPutSnapshot(t, s, second)
	if second.Documents[0].Nodes[0].ID == nodes[0].ID {
		t.Fatal("the second snapshot did not renumber the nodes, so this proves nothing")
	}

	set, err := s.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}
	if !set[hash] {
		t.Errorf("a publication recorded before the corpus changed is no longer "+
			"screened: %v", set)
	}
	asked, err := s.ProducedByRewrite(ctx(), []string{hash})
	if err != nil {
		t.Fatalf("ProducedByRewrite: %v", err)
	}
	if !asked[hash] {
		t.Error("ProducedByRewrite no longer recognizes it either")
	}
}

// And it survives a prune.
//
// `rewrite_attempt.node_id` cascades from `node`, and survives only because
// `pruneConn` seeds it as a reachability root. This table has no such reference,
// so nothing has to be seeded — which is a claim about the schema that only a
// test can keep true, because an implementation that added the foreign key would
// pass every assertion above.
func TestPublicationEvidenceSurvivesAPrune(t *testing.T) {
	s := newStore(t)
	first, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, first.Documents[0].Nodes[0].ID)
	hash := publishedHash("published, then pruned around")

	if err := s.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: first.Documents[0].Nodes[0].ID, ParagraphHash: hash},
		},
	}); err != nil {
		t.Fatalf("RecordPublication: %v", err)
	}
	// A second snapshot nothing references, so the prune has something to take.
	mustPutSnapshot(t, s, snapshotWrite(
		document("essays/orphan.md", identity.HashBytes([]byte("orphan")), node(0, 0, 24)),
	))

	pruned, err := s.Prune(ctx(), nil)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if pruned.Snapshots == 0 {
		t.Fatal("the prune deleted no snapshot, so it never exercised the delete path")
	}

	set, err := s.PublishedParagraphs(ctx())
	if err != nil {
		t.Fatalf("PublishedParagraphs: %v", err)
	}
	if !set[hash] {
		t.Errorf("the prune took the publication evidence with it: %v", set)
	}
}

// The key is the invocation and the target, and nothing is referenced.
//
// `allowlist_test.go` declares COLUMNS and says nothing about keys or foreign
// keys, so both halves of this table's shape would otherwise be unpinned — and
// both are load-bearing. The key is what makes the retry idempotent and the
// conflict detectable; the absence of references is what lets the evidence
// outlive the node identity it names.
func TestThePublicationTableIsKeyedOnTheInvocationAndTheTarget(t *testing.T) {
	db := openRaw(t, newStore(t))

	rows, err := db.Query(
		"SELECT name FROM pragma_table_info('published_paragraph') WHERE pk > 0 ORDER BY pk")
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer rows.Close()
	var key []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		key = append(key, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if want := []string{"invocation_id", "node_id"}; !reflect.DeepEqual(key, want) {
		t.Errorf("the primary key is %v, want %v", key, want)
	}

	var references int
	if err := db.QueryRow(
		"SELECT count(*) FROM pragma_foreign_key_list('published_paragraph')").Scan(&references); err != nil {
		t.Fatalf("pragma_foreign_key_list: %v", err)
	}
	if references != 0 {
		t.Errorf("published_paragraph declares %d foreign keys; the next index "+
			"re-derives every node id, so a reference deletes the evidence at the "+
			"moment the screen needs it", references)
	}
}

// publicationIdentities reads the evidence back keyed by invocation and node, so
// an assertion is against the rows rather than against a count.
func publicationIdentities(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	rows, err := openRaw(t, s).Query(
		"SELECT invocation_id, node_id, paragraph_hash FROM published_paragraph")
	if err != nil {
		t.Fatalf("reading published_paragraph: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var invocation, node, hash string
		if err := rows.Scan(&invocation, &node, &hash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[invocation+"/"+node] = hash
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// publicationRows counts the rows the evidence table holds.
func publicationRows(t *testing.T, s *store.Store) int {
	t.Helper()
	db := openRaw(t, s)
	var n int
	if err := db.QueryRow("SELECT count(*) FROM published_paragraph").Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			t.Fatalf("published_paragraph does not exist: %v", err)
		}
		t.Fatalf("counting published_paragraph: %v", err)
	}
	return n
}
