package rewrite_test

// #149, slice 1. The loop must not start another provider attempt after it has
// observed cancellation.
//
// # Contract
//
// Checked before EVERY attempt, including the first, and surfaced as an error
// rather than as a terminal or a refusal. `Terminals()` is a closed vocabulary of
// three values and none of them means interrupted, so inventing one would widen a
// published contract to describe something that is not an outcome of the loop's
// own reasoning. A refusal would be worse: it would claim this candidate was
// judged and declined.
//
// Attempts already RECORDED stay recorded. The returned outcome is empty, but the
// store keeps what it was given, which is the benign shape — an accepted attempt
// with no publication cannot exclude the author's prose, because since #134 both
// screens read `published_paragraph`.
//
// # Scope
//
// This slice makes the loop and the publication tail safe GIVEN a cancellable
// context. Supplying one — `signal.NotifyContext` in the composition root, prompt
// unregistration so a second signal still terminates, and the subprocess tests
// that prove real SIGINT and SIGTERM arrive — is slice 2, because `main` consumes
// this behaviour rather than providing it.
//
// # Evidence
//
// The production provider already checks cancellation at entry (internal/llm,
// `Rewrite`) and its HTTP request carries the context, and `Execute` checks
// between target paragraphs. So this check is not about avoiding spend: an
// already-dispatched request can still cost money, and three attempts are the
// default PER TARGET rather than per invocation. What it buys is the orchestration
// guarantee — "no further attempt after cancellation is observed" — holding
// independently of any provider implementation.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// loopOverWithProvider is `loopOver` with the provider supplied, since these
// tests need one that reacts to being called.
func loopOverWithProvider(t *testing.T, reports map[string]score.Report, provider rewrite.Provider, gate *fakeGate) (rewrite.Loop, *fakeScorer, *fakeSelector, rewrite.Provider, *fakeStore) {
	t.Helper()
	loop, scorer, selector, _, store := loopOver(t, reports, nil, gate)
	loop.Provider = provider
	return loop, scorer, selector, provider, store
}

// segmentUnderTest is the segment `run` uses, named so these tests read the same.
func segmentUnderTest() rewrite.Segment {
	return rewrite.Segment{Text: original, SpanRef: "span-0"}
}

// cancellingProvider cancels the context after serving `after` candidates, so the
// loop's next iteration begins with cancellation already observable.
type cancellingProvider struct {
	candidates []string
	after      int
	cancel     context.CancelFunc
	calls      int
	// liveOnEntry and cancelledByCaller record what the received context did, so
	// a loop handing the provider `context.WithoutCancel(ctx)` — or a context
	// from an unrelated parent — is caught. Cancellability alone is not the
	// claim: it must be THIS caller's cancellation that arrives.
	liveOnEntry       bool
	cancelledByCaller bool
}

func (p *cancellingProvider) Rewrite(ctx context.Context, _ rewrite.RewriteRequest) (string, error) {
	p.calls++
	if p.calls == p.after {
		p.liveOnEntry = ctx.Done() != nil && ctx.Err() == nil
		p.cancel()
		select {
		case <-ctx.Done():
			p.cancelledByCaller = errors.Is(ctx.Err(), context.Canceled)
		case <-time.After(10 * time.Second):
		}
	}
	if p.calls > len(p.candidates) {
		return "", nil
	}
	// Returning the candidate SUCCESSFULLY, so what stops the next attempt is
	// the loop rather than this provider failing.
	return p.candidates[p.calls-1], nil
}

// No further attempt is made once cancellation is observable, and the error says
// cancellation rather than inventing a terminal.
func TestTheLoopStopsBeforeTheNextAttemptAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancellingProvider{
		// The first candidate IMPROVES and is accepted, so the loop holds
		// rewritten prose when cancellation arrives — which is what makes
		// "return the zero outcome" a real claim rather than a restatement of
		// having nothing to return.
		candidates: []string{better, betterYet},
		after:      1,
		cancel:     cancel,
	}

	loop, _, _, _, store := loopOverWithProvider(t,
		map[string]score.Report{
			original: scored(0.50), better: scored(0.40), betterYet: scored(0.30),
		},
		provider, passingGate())

	outcome, err := loop.Rewrite(ctx, segmentUnderTest())

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to match context.Canceled", err)
	}
	if provider.calls != 1 {
		t.Errorf("the provider was called %d times, want 1: the second attempt "+
			"began after cancellation was observable", provider.calls)
	}
	// The context the provider RECEIVED was this caller's, still live on entry and
	// cancelled by this caller's cancel. A loop forwarding a detached context
	// would leave both of these false.
	if !provider.liveOnEntry {
		t.Error("the provider's context was already done or had no Done channel " +
			"on entry; the loop did not forward a live cancellable context")
	}
	if !provider.cancelledByCaller {
		t.Error("the provider's context was not cancelled by the caller's cancel; " +
			"the loop forwarded a context detached from it")
	}
	// The first attempt's record survives. An accepted-or-refused attempt with no
	// publication is the benign shape; discarding it would lose evidence the run
	// really produced.
	if len(store.attempts) != 1 {
		t.Errorf("%d attempts recorded, want the first one kept", len(store.attempts))
	}
	// And the COMPLETE outcome is empty, including Text and Attempts. Returning
	// the partial outcome would hand a caller rewritten prose for a run the user
	// stopped.
	if !reflect.DeepEqual(outcome, rewrite.Outcome{}) {
		t.Errorf("outcome = %+v, want the zero value", outcome)
	}
}

// Cancelled before the FIRST attempt, nothing is asked at all.
func TestTheLoopAsksNothingWhenCancelledBeforeTheFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &cancellingProvider{candidates: []string{better}, after: 0}

	loop, _, _, _, store := loopOverWithProvider(t,
		map[string]score.Report{original: scored(0.50), better: scored(0.40)},
		provider, passingGate())

	outcome, err := loop.Rewrite(ctx, segmentUnderTest())

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to match context.Canceled", err)
	}
	if provider.calls != 0 {
		t.Errorf("the provider was called %d times, want none", provider.calls)
	}
	if len(store.attempts) != 0 {
		t.Errorf("%d attempts recorded, want none", len(store.attempts))
	}
	// The COMPLETE zero value, not a field-by-field check: no refusal, no
	// completed rewrite, no terminal, and no Text or Attempts either.
	if !reflect.DeepEqual(outcome, rewrite.Outcome{}) {
		t.Errorf("outcome = %+v, want the zero value: an interrupted loop judged "+
			"nothing and must not report a terminal, a refusal or any prose", outcome)
	}
}
