package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/cli"
	"github.com/fissible/hapax/internal/workflow"
)

// Render already refuses a document whose envelope cannot mean anything. The two
// payloads A2a added need the same standard for THEIR couplings: what the CLI's
// own code paths happen to respect is not a guarantee while a document can still
// be built that says something no run produces.
//
// Scoped deliberately to the new payloads. A refused tells document still
// renders, and document_test.go requires every reason in the vocabulary to be
// emittable on one — that allowance predates A2a and tightening it would be a
// change to a settled contract for a command this slice does not touch.
func TestRenderRefusesAnIndexOrProfileDocumentNoRunProduces(t *testing.T) {
	for _, c := range []struct {
		name     string
		document func() cli.Document
	}{
		{
			// index has no refusal in its vocabulary: it completes, or it fails
			// without a document at all.
			"an index that refused",
			func() cli.Document {
				d := indexDocument()
				d.Status, d.Reason = cli.StatusRefused, cli.ReasonNoProfile
				return d
			},
		},
		{
			"an index that refused for some other reason",
			func() cli.Document {
				d := indexDocument()
				d.Status, d.Reason = cli.StatusRefused, cli.ReasonUncalibrated
				return d
			},
		},
		// Ambiguous and unknown-register are exit 2 — diagnostics about the
		// invocation, not results. Forbidden REGARDLESS of status and reason,
		// which is the cross-product the first version of this test missed:
		// dressing one as a refusal made it render again.
		{
			"an ambiguous selection reported as a result",
			func() cli.Document { return selectionDocument(workflow.SelectionAmbiguous, cli.StatusOK, "") },
		},
		{
			"an ambiguous selection dressed as a refusal",
			func() cli.Document {
				return selectionDocument(workflow.SelectionAmbiguous, cli.StatusRefused, cli.ReasonNoProfile)
			},
		},
		{
			"an unknown register reported as a result",
			func() cli.Document { return selectionDocument(workflow.SelectionUnknownRegister, cli.StatusOK, "") },
		},
		{
			"an unknown register dressed as a refusal",
			func() cli.Document {
				return selectionDocument(workflow.SelectionUnknownRegister, cli.StatusRefused, cli.ReasonNoProfile)
			},
		},
		{
			"an ambiguous selection called adverse",
			func() cli.Document { return selectionDocument(workflow.SelectionAmbiguous, cli.StatusAdverse, "") },
		},
		{
			"an unknown register called adverse",
			func() cli.Document {
				return selectionDocument(workflow.SelectionUnknownRegister, cli.StatusAdverse, "")
			},
		},
		// The selection and the list it offers are one fact. A refusal that
		// offers registers contradicts itself, and so does a sole head that
		// came from a store holding none or several.
		{
			"a no-profile refusal that offers profiles anyway",
			func() cli.Document {
				d := selectionDocument(workflow.SelectionNoProfile, cli.StatusRefused, cli.ReasonNoProfile)
				result := d.Result.(cli.ProfileResult)
				result.Available = []string{"essays"}
				d.Result = result
				return d
			},
		},
		{
			"a sole head from a store holding several",
			func() cli.Document {
				d := profileDocument()
				result := d.Result.(cli.ProfileResult)
				result.Available = []string{"essays", "letters"}
				d.Result = result
				return d
			},
		},
		{
			"a sole head whose register is not among those available",
			func() cli.Document {
				d := profileDocument()
				result := d.Result.(cli.ProfileResult)
				result.Available = []string{"letters"}
				d.Result = result
				return d
			},
		},
		// Everything above is a sole head. An explicit selection is the other
		// half of the vocabulary and has the same couplings to break.
		{
			"an explicit selection whose register is not among those available",
			func() cli.Document {
				d := explicitDocument()
				result := d.Result.(cli.ProfileResult)
				result.Available = []string{"essays"}
				d.Result = result
				return d
			},
		},
		{
			"an explicit selection the envelope names differently",
			func() cli.Document {
				d := explicitDocument()
				other := "essays"
				d.Profile = &other
				return d
			},
		},
		{
			"an explicit selection the envelope does not name",
			func() cli.Document {
				d := explicitDocument()
				d.Profile = nil
				return d
			},
		},
		// The workflow sorts the registers and they come from a map keyed by
		// register, so neither an unsorted list nor a repeated one is reachable.
		{
			"available profiles out of order",
			func() cli.Document {
				d := explicitDocument()
				result := d.Result.(cli.ProfileResult)
				result.Available = []string{"letters", "essays"}
				d.Result = result
				return d
			},
		},
		{
			"the same register offered twice",
			func() cli.Document {
				d := explicitDocument()
				result := d.Result.(cli.ProfileResult)
				result.Available = []string{"essays", "essays", "letters"}
				d.Result = result
				return d
			},
		},
		{
			// index cannot run without --profile, so a document from one always
			// names the register it was given.
			"an index that names no profile",
			func() cli.Document {
				d := indexDocument()
				d.Profile = nil
				return d
			},
		},
		{
			// An adverse result labelled ok. Already refused before this file,
			// kept as a lock on a coupling nothing else states.
			"an adverse index calling itself ok",
			func() cli.Document {
				d := indexDocument()
				result := d.Result.(cli.IndexResult)
				result.Mode, result.Adversity = workflow.IndexSnapshotOnly, workflow.AdversityCorpusTooSmall
				result.ProfileID, result.ReferenceID = nil, nil
				d.Result = result
				return d
			},
		},
		// A register is never empty: the flag requires a value and a head is
		// keyed by one.
		{
			"an index naming an empty register",
			func() cli.Document {
				d := indexDocument()
				empty := ""
				d.Profile = &empty
				return d
			},
		},
		{
			"a selection naming an empty register",
			func() cli.Document {
				d := explicitDocument()
				empty := ""
				d.Profile = &empty
				result := d.Result.(cli.ProfileResult)
				result.Profile.Register = ""
				d.Result = result
				return d
			},
		},
		{
			// The validator keys on the payload profile's ID, so a projection
			// that filled in the rest of it while leaving the identity blank
			// gets through — a document naming a profile that has none.
			"a refusal carrying half a profile",
			func() cli.Document {
				d := selectionDocument(workflow.SelectionNoProfile, cli.StatusRefused, cli.ReasonNoProfile)
				result := d.Result.(cli.ProfileResult)
				result.Profile.Register, result.Profile.NotReadyReason = "essays", "declared"
				d.Result = result
				return d
			},
		},
		{
			"an empty register among those available",
			func() cli.Document {
				d := explicitDocument()
				result := d.Result.(cli.ProfileResult)
				result.Available = []string{"", "essays", "letters"}
				d.Result = result
				return d
			},
		},
		// The envelope names the profile, so it cannot name a different one
		// from the payload, or none where one was resolved.
		{
			"an envelope naming a different profile from the payload",
			func() cli.Document {
				d := profileDocument()
				other := "letters"
				d.Profile = &other
				return d
			},
		},
		{
			"an envelope naming nothing where one was resolved",
			func() cli.Document {
				d := profileDocument()
				d.Profile = nil
				return d
			},
		},
		{
			"a refusal naming a profile it did not resolve",
			func() cli.Document {
				d := selectionDocument(workflow.SelectionNoProfile, cli.StatusRefused, cli.ReasonNoProfile)
				named := "essays"
				d.Profile = &named
				return d
			},
		},
		{
			// no-profile is the one refusal profile has, so it is the only
			// reason it may carry.
			"a no-profile refusal blaming something else",
			func() cli.Document {
				return selectionDocument(workflow.SelectionNoProfile, cli.StatusRefused, cli.ReasonUncalibrated)
			},
		},
		{
			// Already refused before this file existed. Kept as a lock, because
			// the validation below is about to be rewritten around it.
			"a profile that found one and called it adverse",
			func() cli.Document {
				d := profileDocument()
				d.Status = cli.StatusAdverse
				return d
			},
		},
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

// And the documents those cases are built from must themselves render, or the
// refusals above would prove only that the fixtures are broken.
func TestTheCoherentDocumentsTheyAreBuiltFromRender(t *testing.T) {
	for name, document := range map[string]cli.Document{
		"index":              indexDocument(),
		"profile, sole head": profileDocument(),
		"profile, explicit":  explicitDocument(),
		"profile, no profile": selectionDocument(
			workflow.SelectionNoProfile, cli.StatusRefused, cli.ReasonNoProfile),
	} {
		t.Run(name, func(t *testing.T) {
			for _, asJSON := range []bool{true, false} {
				var out bytes.Buffer
				if err := document.Render(&out, asJSON); err != nil {
					t.Errorf("json=%v: %v", asJSON, err)
				}
			}
		})
	}
}

// The envelope's profile member is the one place a reader learns which profile a
// document is about, and it currently carries the register that was ASKED for.
// So a sole-head lookup — the ordinary case, where nothing was asked — reports
// null while the payload underneath says essays. Report the profile that was
// resolved, and make the two agree.
func TestRunNamesTheResolvedProfileAtTheEnvelope(t *testing.T) {
	// Through Run, because asserting it against a fixture I wrote to have the
	// property proves only that I wrote the fixture. Nothing was asked for
	// here — no --profile — and the workflow resolved essays.
	got := runWith(t, &fakeService{profileResult: soleHead()}, "--json", "profile")
	if got.code != 0 {
		t.Fatalf("code = %d, stderr %q", got.code, got.stderr)
	}
	if name := envelopeProfile(t, got.stdout); name == nil || *name != "essays" {
		t.Errorf("envelope profile = %v, want essays; the payload resolved it", name)
	}

	// And an explicit selection, where what was asked for and what was resolved
	// are the same value — so a Run that reported the REQUEST rather than the
	// resolution would pass this half while failing the one above.
	explicit := runWith(t, &fakeService{profileResult: workflow.ProfileResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedExplicit,
		Available: []string{"essays", "letters"},
		Profile:   workflow.StoredProfile{ID: "pro", Register: "letters", NotReadyReason: "declared"},
	}}, "--json", "profile", "--profile", "letters")
	if explicit.code != 0 {
		t.Fatalf("code = %d, stderr %q", explicit.code, explicit.stderr)
	}
	if name := envelopeProfile(t, explicit.stdout); name == nil || *name != "letters" {
		t.Errorf("envelope profile = %v, want letters", name)
	}

	// And nothing was resolved, so there is nothing to name.
	refused := runWith(t, &fakeService{profileResult: workflow.ProfileResult{
		StorePath: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectionNoProfile,
	}}, "--json", "profile")
	if refused.code != 4 {
		t.Fatalf("code = %d, want 4 (stderr %q)", refused.code, refused.stderr)
	}
	if name := envelopeProfile(t, refused.stdout); name != nil {
		t.Errorf("a refusal names %q as its profile", *name)
	}
}

func envelopeProfile(t *testing.T, encoded string) *string {
	t.Helper()
	var envelope struct {
		Profile *string `json:"profile"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("decoding %q: %v", encoded, err)
	}
	return envelope.Profile
}

// And a document may not disagree with itself about which profile it is about.
func TestTheEnvelopeAndThePayloadNameTheSameProfile(t *testing.T) {
	document := profileDocument()
	result, ok := document.Result.(cli.ProfileResult)
	if !ok {
		t.Fatalf("fixture result is %T", document.Result)
	}
	if document.Profile == nil {
		t.Fatal("a resolved profile is not named at the envelope")
	}
	if *document.Profile != result.Profile.Register {
		t.Errorf("the envelope says %q and the payload says %q", *document.Profile, result.Profile.Register)
	}
}

// The same empty-member wart the no-profile refusal had: an index that is not
// adverse renders "adversity=" with nothing after it. A member with no value is
// noise at best and reads as a bug at worst.
func TestTheHumanRenderingOmitsMembersWithNothingToSay(t *testing.T) {
	var out bytes.Buffer
	if err := indexDocument().Render(&out, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out.String(), "adversity=") {
		t.Errorf("a complete index renders an empty adversity: %q", out.String())
	}
	for _, wanted := range []string{"index ok", "store=/w/.hapax/hapax.sqlite3", "mode=profile-and-reference"} {
		if !strings.Contains(out.String(), wanted) {
			t.Errorf("the rendering does not say %q: %q", wanted, out.String())
		}
	}
}

// indexDocument is a completed index, with counts a real one would carry rather
// than zeroes — validation may come to care what they say.
func indexDocument() cli.Document {
	profileID, referenceID := "pro", "ref"
	register := "essays"
	return cli.Document{
		Schema: cli.Schema, Command: "index", Status: cli.StatusOK, Profile: &register,
		Result: cli.IndexResult{
			Store: "/w/.hapax/hapax.sqlite3", SnapshotID: "snap",
			Mode: workflow.IndexProfileAndReference, ProfileID: &profileID, ReferenceID: &referenceID,
			Documents: 60, Eligible: 60, Nodes: 180, CalibrateSegments: 60, TrainParagraphs: 153,
			NotReadyReason: "profile minimums are declared, not derived",
			Checks:         notPerformedChecks(),
		},
	}
}

// profileDocument is a sole-head lookup: nothing was asked for and essays was
// resolved, so the envelope names essays.
func profileDocument() cli.Document {
	register := "essays"
	referenceID := "ref"
	return cli.Document{
		Schema: cli.Schema, Command: "profile", Status: cli.StatusOK, Profile: &register,
		Result: cli.ProfileResult{
			Store: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedSoleHead,
			Available: []string{"essays"}, ReferenceID: &referenceID,
			Profile: cli.Profile{ID: "pro", Register: "essays", NotReadyReason: "declared"},
		},
	}
}

// explicitDocument is a lookup that named letters and got it.
func explicitDocument() cli.Document {
	register := "letters"
	return cli.Document{
		Schema: cli.Schema, Command: "profile", Status: cli.StatusOK, Profile: &register,
		Result: cli.ProfileResult{
			Store: "/w/.hapax/hapax.sqlite3", Selection: workflow.SelectedExplicit,
			Available: []string{"essays", "letters"},
			Profile:   cli.Profile{ID: "pro", Register: "letters", NotReadyReason: "declared"},
		},
	}
}

// selectionDocument is a profile document that resolved nothing. Run-shaped: no
// profile is named at the envelope because none was resolved, and the available
// list is empty for no-profile — a discovered store with no head has nothing to
// offer — and populated for the two that are asking the caller to choose.
func selectionDocument(selection workflow.Selection, status cli.Status, reason cli.Reason) cli.Document {
	available := []string{"essays", "letters"}
	if selection == workflow.SelectionNoProfile {
		available = nil
	}
	return cli.Document{
		Schema: cli.Schema, Command: "profile", Status: status, Reason: reason,
		Result: cli.ProfileResult{
			Store: "/w/.hapax/hapax.sqlite3", Selection: selection, Available: available,
		},
	}
}
