package workflow_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/workflow"
)

// B1 builds everything hapax rewrite decides before a provider is involved, and
// deliberately ships no command. That is only true if cli cannot call it: the
// CLI reaches this package through workflow.Service and nothing else, so Plan
// staying off that interface is what keeps the slice closed.
//
// A compile-time sentinel rather than a comment. Adding Plan to Service is a
// reasonable-looking edit, and it is exactly the edit that would let a half-built
// rewrite become reachable.

// planless has every method Service declares and no Plan. If Plan is added to
// Service, this stops compiling.
type planless struct{}

func (planless) Index(context.Context, workflow.IndexRequest) (workflow.IndexResult, error) {
	return workflow.IndexResult{}, nil
}
func (planless) Profile(context.Context, workflow.ProfileRequest) (workflow.ProfileResult, error) {
	return workflow.ProfileResult{}, nil
}
func (planless) Eval(context.Context, workflow.EvalRequest) (workflow.EvalResult, error) {
	return workflow.EvalResult{}, nil
}

// Rewrite exists because B2b-2b widened Service to carry the command. What this
// fake is for is the boundary below, not the rewrite path.
func (planless) Rewrite(context.Context, workflow.RewriteInput) (workflow.RewriteOutcome, error) {
	return workflow.RewriteOutcome{}, nil
}

func (planless) Score(context.Context, workflow.ScoreRequest) (workflow.ScoreResult, error) {
	return workflow.ScoreResult{}, nil
}

var _ workflow.Service = planless{}

// planner is the other half of the boundary: the Runner must have Plan, or the
// sentinel above would be satisfied by Plan not existing anywhere.
//
// Compile-time, not a runtime type assertion. Asserting at runtime that the
// value you just called Plan on has a Plan method is a tautology, and the
// operational tests in plan_test.go already prove Plan does its job.
type planner interface {
	Plan(context.Context, workflow.RewriteRequest) (workflow.RewritePlan, error)
}

var (
	_ planner          = (*workflow.Runner)(nil)
	_ workflow.Service = (*workflow.Runner)(nil)
)

// And an empty request is an error rather than a plan over nothing.
func TestPlanRefusesARequestWithNoDraft(t *testing.T) {
	t.Parallel()
	if _, err := workflow.Default().Plan(ctx(), workflow.RewriteRequest{}); err == nil {
		t.Error("Plan accepted an empty request")
	}
}

// exemplar.Select is handed a profile.Fitted, not a *profile.Profile.
//
// This is A2c's narrowing again. Select reads exactly two things off the profile
// it is given — prof.Fitted() and prof.ID — and Fitted carries both. Taking the
// build artifact instead would force every caller holding a stored profile to
// reconstruct a *profile.Profile it does not have, and a reconstruction that
// differs anywhere from the original changes which exemplars the author gets
// while every test still passes.
//
// The signature is read rather than relied on to fail compilation, because A2b
// met a narrowing that was satisfied in letter by a type union: every test
// passed a Fitted, so nothing caught it.
func TestSelectTakesTheFittedProjectionAndNotTheBuildArtifact(t *testing.T) {
	t.Parallel()
	packages, err := parser.ParseDir(token.NewFileSet(), "../exemplar", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing internal/exemplar: %v", err)
	}

	found := false
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != "Select" || function.Recv != nil {
					continue
				}
				found = true
				if function.Type.TypeParams != nil && len(function.Type.TypeParams.List) > 0 {
					t.Error("Select is generic; a type parameter admits the build artifact " +
						"alongside the projection and every test here passes a projection")
				}
				if len(function.Type.Params.List) == 0 {
					t.Fatal("Select takes no parameters")
				}
				if got := typeOf(function.Type.Params.List[0].Type); got != "profile.Fitted" {
					t.Errorf("Select's first parameter is %s, want profile.Fitted", got)
				}
			}
		}
	}
	if !found {
		t.Fatal("no Select declaration was found; this guard is vacuous")
	}
}

// typeOf renders a declared type as it is spelled in the source, so a union
// written as an interface constraint or as A|B is visible rather than collapsed.
func typeOf(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typeOf(typed.X) + "." + typed.Sel.Name
	case *ast.StarExpr:
		return "*" + typeOf(typed.X)
	case *ast.ArrayType:
		return "[]" + typeOf(typed.Elt)
	case *ast.BinaryExpr:
		return typeOf(typed.X) + "|" + typeOf(typed.Y)
	default:
		return "?"
	}
}

// store.Profile.Fitted() is the one mapping from persisted columns to the
// projection that decides every distance. Until this slice there were two: this
// package had an unexported fittedFrom, and profile.Profile had its own Fitted.
//
// The reason this test exists rather than being taken on trust: the plan and the
// selectFromStore oracle both go through the stored projection, so a mapper that
// dropped a stat, or read variance where it wanted mean, would give both of them
// the same wrong answer and they would agree. The built profile is the
// independent side — it never touches the store at all.
func TestTheStoredProjectionEqualsTheBuiltOne(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 6)
	snapshot, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	built, err := profile.Build(root, snapshot, profile.DefaultRequirements())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	indexed(t, indexRequest(root))
	stored := persistedBundle(t, defaultStorePath(root), "essays").Profile

	// The built profile is rebound to the persisted snapshot's identity before
	// the comparison, because #56 — one corpus has two of them. profile.Build
	// binds to corpus.Snapshot.ID and ingest derives its own over the persisted
	// membership, so Index calls RebindSnapshot and the stored profile is
	// legitimately not the ID a fresh Build produces. Asserting otherwise
	// asserts that #56 is already fixed.
	//
	// The independence that makes this test worth having survives: the built side
	// still never reads the store, so a mapper that dropped a stat or read
	// variance where it wanted mean still fails. Only the identity
	// reconciliation is applied to both sides.
	built.RebindSnapshot(stored.SnapshotID)
	want, err := built.Fitted()
	if err != nil {
		t.Fatalf("built Fitted: %v", err)
	}
	if stored.ID != want.ID {
		t.Fatalf("the store holds profile %s and the build produced %s; these are not the "+
			"same profile and comparing their projections proves nothing", stored.ID, want.ID)
	}
	got, err := stored.Fitted()
	if err != nil {
		t.Fatalf("stored Fitted: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("the stored projection is\n%+v\nand the built one is\n%+v", got, want)
	}
	// DeepEqual over an empty Stats slice on both sides would pass, and Stats is
	// the whole point of the projection.
	if len(got.Stats) == 0 {
		t.Error("the projection carries no feature statistics")
	}
}

// RewritePlan is a wire-adjacent contract: cli projects it into hapax.v1, so a
// field added or retyped here changes what a caller reads. The tests in
// plan_test.go pin the fields they exercise; this pins the SHAPE, so a field
// nothing asserts cannot appear unnoticed and a type cannot widen underneath one
// that does.
//
// The same reasoning as the store's column allowlist, and for the same reason: a
// payload whose members are only ever checked by the tests that happen to read
// them grows members nobody checks.
func TestTheRewritePlanSurfaceIsExactlyThis(t *testing.T) {
	t.Parallel()
	assertShape(t, reflect.TypeOf(workflow.RewritePlan{}), [][2]string{
		{"StorePath", "string"},
		{"CorpusRoot", "string"},
		{"Path", "string"},
		{"Selection", "workflow.Selection"},
		{"Available", "[]string"},
		{"Refusal", "string"},
		{"ProfileID", "string"},
		{"ReferenceID", "string"},
		{"ReleaseID", "string"},
		{"DraftSnapshotID", "string"},
		{"ParagraphsBelowFloor", "int"},
		// #81. Targeting says who chose the paragraphs and Claim says which
		// question the run answered; they are separate because an explicit
		// selection on a calibrated store answers the distance question while a
		// calibration happens to exist. CalibrationAvailable is that third
		// fact, kept apart from the claim so neither can imply the other.
		{"Targeting", "workflow.Targeting"},
		{"Claim", "workflow.Claim"},
		{"CalibrationAvailable", "bool"},
		{"Segments", "[]workflow.PlannedSegment"},
		{"Targets", "int"},
		{"State", "workflow.PlanState"},
		{"ExemplarSelectionID", "string"},
		{"ExemplarCertificateID", "string"},
		{"ExemplarNodes", "[]string"},
	})
}

func TestThePlannedSegmentSurfaceIsExactlyThis(t *testing.T) {
	t.Parallel()
	assertShape(t, reflect.TypeOf(workflow.PlannedSegment{}), [][2]string{
		{"Index", "int"},
		{"NodeID", "string"},
		{"Offset", "int"},
		{"Length", "int"},
		{"LexicalTokens", "int"},
		{"Band", "workflow.BandOutcome"},
		{"Disposition", "workflow.Disposition"},
	})
}

// assertShape compares a struct's exported fields, in declaration order, against
// the names and types this slice froze. Order is included because these are
// read as records: a reordering is a change a reviewer should have to see.
func assertShape(t *testing.T, typed reflect.Type, want [][2]string) {
	t.Helper()
	var got [][2]string
	for i := 0; i < typed.NumField(); i++ {
		field := typed.Field(i)
		if !field.IsExported() {
			t.Errorf("%s.%s is unexported; this contract is the public shape", typed.Name(), field.Name)
			continue
		}
		got = append(got, [2]string{field.Name, field.Type.String()})
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s is\n%v\nand was frozen as\n%v", typed.Name(), got, want)
	}
}
