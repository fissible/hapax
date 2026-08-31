package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/eval/evaltest"
	"github.com/fissible/hapax/internal/store"
)

// Every bug in A2b that mattered was found by running the binary rather than by
// this package's tests: the distractor pool being split down to one cluster, eval
// unable to discover its own store, a deviation-internal error escaping as an
// operational failure, and Document.valid rejecting a payload the workflow
// itself produced. Three of the four live in the gap between a fake service and
// the real composition root — a gap every test here sits on one side of.
//
// This closes it for the commands that touch a store. It is deliberately end to
// end: a real corpus on disk, a real database, the real binary, and no fake
// anywhere. It is slower than the rest of the package and that is the price of
// exercising the path a person actually takes.
func TestTheBinaryIndexesProfilesAndEvaluatesForReal(t *testing.T) {
	binary := buildBinary(t)
	corpus, distractors := smokeCorpus(t)

	// index, from nothing.
	indexed := runBinary(t, binary, corpus, "--json", "index", "--profile", "essays", corpus)
	if indexed.code != 0 {
		t.Fatalf("index exited %d: %s", indexed.code, indexed.stderr)
	}
	indexResult := smokeResult(t, indexed.stdout, "index")
	if indexResult["mode"] != "profile-and-reference" {
		t.Fatalf("index mode = %v; the rest of this test needs a profile and a reference",
			indexResult["mode"])
	}

	// profile, discovering the store rather than being told where it is.
	profiled := runBinary(t, binary, corpus, "--json", "profile")
	if profiled.code != 0 {
		t.Fatalf("profile exited %d: %s", profiled.code, profiled.stderr)
	}
	profileResult := smokeResult(t, profiled.stdout, "profile")
	if profileResult["selection"] != "sole-head" {
		t.Errorf("selection = %v", profileResult["selection"])
	}

	// eval, likewise discovering it, against a real distractor pool.
	evaluated := runBinary(t, binary, corpus, "--json", "eval", "--profile", "essays", "--distractors", distractors)
	if evaluated.code != 0 && evaluated.code != 1 {
		t.Fatalf("eval exited %d, want 0 or 1: %s", evaluated.code, evaluated.stderr)
	}
	evalResult := smokeResult(t, evaluated.stdout, "eval")

	// The whole pool contributed. This is the shape of the bug that shipped
	// six segments from twenty documents.
	members, ok := evalResult["distractor_members"].(float64)
	if !ok || int(members) != smokeDistractors {
		t.Errorf("distractor_members = %v, want %d", evalResult["distractor_members"], smokeDistractors)
	}
	segments, ok := evalResult["distractor_segments"].(float64)
	if !ok || int(segments) != smokeDistractors*smokeDistractorParagraphs {
		t.Errorf("distractor_segments = %v over %d members", evalResult["distractor_segments"], smokeDistractors)
	}

	// score, against the same store, discovering it the same way. Before eval
	// has shipped a release this is the uncalibrated path: it must MEASURE and
	// refuse only the band, which is the contract ADR 0005 and DESIGN agree on
	// and the one most likely to be got wrong by refusing outright.
	draft := filepath.Join(corpus, "draft.md")
	write(t, draft, "A paragraph of ordinary prose that runs on past a single sentence so the "+
		"structure pass reads it as prose rather than as a heading; it says a thing.\n\n"+
		"A second paragraph doing likewise, at enough length to clear the floor.\n\n")

	measured := runBinary(t, binary, corpus, "--json", "score", draft)
	if measured.code != 4 {
		t.Fatalf("score exited %d, want 4 — no release has shipped: %s", measured.code, measured.stderr)
	}
	scoreResult := smokeResult(t, measured.stdout, "score")
	if scoreResult["calibrated"] != false {
		t.Errorf("calibrated = %v with no release", scoreResult["calibrated"])
	}
	segments, ok := scoreResult["segments"].([]any)
	if !ok || len(segments) == 0 {
		t.Fatalf("score refused and measured nothing: %v", scoreResult["segments"])
	}
	for i, raw := range segments {
		segment, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("segment %d is not an object", i)
		}
		band, ok := segment["band"].(map[string]any)
		if !ok {
			t.Fatalf("segment %d has no band member", i)
		}
		if band["defined"] != false {
			t.Errorf("segment %d was banded with nothing calibrated", i)
		}
		if band["reason"] != "uncalibrated" {
			t.Errorf("segment %d gives reason %v for its absent band", i, band["reason"])
		}
		if deltas, ok := segment["features"].([]any); !ok || len(deltas) == 0 {
			t.Errorf("segment %d kept no per-feature deltas", i)
		}
	}

	// And the calibrated path, which nothing else reaches through the binary:
	// the raw refusal above proves score MEASURES without a release, and this
	// proves it BANDS with one. A seeded release rather than a measured one,
	// because reaching a shippable release honestly costs about seven hundred
	// documents — what is under test here is the command, not the arithmetic.
	seeded := seedShippableRelease(t, filepath.Join(corpus, ".hapax", "hapax.sqlite3"))

	banded := runBinary(t, binary, corpus, "--json", "score", draft)
	if banded.code != 0 && banded.code != 1 {
		t.Fatalf("score exited %d, want 0 or 1 against a calibrated release: %s",
			banded.code, banded.stderr)
	}
	bandedResult := smokeResult(t, banded.stdout, "score")
	if bandedResult["calibrated"] != true {
		t.Errorf("calibrated = %v against a release head", bandedResult["calibrated"])
	}
	if bandedResult["release_id"] != seeded {
		t.Errorf("release_id = %v, want the seeded %q", bandedResult["release_id"], seeded)
	}
	bandedSegments, ok := bandedResult["segments"].([]any)
	if !ok || len(bandedSegments) == 0 {
		t.Fatalf("scored nothing: %v", bandedResult["segments"])
	}
	for i, raw := range bandedSegments {
		segment := raw.(map[string]any)
		band := segment["band"].(map[string]any)
		if band["defined"] != true {
			t.Errorf("segment %d was not banded against a calibrated release", i)
		}
		if name, _ := band["band"].(string); name == "" {
			t.Errorf("segment %d is banded and names no band", i)
		}
		if deltas, ok := segment["features"].([]any); !ok || len(deltas) == 0 {
			t.Errorf("segment %d carries no deltas", i)
		}
	}

	// eval with no distractors is a completed measurement, not a failure, and
	// its document renders — the validator rejecting its own producer is what
	// this line exists to catch.
	uncalibrated := runBinary(t, binary, corpus, "--json", "eval", "--profile", "essays")
	if uncalibrated.code != 1 {
		t.Fatalf("uncalibrated eval exited %d, want 1: %s", uncalibrated.code, uncalibrated.stderr)
	}
	if result := smokeResult(t, uncalibrated.stdout, "eval"); result["distractor_pool_id"] != nil {
		t.Errorf("named a pool %v it was not given", result["distractor_pool_id"])
	}
}

// And from a directory with no store above it, every command that needs one
// refuses rather than failing, and none of them creates a database to say so.
func TestTheBinaryRefusesRatherThanFailingWithNoStore(t *testing.T) {
	binary := buildBinary(t)
	empty := t.TempDir()

	for _, args := range [][]string{
		{"--json", "profile"},
		{"--json", "eval", "--profile", "essays"},
		{"--json", "score", "draft.md"},
	} {
		t.Run(args[1], func(t *testing.T) {
			got := runBinary(t, binary, empty, args...)
			if got.code != 4 {
				t.Errorf("exited %d, want 4 (a refusal): %s", got.code, got.stderr)
			}
			entries, err := os.ReadDir(empty)
			if err != nil {
				t.Fatalf("read dir: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("answering the question wrote %v", entries)
			}
		})
	}
}

const (
	smokeDocuments            = 60
	smokeDistractors          = 20
	smokeDistractorParagraphs = 6
)

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hapax")
	build := exec.Command("go", "build", "-o", binary, "github.com/fissible/hapax/cmd/hapax")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/hapax: %v\n%s", err, out)
	}
	return binary
}

type binaryRun struct {
	code           int
	stdout, stderr string
}

// runBinary runs from a working directory, because store discovery is a
// property of where the caller is standing and passing --store everywhere is
// how the missing discovery went unnoticed.
func runBinary(t *testing.T, binary, dir string, args ...string) binaryRun {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = dir
	var out, errOut []byte
	stdout, err := command.Output()
	out = stdout
	if exit, ok := err.(*exec.ExitError); ok {
		errOut = exit.Stderr
		return binaryRun{code: exit.ExitCode(), stdout: string(out), stderr: string(errOut)}
	}
	if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	return binaryRun{code: 0, stdout: string(out)}
}

func smokeResult(t *testing.T, encoded, command string) map[string]any {
	t.Helper()
	var document struct {
		Schema  string         `json:"schema"`
		Command string         `json:"command"`
		Result  map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatalf("decoding %q: %v", encoded, err)
	}
	if document.Schema != "hapax.v1" {
		t.Errorf("schema = %q", document.Schema)
	}
	if document.Command != command {
		t.Errorf("command = %q, want %q", document.Command, command)
	}
	return document.Result
}

// smokeCorpus writes an author corpus and a register-matched distractor pool.
func smokeCorpus(t *testing.T) (corpus, distractors string) {
	t.Helper()
	corpus, distractors = t.TempDir(), t.TempDir()
	for i := 0; i < smokeDocuments; i++ {
		body := ""
		for p := 0; p < 10; p++ {
			body += fmt.Sprintf("Document %d paragraph %d carries ordinary prose past a single "+
				"sentence so the structure pass does not read it as a heading; it names %d.\n\n",
				i, p, i*100+p)
		}
		write(t, filepath.Join(corpus, fmt.Sprintf("doc%03d.md", i)), body)
	}
	for i := 0; i < smokeDistractors; i++ {
		body := ""
		for p := 0; p < smokeDistractorParagraphs; p++ {
			body += fmt.Sprintf("Another writer's paragraph %d in piece %d, running on well past a "+
				"single sentence in a manner of its own so the structure pass reads it as prose; "+
				"it mentions %d.\n\n", p, i, i*1000+p)
		}
		write(t, filepath.Join(distractors, fmt.Sprintf("other%03d.md", i)), body)
	}
	return corpus, distractors
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedShippableRelease installs a release at the head of the store the binary
// will open, so the calibrated path can be exercised without earning a release
// the expensive way.
func seedShippableRelease(t *testing.T, path string) string {
	t.Helper()
	opened, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer opened.Close()

	bundle, err := opened.LoadProfileBundle(context.Background(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	if bundle.Reference.ID == "" {
		t.Fatal("the indexed corpus has no reference to calibrate against")
	}
	release := evaltest.ShippableRelease(t, bundle.Profile.ID, bundle.Reference.ID)
	if err := opened.PutRelease(context.Background(), release, "", store.AdvanceHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}
	return release.ID
}
