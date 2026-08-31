package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

// The five-way selection, as one table, because the interesting part is which
// outcomes are refusals and which are correctable invocations. A store with no
// head is an unmet precondition — the ordinary first-run state — and a name that
// does not exist is a typo the caller can fix.
func TestHowAProfileIsSelected(t *testing.T) {
	// Each case builds its own store, because the no-head case needs a store
	// that EXISTS and holds no head — a snapshot-only index of a corpus too
	// small to fit anything, which is the ordinary first-run state. Pointing at
	// a database that was never created would test the discovery failure
	// instead, which is a different thing and is tested below.
	indexedRoot := func(t *testing.T, registers ...string) string {
		t.Helper()
		root := corpusOf(t, 60)
		for _, register := range registers {
			request := indexRequest(root)
			request.Register = register
			if _, err := workflow.Default().Index(ctx(), request); err != nil {
				t.Fatalf("Index %q: %v", register, err)
			}
		}
		return root
	}
	headlessRoot := func(t *testing.T) string {
		t.Helper()
		root := corpusOf(t, 2)
		result := indexed(t, indexRequest(root))
		if result.ProfileID != "" {
			t.Fatalf("the fixture fitted a profile; it was meant to leave no head")
		}
		return root
	}

	for _, c := range []struct {
		name      string
		root      func(*testing.T) string
		asked     string
		outcome   workflow.Selection
		register  string
		available []string
	}{
		{
			name:    "the sole head, unasked",
			root:    func(t *testing.T) string { return indexedRoot(t, "essays") },
			outcome: workflow.SelectedSoleHead, register: "essays",
		},
		{
			name: "the sole head, named", asked: "essays",
			root:    func(t *testing.T) string { return indexedRoot(t, "essays") },
			outcome: workflow.SelectedExplicit, register: "essays",
		},
		{
			name: "one of several, named", asked: "letters",
			root:    func(t *testing.T) string { return indexedRoot(t, "essays", "letters") },
			outcome: workflow.SelectedExplicit, register: "letters",
		},
		{
			name:    "several, unasked",
			root:    func(t *testing.T) string { return indexedRoot(t, "essays", "letters") },
			outcome: workflow.SelectionAmbiguous, available: []string{"essays", "letters"},
		},
		{
			name: "a name that does not exist", asked: "reviews",
			root:    func(t *testing.T) string { return indexedRoot(t, "essays") },
			outcome: workflow.SelectionUnknownRegister, available: []string{"essays"},
		},
		{
			name:    "a store with no head at all",
			root:    headlessRoot,
			outcome: workflow.SelectionNoProfile,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := c.root(t)
			result, err := workflow.Default().Profile(ctx(), workflow.ProfileRequest{
				StorePath: defaultStorePath(root), Register: c.asked,
			})
			if err != nil {
				t.Fatalf("Profile: %v", err)
			}
			if result.Selection != c.outcome {
				t.Errorf("selection = %q, want %q", result.Selection, c.outcome)
			}
			if result.Profile.Register != c.register {
				t.Errorf("register = %q, want %q", result.Profile.Register, c.register)
			}
			// A caller that has to choose is told exactly what there is to
			// choose from, in a stable order.
			if len(c.available) > 0 {
				if len(result.Available) != len(c.available) {
					t.Fatalf("available = %v, want %v", result.Available, c.available)
				}
				for i, wanted := range c.available {
					if result.Available[i] != wanted {
						t.Errorf("available[%d] = %q, want %q", i, result.Available[i], wanted)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// profile has no operand, so it finds the store by walking up. What it must
// never do is create one: answering "is there a profile" by writing a database
// makes the answer wrong the next time it is asked.
func TestProfileDiscoversUpwardAndCreatesNothing(t *testing.T) {
	root := indexedCorpus(t)

	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	result, err := workflow.Default().Profile(ctx(), workflow.ProfileRequest{StartDir: deep})
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if result.StorePath != defaultStorePath(root) {
		t.Errorf("store = %q, want the ancestor's %q", result.StorePath, defaultStorePath(root))
	}
	if result.Selection != workflow.SelectedSoleHead {
		t.Errorf("selection = %q", result.Selection)
	}
}

// A directory with no store above it anywhere is the no-profile refusal, and
// the filesystem is left exactly as it was.
func TestProfileWithNoStoreAnywhereRefusesWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	before := entries(t, dir)

	result, err := workflow.Default().Profile(ctx(), workflow.ProfileRequest{StartDir: dir})
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if result.Selection != workflow.SelectionNoProfile {
		t.Errorf("selection = %q, want %q", result.Selection, workflow.SelectionNoProfile)
	}
	if after := entries(t, dir); len(after) != len(before) {
		t.Errorf("the query wrote something: %v became %v", before, after)
	}
}

// A .hapax directory holding no database stops the search. Falling through to an
// ancestor would answer with a different corpus's profile, which is worse than
// answering with none.
func TestAnEmptyMarkerStopsTheSearchRatherThanFallingThrough(t *testing.T) {
	outer := indexedCorpus(t)

	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(filepath.Join(inner, ".hapax"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	result, err := workflow.Default().Profile(ctx(), workflow.ProfileRequest{StartDir: inner})
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if result.Selection != workflow.SelectionNoProfile {
		t.Errorf("selection = %q; it fell through to %q", result.Selection, result.StorePath)
	}
}

// A .hapax that is a file is not a marker and not a store. Skipping past it
// would quietly answer from somewhere else.
func TestAMarkerThatIsAFileIsAFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".hapax"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := workflow.Default().Profile(ctx(), workflow.ProfileRequest{StartDir: dir}); err == nil {
		t.Error("walked past a .hapax that is a file")
	}
}

// An explicit --store is exact: no upward search, and a path that is not there
// is a failure rather than a refusal, because the caller named it.
func TestAnExplicitStoreIsNotSearchedFor(t *testing.T) {
	root := indexedCorpus(t)
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	request := workflow.ProfileRequest{
		StartDir:  nested,
		StorePath: filepath.Join(nested, "hapax.sqlite3"),
	}
	if _, err := workflow.Default().Profile(ctx(), request); err == nil {
		t.Error("an explicit store that is not there was searched around")
	}
}

// ---------------------------------------------------------------------------
// What a profile reports
// ---------------------------------------------------------------------------

// The stored profile, including the readiness #53 taught the store to hold —
// there is no second source for it, so a report that omitted it would be
// reporting less than the database knows.
func TestAProfileReportsWhatTheStoreHolds(t *testing.T) {
	root := corpusOf(t, 60)
	written := indexed(t, indexRequest(root))

	result, err := workflow.Default().Profile(ctx(), workflow.ProfileRequest{StartDir: root})
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if result.Profile.ID != written.ProfileID {
		t.Errorf("profile id = %q, want the indexed %q", result.Profile.ID, written.ProfileID)
	}
	if result.Profile.SnapshotID != written.SnapshotID {
		t.Errorf("snapshot id = %q, want %q", result.Profile.SnapshotID, written.SnapshotID)
	}
	if result.Profile.ProductionReady != (result.Profile.NotReadyReason == "") {
		t.Errorf("readiness contradicts its reason: ready=%v reason=%q",
			result.Profile.ProductionReady, result.Profile.NotReadyReason)
	}
	if len(result.Profile.Stats) == 0 {
		t.Error("a profile with no statistics is not a profile")
	}
	if result.ReferenceID != written.ReferenceID {
		t.Errorf("reference id = %q, want %q", result.ReferenceID, written.ReferenceID)
	}
	if result.Evaluated {
		t.Error("nothing has been evaluated; A2b is what does that")
	}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, entry := range found {
		out = append(out, entry.Name())
	}
	return out
}
