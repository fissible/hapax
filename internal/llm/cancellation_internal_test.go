package llm

// #130. A cancelled request whose response arrives anyway must be classified as
// cancellation, not as whatever the completion looked like.
//
// # Contract
//
// `Do` returning an error is already correct: it is returned verbatim, so a
// cancellation that WINS matches `context.Canceled`, ordinarily wrapped in a
// `*url.Error` rather than being the bare sentinel. The gap is the other branch —
// once `Do` succeeds, the status check, the two size checks and `parse` report
// their own errors regardless of the context.
//
// So when an error is being returned AND the context is done, the error carries
// the context's cause for CLASSIFICATION and the response error as DIAGNOSTIC
// text. `DeadlineExceeded` is treated the same. It must NOT also match
// `ErrProvider`: two classifications make a caller's check order significant, and
// exposing wrapped errors is an API commitment.
//
// The SUCCESS path is unchanged. The justification is scope — error
// classification and cancellation of effects are separate contracts — not an
// assumption about what callers do with the result.
//
// # What this does NOT claim
//
// `ctx.Err() != nil` establishes that cancellation was observed WHEN CHECKED, not
// that it preceded the response, and `Do` returning a response does not establish
// that the body completed. "Completed after cancellation" would claim both.
// Nothing here removes the race either: cancellation can arrive immediately after
// the final check. Returning `ctx.Err()` alone would not have been a lie; what
// the wrapping buys is the retained diagnostic, not truer semantics.
//
// # Why this file is in-package, and why it uses a transport
//
// `TestTheExportedSurfaceIsExactlyThis` is a deliberate blunt instrument: the
// package cannot grow an exported name without failing it. The precedence helper
// is therefore unexported, which puts its tests here.
//
// And being here makes the gap DETERMINISTICALLY reachable, which I wrongly
// argued was impossible from outside. Replacing the client's transport lets a
// test cancel the context inside `RoundTrip` and still return a valid response
// with a nil error: a successful non-redirect response proceeds without `Do`
// rejecting it for the context. So every response branch runs with the context
// already done, with no production hook and no timing assumption.
//
// What is NOT here: an end-to-end test over the real server. An earlier draft
// flushed a 500 and cancelled, asserting cancellation under "both" interleavings.
// There are THREE: cancellation makes `Do` fail; `Do` succeeds and cancellation
// is then observed; and `Do` succeeds and classification finishes BEFORE
// cancellation. The third legitimately returns `ErrProvider` even after this fix,
// so that test could have rejected a correct implementation. Real transport
// cancellation is already guarded by `TestAnInFlightRequestIsCancelled`.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/rewrite"
)

// cause is how a context becomes done. `trigger` makes it happen and returns
// only once it has, so nothing here depends on timing.
type cause struct {
	name string
	want error
	// build returns a context and the trigger that ends it.
	build func() (context.Context, func(), func())
}

func causes() []cause {
	return []cause{
		{"cancelled", context.Canceled, func() (context.Context, func(), func()) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, func() { cancel(); <-ctx.Done() }, cancel
		}},
		{"deadline exceeded", context.DeadlineExceeded, func() (context.Context, func(), func()) {
			// A real deadline, triggered by WAITING for it rather than by
			// sleeping a guessed interval: the trigger returns only once
			// Done is closed, so the branch under test always runs after it.
			ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
			return ctx, func() { <-ctx.Done() }, stop
		}},
	}
}

// firingTransport fires the cause and then returns `response` with a nil error,
// so `Rewrite` reaches its response handling with the context already done. When
// `fire` is nil it returns immediately and leaves the context LIVE, which is how
// the body-read case gets to be the thing that ends it.
type firingTransport struct {
	fire     func()
	response *http.Response
}

func (t *firingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if t.fire != nil {
		t.fire()
	}
	return t.response, nil
}

func responseWith(status int, body string, contentLength int64) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: contentLength,
		Header:        make(http.Header),
	}
}

// firingBody fires the cause on its first Read, so the body-read branch is
// reached by a context that was still LIVE when the response arrived — which is
// what makes it a test of LATER cancellation rather than of an already-done one.
type firingBody struct {
	fire func()
	err  error
	read bool
}

func (b *firingBody) Read(p []byte) (int, error) {
	if !b.read {
		b.read = true
		b.fire()
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}

func (b *firingBody) Close() error { return nil }

func localProvider(t *testing.T, transport http.RoundTripper) *provider {
	t.Helper()
	p := newProvider(ProviderOllama, "llama3", DefaultLimits(), 0, true,
		"http://127.0.0.1:11434", nil, nil)
	p.client.Transport = transport
	return p
}

// Every unsuccessful response branch, under BOTH causes, is classified as the
// context's cause and keeps its diagnostic.
//
// Both causes end to end, not only at the helper: a helper wired into `Rewrite`
// only when the cause is `context.Canceled` leaves deadline failures
// misclassified and passes a cancel-only table.
func TestEveryResponseFailureUnderCancellationIsClassifiedAsCancellation(t *testing.T) {
	for _, cause := range causes() {
		t.Run(cause.name, func(t *testing.T) {
			for _, c := range []struct {
				name       string
				response   func(fire func()) *http.Response
				diagnostic string
				// bodyFires leaves the context LIVE when the response arrives and
				// has the body end it, so the branch sees LATER cancellation.
				bodyFires bool
			}{
				{"a non-success status", func(func()) *http.Response {
					return responseWith(http.StatusInternalServerError, "", 0)
				}, "500", false},
				{"a declared length over the limit", func(func()) *http.Response {
					return responseWith(http.StatusOK, "{}", int64(DefaultLimits().MaxResponseBytes)+1)
				}, "exceeds", false},
				{"a body over the limit", func(func()) *http.Response {
					big := strings.Repeat("x", DefaultLimits().MaxResponseBytes+1)
					return responseWith(http.StatusOK, big, -1)
				}, "exceeds", false},
				{"a parse failure", func(func()) *http.Response {
					return responseWith(http.StatusOK, `{"nothing":"useful"}`, -1)
				}, "", false},
				{"a body read failure, the context still live on arrival",
					func(fire func()) *http.Response {
						r := responseWith(http.StatusOK, "", -1)
						r.Body = &firingBody{fire: fire, err: errors.New("unexpected EOF")}
						return r
					}, "unexpected EOF", true},
			} {
				t.Run(c.name, func(t *testing.T) {
					ctx, fire, stop := cause.build()
					defer stop()
					transport := &firingTransport{response: c.response(fire)}
					if !c.bodyFires {
						transport.fire = fire
					}
					p := localProvider(t, transport)

					_, err := p.Rewrite(ctx, rewrite.RewriteRequest{
						Prompt: "a passage", LocalOnly: true,
					})

					if err == nil {
						t.Fatal("no error at all")
					}
					if !errors.Is(err, cause.want) {
						t.Errorf("err = %v, want it to match %v", err, cause.want)
					}
					// ONE classification. Neither of the package's sentinels may
					// survive as a second one: a helper joining the cause with the
					// response error retains `ErrResponseTooLarge` and makes a
					// caller's check order significant.
					for _, sentinel := range []error{ErrProvider, ErrResponseTooLarge} {
						if errors.Is(err, sentinel) {
							t.Errorf("err = %v also matches %v; one classification only",
								err, sentinel)
						}
					}
					if c.diagnostic != "" && !strings.Contains(err.Error(), c.diagnostic) {
						t.Errorf("err = %q, want it to retain the diagnostic %q",
							err.Error(), c.diagnostic)
					}
				})
			}
		})
	}
}

// A request that COMPLETES and parses still returns its text, under both causes,
// with the cause observable before parsing finishes.
func TestACancelledRequestThatSucceedsStillReturnsItsText(t *testing.T) {
	for _, cause := range causes() {
		t.Run(cause.name, func(t *testing.T) {
			ctx, fire, stop := cause.build()
			defer stop()
			transport := &firingTransport{
				fire:     fire,
				response: responseWith(http.StatusOK, `{"response":"rewritten"}`, -1),
			}
			p := localProvider(t, transport)

			got, err := p.Rewrite(ctx, rewrite.RewriteRequest{
				Prompt: "a passage", LocalOnly: true,
			})

			if err != nil {
				t.Fatalf("a %s but SUCCESSFUL request returned %v; classification "+
					"applies to an error being returned, not to success",
					cause.name, err)
			}
			if got != "rewritten" {
				t.Errorf("text = %q, want %q", got, "rewritten")
			}
		})
	}
}

// The precedence rule itself, over both causes and every error class.
func TestCancellationPrecedenceClassifiesAndRetains(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer stop()

	for _, c := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled", cancelled, context.Canceled},
		{"deadline exceeded", expired, context.DeadlineExceeded},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, response := range []error{
				fmt.Errorf("%w: status 500", ErrProvider),
				ErrResponseTooLarge,
				errors.New("unexpected EOF"),
			} {
				got := cancellationPrecedence(c.ctx, response)
				if !errors.Is(got, c.want) {
					t.Errorf("err = %v, want it to match %v", got, c.want)
				}
				// The TEXT, not merely `errors.Is`: a wrapper can expose the
				// original through Unwrap while hiding its message.
				if !strings.Contains(got.Error(), response.Error()) {
					t.Errorf("err = %q, want it to contain the diagnostic %q",
						got.Error(), response.Error())
				}
				// Neither sentinel survives as a classification, and the
				// original is retained as TEXT rather than through wrapping:
				// `errors.Is(got, response)` would make it a second class.
				for _, sentinel := range []error{ErrProvider, ErrResponseTooLarge} {
					if errors.Is(got, sentinel) {
						t.Errorf("err = %v matches %v as well as %v; one "+
							"classification only", got, sentinel, c.want)
					}
				}
				if errors.Is(got, response) {
					t.Errorf("err = %v exposes the response error through wrapping; "+
						"it is a diagnostic, not a classification", got)
				}
			}
			// A nil error stays nil: classification applies to an error being
			// returned, never to success.
			if got := cancellationPrecedence(c.ctx, nil); got != nil {
				t.Errorf("a successful response became %v", got)
			}
		})
	}
}

// With a live context the response error is returned untouched.
func TestALiveContextLeavesTheResponseErrorAlone(t *testing.T) {
	live := context.Background()
	for _, response := range []error{
		fmt.Errorf("%w: status 500", ErrProvider),
		ErrResponseTooLarge,
		errors.New("unexpected EOF"),
	} {
		if got := cancellationPrecedence(live, response); got != response {
			t.Errorf("err = %v, want %v returned unchanged", got, response)
		}
	}
	if got := cancellationPrecedence(live, nil); got != nil {
		t.Errorf("a nil error became %v", got)
	}
}
