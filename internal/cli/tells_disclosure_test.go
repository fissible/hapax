package cli_test

// #117, at the seam where a person finally reads it.
//
// `tells` computes whether its gate can reject anything and `workflow` puts the
// answer in the report. This layer is the last chance to drop it, and dropping
// it is the default: `rewriteResultFrom` copies member by member, so a new
// member of `RewriteReport` reaches nobody until a line is added there.
//
// # Why this is driven through the command
//
// An earlier draft called `rewriteResultFrom` directly, on the claim that
// nothing exercised it. That was false — every test in this package that builds
// a report and runs the command goes through it, and several assert members that
// arrived that way. The real gap is narrower and worse: no test supplies a
// report carrying THIS member, so the one missing line is invisible, and an
// implementation correct in `tells` and `workflow` would print exactly what it
// printed before.
//
// So these run the command, which is also the only way to see the two
// renderings a reader actually meets, and follows
// `TestTheEnvelopeCarriesSelectionClaimAndCalibrationAvailability` — the same
// shape for the same kind of member.
//
// # Named as a reason, spelled as a code
//
// A reason rather than a boolean, following `not_ready_reason`: a consumer
// wanting the flag tests emptiness, and one wanting to tell a human why gets the
// words without this layer holding a second table. Unlike that field it is a
// kebab CODE, because it has a second rendering — `fields.Add` silently DROPS
// any value containing a space, so prose would vanish from the line most likely
// to be read with nothing anywhere reporting it. `tells` owns the vocabulary;
// what is asserted here is that both renderings carry it intact.

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/workflow"
)

// Render refuses a reason no package declares, and accepts the two that are.
//
// This package's standing rule, from
// `TestRenderRefusesAResultOutsideTellsVocabularies`: "the vocabularies a result
// may carry are the OWNING package's. Render checks them, so cli cannot hold a
// second copy that drifts." `PlanState` and `RewriteState` are already checked
// that way. Without the same check here, the answer to "could a layer below
// invent its own string" is only "not in the fixtures we wrote" — with it, the
// answer is no for every value, which is the stronger claim and the cheaper one.
//
// Checked on refusals too, because `validRewriteResult` returns early for a
// refused status: a check placed after that early return would never cover them.
// Not because a refusal must be silent — the workflow slice's
// `TestARefusalTakenAfterTheProfileLoadsStillDiscloses` requires stale-draft
// and stale-exemplars to CARRY the reason, and only PRE-execution refusals must
// not. Which is why the row below accepts a declared reason on a refused status
// rather than rejecting one.
func TestRenderRefusesATellsReasonNoPackageDeclares(t *testing.T) {
	// All three statuses a rewrite document can carry. `StatusAdverse` is a
	// none-improved run — executed, so it DOES carry a reason, and one of the
	// commonest real outputs. It shares `validRewriteResult`'s non-refused path
	// with `StatusOK`, so a check that covered one and not the other would be
	// perverse rather than plausible; it is here because this check is the only
	// thing standing between an invented string and a reader.
	for _, status := range []cli.Status{cli.StatusOK, cli.StatusAdverse, cli.StatusRefused} {
		t.Run(string(status), func(t *testing.T) {
			for _, c := range []struct {
				name   string
				reason string
				want   bool
			}{
				{"an undeclared reason", "vibes", false},
				{"no reason at all", "", true},
				{"the declared reason", tells.InactiveNoValidatedRule, true},
			} {
				t.Run(c.name, func(t *testing.T) {
					doc := rewriteDocument(status)
					result := doc.Result.(cli.RewriteResult)
					result.TellsInactiveReason = c.reason
					doc.Result = result
					for _, asJSON := range []bool{true, false} {
						var out bytes.Buffer
						err := doc.Render(&out, asJSON)
						if c.want && err != nil {
							t.Errorf("json=%v: Render: %v", asJSON, err)
						}
						if !c.want && err == nil {
							t.Errorf("json=%v: rendered %q", asJSON, out.String())
						}
					}
				})
			}
		})
	}
}

// rewriteDocument is a coherent rewrite document in either status, since
// `validRewriteResult` checks far more than the member under test and an
// incoherent fixture would refuse for the wrong reason.
func rewriteDocument(status cli.Status) cli.Document {
	doc := cli.Document{Schema: cli.Schema, Command: "rewrite", Status: status}
	switch status {
	case cli.StatusRefused:
		doc.Reason = "uncalibrated"
		doc.Result = cli.RewriteResult{Path: "draft.md", Refusal: "uncalibrated"}
	case cli.StatusAdverse:
		// `validRewriteResult` requires adverse and none-improved to agree, so
		// the counts have to say nothing improved as well as the state.
		doc.Result = cli.RewriteResult{
			Path: "draft.md", PlanState: workflow.StateTargetsPlanned,
			RewriteState: workflow.RewriteNoneImproved, Targets: 1, NotImproved: 1,
		}
	default:
		doc.Result = cli.RewriteResult{
			Path: "draft.md", PlanState: workflow.StateTargetsPlanned,
			RewriteState: workflow.RewriteImproved, Targets: 1, Improved: 1,
		}
	}
	return doc
}

// disclosing is an improved run whose report carries #117's disclosure.
func disclosing(reason string) workflow.RewriteOutcome {
	return workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
		Targets: 1, Improved: 1,
		Targeting: workflow.TargetingExplicit, Claim: workflow.ClaimCloserByDistance,
		TellsInactiveReason: reason,
	}, []byte("revised\n"))
}

// Both renderings carry it, and both stay quiet when there is nothing to say.
//
// Table-driven over the pair because the two halves fail for different reasons
// and an implementation can get either one alone: a member emitted
// unconditionally would pass the disclosing row and tell every future reader
// that a validated gate is inert, and a tag without `omitempty` would put
// `"tells_inactive_reason": ""` in every envelope ever emitted.
func TestBothRenderingsCarryTheTellsDisclosure(t *testing.T) {
	for _, c := range []struct {
		name   string
		reason string
	}{
		{"the report discloses an inactive gate", tells.InactiveNoValidatedRule},
		{"the report discloses nothing", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			draft := tempDraft(t)
			// `--paragraphs 0` names the draft's only paragraph, zero-based, and
			// is what makes the report's TargetingExplicit coherent with the
			// request the service records.
			args := []string{"rewrite", draft, "--out", draft + ".out", "--model", "llama3",
				"--paragraphs", "0"}

			human := rewriting(t, &rewriteService{result: disclosing(c.reason)}, &spyPublisher{}, args...)
			if human.code != 0 {
				t.Fatalf("code = %d, stderr %q", human.code, human.stderr)
			}
			// The whole key=value, not the key alone: `fields.Add` drops a value
			// it cannot render, so a prose reason would leave the line carrying
			// neither and an assertion on the key would be the one that missed it.
			got, present := humanFields(t, human.stdout)["tells_inactive_reason"]
			if c.reason == "" {
				if present {
					t.Errorf("the line carries tells_inactive_reason=%q with nothing to "+
						"disclose: %q", got, human.stdout)
				}
			} else if got != c.reason {
				t.Errorf("the line carries tells_inactive_reason=%q, want %q: %q",
					got, c.reason, human.stdout)
			}

			encoded := rewriting(t, &rewriteService{result: disclosing(c.reason)}, &spyPublisher{},
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
			value, carried := envelope.Result["tells_inactive_reason"]
			if c.reason == "" {
				// Absent, not empty: a member present on every envelope says
				// nothing, and a consumer cannot then use its presence as the flag.
				if carried {
					t.Errorf("the envelope carries tells_inactive_reason=%v with nothing "+
						"to disclose: %s", value, encoded.stdout)
				}
				return
			}
			if !carried {
				t.Fatalf("the envelope has no tells_inactive_reason: %s", encoded.stdout)
			}
			if value != c.reason {
				t.Errorf("tells_inactive_reason = %v, want %q", value, c.reason)
			}
		})
	}
}
