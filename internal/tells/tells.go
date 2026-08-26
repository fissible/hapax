// Package tells implements deterministic, provenance-carrying text checks.
package tells

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/fissible/hapax/internal/text"
)

type Severity string

const (
	Info  Severity = "info"
	Warn  Severity = "warn"
	Error Severity = "error"
)

type Provenance string

const (
	Derived     Provenance = "derived"
	Unvalidated Provenance = "unvalidated"
	UserDefined Provenance = "user-defined"
)

type Category string

const (
	Formatting          Category = "formatting"
	AuthorDeviation     Category = "author-deviation"
	SourceContamination Category = "source-contamination"
)

type MatcherKind string

const (
	Regex      MatcherKind = "regex"
	Structural MatcherKind = "structural"
)

type Unit string

const (
	Document  Unit = "document"
	Paragraph Unit = "paragraph"
	Sentence  Unit = "sentence"
	Line      Unit = "line"
)

type Evidence struct {
	Reference  string `toml:"reference"`
	Population string `toml:"population"`
	Digest     string `toml:"digest"`
}
type Rule struct {
	ID          string      `toml:"id"`
	Description string      `toml:"description"`
	Matcher     MatcherKind `toml:"matcher"`
	Unit        Unit        `toml:"unit"`
	Pattern     string      `toml:"pattern"`
	Severity    Severity    `toml:"severity"`
	Provenance  Provenance  `toml:"provenance"`
	Category    Category    `toml:"category"`
	Evidence    *Evidence   `toml:"evidence"`
	Registers   []string    `toml:"registers"`
	Authors     []string    `toml:"authors"`
	re          *regexp.Regexp
}
type RuleSet struct {
	Version string `toml:"version"`
	Digest  string `toml:"-"`
	Rules   []Rule `toml:"rule"`
}
type Options struct {
	Register, Author   string
	HonourSuppressions bool
	MaxFindings        int
}

type Finding struct {
	RuleID        string
	Span          text.Span
	Contributing  []text.Span
	SpanWidened   bool
	Severity      Severity
	Provenance    Provenance
	Category      Category
	Reason        string
	DirectiveSpan text.Span
}
type Suppression struct {
	RuleID, Reason string
	DirectiveSpan  text.Span
}
type Screening string

const (
	ScreeningNotRun        Screening = "not-run"
	ScreeningIndeterminate Screening = "indeterminate"
	ScreeningFlagged       Screening = "flagged"
)

type CodeFenceAwareness string

const Unavailable CodeFenceAwareness = "unavailable"

type Report struct {
	Screening             Screening
	ScreeningReason       string
	Findings              []Finding
	Suppressed            []Finding
	UnknownSuppressions   []Suppression
	MalformedSuppressions []Suppression
	Truncated             bool
	CodeFenceAwareness    CodeFenceAwareness
	comparison            Comparison
}

func (r Report) Count() int { return len(r.Findings) }
func (r Report) CountByProvenance() map[Provenance]int {
	m := map[Provenance]int{}
	for _, f := range r.Findings {
		m[f.Provenance]++
	}
	return m
}
func (r Report) Comparison() Comparison { return r.comparison }

// Comparison is a report's acceptance-gate projection.
type Comparison struct {
	digest    string
	options   Options
	truncated bool
	counts    [3]int
}

func (c Comparison) Findings() int { return c.counts[0] + c.counts[1] + c.counts[2] }
func (c Comparison) Comparable(other Comparison) bool {
	return c.digest != "" && c.digest == other.digest && c.options == other.options && !c.options.HonourSuppressions && !other.options.HonourSuppressions && !c.truncated && !other.truncated
}

var ErrIncomparable = errors.New("tells comparisons are not comparable")

// Compare applies the acceptance ordering only to comparable reports.
func (c Comparison) Compare(other Comparison) (int, error) {
	if !c.Comparable(other) {
		return 0, ErrIncomparable
	}
	for i := range c.counts {
		if c.counts[i] < other.counts[i] {
			return -1, nil
		}
		if c.counts[i] > other.counts[i] {
			return 1, nil
		}
	}
	return 0, nil
}

// NoWorseThan is retained for compatibility. Incomparable reports are never
// accepted; new acceptance gates should use Compare and handle its error.
func (c Comparison) NoWorseThan(other Comparison) bool {
	comparison, err := c.Compare(other)
	return err == nil && comparison <= 0
}

// Load reads TOML rules and rejects every key it does not understand, rather
// than treating a typo as a silently weaker rule.
func Load(src []byte) (*RuleSet, error) {
	var rs RuleSet
	metadata, err := toml.Decode(string(src), &rs)
	if err != nil {
		return nil, err
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return nil, fmt.Errorf("unknown TOML key %q", undecoded[0])
	}
	if rs.Version == "" {
		return nil, fmt.Errorf("version is required")
	}
	ids := map[string]bool{}
	for i := range rs.Rules {
		if err := validateRule(&rs.Rules[i]); err != nil {
			return nil, err
		}
		if ids[rs.Rules[i].ID] {
			return nil, fmt.Errorf("duplicate rule ID %q", rs.Rules[i].ID)
		}
		ids[rs.Rules[i].ID] = true
	}
	rs.Digest = ruleSetDigest(&rs)
	return &rs, nil
}

func ruleSetDigest(rs *RuleSet) string {
	h := sha256.New()
	frame := func(parts ...string) {
		for _, part := range parts {
			fmt.Fprintf(h, "%d:", len(part))
			h.Write([]byte(part))
		}
	}
	frame("version", rs.Version)
	rules := append([]Rule(nil), rs.Rules...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	for _, r := range rules {
		frame("rule", r.ID, r.Description, string(r.Matcher), string(r.Unit), r.Pattern, string(r.Severity), string(r.Provenance), string(r.Category))
		for _, scope := range [][]string{r.Registers, r.Authors} {
			values := append([]string(nil), scope...)
			sort.Strings(values)
			frame("scope", fmt.Sprint(len(values)))
			for _, value := range values {
				frame("value", value)
			}
		}
		if r.Evidence == nil {
			frame("evidence", "none")
		} else {
			frame("evidence", r.Evidence.Reference, r.Evidence.Population, r.Evidence.Digest)
		}
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
func validateRule(r *Rule) error {
	if r.ID == "" || r.Description == "" || r.Pattern == "" {
		return fmt.Errorf("rule requires id, description, and pattern")
	}
	if r.Matcher != Regex && r.Matcher != Structural {
		return fmt.Errorf("unknown matcher %q", r.Matcher)
	}
	if r.Matcher == Structural {
		return fmt.Errorf("structural matcher is unimplemented")
	}
	if r.Unit != Document && r.Unit != Paragraph && r.Unit != Sentence && r.Unit != Line {
		return fmt.Errorf("unknown unit %q", r.Unit)
	}
	if r.Severity != Info && r.Severity != Warn && r.Severity != Error {
		return fmt.Errorf("unknown severity %q", r.Severity)
	}
	if r.Provenance != Derived && r.Provenance != Unvalidated && r.Provenance != UserDefined {
		return fmt.Errorf("unknown provenance %q", r.Provenance)
	}
	if r.Category != Formatting && r.Category != AuthorDeviation && r.Category != SourceContamination {
		return fmt.Errorf("unknown category %q", r.Category)
	}
	if r.Provenance == Derived {
		if r.Evidence == nil || r.Evidence.Reference == "" || r.Evidence.Population == "" || r.Evidence.Digest == "" {
			return fmt.Errorf("derived rule %q requires complete evidence", r.ID)
		}
	} else if r.Evidence != nil {
		return fmt.Errorf("non-derived rule %q must not carry evidence", r.ID)
	}
	for _, a := range [][]string{r.Registers, r.Authors} {
		seen := map[string]bool{}
		for _, x := range a {
			if x == "" || seen[x] {
				return fmt.Errorf("scope values must be non-empty and unique")
			}
			seen[x] = true
		}
	}
	re, e := regexp.Compile(r.Pattern)
	if e != nil {
		return e
	}
	// A TOML basic string spells a regex word-boundary as `"\b"`; TOML
	// decodes that escape to U+0008 before it reaches RE2. Treat it as the
	// intended zero-width boundary rather than accepting a surprising control
	// character matcher.
	if strings.ContainsRune(r.Pattern, '\b') || matchesEmpty(re) {
		return fmt.Errorf("pattern %q matches empty string", r.Pattern)
	}
	r.re = re
	return nil
}

func matchesEmpty(re *regexp.Regexp) bool {
	for _, probe := range []string{"", "x", " ", "\n", "ax"} {
		if match := re.FindStringIndex(probe); match != nil && match[0] == match[1] {
			return true
		}
	}
	return false
}

func (rs *RuleSet) Check(doc *text.Document, options Options) Report {
	report := Report{Screening: ScreeningIndeterminate, ScreeningReason: "no derived verdict-eligible finding establishes a verdict", CodeFenceAwareness: Unavailable}
	raw := string(doc.Raw())
	known := map[string]bool{}
	for _, r := range rs.Rules {
		known[r.ID] = true
	}
	suppressions := parseSuppressions(raw, known, options, &report)
	limit := options.MaxFindings
	stopped := false
	for _, r := range rs.Rules {
		if !inScope(r, options) {
			continue
		}
		for _, m := range r.re.FindAllStringIndex(raw, -1) {
			if limit > 0 && len(report.Findings)+len(report.Suppressed) >= limit {
				report.Truncated = true
				stopped = true
				break
			}
			original := text.Span{Offset: m[0], Length: m[1] - m[0]}
			span := doc.Snap(original)
			f := Finding{RuleID: r.ID, Span: span, Contributing: []text.Span{}, SpanWidened: span != original, Severity: r.Severity, Provenance: r.Provenance, Category: r.Category}
			if s, ok := suppressionFor(suppressions, r.ID, m[0]); ok {
				f.Reason = s.Reason
				f.DirectiveSpan = s.DirectiveSpan
				report.Suppressed = append(report.Suppressed, f)
			} else {
				report.Findings = append(report.Findings, f)
			}
		}
		if stopped {
			break
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if a.Span.Offset != b.Span.Offset {
			return a.Span.Offset < b.Span.Offset
		}
		if a.Span.Length != b.Span.Length {
			return a.Span.Length < b.Span.Length
		}
		return a.RuleID < b.RuleID
	})
	// A longer match at an offset owns the bytes that follow it. Matches which
	// start together remain useful independent findings; this prevents a later,
	// overlapping match from being reported merely because another rule happened
	// to have a shorter expression.
	var filtered []Finding
	groupStart, coveredEnd := -1, -1
	for _, f := range report.Findings {
		if f.Span.Offset != groupStart {
			groupStart = f.Span.Offset
			if f.Span.Offset < coveredEnd {
				continue
			}
		}
		end := f.Span.Offset + f.Span.Length
		if end > coveredEnd {
			coveredEnd = end
		}
		filtered = append(filtered, f)
	}
	report.Findings = filtered
	for _, f := range report.Findings {
		if f.Provenance == Derived && eligible(f.Category) {
			report.Screening = ScreeningFlagged
			report.ScreeningReason = "derived verdict-eligible rule matched"
			break
		}
	}
	c := Comparison{digest: rs.Digest, options: options, truncated: report.Truncated}
	for _, f := range report.Findings {
		if f.Provenance == Derived && eligible(f.Category) {
			switch f.Severity {
			case Error:
				c.counts[0]++
			case Warn:
				c.counts[1]++
			case Info:
				c.counts[2]++
			}
		}
	}
	report.comparison = c
	return report
}
func inScope(r Rule, o Options) bool {
	return (len(r.Registers) == 0 || contains(r.Registers, o.Register)) && (len(r.Authors) == 0 || contains(r.Authors, o.Author))
}
func contains(a []string, x string) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}
func eligible(c Category) bool { return c == AuthorDeviation || c == SourceContamination }

type scopedSuppression struct {
	Suppression
	start, end int
}

func parseSuppressions(raw string, known map[string]bool, o Options, report *Report) []scopedSuppression {
	if !o.HonourSuppressions {
		return nil
	}
	re := regexp.MustCompile(`<!--\s*hapax:allow\s+([^\s>]+)(?:\s+reason="([^"]*)")?\s*-->`)
	var out []scopedSuppression
	for _, m := range re.FindAllStringSubmatchIndex(raw, -1) {
		s := Suppression{RuleID: raw[m[2]:m[3]], DirectiveSpan: text.Span{Offset: m[0], Length: m[1] - m[0]}}
		if len(m) >= 6 && m[4] >= 0 {
			s.Reason = raw[m[4]:m[5]]
		}
		if s.Reason == "" {
			report.MalformedSuppressions = append(report.MalformedSuppressions, s)
			continue
		}
		if !known[s.RuleID] {
			report.UnknownSuppressions = append(report.UnknownSuppressions, s)
			continue
		}
		lineEnd := strings.IndexByte(raw[m[1]:], '\n')
		if lineEnd < 0 {
			continue
		}
		start := m[1] + lineEnd + 1
		end := len(raw)
		if p := strings.IndexByte(raw[start:], '\n'); p >= 0 {
			end = start + p
		}
		out = append(out, scopedSuppression{Suppression: s, start: start, end: end})
	}
	return out
}
func suppressionFor(ss []scopedSuppression, id string, offset int) (Suppression, bool) {
	for _, s := range ss {
		if s.RuleID == id && offset >= s.start && offset < s.end {
			return s.Suppression, true
		}
	}
	return Suppression{}, false
}

//go:embed default.toml
var defaultTOML []byte

// Default returns the embedded, reviewable rule set. The file is supplied by
// this package and is validated by the same loader as caller-provided rules.
func Default() *RuleSet {
	rs, _ := Load(defaultTOML)
	return rs
}
