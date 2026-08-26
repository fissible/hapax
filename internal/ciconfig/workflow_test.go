// Package ciconfig holds executable assertions about this repository's CI
// configuration.
//
// Deliberately narrow: it asserts only invariants that GitHub's own YAML and
// schema validation cannot establish, and whose violation fails quietly rather
// than loudly. Searching for tool names or parsing YAML for its own sake is
// maintenance cost without assurance, so it is not done here.
package ciconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const workflowPath = "../../.github/workflows/ci.yml"

type workflow struct {
	Name        string         `yaml:"name"`
	On          yaml.Node      `yaml:"on"`
	Permissions yaml.Node      `yaml:"permissions"`
	Jobs        map[string]job `yaml:"jobs"`
}

type job struct {
	RunsOn   yaml.Node `yaml:"runs-on"`
	Strategy struct {
		Matrix map[string]yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []step `yaml:"steps"`
}

type step struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

func load(t *testing.T) (workflow, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(workflowPath))
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parsing %s: %v", workflowPath, err)
	}
	if len(wf.Jobs) == 0 {
		t.Fatalf("%s defines no jobs", workflowPath)
	}
	return wf, string(raw)
}

// scalars flattens a node that GitHub allows in scalar, sequence or mapping
// form — `on:` and `runs-on:` both accept more than one shape.
func scalars(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, scalars(*c)...)
		}
		return out
	case yaml.MappingNode:
		out := make([]string, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		return out
	}
	return nil
}

// runnersFor resolves the operating systems a job actually executes on,
// following a matrix reference rather than trusting a substring.
func runnersFor(j job) []string {
	src := j.RunsOn
	// Mapping form is {group, labels}; the OS lives under labels, and taking the
	// keys would yield "group"/"labels" and match nothing.
	if src.Kind == yaml.MappingNode {
		if labels, ok := mappingValue(src, "labels"); ok {
			src = labels
		}
	}
	var out []string
	for _, v := range scalars(src) {
		if ref := matrixRef(v); ref != "" {
			if node, ok := j.Strategy.Matrix[ref]; ok {
				out = append(out, scalars(node)...)
				continue
			}
		}
		out = append(out, v)
	}
	return out
}

func mappingValue(n yaml.Node, key string) (yaml.Node, bool) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return *n.Content[i+1], true
		}
	}
	return yaml.Node{}, false
}

// goTestCommand returns the arguments of a `go test` invocation on this line,
// truncated at the first shell separator or comment so that text belonging to a
// different command cannot be mistaken for an argument.
func goTestCommand(line string) (string, bool) {
	idx := strings.Index(line, "go test")
	if idx < 0 {
		return "", false
	}
	seg := line[idx:]
	for _, sep := range []string{"#", ";", "&&", "||", "|"} {
		if i := strings.Index(seg, sep); i >= 0 {
			seg = seg[:i]
		}
	}
	return seg, true
}

func jobRunsGoTest(j job) bool {
	for _, s := range j.Steps {
		for _, line := range strings.Split(s.Run, "\n") {
			if _, ok := goTestCommand(line); ok {
				return true
			}
		}
	}
	return false
}

var matrixExpr = regexp.MustCompile(`\$\{\{\s*matrix\.([A-Za-z0-9_-]+)\s*\}\}`)

func matrixRef(s string) string {
	if m := matrixExpr.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func goDirective(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "go ") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "go "))
			if parts := strings.Split(v, "."); len(parts) >= 2 {
				return parts[0] + "." + parts[1]
			}
			return v
		}
	}
	t.Fatal("go.mod declares no go directive")
	return ""
}

// pull_request_target runs workflow code with a write-scoped token against a
// fork's contents. On a public repository that is a known privilege escalation,
// and nothing here needs it.
func TestNoPullRequestTargetTrigger(t *testing.T) {
	wf, _ := load(t)
	for _, trigger := range scalars(wf.On) {
		if trigger == "pull_request_target" {
			t.Error("workflow triggers on pull_request_target: unsafe on a public repo and unnecessary here")
		}
	}
}

func TestTriggersOnPushAndPullRequest(t *testing.T) {
	wf, _ := load(t)
	got := scalars(wf.On)
	for _, want := range []string{"push", "pull_request"} {
		if !contains(got, want) {
			t.Errorf("workflow does not trigger on %q; triggers are %v", want, got)
		}
	}
}

// Repository secrets are unavailable to pull requests from forks, so a job that
// needs one fails for every outside contributor with an error they cannot fix.
// GITHUB_TOKEN is exempt: GitHub supplies it automatically, including on forks.
func TestNoRepositorySecrets(t *testing.T) {
	_, raw := load(t)
	refs := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(raw, -1)
	for _, m := range refs {
		if m[1] != "GITHUB_TOKEN" {
			t.Errorf("workflow references secrets.%s; fork pull requests cannot access repository secrets", m[1])
		}
	}
}

// Without a checkout step, `go test ./...` runs against an empty workspace and
// reports success. The order matters, not merely the presence.
func TestCheckoutPrecedesGoCommands(t *testing.T) {
	wf, _ := load(t)
	for name, j := range wf.Jobs {
		checkedOut := false
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/checkout@") {
				checkedOut = true
			}
			if runsGo(s.Run) && !checkedOut {
				t.Errorf("job %q runs %q before any actions/checkout step", name, firstGoLine(s.Run))
			}
		}
	}
}

// The module declares a minimum Go version; CI has to actually install it, or
// the declaration is untested.
func TestSetupGoInstallsDeclaredMinimum(t *testing.T) {
	wf, _ := load(t)
	min := goDirective(t)

	tested := 0
	for name, j := range wf.Jobs {
		// A setup-only job proves nothing; the job that runs the tests is the one
		// that must install the declared minimum.
		if !jobRunsGoTest(j) {
			continue
		}
		tested++

		// A job may legitimately have more than one setup-go step; it is enough
		// that one of them covers the declared minimum.
		var seen []string
		found := false
		for _, st := range j.Steps {
			if !strings.HasPrefix(st.Uses, "actions/setup-go@") {
				continue
			}
			found = true
			version := st.With["go-version"]
			if ref := matrixRef(version); ref != "" {
				if node, ok := j.Strategy.Matrix[ref]; ok {
					seen = append(seen, scalars(node)...)
					continue
				}
			}
			seen = append(seen, version)
		}
		if !found {
			t.Errorf("job %q runs go test without actions/setup-go; it would use whatever Go the runner ships", name)
			continue
		}
		if !anyHasPrefix(seen, min) {
			t.Errorf("job %q sets up Go %v, none of which covers the go.mod minimum %s", name, seen, min)
		}
	}
	if tested == 0 {
		t.Error("no job runs go test")
	}
}

// The text package uses sync.Once and lazily populated state. The race detector
// must be on in the step that actually runs the tests, not merely mentioned
// somewhere in the file.
func TestGoTestRunsWithRaceDetector(t *testing.T) {
	wf, _ := load(t)
	sawTest := false
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			for _, line := range strings.Split(s.Run, "\n") {
				cmd, ok := goTestCommand(line)
				if !ok {
					continue
				}
				sawTest = true
				if !contains(strings.Fields(cmd), "-race") {
					t.Errorf("job %q runs %q without -race as an argument", name, strings.TrimSpace(cmd))
				}
			}
		}
	}
	if !sawTest {
		t.Error("no step runs go test")
	}
}

// Byte offsets, line endings and Unicode handling are platform-sensitive, so a
// single runner is not adequate coverage. Resolved through the matrix rather
// than by searching the file for "macos".
func TestTestsExecuteOnLinuxAndMacOS(t *testing.T) {
	wf, _ := load(t)
	covered := map[string]bool{}
	for _, j := range wf.Jobs {
		runsTests := false
		for _, s := range j.Steps {
			if strings.Contains(s.Run, "go test") {
				runsTests = true
			}
		}
		if !runsTests {
			continue
		}
		for _, r := range runnersFor(j) {
			switch {
			case strings.HasPrefix(r, "ubuntu"):
				covered["linux"] = true
			case strings.HasPrefix(r, "macos"):
				covered["macos"] = true
			}
		}
	}
	for _, want := range []string{"linux", "macos"} {
		if !covered[want] {
			t.Errorf("no job running go test executes on %s; span and line-ending behavior is platform-sensitive", want)
		}
	}
}

// A moving tag can be repointed at new code without any commit in this repo.
// Local (./) and Docker (docker://) references are not subject to that.
func TestActionsPinnedToCommitSHA(t *testing.T) {
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	wf, _ := load(t)
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			u := s.Uses
			if u == "" || strings.HasPrefix(u, "./") || strings.HasPrefix(u, "docker://") {
				continue
			}
			_, ref, ok := strings.Cut(u, "@")
			if !ok || !sha.MatchString(ref) {
				t.Errorf("job %q uses %q; pin to a full 40-character commit SHA, since a tag can be repointed", name, u)
			}
		}
	}
}

func runsGo(run string) bool { return firstGoLine(run) != "" }

func firstGoLine(run string) string {
	for _, line := range strings.Split(run, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "go ") || strings.HasPrefix(trimmed, "gofmt") {
			return trimmed
		}
	}
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func anyHasPrefix(values []string, prefix string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// Omitting `permissions` inherits the repository or organization default, which
// may grant write scopes. Nothing in this workflow needs a token, so least
// privilege has to be declared rather than assumed from the absence of secrets.
func TestPermissionsAreLeastPrivilege(t *testing.T) {
	wf, _ := load(t)
	if wf.Permissions.Kind == 0 {
		t.Fatal("workflow declares no top-level permissions; it then inherits repository defaults, which may include write")
	}
	if wf.Permissions.Kind == yaml.ScalarNode {
		if v := wf.Permissions.Value; v != "read-all" && v != "{}" {
			t.Errorf("permissions is %q; nothing here needs a token, so declare an empty mapping", v)
		}
		return
	}
	for i := 0; i+1 < len(wf.Permissions.Content); i += 2 {
		scope, level := wf.Permissions.Content[i].Value, wf.Permissions.Content[i+1].Value
		if level == "write" {
			t.Errorf("permissions grants write on %q; no step here needs it", scope)
		}
	}
}
