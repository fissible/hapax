// Package tells_test defines the contract for the deterministic tell linter.
//
// # Scope, and the decision that shapes it
//
// Section 1 says "rule schema before rules", and this slice takes that
// literally: it delivers the schema, the regex matcher, suppression, scoping and
// the screening result model. It ships NO curated list of AI-sounding words.
//
// The prior-art survey found that banned-word lists are what every competing
// project already ships, and issue #4 records that tell rules should be DERIVED
// from paired author/model data. Shipping folklore as fact would contradict the
// project's thesis. So every rule declares a provenance and a category, a
// derived rule must cite structured evidence, and the linter withholds a
// screening verdict it has not earned.
//
// A sharpening from review, recorded because it changes what this component is
// for: a formatting rule is not evidence of machine authorship no matter how
// well derived. The corpus contamination hole is closed primarily by SOURCE
// PROVENANCE and quarantine — issue #4 — and this linter is a backstop.
//
// # Declared holes
//
//   - STRUCTURAL MATCHERS. Triplet stacking, repeated sentence openers and
//     em-dash density are not regex-shaped. The schema admits a matcher kind and
//     a unit so they can be added without a migration, but only `regex` is
//     implemented; `structural` is rejected as unimplemented rather than
//     silently ignored.
//   - CODE-FENCE AWARENESS for suppression, which needs the structural tree from
//     text slice 2d.
package tells_test

import (
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/text"
)

func doc(t *testing.T, src string) *text.Document {
	t.Helper()
	d, err := text.Admit([]byte(src))
	if err != nil {
		t.Fatalf("Admit(%q): %v", src, err)
	}
	return d
}

func ruleSet(t *testing.T, toml string) *tells.RuleSet {
	t.Helper()
	rs, err := tells.Load([]byte(toml))
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, toml)
	}
	return rs
}

// allowSuppression is the only way a caller gets suppression honoured.
func allowSuppression() tells.Options {
	return tells.Options{HonourSuppressions: true}
}

// derivedSeverities has one error-severity and one info-severity rule, both
// derived and verdict-eligible, so they reach the acceptance gate.
const derivedSeverities = `
version = "t"

[[rule]]
id = "severe"
description = "d"
matcher = "regex"
unit = "document"
pattern = "SEVERE"
severity = "error"
provenance = "derived"
category = "author-deviation"
[rule.evidence]
reference = "r"
population = "p"
digest = "d"

[[rule]]
id = "minor"
description = "d"
matcher = "regex"
unit = "document"
pattern = "minor"
severity = "info"
provenance = "derived"
category = "author-deviation"
[rule.evidence]
reference = "r"
population = "p"
digest = "d"
`

const oneRule = `
version = "test-1"

[[rule]]
id = "double-space"
description = "Two spaces between words."
matcher = "regex"
unit = "document"
pattern = "  "
severity = "warn"
provenance = "unvalidated"
category = "formatting"
`

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

func TestRuleRequiresEverySchemaField(t *testing.T) {
	base := map[string]string{
		"id":          `id = "x"`,
		"description": `description = "d"`,
		"matcher":     `matcher = "regex"`,
		"unit":        `unit = "document"`,
		"pattern":     `pattern = "p"`,
		"severity":    `severity = "warn"`,
		"provenance":  `provenance = "unvalidated"`,
		"category":    `category = "formatting"`,
	}
	for missing := range base {
		t.Run("missing "+missing, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("version = \"t\"\n[[rule]]\n")
			for field, line := range base {
				if field != missing {
					b.WriteString(line + "\n")
				}
			}
			if _, err := tells.Load([]byte(b.String())); err == nil {
				t.Errorf("a rule with no %s was accepted", missing)
			}
		})
	}
}

// A typo in a field name must fail loudly. Silently ignoring an unknown key
// loses scope or evidence while the rule still loads and fires.
func TestUnknownKeysAreRejected(t *testing.T) {
	for name, extra := range map[string]string{
		"misspelt scope":      `registerz = ["essays"]`,
		"misspelt provenance": `provenence = "derived"`,
		"invented field":      `autofix = "x"`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tells.Load([]byte(oneRule + extra + "\n")); err == nil {
				t.Errorf("unknown key (%s) was accepted", name)
			}
		})
	}
	if _, err := tells.Load([]byte("version = \"t\"\nunknown_top_level = 1\n")); err == nil {
		t.Error("unknown top-level key was accepted")
	}
}

// Evidence is structured, not prose. "It looked right to me" is not a citation,
// and a free-text field invites exactly that.
func TestDerivedRulesRequireStructuredEvidence(t *testing.T) {
	derived := `
version = "t"
[[rule]]
id = "x"
description = "d"
matcher = "regex"
unit = "document"
pattern = "p"
severity = "warn"
provenance = "derived"
category = "source-contamination"
`
	if _, err := tells.Load([]byte(derived)); err == nil {
		t.Error("a derived rule with no evidence was accepted")
	}

	partial := derived + "[rule.evidence]\nreference = \"golden-set-2026-08\"\n"
	if _, err := tells.Load([]byte(partial)); err == nil {
		t.Error("a derived rule citing a reference but no population or digest was accepted")
	}

	full := derived + `
[rule.evidence]
reference = "golden-set-2026-08"
population = "14 matched-brief triplets, one author"
digest = "sha256:abcd"
`
	rs := ruleSet(t, full)
	if rs.Rules[0].Evidence == nil || rs.Rules[0].Evidence.Reference == "" {
		t.Error("evidence was not retained")
	}
}

func TestUnvalidatedRulesMustNotClaimEvidence(t *testing.T) {
	src := oneRule + "[rule.evidence]\nreference = \"r\"\npopulation = \"p\"\ndigest = \"d\"\n"
	if _, err := tells.Load([]byte(src)); err == nil {
		t.Error("an unvalidated rule carrying evidence was accepted; the two states must stay distinguishable")
	}
}

func TestEnumeratedFieldsRejectUnknownValues(t *testing.T) {
	for name, replacement := range map[string][2]string{
		"provenance": {`provenance = "unvalidated"`, `provenance = "probably"`},
		"severity":   {`severity = "warn"`, `severity = "catastrophic"`},
		"category":   {`category = "formatting"`, `category = "vibes"`},
		"matcher":    {`matcher = "regex"`, `matcher = "telepathy"`},
		"unit":       {`unit = "document"`, `unit = "fortnight"`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tells.Load([]byte(strings.Replace(oneRule, replacement[0], replacement[1], 1))); err == nil {
				t.Errorf("an unknown %s was accepted", name)
			}
		})
	}
}

// The schema admits structural matchers so they can be added without a
// migration, but this slice implements none. Accepting one silently would let a
// rule load and never fire.
func TestStructuralMatchersAreRejectedAsUnimplemented(t *testing.T) {
	src := strings.Replace(oneRule, `matcher = "regex"`, `matcher = "structural"`, 1)
	_, err := tells.Load([]byte(src))
	if err == nil {
		t.Fatal("a structural matcher was accepted; none is implemented, so the rule would never fire")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unimplemented") {
		t.Errorf("error %q does not say the matcher kind is unimplemented", err)
	}
}

func TestDuplicateRuleIDsAndEmptyIDsAreRejected(t *testing.T) {
	if _, err := tells.Load([]byte(oneRule + strings.TrimPrefix(oneRule, "\nversion = \"test-1\"\n"))); err == nil {
		t.Error("duplicate rule IDs were accepted; findings could not be attributed")
	}
	if _, err := tells.Load([]byte(strings.Replace(oneRule, `id = "double-space"`, `id = ""`, 1))); err == nil {
		t.Error("an empty rule ID was accepted")
	}
}

func TestInvalidPatternsFailAtLoad(t *testing.T) {
	if _, err := tells.Load([]byte(strings.Replace(oneRule, `pattern = "  "`, `pattern = "([unclosed"`, 1))); err == nil {
		t.Error("an invalid pattern was accepted at load rather than failing on some later document")
	}
}

// A rule matching the empty string would report a finding at every position.
func TestZeroWidthPatternsAreRejected(t *testing.T) {
	for _, pattern := range []string{`a*`, `(?:)`, `\b`} {
		t.Run(pattern, func(t *testing.T) {
			if _, err := tells.Load([]byte(strings.Replace(oneRule, `pattern = "  "`, `pattern = "`+pattern+`"`, 1))); err == nil {
				t.Errorf("zero-width pattern %q was accepted", pattern)
			}
		})
	}
}

func TestRuleSetVersionIsRequiredAndDigested(t *testing.T) {
	if _, err := tells.Load([]byte("[[rule]]\nid=\"x\"\n")); err == nil {
		t.Error("a rule set with no version was accepted")
	}
	rs := ruleSet(t, oneRule)
	if rs.Version != "test-1" {
		t.Errorf("Version = %q", rs.Version)
	}
	// The digest identifies the exact rules that produced a report, so two
	// reports can be compared only when it matches.
	if rs.Digest == "" {
		t.Error("rule set has no digest")
	}
	if same := ruleSet(t, oneRule); same.Digest != rs.Digest {
		t.Error("identical rule sets produced different digests")
	}
	changed := ruleSet(t, strings.Replace(oneRule, `severity = "warn"`, `severity = "info"`, 1))
	if changed.Digest == rs.Digest {
		t.Error("changing a rule's severity did not change the digest")
	}
}

// ---------------------------------------------------------------------------
// Matching and findings
// ---------------------------------------------------------------------------

func TestFindingsCarrySpansAndBasis(t *testing.T) {
	d := doc(t, "one  two  three")
	report := ruleSet(t, oneRule).Check(d, tells.Options{})

	if len(report.Findings) != 2 {
		t.Fatalf("%d findings, want 2", len(report.Findings))
	}
	for _, f := range report.Findings {
		got, err := d.Resolve(f.Span)
		if err != nil {
			t.Errorf("Resolve(%+v): %v", f.Span, err)
			continue
		}
		if got != "  " {
			t.Errorf("span resolves to %q, want two spaces", got)
		}
		if f.Provenance != tells.Unvalidated || f.Category != tells.Formatting {
			t.Errorf("finding does not carry its basis: %+v", f)
		}
	}
}

// A regex match can land inside a grapheme cluster, producing a span the
// document refuses to resolve. Spans are snapped, and the finding records that
// it was widened so the report does not misrepresent the match.
func TestFindingSpansAreGraphemeSafe(t *testing.T) {
	// "e" + combining acute: a pattern matching the bare "e" would split it.
	src := "café and more"
	d := doc(t, src)
	rs := ruleSet(t, strings.Replace(oneRule, `pattern = "  "`, `pattern = "e"`, 1))
	report := rs.Check(d, tells.Options{})

	if len(report.Findings) == 0 {
		t.Fatal("no findings")
	}
	for _, f := range report.Findings {
		if _, err := d.Resolve(f.Span); err != nil {
			t.Errorf("finding span %+v does not resolve: %v — spans must be snapped to grapheme boundaries", f.Span, err)
		}
	}
}

// Matching runs against the document's raw bytes, so spans mean the same thing
// as every other span in the system.
func TestSpanOffsetsAreCorrectAcrossMultibyteText(t *testing.T) {
	d := doc(t, "café  résumé")
	report := ruleSet(t, oneRule).Check(d, tells.Options{})
	if len(report.Findings) != 1 {
		t.Fatalf("%d findings, want 1", len(report.Findings))
	}
	if got := report.Findings[0].Span; got.Offset != 5 || got.Length != 2 {
		t.Errorf("span = %+v, want {Offset:5 Length:2}", got)
	}
}

// Ordering: position, then length, then rule ID. Equal offsets with different
// lengths must not be left to map iteration.
func TestFindingsAreOrderedDeterministically(t *testing.T) {
	src := `
version = "t"

[[rule]]
id = "zebra"
description = "one a"
matcher = "regex"
unit = "document"
pattern = "a"
severity = "warn"
provenance = "unvalidated"
category = "formatting"

[[rule]]
id = "alpha"
description = "two a's"
matcher = "regex"
unit = "document"
pattern = "aa"
severity = "info"
provenance = "unvalidated"
category = "formatting"
`
	report := ruleSet(t, src).Check(doc(t, "aa"), tells.Options{})
	var got []string
	for _, f := range report.Findings {
		got = append(got, f.RuleID)
	}
	// Both match at offset 0; the shorter match sorts first.
	if !equalStrings(got, []string{"zebra", "alpha"}) {
		t.Errorf("order %v, want [zebra alpha] — offset, then length, then rule ID", got)
	}
}

// When offset AND length tie, rule ID breaks it, so nothing is left to map
// iteration order.
func TestRuleIDBreaksTiesAtEqualOffsetAndLength(t *testing.T) {
	rule := func(id string) string {
		return "\n[[rule]]\nid = \"" + id + "\"\ndescription = \"d\"\nmatcher = \"regex\"\nunit = \"document\"\n" +
			"pattern = \"ab\"\nseverity = \"warn\"\nprovenance = \"unvalidated\"\ncategory = \"formatting\"\n"
	}
	rs := ruleSet(t, "version = \"t\""+rule("zulu")+rule("alpha")+rule("mike"))
	var got []string
	for _, f := range rs.Check(doc(t, "ab"), tells.Options{}).Findings {
		got = append(got, f.RuleID)
	}
	if !equalStrings(got, []string{"alpha", "mike", "zulu"}) {
		t.Errorf("order %v, want alphabetical by rule ID at identical spans", got)
	}
}

// The snapped span is exact, and the finding says it was widened so a report
// does not misrepresent what the pattern actually matched.
func TestWidenedSpansAreExactAndFlagged(t *testing.T) {
	// "cafe" + combining acute: the bare "e" is at byte 3, the mark occupies 4-5.
	d := doc(t, "cafe\u0301 x")
	rs := ruleSet(t, strings.Replace(oneRule, `pattern = "  "`, `pattern = "e"`, 1))
	report := rs.Check(d, tells.Options{})
	if len(report.Findings) != 1 {
		t.Fatalf("%d findings, want 1", len(report.Findings))
	}
	f := report.Findings[0]
	if f.Span.Offset != 3 || f.Span.Length != 3 {
		t.Errorf("span = %+v, want {Offset:3 Length:3} covering the whole cluster", f.Span)
	}
	if !f.SpanWidened {
		t.Error("span was widened to a grapheme boundary but the finding does not say so")
	}
}

func TestFindingsExposeContributingSpans(t *testing.T) {
	report := ruleSet(t, oneRule).Check(doc(t, "a  b"), tells.Options{})
	if len(report.Findings) != 1 {
		t.Fatalf("%d findings, want 1", len(report.Findings))
	}
	// A regex match has one span and no contributors; the field exists for
	// structural matchers and must be empty rather than absent.
	if report.Findings[0].Contributing == nil {
		t.Error("Contributing is nil; it must be an empty slice so structural matchers have a place to put spans")
	}
	if len(report.Findings[0].Contributing) != 0 {
		t.Errorf("a regex finding has %d contributing spans, want 0", len(report.Findings[0].Contributing))
	}
}

func TestOverlappingMatchesOfOneRuleAreNonOverlapping(t *testing.T) {
	rs := ruleSet(t, strings.Replace(oneRule, `pattern = "  "`, `pattern = "aa"`, 1))
	report := rs.Check(doc(t, "aaa"), tells.Options{})
	if n := len(report.Findings); n != 1 {
		t.Errorf("%d findings for %q, want 1 leftmost non-overlapping match", n, "aaa")
	}
	if got := report.Findings[0].Span.Offset; got != 0 {
		t.Errorf("match at %d, want the leftmost at 0", got)
	}
}

// RE2 cannot backtrack, and the engine bounds its own output, so neither a
// crafted pattern nor a crafted document can exhaust the process.
func TestPathologicalInputIsBounded(t *testing.T) {
	rs := ruleSet(t, strings.Replace(oneRule, `pattern = "  "`, `pattern = "(a+)+b"`, 1))
	rs.Check(doc(t, strings.Repeat("a", 20000)), tells.Options{})

	many := ruleSet(t, strings.Replace(oneRule, `pattern = "  "`, `pattern = "a"`, 1))
	report := many.Check(doc(t, strings.Repeat("a", 50000)), tells.Options{MaxFindings: 100})
	if len(report.Findings) > 100 {
		t.Errorf("%d findings, want at most the 100 requested", len(report.Findings))
	}
	if !report.Truncated {
		t.Error("output was capped but the report does not say so")
	}
}

// ---------------------------------------------------------------------------
// Suppression
// ---------------------------------------------------------------------------

// Suppression is opt-in. A corpus file must not be able to waive its own
// screening, so corpus screening simply never enables it.
func TestSuppressionIsIgnoredUnlessEnabled(t *testing.T) {
	d := doc(t, "<!-- hapax:allow double-space reason=\"table alignment\" -->\none  two")
	if got := ruleSet(t, oneRule).Check(d, tells.Options{}).Count(); got != 1 {
		t.Errorf("Count() = %d with suppression disabled, want 1 — a document must not waive its own check by default", got)
	}
	if got := ruleSet(t, oneRule).Check(d, allowSuppression()).Count(); got != 0 {
		t.Errorf("Count() = %d with suppression enabled, want 0", got)
	}
}

// A waiver must say why, and the directive itself is located so a reader can
// find it.
func TestSuppressionRequiresAReasonAndIsRecorded(t *testing.T) {
	d := doc(t, "<!-- hapax:allow double-space reason=\"table alignment\" -->\none  two")
	report := ruleSet(t, oneRule).Check(d, allowSuppression())

	if len(report.Suppressed) != 1 {
		t.Fatalf("%d suppressed findings, want 1 — waived findings are recorded, never dropped", len(report.Suppressed))
	}
	s := report.Suppressed[0]
	if s.Reason != "table alignment" {
		t.Errorf("Reason = %q", s.Reason)
	}
	if s.DirectiveSpan.Length == 0 {
		t.Error("the directive itself has no span; a reader cannot locate the waiver")
	}
	if len(report.Findings) != 0 {
		t.Errorf("%d active findings, want 0", len(report.Findings))
	}
}

func TestSuppressionWithoutAReasonIsRejected(t *testing.T) {
	d := doc(t, "<!-- hapax:allow double-space -->\none  two")
	report := ruleSet(t, oneRule).Check(d, allowSuppression())
	if len(report.MalformedSuppressions) != 1 {
		t.Fatalf("%d malformed suppressions, want 1 — a waiver with no reason is not a waiver", len(report.MalformedSuppressions))
	}
	if report.Count() != 1 {
		t.Errorf("Count() = %d, want 1; a malformed directive must not suppress anything", report.Count())
	}
}

func TestSuppressionIsScopedToTheNamedRuleAndNextLine(t *testing.T) {
	src := oneRule + `
[[rule]]
id = "trailing-space"
description = "Space before newline."
matcher = "regex"
unit = "document"
pattern = " \n"
severity = "warn"
provenance = "unvalidated"
category = "formatting"
`
	// The waiver covers only the following line and only the named rule.
	d := doc(t, "<!-- hapax:allow double-space reason=\"r\" -->\na  b \nc  d\n")
	report := ruleSet(t, src).Check(d, allowSuppression())

	var active []string
	for _, f := range report.Findings {
		active = append(active, f.RuleID)
	}
	// Line 2 double-space suppressed; its trailing space is not; line 3
	// double-space is beyond the waiver's reach.
	if !equalStrings(active, []string{"trailing-space", "double-space"}) {
		t.Errorf("active findings %v, want the trailing space on the waived line and the double space on the next line", active)
	}
}

func TestSuppressionOfAnUnknownRuleIsReported(t *testing.T) {
	d := doc(t, "<!-- hapax:allow no-such-rule reason=\"r\" -->\none  two")
	report := ruleSet(t, oneRule).Check(d, allowSuppression())
	if len(report.UnknownSuppressions) != 1 {
		t.Fatalf("%d unknown suppressions, want 1", len(report.UnknownSuppressions))
	}
	if report.Count() != 1 {
		t.Errorf("Count() = %d, want 1", report.Count())
	}
}

// Code fences need the structural tree, which does not exist. The limitation is
// reported rather than silently mishandled.
func TestCodeFenceAwarenessIsReportedAsUnavailable(t *testing.T) {
	report := ruleSet(t, oneRule).Check(doc(t, "one  two"), allowSuppression())
	if report.CodeFenceAwareness != tells.Unavailable {
		t.Errorf("CodeFenceAwareness = %q, want %q — suppressions inside code fences cannot yet be distinguished", report.CodeFenceAwareness, tells.Unavailable)
	}
}

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

func TestScopeIsMatchedNeverInferred(t *testing.T) {
	rule := func(id, extra string) string {
		return "\n[[rule]]\nid = \"" + id + "\"\ndescription = \"d\"\nmatcher = \"regex\"\nunit = \"document\"\n" +
			"pattern = \"x\"\nseverity = \"warn\"\nprovenance = \"unvalidated\"\ncategory = \"formatting\"\n" + extra
	}
	src := "version = \"t\"\n" +
		rule("universal", "") +
		rule("essays-only", "registers = [\"essays\"]\n") +
		rule("author-only", "authors = [\"allen\"]\n") +
		rule("both", "registers = [\"essays\"]\nauthors = [\"allen\"]\n")

	rs := ruleSet(t, src)
	d := doc(t, "x")
	fired := func(o tells.Options) []string {
		var ids []string
		for _, f := range rs.Check(d, o).Findings {
			ids = append(ids, f.RuleID)
		}
		return ids
	}

	if got := fired(tells.Options{}); !equalStrings(got, []string{"universal"}) {
		t.Errorf("unscoped: %v", got)
	}
	if got := fired(tells.Options{Register: "essays"}); !equalStrings(got, []string{"essays-only", "universal"}) {
		t.Errorf("register only: %v", got)
	}
	if got := fired(tells.Options{Author: "allen"}); !equalStrings(got, []string{"author-only", "universal"}) {
		t.Errorf("author only: %v", got)
	}
	// The conjunctive rule needs both to match.
	if got := fired(tells.Options{Register: "essays", Author: "allen"}); !equalStrings(got, []string{"author-only", "both", "essays-only", "universal"}) {
		t.Errorf("both: %v", got)
	}
	if got := fired(tells.Options{Register: "email", Author: "allen"}); !equalStrings(got, []string{"author-only", "universal"}) {
		t.Errorf("author matches but register does not: %v, want the conjunctive rule excluded", got)
	}
}

func TestDuplicateAndEmptyScopeValuesAreRejected(t *testing.T) {
	for name, extra := range map[string]string{
		"duplicate register": `registers = ["essays", "essays"]`,
		"empty register":     `registers = [""]`,
		"duplicate author":   `authors = ["allen", "allen"]`,
		"empty author":       `authors = [""]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tells.Load([]byte(oneRule + extra + "\n")); err == nil {
				t.Errorf("%s was accepted", name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The screening result model
// ---------------------------------------------------------------------------

// Three states, and none of them is "clean". Nothing here can establish that a
// document was written by a person.
func TestScreeningStatesAreThreeAndNoneIsClean(t *testing.T) {
	states := map[tells.Screening]bool{
		tells.ScreeningNotRun: true, tells.ScreeningIndeterminate: true, tells.ScreeningFlagged: true,
	}
	if len(states) != 3 {
		t.Fatal("screening states are not three distinct values")
	}
	for _, s := range []tells.Screening{tells.ScreeningNotRun, tells.ScreeningIndeterminate, tells.ScreeningFlagged} {
		if strings.Contains(strings.ToLower(string(s)), "clean") {
			t.Errorf("state %q claims cleanliness, which nothing here establishes", s)
		}
	}
}

// Unvalidated findings, however many, cannot flag a document.
func TestUnvalidatedFindingsProduceIndeterminate(t *testing.T) {
	report := ruleSet(t, oneRule).Check(doc(t, "a  b  c  d"), tells.Options{})
	if report.Screening != tells.ScreeningIndeterminate {
		t.Errorf("Screening = %q, want %q", report.Screening, tells.ScreeningIndeterminate)
	}
	if report.ScreeningReason == "" {
		t.Error("no reason given for withholding a verdict")
	}
	if got := report.CountByProvenance()[tells.Unvalidated]; got != 3 {
		t.Errorf("unvalidated findings = %d, want 3", got)
	}
}

// A formatting rule cannot flag a document however well derived it is. Only
// verdict-eligible categories can.
func TestFormattingCategoryIsNeverVerdictEligible(t *testing.T) {
	derivedFormatting := `
version = "t"
[[rule]]
id = "x"
description = "d"
matcher = "regex"
unit = "document"
pattern = "  "
severity = "error"
provenance = "derived"
category = "formatting"
[rule.evidence]
reference = "r"
population = "p"
digest = "d"
`
	report := ruleSet(t, derivedFormatting).Check(doc(t, "a  b"), tells.Options{})
	if report.Screening == tells.ScreeningFlagged {
		t.Error("a derived FORMATTING rule flagged the document; formatting is not evidence of machine authorship")
	}
}

func TestDerivedSourceContaminationRuleCanFlag(t *testing.T) {
	src := `
version = "t"
[[rule]]
id = "x"
description = "d"
matcher = "regex"
unit = "document"
pattern = "zzq"
severity = "error"
provenance = "derived"
category = "source-contamination"
[rule.evidence]
reference = "r"
population = "p"
digest = "d"
`
	rs := ruleSet(t, src)
	if got := rs.Check(doc(t, "no match here"), tells.Options{}).Screening; got != tells.ScreeningIndeterminate {
		t.Errorf("with no hits: %q, want %q — absence of a hit is not evidence of absence", got, tells.ScreeningIndeterminate)
	}
	if got := rs.Check(doc(t, "contains zzq here"), tells.Options{}).Screening; got != tells.ScreeningFlagged {
		t.Errorf("with a derived source-contamination hit: %q, want %q", got, tells.ScreeningFlagged)
	}
}

// ---------------------------------------------------------------------------
// The comparison ADR 0006 needs
// ---------------------------------------------------------------------------

// Comparison is SEVERITY-LEXICOGRAPHIC: fewer errors wins outright, and only on
// a tie do warnings decide, then infos. A componentwise "no level may increase"
// rule inverts the question, making four new infos worse than one new error.
func TestComparisonIsSeverityLexicographic(t *testing.T) {
	rs := ruleSet(t, derivedSeverities)
	oneError := rs.Check(doc(t, "SEVERE"), tells.Options{}).Comparison()
	fourInfos := rs.Check(doc(t, "minor minor minor minor"), tells.Options{}).Comparison()

	if !fourInfos.NoWorseThan(oneError) {
		t.Error("four info findings were judged worse than one error finding")
	}
	if oneError.NoWorseThan(fourInfos) {
		t.Error("one error finding was judged no worse than four info findings")
	}
}

// Only DERIVED findings enter the gate. An unvalidated rule that could veto a
// rewrite would be making exactly the claim its provenance denies.
func TestOnlyDerivedFindingsEnterTheAcceptanceGate(t *testing.T) {
	unvalidated := strings.ReplaceAll(derivedSeverities, `provenance = "derived"`, `provenance = "unvalidated"`)
	unvalidated = stripEvidenceTables(unvalidated)

	rs := ruleSet(t, unvalidated)
	worse := rs.Check(doc(t, "SEVERE SEVERE"), tells.Options{}).Comparison()
	better := rs.Check(doc(t, "clean text"), tells.Options{}).Comparison()

	if !worse.NoWorseThan(better) {
		t.Error("unvalidated findings blocked acceptance; a rule that establishes nothing cannot veto a rewrite")
	}
	if worse.Findings() != 0 {
		t.Errorf("comparison counted %d unvalidated findings, want 0", worse.Findings())
	}
}

// Formatting is verdict-ineligible and must not enter the gate either.
func TestFormattingFindingsDoNotEnterTheAcceptanceGate(t *testing.T) {
	src := strings.ReplaceAll(derivedSeverities, `category = "author-deviation"`, `category = "formatting"`)
	rs := ruleSet(t, src)
	worse := rs.Check(doc(t, "SEVERE SEVERE"), tells.Options{}).Comparison()
	if worse.Findings() != 0 {
		t.Errorf("comparison counted %d formatting findings, want 0", worse.Findings())
	}
}

// A truncated report is a lower bound, not a count.
func TestTruncatedReportsCannotBeCompared(t *testing.T) {
	rs := ruleSet(t, derivedSeverities)
	full := rs.Check(doc(t, "SEVERE"), tells.Options{}).Comparison()
	capped := rs.Check(doc(t, strings.Repeat("SEVERE ", 200)), tells.Options{MaxFindings: 5}).Comparison()

	if capped.Comparable(full) || full.Comparable(capped) {
		t.Error("a truncated report was treated as comparable; its count is only a lower bound")
	}
}

// Suppression must be off on both sides, or a candidate wins by writing a
// comment that waives its own findings.
func TestComparisonRefusesWhenSuppressionWasEnabled(t *testing.T) {
	rs := ruleSet(t, derivedSeverities)
	plain := rs.Check(doc(t, "SEVERE"), tells.Options{}).Comparison()
	waived := rs.Check(doc(t, "SEVERE"), allowSuppression()).Comparison()
	if plain.Comparable(waived) {
		t.Error("a report produced with suppression enabled was treated as comparable")
	}
}

func TestComparisonRefusesMismatchedRuleSetsAndOptions(t *testing.T) {
	a := ruleSet(t, derivedSeverities).Check(doc(t, "SEVERE"), tells.Options{}).Comparison()
	b := ruleSet(t, strings.Replace(derivedSeverities, `id = "severe"`, `id = "severe2"`, 1)).
		Check(doc(t, "SEVERE"), tells.Options{}).Comparison()
	if a.Comparable(b) {
		t.Error("comparisons from different rule sets were treated as comparable")
	}

	scoped := ruleSet(t, derivedSeverities).Check(doc(t, "SEVERE"), tells.Options{Register: "essays"}).Comparison()
	if a.Comparable(scoped) {
		t.Error("comparisons from different options were treated as comparable")
	}
}

// ---------------------------------------------------------------------------
// The default rule set
// ---------------------------------------------------------------------------

func TestDefaultRuleSetClaimsNothingItHasNotEarned(t *testing.T) {
	rs := tells.Default()
	if rs.Version == "" || rs.Digest == "" {
		t.Error("the default rule set has no version or digest")
	}
	if len(rs.Rules) == 0 {
		t.Fatal("the default rule set is empty; the component would be untestable in use")
	}
	for _, r := range rs.Rules {
		if r.Provenance == tells.Derived {
			t.Errorf("%s claims to be derived, but issue #4's golden set does not exist, so nothing has been derived from anything", r.ID)
		}
		if r.Category == tells.SourceContamination {
			t.Errorf("%s claims to detect source contamination while unvalidated; that category can flag a document and must be earned", r.ID)
		}
		if r.Description == "" {
			t.Errorf("%s has no description", r.ID)
		}
	}
	// It follows that the default set can never flag anything.
	if got := rs.Check(doc(t, "one  two  three"), tells.Options{}).Screening; got == tells.ScreeningFlagged {
		t.Error("the default rule set flagged a document; every rule in it is unvalidated")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestEmptyDocumentAndEmptyRuleSet(t *testing.T) {
	if got := ruleSet(t, oneRule).Check(doc(t, ""), tells.Options{}).Count(); got != 0 {
		t.Errorf("empty document produced %d findings", got)
	}
	empty := ruleSet(t, "version = \"t\"")
	report := empty.Check(doc(t, "one  two"), tells.Options{})
	if report.Count() != 0 {
		t.Errorf("empty rule set produced %d findings", report.Count())
	}
	if report.Screening != tells.ScreeningIndeterminate {
		t.Errorf("empty rule set: Screening = %q, want %q — running no rules decides nothing", report.Screening, tells.ScreeningIndeterminate)
	}
}

func TestCountIsStableAcrossRuns(t *testing.T) {
	d := doc(t, "one  two  three")
	rs := ruleSet(t, oneRule)
	first := rs.Check(d, tells.Options{}).Count()
	for i := 0; i < 3; i++ {
		if again := rs.Check(d, tells.Options{}).Count(); again != first {
			t.Fatalf("count changed between runs: %d then %d", first, again)
		}
	}
}

// stripEvidenceTables removes the [rule.evidence] blocks, since an unvalidated
// rule must not carry evidence.
func stripEvidenceTables(src string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(src, "\n") {
		switch {
		case strings.HasPrefix(line, "[rule.evidence]"):
			skip = true
		case skip && (strings.HasPrefix(line, "reference") || strings.HasPrefix(line, "population") || strings.HasPrefix(line, "digest")):
		default:
			skip = false
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
