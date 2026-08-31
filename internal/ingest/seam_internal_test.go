package ingest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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
		BuildTree: counter.structure,
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
		BuildTree: counter.structure,
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
// realDeps builds a tree or reads a file directly, the count above stops
// meaning anything.
//
// The rule is about doc.Structure, the tree constructor — NOT about calling the
// injected seam, which every path is supposed to do. That is why the seam field
// is named BuildTree: an earlier version of this guard counted every selector
// named Structure, which forbade the package from calling its own seam and was
// satisfied instead by reaching the field through reflect.FieldByName. A guard
// that is cheaper to evade than to obey is a badly written guard.
//
// So reflect is banned outright here. Reflection defeats an AST walk by
// construction — FieldByName on the deps value and MethodByName on the document
// are both invisible to it — and this package has no honest use for it.
//
// What this guard is: a tripwire against reaching around the seam by the routes
// a person would actually take. What it is not: proof. It is syntactic, so it
// cannot see a tree assembled from text.Node values by hand, nor a read reached
// through some other package that exports one — and since every package this
// one imports can read a file, closing that would take a whole-program analysis
// rather than a longer list of banned imports. The list is not the guarantee.
// The guarantee is that snapshot.ReadVerified hashes what it reads, so bytes
// that arrived any other way do not match the snapshot and are caught where it
// matters. This guard just makes the short way round visible.
//
// io/fs is deliberately NOT banned: it is mostly types — fs.FileInfo,
// fs.DirEntry, fs.ValidPath — and banning a package that large to stop one
// function costs more than it buys.
func TestIngestCannotReachAroundItsSeam(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	// Reads are delegated to snapshot.ReadVerified, which does the reading and
	// the hash check together. So the ban is on the IMPORTS, not on selectors
	// spelled os.ReadFile: a selector rule misses a dot-import, misses an
	// aliased one, and misses io/fs.ReadFile(os.DirFS(root), path) entirely,
	// whereas a package that cannot import a file reader cannot call one. It is
	// also unconditional rather than exempting realDeps — nothing here needs
	// the exemption, and an exemption nothing needs is one available to
	// something else later.
	//
	// reflect is banned for a different reason: reflection is invisible to an
	// AST walk by construction, so FieldByName on the deps value and
	// MethodByName on the document would both walk straight past the rule
	// below. An earlier guard was evaded exactly that way.
	banned := map[string]string{
		"os":        "verified reads belong to snapshot.ReadVerified",
		"io/ioutil": "verified reads belong to snapshot.ReadVerified",
		"reflect":   "reflection is invisible to this walk and this package has no use for it",
	}
	scanned, structures := 0, 0
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			scanned++
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatalf("%s: unquoting import %s: %v", name, spec.Path.Value, err)
				}
				if why, forbidden := banned[path]; forbidden {
					t.Errorf("%s imports %s; %s", name, path, why)
				}
				// A dot-import puts another package's names in this file's
				// scope unqualified, which makes every selector rule below
				// unreliable about what it is looking at.
				if spec.Name != nil && spec.Name.Name == "." {
					t.Errorf("%s dot-imports %s; this guard cannot see through that", name, path)
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				selector, ok := n.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Structure" {
					return true
				}
				structures++
				if enclosing(file, selector.Pos()) != "realDeps" {
					t.Errorf("%s builds a tree outside realDeps; the one-tree count would be meaningless", name)
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
