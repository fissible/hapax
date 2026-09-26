package rewrite_test

// #116. Two individually-preserved rewrites compose into one that loses an
// entity, because `Gate.Preserve` receives the ADVANCING `current` and
// `preserve.Check` is not transitive.
//
// Measured against shipped `preserve.Check`, with the strings written out in
// full because abbreviating one of them changed the result:
//
//	O  "Smith met Jones at the market on Tuesday."
//	M  "Smith met jones at the market on Tuesday."
//	C  "Smith went to the market on Tuesday."
//
//	O -> M   preserved=true
//	M -> C   preserved=true
//	O -> C   preserved=FALSE   [preserve-v1:entity:lost:6dc55025ffa645ac]
//
// The "on Tuesday" in C is load-bearing. Drop it and rung two becomes false on
// its own, because Tuesday leaves the pair's watch set — so that shorter triple
// is not a witness for this bug at all. An earlier draft of this header
// abbreviated C exactly that way while presenting the numbers as measured.
//
// Two accepted attempts and the entity the guard exists to protect is gone.
// `--attempts` has no upper bound, so the chain is as long as a caller asks for.
//
// # It needs no proper noun
//
// The same composition, with nothing that looks like an entity in it:
//
//	O  "Yesterday the market was busy and the sellers were loud."
//	M  "the market yesterday was busy and the sellers were loud."
//	C  "the market was busy and the sellers were loud."
//
//	O -> M   preserved=true
//	M -> C   preserved=true
//	O -> C   preserved=FALSE   [preserve-v1:entity:lost:734e476d2f0f911f]
//
// What the advancing anchor launders is not the lowercasing — the watch set is
// case-folded, so a case change is never a loss — but watch MEMBERSHIP. Once no
// text in the pair capitalizes "Yesterday", it leaves the watch set and the next
// candidate may delete it. That is this bug and #118's cost in one triple.
//
// # Why the fix is an anchor and not transitivity
//
// The first proposal was to make `Check` transitive by building `entityItems`'s
// watch set from the current text alone. Measured, that silently drops ENTITY
// invention: only that class is gated by a pair-derived watch set, so a
// candidate fabricating a name would have no watched item and nothing to count.
// Number, negation, URL and quote invention are unaffected. A style rewriter
// that stops noticing invented names to gain transitivity has made a bad trade.
//
// Anchoring on the ORIGINAL removes the need for transitivity instead of
// supplying it: with one fixed reference there is no composition.
//
// # The distinction this settles, because the gates now look inconsistent
//
// They are not. They have opposite structure:
//
//   - An INVARIANT — "the published text must still hold what the original
//     held" — anchors on the ORIGINAL. Preserve, and #107's script growth.
//   - A MONOTONE COMPARISON — "each step must be no worse than the best so far"
//     — ratchets against CURRENT. Tells, the distance, and #91's script
//     introduction.
//
// #91 belongs in the second group, and it is not a judgement call.
// `ScriptSet.Introduced` is threshold-free set containment, so acceptance forces
// `scripts(candidate) ⊆ scripts(current)`, hence `scripts(current) ⊆
// scripts(original)` by induction from `current := segment.Text`. The current
// anchor is therefore strictly STRICTER for introduction, and #107's growth rule
// only adds refusals so it cannot disturb that induction.
//
// The cost of that strictness, unstated until now: once an accepted step drops
// one of the original's own scripts, a later candidate restoring it is refused
// as an introduction. The ratchet over-refuses exactly where preserve
// under-refused.
//
// # What this does NOT close
//
// `segment.Text` is the original of THIS invocation. `workflow` slices the
// passage out of the input document, so running `hapax rewrite` twice over an
// already-rewritten file composes exactly as described above, unbounded and
// unrecorded. That is #111, and this slice inherits it as #107 does.
//
// And `PreserveIdentifiers` changes meaning: differences against the original,
// not against the running text. No schema change, but the recorded evidence
// answers a different question than it did.

import (
	"context"
	"testing"

	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// The gate is asked about the ORIGINAL, at every depth.
//
// Two candidates prove too little: a one-step-LAGGED anchor — "the previous
// current" — passes a depth-two test, because the first call's anchor is the
// original either way and the second is the first candidate under both a lagged
// and a correct implementation only when there has been exactly one acceptance.
// Three acceptances separate those two.
//
// FOUR candidates, because three did not separate a correct anchor from one that
// is correct for three calls and advances from the fourth on — that passed the
// whole suite. A fourth GATE call needs a fourth candidate, not a raised attempt
// cap: an exhausted provider returns an empty response before the gate block
// runs, so `Attempts = 4` with three candidates buys no call at all.
//
// Both gates' arguments are read, so the same lag applied to #107's growth
// anchor is caught here too.
func TestBothGatesAreAskedAboutTheOriginalAtEveryDepth(t *testing.T) {
	gate := passingGate()
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.70), betterYet: scored(0.50),
			bestYet: scored(0.30), bestOfAll: scored(0.20),
		},
		[]string{better, betterYet, bestYet, bestOfAll}, gate)
	loop.Options.Attempts = 5

	got := run(t, loop)

	// Four acceptances, so `current` advanced three times after the first call.
	if !got.Changed || got.Text != bestOfAll {
		t.Fatalf("the fixture must accept all FOUR candidates or depth is not under "+
			"test: changed=%v text=%q", got.Changed, got.Text)
	}
	// Exactly three, because one call per candidate is the contract — the same
	// invariant `TestLanguageIsConsultedOncePerCandidate` states for the other
	// gate. An implementation checking BOTH anchors would be strictly safer and
	// is still refused here, deliberately: two calls per candidate doubles the
	// provider-independent work and the record would have to say which anchor
	// each verdict came from.
	if len(gate.preserveArgs) != 4 {
		t.Fatalf("preserve was asked %d times, want 4 — one per candidate: %v",
			len(gate.preserveArgs), gate.preserveArgs)
	}
	wantCandidates := []string{better, betterYet, bestYet, bestOfAll}
	for i, args := range gate.preserveArgs {
		if args[0] != original {
			t.Errorf("preserve call %d anchored on %q, want the original — a "+
				"one-step-lagged anchor passes at depth two and is wrong here", i, args[0])
		}
		if args[1] != wantCandidates[i] {
			t.Errorf("preserve call %d asked about %q, want %q", i, args[1], wantCandidates[i])
		}
	}
	// #107's anchor, at the same depth and through the same fixture.
	if len(gate.languageArgs) != 4 {
		t.Fatalf("language was asked %d times, want 4: %v",
			len(gate.languageArgs), gate.languageArgs)
	}
	for i, args := range gate.languageArgs {
		if args[0] != original {
			t.Errorf("language call %d anchored on %q, want the original", i, args[0])
		}
	}
	// And tells keeps ratcheting, at depth. The middle argument advances.
	if len(gate.tellsArgs) != 4 {
		t.Fatalf("tells was asked %d times, want 4: %v", len(gate.tellsArgs), gate.tellsArgs)
	}
	wantCurrent := []string{original, better, betterYet, bestYet}
	for i, args := range gate.tellsArgs {
		if args[0] != wantCurrent[i] {
			t.Errorf("tells call %d compared against %q, want %q — tells is a monotone "+
				"comparison and ratchets", i, args[0], wantCurrent[i])
		}
	}
}

// The composition #116 is about, driven through the loop with the REAL gate.
//
// Every other assertion here runs a fake, so nothing ran `preserve.Check`
// through the loop at all — and the production gate's own argument order was
// unpinned: swapping it passes the whole repository, because `Check`'s verdict
// is symmetric. Only the recorded evidence inverts, every `lost` becoming an
// `invented` with a different digest. The decision survives and the audit trail
// lies, so the DIRECTION is asserted here and not merely the refusal.
//
// The fixture is the laundering triple: no proper noun, so what it demonstrates
// is watch membership rather than capitalization.
func TestTheRealPreserveGateRefusesTheComposition(t *testing.T) {
	const (
		originalText = "Yesterday the market was busy and the sellers were loud."
		launderer    = "the market yesterday was busy and the sellers were loud."
		dropper      = "the market was busy and the sellers were loud."
	)
	// The triple's shape is asserted first: both rungs admissible pairwise, the
	// composition not. If `preserve.Check` ever changes, this fails here rather
	// than misattributing the failure to the loop.
	for _, step := range []struct {
		name, from, to string
		want           bool
	}{
		{"first rung", originalText, launderer, true},
		{"second rung", launderer, dropper, true},
		{"the composition", originalText, dropper, false},
	} {
		got, err := preserve.Check(step.from, step.to)
		if err != nil {
			t.Fatalf("%s: Check: %v", step.name, err)
		}
		if got.Preserved != step.want {
			t.Fatalf("%s: preserved=%v, want %v — this fixture no longer demonstrates "+
				"the composition", step.name, got.Preserved, step.want)
		}
	}

	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			originalText: scored(0.90), launderer: scored(0.60), dropper: scored(0.30),
		},
		[]string{launderer, dropper}, passingGate())
	// The gate handed to loopOver is discarded; only the real one below matters.
	// loopOver's signature takes a *fakeGate, which is why it is built at all.
	loop.Gate = realPreserveGate{}

	got, err := loop.Rewrite(context.Background(), rewrite.Segment{Text: originalText, SpanRef: "span-0"})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}

	// The launderer is accepted; the dropper is refused against the ORIGINAL.
	if got.Text != launderer {
		t.Errorf("the outcome carries %q, want the first candidate", got.Text)
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
		// The DIRECTION. An argument-swapped gate refuses identically and
		// records `invented` instead, which is the audit trail lying.
		if len(attempt.PreserveIdentifiers) != 1 ||
			attempt.PreserveIdentifiers[0] != launderedEntityLost {
			t.Errorf("the %s second attempt records %v, want [%s] — a gate with its "+
				"arguments swapped refuses too, and records an invention",
				where, attempt.PreserveIdentifiers, launderedEntityLost)
		}
	}
}

// Measured, not copied: the identifier `preserve.Check` emits for the laundered
// entity loss. Earlier drafts reused a `number:lost` fixture digest from
// elsewhere in the package under an `entity:lost` prefix.
const launderedEntityLost = "preserve-v1:entity:lost:734e476d2f0f911f"

// realPreserveGate runs the production check and passes everything else, so the
// only thing under test is what preserve is asked about.
type realPreserveGate struct{}

func (realPreserveGate) Preserve(original, candidate string) (rewrite.Preservation, error) {
	x, err := preserve.Check(original, candidate)
	return rewrite.Preservation{Preserved: x.Preserved, Identifiers: x.Identifiers()}, err
}

func (realPreserveGate) Tells(current, candidate string) (rewrite.TellsVerdict, error) {
	return rewrite.TellsVerdict{Comparison: -1, Comparable: true}, nil
}

func (realPreserveGate) Language(original, current, candidate string) (rewrite.LanguageVerdict, error) {
	return rewrite.LanguageVerdict{}, nil
}
