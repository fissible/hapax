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

// A1 constructs no provider and no credential, and the binary is where that
// could quietly stop being true: the Run harness fails the test if its injected
// Credentials or Dial seam is called, but the binary runs against the real
// process and the guard above must let it read the real environment.
//
// So the check is on the NAMES. Checking one cli.Deps literal was not enough —
// a main could build an empty literal to satisfy it and assign the fields
// afterwards. If Credentials, Dial and OpenStore do not appear in cmd/hapax at
// all, in any form, they cannot be populated by any route.
func TestTheBinaryNeverNamesAProviderCredentialOrStore(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, "../../cmd/hapax", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing cmd/hapax: %v", err)
	}
	forbidden := map[string]bool{"Credentials": true, "Dial": true, "OpenStore": true}
	scanned := 0
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if ok && forbidden[ident.Name] {
					t.Errorf("%s names %s; no A1 command is served by one, so the binary "+
						"must not be able to populate it by any route", name, ident.Name)
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test source was scanned in cmd/hapax; this guard is vacuous")
	}
}
