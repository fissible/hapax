package cli_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/cli"
)

func TestInterruptedJSONReportsPublicationStateOnlyOnStderr(t *testing.T) {
	for _, c := range tails() {
		for _, phase := range []string{"before admission", "published and recorded", "recording failed"} {
			t.Run(c.name+"/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				service := &cancellingRecorder{rewriteService: improvedService(t)}
				publisher := &spyPublisher{before: cancel}
				failure := errors.New("evidence store unavailable")
				if phase == "before admission" {
					cancel()
				} else if phase == "recording failed" {
					service.err = failure
				}
				draft := tempDraft(t)
				run := interruptedWith(t, ctx, service, publisher, append(c.args(draft), "--json")...)
				if run.code != 3 || run.stdout != "" || !strings.Contains(run.stderr, "interrupted") {
					t.Fatalf("exit %d, stdout %q, stderr %q", run.code, run.stdout, run.stderr)
				}
				switch phase {
				case "before admission":
					if !strings.Contains(run.stderr, "nothing was published") || len(publisher.published) != 0 {
						t.Fatalf("published %d; stderr %q", len(publisher.published), run.stderr)
					}
					if calls, _ := service.observed(); calls != 0 {
						t.Fatalf("recorder called %d times before admission", calls)
					}
				case "published and recorded":
					requirePublishedAndRecorded(t, run.stderr, c.destination(draft))
				case "recording failed":
					requirePublishedAndNotRecorded(t, run.stderr, c.destination(draft))
					if !strings.Contains(run.stderr, failure.Error()) {
						t.Fatalf("underlying error missing: %q", run.stderr)
					}
				}
			})
		}
	}
}

func TestExpiredRewriteDeadlinePublishesNothing(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	service := &cancellingRecorder{rewriteService: improvedService(t)}
	publisher := &spyPublisher{}
	draft := tempDraft(t)
	run := interruptedWith(t, ctx, service, publisher, out(draft, draft+".revised")...)
	if run.code != 3 || run.stdout != "" || !strings.Contains(run.stderr, "nothing was published") || !strings.Contains(run.stderr, context.DeadlineExceeded.Error()) {
		t.Fatalf("exit %d, stdout %q, stderr %q", run.code, run.stdout, run.stderr)
	}
	if calls, _ := service.observed(); calls != 0 || len(publisher.published) != 0 {
		t.Fatalf("published %d, recorder called %d", len(publisher.published), calls)
	}
}

type cancelOnResultWriter struct {
	output strings.Builder
	cancel context.CancelFunc
}

func (w *cancelOnResultWriter) Write(p []byte) (int, error) {
	w.cancel()
	return w.output.Write(p)
}

func TestCancellationDuringResultRenderingDoesNotChangeCompletedExit(t *testing.T) {
	for _, json := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stdout := &cancelOnResultWriter{cancel: cancel}
		var stderr strings.Builder
		service := &cancellingRecorder{rewriteService: improvedService(t)}
		publisher := &spyPublisher{}
		draft := tempDraft(t)
		args := out(draft, draft+".revised")
		if json {
			args = append(args, "--json")
		}
		code := cli.Run(ctx, args, cli.Deps{
			Stdout: stdout, Stderr: &stderr,
			Env:      func(string) (string, bool) { return "", false },
			ReadFile: os.ReadFile,
			Getwd:    func() (string, error) { return "/somewhere", nil },
			Service:  service, Publisher: publisher,
		})
		if code != 0 || stdout.output.Len() == 0 || stderr.Len() != 0 || ctx.Err() == nil {
			t.Fatalf("json=%v: exit %d, stdout %q, stderr %q, cancellation %v", json, code, stdout.output.String(), stderr.String(), ctx.Err())
		}
		if calls, records := service.observed(); calls != 1 || records != 1 || len(publisher.published) != 1 {
			t.Fatalf("published %d, recorder called %d, recorded %d", len(publisher.published), calls, records)
		}
	}
}
