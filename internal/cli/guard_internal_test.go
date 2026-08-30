package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// DESIGN says mode resolution belongs to every composition root, and that no
// library package reads the environment. That is only true if it cannot: the
// guard covers cmd/hapax as well as this package, because a guard on the
// convenient half is not a guard.
//
// A structural backstop, not a proof — it cannot see through arbitrary
// indirection, which is why Deps carries the seams the tests actually assert on.
func TestTheCompositionRootCannotReachAroundItsSeams(t *testing.T) {
	for _, root := range []struct {
		dir            string
		allowGetenv    bool
		allowedImports []string
	}{
		{
			dir:         ".",
			allowGetenv: false,
			allowedImports: []string{
				"context", "encoding/json", "errors", "fmt", "io", "sort", "strings",
				"github.com/fissible/hapax/internal/mode",
				"github.com/fissible/hapax/internal/store",
				"github.com/fissible/hapax/internal/tells",
				"github.com/fissible/hapax/internal/text",
				"github.com/fissible/hapax/internal/llm",
			},
		},
		{
			// The outermost shell is the ONE place the real environment and the
			// real filesystem may be named, because it is what supplies them.
			dir:         "../../cmd/hapax",
			allowGetenv: true,
			allowedImports: []string{
				"context", "os", "time",
				"github.com/fissible/hapax/internal/cli",
			},
		},
	} {
		t.Run(root.dir, func(t *testing.T) {
			allowed := map[string]bool{}
			for _, path := range root.allowedImports {
				allowed[path] = true
			}
			set := token.NewFileSet()
			packages, err := parser.ParseDir(set, root.dir, func(info fs.FileInfo) bool {
				return !strings.HasSuffix(info.Name(), "_test.go")
			}, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", root.dir, err)
			}
			scanned := 0
			for _, pkg := range packages {
				for name, file := range pkg.Files {
					scanned++
					for _, spec := range file.Imports {
						path := strings.Trim(spec.Path.Value, `"`)
						if !allowed[path] {
							t.Errorf("%s imports %q, which the composition root may not reach", name, path)
						}
					}
					ast.Inspect(file, func(n ast.Node) bool {
						selector, ok := n.(*ast.SelectorExpr)
						if !ok {
							return true
						}
						ident, ok := selector.X.(*ast.Ident)
						if !ok {
							return true
						}
						if ident.Name == "os" && selector.Sel.Name == "Getenv" && !root.allowGetenv {
							t.Errorf("%s reads the environment directly; it arrives through Deps", name)
						}
						return true
					})
				}
			}
			if scanned == 0 {
				t.Fatalf("no non-test source was scanned in %s; this guard is vacuous", root.dir)
			}
		})
	}
}
