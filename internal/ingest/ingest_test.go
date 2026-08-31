package ingest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/ingest"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

const tooShort = "hi\n"

// Identical bodies would be deduplicated by corpus, leaving one eligible
// document and a fixture that quietly tests almost nothing. Each is distinct.
func prose(n int) string {
	return fmt.Sprintf(
		"Document %d opens with a paragraph long enough to clear the lexical floor, "+
			"and continues past a single sentence so it is not read as a heading.\n\n"+
			"Its second paragraph is also long enough to count, and mentions %d again "+
			"so that no two documents share a content hash.\n", n, n)
}

func distinct(count int) map[string]string {
	files := map[string]string{}
	for i := 0; i < count; i++ {
		files[fmt.Sprintf("doc%02d.md", i)] = prose(i)
	}
	return files
}

func walked(t *testing.T, files map[string]string) (string, *corpus.Snapshot) {
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
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return root, snapshot
}

// The corpus index records what it REFUSED as well as what it kept, so every
// document becomes a row. Only the eligible ones get a structural graph: a
// rejected document is a row with no nodes.
func TestEveryDocumentIsPersistedAndOnlyEligibleOnesGetAGraph(t *testing.T) {
	root, snapshot := walked(t, withShort(distinct(4)))

	write, err := ingest.Snapshot(root, snapshot)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// The exact set, not the count: duplicating one document and dropping
	// another keeps the length and loses the corpus.
	wantPaths, gotPaths := []string{}, []string{}
	for _, document := range snapshot.Documents {
		wantPaths = append(wantPaths, document.Path)
	}
	for _, document := range write.Documents {
		gotPaths = append(gotPaths, document.Path)
	}
	sort.Strings(wantPaths)
	sort.Strings(gotPaths)
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("documents =\n%v\nwant\n%v", gotPaths, wantPaths)
	}

	eligible := map[string]bool{}
	for _, document := range snapshot.Eligible() {
		eligible[document.Path] = true
	}
	for _, document := range write.Documents {
		if eligible[document.Path] && len(document.Nodes) == 0 {
			t.Errorf("%s is eligible and has no nodes", document.Path)
		}
		if !eligible[document.Path] && len(document.Nodes) != 0 {
			t.Errorf("%s was refused and has %d nodes", document.Path, len(document.Nodes))
		}
	}
}

// The verdict that reaches the store is the one corpus produced, not a value
// ingest invented — which is the whole point of the column being a verdict.
func TestTheLanguageVerdictIsTheOneCorpusProduced(t *testing.T) {
	root, snapshot := walked(t, distinct(2))
	// Corpus produces not-performed for everything today, so a hard-coded
	// not-performed would pass. Vary them, and require the value to travel.
	varied := []corpus.CheckState{corpus.CheckPassed, corpus.CheckSkippedByPolicy}
	for i := range snapshot.Documents {
		snapshot.Documents[i].Language.State = varied[i%len(varied)]
	}
	write, err := ingest.Snapshot(root, snapshot)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	byPath := map[string]corpus.Document{}
	for _, document := range snapshot.Documents {
		byPath[document.Path] = document
	}
	for _, document := range write.Documents {
		if want := byPath[document.Path].Language.State; document.Language != want {
			t.Errorf("%s language = %q, want %q", document.Path, document.Language, want)
		}
	}
}

// Ordinals are contiguous from zero in one deterministic traversal, which is
// what makes a node's identity stable across reindexes of an unchanged corpus.
func TestOrdinalsAreContiguousAndDeterministic(t *testing.T) {
	root, snapshot := walked(t, distinct(2))

	first, err := ingest.Snapshot(root, snapshot)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, document := range first.Documents {
		for i, node := range document.Nodes {
			if node.Ordinal != i {
				t.Errorf("%s node %d has ordinal %d", document.Path, i, node.Ordinal)
			}
		}
	}

	second, err := ingest.Snapshot(root, snapshot)
	if err != nil {
		t.Fatalf("Snapshot again: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Error("two ingests of one unchanged corpus differ")
	}
}

// One tree per document. ingest builds the graph it persists and derives the
// vectors from that same tree, so every vector-bearing node is a leaf
// profile.ParagraphLeaves returned for that root — same span, same vector, same
// count. A second tree would have to agree on all three by coincidence.
func TestVectorsComeFromTheSameTreeAsTheNodes(t *testing.T) {
	root, snapshot := walked(t, distinct(1))
	write, err := ingest.Snapshot(root, snapshot)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "doc00.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	doc, err := text.Admit(raw)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	leaves, _, err := profile.ParagraphLeaves(doc, doc.Structure(text.DefaultStructureOptions()),
		profile.DefaultRequirements().MinParagraphLexicalTokens)
	if err != nil {
		t.Fatalf("ParagraphLeaves: %v", err)
	}

	var carrying []store.Node
	for _, document := range write.Documents {
		for _, node := range document.Nodes {
			if node.Vector != nil {
				carrying = append(carrying, node)
			}
		}
	}
	if len(carrying) != len(leaves) {
		t.Fatalf("%d nodes carry vectors for %d included leaves", len(carrying), len(leaves))
	}
	for i, node := range carrying {
		if node.Offset != leaves[i].Node.Span.Offset || node.Length != leaves[i].Node.Span.Length {
			t.Errorf("node %d spans %d+%d, the leaf spans %d+%d", i,
				node.Offset, node.Length, leaves[i].Node.Span.Offset, leaves[i].Node.Span.Length)
		}
		if !reflect.DeepEqual(*node.Vector, leaves[i].Vector) {
			t.Errorf("node %d carries a different vector than its leaf", i)
		}
	}
}

// A file that changed under the snapshot is an error, not a quietly smaller
// graph: ReadVerified owns that check and ingest does not work around it.
func TestAChangedFileIsRefusedRatherThanSkipped(t *testing.T) {
	root, snapshot := walked(t, distinct(2))
	if err := os.WriteFile(filepath.Join(root, "doc00.md"), []byte(prose(0)+"\nAnd more.\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if _, err := ingest.Snapshot(root, snapshot); err == nil {
		t.Error("ingested a corpus whose content had changed underneath it")
	}
}

func TestAMissingFileIsRefusedRatherThanSkipped(t *testing.T) {
	root, snapshot := walked(t, distinct(2))
	if err := os.Remove(filepath.Join(root, "doc00.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := ingest.Snapshot(root, snapshot); err == nil {
		t.Error("ingested a corpus with a document missing")
	}
}

// The reference is built from Calibrate-split standardizations, which is what
// deviation.BuildReference takes — a snapshot and a split will not do.
func TestCalibrateStandardizationsCoverTheCalibrateSplitOnly(t *testing.T) {
	root, snapshot := walked(t, distinct(12))

	requirements := profile.DefaultRequirements()
	requirements.MinDocuments, requirements.MinParagraphs, requirements.MinObservationsPerFeature = 1, 1, 1
	prof, err := profile.Build(root, snapshot, requirements)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	standardizations, err := ingest.CalibrateStandardizations(root, snapshot, prof)
	if err != nil {
		t.Fatalf("CalibrateStandardizations: %v", err)
	}
	// The COMPLETE MULTISET, derived independently. A count alone is satisfied
	// by repeating one valid leaf while dropping every other.
	var want []deviation.Standardization
	for _, document := range snapshot.Eligible() {
		if document.Split != corpus.Calibrate {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, document.Path))
		if err != nil {
			t.Fatalf("read %s: %v", document.Path, err)
		}
		admitted, err := text.Admit(raw)
		if err != nil {
			t.Fatalf("admit %s: %v", document.Path, err)
		}
		leaves, _, err := profile.ParagraphLeaves(admitted,
			admitted.Structure(text.DefaultStructureOptions()), prof.Requirements.MinParagraphLexicalTokens)
		if err != nil {
			t.Fatalf("leaves of %s: %v", document.Path, err)
		}
		for _, leaf := range leaves {
			standardized, err := deviation.Standardize(leaf.Vector, prof, corpus.Calibrate)
			if err != nil {
				t.Fatalf("standardize: %v", err)
			}
			want = append(want, standardized)
		}
	}
	if len(want) == 0 {
		t.Fatal("no calibrate leaves; adjust the fixture rather than proving nothing")
	}
	if !reflect.DeepEqual(sortStandardizations(standardizations), sortStandardizations(want)) {
		t.Errorf("%d standardizations do not match the %d calibrate leaves",
			len(standardizations), len(want))
	}

	for i, standardization := range standardizations {
		if standardization.Split != corpus.Calibrate {
			t.Errorf("standardization %d is from the %s split", i, standardization.Split)
		}
		if standardization.ProfileID != prof.ID {
			t.Errorf("standardization %d names profile %q", i, standardization.ProfileID)
		}
	}
}

func withShort(files map[string]string) map[string]string {
	files["short.md"] = tooShort
	return files
}

// sortStandardizations makes the multiset comparison independent of the order
// documents were walked in.
func sortStandardizations(values []deviation.Standardization) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = fmt.Sprintf("%+v", value)
	}
	sort.Strings(out)
	return out
}
