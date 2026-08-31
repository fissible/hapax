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
// Grammar
// ---------------------------------------------------------------------------

// --distractors takes a value, so it consumes the token after it exactly as
// --profile and --store do, and the command stays the first non-flag token.
func TestEvalPassesItsFlagsThrough(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "--profile", "essays", "--distractors", "/others"},
		{"eval", "--distractors=/others", "--profile=essays"},
		{"--distractors", "/others", "eval", "--profile", "essays"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			service := &fakeService{evalResult: shippableEval()}
			if got := runWith(t, service, args...); got.code != 0 {
				t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
			}
			if service.evalRequest.DistractorRoot != "/others" {
				t.Errorf("distractor root = %q", service.evalRequest.DistractorRoot)
			}
			if service.evalRequest.Register != "essays" {
				t.Errorf("register = %q", service.evalRequest.Register)
			}
		})
	}
}

// Omitting --distractors is a declared outcome and not a usage error: ADR 0005
// says eval reports uncalibrated without them. So the command runs.
func TestEvalWithoutDistractorsStillRuns(t *testing.T) {
	service := &fakeService{evalResult: uncalibratedEval()}
	got := runWith(t, service, "eval", "--profile", "essays")
	if got.code != 1 {
		t.Fatalf("code = %d, want 1 (stderr %q)", got.code, got.stderr)
	}
	if service.calls != 1 {
		t.Errorf("the service was called %d times", service.calls)
	}
	if service.evalRequest.DistractorRoot != "" {
		t.Errorf("invented a distractor root %q", service.evalRequest.DistractorRoot)
	}
}

// eval has no operand, so the directory it searches from is the process's own,
// exactly as profile's is.
func TestEvalSearchesFromTheWorkingDirectory(t *testing.T) {
	service := &fakeService{evalResult: shippableEval()}
	var out, errOut bytes.Buffer
	cli.Run(context.Background(), []string{"eval", "--profile", "essays"}, cli.Deps{
		Stdout: &out, Stderr: &errOut,
		Env:      func(string) (string, bool) { return "", false },
		ReadFile: func(string) ([]byte, error) { return nil, errNotUsed{} },
		Getwd:    func() (string, error) { return "/where/the/user/is", nil },
		Service:  service,
	})
	if service.evalRequest.StartDir != "/where/the/user/is" {
		t.Errorf("start dir = %q", service.evalRequest.StartDir)
	}
}

func TestEvalTakesNoOperand(t *testing.T) {
	if got := runWith(t, &fakeService{}, "eval", "--profile", "essays", "draft.md"); got.code != 2 {
		t.Errorf("code = %d, want 2", got.code)
	}
}

func TestEvalRejectsAFlagWithNoValue(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "--distractors"},
		{"eval", "--profile", "essays", "--distractors="},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if got := runWith(t, &fakeService{}, args...); got.code != 2 {
				t.Errorf("code = %d, want 2", got.code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Exit codes
// ---------------------------------------------------------------------------

// A measurement that happened is 0 or 1 and never 4. DESIGN is explicit that an
// uncalibrated profile makes eval exit 1 rather than refuse: the measurement
// exists and is adverse. What eval refuses is having no profile to measure.
func TestEvalExitCodes(t *testing.T) {
	for _, c := range []struct {
		name   string
		result workflow.EvalResult
		err    error
		code   int
		status cli.Status
		reason cli.Reason
	}{
		{name: "shippable", result: shippableEval(), code: 0, status: cli.StatusOK},
		{name: "uncalibrated", result: uncalibratedEval(), code: 1, status: cli.StatusAdverse},
		{
			name: "discrimination below the floor", code: 1, status: cli.StatusAdverse,
			result: adverseEval("discrimination-failed"),
		},
		{
			name:   "no profile to measure",
			result: workflow.EvalResult{Selection: workflow.SelectionNoProfile, StorePath: "/w/.hapax/hapax.sqlite3"},
			code:   4, status: cli.StatusRefused, reason: cli.ReasonNoProfile,
		},
		{name: "operational failure", result: workflow.EvalResult{}, err: errNotUsed{}, code: 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runWith(t, &fakeService{evalResult: c.result, err: c.err},
				"--json", "eval", "--profile", "essays", "--distractors", "/others")
			if got.code != c.code {
				t.Fatalf("code = %d, want %d (stderr %q)", got.code, c.code, got.stderr)
			}
			if c.code == 3 {
				if got.stdout != "" {
					t.Errorf("a failed command emitted a document: %q", got.stdout)
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
// The payload
// ---------------------------------------------------------------------------

// The whole answer, by value. A calibration report whose numbers were dropped
// would still look like a report.
func TestTheEvalPayloadCarriesTheWholeAnswer(t *testing.T) {
	result := shippableEval()
	got := runWith(t, &fakeService{evalResult: result},
		"--json", "eval", "--profile", "essays", "--distractors", "/others")
	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}

	var payload struct {
		Store              string  `json:"store"`
		ReleaseID          *string `json:"release_id"`
		ProfileID          *string `json:"profile_id"`
		ReferenceID        *string `json:"reference_id"`
		DistractorPoolID   *string `json:"distractor_pool_id"`
		DistractorMembers  int     `json:"distractor_members"`
		AuthorSegments     int     `json:"author_segments"`
		DistractorSegments int     `json:"distractor_segments"`
		Split              string  `json:"split"`
		Shippable          bool    `json:"shippable"`
		Reason             string  `json:"reason"`
		Discrimination     struct {
			AUC        float64 `json:"auc"`
			LowerBound float64 `json:"lower_bound"`
			Floor      float64 `json:"floor"`
			Passes     bool    `json:"passes"`
			Reason     string  `json:"reason"`
		} `json:"discrimination"`
		Calibration struct {
			Calibrated bool   `json:"calibrated"`
			Reason     string `json:"reason"`
			Bands      []struct {
				Band      string  `json:"band"`
				Claims    string  `json:"claims"`
				Target    float64 `json:"target"`
				ErrorRate float64 `json:"error_rate"`
				Emitted   bool    `json:"emitted"`
				Reason    string  `json:"reason"`
			} `json:"bands"`
		} `json:"calibration"`
	}
	if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
		t.Fatalf("decoding result: %v", err)
	}

	if payload.Store != result.StorePath {
		t.Errorf("store = %q", payload.Store)
	}
	for _, member := range []struct {
		name   string
		got    *string
		wanted string
	}{
		{"release_id", payload.ReleaseID, result.ReleaseID},
		{"profile_id", payload.ProfileID, result.ProfileID},
		{"reference_id", payload.ReferenceID, result.ReferenceID},
		{"distractor_pool_id", payload.DistractorPoolID, result.DistractorPoolID},
	} {
		if member.got == nil || *member.got != member.wanted {
			t.Errorf("%s = %v, want %q", member.name, member.got, member.wanted)
		}
	}
	if payload.DistractorMembers != result.DistractorMembers {
		t.Errorf("distractor_members = %d, want %d", payload.DistractorMembers, result.DistractorMembers)
	}
	if payload.AuthorSegments != result.AuthorSegments || payload.DistractorSegments != result.DistractorSegments {
		t.Errorf("segment counts %d/%d, want %d/%d", payload.AuthorSegments, payload.DistractorSegments,
			result.AuthorSegments, result.DistractorSegments)
	}
	if !payload.Shippable {
		t.Error("shippable = false for a shippable release")
	}
	if payload.Discrimination.AUC != result.Discrimination.AUC ||
		payload.Discrimination.LowerBound != result.Discrimination.LowerBound ||
		payload.Discrimination.Floor != result.Discrimination.Floor ||
		!payload.Discrimination.Passes {
		t.Errorf("the discrimination figures did not survive: %+v", payload.Discrimination)
	}
	if !payload.Calibration.Calibrated {
		t.Error("calibrated = false")
	}
	// Every band, including the ones that were not emitted — a band that failed
	// its test and collapsed to drifting is the most useful line in the report.
	if len(payload.Calibration.Bands) != len(result.Calibration.Bands) {
		t.Fatalf("%d bands in the document, %d reported", len(payload.Calibration.Bands), len(result.Calibration.Bands))
	}
	for i, band := range payload.Calibration.Bands {
		want := result.Calibration.Bands[i]
		if band.Band != want.Band || band.Claims != want.Claims || band.Target != want.Target ||
			band.ErrorRate != want.ErrorRate || band.Emitted != want.Emitted || band.Reason != want.Reason {
			t.Errorf("band %d did not survive: %+v, want %+v", i, band, want)
		}
	}
}

// An adverse evaluation carries its evidence: a document that reported the
// right status while dropping the figures would still exit 1 and still look
// like a report.
func TestAnAdverseEvalPayloadCarriesWhyItFailed(t *testing.T) {
	result := adverseEval("discrimination-failed")
	got := runWith(t, &fakeService{evalResult: result},
		"--json", "eval", "--profile", "essays", "--distractors", "/others")
	if got.code != 1 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	var payload struct {
		Shippable      bool   `json:"shippable"`
		Reason         string `json:"reason"`
		Discrimination struct {
			LowerBound float64 `json:"lower_bound"`
			Floor      float64 `json:"floor"`
			Passes     bool    `json:"passes"`
			Reason     string  `json:"reason"`
		} `json:"discrimination"`
		Calibration struct {
			Calibrated bool `json:"calibrated"`
		} `json:"calibration"`
	}
	if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if payload.Shippable {
		t.Error("shippable = true on an adverse evaluation")
	}
	if payload.Reason != result.Reason {
		t.Errorf("reason = %q, want %q", payload.Reason, result.Reason)
	}
	if payload.Discrimination.Passes {
		t.Error("the gate that failed reports it passed")
	}
	if payload.Discrimination.LowerBound != result.Discrimination.LowerBound ||
		payload.Discrimination.Floor != result.Discrimination.Floor ||
		payload.Discrimination.Reason != result.Discrimination.Reason {
		t.Errorf("the figures that explain the failure did not survive: %+v", payload.Discrimination)
	}
	if payload.Calibration.Calibrated {
		t.Error("calibrated = true on an evaluation that did not calibrate")
	}
}

// A refusal names the store it looked in and nothing it did not measure. The
// danger here is a refusal that renders a successful-looking payload.
func TestARefusedEvalPayloadClaimsNothing(t *testing.T) {
	got := runWith(t, &fakeService{evalResult: workflow.EvalResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectionNoProfile,
	}}, "--json", "eval", "--profile", "essays", "--distractors", "/others")
	if got.code != 4 {
		t.Fatalf("code = %d, want 4", got.code)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	for _, name := range []string{"release_id", "profile_id", "reference_id", "distractor_pool_id"} {
		value, present := payload[name]
		if !present {
			t.Errorf("%s is not emitted at all", name)
			continue
		}
		if string(value) != "null" {
			t.Errorf("%s = %s where nothing was measured, want null", name, value)
		}
	}
	if shippable, present := payload["shippable"]; !present || string(shippable) != "false" {
		t.Errorf("shippable = %s on a refusal", shippable)
	}
	for _, name := range []string{"author_segments", "distractor_segments", "distractor_members"} {
		if value, present := payload[name]; !present || string(value) != "0" {
			t.Errorf("%s = %s where nothing was measured, want 0", name, value)
		}
	}
	// The nested reports are where a successful-looking payload would hide: a
	// discrimination object full of zeroes reads as a measurement that happened
	// and found nothing, which is a different claim from not having measured.
	for _, name := range []string{"discrimination", "calibration"} {
		value, present := payload[name]
		if !present {
			t.Errorf("%s is not emitted at all", name)
			continue
		}
		if string(value) != "null" {
			t.Errorf("%s = %s where nothing was measured, want null", name, value)
		}
	}
}

// An uncalibrated evaluation has no pool and no release to name, and says so
// with null rather than an empty string that reads like an identity.
func TestAnUncalibratedEvalNamesNoPool(t *testing.T) {
	got := runWith(t, &fakeService{evalResult: uncalibratedEval()},
		"--json", "eval", "--profile", "essays")
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	for _, name := range []string{"distractor_pool_id", "release_id"} {
		value, present := payload[name]
		if !present {
			t.Errorf("%s is not emitted at all", name)
			continue
		}
		if string(value) != "null" {
			t.Errorf("%s = %s with nothing calibrated, want null", name, value)
		}
	}
}

// The human line says whether it shipped and why not, because that is the whole
// question being asked.
func TestTheHumanEvalRenderingSaysWhetherItShipped(t *testing.T) {
	adverse := runWith(t, &fakeService{evalResult: adverseEval("discrimination-failed")},
		"eval", "--profile", "essays", "--distractors", "/others")
	for _, wanted := range []string{"eval adverse", "shippable=false", "reason=discrimination-failed"} {
		if !strings.Contains(adverse.stdout, wanted) {
			t.Errorf("the rendering does not say %q: %q", wanted, adverse.stdout)
		}
	}

	shipped := runWith(t, &fakeService{evalResult: shippableEval()},
		"eval", "--profile", "essays", "--distractors", "/others")
	if !strings.Contains(shipped.stdout, "shippable=true") {
		t.Errorf("the rendering does not say it shipped: %q", shipped.stdout)
	}
	if strings.Contains(shipped.stdout, "reason=") {
		t.Errorf("a shippable release renders an empty reason: %q", shipped.stdout)
	}
}

// Every case here is a document the REAL workflow can produce, rendered through
// the real Document.valid. The fake means the CLI tests never see the shapes the
// workflow actually emits, and running the binary found one: an uncalibrated
// evaluation was refused by its own validator with "incoherent eval result" at
// exit 3. A payload the producer can build and the renderer rejects is a
// contract disagreeing with itself.
func TestEveryShapeTheWorkflowProducesRenders(t *testing.T) {
	for name, result := range map[string]workflow.EvalResult{
		"shippable":               shippableEval(),
		"uncalibrated":            uncalibratedEval(),
		"discrimination failed":   adverseEval("discrimination-failed"),
		"no reference to measure": noReferenceEval(),
		"no profile at all": {
			StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectionNoProfile,
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, asJSON := range []bool{true, false} {
				got := runWith(t, &fakeService{evalResult: result}, renderArgs(asJSON)...)
				if got.code == 3 {
					t.Errorf("json=%v: refused its own producer's result: %q", asJSON, got.stderr)
				}
				if got.stdout == "" {
					t.Errorf("json=%v: emitted no document", asJSON)
				}
			}
		})
	}
}

func renderArgs(asJSON bool) []string {
	args := []string{"eval", "--profile", "essays", "--distractors", "/others"}
	if asJSON {
		return append([]string{"--json"}, args...)
	}
	return args
}

// noReferenceEval is index having kept a profile it could not build a reference
// for: a state the design names and the binary hit.
func noReferenceEval() workflow.EvalResult {
	return workflow.EvalResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedExplicit,
		ProfileID: "pro", Adverse: true, Reason: "no-reference",
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// shippableEval is a release that passed both gates. Distinct values throughout,
// because a figure seeded and never asserted is a figure that can be dropped.
func shippableEval() workflow.EvalResult {
	return workflow.EvalResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedExplicit,
		ReleaseID: "rel", ProfileID: "pro", ReferenceID: "ref",
		DistractorPoolID: "pool", DistractorMembers: 20,
		AuthorSegments: 30, DistractorSegments: 120, Split: "test",
		Shippable: true,
		Discrimination: workflow.DiscriminationReport{
			AUC: 0.91, LowerBound: 0.84, Floor: 0.80, Passes: true,
		},
		Calibration: workflow.CalibrationReport{
			Calibrated: true,
			Bands: []workflow.BandReport{
				// Claims is the error class the band's threshold BOUNDS, not
				// the class it labels: in-range bounds the distractor rate,
				// because its threshold was built to respect that target.
				// Crossing them renders inverted evidence that still looks like
				// a report.
				{Band: "in-range", Claims: "distractor", Target: 0.10, ErrorRate: 0.07, Emitted: true},
				{Band: "not-you", Claims: "author", Target: 0.05, ErrorRate: 0.03, Emitted: true},
				// The interesting line: a band that failed its test, was not
				// emitted, and collapsed to drifting.
				{Band: "drifting", Claims: "", Target: 0.05, ErrorRate: 0.22, Emitted: false,
					Reason: "error-bound-exceeds-target"},
			},
		},
	}
}

// uncalibratedEval is what no distractors produces: a completed measurement
// that could not calibrate, naming no pool and no release.
func uncalibratedEval() workflow.EvalResult {
	return workflow.EvalResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedExplicit,
		ProfileID: "pro", ReferenceID: "ref",
		Adverse: true, Reason: "uncalibrated",
	}
}

func adverseEval(reason string) workflow.EvalResult {
	result := shippableEval()
	result.Shippable, result.Adverse, result.Reason = false, true, reason
	result.Discrimination.Passes = false
	result.Discrimination.LowerBound, result.Discrimination.Reason = 0.62, "lower-bound-below-floor"
	result.Calibration.Calibrated = false
	return result
}
