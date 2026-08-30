package cli_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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

// The document Run emits is the CONVERTED report, field for field. Without
// this, a skeletal {"status":"adverse","result":{"count":1}} satisfies every
// other integration test here while path, screening, spans and vocabularies
// are all broken.
func TestRunEmitsTheConvertedReport(t *testing.T) {
	path := draft(t, adverse)
	h := newHarness(t, nil)
	if code := h.run("tells", "--json", path); code != 1 {
		t.Fatalf("exit = %d, stderr %q", code, h.Stderr.String())
	}

	var got struct {
		Schema, Command, Status, Reason string
		Profile                         *string
		Result                          cli.TellsResult
	}
	if err := json.Unmarshal([]byte(h.Stdout.String()), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", h.Stdout.String(), err)
	}
	if got.Schema != cli.Schema || got.Command != "tells" || got.Status != "adverse" ||
		got.Reason != "" || got.Profile != nil {
		t.Errorf("envelope = %+v", got)
	}
	requireEnvelopeFields(t, h.Stdout.String())
	// Measured against tells directly: the double-space rule matches at byte 16.
	want := cli.TellsResult{
		Path: path, Screening: "indeterminate", Count: 1,
		Findings: []cli.TellsFinding{{
			Rule: "double-space", Category: "formatting", Provenance: "unvalidated",
			Severity: "warn", Offset: 16, Length: 2,
		}},
	}
	if !reflect.DeepEqual(got.Result, want) {
		t.Errorf("result =\n%+v\nwant\n%+v", got.Result, want)
	}
}

// And a clean draft emits a complete document with an EMPTY findings list, not
// a null one and not an absent result.
func TestRunEmitsACompleteDocumentForACleanDraft(t *testing.T) {
	path := draft(t, clean)
	h := newHarness(t, nil)
	if code := h.run("tells", "--json", path); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, h.Stderr.String())
	}
	var got struct {
		Schema, Command, Status, Reason string
		Profile                         *string
		Result                          cli.TellsResult
	}
	if err := json.Unmarshal([]byte(h.Stdout.String()), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Schema != cli.Schema || got.Command != "tells" || got.Status != "ok" ||
		got.Reason != "" || got.Profile != nil {
		t.Errorf("envelope = %+v", got)
	}
	requireEnvelopeFields(t, h.Stdout.String())
	want := cli.TellsResult{Path: path, Screening: "indeterminate", Count: 0, Findings: []cli.TellsFinding{}}
	if !reflect.DeepEqual(got.Result, want) {
		t.Errorf("result =\n%+v\nwant\n%+v", got.Result, want)
	}
	if !strings.Contains(h.Stdout.String(), `"findings":[]`) {
		t.Errorf("findings is not an empty list: %s", h.Stdout.String())
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
		{"an equals form on local-only", []string{"tells", "--local-only=1", "draft.md"}},
		{"an equals form with no value", []string{"tells", "--json=", "draft.md"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if code := h.run(c.args...); code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if h.Stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", h.Stdout.String())
			}
			requireOneDiagnostic(t, h.Stderr.String())
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
	requireOneDiagnostic(t, h.Stderr.String())
}

// The file was read; it simply is not a document. Not an invalid invocation,
// because the invocation was well formed, and not a refusal, because no reason
// in the closed set covers it.
func TestADraftThatWillNotAdmitExitsThree(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("tells", draft(t, "Prose interrupted by \xff\xfe invalid bytes.\n")); code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	requireOneDiagnostic(t, h.Stderr.String())
	// A bare "21" would be satisfied by 121, a path, or unrelated prose.
	if !strings.Contains(h.Stderr.String(), "byte offset 21") {
		t.Errorf("the diagnostic does not name the byte offset: %q", h.Stderr.String())
	}
}

// Codes 2 and 3 carry no document, --json or not, because inventing a status
// for a failure would blur the correspondence the whole scheme rests on.
func TestFailuresWriteNoDocumentEvenAskedForJSON(t *testing.T) {
	notText := draft(t, "Prose interrupted by \xff\xfe invalid bytes.\n")
	for _, args := range [][]string{
		{"tells", "--json", "--nonsense"},
		{"tells", "--json", filepath.Join(t.TempDir(), "absent.md")},
		{"tells", "--json", notText},
		{"tells", "--json"},
	} {
		h := newHarness(t, nil)
		h.run(args...)
		requireNoDocument(t, h)
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
		{"tells", "--local-only", "--local-only", "--json", path},
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
	for _, operand := range []string{"--json", "-j", "--local-only", "-"} {
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
			requireNoDocument(t, h)
		})
	}
}

func TestAMalformedEnvironmentExitsTwoEvenWithTheFlag(t *testing.T) {
	h := newHarness(t, map[string]string{"HAPAX_LOCAL_ONLY": "yes"})
	if code := h.run("tells", "--local-only", draft(t, clean)); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	requireNoDocument(t, h)
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
	requireNoDocument(t, h)
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
// newline must not become two diagnostics, and every dynamic value is rendered
// with %q so the exact quoted form is what reaches the stream.
func TestADiagnosticIsOneEscapedLineWhateverTheFilenameContains(t *testing.T) {
	for _, name := range []string{"two\nlines.md", `quo"te.md`, "tab\there.md"} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			h := newHarness(t, nil)
			h.run("tells", path)
			diagnostic := h.Stderr.String()
			requireOneDiagnostic(t, diagnostic)
			if !strings.Contains(diagnostic, strconv.Quote(path)) {
				t.Errorf("the diagnostic does not carry %s: %q", strconv.Quote(path), diagnostic)
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

// Every path that fails writes exactly one escaped line to stderr. Asserted in
// one place so a new failure path cannot quietly write nothing, or two lines.
func requireOneDiagnostic(t *testing.T, diagnostic string) {
	t.Helper()
	if diagnostic == "" {
		t.Fatal("no diagnostic on stderr")
	}
	if !strings.HasSuffix(diagnostic, "\n") {
		t.Errorf("the diagnostic is not newline-terminated: %q", diagnostic)
	}
	if strings.Count(diagnostic, "\n") != 1 {
		t.Errorf("the diagnostic is %d lines, want one: %q", strings.Count(diagnostic, "\n"), diagnostic)
	}
}

// HAPAX_LOCAL_ONLY=1 on a command that succeeds, not only the flag on one that
// fails: the environment path has to reach the same place the flag does.
func TestTheEnvironmentSelectsLocalOnlyOnASuccessfulRun(t *testing.T) {
	h := newHarness(t, map[string]string{"HAPAX_LOCAL_ONLY": "1"})
	if code := h.run("tells", draft(t, clean)); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, h.Stderr.String())
	}
	if got := h.document(t)["status"]; got != "ok" {
		t.Errorf("status = %v, want ok", got)
	}
}

// A failing invocation writes nothing to stdout and exactly one line to stderr,
// whatever was asked for.
func requireNoDocument(t *testing.T, h *harness) {
	t.Helper()
	if h.Stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", h.Stdout.String())
	}
	requireOneDiagnostic(t, h.Stderr.String())
}

// The envelope carries exactly six fields — no more, so a document cannot grow
// one silently, and no fewer, so an absent one cannot pass as null.
func requireEnvelopeFields(t *testing.T, rendered string) {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := make([]string, 0, len(decoded))
	for name := range decoded {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"command", "profile", "reason", "result", "schema", "status"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("envelope fields =\n%v\nwant\n%v", got, want)
	}
}
