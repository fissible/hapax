package publish

// Why this file exists, and what publish_test.go cannot do.
//
// Final state cannot establish no-clobber. "The destination already existed,
// Create returned ErrExists, and its contents are unchanged" is satisfied
// exactly as well by an implementation that stats the destination and then
// RENAMES onto it — right up until the destination appears between the stat and
// the rename, at which point it silently destroys a file. The test passes; the
// user loses their work.
//
// Nor can final state establish atomicity. "Afterwards the file is wholly old
// or wholly new" says nothing about being interrupted, because nothing
// interrupted it.
//
// So these tests watch the mechanism through an injected seam, and — for the
// case that matters most — SCHEDULE THE RACE. A Link stub that creates the
// destination immediately before delegating is not a proxy for the race; it is
// a real interleaving, deterministically. A link-based implementation gets
// EEXIST from the kernel. A check-then-rename implementation overwrites.
//
// # What Replace's authority actually is
//
// `Replace` resolves the source once, when publication begins, and renames onto
// the path that resolution produced. So its authority is over **the object that
// path named at admission**, not over whatever the user's original spelling
// resolves to at the instant of the rename. A symlink retargeted in between
// leaves `Replace` replacing the former target.
//
// There is no portable rename-with-expected-inode, so this cannot be tightened
// into an identity guarantee. A last-moment `os.SameFile` was considered and
// rejected: it narrows the window without closing it, and a check that cannot
// deliver what it appears to promise is worse than a documented absence — the
// reasoning that closed #2 on the calibration claim. What is written down is
// the weaker true thing.
//
// # What these tests claim, precisely
//
// That the implementation asks the operating system for the right operations, in
// the right order, and handles their failures correctly. Call order is the
// honest limit of a unit test: a recorded Sync proves the implementation
// requested a flush, not that any particular filesystem, kernel, controller or
// power-loss sequence made the bytes durable. The tests say that rather than
// implying more.

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// The recorder
// ---------------------------------------------------------------------------

// step is one primitive the implementation asked for. Arguments are recorded,
// not just names, because "Link was called" is a much weaker claim than "Link
// was called with staging and the destination".
type step struct {
	Op   string
	Args []string
}

type recorder struct {
	steps []step
	// interpose runs before the named operation delegates, and is how a test
	// makes the world change underneath an implementation in progress.
	interpose map[string]func(t *testing.T, args []string)
	t         *testing.T
	// fail makes the named operation return this error instead of delegating.
	fail map[string]error
	// failAfter fails an operation only once another has already happened, so a
	// primitive used on both sides of publication can be failed on one side.
	failAfter map[string]struct {
		after string
		err   error
	}
	// staged is every path CreateTemp actually RETURNED. Recording the
	// directory it was asked for is not the same thing: an implementation can
	// remove any name it likes in that directory, which is how a fixed cleanup
	// name beside the source gets deleted.
	staged []string
}

func newRecorder(t *testing.T) *recorder {
	return &recorder{
		t: t, interpose: map[string]func(*testing.T, []string){}, fail: map[string]error{},
		failAfter: map[string]struct {
			after string
			err   error
		}{},
	}
}

func (r *recorder) note(op string, args ...string) error {
	r.steps = append(r.steps, step{Op: op, Args: args})
	if hook, ok := r.interpose[op]; ok {
		hook(r.t, args)
	}
	if err, ok := r.fail[op]; ok {
		return err
	}
	if when, ok := r.failAfter[op]; ok && r.count(when.after) > 0 {
		return when.err
	}
	return nil
}

// ops is the sequence of operation names, for order assertions.
func (r *recorder) ops() []string {
	out := make([]string, 0, len(r.steps))
	for _, s := range r.steps {
		out = append(out, s.Op)
	}
	return out
}

func (r *recorder) count(op string) int {
	n := 0
	for _, s := range r.steps {
		if s.Op == op {
			n++
		}
	}
	return n
}

func (r *recorder) argsOf(t *testing.T, op string) []string {
	t.Helper()
	for _, s := range r.steps {
		if s.Op == op {
			return s.Args
		}
	}
	t.Fatalf("%s was never called; the operations were %v", op, r.ops())
	return nil
}

// seam wires a recorder into the package's primitives, delegating to the real
// ones so the filesystem effects still happen and can be checked too.
func (r *recorder) seam() deps {
	real := realDeps()
	return deps{
		EvalSymlinks: func(path string) (string, error) {
			if err := r.note("EvalSymlinks", path); err != nil {
				return "", err
			}
			return real.EvalSymlinks(path)
		},
		Stat: func(path string) (os.FileInfo, error) {
			if err := r.note("Stat", path); err != nil {
				return nil, err
			}
			return real.Stat(path)
		},
		CreateTemp: func(dir, pattern string) (*os.File, error) {
			if err := r.note("CreateTemp", dir, pattern); err != nil {
				return nil, err
			}
			f, err := real.CreateTemp(dir, pattern)
			if f != nil {
				r.staged = append(r.staged, f.Name())
			}
			return f, err
		},
		Write: func(f *os.File, b []byte) (int, error) {
			if err := r.note("Write", f.Name()); err != nil {
				return 0, err
			}
			return real.Write(f, b)
		},
		Chmod: func(f *os.File, mode os.FileMode) error {
			if err := r.note("Chmod", f.Name(), mode.String()); err != nil {
				return err
			}
			return real.Chmod(f, mode)
		},
		Sync: func(f *os.File) error {
			if err := r.note("Sync", f.Name()); err != nil {
				return err
			}
			return real.Sync(f)
		},
		Close: func(f *os.File) error {
			if err := r.note("Close", f.Name()); err != nil {
				return err
			}
			return real.Close(f)
		},
		Link: func(from, to string) error {
			if err := r.note("Link", from, to); err != nil {
				return err
			}
			return real.Link(from, to)
		},
		Rename: func(from, to string) error {
			if err := r.note("Rename", from, to); err != nil {
				return err
			}
			return real.Rename(from, to)
		},
		Remove: func(path string) error {
			if err := r.note("Remove", path); err != nil {
				return err
			}
			return real.Remove(path)
		},
		OpenDir: func(path string) (*os.File, error) {
			if err := r.note("OpenDir", path); err != nil {
				return nil, err
			}
			return real.OpenDir(path)
		},
	}
}

// resolvedDir is the directory a publication will actually stage into, which is
// not always the spelling the caller used.
//
// These tests originally compared against the raw path, and on Darwin
// t.TempDir() returns /var/folders/... which resolves through the system's
// /private/var alias. That made the implementation carry a helper whose only
// job was to stop EvalSymlinks resolving one prefix, so an assertion here would
// pass — production behaviour bent to fit a test, which is the defect this
// project keeps finding and removing.
//
// The assertion was the wrong one. Replace is documented to resolve its source
// once and rename onto the path that resolution produced; requiring staging to
// sit in the directory AS THE CALLER SPELLED IT contradicts that. So the
// expectation resolves too.
func resolvedDir(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

func seamSource(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

// requireOrder fails unless the wanted operations appear in this order. Other
// operations may appear between them; what is pinned is the relative order of
// the ones named, because that is what the durability and no-clobber claims
// actually rest on.
func requireOrder(t *testing.T, got []string, wanted ...string) {
	t.Helper()
	at := 0
	for _, want := range wanted {
		found := -1
		for i := at; i < len(got); i++ {
			if got[i] == want {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("%s does not appear after the previous step; the operations were %v", want, got)
		}
		at = found + 1
	}
}

// ---------------------------------------------------------------------------
// The race, scheduled rather than argued
// ---------------------------------------------------------------------------

// The test this whole file exists for.
//
// The destination does not exist when the implementation looks, and DOES exist
// by the time it publishes. A link-based implementation is told EEXIST by the
// kernel and refuses. A check-then-rename implementation has already decided the
// destination is free and destroys it.
//
// This is not a simulation of the race. The destination genuinely appears
// between the decision and the publication; the only thing arranged is the
// timing, so the test is deterministic rather than flaky.
func TestCreateLosesTheRaceRatherThanTheUsersFile(t *testing.T) {
	dir := t.TempDir()
	source := seamSource(t, dir, "draft.md", "the input\n", 0o644)
	destination := filepath.Join(dir, "revised.md")
	const theirs = "what the other writer put there\n"

	// The destination appears immediately before whichever primitive the
	// implementation reaches for. Both hooks are installed in ONE run rather
	// than one run each: a correct implementation never calls Rename, so a
	// Rename-only schedule would leave the destination absent, Link would
	// succeed, and the test would fail the correct implementation. That is a
	// false failure, which is worse than a missing one.
	appear := func(t *testing.T, _ []string) {
		if _, err := os.Stat(destination); err == nil {
			return
		}
		if err := os.WriteFile(destination, []byte(theirs), 0o644); err != nil {
			t.Fatalf("arranging the race: %v", err)
		}
	}
	r := newRecorder(t)
	r.interpose["Link"] = appear
	r.interpose["Rename"] = appear

	err := create(r.seam(), source, destination, []byte("ours\n"))

	body, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatalf("the destination is gone: %v", readErr)
	}
	if string(body) != theirs {
		t.Errorf("the destination is now %q; the other writer's file was destroyed. Operations: %v",
			body, r.ops())
	}
	if err == nil {
		t.Error("Create reported success while another writer held the destination")
	} else if !errors.Is(err, ErrExists) {
		t.Errorf("error = %v, want ErrExists", err)
	}
	// And the fixture really did interpose, or this proves nothing.
	if r.count("Link") == 0 && r.count("Rename") == 0 {
		t.Error("neither publication primitive was called, so no race was scheduled")
	}
}

// Two Creates racing for one destination: exactly one wins, the other is told
// so, and neither leaves litter. This is the same property as above without the
// scheduling, and it exercises the real kernel rather than a stub.
func TestTwoCreatesRacingLeaveOneWinnerAndNoLitter(t *testing.T) {
	dir := t.TempDir()
	source := seamSource(t, dir, "draft.md", "the input\n", 0o644)
	destination := filepath.Join(dir, "out.md")

	const racers = 8
	errs := make([]error, racers)
	var ready, done sync.WaitGroup
	ready.Add(racers)
	done.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			errs[i] = Create(source, destination, []byte("ours\n"))
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()

	won, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrExists):
			refused++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of %d publications succeeded, want exactly one", won, racers)
	}
	if refused != racers-1 {
		t.Errorf("%d were refused, want %d", refused, racers-1)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %v; the losers left staging files behind", names)
	}
}

// And the mechanism itself: Create never renames. Rename is Replace's
// authority, and a Create that reaches for it has the power to overwrite
// whether or not it currently does.
func TestCreateNeverRenames(t *testing.T) {
	dir := t.TempDir()
	source := seamSource(t, dir, "draft.md", "the input\n", 0o644)
	r := newRecorder(t)

	if err := create(r.seam(), source, filepath.Join(dir, "out.md"), []byte("ours\n")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if r.count("Rename") != 0 {
		t.Errorf("Create renamed; the operations were %v", r.ops())
	}
	if r.count("Link") != 1 {
		t.Errorf("Create called Link %d times, want once", r.count("Link"))
	}
	linked := r.argsOf(t, "Link")
	if len(linked) != 2 || linked[1] != filepath.Join(resolvedDir(t, dir), "out.md") {
		t.Errorf("Link was called with %v; the second argument must be the destination", linked)
	}
	if filepath.Dir(linked[0]) != resolvedDir(t, dir) {
		t.Errorf("staging is %q, which is not in the destination's directory; Link would cross devices", linked[0])
	}
}

// A failed link is ErrExists and is NOT retried by another route. An
// implementation that fell back to Rename would turn a refusal into the
// destruction it exists to prevent.
func TestAFailedLinkIsNotRetriedByAnotherRoute(t *testing.T) {
	dir := t.TempDir()
	source := seamSource(t, dir, "draft.md", "the input\n", 0o644)
	r := newRecorder(t)
	r.fail["Link"] = &os.LinkError{Op: "link", Err: fs.ErrExist}

	err := create(r.seam(), source, filepath.Join(dir, "out.md"), []byte("ours\n"))
	if !errors.Is(err, ErrExists) {
		t.Errorf("error = %v, want ErrExists", err)
	}
	if r.count("Rename") != 0 {
		t.Errorf("a failed Link was followed by a Rename; the operations were %v", r.ops())
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out.md")); statErr == nil {
		t.Error("the destination exists after a failed publication")
	}
}

// ---------------------------------------------------------------------------
// Ordering, which is what the durability claim rests on
// ---------------------------------------------------------------------------

// The bytes are flushed before they are published, and the directory entry is
// flushed after. Either order reversed and a crash can leave a destination that
// exists and is empty, or one that does not exist at all despite a reported
// success.
func TestTheContentIsSyncedBeforePublicationAndTheDirectoryAfter(t *testing.T) {
	for _, name := range []string{"create", "replace"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := seamSource(t, dir, "draft.md", "the input\n", 0o640)
			r := newRecorder(t)

			var err error
			publication, destinationDir := "Link", resolvedDir(t, dir)
			if name == "create" {
				err = create(r.seam(), source, filepath.Join(dir, "out.md"), []byte("ours\n"))
			} else {
				publication = "Rename"
				err = replace(r.seam(), source, []byte("ours\n"))
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			// Chmod is deliberately NOT in this sequence: an implementation may
			// skip it when staging already carries the source's mode, which is
			// the 0600 case. That the mode is right when the file is published
			// is asserted by TestTheStagedFileCarriesTheSourcesModeWhenItIsPublished,
			// which checks the state rather than the call.
			requireOrder(t, r.ops(), "CreateTemp", "Write", "Sync", "Close", publication, "OpenDir", "Sync")
			if r.count(publication) != 1 {
				t.Errorf("%s called %s %d times, want once", name, publication, r.count(publication))
			}
			staging := r.argsOf(t, publication)[0]

			// The sync BEFORE publication is the staged file's, and the sync
			// AFTER it is the directory's. Checking only that two syncs happened
			// in some order around the publication is satisfied by an
			// implementation that syncs the staging file twice and never flushes
			// the directory entry at all — so a crash loses the publication
			// while the call order looks right.
			var before, after []string
			seenPublication := false
			for _, s := range r.steps {
				if s.Op == publication {
					seenPublication = true
					continue
				}
				if s.Op != "Sync" {
					continue
				}
				if seenPublication {
					after = append(after, s.Args[0])
				} else {
					before = append(before, s.Args[0])
				}
			}
			// At least one of each, of the right thing. Exactly one would reject
			// an implementation that syncs more than it must, which is wasteful
			// rather than wrong. What is not acceptable is syncing the staged
			// file twice and calling the second one the directory: that leaves
			// the directory entry unflushed while the call order looks right.
			if !contains(before, staging) {
				t.Errorf("the syncs before publication were %v, and none was the staged file %q", before, staging)
			}
			if !contains(after, destinationDir) {
				t.Errorf("the syncs after publication were %v, and none was the directory %q", after, destinationDir)
			}
			if got := r.argsOf(t, "OpenDir")[0]; got != destinationDir {
				t.Errorf("OpenDir was called on %q, want the publication directory %q", got, destinationDir)
			}
			// The directory handle is closed. An implementation that opened and
			// synced it but never closed it leaks a descriptor on every
			// publication, which a long-running caller pays for and no
			// assertion about ordering would notice.
			closedDirectory := false
			for _, step := range r.steps[indexOfOp(r.steps, publication)+1:] {
				if step.Op == "Close" && step.Args[0] == destinationDir {
					closedDirectory = true
				}
			}
			if !closedDirectory {
				t.Errorf("the directory handle opened on %q was never closed; the operations were %v",
					destinationDir, r.ops())
			}
		})
	}
}

// The staged file already carries the source's permissions AT THE MOMENT it is
// published, and the published file is never chmod-ed afterwards.
//
// This asserts the staged file's actual mode rather than counting Chmod calls.
// Counting would reject a correct implementation that skips a redundant chmod
// when staging is already at the source's mode — which is exactly the 0600 case
// — and it would accept an implementation that chmod-ed the wrong file. The
// state at publication is the thing that matters; how it got there is not.
func TestTheStagedFileCarriesTheSourcesModeWhenItIsPublished(t *testing.T) {
	for _, name := range []string{"create", "replace"} {
		for _, mode := range []os.FileMode{0o600, 0o640, 0o644} {
			t.Run(name+"/"+mode.String(), func(t *testing.T) {
				dir := t.TempDir()
				source := seamSource(t, dir, "draft.md", "the input\n", mode)
				r := newRecorder(t)

				publication := "Link"
				if name == "replace" {
					publication = "Rename"
				}
				var atPublication os.FileMode
				var checked bool
				r.interpose[publication] = func(t *testing.T, args []string) {
					info, err := os.Stat(args[0])
					if err != nil {
						t.Fatalf("stat the staged file: %v", err)
					}
					atPublication, checked = info.Mode().Perm(), true
				}

				var err error
				if name == "create" {
					err = create(r.seam(), source, filepath.Join(dir, "out.md"), []byte("ours\n"))
				} else {
					err = replace(r.seam(), source, []byte("ours\n"))
				}
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if !checked {
					t.Fatalf("%s was never called, so the staged file's mode was never observed", publication)
				}
				if atPublication != mode.Perm() {
					t.Errorf("the staged file was %v when it was published, want the source's %v",
						atPublication, mode.Perm())
				}
				// And nothing corrects it afterwards, which would mean the
				// published file was briefly at the staging mode.
				for _, step := range r.steps[indexOfOp(r.steps, publication)+1:] {
					if step.Op == "Chmod" {
						t.Error("the mode was set after publication, so the published file was " +
							"briefly readable to the wrong people")
					}
				}
			})
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}

func indexOfOp(steps []step, op string) int {
	for i, s := range steps {
		if s.Op == op {
			return i
		}
	}
	return len(steps) - 1
}

// Staging is created in the directory it will be published into, in both modes.
// Anywhere else and Link fails across devices, and Rename stops being atomic.
func TestStagingLivesInThePublicationDirectory(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := seamSource(t, dir, "draft.md", "the input\n", 0o644)

	t.Run("create stages beside the destination, not beside the source", func(t *testing.T) {
		r := newRecorder(t)
		if err := create(r.seam(), source, filepath.Join(elsewhere, "out.md"), []byte("ours\n")); err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := r.argsOf(t, "CreateTemp")[0]; got != resolvedDir(t, elsewhere) {
			t.Errorf("staged in %q, want the destination's directory %q", got, resolvedDir(t, elsewhere))
		}
	})

	t.Run("replace stages beside the resolved target", func(t *testing.T) {
		target := seamSource(t, elsewhere, "target.md", "the input\n", 0o644)
		link := filepath.Join(dir, "link.md")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		r := newRecorder(t)
		if err := replace(r.seam(), link, []byte("ours\n")); err != nil {
			t.Fatalf("replace: %v", err)
		}
		if got := r.argsOf(t, "CreateTemp")[0]; got != resolvedDir(t, elsewhere) {
			t.Errorf("staged in %q, want the resolved target's directory %q", got, resolvedDir(t, elsewhere))
		}
		wantTarget := filepath.Join(resolvedDir(t, elsewhere), filepath.Base(target))
		if got := r.argsOf(t, "Rename")[1]; got != wantTarget {
			t.Errorf("renamed onto %q, want the resolved target %q; renaming onto the link "+
				"would replace the link with a regular file", got, wantTarget)
		}
	})
}

// ---------------------------------------------------------------------------
// Failures, and what is left behind
// ---------------------------------------------------------------------------

// Every failure BEFORE publication leaves no destination and no staging file.
// A staging file left in a user's directory is litter they cannot explain, and
// one left after a failure is litter that looks like output.
func TestAFailureBeforePublicationLeavesNothingBehind(t *testing.T) {
	const input = "the input\n"
	for _, name := range []string{"create", "replace"} {
		for _, op := range []string{"CreateTemp", "Write", "Chmod", "Sync", "Close"} {
			t.Run(name+" when "+op+" fails", func(t *testing.T) {
				dir := t.TempDir()
				source := seamSource(t, dir, "draft.md", input, 0o640)
				destination := filepath.Join(dir, "out.md")
				r := newRecorder(t)
				r.fail[op] = errors.New(op + " refused")

				var err error
				if name == "create" {
					err = create(r.seam(), source, destination, []byte("ours\n"))
				} else {
					err = replace(r.seam(), source, []byte("ours\n"))
				}
				if err == nil {
					t.Fatalf("%s succeeded with %s failing", name, op)
				}
				if _, statErr := os.Stat(destination); statErr == nil {
					t.Error("a destination exists after a failed publication")
				}
				// The source is untouched, whichever authority was being
				// exercised. Replace is the one that could damage it.
				body, readErr := os.ReadFile(source)
				if readErr != nil {
					t.Fatalf("the source is gone: %v", readErr)
				}
				if string(body) != input {
					t.Errorf("the source is now %q; a failed publication altered the input", body)
				}
				entries, readErr := os.ReadDir(dir)
				if readErr != nil {
					t.Fatalf("read dir: %v", readErr)
				}
				for _, entry := range entries {
					if entry.Name() != "draft.md" {
						t.Errorf("%s was left behind after %s failed", entry.Name(), op)
					}
				}
			})
		}
	}
}

// A write that reports fewer bytes than it was given is a failure, not a
// success with a truncated document. os.File.Write returns an error with a
// short count, but an implementation that ignored n and checked only err would
// publish a partial draft over the user's work.
func TestAShortWriteIsAFailureAndPublishesNothing(t *testing.T) {
	const input = "the input\n"
	for _, name := range []string{"create", "replace"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := seamSource(t, dir, "draft.md", input, 0o644)
			destination := filepath.Join(dir, "out.md")
			r := newRecorder(t)
			short := r.seam()
			inner := short.Write
			short.Write = func(f *os.File, b []byte) (int, error) {
				if len(b) < 2 {
					return inner(f, b)
				}
				// Half the bytes, and no error: the shape an implementation that
				// only checks err will accept.
				n, _ := inner(f, b[:len(b)/2])
				return n, nil
			}

			var err error
			if name == "create" {
				err = create(short, source, destination, []byte("a document long enough to truncate\n"))
			} else {
				err = replace(short, source, []byte("a document long enough to truncate\n"))
			}
			if err == nil {
				t.Fatal("a short write was published as a complete document")
			}
			if _, statErr := os.Stat(destination); statErr == nil {
				t.Error("a destination exists after a short write")
			}
			if body, _ := os.ReadFile(source); string(body) != input {
				t.Errorf("the source is now %q after a short write", body)
			}
		})
	}
}

// A failure AFTER publication is different in kind, and the distinction is the
// caller's whole problem: the destination now exists and is correct, but its
// durability is uncertain. Returning nil would claim a guarantee that was not
// obtained; removing the destination would destroy a published file to tidy up.
func TestAFailureAfterPublicationReportsItAndKeepsTheDestination(t *testing.T) {
	// Remove is Create's only: Rename consumes the staging name, so a Replace
	// that has published has nothing left to tidy. Injecting it there would be
	// a scenario that cannot happen, and — worse — it would pass an
	// implementation that removed some other path entirely.
	// Sync and Close are shared with the staging file, so both are failed only
	// AFTER the publication primitive: the staged-file forms of each happen
	// before it and are separate cases, covered above.
	failures := map[string][]string{
		"create":  {"OpenDir", "Sync", "Close", "Remove"},
		"replace": {"OpenDir", "Sync", "Close"},
	}
	for _, name := range []string{"create", "replace"} {
		for _, op := range failures[name] {
			t.Run(name+" when "+op+" fails", func(t *testing.T) {
				runPostPublicationFailure(t, name, op, false)
			})
			// And again with cleanup ALSO failing. Without this a deferred
			// `return d.Remove(staging)` after a failed directory sync passes:
			// the test sees a non-nil error and never notices it is the wrong
			// one, so the durability failure the caller needed to hear about is
			// replaced by a tidying complaint.
			t.Run(name+" when "+op+" fails and cleanup fails too", func(t *testing.T) {
				runPostPublicationFailure(t, name, op, true)
			})
		}
	}
}

// Nothing is ever removed but the staging file this call created.
//
// Every Remove the implementation makes is checked against the staging path,
// across both authorities and across success and failure. Without this, an
// implementation could call Remove on any path it liked — a fixed cleanup name
// beside the source, say — and pass every other test here while deleting a
// user's file that happened to be called that.
func runPostPublicationFailure(t *testing.T, name, op string, cleanupFailsToo bool) {
	t.Helper()
	dir := t.TempDir()
	source := seamSource(t, dir, "draft.md", "the input\n", 0o644)
	destination := filepath.Join(dir, "out.md")
	publication := "Link"
	if name == "replace" {
		publication, destination = "Rename", source
	}
	r := newRecorder(t)
	primary := errors.New("the post-publication failure")
	if cleanupFailsToo && op != "Remove" {
		r.fail["Remove"] = errors.New("cannot tidy up")
	}
	if op == "Sync" || op == "Close" {
		// The DIRECTORY's sync and close, which happen after publication. The
		// staged file's happen before it and are separate cases, covered above,
		// so these fail the primitive only once publication has happened.
		r.failAfter[op] = struct {
			after string
			err   error
		}{after: publication, err: primary}
	} else {
		r.fail[op] = primary
	}

	var err error
	if name == "create" {
		err = create(r.seam(), source, destination, []byte("ours\n"))
	} else {
		err = replace(r.seam(), source, []byte("ours\n"))
	}
	if err == nil {
		t.Fatalf("%s reported success though %s failed after publication", name, op)
	}
	// The error the caller gets is the one that matters, not whatever went
	// wrong tidying up afterwards.
	if !errors.Is(err, primary) {
		t.Errorf("error = %v, want the %s failure; a cleanup error displaced it", err, op)
	}
	if !cleanupFailsToo && name == "create" && op != "Remove" {
		// Cleanup could run and did, so nothing staged may remain: a post-
		// publication failure is not a licence to leave the staged hard link
		// beside the destination.
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatalf("read dir: %v", readErr)
		}
		for _, entry := range entries {
			if entry.Name() != "draft.md" && entry.Name() != "out.md" {
				t.Errorf("%s was left behind after %s failed post-publication", entry.Name(), op)
			}
		}
	}
	body, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatalf("the destination was removed to tidy up after %s: %v", op, readErr)
	}
	if string(body) != "ours\n" {
		t.Errorf("the destination holds %q, want the published content", body)
	}
	if errors.Is(err, ErrExists) || errors.Is(err, ErrAliasesInput) {
		t.Errorf("a post-publication failure was reported as a refusal: %v", err)
	}
	if r.count(publication) != 1 {
		t.Errorf("%s published %d times", name, r.count(publication))
	}
}

// A cleanup that itself fails must not lose the real failure. The staging file
// is a consequence; the caller needs to hear about the thing that went wrong.
//
// Across every failure point, not only Write: an implementation that preserved
// the write error and let a cleanup failure displace a chmod, sync, close or
// link failure would satisfy a single-case test while making five other
// problems unreportable.
func TestACleanupFailureNeverDisplacesTheRealOne(t *testing.T) {
	for _, name := range []string{"create", "replace"} {
		for _, op := range cleanupFailurePoints[name] {
			if op == "Remove" {
				continue // the cleanup itself; there is no other failure to preserve
			}
			t.Run(name+"/"+op, func(t *testing.T) {
				dir := t.TempDir()
				source := seamSource(t, dir, "draft.md", "the input\n", 0o644)
				r := newRecorder(t)
				wanted := errors.New("the underlying failure")
				r.fail[op] = wanted
				r.fail["Remove"] = errors.New("cannot remove")

				var err error
				if name == "create" {
					err = create(r.seam(), source, filepath.Join(dir, "out.md"), []byte("ours\n"))
				} else {
					err = replace(r.seam(), source, []byte("ours\n"))
				}
				if err == nil {
					t.Fatalf("a failed %s was reported as success", op)
				}
				if !errors.Is(err, wanted) {
					t.Errorf("error = %v; the cleanup failure displaced the %s failure", err, op)
				}
			})
		}
	}
}

// The confidentiality contract, and it is narrower than the one first written
// here.
//
// A first draft said "litter is possible, disclosure is not", asserting every
// leftover is 0600 or tighter. That is false: staging is chmod-ed to the
// SOURCE's mode before publication, so a failure after that point strands a file
// at whatever the draft itself is — 0644 for an ordinary draft.
//
// A second attempt said a stranded file "never grants access the source does not
// already grant". Also too strong: effective access depends on the traversability
// of the containing directory and on ACLs, and this measures neither.
//
// So the guarantee is exactly what is measured: no stranded file carries
// permission bits more permissive than the source's. That is what stops a
// private draft's revision being left world-readable, and it is all this can
// honestly claim.
func TestNoStrandedFileIsMorePermissiveThanTheDraft(t *testing.T) {
	for _, name := range []string{"create", "replace"} {
		for _, op := range cleanupFailurePoints[name] {
			for _, mode := range []os.FileMode{0o600, 0o644} {
				t.Run(name+"/"+op+"/"+mode.String(), func(t *testing.T) {
					home := t.TempDir()
					away := filepath.Join(home, "elsewhere")
					if err := os.Mkdir(away, 0o755); err != nil {
						t.Fatalf("mkdir: %v", err)
					}
					source := seamSource(t, home, "draft.md", "the input\n", mode)
					r := newRecorder(t)
					r.fail[op] = errors.New(op + " refused")
					if op != "Remove" {
						r.fail["Remove"] = errors.New("cannot tidy up")
					}

					if name == "create" {
						_ = create(r.seam(), source, filepath.Join(away, "out.md"), []byte("ours\n"))
					} else {
						_ = replace(r.seam(), source, []byte("ours\n"))
					}

					for _, dir := range []string{home, away} {
						entries, err := os.ReadDir(dir)
						if err != nil {
							t.Fatalf("read %s: %v", dir, err)
						}
						for _, entry := range entries {
							if entry.Name() == "draft.md" || entry.Name() == "out.md" || entry.IsDir() {
								continue
							}
							info, statErr := entry.Info()
							if statErr != nil {
								t.Fatalf("stat: %v", statErr)
							}
							if info.Mode().Perm()&^mode.Perm() != 0 {
								t.Errorf("%s was stranded at %v, which is more permissive than the "+
									"draft's own %v", entry.Name(), info.Mode().Perm(), mode.Perm())
							}
						}
					}
				})
			}
		}
	}
}

// cleanupFailurePoints is every primitive whose failure can strand a staging
// file, per authority. Replace never links and Create never renames, so those
// would be scenarios that cannot happen dressed as coverage.
var cleanupFailurePoints = map[string][]string{
	"create":  {"Write", "Chmod", "Sync", "Close", "Link", "Remove"},
	"replace": {"Write", "Chmod", "Sync", "Close", "Rename"},
}

func TestTheOnlyThingEverRemovedIsItsOwnStagingFile(t *testing.T) {
	for _, name := range []string{"create", "replace"} {
		for _, breaks := range append([]string{""}, cleanupFailurePoints[name]...) {
			label := name + "/succeeds"
			if breaks != "" {
				label = name + "/" + breaks + " fails"
			}
			t.Run(label, func(t *testing.T) {
				// Source and destination in DIFFERENT directories, so cleanup
				// beside the source is distinguishable from cleanup beside the
				// destination. When they share a directory, the two are the same
				// place and the test cannot tell them apart.
				home := t.TempDir()
				away := filepath.Join(home, "elsewhere")
				if err := os.Mkdir(away, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				source := seamSource(t, home, "draft.md", "the input\n", 0o644)
				// A bystander in each directory, named the way a cleanup routine
				// would plausibly name its own file.
				bystanders := map[string]string{
					filepath.Join(home, ".hapax-cleanup"): "not ours, at home\n",
					filepath.Join(away, ".hapax-cleanup"): "not ours, away\n",
				}
				for path, body := range bystanders {
					if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
						t.Fatalf("plant %s: %v", path, err)
					}
				}

				r := newRecorder(t)
				if breaks != "" {
					r.fail[breaks] = errors.New(breaks + " refused")
				}
				if name == "create" {
					_ = create(r.seam(), source, filepath.Join(away, "out.md"), []byte("ours\n"))
				} else {
					_ = replace(r.seam(), source, []byte("ours\n"))
				}

				// Every Remove must name a path CreateTemp actually returned.
				// Comparing against the staging DIRECTORY would accept any name
				// in it, which is exactly the hole this closes.
				for _, step := range r.steps {
					if step.Op != "Remove" {
						continue
					}
					if !contains(r.staged, step.Args[0]) {
						t.Errorf("Remove was called on %q; the only staging files this call created "+
							"were %v", step.Args[0], r.staged)
					}
				}
				for path, body := range bystanders {
					got, err := os.ReadFile(path)
					if err != nil {
						t.Errorf("%s was deleted by a publication that did not create it: %v", path, err)
						continue
					}
					if string(got) != body {
						t.Errorf("%s is now %q", path, got)
					}
				}
			})
		}
	}
}

// Replace does not link. Overwriting is its declared authority, and reaching
// for the no-clobber primitive there would mean it could refuse a replacement
// the caller explicitly asked for.
func TestReplaceDoesNotReachForTheNoClobberPrimitive(t *testing.T) {
	dir := t.TempDir()
	source := seamSource(t, dir, "draft.md", "the input\n", 0o644)
	r := newRecorder(t)

	if err := replace(r.seam(), source, []byte("ours\n")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if r.count("Link") != 0 {
		t.Errorf("Replace linked; the operations were %v", r.ops())
	}
	if r.count("Rename") != 1 {
		t.Errorf("Replace renamed %d times, want once", r.count("Rename"))
	}
}

// ---------------------------------------------------------------------------
// The seam is the only door
// ---------------------------------------------------------------------------

// Every mechanism test in this file drives the unexported core. That leaves one
// way for a correct core to be irrelevant: an exported Create or Replace that
// does the work itself — a shortcut that skips the directory sync, or a
// check-then-rename — while the core sits beside it passing every test.
//
// So the primitives are made unnameable anywhere but the one function whose job
// is to name them. This is the same reasoning as B2a's credential boundary: a
// test says an implementation does not take a shortcut, a structural guard says
// there is no shortcut to take.
//
// Two things this guard got wrong on its first attempt, both found in review and
// both worth naming because they are how a structural guard usually fails:
//
//   - It walked function bodies only, so a package-level `var remove = os.Remove`
//     was invisible and `create` could then call `remove(staging)`.
//   - It matched the literal text "os.Link", so `import fsys "os"` followed by
//     `fsys.Link` walked straight past it.
//
// It now walks every declaration in the file except realDeps's own body, and it
// resolves each file's imports to their local names rather than assuming what
// they are called.
func TestTheFilesystemPrimitivesAreNamedOnlyInRealDeps(t *testing.T) {
	// Keyed by import path, then symbol: the operations that can create, destroy
	// or publish. Anything here, named outside realDeps, is a route around the
	// seam.
	forbidden := map[string]map[string]bool{
		"os": {
			"Link": true, "Rename": true, "Remove": true, "RemoveAll": true,
			"CreateTemp": true, "Create": true, "OpenFile": true, "WriteFile": true,
			"Chmod": true, "Chown": true, "Truncate": true, "Symlink": true,
			"Open": true, "Stat": true, "Lstat": true, "MkdirTemp": true,
		},
		"path/filepath": {"EvalSymlinks": true},
		"io/ioutil":     {"WriteFile": true, "TempFile": true},
	}

	set := token.NewFileSet()
	parsed, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	files := 0
	for _, pkg := range parsed {
		for _, file := range pkg.Files {
			files++

			// Resolve this file's imports to the identifiers they actually bind,
			// so an alias cannot slip past a literal name comparison.
			guarded := map[string]map[string]bool{}
			for _, imported := range file.Imports {
				path := strings.Trim(imported.Path.Value, `"`)
				symbols, watched := forbidden[path]
				if !watched {
					continue
				}
				local := path[strings.LastIndex(path, "/")+1:]
				if imported.Name != nil {
					local = imported.Name.Name
				}
				if local == "." {
					t.Errorf("%s is dot-imported, which makes its symbols unqualified and this "+
						"guard unable to see them", path)
					continue
				}
				guarded[local] = symbols
			}

			// Every declaration, not only functions: a package-level var or
			// const binding a primitive is outside every function body.
			for _, declaration := range file.Decls {
				if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "realDeps" {
					continue
				}
				where := "a package-level declaration"
				if function, ok := declaration.(*ast.FuncDecl); ok {
					where = function.Name.Name
				}
				ast.Inspect(declaration, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkgName, ok := selector.X.(*ast.Ident)
					if !ok {
						return true
					}
					if symbols, watched := guarded[pkgName.Name]; watched && symbols[selector.Sel.Name] {
						t.Errorf("%s names %s.%s at %s; every filesystem primitive must come "+
							"through the seam so no path can bypass it",
							where, pkgName.Name, selector.Sel.Name, set.Position(selector.Pos()))
					}
					return true
				})
			}
		}
	}
	if files == 0 {
		t.Fatal("no non-test source was parsed, so this guard read nothing")
	}
}

// realDeps is itself a door, and closing the primitives without closing it
// leaves the seam open.
//
// `create` could call `realDeps().Remove(staging)` and never name os.Remove at
// all. Several cleanup cases would still pass — the real staging file does get
// removed — while every injected failure was silently bypassed, which is the
// worst kind of pass: green, and testing nothing.
//
// The first version of this guard looked for CALLS, which a package-level
// `var defaults = realDeps` walks straight past: the identifier is referenced,
// not called, and `defaults()` inside create then reaches the real primitives.
// So every USE of the identifier is checked, and only two are allowed — its own
// declaration, and a direct call inside each exported wrapper.
func TestTheRealPrimitivesAreWiredOnlyByTheExportedFunctions(t *testing.T) {
	set := token.NewFileSet()
	parsed, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wired := map[string]int{}
	for _, pkg := range parsed {
		for _, file := range pkg.Files {
			for _, declaration := range file.Decls {
				where := "a package-level declaration"
				if function, ok := declaration.(*ast.FuncDecl); ok {
					if function.Name.Name == "realDeps" {
						continue // its own declaration
					}
					where = function.Name.Name
				}

				// Which identifiers are the callee of a call, so a bare
				// reference can be told from an invocation.
				invoked := map[*ast.Ident]bool{}
				ast.Inspect(declaration, func(node ast.Node) bool {
					if call, ok := node.(*ast.CallExpr); ok {
						if ident, ok := call.Fun.(*ast.Ident); ok {
							invoked[ident] = true
						}
					}
					return true
				})

				ast.Inspect(declaration, func(node ast.Node) bool {
					ident, ok := node.(*ast.Ident)
					if !ok || ident.Name != "realDeps" {
						return true
					}
					switch {
					case !invoked[ident]:
						t.Errorf("%s refers to realDeps without calling it at %s; a reference can "+
							"be stored and called later, which walks around the injected seam",
							where, set.Position(ident.Pos()))
					case where != "Create" && where != "Replace":
						t.Errorf("%s calls realDeps at %s; only the exported wrappers may wire "+
							"the real primitives, or the injected seam can be walked around",
							where, set.Position(ident.Pos()))
					default:
						wired[where]++
					}
					return true
				})
			}
		}
	}
	// Each wrapper, not merely one of them: an unwired export reaches nothing.
	for _, name := range []string{"Create", "Replace"} {
		if wired[name] != 1 {
			t.Errorf("%s calls realDeps %d times, want exactly once", name, wired[name])
		}
	}
}

// realDeps is exempt from the primitive guard — naming them is its whole job —
// and that exemption is itself a door if the function does anything else:
//
//	func realDeps() deps {
//	    _ = os.Remove(filepath.Join(os.TempDir(), "hapax-staging"))  // "tidy up"
//	    return deps{ ... }
//	}
//
// Every other guard permits that, and it deletes a path no test observes. A
// preflight or a stale-staging sweep placed in the constructor is an easy
// boundary mistake precisely because the constructor is the one place allowed to
// touch the filesystem vocabulary.
//
// So it is required to be a pure constructor: one statement, returning the
// struct. The primitives may appear inside that literal — as the field values
// they are, and inside the function values wrapping methods — but there is
// nowhere for a statement with an effect to live.
func TestRealDepsIsAPureConstructor(t *testing.T) {
	set := token.NewFileSet()
	parsed, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, pkg := range parsed {
		for _, file := range pkg.Files {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != "realDeps" || function.Recv != nil {
					continue
				}
				found = true
				if function.Body == nil || len(function.Body.List) != 1 {
					t.Errorf("realDeps has %d statements; it must be exactly `return deps{...}`, "+
						"or it can do filesystem work no test observes", len(function.Body.List))
					continue
				}
				returned, ok := function.Body.List[0].(*ast.ReturnStmt)
				if !ok || len(returned.Results) != 1 {
					t.Error("realDeps does not return a single expression")
					continue
				}
				literal, ok := returned.Results[0].(*ast.CompositeLit)
				if !ok {
					t.Error("realDeps does not return a struct literal")
					continue
				}
				if name, ok := literal.Type.(*ast.Ident); !ok || name.Name != "deps" {
					t.Errorf("realDeps returns a %v literal, want deps", literal.Type)
				}
			}
		}
	}
	if !found {
		t.Fatal("realDeps was not found in the package's non-test source")
	}
}

// And the exported functions are thin. Not "one statement containing a call to
// the core" — that accepts `if err := create(...); err != nil { ... }` followed
// by arbitrary work — but exactly `return create(...)`, so there is nowhere for
// a second implementation to live.
func TestTheExportedFunctionsAreExactlyAReturnOfTheirCore(t *testing.T) {
	set := token.NewFileSet()
	parsed, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wanted := map[string]string{"Create": "create", "Replace": "replace"}
	found := map[string]bool{}
	for _, pkg := range parsed {
		for _, file := range pkg.Files {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Recv != nil {
					continue
				}
				core, wants := wanted[function.Name.Name]
				if !wants {
					continue
				}
				found[function.Name.Name] = true
				if function.Body == nil || len(function.Body.List) != 1 {
					t.Errorf("%s has %d statements; it must be exactly `return %s(...)`",
						function.Name.Name, len(function.Body.List), core)
					continue
				}
				returned, ok := function.Body.List[0].(*ast.ReturnStmt)
				if !ok || len(returned.Results) != 1 {
					t.Errorf("%s does not return a single expression; it must be exactly "+
						"`return %s(...)`", function.Name.Name, core)
					continue
				}
				call, ok := returned.Results[0].(*ast.CallExpr)
				if !ok {
					t.Errorf("%s does not return a call", function.Name.Name)
					continue
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || ident.Name != core {
					t.Errorf("%s returns something other than a direct call to %s",
						function.Name.Name, core)
				}
			}
		}
	}
	for name := range wanted {
		if !found[name] {
			t.Errorf("%s was not found in the package's non-test source", name)
		}
	}
}
