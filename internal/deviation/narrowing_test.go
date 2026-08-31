package deviation_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// A2b asked for Standardize to take the scoring projection, and it ended up
// generic over 'profile.Fitted | *profile.Profile' with a comment about letting
// callers migrate. Every test passed, because every test passed a Fitted. The
// broad artifact stayed reachable by anyone who wanted it — the same shape as
// the reflection that got around the ingest seam in #53, a constraint met in
// letter because the guard did not forbid the thing.
//
// Compilation cannot catch this. Callers passing Fitted compile either way, so a
// permissive union can come back at any time and nothing goes red. This reads
// the signature instead.
func TestStandardizeExposesOnlyTheFittedProjection(t *testing.T) {
	declaration := functionNamed(t, "Standardize")

	if declaration.Type.TypeParams != nil && len(declaration.Type.TypeParams.List) > 0 {
		t.Errorf("Standardize is generic over %d type parameters; a union is how the broad "+
			"artifact stayed reachable last time", len(declaration.Type.TypeParams.List))
	}
	if declaration.Type.Params == nil || len(declaration.Type.Params.List) != 3 {
		t.Fatalf("Standardize takes %d parameter groups, want 3", groups(declaration))
	}
	if got := typeOf(t, declaration.Type.Params.List[1].Type); got != "profile.Fitted" {
		t.Errorf("Standardize's second parameter is %s, want profile.Fitted", got)
	}
}

// And nothing in the package declares an interface admitting the broad artifact,
// which is where a union would reappear if it were moved out of the signature.
func TestNothingHereDeclaresAUnionOverTheBuildArtifact(t *testing.T) {
	for name, file := range packageFiles(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			union, ok := n.(*ast.BinaryExpr)
			if !ok || union.Op != token.OR {
				return true
			}
			// A type union in a constraint reads as a binary OR of types.
			text := typeOf(t, union.X) + "|" + typeOf(t, union.Y)
			if strings.Contains(text, "profile.Profile") {
				t.Errorf("%s declares a union admitting profile.Profile: %s", name, text)
			}
			return true
		})
	}
}

func packageFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	out := map[string]*ast.File{}
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			out[name] = file
		}
	}
	if len(out) == 0 {
		t.Fatal("no non-test source was parsed; this guard is vacuous")
	}
	return out
}

func functionNamed(t *testing.T, name string) *ast.FuncDecl {
	t.Helper()
	for _, file := range packageFiles(t) {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == name {
				return function
			}
		}
	}
	t.Fatalf("no function named %s", name)
	return nil
}

func groups(declaration *ast.FuncDecl) int {
	if declaration.Type.Params == nil {
		return 0
	}
	return len(declaration.Type.Params.List)
}

// typeOf renders a type expression as written, so the assertion is about the
// declared type rather than about whatever it resolves to.
func typeOf(t *testing.T, expression ast.Expr) string {
	t.Helper()
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typeOf(t, typed.X) + "." + typed.Sel.Name
	case *ast.StarExpr:
		return "*" + typeOf(t, typed.X)
	case *ast.ArrayType:
		return "[]" + typeOf(t, typed.Elt)
	case *ast.BinaryExpr:
		return typeOf(t, typed.X) + "|" + typeOf(t, typed.Y)
	default:
		return "?"
	}
}
