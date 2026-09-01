package publish_test

// The only code in hapax that can destroy a user's file.
//
// Everything upstream is careful so that this is the sole authority: `Execute`
// returns bytes it cannot write, and `assemble` returns nil rather than a
// partial document. This package is where that care is either honoured or lost.
//
// # Two authorities, and no way to hold the wrong one
//
// `Create` publishes where nothing is, and fails if something is. `Replace`
// overwrites, and is the only Rename here. They are two functions rather than
// one function and a mode, so no invalid combination is representable and the
// call site says which authority it is exercising — checked by the compiler
// rather than by a validator. B2a paid six review rounds for that lesson: a
// test says an implementation does not do the wrong thing, a signature says
// none can.
//
// # What these tests deliberately do NOT claim
//
// They are about behaviour a caller can observe: what is on disk afterwards,
// what error came back, and what was left behind. Final state cannot establish
// no-clobber or atomicity — a check-then-rename implementation satisfies
// "the destination was not overwritten" right up until it loses the race, and
// "the file is wholly old or wholly new afterwards" says nothing about being
// interrupted. Those live in seam_internal_test.go, which can watch the
// mechanism and schedule the race deterministically.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/publish"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	original = "The draft as its author left it.\n"
	revised  = "The draft after the rewrite, which is a different length.\n"
)

func sourceFile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	// WriteFile is subject to umask, so the mode is set explicitly afterwards.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return info.Mode()
}

// requireNoStrays fails if anything is left in the directory beyond the files
// named. A staging file that survives a failure is litter in the user's
// directory, and a staging file that survives a SUCCESS is litter they will
// never explain.
func requireNoStrays(t *testing.T, dir string, expected ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	wanted := map[string]bool{}
	for _, name := range expected {
		wanted[name] = true
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] {
			t.Errorf("%s was left behind in the destination directory", entry.Name())
		}
	}
	if len(entries) < len(expected) {
		t.Errorf("the directory holds %d entries and %d were expected", len(entries), len(expected))
	}
}

// ---------------------------------------------------------------------------
// Create: publishes where nothing is
// ---------------------------------------------------------------------------

func TestCreateWritesTheContentAndLeavesTheSourceAlone(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)
	destination := filepath.Join(dir, "revised.md")

	if err := publish.Create(source, destination, []byte(revised)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := read(t, destination); got != revised {
		t.Errorf("destination is %q, want %q", got, revised)
	}
	if got := read(t, source); got != original {
		t.Errorf("the source is %q; publishing must not touch it", got)
	}
	requireNoStrays(t, dir, "draft.md", "revised.md")
}

// A destination that already exists is refused, and the thing that was there is
// still there. This is the whole point of the Create authority.
func TestCreateRefusesAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)
	destination := sourceFile(t, dir, "revised.md", "something a user wrote earlier", 0o644)

	err := publish.Create(source, destination, []byte(revised))
	if !errors.Is(err, publish.ErrExists) {
		t.Fatalf("error = %v, want ErrExists", err)
	}
	if got := read(t, destination); got != "something a user wrote earlier" {
		t.Errorf("the destination is now %q; a refused publication overwrote it", got)
	}
	requireNoStrays(t, dir, "draft.md", "revised.md")
}

// Naming the input as the destination is refused by name rather than by
// accident. The SAFETY here comes from Create's link, which cannot overwrite an
// existing file and so cannot overwrite the input either; this check exists so
// the user is told what they did rather than being told their own draft is in
// the way.
func TestCreateRefusesADestinationThatIsTheInput(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)

	for _, name := range []string{
		"the same path",
		"an uncleaned path",
		"a relative spelling of it",
	} {
		t.Run(name, func(t *testing.T) {
			var destination string
			switch name {
			case "the same path":
				destination = source
			case "an uncleaned path":
				destination = filepath.Join(dir, "sub", "..", "draft.md")
			case "a relative spelling of it":
				destination = filepath.Join(dir, ".", "draft.md")
			}
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			err := publish.Create(source, destination, []byte(revised))
			if !errors.Is(err, publish.ErrAliasesInput) {
				t.Errorf("error = %v, want ErrAliasesInput", err)
			}
			if got := read(t, source); got != original {
				t.Errorf("the input is now %q", got)
			}
		})
	}
}

// A symlinked directory in the destination's path still resolves to the input.
// Comparing the spellings would miss it; resolving the destination's PARENT and
// joining the base is what catches it, and it has to be the parent because the
// destination itself need not exist.
func TestCreateRefusesTheInputReachedThroughASymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := sourceFile(t, real, "draft.md", original, 0o644)
	linked := filepath.Join(dir, "linked")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := publish.Create(source, filepath.Join(linked, "draft.md"), []byte(revised))
	if !errors.Is(err, publish.ErrAliasesInput) {
		t.Errorf("error = %v, want ErrAliasesInput", err)
	}
	if got := read(t, source); got != original {
		t.Errorf("the input is now %q", got)
	}
}

// And a hard link to the input is the input, which no amount of path comparison
// can see. os.SameFile is what catches it, and it needs the destination to
// exist — which it does, or there would be nothing to be a link to.
func TestCreateRefusesAHardLinkToTheInput(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)
	destination := filepath.Join(dir, "another-name.md")
	if err := os.Link(source, destination); err != nil {
		t.Skipf("this filesystem does not support hard links: %v", err)
	}

	// ErrAliasesInput specifically, not ErrExists. Both paths exist and
	// os.SameFile can tell they are one file, so reporting "the destination is
	// in the way" would send the user looking for a file to delete that is the
	// draft they are rewriting.
	err := publish.Create(source, destination, []byte(revised))
	if !errors.Is(err, publish.ErrAliasesInput) {
		t.Fatalf("error = %v, want ErrAliasesInput", err)
	}
	if got := read(t, source); got != original {
		t.Errorf("the input is now %q; a hard-linked destination is the input", got)
	}
}

// A destination that is a symlink is not published through: Create's authority
// is to make a file where none is, and a symlink is something. Both the link
// and whatever it points at come through untouched.
func TestCreateRefusesASymlinkDestinationAndLeavesItsTargetAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)
	target := sourceFile(t, dir, "target.md", "someone else's file\n", 0o644)
	destination := filepath.Join(dir, "link.md")
	if err := os.Symlink(target, destination); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := publish.Create(source, destination, []byte(revised)); err == nil {
		t.Fatal("Create published over a symlink")
	}
	if modeOf(t, destination)&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced")
	}
	if got := read(t, target); got != "someone else's file\n" {
		t.Errorf("the symlink's target is now %q; publishing followed the link", got)
	}
}

// Create preserves the source's mode too. The destination is a copy of the
// user's draft in every sense that matters, and a rewrite that silently made it
// world-readable would be a disclosure the user did not choose.
func TestCreatePreservesTheInputsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not modelled the same way on Windows")
	}
	for _, mode := range []os.FileMode{0o600, 0o644, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			source := sourceFile(t, dir, "draft.md", original, mode)
			if before := modeOf(t, source).Perm(); before != mode {
				t.Fatalf("the fixture wrote mode %v, not %v", before, mode)
			}
			destination := filepath.Join(dir, "revised.md")

			if err := publish.Create(source, destination, []byte(revised)); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got := modeOf(t, destination).Perm(); got != mode {
				t.Errorf("the destination is %v, want the source's %v", got, mode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Replace: the only overwrite authority
// ---------------------------------------------------------------------------

func TestReplaceOverwritesInPlace(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)

	if err := publish.Replace(source, []byte(revised)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := read(t, source); got != revised {
		t.Errorf("the file is %q, want %q", got, revised)
	}
	requireNoStrays(t, dir, "draft.md")
}

// Replacement must not change permissions as well as prose. A draft the author
// made private stays private.
func TestReplacePreservesTheInputsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not modelled the same way on Windows")
	}
	for _, mode := range []os.FileMode{0o600, 0o644, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			source := sourceFile(t, dir, "draft.md", original, mode)
			if before := modeOf(t, source).Perm(); before != mode {
				t.Fatalf("the fixture wrote mode %v, not %v, so this test asserts nothing", before, mode)
			}

			if err := publish.Replace(source, []byte(revised)); err != nil {
				t.Fatalf("Replace: %v", err)
			}
			if got := modeOf(t, source).Perm(); got != mode {
				t.Errorf("mode is %v after replacement, want %v", got, mode)
			}
		})
	}
}

// A symlinked input has its TARGET replaced and stays a symlink. Replacing the
// link itself would silently convert a link into a regular file, which is a
// change the user did not ask for and would not notice.
func TestReplaceFollowsASymlinkAndLeavesTheLinkALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	dir := t.TempDir()
	target := sourceFile(t, dir, "target.md", original, 0o644)
	link := filepath.Join(dir, "draft.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := publish.Replace(link, []byte(revised)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if modeOf(t, link)&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	if got := read(t, target); got != revised {
		t.Errorf("the target is %q, want the revision", got)
	}
	requireNoStrays(t, dir, "target.md", "draft.md")
}

// Replacement is by rename, so the published file is a NEW inode, and the old
// one survives wherever something else still refers to it.
//
// The claim is deliberately narrow: content and permission bits are what this
// package carries across, and hard links are what visibly do not. Whether
// extended attributes, ACLs or timestamps survive is platform and filesystem
// dependent, so this says nothing about them rather than asserting an
// absolute metadata erasure it has not measured.
//
// It is documented by test rather than by comment because it is the most
// surprising thing about a flag called `--in-place`, and because a comment
// cannot fail when someone changes it. A second hard link to the draft keeps
// the OLD content after a replacement — the two names have come apart.
func TestReplaceBreaksHardLinksBecauseItReplacesTheInode(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)
	other := filepath.Join(dir, "also-the-draft.md")
	if err := os.Link(source, other); err != nil {
		t.Skipf("this filesystem does not support hard links: %v", err)
	}

	if err := publish.Replace(source, []byte(revised)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := read(t, source); got != revised {
		t.Errorf("the source is %q, want the revision", got)
	}
	if got := read(t, other); got != original {
		t.Errorf("the other link holds %q; if it now holds the revision, replacement is "+
			"writing through the old inode rather than renaming onto the path, and this "+
			"package's atomicity claim is wrong", got)
	}
}

// ---------------------------------------------------------------------------
// What both refuse
// ---------------------------------------------------------------------------

func TestBothRefuseAnEmptyPath(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)

	if err := publish.Create("", filepath.Join(dir, "out.md"), []byte(revised)); err == nil {
		t.Error("Create accepted an empty source")
	}
	if err := publish.Create(source, "", []byte(revised)); err == nil {
		t.Error("Create accepted an empty destination")
	}
	if err := publish.Replace("", []byte(revised)); err == nil {
		t.Error("Replace accepted an empty source")
	}
	requireNoStrays(t, dir, "draft.md")
}

// The source must resolve to a regular file. Replacing a directory or a device
// is not something a rewrite can mean, and attempting it is worse than refusing.
func TestBothRefuseASourceThatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	directory := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := publish.Create(directory, filepath.Join(dir, "out.md"), []byte(revised)); err == nil {
		t.Error("Create accepted a directory as its source")
	}
	if err := publish.Replace(directory, []byte(revised)); err == nil {
		t.Error("Replace accepted a directory as its source")
	}
	if _, err := os.Stat(directory); err != nil {
		t.Errorf("the directory is gone: %v", err)
	}
}

// A missing source is an error rather than a publication. Both operations read
// the source's identity and mode, so neither can proceed without it.
func TestBothRefuseAMissingSource(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-here.md")

	if err := publish.Create(missing, filepath.Join(dir, "out.md"), []byte(revised)); err == nil {
		t.Error("Create accepted a source that does not exist")
	}
	if err := publish.Replace(missing, []byte(revised)); err == nil {
		t.Error("Replace accepted a source that does not exist")
	}
	requireNoStrays(t, dir)
}

// A destination whose directory does not exist fails, and fails without leaving
// anything: staging lives in the destination's directory, so there is nowhere
// to stage.
func TestCreateRefusesADestinationWithNoDirectory(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)

	err := publish.Create(source, filepath.Join(dir, "no-such-dir", "out.md"), []byte(revised))
	if err == nil {
		t.Fatal("Create accepted a destination in a directory that does not exist")
	}
	if errors.Is(err, publish.ErrExists) {
		t.Error("a missing directory was reported as an existing destination")
	}
	requireNoStrays(t, dir, "draft.md")
}

// A destination that is a directory is refused rather than published into.
func TestCreateRefusesADirectoryAsItsDestination(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)
	destination := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := publish.Create(source, destination, []byte(revised)); err == nil {
		t.Fatal("Create published over a directory")
	}
	info, err := os.Stat(destination)
	if err != nil || !info.IsDir() {
		t.Errorf("the directory is no longer a directory: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Content
// ---------------------------------------------------------------------------

// Empty content is a document with nothing in it, not an error and not a
// refusal. A rewrite cannot produce it today, but "publish these bytes" has an
// obvious meaning for zero bytes and inventing an error would be a surprise.
func TestEmptyContentPublishesAnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)

	if err := publish.Create(source, filepath.Join(dir, "empty.md"), nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := read(t, filepath.Join(dir, "empty.md")); got != "" {
		t.Errorf("the destination holds %q, want nothing", got)
	}
}

// Content is published byte for byte, including a byte-order mark and multibyte
// text. Byte ownership is the risk the whole rewrite path carries, and this is
// the last place bytes can be lost.
func TestContentIsPublishedByteForByte(t *testing.T) {
	body := "\xef\xbb\xbfA draft with a byte-order mark, café, naïve, and an em—dash.\n\nSecond paragraph.\n"
	dir := t.TempDir()
	source := sourceFile(t, dir, "draft.md", original, 0o644)

	destination := filepath.Join(dir, "out.md")
	if err := publish.Create(source, destination, []byte(body)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := read(t, destination); got != body {
		t.Errorf("published %q, want %q", got, body)
	}

	if err := publish.Replace(source, []byte(body)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := read(t, source); got != body {
		t.Errorf("replaced with %q, want %q", got, body)
	}
}

// ---------------------------------------------------------------------------
// The errors are a closed, distinguishable set
// ---------------------------------------------------------------------------

// A caller maps these to exit codes, so two different refusals must not be the
// same error, and neither must be indistinguishable from an operational
// failure.
func TestTheDeclaredErrorsAreDistinct(t *testing.T) {
	if errors.Is(publish.ErrExists, publish.ErrAliasesInput) || errors.Is(publish.ErrAliasesInput, publish.ErrExists) {
		t.Error("ErrExists and ErrAliasesInput are the same error")
	}
	for _, declared := range []error{publish.ErrExists, publish.ErrAliasesInput} {
		if declared == nil {
			t.Error("a declared error is nil")
		}
		if strings.TrimSpace(declared.Error()) == "" {
			t.Error("a declared error has no message")
		}
	}
}
