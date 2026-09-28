package workflow_test

// #117, the half that reaches a reader.
//
// `tells` can now say whether its gate is able to reject anything. That fact is
// useless inside the package: the person who needs it is whoever reads a rewrite
// result, sees no `tells-worse` among the rejections, and concludes the
// candidates were clean. Measured, they were not checked at all.
//
// This is the failure mode #91's measurement had and this file exists to rule
// out. `text.Scripts` and `ScriptSet.Introduced` shipped correct, tested, and
// with NO production caller — which is precisely what kept that incident live
// after the measurement existed. An `InactiveReason` nothing calls is the same
// slice again: every assertion in `internal/tells` would pass while `hapax
// rewrite` printed exactly what it printed before.
//
// # Where the disclosure appears
//
// The rule is: a report discloses iff the run reached EXECUTION'S profile load.
//
//	before it   `Rewrite`'s local-only refusal and every refused plan, and
//	            `Execute`'s own local-only refusal
//	after it    stale draft, stale exemplars, no-targets, and every executed run
//
// An earlier draft of this header stated the rule as "once the run has RESOLVED
// a register" and that is not the line. `Plan` resolves the register at the top,
// well before `RefusalUncalibrated`, `RefusalNoReference` and the others that
// follow the scoring bundle's load. `RefusalNoProfile` is the one with a site on
// each side, which is what makes it useless as a fixture here. An uncalibrated
// refusal has a register, a profile and a reference, and already carries one
// resolved fact all the way to the CLI in `CalibrationAvailable`.
// Under the register rule it would have to disclose; under the rule actually
// implemented it does not. The fixture that was supposed to settle this used
// `emptyStore`, whose `RefusalNoProfile` resolves nothing either way and so
// discriminated between nothing. The uncalibrated case is run below, and it is
// the one that separates the two rules.
//
// A resolved register is the reason the disclosure cannot come EARLIER, not the
// criterion for when it comes. The criterion is that the run got far enough to
// gate a candidate, or would have: that is the run whose acceptances a reader
// can misread, and a refused plan produced none — the reason `plan_test.go`
// gives for its own refusals, that such a run "has resolved nothing and so can
// claim nothing", is about no-store / no-reference / two-references and is
// borrowed here only for its shape.
//
// Deliberately NOT narrowed to runs that had targets. A no-targets run consulted
// no gate, and a reader is still entitled to know the tool's tells gate is inert
// before they point it at something that does have targets.
//
// The simplest rule of all — always disclose, answering at `tells.Options{}`
// where no register is known — is refused for the reason given below about
// `hapax tells`: `Options{}` answers a different question, and a report that
// silently switched which question it answered would be worse than one that
// declines to answer.
//
// # Why `Runner.Tells` exists
//
// Measured, 0 of the 22 shipped rules carry a register or an author, so the
// shipped answer is the same at every register. That makes two wrong
// implementations invisible against `Default()`: one that hardwires
// `InactiveNoValidatedRule`, and one that asks the rule set but drops the run's
// register. Both matter on exactly the day the design anticipates — the first
// validated rule being register-scoped — and both would ship green.
//
// So the rule set becomes a seam on `Runner`, beside `Providers` and
// `NewInvocationID`, which exist for the same reason: a policy the binary only
// ever has one of cannot otherwise be varied under test. It is not a flag and
// there is no plan for one.
//
// One seam, not two. Before this slice, `executionGate` built `tells.Default()`
// inline for its own comparison; a run whose disclosure came from one rule set
// while its gate used another would be worse than no disclosure. Asserted below
// by a run where the injected rule actually rejects a candidate.
//
// # Two seams deliberately left alone
//
// The store keeps `tells_comparable` and `tells_comparison` per attempt, and the
// 21 attempts cited above read 1 and 0 — the record that made this bug look like
// a working gate. This slice does not touch it: `rewrite_attempt` is rebuilt by
// a full-table migration for every column it gains, which is its own slice, and
// the disclosure is derivable from the rule set digest already stored beside it.
// #122, so the record is not left looking unconsidered.
//
// `hapax tells` is also untouched. It runs the same inert set and says nothing
// about validity either, but it gates nothing — it reports per-finding
// `provenance`, so a reader can already see that every finding is unvalidated —
// and it asks at `tells.Options{}` rather than at the gate's register, so its
// answer would be a different one. Left out on purpose, not overlooked.
//
// # An absent disclosure means two things, and `Refusal` says which
//
// In `tells`, an empty reason means the gate CAN reject. On a report it means
// either that or "this run never got far enough to ask". They are told apart by
// the member beside it: a report with a pre-execution `Refusal` has not answered
// the question, and every test below that asserts an empty reason asserts the
// refusal in the same breath.

import (
	"os"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/mode"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/workflow"
)

// The expected value is `tells.InactiveNoValidatedRule` and not a literal,
// because a literal here would be a second copy of a vocabulary `workflow` does
// not own — the drift
// `TestTheExecutionVocabulariesAreDerivedFromRewrite` exists to prevent. That
// the SHIPPED set gives that particular reason at this register is
// `internal/tells`'s assertion, not this file's; an earlier draft wrapped it in
// a helper that SKIPPED when the shipped set turned out able to reject, which
// would have reported a check that could not run as a check that passed.

// Every executed run discloses it, whatever the run decided.
//
// All three executed states, because the disclosure describes the RULE SET and
// not the outcome: an implementation that set it only where a candidate was
// accepted would pass a single improved-run test and stay silent on exactly the
// runs a reader is most likely to misread — the ones with no rejections in them
// at all.
func TestEveryExecutedRunDisclosesTheInactiveTellsGate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		replies map[string][]string
		settled bool
		want    workflow.RewriteState
	}{
		{
			name:    "a candidate was accepted",
			replies: map[string][]string{paragraphOne: {improvesOne}, paragraphTwo: {improvesTwo}},
			want:    workflow.RewriteImproved,
		},
		{
			name:    "every candidate was refused",
			replies: map[string][]string{paragraphOne: {matchesOne, matchesOne, matchesOne}},
			want:    workflow.RewriteNoneImproved,
		},
		{
			name:    "there was nothing to change",
			settled: true,
			want:    workflow.RewriteNoTargets,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var root, draft string
			if c.settled {
				root, draft = settledStore(t)
			} else {
				root, draft = targetStore(t)
				requireCandidates(t, root)
			}
			plan := planned(t, planRequest(root, draft))
			runner, _ := executingRunner(&arm{provider: newProvider(t, c.replies)}, nil)

			result := executed(t, runner, executeRequest(plan, localChoice()))

			if result.State != c.want {
				t.Fatalf("State = %q, want %q; the fixture is not exercising the run it "+
					"claims to", result.State, c.want)
			}
			if result.TellsInactiveReason != tells.InactiveNoValidatedRule {
				t.Errorf("TellsInactiveReason = %q, want %q", result.TellsInactiveReason,
					tells.InactiveNoValidatedRule)
			}
		})
	}
}

// A refusal taken before execution loads the profile claims nothing about tells.
//
// The local-only arm refuses at workflow.go:1301 and `LoadProfile` is ten lines
// below it, so this run never resolved the register the answer depends on.
func TestARefusalTakenBeforeTheProfileLoadsDisclosesNothing(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))
	runner, _ := executingRunner(nil, nil)
	request := executeRequest(plan, cloudChoice())
	request.Mode.LocalOnly = true

	result := executed(t, runner, request)

	if result.Refusal != workflow.RefusalLocalOnlyForbidsProvider {
		t.Fatalf("Refusal = %q, want %q", result.Refusal,
			workflow.RefusalLocalOnlyForbidsProvider)
	}
	if result.TellsInactiveReason != "" {
		t.Errorf("TellsInactiveReason = %q on a refusal that resolved no register",
			result.TellsInactiveReason)
	}
}

// The disclosure is the RUNNER'S rule set, answered at the RUN'S register.
//
// One rule, one register, two runs: scoped to the register the fixture rewrites
// at, the gate is active and discloses nothing; scoped to another, the same set
// discloses that nothing applies here. A hardwired reason fails the first, and a
// register-blind implementation fails the second.
func TestTheDisclosureIsTheRunnersRuleSetAtTheRunsRegister(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		register string
		want     string
	}{
		{"the rule covers this register", "essays", ""},
		{"the rule covers another", "letters", tells.InactiveNoRuleInScope},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, draft := targetStore(t)
			plan := planned(t, planRequest(root, draft))
			runner, _ := executingRunner(&arm{provider: newProvider(t,
				map[string][]string{paragraphOne: {improvesOne}})}, nil)
			// A pattern that matches NEITHER the paragraph nor the candidate, so
			// the rule's scope is the only thing under test here. An earlier draft
			// reused the matching rule from the rejection test below, which made
			// the two tests mutually unsatisfiable: the moment the gate reads
			// `Runner.Tells`, that rule refuses the candidate and this row's own
			// "must be an executed run" guard fires.
			runner.Tells = scopedRuleSet(t, c.register, inertPattern)

			result := executed(t, runner, executeRequest(plan, localChoice()))

			// The fixture's profile is the essays one every test here plans
			// against, so the run's register is essays whatever the rule says.
			if result.State != workflow.RewriteImproved {
				t.Fatalf("State = %q, want %q; the fixture is not exercising an "+
					"executed run", result.State, workflow.RewriteImproved)
			}
			if result.TellsInactiveReason != c.want {
				t.Errorf("TellsInactiveReason = %q, want %q", result.TellsInactiveReason,
					c.want)
			}
		})
	}
}

// And the gate the disclosure describes is the gate that runs.
//
// The first test anywhere in this repository in which the tells gate REJECTS
// something. It is what makes the disclosure worth printing: the machinery
// works, and the shipped set is inert because no rule is validated rather than
// because nothing is wired up.
//
// It also closes the two-rule-sets hole. An implementation reading `Runner.Tells`
// for the disclosure while `executionGate` went on building `tells.Default()`
// passes every other test in this file, and would report an active gate while
// running an inert one.
func TestAValidatedRuleInTheRunnersSetRejectsTheCandidateItMatches(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))
	runner, _ := executingRunner(&arm{provider: newProvider(t,
		map[string][]string{paragraphOne: {improvesOne}})}, nil)
	runner.Tells = scopedRuleSet(t, "essays", matchingPattern)

	result := executed(t, runner, executeRequest(plan, localChoice()))

	// `improvesOne` is accepted by every other test in this package, so the only
	// thing that can refuse it here is the rule.
	if result.Improved != 0 || result.State != workflow.RewriteNoneImproved {
		t.Fatalf("Improved = %d and State = %q; the candidate every other test "+
			"accepts was not refused by the rule that matches it",
			result.Improved, result.State)
	}
	var refused bool
	for _, outcome := range result.Outcomes {
		for _, reason := range outcome.Rejections {
			if reason == string(rewrite.RejectionTellsWorse) {
				refused = true
			}
		}
	}
	if !refused {
		t.Errorf("no attempt was refused as %q: %+v", rewrite.RejectionTellsWorse,
			result.Outcomes)
	}
	// And a set that can reject discloses nothing, end to end.
	if result.TellsInactiveReason != "" {
		t.Errorf("TellsInactiveReason = %q on a run whose gate just rejected something",
			result.TellsInactiveReason)
	}
}

// The two patterns the seam tests use, and the difference between them is what
// keeps those tests from contradicting each other.
//
// `matchingPattern` is a phrase `improvesOne` carries and `paragraphOne` does
// not, so a candidate trips it where the text it replaces did not — which is
// what the comparison counts. Measured through `Check` and `Compare` at Register
// "essays": the paragraph counts 0 findings, the candidate 1, and the comparison
// is +1, which is `tells-worse`. At "letters" both count 0, so the rule is out
// of scope rather than merely unmatched.
//
// `inertPattern` matches neither, so a run carrying it reaches the same outcome
// it would with no rule at all. `InactiveReason` never reads any text, so the
// scope tests want exactly that: a rule whose only effect is on the disclosure.
const (
	matchingPattern = "and it says"
	inertPattern    = "delve into"
)

// scopedRuleSet is one validated rule, scoped to one register, with the pattern
// the caller needs.
func scopedRuleSet(t *testing.T, register, pattern string) *tells.RuleSet {
	t.Helper()
	switch pattern {
	case matchingPattern:
		if !strings.Contains(improvesOne, pattern) || strings.Contains(paragraphOne, pattern) {
			t.Fatalf("the fixture phrase %q no longer separates the candidate from the "+
				"paragraph it replaces", pattern)
		}
	case inertPattern:
		if strings.Contains(improvesOne, pattern) || strings.Contains(paragraphOne, pattern) {
			t.Fatalf("the inert phrase %q now matches the fixture, so a scope test "+
				"would change the run's outcome", pattern)
		}
	default:
		t.Fatalf("pattern %q is neither of the two this file measured", pattern)
	}
	rs, err := tells.Load([]byte(`version = "probe-v1"
[[rule]]
id = "probe"
description = "A probe rule, validated for this test and nowhere else."
matcher = "regex"
unit = "document"
pattern = "(?i)` + pattern + `"
severity = "warn"
provenance = "derived"
category = "author-deviation"
registers = ["` + register + `"]
[rule.evidence]
reference = "probe-reference"
population = "probe-population"
digest = "probe-digest"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rs
}

// A refusal taken AFTER the profile loads discloses, like the runs beside it.
//
// The header names four things on that side of the line and the table above
// covers two. These are the other two, and they are not decoration: an
// implementation that sets the member at each of the three return sites a test
// happens to reach — rather than once, where the profile loads — passes
// everything above while `refusal=stale-draft` and `refusal=stale-exemplars`
// stay silent. Both are refusals a reader meets in practice.
func TestARefusalTakenAfterTheProfileLoadsStillDiscloses(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		disturb func(t *testing.T, root, draft string)
		refusal string
	}{
		{
			name: "the draft moved under the plan",
			disturb: func(t *testing.T, root, draft string) {
				if err := os.WriteFile(draft, []byte("An entirely different draft, long "+
					"enough to be admitted and measured on its own terms rather than "+
					"skipped by the floor.\n\n"), 0o644); err != nil {
					t.Fatalf("rewrite draft: %v", err)
				}
			},
			refusal: workflow.RefusalStaleDraft,
		},
		{
			name:    "the exemplars are gone",
			disturb: func(t *testing.T, root, draft string) { removeCorpusDocuments(t, root, draft) },
			refusal: workflow.RefusalStaleExemplars,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, draft := targetStore(t)
			plan := planned(t, planRequest(root, draft))
			c.disturb(t, root, draft)
			runner, _ := executingRunner(&arm{provider: newProvider(t,
				map[string][]string{paragraphOne: {improvesOne}})}, nil)

			result := executed(t, runner, executeRequest(plan, localChoice()))

			if result.Refusal != c.refusal {
				t.Fatalf("Refusal = %q, want %q; the fixture is not exercising the "+
					"refusal it claims to", result.Refusal, c.refusal)
			}
			if result.TellsInactiveReason != tells.InactiveNoValidatedRule {
				t.Errorf("TellsInactiveReason = %q, want %q", result.TellsInactiveReason,
					tells.InactiveNoValidatedRule)
			}
		})
	}
}

// And the report the composition root actually receives carries it.
//
// `Runner.Rewrite` builds three `RewriteReport`s and `Execute`'s result feeds
// only one of them. Nothing in this package drove `Rewrite` before this file, so
// a field added to `ExecuteResult` and correct in every test above could still
// be dropped on the way out and no test would notice — which is the whole
// producer gap this slice is about, one layer up.
func TestTheRewriteReportCarriesTheDisclosure(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	runner, _ := executingRunner(&arm{provider: newProvider(t, map[string][]string{
		paragraphOne: {improvesOne}, paragraphTwo: {improvesTwo},
	})}, nil)

	outcome, err := runner.Rewrite(ctx(), workflow.RewriteInput{
		StorePath: defaultStorePath(root), CorpusRoot: root, Register: "essays",
		Path: draft, Choice: localChoice(),
	})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}

	report := outcome.Report()
	if report.State != workflow.RewriteImproved {
		t.Fatalf("State = %q, want %q; the fixture is not exercising an executed run",
			report.State, workflow.RewriteImproved)
	}
	if report.TellsInactiveReason != tells.InactiveNoValidatedRule {
		t.Errorf("TellsInactiveReason = %q, want %q", report.TellsInactiveReason,
			tells.InactiveNoValidatedRule)
	}
}

// The reports `Rewrite` builds without executing claim nothing.
//
// Both of its other literals: the local-only refusal it takes before planning,
// and a refused plan. Neither reached `Execute`, which is the criterion — NOT
// that neither resolved anything, because the uncalibrated row below has a
// register, a profile and a reference in hand and still must not answer.
func TestTheReportsBuiltWithoutExecutingDiscloseNothing(t *testing.T) {
	t.Parallel()
	t.Run("local-only refuses the provider before planning", func(t *testing.T) {
		t.Parallel()
		runner, _ := executingRunner(nil, nil)

		outcome, err := runner.Rewrite(ctx(), workflow.RewriteInput{
			Register: "essays", Choice: cloudChoice(), Mode: mode.Mode{LocalOnly: true},
		})
		if err != nil {
			t.Fatalf("Rewrite: %v", err)
		}

		report := outcome.Report()
		if report.Refusal != workflow.RefusalLocalOnlyForbidsProvider {
			t.Fatalf("Refusal = %q, want %q", report.Refusal,
				workflow.RefusalLocalOnlyForbidsProvider)
		}
		if report.TellsInactiveReason != "" {
			t.Errorf("TellsInactiveReason = %q on a run that never planned anything",
				report.TellsInactiveReason)
		}
	})
	// Both plan refusals, because they differ in exactly the way that matters:
	// `RefusalNoProfile` resolved nothing, so it is consistent with any rule
	// anyone might state, while `RefusalUncalibrated` has a register, a profile
	// and a reference in hand and still must not answer. Only the second one
	// discriminates, and the first is kept because it is the shape the borrowed
	// justification actually covers.
	for _, c := range []struct {
		name    string
		root    func(*testing.T) string
		refusal string
	}{
		{"the plan finds no profile", emptyStore, workflow.RefusalNoProfile},
		{"the plan is uncalibrated", uncalibratedStore, workflow.RefusalUncalibrated},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := c.root(t)
			draft := writeDraft(t, root, twoParagraphs)
			runner, _ := executingRunner(&arm{provider: newProvider(t, nil)}, nil)

			outcome, err := runner.Rewrite(ctx(), workflow.RewriteInput{
				StorePath: defaultStorePath(root), CorpusRoot: root, Register: "essays",
				Path: draft, Choice: localChoice(),
			})
			if err != nil {
				t.Fatalf("Rewrite: %v", err)
			}

			report := outcome.Report()
			if report.Refusal != c.refusal {
				t.Fatalf("Refusal = %q, want %q; the fixture is not exercising the "+
					"refusal it claims to", report.Refusal, c.refusal)
			}
			if report.TellsInactiveReason != "" {
				t.Errorf("TellsInactiveReason = %q on a refused plan", report.TellsInactiveReason)
			}
		})
	}
}
