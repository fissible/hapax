package cli

import (
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/text"
)

// cli does NOT sort. tells orders its findings by offset, then span length,
// then rule ID, and its own frozen test pins the shorter match first. A second
// ordering at this layer would differ from the library exactly in the
// shared-offset unequal-length case — the one place anyone would notice.
func TestFindingsKeepTheOrderTellsProduced(t *testing.T) {
	report := tells.Report{Findings: []tells.Finding{
		{RuleID: "zzz-short", Span: text.Span{Offset: 4, Length: 2},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
		{RuleID: "aaa-long", Span: text.Span{Offset: 4, Length: 9},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
		{RuleID: "mmm-later", Span: text.Span{Offset: 20, Length: 2},
			Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
	}}

	got := tellsResultFrom("draft.md", report)
	want := []string{"zzz-short", "aaa-long", "mmm-later"}
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

// Every vocabulary field on EVERY finding comes from the owning package's
// declared set, checked per finding rather than in aggregate, so one stray
// value cannot hide behind the others.
func TestEveryFindingCarriesDeclaredVocabulary(t *testing.T) {
	report := tells.Report{
		Screening: tells.ScreeningFlagged,
		Findings: []tells.Finding{
			{RuleID: "one", Span: text.Span{Offset: 0, Length: 2},
				Severity: "warn", Provenance: "unvalidated", Category: "formatting"},
		},
	}
	got := tellsResultFrom("draft.md", report)

	if !inSet(got.Screening, stringsOfScreenings()) {
		t.Errorf("screening %q is not a declared value", got.Screening)
	}
	for i, finding := range got.Findings {
		if !inSet(finding.Severity, stringsOf(tells.Severities())) {
			t.Errorf("finding %d severity %q is not declared", i, finding.Severity)
		}
		if !inSet(finding.Provenance, stringsOf(tells.Provenances())) {
			t.Errorf("finding %d provenance %q is not declared", i, finding.Provenance)
		}
		if !inSet(finding.Category, stringsOf(tells.Categories())) {
			t.Errorf("finding %d category %q is not declared", i, finding.Category)
		}
	}
}

// And the vocabularies cli accepts are exactly the ones tells declares, so the
// two cannot drift apart without this failing.
func TestTheAcceptedVocabulariesAreTellsOwn(t *testing.T) {
	for name, pair := range map[string][2][]string{
		"screening":  {acceptedScreenings(), stringsOfScreenings()},
		"severity":   {acceptedSeverities(), stringsOf(tells.Severities())},
		"provenance": {acceptedProvenances(), stringsOf(tells.Provenances())},
		"category":   {acceptedCategories(), stringsOf(tells.Categories())},
	} {
		if !reflect.DeepEqual(sorted(pair[0]), sorted(pair[1])) {
			t.Errorf("%s: cli accepts %v, tells declares %v", name, sorted(pair[0]), sorted(pair[1]))
		}
	}
}

// ---------------------------------------------------------------------------

func stringsOf[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func stringsOfScreenings() []string { return stringsOf(tells.Screenings()) }

func inSet(value string, set []string) bool {
	for _, member := range set {
		if value == member {
			return true
		}
	}
	return false
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
