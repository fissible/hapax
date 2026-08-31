package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// Grammar
// ---------------------------------------------------------------------------

// score takes exactly one draft, and finds its store the way profile and eval
// do rather than being told where it is.
func TestScoreTakesOneDraftAndFindsItsStore(t *testing.T) {
	service := &fakeService{scoreResult: cleanScore()}
	got := runWith(t, service, "score", "draft.md")
	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	if service.scoreRequest.Path != "draft.md" {
		t.Errorf("path = %q", service.scoreRequest.Path)
	}
	if service.scoreRequest.StorePath != "" {
		t.Errorf("store path = %q; an unspecified store is the workflow's to find", service.scoreRequest.StorePath)
	}
	if service.scoreRequest.StartDir == "" {
		t.Error("the working directory did not reach the request")
	}
}

func TestScoreRequiresExactlyOneDraft(t *testing.T) {
	for _, args := range [][]string{
		{"score"},
		{"score", "one.md", "two.md"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			service := &fakeService{scoreResult: cleanScore()}
			if got := runWith(t, service, args...); got.code != 2 {
				t.Errorf("code = %d, want 2", got.code)
			}
			if service.calls != 0 {
				t.Error("the service ran for an invocation that could not be valid")
			}
		})
	}
}

func TestScorePassesItsFlagsThrough(t *testing.T) {
	service := &fakeService{scoreResult: cleanScore()}
	if got := runWith(t, service, "score", "--profile", "letters", "--store", "/tmp/x.db", "draft.md"); got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	if service.scoreRequest.Register != "letters" || service.scoreRequest.StorePath != "/tmp/x.db" {
		t.Errorf("request = %+v", service.scoreRequest)
	}
}

// ---------------------------------------------------------------------------
// Exit codes
// ---------------------------------------------------------------------------

// score has no LLM and no network, so no provider refusal can arise here. What
// it can do is measure and find nothing adverse, measure and find something,
// refuse a precondition, or fail.
func TestScoreExitCodes(t *testing.T) {
	for _, c := range []struct {
		name   string
		result workflow.ScoreResult
		err    error
		code   int
		status cli.Status
		reason cli.Reason
	}{
		{name: "nothing adverse", result: cleanScore(), code: 0, status: cli.StatusOK},
		{name: "a drifting segment", result: adverseScore("drifting"), code: 1, status: cli.StatusAdverse},
		{name: "a not-you segment", result: adverseScore("not-you"), code: 1, status: cli.StatusAdverse},
		{
			name: "no profile", code: 4, status: cli.StatusRefused, reason: cli.ReasonNoProfile,
			result: refusedScore(workflow.RefusalNoProfile),
		},
		{
			name: "uncalibrated", code: 4, status: cli.StatusRefused, reason: cli.ReasonUncalibrated,
			result: uncalibratedScore(),
		},
		{
			name: "nothing to transform against", code: 4, status: cli.StatusRefused,
			reason: cli.ReasonNoReference, result: refusedScore(workflow.RefusalNoReference),
		},
		{
			name: "a reference nothing designates", code: 4, status: cli.StatusRefused,
			reason: cli.ReasonAmbiguousReference, result: refusedScore(workflow.RefusalAmbiguousReference),
		},
		{
			name: "nothing above the floor", code: 4, status: cli.StatusRefused,
			reason: cli.ReasonInsufficientEvidence, result: refusedScore(workflow.RefusalInsufficientEvidence),
		},
		{name: "a draft that cannot be read", result: workflow.ScoreResult{}, err: errNotUsed{}, code: 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runWith(t, &fakeService{scoreResult: c.result, err: c.err}, "--json", "score", "draft.md")
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

// The per-segment measurement is the whole answer. There is deliberately no
// aggregate distance: it has no statistical meaning here and would let one
// not-you paragraph average away against a document of clean ones.
func TestTheScorePayloadIsPerSegmentWithNoAggregate(t *testing.T) {
	result := cleanScore()
	got := runWith(t, &fakeService{scoreResult: result}, "--json", "score", "draft.md")

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	// The EXACT key set, not a denylist. A denylist of aggregate-sounding names
	// is routed around by the next one nobody thought of — score_summary,
	// worst_distance — and the allowed shape is the actual contract.
	want := map[string]bool{
		"path": true, "store": true, "profile_id": true, "reference_id": true,
		"release_id": true, "calibrated": true, "paragraphs_below_floor": true,
		"segments": true,
	}
	for name := range payload {
		if !want[name] {
			t.Errorf("the document carries a document-level %q; scoring is per segment "+
				"and there is deliberately no aggregate — one not-you paragraph must not "+
				"average away against a document of clean ones", name)
		}
	}
	for name := range want {
		if _, present := payload[name]; !present {
			t.Errorf("the document has no %q", name)
		}
	}
}

// Every figure by value, over a clean segment and an adverse one, because a
// payload that dropped the numbers would still look like a score.
func TestTheScorePayloadCarriesTheWholeAnswer(t *testing.T) {
	for _, c := range []struct {
		name   string
		result workflow.ScoreResult
		band   string
	}{
		{"clean", cleanScore(), "in-range"},
		{"adverse", adverseScore("not-you"), "not-you"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runWith(t, &fakeService{scoreResult: c.result}, "--json", "score", "draft.md")
			var payload struct {
				Path     string `json:"path"`
				Segments []struct {
					Index         int `json:"index"`
					LexicalTokens int `json:"lexical_tokens"`
					Distance      struct {
						Value   float64 `json:"value"`
						Defined bool    `json:"defined"`
						Reason  string  `json:"reason"`
						Partial bool    `json:"partial"`
					} `json:"distance"`
					Band struct {
						Band     string  `json:"band"`
						Defined  bool    `json:"defined"`
						Reason   string  `json:"reason"`
						Distance float64 `json:"distance"`
					} `json:"band"`
					Features []struct {
						Feature   string  `json:"feature"`
						Deviation float64 `json:"deviation"`
						Defined   bool    `json:"defined"`
						Direction string  `json:"direction"`
					} `json:"features"`
				} `json:"segments"`
			}
			if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
				t.Fatalf("decoding result: %v", err)
			}
			if payload.Path != c.result.Path || payload.Store != c.result.StorePath {
				t.Errorf("path = %q store = %q", payload.Path, payload.Store)
			}
			// Provenance by value. A projection substituting one identity for
			// another passes every other assertion here — the numbers are
			// right and they are about the wrong artifacts.
			for _, member := range []struct {
				name   string
				got    *string
				wanted string
			}{
				{"profile_id", payload.ProfileID, c.result.ProfileID},
				{"reference_id", payload.ReferenceID, c.result.ReferenceID},
				{"release_id", payload.ReleaseID, c.result.ReleaseID},
			} {
				if member.got == nil || *member.got != member.wanted {
					t.Errorf("%s = %v, want %q", member.name, member.got, member.wanted)
				}
			}
			if payload.Calibrated != c.result.Calibrated ||
				payload.ParagraphsBelowFloor != c.result.ParagraphsBelowFloor {
				t.Errorf("calibrated=%v below-floor=%d", payload.Calibrated, payload.ParagraphsBelowFloor)
			}
			if len(payload.Segments) != len(c.result.Segments) {
				t.Fatalf("%d segments in the document, %d measured", len(payload.Segments), len(c.result.Segments))
			}
			first, want := payload.Segments[0], c.result.Segments[0]
			if first.Index != want.Index || first.LexicalTokens != want.LexicalTokens {
				t.Errorf("segment identity did not survive: %+v", first)
			}
			if first.Distance.Value != want.Distance.Value || !first.Distance.Defined {
				t.Errorf("the distance did not survive: %+v", first.Distance)
			}
			if first.Band.Band != c.band || !first.Band.Defined {
				t.Errorf("band = %q defined=%v, want %q", first.Band.Band, first.Band.Defined, c.band)
			}
			if len(first.Features) != len(want.Features) {
				t.Fatalf("%d deltas in the document, %d measured", len(first.Features), len(want.Features))
			}
			delta := first.Features[0]
			if delta.Feature != want.Features[0].Feature || delta.Deviation != want.Features[0].Deviation ||
				delta.Direction != want.Features[0].Direction {
				t.Errorf("a delta did not survive: %+v", delta)
			}
		})
	}
}

// An uncalibrated refusal keeps its measurement and drops only the band. That is
// the contract ADR 0005 and DESIGN agree on, and it is the reason score refuses
// with a payload rather than with nothing.
func TestAnUncalibratedRefusalKeepsWhatItMeasured(t *testing.T) {
	got := runWith(t, &fakeService{scoreResult: uncalibratedScore()}, "--json", "score", "draft.md")
	if got.code != 4 {
		t.Fatalf("code = %d, want 4", got.code)
	}
	var payload struct {
		Calibrated bool    `json:"calibrated"`
		ReleaseID  *string `json:"release_id"`
		Segments   []struct {
			Distance struct {
				Value   float64 `json:"value"`
				Defined bool    `json:"defined"`
			} `json:"distance"`
			Band struct {
				Band    string `json:"band"`
				Defined bool   `json:"defined"`
				Reason  string `json:"reason"`
			} `json:"band"`
			Features []struct {
				Feature string `json:"feature"`
			} `json:"features"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if payload.Calibrated {
		t.Error("calibrated = true on an uncalibrated refusal")
	}
	if payload.ReleaseID != nil {
		t.Errorf("release_id = %v; an absent head is not a release", *payload.ReleaseID)
	}
	if len(payload.Segments) == 0 {
		t.Fatal("refused and measured nothing; the payload is the point")
	}
	for i, segment := range payload.Segments {
		if !segment.Distance.Defined {
			continue
		}
		if segment.Band.Defined || segment.Band.Band != "" {
			t.Errorf("segment %d was banded with nothing calibrated: %+v", i, segment.Band)
		}
		// Exactly uncalibrated. "Not-calibrated", or a reason replaced on the
		// way through the projection, reads the same to a struct decode and
		// differently to anyone reading the output.
		if segment.Band.Reason != "uncalibrated" {
			t.Errorf("segment %d has no band and gives reason %q, want %q",
				i, segment.Band.Reason, "uncalibrated")
		}
		if len(segment.Features) == 0 {
			t.Errorf("segment %d kept a distance and dropped its deltas", i)
		}
	}
}

// A refusal that measured nothing claims nothing.
func TestARefusalBeforeMeasurementClaimsNothing(t *testing.T) {
	got := runWith(t, &fakeService{scoreResult: refusedScore(workflow.RefusalNoProfile)},
		"--json", "score", "draft.md")
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(decode(t, got.stdout).Result, &payload); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	for _, name := range []string{"profile_id", "reference_id", "release_id"} {
		if value, present := payload[name]; !present || string(value) != "null" {
			t.Errorf("%s = %s where nothing was resolved, want null", name, value)
		}
	}
	if segments, present := payload["segments"]; !present || string(segments) != "null" {
		t.Errorf("segments = %s where nothing was measured", segments)
	}
}

// Every shape the workflow can return renders through the real validator. A2b's
// uncalibrated eval was refused by its own producer's validator, and the fake is
// what let that reach the binary.
func TestEveryScoreShapeTheWorkflowProducesRenders(t *testing.T) {
	for name, result := range map[string]workflow.ScoreResult{
		"clean":                 cleanScore(),
		"drifting":              adverseScore("drifting"),
		"not-you":               adverseScore("not-you"),
		"uncalibrated":          uncalibratedScore(),
		"no profile":            refusedScore(workflow.RefusalNoProfile),
		"no reference":          refusedScore(workflow.RefusalNoReference),
		"ambiguous reference":   refusedScore(workflow.RefusalAmbiguousReference),
		"insufficient evidence": refusedScore(workflow.RefusalInsufficientEvidence),
	} {
		t.Run(name, func(t *testing.T) {
			for _, asJSON := range []bool{true, false} {
				args := []string{"score", "draft.md"}
				if asJSON {
					args = append([]string{"--json"}, args...)
				}
				got := runWith(t, &fakeService{scoreResult: result}, args...)
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

// The human line says what a person asked: which bands came back, and how many
// paragraphs were not measured at all.
func TestTheHumanScoreRenderingSaysWhatCameBack(t *testing.T) {
	got := runWith(t, &fakeService{scoreResult: adverseScore("not-you")}, "score", "draft.md")
	for _, wanted := range []string{"score adverse", "draft.md", "not-you"} {
		if !strings.Contains(got.stdout, wanted) {
			t.Errorf("the rendering does not say %q: %q", wanted, got.stdout)
		}
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func scoredSegment(index int, band string, distance, deviation float64, direction string) workflow.ScoredSegment {
	return workflow.ScoredSegment{
		Index: index, LexicalTokens: 40 + index,
		Distance: workflow.MeasuredDistance{Value: distance, Defined: true},
		Band:     workflow.BandOutcome{Band: band, Defined: band != "", Distance: distance},
		Features: []workflow.FeatureDelta{
			{Feature: "function-word-rate", Deviation: deviation, Defined: true, Direction: direction},
			{Feature: "comma-density", Deviation: deviation / 2, Defined: true, Direction: direction},
		},
	}
}

func cleanScore() workflow.ScoreResult {
	return workflow.ScoreResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Path: "draft.md",
		ProfileID: "pro", ReferenceID: "ref", ReleaseID: "rel", Calibrated: true,
		ParagraphsBelowFloor: 1,
		Segments: []workflow.ScoredSegment{
			scoredSegment(0, "in-range", 0.31, -0.22, "below"),
			scoredSegment(1, "in-range", 0.44, 0.18, "above"),
		},
	}
}

func adverseScore(band string) workflow.ScoreResult {
	result := cleanScore()
	result.Adverse = true
	result.Segments[0] = scoredSegment(0, band, 1.17, 1.41, "above")
	return result
}

// uncalibratedScore kept its measurement and dropped only the band, which is
// what refusing the classification rather than the answer looks like.
func uncalibratedScore() workflow.ScoreResult {
	result := cleanScore()
	result.Calibrated, result.ReleaseID = false, ""
	result.Refusal = workflow.RefusalUncalibrated
	for i := range result.Segments {
		result.Segments[i].Band = workflow.BandOutcome{
			Distance: result.Segments[i].Distance.Value, Reason: "uncalibrated",
		}
	}
	return result
}

// refusedScore is a refusal that never got as far as measuring.
func refusedScore(refusal string) workflow.ScoreResult {
	return workflow.ScoreResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Path: "draft.md", Refusal: refusal,
	}
}

// Rendering every shape the workflow produces proves acceptance. This proves
// refusal, which is the half that stops a payload saying something no run could
// have produced.
func TestRenderRefusesAScoreDocumentNoRunProduces(t *testing.T) {
	for _, c := range []struct {
		name     string
		document func() cli.Document
	}{
		{"calibrated with no release", func() cli.Document {
			d := scoreDocument(cleanScore())
			result := d.Result.(cli.ScoreResult)
			result.ReleaseID = nil
			d.Result = result
			return d
		}},
		{"a release on an uncalibrated score", func() cli.Document {
			d := scoreDocument(uncalibratedScore())
			result := d.Result.(cli.ScoreResult)
			id := "rel"
			result.ReleaseID = &id
			d.Result = result
			return d
		}},
		{"a band defined with nothing calibrated", func() cli.Document {
			d := scoreDocument(uncalibratedScore())
			result := d.Result.(cli.ScoreResult)
			result.Segments[0].Band.Defined = true
			result.Segments[0].Band.Band = "in-range"
			d.Result = result
			return d
		}},
		{"a refusal that measured something anyway", func() cli.Document {
			d := scoreDocument(refusedScore(workflow.RefusalNoProfile))
			banded := scoreDocument(cleanScore()).Result.(cli.ScoreResult)
			result := d.Result.(cli.ScoreResult)
			result.Segments = banded.Segments
			d.Result = result
			return d
		}},
		{"insufficient evidence that measured something", func() cli.Document {
			d := scoreDocument(refusedScore(workflow.RefusalInsufficientEvidence))
			banded := scoreDocument(cleanScore()).Result.(cli.ScoreResult)
			result := d.Result.(cli.ScoreResult)
			result.Segments, result.ParagraphsBelowFloor = banded.Segments, 2
			d.Result = result
			return d
		}},
		{"a band outside the vocabulary", func() cli.Document {
			d := scoreDocument(cleanScore())
			result := d.Result.(cli.ScoreResult)
			result.Segments[0].Band.Band = "somewhat-you"
			d.Result = result
			return d
		}},
		{"a delta pointing nowhere", func() cli.Document {
			d := scoreDocument(cleanScore())
			result := d.Result.(cli.ScoreResult)
			result.Segments[0].Features[0].Direction = "sideways"
			d.Result = result
			return d
		}},
		{"an adverse score whose every band is in range", func() cli.Document {
			d := scoreDocument(cleanScore())
			d.Status = cli.StatusAdverse
			return d
		}},
		{"a clean score carrying a not-you segment", func() cli.Document {
			d := scoreDocument(adverseScore("not-you"))
			d.Status = cli.StatusOK
			return d
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, asJSON := range []bool{true, false} {
				var out bytes.Buffer
				if err := c.document().Render(&out, asJSON); err == nil {
					t.Errorf("json=%v: rendered %q", asJSON, out.String())
				} else if out.Len() != 0 {
					t.Errorf("json=%v: refused but still wrote %q", asJSON, out.String())
				}
			}
		})
	}
}

// And the documents those are built from render, or the refusals above would
// prove only that the fixtures are broken.
func TestTheCoherentScoreDocumentsRender(t *testing.T) {
	for name, result := range map[string]workflow.ScoreResult{
		"clean":        cleanScore(),
		"adverse":      adverseScore("not-you"),
		"uncalibrated": uncalibratedScore(),
		"refused":      refusedScore(workflow.RefusalNoProfile),
	} {
		t.Run(name, func(t *testing.T) {
			for _, asJSON := range []bool{true, false} {
				var out bytes.Buffer
				if err := scoreDocument(result).Render(&out, asJSON); err != nil {
					t.Errorf("json=%v: %v", asJSON, err)
				}
			}
		})
	}
}

// scoreDocument builds the document a score result renders as. Built here
// rather than reached through Run, because these cases mutate it into shapes Run
// cannot produce — which is the point — and exporting a constructor purely so a
// test could call it would be adding API for the test's convenience.
func scoreDocument(result workflow.ScoreResult) cli.Document {
	register := "essays"
	status, reason := cli.StatusOK, cli.Reason("")
	switch {
	case result.Refusal == workflow.RefusalUncalibrated:
		status, reason = cli.StatusRefused, cli.ReasonUncalibrated
	case result.Refusal != "":
		status, reason = cli.StatusRefused, cli.ReasonNoProfile
	case result.Adverse:
		status = cli.StatusAdverse
	}

	payload := cli.ScoreResult{
		Path: result.Path, Store: result.StorePath,
		Calibrated: result.Calibrated, ParagraphsBelowFloor: result.ParagraphsBelowFloor,
	}
	if result.ProfileID != "" {
		payload.ProfileID, payload.ReferenceID = &result.ProfileID, &result.ReferenceID
	}
	if result.ReleaseID != "" {
		payload.ReleaseID = &result.ReleaseID
	}
	for _, segment := range result.Segments {
		converted := cli.ScoredSegment{
			Index: segment.Index, LexicalTokens: segment.LexicalTokens,
			Distance: cli.MeasuredDistance{Value: segment.Distance.Value, Defined: segment.Distance.Defined},
			Band: cli.BandOutcome{
				Band: segment.Band.Band, Defined: segment.Band.Defined,
				Distance: segment.Band.Distance, Reason: segment.Band.Reason,
			},
		}
		for _, delta := range segment.Features {
			converted.Features = append(converted.Features, cli.FeatureDelta{
				Feature: delta.Feature, Deviation: delta.Deviation,
				Defined: delta.Defined, Direction: delta.Direction,
			})
		}
		payload.Segments = append(payload.Segments, converted)
	}

	return cli.Document{
		Schema: cli.Schema, Command: "score", Status: status, Reason: reason,
		Profile: &register, Result: payload,
	}
}
