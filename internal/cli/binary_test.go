package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The AST guard says cmd/hapax may not reach around the seams; it cannot say
// that main WIRES them, or that it returns what Run returned. A main that read
// a credential before calling Run would satisfy every other test in this
// package, because no other test invokes it.
func TestTheBinaryIsTheRunFunction(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "hapax")
	build := exec.Command("go", "build", "-o", binary, "github.com/fissible/hapax/cmd/hapax")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/hapax: %v\n%s", err, out)
	}

	for _, c := range []struct {
		name string
		body string
		args []string
		want int
	}{
		{"a clean draft", clean, nil, 0},
		{"a draft with a finding", adverse, nil, 1},
		{"an unknown command", clean, []string{"score"}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "draft.md")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			args := append(append([]string(nil), c.args...), "tells", "--json", path)
			if c.args != nil {
				args = c.args
			}
			run := exec.Command(binary, args...)
			run.Env = append(os.Environ(), "HAPAX_LOCAL_ONLY=1")
			var stdout, stderr strings.Builder
			run.Stdout, run.Stderr = &stdout, &stderr
			err := run.Run()

			code := 0
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatalf("running: %v", err)
			}
			if code != c.want {
				t.Errorf("exit = %d, want %d; stderr %q", code, c.want, stderr.String())
			}
			if c.want <= 1 && !strings.Contains(stdout.String(), `"schema":"hapax.v1"`) {
				t.Errorf("stdout carries no document: %q", stdout.String())
			}
			if c.want == 2 && stdout.Len() != 0 {
				t.Errorf("a failing invocation wrote to stdout: %q", stdout.String())
			}
		})
	}
}

// And the binary passes the real environment through: a malformed value reaches
// mode resolution rather than being swallowed by main.
func TestTheBinaryResolvesTheRealEnvironment(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "hapax")
	if out, err := exec.Command("go", "build", "-o", binary, "github.com/fissible/hapax/cmd/hapax").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte(clean), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run := exec.Command(binary, "tells", path)
	run.Env = append(os.Environ(), "HAPAX_LOCAL_ONLY=maybe")
	err := run.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("exit = 0, want 2: %v", err)
	}
	if exit.ExitCode() != 2 {
		t.Errorf("exit = %d, want 2", exit.ExitCode())
	}
}
