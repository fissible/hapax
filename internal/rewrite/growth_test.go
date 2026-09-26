package rewrite_test

// #107. #91's gate refuses a script the CURRENT text does not use, so one
// pre-existing character disarms it: a paragraph containing a single Han
// letter can come back largely Han, introducing nothing.
//
// The rule, measured in `internal/text`: a script already at or above
// `ScriptCeiling` in the ORIGINAL is established and unconstrained; every other
// script, present or absent, may not exceed that share of the candidate.
//
// The number is declared rather than derived, and `ScriptCeilingDerived` says
// so. Three constant-free designs were measured and discarded first — the last
// two died on the same inequality, refusing every lengthening rewrite of a
// multi-script paragraph.
//
// The half that belongs HERE, rather than in the measurement, is the ANCHOR.
// Establishment is judged against the paragraph the loop started from, not the
// advancing `current`, and the ceiling makes the ladder sharper rather than
// milder. A candidate sitting EXACTLY on the ceiling is admissible, because the
// bound is strict. Anchored on `current`, that candidate is then established —
// its share is at the ceiling — and the next candidate is unconstrained:
//
//	original            Han 0%      not established
//	attempt 1  Han 1/20 = 5.0%      admissible, the bound is strict
//	attempt 2  Han 10/10 = 100%     admissible against attempt 1, which is
//	                                 established at exactly the ceiling
//
// Two accepted attempts and the paragraph is entirely Han. The live store shows
// 2 of 7 recorded invocations accepted twice, including #91's own, so this is a
// reachable sequence rather than a hypothetical one. A fixed anchor closes it,
// because the original never becomes established.
//
// That makes `Language`'s signature `(original, current, candidate)`: two
// anchors, one call, because both facts are measured from the same candidate
// and a second gate method would need its own "consulted once" invariant.
//
// The two guards are COMPLEMENTARY at a non-zero ceiling, not nested. #91
// refuses any introduction however small; this refuses any crossing of the
// ceiling whether the script was present or not. Measured: one Han character
// added to a 69-letter paragraph is 1.4% and this rule does not see it, while
// #91 does. They coincide only at a ceiling of zero, which `internal/text`
// asserts. When both fire, `language` is reported — a script that was never
// there is the more specific claim — and both records are populated either way.

import (
	"context"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// growthLoop builds a loop whose single candidate strictly IMPROVES the
// distance, so acceptance would follow from distance alone and the growth
// refusal has to be the thing that stops it.
func growthLoop(t *testing.T, gate *fakeGate) rewrite.Loop {
	t.Helper()
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)
	return loop
}

// ---------------------------------------------------------------------------
// The refusal
// ---------------------------------------------------------------------------

// A candidate whose script grew out of proportion is refused, and the reason
// says so.
func TestACandidateThatOvergrowsAScriptIsRefused(t *testing.T) {
	for _, overgrown := range [][]string{
		{"Han"},
		{"Cyrillic"},
		{"Katakana"},
		{"Arabic", "Devanagari"},
	} {
		// And under BOTH provider routings, because gating an acceptance check
		// on `LocalOnly` passed the whole package in #91's review.
		for _, localOnly := range []bool{true, false} {
			gate := passingGate()
			gate.fallback.overgrown = overgrown
			loop := growthLoop(t, gate)
			loop.Options.LocalOnly = localOnly

			got := run(t, loop)

			if got.Changed {
				t.Fatalf("local-only %v: a candidate that overgrew %v was accepted: text %q",
					localOnly, overgrown, got.Text)
			}
			if got.Text != original {
				t.Errorf("local-only %v: %v: the outcome carries %q, want the original",
					localOnly, overgrown, got.Text)
			}
			if len(got.Attempts) == 0 {
				t.Fatalf("local-only %v: %v: no attempt was recorded", localOnly, overgrown)
			}
			for i, attempt := range got.Attempts {
				if attempt.Rejection != rewrite.RejectionLanguageGrowth {
					t.Errorf("local-only %v: %v: attempt %d rejected as %q, want %q",
						localOnly, overgrown, i, attempt.Rejection, rewrite.RejectionLanguageGrowth)
				}
				if attempt.Accepted {
					t.Errorf("local-only %v: %v: attempt %d was accepted", localOnly, overgrown, i)
				}
				if !equalStrings(attempt.OvergrownScripts, overgrown) {
					t.Errorf("local-only %v: %v: attempt %d records %v",
						localOnly, overgrown, i, attempt.OvergrownScripts)
				}
			}
		}
	}
}

// A candidate that grows nothing is not refused for growth.
//
// The other half: a policy refusing everything satisfies the test above. Both
// shapes of "nothing", because `Overgrown` returns an empty non-nil slice on
// the ordinary path and `!= nil` would refuse every real candidate.
func TestACandidateThatOvergrowsNothingIsNotRefusedForGrowth(t *testing.T) {
	for _, overgrown := range [][]string{nil, {}} {
		gate := passingGate()
		gate.fallback.overgrown = overgrown

		got := run(t, growthLoop(t, gate))

		if !got.Changed {
			t.Fatalf("%#v: a candidate growing nothing was not accepted: %+v",
				overgrown, got.Attempts)
		}
		for i, attempt := range got.Attempts {
			if attempt.Rejection == rewrite.RejectionLanguageGrowth {
				t.Errorf("%#v: attempt %d refused for growth despite growing nothing",
					overgrown, i)
			}
			if len(attempt.OvergrownScripts) != 0 {
				t.Errorf("%#v: attempt %d records %v, want none",
					overgrown, i, attempt.OvergrownScripts)
			}
		}
	}
}

// Growth is checked even though the distance improves.
//
// #91's candidate improved, which is why it was accepted. A check consulted
// only on candidates that already failed the comparison would not have caught
// the incident it exists for.
func TestGrowthIsCheckedEvenWhenTheDistanceImproves(t *testing.T) {
	gate := passingGate()
	gate.fallback.overgrown = []string{"Han"}

	got := run(t, growthLoop(t, gate))

	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	first := got.Attempts[0]
	if first.CandidateDistance >= first.CurrentDistance {
		t.Fatalf("this fixture must IMPROVE the distance or it proves nothing: "+
			"candidate %.4f against current %.4f", first.CandidateDistance, first.CurrentDistance)
	}
	if got.Changed {
		t.Error("an improving candidate whose script overgrew was accepted")
	}
}

// Growth is checked when the band is unavailable too.
//
// `AllowUncalibrated` weakens the acceptance CLAIM. It does not remove a check
// on what the prose is made of, and an uncalibrated corpus is where a writer is
// least able to notice a substitution themselves.
func TestGrowthIsCheckedWhenRewritingUncalibrated(t *testing.T) {
	gate := passingGate()
	gate.fallback.overgrown = []string{"Han"}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{original: uncalibratedAt(0.90), better: uncalibratedAt(0.30)},
		[]string{better}, gate)

	got := run(t, allowing(loop))

	if got.Changed || got.Text != original {
		t.Fatalf("an uncalibrated rewrite accepted overgrowth: changed=%v text=%q",
			got.Changed, got.Text)
	}
	if got.Attempts[0].Rejection != rewrite.RejectionLanguageGrowth {
		t.Errorf("rejected as %q, want %q",
			got.Attempts[0].Rejection, rewrite.RejectionLanguageGrowth)
	}
}

// ---------------------------------------------------------------------------
// The anchor
// ---------------------------------------------------------------------------

// The gate is asked about the ORIGINAL, the current text, and the candidate.
//
// This is the whole reason the signature has three arguments. `current` advances
// on acceptance under ADR 0006, and the growth bound must not: a gate handed
// `(current, current, candidate)` measures growth against whatever was last
// accepted, which is the ratchet. Measured in `internal/text`: a long candidate
// carrying six Han letters is admissible against a 22-letter original, and a
// short candidate carrying the same six is admissible against THAT long one —
// but refused against the original, which is the anchor that closes it.
//
// So the first argument must be the segment's own text on every call, however
// far `current` has advanced.
func TestTheGateIsAskedAboutTheOriginalOnEveryAttempt(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
		verdicts: map[string]gateVerdict{
			better:    {preserved: true, comparison: -1, comparable: true},
			betterYet: {preserved: true, comparison: -1, comparable: true},
		},
	}
	loop, _, _, _, _ := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	if len(gate.languageArgs) != 2 {
		t.Fatalf("the gate was asked %d times, want 2: %v",
			len(gate.languageArgs), gate.languageArgs)
	}
	// Both accepted, so `current` advanced between the calls.
	if !got.Changed || got.Text != betterYet {
		t.Fatalf("the fixture must accept BOTH candidates or the anchor is not "+
			"under test: changed=%v text=%q", got.Changed, got.Text)
	}
	if gate.languageArgs[0] != [3]string{original, original, better} {
		t.Errorf("the first call was %v, want (original, original, better)",
			gate.languageArgs[0])
	}
	// The anchor is unchanged; only the middle argument advanced.
	if gate.languageArgs[1] != [3]string{original, better, betterYet} {
		t.Errorf("the second call was %v, want (original, better, betterYet) — the "+
			"first argument is the ANCHOR and must not advance", gate.languageArgs[1])
	}
}

// The anchor does not move after a refusal either.
func TestTheAnchorIsTheOriginalAfterARefusal(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
		verdicts: map[string]gateVerdict{
			better: {preserved: true, comparison: -1, comparable: true,
				overgrown: []string{"Han"}},
			betterYet: {preserved: true, comparison: -1, comparable: true},
		},
	}
	loop, _, _, provider, _ := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.30), betterYet: scored(0.75),
		},
		[]string{better, betterYet}, gate)

	got := run(t, loop)

	requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
	if len(gate.languageArgs) != 2 {
		t.Fatalf("the gate was asked %d times, want 2: %v",
			len(gate.languageArgs), gate.languageArgs)
	}
	for i, args := range gate.languageArgs {
		if args[0] != original {
			t.Errorf("call %d anchored on %q, want the original", i, args[0])
		}
	}
	// A refusal advances nothing, so the second call's current is the original.
	if gate.languageArgs[1] != [3]string{original, original, betterYet} {
		t.Errorf("the second call was %v, want (original, original, betterYet)",
			gate.languageArgs[1])
	}
	if !got.Changed || got.Text != betterYet {
		t.Errorf("the outcome is changed=%v text=%q, want the later candidate accepted",
			got.Changed, got.Text)
	}
}

// ---------------------------------------------------------------------------
// Precedence
// ---------------------------------------------------------------------------

// Introduction is reported before growth, and both are recorded.
//
// A script can be both: introduced AND above the ceiling. `language` wins
// because it is the more specific claim — the script was never there at all —
// and the record carries both sets, so choosing a reason loses no evidence.
func TestIntroductionIsReportedBeforeGrowth(t *testing.T) {
	gate := passingGate()
	gate.fallback.introduced = []string{"Greek"}
	gate.fallback.overgrown = []string{"Greek", "Han"}
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)

	got := run(t, loop)

	if len(got.Attempts) == 0 || len(store.attempts) == 0 {
		t.Fatalf("%d attempts recorded and %d stored", len(got.Attempts), len(store.attempts))
	}
	for where, attempt := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[0], "store": store.attempts[0],
	} {
		if attempt.Rejection != rewrite.RejectionLanguage {
			t.Errorf("the %s attempt was rejected as %q, want %q",
				where, attempt.Rejection, rewrite.RejectionLanguage)
		}
		// BOTH measurements survive the precedence contest, the same way a
		// preserve refusal keeps its introduced scripts.
		if !equalStrings(attempt.IntroducedScripts, []string{"Greek"}) {
			t.Errorf("the %s attempt records introduced %v, want [Greek]",
				where, attempt.IntroducedScripts)
		}
		if !equalStrings(attempt.OvergrownScripts, []string{"Greek", "Han"}) {
			t.Errorf("the %s attempt records overgrown %v, want [Greek Han]",
				where, attempt.OvergrownScripts)
		}
	}
}

// Growth outranks both tells outcomes and the distance comparison.
//
// Same shape as #91's: a candidate failing growth AND something weaker must
// report growth, because it is the more specific and more actionable reason.
func TestGrowthIsReportedBeforeTellsAndDistance(t *testing.T) {
	for _, c := range []struct {
		name    string
		mutate  func(*fakeGate)
		reports map[string]score.Report
	}{
		{
			name:    "tells worse",
			mutate:  func(g *fakeGate) { g.fallback.comparison = 1 },
			reports: map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		},
		{
			name:    "tells incomparable",
			mutate:  func(g *fakeGate) { g.fallback.comparable = false },
			reports: map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		},
		{
			name:    "not improved",
			mutate:  func(g *fakeGate) {},
			reports: map[string]score.Report{original: scored(0.30), better: scored(0.90)},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			gate := passingGate()
			gate.fallback.overgrown = []string{"Han"}
			c.mutate(gate)
			loop, _, _, _, _ := loopOver(t, c.reports, []string{better}, gate)

			got := run(t, loop)

			if len(got.Attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			if got.Attempts[0].Rejection != rewrite.RejectionLanguageGrowth {
				t.Errorf("rejected as %q, want %q",
					got.Attempts[0].Rejection, rewrite.RejectionLanguageGrowth)
			}
		})
	}
}

// A preserve failure still outranks growth, and keeps growth's evidence.
func TestPreserveIsReportedBeforeGrowth(t *testing.T) {
	gate := passingGate()
	gate.fallback.preserved = false
	gate.fallback.identifiers = []string{"preserve-v1:number:lost:3d4c981bf761d9b8"}
	gate.fallback.overgrown = []string{"Han"}

	got := run(t, growthLoop(t, gate))

	if len(got.Attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	if code := got.Attempts[0].Rejection; code != rewrite.RejectionNotPreserved {
		t.Errorf("rejected as %q, want %q", code, rewrite.RejectionNotPreserved)
	}
	if !equalStrings(got.Attempts[0].OvergrownScripts, []string{"Han"}) {
		t.Errorf("the attempt records %v, want [Han] — the measurement happened "+
			"whichever rejection is reported", got.Attempts[0].OvergrownScripts)
	}
}

// ---------------------------------------------------------------------------
// The record and the loop
// ---------------------------------------------------------------------------

// Two overgrowths in a row are both refused.
//
// A policy applying only when the preceding attempt was not a growth refusal
// passed #91's package in the equivalent case. There is no reason a second
// substitution is more acceptable than the first.
func TestConsecutiveOvergrowthsAreBothRefused(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true},
		verdicts: map[string]gateVerdict{
			better: {preserved: true, comparison: -1, comparable: true,
				overgrown: []string{"Han"}},
			betterYet: {preserved: true, comparison: -1, comparable: true,
				overgrown: []string{"Cyrillic"}},
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
		t.Fatalf("a second overgrowth was accepted: changed=%v text=%q",
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
			if attempt.Rejection != rewrite.RejectionLanguageGrowth || attempt.Accepted {
				t.Errorf("%s attempt %d is %q/accepted=%v", where, i,
					attempt.Rejection, attempt.Accepted)
			}
			if !equalStrings(attempt.OvergrownScripts, want) {
				t.Errorf("%s attempt %d records %v, want %v",
					where, i, attempt.OvergrownScripts, want)
			}
		}
	}
}

// Growth refusals consume attempts like any other.
//
// Refunding the attempt turns the one gate a provider can trigger repeatedly
// into an unbounded loop, which is the failure the cap exists for.
func TestGrowthRefusalsConsumeTheAttemptCap(t *testing.T) {
	gate := &fakeGate{
		fallback: gateVerdict{preserved: true, comparison: -1, comparable: true,
			overgrown: []string{"Han"}},
		verdicts: map[string]gateVerdict{
			betterYet: {preserved: true, comparison: -1, comparable: true},
		},
	}
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.30), betterYet: scored(0.20),
		},
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

// The recorded scripts are the loop's own, not the gate's buffer.
//
// #91's review proved this reachable for the introduced slice; the same hazard
// applies to this one, and a gate computing into a scratch buffer would rewrite
// the evidence of an already recorded refusal.
func TestTheRecordedOvergrownScriptsSurviveTheGateReusingItsBuffer(t *testing.T) {
	gate := &reusingGrowthGate{
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
			if !equalStrings(attempts[i].OvergrownScripts, want) {
				t.Errorf("the %s attempt %d records %v, want %v — the gate's buffer "+
					"was reused after the attempt was recorded",
					where, i, attempts[i].OvergrownScripts, want)
			}
		}
	}
}

// reusingGrowthGate answers from ONE backing slice, overwriting it in place.
type reusingGrowthGate struct {
	*fakeGate
	scripts [][]string
	buffer  []string
	calls   int
}

func (r *reusingGrowthGate) Language(origin, current, candidate string) (rewrite.LanguageVerdict, error) {
	r.fakeGate.Language(origin, current, candidate)
	want := r.scripts[min(r.calls, len(r.scripts)-1)]
	r.calls++
	r.buffer = append(r.buffer[:0], want...)
	return rewrite.LanguageVerdict{Overgrown: r.buffer}, nil
}

// A store failure on a growth refusal is still a failure. A refusal nobody can
// read afterwards is where retention matters most, since the prose is discarded.
func TestAStoreFailureOnAGrowthRefusalIsAnError(t *testing.T) {
	gate := passingGate()
	gate.fallback.overgrown = []string{"Han"}
	loop := growthLoop(t, gate)
	loop.Store = failingStore{}

	if _, err := loop.Rewrite(context.Background(),
		rewrite.Segment{Text: original, SpanRef: "span-0"}); err == nil {
		t.Error("a store failure on a growth refusal was swallowed")
	}
}

// The complete record on a growth refusal, in both sinks.
//
// #91's review found ten defects in the audit record and none in the refusal,
// every one a field some test happened not to read. The whitelist is asserted
// whole here for the new path.
func TestAGrowthRefusalCarriesTheWholeAuditRecord(t *testing.T) {
	gate := passingGate()
	gate.fallback.overgrown = []string{"Han"}
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
		if a.Rejection != rewrite.RejectionLanguageGrowth || a.Accepted {
			t.Errorf("the %s attempt is %q/accepted=%v", where, a.Rejection, a.Accepted)
		}
		if a.Index != 0 || a.SpanRef != "span-0" {
			t.Errorf("the %s attempt is index %d span %q", where, a.Index, a.SpanRef)
		}
		if a.CurrentDistance != 0.90 || a.CandidateDistance != 0.30 {
			t.Errorf("the %s attempt records distances %v and %v",
				where, a.CurrentDistance, a.CandidateDistance)
		}
		if a.CurrentBand != eval.BandDrifting || a.CandidateBand != eval.BandDrifting {
			t.Errorf("the %s attempt records bands %q and %q",
				where, a.CurrentBand, a.CandidateBand)
		}
		// The guards it PASSED.
		if !a.Preserved || !a.TellsComparable || a.TellsComparison != -1 {
			t.Errorf("the %s attempt records guards %v/%v/%d",
				where, a.Preserved, a.TellsComparable, a.TellsComparison)
		}
		if len(a.IntroducedScripts) != 0 {
			t.Errorf("the %s attempt records introduced %v, want none",
				where, a.IntroducedScripts)
		}
		if !equalStrings(a.OvergrownScripts, []string{"Han"}) {
			t.Errorf("the %s attempt records overgrown %v, want [Han]",
				where, a.OvergrownScripts)
		}
	}
}

// The code is declared, spelled as it reads, and appears once.
func TestRejectionLanguageGrowthIsDeclared(t *testing.T) {
	if string(rewrite.RejectionLanguageGrowth) != "language-growth" {
		t.Errorf("RejectionLanguageGrowth = %q, want %q",
			rewrite.RejectionLanguageGrowth, "language-growth")
	}
	// Distinct from its sibling, and not a substring collision with the band
	// vocabulary: `eval.BandDrifting` is "drifting", and a reader sees both on
	// one output line.
	if rewrite.RejectionLanguageGrowth == rewrite.RejectionLanguage {
		t.Error("the growth code is the same string as the introduction code")
	}
	if strings.Contains(string(rewrite.RejectionLanguageGrowth), string(eval.BandDrifting)) {
		t.Errorf("%q contains the band name %q; the CLI prints both on one line",
			rewrite.RejectionLanguageGrowth, eval.BandDrifting)
	}
	seen := 0
	for _, code := range rewrite.RejectionCodes() {
		if code == rewrite.RejectionLanguageGrowth {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%q appears %d times in RejectionCodes(): %v",
			rewrite.RejectionLanguageGrowth, seen, rewrite.RejectionCodes())
	}
}

// equalStrings compares two slices treating nil and empty as the same, since
// both mean "none" and the loop is not required to choose between them.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
