package ingest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

// "One tree per document" cannot be shown by comparing outputs: two trees built
// deterministically from the same bytes with the same options agree on every
// span, vector and count, so an output comparison proves agreement and not
// shared identity. It is observable only at the construction itself.
func TestEachDocumentsTreeIsBuiltExactlyOnce(t *testing.T) {
	root := seamCorpus(t, 4)
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	counter := &treeCounter{}
	if _, err := snapshotWith(root, snapshot, deps{
		ReadFile: os.ReadFile, Structure: counter.structure,
	}); err != nil {
		t.Fatalf("snapshotWith: %v", err)
	}

	eligible := len(snapshot.Eligible())
	if eligible == 0 {
		t.Fatal("no eligible documents; the count would be vacuous")
	}
	if got := counter.count(); got != eligible {
		t.Errorf("%d trees built for %d eligible documents", got, eligible)
	}
}

// And CalibrateStandardizations builds each document's tree once too. The
// previous version of this test never called it, so an implementation that
// rebuilt every tree there passed.
func TestStandardizationsBuildEachTreeOnce(t *testing.T) {
	root := seamCorpus(t, 12)
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	requirements := profile.DefaultRequirements()
	requirements.MinDocuments, requirements.MinParagraphs, requirements.MinObservationsPerFeature = 1, 1, 1
	built, err := profile.Build(root, snapshot, requirements)
	if err != nil {
		t.Fatalf("the fixture cannot fit a profile: %v", err)
	}

	calibrate := 0
	for _, document := range snapshot.Eligible() {
		if document.Split == corpus.Calibrate {
			calibrate++
		}
	}
	if calibrate == 0 {
		t.Fatal("the split assigned no calibrate documents; the count would prove nothing")
	}

	counter := &treeCounter{}
	if _, err := calibrateStandardizationsWith(root, snapshot, built, deps{
		ReadFile: os.ReadFile, Structure: counter.structure,
	}); err != nil {
		t.Fatalf("calibrateStandardizationsWith: %v", err)
	}
	if got := counter.count(); got != calibrate {
		t.Errorf("%d trees built for %d calibrate documents", got, calibrate)
	}
}

type treeCounter struct {
	mu   sync.Mutex
	seen int
}

func (c *treeCounter) structure(doc *text.Document) *text.Node {
	c.mu.Lock()
	c.seen++
	c.mu.Unlock()
	return doc.Structure(text.DefaultStructureOptions())
}

func (c *treeCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// Distinct bodies: identical ones are deduplicated by corpus, which would leave
// one eligible document and a count that proves nothing.
func seamCorpus(t *testing.T, count int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < count; i++ {
		body := fmt.Sprintf(
			"Document %d opens with a paragraph long enough to clear the lexical floor, "+
				"and continues past a single sentence so it is not read as a heading.\n\n"+
				"Its second paragraph is also long enough to count, and mentions %d again.\n", i, i)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("doc%02d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

// A seam the package can reach around is not a seam: if any path outside
// realDeps calls doc.Structure or reads a file directly, the count above stops
// meaning anything.
func TestIngestCannotReachAroundItsSeam(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	forbidden := map[string][]string{"os": {"ReadFile", "Open", "OpenFile", "ReadDir"}}
	scanned, structures := 0, 0
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				selector, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if selector.Sel.Name == "Structure" {
					structures++
					if enclosing(file, selector.Pos()) != "realDeps" {
						t.Errorf("%s builds a tree outside realDeps; the one-tree count would be meaningless", name)
					}
				}
				ident, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				for _, banned := range forbidden[ident.Name] {
					if selector.Sel.Name == banned && enclosing(file, selector.Pos()) != "realDeps" {
						t.Errorf("%s uses %s.%s outside realDeps", name, ident.Name, banned)
					}
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test source was scanned; this guard is vacuous")
	}
	// EXACTLY one, not merely "all of them inside realDeps": a second call in
	// realDeps would build a tree the injected counter never sees.
	if structures != 1 {
		t.Errorf("the package builds %d trees; there is one seam and it is used once", structures)
	}
}

func enclosing(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Pos() <= pos && pos <= fn.End() {
			return fn.Name.Name
		}
	}
	return ""
}
