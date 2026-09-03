package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The template is built once, and everything a fresh open needs after that is a
// copy of it. Nothing else should accumulate.
//
// Added after the freeze, because it is a contract the frozen tests did not
// state and the first implementation therefore did not have to meet: it created
// a template directory on every call rather than on every build, and obtained
// the template twice per open, so a process leaked two directories for every
// store it created. Measured at 21,575 of them in one temp directory after a
// session's test runs.
//
// Nothing in the frozen set could see it. Those tests assert what a store
// contains and which route built it; a directory nobody deletes is neither.
func TestFreshOpensLeaveNoTemplateDirectoriesBehind(t *testing.T) {
	dir := t.TempDir()

	// Warm the cache first, so what this counts is the per-open cost rather than
	// the one directory the process's single template legitimately occupies.
	warm, err := Open(filepath.Join(dir, "warm.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := warm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before := templateDirectories(t)
	const opens = 5
	for i := 0; i < opens; i++ {
		s, err := Open(filepath.Join(dir, fmt.Sprintf("store%d.db", i)))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	if after := templateDirectories(t); after != before {
		t.Errorf("%d fresh opens left %d template directories behind, %.1f per open",
			opens, after-before, float64(after-before)/opens)
	}
}

// And the cache owns what it creates: resetting it takes the directory too, not
// just the database inside.
func TestResettingTheTemplateCacheRemovesItsDirectory(t *testing.T) {
	resetTemplateCache()
	t.Cleanup(resetTemplateCache)

	template, err := freshTemplate()
	if err != nil {
		t.Fatalf("freshTemplate: %v", err)
	}
	// This test names the directory it created rather than COUNTING the ones in
	// os.TempDir(). The count was a cross-package race: `go test ./...` runs
	// packages concurrently and internal/workflow builds its own templates in
	// the same shared directory, so a count taken before and after could move
	// for reasons that have nothing to do with this cache. It failed
	// intermittently once internal/store's own tests grew long enough to widen
	// the window.
	//
	// Naming the directory asserts the same property — the cache owns what it
	// creates, and resetting takes the directory and not just the database
	// inside — without depending on what any other package is doing.
	directory := filepath.Dir(template)
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("building a template created no directory: %v", err)
	}

	resetTemplateCache()
	if _, err := os.Stat(template); !os.IsNotExist(err) {
		t.Errorf("the template file survived the reset: %v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("the template DIRECTORY survived the reset: %v", err)
	}
}

func templateDirectories(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "hapax-template") {
			count++
		}
	}
	return count
}
