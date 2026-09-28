package cli_test

// #111, at the seam where a person reads it.
//
// `workflow` refuses to target a paragraph this tool already wrote, and counts
// them. Without the count reaching here, a second `--in-place` run reports
// `plan_state=nothing-to-change` and exits 0, which reads as "your draft is in
// your voice" when it means "every paragraph here is my own output". Those are
// different situations with different next steps.
//
// A count rather than a flag, because a run can be a mixture: two paragraphs
// rewritten and one refused as already-rewritten is an ordinary second pass over
// an edited draft.
//
// `paragraphs_below_floor` is the model for the RENDERING — `fields` draws the
// distinction that EMPTY is an absence and is omitted while ZERO is a
// measurement and is not, so `below-floor=0` prints today and so does this. It
// is NOT a precedent for the plumbing: `ParagraphsBelowFloor` reaches the plan
// and stops there. It is on no rewrite report and no rewrite payload, and
// `below-floor=` renders only on the score line. This count makes two more hops
// than that one ever has, and each is a line someone can forget.

import (
	"encoding/json"
	"testing"

	"github.com/fissible/hapax/internal/workflow"
)

// mixed is a run where one paragraph was rewritten and one was refused as
// already-rewritten — the shape a second pass over an edited draft produces.
func mixed(alreadyRewritten int) workflow.RewriteOutcome {
	return workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
		Targets: 1, Improved: 1,
		Targeting: workflow.TargetingExplicit, Claim: workflow.ClaimCloserByDistance,
		ParagraphsAlreadyRewritten: alreadyRewritten,
	}, []byte("revised\n"))
}

// Both renderings carry the count, and a zero is a measurement rather than an
// absence.
//
// The zero row is the half that a `f.Add` instead of `f.AddInt` would fail, and
// the half an `omitempty` on the JSON tag would fail: a consumer reading
// `paragraphs_already_rewritten` needs to tell "none were refused" from "this
// version does not report it", and an omitted member cannot.
func TestBothRenderingsCarryTheAlreadyRewrittenCount(t *testing.T) {
	for _, count := range []int{2, 0} {
		t.Run(map[bool]string{true: "some were refused", false: "none were"}[count > 0], func(t *testing.T) {
			draft := tempDraft(t)
			args := []string{"rewrite", draft, "--out", draft + ".out", "--model", "llama3",
				"--paragraphs", "0"}

			human := rewriting(t, &rewriteService{result: mixed(count)}, &spyPublisher{}, args...)
			if human.code != 0 {
				t.Fatalf("code = %d, stderr %q", human.code, human.stderr)
			}
			got, present := humanFields(t, human.stdout)["already-rewritten"]
			if !present {
				t.Errorf("the line has no already-rewritten: %q", human.stdout)
			}
			if want := map[int]string{2: "2", 0: "0"}[count]; got != want {
				t.Errorf("already-rewritten=%q, want %q: %q", got, want, human.stdout)
			}

			encoded := rewriting(t, &rewriteService{result: mixed(count)}, &spyPublisher{},
				append([]string{"--json"}, args...)...)
			if encoded.code != 0 {
				t.Fatalf("code = %d, stderr %q", encoded.code, encoded.stderr)
			}
			var envelope struct {
				Result map[string]any `json:"result"`
			}
			if err := json.Unmarshal([]byte(encoded.stdout), &envelope); err != nil {
				t.Fatalf("decode %q: %v", encoded.stdout, err)
			}
			value, carried := envelope.Result["paragraphs_already_rewritten"]
			if !carried {
				t.Fatalf("the envelope has no paragraphs_already_rewritten: %s", encoded.stdout)
			}
			if value != float64(count) {
				t.Errorf("paragraphs_already_rewritten = %v, want %d", value, count)
			}
		})
	}
}

// A negative count is incoherent and must not render.
//
// The three sibling counts on this payload are already checked this way
// (`Targets`, `Improved`, `NotImproved` in `validRewriteResult`), and a count
// that arrived negative would mean the layer below miscounted — which a reader
// should never be shown as though it were a measurement.
func TestRenderRefusesANegativeAlreadyRewrittenCount(t *testing.T) {
	draft := tempDraft(t)
	got := rewriting(t, &rewriteService{result: mixed(-1)}, &spyPublisher{},
		"rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", "0")

	if got.code == 0 {
		t.Errorf("a negative count rendered and exited 0: %q", got.stdout)
	}
}

// The nothing-to-change run is the one this exists for.
//
// A second `--in-place` pass refuses every paragraph, so the plan has no targets
// and the state is `nothing-to-change` — the same state a draft that genuinely
// reads as the author produces. The count is the only thing in either rendering
// that separates them, so it is asserted on that exact report rather than only on
// the improved one.
func TestANothingToChangeRunSaysHowManyWereItsOwnOutput(t *testing.T) {
	draft := tempDraft(t)
	report := workflow.RewriteReport{
		PlanState: workflow.StateNothingToChange, State: workflow.RewriteNoTargets,
		ParagraphsAlreadyRewritten: 3,
	}
	service := &rewriteService{result: workflow.NewRewriteOutcome(report, []byte("unchanged\n"))}

	got := rewriting(t, service, &spyPublisher{},
		"rewrite", draft, "--out", draft+".out", "--model", "llama3")

	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	fields := humanFields(t, got.stdout)
	if fields["rewrite_state"] != string(workflow.RewriteNoTargets) {
		t.Fatalf("rewrite_state = %q, want %q; the fixture is not the run this test is "+
			"about", fields["rewrite_state"], workflow.RewriteNoTargets)
	}
	if fields["already-rewritten"] != "3" {
		t.Errorf("already-rewritten=%q on a nothing-to-change run, want 3: %q",
			fields["already-rewritten"], got.stdout)
	}
}
