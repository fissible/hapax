package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// A fake service: cli owns grammar, classification and rendering, and nothing
// in this file needs a corpus or a database to say whether it got those right.
// ---------------------------------------------------------------------------

type fakeService struct {
	indexRequest   workflow.IndexRequest
	profileRequest workflow.ProfileRequest
	evalRequest    workflow.EvalRequest
	scoreRequest   workflow.ScoreRequest
	indexResult    workflow.IndexResult
	profileResult  workflow.ProfileResult
	evalResult     workflow.EvalResult
	scoreResult    workflow.ScoreResult
	rewriteRequest workflow.RewriteInput
	rewriteResult  workflow.RewriteOutcome
	err            error
	calls          int
}

func (f *fakeService) Score(_ context.Context, request workflow.ScoreRequest) (workflow.ScoreResult, error) {
	f.calls++
	f.scoreRequest = request
	return f.scoreResult, f.err
}

func (f *fakeService) Eval(_ context.Context, request workflow.EvalRequest) (workflow.EvalResult, error) {
	f.calls++
	f.evalRequest = request
	return f.evalResult, f.err
}

func (f *fakeService) Index(_ context.Context, request workflow.IndexRequest) (workflow.IndexResult, error) {
	f.calls++
	f.indexRequest = request
	return f.indexResult, f.err
}

func (f *fakeService) Profile(_ context.Context, request workflow.ProfileRequest) (workflow.ProfileResult, error) {
	f.calls++
	f.profileRequest = request
	return f.profileResult, f.err
}

// Rewrite exists because B2b-2b widened Service to carry the command. Nothing in
// this file exercises it; rewrite_test.go has a service of its own.
func (f *fakeService) Rewrite(_ context.Context, request workflow.RewriteInput) (workflow.RewriteOutcome, error) {
	f.calls++
	f.rewriteRequest = request
	return f.rewriteResult, f.err
}

type run struct {
	code   int
	stdout string
	stderr string
}

func runWith(t *testing.T, service workflow.Service, args ...string) run {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), args, cli.Deps{
		Stdout: &out, Stderr: &errOut,
		Env:      func(string) (string, bool) { return "", false },
		ReadFile: func(string) ([]byte, error) { return nil, errNotUsed{} },
		Getwd:    func() (string, error) { return "/somewhere", nil },
		Service:  service,
	})
	return run{code: code, stdout: out.String(), stderr: errOut.String()}
}

type errNotUsed struct{}

func (errNotUsed) Error() string { return "ReadFile is not part of these commands" }

func fullIndex() workflow.IndexResult {
	return workflow.IndexResult{
		StorePath: "/w/.hapax/hapax.sqlite3", SnapshotID: "5na", Mode: workflow.IndexProfileAndReference,
		Documents: 60, Eligible: 60, Nodes: 600, ProfileID: "pro", ReferenceID: "ref",
		NotReadyReason: "profile minimums are declared, not derived",
		Checks:         notPerformedChecks(),
	}
}

func notPerformedChecks() []workflow.Check {
	var out []workflow.Check
	for _, name := range workflow.CheckNames() {
		out = append(out, workflow.Check{Name: name, State: "not-performed"})
	}
	return out
}

// ---------------------------------------------------------------------------
// The grammar A1 settled, extended rather than replaced
// ---------------------------------------------------------------------------

// A1's rule is that the command is the first non-flag token wherever it appears.
// That worked because every flag was bare. --profile takes a value, so the value
// must be CONSUMED — otherwise "essays" is the first non-flag token and becomes
// the command.
func TestAValueTakingFlagConsumesItsValueRatherThanOfferingItAsTheCommand(t *testing.T) {
	for _, args := range [][]string{
		{"--profile", "essays", "index", "/w"},
		{"index", "--profile", "essays", "/w"},
		{"index", "--profile=essays", "/w"},
		{"--profile=essays", "index", "/w"},
		{"index", "/w", "--profile", "essays"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			service := &fakeService{indexResult: fullIndex()}
			result := runWith(t, service, args...)
			if result.code != 0 {
				t.Fatalf("code = %d, stderr %q", result.code, result.stderr)
			}
			if service.indexRequest.Register != "essays" {
				t.Errorf("register = %q", service.indexRequest.Register)
			}
			if service.indexRequest.CorpusRoot != "/w" {
				t.Errorf("corpus root = %q", service.indexRequest.CorpusRoot)
			}
		})
	}
}

// A flag that wants a value and is given none is an invalid invocation, not a
// flag with an empty value.
func TestAValueTakingFlagAtTheEndIsAnInvalidInvocation(t *testing.T) {
	result := runWith(t, &fakeService{}, "index", "/w", "--profile")
	if result.code != 2 {
		t.Errorf("code = %d, want 2", result.code)
	}
}

// -- still stops flag scanning and nothing else, so a corpus root that looks
// like a flag stays reachable.
func TestDoubleDashStillStopsFlagScanningOnly(t *testing.T) {
	service := &fakeService{indexResult: fullIndex()}
	result := runWith(t, service, "index", "--profile", "essays", "--", "--odd-root")
	if result.code != 0 {
		t.Fatalf("code = %d, stderr %q", result.code, result.stderr)
	}
	if service.indexRequest.CorpusRoot != "--odd-root" {
		t.Errorf("corpus root = %q", service.indexRequest.CorpusRoot)
	}
}

// index names a register or it does not run: the head is register-scoped and
// there is no safe default.
func TestIndexWithoutARegisterIsAnInvalidInvocation(t *testing.T) {
	service := &fakeService{indexResult: fullIndex()}
	result := runWith(t, service, "index", "/w")
	if result.code != 2 {
		t.Errorf("code = %d, want 2", result.code)
	}
	if service.calls != 0 {
		t.Error("the service was called for an invocation that could not be valid")
	}
}

func TestIndexRequiresExactlyOneCorpusRoot(t *testing.T) {
	for _, args := range [][]string{
		{"index", "--profile", "essays"},
		{"index", "--profile", "essays", "/one", "/two"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if result := runWith(t, &fakeService{}, args...); result.code != 2 {
				t.Errorf("code = %d, want 2", result.code)
			}
		})
	}
}

// profile takes no operand; the store is discovered or named.
func TestProfileTakesNoOperand(t *testing.T) {
	if result := runWith(t, &fakeService{}, "profile", "/w"); result.code != 2 {
		t.Errorf("code = %d, want 2", result.code)
	}
}

// ---------------------------------------------------------------------------
// Exit codes
// ---------------------------------------------------------------------------

// Index's codes turn on whether a verdict was produced, not on whether every
// check ran. A complete index exits 0 with every check reported not-performed:
// that is a limit of this slice, not an adverse finding about the corpus, and an
// index that could never exit 0 would tell a script nothing.
func TestIndexExitCodes(t *testing.T) {
	adverse := func(adversity workflow.Adversity, mode workflow.IndexMode) workflow.IndexResult {
		result := fullIndex()
		result.Mode, result.Adverse, result.Adversity = mode, true, adversity
		if mode == workflow.IndexSnapshotOnly {
			result.ProfileID, result.ReferenceID = "", ""
		} else {
			result.ReferenceID = ""
		}
		return result
	}
	for _, c := range []struct {
		name   string
		result workflow.IndexResult
		err    error
		code   int
		status cli.Status
	}{
		{"complete", fullIndex(), nil, 0, cli.StatusOK},
		{"corpus too small", adverse(workflow.AdversityCorpusTooSmall, workflow.IndexSnapshotOnly), nil, 1, cli.StatusAdverse},
		{"reference too small", adverse(workflow.AdversityReferenceTooSmall, workflow.IndexProfile), nil, 1, cli.StatusAdverse},
		{"operational failure", workflow.IndexResult{}, errNotUsed{}, 3, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			service := &fakeService{indexResult: c.result, err: c.err}
			got := runWith(t, service, "--json", "index", "--profile", "essays", "/w")
			if got.code != c.code {
				t.Fatalf("code = %d, want %d (stderr %q)", got.code, c.code, got.stderr)
			}
			if c.code == 3 {
				if got.stdout != "" {
					t.Errorf("a failed command emitted a result document: %q", got.stdout)
				}
				return
			}
			if status := decode(t, got.stdout).Status; status != c.status {
				t.Errorf("status = %q, want %q", status, c.status)
			}
		})
	}
}

// profile's are different: a store with no head is a refusal with a reason,
// while an ambiguous or misspelled register is a correctable invocation.
func TestProfileExitCodes(t *testing.T) {
	with := func(selection workflow.Selection, available ...string) workflow.ProfileResult {
		return workflow.ProfileResult{
			StorePath: "/w/.hapax/hapax.sqlite3", Selection: selection, Available: available,
			Profile: workflow.StoredProfile{ID: "pro", Register: "essays", NotReadyReason: "declared"},
		}
	}
	for _, c := range []struct {
		name       string
		result     workflow.ProfileResult
		code       int
		status     cli.Status
		reason     cli.Reason
		mentions   []string
		noDocument bool
	}{
		{name: "sole head", result: with(workflow.SelectedSoleHead), code: 0, status: cli.StatusOK},
		{name: "named", result: with(workflow.SelectedExplicit), code: 0, status: cli.StatusOK},
		{
			name: "no profile at all", result: with(workflow.SelectionNoProfile),
			code: 4, status: cli.StatusRefused, reason: cli.ReasonNoProfile,
		},
		{
			name: "several and none named", result: with(workflow.SelectionAmbiguous, "essays", "letters"),
			code: 2, mentions: []string{"essays", "letters"}, noDocument: true,
		},
		{
			name: "a register that is not there", result: with(workflow.SelectionUnknownRegister, "essays"),
			code: 2, mentions: []string{"essays"}, noDocument: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runWith(t, &fakeService{profileResult: c.result}, "--json", "profile")
			if got.code != c.code {
				t.Fatalf("code = %d, want %d (stderr %q)", got.code, c.code, got.stderr)
			}
			for _, wanted := range c.mentions {
				if !strings.Contains(got.stderr, wanted) {
					t.Errorf("the diagnostic does not name %q: %q", wanted, got.stderr)
				}
			}
			if c.noDocument {
				if got.stdout != "" {
					t.Errorf("an invalid invocation emitted a result document: %q", got.stdout)
				}
				return
			}
			document := decode(t, got.stdout)
			if document.Status != c.status {
				t.Errorf("status = %q, want %q", document.Status, c.status)
			}
			if document.Reason != c.reason {
				t.Errorf("reason = %q, want %q", document.Reason, c.reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The result document
// ---------------------------------------------------------------------------

// One payload, named by command. hapax.v1 consumers of tells read result.path;
// wrapping it in result.tells to make room would break them for no gain, so the
// discriminator is the command field that was always there.
func TestTheResultCarriesExactlyTheCommandsOwnPayload(t *testing.T) {
	indexOut := runWith(t, &fakeService{indexResult: fullIndex()}, "--json", "index", "--profile", "essays", "/w")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(indexOut.stdout), &raw); err != nil {
		t.Fatalf("decoding %q: %v", indexOut.stdout, err)
	}
	if _, wrapped := raw["result"]; !wrapped {
		t.Fatal("no result member")
	}
	var result map[string]any
	if err := json.Unmarshal(raw["result"], &result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	for _, wanted := range []string{"store", "snapshot_id", "mode", "profile_id", "reference_id", "checks"} {
		if _, present := result[wanted]; !present {
			t.Errorf("an index result has no %q: %v", wanted, keysOf(result))
		}
	}
	// And nothing from another command's payload rides along.
	for _, foreign := range []string{"findings", "screening", "available_profiles"} {
		if _, present := result[foreign]; present {
			t.Errorf("an index result carries %q, which belongs to another command", foreign)
		}
	}

	profileOut := runWith(t, &fakeService{profileResult: workflow.ProfileResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedSoleHead,
		Profile: workflow.StoredProfile{ID: "pro", Register: "essays", NotReadyReason: "declared"},
	}}, "--json", "profile")
	var profileRaw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(profileOut.stdout), &profileRaw); err != nil {
		t.Fatalf("decoding %q: %v", profileOut.stdout, err)
	}
	var profileResult map[string]any
	if err := json.Unmarshal(profileRaw["result"], &profileResult); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if _, present := profileResult["snapshot_id"]; present {
		t.Error("a profile result carries an index payload")
	}
}

// Every check the workflow reported reaches the document. Reporting a subset
// would let a check added later go unmentioned while everything still passed.
func TestEveryReportedCheckReachesTheDocument(t *testing.T) {
	got := runWith(t, &fakeService{indexResult: fullIndex()}, "--json", "index", "--profile", "essays", "/w")
	var result struct {
		Checks []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(decode(t, got.stdout).Result, &result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	seen := map[string]string{}
	for _, check := range result.Checks {
		if _, twice := seen[check.Name]; twice {
			t.Errorf("%s appears twice", check.Name)
		}
		seen[check.Name] = check.State
	}
	for _, name := range workflow.CheckNames() {
		state, present := seen[name]
		if !present {
			t.Errorf("%s never reached the document", name)
		} else if state != "not-performed" {
			t.Errorf("%s = %q", name, state)
		}
	}
	if len(seen) != len(workflow.CheckNames()) {
		t.Errorf("the document carries %d checks and %d are declared", len(seen), len(workflow.CheckNames()))
	}
}

// The human rendering says which store it used, because "which database did
// that touch" is the first question a second corpus makes you ask.
func TestTheHumanRenderingNamesTheStore(t *testing.T) {
	got := runWith(t, &fakeService{indexResult: fullIndex()}, "index", "--profile", "essays", "/w")
	if !strings.Contains(got.stdout, "/w/.hapax/hapax.sqlite3") {
		t.Errorf("the rendering does not name the store: %q", got.stdout)
	}
}

// A1's command is untouched by any of this.
func TestTellsStillWorks(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), []string{"tells", "draft.md"}, cli.Deps{
		Stdout: &out, Stderr: &errOut,
		Env:      func(string) (string, bool) { return "", false },
		ReadFile: func(string) ([]byte, error) { return []byte("An ordinary paragraph of prose.\n"), nil },
		Getwd:    func() (string, error) { return "/somewhere", nil },
		Service:  &fakeService{},
	})
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "tells ok") {
		t.Errorf("rendering = %q", out.String())
	}
}

// decoded reads the WIRE FORMAT rather than cli's own types. Unmarshalling
// into cli.Document would test that the union round-trips through Go, which is
// not what a consumer of hapax.v1 does and would oblige the union to implement
// unmarshalling it has no other reason to have.
type decoded struct {
	Schema  string          `json:"schema"`
	Command string          `json:"command"`
	Status  cli.Status      `json:"status"`
	Reason  cli.Reason      `json:"reason"`
	Result  json.RawMessage `json:"result"`
}

func decode(t *testing.T, encoded string) decoded {
	t.Helper()
	var document decoded
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatalf("decoding %q: %v", encoded, err)
	}
	if document.Schema != cli.Schema {
		t.Errorf("schema = %q, want %q", document.Schema, cli.Schema)
	}
	if !contains(cli.Commands(), document.Command) {
		t.Errorf("command = %q, which is not a command this binary has", document.Command)
	}
	return document
}

func keysOf(m map[string]any) []string {
	var out []string
	for key := range m {
		out = append(out, key)
	}
	return out
}

// ---------------------------------------------------------------------------
// What the command hands the service
// ---------------------------------------------------------------------------

// The seam is where a feature goes missing without any test noticing: the CLI
// tests use a fake and the workflow tests call it directly, so a flag that never
// reaches the request would leave both green.
func TestTheRequestCarriesWhatWasAskedFor(t *testing.T) {
	t.Run("index --store", func(t *testing.T) {
		service := &fakeService{indexResult: fullIndex()}
		if got := runWith(t, service, "index", "--profile", "essays", "--store", "/tmp/x.db", "/w"); got.code != 0 {
			t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
		}
		if service.indexRequest.StorePath != "/tmp/x.db" {
			t.Errorf("store path = %q", service.indexRequest.StorePath)
		}
	})

	t.Run("index without --store leaves the choice to the workflow", func(t *testing.T) {
		service := &fakeService{indexResult: fullIndex()}
		runWith(t, service, "index", "--profile", "essays", "/w")
		if service.indexRequest.StorePath != "" {
			t.Errorf("store path = %q; an unspecified store is the workflow's to derive", service.indexRequest.StorePath)
		}
	})

	t.Run("profile --profile", func(t *testing.T) {
		service := &fakeService{profileResult: soleHead()}
		if got := runWith(t, service, "profile", "--profile", "letters"); got.code != 0 {
			t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
		}
		if service.profileRequest.Register != "letters" {
			t.Errorf("register = %q", service.profileRequest.Register)
		}
	})

	t.Run("profile --store", func(t *testing.T) {
		service := &fakeService{profileResult: soleHead()}
		runWith(t, service, "profile", "--store", "/tmp/y.db")
		if service.profileRequest.StorePath != "/tmp/y.db" {
			t.Errorf("store path = %q", service.profileRequest.StorePath)
		}
	})

	// profile has no operand, so the directory it searches from is the process's
	// own. If that never reached the request, discovery would start somewhere
	// the user is not.
	t.Run("the working directory reaches the request", func(t *testing.T) {
		service := &fakeService{profileResult: soleHead()}
		var out, errOut bytes.Buffer
		cli.Run(context.Background(), []string{"profile"}, cli.Deps{
			Stdout: &out, Stderr: &errOut,
			Env:      func(string) (string, bool) { return "", false },
			ReadFile: func(string) ([]byte, error) { return nil, errNotUsed{} },
			Getwd:    func() (string, error) { return "/where/the/user/is", nil },
			Service:  service,
		})
		if service.profileRequest.StartDir != "/where/the/user/is" {
			t.Errorf("start dir = %q", service.profileRequest.StartDir)
		}
	})

	// And a working directory that cannot be determined is an operational
	// failure rather than a search from an empty string.
	t.Run("an unavailable working directory fails", func(t *testing.T) {
		service := &fakeService{profileResult: soleHead()}
		var out, errOut bytes.Buffer
		code := cli.Run(context.Background(), []string{"profile"}, cli.Deps{
			Stdout: &out, Stderr: &errOut,
			Env:      func(string) (string, bool) { return "", false },
			ReadFile: func(string) ([]byte, error) { return nil, errNotUsed{} },
			Getwd:    func() (string, error) { return "", errNotUsed{} },
			Service:  service,
		})
		if code != 3 {
			t.Errorf("code = %d, want 3", code)
		}
		if service.calls != 0 {
			t.Error("the service was called with no directory to search from")
		}
	})
}

// A profile lookup that fails operationally is exit 3 and no document, the same
// as an index that fails: a refusal is a decision and this is not one.
func TestAProfileThatFailsOperationallyIsExitThree(t *testing.T) {
	got := runWith(t, &fakeService{err: errNotUsed{}}, "--json", "profile")
	if got.code != 3 {
		t.Errorf("code = %d, want 3", got.code)
	}
	if got.stdout != "" {
		t.Errorf("a failed command emitted a result document: %q", got.stdout)
	}
}

// The new commands do not loosen A1's flag checking.
func TestTheNewCommandsStillRejectUnknownFlags(t *testing.T) {
	for _, args := range [][]string{
		{"index", "--profile", "essays", "--nonsense", "/w"},
		{"profile", "--nonsense"},
		{"profile", "--store"},
		{"index", "--profile", "essays", "/w", "--store"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			service := &fakeService{indexResult: fullIndex(), profileResult: soleHead()}
			if got := runWith(t, service, args...); got.code != 2 {
				t.Errorf("code = %d, want 2", got.code)
			}
			if service.calls != 0 {
				t.Error("the service ran for an invocation that could not be valid")
			}
		})
	}
}

// rawResultMembers returns the result payload's members unparsed.
func rawResultMembers(t *testing.T, encoded string) map[string]json.RawMessage {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(decode(t, encoded).Result, &members); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	return members
}

// rawProfileMember returns one member of the profile payload unparsed.
func rawProfileMember(t *testing.T, encoded, name string) json.RawMessage {
	t.Helper()
	var result struct {
		Profile map[string]json.RawMessage `json:"profile"`
	}
	if err := json.Unmarshal(decode(t, encoded).Result, &result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	value, present := result.Profile[name]
	if !present {
		t.Fatalf("the profile payload has no %q member", name)
	}
	return value
}

// rawStats reads the statistics as raw members, so a missing key is visible as
// a missing key rather than as a zero value.
func rawStats(t *testing.T, encoded string) []map[string]json.RawMessage {
	t.Helper()
	var result struct {
		Profile struct {
			Stats []map[string]json.RawMessage `json:"stats"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(decode(t, encoded).Result, &result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	return result.Profile.Stats
}

func soleHead() workflow.ProfileResult {
	return workflow.ProfileResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedSoleHead,
		Profile: workflow.StoredProfile{ID: "pro", Register: "essays", NotReadyReason: "declared"},
	}
}

func contains[T comparable](values []T, wanted T) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// The profile payload has to carry the whole answer across, not merely avoid
// carrying the wrong command's. Two cases with OPPOSITE readiness, because a
// mapping that hard-coded either value would satisfy one of them, and every
// statistic field asserted against a distinct value, because a field seeded and
// never checked is a field that can be dropped.
func TestTheProfilePayloadCarriesTheWholeAnswer(t *testing.T) {
	for _, c := range []struct {
		name      string
		ready     bool
		reason    string
		evaluated bool
	}{
		{"not ready, not evaluated", false, "profile minimums are declared, not derived", false},
		{"ready and evaluated", true, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			service := &fakeService{profileResult: workflow.ProfileResult{
				StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedExplicit,
				Available:   []string{"essays", "letters"},
				ReferenceID: "ref", Evaluated: c.evaluated,
				Profile: workflow.StoredProfile{
					ID: "pro", SnapshotID: "snap", Register: "essays",
					ProductionReady: c.ready, NotReadyReason: c.reason,
					// Two statistics, because an implementation that emitted
					// only the first would satisfy a single-element fixture;
					// and defined against variance_defined, because two fields
					// that always agree can be mapped from each other.
					Stats: []workflow.StoredStat{
						{
							Feature: "function-word-rate", N: 612, Mean: 0.42,
							Variance: 0.0031, Defined: true, VarianceDefined: false, MinObservations: 30,
						},
						{
							Feature: "comma-density", N: 611, Mean: 0.07,
							Variance: 0.0004, Defined: false, VarianceDefined: true, MinObservations: 29,
						},
					},
				},
			}}
			got := runWith(t, service, "--json", "profile", "--profile", "essays")
			if got.code != 0 {
				t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
			}

			var result struct {
				Store     string   `json:"store"`
				Selection string   `json:"selection"`
				Available []string `json:"available_profiles"`
				Reference *string  `json:"reference_id"`
				Evaluated bool     `json:"evaluated"`
				Profile   struct {
					ID              string `json:"id"`
					SnapshotID      string `json:"snapshot_id"`
					Register        string `json:"register"`
					ProductionReady bool   `json:"production_ready"`
					NotReadyReason  string `json:"not_ready_reason"`
					Stats           []struct {
						Feature         string  `json:"feature"`
						N               int     `json:"n"`
						Mean            float64 `json:"mean"`
						Variance        float64 `json:"variance"`
						Defined         bool    `json:"defined"`
						VarianceDefined bool    `json:"variance_defined"`
						MinObservations int     `json:"min_observations"`
					} `json:"stats"`
				} `json:"profile"`
			}
			if err := json.Unmarshal(decode(t, got.stdout).Result, &result); err != nil {
				t.Fatalf("decoding result: %v", err)
			}

			if result.Store != "/w/.hapax/hapax.sqlite3" {
				t.Errorf("store = %q", result.Store)
			}
			if result.Selection != string(workflow.SelectedExplicit) {
				t.Errorf("selection = %q", result.Selection)
			}
			if len(result.Available) != 2 || result.Available[0] != "essays" || result.Available[1] != "letters" {
				t.Errorf("available = %v", result.Available)
			}
			if result.Reference == nil || *result.Reference != "ref" {
				t.Errorf("reference = %v", result.Reference)
			}
			if result.Evaluated != c.evaluated {
				t.Errorf("evaluated = %v, want %v", result.Evaluated, c.evaluated)
			}
			if result.Profile.ID != "pro" || result.Profile.SnapshotID != "snap" || result.Profile.Register != "essays" {
				t.Errorf("identity did not survive: %+v", result.Profile)
			}
			if result.Profile.ProductionReady != c.ready {
				t.Errorf("production_ready = %v, want %v", result.Profile.ProductionReady, c.ready)
			}
			if raw := rawProfileMember(t, got.stdout, "production_ready"); string(raw) != "true" && string(raw) != "false" {
				t.Errorf("production_ready is emitted as %s, which is not a boolean", raw)
			}
			if result.Profile.NotReadyReason != c.reason {
				t.Errorf("not_ready_reason = %q, want %q", result.Profile.NotReadyReason, c.reason)
			}
			if len(result.Profile.Stats) != 2 {
				t.Fatalf("%d statistics survived, want 2", len(result.Profile.Stats))
			}
			// Decoding into a struct cannot tell an absent member from a false
			// one, so the members are checked as raw keys first: omitting
			// variance_defined entirely would otherwise read as false and pass.
			for index, raw := range rawStats(t, got.stdout) {
				for _, key := range []string{
					"feature", "n", "mean", "variance", "defined", "variance_defined", "min_observations",
				} {
					value, present := raw[key]
					if !present {
						t.Errorf("statistic %d does not emit %q at all", index, key)
						continue
					}
					// null decodes to the zero value exactly as an absent member
					// does, so presence alone is not enough.
					if string(value) == "null" {
						t.Errorf("statistic %d emits %q as null", index, key)
					}
				}
			}
			first, second := result.Profile.Stats[0], result.Profile.Stats[1]
			if first.Feature != "function-word-rate" || first.N != 612 ||
				first.Mean != 0.42 || first.Variance != 0.0031 ||
				!first.Defined || first.VarianceDefined || first.MinObservations != 30 {
				t.Errorf("a field of the first statistic was dropped or rewritten: %+v", first)
			}
			if second.Feature != "comma-density" || second.N != 611 ||
				second.Mean != 0.07 || second.Variance != 0.0004 ||
				second.Defined || !second.VarianceDefined || second.MinObservations != 29 {
				t.Errorf("a field of the second statistic was dropped or rewritten: %+v", second)
			}
		})
	}
}

// A profile with no reference reports null rather than an empty string, so a
// consumer can tell "no reference" from "a reference whose id is blank".
func TestAnAbsentReferenceIsNullAndNotEmpty(t *testing.T) {
	got := runWith(t, &fakeService{profileResult: soleHead()}, "--json", "profile")
	var result map[string]any
	if err := json.Unmarshal(decode(t, got.stdout).Result, &result); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	value, present := result["reference_id"]
	if !present {
		t.Fatal("no reference_id member")
	}
	if value != nil {
		t.Errorf("reference_id = %v, want null", value)
	}
}

// The index payload, by value rather than by presence. An earlier version of
// this file only checked that six members existed, which a projection emitting
// zeroes for all of them would have satisfied.
func TestTheIndexPayloadCarriesTheWholeAnswer(t *testing.T) {
	full := fullIndex()
	full.Documents, full.Eligible, full.Nodes = 64, 60, 600
	full.CalibrateSegments, full.TrainParagraphs = 60, 510
	full.Pruned = workflow.Pruned{Snapshots: 1, Documents: 2, Nodes: 3, Profiles: 4}

	adverse := fullIndex()
	adverse.Mode, adverse.Adverse, adverse.Adversity = workflow.IndexSnapshotOnly, true, workflow.AdversityCorpusTooSmall
	adverse.ProfileID, adverse.ReferenceID, adverse.NotReadyReason = "", "", ""
	adverse.Documents, adverse.Eligible, adverse.Nodes = 2, 2, 20
	adverse.CalibrateSegments, adverse.TrainParagraphs = 0, 0

	// The middle mode is the only shape where one identity is set and the other
	// is not, so neither row above would catch a projection that dropped the
	// profile it kept.
	middle := fullIndex()
	middle.Mode, middle.Adverse, middle.Adversity = workflow.IndexProfile, true, workflow.AdversityReferenceTooSmall
	middle.ReferenceID = ""
	middle.Documents, middle.Eligible, middle.Nodes = 3, 3, 30
	middle.CalibrateSegments, middle.TrainParagraphs = 0, 30

	for _, c := range []struct {
		name       string
		result     workflow.IndexResult
		code       int
		wantProfle bool
		wantRef    bool
		adversity  workflow.Adversity
	}{
		{"complete", full, 0, true, true, ""},
		{"corpus too small", adverse, 1, false, false, workflow.AdversityCorpusTooSmall},
		{"reference too small", middle, 1, true, false, workflow.AdversityReferenceTooSmall},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runWith(t, &fakeService{indexResult: c.result}, "--json", "index", "--profile", "essays", "/w")
			if got.code != c.code {
				t.Fatalf("code = %d, want %d (stderr %q)", got.code, c.code, got.stderr)
			}
			var result struct {
				Store             string  `json:"store"`
				SnapshotID        string  `json:"snapshot_id"`
				Mode              string  `json:"mode"`
				Adversity         string  `json:"adversity"`
				Documents         int     `json:"documents"`
				Eligible          int     `json:"eligible"`
				Nodes             int     `json:"nodes"`
				CalibrateSegments int     `json:"calibrate_segments"`
				TrainParagraphs   int     `json:"train_paragraphs"`
				ProfileID         *string `json:"profile_id"`
				ReferenceID       *string `json:"reference_id"`
				NotReadyReason    string  `json:"profile_not_ready_reason"`
				Pruned            struct {
					Snapshots, Documents, Nodes, Profiles int
				} `json:"pruned"`
			}
			if err := json.Unmarshal(decode(t, got.stdout).Result, &result); err != nil {
				t.Fatalf("decoding result: %v", err)
			}

			if result.Store != c.result.StorePath {
				t.Errorf("store = %q, want %q", result.Store, c.result.StorePath)
			}
			if result.SnapshotID != c.result.SnapshotID {
				t.Errorf("snapshot_id = %q, want %q", result.SnapshotID, c.result.SnapshotID)
			}
			if result.Mode != string(c.result.Mode) {
				t.Errorf("mode = %q, want %q", result.Mode, c.result.Mode)
			}
			if result.Adversity != string(c.adversity) {
				t.Errorf("adversity = %q, want %q", result.Adversity, c.adversity)
			}
			if result.Documents != c.result.Documents || result.Eligible != c.result.Eligible ||
				result.Nodes != c.result.Nodes {
				t.Errorf("counts did not survive: %d/%d/%d, want %d/%d/%d",
					result.Documents, result.Eligible, result.Nodes,
					c.result.Documents, c.result.Eligible, c.result.Nodes)
			}
			if result.CalibrateSegments != c.result.CalibrateSegments || result.TrainParagraphs != c.result.TrainParagraphs {
				t.Errorf("segment counts did not survive: %d calibrate, %d train paragraphs",
					result.CalibrateSegments, result.TrainParagraphs)
			}
			// What was fitted is reported; what was not is null rather than an
			// empty string that reads as an identity. Checked as RAW members,
			// because a struct decode cannot tell an absent key from a null one.
			raw := rawResultMembers(t, got.stdout)
			for _, member := range []struct {
				name   string
				wanted bool
				value  string
			}{
				{"profile_id", c.wantProfle, c.result.ProfileID},
				{"reference_id", c.wantRef, c.result.ReferenceID},
			} {
				encoded, present := raw[member.name]
				if !present {
					t.Errorf("%s is not emitted at all", member.name)
					continue
				}
				if !member.wanted {
					if string(encoded) != "null" {
						t.Errorf("%s = %s where nothing was fitted, want null", member.name, encoded)
					}
					continue
				}
				if string(encoded) == "null" {
					t.Errorf("%s is null where %q was fitted", member.name, member.value)
				}
			}
			if c.wantProfle {
				if result.ProfileID == nil || *result.ProfileID != c.result.ProfileID {
					t.Errorf("profile_id = %v, want %q", result.ProfileID, c.result.ProfileID)
				}
				if result.NotReadyReason != c.result.NotReadyReason {
					t.Errorf("profile_not_ready_reason = %q, want %q", result.NotReadyReason, c.result.NotReadyReason)
				}
			} else if result.NotReadyReason != "" {
				t.Errorf("profile_not_ready_reason = %q with no profile to be unready", result.NotReadyReason)
			}
			if c.wantRef && (result.ReferenceID == nil || *result.ReferenceID != c.result.ReferenceID) {
				t.Errorf("reference_id = %v, want %q", result.ReferenceID, c.result.ReferenceID)
			}
			if result.Pruned.Snapshots != c.result.Pruned.Snapshots ||
				result.Pruned.Documents != c.result.Pruned.Documents ||
				result.Pruned.Nodes != c.result.Pruned.Nodes ||
				result.Pruned.Profiles != c.result.Pruned.Profiles {
				t.Errorf("what was pruned did not survive: %+v, want %+v", result.Pruned, c.result.Pruned)
			}
		})
	}
}
