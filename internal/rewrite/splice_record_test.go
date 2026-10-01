package rewrite_test

// #135. The splice gate is consulted on every candidate and recorded on none.
//
// `SpliceableIntoOriginal` runs before the precedence switch — deliberately, so
// its evidence exists whichever refusal wins — and `Attempt` had no field for it,
// so when preserve, language, tells or the distance rejected first the verdict was
// a local variable that went out of scope. An unspliceable non-improving candidate
// and a spliceable one left indistinguishable records, after a check costing up to
// 89.6 ms against 26 µs to score a paragraph.
//
// The inconsistency is visible in the same struct: `IntroducedScripts` and
// `OvergrownScripts` ARE recorded per attempt, for exactly the stated reason.
//
// # Contract
//
//	SpliceNotRecorded = ""            no verdict is recorded on this attempt
//	SpliceIntact      = "intact"      the gate ran and the candidate splices
//	SpliceNotIntact   = "not-intact"  the gate ran and it does not
//
// Assigned immediately after the gate returns successfully and BEFORE the
// precedence switch, so the value does not depend on which code wins.
//
// # What the empty value does and does not say
//
// It says no verdict is recorded. For an attempt this loop writes, that means the
// gate block did not run, because all four gates are reached only when scoring
// produced no rejection. For a row written before this slice it means the gate may
// well have run and its result was discarded.
//
// The column cannot distinguish those, and deliberately does not try: a second
// value meaning "not evaluated" would be inventing a historical fact about every
// row already stored.
//
// # Which rejections skip the gates, measured rather than assumed
//
// Only four of them reach an attempt at all. `judged` runs twice — once on the
// CURRENT text before the loop is entered, once on each candidate — and the first
// returns `TerminalNotEntered` with NO attempt, so `unscoreable`,
// `not-one-segment` and `uncalibrated` on the current text never produce a row.
// What produces a row with the gates skipped is the CANDIDATE's: `not-one-segment`,
// `candidate-unscoreable`, `uncalibrated`, and `different-features` after it.

import (
	"context"
	"testing"

	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// The verdict is recorded whichever code wins, and it is the GATE's answer.
//
// Both dimensions in one table: every rejection that can co-occur with the splice
// check, crossed with a gate that answers yes and one that answers no. The
// spliceable half is what stops the field being derived from the rejection — an
// implementation writing `not-intact` whenever a candidate was refused passes the
// unspliceable half alone.
func TestTheSpliceVerdictIsRecordedWhicheverRejectionWins(t *testing.T) {
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
			// The one an earlier version of this table omitted while its own
			// name claimed every co-occurring code. Measured by codex as a
			// surviving mutant: clearing the verdict only when THIS rejection
			// wins passed the whole suite.
			name:    "tells incomparable",
			mutate:  func(g *fakeGate) { g.fallback.comparable = false; g.fallback.comparison = 0 },
			reports: map[string]score.Report{original: scored(0.90), better: scored(0.30)},
			want:    rewrite.RejectionTellsIncomparable,
		},
		{
			name:    "not improved",
			mutate:  func(g *fakeGate) {},
			reports: map[string]score.Report{original: scored(0.30), better: scored(0.90)},
			want:    rewrite.RejectionNotImproved,
		},
	} {
		for _, splices := range []bool{true, false} {
			name := c.name + ", spliceable"
			want := rewrite.SpliceIntact
			if !splices {
				name, want = c.name+", unspliceable", rewrite.SpliceNotIntact
			}
			t.Run(name, func(t *testing.T) {
				gate := passingGate()
				gate.fallback.unspliceable = !splices
				c.mutate(gate)
				loop, _, _, _, store := loopOver(t, c.reports, []string{better}, gate)

				got := run(t, loop)

				if len(got.Attempts) == 0 || len(store.attempts) == 0 {
					t.Fatalf("%d attempts recorded and %d stored", len(got.Attempts), len(store.attempts))
				}
				// The rejection is still the one precedence chose: recording the
				// evidence must not change which code is reported.
				if got.Attempts[0].Rejection != c.want {
					t.Fatalf("rejected as %q, want %q", got.Attempts[0].Rejection, c.want)
				}
				for where, attempt := range map[string]rewrite.Attempt{
					"outcome": got.Attempts[0], "store": store.attempts[0],
				} {
					if attempt.Splice != want {
						t.Errorf("the %s attempt records splice %q, want %q — the gate ran "+
							"and its answer is evidence whichever refusal won",
							where, attempt.Splice, want)
					}
				}
			})
		}
	}
}

// An accepted candidate records intact.
//
// The gate passed, so there is a verdict, and an implementation recording only on
// the refusal paths leaves the accepted row looking like one the gate never saw.
func TestAnAcceptedAttemptRecordsThatItSplices(t *testing.T) {
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, passingGate())

	got := run(t, loop)

	if !got.Changed {
		t.Fatalf("the fixture must accept or there is no accepted attempt: %+v", got.Attempts)
	}
	for where, attempt := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[0], "store": store.attempts[0],
	} {
		if !attempt.Accepted {
			t.Fatalf("the %s attempt was not accepted", where)
		}
		if attempt.Splice != rewrite.SpliceIntact {
			t.Errorf("the %s accepted attempt records splice %q, want %q",
				where, attempt.Splice, rewrite.SpliceIntact)
		}
	}
}

// And the refusal that IS the splice check records not-intact.
//
// Trivial on its face and kept because the inversion is not: a field assigned
// `SpliceIntact` whenever the gate was consulted satisfies every other case here.
func TestAnUnspliceableRefusalRecordsNotIntact(t *testing.T) {
	gate := passingGate()
	gate.fallback.unspliceable = true
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.30)},
		[]string{better}, gate)

	got := run(t, loop)

	if got.Attempts[0].Rejection != rewrite.RejectionNotSpliceable {
		t.Fatalf("rejected as %q, want %q", got.Attempts[0].Rejection, rewrite.RejectionNotSpliceable)
	}
	for where, attempt := range map[string]rewrite.Attempt{
		"outcome": got.Attempts[0], "store": store.attempts[0],
	} {
		if attempt.Splice != rewrite.SpliceNotIntact {
			t.Errorf("the %s attempt records splice %q, want %q",
				where, attempt.Splice, rewrite.SpliceNotIntact)
		}
	}
}

// A rejection that skips the gates records NO verdict.
//
// These four are the ones that reach an attempt without the gate block running.
// The current text's own `unscoreable`, `not-one-segment` and `uncalibrated` are
// absent deliberately: they return `TerminalNotEntered` before any attempt exists,
// so there is no row to carry a verdict and asserting one would be asserting
// against a row that is never written.
func TestARejectionThatSkipsTheGatesRecordsNoVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		reports map[string]score.Report
		want    rewrite.RejectionCode
	}{
		{
			name:    "the candidate is unscoreable",
			reports: map[string]score.Report{original: scored(0.90), better: unscoreable()},
			want:    rewrite.RejectionCandidateUnscoreable,
		},
		{
			name:    "the candidate is not one segment",
			reports: map[string]score.Report{original: scored(0.90), better: paragraphs(2)},
			want:    rewrite.RejectionNotOneSegment,
		},
		{
			name:    "the candidate is uncalibrated",
			reports: map[string]score.Report{original: scored(0.90), better: uncalibrated()},
			want:    rewrite.RejectionUncalibrated,
		},
		{
			name: "the candidate measures different features",
			reports: map[string]score.Report{
				original: scored(0.90, features.WordLengthMean, features.CommaDensity),
				better:   scored(0.30, features.WordLengthMean),
			},
			want: rewrite.RejectionDifferentFeatures,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// A gate that would answer NO if it were asked, so an implementation
			// consulting it anyway and recording the answer fails here rather
			// than agreeing by accident.
			gate := passingGate()
			gate.fallback.unspliceable = true
			loop, _, _, _, store := loopOver(t, c.reports, []string{better}, gate)

			got := run(t, loop)

			if len(got.Attempts) == 0 || len(store.attempts) == 0 {
				t.Fatalf("%d attempts recorded and %d stored; this rejection must still "+
					"produce a row", len(got.Attempts), len(store.attempts))
			}
			if got.Attempts[0].Rejection != c.want {
				t.Fatalf("rejected as %q, want %q — the fixture is not producing this case",
					got.Attempts[0].Rejection, c.want)
			}
			for where, attempt := range map[string]rewrite.Attempt{
				"outcome": got.Attempts[0], "store": store.attempts[0],
			} {
				if attempt.Splice != rewrite.SpliceNotRecorded {
					t.Errorf("the %s attempt records splice %q, want %q — the gates were "+
						"never reached, so there is no verdict to record",
						where, attempt.Splice, rewrite.SpliceNotRecorded)
				}
			}
		})
	}
}

// A skipped gate does not inherit the verdict of the attempt before it.
//
// Every other skip case here is attempt ZERO, where there is nothing to inherit,
// and the two-attempt test runs the gate on both. So hoisting the verdict out of
// the loop, updating it only when the gate runs, and copying it into each attempt
// passes all of them — measured by codex as exactly that mutant.
//
// Both preceding verdicts crossed with all four skipping rejections, because the
// inherited value could be either and the four reach the attempt by different
// routes.
func TestASkippedGateDoesNotInheritTheLastVerdict(t *testing.T) {
	for _, prior := range []struct {
		name         string
		unspliceable bool
		want         rewrite.SpliceOutcome
	}{
		{"after intact", false, rewrite.SpliceIntact},
		{"after not-intact", true, rewrite.SpliceNotIntact},
	} {
		for _, skip := range []struct {
			name   string
			report score.Report
			want   rewrite.RejectionCode
		}{
			{"candidate unscoreable", unscoreable(), rewrite.RejectionCandidateUnscoreable},
			{"not one segment", paragraphs(2), rewrite.RejectionNotOneSegment},
			{"uncalibrated", uncalibrated(), rewrite.RejectionUncalibrated},
			{
				"different features",
				scored(0.20, features.WordLengthMean),
				rewrite.RejectionDifferentFeatures,
			},
		} {
			t.Run(prior.name+", "+skip.name, func(t *testing.T) {
				gate := passingGate()
				gate.fallback.unspliceable = prior.unspliceable
				// The first candidate is WORSE, so it is refused as not-improved
				// with the gate having run; the second is the one whose scoring
				// skips the gate block entirely.
				loop, _, _, _, store := loopOver(t,
					map[string]score.Report{
						original: scored(0.50), better: scored(0.90), betterYet: skip.report,
					},
					[]string{better, betterYet}, gate)
				loop.Options.Attempts = 2

				got := run(t, loop)

				if len(got.Attempts) != 2 || len(store.attempts) != 2 {
					t.Fatalf("%d attempts recorded and %d stored, want 2 and 2",
						len(got.Attempts), len(store.attempts))
				}
				// The fixture must really produce a non-empty verdict first, or
				// there is nothing to inherit and this proves nothing.
				if got.Attempts[0].Splice != prior.want {
					t.Fatalf("the first attempt records splice %q, want %q",
						got.Attempts[0].Splice, prior.want)
				}
				if got.Attempts[1].Rejection != skip.want {
					t.Fatalf("the second attempt was rejected as %q, want %q",
						got.Attempts[1].Rejection, skip.want)
				}
				for where, attempt := range map[string]rewrite.Attempt{
					"outcome": got.Attempts[1], "store": store.attempts[1],
				} {
					if attempt.Splice != rewrite.SpliceNotRecorded {
						t.Errorf("the %s second attempt records splice %q, want %q — the "+
							"gate never ran for it and the value is the previous "+
							"attempt's", where, attempt.Splice, rewrite.SpliceNotRecorded)
					}
				}
			})
		}
	}
}

// Each attempt carries its OWN verdict.
//
// Two candidates, one unspliceable and one not, so a single variable reused across
// the loop — or a verdict copied from the last attempt — is caught. The scores are
// arranged so the first is refused and the second accepted, which also pins that
// the value is not simply a function of acceptance.
func TestEachAttemptCarriesItsOwnSpliceVerdict(t *testing.T) {
	gate := passingGate()
	gate.verdicts = map[string]gateVerdict{
		better:    {preserved: true, comparable: true, unspliceable: true},
		betterYet: {preserved: true, comparable: true},
	}
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, gate)
	loop.Options.Attempts = 2

	got := run(t, loop)

	if len(got.Attempts) != 2 || len(store.attempts) != 2 {
		t.Fatalf("%d attempts recorded and %d stored, want 2 and 2",
			len(got.Attempts), len(store.attempts))
	}
	want := []rewrite.SpliceOutcome{rewrite.SpliceNotIntact, rewrite.SpliceIntact}
	for i, attempt := range got.Attempts {
		if attempt.Splice != want[i] {
			t.Errorf("outcome attempt %d records splice %q, want %q",
				i, attempt.Splice, want[i])
		}
		if store.attempts[i].Splice != want[i] {
			t.Errorf("stored attempt %d records splice %q, want %q",
				i, store.attempts[i].Splice, want[i])
		}
	}
	// And the first was refused for exactly that while the second was accepted, so
	// the two rows differ in the verdict AND in the outcome.
	if got.Attempts[0].Rejection != rewrite.RejectionNotSpliceable || !got.Attempts[1].Accepted {
		t.Errorf("the fixture produced %q then accepted=%v; it must refuse the first "+
			"as unspliceable and accept the second",
			got.Attempts[0].Rejection, got.Attempts[1].Accepted)
	}
}

// A gate error records nothing for the failing attempt, and keeps what came before.
//
// A check that did not complete has no verdict, so the attempt it belongs to is not
// written at all — the existing behaviour, pinned here because the new field makes
// "record a partial attempt" newly tempting. Earlier attempts stand: this is not an
// invocation-wide rollback.
func TestAGateErrorRecordsNoAttemptAndKeepsTheEarlierOnes(t *testing.T) {
	gate := &erroringAfterFirstSpliceGate{fakeGate: passingGate()}
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original: scored(0.90), better: scored(0.60), betterYet: scored(0.30),
		},
		[]string{better, betterYet}, passingGate())
	loop.Gate = gate
	loop.Options.Attempts = 2

	_, err := loop.Rewrite(context.Background(), rewrite.Segment{Text: original, SpanRef: "span-0"})

	if err == nil {
		t.Fatal("the second gate call failed and Rewrite returned no error")
	}
	if len(store.attempts) != 1 {
		t.Fatalf("the store holds %d attempts, want 1 — the first completed and the "+
			"second never got a verdict", len(store.attempts))
	}
	if store.attempts[0].Splice != rewrite.SpliceIntact {
		t.Errorf("the attempt that DID complete records splice %q, want %q",
			store.attempts[0].Splice, rewrite.SpliceIntact)
	}
}

// The vocabulary is declared, and the store's column is checked against it.
//
// `vocabulary_test.go` next door requires every enum column to name a vocabulary
// from the package that owns it, so this is the function that keeps the schema and
// the Go constants from drifting apart.
func TestTheSpliceOutcomeVocabularyIsDeclared(t *testing.T) {
	// MEMBERSHIP and uniqueness, not order: the store binds these by name and
	// nothing reads the slice positionally, so pinning the order would pin an
	// implementation detail. The empty member has to be present — the column is
	// NOT NULL and every stored row names one of these.
	got := rewrite.SpliceOutcomes()
	want := map[rewrite.SpliceOutcome]bool{
		rewrite.SpliceNotRecorded: true, rewrite.SpliceIntact: true,
		rewrite.SpliceNotIntact: true,
	}
	if len(got) != len(want) {
		t.Fatalf("SpliceOutcomes() = %v, want the three members of %v", got, want)
	}
	seen := map[rewrite.SpliceOutcome]int{}
	for _, outcome := range got {
		if !want[outcome] {
			t.Errorf("SpliceOutcomes() names %q, which is not a member", outcome)
		}
		seen[outcome]++
	}
	for outcome, n := range seen {
		if n != 1 {
			t.Errorf("%q appears %d times", outcome, n)
		}
	}
	// The spellings, because the database CHECK is written against these exact
	// strings and a rename that changed only the constant would pass everything
	// above.
	for outcome, want := range map[rewrite.SpliceOutcome]string{
		rewrite.SpliceNotRecorded: "", rewrite.SpliceIntact: "intact",
		rewrite.SpliceNotIntact: "not-intact",
	} {
		if string(outcome) != want {
			t.Errorf("the outcome spelled %q should be %q", string(outcome), want)
		}
	}
}

// erroringAfterFirstSpliceGate answers once and then fails, so the loop has a
// completed attempt behind it when the second call errors.
type erroringAfterFirstSpliceGate struct {
	*fakeGate
	calls int
}

func (e *erroringAfterFirstSpliceGate) SpliceableIntoOriginal(candidate string) (rewrite.SpliceVerdict, error) {
	e.calls++
	if e.calls > 1 {
		return rewrite.SpliceVerdict{}, errSpliceGate
	}
	return e.fakeGate.SpliceableIntoOriginal(candidate)
}
