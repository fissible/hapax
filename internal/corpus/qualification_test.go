package corpus_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
)

// A corpus root with enough prose to walk.
func walkable(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"a.md": "A first document with several sentences in it. It says a little more than one line.\n",
		"b.md": "A second document, also with prose. It too continues past a single sentence.\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// hapax performs no qualification. Every check corpus declares comes back
// not-performed, and index therefore cannot report a clean corpus — only an
// indexed one. Pinned rather than assumed, because the whole claim that
// `ok` does not mean `clean` rests on it.
func TestEveryQualificationCheckIsNotPerformed(t *testing.T) {
	snapshot, err := corpus.Walk(walkable(t), corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	for name, status := range map[string]corpus.CheckStatus{
		"contamination":            snapshot.Contamination,
		"language":                 snapshot.Language,
		"structure":                snapshot.Structure,
		"git provenance":           snapshot.GitProvenance,
		"near-duplicate detection": snapshot.NearDuplicateDetection,
	} {
		if status.State != corpus.CheckNotPerformed {
			t.Errorf("the %s check is %q; nothing performs it, so index must not imply it did",
				name, status.State)
		}
	}
	for _, document := range snapshot.Documents {
		for name, status := range map[string]corpus.CheckStatus{
			"contamination": document.Contamination,
			"language":      document.Language,
			"structure":     document.Structure,
		} {
			if status.State != corpus.CheckNotPerformed {
				t.Errorf("%s: the %s check is %q", document.Path, name, status.State)
			}
		}
	}
}

// The check-state vocabulary is corpus's own, so the store can be closed over
// it rather than holding a second copy.
func TestTheCheckStateVocabularyIsDeclared(t *testing.T) {
	got := make([]string, 0, len(corpus.CheckStates()))
	for _, state := range corpus.CheckStates() {
		got = append(got, string(state))
	}
	sort.Strings(got)
	want := []string{"failed", "not-performed", "passed", "skipped-by-policy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("check states =\n%v\nwant\n%v", got, want)
	}
}

// cli passes declared values rather than inventing numbers, so the policy it
// passes has to exist here and be usable.
func TestTheDefaultPolicyIsUsable(t *testing.T) {
	policy := corpus.DefaultPolicy("essays")
	if policy.Register != "essays" {
		t.Errorf("register = %q, want the one it was given", policy.Register)
	}
	if policy.MinLexicalTokens <= 0 {
		t.Errorf("minimum lexical tokens = %d, want a positive floor", policy.MinLexicalTokens)
	}
	if policy.SplitSeed == "" {
		t.Error("the split seed is empty; splits would not be reproducible")
	}
	total := policy.Splits.Train + policy.Splits.Calibrate + policy.Splits.Test
	if total <= 0 {
		t.Errorf("split weights sum to %d", total)
	}
	if _, err := corpus.Walk(walkable(t), policy); err != nil {
		t.Errorf("the default policy does not walk a corpus: %v", err)
	}
}
