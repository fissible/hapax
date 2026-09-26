package workflow_test

// The seam between the Runner and the gate.
//
// Every other test of #91 and #107 sits on one side of it: the policy tests in
// `internal/rewrite` drive a `fakeGate`, and the gate tests in this package call
// `executionGate{}.Language(...)` directly. So each half is tested against the
// other's fake, and nothing drives real prose through the real Runner into the
// real gate — which is the same defect the store slice records for
// `recorder.RecordAttempt`, where deleting one line passed the entire
// repository.
//
// Proven reachable here too: making the gate's verdict inert at the Runner's
// construction site passes `go test ./...`. This line is the only place the
// declared ceiling ever meets a real paragraph.
//
// It also answers reachability without asserting anything about the token
// floor. A test that drives a paragraph end to end has to clear the floor to
// reach the gate at all, so if the fixture is below it the test fails rather
// than quietly proving nothing.

import (
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/workflow"
)

// scriptDraft carries ONE Han character in its first paragraph — 1 letter of
// 119, or 0.84%, which is below the ceiling and far below establishment. That
// is what makes the refusal `language-growth` rather than `language`: Han is
// already present, so nothing is introduced, and #91's guard is silent.
const scriptParagraph = "A paragraph of ordinary prose that runs on past a single sentence so the " +
	"structure pass reads it as prose 著 rather than as a heading; it says a thing."

// The candidate keeps Han present and takes it to 20 letters of 118, or 16.95%
// — over the ceiling, on more letters than the original had.
const scriptCandidate = "A paragraph of ordinary prose 著著著著著著著著著著著著 runs on past a single " +
	"sentence. The structure 著著著著著著著著 pass reads it as prose rather than a heading."

func scriptDraft() string {
	return "# A heading, which is a leaf and is not admitted\n\n" +
		scriptParagraph + "\n\n" + paragraphTwo + "\n\n"
}

// A real paragraph, through the real Runner, refused as language-growth.
func TestTheRunnerRefusesACandidateThatCrossesTheScriptCeiling(t *testing.T) {
	t.Parallel()
	root := installRelease(t, 0.05, 5.0)
	draft := writeDraft(t, root, scriptDraft())
	plan := planned(t, planRequest(root, draft))

	local := &arm{provider: newProvider(t, map[string][]string{
		scriptParagraph: {scriptCandidate},
	})}
	runner, _ := executingRunner(local, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.Refusal != "" {
		t.Fatalf("the run was refused as %q; this test needs it to reach the loop",
			result.Refusal)
	}
	if len(result.Outcomes) == 0 {
		t.Fatal("no targets, so the gate never ran; the fixture paragraph is not " +
			"being planned as a target")
	}

	var sawGrowth, sawIntroduction bool
	var recorded []string
	for _, outcome := range result.Outcomes {
		for _, reason := range outcome.Rejections {
			recorded = append(recorded, reason)
			switch reason {
			case string(rewrite.RejectionLanguageGrowth):
				sawGrowth = true
			case string(rewrite.RejectionLanguage):
				sawIntroduction = true
			}
		}
	}
	if !sawGrowth {
		t.Errorf("no attempt was refused as %q; recorded %v",
			rewrite.RejectionLanguageGrowth, recorded)
	}
	// And NOT as an introduction, which is what makes this a #107 test rather
	// than a #91 one: Han is in the original, so a gate whose growth half is
	// inert would report nothing here at all.
	if sawIntroduction {
		t.Errorf("an attempt was refused as %q; Han is present in the original, so "+
			"nothing is introduced: %v", rewrite.RejectionLanguage, recorded)
	}
	// The prose the guard refused must not reach the file.
	if strings.Contains(string(result.Bytes), scriptCandidate) {
		t.Error("the refused candidate was written into the draft")
	}
}

// And the same paragraph with an admissible candidate is accepted, so the test
// above is not passing because the Runner refuses everything.
func TestTheRunnerAcceptsACandidateBelowTheScriptCeiling(t *testing.T) {
	t.Parallel()
	root := installRelease(t, 0.05, 5.0)
	draft := writeDraft(t, root, scriptDraft())
	plan := planned(t, planRequest(root, draft))

	// One Han letter still, and shorter: the count did not grow.
	const admissible = "A paragraph of ordinary prose 著 runs on past a single sentence. " +
		"The structure pass reads it as prose rather than as a heading."

	local := &arm{provider: newProvider(t, map[string][]string{
		scriptParagraph: {admissible},
	})}
	runner, _ := executingRunner(local, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	for _, outcome := range result.Outcomes {
		for _, reason := range outcome.Rejections {
			if reason == string(rewrite.RejectionLanguageGrowth) ||
				reason == string(rewrite.RejectionLanguage) {
				t.Errorf("an admissible candidate was refused as %q", reason)
			}
		}
	}
	// And ACCEPTED, not merely unrefused. Without this the test passes on a run
	// that never reached the gate at all, which is exactly the vacuity it exists
	// to rule out for its sibling.
	if result.State != workflow.RewriteImproved {
		t.Errorf("State = %q, want %q", result.State, workflow.RewriteImproved)
	}
	if result.Improved != 1 {
		t.Errorf("Improved = %d, want 1", result.Improved)
	}
	if len(result.Outcomes) == 0 || !result.Outcomes[0].Changed {
		t.Error("the first target did not change")
	}
	if !strings.Contains(string(result.Bytes), admissible) {
		t.Error("the accepted candidate was not written into the draft")
	}
}
