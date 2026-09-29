package cli_test

// #109, at the terminal. `Index` counts the documents it screened out as this
// tool's own prose; a reader who never sees that number cannot act on it.
//
// # Why this one renders only when it is not zero
//
// `fields` draws a rule — EMPTY is an absence and is omitted, ZERO is a
// measurement and is not — and `below-floor=0` exists because of it. The index
// line is the exception that proves it: it carries `store`, `mode` and
// `adversity` and NO counts at all. `documents`, `eligible`, `nodes`,
// `calibrate_segments` and `train_paragraphs` are JSON-only.
//
// So `tool-output=0` would be the only count on that line, on every ordinary run,
// saying nothing — which is #65's complaint arriving by the front door. A
// non-zero one is not a measurement among peers, it is a finding: some of what
// you indexed was written by this tool. It renders when there is a finding.
//
// The JSON keeps the count unconditionally, beside the five other counts, so a
// consumer can tell zero from absent without parsing a line.
//
// The two keys differ on purpose and the tests below index both: the envelope's
// is `tool_output_documents`, naming what it counts like its five siblings; the
// line's is `tool-output`, short like every other key there, reading as the
// check's finding. A rename that swept the line's key would leave the
// non-zero assertion red against a correct implementation and the zero
// assertion green forever, which is how this comment came to be here.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"

	"github.com/fissible/hapax/internal/workflow"
)

// screenedIndex is `fullIndex` with a tool-output count, so every other member
// is the shape the rest of this package already asserts.
func screenedIndex(contaminated int) workflow.IndexResult {
	result := fullIndex()
	result.Eligible = result.Documents - contaminated
	result.ToolOutputDocuments = contaminated
	// The check reports whichever way it went. An earlier draft left the zero
	// branch on `not-performed`, which is a state a screened index can no longer
	// produce — so the test that must prove "a zero renders nothing" was the one
	// modelling something impossible.
	state, reason := "passed", ""
	if contaminated > 0 {
		state, reason = "failed", "documents hold paragraphs this tool published"
	}
	for i := range result.Checks {
		if result.Checks[i].Name == "tool-output" {
			result.Checks[i] = workflow.Check{Name: "tool-output", State: state,
				Reason: reason, Version: corpus.ToolOutputCheckVersion}
		}
	}
	return result
}

// The envelope carries the count whether or not anything was found.
func TestTheIndexEnvelopeAlwaysCarriesTheToolOutputDocumentCount(t *testing.T) {
	for _, count := range []int{3, 0} {
		t.Run(map[bool]string{true: "some were screened out", false: "none were"}[count > 0],
			func(t *testing.T) {
				got := runWith(t, &fakeService{indexResult: screenedIndex(count)},
					"--json", "index", "/w", "--profile", "essays")
				if got.code != 0 {
					t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
				}
				var envelope struct {
					Result map[string]any `json:"result"`
				}
				if err := json.Unmarshal([]byte(got.stdout), &envelope); err != nil {
					t.Fatalf("decode %q: %v", got.stdout, err)
				}
				value, carried := envelope.Result["tool_output_documents"]
				if !carried {
					t.Fatalf("the envelope has no tool_output_documents: %s", got.stdout)
				}
				if value != float64(count) {
					t.Errorf("tool_output_documents = %v, want %d", value, count)
				}
			})
	}
}

// The human line says it when there is something to say, and stays quiet
// otherwise.
func TestTheIndexLineReportsAFindingAndNotAZero(t *testing.T) {
	found := runWith(t, &fakeService{indexResult: screenedIndex(3)},
		"index", "/w", "--profile", "essays")
	if found.code != 0 {
		t.Fatalf("code = %d, stderr %q", found.code, found.stderr)
	}
	if got := humanFields(t, found.stdout)["tool-output"]; got != "3" {
		t.Errorf("tool-output=%q, want 3: %q", got, found.stdout)
	}

	clean := runWith(t, &fakeService{indexResult: screenedIndex(0)},
		"index", "/w", "--profile", "essays")
	if clean.code != 0 {
		t.Fatalf("code = %d, stderr %q", clean.code, clean.stderr)
	}
	if _, present := humanFields(t, clean.stdout)["tool-output"]; present {
		t.Errorf("a clean index printed tool-output: %q", clean.stdout)
	}
}

// A negative count is incoherent and must not render.
//
// Exactly one incoherence in the fixture: `screenedIndex(-1)` would also set
// `Eligible` above `Documents`, so a guard on THAT would make this green with no
// count guard at all. Eligible is put back, leaving the negative count as the
// only thing wrong.
func TestRenderRefusesANegativeToolOutputDocumentCount(t *testing.T) {
	result := screenedIndex(0)
	result.ToolOutputDocuments = -1

	got := runWith(t, &fakeService{indexResult: result}, "index", "/w", "--profile", "essays")

	if got.code == 0 {
		t.Fatalf("a negative count rendered and exited 0: %q", got.stdout)
	}
	if !strings.Contains(got.stderr, "incoherent index result") {
		t.Errorf("stderr is %q and does not say what was wrong", got.stderr)
	}
}
