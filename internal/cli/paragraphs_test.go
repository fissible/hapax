package cli_test

// `--paragraphs`, and the two things a user reads off the result.
//
// # The indices are the ones `hapax score` printed
//
// score reports segment 0 for the first paragraph. A one-based flag would take
// the numbers a user copied out of that output and name a DIFFERENT paragraph,
// and — because index n-1 also exists — would rewrite it without erroring. So
// zero is a valid value here, and only a negative index is a usage error. The
// test below exists to keep that decision from being quietly reversed by
// someone who finds zero-based flags surprising.
//
// # What the line has to say
//
// Two facts a user cannot recover from targets and counts alone: that they
// chose the paragraphs rather than the tool choosing them, and that the
// improvement claim is about distance rather than a calibrated band. Both are
// closed vocabularies from workflow, so neither the renderer nor the envelope
// may invent a value.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

// explicitOutcome is a completed run under a named target set.
func explicitOutcome() workflow.RewriteOutcome {
	return workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
		Targets: 2, Improved: 1,
		Targeting: workflow.TargetingExplicit, Claim: workflow.ClaimCloserByDistance,
	}, []byte("a revised document\n"))
}

// ---------------------------------------------------------------------------
// Grammar
// ---------------------------------------------------------------------------

// --paragraphs takes a value, so it behaves in both spellings exactly as every
// other value-taking flag does. #82's parser defect was precisely a flag that
// worked in one spelling and not the other.
func TestParagraphsIsAcceptedInBothSpellings(t *testing.T) {
	draft := tempDraft(t)
	for _, args := range [][]string{
		{"rewrite", draft, "--out", draft + ".out", "--model", "llama3", "--paragraphs", "0,2,5"},
		{"rewrite", draft, "--out", draft + ".out", "--model", "llama3", "--paragraphs=0,2,5"},
		{"--paragraphs", "0,2,5", "rewrite", draft, "--out", draft + ".out", "--model", "llama3"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			service := &rewriteService{result: explicitOutcome()}
			got := rewriting(t, service, &spyPublisher{}, args...)
			if got.code != 0 {
				t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
			}
			want := []int{0, 2, 5}
			if !sameIndices(service.request.Paragraphs, want) {
				t.Errorf("paragraphs = %v, want %v", service.request.Paragraphs, want)
			}
		})
	}
}

func sameIndices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Zero is the first paragraph, not a mistake. This is the assertion that stops
// the flag drifting one-based and silently rewriting the wrong text.
func TestParagraphZeroIsTheFirstParagraphAndIsValid(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: explicitOutcome()}

	got := rewriting(t, service, &spyPublisher{},
		"rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", "0")

	if got.code != 0 {
		t.Fatalf("naming paragraph 0 exited %d: %s", got.code, got.stderr)
	}
	if !sameIndices(service.request.Paragraphs, []int{0}) {
		t.Errorf("paragraphs = %v, want [0]", service.request.Paragraphs)
	}
}

// The order a user typed is the order that arrives. Sorting or deduplicating
// silently would make the service see a selection nobody asked for; whether
// the plan cares about order is the plan's business, not the parser's.
func TestTheOrderTypedIsTheOrderPassedThrough(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: explicitOutcome()}

	got := rewriting(t, service, &spyPublisher{},
		"rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", "5,0,2")

	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	if !sameIndices(service.request.Paragraphs, []int{5, 0, 2}) {
		t.Errorf("paragraphs = %v, want [5 0 2] unchanged", service.request.Paragraphs)
	}
}

// Omitting the flag selects nothing, and that is what reaches the service.
//
// Nothing here distinguishes a nil slice from an empty one, and nothing should:
// `--paragraphs=` is a usage error, so an empty-but-present list cannot arrive
// from the command line at all. A presence bit would be state for a case the
// parser makes unreachable.
func TestOmittingParagraphsPassesNoSelectionAtAll(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
		Targets: 1, Improved: 1,
		Targeting: workflow.TargetingAutomatic, Claim: workflow.ClaimCalibratedBand,
	}, []byte("a revised document\n"))}

	got := rewriting(t, service, &spyPublisher{}, out(draft, draft+".out")...)

	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	if len(service.request.Paragraphs) != 0 {
		t.Errorf("paragraphs = %v, want none", service.request.Paragraphs)
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// Every one of these is a usage error the user can fix by retyping, so each
// exits 2 and the service is never reached. A malformed list that reached the
// service would become a selection nobody typed.
func TestAMalformedParagraphListIsAUsageError(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"not a number", "one"},
		{"negative", "-1"},
		{"negative among valid", "0,-2"},
		{"a float", "1.5"},
		{"repeated", "1,1"},
		{"repeated apart", "0,3,0"},
		{"empty element", "0,,2"},
		{"trailing comma", "0,2,"},
		{"leading comma", ",0,2"},
		{"whitespace only element", "0, ,2"},
		{"a bare comma", ","},
		{"hex", "0x2"},
		{"signed positive", "+2"},
	}
	draft := tempDraft(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			service := &rewriteService{result: explicitOutcome()}
			got := rewriting(t, service, &spyPublisher{},
				"rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", c.value)

			if got.code != 2 {
				t.Errorf("--paragraphs %q exited %d, want 2", c.value, got.code)
			}
			if service.calls != 0 {
				t.Errorf("--paragraphs %q reached the service", c.value)
			}
			if strings.TrimSpace(got.stdout) != "" {
				t.Errorf("--paragraphs %q printed %q on an invalid invocation", c.value, got.stdout)
			}
			if !strings.Contains(got.stderr, "--paragraphs") {
				t.Errorf("the diagnostic %q does not name the flag at fault", strings.TrimSpace(got.stderr))
			}
		})
	}
}

// An empty value is the same usage error every other value-taking flag reports,
// reached through both spellings.
func TestParagraphsRequiresAValue(t *testing.T) {
	draft := tempDraft(t)
	for _, args := range [][]string{
		{"rewrite", draft, "--out", draft + ".out", "--model", "llama3", "--paragraphs="},
		{"rewrite", draft, "--out", draft + ".out", "--model", "llama3", "--paragraphs"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			service := &rewriteService{result: explicitOutcome()}
			if got := rewriting(t, service, &spyPublisher{}, args...); got.code != 2 {
				t.Errorf("code = %d, want 2", got.code)
			}
			if service.calls != 0 {
				t.Error("an invalid invocation reached the service")
			}
		})
	}
}

// And it may not be repeated, like every other flag in the table. Two lists
// would leave the parser choosing which one the user meant.
func TestParagraphsMayNotBeRepeated(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: explicitOutcome()}

	got := rewriting(t, service, &spyPublisher{},
		"rewrite", draft, "--out", draft+".out", "--model", "llama3",
		"--paragraphs", "0", "--paragraphs", "2")

	if got.code != 2 {
		t.Errorf("code = %d, want 2", got.code)
	}
	if service.calls != 0 {
		t.Error("a repeated flag reached the service")
	}
}

// ---------------------------------------------------------------------------
// What the result says
// ---------------------------------------------------------------------------

// Both facts reach the human line. Neither is recoverable from the counts: a
// run with two targets looks identical whether the tool or the user chose them.
func TestTheHumanLineSaysHowTargetsWereChosenAndWhatIsClaimed(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: explicitOutcome()}

	got := rewriting(t, service, &spyPublisher{},
		"rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", "0,1")

	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	fields := humanFields(t, got.stdout)
	if fields["selection"] != string(workflow.TargetingExplicit) {
		t.Errorf("selection = %q, want %q", fields["selection"], workflow.TargetingExplicit)
	}
	if fields["claim"] != string(workflow.ClaimCloserByDistance) {
		t.Errorf("claim = %q, want %q", fields["claim"], workflow.ClaimCloserByDistance)
	}
}

// And the human line says whether a release existed, because without it a
// person reading one line cannot tell "there was no calibration to be had"
// from "calibration existed and is not what this claim rests on". Those are
// different situations with different next steps, and only the JSON envelope
// could distinguish them otherwise.
func TestTheHumanLineSaysWhetherCalibrationWasAvailable(t *testing.T) {
	draft := tempDraft(t)
	for _, available := range []bool{true, false} {
		t.Run(map[bool]string{true: "available", false: "absent"}[available], func(t *testing.T) {
			report := workflow.RewriteReport{
				PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
				Targets: 1, Improved: 1,
				Targeting: workflow.TargetingExplicit, Claim: workflow.ClaimCloserByDistance,
				CalibrationAvailable: available,
			}
			service := &rewriteService{result: workflow.NewRewriteOutcome(report, []byte("revised\n"))}

			got := rewriting(t, service, &spyPublisher{},
				"rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", "0")

			if got.code != 0 {
				t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
			}
			fields := humanFields(t, got.stdout)
			want := map[bool]string{true: "true", false: "false"}[available]
			// False is a measurement, not an absence, so it must render rather
			// than being omitted the way an empty string is.
			if fields["calibration_available"] != want {
				t.Errorf("calibration_available = %q, want %q", fields["calibration_available"], want)
			}
		})
	}
}

// The envelope carries the same two facts under stable keys, plus whether a
// release existed at all — which is how a reader tells "no calibration to be
// had" from "calibration existed and was not what this claim rests on".
func TestTheEnvelopeCarriesSelectionClaimAndCalibrationAvailability(t *testing.T) {
	draft := tempDraft(t)
	report := workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
		Targets: 2, Improved: 1,
		Targeting: workflow.TargetingExplicit, Claim: workflow.ClaimCloserByDistance,
		CalibrationAvailable: true,
	}
	service := &rewriteService{result: workflow.NewRewriteOutcome(report, []byte("revised\n"))}

	got := rewriting(t, service, &spyPublisher{},
		"--json", "rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", "0,1")

	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	var envelope struct {
		Result struct {
			Selection            string `json:"selection"`
			Claim                string `json:"claim"`
			CalibrationAvailable bool   `json:"calibration_available"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &envelope); err != nil {
		t.Fatalf("decode %q: %v", got.stdout, err)
	}
	if envelope.Result.Selection != string(workflow.TargetingExplicit) {
		t.Errorf("selection = %q, want %q", envelope.Result.Selection, workflow.TargetingExplicit)
	}
	if envelope.Result.Claim != string(workflow.ClaimCloserByDistance) {
		t.Errorf("claim = %q, want %q", envelope.Result.Claim, workflow.ClaimCloserByDistance)
	}
	if !envelope.Result.CalibrationAvailable {
		t.Error("calibration_available = false on a run that reported it available")
	}
}

// The renderer may not invent a claim. Whatever workflow decided is what is
// printed, so a calibrated automatic run says calibrated-band and an explicit
// run on the same store does not.
func TestTheRendererReportsTheClaimWorkflowMade(t *testing.T) {
	draft := tempDraft(t)
	for _, c := range []struct {
		name      string
		targeting workflow.Targeting
		claim     workflow.Claim
	}{
		{"automatic", workflow.TargetingAutomatic, workflow.ClaimCalibratedBand},
		{"explicit", workflow.TargetingExplicit, workflow.ClaimCloserByDistance},
	} {
		t.Run(c.name, func(t *testing.T) {
			report := workflow.RewriteReport{
				PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
				Targets: 1, Improved: 1, Targeting: c.targeting, Claim: c.claim,
			}
			service := &rewriteService{result: workflow.NewRewriteOutcome(report, []byte("revised\n"))}

			got := rewriting(t, service, &spyPublisher{}, out(draft, draft+".out")...)

			if got.code != 0 {
				t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
			}
			fields := humanFields(t, got.stdout)
			if fields["claim"] != string(c.claim) {
				t.Errorf("claim = %q, want %q", fields["claim"], c.claim)
			}
			if fields["selection"] != string(c.targeting) {
				t.Errorf("selection = %q, want %q", fields["selection"], c.targeting)
			}
		})
	}
}

// A refusal names the paragraph problem in a form a script can read, and writes
// nothing. no-such-paragraph is discovered after the draft is scored, so it
// cannot be a parse error, but it is still the user's mistake to fix.
func TestNamingAParagraphThatDoesNotExistIsRefusedNotWritten(t *testing.T) {
	draft := tempDraft(t)
	publisher := &spyPublisher{}
	service := &rewriteService{result: refused(workflow.RefusalNoSuchParagraph)}

	got := rewriting(t, service, publisher,
		"--json", "rewrite", draft, "--out", draft+".out", "--model", "llama3", "--paragraphs", "99")

	if got.code != 4 {
		t.Errorf("code = %d, want 4", got.code)
	}
	if len(publisher.published) != 0 {
		t.Errorf("%d publications under a refusal", len(publisher.published))
	}
	var envelope struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &envelope); err != nil {
		t.Fatalf("decode %q: %v", got.stdout, err)
	}
	if envelope.Reason != workflow.RefusalNoSuchParagraph {
		t.Errorf("reason = %q, want %q", envelope.Reason, workflow.RefusalNoSuchParagraph)
	}
	// The status is the field a script branches on. A refusal that reported
	// status "ok" with a reason beside it would read as a completed run.
	if envelope.Status != "refused" {
		t.Errorf("status = %q, want refused", envelope.Status)
	}
}

// The help text mentions the flag. #81's finding was that the tool could not be
// used because nothing said how; a flag that only exists in the source repeats
// that.
func TestTheUsageTextDocumentsParagraphs(t *testing.T) {
	if !strings.Contains(cli.Usage, "--paragraphs") {
		t.Error("the usage text does not mention --paragraphs")
	}
}
