package ingest

import (
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
	"github.com/fissible/hapax/internal/text"
)

// "One tree per document" cannot be shown by comparing outputs: two trees built
// deterministically from the same bytes with the same options agree on every
// span, vector and count, so an output comparison proves agreement and not
// shared identity. It is observable only at the construction itself.
func TestEachDocumentsTreeIsBuiltExactlyOnce(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(seamProse), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
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

// And the standardizations come from that same pass rather than a second one.
func TestStandardizationsDoNotRebuildTheTrees(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(seamProse), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	counter := &treeCounter{}
	if _, err := snapshotWith(root, snapshot, deps{ReadFile: os.ReadFile, Structure: counter.structure}); err != nil {
		t.Fatalf("snapshotWith: %v", err)
	}
	if got, eligible := counter.count(), len(snapshot.Eligible()); got != eligible {
		t.Fatalf("%d trees for %d documents", got, eligible)
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

const seamProse = "A paragraph of prose with enough lexical tokens in it to clear the floor, " +
	"and a second sentence so it is not taken for a heading.\n\n" +
	"A second paragraph, also long enough to be counted, continuing past one sentence.\n"

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
	forbidden := map[string][]string{
		"os":   {"ReadFile", "Open", "OpenFile", "ReadDir"},
		"text": {},
	}
	scanned := 0
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				selector, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if selector.Sel.Name == "Structure" && enclosing(file, selector.Pos()) != "realDeps" {
					t.Errorf("%s builds a tree outside realDeps; the one-tree count would be meaningless", name)
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
