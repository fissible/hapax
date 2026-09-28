package tells_test

// #117. The tells gate accepts anything, and nothing says so.
//
// `Comparison` counts a finding only when its rule's provenance is `derived` and
// its category is eligible. Every rule in `Default()` is `unvalidated`, so the
// counts are always zero. Measured against the shipped set, with the fixture
// below: six rules fire — important-to-note, in-todays-landscape, delve,
// not-just-but, testament-to, core-competency — and the comparison is 0. So
// `Gate.Tells` reports "no worse" for a candidate that added six tells and the
// loop accepts it. The maintainer's store agrees: `tells_comparison = 0` in all
// 21 recorded attempts.
//
// # Why this discloses rather than validates
//
// Validation was attempted, on the maintainer's instruction, and the corpus
// cannot support it. An `author-deviation` rule must not fire on the author's
// prose and must fire on model prose. Over 55 author documents, 437 model
// documents, 2 published posts and 854 distractors, the decisive evidence is:
//
//	not-just-but   fires in 11.36% of distractors — 97 of 854 documents of other
//	               people's human writing — and 1 of the 2 published posts
//
// A rule flagging one human document in nine is not an author-deviation signal
// on any reading, and it needs no ratio argument.
//
// Two rules fire on model prose at all, at 1.14% and 0.69%. `operationalize` is
// model-ONLY, at 0.23%, which is one supporting document out of 437. And the
// author's apparent usage is largely self-reference: 40% of that corpus
// discusses the tells machinery, and of the rules that fire, `delve` appears in
// three documents of which three discuss it, `at-scale` one of one.
//
// An earlier summary of this said every rule firing on model prose fires four to
// ten times more on the author, and that the candidate sets were disjoint and
// both empty. Both false — `operationalize` is the counterexample and it was in
// my own table. The conclusion survives on the distractor rate instead.
//
// So marking a rule `derived` would assert a measurement nobody made. The
// interim is to tell the reader the gate is inactive.
//
// # A reason, not a boolean, and two of them
//
// The question is not "is this rule set validated" but "can the gate reject
// anything HERE", and there are two distinct ways for the answer to be no:
//
//   - no-validated-rule   the set holds no derived, eligible rule at all. This
//     is the shipped state, and measured: 22 rules, 0 derived.
//   - no-rule-in-scope    it holds one, but none applies at these options.
//
// The second reason is unreachable by anything the binary can be asked to do
// today: 0 of the 22 shipped rules carry a register or an author, and there is
// no flag that loads a different set, so `Default()` is the only rule set that
// ever runs. Taken alone that is a fair charge of speculative API.
//
// What answers it is not the future corpus but this: the second reason is the
// ONLY thing that forces an implementation to read `Options` at all. With one
// reason, "does a derived eligible rule exist anywhere in the set" satisfies
// every test that can be written against the shipped set, and the scope-blind
// version ships — to be discovered on the day the first validated rule turns out
// to be register-scoped, which is the likely day, since rules are derived from a
// register-specific corpus. That argument is checkable now rather than later,
// and it is why every fixture below is built rather than drawn from the shipped
// set.
//
// The two are mutually exclusive by construction, so there is no precedence to
// settle: the first asks whether any qualifying rule exists, the second whether
// any of the ones that exist apply.
//
// # The reasons are codes, because one of their two renderings cannot hold prose
//
// `cli.fields.Add` silently DROPS a value containing a space — measured, and
// deliberate, since the human line's grammar is `key=value` separated by spaces.
// A prose reason therefore renders as nothing at all on the line most likely to
// be read, with no error anywhere. So the reasons are kebab codes like every
// other closed vocabulary in this project (`not-spliceable`, `uncalibrated`),
// and `internal/cli`'s `TestEveryTellsInactiveReasonSurvivesTheLine` holds the
// next one to it, by running the real `Add` over this vocabulary — a guard
// belongs where the function it guards lives.
//
// # And it is not called Comparable
//
// That name was already taken three times over, all meaning something else:
// `Comparison.Comparable` reports whether two comparisons can be compared,
// `rewrite.TellsVerdict.Comparable` carries that to the loop, and the store's
// `tells_comparable` column persists it — and that column reads `true` in the
// very 21 attempts cited above as evidence of this bug. A fourth `Comparable`
// meaning the opposite would be the worst possible name.

import (
	"testing"

	"github.com/fissible/hapax/internal/tells"
)

// derivedRule is a rule set built through `Load`, so the fixtures are rule sets
// the product can actually hold.
//
// An earlier draft built `tells.Rule` literals directly with `Provenance:
// Derived` and no evidence. `validateRule` rejects that — "derived rule requires
// complete evidence" — so the only fixtures that could fail a hardwired inactive
// answer were sets `Load` refuses, and any implementation with a STRONGER
// condition was forbidden by fixtures that could not exist.
func derivedRule(t *testing.T, id, category, scope string) *tells.RuleSet {
	t.Helper()
	return ruleSet(t, `version = "probe-v1"
[[rule]]
id = "`+id+`"
description = "A probe rule."
matcher = "regex"
unit = "document"
pattern = "(?i)delve into"
severity = "warn"
provenance = "derived"
category = "`+category+`"
`+scope+`
[rule.evidence]
reference = "probe-reference"
population = "probe-population"
digest = "probe-digest"
`)
}

// The shipped set cannot reject, whatever options it is asked about, and it says
// which of the two reasons applies.
func TestTheDefaultRuleSetIsInactive(t *testing.T) {
	for _, o := range []tells.Options{
		{},
		{Register: "essays"},
		{Register: "letters"},
		{Register: "essays", Author: "someone"},
	} {
		got := tells.Default().InactiveReason(o)
		if got != tells.InactiveNoValidatedRule {
			t.Errorf("InactiveReason(%+v) = %q, want %q — every rule in the shipped set "+
				"is unvalidated, so Comparison always counts zero", o, got,
				tells.InactiveNoValidatedRule)
		}
	}
}

// And the claim agrees with what `Check` actually counts, rather than restating
// the rule table.
func TestTheDefaultSetsInactivityAgreesWithWhatCheckCounts(t *testing.T) {
	rs := tells.Default()
	o := tells.Options{Register: "essays"}
	loaded := doc(t, "It's important to note that, in today's fast-paced world, we must "+
		"delve into the rich tapestry of this multifaceted landscape; moreover, it is not "+
		"merely a question of navigating complexities, but rather of unlocking a testament "+
		"to robust synergy.")

	report := rs.Check(loaded, o)
	if len(report.Findings) == 0 {
		t.Fatal("the fixture must trip some rules or this test proves nothing")
	}
	if got := report.Comparison().Findings(); got != 0 {
		t.Errorf("the comparison counts %d of %d findings; the shipped set is inert",
			got, len(report.Findings))
	}
	if rs.InactiveReason(o) == "" {
		t.Errorf("the gate reports itself active while the comparison counts none of "+
			"%d findings", len(report.Findings))
	}
}

// One validated, eligible, in-scope rule makes the gate active.
func TestAValidatedEligibleInScopeRuleMakesTheGateActive(t *testing.T) {
	rs := derivedRule(t, "probe", "author-deviation", "")
	if got := rs.InactiveReason(tells.Options{Register: "essays"}); got != "" {
		t.Errorf("InactiveReason = %q with a validated author-deviation rule in scope, "+
			"want empty", got)
	}
}

// Provenance is necessary. Both other values, because a reason hardwired to the
// empty string satisfies only the test above.
func TestAnUnvalidatedRuleLeavesTheGateInactive(t *testing.T) {
	for _, provenance := range []string{"unvalidated", "user-defined"} {
		rs := ruleSet(t, `version = "probe-v1"
[[rule]]
id = "probe"
description = "A probe rule."
matcher = "regex"
unit = "document"
pattern = "(?i)delve into"
severity = "warn"
provenance = "`+provenance+`"
category = "author-deviation"
`)
		if got := rs.InactiveReason(tells.Options{Register: "essays"}); got != tells.InactiveNoValidatedRule {
			t.Errorf("a %s rule gave reason %q, want %q", provenance, got,
				tells.InactiveNoValidatedRule)
		}
	}
}

// Category is necessary, and every eligible category counts.
//
// `eligible` admits author-deviation and source-contamination only, and both
// rows are needed: an `== AuthorDeviation` implementation passes on the shipped
// set, which has no source-contamination rule, so only a built fixture catches
// it.
//
// What this does NOT pin, measured by mutation: a `Category != Formatting`
// implementation. `categories` holds exactly three values and `validateRule`
// refuses a fourth — "unknown category %q" — so over every category a rule set
// can hold, the two predicates are the same function. The mutant is equivalent
// rather than surviving, and no test can kill it. An earlier draft of this
// comment named that implementation as a risk and then claimed both directions
// were pinned; the risk does not exist while the vocabulary is closed, and if a
// fourth category is ever declared, THIS is the test that needs a row for it.
func TestOnlyEligibleCategoriesActivateTheGate(t *testing.T) {
	for _, c := range []struct {
		category string
		want     string
	}{
		{"author-deviation", ""},
		{"source-contamination", ""},
		{"formatting", tells.InactiveNoValidatedRule},
	} {
		t.Run(c.category, func(t *testing.T) {
			pattern := "(?i)delve into"
			if c.category == "formatting" {
				pattern = "  "
			}
			rs := ruleSet(t, `version = "probe-v1"
[[rule]]
id = "probe"
description = "A probe rule."
matcher = "regex"
unit = "document"
pattern = "`+pattern+`"
severity = "warn"
provenance = "derived"
category = "`+c.category+`"
[rule.evidence]
reference = "probe-reference"
population = "probe-population"
digest = "probe-digest"
`)
			if got := rs.InactiveReason(tells.Options{Register: "essays"}); got != c.want {
				t.Errorf("InactiveReason = %q, want %q", got, c.want)
			}
		})
	}
}

// SCOPE is necessary, it is the case a per-rule-set question gets wrong, and it
// has its own reason.
//
// Both directions on both axes, since `inScope` reads register AND author and an
// implementation ignoring either would otherwise survive.
func TestARuleOutOfScopeLeavesTheGateInactiveForADifferentReason(t *testing.T) {
	for _, c := range []struct {
		axis, scope   string
		active, inert tells.Options
	}{
		{
			axis:   "register",
			scope:  `registers = ["letters"]`,
			active: tells.Options{Register: "letters"},
			inert:  tells.Options{Register: "essays"},
		},
		{
			axis:   "author",
			scope:  `authors = ["someone"]`,
			active: tells.Options{Register: "essays", Author: "someone"},
			inert:  tells.Options{Register: "essays", Author: "another"},
		},
	} {
		t.Run(c.axis, func(t *testing.T) {
			rs := derivedRule(t, "probe", "author-deviation", c.scope)
			if got := rs.InactiveReason(c.active); got != "" {
				t.Errorf("within its own %s the rule gives reason %q, want empty",
					c.axis, got)
			}
			if got := rs.InactiveReason(c.inert); got != tells.InactiveNoRuleInScope {
				t.Errorf("out of %s scope the reason is %q, want %q — a validated rule "+
					"that does not apply here is not the same absence as no validated "+
					"rule at all", c.axis, got, tells.InactiveNoRuleInScope)
			}
		})
	}
}

// The two reasons are distinguished by what EXISTS, not by what applies, so a
// set holding both an out-of-scope validated rule and an in-scope invalid one
// reports the scope reason.
//
// This is the case an implementation that filters by scope FIRST and then asks
// "any derived rule left?" gets wrong: it would answer no-validated-rule, which
// tells the reader to go validate a rule they have already validated.
func TestAScopeMissIsReportedEvenWhenAnUnvalidatedRuleApplies(t *testing.T) {
	rs := ruleSet(t, `version = "probe-v1"
[[rule]]
id = "applies-but-unvalidated"
description = "In scope everywhere, and not validated."
matcher = "regex"
unit = "document"
pattern = "  "
severity = "warn"
provenance = "unvalidated"
category = "author-deviation"

[[rule]]
id = "validated-elsewhere"
description = "Validated, and scoped to another register."
matcher = "regex"
unit = "document"
pattern = "(?i)delve into"
severity = "warn"
provenance = "derived"
category = "author-deviation"
registers = ["letters"]
[rule.evidence]
reference = "probe-reference"
population = "probe-population"
digest = "probe-digest"
`)
	if got := rs.InactiveReason(tells.Options{Register: "essays"}); got != tells.InactiveNoRuleInScope {
		t.Errorf("InactiveReason = %q, want %q", got, tells.InactiveNoRuleInScope)
	}
	// And the same set is active in the register the validated rule names.
	if got := rs.InactiveReason(tells.Options{Register: "letters"}); got != "" {
		t.Errorf("InactiveReason = %q in the rule's own register, want empty", got)
	}
}

// The other two members of `Options` do not enter the answer.
//
// `Options` has four fields and only two of them are rule scope. The other two
// are not "unspecified by omission"; they are specified to be irrelevant, and
// for different reasons:
//
//   - HonourSuppressions makes `Comparison.Compare` return ErrIncomparable, and
//     the loop turns that into a `tells-incomparable` REJECTION. A gate that
//     refuses everything is the opposite of an inactive one, so folding it in
//     here would report the two indistinguishably.
//   - MaxFindings truncates a particular document's findings. Whether that
//     happens depends on the text, not on the rule set, so it is not a fact
//     `InactiveReason` can answer at all.
//
// Asserted against a set that IS active, because a hardwired reason would
// satisfy the inactive direction whatever it did with these.
func TestSuppressionsAndTruncationDoNotEnterTheAnswer(t *testing.T) {
	rs := derivedRule(t, "probe", "author-deviation", "")
	base := tells.Options{Register: "essays"}
	if got := rs.InactiveReason(base); got != "" {
		t.Fatalf("InactiveReason = %q for the base options; the fixture is not active "+
			"and the variants below would prove nothing", got)
	}
	for name, variant := range map[string]tells.Options{
		"suppressions honoured": {Register: "essays", HonourSuppressions: true},
		"findings capped":       {Register: "essays", MaxFindings: 1},
		"both":                  {Register: "essays", HonourSuppressions: true, MaxFindings: 1},
	} {
		if got := rs.InactiveReason(variant); got != "" {
			t.Errorf("%s: InactiveReason = %q, want empty — neither member is a fact "+
				"about what the rule set can reject", name, got)
		}
	}
}

// A qualifying rule anywhere in the set counts, not just the first.
//
// An early return inside the loop — answering from `Rules[0]` — passes every
// single-rule fixture above, and every multi-rule set in the package has the
// same answer for all its rules.
func TestAQualifyingRuleAnywhereInTheSetCounts(t *testing.T) {
	rs := ruleSet(t, `version = "probe-v1"
[[rule]]
id = "first"
description = "An unvalidated rule, deliberately first."
matcher = "regex"
unit = "document"
pattern = "  "
severity = "warn"
provenance = "unvalidated"
category = "formatting"

[[rule]]
id = "second"
description = "A validated rule, deliberately second."
matcher = "regex"
unit = "document"
pattern = "(?i)delve into"
severity = "warn"
provenance = "derived"
category = "author-deviation"
[rule.evidence]
reference = "probe-reference"
population = "probe-population"
digest = "probe-digest"
`)
	if got := rs.InactiveReason(tells.Options{Register: "essays"}); got != "" {
		t.Errorf("InactiveReason = %q; a qualifying rule in second position was not "+
			"found", got)
	}
}

// A set with no rules is inactive, and for the reason that names the absence.
func TestASetWithNoRulesIsInactive(t *testing.T) {
	rs := ruleSet(t, `version = "probe-v1"`)
	if got := rs.InactiveReason(tells.Options{Register: "essays"}); got != tells.InactiveNoValidatedRule {
		t.Errorf("InactiveReason = %q for a set with no rules, want %q", got,
			tells.InactiveNoValidatedRule)
	}
}

// The two answers move together: when the gate reports itself active, `Check`
// counts what it finds, and when it does not, it counts nothing.
//
// `TestTheDefaultSetsInactivityAgreesWithWhatCheckCounts` pins one direction on
// the shipped set — inactive, and nothing counted. Nothing pinned the converse,
// so the two predicates could drift apart: an `InactiveReason` that skipped the
// scope filter `Check` applies would still pass every test above, because no
// active-direction fixture looks at what `Check` actually counted. The pair
// below is the same rule seen from both sides of one scope boundary.
func TestTheReasonAndTheCountAgreeInBothDirections(t *testing.T) {
	rs := derivedRule(t, "probe", "author-deviation", `registers = ["essays"]`)
	loaded := doc(t, "We delve into the matter at some length.")

	for _, c := range []struct {
		name   string
		o      tells.Options
		want   string
		counts int
	}{
		{"in scope", tells.Options{Register: "essays"}, "", 1},
		{"out of scope", tells.Options{Register: "letters"}, tells.InactiveNoRuleInScope, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := rs.InactiveReason(c.o); got != c.want {
				t.Errorf("InactiveReason = %q, want %q", got, c.want)
			}
			if got := rs.Check(loaded, c.o).Comparison().Findings(); got != c.counts {
				t.Errorf("Check counted %d findings, want %d — the two answers have "+
					"drifted apart", got, c.counts)
			}
		})
	}
}

// The vocabulary is closed, declared, and spelled as it reads.
//
// Asserted as literals, because every other assertion here compares against the
// constants and renaming one would satisfy them all.
func TestTheInactiveReasonsAreDeclared(t *testing.T) {
	for want, got := range map[string]string{
		"no-validated-rule": tells.InactiveNoValidatedRule,
		"no-rule-in-scope":  tells.InactiveNoRuleInScope,
	} {
		if got != want {
			t.Errorf("a declared reason is %q, want %q", got, want)
		}
	}
	reasons := tells.InactiveReasons()
	if len(reasons) != 2 {
		t.Fatalf("InactiveReasons() = %v, want the two declared reasons", reasons)
	}
	seen := map[string]int{}
	for _, r := range reasons {
		seen[r]++
	}
	for _, want := range []string{tells.InactiveNoValidatedRule, tells.InactiveNoRuleInScope} {
		if seen[want] != 1 {
			t.Errorf("%q appears %d times in InactiveReasons(): %v", want, seen[want], reasons)
		}
	}
}

// No reason is the empty string, because the empty string already means the
// opposite.
//
// That a reason also survives the human line's grammar is asserted where `Add`
// lives, in `internal/cli`, by running the real function over this vocabulary.
// An earlier draft asserted it here by hand-copying Add's character set across a
// package boundary, which would have gone on passing the day that set widened.
func TestNoReasonIsTheEmptyString(t *testing.T) {
	for _, reason := range tells.InactiveReasons() {
		if reason == "" {
			t.Error("the empty string is a declared reason; it is what ACTIVE means")
		}
	}
}

// The vocabulary is handed out by copy.
//
// `rewrite.Terminals` has this test for the same reason: an accessor returning
// its own backing array lets any caller edit the vocabulary for every other one.
func TestTheReasonVocabularyCannotBeEditedByACaller(t *testing.T) {
	first := tells.InactiveReasons()
	if len(first) == 0 {
		t.Fatal("InactiveReasons() is empty")
	}
	first[0] = "tampered"
	for _, reason := range tells.InactiveReasons() {
		if reason == "tampered" {
			t.Error("InactiveReasons() returns its own backing array")
		}
	}
}

// `Default()` must actually load, because the headline test above passes
// vacuously if it does not.
//
// `Default()` discards its error and returns whatever it got, so a broken
// embedded file makes it nil — and a pointer method that never dereferences
// returns a reason all the same, which reads as "correctly inert". That is a
// check that could not run reported as a check that passed.
func TestTheDefaultRuleSetActuallyLoads(t *testing.T) {
	rs := tells.Default()
	if rs == nil {
		t.Fatal("Default() is nil; the embedded rule file did not load, and every " +
			"inactivity assertion in this file would pass vacuously")
	}
	if len(rs.Rules) != 22 {
		t.Errorf("Default() holds %d rules, want 22", len(rs.Rules))
	}
	// Measured: 0 of 22 carry a register or an author, which is why the shipped
	// answer above is the same at every register, and why no fixture drawn from
	// the shipped set could ever exercise scope.
	for _, r := range rs.Rules {
		if len(r.Registers) > 0 || len(r.Authors) > 0 {
			t.Errorf("rule %s is scoped (registers=%v authors=%v); the claim that no "+
				"shipped rule is scoped no longer holds", r.ID, r.Registers, r.Authors)
		}
	}
}
