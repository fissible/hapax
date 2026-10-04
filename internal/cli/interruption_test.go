package cli_test

// #149, slice 1. Interruption must not leave the destination published without
// its screening evidence.
//
// # Contract
//
// There is an ADMISSION BOUNDARY immediately before publication. Cancellation
// observed before it means nothing is published and nothing is recorded.
// Cancellation arriving after it takes the protected path: the publication
// completes and its required evidence is recorded under a context that carries
// the caller's values but not its cancellation.
//
// The guarantee is bounded, deliberately. It is NOT "publication and evidence
// finish together" — a disk or database failure still ends either. It is "once
// publication is admitted, caller cancellation does not abandon the publication
// or suppress required evidence recording after a successful publication". There
// is necessarily a race between the final check and entering the publisher, and
// nothing here promises a Ctrl-C's wall-clock arrival precedes every subsequent
// filesystem action.
//
// # Exit code
//
// 3, with the documented meaning widened to include interrupted execution rather
// than the five-value contract widened to six. 4 is a deliberate refusal under
// the tool's own domain rules carrying a reason from a closed set, which a user's
// Ctrl-C is not.
//
// EXIT 3 MUST NOT IMPLY THE DESTINATION IS UNCHANGED. That is the point these
// tests exist to protect: an interrupted run that published and recorded still
// exits 3, and its diagnostic has to say so, or a script reading only the code
// will conclude the file was untouched.
//
// # Scope
//
// Slice 1 makes this tail safe GIVEN a cancellable context. Supplying one —
// `signal.NotifyContext` in the composition root, prompt unregistration so a
// second signal still terminates, and subprocess tests proving real SIGINT and
// SIGTERM arrive — is slice 2. `main` consumes this behaviour rather than
// providing it, so it is the root of the dependency rather than its leaf.
//
// Two exposures stay OPEN and are documented rather than closed here. A hard kill
// during staging can leave a `.hapax-*` file holding rewritten prose, at the
// source's permissions rather than 0600; no userspace code prevents that, and
// sweeping the directory is refused because a filename, an age and a PID
// establish neither ownership nor abandonment, so a concurrent run's staging file
// is indistinguishable from an abandoned one. And a publisher that renames
// successfully and then fails on directory sync or cleanup still causes this
// command to record nothing — which publication_test.go's header already states,
// and which this slice does not change.

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

// cancellingRecorder honours its context, which the shipped fakes do not. Without
// that, removing the detached context leaves the suite green.
//
// It counts CALLS separately from successful records, because the two are
// different observations: an implementation that wrongly hands this recorder a
// cancelled context returns early and appends nothing, so "nothing recorded"
// alone cannot tell that apart from never being called.
type cancellingRecorder struct {
	*rewriteService
	err error
	// entered and release, when both set, let a test cancel the caller's context
	// while recording is in progress and then control when it returns.
	entered chan struct{}
	release chan struct{}
	// finished is closed once recording has returned, so a test can observe
	// completion rather than inferring it.
	finished chan struct{}

	mu       sync.Mutex
	calls    int
	recorded []workflow.Publication
	seen     map[any]any
	done     <-chan struct{}
	ctxErr   error
	deadline bool
}

type firstKey struct{}
type secondKey struct{}

func (r *cancellingRecorder) RecordPublication(ctx context.Context, p workflow.Publication) error {
	r.mu.Lock()
	r.calls++
	r.seen = map[any]any{firstKey{}: ctx.Value(firstKey{}), secondKey{}: ctx.Value(secondKey{})}
	r.done, r.ctxErr = ctx.Done(), ctx.Err()
	_, r.deadline = ctx.Deadline()
	r.mu.Unlock()

	if r.entered != nil {
		close(r.entered)
		<-r.release
	}
	if ctx.Err() != nil {
		if r.finished != nil {
			close(r.finished)
		}
		return ctx.Err()
	}
	r.mu.Lock()
	r.recorded = append(r.recorded, p)
	r.mu.Unlock()
	if r.finished != nil {
		close(r.finished)
	}
	return r.err
}

func (r *cancellingRecorder) observed() (calls, records int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, len(r.recorded)
}

// cancellingService returns a publishable outcome and cancels the caller's
// context immediately before it does, which is where the admission boundary has
// to be: at command ENTRY is too early to catch this, and a check only there
// would pass while still publishing.
type cancellingService struct {
	// The COUNTING recorder, not the no-op one it would otherwise inherit from
	// *rewriteService. With the no-op, a mutation that checks cancellation at
	// entry and then records evidence at a cancelled admission without
	// publishing is invisible.
	*cancellingRecorder
	cancel context.CancelFunc
}

func (s *cancellingService) Rewrite(ctx context.Context, request workflow.RewriteInput) (workflow.RewriteOutcome, error) {
	outcome, err := s.cancellingRecorder.rewriteService.Rewrite(ctx, request)
	s.cancel()
	return outcome, err
}

// Nothing is published or recorded when cancellation is observable before the
// admission boundary, in either of two places: before the command starts, and
// during the rewrite that precedes publication.
func TestCancellationBeforeAdmissionPublishesNothing(t *testing.T) {
	for _, when := range []struct {
		name string
		// pre cancels before the command starts; otherwise the service cancels
		// immediately before returning a publishable outcome, which is the
		// position a check at command ENTRY cannot see.
		pre bool
	}{
		{"cancelled before the command starts", true},
		{"cancelled during the rewrite", false},
	} {
		for _, c := range tails() {
			t.Run(when.name+"/"+c.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				recorder := &cancellingRecorder{rewriteService: improvedService(t)}
				var service workflow.Service = recorder
				if when.pre {
					cancel()
				} else {
					service = &cancellingService{cancellingRecorder: recorder, cancel: cancel}
				}
				publisher := &spyPublisher{}
				draft := tempDraft(t)

				run := interruptedWith(t, ctx, service, publisher, c.args(draft)...)

				if len(publisher.published) != 0 {
					t.Errorf("the publisher was called %d times, want none",
						len(publisher.published))
				}
				// CALLS and records, unconditionally. A recorder handed a
				// cancelled context returns early and appends nothing, which
				// "nothing recorded" alone cannot distinguish from never being
				// asked — and the rewrite-time rows used to observe neither.
				if calls, records := recorder.observed(); calls != 0 || records != 0 {
					t.Errorf("the recorder was called %d times and recorded %d, want "+
						"none of either", calls, records)
				}
				if run.code != 3 {
					t.Errorf("exit = %d, want 3", run.code)
				}
				if !strings.Contains(strings.ToLower(run.stderr), "interrupt") {
					t.Errorf("stderr = %q, want it to name the interruption", run.stderr)
				}
				if strings.TrimSpace(run.stdout) != "" {
					t.Errorf("stdout = %q, want empty", run.stdout)
				}
			})
		}
	}
}

// Cancelled while the publisher is working: the evidence is still recorded —
// ALL of it — and the detached context keeps the caller's values while carrying
// no cancellation at all.
//
// Both destinations, because `create` and `replace` are different code paths in
// this command and a tail that detached one while forwarding the cancelled
// context to the other would otherwise pass.
func TestCancellationInsideThePublisherStillRecordsEvidence(t *testing.T) {
	for _, c := range tails() {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			service := &cancellingRecorder{rewriteService: improvedService(t)}
			want := service.rewriteService.result.Publication()
			publisher := &spyPublisher{before: func() { cancel() }}
			draft := tempDraft(t)

			run := interruptedWith(t, ctx, service, publisher, c.args(draft)...)

			if len(publisher.published) != 1 {
				t.Fatalf("published %d times, want 1", len(publisher.published))
			}
			calls, records := service.observed()
			if calls != 1 || records != 1 {
				t.Fatalf("the recorder was called %d times and recorded %d, want 1 and 1",
					calls, records)
			}
			// The WHOLE publication, not just that one call happened. The fixture
			// carries two paragraphs, so truncating the batch when interrupted
			// would otherwise satisfy a count of one.
			service.mu.Lock()
			got := service.recorded[0]
			seen, done, ctxErr, deadline := service.seen, service.done, service.ctxErr, service.deadline
			service.mu.Unlock()
			if len(want.Paragraphs) < 2 {
				t.Fatalf("the fixture carries %d paragraphs; it needs more than one "+
					"for truncation to be visible", len(want.Paragraphs))
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("recorded publication:\n got  %+v\n want %+v", got, want)
			}

			// The EXACT values, under two distinct private keys.
			if seen[firstKey{}] != "first" || seen[secondKey{}] != "second" {
				t.Errorf("the recorder saw values %v, want both of the caller's; the "+
					"tail substituted a context rather than stripping only its "+
					"cancellation", seen)
			}
			// Done() nil, not merely Err() nil: a wrapper masking Err() while
			// keeping the caller's cancelled Done() passes an Err-only check, and
			// a database operation selecting on Done can still abort.
			if done != nil {
				t.Error("the detached context has a Done channel")
			}
			if ctxErr != nil {
				t.Errorf("the detached context reports %v, want nil", ctxErr)
			}
			if deadline {
				t.Error("the detached context inherited a deadline")
			}

			if run.code != 3 {
				t.Errorf("exit = %d, want 3", run.code)
			}
			requirePublishedAndRecorded(t, run.stderr, c.destination(draft))
			if strings.TrimSpace(run.stdout) != "" {
				t.Errorf("stdout = %q, want empty", run.stdout)
			}
		})
	}
}

// A caller DEADLINE must not be inherited either, which `Done() == nil` alone
// would not show if the implementation detached cancellation but kept a timeout.
//
// The successful execution is REQUIRED first. The default observations are
// already `deadline == false` and `done == nil`, so a mutation skipping evidence
// entirely whenever the caller has a deadline would otherwise pass by never
// giving this anything to inspect.
func TestTheDetachedContextDoesNotInheritADeadline(t *testing.T) {
	for _, c := range tails() {
		t.Run(c.name, func(t *testing.T) { detachedDeadline(t, c) })
	}
}

func detachedDeadline(t *testing.T, c tail) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	service := &cancellingRecorder{rewriteService: improvedService(t)}
	publisher := &spyPublisher{}

	draft := tempDraft(t)
	run := interruptedWith(t, ctx, service, publisher, c.args(draft)...)

	if run.code != 0 {
		t.Fatalf("exit = %d, want 0: this run was never interrupted", run.code)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("published %d times, want 1", len(publisher.published))
	}
	calls, records := service.observed()
	if calls != 1 || records != 1 {
		t.Fatalf("the recorder was called %d times and recorded %d, want 1 and 1; "+
			"there is nothing to inspect otherwise", calls, records)
	}

	service.mu.Lock()
	deadline, done := service.deadline, service.done
	service.mu.Unlock()
	if deadline {
		t.Error("the recorder's context carries a deadline inherited from the caller")
	}
	if done != nil {
		t.Error("the recorder's context has a Done channel")
	}
}

// Cancelled while recording is already in progress: recording runs to completion
// AND the command waits for it, which are two claims. The release is held until
// completion is observed, so a fire-and-forget recorder cannot finish early and
// pass by accident.
func TestCancellationDuringRecordingLetsItFinish(t *testing.T) {
	for _, c := range tails() {
		t.Run(c.name, func(t *testing.T) { cancellationDuringRecording(t, c) })
	}
}

// A mutation detaching only the `create` path's recording context, or detaching
// `replace`'s only when it was already cancelled, survives a single-destination
// test: publisher-time cancellation passes while cancellation DURING recording
// loses the evidence.
func cancellationDuringRecording(t *testing.T, c tail) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &cancellingRecorder{
		rewriteService: improvedService(t),
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
		finished:       make(chan struct{}),
	}
	publisher := &spyPublisher{}
	draft := tempDraft(t)

	returned := make(chan publishRun, 1)
	go func() {
		returned <- interruptedWith(t, ctx, service, publisher, c.args(draft)...)
	}()

	select {
	case <-service.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("recording was never entered")
	}
	cancel()
	// The command must not have returned while recording is still held.
	select {
	case run := <-returned:
		t.Fatalf("the command returned (exit %d) while recording was still "+
			"in progress; it did not wait", run.code)
	case <-time.After(50 * time.Millisecond):
	}
	close(service.release)

	select {
	case <-service.finished:
	case <-time.After(10 * time.Second):
		t.Fatal("recording never finished")
	}
	var run publishRun
	select {
	case run = <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the command never returned after recording finished")
	}

	calls, records := service.observed()
	if calls != 1 || records != 1 {
		t.Errorf("the recorder was called %d times and recorded %d, want 1 and 1: "+
			"recording was abandoned rather than allowed to finish", calls, records)
	}
	if len(publisher.published) != 1 {
		t.Errorf("published %d times, want 1", len(publisher.published))
	}
	if run.code != 3 {
		t.Errorf("exit = %d, want 3", run.code)
	}
	requirePublishedAndRecorded(t, run.stderr, c.destination(draft))
	if strings.TrimSpace(run.stdout) != "" {
		t.Errorf("stdout = %q, want empty", run.stdout)
	}
}

// An interruption must not swallow a real evidence failure and announce durable
// evidence that does not exist.
func TestAnEvidenceFailureDuringInterruptionIsDisclosed(t *testing.T) {
	for _, c := range tails() {
		t.Run(c.name, func(t *testing.T) { evidenceFailureDuringInterruption(t, c) })
	}
}

// Both destinations, because a mutation clearing the recorder's error when
// `action == replace && ctx.Err() != nil` passes the whole suite on `--out`
// alone: it suppresses the database error and falsely announces recorded
// evidence.
func evidenceFailureDuringInterruption(t *testing.T, c tail) {
	sentinel := errors.New("the database is unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	service := &cancellingRecorder{rewriteService: improvedService(t), err: sentinel}
	publisher := &spyPublisher{before: func() { cancel() }}

	draft := tempDraft(t)
	run := interruptedWith(t, ctx, service, publisher, c.args(draft)...)

	if len(publisher.published) != 1 {
		t.Fatalf("published %d times, want 1", len(publisher.published))
	}
	if calls, _ := service.observed(); calls != 1 {
		t.Fatalf("the recorder was called %d times, want 1", calls)
	}
	if run.code != 3 {
		t.Errorf("exit = %d, want 3", run.code)
	}
	low := strings.ToLower(run.stderr)
	if !strings.Contains(run.stderr, sentinel.Error()) {
		t.Errorf("stderr = %q, want the underlying error %q retained",
			run.stderr, sentinel)
	}
	requirePublishedAndNotRecorded(t, run.stderr, c.destination(draft))
	_ = low
	if strings.TrimSpace(run.stdout) != "" {
		t.Errorf("stdout = %q, want empty", run.stdout)
	}
}

// An interrupted publication with NO evidence obligation must not claim it
// recorded any.
//
// The uninterrupted rows below cannot see this: an implementation that reports
// "publication evidence recorded" unconditionally after an interrupted
// publication passes them with an empty batch and zero recorder calls.
func TestAnInterruptedPublicationWithNoObligationClaimsNoEvidence(t *testing.T) {
	for _, c := range tails() {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			service := &cancellingRecorder{
				rewriteService: &rewriteService{result: improved("revised\n")},
			}
			if len(service.rewriteService.result.Publication().Paragraphs) != 0 {
				t.Fatal("this fixture is supposed to carry NO publication evidence")
			}
			publisher := &spyPublisher{before: func() { cancel() }}
			draft := tempDraft(t)

			run := interruptedWith(t, ctx, service, publisher, c.args(draft)...)

			if len(publisher.published) != 1 {
				t.Fatalf("published %d times, want 1", len(publisher.published))
			}
			if calls, records := service.observed(); calls != 0 || records != 0 {
				t.Errorf("the recorder was called %d times and recorded %d, want "+
					"none of either: the batch is empty", calls, records)
			}
			if run.code != 3 {
				t.Errorf("exit = %d, want 3", run.code)
			}
			requireAffirmativePublication(t, run.stderr, c.destination(draft))
			if !strings.Contains(strings.ToLower(run.stderr), "interrupt") {
				t.Errorf("stderr = %q, want it to name the interruption", run.stderr)
			}
			// And it must NOT claim evidence it never had to record.
			if strings.Contains(strings.ToLower(run.stderr), "evidence recorded") {
				t.Errorf("stderr = %q claims recorded evidence, but there was no "+
					"obligation and the recorder was never called", run.stderr)
			}
			if strings.TrimSpace(run.stdout) != "" {
				t.Errorf("stdout = %q, want empty", run.stdout)
			}
		})
	}
}

// No evidence obligation, no record — and the diagnostics must tell the two
// no-record cases apart.
func TestNoEvidenceObligationManufacturesNoRecord(t *testing.T) {
	for _, c := range []struct {
		name    string
		outcome workflow.RewriteOutcome
		args    func(string) []string
		// wantPublished is false for the nothing-to-change path, which publishes
		// nothing at all, and true for a publication carrying no evidence.
		wantPublished bool
	}{
		{"nothing to change", nothingToChange("unchanged\n"), inPlace, false},
		{"published with an empty evidence batch", improved("revised\n"),
			func(d string) []string { return out(d, d+".revised") }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			service := &cancellingRecorder{rewriteService: &rewriteService{result: c.outcome}}
			publisher := &spyPublisher{}

			run := interruptedWith(t, context.Background(), service, publisher,
				c.args(tempDraft(t))...)

			if calls, records := service.observed(); calls != 0 || records != 0 {
				t.Errorf("the recorder was called %d times and recorded %d, want "+
					"none of either: there was no evidence to record", calls, records)
			}
			if published := len(publisher.published) != 0; published != c.wantPublished {
				t.Errorf("published = %v, want %v", published, c.wantPublished)
			}
			if run.code != 0 {
				t.Errorf("exit = %d, want 0: nothing here is an interruption or a "+
					"failure", run.code)
			}
		})
	}
}

// Cancellation combined with a publisher failure must not record, and must not
// announce a success.
func TestCancellationWithAPublisherFailureRecordsNothing(t *testing.T) {
	for _, c := range tails() {
		t.Run(c.name, func(t *testing.T) { cancellationWithPublisherFailure(t, c) })
	}
}

// Parameterized because a mutation clearing the publisher's error when
// `action == replace && ctx.Err() != nil` survived the `--out` case alone: it
// recorded evidence despite the failure and suppressed the error text.
func cancellationWithPublisherFailure(t *testing.T, c tail) {
	ctx, cancel := context.WithCancel(context.Background())
	failure := errors.New("disk full")
	service := &cancellingRecorder{rewriteService: improvedService(t)}
	publisher := &spyPublisher{err: failure, before: func() { cancel() }}

	draft := tempDraft(t)
	run := interruptedWith(t, ctx, service, publisher, c.args(draft)...)

	if calls, records := service.observed(); calls != 0 || records != 0 {
		t.Errorf("the recorder was called %d times and recorded %d, want none: the "+
			"publisher failed", calls, records)
	}
	if run.code != 3 {
		t.Errorf("exit = %d, want 3", run.code)
	}
	if !strings.Contains(run.stderr, failure.Error()) {
		t.Errorf("stderr = %q, want the publisher's error retained", run.stderr)
	}
	if strings.TrimSpace(run.stdout) != "" {
		t.Errorf("stdout = %q, want empty", run.stdout)
	}
}

// An uninterrupted run is untouched by any of this.
func TestAnUninterruptedRunIsUnchanged(t *testing.T) {
	service := &cancellingRecorder{rewriteService: improvedService(t)}
	publisher := &spyPublisher{}

	draft := tempDraft(t)
	run := interruptedWith(t, context.Background(), service, publisher,
		out(draft, draft+".revised")...)

	if run.code != 0 {
		t.Errorf("exit = %d, want 0", run.code)
	}
	calls, records := service.observed()
	if len(publisher.published) != 1 || calls != 1 || records != 1 {
		t.Errorf("published %d, called %d, recorded %d; want 1, 1, 1",
			len(publisher.published), calls, records)
	}
	if strings.Contains(strings.ToLower(run.stderr), "interrupt") {
		t.Errorf("stderr = %q names an interruption on a run that was never "+
			"interrupted", run.stderr)
	}
	if strings.TrimSpace(run.stdout) == "" {
		t.Error("stdout is empty on a completed run")
	}
}

// ---------------------------------------------------------------------------
// Driving the command with a context the test controls
// ---------------------------------------------------------------------------

// improvedService is a rewriteService whose outcome carries publication evidence,
// so the publication tail has something it is obliged to record.
func improvedService(t *testing.T) *rewriteService {
	t.Helper()
	return &rewriteService{result: publishedOutcome(strings.Repeat("c", 64))}
}

// interruptedWith is `publishing` with the context and the service supplied. The
// shipped helper always passes context.Background() and a *recordingService.
func interruptedWith(t *testing.T, ctx context.Context, service workflow.Service, publisher *spyPublisher, args ...string) publishRun {
	t.Helper()
	// Two values under two distinct private keys, so a tail that detaches
	// cancellation by substituting a context — or by copying one known key —
	// can be told apart from one that strips only the cancellation.
	ctx = context.WithValue(ctx, firstKey{}, "first")
	ctx = context.WithValue(ctx, secondKey{}, "second")
	var events []string
	publisher.events = &events
	out := &recordingWriter{events: &events}
	var errOut strings.Builder
	code := cli.Run(ctx, args, cli.Deps{
		Stdout: out, Stderr: &errOut,
		Env:       func(string) (string, bool) { return "", false },
		ReadFile:  os.ReadFile,
		Getwd:     func() (string, error) { return "/somewhere", nil },
		Service:   service,
		Publisher: publisher,
	})
	return publishRun{code: code, stdout: out.String(), stderr: errOut.String(), events: events}
}

// tails pairs each destination's flags with the path the diagnostics must name,
// so the protected-tail cases can run against both `create` and `replace`.
type tail struct {
	name        string
	args        func(string) []string
	destination func(string) string
}

func tails() []tail {
	return []tail{
		{"--out",
			func(d string) []string { return out(d, d+".revised") },
			func(d string) string { return d + ".revised" }},
		{"--in-place", inPlace, func(d string) string { return d }},
	}
}

// requirePublishedAndRecorded pins the AFFIRMATIVE meaning. Matching "recorded"
// alone accepts "publication evidence not recorded", and naming the destination
// alone accepts calling it unchanged — both of which are the opposite claim.
func requirePublishedAndRecorded(t *testing.T, stderr, destination string) {
	t.Helper()
	low := strings.ToLower(stderr)
	if !strings.Contains(low, "interrupt") {
		t.Errorf("stderr = %q, want it to name the interruption", stderr)
	}
	requireAffirmativePublication(t, stderr, destination)
	if !strings.Contains(low, "recorded") {
		t.Errorf("stderr = %q, want it to say the evidence was recorded", stderr)
	}
	for _, denial := range []string{"not recorded", "could not record", "unchanged"} {
		if strings.Contains(low, denial) {
			t.Errorf("stderr = %q contains %q, which asserts the opposite of what "+
				"happened: the destination changed and the evidence exists",
				stderr, denial)
		}
	}
}

// requirePublishedAndNotRecorded is its mirror, for the case where publication
// succeeded and recording genuinely failed.
func requirePublishedAndNotRecorded(t *testing.T, stderr, destination string) {
	t.Helper()
	low := strings.ToLower(stderr)
	requireAffirmativePublication(t, stderr, destination)
	if !strings.Contains(low, "not recorded") && !strings.Contains(low, "could not record") {
		t.Errorf("stderr = %q, want an explicit recording-failure clause; a bare "+
			"error string lets a reader assume the evidence exists", stderr)
	}
	if strings.Contains(low, "unchanged") {
		t.Errorf("stderr = %q calls the destination unchanged after publishing it",
			stderr)
	}
}

// requireAffirmativePublication pins that the destination was PUBLISHED, not
// merely mentioned. Naming it is not enough: "did not publish <destination>"
// names it while asserting the opposite, and both diagnostic helpers accepted
// that until this existed.
func requireAffirmativePublication(t *testing.T, stderr, destination string) {
	t.Helper()
	low := strings.ToLower(stderr)
	if !strings.Contains(low, strings.ToLower(destination)) {
		t.Errorf("stderr = %q, want the exact destination %q", stderr, destination)
	}
	if !strings.Contains(low, "published") {
		t.Errorf("stderr = %q, want an affirmative publication clause: exit 3 must "+
			"not imply the destination is unchanged", stderr)
	}
	for _, denial := range []string{"did not publish", "not published", "nothing was published"} {
		if strings.Contains(low, denial) {
			t.Errorf("stderr = %q contains %q, but the destination WAS published",
				stderr, denial)
		}
	}
}
