package rewrite_test

// #115. Nothing checks what a candidate becomes once spliced back into its
// document, so a rewrite can change the document's STRUCTURE while the caller is
// told its prose improved.
//
// # Scope, corrected by measuring end to end
//
// The loop scores each candidate in isolation before any gate runs, so a
// candidate admitting to zero scoreable segments is refused as `not-one-segment`
// long before assembly. Measured through the real Runner, a `> ` prefix, a `## `
// prefix and a below-floor candidate never reach this far. The issue was filed
// listing all five shapes, from a measurement of the splice alone.
//
// Two survivors, both still `role=paragraph`, both changing structure. Measured
// against a two-paragraph draft:
//
//	plain                 included=2  paragraph/[document]@0+148
//	a list-item prefix    included=2  paragraph/[document list list-item]@2+148
//	a blank line          included=3  two leaves where there was one
//	an unterminated fence included=0  the whole document becomes one code block
//
// # Why this is a rejection and not an error
//
// `Assemble`'s failure path is `return ExecuteResult{}, err` — a bare error with
// no refusal code — and the loop has already written `accepted=1, rejection=''`
// for every target it accepted, because `RecordAttempt` fires before assembly
// ever runs. So one damaging candidate discarded every other improvement in the
// run AND left the store claiming acceptance for a run that emitted nothing.
//
// A rejection fixes the second, which is the one that matters: the store no
// longer asserts something false.
//
// It does NOT recover the candidate. Measured: after a rejection `current` does
// not advance, so the next attempt's prompt is byte-identical, and
// `InstructionPreamble` says nothing about Markdown or structure. If the model
// emits a list marker at rate p, the retry emits it at rate p. Making the retry
// informative needs a preamble clause, which shifts every profile distance and
// needs its own measurement.
//
// # Precedence: LAST
//
// Not because it is the most expensive, though it is — up to 89.6 ms against
// 26 µs to score a paragraph. Because it is **the only gate whose verdict is not
// a function of the recorded texts.** Preserve, tells, language and the distance
// are all functions of (original, current, candidate), so a stored
// `rewrite_attempt` row can be replayed and its `rejection` recomputed.
// Spliceability depends on the document and the span, and the row carries only
// `node_id`. Ranking a document-dependent verdict above text-dependent ones
// would stop the `rejection` field being reproducible from the evidence beside
// it.
//
// # The anchor is in the method's name
//
// `Assemble` splices into the ORIGINAL document at the ORIGINAL span, so the gate
// must judge against those. The other three gates put every anchor in the
// signature because #116 made anchor choice something a reader has to see; this
// one holds the document as construction state, so the name carries it instead.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// A candidate that would not splice back as one leaf in place is refused.
func TestACandidateThatWouldNotSpliceIsRefused(t *testing.T) {
	gate := passingGate()
	gate.fallback.spliceable = false
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)

	got := run(t, loop)

	if got.Changed || got.Text != original {
		t.Fatalf("an unspliceable candidate was accepted: changed=%v text=%q",
			got.Changed, got.Text)
	}
	if len(got.Attempts) == 0 || len(store.attempts) == 0 {
		t.Fatalf("%d attempts recorded and %d stored", len(got.Attempts), len(store.attempts))
	}
	for where, attempt := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[0], "store": store.attempts[0],
	} {
		if attempt.Rejection != rewrite.RejectionNotSpliceable {
			t.Errorf("the %s attempt was rejected as %q, want %q",
				where, attempt.Rejection, rewrite.RejectionNotSpliceable)
		}
		if attempt.Accepted {
			t.Errorf("the %s attempt was accepted", where)
		}
	}
}

// A candidate that would splice cleanly is not refused for it.
//
// The other half: a gate consulted and always answering no satisfies the test
// above.
func TestASpliceableCandidateIsNotRefusedForIt(t *testing.T) {
	got := run(t, func() rewrite.Loop {
		loop, _, _, _, _ := loopOver(t,
			map[string]score.Report{original: scored(0.90), better: scored(0.30)},
			[]string{better}, passingGate())
		return loop
	}())

	if !got.Changed {
		t.Fatalf("a spliceable candidate was not accepted: %+v", got.Attempts)
	}
	for i, attempt := range got.Attempts {
		if attempt.Rejection == rewrite.RejectionNotSpliceable {
			t.Errorf("attempt %d refused as unspliceable despite splicing cleanly", i)
		}
	}
}

// Every other rejection is reported ahead of this one.
//
// Precedence is LAST, so each of the codes that can co-occur with it must win.
// Table-driven over all five, because a single case pins only one edge of the
// ordering and this gate sits below the whole set.
func TestEveryOtherRejectionIsReportedBeforeUnspliceable(t *testing.T) {
	for _, c := range []struct {
		name    string
		mutate  func(*fakeGate)
		reports map[string]score.Report
		want    rewrite.RejectionCode
	}{
		{
			name:    "not preserved",
			mutate:  func(g *fakeGate) { g.fallback.preserved = false; g.fallback.identifiers = []string{lostIdentifier} },
			reports: map[string]score.Report{original: scored(0.90), better: scored(0.30)},
			want:    rewrite.RejectionNotPreserved,
		},
		{
			name:    "language",
			mutate:  func(g *fakeGate) { g.fallback.introduced = []string{"Han"} },
			reports: map[string]score.Report{original: scored(0.90), better: scored(0.30)},
			want:    rewrite.RejectionLanguage,
		},
		{
			name:    "language growth",
			mutate:  func(g *fakeGate) { g.fallback.overgrown = []string{"Han"} },
			reports: map[string]score.Report{original: scored(0.90), better: scored(0.30)},
			want:    rewrite.RejectionLanguageGrowth,
		},
		{
			name:    "tells worse",
			mutate:  func(g *fakeGate) { g.fallback.comparison = 1 },
			reports: map[string]score.Report{original: scored(0.90), better: scored(0.30)},
			want:    rewrite.RejectionTellsWorse,
		},
		{
			name:    "not improved",
			mutate:  func(g *fakeGate) {},
			reports: map[string]score.Report{original: scored(0.30), better: scored(0.90)},
			want:    rewrite.RejectionNotImproved,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			gate := passingGate()
			gate.fallback.spliceable = false
			c.mutate(gate)
			loop, _, _, _, _ := loopOver(t, c.reports, []string{better}, gate)

			got := run(t, loop)

			if len(got.Attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			if got.Attempts[0].Rejection != c.want {
				t.Errorf("rejected as %q, want %q — unspliceable is reported LAST",
					got.Attempts[0].Rejection, c.want)
			}
		})
	}
}

const lostIdentifier = "preserve-v1:number:lost:3d4c981bf761d9b8"

// The gate is consulted even when something else already refuses.
//
// "Every gate is consulted, whatever the first one says" is the loop's existing
// invariant, and this gate joins it. Without this, an implementation that skipped
// the expensive check once a rejection existed would pass every test above —
// and the evidence each gate produced belongs in the record either way.
func TestTheSpliceGateIsConsultedEvenWhenSomethingElseRefuses(t *testing.T) {
	gate := &countingGate{fakeGate: passingGate()}
	gate.fakeGate.fallback.preserved = false
	gate.fakeGate.fallback.identifiers = []string{lostIdentifier}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, passingGate())
	loop.Gate = gate

	got := run(t, loop)

	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	if got.Attempts[0].Rejection != rewrite.RejectionNotPreserved {
		t.Fatalf("rejected as %q; this fixture must fail preserve",
			got.Attempts[0].Rejection)
	}
	if gate.spliceCalls != len(got.Attempts) {
		t.Errorf("the splice gate was consulted %d times across %d attempts",
			gate.spliceCalls, len(got.Attempts))
	}
}

// Consulted once per candidate, like the others.
func TestTheSpliceGateIsConsultedOncePerCandidate(t *testing.T) {
	gate := &countingGate{fakeGate: passingGate()}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, passingGate())
	loop.Gate = gate

	got := run(t, loop)

	if gate.spliceCalls != len(got.Attempts) {
		t.Errorf("the splice gate was consulted %d times across %d attempts",
			gate.spliceCalls, len(got.Attempts))
	}
}

// A gate error is an operational failure, not a refusal.
func TestASpliceGateErrorFailsRatherThanAccepting(t *testing.T) {
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, passingGate())
	loop.Gate = &erroringSpliceGate{fakeGate: passingGate()}

	_, err := loop.Rewrite(context.Background(), rewrite.Segment{Text: original, SpanRef: "span-0"})

	if err == nil {
		t.Fatal("a gate error produced no error")
	}
	if !strings.Contains(err.Error(), "splice") {
		t.Errorf("the error does not name the gate that failed: %v", err)
	}
	// A check that did not run has nothing to persist.
	if len(store.attempts) != 0 {
		t.Errorf("the store recorded %d attempts for a check that never ran",
			len(store.attempts))
	}
}

// The code is declared, spelled as it reads, and distinct.
func TestRejectionNotSpliceableIsDeclared(t *testing.T) {
	if string(rewrite.RejectionNotSpliceable) != "not-spliceable" {
		t.Errorf("RejectionNotSpliceable = %q, want %q",
			rewrite.RejectionNotSpliceable, "not-spliceable")
	}
	seen := 0
	for _, code := range rewrite.RejectionCodes() {
		if code == rewrite.RejectionNotSpliceable {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%q appears %d times in RejectionCodes(): %v",
			rewrite.RejectionNotSpliceable, seen, rewrite.RejectionCodes())
	}
	// It must be LAST in the declared order, because the order is the precedence
	// and this gate is the only one whose verdict cannot be recomputed from a
	// stored row's two hashes.
	codes := rewrite.RejectionCodes()
	if codes[len(codes)-1] != rewrite.RejectionNotSpliceable {
		t.Errorf("the last declared code is %q, want %q", codes[len(codes)-1],
			rewrite.RejectionNotSpliceable)
	}
}

// erroringSpliceGate passes every other check and fails only this one.
type erroringSpliceGate struct {
	*fakeGate
}

func (e *erroringSpliceGate) SpliceableIntoOriginal(candidate string) (rewrite.SpliceVerdict, error) {
	return rewrite.SpliceVerdict{}, errSpliceGate
}

// Deliberately naming no gate, so the loop has to supply the attribution.
var errSpliceGate = errors.New("dependency unavailable")
