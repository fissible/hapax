package cli

// The human line, and the defect that shipped three times.
//
//	A2a  profile refused reason=no-profile store=
//	A2b  index ok ... mode=profile-and-reference adversity=
//	A2c  score refused reason=uncalibrated path=draft.md bands= below-floor=0
//
// Each was found by running the binary rather than by a test, and each was
// fixed by adding another conditional to a Sprintf. The class is structural:
// the line is assembled by concatenation, so every optional member is an
// independent opportunity to emit `key=` with nothing after it, and nothing
// makes the omission the default.
//
// # The distinction the builder has to encode
//
// EMPTY is not ZERO. `below-floor=0` is a real measurement and must render;
// `store=` is an absence and must not. A Sprintf per command cannot express
// that difference once; a builder can express it exactly once.
//
// # What this file can and cannot prove
//
// It cannot prove every payload TYPE is covered. Go cannot enumerate the cases
// of a type switch, and building a registry solely so a test could was
// considered and rejected — it rewrites validation for four shipped commands to
// buy a property in a test.
//
// What it does prove is that every COMMAND has a rendering fixture, checked
// against Commands() by name. That is the half of the completeness requirement
// Go can actually deliver, and it fails in the way a new payload would actually
// arrive: someone adds a command and does not add its fixture.

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The builder
// ---------------------------------------------------------------------------

func TestAnAbsentStringIsOmittedAndAZeroMeasurementIsNot(t *testing.T) {
	line := fields{}
	line.Add("store", "")
	line.Add("mode", "profile")
	line.AddInt("below-floor", 0)
	line.AddInt("targets", 2)
	line.AddBool("shippable", false)

	got := line.String()
	want := "mode=profile below-floor=0 targets=2 shippable=false"
	if got != want {
		t.Errorf("line is %q, want %q", got, want)
	}
}

// The order is the order members were added. A line whose fields moved between
// runs would be unreadable to anything scraping it, and unreviewable in a diff.
func TestTheOrderIsTheOrderTheyWereAdded(t *testing.T) {
	line := fields{}
	line.Add("z", "last")
	line.Add("a", "first")
	if got := line.String(); got != "z=last a=first" {
		t.Errorf("line is %q; the builder must not sort", got)
	}
}

// A builder that was given nothing renders nothing, rather than a stray space
// or an empty pair. Render treats an empty result as "no fields to append".
func TestAnEmptyBuilderRendersNothing(t *testing.T) {
	if got := (fields{}).String(); got != "" {
		t.Errorf("an empty builder rendered %q", got)
	}
	line := fields{}
	line.Add("store", "")
	if got := line.String(); got != "" {
		t.Errorf("a builder given only an absent value rendered %q", got)
	}
}

// A value that would itself break the grammar is refused rather than emitted.
// A field carrying a space or an equals sign makes the line unparseable, and a
// path is the value most likely to contain one.
func TestAValueThatWouldBreakTheGrammarIsRefused(t *testing.T) {
	for _, value := range []string{"two words", "a=b", "with\ttab", "with\nnewline"} {
		t.Run(value, func(t *testing.T) {
			line := fields{}
			line.Add("path", value)
			got := line.String()
			if strings.Contains(got, " ") && strings.Count(got, "=") > 1 {
				t.Errorf("line is %q, which cannot be parsed back into fields", got)
			}
			if strings.ContainsAny(got, "\t\n") {
				t.Errorf("line is %q and spans more than one line", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The contract every payload is held to
// ---------------------------------------------------------------------------

// renderingFixtures is the closed registration: one entry per command, holding
// the payloads that command can render. The awkward ones — every optional
// member empty, every count zero, every slice nil — because a payload populated
// the way a SUCCESSFUL run populates it cannot produce a dangling key, which is
// exactly why running the binary on a refusal is what found all three defects.
var renderingFixtures = map[string][]any{
	"tells":   {TellsResult{}},
	"index":   {IndexResult{}},
	"profile": {ProfileResult{}},
	"eval":    {EvalResult{}},
	"score":   {ScoreResult{}},
	"rewrite": {
		RewriteResult{},
		RewriteResult{Refusal: "uncalibrated"},
		RewriteResult{PlanState: "nothing-to-change", RewriteState: "no-targets", Path: "draft.md"},
		RewriteResult{
			PlanState: "targets-planned", RewriteState: "none-improved",
			Path: "draft.md", Targets: 2, Improved: 0, NotImproved: 2,
		},
	},
}

// A command without a fixture is a payload nothing below can check. This is the
// half of #65's completeness requirement that Go can actually deliver: not "every
// payload type", which no reflection can enumerate, but "every command has been
// considered".
func TestEveryCommandHasARenderingFixture(t *testing.T) {
	for _, command := range Commands() {
		if len(renderingFixtures[command]) == 0 {
			t.Errorf("%s has no rendering fixture, so nothing checks whether its payload can "+
				"emit a dangling key", command)
		}
	}
	for command := range renderingFixtures {
		if !contains(Commands(), command) {
			t.Errorf("%s has a rendering fixture and is not a command", command)
		}
	}
}

func TestNoPayloadEverRendersADanglingKey(t *testing.T) {
	type fixture struct {
		name   string
		result any
	}
	var cases []fixture
	for command, payloads := range renderingFixtures {
		for i, payload := range payloads {
			cases = append(cases, fixture{name: fmt.Sprintf("%s/%d", command, i), result: payload})
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := humanResult(c.result)
			if line == "" {
				return // rendering nothing is always safe
			}
			if strings.HasPrefix(line, " ") || strings.HasSuffix(line, " ") {
				t.Errorf("line %q is padded", line)
			}
			if strings.Contains(line, "  ") {
				t.Errorf("line %q has a doubled space, which is a member that rendered empty", line)
			}
			for _, pair := range strings.Split(line, " ") {
				key, value, ok := strings.Cut(pair, "=")
				if !ok {
					t.Errorf("%q in %q is not a key=value pair", pair, line)
					continue
				}
				if key == "" {
					t.Errorf("%q in %q has no key", pair, line)
				}
				if value == "" {
					t.Errorf("%q in %q declares a key with nothing after it, which is the defect "+
						"#65 exists to prevent", pair, line)
				}
			}
		})
	}
}

// And the rewrite line says the things a person reading one line needs: which
// file, what the plan found, and what the rewrite did about it. The counts are
// not decoration — rewrite_state alone cannot separate the two runs that both
// exit zero.
func TestTheRewriteLineCarriesBothStatesAndTheCounts(t *testing.T) {
	line := humanResult(RewriteResult{
		Path: "draft.md", PlanState: "targets-planned", RewriteState: "improved",
		Targets: 3, Improved: 2, NotImproved: 1,
	})
	for _, wanted := range []string{
		"path=draft.md", "plan_state=targets-planned", "rewrite_state=improved",
		"targets=3", "improved=2", "not-improved=1",
	} {
		if !strings.Contains(line, wanted) {
			t.Errorf("line %q is missing %q", line, wanted)
		}
	}
}

// A zero count still renders, because zero is a measurement. "improved=0" is
// the difference between a run that changed nothing and a run that says nothing
// about whether it changed anything.
func TestAZeroCountStillRenders(t *testing.T) {
	line := humanResult(RewriteResult{
		Path: "draft.md", PlanState: "targets-planned", RewriteState: "none-improved",
		Targets: 2, Improved: 0, NotImproved: 2,
	})
	if !strings.Contains(line, "improved=0") {
		t.Errorf("line %q omits improved=0; a zero measurement is not an absence", line)
	}
}
