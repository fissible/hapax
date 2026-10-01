package cli_test

// #134's ordering, which only this slice can get wrong.
//
// `Execute` returns bytes it cannot write and evidence it cannot record. The
// bytes become visible here, and the evidence is recorded here, and the order
// between them is the whole contract: a row written first is a claim about
// something that has not happened.
//
// # Contract
//
// Publish, then record, then render. A publication failure records nothing. A
// recording failure after a successful publication is an OPERATIONAL failure —
// exit 3, with a diagnostic saying the file was written and the evidence was not
// — and not an ordinary success.
//
// # The failure direction, chosen rather than discovered
//
// Nothing makes the file write and the store write atomic, so a crash between
// them leaves bytes published and unrecorded. The screens then UNDER-report: a
// paragraph this tool published is not recognized. The opposite ordering
// over-reports, and an over-reporting screen excludes the author's own prose from
// their own corpus, which is what #134 is about.
//
// Three windows leave the same residue, and the second is not a crash:
//
//   - the process dies between the two writes;
//   - `RecordPublication` fails, which is the case below;
//   - `internal/publish` returns an error AFTER the destination changed — it can
//     link or rename successfully and then fail on directory sync, close, or
//     staging cleanup (publish.go:131). A publisher error is not proof the
//     destination is unchanged, so "publication failed, therefore nothing was
//     published" is false in general. What the test below asserts is narrower:
//     when the publisher reports failure, this command records nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// Driving the command with a service that also records
// ---------------------------------------------------------------------------

type publishRun struct {
	code           int
	stdout, stderr string
	events         []string
}

// publishing is `rewriting` with a wider service parameter, because the fake
// here implements one more method than *rewriteService does.
func publishing(t *testing.T, service *recordingService, publisher *spyPublisher, args ...string) publishRun {
	t.Helper()
	var events []string
	publisher.events = &events
	service.events = &events
	out := &recordingWriter{events: &events}
	var errOut strings.Builder
	code := cli.Run(context.Background(), args, cli.Deps{
		Stdout: out, Stderr: &errOut,
		Env:       func(string) (string, bool) { return "", false },
		ReadFile:  os.ReadFile,
		Getwd:     func() (string, error) { return "/somewhere", nil },
		Service:   service,
		Publisher: publisher,
	})
	return publishRun{code: code, stdout: out.String(), stderr: errOut.String(), events: events}
}

// recordingService is a rewriteService that also notes when evidence was
// recorded, into the same event sequence the publisher and the stdout writer
// share.
type recordingService struct {
	*rewriteService
	events   *[]string
	recorded []workflow.Publication
	err      error
}

func (r *recordingService) RecordPublication(_ context.Context, p workflow.Publication) error {
	if r.events != nil {
		*r.events = append(*r.events, "recorded")
	}
	r.recorded = append(r.recorded, p)
	return r.err
}

// publishedOutcome is a successful rewrite carrying publication evidence for TWO
// paragraphs.
//
// Two, not one, because the whole-value comparison cannot see truncation in a
// batch of one: `evidence.Paragraphs = evidence.Paragraphs[:1]` immediately before
// `RecordPublication` passes every cli test when the fixture carries a single
// entry, which is how this command would come to record half a publication while
// the file holds both paragraphs.
func publishedOutcome(hash string) workflow.RewriteOutcome {
	return workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
		Targets: 2, Improved: 2,
		Outcomes: []workflow.TargetOutcome{
			{Index: 0, NodeID: strings.Repeat("b", 64), Changed: true,
				Terminal: "attempts-exhausted", Rejections: []string{""}},
			{Index: 1, NodeID: strings.Repeat("d", 64), Changed: true,
				Terminal: "attempts-exhausted", Rejections: []string{""}},
		},
	}, []byte("revised\n")).WithPublication(workflow.Publication{
		StorePath:    "/store/hapax.db",
		InvocationID: strings.Repeat("a", 64),
		Paragraphs: []workflow.PublishedParagraph{
			{NodeID: strings.Repeat("b", 64), ParagraphHash: hash},
			{NodeID: strings.Repeat("d", 64), ParagraphHash: strings.Repeat("e", 64)},
		},
	})
}

// The evidence is recorded after the bytes are visible, and before the result is
// rendered.
//
// Order, not occurrence. An implementation recording first satisfies every
// assertion about WHAT was recorded, and writes a row for a file that may never
// appear.
func TestPublicationEvidenceIsRecordedAfterTheBytesAreVisible(t *testing.T) {
	hash := strings.Repeat("c", 64)
	// Both destinations, because they are different code paths in this command —
	// `create` against a new file and `replace` over the source.
	for _, c := range destinations() {
		t.Run(c.name, func(t *testing.T) {
			service := &recordingService{
				rewriteService: &rewriteService{result: publishedOutcome(hash)},
			}
			draft := tempDraft(t)
			run := publishing(t, service, &spyPublisher{}, c.args(draft)...)

			if run.code != 0 {
				t.Fatalf("exit code %d, want 0: %s%s", run.code, run.stdout, run.stderr)
			}
			want := []string{"published", "recorded", "rendered"}
			if strings.Join(run.events, ",") != strings.Join(want, ",") {
				t.Errorf("the sequence was %v, want %v", run.events, want)
			}
			if len(service.recorded) != 1 {
				t.Fatalf("%d publications recorded, want 1", len(service.recorded))
			}
			// The WHOLE value, handed back verbatim: the command reconstructs no
			// identity and chooses no database of its own. Comparing the path and
			// the hash alone leaves an implementation free to rebuild the
			// invocation or the node id, which is how evidence ends up attached to
			// the wrong target.
			if got := service.recorded[0]; !reflect.DeepEqual(got, publishedOutcome(hash).Publication()) {
				t.Errorf("the recorded evidence is\n%+v\nwant the evidence the run returned\n%+v",
					got, publishedOutcome(hash).Publication())
			}
		})
	}
}

// A publication failure records nothing, by either destination.
//
// Both, because `create` and `replace` are different branches here and the
// successful ordering test cannot catch a replacement branch that records anyway.
func TestNothingIsRecordedWhenPublicationFails(t *testing.T) {
	for _, c := range destinations() {
		t.Run(c.name, func(t *testing.T) {
			service := &recordingService{
				rewriteService: &rewriteService{result: publishedOutcome(strings.Repeat("c", 64))},
			}
			draft := tempDraft(t)
			run := publishing(t, service, &spyPublisher{err: errors.New("disk full")},
				c.args(draft)...)

			if run.code == 0 {
				t.Errorf("exit code 0 after a publication failure: %s", run.stdout)
			}
			if len(service.recorded) != 0 {
				t.Errorf("%d publications recorded for a file that failed to publish: %+v",
					len(service.recorded), service.recorded)
			}
		})
	}
}

// destinations is the two publication branches this command has.
func destinations() []struct {
	name string
	args func(string) []string
} {
	return []struct {
		name string
		args func(string) []string
	}{
		{"--out", func(draft string) []string { return out(draft, draft+".revised") }},
		{"--in-place", inPlace},
	}
}

// Publishing and then failing to record is an operational failure.
//
// Not a refusal — the bytes are on disk and the user's file has changed. Not a
// success either: the screens no longer know about it, so the next `index` can
// take this output into the corpus and the next `rewrite --in-place` can compose
// on top of it, which is #111 restored. The user is told both halves.
func TestPublishingWithoutRecordingIsAnOperationalFailure(t *testing.T) {
	for _, mode := range []struct {
		name string
		args []string
	}{
		{"human", nil},
		{"json", []string{"--json"}},
	} {
		for _, destination := range destinations() {
			t.Run(mode.name+" "+destination.name, func(t *testing.T) {
				service := &recordingService{
					rewriteService: &rewriteService{result: publishedOutcome(strings.Repeat("c", 64))},
					err:            errors.New("database is locked"),
				}
				publisher := &spyPublisher{}
				draft := tempDraft(t)
				args := append(destination.args(draft), mode.args...)
				run := publishing(t, service, publisher, args...)

				if run.code != 3 {
					t.Errorf("exit code %d, want 3 — the file was written and the record was not",
						run.code)
				}
				if len(publisher.published) != 1 {
					t.Fatalf("%d publications; this test needs the bytes to have landed",
						len(publisher.published))
				}
				// Phrases, not two words that any sentence can satisfy: "not
				// published; evidence recorded" contains both "published" and
				// "evidence" and says the opposite of what happened. The underlying
				// error is named too, because a diagnostic that does not say WHY
				// leaves the user nothing to act on.
				for _, want := range []string{
					"published", "could not record", "publication evidence", "database is locked",
				} {
					if !strings.Contains(run.stderr, want) {
						t.Errorf("the diagnostic does not say %q: %q", want, run.stderr)
					}
				}
				// Nothing was rendered at all — asserted on the event sequence rather
				// than by looking for strings that are absent, because the absence of
				// `"status":"ok"` is also satisfied by a differently-worded completed
				// document.
				for _, event := range run.events {
					if event == "rendered" {
						t.Errorf("a document was rendered for a run whose evidence was lost: "+
							"%q", run.stdout)
					}
				}
				if run.stdout != "" {
					t.Errorf("stdout is %q, want nothing", run.stdout)
				}
			})
		}
	}
}

// A run that publishes nothing records nothing.
//
// `--in-place` over a draft with nothing to change takes the `noPublication`
// path, and an implementation recording whatever evidence the outcome carried
// would write rows for a file it never touched.
func TestARunThatPublishesNothingRecordsNothing(t *testing.T) {
	outcome := workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateNothingToChange, State: workflow.RewriteNoneImproved,
	}, []byte("unchanged\n"))
	service := &recordingService{rewriteService: &rewriteService{result: outcome}}
	publisher := &spyPublisher{}
	publishing(t, service, publisher, inPlace(tempDraft(t))...)

	if len(publisher.published) != 0 {
		t.Fatalf("%d publications; the nothing-to-change path must not write",
			len(publisher.published))
	}
	if len(service.recorded) != 0 {
		t.Errorf("%d publications recorded when nothing was published: %+v",
			len(service.recorded), service.recorded)
	}
}

// The index envelope discloses that earlier runs left no publication evidence.
//
// Both renderings, because the JSON consumer and the person reading a line are
// different audiences and #109's `tool-output` key already establishes the split:
// the envelope carries the field unconditionally so absent and false are
// distinguishable, and the human line says something only when there is something
// to say.
func TestTheIndexEnvelopeDisclosesThePublicationEvidenceGap(t *testing.T) {
	for _, c := range []struct {
		name    string
		gap     bool
		wantKey bool
	}{
		{"a store with earlier accepted attempts", true, true},
		{"a store with no history", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Through the command, so the projection from workflow.IndexResult is
			// under test too. Building a cli.IndexResult by hand would leave
			// `indexResultFrom` free to drop the field.
			result := fullIndex()
			result.PublicationEvidenceGap = c.gap
			args := []string{"index", "/w", "--profile", "essays"}

			human := runWith(t, &fakeService{indexResult: result}, args...)
			if human.code != 0 {
				t.Fatalf("exit code %d: %s", human.code, human.stderr)
			}
			if got := strings.Contains(human.stdout, "publication-evidence-gap"); got != c.wantKey {
				t.Errorf("the human output is %q; mentions the gap = %v, want %v",
					human.stdout, got, c.wantKey)
			}

			asJSON := runWith(t, &fakeService{indexResult: result}, append(args, "--json")...)
			var envelope map[string]any
			if err := json.Unmarshal([]byte(asJSON.stdout), &envelope); err != nil {
				t.Fatalf("the JSON rendering is not one document: %q: %v", asJSON.stdout, err)
			}
			rendered, _ := envelope["result"].(map[string]any)
			value, named := rendered["publication_evidence_gap"]
			if !named {
				t.Fatalf("the envelope carries no publication_evidence_gap: %v", rendered)
			}
			if value != c.gap {
				t.Errorf("publication_evidence_gap = %v, want %v", value, c.gap)
			}
		})
	}
}
