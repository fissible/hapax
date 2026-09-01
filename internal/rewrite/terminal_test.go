package rewrite_test

// Why an Outcome needs a terminal reason of its own.
//
// Three endings were indistinguishable, because all three left Reason empty and
// Changed false: the provider returned an empty candidate, every attempt was
// spent and every candidate rejected, and — nearly — a loop that was never
// entered at all. A caller reading such an outcome could not tell a wasted
// provider call from an exhausted budget from a paragraph the loop declined to
// judge, and those want different things said about them.
//
// The terminal reason is a separate vocabulary from RejectionCode rather than an
// extension of it. A RejectionCode describes ONE scored candidate: it names what
// was wrong with a particular piece of text. An empty response has no candidate
// to describe, and "every attempt was spent" is a fact about the loop rather
// than about any one of them. Overloading the per-attempt enum would put a value
// into `rewrite_attempt.rejection` that no attempt could ever carry.
//
// # What these tests are actually pinning
//
// Not "Terminal is set". An implementation that returned attempts-exhausted for
// everything would satisfy a test that only read the string on the ordinary
// path. What is pinned is the RELATION between the terminal reason and the
// recorded attempts, because that is what a wrong implementation cannot fake:
//
//	not-entered             => no provider call, no attempt, and Reason names why
//	empty-provider-response => exactly one call more than it recorded an attempt for
//	attempts-exhausted      => calls and attempts both equal the cap
//
// The middle line is the one that matters most, and it is the reason an empty
// response is worth naming at all: the loop records no Attempt for it, so a
// provider call was spent that leaves no trace anywhere. Nothing else in the
// outcome says so — which is also why "fewer attempts than the cap" is not
// enough on its own, since a loop that stopped early for no reason satisfies it.

import (
	"context"
	"errors"
	"testing"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// ---------------------------------------------------------------------------
// The vocabulary
// ---------------------------------------------------------------------------

func TestTheTerminalVocabularyIsClosedAndDeclaredInFull(t *testing.T) {
	want := []rewrite.Terminal{
		rewrite.TerminalNotEntered,
		rewrite.TerminalEmptyResponse,
		rewrite.TerminalExhausted,
	}
	got := rewrite.Terminals()
	if len(got) != len(want) {
		t.Fatalf("Terminals() has %d values %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Terminals()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, terminal := range got {
		if terminal == "" {
			t.Error("the empty string is a declared terminal reason, so a zero Outcome would look like a completed one")
		}
	}
}

// The accessor hands out a copy. A caller that sorts or truncates the returned
// slice would otherwise edit the vocabulary for every later caller in the
// process, and store validation reads this list to decide what it will persist.
func TestTheTerminalVocabularyCannotBeEditedByACaller(t *testing.T) {
	first := rewrite.Terminals()
	if len(first) == 0 {
		t.Fatal("Terminals() is empty")
	}
	first[0] = "tampered"
	for _, terminal := range rewrite.Terminals() {
		if terminal == "tampered" {
			t.Fatal("Terminals() returns its own backing array, so a caller can rewrite the vocabulary")
		}
	}
}

// The two vocabularies must not collide. `workflow` projects both of them into a
// result as plain strings, matching what that boundary already does for bands,
// refusals and eval reasons — so a value that appeared in both lists would be
// ambiguous to anything reading the result, and a mistake mapping one to the
// other would be invisible.
func TestATerminalReasonIsNeverAlsoARejectionCode(t *testing.T) {
	rejections := map[string]bool{}
	for _, code := range rewrite.RejectionCodes() {
		rejections[string(code)] = true
	}
	for _, terminal := range rewrite.Terminals() {
		if rejections[string(terminal)] {
			t.Errorf("%q is both a terminal reason and a rejection code", terminal)
		}
	}
}

// ---------------------------------------------------------------------------
// The relation between the terminal reason and what was recorded
// ---------------------------------------------------------------------------

// requireConsistent is the cross-check every case below runs. It is deliberately
// not "Terminal is the string I expected": it asserts the facts about the
// provider calls and the recorded attempts that the string CLAIMS, so an
// implementation that reports a terminal reason it did not reach is caught by
// the disagreement.
//
// The provider-call count is what makes the empty-response arm mean anything.
// "Fewer attempts than the cap" would also be satisfied by a loop that simply
// stopped early for no reason; "exactly one call more than it recorded" says the
// specific thing the reason names — a call was spent and left no record.
func requireConsistent(t *testing.T, got rewrite.Outcome, cap, calls int) {
	t.Helper()
	switch got.Terminal {
	case rewrite.TerminalNotEntered:
		if len(got.Attempts) != 0 {
			t.Errorf("not-entered, and yet %d attempts were recorded", len(got.Attempts))
		}
		if calls != 0 {
			t.Errorf("not-entered, and yet the provider was called %d times", calls)
		}
		if got.Reason == rewrite.RejectionNone {
			t.Error("not-entered without a reason, which is the ambiguity this enum exists to remove")
		}
		if got.Changed {
			t.Error("not-entered, and yet the outcome reports a change")
		}
	case rewrite.TerminalEmptyResponse:
		if len(got.Attempts) != calls-1 {
			t.Errorf("empty-provider-response after %d calls with %d attempts recorded; "+
				"exactly one call must have been spent without producing one", calls, len(got.Attempts))
		}
		if len(got.Attempts) >= cap {
			t.Errorf("empty-provider-response with %d attempts recorded against a cap of %d; "+
				"a loop that spent its whole budget did not stop early", len(got.Attempts), cap)
		}
		if got.Reason != rewrite.RejectionNone {
			t.Errorf("empty-provider-response carries the per-attempt reason %q; the loop was entered", got.Reason)
		}
	case rewrite.TerminalExhausted:
		if len(got.Attempts) != cap {
			t.Errorf("attempts-exhausted with %d attempts recorded against a cap of %d", len(got.Attempts), cap)
		}
		if calls != cap {
			t.Errorf("attempts-exhausted after %d provider calls against a cap of %d; every call produced "+
				"a candidate, so the counts must agree", calls, cap)
		}
		if got.Reason != rewrite.RejectionNone {
			t.Errorf("attempts-exhausted carries the per-attempt reason %q; the loop was entered", got.Reason)
		}
	default:
		t.Errorf("terminal reason %q is not in the declared vocabulary %v", got.Terminal, rewrite.Terminals())
	}
}

// Each of the three endings is reached by a different route, and each is
// asserted both by name and by the fact it claims.
func TestEveryCompletedOutcomeCarriesOneTerminalReason(t *testing.T) {
	for _, c := range []struct {
		name       string
		reports    map[string]score.Report
		candidates []string
		gate       *fakeGate
		want       rewrite.Terminal
		wantChange bool
	}{
		{
			// Nothing was ever asked of the provider, because the segment the
			// loop was handed could not be judged.
			name:    "the segment cannot be judged",
			reports: map[string]score.Report{original: unscoreable()},
			gate:    passingGate(),
			want:    rewrite.TerminalNotEntered,
		},
		{
			// The provider had nothing to say on the very first call.
			name:       "nothing on the first call",
			reports:    map[string]score.Report{original: scored(0.90)},
			candidates: nil,
			gate:       passingGate(),
			want:       rewrite.TerminalEmptyResponse,
		},
		{
			// And nothing more to say after one candidate, which is the case
			// that separates "stopped early" from "recorded nothing".
			name:       "nothing after one candidate",
			reports:    map[string]score.Report{original: scored(0.90), better: scored(0.50)},
			candidates: []string{better},
			gate:       passingGate(),
			want:       rewrite.TerminalEmptyResponse,
			wantChange: true,
		},
		{
			// Every attempt spent, every candidate refused.
			name:       "every attempt refused",
			reports:    map[string]score.Report{original: scored(0.50), worse: scored(0.90)},
			candidates: []string{worse, worse, worse},
			gate:       passingGate(),
			want:       rewrite.TerminalExhausted,
		},
		{
			// Every attempt spent, and the first one accepted. Acceptance is
			// not an ending: the loop is a hill climber and keeps asking.
			name:       "every attempt spent after an acceptance",
			reports:    map[string]score.Report{original: scored(0.90), better: scored(0.50), worse: scored(0.95)},
			candidates: []string{better, worse, worse},
			gate:       passingGate(),
			want:       rewrite.TerminalExhausted,
			wantChange: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			loop, _, _, provider, _ := loopOver(t, c.reports, c.candidates, c.gate)
			got := run(t, loop)
			if got.Terminal != c.want {
				t.Errorf("Terminal = %q, want %q", got.Terminal, c.want)
			}
			if got.Changed != c.wantChange {
				t.Errorf("Changed = %v, want %v", got.Changed, c.wantChange)
			}
			requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
		})
	}
}

// An empty response on the first call is the ending with the least evidence
// anywhere else: no attempt is recorded, nothing is persisted, and the outcome
// is otherwise identical to a loop that was never entered. This is the case the
// enum was added for, so it is asserted on its own rather than only in a table.
func TestAnEmptyFirstResponseLeavesNoOtherTrace(t *testing.T) {
	loop, _, _, provider, store := loopOver(t,
		map[string]score.Report{original: scored(0.90)}, nil, passingGate())

	got := run(t, loop)
	if got.Terminal != rewrite.TerminalEmptyResponse {
		t.Fatalf("Terminal = %q, want %q", got.Terminal, rewrite.TerminalEmptyResponse)
	}
	if len(provider.requests) != 1 {
		t.Errorf("the provider was called %d times, want once before the loop stopped", len(provider.requests))
	}
	if len(store.attempts) != 0 {
		t.Errorf("%d attempts reached the store; an empty candidate is not an attempt", len(store.attempts))
	}
	if len(got.Attempts) != 0 {
		t.Errorf("the outcome carries %d attempts", len(got.Attempts))
	}
	if got.Reason != rewrite.RejectionNone {
		t.Errorf("Reason = %q; the loop was entered, so no per-attempt reason applies", got.Reason)
	}
	if got.Text != original {
		t.Errorf("text = %q, want the original untouched", got.Text)
	}
	// The distinguishing fact: a provider call was spent and left no record.
	// Without the terminal reason there is nothing in the outcome or the store
	// that says so.
	if len(provider.requests) == len(store.attempts) {
		t.Error("the fixture did not produce an unrecorded provider call, so this test proves nothing")
	}
}

// Every pre-loop rejection reaches the same terminal reason and keeps its own
// per-attempt code. The codes are distinct and the terminal reason is not, which
// is the division of labour: Reason says what was wrong with the segment, and
// Terminal says the loop never started.
func TestEveryPreLoopRejectionIsNotEntered(t *testing.T) {
	for _, c := range []struct {
		name   string
		report score.Report
		want   rewrite.RejectionCode
	}{
		{"unscoreable", unscoreable(), rewrite.RejectionUnscoreable},
		{"uncalibrated", uncalibrated(), rewrite.RejectionUncalibrated},
		{"two segments", paragraphs(2), rewrite.RejectionNotOneSegment},
		{"no segments", paragraphs(0), rewrite.RejectionNotOneSegment},
	} {
		t.Run(c.name, func(t *testing.T) {
			loop, _, _, provider, store := loopOver(t,
				map[string]score.Report{original: c.report}, []string{better}, passingGate())

			got := run(t, loop)
			if got.Terminal != rewrite.TerminalNotEntered {
				t.Errorf("Terminal = %q, want %q", got.Terminal, rewrite.TerminalNotEntered)
			}
			if got.Reason != c.want {
				t.Errorf("Reason = %q, want %q", got.Reason, c.want)
			}
			if len(provider.requests) != 0 {
				t.Errorf("the provider was called %d times for a segment that was never judged", len(provider.requests))
			}
			if len(store.attempts) != 0 {
				t.Errorf("%d attempts were recorded for a loop that was never entered", len(store.attempts))
			}
			requireConsistent(t, got, loop.Options.Attempts, len(provider.requests))
		})
	}
}

// An error returns the zero Outcome, so the terminal reason is the empty string
// — which is deliberately not a declared value. A caller cannot mistake a failed
// run for a completed one by reading Terminal alone.
func TestAnErrorCarriesNoTerminalReason(t *testing.T) {
	for _, c := range []struct {
		name string
		loop func() rewrite.Loop
	}{
		{"the provider failed", func() rewrite.Loop {
			return rewrite.Loop{
				Scorer:   &fakeScorer{reports: map[string]score.Report{original: scored(0.90)}},
				Selector: &fakeSelector{exemplars: []string{"an exemplar."}},
				Gate:     passingGate(),
				Provider: &fakeProvider{err: errors.New("provider unavailable")},
				Store:    &fakeStore{},
				Options:  rewrite.Options{ProfileID: "p", InvocationID: "i", ProviderID: "v", Attempts: 3, Exemplars: 1},
			}
		}},
		{"the store failed", func() rewrite.Loop {
			return rewrite.Loop{
				Scorer:   &fakeScorer{reports: map[string]score.Report{original: scored(0.50), worse: scored(0.90)}},
				Selector: &fakeSelector{exemplars: []string{"an exemplar."}},
				Gate:     passingGate(),
				Provider: &fakeProvider{candidates: []string{worse}},
				Store:    failingStore{},
				Options:  rewrite.Options{ProfileID: "p", InvocationID: "i", ProviderID: "v", Attempts: 3, Exemplars: 1},
			}
		}},
		{"the input was invalid", func() rewrite.Loop {
			return rewrite.Loop{
				Scorer:   &fakeScorer{reports: map[string]score.Report{original: scored(0.90)}},
				Selector: &fakeSelector{exemplars: []string{"an exemplar."}},
				Gate:     passingGate(),
				Provider: &fakeProvider{candidates: []string{better}},
				Store:    &fakeStore{},
				Options:  rewrite.Options{ProfileID: "p", InvocationID: "i", ProviderID: "v", Attempts: 0, Exemplars: 1},
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.loop().Rewrite(context.Background(), rewrite.Segment{Text: original, SpanRef: "span-0"})
			if err == nil {
				t.Fatal("no error was returned")
			}
			if got.Terminal != "" {
				t.Errorf("Terminal = %q beside an error, want the zero value", got.Terminal)
			}
			for _, declared := range rewrite.Terminals() {
				if got.Terminal == declared {
					t.Errorf("a failed run reports the declared terminal reason %q", declared)
				}
			}
		})
	}
}

// The terminal reason is additive: nothing about the existing outcome changes.
// B2a's refactor nearly lost three properties by relocating tests, and this
// enum arrives in the same package as an established contract, so the fields it
// sits beside are asserted to still mean what they meant.
func TestTheExistingOutcomeIsUnchangedBesideIt(t *testing.T) {
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{original: scored(0.90), better: scored(0.50), betterYet: scored(0.10)},
		[]string{better, betterYet, worse}, passingGate())
	loop.Scorer.(*fakeScorer).reports[worse] = scored(0.95)

	got := run(t, loop)
	if !got.Changed {
		t.Error("Changed is false after two accepted candidates")
	}
	if got.Text != betterYet {
		t.Errorf("text = %q, want the last accepted candidate", got.Text)
	}
	if got.Reason != rewrite.RejectionNone {
		t.Errorf("Reason = %q on a loop that was entered", got.Reason)
	}
	if len(got.Attempts) != 3 {
		t.Fatalf("%d attempts recorded, want 3", len(got.Attempts))
	}
	for i, attempt := range got.Attempts {
		if attempt.Index != i {
			t.Errorf("attempt %d carries index %d; the index is the attempt number within this paragraph", i, attempt.Index)
		}
	}
	if len(store.attempts) != len(got.Attempts) {
		t.Errorf("the store received %d attempts and the outcome carries %d", len(store.attempts), len(got.Attempts))
	}
	if got.Terminal != rewrite.TerminalExhausted {
		t.Errorf("Terminal = %q, want %q", got.Terminal, rewrite.TerminalExhausted)
	}
}
