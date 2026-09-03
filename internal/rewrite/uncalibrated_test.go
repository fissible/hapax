package rewrite_test

// #81's claim, reduced to the one thing this package can settle.
//
// The acceptance rule is `d(candidate) <= d(current) - epsilon`. It reads
// Distance.Value and nothing else. The band is never consulted by it. Yet
// judged() refuses to enter the loop at all without a defined band, so a corpus
// too small to calibrate cannot be improved against even though the measurement
// it needs exists and is real.
//
// That is one gate doing two jobs. Calibration decides WHICH paragraphs are
// worth rewriting — it is what lets the tool say "this one is drifting". Under
// explicit selection the user has already answered that question, so the band
// has no remaining job, and the distance is sufficient.
//
// # What must NOT follow from that
//
// Everything else in the loop stays mandatory. The temptation an implementer
// faces here is to read "allow uncalibrated" as "relax the gate", delete the
// band check, and ship. The tests below exist because that change also deletes:
//
//   - the refusal to rewrite a passage nothing could measure,
//   - the refusal to accept a candidate nothing could measure,
//   - preserve, tells comparability and tells non-regression,
//   - the same-feature-set comparability check,
//   - strict improvement.
//
// Each of those is asserted again HERE, under AllowUncalibrated, rather than
// relying on the calibrated tests in rewrite_test.go — because the defect this
// slice can introduce is precisely a second code path where they do not run.
//
// # And it must be off unless asked for
//
// Automatic targeting stays band-driven. A default that allowed uncalibrated
// rewriting would make the weaker claim the one a user gets without choosing
// it, which is the failure this project's refusal codes exist to prevent.

import (
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// uncalibratedAt is a report with a real distance and no band: what a corpus
// large enough to measure and too small to calibrate actually produces.
func uncalibratedAt(distance float64, contributing ...features.ID) score.Report {
	out := scored(distance, contributing...)
	out.Calibrated = false
	out.Segments[0].Band = eval.BandOutcome{Defined: false, Reason: eval.ReasonUncalibrated}
	return out
}

// allowing turns the option on without disturbing the rest of the assembled loop.
func allowing(loop rewrite.Loop) rewrite.Loop {
	loop.Options.AllowUncalibrated = true
	return loop
}

// ---------------------------------------------------------------------------
// The option itself
// ---------------------------------------------------------------------------

// The weaker claim is opt-in. A caller that does not ask for it gets today's
// behaviour exactly, which is what keeps automatic targeting band-driven.
func TestUncalibratedRewritingIsOffByDefault(t *testing.T) {
	if rewrite.DefaultOptions().AllowUncalibrated {
		t.Error("DefaultOptions allows uncalibrated rewriting; the weaker claim must be asked for")
	}
}

// And with it off, an uncalibrated report is refused before the provider is
// reached — no prompt, no call, no attempt recorded.
//
// This is the test that fails if the band check is simply deleted rather than
// made conditional.
func TestWithoutTheOptionAnUncalibratedSegmentStillRefuses(t *testing.T) {
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{original: uncalibratedAt(0.5), better: uncalibratedAt(0.1)},
		[]string{better}, passingGate())

	got := run(t, loop)

	if got.Reason != rewrite.RejectionUncalibrated {
		t.Errorf("reason = %q, want %q", got.Reason, rewrite.RejectionUncalibrated)
	}
	if got.Terminal != rewrite.TerminalNotEntered {
		t.Errorf("terminal = %q, want %q", got.Terminal, rewrite.TerminalNotEntered)
	}
	if got.Changed {
		t.Error("an uncalibrated segment was rewritten without the option")
	}
	if got.Text != original {
		t.Error("the text changed under a refusal")
	}
	if len(provider.requests) != 0 {
		t.Errorf("the provider was called %d times under a refusal", len(provider.requests))
	}
	if len(store.attempts) != 0 {
		t.Errorf("%d attempts recorded under a refusal", len(store.attempts))
	}
}

// ---------------------------------------------------------------------------
// What the option buys
// ---------------------------------------------------------------------------

// A real distance and no band is enough to enter the loop, improve, and accept.
func TestAnUncalibratedSegmentWithADistanceIsRewritten(t *testing.T) {
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{original: uncalibratedAt(0.5), better: uncalibratedAt(0.1)},
		[]string{better}, passingGate())

	got := run(t, allowing(loop))

	if got.Reason != rewrite.RejectionNone {
		t.Fatalf("reason = %q, want no rejection", got.Reason)
	}
	if !got.Changed {
		t.Fatal("a candidate measuring strictly closer was not accepted")
	}
	if got.Text != better {
		t.Errorf("text = %q, want the accepted candidate", got.Text)
	}
	if len(provider.requests) == 0 {
		t.Fatal("the provider was never called")
	}
	// The passage sent is the segment's own bytes. A loop that entered but sent
	// something else would satisfy every assertion above.
	if provider.requests[0].segment != original {
		t.Errorf("the provider was sent %q, want the segment's own text", provider.requests[0].segment)
	}
	if len(store.attempts) != 1 {
		t.Fatalf("%d attempts recorded, want 1", len(store.attempts))
	}
	if !store.attempts[0].Accepted {
		t.Error("the recorded attempt does not say it was accepted")
	}
}

// The audit record says there was no band, rather than inventing one. An empty
// band is a declared member of eval.Bands(), so this is representable without
// lying and without a sentinel.
func TestTheRecordedAttemptDoesNotInventABand(t *testing.T) {
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: uncalibratedAt(0.5), better: uncalibratedAt(0.1)},
		[]string{better}, passingGate())

	run(t, allowing(loop))

	if len(store.attempts) != 1 {
		t.Fatalf("%d attempts recorded, want 1", len(store.attempts))
	}
	attempt := store.attempts[0]
	if attempt.CurrentBand != eval.Band("") {
		t.Errorf("current band = %q, want empty; there was no band to record", attempt.CurrentBand)
	}
	if attempt.CandidateBand != eval.Band("") {
		t.Errorf("candidate band = %q, want empty; there was no band to record", attempt.CandidateBand)
	}
	// The distances are the real measurement and must survive into the record,
	// or the audit trail cannot say why the candidate was accepted.
	if attempt.CurrentDistance != 0.5 || attempt.CandidateDistance != 0.1 {
		t.Errorf("distances recorded as current=%v candidate=%v, want 0.5 and 0.1",
			attempt.CurrentDistance, attempt.CandidateDistance)
	}
	if !contains(eval.Bands(), attempt.CurrentBand) {
		t.Errorf("the recorded band %q is not a declared band", attempt.CurrentBand)
	}
}

func contains[T comparable](haystack []T, needle T) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// What the option must NOT buy
// ---------------------------------------------------------------------------

// A passage nothing could measure is reported unchanged, never rewritten
// blindly. Without a distance there is no acceptance rule at all, so the option
// has nothing to relax.
func TestAnUnmeasurableSegmentIsStillNotEnteredWhenUncalibrated(t *testing.T) {
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{original: unscoreable(), better: uncalibratedAt(0.1)},
		[]string{better}, passingGate())

	got := run(t, allowing(loop))

	if got.Reason != rewrite.RejectionUnscoreable {
		t.Errorf("reason = %q, want %q", got.Reason, rewrite.RejectionUnscoreable)
	}
	if got.Terminal != rewrite.TerminalNotEntered {
		t.Errorf("terminal = %q, want %q", got.Terminal, rewrite.TerminalNotEntered)
	}
	if got.Changed || got.Text != original {
		t.Error("an unmeasurable segment was rewritten")
	}
	if len(provider.requests) != 0 {
		t.Errorf("the provider was called %d times on an unmeasurable segment", len(provider.requests))
	}
	if len(store.attempts) != 0 {
		t.Errorf("%d attempts recorded on an unmeasurable segment", len(store.attempts))
	}
}

// And a candidate nothing could measure cannot be accepted, because there is no
// value to compare against the current one.
func TestAnUnmeasurableCandidateIsStillRejectedWhenUncalibrated(t *testing.T) {
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: uncalibratedAt(0.5), worse: unscoreable()},
		[]string{worse}, passingGate())

	got := run(t, allowing(loop))

	if got.Changed || got.Text != original {
		t.Fatal("an unmeasurable candidate was accepted")
	}
	if len(store.attempts) == 0 {
		t.Fatal("no attempt was recorded for a candidate that was actually produced")
	}
	if code := store.attempts[0].Rejection; code != rewrite.RejectionCandidateUnscoreable {
		t.Errorf("rejection = %q, want %q", code, rewrite.RejectionCandidateUnscoreable)
	}
}

// Every guard that governs a calibrated rewrite governs an uncalibrated one.
// Asserted here rather than by reference, because the risk is a second path in
// which they do not run.
func TestTheGuardsAreStillMandatoryWhenUncalibrated(t *testing.T) {
	cases := []struct {
		name    string
		verdict gateVerdict
		want    rewrite.RejectionCode
	}{
		{"preservation fails", gateVerdict{preserved: false, identifiers: []string{"url:1"}, comparison: -1, comparable: true}, rewrite.RejectionNotPreserved},
		{"tells incomparable", gateVerdict{preserved: true, comparison: 0, comparable: false}, rewrite.RejectionTellsIncomparable},
		{"tells regress", gateVerdict{preserved: true, comparison: 1, comparable: true}, rewrite.RejectionTellsWorse},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gate := &fakeGate{fallback: c.verdict}
			// The candidate measures strictly closer, so distance alone would
			// accept it. Only the guard can refuse it.
			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{original: uncalibratedAt(0.5), better: uncalibratedAt(0.1)},
				[]string{better}, gate)

			got := run(t, allowing(loop))

			if got.Changed || got.Text != original {
				t.Fatalf("a candidate was accepted despite %s", c.name)
			}
			if len(store.attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			if code := store.attempts[0].Rejection; code != c.want {
				t.Errorf("rejection = %q, want %q", code, c.want)
			}
		})
	}
}

// Two reports scored over different feature sets are not comparable, so the
// distance difference between them is not an improvement. This is separate from
// the guards above and has its own path through the loop.
func TestDifferentFeatureSetsAreStillIncomparableWhenUncalibrated(t *testing.T) {
	current := uncalibratedAt(0.5, features.WordLengthMean, features.CommaDensity)
	candidate := uncalibratedAt(0.1, features.WordLengthMean)

	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: current, better: candidate},
		[]string{better}, passingGate())

	got := run(t, allowing(loop))

	if got.Changed {
		t.Fatal("a candidate scored over a different feature set was accepted")
	}
	if len(store.attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	if code := store.attempts[0].Rejection; code != rewrite.RejectionDifferentFeatures {
		t.Errorf("rejection = %q, want %q", code, rewrite.RejectionDifferentFeatures)
	}
}

// Improvement stays strict. A tie is a rejection and a difference inside epsilon
// is a tie — the same rule ADR 0006 states for the calibrated path, asserted
// against the measurement that is now doing the work alone.
func TestImprovementIsStillStrictWhenUncalibrated(t *testing.T) {
	cases := []struct {
		name             string
		current, cand    float64
		wantAccepted     bool
		wantRejectionRaw rewrite.RejectionCode
	}{
		{"identical", 0.5, 0.5, false, rewrite.RejectionNotImproved},
		{"inside epsilon", 0.5, 0.5 - rewrite.Epsilon/2, false, rewrite.RejectionNotImproved},
		{"further away", 0.5, 0.9, false, rewrite.RejectionNotImproved},
		{"strictly closer", 0.5, 0.4, true, rewrite.RejectionNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{original: uncalibratedAt(c.current), better: uncalibratedAt(c.cand)},
				[]string{better}, passingGate())

			got := run(t, allowing(loop))

			if got.Changed != c.wantAccepted {
				t.Fatalf("changed = %v, want %v (d %v -> %v)", got.Changed, c.wantAccepted, c.current, c.cand)
			}
			if len(store.attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			if code := store.attempts[0].Rejection; code != c.wantRejectionRaw {
				t.Errorf("rejection = %q, want %q", code, c.wantRejectionRaw)
			}
		})
	}
}

// The cap still bounds an uncalibrated run. A provider that never improves must
// not be asked for ever, and the terminal must say why the loop stopped.
func TestTheAttemptCapStillBoundsAnUncalibratedRun(t *testing.T) {
	loop, _, _, provider, _ := loopOver(t,
		map[string]score.Report{original: uncalibratedAt(0.5), worse: uncalibratedAt(0.9)},
		[]string{worse, worse, worse, worse}, passingGate())

	got := run(t, allowing(loop))

	if got.Changed {
		t.Fatal("a candidate measuring further away was accepted")
	}
	if got.Terminal != rewrite.TerminalExhausted {
		t.Errorf("terminal = %q, want %q", got.Terminal, rewrite.TerminalExhausted)
	}
	if len(provider.requests) != loop.Options.Attempts {
		t.Errorf("the provider was called %d times, want the cap of %d",
			len(provider.requests), loop.Options.Attempts)
	}
}
