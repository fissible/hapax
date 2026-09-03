package cli_test

// The real binary, on a corpus that has no release at all.
//
// Everything above this file is a fake at some seam. This is the only test that
// answers the question #81 actually asked: can a person with a corpus too small
// to calibrate rewrite a paragraph? The store here has a profile and a
// reference and no release, which is the state `hapax index` leaves a corpus in
// before `hapax eval` has ever run — and, for a corpus of fifty documents, the
// state it stays in.
//
// It asserts what only this composition can get wrong, and nothing that
// internal/rewrite, internal/workflow or #79's filesystem matrix already own.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// uncalibratedRewritableCorpus is rewritableCorpus without the release: enough
// to measure, not enough to band.
func uncalibratedRewritableCorpus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		body := ""
		for p := 0; p < 10; p++ {
			body += fmt.Sprintf(rewriteShapes[(i+p)%len(rewriteShapes)], i*100+p)
		}
		write(t, filepath.Join(root, fmt.Sprintf("doc%03d.md", i)), body)
	}
	binary := buildBinary(t)
	indexed := runBinary(t, binary, root, "--json", "index", "--profile", "essays", root)
	if indexed.code != 0 {
		t.Fatalf("index exited %d: %s", indexed.code, indexed.stderr)
	}
	if mode := smokeResult(t, indexed.stdout, "index")["mode"]; mode != "profile-and-reference" {
		t.Fatalf("index mode = %v; measuring a draft needs a profile and a reference", mode)
	}
	// The fixture's value depends on there being no usable calibration. If a
	// future change to index produced one, every assertion below would be about
	// the calibrated path while claiming to be about the uncalibrated one.
	requireNoUsableCalibration(t, binary, root)
	return root
}

// requireNoUsableCalibration fails unless the real score command reports this
// corpus as uncalibrated.
//
// It proves operational state, not database absence: what matters downstream is
// that nothing here can band a paragraph, which is exactly what score refusing
// "uncalibrated" establishes. A test asserting no release ROW would be checking
// a different and less relevant fact.
func requireNoUsableCalibration(t *testing.T, binary, root string) {
	t.Helper()
	draft := filepath.Join(root, "probe.md")
	write(t, draft, "A paragraph of ordinary prose that runs on past a single sentence so the "+
		"structure pass reads it as prose rather than as a heading; it says a thing.\n")
	defer os.Remove(draft)

	scored := runBinary(t, binary, root, "--json", "score", draft, "--profile", "essays")
	result := smokeResult(t, scored.stdout, "score")
	if result["refusal"] != "uncalibrated" {
		t.Fatalf("score on this corpus refused %v, want uncalibrated; the fixture is not uncalibrated",
			result["refusal"])
	}
}

// A loopback provider, kept separate from rewrite_smoke_test.go's so the two
// smokes cannot interfere.
type namedTargetStub struct {
	mu       sync.Mutex
	prompts  []string
	replies  []string
	requests int
}

func (s *namedTargetStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		prompt, _ := decoded["prompt"].(string)
		s.mu.Lock()
		s.prompts = append(s.prompts, prompt)
		s.requests++
		reply := s.replies[min(s.requests-1, len(s.replies)-1)]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":` + quote(reply) + `,"done":true}`))
	})
}

func (s *namedTargetStub) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

// The whole point, end to end: no release, a named paragraph, a real rewrite.
func TestTheBinaryRewritesANamedParagraphWithoutACalibratedRelease(t *testing.T) {
	binary := buildBinary(t)
	root := uncalibratedRewritableCorpus(t)

	const first = "A paragraph of ordinary prose that runs on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing about café."
	const second = "A second paragraph doing likewise, at enough length to clear the floor and " +
		"be measured on its own terms rather than skipped entirely."
	draft := filepath.Join(root, "draft.md")
	body := first + "\n\n" + second + "\n"
	write(t, draft, body)
	before := corpusTree(t, root)

	// The reply is checked against the real scorer before the run, so "it was
	// accepted" is a fact about measurement rather than a hope about a stub.
	const accepted = "A paragraph of ordinary prose runs on past a single sentence. The structure " +
		"pass reads it as prose rather than as a heading, and it says a thing about café."
	requireCandidateImproves(t, binary, root, first, accepted)

	stub := &namedTargetStub{replies: []string{accepted}}
	server := httptest.NewServer(stub.handler())
	defer server.Close()

	destination := filepath.Join(root, "revised.md")
	got := runBinary(t, binary, root, "--json", "rewrite", draft,
		"--out", destination, "--profile", "essays", "--paragraphs", "0",
		"--provider", "ollama", "--model", "llama3",
		"--local-endpoint", server.URL, "--local-only")

	if got.code != 0 {
		t.Fatalf("rewrite exited %d on an uncalibrated store with a named paragraph: %s",
			got.code, got.stderr)
	}
	result := smokeResult(t, got.stdout, "rewrite")

	// 1. It said which question it answered. On an uncalibrated store there was
	//    no band to consult, so a result claiming one would be a fabrication.
	if result["selection"] != "explicit" {
		t.Errorf("selection = %v, want explicit", result["selection"])
	}
	if result["claim"] != "closer-by-distance" {
		t.Errorf("claim = %v, want closer-by-distance", result["claim"])
	}
	if available, ok := result["calibration_available"].(bool); !ok || available {
		t.Errorf("calibration_available = %v, want false on a store with no release",
			result["calibration_available"])
	}
	if reason, ok := result["refusal"].(string); ok && reason != "" {
		t.Errorf("refusal = %q on a run that exited 0", reason)
	}

	// 2. Exactly the named paragraph was sent, and the unnamed one never was.
	//    A run that ignored the flag and rewrote both would satisfy every count
	//    assertion below.
	prompts := stub.seen()
	if len(prompts) == 0 {
		t.Fatal("the provider was never called")
	}
	for i, prompt := range prompts {
		if !strings.Contains(prompt, first) {
			t.Errorf("prompt %d does not carry the named paragraph", i)
		}
		if strings.Contains(prompt, second) {
			t.Errorf("prompt %d carries the paragraph that was NOT named", i)
		}
	}
	if targets, ok := result["targets"].(float64); !ok || targets != 1 {
		t.Errorf("targets = %v, want exactly 1", result["targets"])
	}
	if improved, ok := result["improved"].(float64); !ok || improved != 1 {
		t.Errorf("improved = %v, want 1", result["improved"])
	}

	// 3. The whole document, byte for byte: the named paragraph replaced and
	//    NOTHING else touched.
	//
	//    A containment check is not this claim. "the unnamed paragraph is still
	//    present" is satisfied by a document that also appended to it, that
	//    reflowed the separators, or that added a trailing block. Exact
	//    equality is the only form that says what the comment says.
	published, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("the destination was not written: %v", err)
	}
	want := accepted + "\n\n" + second + "\n"
	if string(published) != want {
		t.Errorf("the published document is not the draft with one paragraph replaced.\n got: %q\nwant: %q",
			published, want)
	}
	if strings.Contains(string(published), first) {
		t.Error("the named paragraph was not rewritten")
	}
	if !strings.Contains(string(published), "café") {
		t.Error("the published document lost its multibyte text")
	}
	if after, err := os.ReadFile(draft); err != nil || string(after) != body {
		t.Error("the input was modified; --out has no authority over it")
	}
	requireTree(t, root, before, "revised.md")
}

// Naming a paragraph that does not exist refuses and writes nothing, through
// the real binary. The count is only known after scoring, so this cannot be a
// parse error, and a run that clamped 99 to the last paragraph would rewrite
// text the user never named.
func TestTheBinaryRefusesAParagraphThatDoesNotExist(t *testing.T) {
	binary := buildBinary(t)
	root := uncalibratedRewritableCorpus(t)
	draft := filepath.Join(root, "draft.md")
	write(t, draft, "A paragraph of ordinary prose that runs on past a single sentence so the "+
		"structure pass reads it as prose rather than as a heading; it says a thing.\n")
	before := corpusTree(t, root)

	destination := filepath.Join(root, "revised.md")
	got := runBinary(t, binary, root, "--json", "rewrite", draft,
		"--out", destination, "--profile", "essays", "--paragraphs", "99",
		"--provider", "ollama", "--model", "llama3", "--local-only")

	if got.code != 4 {
		t.Errorf("exit = %d, want 4", got.code)
	}
	var envelope struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &envelope); err != nil {
		t.Fatalf("decode %q: %v", got.stdout, err)
	}
	if envelope.Reason != "no-such-paragraph" {
		t.Errorf("reason = %q, want no-such-paragraph", envelope.Reason)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Error("a refusal wrote the destination")
	}
	requireTree(t, root, before)
}

// And without the flag, the same corpus still refuses. This is the regression
// that matters most: the weaker claim must stay unreachable unless asked for.
func TestTheBinaryStillRefusesAnUncalibratedStoreWithoutNamedParagraphs(t *testing.T) {
	binary := buildBinary(t)
	root := uncalibratedRewritableCorpus(t)
	draft := filepath.Join(root, "draft.md")
	write(t, draft, "A paragraph of ordinary prose that runs on past a single sentence so the "+
		"structure pass reads it as prose rather than as a heading; it says a thing.\n")

	got := runBinary(t, binary, root, "--json", "rewrite", draft,
		"--out", filepath.Join(root, "revised.md"), "--profile", "essays",
		"--provider", "ollama", "--model", "llama3", "--local-only")

	if got.code != 4 {
		t.Fatalf("exit = %d, want 4", got.code)
	}
	var envelope struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &envelope); err != nil {
		t.Fatalf("decode %q: %v", got.stdout, err)
	}
	if envelope.Reason != "uncalibrated" {
		t.Errorf("reason = %q, want uncalibrated", envelope.Reason)
	}
}

// The gates are still the gates, proven through the real command rather than
// through internal/rewrite's fakes.
//
// This is the test that fails an implementation which special-cases explicit
// selection: call the provider once, splice whatever came back, report the
// metadata the flag implies. That implementation passes every other assertion
// in this file, because everywhere else the scripted candidate happens to be an
// improvement. Here it is not, and the only correct behaviour is to change
// nothing.
func TestTheBinaryStillRefusesACandidateThatDoesNotImprove(t *testing.T) {
	binary := buildBinary(t)
	root := uncalibratedRewritableCorpus(t)

	const paragraph = "A paragraph of ordinary prose that runs on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing about café."
	draft := filepath.Join(root, "draft.md")
	body := paragraph + "\n"
	write(t, draft, body)

	// Checked against the real scorer, so "it does not improve" is a
	// measurement rather than an assumption about which string is worse.
	const refused = "A passage of plain prose that carries on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing about café."
	requireCandidateDoesNotImprove(t, binary, root, paragraph, refused)

	stub := &namedTargetStub{replies: []string{refused}}
	server := httptest.NewServer(stub.handler())
	defer server.Close()

	destination := filepath.Join(root, "revised.md")
	got := runBinary(t, binary, root, "--json", "rewrite", draft,
		"--out", destination, "--profile", "essays", "--paragraphs", "0",
		"--provider", "ollama", "--model", "llama3",
		"--local-endpoint", server.URL, "--local-only")

	// A run that changed nothing is adverse, not clean. Exit 1 is the whole
	// difference between "I improved your paragraph" and "I could not".
	if got.code != 1 {
		t.Fatalf("rewrite exited %d over a candidate that measures no better, want 1: %s",
			got.code, got.stderr)
	}
	// The envelope's own status, not only the exit code. A binary that exited 1
	// while rendering status "ok" would read to a script as a completed run.
	var envelope struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &envelope); err != nil {
		t.Fatalf("decode %q: %v", got.stdout, err)
	}
	if envelope.Status != "adverse" {
		t.Errorf("status = %q, want adverse", envelope.Status)
	}
	result := smokeResult(t, got.stdout, "rewrite")
	if result["rewrite_state"] != "none-improved" {
		t.Errorf("rewrite_state = %v, want none-improved", result["rewrite_state"])
	}
	if improved, ok := result["improved"].(float64); !ok || improved != 0 {
		t.Errorf("improved = %v, want 0", result["improved"])
	}
	// The provider WAS reached — this is a rejection by the loop, not a refusal
	// before it. An implementation that never called out would also report zero
	// improvements, and would be failing for a different reason.
	if len(stub.seen()) == 0 {
		t.Fatal("the provider was never called; the loop was not entered at all")
	}

	// And the document is the document — byte for byte, not merely containing
	// the original paragraph. Nothing was accepted, so nothing may differ: not
	// the paragraph, not the separators, not a trailing byte.
	published, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("the destination was not written: %v", err)
	}
	if string(published) != body {
		t.Errorf("a run that accepted nothing published a changed document.\n got: %q\nwant: %q",
			published, body)
	}
	if strings.Contains(string(published), "A passage of plain prose") {
		t.Error("a candidate that measures no better was spliced into the document anyway")
	}
	if after, err := os.ReadFile(draft); err != nil || string(after) != body {
		t.Error("the input was modified")
	}
}

// Explicit selection on a CALIBRATED store, through the whole command.
//
// This closes the gap between the plan and the rendered result: an
// implementation can compute explicit/closer-by-distance while planning and
// then have execution overwrite it with the store's calibrated verdict. Nothing
// below the binary can catch that, because the plan tests stop at the plan and
// the CLI tests hand the renderer a report they wrote themselves.
func TestTheBinaryDoesNotUpgradeTheClaimOnACalibratedStore(t *testing.T) {
	binary := buildBinary(t)
	root := rewritableCorpus(t)

	const paragraph = "A paragraph of ordinary prose that runs on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing about café."
	draft := filepath.Join(root, "draft.md")
	write(t, draft, paragraph+"\n")

	// The claim under test is that a REAL calibration does not upgrade an
	// explicit selection's claim. If this fixture were quietly uncalibrated,
	// the test would pass while proving nothing.
	requireUsableCalibration(t, binary, root, draft)

	const accepted = "A paragraph of ordinary prose runs on past a single sentence. The structure " +
		"pass reads it as prose rather than as a heading, and it says a thing about café."
	requireCandidateImproves(t, binary, root, paragraph, accepted)

	stub := &namedTargetStub{replies: []string{accepted}}
	server := httptest.NewServer(stub.handler())
	defer server.Close()

	got := runBinary(t, binary, root, "--json", "rewrite", draft,
		"--out", filepath.Join(root, "revised.md"), "--profile", "essays", "--paragraphs", "0",
		"--provider", "ollama", "--model", "llama3",
		"--local-endpoint", server.URL, "--local-only")

	if got.code != 0 {
		t.Fatalf("rewrite exited %d: %s", got.code, got.stderr)
	}
	result := smokeResult(t, got.stdout, "rewrite")
	if result["selection"] != "explicit" {
		t.Errorf("selection = %v, want explicit", result["selection"])
	}
	// The claim does NOT upgrade. A release existed, and it is still not what
	// this result rests on: no band chose this paragraph.
	if result["claim"] != "closer-by-distance" {
		t.Errorf("claim = %v, want closer-by-distance on a calibrated store under explicit selection",
			result["claim"])
	}
	// And the fact that a release existed is reported separately, which is the
	// only thing that distinguishes this run from the uncalibrated one.
	if available, ok := result["calibration_available"].(bool); !ok || !available {
		t.Errorf("calibration_available = %v, want true on a store with a shippable release",
			result["calibration_available"])
	}
}

// requireUsableCalibration is the other half of requireNoUsableCalibration: the
// real score command reports this corpus as calibrated, against a named
// release. Without it, the calibrated smoke could pass on a store that never
// had one.
func requireUsableCalibration(t *testing.T, binary, root, draft string) {
	t.Helper()
	scored := runBinary(t, binary, root, "--json", "score", draft, "--profile", "essays")
	if scored.code != 0 && scored.code != 1 {
		t.Fatalf("score exited %d: %s", scored.code, scored.stderr)
	}
	result := smokeResult(t, scored.stdout, "score")
	if calibrated, ok := result["calibrated"].(bool); !ok || !calibrated {
		t.Fatalf("score reports calibrated=%v; this fixture is meant to have a shippable release",
			result["calibrated"])
	}
	if id, ok := result["release_id"].(string); !ok || id == "" {
		t.Fatalf("score names no release; calibrated=true with no release id is incoherent")
	}
}
