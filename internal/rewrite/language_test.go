package rewrite_test

// #91's acceptance policy: a candidate that introduces a SCRIPT the current
// paragraph does not use is refused.
//
// Script introduction, precisely — not a language change. The two are not the
// same in either direction: introducing a script does not establish that the
// language changed, and reusing only the current text's scripts does not
// establish that it did not. A Japanese paragraph rewritten into Chinese using
// only already-present Han passes this gate untouched. The rejection code is
// `language` because DESIGN and the store already use that word for this gate,
// but what is measured is scripts.
//
// The measurement landed separately as `text.Scripts` and nothing consumed it,
// so the bug in #91 was still live: a candidate 40% of which was Han was
// accepted with `rewrite ok`, exit 0 and `claim=closer-by-distance`.
//
// # Why ANY introduction, and not a share
//
// #91's candidate was 18.6% Han and a single foreign name is around 2%, so a
// threshold near 5% would separate them. It is not used, for two reasons.
//
// The first is that the number would have no derivation. This project refused
// to invent one for the paragraph floor — #92 records ten as a "declared
// interim bound" with a statable rationale and `ParagraphFloorDerived` left
// false — and a share here would have less justification than that, not more.
//
// The second is the asymmetry. A refusal leaves the paragraph reported
// unchanged, which is an ordinary outcome for a bounded hill climb; an
// acceptance puts prose in the user's file. Those are not comparable costs.
//
// That said, the refusal cost is understated by calling it "one more attempt".
// Where the policy is wrong it is wrong for EVERY attempt on that paragraph, so
// the loss is systematic rather than incidental: such a writer gets no useful
// rewriting at all, not a slower path to one.
//
// # What this is wrong about, recorded rather than hidden
//
// Japanese. Measured in internal/text: `犬が好きです` is Han and Hiragana, and
// `イヌが好きです` adds Katakana — an ordinary Japanese rewrite that this policy
// refuses. Such a rewrite CAN fail every attempt on a paragraph, leaving it
// unchanged; whether it does depends on what the provider returns, so this is a
// risk of systematic loss rather than a certainty of it. The reason is at least
// legible as `reason=language`.
//
// #94 is where that gets fixed properly, by making a language a register. This
// slice deliberately does not attempt it.
//
// # Where it sits in the rejection order
//
// Immediately after preserve, because both ask whether a candidate is
// admissible at all rather than whether it is better.
//
// The dangerous behaviour is BYPASSING the check when the distance improved,
// not computing the improvement first: a gate consulted after the comparison
// still prevents acceptance, as long as it is consulted. What must not happen
// is a candidate winning on distance and never being asked. Both orderings are
// pinned below — language before the distance rejection, and language reached
// at all on an improving candidate.

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// languageLoop builds a loop whose single candidate strictly IMPROVES the
// distance, so acceptance would follow from distance alone and a language
// refusal has to be the thing that stops it.
//
// That mirrors #91: its candidate improved from 1.6570 to 0.8289, which is
// exactly why it was accepted.
func languageLoop(t *testing.T, gate *fakeGate) rewrite.Loop {
	t.Helper()
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)
	return loop
}

// banded is a report whose segment lands in a chosen band. `scored` always
// produces `drifting`, so every band but that one was absent from these
// fixtures, and a record that carried the wrong one compared equal.
func banded(band eval.Band, distance float64) score.Report {
	out := scored(distance)
	out.Segments[0].Band = eval.BandOutcome{Band: band, Defined: true, Distance: distance}
	return out
}

// inRangeAt is the band a rewrite is trying to reach.
func inRangeAt(distance float64) score.Report { return banded(eval.BandInRange, distance) }

// A candidate that introduces a script is refused, and the reason says so.
//
// ANY script, not Han. #91's incident was Han, and a policy written as
// `slices.Contains(Introduced, "Han")` passed the whole package while accepting
// Cyrillic- and Katakana-only introductions — the substitutions a Russian or
// Japanese writer would actually receive. The policy is about introduction, not
// about a script.
func TestACandidateThatIntroducesAScriptIsRefused(t *testing.T) {
	for _, introduced := range [][]string{
		{"Han"},      // the reported incident
		{"Cyrillic"}, // and each of these alone must refuse identically
		{"Katakana"},
		{"Greek"},
		{"Arabic", "Devanagari"},
	} {
		// And under BOTH provider routings. Every fixture here inherited
		// `LocalOnly: true`, so `l.Options.LocalOnly && introduced` passed the
		// package while accepting every introduction from a remote provider —
		// where the substitutions come from a model the writer has even less
		// visibility into. Routing decides where the prose is generated, not
		// which acceptance guards apply to it.
		for _, localOnly := range []bool{true, false} {
			gate := passingGate()
			gate.fallback.introduced = introduced
			loop := languageLoop(t, gate)
			loop.Options.LocalOnly = localOnly

			got := run(t, loop)

			if got.Changed {
				t.Fatalf("local-only %v: a candidate that introduced %v was accepted: text %q",
					localOnly, introduced, got.Text)
			}
			if len(got.Attempts) == 0 {
				t.Fatalf("local-only %v: %v: no attempt was recorded", localOnly, introduced)
			}
			if got.Attempts[0].Rejection != rewrite.RejectionLanguage {
				t.Errorf("local-only %v: %v: rejected as %q, want %q",
					localOnly, introduced, got.Attempts[0].Rejection, rewrite.RejectionLanguage)
			}
		}

		gate := passingGate()
		gate.fallback.introduced = introduced

		got := run(t, languageLoop(t, gate))

		if got.Changed {
			t.Fatalf("a candidate that introduced %v was accepted: text %q",
				introduced, got.Text)
		}
		// The TEXT, not only the flag. Assigning the rejected candidate to
		// `Outcome.Text` while leaving `Changed` false survived the whole
		// package, and the text is what reaches the user's file.
		if got.Text != original {
			t.Errorf("%v: the outcome carries %q, want the original untouched",
				introduced, got.Text)
		}
		if len(got.Attempts) == 0 {
			t.Fatalf("%v: no attempt was recorded", introduced)
		}
		for i, attempt := range got.Attempts {
			if attempt.Rejection != rewrite.RejectionLanguage {
				t.Errorf("%v: attempt %d was rejected as %q, want %q",
					introduced, i, attempt.Rejection, rewrite.RejectionLanguage)
			}
			if attempt.Accepted {
				t.Errorf("%v: attempt %d was accepted", introduced, i)
			}
			if !slices.Equal(attempt.IntroducedScripts, introduced) {
				t.Errorf("%v: attempt %d records %v",
					introduced, i, attempt.IntroducedScripts)
			}
		}
	}
}

// A candidate that introduces nothing is not refused for language.
//
// The other half: a policy refusing everything satisfies the test above.
func TestACandidateThatIntroducesNothingIsNotRefusedForLanguage(t *testing.T) {
	// BOTH shapes of "nothing". `text.ScriptSet.Introduced` returns an empty
	// non-nil slice for a candidate whose scripts are all already present, so
	// a policy testing `Introduced != nil` refuses every real candidate while
	// a nil-only fixture passes.
	for _, introduced := range [][]string{nil, {}} {
		gate := passingGate()
		gate.fallback.introduced = introduced

		got := run(t, languageLoop(t, gate))

		if !got.Changed {
			t.Fatalf("a candidate introducing no script (%#v) was not accepted: %+v",
				introduced, got.Attempts)
		}
		for i, attempt := range got.Attempts {
			if attempt.Rejection == rewrite.RejectionLanguage {
				t.Errorf("%#v: attempt %d was refused for language despite introducing nothing",
					introduced, i)
			}
			if len(attempt.IntroducedScripts) != 0 {
				t.Errorf("%#v: attempt %d records %v, want none",
					introduced, i, attempt.IntroducedScripts)
			}
		}
	}
}

// An in-range candidate is refused too.
//
// Every calibrated fixture here lands in `drifting`, so narrowing the refusal
// with `&& candidate.Band != eval.BandInRange` passed the package. That is the
// band a rewrite is aiming AT: the mutation would refuse introductions only
// while they failed to reach the goal, and wave through every one that
// succeeded.
func TestAnInRangeCandidateThatIntroducesAScriptIsRefused(t *testing.T) {
	gate := passingGate()
	gate.fallback.introduced = []string{"Han"}
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: inRangeAt(0.05)},
		[]string{better}, gate)

	got := run(t, loop)

	if got.Changed || got.Text != original {
		t.Fatalf("an in-range introduction was accepted: changed=%v text=%q",
			got.Changed, got.Text)
	}
	if len(got.Attempts) == 0 || len(store.attempts) == 0 {
		t.Fatalf("%d attempts recorded and %d stored", len(got.Attempts), len(store.attempts))
	}
	if got.Attempts[0].CandidateBand != eval.BandInRange {
		t.Fatalf("this fixture must land IN-RANGE or it proves nothing: band %q",
			got.Attempts[0].CandidateBand)
	}
	for where, attempt := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[0], "store": store.attempts[0],
	} {
		if attempt.Rejection != rewrite.RejectionLanguage || attempt.Accepted {
			t.Errorf("the %s attempt is %q/accepted=%v, want %q/false",
				where, attempt.Rejection, attempt.Accepted, rewrite.RejectionLanguage)
		}
		if !slices.Equal(attempt.IntroducedScripts, []string{"Han"}) {
			t.Errorf("the %s attempt records %v, want [Han]",
				where, attempt.IntroducedScripts)
		}
	}
}

// Language refusals consume attempts like any other.
//
// `TestTheCapCountsAttemptsNotAcceptances` refuses on distance, and the retry
// test here supplies only two candidates against a cap of three, so refunding
// the attempt on a language refusal passed the whole package. A provider that
// keeps substituting scripts would then be asked without end — the exact
// failure mode the cap exists for, reachable by the exact gate being added.
//
// Four introducing candidates, a cap of three, and a clean improvement waiting
// behind them that must never be reached.
func TestLanguageRefusalsConsumeTheAttemptCap(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true,
			introduced: []string{"Han"}},
		verdicts: map[string]gateVerdict{
			betterYet: {preserved: true, comparison: -1, comparable: true},
		},
	}
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.30), betterYet: scored(0.20),
		},
		// The fourth call would reach `betterYet`, which introduces nothing.
		[]string{better, better, better, betterYet}, gate)

	got := run(t, loop)

	requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
	if got.Changed || got.Text != original {
		t.Fatalf("the loop ran past its cap and accepted %q", got.Text)
	}
	if len(provider.requests) != 3 {
		t.Errorf("the provider was called %d times against a cap of 3", len(provider.requests))
	}
	if len(got.Attempts) != 3 || len(store.attempts) != 3 {
		t.Errorf("%d attempts recorded and %d stored against a cap of 3",
			len(got.Attempts), len(store.attempts))
	}
	if got.Terminal != rewrite.TerminalExhausted {
		t.Errorf("the loop ended as %q, want %q", got.Terminal, rewrite.TerminalExhausted)
	}
}

// Language outranks a tie in tells.
//
// Every introduction fixture here has tells strictly better or strictly worse,
// so narrowing the refusal with `&& tells.Comparison != 0` passed the package
// and accepted the introducing candidate. A tie is the ordinary case — most
// rewrites move no tell at all — so this is the path a real refusal takes.
func TestLanguageIsReportedWhenTellsTie(t *testing.T) {
	gate := passingGate()
	gate.fallback.comparison = 0
	gate.fallback.introduced = []string{"Han"}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)

	got := run(t, loop)

	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	first := got.Attempts[0]
	if first.TellsComparison != 0 || !first.TellsComparable {
		t.Fatalf("this fixture must TIE on comparable tells or it proves nothing: "+
			"comparison %d comparable %v", first.TellsComparison, first.TellsComparable)
	}
	if got.Changed || got.Text != original {
		t.Fatalf("an introducing candidate was accepted on a tells tie: changed=%v text=%q",
			got.Changed, got.Text)
	}
	if first.Rejection != rewrite.RejectionLanguage {
		t.Errorf("rejected as %q, want %q", first.Rejection, rewrite.RejectionLanguage)
	}
}

// Language is checked even though the distance improves.
//
// #91's candidate improved the distance, which is why it was accepted. A policy
// consulted only after the distance comparison, or only on candidates that
// failed it, would not have caught the incident it exists for. The fixture
// asserts the improvement so it cannot quietly stop being one.
func TestLanguageIsCheckedEvenWhenTheDistanceImproves(t *testing.T) {
	gate := passingGate()
	gate.fallback.introduced = []string{"Han"}

	got := run(t, languageLoop(t, gate))

	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	first := got.Attempts[0]
	if first.CandidateDistance >= first.CurrentDistance {
		t.Fatalf("this fixture must IMPROVE the distance or it proves nothing: "+
			"candidate %.4f against current %.4f", first.CandidateDistance, first.CurrentDistance)
	}
	if got.Changed {
		t.Error("an improving candidate that changed language was accepted")
	}
}

// Preserve is reported first when a candidate fails both.
//
// Not because either is more correct — both refuse — but because one rejection
// code is recorded per attempt, and which one appears must be deterministic
// rather than an accident of evaluation order.
func TestPreserveIsReportedBeforeLanguage(t *testing.T) {
	gate := passingGate()
	gate.fallback.preserved = false
	// A real preserve identifier, because the loop validates them and a
	// not-preserved verdict without one is an operational failure rather than a
	// rejection. I made that exact fixture error earlier in this project.
	gate.fallback.identifiers = []string{"preserve-v1:number:lost:3d4c981bf761d9b8"}
	gate.fallback.introduced = []string{"Han"}

	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)

	got := run(t, loop)

	if len(got.Attempts) != 1 || len(store.attempts) != 1 {
		t.Fatalf("%d attempts recorded and %d stored, want 1 and 1",
			len(got.Attempts), len(store.attempts))
	}
	for where, a := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[0], "store": store.attempts[0],
	} {
		if a.Rejection != rewrite.RejectionNotPreserved {
			t.Errorf("the %s attempt: a candidate failing both was rejected as %q, want %q",
				where, a.Rejection, rewrite.RejectionNotPreserved)
		}
		// The measurement survives losing the precedence contest. Clearing
		// `IntroducedScripts` on any non-language rejection passed the whole
		// package, and it throws away the one record of the substitution — a
		// writer reading `not-preserved` would never learn their paragraph had
		// also changed script.
		if !slices.Equal(a.IntroducedScripts, []string{"Han"}) {
			t.Errorf("the %s attempt records scripts %v, want [Han] — the measurement "+
				"happened whichever rejection is reported", where, a.IntroducedScripts)
		}
	}
}

// And language is reported before the tells comparison, so a language change is
// never reported as a tells problem.
func TestLanguageIsReportedBeforeTells(t *testing.T) {
	gate := passingGate()
	gate.fallback.introduced = []string{"Han"}
	gate.fallback.comparison = 1

	got := run(t, languageLoop(t, gate))

	if code := got.Attempts[0].Rejection; code != rewrite.RejectionLanguage {
		t.Errorf("a candidate failing both was rejected as %q, want %q",
			code, rewrite.RejectionLanguage)
	}
}

// The loop asks about language once per candidate.
//
// Not an efficiency claim: the gate reads the CURRENT text, which advances on
// acceptance under ADR 0006, so asking twice for one candidate would be asking
// two different questions and recording one answer.
func TestLanguageIsConsultedOncePerCandidate(t *testing.T) {
	counting := &countingGate{fakeGate: passingGate()}
	loop := languageLoop(t, counting.fakeGate)
	loop.Gate = counting

	out := run(t, loop)

	if counting.languageCalls != len(out.Attempts) {
		t.Errorf("language was consulted %d times across %d attempts",
			counting.languageCalls, len(out.Attempts))
	}
}

// A gate error is an operational failure, not a refusal.
//
// The rule preserve and tells already follow: an error means the check did not
// happen, and a check that did not happen must never read as one that passed.
// That is #58 and #65's principle applied to this gate.
func TestALanguageGateErrorFailsRatherThanAccepting(t *testing.T) {
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, passingGate())
	loop.Gate = &erroringLanguageGate{fakeGate: passingGate()}

	_, err := loop.Rewrite(context.Background(),
		rewrite.Segment{Text: original, SpanRef: "span-0"})

	if err == nil {
		t.Fatal("a gate error produced no error")
	}
	if !strings.Contains(err.Error(), "language") {
		t.Errorf("the error does not name the gate that failed: %v", err)
	}
}

// Every gate names itself when it fails, not only this one.
//
// The language tests require the error to say "language". Preserve and tells
// return their gate errors UNWRAPPED today, so that requirement reads as an
// unannounced exception to local convention — an implementer following the two
// lines above it would fail a frozen test and reasonably conclude the test was
// wrong. It is not; the convention is. With three gates, an error that names
// none of them tells a caller nothing about which dependency is unavailable.
//
// So the requirement is made uniform here rather than left as a special case.
// This is a deliberate widening of #91's scope, and it is two lines of
// implementation.
func TestEveryGateErrorNamesTheGateThatFailed(t *testing.T) {
	for _, c := range []struct {
		name string
		gate func() rewrite.Gate
	}{
		{"preserve", func() rewrite.Gate {
			return &erroringPreserveGate{fakeGate: passingGate()}
		}},
		{"tells", func() rewrite.Gate {
			return &erroringTellsGate{fakeGate: passingGate()}
		}},
		{"language", func() rewrite.Gate {
			return &erroringLanguageGate{fakeGate: passingGate()}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			loop, _, _, _, _ := loopOver(t,
				map[string]score.Report{original: scored(0.90), better: scored(0.30)},
				[]string{better}, passingGate())
			loop.Gate = c.gate()

			_, err := loop.Rewrite(context.Background(),
				rewrite.Segment{Text: original, SpanRef: "span-0"})

			if err == nil {
				t.Fatal("a gate error produced no error")
			}
			if !strings.Contains(err.Error(), c.name) {
				t.Errorf("the error does not name %q: %v", c.name, err)
			}
		})
	}
}

type erroringPreserveGate struct{ *fakeGate }

func (e *erroringPreserveGate) Preserve(current, candidate string) (rewrite.Preservation, error) {
	return rewrite.Preservation{}, errGateUnavailable
}

type erroringTellsGate struct{ *fakeGate }

func (e *erroringTellsGate) Tells(current, candidate string) (rewrite.TellsVerdict, error) {
	return rewrite.TellsVerdict{}, errGateUnavailable
}

// Deliberately naming no gate, so the loop has to supply the attribution.
var errGateUnavailable = errors.New("dependency unavailable")

// RejectionLanguage is a declared code, so the CLI's closed vocabulary admits
// it and the store can persist it.
func TestRejectionLanguageIsDeclared(t *testing.T) {
	// The LITERAL, because every other assertion in this file compares against
	// the constant itself: renaming it to "tells-worse" satisfied them all
	// while destroying the distinct `reason=language` the issue asks for.
	if string(rewrite.RejectionLanguage) != "language" {
		t.Errorf("RejectionLanguage = %q, want %q", rewrite.RejectionLanguage, "language")
	}
	seen := 0
	for _, code := range rewrite.RejectionCodes() {
		if code == rewrite.RejectionLanguage {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%q appears %d times in RejectionCodes(): %v",
			rewrite.RejectionLanguage, seen, rewrite.RejectionCodes())
	}
}

// The gate is asked about the CURRENT text and the candidate.
//
// A gate handed `(candidate, candidate)` reports no introduction ever, and one
// handed the original segment instead of the current text reads stale state
// after an acceptance — ADR 0006 advances `current` on acceptance. Both passed
// every assertion about the verdict, because the fake ignored its arguments.
//
// The sequence matters: one candidate is accepted, and the next must be
// compared against THAT text rather than the original.
func TestTheLanguageGateSeesTheCurrentTextAndTheCandidate(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
		verdicts: map[string]gateVerdict{
			// Accepted: improves and introduces nothing.
			better: {preserved: true, comparison: -1, comparable: true},
			// Then refused, so the loop ends with `better` as its text.
			betterYet: {preserved: true, comparison: -1, comparable: true,
				introduced: []string{"Han"}},
		},
	}
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	// The terminal reason and the record must agree. Setting `Outcome.Reason`
	// from a language refusal passed the package, and a later acceptance then
	// returned `changed=true` alongside `reason=language` — the per-attempt
	// code escaping into the outcome, which the envelope contract reserves for
	// a loop that was never entered.
	requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
	if len(gate.languageArgs) != 2 {
		t.Fatalf("the gate was asked %d times, want 2: %v",
			len(gate.languageArgs), gate.languageArgs)
	}
	if gate.languageArgs[0] != [3]string{original, original, better} {
		t.Errorf("the first call was %v, want (original, better)", gate.languageArgs[0])
	}
	// The second compares against the ACCEPTED text, not the original.
	if gate.languageArgs[1] != [3]string{original, better, betterYet} {
		t.Errorf("the second call was %v, want (better, betterYet) — current advances "+
			"on acceptance", gate.languageArgs[1])
	}
	// And the accepted text survives the later refusal.
	if !got.Changed || got.Text != better {
		t.Errorf("the outcome is changed=%v text=%q, want the accepted %q",
			got.Changed, got.Text, better)
	}
	// The SECOND attempt's audit, in the outcome and in the store. Every other
	// script assertion reads attempt zero, so restricting the assignment to
	// `index == 0` passed the package and lost the record for exactly the
	// refusals that happen after a successful one.
	if len(got.Attempts) != 2 {
		t.Fatalf("%d attempts were recorded, want 2: %+v", len(got.Attempts), got.Attempts)
	}
	second := got.Attempts[1]
	if second.Rejection != rewrite.RejectionLanguage ||
		!slices.Equal(second.IntroducedScripts, []string{"Han"}) {
		t.Errorf("the second attempt is %q/%v, want %q/[Han]",
			second.Rejection, second.IntroducedScripts, rewrite.RejectionLanguage)
	}
	if len(store.attempts) != 2 {
		t.Fatalf("the store holds %d attempts, want 2", len(store.attempts))
	}
	storedSecond := store.attempts[1]
	if !slices.Equal(storedSecond.IntroducedScripts, []string{"Han"}) {
		t.Errorf("the stored second attempt records %v, want [Han]",
			storedSecond.IntroducedScripts)
	}
	if storedSecond.Rejection != rewrite.RejectionLanguage || storedSecond.Accepted {
		t.Errorf("the stored second attempt is %q/accepted=%v, want %q/false",
			storedSecond.Rejection, storedSecond.Accepted, rewrite.RejectionLanguage)
	}
}

// Language is reported even when the candidate also fails on distance.
//
// Every other language fixture here improves, so moving the distance rejection
// ahead of language passed the package. A candidate that introduces a script
// AND does not improve must still be refused for language, because that is the
// more specific and more actionable reason.
func TestLanguageIsReportedBeforeTheDistanceComparison(t *testing.T) {
	gate := passingGate()
	gate.fallback.introduced = []string{"Han"}
	loop, _, _, _, _ := loopOver(t,
		// The candidate is WORSE, so distance alone would reject it too.
		map[string]score.Report{original: scored(0.30), worse: scored(0.90)},
		[]string{worse}, gate)

	got := run(t, loop)

	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	first := got.Attempts[0]
	if first.CandidateDistance <= first.CurrentDistance {
		t.Fatalf("this fixture must WORSEN the distance or it proves nothing: "+
			"candidate %.4f against current %.4f", first.CandidateDistance, first.CurrentDistance)
	}
	if first.Rejection != rewrite.RejectionLanguage {
		t.Errorf("rejected as %q, want %q", first.Rejection, rewrite.RejectionLanguage)
	}
}

// erroringLanguageGate passes every other check and fails only this one, on
// the call at index `after` and every call thereafter. Zero fails immediately,
// which is what the first-attempt fixtures want.
type erroringLanguageGate struct {
	*fakeGate
	after int
	calls int
}

func (e *erroringLanguageGate) Language(original, current, candidate string) (rewrite.LanguageVerdict, error) {
	e.calls++
	if e.calls <= e.after {
		return e.fakeGate.Language(original, current, candidate)
	}
	return rewrite.LanguageVerdict{}, errLanguageGate
}

// The message deliberately does NOT contain "language": returning the gate's
// error unchanged then satisfied the assertion that the error names the gate
// that failed, because the fixture had supplied the word.
var errLanguageGate = errors.New("dependency unavailable")

// The gate is consulted when the band is unavailable.
//
// Every other fixture here is calibrated, so skipping the gate whenever
// `AllowUncalibrated` is set passed the package. That option weakens the
// acceptance CLAIM — it does not remove a check on what the prose is made of,
// and an uncalibrated corpus is exactly where a writer is least able to notice
// the substitution themselves.
func TestLanguageIsCheckedWhenRewritingUncalibrated(t *testing.T) {
	gate := passingGate()
	gate.fallback.introduced = []string{"Han"}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{
			original: uncalibratedAt(0.90), better: uncalibratedAt(0.30),
		},
		[]string{better}, gate)

	got := run(t, allowing(loop))

	if got.Changed || got.Text != original {
		t.Fatalf("an uncalibrated rewrite accepted an introduction: changed=%v text=%q",
			got.Changed, got.Text)
	}
	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	if got.Attempts[0].Rejection != rewrite.RejectionLanguage {
		t.Errorf("rejected as %q, want %q",
			got.Attempts[0].Rejection, rewrite.RejectionLanguage)
	}
}

// A language refusal costs one attempt, not the loop.
//
// Returning immediately after recording a language rejection passed every
// other test here, because none of them offered a second candidate after a
// refusal. The provider is non-deterministic: the attempt that introduced Han
// says nothing about the next one, and a writer who asked for three attempts
// must get three.
func TestARefusedCandidateDoesNotEndTheLoop(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
		verdicts: map[string]gateVerdict{
			better: {preserved: true, comparison: -1, comparable: true,
				introduced: []string{"Han"}},
			betterYet: {preserved: true, comparison: -1, comparable: true},
		},
	}
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{
			// The second candidate is worse than the refused one and better
			// than the original: a loop that advanced `current` to the prose
			// it just refused would reject this valid improvement, and one
			// that advanced only the SCORE would accept it while reporting the
			// wrong current distance.
			original: scored(0.90), better: scored(0.30), betterYet: scored(0.75),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
	if len(gate.languageArgs) != 2 {
		t.Fatalf("the gate was asked %d times, want 2: %v",
			len(gate.languageArgs), gate.languageArgs)
	}
	if len(got.Attempts) != 2 {
		t.Fatalf("%d attempts were recorded, want 2: %+v", len(got.Attempts), got.Attempts)
	}
	if got.Attempts[1].CurrentDistance != 0.90 {
		t.Errorf("the second attempt measures against current %.4f, want 0.9000 — "+
			"a refusal advances nothing", got.Attempts[1].CurrentDistance)
	}
	// The refusal did not advance `current`, so the second comparison is
	// against the original text.
	if gate.languageArgs[1] != [3]string{original, original, betterYet} {
		t.Errorf("the second call was %v, want (original, betterYet) — a refusal "+
			"leaves current where it was", gate.languageArgs[1])
	}
	if !got.Changed || got.Text != betterYet {
		t.Errorf("the outcome is changed=%v text=%q, want the later %q accepted",
			got.Changed, got.Text, betterYet)
	}
	// The ACCEPTED attempt introduced nothing, and must say so. Hoisting the
	// audit slice out of the loop and updating it only for nonempty
	// introductions passed the package, and recorded [Han] against the clean
	// candidate that was actually taken — the worst possible place for a
	// stale value, since that is the prose the writer keeps.
	if len(store.attempts) != 2 {
		t.Fatalf("the store holds %d attempts, want 2", len(store.attempts))
	}
	for where, accepted := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[1], "store": store.attempts[1],
	} {
		if len(accepted.IntroducedScripts) != 0 {
			t.Errorf("the %s accepted attempt records %v, want none",
				where, accepted.IntroducedScripts)
		}
		// And the DECISION. Hoisting a rejection variable out of the loop and
		// updating it only on a nonempty value stamped `accepted=true` next to
		// `reason=language` on the very candidate that was taken.
		if !accepted.Accepted || accepted.Rejection != rewrite.RejectionNone {
			t.Errorf("the %s accepted attempt is accepted=%v/%q, want true/%q",
				where, accepted.Accepted, accepted.Rejection, rewrite.RejectionNone)
		}
	}
}

// Two introductions in a row are both refused.
//
// Every refusal fixture here is followed by an acceptance or by nothing, so a
// policy applying only when the PRECEDING attempt was not a language refusal
// passed the package: it rejected Han and then accepted an improving Cyrillic
// introduction. There is no reason a second substitution is more acceptable
// than the first, and a provider that produced one is more likely, not less,
// to produce another.
func TestConsecutiveIntroductionsAreBothRefused(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
		verdicts: map[string]gateVerdict{
			better: {preserved: true, comparison: -1, comparable: true,
				introduced: []string{"Han"}},
			// A DIFFERENT script, improving further, so neither the repeat nor
			// the distance can be what stops it.
			betterYet: {preserved: true, comparison: -1, comparable: true,
				introduced: []string{"Cyrillic"}},
		},
	}
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
	if got.Changed || got.Text != original {
		t.Fatalf("a second introduction was accepted: changed=%v text=%q",
			got.Changed, got.Text)
	}
	if len(got.Attempts) != 2 || len(store.attempts) != 2 {
		t.Fatalf("%d attempts recorded and %d stored, want 2 and 2",
			len(got.Attempts), len(store.attempts))
	}
	for i, want := range [][]string{{"Han"}, {"Cyrillic"}} {
		for where, attempt := range map[string]rewrite.Attempt{
			"outcome": got.Attempts[i], "store": store.attempts[i],
		} {
			if attempt.Rejection != rewrite.RejectionLanguage || attempt.Accepted {
				t.Errorf("%s attempt %d is %q/accepted=%v, want %q/false",
					where, i, attempt.Rejection, attempt.Accepted, rewrite.RejectionLanguage)
			}
			if !slices.Equal(attempt.IntroducedScripts, want) {
				t.Errorf("%s attempt %d records %v, want %v",
					where, i, attempt.IntroducedScripts, want)
			}
		}
	}
}

// Language outranks an incomparable tells verdict too.
//
// The ordering test above uses tells-WORSE. Moving the incomparability branch
// ahead of language survived it, and reported `tells-incomparable` for a
// candidate whose actual defect was the script it introduced. Both tells
// outcomes have to sit behind language, so both are pinned.
func TestLanguageIsReportedBeforeIncomparableTells(t *testing.T) {
	gate := passingGate()
	gate.fallback.comparable = false
	gate.fallback.introduced = []string{"Han"}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)

	got := run(t, loop)

	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	first := got.Attempts[0]
	if first.TellsComparable {
		t.Fatal("this fixture must make tells INCOMPARABLE or it proves nothing")
	}
	if first.Rejection != rewrite.RejectionLanguage {
		t.Errorf("rejected as %q, want %q", first.Rejection, rewrite.RejectionLanguage)
	}
}

// A store failure on a language refusal is still a failure.
//
// `TestAStoreFailureIsAnError` accepts its candidate, so ignoring the store
// error when the rejection is `language` passed the whole package: the loop
// returned a clean success having failed to persist the only record that the
// substitution happened. A refusal nobody can read afterwards is the case where
// retention matters most, since the prose itself is discarded.
func TestAStoreFailureOnALanguageRefusalIsAnError(t *testing.T) {
	gate := passingGate()
	gate.fallback.introduced = []string{"Han"}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)
	loop.Store = failingStore{}

	if _, err := loop.Rewrite(context.Background(),
		rewrite.Segment{Text: original, SpanRef: "span-0"}); err == nil {
		t.Error("a store failure on a language refusal was swallowed")
	}
}

// The recorded scripts are the loop's own, not the gate's buffer.
//
// Every fixture here hands the gate's verdicts separate backing slices, so
// assigning `language.Introduced` straight onto the attempt passed the package.
// A gate that reuses one buffer across calls — which a real one computing into
// a scratch slice would — then rewrites the evidence of an ALREADY RECORDED
// refusal when the next candidate is measured. The store holds the aliased
// slice too, so both sinks change under it.
func TestTheRecordedScriptsSurviveTheGateReusingItsBuffer(t *testing.T) {
	gate := &reusingLanguageGate{
		fakeGate: passingGate(),
		scripts:  [][]string{{"Han"}, {"Cyrillic"}},
	}
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, passingGate())
	loop.Gate = gate

	got := run(t, loop)

	if len(got.Attempts) != 2 || len(store.attempts) != 2 {
		t.Fatalf("%d attempts recorded and %d stored, want 2 and 2",
			len(got.Attempts), len(store.attempts))
	}
	for where, attempts := range map[string][]rewrite.Attempt{
		"outcome": got.Attempts, "store": store.attempts,
	} {
		for i, want := range [][]string{{"Han"}, {"Cyrillic"}} {
			if !slices.Equal(attempts[i].IntroducedScripts, want) {
				t.Errorf("the %s attempt %d records %v, want %v — the gate's buffer "+
					"was reused after the attempt was recorded",
					where, i, attempts[i].IntroducedScripts, want)
			}
		}
	}
}

// reusingLanguageGate answers from ONE backing slice, overwriting it in place
// on each call, the way a gate computing into a scratch buffer would.
type reusingLanguageGate struct {
	*fakeGate
	scripts [][]string
	buffer  []string
	calls   int
}

func (r *reusingLanguageGate) Language(original, current, candidate string) (rewrite.LanguageVerdict, error) {
	r.fakeGate.Language(original, current, candidate)
	want := r.scripts[min(r.calls, len(r.scripts)-1)]
	r.calls++
	r.buffer = append(r.buffer[:0], want...)
	return rewrite.LanguageVerdict{Introduced: r.buffer}, nil
}

// A gate failure after an acceptance is still a failure.
//
// Both error fixtures fail on attempt zero, so `err != nil && !outcome.Changed`
// passed the whole package: once anything had been accepted, a gate that could
// not answer was treated as one that said yes, and the unchecked candidate was
// accepted and persisted. An accepted first attempt is not evidence about the
// second — under ADR 0006 the second is measured against different text.
func TestALanguageGateErrorAfterAnAcceptanceStillFails(t *testing.T) {
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, passingGate())
	// The first candidate is answered; the second is not.
	loop.Gate = &erroringLanguageGate{fakeGate: passingGate(), after: 1}

	_, err := loop.Rewrite(context.Background(),
		rewrite.Segment{Text: original, SpanRef: "span-0"})

	if err == nil {
		t.Fatal("a gate error after an acceptance produced no error")
	}
	if !strings.Contains(err.Error(), "language") {
		t.Errorf("the error does not name the gate that failed: %v", err)
	}
	// Only the attempt that actually completed. A record for the second would
	// document a decision the gate never made.
	if len(store.attempts) != 1 {
		t.Fatalf("the store holds %d attempts, want only the completed first: %+v",
			len(store.attempts), store.attempts)
	}
	if !store.attempts[0].Accepted || store.attempts[0].CandidateHash != hashOf(better) {
		t.Errorf("the stored attempt is accepted=%v for candidate %q, want the accepted first",
			store.attempts[0].Accepted, store.attempts[0].CandidateHash)
	}
}

// A gate failure records no decision.
//
// Recording a `language` rejection and then returning the error survived,
// which writes a durable refusal the gate never issued — the check did not
// run, so there is nothing to persist about it. An operational failure and a
// refusal are different outcomes and the store must not conflate them.
func TestALanguageGateErrorRecordsNoAttempt(t *testing.T) {
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, passingGate())
	loop.Gate = &erroringLanguageGate{fakeGate: passingGate()}

	_, err := loop.Rewrite(context.Background(),
		rewrite.Segment{Text: original, SpanRef: "span-0"})

	if err == nil {
		t.Fatal("a gate error produced no error")
	}
	if len(store.attempts) != 0 {
		t.Errorf("the store recorded %d attempt(s) for a check that never ran: %+v",
			len(store.attempts), store.attempts)
	}
}

// ---------------------------------------------------------------------------
// The record, in full, on every path
// ---------------------------------------------------------------------------

// normalized nils out empty slices. A nil and an empty non-nil slice both mean
// "none" here, and the loop is not required to choose between them.
func normalized(a rewrite.Attempt) rewrite.Attempt {
	if len(a.PreserveIdentifiers) == 0 {
		a.PreserveIdentifiers = nil
	}
	if len(a.IntroducedScripts) == 0 {
		a.IntroducedScripts = nil
	}
	if len(a.OvergrownScripts) == 0 {
		a.OvergrownScripts = nil
	}
	return a
}

// identities fills the three fixed fields every attempt carries, so the
// expectations below state only what differs between paths.
func identities(a rewrite.Attempt) rewrite.Attempt {
	a.SpanRef = "span-0"
	a.ProfileID = "profile-under-test"
	a.ProviderID = "provider-under-test"
	a.InvocationID = "invocation-under-test"
	return a
}

// hashOf is the loop's own hash, not arithmetic repeated here.
func hashOf(text string) string { return identity.HashBytes([]byte(text)) }

// Every field of the record, on each distinct decision path, in both sinks.
//
// Nine rounds of review found ten defects in this record and none in the
// refusal, every one of them a field that some test happened not to read:
// swapped hashes, a swapped provider and invocation, a current band overwritten
// by the candidate's, preserve evidence dropped when a script was also
// introduced, scripts dropped when preserve won the precedence contest. Field
// assertions scattered across behavioural tests cannot close that, because the
// hole is always the field nobody looked at.
//
// So the expectation here is a COMPLETE `Attempt`, written independently of the
// loop, compared whole. The values are chosen to be mutually distinguishable —
// distinct distances, distinct bands, distinct texts on each side — so that a
// swap or a stale carry-over cannot compare equal.
func TestTheAttemptRecordIsCompleteOnEveryLanguagePath(t *testing.T) {
	passing := rewrite.Attempt{Preserved: true, TellsComparable: true, TellsComparison: -1}

	cases := []struct {
		name       string
		gate       *fakeGate
		reports    map[string]score.Report
		candidates []string
		allow      bool
		want       []rewrite.Attempt
	}{
		{
			// A refusal on the path that matters most: the candidate REACHED
			// the target band and is still refused, and the record must not
			// claim the original was already there.
			name: "refused, drifting into in-range, tells worse, two scripts",
			gate: func() *fakeGate {
				g := passingGate()
				// Tells is WORSE, and two scripts are introduced. Every table
				// row began with comparable tells at -1 and a single script,
				// so zeroing a positive comparison when language wins, and
				// truncating the stored scripts to their first element, both
				// compared equal.
				g.fallback.comparison = 1
				g.fallback.introduced = []string{"Cyrillic", "Han"}
				return g
			}(),
			reports: map[string]score.Report{
				original: scored(0.90), better: inRangeAt(0.05),
			},
			candidates: []string{better},
			want: []rewrite.Attempt{identities(func() rewrite.Attempt {
				a := passing
				a.Index = 0
				a.CurrentHash, a.CandidateHash = hashOf(original), hashOf(better)
				a.CurrentDistance, a.CandidateDistance = 0.90, 0.05
				a.CurrentBand, a.CandidateBand = eval.BandDrifting, eval.BandInRange
				a.TellsComparison = 1
				a.Rejection = rewrite.RejectionLanguage
				a.IntroducedScripts = []string{"Cyrillic", "Han"}
				return a
			}())},
		},
		{
			// Two gates fail at once. Precedence picks ONE reason to report;
			// the evidence from both is measured and must both survive.
			name: "refused, preserve and language together",
			gate: func() *fakeGate {
				g := passingGate()
				g.fallback.preserved = false
				g.fallback.identifiers = []string{"preserve-v1:number:lost:3d4c981bf761d9b8"}
				g.fallback.introduced = []string{"Han"}
				return g
			}(),
			reports: map[string]score.Report{
				original: scored(0.90), better: inRangeAt(0.05),
			},
			candidates: []string{better},
			want: []rewrite.Attempt{identities(func() rewrite.Attempt {
				a := passing
				a.Preserved = false
				a.PreserveIdentifiers = []string{"preserve-v1:number:lost:3d4c981bf761d9b8"}
				a.CurrentHash, a.CandidateHash = hashOf(original), hashOf(better)
				a.CurrentDistance, a.CandidateDistance = 0.90, 0.05
				a.CurrentBand, a.CandidateBand = eval.BandDrifting, eval.BandInRange
				a.Rejection = rewrite.RejectionNotPreserved
				a.IntroducedScripts = []string{"Han"}
				return a
			}())},
		},
		{
			// An acceptance advances the current side. The second record's
			// current fields are the first candidate's, and its own audit is
			// clean of the first attempt's values.
			name: "accepted, then refused",
			gate: &fakeGate{
				fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
				verdicts: map[string]gateVerdict{
					betterYet: {preserved: true, comparison: -1, comparable: true,
						introduced: []string{"Cyrillic"}},
				},
			},
			reports: map[string]score.Report{
				// Three DISTINCT bands, so a record that froze the current
				// band at the original's cannot compare equal.
				original: banded(eval.BandNotYou, 1.60), better: scored(0.60),
				betterYet: inRangeAt(0.05),
			},
			candidates: []string{better, betterYet},
			want: []rewrite.Attempt{
				identities(func() rewrite.Attempt {
					a := passing
					a.Index = 0
					a.CurrentHash, a.CandidateHash = hashOf(original), hashOf(better)
					a.CurrentDistance, a.CandidateDistance = 1.60, 0.60
					a.CurrentBand, a.CandidateBand = eval.BandNotYou, eval.BandDrifting
					a.Accepted = true
					return a
				}()),
				identities(func() rewrite.Attempt {
					a := passing
					a.Index = 1
					a.CurrentHash, a.CandidateHash = hashOf(better), hashOf(betterYet)
					a.CurrentDistance, a.CandidateDistance = 0.60, 0.05
					a.CurrentBand, a.CandidateBand = eval.BandDrifting, eval.BandInRange
					a.Rejection = rewrite.RejectionLanguage
					a.IntroducedScripts = []string{"Cyrillic"}
					return a
				}()),
			},
		},
		{
			// A refusal advances nothing. The second record's current fields
			// are the ORIGINAL's, and the accepted record carries no trace of
			// the refused attempt's scripts or reason.
			name: "refused, then accepted",
			gate: &fakeGate{
				fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
				verdicts: map[string]gateVerdict{
					better: {preserved: true, comparison: -1, comparable: true,
						introduced: []string{"Han"}},
				},
			},
			reports: map[string]score.Report{
				original: scored(0.90), better: inRangeAt(0.05), betterYet: scored(0.75),
			},
			candidates: []string{better, betterYet},
			want: []rewrite.Attempt{
				identities(func() rewrite.Attempt {
					a := passing
					a.Index = 0
					a.CurrentHash, a.CandidateHash = hashOf(original), hashOf(better)
					a.CurrentDistance, a.CandidateDistance = 0.90, 0.05
					a.CurrentBand, a.CandidateBand = eval.BandDrifting, eval.BandInRange
					a.Rejection = rewrite.RejectionLanguage
					a.IntroducedScripts = []string{"Han"}
					return a
				}()),
				identities(func() rewrite.Attempt {
					a := passing
					a.Index = 1
					a.CurrentHash, a.CandidateHash = hashOf(original), hashOf(betterYet)
					a.CurrentDistance, a.CandidateDistance = 0.90, 0.75
					a.CurrentBand, a.CandidateBand = eval.BandDrifting, eval.BandDrifting
					a.Accepted = true
					return a
				}()),
			},
		},
		{
			// #107's path, so the new audit field is compared by a whole-record
			// DeepEqual on at least one row rather than passing by being zero
			// everywhere. Growth is reported, and nothing was introduced.
			name: "refused, a script grew out of proportion",
			gate: func() *fakeGate {
				g := passingGate()
				g.fallback.overgrown = []string{"Cyrillic", "Han"}
				return g
			}(),
			reports: map[string]score.Report{
				original: scored(0.90), better: inRangeAt(0.05),
			},
			candidates: []string{better},
			want: []rewrite.Attempt{identities(func() rewrite.Attempt {
				a := passing
				a.CurrentHash, a.CandidateHash = hashOf(original), hashOf(better)
				a.CurrentDistance, a.CandidateDistance = 0.90, 0.05
				a.CurrentBand, a.CandidateBand = eval.BandDrifting, eval.BandInRange
				a.Rejection = rewrite.RejectionLanguageGrowth
				a.OvergrownScripts = []string{"Cyrillic", "Han"}
				return a
			}())},
		},
		{
			// No bands at all. The uncalibrated path records the distances it
			// has and leaves the bands empty rather than inventing one.
			name: "refused, uncalibrated",
			gate: func() *fakeGate {
				g := passingGate()
				// INCOMPARABLE tells, the other verdict no row carried:
				// forcing the stored `TellsComparable` to true on language
				// refusals compared equal while every row had it true anyway.
				g.fallback.comparable = false
				g.fallback.comparison = 0
				g.fallback.introduced = []string{"Katakana"}
				return g
			}(),
			reports: map[string]score.Report{
				original: uncalibratedAt(0.90), better: uncalibratedAt(0.05),
			},
			candidates: []string{better},
			allow:      true,
			want: []rewrite.Attempt{identities(func() rewrite.Attempt {
				a := passing
				a.CurrentHash, a.CandidateHash = hashOf(original), hashOf(better)
				a.CurrentDistance, a.CandidateDistance = 0.90, 0.05
				a.TellsComparable, a.TellsComparison = false, 0
				a.Rejection = rewrite.RejectionLanguage
				a.IntroducedScripts = []string{"Katakana"}
				return a
			}())},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loop, _, _, _, store := loopOver(t, c.reports, c.candidates, c.gate)
			if c.allow {
				loop = allowing(loop)
			}

			got := run(t, loop)

			for where, attempts := range map[string][]rewrite.Attempt{
				"outcome": got.Attempts, "store": store.attempts,
			} {
				if len(attempts) != len(c.want) {
					t.Errorf("the %s holds %d attempts, want %d: %+v",
						where, len(attempts), len(c.want), attempts)
					continue
				}
				for i, want := range c.want {
					if diff := normalized(attempts[i]); !reflect.DeepEqual(diff, normalized(want)) {
						t.Errorf("the %s attempt %d is\n\t%+v\nwant\n\t%+v", where, i, diff, want)
					}
				}
			}
		})
	}
}

// A new audit field needs a decision, not a default.
//
// The table above compares whole records, so it covers every field that exists
// when it is written — and silently covers a new one with whatever the loop
// happens to put there. This fails when the struct grows, which is the moment
// to decide what each path should record.
func TestEveryAuditFieldIsSpecifiedByTheRecordTable(t *testing.T) {
	const specified = 19
	if n := reflect.TypeOf(rewrite.Attempt{}).NumField(); n != specified {
		t.Errorf("rewrite.Attempt has %d fields and the record table specifies %d; "+
			"add the new field to TestTheAttemptRecordIsCompleteOnEveryLanguagePath "+
			"on every path before raising this count", n, specified)
	}
}
