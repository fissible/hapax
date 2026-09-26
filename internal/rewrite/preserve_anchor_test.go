package rewrite_test

// #116. Two individually-preserved rewrites compose into one that loses an
// entity, because `Gate.Preserve` receives the ADVANCING `current` and
// `preserve.Check` is not transitive.
//
// Measured against the shipped code:
//
//	"Smith met Jones at the market on Tuesday."  ->  "...met jones..."          preserved=true
//	"...met jones..."                            ->  "Smith went to the market" preserved=true
//	"Smith met Jones at the market on Tuesday."  ->  "Smith went to the market" preserved=FALSE
//	                                                   [preserve-v1:entity:lost:...]
//
// Two accepted attempts and the entity the guard exists to protect is gone.
// `--attempts` has no upper bound, so the chain is as long as a caller asks for.
//
// # Why the fix is an anchor and not transitivity
//
// The first proposal was to make `preserve.Check` transitive by building
// `entityItems`'s watch set from the current text alone. Measured, that silently
// drops ENTITY invention: only the entity class is gated by a pair-derived watch
// set, so a candidate fabricating a name would have no watched item and nothing
// to count. The other four classes — number, negation, URL, quote — detect
// invention from per-document maps and are unaffected. A style rewriter that
// stops noticing invented names to gain transitivity has made a bad trade.
//
// Anchoring on the ORIGINAL removes the need for transitivity instead of
// supplying it. With one fixed reference there is no composition: every accepted
// candidate preserves the original's items by construction.
//
// # The distinction this settles, because the two gates now look inconsistent
//
// They are not. They have opposite structure:
//
//   - An INVARIANT — "the published text must still hold what the original
//     held" — anchors on the ORIGINAL. Preserve, and #107's script growth.
//   - A MONOTONE COMPARISON — "each step must be no worse than the best so far"
//     — ratchets against CURRENT. Tells, the distance, and #91's script
//     introduction, where the script set only shrinks so current is stricter.
//
// Anchoring tells would WEAKEN it: original at 5 findings, one accepted at 3,
// the next at 4 passes against the original while regressing against the best
// found, and that fourth is what gets published. Making the gates uniform would
// break the two that ratchet.
//
// # Two consequences, both stated rather than discovered
//
// `PreserveIdentifiers` changes meaning: differences against the original, not
// against the running text. No schema change, but the recorded evidence answers
// a different question than it did.
//
// And the accept rate will fall. The watch predicate treats every
// sentence-initial ordinary word as an entity — measured, 40.7% of watched items
// in the maintainer's corpus are words that corpus mostly writes lowercase — and
// today the advancing anchor launders them away after one accepted step. That
// laundering IS this bug, so closing it pins those artefacts for the whole loop.
// `not-preserved` is already the most common rejection in that store, 8 of 21
// attempts. The predicate is #118; it is not this issue, and it will be more
// visible after this.

import (
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// The gate is asked about the ORIGINAL and the candidate.
//
// A gate handed the advancing `current` passes both rungs of the measured
// composition above. The arguments are recorded because a verdict-only assertion
// cannot tell the two apart: the fake answers the same either way.
func TestThePreserveGateIsAskedAboutTheOriginal(t *testing.T) {
	gate := passingGate()
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	// Both accepted, so `current` advanced between the two calls.
	if !got.Changed || got.Text != betterYet {
		t.Fatalf("the fixture must accept BOTH candidates or the anchor is not under "+
			"test: changed=%v text=%q", got.Changed, got.Text)
	}
	if len(gate.preserveArgs) != 2 {
		t.Fatalf("the gate was asked %d times, want 2: %v",
			len(gate.preserveArgs), gate.preserveArgs)
	}
	if gate.preserveArgs[0] != [2]string{original, better} {
		t.Errorf("the first call was %v, want (original, better)", gate.preserveArgs[0])
	}
	// The anchor does not advance with `current`.
	if gate.preserveArgs[1] != [2]string{original, betterYet} {
		t.Errorf("the second call was %v, want (original, betterYet) — preserve is an "+
			"INVARIANT and anchors on the original", gate.preserveArgs[1])
	}
}

// And it does not advance after a refusal either.
func TestThePreserveAnchorSurvivesARefusal(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
		verdicts: map[string]gateVerdict{
			// Refused on tells, so preserve ran and the attempt was not accepted.
			better: {preserved: true, comparison: 1, comparable: true},
		},
	}
	loop, _, _, provider, _ := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.30), betterYet: scored(0.75),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
	if len(gate.preserveArgs) != 2 {
		t.Fatalf("the gate was asked %d times, want 2: %v",
			len(gate.preserveArgs), gate.preserveArgs)
	}
	for i, args := range gate.preserveArgs {
		if args[0] != original {
			t.Errorf("call %d anchored on %q, want the original", i, args[0])
		}
	}
}

// The composition that #116 is about, driven through the loop.
//
// The fake answers per candidate, so this pins the LOOP's behaviour rather than
// `preserve.Check`'s: a candidate that would be preserved against the running
// text but not against the original must be refused. Under the old anchor the
// second candidate was accepted and published.
func TestACandidatePreservedOnlyAgainstTheRunningTextIsRefused(t *testing.T) {
	gate := &anchorSensitiveGate{fakeGate: passingGate()}
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, passingGate())
	loop.Gate = gate

	got := run(t, loop)

	// The first candidate is admissible against the original and is accepted.
	// The second is admissible against the FIRST and not against the original,
	// so it must be refused and must not reach the text.
	if got.Text != better {
		t.Errorf("the outcome carries %q, want the first candidate — the second is "+
			"preserved only against the running text", got.Text)
	}
	if len(got.Attempts) != 2 || len(store.attempts) != 2 {
		t.Fatalf("%d attempts recorded and %d stored, want 2 and 2",
			len(got.Attempts), len(store.attempts))
	}
	for where, attempt := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[1], "store": store.attempts[1],
	} {
		if attempt.Rejection != rewrite.RejectionNotPreserved {
			t.Errorf("the %s second attempt was rejected as %q, want %q",
				where, attempt.Rejection, rewrite.RejectionNotPreserved)
		}
		if attempt.Preserved {
			t.Errorf("the %s second attempt records preserve as passing", where)
		}
		// The identifiers now name differences against the ORIGINAL.
		if len(attempt.PreserveIdentifiers) != 1 ||
			attempt.PreserveIdentifiers[0] != lostAgainstTheOriginal {
			t.Errorf("the %s second attempt records %v, want the identifier measured "+
				"against the original", where, attempt.PreserveIdentifiers)
		}
	}
}

const lostAgainstTheOriginal = "preserve-v1:entity:lost:3d4c981bf761d9b8"

// anchorSensitiveGate answers differently depending on which text it is handed,
// which is the whole point: it is preserved against `better` and not against
// `original`. A gate reading the advancing `current` sees the first and accepts.
type anchorSensitiveGate struct {
	*fakeGate
}

func (a *anchorSensitiveGate) Preserve(anchor, candidate string) (rewrite.Preservation, error) {
	a.fakeGate.Preserve(anchor, candidate)
	if candidate == betterYet && anchor == original {
		return rewrite.Preservation{
			Preserved:   false,
			Identifiers: []string{lostAgainstTheOriginal},
		}, nil
	}
	return rewrite.Preservation{Preserved: true}, nil
}

// Tells still ratchets against the running text, and must not be anchored.
//
// The two gates have opposite structure, so this is asserted rather than left to
// a reader to infer from the absence of a test. Anchoring tells would accept a
// candidate that regressed against the best found so far.
func TestTheTellsGateStillRatchetsAgainstTheRunningText(t *testing.T) {
	gate := passingGate()
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	if !got.Changed || got.Text != betterYet {
		t.Fatalf("the fixture must accept BOTH candidates: changed=%v text=%q",
			got.Changed, got.Text)
	}
	if len(gate.tellsArgs) != 2 {
		t.Fatalf("the gate was asked %d times, want 2: %v",
			len(gate.tellsArgs), gate.tellsArgs)
	}
	if gate.tellsArgs[0] != [2]string{original, better} {
		t.Errorf("the first call was %v, want (original, better)", gate.tellsArgs[0])
	}
	// ADVANCES, unlike preserve.
	if gate.tellsArgs[1] != [2]string{better, betterYet} {
		t.Errorf("the second call was %v, want (better, betterYet) — tells is a "+
			"monotone comparison and ratchets against the running text",
			gate.tellsArgs[1])
	}
}
