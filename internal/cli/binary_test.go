package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The binary honours the same contract Run does. Every other test in this
// package calls Run directly, so a main that read a credential or wrote to the
// wrong stream before delegating would pass all of them.
//
// It does NOT prove that main DELEGATES: a duplicate implementation with the
// same behaviour would pass. Proving the call graph would need a seam inside
// main, which is a worse trade than testing what the binary actually does —
// the contract is the exit code and the stream, not the call.
func TestTheBinaryHonoursTheSameContract(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "hapax")
	build := exec.Command("go", "build", "-o", binary, "github.com/fissible/hapax/cmd/hapax")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/hapax: %v\n%s", err, out)
	}

	for _, c := range []struct {
		name   string
		body   string
		args   []string
		want   int
		status string
		asJSON bool
	}{
		{"a clean draft", clean, nil, 0, "ok", true},
		{"a draft with a finding", adverse, nil, 1, "adverse", true},
		// Without --json the binary must render for a person, not force the
		// machine form: a main that always emitted JSON would pass above.
		{"a clean draft, rendered for a person", clean, nil, 0, "ok", false},
		{"a finding, rendered for a person", adverse, nil, 1, "adverse", false},
		{"an unknown command", clean, []string{"publish"}, 2, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "draft.md")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			args := []string{"tells", path}
			if c.asJSON {
				args = []string{"tells", "--json", path}
			}
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
			switch {
			case c.want > 1:
				if stdout.Len() != 0 {
					t.Errorf("a failing invocation wrote to stdout: %q", stdout.String())
				}
				requireOneDiagnostic(t, stderr.String())
			case c.asJSON:
				// Literally the same helper Run's tests use, so the binary and
				// the function cannot be held to different standards.
				want := cleanResult(path)
				if c.status == "adverse" {
					want = adverseResult(path)
				}
				requireSuccessfulDocument(t, stdout.String(), stderr.String(), c.status, want)
			default:
				requireSuccessfulHumanOutput(t, stdout.String(), stderr.String(), c.status)
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
