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
			// Neither internal/llm nor internal/store nor net is here, and that
			// is the guarantee rather than a convenience. A1 cannot construct a
			// provider, read a credential or open a store because it cannot
			// NAME them. A failing seam in a harness only covers the paths a
			// test walks; a package that cannot reach a type cannot reach it on
			// any path, including one that lazily fills a nil callback.
			allowedImports: []string{
				// time is here so Deps.Now can be typed. It cannot open a
				// socket or read the environment, so it does not weaken what
				// this list is for.
				"context", "encoding/json", "errors", "fmt", "io", "sort", "strings", "time",
				"github.com/fissible/hapax/internal/mode",
				"github.com/fissible/hapax/internal/tells",
				"github.com/fissible/hapax/internal/text",
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
						// A dot import or a rename makes every later check
						// spelling-dependent: Deps{...} stops being a selector,
						// and os.Getenv stops being called os.Getenv.
						if spec.Name != nil {
							t.Errorf("%s imports %q as %q; the composition root's imports are "+
								"unrenamed so the guards below can see what they are looking at",
								name, path, spec.Name.Name)
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

// A1 constructs no provider, no credential and no store, and the import rule
// above is what says so: internal/cli cannot name internal/llm, internal/store
// or net, so there is no path — including one that lazily fills a nil callback
// inside Run — by which it could.
//
// What is left for the binary is that it builds its Deps by NAME. A positional
// literal populates fields without naming them, which would silently carry
// whatever a later slice adds.
func TestTheBinaryBuildsItsDepsByName(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, "../../cmd/hapax", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing cmd/hapax: %v", err)
	}
	// Deps carries no provider, credential or store field in A1, because
	// internal/cli cannot name those packages at all. What is left to check is
	// that the literal is built by NAME: a positional one would populate
	// whatever fields a later slice adds without saying so.
	scanned, deps := 0, 0
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				literal, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				// Either spelling: cli.Deps, or a bare Deps reachable through a
				// dot import or a local alias.
				switch typed := literal.Type.(type) {
				case *ast.SelectorExpr:
					if typed.Sel.Name != "Deps" {
						return true
					}
				case *ast.Ident:
					if typed.Name != "Deps" {
						return true
					}
				default:
					return true
				}
				deps++
				for _, element := range literal.Elts {
					if _, named := element.(*ast.KeyValueExpr); !named {
						t.Errorf("%s builds Deps positionally, which populates fields "+
							"without naming them; every field must be named", name)
					}
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test source was scanned in cmd/hapax; this guard is vacuous")
	}
	if deps == 0 {
		t.Fatal("no cli.Deps literal was found in cmd/hapax; this guard is vacuous")
	}
}
