package cli

// #117, the half that reaches a reader.
//
// `internal/tells` can now say whether a rule set is able to reject anything.
// That fact is useless inside the package: the person who needs it is whoever
// reads a rewrite result and sees no `tells-worse` among the rejections.
//
// Measured, on the shipped rule set: prose that trips six rules produces a
// comparison of zero, so the gate reports "no worse" and the loop accepts. The
// store agrees — `tells_comparison = 0` in all 21 recorded attempts. Without a
// disclosure, the only honest reading of a clean run is unavailable, and the
// available reading is wrong.
//
// This mirrors #92, which surfaces `profile minimums are declared, not derived`
// through `NotReadyReasons()` and the `not_ready_reason` field rather than
// leaving it in a comment. The difference is that this fact is DERIVED — it is
// computed from the loaded rule set — so it cannot go stale the way a declared
// flag can (#113).

import (
	"encoding/json"
	"strings"
	"testing"
)

// The JSON envelope carries the reason the tells gate cannot reject.
//
// Named as a REASON rather than a boolean, following `not_ready_reason`: a
// consumer that wants the flag can test emptiness, and one that wants to tell a
// human why gets the words without the CLI having to hold a second table.
func TestTheRewriteEnvelopeDisclosesAnInactiveTellsGate(t *testing.T) {
	encoded, err := json.Marshal(RewriteResult{
		Path: "draft.md", TellsInactiveReason: TellsInactiveNoValidatedRule,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, present := decoded["tells_inactive_reason"]
	if !present {
		t.Fatalf("the envelope has no tells_inactive_reason: %s", encoded)
	}
	if got != TellsInactiveNoValidatedRule {
		t.Errorf("tells_inactive_reason = %v, want %q", got, TellsInactiveNoValidatedRule)
	}
}

// And omits it when the gate can reject, so its presence means something.
//
// A field that is always there says nothing. This is the half that a
// `json:"tells_inactive_reason"` without `omitempty` would fail.
func TestTheRewriteEnvelopeOmitsTheReasonWhenTheGateIsActive(t *testing.T) {
	encoded, err := json.Marshal(RewriteResult{Path: "draft.md"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "tells_inactive_reason") {
		t.Errorf("an active gate still emitted the field: %s", encoded)
	}
}

// The reason says what is wrong in words a reader can act on.
//
// Asserted as a literal, because every other assertion here compares against
// the constant and renaming it to "tells" would satisfy them all. It has to name
// the condition — no validated rule — rather than the symptom, because the
// symptom is what the reader already sees.
func TestTheInactiveReasonNamesTheConditionNotTheSymptom(t *testing.T) {
	const want = "no validated rule in the active tells rule set"
	if TellsInactiveNoValidatedRule != want {
		t.Errorf("TellsInactiveNoValidatedRule = %q, want %q",
			TellsInactiveNoValidatedRule, want)
	}
	// And it is in the closed vocabulary, so a consumer can enumerate it.
	var found bool
	for _, reason := range TellsInactiveReasons() {
		if reason == want {
			found = true
		}
	}
	if !found {
		t.Errorf("%q is not in TellsInactiveReasons(): %v", want, TellsInactiveReasons())
	}
}

// The human renderer says it too.
//
// A reader of the terminal output is the one most likely to draw the wrong
// conclusion from a quiet run, and least likely to be parsing JSON.
func TestTheHumanRewriteLineDisclosesAnInactiveTellsGate(t *testing.T) {
	line := humanResult(RewriteResult{
		Path: "draft.md", Targets: 2, Improved: 2,
		TellsInactiveReason: TellsInactiveNoValidatedRule,
	})
	if !strings.Contains(line, "tells_inactive") {
		t.Errorf("the human line does not disclose the inactive gate: %q", line)
	}
	// And stays quiet when the gate works, so the line does not grow a member
	// that is empty on every ordinary run — which is #65's complaint.
	active := humanResult(RewriteResult{Path: "draft.md", Targets: 2, Improved: 2})
	if strings.Contains(active, "tells_inactive") {
		t.Errorf("an active gate still printed the field: %q", active)
	}
}
