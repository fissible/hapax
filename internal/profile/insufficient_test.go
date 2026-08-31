package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

func corpusOf(t *testing.T, files map[string]string) (string, *corpus.Snapshot) {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return root, snapshot
}

const paragraph = "A paragraph of prose with enough lexical tokens in it to clear any reasonable floor, " +
	"continuing for a second sentence so that it is not mistaken for a heading.\n"

// A corpus too small to fit a profile is an ADVERSE completed outcome, not an
// operational failure — index reports it and exits 1. That is only possible if
// the outcome is typed, and every insufficiency has to carry the same type: a
// caller asking "is this the corpus being small?" must get one answer.
func TestEveryInsufficientCorpusIsTypedAsSuch(t *testing.T) {
	for _, c := range []struct {
		name         string
		files        map[string]string
		requirements func() profile.Requirements
	}{
		{"too few documents", map[string]string{"a.md": paragraph},
			func() profile.Requirements {
				r := profile.DefaultRequirements()
				r.MinDocuments = 50
				return r
			}},
		{"too few paragraphs", map[string]string{"a.md": paragraph, "b.md": paragraph},
			func() profile.Requirements {
				r := profile.DefaultRequirements()
				r.MinDocuments, r.MinParagraphs = 1, 5000
				return r
			}},
		{"no eligible document", map[string]string{"a.md": "hi\n"},
			func() profile.Requirements {
				r := profile.DefaultRequirements()
				r.MinDocuments = 1
				return r
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, snapshot := corpusOf(t, c.files)
			_, err := profile.Build(root, snapshot, c.requirements())
			if err == nil {
				t.Fatal("built a profile from an insufficient corpus")
			}
			if !errors.Is(err, profile.ErrCorpusTooSmall) {
				t.Errorf("error = %v, want ErrCorpusTooSmall", err)
			}
		})
	}
}

// A corpus whose split assigns nothing to train is insufficient for the same
// reason and must carry the same type. The snapshot's own splits are moved
// rather than the policy's weights: corpus refuses a non-positive weight, so
// weighting train to zero would demand an unrelated change to policy semantics
// just to make a fixture deterministic.
func TestNoTrainDocumentIsCorpusInsufficiency(t *testing.T) {
	root, snapshot := corpusOf(t, map[string]string{
		"a.md": paragraph, "b.md": paragraph + "\nAnd a little more.\n",
		"c.md": paragraph + "\nAnd more again.\n", "d.md": paragraph + "\nAnd once more.\n",
	})
	moved := 0
	for i := range snapshot.Documents {
		if snapshot.Documents[i].Split == corpus.Train {
			snapshot.Documents[i].Split = corpus.Test
			moved++
		}
	}
	if moved == 0 {
		t.Skip("the seed assigned nothing to train, so there is nothing to move")
	}
	requirements := profile.DefaultRequirements()
	requirements.MinDocuments, requirements.MinParagraphs, requirements.MinObservationsPerFeature = 1, 1, 1

	_, err := profile.Build(root, snapshot, requirements)
	if err == nil {
		t.Fatal("built a profile with no train document")
	}
	if !errors.Is(err, profile.ErrCorpusTooSmall) {
		t.Errorf("error = %v, want ErrCorpusTooSmall", err)
	}
}

// And an operational failure is NOT that: a corpus whose file has gone missing
// under the snapshot is an I/O problem, which index reports as exit 3. If the
// two shared a type the exit code would be a coin flip.
func TestAMissingFileIsNotCorpusInsufficiency(t *testing.T) {
	root, snapshot := corpusOf(t, map[string]string{"a.md": paragraph, "b.md": paragraph})
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	requirements := profile.DefaultRequirements()
	requirements.MinDocuments, requirements.MinParagraphs = 1, 1

	_, err := profile.Build(root, snapshot, requirements)
	if err == nil {
		t.Fatal("built a profile over a document that is gone")
	}
	if errors.Is(err, profile.ErrCorpusTooSmall) {
		t.Errorf("a missing file was reported as an insufficient corpus: %v", err)
	}
}

// The reason a profile is not production ready is a closed set its own package
// declares, so store validates membership rather than holding a second copy.
func TestTheNotReadyReasonsAreDeclared(t *testing.T) {
	reasons := profile.NotReadyReasons()
	if len(reasons) == 0 {
		t.Fatal("no not-ready reasons are declared")
	}
	sorted := append([]string(nil), reasons...)
	sort.Strings(sorted)
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			t.Errorf("%q is declared twice", sorted[i])
		}
	}
	for _, reason := range reasons {
		if reason == "" {
			t.Error("the empty reason is declared; it means READY and is not a reason")
		}
	}
}

// And the reason a built profile actually carries is one of them, so the
// vocabulary cannot drift from what Build produces.
func TestABuiltProfilesReasonIsDeclared(t *testing.T) {
	root, snapshot := corpusOf(t, map[string]string{
		"a.md": paragraph + "\n" + paragraph, "b.md": paragraph + "\n" + paragraph,
		"c.md": paragraph + "\n" + paragraph, "d.md": paragraph + "\n" + paragraph,
	})
	requirements := profile.DefaultRequirements()
	requirements.MinDocuments, requirements.MinParagraphs, requirements.MinObservationsPerFeature = 1, 1, 1

	built, err := profile.Build(root, snapshot, requirements)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.ProductionReady {
		if built.NotProductionReason != "" {
			t.Errorf("a ready profile carries the reason %q", built.NotProductionReason)
		}
		return
	}
	for _, reason := range profile.NotReadyReasons() {
		if built.NotProductionReason == reason {
			return
		}
	}
	t.Errorf("Build produced the reason %q, which is not declared", built.NotProductionReason)
}

// ---------------------------------------------------------------------------

// The inclusion rule has ONE owner, and it takes the caller's tree: ingest
// builds the graph it is going to persist and hands that root in, so the leaves
// it gets back identify nodes in the graph it already has. Building a second
// tree here would make the pointers meaningless to it.
func TestParagraphLeavesDescribeTheCallersTree(t *testing.T) {
	doc, err := text.Admit([]byte(paragraph + "\n" + paragraph))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	root := doc.Structure(text.DefaultStructureOptions())

	leaves, belowFloor, err := profile.ParagraphLeaves(doc, root, 1)
	if err != nil {
		t.Fatalf("ParagraphLeaves: %v", err)
	}
	if len(leaves) == 0 {
		t.Fatal("no leaves")
	}
	_ = belowFloor

	included := root.IncludedLeaves()
	within := map[*text.Node]bool{}
	for _, leaf := range included {
		within[leaf] = true
	}
	for i, leaf := range leaves {
		if !within[leaf.Node] {
			t.Errorf("leaf %d is not a node of the tree that was passed in", i)
		}
	}
}

// ParagraphVectors keeps its contract by building a tree and delegating, so the
// two cannot describe different paragraphs.
func TestParagraphVectorsAgreesWithParagraphLeaves(t *testing.T) {
	doc, err := text.Admit([]byte(paragraph + "\n" + paragraph))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	root := doc.Structure(text.DefaultStructureOptions())

	leaves, belowFloor, err := profile.ParagraphLeaves(doc, root, 12)
	if err != nil {
		t.Fatalf("ParagraphLeaves: %v", err)
	}
	paragraphs, err := profile.ParagraphVectors(doc, 12)
	if err != nil {
		t.Fatalf("ParagraphVectors: %v", err)
	}
	if belowFloor != paragraphs.BelowFloor {
		t.Errorf("below floor = %d and %d", belowFloor, paragraphs.BelowFloor)
	}
	if len(leaves) != len(paragraphs.Vectors) {
		t.Fatalf("%d leaves and %d vectors", len(leaves), len(paragraphs.Vectors))
	}
	for i := range leaves {
		if !reflect.DeepEqual(leaves[i].Vector, paragraphs.Vectors[i]) {
			t.Errorf("vector %d differs between the two paths", i)
		}
	}
}

// cli passes declared requirements rather than inventing numbers.
func TestTheDefaultRequirementsAreUsable(t *testing.T) {
	requirements := profile.DefaultRequirements()
	if requirements.MinDocuments <= 0 || requirements.MinParagraphs <= 0 ||
		requirements.MinObservationsPerFeature <= 0 || requirements.MinParagraphLexicalTokens <= 0 {
		t.Errorf("a declared floor is not positive: %+v", requirements)
	}
}
