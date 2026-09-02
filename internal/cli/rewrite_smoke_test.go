package cli_test

// The real binary, a real corpus, a real database, a real HTTP server, and a
// real file written to disk.
//
// Almost every defect that mattered in this project was found by running the
// binary rather than by a package's own tests: twenty distractors collapsing to
// one cluster behind a perfect AUC, `hapax eval` unable to discover its own
// store, a deviation-internal error escaping as an operational failure,
// `Document.valid` rejecting a payload its own workflow produced, `hapax score`
// looking up the empty register. Three of those live in the gap between a fake
// service and the real composition root — the gap every other test in this
// package sits on one side of.
//
// This is the one test that crosses every seam this slice adds at once: flag to
// provider to loop to assembly to publication to rendered document. It is
// deliberately narrow. It does not reproduce the loop's matrix, the provider's,
// the assembler's or publication's — those have their own packages, and #79's
// filesystem tests in particular are not repeated here.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ollamaStub is a loopback provider. It records what it was asked and answers
// with a fixed rewrite, so the wire contract can be asserted without a model.
type ollamaStub struct {
	mu       sync.Mutex
	requests []map[string]any
	paths    []string
	// replies are served in order, so a run can be made to retry. The last one
	// repeats once they are exhausted.
	replies []string
}

func (o *ollamaStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		o.mu.Lock()
		o.requests = append(o.requests, decoded)
		o.paths = append(o.paths, r.URL.Path)
		o.mu.Unlock()
		o.mu.Lock()
		reply := o.replies[min(len(o.requests)-1, len(o.replies)-1)]
		o.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":` + quote(reply) + `,"done":true}`))
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func quote(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func (o *ollamaStub) seen() ([]map[string]any, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requests, o.paths
}

// The one crossing test. It asserts the four things only this composition can
// get wrong, and nothing that a lower package already owns.
func TestTheBinaryRewritesADraftForReal(t *testing.T) {
	binary := buildBinary(t)
	root := rewritableCorpus(t)

	// A draft carrying a byte-order mark and multibyte text, because byte
	// ownership is the risk this whole path carries and the BOM is where an
	// off-by-three lives.
	const paragraph = "A paragraph of ordinary prose that runs on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing about café."
	draft := filepath.Join(root, "draft.md")
	body := "\xef\xbb\xbf" + paragraph + "\n\nA second paragraph doing likewise, at enough " +
		"length to clear the floor and be measured on its own terms rather than skipped.\n"
	write(t, draft, body)
	// The fixture's release must actually make this draft a target, or the
	// assertions below hold over a run in which nothing happened.
	requireDraftIsATarget(t, binary, root, draft)
	before := corpusTree(t, root)

	// TWO replies, so the run retries. With a single accepted reply the loop
	// makes exactly one call and every "each request" assertion below is
	// vacuous — a command that got its first request right and then used the
	// wrong model, or dropped the passage, would pass.
	//
	// The first measures no better than the paragraph and is refused; the second
	// improves and is accepted. Both are checked against the real scorer, so the
	// retry is a fact rather than a hope.
	const refusedReply = "A passage of plain prose that carries on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing about café."
	const acceptedReply = "A paragraph of ordinary prose runs on past a single sentence. The structure " +
		"pass reads it as prose rather than as a heading, and it says a thing about café."
	requireCandidateDoesNotImprove(t, binary, root, paragraph, refusedReply)
	requireCandidateImproves(t, binary, root, paragraph, acceptedReply)

	stub := &ollamaStub{replies: []string{refusedReply, acceptedReply}}
	server := httptest.NewServer(stub.handler())
	defer server.Close()

	destination := filepath.Join(root, "revised.md")
	got := runBinary(t, binary, root, "--json", "rewrite", draft,
		"--out", destination, "--profile", "essays",
		"--provider", "ollama", "--model", "llama3",
		"--local-endpoint", server.URL, "--local-only")

	if got.code != 0 {
		t.Fatalf("rewrite exited %d, want 0; the reply was checked to be an improvement: %s",
			got.code, got.stderr)
	}
	result := smokeResult(t, got.stdout, "rewrite")

	// 1. The configured provider was actually reached, with the model the flag
	//    named and the paragraph the plan targeted.
	//
	//    What is deliberately NOT asserted here: stream=false, the exact request
	//    encoding, the prompt's fencing. Those are internal/llm's contract and
	//    internal/rewrite's, both of which pin them against their own packages.
	//    Repeating them here would mean two places to update and one of them
	//    would drift. What only this test can say is that the flag reached that
	//    provider at all.
	requests, paths := stub.seen()
	if len(requests) == 0 {
		t.Fatal("the provider was never called; nothing downstream of the flag was exercised")
	}
	for i, path := range paths {
		if path != "/api/generate" {
			t.Errorf("request %d went to %q; the ollama arm was not the one that ran", i, path)
		}
	}
	// EVERY request, not the first. A loop that used the right model once and
	// something else afterwards, or leaked on its second call, would pass a
	// check that only ever looked at requests[0].
	if len(requests) < 2 {
		t.Fatalf("the provider was called %d times; the fixture scripts a refusal then an "+
			"acceptance, so a retry must have happened or the per-request checks below say "+
			"nothing about retries", len(requests))
	}
	var prompts []string
	for i, request := range requests {
		if request["model"] != "llama3" {
			t.Errorf("request %d carried model %v, want the one the flag named", i, request["model"])
		}
		prompt, _ := request["prompt"].(string)
		if prompt == "" {
			t.Errorf("request %d carried no prompt", i)
		}
		prompts = append(prompts, prompt)
	}
	// EVERY prompt carries the paragraph being rewritten. The loop advances its
	// current text only on acceptance, and the first candidate was refused, so
	// the retry must still be asking about the original.
	for i, prompt := range prompts {
		if !strings.Contains(prompt, paragraph) {
			t.Errorf("prompt %d does not carry the paragraph it was supposed to rewrite", i)
		}
	}
	// 2. Nothing from the corpus but the exemplars this run actually selected,
	//    checked against the selection it persisted rather than against an
	//    assumption about which paragraphs a correct selector would pick.
	requirePromptCarriesExactlyTheSelectedExemplars(t, root, prompts)

	// 3. The destination exists, is what the run says it is, and the input is
	//    untouched. This is the only test in the project that checks a real
	//    file written by the real binary.
	published, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("the destination was not written: %v", err)
	}
	if !strings.HasPrefix(string(published), "\xef\xbb\xbf") {
		t.Error("the published document lost its byte-order mark")
	}
	if !strings.Contains(string(published), "café") {
		t.Error("the published document lost its multibyte text")
	}
	after, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft back: %v", err)
	}
	if string(after) != body {
		t.Error("the input was modified; --out has no authority over it")
	}

	// And nothing else of the user's was touched: not another corpus document,
	//    not a neighbour of the destination.
	requireTree(t, root, before, "revised.md")

	// 4. The document says both states and the counts, and says something. Key
	//    presence alone is satisfied by a result full of zeroes, which is what a
	//    run that did nothing would produce.
	if result["plan_state"] != "targets-planned" {
		t.Errorf("plan_state = %v, want targets-planned; the fixture was checked to have targets",
			result["plan_state"])
	}
	if result["rewrite_state"] != "improved" {
		t.Errorf("rewrite_state = %v, want improved; the reply was checked to be one",
			result["rewrite_state"])
	}
	targets, ok := result["targets"].(float64)
	if !ok || targets < 1 {
		t.Errorf("targets = %v, want at least one", result["targets"])
	}
	if count, ok := result["improved"].(float64); !ok || count < 1 {
		t.Errorf("improved = %v, want at least one", result["improved"])
	}
	// The receipt names the file that was actually written. A path that named
	// the draft, or nothing, would be a plausible-looking lie.
	if result["path"] != destination {
		t.Errorf("path = %v, want the destination %q", result["path"], destination)
	}

	// And the published document is genuinely different from the input. Every
	// assertion above is satisfied by a byte-for-byte copy if the counts happen
	// to be right.
	if string(published) == body {
		t.Error("the destination is byte-identical to the draft; nothing was actually rewritten")
	}
	if !strings.Contains(string(published), "structure pass reads it as prose") {
		t.Error("the destination does not contain the rewritten paragraph")
	}
}

// Refusing to overwrite is the property a user's other work depends on, and it
// is worth one real-binary run because every layer between the flag and the
// filesystem could get it wrong.
func TestTheBinaryRefusesToOverwriteThroughOut(t *testing.T) {
	binary := buildBinary(t)
	root := rewritableCorpus(t)
	draft := filepath.Join(root, "draft.md")
	write(t, draft, "A paragraph of ordinary prose that runs on past a single sentence so the "+
		"structure pass reads it as prose rather than as a heading; it says a thing.\n")

	destination := filepath.Join(root, "revised.md")
	const theirs = "something a person wrote and did not want replaced\n"
	write(t, destination, theirs)

	got := runBinary(t, binary, root, "rewrite", draft, "--out", destination,
		"--profile", "essays", "--provider", "ollama", "--model", "llama3", "--local-only")

	if got.code == 0 {
		t.Errorf("rewrite exited 0 over an existing destination")
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read the destination: %v", err)
	}
	if string(body) != theirs {
		t.Errorf("the destination is now %q; --out overwrote a file it does not own", body)
	}
}

// A bare invocation names both destinations rather than guessing one. Guessing
// would mean a user who forgot the flag gets a file somewhere they did not ask
// for, or their draft replaced.
func TestTheBinaryNamesBothDestinationsWhenGivenNeither(t *testing.T) {
	binary := buildBinary(t)
	root := rewritableCorpus(t)
	draft := filepath.Join(root, "draft.md")
	write(t, draft, "A paragraph long enough to be admitted and measured on its own terms.\n")

	got := runBinary(t, binary, root, "rewrite", draft, "--model", "llama3")
	if got.code != 2 {
		t.Errorf("exit = %d, want 2", got.code)
	}
	for _, flag := range []string{"--out", "--in-place"} {
		if !strings.Contains(got.stderr, flag) {
			t.Errorf("the diagnostic %q does not name %s", strings.TrimSpace(got.stderr), flag)
		}
	}
	if strings.TrimSpace(got.stdout) != "" {
		t.Errorf("an invalid invocation printed %q", got.stdout)
	}
}

// Publication succeeded and rendering then failed. The file IS written, so the
// exit must not suggest otherwise by being 0, and must not suggest nothing
// happened either — the user has a new file and needs to know.
//
// Driven through the real binary with stdout closed, because that is the only
// way to make a write to it fail without a fake.
func TestTheBinaryReportsARenderFailureAfterASuccessfulPublication(t *testing.T) {
	binary := buildBinary(t)
	root := rewritableCorpus(t)
	draft := filepath.Join(root, "draft.md")
	write(t, draft, "A paragraph of ordinary prose that runs on past a single sentence so the "+
		"structure pass reads it as prose rather than as a heading; it says a thing.\n")
	requireDraftIsATarget(t, binary, root, draft)

	stub := &ollamaStub{replies: []string{"A paragraph of ordinary prose runs on past a single " +
		"sentence. The structure pass reads it as prose rather than as a heading, and it says a thing."}}
	server := httptest.NewServer(stub.handler())
	defer server.Close()

	destination := filepath.Join(root, "revised.md")
	got := runBinaryWithClosedStdout(t, binary, root, "rewrite", draft,
		"--out", destination, "--profile", "essays",
		"--provider", "ollama", "--model", "llama3",
		"--local-endpoint", server.URL, "--local-only")

	if got.code != 3 {
		t.Errorf("exit = %d, want 3; a failure to write the result is operational, and exit 2 "+
			"would tell a caller they had made an invalid invocation", got.code)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Errorf("the destination is missing: %v; publication came first and had already "+
			"succeeded, so a render failure must not undo it", err)
	}
}
