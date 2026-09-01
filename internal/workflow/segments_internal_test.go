package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/store"
)

// eval.Segment.Index is the position among paragraphs that CLEARED the floor.
// The persisted node ordinal counts every leaf, including those below it. The
// two coincide whenever nothing was excluded — which is exactly why a fixture
// where nothing is excluded cannot tell a correct reconstruction from reading
// the ordinal straight off the node, and both produce a plausible evaluation.
//
// This fixture puts half of every document below the floor, so the numberings
// diverge from the second admitted paragraph of each document onward.
func TestSegmentIndexCountsAdmittedParagraphsAndNotNodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Long paragraphs clear a floor of twelve; short ones do not. Alternated,
	// so an admitted paragraph's node ordinal is twice its admitted index.
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		body := ""
		for p := 0; p < 5; p++ {
			body += fmt.Sprintf("Document %d paragraph %d carries ordinary prose past a single sentence "+
				"so the structure pass does not read it as a heading; it names %d.\n\n", i, p, i*100+p)
			body += fmt.Sprintf("Short %d here.\n\n", i*100+p)
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("doc%03d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	requirements := profile.DefaultRequirements()
	requirements.MinParagraphLexicalTokens = 12
	indexed, err := New(requirements, 30).Index(ctx, IndexRequest{CorpusRoot: root, Register: "essays"})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	opened, err := store.Open(indexed.StorePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer opened.Close()

	bundle, err := opened.LoadProfileBundle(ctx, "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	fitted, err := bundle.Profile.Fitted()
	if err != nil {
		t.Fatalf("Profile.Fitted: %v", err)
	}

	// The fixture has to actually exclude something, or admitted index and node
	// ordinal coincide and the oracle below cannot tell the two rules apart.
	// Asserted against the graph rather than assumed from the prose.
	written, err := opened.Snapshot(ctx, indexed.SnapshotID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	leaves, vectored, heldOut := 0, 0, 0
	for _, document := range written.Documents {
		if document.Split != corpus.Test {
			continue
		}
		heldOut++
		for _, node := range document.Nodes {
			leaves++
			if node.Vector != nil {
				vectored++
			}
		}
	}
	if heldOut == 0 {
		t.Fatal("the fixture held nothing out")
	}
	if vectored == 0 || vectored == leaves {
		t.Fatalf("%d of %d held-out leaves cleared the floor; nothing straddles it", vectored, leaves)
	}

	segments, err := heldOutSegments(ctx, opened, indexed.SnapshotID, fitted)
	if err != nil {
		t.Fatalf("heldOutSegments: %v", err)
	}
	if len(segments) == 0 {
		t.Fatal("no held-out segments; this proves nothing")
	}

	if len(segments) != vectored {
		t.Errorf("%d segments over %d vectored held-out leaves", len(segments), vectored)
	}

	// The node ordinal each admitted leaf actually has, so the test can see
	// whether the reconstruction reproduced it instead of counting.
	ordinals := map[string][]int{}
	for _, document := range written.Documents {
		if document.Split != corpus.Test {
			continue
		}
		for _, node := range document.Nodes {
			if node.Vector != nil {
				ordinals[document.ContentHash] = append(ordinals[document.ContentHash], node.Ordinal)
			}
		}
	}

	seen := map[string]int{}
	diverged := false
	for _, segment := range segments {
		want := seen[segment.DocumentHash]
		if segment.Index != want {
			t.Errorf("a segment of %s has index %d, want %d — admitted indexes run "+
				"consecutively from zero within a document", segment.DocumentHash, segment.Index, want)
		}
		positions := ordinals[segment.DocumentHash]
		if want >= len(positions) {
			t.Fatalf("%s yielded more segments than it has vectored leaves", segment.DocumentHash)
		}
		if positions[want] != segment.Index {
			diverged = true
		}
		seen[segment.DocumentHash]++
	}

	// Without this the assertion above would pass against an implementation
	// that read the node ordinal, because on an unfiltered document they agree.
	if !diverged {
		t.Fatal("no admitted index differs from its node ordinal; the fixture cannot " +
			"tell the two rules apart")
	}
	// Every held-out document is represented, and ONLY held-out ones. A train
	// document reaching the evaluation is text the profile was fitted on, which
	// would inflate every figure without changing any identity.
	if len(seen) != len(ordinals) {
		t.Errorf("%d documents produced segments and %d held-out documents have vectors",
			len(seen), len(ordinals))
	}
	for hash := range seen {
		if _, heldOut := ordinals[hash]; !heldOut {
			t.Errorf("%s produced segments and is not held out", hash)
		}
	}
}
