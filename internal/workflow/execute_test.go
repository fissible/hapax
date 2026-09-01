package workflow_test

// What Execute is, and what these tests hold it to.
//
// Execute consumes a plan and returns assembled bytes plus per-target outcomes.
// It writes audit records and nothing else — no destination exists in this
// slice, which is why the slice exists: everything here is testable without one,
// so the code that can overwrite a user's file is small and reviewed alone.
//
// The contract in one line: bytes are publishable only when there is no error,
// no refusal, and the bytes are non-nil.
//
// # What an incorrect implementation would also satisfy, and how that is closed
//
// "The output has the rewrite in it" is satisfied by an implementation that
// splices at the wrong offsets, provided the expectation is built from the same
// wrong offsets. So the expectation is built from the DRAFT'S OWN BYTES by
// searching for the paragraph text — see spliced — and never from the plan.
//
// "The exemplars were rehydrated once" is satisfied by an implementation that
// rehydrates per segment, provided nothing counts the reads. So instead the
// corpus is DELETED during the run: an implementation that reads again cannot
// survive it, which is a stronger statement than a call count.
//
// "The draft was checked for freshness" is satisfied by an implementation that
// stats the file. So the file is rewritten with the SAME bytes, which must not
// refuse, and with different bytes that leave the length unchanged, which must.

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// The vocabularies, which are projections rather than restatements
// ---------------------------------------------------------------------------

// `workflow` projects closed vocabularies into results as plain strings — it
// does so already for bands, refusals and eval reasons — so `cli` never has to
// import the package that owns them. The risk that carries is drift: two lists
// that are written out twice can disagree. These are therefore DERIVED from
// rewrite's own accessors, and the test compares against rewrite rather than
// against a literal, because a literal here would be the second copy.
func TestTheExecutionVocabulariesAreDerivedFromRewrite(t *testing.T) {
	t.Parallel()
	t.Run("terminals", func(t *testing.T) {
		var want []string
		for _, terminal := range rewrite.Terminals() {
			want = append(want, string(terminal))
		}
		if got := workflow.Terminals(); !reflect.DeepEqual(got, want) {
			t.Errorf("Terminals() = %v, want the projection of rewrite's %v", got, want)
		}
	})
	t.Run("rejection codes", func(t *testing.T) {
		var want []string
		for _, code := range rewrite.RejectionCodes() {
			want = append(want, string(code))
		}
		if got := workflow.RejectionCodes(); !reflect.DeepEqual(got, want) {
			t.Errorf("RejectionCodes() = %v, want the projection of rewrite's %v", got, want)
		}
	})
	t.Run("both accessors hand out copies", func(t *testing.T) {
		for name, accessor := range map[string]func() []string{
			"Terminals": workflow.Terminals, "RejectionCodes": workflow.RejectionCodes,
		} {
			first := accessor()
			if len(first) == 0 {
				t.Fatalf("%s() is empty", name)
			}
			first[0] = "tampered"
			for _, value := range accessor() {
				if value == "tampered" {
					t.Errorf("%s() returns its own backing array", name)
				}
			}
		}
	})
}

func TestTheRewriteStateVocabularyIsExactlyThis(t *testing.T) {
	t.Parallel()
	want := []workflow.RewriteState{
		workflow.RewriteNoTargets,
		workflow.RewriteImproved,
		workflow.RewriteNoneImproved,
	}
	got := workflow.RewriteStates()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RewriteStates() = %v, want %v", got, want)
	}
	for _, state := range got {
		if state == "" {
			t.Error("the empty string is a declared state, so a zero result would look like a completed one")
		}
	}
}

// ---------------------------------------------------------------------------
// The shape, because privacy is a property of the fields that exist
// ---------------------------------------------------------------------------

// A per-target outcome carries ordered rejection codes, whether it changed, and
// why the loop stopped. It carries NO prose and no candidate text, and a shape
// test is the right guard for that because a field cannot leak what it does not
// have. The same reasoning froze RewritePlan's surface in B1.
//
// The absent member is as deliberate as the present ones: there is no attempt
// count. The loop records no attempt for an empty response, so a count and a
// list are two members that can disagree — the habit that cost ten review rounds
// in #70 — and Terminal already says when a call was spent without producing
// one. Freezing the field list is what holds that; a rule against integers
// generally would forbid a future member that is not this mistake.
func TestTheTargetOutcomeSurfaceIsExactlyThis(t *testing.T) {
	t.Parallel()
	assertShape(t, reflect.TypeOf(workflow.TargetOutcome{}), [][2]string{
		{"Index", "int"},
		{"NodeID", "string"},
		{"Changed", "bool"},
		{"Terminal", "string"},
		{"Rejections", "[]string"},
	})
}

// Bytes is the one member that carries prose, and it is the document the caller
// asked for rather than anything a provider said about a paragraph.
func TestTheExecuteResultSurfaceIsExactlyThis(t *testing.T) {
	t.Parallel()
	assertShape(t, reflect.TypeOf(workflow.ExecuteResult{}), [][2]string{
		{"Bytes", "[]uint8"},
		{"InvocationID", "string"},
		{"State", "workflow.RewriteState"},
		{"Targets", "int"},
		{"Improved", "int"},
		{"Refusal", "string"},
		{"Outcomes", "[]workflow.TargetOutcome"},
	})
}

// ---------------------------------------------------------------------------
// The accepted path
// ---------------------------------------------------------------------------

// The output is the draft with exactly the target spans replaced and every other
// byte untouched. The expectation is built by finding the paragraph text in the
// file and substituting for it, so an implementation splicing at the wrong
// offsets cannot agree with it.
func TestAnAcceptedRewriteReplacesExactlyTheTargets(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))
	// An attempt from an earlier run over the same paragraph. Its whole value is
	// being trustworthy, so a later run must leave it exactly as it was.
	seeded := seedAttempt(t, root, plan)
	// The store starts believing the exemplars cannot be read, with their bytes
	// back in place. Only a run that goes through store.Rehydrate clears that,
	// so "not marked unreadable" afterwards is a fact about this run rather than
	// about a store nobody had touched.
	markExemplarsUnavailable(t, root, plan)
	beforeFiles := tree(t, root)
	beforeStore := storeState(t, root, plan.DraftSnapshotID, profileSnapshotID(t, root))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {improvesOne},
		paragraphTwo: {improvesTwo},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	want := spliced(t, draft, substitution{paragraphOne, improvesOne}, substitution{paragraphTwo, improvesTwo})
	if string(result.Bytes) != string(want) {
		t.Errorf("assembled bytes are\n%q\nwant\n%q", result.Bytes, want)
	}
	if result.Refusal != "" {
		t.Errorf("Refusal = %q on a completed rewrite", result.Refusal)
	}

	// And nothing outside the database moved, and inside it only the audit
	// tables did. "The draft is unchanged" is the weaker claim on both counts:
	// this slice has no filesystem authority at all, and inside the store it may
	// write attempts and nothing else — not a head, not a selection.
	requireOnlyTheStoreChanged(t, root, beforeFiles)
	requireOnlyAuditRecordsWereWritten(t, root, beforeStore,
		exemplarPaths(t, root, plan.ExemplarSelectionID), wasRead)
	requireSeededAttemptSurvives(t, root, seeded)
}

// Which occurrence a rewrite lands on is invisible in a draft whose paragraphs
// are all distinct, and it is the byte-ownership question this slice has to get
// right. Here the same paragraph appears twice, only the first is rewritten, and
// the second must come through untouched.
func TestARepeatedParagraphIsReplacedWhereItWasTargeted(t *testing.T) {
	t.Parallel()
	root := installRelease(t, 0.05, 5.0)
	requireCandidates(t, root)
	draft := writeDraftBytes(t, root, repeatedDraft())
	plan := planned(t, planRequest(root, draft))
	targets := targetsOf(plan)
	if len(targets) != 2 {
		t.Fatalf("the repeated draft planned %d targets and this test needs two", len(targets))
	}
	if targets[0].Offset >= targets[1].Offset {
		t.Fatal("the targets are not in source order, so first and second are not distinguishable")
	}

	// One candidate for the first occurrence, nothing for the second.
	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	want := spliced(t, draft, substitution{paragraphOne, improvesOne})
	if string(result.Bytes) != string(want) {
		t.Errorf("assembled bytes are\n%q\nwant the FIRST occurrence replaced\n%q", result.Bytes, want)
	}
	if !outcomeAt(t, result, 0).Changed || outcomeAt(t, result, 1).Changed {
		t.Errorf("outcomes report changed=%v and %v; the first occurrence was the one rewritten",
			outcomeAt(t, result, 0).Changed, outcomeAt(t, result, 1).Changed)
	}
}

// Execute runs the plan it was given, against the artifacts the plan named. A
// head that moved between planning and execution must not silently change what
// runs: the plan chose a release, and the audit record has to name what was
// actually used.
func TestTheReleaseIsThePlansAndNotWhateverIsCurrent(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))
	plannedBand := targetsOf(plan)[0].Band.Band

	// A new head whose boundaries put the same paragraphs in a different band —
	// and that is MEASURED rather than asserted in a comment. Score resolves the
	// head, so what it reports afterwards is what a head-resolving Execute would
	// record, and if the two bands agreed this test would prove nothing.
	moved := moveTheHead(t, root, 2.0, 8.0)
	if moved.ID == plan.ReleaseID {
		t.Fatal("the head did not change, so this test asserts nothing")
	}
	current := scored(t, workflow.ScoreRequest{
		StorePath: defaultStorePath(root), Register: "essays", Path: draft,
	})
	if len(current.Segments) == 0 {
		t.Fatal("the draft measures nothing against the new head")
	}
	if current.Segments[0].Band.Band == plannedBand {
		t.Fatalf("the new head still reports band %q, so a head-resolving implementation "+
			"records the same thing the plan did and this test cannot tell them apart", plannedBand)
	}

	provider := newProvider(t, map[string][]string{paragraphOne: {matchesOne}})
	runner, invocation := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if len(result.Outcomes) != len(targetsOf(plan)) {
		t.Fatalf("%d outcomes after the head moved, want %d; the plan's targets are still the targets",
			len(result.Outcomes), len(targetsOf(plan)))
	}
	stored := storedAttempt(t, root, invocation, outcomeAt(t, result, 0).NodeID, 0)
	if string(stored.CurrentBand) != plannedBand {
		t.Errorf("the attempt records band %q; the plan measured %q against the release it named, "+
			"so execution resolved the head instead of the plan", stored.CurrentBand, plannedBand)
	}
}

// The attempt cap comes from the request. An implementation that hard-coded the
// default would pass every other test in this file, because every other test
// uses it.
func TestTheAttemptCapComesFromTheRequest(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		cap  int
		want int
	}{
		{"one attempt", 1, 1},
		{"two attempts", 2, 2},
		{"zero means the default", 0, rewrite.DefaultOptions().Attempts},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, draft := targetStore(t)
			requireCandidates(t, root)
			plan := planned(t, planRequest(root, draft))

			// More candidates than any cap under test, all refused, so the loop
			// stops on the cap rather than on an empty response.
			refusals := make([]string, rewrite.DefaultOptions().Attempts+2)
			for i := range refusals {
				refusals[i] = matchesOne
			}
			provider := newProvider(t, map[string][]string{paragraphOne: refusals})
			runner, _ := executingRunner(&arm{provider: provider}, nil)
			request := executeRequest(plan, localChoice())
			request.Attempts = c.cap
			result := executed(t, runner, request)

			first := outcomeAt(t, result, 0)
			if len(first.Rejections) != c.want {
				t.Errorf("%d rejections against a requested cap of %d, want %d",
					len(first.Rejections), c.cap, c.want)
			}
			if first.Terminal != string(rewrite.TerminalExhausted) {
				t.Errorf("Terminal = %q, want %q", first.Terminal, rewrite.TerminalExhausted)
			}
		})
	}
}

// And a cap that is not a cap is an invalid request rather than a silent
// substitution of the default. Failing closed matters here: a negative cap that
// quietly became three would spend money nobody asked for.
func TestANegativeAttemptCapIsAnError(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))

	local := &arm{provider: newProvider(t, map[string][]string{paragraphOne: {improvesOne}})}
	runner, _ := executingRunner(local, nil)
	request := executeRequest(plan, localChoice())
	request.Attempts = -1
	if _, err := runner.Execute(ctx(), request); err == nil {
		t.Fatal("a negative attempt cap was accepted")
	}
	if calls := local.provider.(*scriptedProvider).calls; len(calls) != 0 {
		t.Errorf("%d provider calls were made on an invalid request", len(calls))
	}
}

// Each target is offered once, in plan order, and what is offered is the
// paragraph's own bytes. The passage is recovered from the prompt rather than
// read off a field, because the prompt is what a provider is actually sent.
func TestTheProviderIsAskedForEachTargetInPlanOrder(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))
	targets := targetsOf(plan)
	if len(targets) != 2 {
		t.Fatalf("the fixture planned %d targets and this test needs two", len(targets))
	}

	provider := newProvider(t, map[string][]string{
		paragraphOne: {improvesOne},
		paragraphTwo: {improvesTwo},
	})
	local := &arm{provider: provider}
	runner, invocation := executingRunner(local, nil)
	executed(t, runner, executeRequest(plan, localChoice()))

	// The choice reaches the arm verbatim, including the model. An arm that was
	// handed a choice it had rewritten would send a different model than the one
	// the audit record then claims.
	if len(local.choices) != 1 {
		t.Fatalf("the local arm was built %d times, want once for the whole run", len(local.choices))
	}
	if local.choices[0] != localChoice() {
		t.Errorf("the arm was given %+v, want %+v", local.choices[0], localChoice())
	}

	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	for _, call := range provider.calls {
		if call.Request.ProfileID != plan.ProfileID {
			t.Errorf("a request carried profile %q, want the plan's %q", call.Request.ProfileID, plan.ProfileID)
		}
		if call.Request.InvocationID != invocation {
			t.Errorf("a request carried invocation %q, want %q", call.Request.InvocationID, invocation)
		}
	}

	// A target's own bytes are what OPENS its run of calls. What comes after
	// them inside that run is the loop's business and not this test's: the loop
	// is a hill climber, so an accepted candidate becomes the next attempt's
	// passage. ADR 0006 says so and internal/rewrite pins it.
	//
	// So the calls must partition into one contiguous block per target, in plan
	// order, each opened by that target's own bytes. Contiguity is the load
	// bearing half: without it the calls
	//
	//	target 0, target 1, improved 0, improved 1
	//
	// would satisfy "each target's first call is its own bytes, in order" while
	// interleaving two loops that must not overlap.
	opens := make([]int, len(targets))
	body := make([]string, len(targets))
	for i, target := range targets {
		body[i] = string(raw[target.Offset : target.Offset+target.Length])
		opens[i] = -1
		for at, call := range provider.calls {
			if call.Passage == body[i] {
				opens[i] = at
				break
			}
		}
		if opens[i] < 0 {
			t.Fatalf("target %d was never offered its own bytes %q", i, body[i])
		}
		if i == 0 && opens[i] != 0 {
			t.Errorf("the first call carried %q, want target 0's own bytes", provider.calls[0].Passage)
		}
		if i > 0 && opens[i] <= opens[i-1] {
			t.Errorf("target %d opens at call %d and target %d at call %d; targets are not visited in plan order",
				i, opens[i], i-1, opens[i-1])
		}
	}
	for i := range targets {
		end := len(provider.calls)
		if i+1 < len(targets) {
			end = opens[i+1]
		}
		for at := opens[i]; at < end; at++ {
			for j, other := range body {
				if j != i && provider.calls[at].Passage == other {
					t.Errorf("call %d, inside target %d's run, carries target %d's bytes; the two loops interleave",
						at, i, j)
				}
			}
		}
	}
}

// Every target gets an outcome, nothing else does, and each outcome names the
// segment it belongs to. A result that reported outcomes positionally would let
// a caller attribute a rejection to the wrong paragraph.
func TestEveryTargetGetsAnOutcomeAndNothingElseDoes(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{paragraphOne: {improvesOne}})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	targets := targetsOf(plan)
	if len(result.Outcomes) != len(targets) {
		t.Fatalf("%d outcomes for %d targets", len(result.Outcomes), len(targets))
	}
	if result.Targets != len(targets) {
		t.Errorf("Targets = %d, want %d", result.Targets, len(targets))
	}
	for i, outcome := range result.Outcomes {
		if outcome.Index != targets[i].Index || outcome.NodeID != targets[i].NodeID {
			t.Errorf("outcome %d names segment %d / %s, want %d / %s",
				i, outcome.Index, outcome.NodeID, targets[i].Index, targets[i].NodeID)
		}
	}
	// A non-target segment must not appear at all. The plan reports what it
	// measured; only targets were executed.
	planned := map[string]bool{}
	for _, target := range targets {
		planned[target.NodeID] = true
	}
	for _, outcome := range result.Outcomes {
		if !planned[outcome.NodeID] {
			t.Errorf("an outcome names %s, which the plan did not target", outcome.NodeID)
		}
	}
}

// The state is not decoration: rewrite_state alone cannot separate the two runs
// that both exit zero, which is why it comes with counts, and why the counts and
// the state are asserted against each other rather than each on its own.
func TestTheStateAndTheCountsAgree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		replies  map[string][]string
		settled  bool
		want     workflow.RewriteState
		improved int
	}{
		{
			name:    "both targets improved",
			replies: map[string][]string{paragraphOne: {improvesOne}, paragraphTwo: {improvesTwo}},
			want:    workflow.RewriteImproved, improved: 2,
		},
		{
			name:    "one target improved",
			replies: map[string][]string{paragraphOne: {improvesOne}},
			want:    workflow.RewriteImproved, improved: 1,
		},
		{
			// Every candidate measured exactly what it replaced, so every one
			// was refused as not-improved. That is a completed decision, not a
			// failure.
			name:    "nothing improved",
			replies: map[string][]string{paragraphOne: {matchesOne, matchesOne, matchesOne}},
			want:    workflow.RewriteNoneImproved, improved: 0,
		},
		{
			name:    "nothing to change",
			settled: true,
			want:    workflow.RewriteNoTargets, improved: 0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
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
				t.Errorf("State = %q, want %q", result.State, c.want)
			}
			if result.Improved != c.improved {
				t.Errorf("Improved = %d, want %d", result.Improved, c.improved)
			}
			// The equations, asserted rather than assumed, so a state that
			// disagrees with its own counts is caught wherever it appears.
			switch result.State {
			case workflow.RewriteNoTargets:
				if result.Targets != 0 {
					t.Errorf("no-targets with Targets = %d", result.Targets)
				}
			case workflow.RewriteImproved:
				if result.Improved <= 0 {
					t.Errorf("improved with Improved = %d", result.Improved)
				}
			case workflow.RewriteNoneImproved:
				if result.Targets == 0 || result.Improved != 0 {
					t.Errorf("none-improved with Targets = %d and Improved = %d", result.Targets, result.Improved)
				}
			default:
				t.Errorf("State %q is not in the declared vocabulary %v", result.State, workflow.RewriteStates())
			}
			changed := 0
			for _, outcome := range result.Outcomes {
				if outcome.Changed {
					changed++
				}
			}
			if changed != result.Improved {
				t.Errorf("%d outcomes report a change and Improved = %d", changed, result.Improved)
			}
		})
	}
}

// A refused candidate leaves the paragraph exactly as it was, and the result
// says which guard refused it and that the budget was spent. Bytes are still
// returned: nothing improved is a completed decision.
func TestARefusedRewriteReturnsTheDraftAndNamesTheRefusal(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {matchesOne, matchesOne, matchesOne},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if string(result.Bytes) != executableDraft() {
		t.Errorf("bytes are\n%q\nwant the draft unchanged\n%q", result.Bytes, executableDraft())
	}
	first := outcomeAt(t, result, 0)
	if first.Changed {
		t.Error("the first target reports a change after three refused candidates")
	}
	if first.Terminal != string(rewrite.TerminalExhausted) {
		t.Errorf("Terminal = %q, want %q", first.Terminal, rewrite.TerminalExhausted)
	}
	if len(first.Rejections) != 3 {
		t.Fatalf("%d rejections recorded against a cap of three: %v", len(first.Rejections), first.Rejections)
	}
	for i, rejection := range first.Rejections {
		if rejection != string(rewrite.RejectionNotImproved) {
			t.Errorf("rejection %d is %q, want %q; the fixture chose a candidate that clears both guards",
				i, rejection, rewrite.RejectionNotImproved)
		}
	}
}

// An empty response is named rather than left to look like exhaustion. It is the
// ending with the least evidence anywhere else: the loop records no attempt for
// it, so a provider call was spent that leaves no trace in the store.
func TestAnEmptyResponseIsNamedRatherThanSilent(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, nil)
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if string(result.Bytes) != executableDraft() {
		t.Error("the draft was not returned unchanged")
	}
	if result.State != workflow.RewriteNoneImproved {
		t.Errorf("State = %q, want %q", result.State, workflow.RewriteNoneImproved)
	}
	for i, outcome := range result.Outcomes {
		if outcome.Terminal != string(rewrite.TerminalEmptyResponse) {
			t.Errorf("outcome %d Terminal = %q, want %q", i, outcome.Terminal, rewrite.TerminalEmptyResponse)
		}
		if len(outcome.Rejections) != 0 {
			t.Errorf("outcome %d carries rejections %v for a loop that recorded no attempt", i, outcome.Rejections)
		}
	}
	if got := countAttempts(t, root); got != 0 {
		t.Errorf("%d attempts reached the store; an empty candidate is not an attempt", got)
	}
	if len(provider.calls) == 0 {
		t.Error("the provider was never called, so nothing was spent and this test proves nothing")
	}
}

// ---------------------------------------------------------------------------
// The audit record
// ---------------------------------------------------------------------------

// Every rejection the result reports must be readable from the store under the
// invocation, node and index it claims. Counting attempts would not do it: the
// question is whether the record a caller is pointed at is actually there and
// says the same thing.
func TestEveryReportedAttemptIsInTheStoreWhereTheResultSaysItIs(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {matchesOne, improvesOne},
		paragraphTwo: {improvesTwo},
	})
	runner, invocation := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	if result.InvocationID != invocation {
		t.Errorf("InvocationID = %q, want %q", result.InvocationID, invocation)
	}
	total := 0
	for _, outcome := range result.Outcomes {
		for index, rejection := range outcome.Rejections {
			total++
			stored := storedAttempt(t, root, invocation, outcome.NodeID, index)
			if string(stored.Rejection) != rejection {
				t.Errorf("attempt %d of %s is %q in the store and %q in the result",
					index, outcome.NodeID, stored.Rejection, rejection)
			}
			if stored.Accepted != (rejection == "") {
				t.Errorf("attempt %d of %s reports accepted=%v beside rejection %q",
					index, outcome.NodeID, stored.Accepted, rejection)
			}
			if stored.ProfileID != plan.ProfileID {
				t.Errorf("attempt %d of %s carries profile %q, want %q",
					index, outcome.NodeID, stored.ProfileID, plan.ProfileID)
			}
			if string(stored.ProviderID) != localChoice().Provider {
				t.Errorf("attempt %d of %s carries provider %q, want %q",
					index, outcome.NodeID, stored.ProviderID, localChoice().Provider)
			}
			if !declaredBand(string(stored.CurrentBand)) || !declaredBand(string(stored.CandidateBand)) {
				t.Errorf("attempt %d of %s carries bands %q and %q, which are not both declared",
					index, outcome.NodeID, stored.CurrentBand, stored.CandidateBand)
			}
		}
	}
	if total == 0 {
		t.Fatal("no attempts were reported, so nothing was checked against the store")
	}
	if got := countAttempts(t, root); got != total {
		t.Errorf("the store holds %d attempts and the result reports %d; a record the result does not "+
			"mention is one nothing points at", got, total)
	}
}

// The privacy invariant, asserted as an effect rather than as a shape. No prose
// from the draft, and no prose from any candidate, appears anywhere in the
// result except in the assembled document itself.
func TestNoProseReachesTheOutcomes(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	plan := planned(t, planRequest(root, draft))

	provider := newProvider(t, map[string][]string{
		paragraphOne: {matchesOne, improvesOne},
		paragraphTwo: {improvesTwo},
	})
	runner, _ := executingRunner(&arm{provider: provider}, nil)
	result := executed(t, runner, executeRequest(plan, localChoice()))

	prose := []string{paragraphOne, paragraphTwo, improvesOne, improvesTwo, matchesOne}
	for _, outcome := range result.Outcomes {
		rendered := outcome.NodeID + "\x00" + outcome.Terminal + "\x00" + strings.Join(outcome.Rejections, "\x00")
		for _, text := range prose {
			// A prefix, because a whole paragraph is easy to keep out by
			// accident and a fragment is what actually leaks.
			fragment := text
			if len(fragment) > 24 {
				fragment = fragment[:24]
			}
			if strings.Contains(rendered, fragment) {
				t.Errorf("outcome for %s carries the prose fragment %q", outcome.NodeID, fragment)
			}
		}
	}
	if result.InvocationID == "" {
		t.Error("the result carries no invocation, so an attempt cannot be found from it")
	}
}
