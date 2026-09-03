package cli_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/cli"
)

func essays() *string { s := "essays"; return &s }

func okDocument() cli.Document {
	return cli.Document{
		Schema: cli.Schema, Command: "tells", Status: cli.StatusOK,
		Result: cli.TellsResult{Path: "draft.md", Screening: "indeterminate", Findings: []cli.TellsFinding{}},
	}
}

func render(t *testing.T, doc cli.Document, asJSON bool) string {
	t.Helper()
	var out bytes.Buffer
	if err := doc.Render(&out, asJSON); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out.String()
}

// The refusal vocabulary is DESIGN's, exactly. A sixth reason cannot be
// invented without amending the document that declares the set.
func TestTheRefusalVocabularyIsTheDeclaredSet(t *testing.T) {
	got := make([]string, 0, len(cli.Reasons()))
	for _, reason := range cli.Reasons() {
		got = append(got, string(reason))
	}
	sort.Strings(got)
	want := []string{
		// A2c added the two a raw score can hit: nothing to transform against,
		// and several references with no release to designate one — #62.
		// B2b-2b added stale-draft, and derives this list from
		// workflow.Refusals() rather than restating it, so the next one cannot
		// be forgotten.
		//
		// #81 added no-such-paragraph: a named paragraph outside the draft's
		// range. It is a refusal rather than a usage error because the
		// paragraph count is not known until the draft has been scored.
		"ambiguous-reference", "insufficient-evidence", "local-only-forbids-provider",
		"no-profile", "no-reference", "no-such-paragraph", "stale-draft",
		"stale-exemplars", "uncalibrated",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reasons =\n%v\nwant\n%v", got, want)
	}
}

func TestTheStatusVocabularyIsThree(t *testing.T) {
	got := make([]string, 0, len(cli.Statuses()))
	for _, status := range cli.Statuses() {
		got = append(got, string(status))
	}
	sort.Strings(got)
	if want := []string{"adverse", "ok", "refused"}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

// The surface says what is built rather than promising the six DESIGN names.
// This list is hand-maintained ON PURPOSE: there is no way to derive "what is
// implemented" from the code, so the only honest source is a slice adding its
// own command here when it lands. It going stale is the test doing its job.
func TestTheCommandSurfaceIsWhatIsImplemented(t *testing.T) {
	want := []string{"eval", "index", "profile", "rewrite", "score", "tells"}
	got := append([]string(nil), cli.Commands()...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
}

// Render refuses to emit a document that cannot mean anything, because a
// malformed envelope on stdout is worse than no envelope at all.
func TestRenderRefusesAnIncoherentDocument(t *testing.T) {
	for _, c := range []struct {
		name  string
		alter func(*cli.Document)
	}{
		{"a schema that is not this one", func(d *cli.Document) { d.Schema = "hapax.v2" }},
		{"an empty schema", func(d *cli.Document) { d.Schema = "" }},
		{"a status outside the set", func(d *cli.Document) { d.Status = "failed" }},
		{"an empty status", func(d *cli.Document) { d.Status = "" }},
		{"a command that is not implemented", func(d *cli.Document) { d.Command = "rewrite" }},
		{"an empty command", func(d *cli.Document) { d.Command = "" }},
		{"a reason on an ok document", func(d *cli.Document) { d.Reason = cli.ReasonUncalibrated }},
		{"a reason on an adverse document", func(d *cli.Document) {
			d.Status, d.Reason = cli.StatusAdverse, cli.ReasonUncalibrated
		}},
		{"a refusal with no reason", func(d *cli.Document) { d.Status = cli.StatusRefused }},
		{"a reason outside the set", func(d *cli.Document) {
			d.Status, d.Reason = cli.StatusRefused, "because-i-said-so"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := okDocument()
			c.alter(&doc)
			for _, asJSON := range []bool{true, false} {
				var out bytes.Buffer
				if err := doc.Render(&out, asJSON); err == nil {
					t.Errorf("json=%v: rendered %q", asJSON, out.String())
				} else if out.Len() != 0 {
					t.Errorf("json=%v: refused but still wrote %q", asJSON, out.String())
				}
			}
		})
	}
}

// Every declared reason is emittable on a refusal. A vocabulary with a member
// no document can carry is a vocabulary with a dead member.
func TestEveryDeclaredReasonCanBeEmitted(t *testing.T) {
	for _, reason := range cli.Reasons() {
		t.Run(string(reason), func(t *testing.T) {
			doc := okDocument()
			doc.Status, doc.Reason = cli.StatusRefused, reason
			var decoded map[string]any
			if err := json.Unmarshal([]byte(render(t, doc, true)), &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if decoded["reason"] != string(reason) {
				t.Errorf("reason = %v, want %q", decoded["reason"], reason)
			}
			if decoded["status"] != "refused" {
				t.Errorf("status = %v, want refused", decoded["status"])
			}
		})
	}
}

// The JSON field names are the contract a script depends on.
func TestTheJSONFieldsAreExactlyTheEnvelope(t *testing.T) {
	doc := okDocument()
	doc.Profile = essays()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(render(t, doc, true)), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := make([]string, 0, len(decoded))
	for name := range decoded {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"command", "profile", "reason", "result", "schema", "status"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields =\n%v\nwant\n%v", got, want)
	}
	if decoded["schema"] != cli.Schema {
		t.Errorf("schema = %v, want %q", decoded["schema"], cli.Schema)
	}
}

// A command that takes no register emits null rather than an empty string, so a
// consumer can tell "no register applies" from "the register is named ”".
func TestAnAbsentProfileIsNullAndNotEmpty(t *testing.T) {
	rendered := render(t, okDocument(), true)
	if !strings.Contains(rendered, `"profile":null`) {
		t.Errorf("profile is not null in %s", rendered)
	}
}

// JSON is canonical and the human form is produced from the same document, so
// the two cannot disagree about what happened.
func TestTheHumanRenderingCarriesWhatTheDocumentSays(t *testing.T) {
	doc := okDocument()
	doc.Status, doc.Reason = cli.StatusRefused, cli.ReasonNoProfile
	doc.Profile = essays()
	human := render(t, doc, false)
	for _, fragment := range []string{"refused", "no-profile", "essays", "tells"} {
		if !strings.Contains(human, fragment) {
			t.Errorf("the human rendering omits %q:\n%s", fragment, human)
		}
	}
	if strings.Contains(human, "{") {
		t.Errorf("the human rendering is JSON:\n%s", human)
	}
}

// One document, one line of JSON, newline-terminated: a caller appending output
// from several invocations gets one object per line.
func TestJSONIsOneNewlineTerminatedLine(t *testing.T) {
	rendered := render(t, okDocument(), true)
	if strings.Count(rendered, "\n") != 1 || !strings.HasSuffix(rendered, "\n") {
		t.Errorf("rendered %q, want exactly one trailing newline", rendered)
	}
}

// The vocabularies a result may carry are the OWNING package's. Render checks
// them, so cli cannot hold a second copy that drifts — there is nothing to
// compare, which is better than a test that compares two lists.
func TestRenderRefusesAResultOutsideTellsVocabularies(t *testing.T) {
	for _, c := range []struct {
		name  string
		alter func(*cli.TellsResult)
	}{
		{"an undeclared screening", func(r *cli.TellsResult) { r.Screening = "half-run" }},
		{"an undeclared severity", func(r *cli.TellsResult) { r.Findings[0].Severity = "catastrophic" }},
		{"an undeclared provenance", func(r *cli.TellsResult) { r.Findings[0].Provenance = "hearsay" }},
		{"an undeclared category", func(r *cli.TellsResult) { r.Findings[0].Category = "vibes" }},
		{"a count that is not the findings", func(r *cli.TellsResult) { r.Count = 7 }},
		{"a negative suppressed count", func(r *cli.TellsResult) { r.Suppressed = -1 }},
		{"a negative offset", func(r *cli.TellsResult) { r.Findings[0].Offset = -1 }},
		{"a zero length", func(r *cli.TellsResult) { r.Findings[0].Length = 0 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			result := cli.TellsResult{
				Path: "draft.md", Screening: "indeterminate", Count: 1,
				Findings: []cli.TellsFinding{{
					Rule: "double-space", Category: "formatting", Provenance: "unvalidated",
					Severity: "warn", Offset: 4, Length: 2,
				}},
			}
			c.alter(&result)
			doc := okDocument()
			doc.Result = result
			for _, asJSON := range []bool{true, false} {
				var out bytes.Buffer
				if err := doc.Render(&out, asJSON); err == nil {
					t.Errorf("json=%v: rendered %q", asJSON, out.String())
				}
			}
		})
	}
}
