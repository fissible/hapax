package cli_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/store"
)

// harness gives Run an environment where every dependency A1 must NOT reach
// fails the test if it is reached. Absence of a store, a provider and a
// credential is asserted, not assumed.
type harness struct {
	Stdout, Stderr strings.Builder
	deps           cli.Deps
}

func newHarness(t *testing.T, environment map[string]string) *harness {
	t.Helper()
	h := &harness{}
	h.deps = cli.Deps{
		Stdout: &h.Stdout,
		Stderr: &h.Stderr,
		Env: func(name string) (string, bool) {
			value, ok := environment[name]
			return value, ok
		},
		Now:      func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
		ReadFile: os.ReadFile,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("a dial was attempted; no A1 command may reach the network")
			return nil, nil
		},
		Credentials: func(context.Context) (string, error) {
			t.Fatal("a credential was read; no A1 command may construct a provider")
			return "", nil
		},
		OpenStore: func(string) (*store.Store, error) {
			t.Fatal("a store was opened; no A1 command needs one")
			return nil, nil
		},
	}
	return h
}

func (h *harness) run(args ...string) int {
	return cli.Run(context.Background(), args, h.deps)
}

func (h *harness) document(t *testing.T) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(h.Stdout.String()), &decoded); err != nil {
		t.Fatalf("stdout is not one JSON document: %q: %v", h.Stdout.String(), err)
	}
	return decoded
}

func draft(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "draft.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// The default rule set flags two spaces between words, so this is a draft with
// exactly one finding and one without any.
const (
	clean   = "A draft with single spaces throughout it.\n"
	adverse = "A draft with two  spaces in it.\n"
)

// ---------------------------------------------------------------------------
// Exit codes
// ---------------------------------------------------------------------------

func TestTellsExitsZeroWhenNothingIsFlagged(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("tells", draft(t, clean)); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr %q", code, h.Stderr.String())
	}
	if got := h.document(t)["status"]; got != "ok" {
		t.Errorf("status = %v, want ok", got)
	}
}

func TestTellsExitsOneWhenSomethingIsFlagged(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("tells", draft(t, adverse)); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr %q", code, h.Stderr.String())
	}
	document := h.document(t)
	if got := document["status"]; got != "adverse" {
		t.Errorf("status = %v, want adverse", got)
	}
	if got := document["command"]; got != "tells" {
		t.Errorf("command = %v, want tells", got)
	}
}

// A finding is what makes it adverse. tells is a linter, and a linter that
// found nothing has nothing adverse to say.
func TestAdversityIsExactlyHavingAFinding(t *testing.T) {
	h := newHarness(t, nil)
	h.run("tells", draft(t, adverse))
	result, ok := h.document(t)["result"].(map[string]any)
	if !ok {
		t.Fatal("no result object")
	}
	if count, ok := result["count"].(float64); !ok || count < 1 {
		t.Errorf("count = %v, want at least one finding", result["count"])
	}
}

func TestInvalidInvocationsExitTwo(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"no command at all", nil},
		{"an unknown command", []string{"rewrite", "draft.md"}},
		{"an unknown flag", []string{"tells", "--verbose", "draft.md"}},
		{"a single-dash flag", []string{"tells", "-j", "draft.md"}},
		{"an equals form", []string{"tells", "--json=true", "draft.md"}},
		{"a false equals form", []string{"tells", "--json=false", "draft.md"}},
		{"no operand", []string{"tells"}},
		{"two operands", []string{"tells", "a.md", "b.md"}},
		{"a stdin dash", []string{"tells", "-"}},
		{"nothing after the delimiter", []string{"tells", "--"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if code := h.run(c.args...); code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if h.Stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", h.Stdout.String())
			}
			if h.Stderr.Len() == 0 {
				t.Error("no diagnostic on stderr")
			}
		})
	}
}

func TestAnUnreadableDraftExitsThree(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("tells", filepath.Join(t.TempDir(), "absent.md")); code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	if h.Stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", h.Stdout.String())
	}
}

// The file was read; it simply is not a document. Not an invalid invocation,
// because the invocation was well formed, and not a refusal, because no reason
// in the closed set covers it.
func TestADraftThatWillNotAdmitExitsThree(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("tells", draft(t, "Prose interrupted by \xff\xfe invalid bytes.\n")); code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	if !strings.Contains(h.Stderr.String(), "21") {
		t.Errorf("the diagnostic does not name the byte offset: %q", h.Stderr.String())
	}
}

// Codes 2 and 3 carry no document, --json or not, because inventing a status
// for a failure would blur the correspondence the whole scheme rests on.
func TestFailuresWriteNoDocumentEvenAskedForJSON(t *testing.T) {
	for _, args := range [][]string{
		{"tells", "--json", "--nonsense"},
		{"tells", "--json", filepath.Join(t.TempDir(), "absent.md")},
	} {
		h := newHarness(t, nil)
		h.run(args...)
		if h.Stdout.Len() != 0 {
			t.Errorf("%v wrote %q to stdout", args, h.Stdout.String())
		}
	}
}

// ---------------------------------------------------------------------------
// The grammar
// ---------------------------------------------------------------------------

// Flags are position-independent before the delimiter: every spelling below is
// the same invocation.
func TestFlagsMayAppearAnywhereBeforeTheDelimiter(t *testing.T) {
	path := draft(t, clean)
	var first string
	for _, args := range [][]string{
		{"--json", "tells", path},
		{"tells", "--json", path},
		{"tells", path, "--json"},
		{"--json", "--local-only", "tells", path},
		{"tells", "--json", "--json", path},
	} {
		h := newHarness(t, nil)
		if code := h.run(args...); code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, h.Stderr.String())
		}
		if first == "" {
			first = h.Stdout.String()
			continue
		}
		if h.Stdout.String() != first {
			t.Errorf("%v produced\n%s\nwant\n%s", args, h.Stdout.String(), first)
		}
	}
}

// The delimiter stops FLAG scanning, not command scanning, and everything after
// it is an operand however it is spelled.
func TestTheDelimiterEndsFlagsAndNothingElse(t *testing.T) {
	t.Run("the command may follow the delimiter", func(t *testing.T) {
		h := newHarness(t, nil)
		if code := h.run("--", "tells", draft(t, clean)); code != 0 {
			t.Errorf("exit = %d, want 0; stderr %q", code, h.Stderr.String())
		}
	})
	for _, operand := range []string{"--json", "-j", "--local-only"} {
		t.Run("a file named "+operand, func(t *testing.T) {
			// It is one operand, and it does not exist, so this reaches exit 3
			// rather than being parsed as a flag and reaching exit 2.
			h := newHarness(t, nil)
			if code := h.run("tells", "--", operand); code != 3 {
				t.Errorf("exit = %d, want 3; %q was treated as a flag", code, operand)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Mode, and what must not be reached
// ---------------------------------------------------------------------------

func TestAMalformedEnvironmentExitsTwo(t *testing.T) {
	for _, value := range []string{"", "true", "yes", "2"} {
		t.Run("value:"+value, func(t *testing.T) {
			h := newHarness(t, map[string]string{"HAPAX_LOCAL_ONLY": value})
			if code := h.run("tells", draft(t, clean)); code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
		})
	}
}

func TestAMalformedEnvironmentExitsTwoEvenWithTheFlag(t *testing.T) {
	h := newHarness(t, map[string]string{"HAPAX_LOCAL_ONLY": "yes"})
	if code := h.run("tells", "--local-only", draft(t, clean)); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

// Mode is resolved before anything else can happen. With a malformed value and
// an otherwise perfectly good invocation, nothing is read and nothing is
// constructed — the seams in the harness fail the test if they are touched.
func TestModeIsResolvedBeforeAnySeamCanRun(t *testing.T) {
	h := newHarness(t, map[string]string{"HAPAX_LOCAL_ONLY": "maybe"})
	read := false
	h.deps.ReadFile = func(string) ([]byte, error) {
		read = true
		return nil, nil
	}
	if code := cli.Run(context.Background(), []string{"tells", draft(t, clean)}, h.deps); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if read {
		t.Error("the draft was read before the mode was resolved")
	}
}

// A1's commands need no store, no provider and no credential, and the harness
// proves it: those seams fail the test if called. This test exists so that the
// claim is named rather than only implied by the others passing.
func TestTheOfflineCommandTouchesNothingItDoesNotNeed(t *testing.T) {
	for _, args := range [][]string{
		{"tells", draft(t, clean)},
		{"tells", "--local-only", draft(t, adverse)},
		{"tells", "--json", draft(t, adverse)},
	} {
		h := newHarness(t, nil)
		h.run(args...)
	}
}

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

// One line, always. A filename is caller-controlled, so a name carrying a
// newline must not become two diagnostics, and a quote must not end the
// quoting early.
func TestADiagnosticIsOneLineWhateverTheFilenameContains(t *testing.T) {
	for _, name := range []string{"two\nlines.md", `quo"te.md`, "tab\there.md"} {
		t.Run(strings.ReplaceAll(name, "\n", "\\n"), func(t *testing.T) {
			h := newHarness(t, nil)
			h.run("tells", filepath.Join(t.TempDir(), name))
			diagnostic := h.Stderr.String()
			if strings.Count(strings.TrimSuffix(diagnostic, "\n"), "\n") != 0 {
				t.Errorf("the diagnostic spans lines: %q", diagnostic)
			}
			if strings.Contains(diagnostic, "\n"+name) || strings.Contains(diagnostic, "\t") {
				t.Errorf("a dynamic value was not escaped: %q", diagnostic)
			}
		})
	}
}

// The unknown-command diagnostic names what exists. Listing commands that are
// not built would tell a user they can run something they cannot.
func TestTheUnknownCommandDiagnosticPromisesNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.run("score", "draft.md")
	diagnostic := h.Stderr.String()
	if !strings.Contains(diagnostic, "tells") {
		t.Errorf("the diagnostic does not name the command that exists: %q", diagnostic)
	}
	for _, unimplemented := range []string{"index", "profile", "eval", "score", "rewrite"} {
		if strings.Contains(diagnostic, unimplemented) && unimplemented != "score" {
			t.Errorf("the diagnostic offers %q, which is not implemented: %q", unimplemented, diagnostic)
		}
	}
}
