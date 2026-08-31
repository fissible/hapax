package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
	"github.com/fissible/hapax/internal/workflow"
)

func ctx() context.Context { return context.Background() }

// Paragraph bodies whose lexical token counts differ enough that a floor can be
// put BETWEEN them. The counts are NOT asserted in a comment — an earlier
// version of this file claimed 23 and 6, which were measurements of the format
// strings before the interpolated numbers went in, and numbers are not lexical
// tokens. requireStraddle measures the fixture that is actually written.
const (
	longParagraph  = "Document %d paragraph %d carries ordinary prose past a single sentence so the structure pass does not read it as a heading; it names %d.\n\n"
	shortParagraph = "Short %d here. Short %d also.\n\n"
)

// corpusOf writes n documents of ten long paragraphs each.
func corpusOf(t *testing.T, n int) string {
	t.Helper()
	return writeCorpus(t, n, 10, 0)
}

// mixedCorpusOf writes documents whose paragraphs straddle a raised floor: five
// long ones above it and five short ones below.
func mixedCorpusOf(t *testing.T, n int) string {
	t.Helper()
	return writeCorpus(t, n, 5, 5)
}

func writeCorpus(t *testing.T, documents, long, short int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < documents; i++ {
		body := ""
		for p := 0; p < long; p++ {
			body += fmt.Sprintf(longParagraph, i, p, i*100+p)
		}
		for p := 0; p < short; p++ {
			body += fmt.Sprintf(shortParagraph, i*100+p, i*100+p)
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("doc%03d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

// requireComposition fails unless the fixture produced EXACTLY this split. The
// split is content-derived, so a fixture edit can move a document between splits
// and send a test down a different path in silence; asserting a threshold would
// let that happen, and asserting the exact shape will not.
func requireComposition(t *testing.T, root string, train, calibrate, test int) {
	t.Helper()
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	counts := map[corpus.Split]int{}
	for _, document := range snapshot.Eligible() {
		counts[document.Split]++
	}
	if counts[corpus.Train] != train || counts[corpus.Calibrate] != calibrate || counts[corpus.Test] != test {
		t.Fatalf("fixture is %d train / %d calibrate / %d test, and this test needs %d / %d / %d",
			counts[corpus.Train], counts[corpus.Calibrate], counts[corpus.Test], train, calibrate, test)
	}
}

func indexed(t *testing.T, request workflow.IndexRequest) workflow.IndexResult {
	t.Helper()
	result, err := workflow.Default().Index(ctx(), request)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	return result
}

func indexRequest(root string) workflow.IndexRequest {
	return workflow.IndexRequest{CorpusRoot: root, Register: "essays"}
}

func defaultStorePath(root string) string {
	return filepath.Join(root, ".hapax", "hapax.sqlite3")
}

// requireStraddle fails unless the corpus actually has paragraphs on both sides
// of the floor. A fixture whose paragraphs all cleared it, or none did, would
// make a comparison across the floor prove nothing, and the token counts are a
// property of the tokenizer rather than of anything this test controls.
func requireStraddle(t *testing.T, root string, floor int) {
	t.Helper()
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	eligible := snapshot.Eligible()
	if len(eligible) == 0 {
		t.Fatal("no eligible documents to measure")
	}
	raw, err := os.ReadFile(filepath.Join(root, eligible[0].Path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	document, err := text.Admit(raw)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	paragraphs, err := profile.ParagraphVectors(document, 1)
	if err != nil {
		t.Fatalf("ParagraphVectors: %v", err)
	}
	above, below := 0, 0
	for _, vector := range paragraphs.Vectors {
		if vector.LexicalTokens >= floor {
			above++
		} else {
			below++
		}
	}
	if above == 0 || below == 0 {
		t.Fatalf("a floor of %d leaves %d paragraphs above and %d below; nothing straddles it",
			floor, above, below)
	}
}

// openStore reads the database the workflow wrote, through store's own API. The
// workflow's counters are its account of what it did; this is the database's,
// and going through the public reader keeps a schema change from breaking tests
// that are not about the schema.
func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	opened, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}

// vectoredTrainNodes counts the persisted leaves of TRAIN documents that carry a
// feature vector — which is to say, the leaves the graph decided cleared the
// paragraph floor.
func vectoredTrainNodes(t *testing.T, path, snapshotID string) int {
	t.Helper()
	return vectoredLeaves(t, path, snapshotID, corpus.Train)
}

// persistedBundle is the profile the store actually holds, which is the only
// account of what was fitted that the workflow did not write in its own words.
func persistedBundle(t *testing.T, path, register string) store.ProfileBundle {
	t.Helper()
	bundle, err := openStore(t, path).LoadProfileBundle(ctx(), register)
	if err != nil {
		t.Fatalf("LoadProfileBundle %q: %v", register, err)
	}
	return bundle
}

// vectoredLeaves counts persisted leaves carrying a vector in one split.
func vectoredLeaves(t *testing.T, path, snapshotID string, split corpus.Split) int {
	t.Helper()
	written, err := openStore(t, path).Snapshot(ctx(), snapshotID)
	if err != nil {
		t.Fatalf("reading snapshot %s: %v", snapshotID, err)
	}
	count := 0
	for _, document := range written.Documents {
		if document.Split != split {
			continue
		}
		for _, node := range document.Nodes {
			if node.Vector != nil {
				count++
			}
		}
	}
	return count
}

// persistedProfiles reports how many profiles and references the store holds,
// through its own readers rather than by reading its tables.
func persistedProfiles(t *testing.T, path string) (heads int, references int) {
	t.Helper()
	opened := openStore(t, path)
	registers, err := opened.ProfileHeads(ctx())
	if err != nil {
		t.Fatalf("ProfileHeads: %v", err)
	}
	for register := range registers {
		bundle, err := opened.LoadProfileBundle(ctx(), register)
		if err != nil {
			t.Fatalf("LoadProfileBundle %q: %v", register, err)
		}
		if bundle.Reference.ID != "" {
			references++
		}
	}
	return len(registers), references
}

// ---------------------------------------------------------------------------
// Evaluation
// ---------------------------------------------------------------------------

// distractorCorpus is other people's writing: distinct from the author's
// fixtures and from each other, because identical content is refused.
func distractorCorpus(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		writeDistractor(t, root, i)
	}
	return root
}

func writeDistractor(t *testing.T, root string, i int) {
	t.Helper()
	body := ""
	for p := 0; p < 6; p++ {
		body += fmt.Sprintf("Another writer's paragraph %d in piece %d, running on well past a single "+
			"sentence in a manner of its own so the structure pass reads it as prose; it mentions %d.\n\n",
			p, i, i*1000+p)
	}
	if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("other%03d.md", i)), []byte(body), 0o644); err != nil {
		t.Fatalf("write distractor: %v", err)
	}
}

func evalRequest(root, distractors string) workflow.EvalRequest {
	return workflow.EvalRequest{
		StorePath: defaultStorePath(root), Register: "essays", DistractorRoot: distractors,
	}
}

func evaluated(t *testing.T, request workflow.EvalRequest) workflow.EvalResult {
	t.Helper()
	result, err := workflow.Default().Eval(ctx(), request)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	return result
}

func releaseHead(t *testing.T, path, profileID string) string {
	t.Helper()
	head, err := openStore(t, path).ReleaseHead(ctx(), profileID)
	if err != nil {
		t.Fatalf("ReleaseHead: %v", err)
	}
	return head
}

// storedPool is the pool's membership as the database holds it: content hashes
// and nothing that could name a file or a person.
func storedPool(t *testing.T, path, poolID string) []string {
	t.Helper()
	pool, err := openStore(t, path).LoadDistractorPool(ctx(), poolID)
	if err != nil {
		t.Fatalf("LoadDistractorPool: %v", err)
	}
	return pool.ContentHashes
}

func storedRelease(t *testing.T, path, releaseID string) store.EvalResult {
	t.Helper()
	stored, err := openStore(t, path).LoadEvalResult(ctx(), releaseID)
	if err != nil {
		t.Fatalf("LoadEvalResult: %v", err)
	}
	return stored
}

// releaseHeadOrEmpty tolerates there being no head yet, which is the ordinary
// state before anything has shipped.
func releaseHeadOrEmpty(t *testing.T, path, profileID string) string {
	t.Helper()
	head, err := openStore(t, path).ReleaseHead(ctx(), profileID)
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatalf("ReleaseHead: %v", err)
	}
	return head
}

// installReleaseHead makes an existing release the head directly, so a test
// about what may MOVE a head does not have to earn one through a gate that
// wants sixty held-out documents.
func installReleaseHead(t *testing.T, path, releaseID string) {
	t.Helper()
	opened := openStore(t, path)
	stored, err := opened.LoadEvalResult(ctx(), releaseID)
	if err != nil {
		t.Fatalf("LoadEvalResult: %v", err)
	}
	if err := opened.PutEvalResult(ctx(), stored, store.AdvanceHead); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}
}
