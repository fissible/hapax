package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Publication is where this slice can lose data that the migration chain never
// could. The chain built the destination in place and a crash left a
// half-migrated database that the next open refuses; a copy that goes wrong can
// leave a file that looks finished and is not.
//
// So the destination is never written directly. The template is copied to a
// staging file beside it, stamped, synced, and then published with a primitive
// that fails rather than overwrites. These tests are about that, and they are
// in-package because the failure has to be injected.

// Two processes indexing the same corpus at once is an ordinary thing, and the
// stat-then-open window has always been there. Publication that refuses to
// clobber closes it: whoever loses the race opens the winner's database rather
// than replacing it.
func TestConcurrentOpensOfOneAbsentPathAllGetTheSameStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contended.db")

	const racers = 8
	var wait sync.WaitGroup
	results := make([]error, racers)
	versions := make([]int, racers)
	wait.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wait.Done()
			s, err := Open(path)
			if err != nil {
				results[i] = err
				return
			}
			defer s.Close()
			versions[i], results[i] = s.SchemaVersion(context.Background())
		}(i)
	}
	wait.Wait()

	for i, err := range results {
		if err != nil {
			t.Errorf("racer %d: %v", i, err)
			continue
		}
		if versions[i] != len(migrations)-1 {
			t.Errorf("racer %d sees schema version %d, want %d", i, versions[i], len(migrations)-1)
		}
	}
	if left := stagingFiles(t, dir); len(left) != 0 {
		t.Errorf("staging files survived the race: %v", left)
	}
	// One database, not eight. A publish that overwrote would leave the last
	// writer's file and every earlier handle pointing at a replaced inode.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	databases := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".db" {
			databases++
		}
	}
	if databases != 1 {
		t.Errorf("%d databases in the directory, want 1", databases)
	}
}

// A publish that fails for any reason other than the destination already
// existing must leave nothing: no destination, no staging file. Chaining into
// the destination instead would follow a failed atomic publication with a
// non-atomic one, which is worse than refusing.
func TestAFailedPublishLeavesNoDestinationAndNoStaging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unpublishable.db")

	refused := errors.New("no links on this filesystem")
	d, record := recordingDeps()
	d.Link = func(string, string) error { return refused }

	if _, err := open(path, "sqlite", d); !errors.Is(err, refused) {
		t.Fatalf("error is %v, want the publish failure", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed publish left a destination behind: %v", err)
	}
	if left := stagingFiles(t, dir); len(left) != 0 {
		t.Errorf("a failed publish left staging behind: %v", left)
	}
	// Not a fallback: the destination directory is involved by this point, and
	// chaining into it after an atomic publish failed is the thing that would
	// produce a partial database.
	if len(record.chained) != 0 {
		t.Errorf("a failed publish fell back to chaining into %v", record.chained)
	}
}

// Losing the publish race is not a failure. The destination appearing between
// staging and publication means somebody else finished first, so the loser
// discards its staging and opens what is there.
func TestLosingThePublishRaceOpensTheWinnersStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lost.db")

	var attempted error
	d, record := recordingDeps()
	d.Link = func(staging, destination string) error {
		// Somebody else published while this open was staging.
		winner, err := open(destination, "sqlite", chainOnly())
		if err != nil {
			return err
		}
		if err := winner.Close(); err != nil {
			return err
		}
		attempted = os.Link(staging, destination)
		return attempted
	}

	s, err := open(path, "sqlite", d)
	if err != nil {
		t.Fatalf("losing the race is not an error: %v", err)
	}
	defer s.Close()

	version, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != len(migrations)-1 {
		t.Errorf("schema version %d, want %d", version, len(migrations)-1)
	}
	if left := stagingFiles(t, dir); len(left) != 0 {
		t.Errorf("the loser left its staging behind: %v", left)
	}
	if len(record.materialised) != 0 {
		t.Errorf("the loser reported publishing %v", record.materialised)
	}
	// The collision must be the real one. If the link failed for some other
	// reason this test would still pass while proving nothing about losing a
	// race, which is what it is named for.
	if !errors.Is(attempted, fs.ErrExist) {
		t.Errorf("the link attempt failed with %v, want fs.ErrExist", attempted)
	}
}

// Losing to something that is not a hapax store is not the same as losing to
// one. The loser still must not overwrite it, must clean up its staging, and
// must report the refusal the file deserves rather than the race.
func TestLosingThePublishRaceToAForeignFileRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foreign.db")
	foreign := []byte("someone else's file, created mid-publish\n")

	d, record := recordingDeps()
	d.Link = func(staging, destination string) error {
		if err := os.WriteFile(destination, foreign, 0o644); err != nil {
			return err
		}
		return os.Link(staging, destination)
	}

	_, err := open(path, "sqlite", d)
	if !errors.Is(err, ErrSchemaForeign) {
		t.Fatalf("error is %v, want ErrSchemaForeign", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(after) != string(foreign) {
		t.Errorf("the foreign file was rewritten:\n%q", after)
	}
	if left := stagingFiles(t, dir); len(left) != 0 {
		t.Errorf("staging survived a refusal: %v", left)
	}
	if len(record.materialised) != 0 {
		t.Errorf("a refused open reported publishing %v", record.materialised)
	}
}

// chainOnly is the winner's route in the test above: it must not recurse back
// into publication, or the injected Link runs again.
func chainOnly() deps {
	d := realDeps()
	d.ForceMigrationChain = true
	return d
}

// A lazily built template with an unlocked build window lets several concurrent
// first opens each build one, then converge on whichever landed in the cache.
// Nothing is a lie and nothing races: every store is correct, the cached path is
// consistent afterwards, and the process has paid for the migration chain
// several times over — which is the whole cost this slice removes.
//
// TestTheTemplateIsBuiltOncePerProcess cannot see it: it is sequential, and it
// runs against a cache some earlier test has already warmed.
//
// The barrier is what makes this deterministic, and where it sits is the whole
// point. deps.TemplateNeeded is called by EVERY caller that is about to obtain
// the template, before any serialisation — not on entry to the build, which is
// already past the cache-miss decision and would let one goroutine finish
// build-and-cache before another arrived. Holding all of them there and then
// releasing at once means a serialised implementation builds exactly one
// template and an unserialised one builds several. Both produce the same number
// of arrivals, so the arrivals are the barrier and the build count is the
// assertion.
func TestConcurrentFirstOpensBuildOneTemplate(t *testing.T) {
	resetTemplateCache()
	t.Cleanup(resetTemplateCache)

	const racers = 8
	arrived := make(chan struct{}, racers)
	release := make(chan struct{})

	dir := t.TempDir()
	var wait sync.WaitGroup
	errs := make([]error, racers)
	wait.Add(racers)
	before := templateBuilds()
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wait.Done()
			d, _ := recordingDeps()
			d.TemplateNeeded = func() {
				arrived <- struct{}{}
				<-release
			}
			s, err := open(filepath.Join(dir, fmt.Sprintf("racer%d.db", i)), "sqlite", d)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = s.Close()
		}(i)
	}

	// Every racer is past the cache check and before the build. Nothing has been
	// cached, so an implementation that does not serialise sends all of them
	// into a build of their own the moment they are released.
	//
	// The watchdog is not part of the verdict — release still happens only after
	// all eight have arrived, so the build count stays deterministic. It is here
	// because an implementation that calls the hook after taking the lock, or
	// not at all, would otherwise leave this test hanging until the package
	// timeout kills the whole run with no indication of which test or why.
	deadline := time.After(30 * time.Second)
	for i := 0; i < racers; i++ {
		select {
		case <-arrived:
		case <-deadline:
			close(release)
			t.Fatalf("received %d of %d arrivals at the template seam; it is reached after "+
				"serialisation, or not at all", i, racers)
		}
	}
	close(release)
	wait.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: %v", i, err)
		}
	}
	if built := templateBuilds() - before; built != 1 {
		t.Errorf("%d concurrent first opens built the template %d times, want 1", racers, built)
	}
}
