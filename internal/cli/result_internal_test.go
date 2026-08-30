package cli

import (
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/text"
)

// cli does NOT sort. tells orders its findings by offset, then span length,
// then rule ID, and its own frozen test pins the shorter match first. A second
// ordering at this layer would differ from the library exactly in the
// shared-offset unequal-length case — the one place anyone would notice.
func TestFindingsKeepTheOrderTellsProduced(t *testing.T) {
	// Deliberately NOT in the order tells would produce. A converter that
	// re-sorted into the same ordering would be indistinguishable from one that
	// passes through, if the input were already canonical.
	report := tells.Report{Findings: []tells.Finding{
		{RuleID: "mmm-later", Span: text.Span{Offset: 20, Length: 2},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
		{RuleID: "aaa-long", Span: text.Span{Offset: 4, Length: 9},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
		{RuleID: "zzz-short", Span: text.Span{Offset: 4, Length: 2},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
	}}

	got := tellsResultFrom("draft.md", report)
	want := []string{"mmm-later", "aaa-long", "zzz-short"}
	rules := make([]string, len(got.Findings))
	for i, finding := range got.Findings {
		rules[i] = finding.Rule
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("rules =\n%v\nwant the order tells produced\n%v", rules, want)
	}
}

// The count is the list, not a number beside it.
func TestTheCountIsTheFindings(t *testing.T) {
	report := tells.Report{Findings: []tells.Finding{
		{RuleID: "one", Span: text.Span{Offset: 0, Length: 2},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
		{RuleID: "two", Span: text.Span{Offset: 5, Length: 2},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
	}}
	got := tellsResultFrom("draft.md", report)
	if got.Count != len(got.Findings) || got.Count != 2 {
		t.Errorf("count = %d over %d findings", got.Count, len(got.Findings))
	}
}

// The path is the operand as typed. Normalising it would echo back something
// the user did not write.
func TestThePathIsVerbatim(t *testing.T) {
	for _, path := range []string{"draft.md", "./draft.md", "../up/draft.md", "a//b.md"} {
		if got := tellsResultFrom(path, tells.Report{}).Path; got != path {
			t.Errorf("path = %q, want %q", got, path)
		}
	}
}

// A report with nothing in it renders an empty list, not a null one, so a
// consumer can iterate without a nil check.
func TestNoFindingsIsAnEmptyList(t *testing.T) {
	got := tellsResultFrom("draft.md", tells.Report{})
	if got.Findings == nil {
		t.Error("findings is null, want an empty list")
	}
	if got.Count != 0 {
		t.Errorf("count = %d, want 0", got.Count)
	}
}

// Every field is carried across, not merely a plausible value produced. A
// converter that hard-coded "warn" would satisfy a membership check.
func TestEveryFieldOfAFindingIsCarriedAcross(t *testing.T) {
	report := tells.Report{
		Screening: tells.ScreeningFlagged,
		Truncated: true,
		Findings: []tells.Finding{
			{RuleID: "double-space", Span: text.Span{Offset: 17, Length: 5},
				Severity: "error", Provenance: "validated", Category: "lexis",
				Reason: "a rule-authored explanation"},
		},
		Suppressed: []tells.Finding{
			{RuleID: "other", Span: text.Span{Offset: 1, Length: 1}},
		},
	}

	got := tellsResultFrom("draft.md", report)
	if got.Screening != string(tells.ScreeningFlagged) {
		t.Errorf("screening = %q, want %q", got.Screening, tells.ScreeningFlagged)
	}
	if !got.Truncated {
		t.Error("truncated was not carried across")
	}
	if got.Suppressed != len(report.Suppressed) {
		t.Errorf("suppressed = %d, want %d", got.Suppressed, len(report.Suppressed))
	}
	want := TellsFinding{
		Rule: "double-space", Category: "lexis", Provenance: "validated",
		Severity: "error", Reason: "a rule-authored explanation",
		Offset: 17, Length: 5,
	}
	if len(got.Findings) != 1 || got.Findings[0] != want {
		t.Errorf("finding =\n%+v\nwant\n%+v", got.Findings, want)
	}
}
