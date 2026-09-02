package cli_test

// The corpus the rewrite smoke runs against, and why it is not the one the
// other smoke uses.
//
// #75 established, by measuring, that a corpus of sixty copies of one sentence
// pattern has a degenerate reference distribution: every feature's rank
// saturates at the same end and every piece of prose measures the same constant.
// Against such a corpus no paragraph can be drifting relative to another and no
// candidate can ever improve, so a rewrite smoke built on it would exercise the
// flag, the provider and the publisher while never once entering the loop.
//
// So this corpus is written out of paragraph shapes that genuinely differ in
// length, clause structure and comma density. And the fixture CHECKS what it
// produced rather than assuming: requireDraftIsATarget runs the real `hapax
// score` and fails if the draft is in-range, because a smoke that publishes an
// unchanged document while asserting the provider was called would be asserting
// nothing at all.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/eval/evaltest"
	"github.com/fissible/hapax/internal/store"
)

// requirePromptCarriesOnlySelectedExemplars is the end-to-end privacy claim, and
// it is grounded in what was actually selected rather than in an assumption
// about what would be.
//
// An earlier version planted a sentinel in one corpus document and asserted it
// never appeared in a prompt. That is unsound in both directions: a conforming
// selector that legitimately chose that paragraph would fail the smoke, and
// leakage of any OTHER corpus text would pass it.
//
// This reads the selection the run actually persisted, rehydrates its members,
// and requires every corpus paragraph in the prompt to be one of them. ADR 0007
// says a handful of exemplars and no more; this is that sentence, checked at the
// only place where the whole path is real.
func requirePromptCarriesExactlyTheSelectedExemplars(t *testing.T, root string, prompts []string) {
	t.Helper()
	opened, err := store.Open(filepath.Join(root, ".hapax", "hapax.sqlite3"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer opened.Close()

	// The member node ids, read straight out of the table the run wrote them to.
	// `store` has no accessor for "every selection", and adding one so a test
	// could call it would be production API existing for a test's benefit.
	members := selectionMembers(t, filepath.Join(root, ".hapax", "hapax.sqlite3"))
	rehydrated, err := opened.Rehydrate(context.Background(), root, members)
	if err != nil {
		t.Fatalf("rehydrate the selection: %v", err)
	}
	selected := map[string]bool{}
	for _, member := range rehydrated {
		selected[member.Text] = true
	}
	if len(selected) == 0 {
		t.Fatal("the selection rehydrated to nothing, so this check would permit anything")
	}

	if len(prompts) == 0 {
		t.Fatal("no prompts were recorded, so this check would permit anything")
	}
	corpus := corpusParagraphs(t, root)
	for i, prompt := range prompts {
		// Nothing from the corpus but the selection.
		for _, paragraph := range corpus {
			if strings.Contains(prompt, paragraph) && !selected[paragraph] {
				t.Errorf("prompt %d carries a corpus paragraph that was not selected:\n%q", i, paragraph)
			}
		}
		// And ALL of the selection. Without this the check is satisfied by a run
		// that persists a valid selection and then sends no exemplars at all —
		// which leaks nothing and also rewrites against nothing, and is exactly
		// the shape ADR 0007 forbids from the other direction.
		for text := range selected {
			if !strings.Contains(prompt, text) {
				t.Errorf("prompt %d is missing a selected exemplar:\n%q", i, text)
			}
		}
	}
}

// selectionMembers is the ordered node ids of the one exemplar selection the run
// persisted.
func selectionMembers(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT selection_id, node_id FROM exemplar_member ORDER BY selection_id, ordinal")
	if err != nil {
		t.Fatalf("read exemplar_member: %v", err)
	}
	defer rows.Close()
	selections := map[string][]string{}
	var order []string
	for rows.Next() {
		var selection, node string
		if err := rows.Scan(&selection, &node); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, seen := selections[selection]; !seen {
			order = append(order, selection)
		}
		selections[selection] = append(selections[selection], node)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read exemplar_member: %v", err)
	}
	if len(order) != 1 {
		t.Fatalf("the store holds %d exemplar selections; the run should have made exactly one",
			len(order))
	}
	return selections[order[0]]
}

// corpusParagraphs is every admitted-looking paragraph of every corpus document,
// so the check above has something to look for rather than trusting a sentinel.
func corpusParagraphs(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "doc*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var out []string
	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, paragraph := range strings.Split(string(body), "\n\n") {
			if trimmed := strings.TrimSpace(paragraph); len(trimmed) > 40 {
				out = append(out, trimmed)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("the corpus has no paragraphs, so this check would permit anything")
	}
	return out
}

var rewriteShapes = []string{
	"The %d question here is a short one, and it is put plainly, without much in the way of hedging or apparatus.\n\n",
	"When the matter came up again in the %d meeting, which it did rather sooner than anyone had planned for, the argument that followed was long, circuitous, hedged about with qualifications, and in the end not especially conclusive about anything at all.\n\n",
	"Consider %d. Consider it again. The point is small. The point is repeated. Short sentences carry it.\n\n",
	"I had thought, before the %d revision, that the whole business would resolve itself; it did not, and the reasons why it did not are worth setting down at some length, because they recur.\n\n",
	"Numbers, commas, clauses, and a certain fondness for the list: these are the marks of the %d paragraph, and they are marks that a reader learns to recognise quickly enough.\n\n",
	"It is enough to say that the %d case failed, and that nobody involved was much surprised by it.\n\n",
	"Every so often a paragraph arrives that wants to be much longer than its neighbours, and this is one of them; it accumulates clauses, it doubles back on itself, it qualifies what it has just said, and it declines to stop until the reader has entirely lost the thread of the original claim about %d.\n\n",
	"Plain prose for %d, of moderate length, with one comma, and nothing else remarkable about it whatsoever.\n\n",
}

// rewritableCorpus writes a varied corpus, indexes it with the real binary, and
// installs a release under which ordinary prose is outside in-range — so a
// draft has something to be a target for.
func rewritableCorpus(t *testing.T) string {
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
		t.Fatalf("index mode = %v; a rewrite needs a profile and a reference", mode)
	}
	seedTargetRelease(t, filepath.Join(root, ".hapax", "hapax.sqlite3"))
	return root
}

// seedTargetRelease installs a release whose boundaries put ordinary prose
// outside in-range. ShippableRelease's own centres are chosen for scoring tests
// and say nothing about where this corpus's distances fall.
func seedTargetRelease(t *testing.T, path string) {
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
	release := evaltest.ReleaseAround(t, bundle.Profile.ID, bundle.Reference.ID, 0.05, 5.0)
	if !release.Shippable {
		t.Fatalf("the crafted release is not shippable (%s)", release.Reason)
	}
	if err := opened.PutRelease(context.Background(), release, "", store.AdvanceHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}
}

// requireDraftIsATarget fails unless the real binary measures this draft as
// something worth rewriting. Without it, a smoke that asserts "the provider was
// called" could pass on a draft the planner would never target — and would
// then be asserting only that nothing happened.
func requireDraftIsATarget(t *testing.T, binary, root, draft string) {
	t.Helper()
	scored := runBinary(t, binary, root, "--json", "score", draft, "--profile", "essays")
	if scored.code != 0 && scored.code != 1 {
		t.Fatalf("score exited %d: %s", scored.code, scored.stderr)
	}
	result := smokeResult(t, scored.stdout, "score")
	segments, ok := result["segments"].([]any)
	if !ok || len(segments) == 0 {
		t.Fatalf("score reported no segments for %s; there is nothing to rewrite", draft)
	}
	targets := 0
	for _, raw := range segments {
		segment, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		band, _ := segment["band"].(map[string]any)
		if name, _ := band["band"].(string); name == "drifting" || name == "not-you" {
			targets++
		}
	}
	if targets == 0 {
		t.Fatalf("every paragraph of %s is in-range, so the rewrite has no targets and the "+
			"smoke would assert nothing; the fixture's release needs different boundaries", draft)
	}
}

// entry is one thing found under the corpus root: its bytes if it is a file, its
// target if it is a symlink, and its kind either way.
type entry struct {
	Kind string // "file", "dir", "symlink"
	Body string
	Perm os.FileMode // permission bits only, which is all this compares
}

// requireTree fails if any of the USER's files under root changed other than the
// paths named — created, modified, removed, or changed kind or permission bits.
//
// The store is deliberately excluded. A rewrite that improves anything must
// record its attempts, so `.hapax` changing is the run doing its job; an earlier
// version of this snapshot included it and would have rejected every correct
// implementation. What the invariant is about is the user's own files, which a
// rewrite has no business touching beyond the one destination it was given.
//
// It also does not compare file modes in full, only permission bits, and it
// cannot say nothing OUTSIDE the root was touched. That would need a bounded
// filesystem seam, and the seam that has one is publish, whose own tests own
// that question.
func requireTree(t *testing.T, root string, before map[string]entry, mayChange ...string) {
	t.Helper()
	allowed := map[string]bool{}
	for _, path := range mayChange {
		allowed[path] = true
	}
	after := corpusTree(t, root)
	for path, now := range after {
		if allowed[path] || isStore(path) {
			continue
		}
		was, existed := before[path]
		switch {
		case !existed:
			t.Errorf("the run created %s, which it was not asked to write", path)
		case was.Kind != now.Kind:
			t.Errorf("%s was a %s and is now a %s", path, was.Kind, now.Kind)
		case was.Body != now.Body:
			t.Errorf("the run modified %s, which it was not asked to write", path)
		case was.Perm != now.Perm:
			t.Errorf("%s was %v and is now %v", path, was.Perm, now.Perm)
		}
	}
	for path := range before {
		if allowed[path] || isStore(path) {
			continue
		}
		if _, ok := after[path]; !ok {
			t.Errorf("the run removed %s", path)
		}
	}
}

// isStore reports whether a path is inside the database directory, which the
// run owns and this invariant does not speak for.
func isStore(path string) bool {
	return path == ".hapax" || strings.HasPrefix(path, ".hapax"+string(filepath.Separator))
}

// corpusTree walks everything under root, including dotfiles and the database
// directory, so a change anywhere beneath it is visible.
func corpusTree(t *testing.T, root string) map[string]entry {
	t.Helper()
	out := map[string]entry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[relative] = entry{Kind: "symlink", Body: target}
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			out[relative] = entry{Kind: "dir", Perm: info.Mode().Perm()}
		default:
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out[relative] = entry{Kind: "file", Body: string(body), Perm: info.Mode().Perm()}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s is empty, so this snapshot would compare nothing", root)
	}
	return out
}

// requireCandidateImproves fails unless the reply the stub will give measures
// strictly closer to the author than the paragraph it replaces.
//
// Without it the smoke can pass over a run that called the provider, rejected
// every candidate, and wrote an unchanged copy — the vacuous shape #75 found in
// its own fixture. The check uses the real binary's own scorer, so it is the
// same judgement the loop will make.
func requireCandidateImproves(t *testing.T, binary, root, current, candidate string) {
	t.Helper()
	before, after := measureBoth(t, binary, root, current, candidate)
	if !(after < before) {
		t.Fatalf("the scripted reply measures %v against the paragraph's %v, so the loop will "+
			"reject it and the smoke would assert only that nothing happened", after, before)
	}
}

// requireCandidateDoesNotImprove is the other half: a reply the loop must refuse,
// so a retry actually happens. Without checking it, a fixture meant to force a
// second call could be silently accepted on the first.
func requireCandidateDoesNotImprove(t *testing.T, binary, root, current, candidate string) {
	t.Helper()
	before, after := measureBoth(t, binary, root, current, candidate)
	if after < before {
		t.Fatalf("the reply meant to be refused measures %v against the paragraph's %v, so it "+
			"will be accepted on the first call and no retry will happen", after, before)
	}
}

func measureBoth(t *testing.T, binary, root, current, candidate string) (float64, float64) {
	t.Helper()
	measure := func(body string) float64 {
		probe := filepath.Join(t.TempDir(), "probe.md")
		write(t, probe, body+"\n")
		scored := runBinary(t, binary, root, "--json", "score", probe, "--profile", "essays")
		if scored.code != 0 && scored.code != 1 {
			t.Fatalf("score exited %d: %s", scored.code, scored.stderr)
		}
		segments, _ := smokeResult(t, scored.stdout, "score")["segments"].([]any)
		if len(segments) != 1 {
			t.Fatalf("%q measures %d segments; the loop requires exactly one", body, len(segments))
		}
		segment, _ := segments[0].(map[string]any)
		distance, _ := segment["distance"].(map[string]any)
		value, ok := distance["value"].(float64)
		if !ok {
			t.Fatalf("%q has no measured distance", body)
		}
		return value
	}
	return measure(current), measure(candidate)
}

// runBinaryWithClosedStdout runs the binary with stdout pointed at a closed
// pipe, so any write to it fails. It is the only way to exercise a rendering
// failure without a fake writer, and a fake writer cannot exist in a test that
// runs the real binary.
func runBinaryWithClosedStdout(t *testing.T, binary, dir string, args ...string) binaryRun {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close the read end: %v", err)
	}
	command := exec.Command(binary, args...)
	command.Dir = dir
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr
	runErr := command.Run()
	_ = writer.Close()

	code := 0
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		code = exit.ExitCode()
	} else if runErr != nil {
		t.Fatalf("run %s: %v", binary, runErr)
	}
	return binaryRun{code: code, stderr: stderr.String()}
}
