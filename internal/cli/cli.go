// Package cli owns Hapax's command surface, output schema, and exit codes.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/fissible/hapax/internal/mode"
	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/text"
)

// Schema is the versioned JSON envelope emitted for completed commands.
const Schema = "hapax.v1"

// Status classifies a completed command result.
type Status string

const (
	StatusOK      Status = "ok"
	StatusAdverse Status = "adverse"
	StatusRefused Status = "refused"
)

// Reason classifies a refusal.
type Reason string

const (
	ReasonUncalibrated             Reason = "uncalibrated"
	ReasonInsufficientEvidence     Reason = "insufficient-evidence"
	ReasonStaleExemplars           Reason = "stale-exemplars"
	ReasonLocalOnlyForbidsProvider Reason = "local-only-forbids-provider"
	ReasonNoProfile                Reason = "no-profile"
)

var statuses = []Status{StatusOK, StatusAdverse, StatusRefused}
var reasons = []Reason{
	ReasonUncalibrated,
	ReasonInsufficientEvidence,
	ReasonStaleExemplars,
	ReasonLocalOnlyForbidsProvider,
	ReasonNoProfile,
}
var commands = []string{"tells"}

// Reasons returns the closed refusal vocabulary.
func Reasons() []Reason { return append([]Reason(nil), reasons...) }

// Statuses returns the closed result-status vocabulary.
func Statuses() []Status { return append([]Status(nil), statuses...) }

// Commands returns the commands implemented by this binary.
func Commands() []string { return append([]string(nil), commands...) }

// TellsFinding is the CLI projection of one tells finding.
type TellsFinding struct {
	Rule       string `json:"rule"`
	Category   string `json:"category"`
	Provenance string `json:"provenance"`
	Severity   string `json:"severity"`
	Reason     string `json:"reason"`
	Offset     int    `json:"offset"`
	Length     int    `json:"length"`
}

// TellsResult is the complete CLI projection of a tells report.
type TellsResult struct {
	Path       string         `json:"path"`
	Screening  string         `json:"screening"`
	Count      int            `json:"count"`
	Findings   []TellsFinding `json:"findings"`
	Suppressed int            `json:"suppressed"`
	Truncated  bool           `json:"truncated"`
}

// Document is the shared output envelope for completed commands.
type Document struct {
	Schema  string  `json:"schema"`
	Command string  `json:"command"`
	Status  Status  `json:"status"`
	Reason  Reason  `json:"reason"`
	Profile *string `json:"profile"`
	// Result widens when a second command lands.
	Result TellsResult `json:"result"`
}

// Render validates and writes this document in the requested representation.
func (d Document) Render(w io.Writer, asJSON bool) error {
	if err := d.valid(); err != nil {
		return err
	}
	if asJSON {
		encoded, err := json.Marshal(d)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(encoded))
		return err
	}

	profile := ""
	if d.Profile != nil {
		profile = fmt.Sprintf(" profile=%s", *d.Profile)
	}
	if _, err := fmt.Fprintf(w, "%s %s%s", d.Command, d.Status, profile); err != nil {
		return err
	}
	if d.Reason != "" {
		if _, err := fmt.Fprintf(w, " reason=%s", d.Reason); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, " findings=%d\n", d.Result.Count)
	return err
}

func (d Document) valid() error {
	if d.Schema != Schema {
		return errors.New("incoherent document schema")
	}
	if !contains(Commands(), d.Command) {
		return errors.New("incoherent document command")
	}
	if !contains(Statuses(), d.Status) {
		return errors.New("incoherent document status")
	}
	if (d.Status == StatusRefused) != (d.Reason != "") {
		return errors.New("incoherent document reason")
	}
	if d.Reason != "" && !contains(Reasons(), d.Reason) {
		return errors.New("incoherent document reason")
	}
	return validTellsResult(d.Result)
}

func validTellsResult(result TellsResult) error {
	if !contains(tells.Screenings(), tells.Screening(result.Screening)) {
		return errors.New("incoherent tells result screening")
	}
	if result.Count != len(result.Findings) {
		return errors.New("incoherent tells result count")
	}
	if result.Suppressed < 0 {
		return errors.New("incoherent tells result suppressed")
	}
	for _, finding := range result.Findings {
		if finding.Offset < 0 || finding.Length <= 0 ||
			!contains(tells.Severities(), tells.Severity(finding.Severity)) ||
			!contains(tells.Provenances(), tells.Provenance(finding.Provenance)) ||
			!contains(tells.Categories(), tells.Category(finding.Category)) {
			return errors.New("incoherent tells finding")
		}
	}
	return nil
}

func contains[T comparable](values []T, wanted T) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Deps provides the composition-root seams that A1 is allowed to use.
type Deps struct {
	Stdout   io.Writer
	Stderr   io.Writer
	Env      func(string) (string, bool)
	Now      func() time.Time
	ReadFile func(string) ([]byte, error)
}

// Run executes one command and returns its process exit code.
func Run(ctx context.Context, args []string, deps Deps) int {
	// A1 has no cancellable work; retain ctx so later commands need not change Run.
	_ = ctx
	parsed, parseErr := parse(args)
	modeValue, modeErr := mode.Resolve(parsed.localOnly, deps.Env)
	// A1 has no provider to configure from the resolved mode.
	_ = modeValue
	if modeErr != nil {
		diagnostic(deps.Stderr, "invalid HAPAX_LOCAL_ONLY")
		return 2
	}
	if parseErr != nil {
		diagnostic(deps.Stderr, parseErr.Error())
		return 2
	}

	raw, err := deps.ReadFile(parsed.path)
	if err != nil {
		diagnostic(deps.Stderr, fmt.Sprintf("cannot read draft %q: %q", parsed.path, err.Error()))
		return 3
	}
	doc, err := text.Admit(raw)
	if err != nil {
		diagnostic(deps.Stderr, fmt.Sprintf("cannot admit draft %q: %q", parsed.path, err.Error()))
		return 3
	}
	report := tells.Default().Check(doc, tells.Options{})
	result := tellsResultFrom(parsed.path, report)
	status := StatusOK
	code := 0
	if result.Count > 0 {
		status, code = StatusAdverse, 1
	}
	if err := (Document{Schema: Schema, Command: "tells", Status: status, Result: result}).Render(deps.Stdout, parsed.json); err != nil {
		diagnostic(deps.Stderr, fmt.Sprintf("cannot write result: %q", err.Error()))
		return 3
	}
	return code
}

type invocation struct {
	json      bool
	localOnly bool
	path      string
}

func parse(args []string) (invocation, error) {
	var positional []string
	flags := true
	result := invocation{}
	for _, arg := range args {
		if flags && arg == "--" {
			flags = false
			continue
		}
		if flags && strings.HasPrefix(arg, "-") {
			switch arg {
			case "--json":
				result.json = true
			case "--local-only":
				result.localOnly = true
			default:
				return invocation{}, fmt.Errorf("invalid flag %q", arg)
			}
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) == 0 {
		return invocation{}, errors.New("missing command")
	}
	if positional[0] != "tells" {
		available := Commands()
		sort.Strings(available)
		return invocation{}, fmt.Errorf("unknown command %q (available: %s)", positional[0], strings.Join(available, ", "))
	}
	if len(positional) != 2 {
		return invocation{}, errors.New("tells requires exactly one file operand")
	}
	result.path = positional[1]
	return result, nil
}

func diagnostic(w io.Writer, message string) {
	fmt.Fprintln(w, message)
}

// tellsResultFrom converts a tells report without changing its finding order.
func tellsResultFrom(path string, report tells.Report) TellsResult {
	result := TellsResult{
		Path: path, Screening: string(report.Screening), Count: len(report.Findings),
		Findings: make([]TellsFinding, 0, len(report.Findings)), Suppressed: len(report.Suppressed), Truncated: report.Truncated,
	}
	for _, finding := range report.Findings {
		result.Findings = append(result.Findings, TellsFinding{
			Rule: finding.RuleID, Category: string(finding.Category), Provenance: string(finding.Provenance),
			Severity: string(finding.Severity), Reason: finding.Reason, Offset: finding.Span.Offset, Length: finding.Span.Length,
		})
	}
	return result
}
