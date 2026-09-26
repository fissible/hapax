package tells_test

// #117. The tells gate accepts anything, and nothing says so.
//
// `Comparison` counts only findings whose provenance is `derived` and whose
// category is eligible. Every one of the 22 rules in `Default()` is
// `unvalidated`, so the counts are always zero. Measured against the shipped
// rule set:
//
//	clean prose    findings= 0   counts=[0 0 0]
//	loaded prose   findings= 6   counts=[0 0 0]
//
// So `Gate.Tells` reports "no worse" for a candidate that added six tells, and
// the loop accepts it. The real store agrees: `tells_comparison = 0` in all 21
// recorded attempts across 7 invocations.
//
// # Why this slice discloses rather than validates
//
// Validation was attempted and the corpus cannot support it. An
// `author-deviation` rule must not fire on the author's prose and must fire on
// model prose. Measured over 55 author documents, 437 model documents, 2
// published posts and 854 distractors: every rule that fires on model prose
// fires four to ten times MORE on the author, and every rule that spares the
// author fires on nothing. The two candidate sets are disjoint and both empty.
//
// And the author's apparent usage is self-reference: 40% of that corpus
// discusses the tells machinery, and of the rules that fire, `delve` appears in
// three documents of which three discuss it, `at-scale` in one of one, and so
// on. The corpus contains discussion of the measuring instrument, which is
// specifically fatal to validating that instrument.
//
// Marking a rule `derived` on that evidence would assert a measurement nobody
// made. So the interim is to tell the reader the gate is inactive, rather than
// let them read `tells-worse` never appearing as evidence their prose is clean.
//
// # The fact is DERIVED, not declared
//
// Whether the gate can reject is a property of the loaded rule set: it can
// reject only if some rule is both `derived` and of an eligible category. That
// is computable, so there is no third undeliverable constant here — unlike
// `ScriptCeilingDerived`, which is a declaration (#113).

import (
	"testing"

	"github.com/fissible/hapax/internal/tells"
)

// A rule set reports whether any of its rules can move a comparison.
//
// Not "how many rules are loaded" and not "is the default set inert" — the
// question a reader needs answered is whether a refusal is POSSIBLE, and that
// is the conjunction of provenance and category.
func TestARuleSetReportsWhetherItCanRejectAnything(t *testing.T) {
	if tells.Default().Comparable() {
		t.Error("the default rule set reports that it can reject; every rule in it " +
			"is unvalidated, so Comparison always counts zero")
	}
}

// The default set is inert, and the measurement that says so agrees with the
// behaviour rather than restating the rule table.
//
// A `Comparable` that read the rule list and reasoned about it could drift from
// what `Check` actually counts. This compares the claim against a real
// comparison over prose that trips six rules.
func TestTheDefaultSetsInertnessAgreesWithWhatCheckCounts(t *testing.T) {
	rs := tells.Default()
	loaded := doc(t, "It's important to note that, in today's fast-paced world, we must "+
		"delve into the rich tapestry of this multifaceted landscape; moreover, it is not "+
		"merely a question of navigating complexities, but rather of unlocking a testament "+
		"to robust synergy.")

	report := rs.Check(loaded, tells.Options{Register: "essays"})
	if len(report.Findings) == 0 {
		t.Fatal("the fixture must trip some rules or this test proves nothing")
	}
	// Findings exist and the comparison is still empty. That gap IS the defect.
	if got := report.Comparison().Findings(); got != 0 {
		t.Errorf("the comparison counts %d of %d findings; the default set is supposed "+
			"to be inert", got, len(report.Findings))
	}
	if rs.Comparable() {
		t.Errorf("Comparable() is true while the comparison counts none of %d findings",
			len(report.Findings))
	}
}

// A validated rule makes the set comparable, and an unvalidated one does not.
//
// Both directions, because a `Comparable` hardwired to false satisfies every
// assertion above. The rule is otherwise identical between the two cases, so
// provenance is the only thing that can account for the difference.
func TestOneValidatedRuleMakesASetComparable(t *testing.T) {
	for _, c := range []struct {
		name       string
		provenance tells.Provenance
		want       bool
	}{
		{"derived", tells.Derived, true},
		{"unvalidated", tells.Unvalidated, false},
		{"user-defined", tells.UserDefined, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rs := ruleSetWith(t, tells.Rule{
				ID: "probe", Description: "A probe rule.",
				Matcher: tells.Regex, Unit: tells.Document,
				Pattern: "(?i)delve into", Severity: tells.Warn,
				Provenance: c.provenance, Category: tells.AuthorDeviation,
			})
			if got := rs.Comparable(); got != c.want {
				t.Errorf("Comparable() = %v, want %v", got, c.want)
			}
		})
	}
}

// Category matters as much as provenance.
//
// `eligible` admits only author-deviation and source-contamination, so a
// validated formatting rule cannot move a comparison and must not make the set
// report otherwise. Without this, `Comparable` reduces to a provenance scan and
// `double-space` — the one non-deviation rule in the default set — would flip it
// the moment anyone validated it.
func TestAValidatedRuleOfAnIneligibleCategoryDoesNotMakeASetComparable(t *testing.T) {
	rs := ruleSetWith(t, tells.Rule{
		ID: "probe", Description: "A probe rule.",
		Matcher: tells.Regex, Unit: tells.Document,
		Pattern: "  ", Severity: tells.Warn,
		Provenance: tells.Derived, Category: tells.Formatting,
	})
	if rs.Comparable() {
		t.Error("a validated FORMATTING rule made the set comparable; eligible() " +
			"admits only author-deviation and source-contamination")
	}
}

// Every eligible category counts, not just the one the default set uses.
//
// A `Comparable` that tested `== AuthorDeviation` passes every case above,
// because the default set has no source-contamination rule.
func TestEveryEligibleCategoryMakesAValidatedRuleCount(t *testing.T) {
	for _, category := range []tells.Category{tells.AuthorDeviation, tells.SourceContamination} {
		rs := ruleSetWith(t, tells.Rule{
			ID: "probe", Description: "A probe rule.",
			Matcher: tells.Regex, Unit: tells.Document,
			Pattern: "(?i)delve into", Severity: tells.Warn,
			Provenance: tells.Derived, Category: category,
		})
		if !rs.Comparable() {
			t.Errorf("a validated %s rule did not make the set comparable", category)
		}
	}
}

// An empty set cannot reject, and neither can a nil one.
func TestASetWithNoRulesIsNotComparable(t *testing.T) {
	if ruleSetWith(t).Comparable() {
		t.Error("a rule set with no rules reports that it can reject")
	}
}

// ruleSetWith builds a rule set around the given rules, so each case differs
// only in the rule under test.
func ruleSetWith(t *testing.T, rules ...tells.Rule) *tells.RuleSet {
	t.Helper()
	return &tells.RuleSet{Version: "probe-v1", Rules: rules}
}
