package cli_test

// The last command, and the first one that can write to a user's file.
//
// Everything under it is careful so that publication is a single, isolated
// authority: Execute returns bytes it cannot write, assemble returns nil rather
// than a partial document, and internal/publish is the only code that touches a
// destination. This slice is where those are composed, and composition is where
// the remaining mistakes live.
//
// # The failure this slice can make and no other can
//
// Telling a user their file was written when it was not. `hapax rewrite ok
// ... targets=2 improved=2` printed before publication succeeded is a lie the
// rest of the pipeline cannot produce, because nothing else knows about a
// destination. So the order is publish, THEN render, and a publication failure
// renders no completed document at all.
//
// # What these tests are not
//
// They are not a second copy of publication's filesystem matrix — that is
// #79's, over real temp directories, with a scheduled race. Here the publisher
// is a spy, because what is under test is which authority the command reaches
// for and when, not what the filesystem does about it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// A spy publisher
// ---------------------------------------------------------------------------

type publication struct {
	Op                  string // "create" or "replace"
	Source, Destination string
	Content             []byte
}

type spyPublisher struct {
	published []publication
	err       error
	// events, when set, is shared with the stdout writer so the ORDER of
	// publication and rendering is observable. Without it an implementation can
	// render into a buffer, notice the publication failed, discard the buffer,
	// and satisfy every outcome assertion while violating the sequencing the
	// package comment states.
	events *[]string
}

func (s *spyPublisher) note(event string) {
	if s.events != nil {
		*s.events = append(*s.events, event)
	}
}

// recordingWriter notes the first write, so "rendered" has a position in the
// same sequence as "published".
type recordingWriter struct {
	strings.Builder
	events *[]string
	noted  bool
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	if !w.noted && len(p) > 0 {
		w.noted = true
		if w.events != nil {
			*w.events = append(*w.events, "rendered")
		}
	}
	return w.Builder.Write(p)
}

func (s *spyPublisher) Create(source, destination string, content []byte) error {
	s.note("published")
	s.published = append(s.published, publication{
		Op: "create", Source: source, Destination: destination,
		Content: append([]byte(nil), content...),
	})
	return s.err
}

func (s *spyPublisher) Replace(source string, content []byte) error {
	s.note("published")
	s.published = append(s.published, publication{
		Op: "replace", Source: source, Content: append([]byte(nil), content...),
	})
	return s.err
}

// ---------------------------------------------------------------------------
// A rewriting service
// ---------------------------------------------------------------------------

type rewriteService struct {
	request workflow.RewriteInput
	result  workflow.RewriteOutcome
	err     error
	calls   int
}

func (r *rewriteService) Rewrite(_ context.Context, request workflow.RewriteInput) (workflow.RewriteOutcome, error) {
	r.calls++
	r.request = request
	return r.result, r.err
}

func (r *rewriteService) Score(context.Context, workflow.ScoreRequest) (workflow.ScoreResult, error) {
	return workflow.ScoreResult{}, errNotUsed{}
}
func (r *rewriteService) Eval(context.Context, workflow.EvalRequest) (workflow.EvalResult, error) {
	return workflow.EvalResult{}, errNotUsed{}
}
func (r *rewriteService) Index(context.Context, workflow.IndexRequest) (workflow.IndexResult, error) {
	return workflow.IndexResult{}, errNotUsed{}
}
func (r *rewriteService) Profile(context.Context, workflow.ProfileRequest) (workflow.ProfileResult, error) {
	return workflow.ProfileResult{}, errNotUsed{}
}

// improved is a completed rewrite that changed one of two targets.
func improved(content string) workflow.RewriteOutcome {
	return workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteImproved,
		Targets: 2, Improved: 1,
		Outcomes: []workflow.TargetOutcome{
			{Index: 0, NodeID: strings.Repeat("a", 64), Changed: true, Terminal: "attempts-exhausted", Rejections: []string{""}},
			{Index: 1, NodeID: strings.Repeat("b", 64), Changed: false, Terminal: "attempts-exhausted", Rejections: []string{"not-improved"}},
		},
	}, []byte(content))
}

func nothingToChange(content string) workflow.RewriteOutcome {
	return workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateNothingToChange, State: workflow.RewriteNoTargets,
	}, []byte(content))
}

func refused(reason string) workflow.RewriteOutcome {
	return workflow.NewRewriteOutcome(workflow.RewriteReport{Refusal: reason}, nil)
}

// ---------------------------------------------------------------------------
// Driving the command
// ---------------------------------------------------------------------------

type rewriteRun struct {
	code           int
	stdout, stderr string
	service        *rewriteService
	publisher      *spyPublisher
	events         []string
}

func rewriting(t *testing.T, service *rewriteService, publisher *spyPublisher, args ...string) rewriteRun {
	t.Helper()
	var events []string
	publisher.events = &events
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
	return rewriteRun{
		code: code, stdout: out.String(), stderr: errOut.String(),
		service: service, publisher: publisher, events: events,
	}
}

// humanFields parses a rendered line into its key=value pairs. A substring
// check is satisfied by `path=<destination>.bak`, which is a different file;
// comparing the parsed value is not.
func humanFields(t *testing.T, line string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, pair := range strings.Fields(strings.TrimSpace(line)) {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue // the leading `rewrite ok` words carry no value
		}
		out[key] = value
	}
	return out
}

// requireReceiptNames checks both representations agree on the file that was
// written. Two renderings of one fact are two chances to name the wrong one.
func requireReceiptNames(t *testing.T, service *rewriteService, publisher *spyPublisher, args []string, wanted string) {
	t.Helper()
	human := rewriting(t, service, publisher, args...)
	if human.code != 0 {
		t.Fatalf("exit = %d: %s", human.code, human.stderr)
	}
	if got := humanFields(t, human.stdout)["path"]; got != wanted {
		t.Errorf("the human receipt names path=%q, want %q", got, wanted)
	}

	asJSON := rewriting(t, &rewriteService{result: service.result}, &spyPublisher{},
		append(append([]string(nil), args...), "--json")...)
	var document map[string]any
	if err := json.Unmarshal([]byte(asJSON.stdout), &document); err != nil {
		t.Fatalf("stdout is not one JSON document: %q: %v", asJSON.stdout, err)
	}
	result, _ := document["result"].(map[string]any)
	if got := result["path"]; got != wanted {
		t.Errorf("the JSON receipt names path=%v, want %q", got, wanted)
	}
}

// out is the ordinary successful invocation: a draft, a destination, a model.
func out(draft, destination string) []string {
	return []string{"rewrite", draft, "--out", destination, "--model", "llama3"}
}

func inPlace(draft string) []string {
	return []string{"rewrite", draft, "--in-place", "--model", "llama3"}
}

func tempDraft(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte("A draft.\n"), 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Publication happens, and it happens before anything is claimed
// ---------------------------------------------------------------------------

// The failure this slice alone can make: telling a user their file was written
// when it was not. Publication comes first, and the document that says the
// rewrite succeeded is written only after it did.
func TestNothingIsClaimedBeforeItIsPublished(t *testing.T) {
	draft := tempDraft(t)
	publisher := &spyPublisher{err: errors.New("the disk is full")}
	got := rewriting(t, &rewriteService{result: improved("revised\n")}, publisher,
		out(draft, filepath.Join(filepath.Dir(draft), "revised.md"))...)

	if got.code != 3 {
		t.Errorf("exit = %d, want 3; a publication failure is operational", got.code)
	}
	if strings.TrimSpace(got.stdout) != "" {
		t.Errorf("stdout is %q; a run whose publication failed must not report a completed rewrite",
			got.stdout)
	}
	if len(publisher.published) != 1 {
		t.Errorf("the publisher was called %d times", len(publisher.published))
	}
	for _, event := range got.events {
		if event == "rendered" {
			t.Error("something was rendered on a run whose publication failed")
		}
	}
}

// And on a successful run the order is publication first. The outcome
// assertions cannot see the difference — an implementation may render into a
// buffer and flush it afterwards — so the order is observed directly.
func TestPublicationHappensBeforeAnythingIsRendered(t *testing.T) {
	draft := tempDraft(t)
	got := rewriting(t, &rewriteService{result: improved("revised\n")}, &spyPublisher{},
		out(draft, filepath.Join(filepath.Dir(draft), "revised.md"))...)

	if got.code != 0 {
		t.Fatalf("exit = %d: %s", got.code, got.stderr)
	}
	if len(got.events) < 2 {
		t.Fatalf("events are %v; both a publication and a render were expected", got.events)
	}
	if got.events[0] != "published" {
		t.Errorf("events are %v; publication must come first, or a user can be told their "+
			"file was written before it was", got.events)
	}
}

// And on success the bytes that reach the publisher are the ones the service
// produced — not re-read, not re-derived, not the draft.
func TestTheBytesPublishedAreTheOnesTheServiceProduced(t *testing.T) {
	draft := tempDraft(t)
	destination := filepath.Join(filepath.Dir(draft), "revised.md")
	publisher := &spyPublisher{}
	got := rewriting(t, &rewriteService{result: improved("the revision\n")}, publisher, out(draft, destination)...)

	if got.code != 0 {
		t.Fatalf("exit = %d: %s", got.code, got.stderr)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("the publisher was called %d times, want once", len(publisher.published))
	}
	only := publisher.published[0]
	if only.Op != "create" {
		t.Errorf("--out published by %q, want create; only --in-place may overwrite", only.Op)
	}
	if string(only.Content) != "the revision\n" {
		t.Errorf("published %q, want the service's bytes", only.Content)
	}
	if only.Source != draft || only.Destination != destination {
		t.Errorf("published %s -> %s, want %s -> %s", only.Source, only.Destination, draft, destination)
	}
	// The receipt names the file that was written, not the one that was read,
	// in both renderings and by exact comparison.
	requireReceiptNames(t, &rewriteService{result: improved("the revision\n")}, &spyPublisher{},
		out(draft, destination), destination)
}

// --in-place is the only overwrite authority, and it reaches for the only
// operation that has it.
func TestInPlacePublishesByReplacing(t *testing.T) {
	draft := tempDraft(t)
	publisher := &spyPublisher{}
	got := rewriting(t, &rewriteService{result: improved("the revision\n")}, publisher, inPlace(draft)...)

	if got.code != 0 {
		t.Fatalf("exit = %d: %s", got.code, got.stderr)
	}
	if len(publisher.published) != 1 || publisher.published[0].Op != "replace" {
		t.Fatalf("published %+v, want exactly one replace", publisher.published)
	}
	if publisher.published[0].Source != draft {
		t.Errorf("replaced %s, want the draft %s", publisher.published[0].Source, draft)
	}
	// In place, the file written IS the draft, so that is what the receipt names.
	requireReceiptNames(t, &rewriteService{result: improved("the revision\n")}, &spyPublisher{},
		inPlace(draft), draft)
}

// ---------------------------------------------------------------------------
// The asymmetry: nothing to change
// ---------------------------------------------------------------------------

// A run with nothing to change performs NO WRITE AT ALL in place. Not a write
// of identical bytes — no call to the publisher, so there is nothing for a
// concurrent reader to observe and nothing for a backup to notice.
func TestNothingToChangeInPlaceDoesNotPublishAtAll(t *testing.T) {
	draft := tempDraft(t)
	publisher := &spyPublisher{}
	got := rewriting(t, &rewriteService{result: nothingToChange("A draft.\n")}, publisher, inPlace(draft)...)

	if got.code != 0 {
		t.Fatalf("exit = %d: %s", got.code, got.stderr)
	}
	if len(publisher.published) != 0 {
		t.Errorf("the publisher was called %+v; in place, nothing to change means no write",
			publisher.published)
	}
	if !strings.Contains(got.stdout, "nothing-to-change") {
		t.Errorf("stdout is %q; it must still say what happened", got.stdout)
	}
}

// Through --out the same run writes an exact copy, because the user asked for a
// file to exist and one must.
func TestNothingToChangeThroughOutWritesAnExactCopy(t *testing.T) {
	draft := tempDraft(t)
	destination := filepath.Join(filepath.Dir(draft), "revised.md")
	publisher := &spyPublisher{}
	got := rewriting(t, &rewriteService{result: nothingToChange("A draft.\n")}, publisher, out(draft, destination)...)

	if got.code != 0 {
		t.Fatalf("exit = %d: %s", got.code, got.stderr)
	}
	if len(publisher.published) != 1 || publisher.published[0].Op != "create" {
		t.Fatalf("published %+v, want one create", publisher.published)
	}
	if string(publisher.published[0].Content) != "A draft.\n" {
		t.Errorf("published %q, want the draft byte for byte", publisher.published[0].Content)
	}
}

// ---------------------------------------------------------------------------
// Refusals never publish
// ---------------------------------------------------------------------------

// Every refusal the workflow can return exits 4, names its reason, and reaches
// no publisher. The table is driven from workflow.Refusals() rather than
// written out, so a refusal added there and not handled here fails rather than
// producing a plausible-looking exit code for a reason cli has never seen.
func TestEveryRefusalExitsFourAndPublishesNothing(t *testing.T) {
	for _, reason := range workflow.Refusals() {
		t.Run(reason, func(t *testing.T) {
			draft := tempDraft(t)
			publisher := &spyPublisher{}
			got := rewriting(t, &rewriteService{result: refused(reason)}, publisher,
				out(draft, filepath.Join(filepath.Dir(draft), "revised.md"))...)

			if got.code != 4 {
				t.Errorf("exit = %d for refusal %q, want 4", got.code, reason)
			}
			if len(publisher.published) != 0 {
				t.Errorf("a refused run published %+v", publisher.published)
			}

			// The document is parsed rather than searched. A substring match is
			// satisfied by a reason appearing anywhere — in a diagnostic, in a
			// path — while the envelope carries a status or a reason the schema
			// does not admit.
			asJSON := rewriting(t, &rewriteService{result: refused(reason)}, &spyPublisher{},
				append(out(draft, filepath.Join(filepath.Dir(draft), "revised.md")), "--json")...)
			var document map[string]any
			if err := json.Unmarshal([]byte(asJSON.stdout), &document); err != nil {
				t.Fatalf("stdout is not one JSON document: %q: %v", asJSON.stdout, err)
			}
			if document["status"] != "refused" {
				t.Errorf("status = %v, want refused", document["status"])
			}
			if document["reason"] != reason {
				t.Errorf("reason = %v, want %q", document["reason"], reason)
			}
			if document["command"] != "rewrite" {
				t.Errorf("command = %v, want rewrite", document["command"])
			}

			// And the human line, which is what a person actually reads. Exit
			// code and JSON both being right while the line is blank or says
			// something else would be a defect only a person would find.
			if wanted := "rewrite refused reason=" + reason; !strings.Contains(got.stdout, wanted) {
				t.Errorf("the human output is %q, want it to contain %q",
					strings.TrimSpace(got.stdout), wanted)
			}
		})
	}
}

// And cli's own reason vocabulary is derived from the workflow's rather than
// restated, so the two cannot drift. Document.valid rejects an undeclared
// reason, which is how a drifted vocabulary would surface — as a rendering
// failure on a correct run.
func TestTheReasonVocabularyIsDerivedFromTheWorkflows(t *testing.T) {
	declared := map[string]bool{}
	for _, reason := range cli.Reasons() {
		declared[string(reason)] = true
	}
	for _, reason := range workflow.Refusals() {
		if !declared[reason] {
			t.Errorf("the workflow can refuse %q and cli does not declare it, so a real refusal "+
				"would fail to render", reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Exit codes
// ---------------------------------------------------------------------------

// The two runs that both complete, and the one that does not improve. Exit 0
// covers "nothing needed changing" and "something improved"; which happened is
// in the document, not the code.
func TestTheExitCodeSaysWhetherTheToolWorked(t *testing.T) {
	noneImproved := workflow.NewRewriteOutcome(workflow.RewriteReport{
		PlanState: workflow.StateTargetsPlanned, State: workflow.RewriteNoneImproved,
		Targets: 2, Improved: 0,
		Outcomes: []workflow.TargetOutcome{
			{Index: 0, NodeID: strings.Repeat("a", 64), Terminal: "attempts-exhausted", Rejections: []string{"not-improved"}},
			{Index: 1, NodeID: strings.Repeat("b", 64), Terminal: "empty-provider-response"},
		},
	}, []byte("unchanged\n"))

	for _, c := range []struct {
		name   string
		result workflow.RewriteOutcome
		err    error
		want   int
	}{
		{"something improved", improved("revised\n"), nil, 0},
		{"nothing needed changing", nothingToChange("A draft.\n"), nil, 0},
		{"nothing improved", noneImproved, nil, 1},
		{"the workflow failed", workflow.RewriteOutcome{}, errors.New("the store is corrupt"), 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			draft := tempDraft(t)
			publisher := &spyPublisher{}
			got := rewriting(t, &rewriteService{result: c.result, err: c.err}, publisher,
				out(draft, filepath.Join(filepath.Dir(draft), "revised.md"))...)
			if got.code != c.want {
				t.Errorf("exit = %d, want %d: %s", got.code, c.want, got.stderr)
			}
			if c.err == nil {
				return
			}
			// A failed run publishes nothing and claims nothing. The zero
			// outcome carries no bytes, and an implementation that published
			// them anyway would write an empty file over a user's draft.
			if len(publisher.published) != 0 {
				t.Errorf("a failed run published %+v", publisher.published)
			}
			if strings.TrimSpace(got.stdout) != "" {
				t.Errorf("a failed run printed %q", got.stdout)
			}
		})
	}
}

// Publication errors are classified by IDENTITY, not by message — and both
// routes to an occupied destination are one exit code.
//
// The first version of this test mapped a bare ErrExists to 2 and a "wrapped"
// one to 3, on the reasoning that naming an existing file is the user's mistake
// while losing a race is not. Two things were wrong with it. The fixture was
// `errors.New("link: " + ErrExists.Error())`, which is not wrapped at all, so it
// proved only that message text is ignored. And the distinction is undeliverable
// anyway: publish returns the lost race as `fmt.Errorf("%w: %v", ErrExists, err)`,
// so errors.Is is true for both and a caller cannot tell them apart. #79 says so
// explicitly, and encoding a difference the errors do not carry would mean
// reading the wrapped message, which is kernel detail and not API.
//
// So an occupied destination is exit 2 however it was discovered.
//
// The sentinels are cli's own, not internal/publish's. cli cannot import that
// package — the import guard forbids it, because naming it would give this
// package the ability to write to a destination directly rather than only
// through the seam it was handed. The composition root's adapter translates.
func TestPublicationErrorsAreClassifiedByIdentity(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"the destination exists", cli.ErrDestinationExists, 2},
		{"the destination exists, discovered by losing the race", fmt.Errorf("%w: link: file exists", cli.ErrDestinationExists), 2},
		{"the destination is the input", cli.ErrDestinationIsInput, 2},
		{"the destination is the input, wrapped", fmt.Errorf("%w: /tmp/draft.md", cli.ErrDestinationIsInput), 2},
		{"an ordinary write failure", errors.New("read-only file system"), 3},
		{"a failure whose message merely mentions one", errors.New("publication destination exists"), 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			draft := tempDraft(t)
			got := rewriting(t, &rewriteService{result: improved("revised\n")},
				&spyPublisher{err: c.err},
				out(draft, filepath.Join(filepath.Dir(draft), "revised.md"))...)
			if got.code != c.want {
				t.Errorf("exit = %d, want %d for %v", got.code, c.want, c.err)
			}
			if strings.TrimSpace(got.stdout) != "" {
				t.Errorf("a failed publication printed %q", got.stdout)
			}
			if strings.TrimSpace(got.stderr) == "" {
				t.Error("a failed publication said nothing to stderr")
			}
		})
	}
}

// The last case above is the one that matters most: an unrelated error whose
// MESSAGE happens to read like ErrExists must not be classified as one. That is
// what "by identity" means, and a strings.Contains implementation passes every
// other case in the table.

// ---------------------------------------------------------------------------
// The invocation
// ---------------------------------------------------------------------------

// Everything the grammar refuses, and every one of them before the service is
// reached — an invalid invocation must not index a draft or resolve a profile.
func TestInvalidInvocationsExitTwoWithoutReachingTheService(t *testing.T) {
	draft := tempDraft(t)
	destination := filepath.Join(filepath.Dir(draft), "revised.md")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"no destination at all", []string{"rewrite", draft, "--model", "llama3"}},
		{"both destinations", []string{"rewrite", draft, "--out", destination, "--in-place", "--model", "llama3"}},
		{"no draft", []string{"rewrite", "--out", destination, "--model", "llama3"}},
		{"two drafts", []string{"rewrite", draft, draft, "--out", destination, "--model", "llama3"}},
		{"no model", []string{"rewrite", draft, "--out", destination}},
		{"an empty model", []string{"rewrite", draft, "--out", destination, "--model", ""}},
		{"an empty destination", []string{"rewrite", draft, "--out", "", "--model", "llama3"}},
		{"a missing flag value", []string{"rewrite", draft, "--out"}},
		{"zero attempts", []string{"rewrite", draft, "--out", destination, "--model", "llama3", "--attempts", "0"}},
		{"negative attempts", []string{"rewrite", draft, "--out", destination, "--model", "llama3", "--attempts", "-1"}},
		{"attempts that are not a number", []string{"rewrite", draft, "--out", destination, "--model", "llama3", "--attempts", "many"}},
		// A repeated value flag is ambiguous, and silently taking the last one
		// means a user who typed two destinations gets a file at whichever the
		// shell happened to put second.
		{"two destinations through --out", []string{"rewrite", draft, "--out", destination, "--out", destination + ".2", "--model", "llama3"}},
		{"two models", []string{"rewrite", draft, "--out", destination, "--model", "llama3", "--model", "mistral"}},
		{"two attempt caps", []string{"rewrite", draft, "--out", destination, "--model", "llama3", "--attempts", "2", "--attempts", "3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			service := &rewriteService{result: improved("revised\n")}
			publisher := &spyPublisher{}
			got := rewriting(t, service, publisher, c.args...)

			if got.code != 2 {
				t.Errorf("exit = %d, want 2", got.code)
			}
			if service.calls != 0 {
				t.Errorf("the service ran %d times on an invalid invocation", service.calls)
			}
			if len(publisher.published) != 0 {
				t.Errorf("an invalid invocation published %+v", publisher.published)
			}
			if strings.TrimSpace(got.stderr) == "" {
				t.Error("nothing was written to stderr")
			}
			if strings.TrimSpace(got.stdout) != "" {
				t.Errorf("an invalid invocation printed %q to stdout", got.stdout)
			}
		})
	}
}

// A draft that is missing, or is not a file, is NOT cli's to refuse.
//
// An earlier version of the table above rejected both before the service ran.
// That was wrong twice: it is an operational failure rather than invalid syntax,
// and reading the draft belongs to workflow.Plan, which already opens
// request.Path and returns its error. Pre-empting it here would be a second
// authority for one rule — the thing this design has refused everywhere else —
// and it would have failed a correct implementation.
//
// What cli owns is the classification of what comes back.
func TestAnUnreadableDraftIsTheServicesToReportAndCliClassifiesIt(t *testing.T) {
	for _, name := range []string{"a draft that is not there", "a draft that is a directory"} {
		t.Run(name, func(t *testing.T) {
			draft := tempDraft(t)
			path := filepath.Join(filepath.Dir(draft), "absent.md")
			if name == "a draft that is a directory" {
				path = filepath.Dir(draft)
			}
			service := &rewriteService{err: errors.New("open " + path + ": not a regular file")}
			publisher := &spyPublisher{}
			got := rewriting(t, service, publisher,
				"rewrite", path, "--out", filepath.Join(filepath.Dir(draft), "revised.md"),
				"--model", "llama3")

			if service.calls != 1 {
				t.Errorf("the service ran %d times; it owns reading the draft", service.calls)
			}
			if got.code != 3 {
				t.Errorf("exit = %d, want 3; an unreadable draft is operational, not a bad invocation", got.code)
			}
			if len(publisher.published) != 0 {
				t.Errorf("a failed run published %+v", publisher.published)
			}
		})
	}
}

// The absent attempt cap leaves the field unset so the workflow applies its own
// default. Substituting a number here would put the same constant in two
// places, and cli is not where that constant lives.
func TestAnAbsentAttemptCapIsNotSubstituted(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: improved("revised\n")}
	rewriting(t, service, &spyPublisher{}, out(draft, filepath.Join(filepath.Dir(draft), "revised.md"))...)

	if service.calls != 1 {
		t.Fatalf("the service ran %d times", service.calls)
	}
	if service.request.Attempts != 0 {
		t.Errorf("Attempts = %d with no flag given; it must be left unset", service.request.Attempts)
	}
}

// And a supplied one reaches the service unchanged, along with the rest of the
// invocation.
func TestTheInvocationReachesTheServiceUnchanged(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: improved("revised\n")}
	rewriting(t, service, &spyPublisher{},
		"rewrite", draft, "--out", filepath.Join(filepath.Dir(draft), "revised.md"),
		"--model", "mistral", "--provider", "ollama", "--attempts", "5",
		"--local-endpoint", "http://127.0.0.1:9999", "--profile", "essays",
		"--store", "/somewhere/else/hapax.sqlite3")

	if service.calls != 1 {
		t.Fatalf("the service ran %d times", service.calls)
	}
	got := service.request
	if got.Path != draft {
		t.Errorf("Path = %q, want %q", got.Path, draft)
	}
	if got.Register != "essays" {
		t.Errorf("Register = %q, want essays", got.Register)
	}
	if got.Attempts != 5 {
		t.Errorf("Attempts = %d, want 5", got.Attempts)
	}
	if got.Choice.Model != "mistral" {
		t.Errorf("Model = %q, want mistral", got.Choice.Model)
	}
	if got.Choice.Provider != "ollama" {
		t.Errorf("Provider = %q, want ollama", got.Choice.Provider)
	}
	if got.Choice.Endpoint != "http://127.0.0.1:9999" {
		t.Errorf("Endpoint = %q", got.Choice.Endpoint)
	}
	// --store is how a user points at a database that is not under the working
	// directory. Dropping it silently would send the rewrite to whichever store
	// discovery happens to find.
	if got.StorePath != "/somewhere/else/hapax.sqlite3" {
		t.Errorf("StorePath = %q, want the one the flag named", got.StorePath)
	}
	// And the working directory, which is what discovery starts from when no
	// store is named.
	if got.StartDir != "/somewhere" {
		t.Errorf("StartDir = %q, want the working directory", got.StartDir)
	}
}

// local-only reaches the service as a mode rather than being interpreted here.
// cli cannot import internal/llm, so it cannot know which providers a mode
// forbids, and guessing would put that knowledge in two places.
func TestLocalOnlyReachesTheServiceAsAMode(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: improved("revised\n")}
	rewriting(t, service, &spyPublisher{},
		append(out(draft, filepath.Join(filepath.Dir(draft), "revised.md")), "--local-only")...)

	if !service.request.Mode.LocalOnly {
		t.Error("the resolved mode did not reach the service")
	}
}

// ---------------------------------------------------------------------------
// The payload
// ---------------------------------------------------------------------------

// plan_state and rewrite_state are distinct members. A single generic `state`
// cannot separate "there was nothing to change" from "there was and none of it
// improved", and those are different things to tell someone.
func TestThePayloadCarriesBothStatesSeparately(t *testing.T) {
	draft := tempDraft(t)
	got := rewriting(t, &rewriteService{result: improved("revised\n")}, &spyPublisher{},
		append(out(draft, filepath.Join(filepath.Dir(draft), "revised.md")), "--json")...)
	if got.code != 0 {
		t.Fatalf("exit = %d: %s", got.code, got.stderr)
	}

	if !strings.Contains(got.stdout, `"plan_state"`) {
		t.Error("the payload has no plan_state")
	}
	if !strings.Contains(got.stdout, `"rewrite_state"`) {
		t.Error("the payload has no rewrite_state")
	}
	if strings.Contains(got.stdout, `"state"`) {
		t.Error("the payload has a generic `state` member, which cannot say which of the two it means")
	}
}

// No prose reaches the payload. The document itself goes to the destination;
// the result describes what was decided, and a draft's text has no business in
// a machine-readable envelope a user may paste anywhere.
func TestNoProseReachesThePayload(t *testing.T) {
	draft := tempDraft(t)
	const revision = "A sentence that exists only in the rewritten document.\n"
	got := rewriting(t, &rewriteService{result: improved(revision)}, &spyPublisher{},
		append(out(draft, filepath.Join(filepath.Dir(draft), "revised.md")), "--json")...)
	if got.code != 0 {
		t.Fatalf("exit = %d: %s", got.code, got.stderr)
	}
	if strings.Contains(got.stdout, "A sentence that exists only") {
		t.Error("the rewritten prose is in the payload")
	}
	if strings.Contains(got.stdout, "A draft.") {
		t.Error("the draft's prose is in the payload")
	}
}

// ---------------------------------------------------------------------------
// The mode, which is resolved once and passed inward
// ---------------------------------------------------------------------------

// The environment and the flag agree on one resolved mode, and the flag wins.
// DESIGN puts mode resolution in every composition root rather than in cli
// specifically, so what is pinned here is that this root resolves it once and
// hands the result on rather than interpreting it.
func TestTheModeIsResolvedOnceAndHandedOn(t *testing.T) {
	draft := tempDraft(t)
	destination := filepath.Join(filepath.Dir(draft), "revised.md")
	for _, c := range []struct {
		name        string
		environment map[string]string
		flag        bool
		want        bool
	}{
		{"neither", nil, false, false},
		{"the flag alone", nil, true, true},
		{"the environment alone", map[string]string{"HAPAX_LOCAL_ONLY": "1"}, false, true},
		{"the environment says no and the flag says yes", map[string]string{"HAPAX_LOCAL_ONLY": "0"}, true, true},
		{"both", map[string]string{"HAPAX_LOCAL_ONLY": "1"}, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := out(draft, destination)
			if c.flag {
				args = append(args, "--local-only")
			}
			service := &rewriteService{result: improved("revised\n")}
			var stdout, stderr strings.Builder
			cli.Run(context.Background(), args, cli.Deps{
				Stdout: &stdout, Stderr: &stderr,
				Env: func(name string) (string, bool) {
					value, ok := c.environment[name]
					return value, ok
				},
				ReadFile:  os.ReadFile,
				Getwd:     func() (string, error) { return "/somewhere", nil },
				Service:   service,
				Publisher: &spyPublisher{},
			})
			if service.calls != 1 {
				t.Fatalf("the service ran %d times: %s", service.calls, stderr.String())
			}
			if service.request.Mode.LocalOnly != c.want {
				t.Errorf("LocalOnly = %v, want %v", service.request.Mode.LocalOnly, c.want)
			}
		})
	}
}

// A malformed environment is an invalid invocation and nothing runs. Failing
// closed means it can never select a mode; it does not mean its classification
// becomes ambiguous.
func TestAMalformedEnvironmentRunsNothing(t *testing.T) {
	draft := tempDraft(t)
	service := &rewriteService{result: improved("revised\n")}
	publisher := &spyPublisher{}
	var stdout, stderr strings.Builder
	code := cli.Run(context.Background(), out(draft, filepath.Join(filepath.Dir(draft), "revised.md")), cli.Deps{
		Stdout: &stdout, Stderr: &stderr,
		Env:       func(string) (string, bool) { return "yes", true },
		ReadFile:  os.ReadFile,
		Getwd:     func() (string, error) { return "/somewhere", nil },
		Service:   service,
		Publisher: publisher,
	})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if service.calls != 0 {
		t.Errorf("the service ran %d times on a malformed environment", service.calls)
	}
	if len(publisher.published) != 0 {
		t.Errorf("a malformed environment published %+v", publisher.published)
	}
}

// ---------------------------------------------------------------------------
// A render that fails after a successful publication
// ---------------------------------------------------------------------------

// failingWriter accepts nothing. It is how a rendering failure is produced
// without a real terminal.
//
// The first version of this test ran the real binary with stdout on a closed
// pipe. That does not produce a failed write — it produces SIGPIPE, and the
// process dies with exit -1 rather than classifying anything. The mechanism was
// wrong, not the property.
type failingWriter struct{ events *[]string }

func (w failingWriter) Write([]byte) (int, error) {
	if w.events != nil {
		*w.events = append(*w.events, "rendered")
	}
	return 0, errors.New("the output is gone")
}

// The file IS written. The exit must say the run failed operationally — not 0,
// which would be a lie, and not 2, which would tell the caller they had typed
// something wrong when in fact their document is on disk and only the receipt
// was lost.
func TestARenderFailureAfterPublicationIsOperationalAndKeepsTheFile(t *testing.T) {
	draft := tempDraft(t)
	destination := filepath.Join(filepath.Dir(draft), "revised.md")
	publisher := &spyPublisher{}
	var events []string
	publisher.events = &events

	var stderr strings.Builder
	code := cli.Run(context.Background(), out(draft, destination), cli.Deps{
		Stdout: failingWriter{events: &events}, Stderr: &stderr,
		Env:       func(string) (string, bool) { return "", false },
		ReadFile:  os.ReadFile,
		Getwd:     func() (string, error) { return "/somewhere", nil },
		Service:   &rewriteService{result: improved("the revision\n")},
		Publisher: publisher,
	})

	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("the publisher was called %d times; the document must still have been written",
			len(publisher.published))
	}
	if len(events) == 0 || events[0] != "published" {
		t.Errorf("events are %v; publication still comes first", events)
	}
	if strings.TrimSpace(stderr.String()) == "" {
		t.Error("nothing was written to stderr, so the user is told nothing about a failed receipt")
	}
}
