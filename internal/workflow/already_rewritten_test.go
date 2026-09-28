package workflow_test

// #111, the plan's half. A paragraph this tool already wrote is not a target.
//
// Every guard anchors on the paragraph the invocation started from, and
// `--in-place` — documented as "The only overwrite authority" — makes that the
// previous run's output. Measured against the shipped script guard at ceiling
// 0.05 and established 0.25:
//
//	original     letters= 22 Han= 1 share=0.0455
//	after run 1  letters=132 Han= 6 share=0.0455   admissible
//	after run 2  letters= 25 Han= 6 share=0.2400   admissible against run 1's output
//	                                               REFUSED against the true original
//
// Not every guard drifts. The DISTANCE is safe: `rewrite.go:355` refuses unless
// `candidate <= current - Epsilon` against the text as it stands, so acceptance
// implies strict improvement and a later run can only improve it. What drifts is
// preserve (non-transitive, per #116's triple), the script guards (share-based,
// so a new baseline resets the denominator) and tells (moot while inert, #117).
//
// # Plan time, not gate time
//
// The verdict is a function of the paragraph's CURRENT TEXT alone. It does not
// depend on any candidate, so as a per-attempt rejection it would re-ask one
// question once per attempt and spend a provider call to do it. The other four
// gates are per-candidate because their verdicts genuinely are.
//
// And `rewrite_attempt.rejection` is a closed vocabulary pinned by a SQL CHECK,
// so a new rejection code is a schema migration — for a verdict that is a pure
// function of text which cannot change within a run. That is the decisive
// argument; the provider call is the cheap one.
//
// So it is a disposition, beside `in-range` and `contains-excisions`: the
// paragraph is not a target and nothing is spent on it.
//
// # Highest precedence among the reasons a CONSIDERED paragraph is skipped
//
// Deliberately above `in-range`, and that ordering is the point rather than a
// detail. A paragraph this tool rewrote successfully is LIKELY to be in range —
// it was moved there. Reporting `in-range` for it tells the author "this already
// reads as you" about the tool's own prose, which is the misleading report #111
// is made of. `already-rewritten` is also the only disposition that reports a
// provenance fact rather than a measurement, and the cheapest to compute.
//
// Pinned: above `contains-excisions` in both switches, and above `in-range` in
// the automatic one — the explicit switch has no `in-range` case at all, because
// selection and excisions decide there instead.
//
// NOT pinned, and therefore not claimed: above `unmeasurable`. A published
// paragraph is measurable BY CONSTRUCTION — `judged` refuses a candidate as
// `candidate-unscoreable` when its distance is undefined, so an accepted
// candidate had a defined distance, and the same bytes under the same feature
// manifest measure the same way. "Published and unmeasurable" is reachable only
// if the profile changed underneath the draft. No fixture in this package has
// ever produced `DispositionUnmeasurable` either — it appears in the vocabulary
// list and nowhere else. The implementation is free there.
//
// `not-selected` is the exception and stays first. Under explicit targeting the
// caller's selection decides what is under consideration at all, so a paragraph
// nobody named is `not-selected` whatever else is true of it — which is what #81
// established for bands and excisions. The alternative inflates the count with
// paragraphs the caller never asked about: `--paragraphs 0` on a re-run would
// report `already-rewritten=7` about six paragraphs outside the request. The
// count therefore counts paragraphs that were candidates for rewriting, and
// under an explicit selection that is the named ones.
//
// Which means it is NOT a "how much of this file is my own prose" figure, and a
// reader should not have to discover that: `--paragraphs 0` reports 1 for a
// draft whose other six paragraphs are all this tool's output. It is on the same
// footing as `targets`.
//
// # It refuses under explicit targeting too
//
// Explicit targeting already overrides one guard — `AllowUncalibrated` — so the
// question is fair. It does not override this one. Calibration is about
// confidence in a MEASUREMENT, and a caller may reasonably accept a weaker one.
// This is about the anchor being the wrong text, and no request makes a moved
// anchor sound.
//
// # Nothing-to-change plus a count, rather than a plan refusal
//
// A run whose every paragraph is refused could have been a refusal, and is not,
// for two reasons. `refusedRewritePlan` short-circuits before `Segments` are
// built, so a refusal would discard the per-paragraph dispositions the mixed
// case needs — and the mixed case is the common one, a second pass over a draft
// the author has since edited. And a refusal exits non-zero for a user whose
// draft is simply finished, which is a worse report rather than a better one.
//
// # Two things deliberately left out
//
// `internal/rewrite` is untouched. The loop already records `candidate_hash` on
// acceptance and this guard is a plan-time read, so there is nothing for it to
// do.
//
// And the DISPOSITION does not reach the CLI. It reaches it nowhere today — no
// payload carries one — so `already-rewritten=2` is all a reader sees and they
// cannot ask WHICH paragraphs, which for `--in-place` is the actionable
// question. Surfacing per-segment dispositions is its own slice with its own
// payload decisions; deferred on purpose, recorded here so the next reader knows
// it was weighed.
//
// # What it does not catch
//
// A paragraph the author hand-edits after this tool wrote it hashes differently
// and is targeted normally. That is deliberate, and it answers the question #111
// left open — which state counts as original for a paragraph the author has since
// edited — by construction: this refuses only text it produced verbatim, and
// anything the author touched is the author's.

import (
	"os"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
	"github.com/fissible/hapax/internal/workflow"
)

// published records that this tool once accepted `text` for some paragraph, which
// is the only fact the check consults.
//
// The attempt is recorded against whichever node the plan happens to name,
// because the answer must not depend on that: `node_id` is
// `H(H(H(policy, every document hash), path), ordinal)`, so it does not survive
// the rewrite this exists to catch.
func published(t *testing.T, root string, plan workflow.RewritePlan, text string) {
	t.Helper()
	// ANY segment's node, not a target's: `settledStore`'s plan has no targets at
	// all, and that fixture is the one that shows the precedence over in-range.
	if len(plan.Segments) == 0 {
		t.Fatal("the plan has no node to record an attempt against")
	}
	attempt := store.RewriteAttempt{
		// Derived from the text, so publishing two paragraphs is two invocations
		// rather than one primary-key collision on (invocation, node, index).
		InvocationID:    identity.HashBytes([]byte("invocation for " + text)),
		Index:           0,
		ProfileID:       plan.ProfileID,
		ProviderID:      llm.ProviderOllama,
		NodeID:          plan.Segments[0].NodeID,
		CurrentHash:     identity.HashBytes([]byte("whatever the author wrote first")),
		CandidateHash:   identity.HashBytes([]byte(text)),
		CurrentDistance: 1.5, CandidateDistance: 0.5,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandInRange,
		Preserved:       true,
		TellsComparable: true,
		Accepted:        true,
	}
	if err := openStore(t, defaultStorePath(root)).PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("record an accepted attempt: %v", err)
	}
}

// dispositionOf returns the plan's disposition for the segment holding this text.
func dispositionOf(t *testing.T, plan workflow.RewritePlan, index int) workflow.Disposition {
	t.Helper()
	for _, segment := range plan.Segments {
		if segment.Index == index {
			return segment.Disposition
		}
	}
	t.Fatalf("the plan has no segment %d: %+v", index, plan.Segments)
	return ""
}

// dispositionSegment returns the planned segment at this index.
func dispositionSegment(t *testing.T, plan workflow.RewritePlan, index int) workflow.PlannedSegment {
	t.Helper()
	for _, segment := range plan.Segments {
		if segment.Index == index {
			return segment
		}
	}
	t.Fatalf("the plan has no segment %d", index)
	return workflow.PlannedSegment{}
}

// admittedBytes is the draft in the coordinates a plan's offsets are expressed
// in, which is NOT the file's.
//
// `text.Admit` strips a leading BOM, so its spans are in stripped coordinates and
// `Execute` takes its passage as `doc.Raw()[offset : offset+length]`. But `Plan`
// reads the file with `os.ReadFile` — BOM and all — so the most natural
// implementation of this check hashes `source[offset:offset+length]` and is off
// by three bytes on any BOM'd draft. `skipped_test.go` states the same hazard
// measured: "a reported offset of 0 does not select the first paragraph of the
// file — it selects the BOM."
//
// An earlier version of this helper read the file directly and would have passed
// against exactly that implementation, because no fixture here carried a BOM.
func admittedBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	doc, err := text.Admit(raw)
	if err != nil {
		t.Fatalf("admit draft: %v", err)
	}
	return doc.Raw()
}

// A paragraph this tool published is not a target; its neighbour still is.
//
// Both halves in one fixture, because a check that refused everything would
// satisfy the first alone, and the draft's two paragraphs differ only in that one
// of them was published.
func TestAParagraphThisToolPublishedIsNotATarget(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	first := planned(t, planRequest(root, draft))
	published(t, root, first, paragraphOne)

	plan := planned(t, planRequest(root, draft))

	if got := dispositionOf(t, plan, 0); got != workflow.DispositionAlreadyRewritten {
		t.Errorf("the published paragraph is %q, want %q", got,
			workflow.DispositionAlreadyRewritten)
	}
	if got := dispositionOf(t, plan, 1); got != workflow.DispositionTarget {
		t.Errorf("the untouched paragraph is %q, want %q", got, workflow.DispositionTarget)
	}
	if plan.Targets != 1 {
		t.Errorf("Targets = %d, want 1 — only the untouched paragraph", plan.Targets)
	}
}

// A refused candidate does not make its text untouchable.
//
// It was never published, so no file can hold it, and refusing it would refuse
// text this tool never emitted. `targetStore`'s draft is planned twice with an
// unaccepted attempt in between, and nothing may change.
func TestARefusedCandidateDoesNotMakeAParagraphUntouchable(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	first := planned(t, planRequest(root, draft))
	targets := targetsOf(first)
	if len(targets) == 0 {
		t.Fatal("no target to record against")
	}
	refused := store.RewriteAttempt{
		InvocationID:    strings.Repeat("ef", 32),
		Index:           0,
		ProfileID:       first.ProfileID,
		ProviderID:      llm.ProviderOllama,
		NodeID:          targets[0].NodeID,
		CurrentHash:     identity.HashBytes([]byte("an earlier current")),
		CandidateHash:   identity.HashBytes([]byte(paragraphOne)),
		CurrentDistance: 1.5, CandidateDistance: 1.75,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandNotYou,
		Preserved:       true,
		TellsComparable: true,
		Rejection:       rewrite.RejectionNotImproved,
	}
	if err := openStore(t, defaultStorePath(root)).PutRewriteAttempt(ctx(), refused); err != nil {
		t.Fatalf("record a refused attempt: %v", err)
	}

	plan := planned(t, planRequest(root, draft))

	if got := dispositionOf(t, plan, 0); got != workflow.DispositionTarget {
		t.Errorf("a paragraph whose text was only ever REFUSED is %q, want %q", got,
			workflow.DispositionTarget)
	}
	if plan.Targets != 2 {
		t.Errorf("Targets = %d, want 2", plan.Targets)
	}
}

// The hash is over the paragraph's own bytes, in the coordinates the plan speaks.
//
// `rewrite.Loop` records `identity.HashBytes([]byte(current))` where `current` is
// `doc.Raw()[offset : offset+length]`. The BOM row is the one no other test in
// this file can catch: `Plan` holds the file's bytes while the offsets are the
// admitted document's, and on a BOM'd draft the two differ by three.
//
// An earlier version of this comment claimed every other test here would pass a
// trimmed or newline-terminated hash. False —
// `TestAParagraphThisToolPublishedIsNotATarget` publishes `paragraphOne` and
// requires it flagged, which no trimming variant survives. What this adds is the
// byte SOURCE, not the byte range.
func TestTheHashIsOverTheParagraphsOwnBytes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body string
	}{
		{"a plain draft", executableDraft()},
		{"a draft with a byte order mark", "\ufeff" + executableDraft()},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, _ := targetStore(t)
			draft := writeDraft(t, root, c.body)
			first := planned(t, planRequest(root, draft))
			segment := dispositionSegment(t, first, 0)

			// Taken from the ADMITTED bytes, which is where the offsets point.
			span := string(admittedBytes(t, draft)[segment.Offset : segment.Offset+segment.Length])
			if span != paragraphOne {
				t.Fatalf("the planned span is %q, not the paragraph; the fixture has moved",
					span)
			}
			published(t, root, first, span)

			plan := planned(t, planRequest(root, draft))

			if got := dispositionOf(t, plan, 0); got != workflow.DispositionAlreadyRewritten {
				t.Errorf("a paragraph published under the hash of its own span is %q, want "+
					"%q — the check hashes bytes other than "+
					"doc.Raw()[offset:offset+length]", got,
					workflow.DispositionAlreadyRewritten)
			}
		})
	}
}

// It outranks in-range, which is the reading it exists to correct.
//
// `settledStore` places both paragraphs in range, so without this precedence a
// re-run over this tool's own output reports `in-range` — "this already reads as
// you" — about prose the tool wrote.
func TestAlreadyRewrittenOutranksInRange(t *testing.T) {
	t.Parallel()
	root, draft := settledStore(t)
	before := planned(t, planRequest(root, draft))
	if got := dispositionOf(t, before, 0); got != workflow.DispositionInRange {
		t.Fatalf("the fixture's first paragraph is %q, want %q; it cannot show the "+
			"precedence", got, workflow.DispositionInRange)
	}
	published(t, root, before, paragraphOne)

	plan := planned(t, planRequest(root, draft))

	if got := dispositionOf(t, plan, 0); got != workflow.DispositionAlreadyRewritten {
		t.Errorf("a published paragraph that now measures in range is %q, want %q",
			got, workflow.DispositionAlreadyRewritten)
	}
}

// It outranks contains-excisions too, in BOTH switches.
//
// The second rung of the precedence claim. Two rows because `Plan` has two
// switches and an implementation can insert the new case correctly into one and
// miss the other — which is why in-range got its own test, and which the
// explicit branch's separate `not-selected` handling makes easy to do.
//
// An earlier draft of this test paired `uncalibratedStore` with `planRequest`,
// which is automatic targeting on an uncalibrated store: `Plan` returns
// `refusedRewritePlan(base, RefusalUncalibrated)` BEFORE the segment loop, so
// the plan had no segments at all and the test failed on an empty slice rather
// than on a disposition. It never reached the code it named, and it was reported
// as intended red.
func TestAlreadyRewrittenOutranksContainsExcisions(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		root    func(*testing.T) string
		request func(root, draft string, excised int) workflow.RewriteRequest
	}{
		{
			name: "automatic targeting",
			root: func(t *testing.T) string { return bandedStore(t, "not-you") },
			request: func(root, draft string, _ int) workflow.RewriteRequest {
				return planRequest(root, draft)
			},
		},
		{
			name: "explicit targeting",
			root: uncalibratedStore,
			request: func(root, draft string, excised int) workflow.RewriteRequest {
				return explicitRequest(root, draft, excised)
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := c.root(t)
			draft := writeDraft(t, root, excisionDraft)
			leaves := admittedLeaves(t, draft, storedFloor(t, root))
			excised := -1
			for i, leaf := range leaves {
				if leaf.HasExcisions {
					excised = i
					break
				}
			}
			if excised < 0 {
				t.Fatal("the excision fixture produced no paragraph with excisions")
			}

			before := planned(t, c.request(root, draft, excised))
			if before.Refusal != "" {
				t.Fatalf("the plan refused %q; this fixture never reaches the switch",
					before.Refusal)
			}
			if got := dispositionOf(t, before, excised); got != workflow.DispositionContainsExcisions {
				t.Fatalf("segment %d is %q, want %q; the fixture cannot show the "+
					"precedence", excised, got, workflow.DispositionContainsExcisions)
			}
			segment := dispositionSegment(t, before, excised)
			span := string(admittedBytes(t, draft)[segment.Offset : segment.Offset+segment.Length])

			published(t, root, before, span)
			plan := planned(t, c.request(root, draft, excised))

			if got := dispositionOf(t, plan, excised); got != workflow.DispositionAlreadyRewritten {
				t.Errorf("a published paragraph that also carries excisions is %q, want %q",
					got, workflow.DispositionAlreadyRewritten)
			}
		})
	}
}

// Every occurrence of a published paragraph is refused, and each one counts.
//
// `repeatedDraft` says the same paragraph twice, and exists because "which
// occurrence a rewrite lands on is invisible in a draft whose paragraphs are all
// distinct". A hash-keyed check sharpens that: one recorded hash answers for both
// segments. This also pins what the count counts — SEGMENTS refused, not distinct
// hashes — which nothing else here decides.
func TestEveryOccurrenceOfAPublishedParagraphIsRefused(t *testing.T) {
	t.Parallel()
	root, _ := targetStore(t)
	draft := writeDraft(t, root, repeatedDraft())
	before := planned(t, planRequest(root, draft))
	if len(before.Segments) != 2 {
		t.Fatalf("the repeated draft planned %d segments, want 2", len(before.Segments))
	}

	published(t, root, before, paragraphOne)
	plan := planned(t, planRequest(root, draft))

	for _, index := range []int{0, 1} {
		if got := dispositionOf(t, plan, index); got != workflow.DispositionAlreadyRewritten {
			t.Errorf("occurrence %d is %q, want %q", index, got,
				workflow.DispositionAlreadyRewritten)
		}
	}
	if plan.ParagraphsAlreadyRewritten != 2 {
		t.Errorf("ParagraphsAlreadyRewritten = %d, want 2 — the count is of segments "+
			"refused, not of distinct hashes", plan.ParagraphsAlreadyRewritten)
	}
}

// And under explicit targeting, where a caller asked for it by number.
//
// Explicit targeting overrides calibration; it does not override this. The
// second paragraph is published TOO and still reports `not-selected`, which is
// the assertion that carries weight: an earlier draft left it unpublished, where
// `not-selected` was the only disposition it could possibly have had and the
// assertion was compatible with every precedence.
//
// And the count follows consideration, not the store: one, not two.
func TestExplicitTargetingDoesNotOverrideIt(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	first := planned(t, planRequest(root, draft))
	published(t, root, first, paragraphOne)
	published(t, root, first, paragraphTwo)

	request := planRequest(root, draft)
	request.Paragraphs = []int{0}
	plan := planned(t, request)

	if got := dispositionOf(t, plan, 0); got != workflow.DispositionAlreadyRewritten {
		t.Errorf("an explicitly named published paragraph is %q, want %q", got,
			workflow.DispositionAlreadyRewritten)
	}
	if got := dispositionOf(t, plan, 1); got != workflow.DispositionNotSelected {
		t.Errorf("a published paragraph nobody named is %q, want %q — the caller's "+
			"selection decides what is under consideration", got,
			workflow.DispositionNotSelected)
	}
	if plan.Targets != 0 {
		t.Errorf("Targets = %d, want 0", plan.Targets)
	}
	if plan.ParagraphsAlreadyRewritten != 1 {
		t.Errorf("ParagraphsAlreadyRewritten = %d, want 1 — both paragraphs were "+
			"published and only one was under consideration",
			plan.ParagraphsAlreadyRewritten)
	}
}

// The count reaches the plan, so a run with nothing left to do can say why.
//
// Without it, a second `--in-place` run reports `nothing-to-change` and exit 0,
// which reads as "your draft is in your voice" when it means "this is my own
// output". The count is what separates those two, and it follows
// `ParagraphsBelowFloor`, which is on this surface for the same reason.
func TestThePlanCountsTheParagraphsItRefusedAsAlreadyRewritten(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	first := planned(t, planRequest(root, draft))
	published(t, root, first, paragraphOne)
	published(t, root, first, paragraphTwo)

	plan := planned(t, planRequest(root, draft))

	if plan.ParagraphsAlreadyRewritten != 2 {
		t.Errorf("ParagraphsAlreadyRewritten = %d, want 2", plan.ParagraphsAlreadyRewritten)
	}
	if plan.Targets != 0 || plan.State != workflow.StateNothingToChange {
		t.Errorf("Targets = %d and State = %q, want 0 and %q", plan.Targets, plan.State,
			workflow.StateNothingToChange)
	}
	// And zero when none were, so the member is a measurement rather than a
	// constant. A fresh STORE, not a fresh draft: the same bytes, with nothing
	// recorded against them.
	clean, cleanDraft := targetStore(t)
	if got := planned(t, planRequest(clean, cleanDraft)).ParagraphsAlreadyRewritten; got != 0 {
		t.Errorf("ParagraphsAlreadyRewritten = %d on a draft nothing was published for, "+
			"want 0", got)
	}
}

// The disposition is in the declared vocabulary.
func TestAlreadyRewrittenIsDeclared(t *testing.T) {
	t.Parallel()
	if string(workflow.DispositionAlreadyRewritten) != "already-rewritten" {
		t.Errorf("DispositionAlreadyRewritten = %q, want %q",
			workflow.DispositionAlreadyRewritten, "already-rewritten")
	}
	seen := 0
	for _, d := range workflow.Dispositions() {
		if d == workflow.DispositionAlreadyRewritten {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%q appears %d times in Dispositions(): %v",
			workflow.DispositionAlreadyRewritten, seen, workflow.Dispositions())
	}
}

// The round trip, which is #111's reproduction and the only test here that does
// not fabricate the store row.
//
// Every other test calls `published()`, which hashes bytes the test just read.
// That pins the plan's half and nothing else. What it cannot establish is that
// what `rewrite.Loop` RECORDS equals what a later plan READS OUT of the
// rewritten file — a chain of four files, any one of which could trim, re-wrap
// or re-terminate and leave #111 unfixed with a green suite:
//
//	rewrite.go:437     hashes the candidate that becomes outcome.Text
//	workflow.go        splices out.Text into the replacement
//	assemble.go        appends it verbatim, and re-adds the BOM
//	                   so the next run's stripped coordinates realign
//
// So this runs a real rewrite, writes the bytes back over the draft exactly as
// `--in-place` does, and plans again. That IS the second invocation the issue is
// about.
func TestARealRewriteWrittenBackIsRefusedOnTheNextRun(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	requireCandidates(t, root)
	runner, _ := executingRunner(&arm{provider: newProvider(t,
		map[string][]string{paragraphOne: {improvesOne}})}, nil)

	outcome, err := runner.Rewrite(ctx(), workflow.RewriteInput{
		StorePath: defaultStorePath(root), CorpusRoot: root, Register: "essays",
		Path: draft, Choice: localChoice(),
	})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if outcome.Report().Improved != 1 {
		t.Fatalf("Improved = %d, want 1; nothing was rewritten to write back",
			outcome.Report().Improved)
	}

	// What --in-place does: publish.Replace over the source.
	if err := os.WriteFile(draft, outcome.Content(), 0o644); err != nil {
		t.Fatalf("write the rewrite back: %v", err)
	}

	plan := planned(t, planRequest(root, draft))

	// The rewritten paragraph is the one whose text changed, so find it by
	// comparing against what the draft used to say rather than by index.
	rewritten := -1
	for _, segment := range plan.Segments {
		span := string(admittedBytes(t, draft)[segment.Offset : segment.Offset+segment.Length])
		if span == improvesOne {
			rewritten = segment.Index
		}
	}
	if rewritten < 0 {
		t.Fatalf("the written-back draft holds no segment equal to the accepted "+
			"candidate; the bytes changed between the loop and the file: %q",
			string(outcome.Content()))
	}
	if got := dispositionOf(t, plan, rewritten); got != workflow.DispositionAlreadyRewritten {
		t.Errorf("segment %d holds this tool's own accepted output and is %q, want %q — "+
			"a second --in-place run would rewrite it again, anchored on it",
			rewritten, got, workflow.DispositionAlreadyRewritten)
	}
	// And the author's untouched paragraph is still a target. #111 is
	// "re-targets its own output while leaving the author's alone", and an
	// implementation that refused everything once anything was published would
	// satisfy the assertion above. Guarded elsewhere, but this is the test that
	// carries the name.
	for _, segment := range plan.Segments {
		if segment.Index == rewritten {
			continue
		}
		if segment.Disposition != workflow.DispositionTarget {
			t.Errorf("the untouched paragraph at %d is %q, want %q", segment.Index,
				segment.Disposition, workflow.DispositionTarget)
		}
	}
	if plan.ParagraphsAlreadyRewritten != 1 {
		t.Errorf("ParagraphsAlreadyRewritten = %d, want 1",
			plan.ParagraphsAlreadyRewritten)
	}
}

// And the count reaches the report the composition root receives.
//
// `Runner.Rewrite` builds its report from the plan and the execution; the count
// is the plan's, and a report that dropped it would leave the CLI nothing to
// print. This is the producer step #117 established is not optional.
func TestTheRewriteReportCarriesTheAlreadyRewrittenCount(t *testing.T) {
	t.Parallel()
	root, draft := targetStore(t)
	first := planned(t, planRequest(root, draft))
	published(t, root, first, paragraphOne)
	requireCandidates(t, root)
	runner, _ := executingRunner(&arm{provider: newProvider(t,
		map[string][]string{paragraphTwo: {improvesTwo}})}, nil)

	outcome, err := runner.Rewrite(ctx(), workflow.RewriteInput{
		StorePath: defaultStorePath(root), CorpusRoot: root, Register: "essays",
		Path: draft, Choice: localChoice(),
	})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}

	report := outcome.Report()
	if report.ParagraphsAlreadyRewritten != 1 {
		t.Errorf("ParagraphsAlreadyRewritten = %d, want 1",
			report.ParagraphsAlreadyRewritten)
	}
	// The other paragraph still ran, so the count is not a refusal in disguise.
	if report.Targets != 1 || report.Improved != 1 {
		t.Errorf("Targets = %d and Improved = %d, want 1 and 1 — the paragraph that "+
			"was not published should still have been rewritten",
			report.Targets, report.Improved)
	}
}
