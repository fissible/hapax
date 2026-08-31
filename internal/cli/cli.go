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
	"github.com/fissible/hapax/internal/workflow"
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
	ReasonNoReference              Reason = "no-reference"
	ReasonAmbiguousReference       Reason = "ambiguous-reference"
)

var statuses = []Status{StatusOK, StatusAdverse, StatusRefused}
var reasons = []Reason{
	ReasonUncalibrated,
	ReasonInsufficientEvidence,
	ReasonStaleExemplars,
	ReasonLocalOnlyForbidsProvider,
	ReasonNoProfile,
	ReasonNoReference, ReasonAmbiguousReference,
}
var commands = []string{"eval", "index", "profile", "score", "tells"}

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
	// Result is the command-selected payload. The command discriminator keeps
	// existing hapax.v1 tells consumers reading result.path unchanged.
	Result any `json:"result"`
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
	if result := humanResult(d.Result); result != "" {
		if _, err := fmt.Fprintf(w, " %s", result); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
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
	switch result := d.Result.(type) {
	case TellsResult:
		if d.Command != "tells" {
			return errors.New("incoherent document result")
		}
		return validTellsResult(result)
	case IndexResult:
		if d.Command != "index" {
			return errors.New("incoherent document result")
		}
		if d.Status == StatusRefused || d.Profile == nil || *d.Profile == "" {
			return errors.New("incoherent index document")
		}
		return validIndexResult(d.Status, result)
	case ProfileResult:
		if d.Command != "profile" {
			return errors.New("incoherent document result")
		}
		return validProfileResult(d.Status, d.Reason, d.Profile, result)
	case EvalResult:
		if d.Command != "eval" {
			return errors.New("incoherent document result")
		}
		return validEvalResult(d.Status, d.Reason, result)
	case ScoreResult:
		if d.Command != "score" {
			return errors.New("incoherent document result")
		}
		return validScoreResult(d.Status, d.Reason, result)
	default:
		return errors.New("incoherent document result")
	}
}

func validScoreResult(status Status, reason Reason, r ScoreResult) error {
	if r.Calibrated != (r.ReleaseID != nil) {
		return errors.New("incoherent score calibration")
	}
	if r.ParagraphsBelowFloor < 0 {
		return errors.New("incoherent score paragraph floor")
	}
	identified := r.ProfileID != nil && *r.ProfileID != "" && r.ReferenceID != nil && *r.ReferenceID != ""
	switch reason {
	case "":
		if !r.Calibrated || !identified || len(r.Segments) == 0 {
			return errors.New("incoherent score result")
		}
	case ReasonUncalibrated:
		if r.Calibrated || !identified || len(r.Segments) == 0 {
			return errors.New("incoherent score refusal")
		}
	case ReasonInsufficientEvidence:
		if len(r.Segments) != 0 {
			return errors.New("incoherent score refusal")
		}
	case ReasonNoProfile, ReasonNoReference, ReasonAmbiguousReference:
		if r.Calibrated || identified || len(r.Segments) != 0 {
			return errors.New("incoherent score refusal")
		}
	}
	if status == StatusRefused && reason != ReasonUncalibrated && len(r.Segments) != 0 {
		return errors.New("incoherent score refusal")
	}
	adverse := false
	for _, s := range r.Segments {
		if s.Band.Defined != (s.Band.Band != "") {
			return errors.New("incoherent score band")
		}
		if s.Band.Defined && (!r.Calibrated || !contains(workflow.Bands(), s.Band.Band)) {
			return errors.New("incoherent score band")
		}
		if s.Band.Band == "drifting" || s.Band.Band == "not-you" {
			adverse = true
		}
		for _, d := range s.Features {
			if d.Direction != "" && !contains([]string{"above", "below", "typical"}, d.Direction) {
				return errors.New("incoherent score delta")
			}
		}
	}
	if (status == StatusAdverse) != adverse {
		return errors.New("incoherent score adversity")
	}
	return nil
}

func validIndexResult(status Status, result IndexResult) error {
	if !contains(workflow.IndexModes(), result.Mode) {
		return errors.New("incoherent index result mode")
	}
	if result.ProfileID != nil && *result.ProfileID == "" || result.ReferenceID != nil && *result.ReferenceID == "" {
		return errors.New("incoherent index result identity")
	}
	if result.Adversity != "" && !contains(workflow.Adversities(), result.Adversity) {
		return errors.New("incoherent index result adversity")
	}
	if (status == StatusAdverse) != (result.Adversity != "") {
		return errors.New("incoherent index result adversity")
	}
	switch result.Mode {
	case workflow.IndexSnapshotOnly:
		if result.Adversity != workflow.AdversityCorpusTooSmall || result.ProfileID != nil || result.ReferenceID != nil {
			return errors.New("incoherent index result mode")
		}
	case workflow.IndexProfile:
		if result.Adversity != workflow.AdversityReferenceTooSmall || result.ProfileID == nil || result.ReferenceID != nil {
			return errors.New("incoherent index result mode")
		}
	case workflow.IndexProfileAndReference:
		if result.Adversity != "" || result.ProfileID == nil || result.ReferenceID == nil {
			return errors.New("incoherent index result mode")
		}
	}
	return nil
}

func validProfileResult(status Status, reason Reason, envelopeProfile *string, result ProfileResult) error {
	if !contains(workflow.Selections(), result.Selection) {
		return errors.New("incoherent profile result selection")
	}
	selected := result.Selection == workflow.SelectedSoleHead || result.Selection == workflow.SelectedExplicit
	if result.Selection == workflow.SelectionAmbiguous || result.Selection == workflow.SelectionUnknownRegister {
		return errors.New("incoherent profile result selection")
	}
	if !validAvailableProfiles(result.Available) {
		return errors.New("incoherent profile result available profiles")
	}
	if !selected {
		if result.Selection != workflow.SelectionNoProfile || status != StatusRefused || reason != ReasonNoProfile ||
			envelopeProfile != nil || len(result.Available) != 0 || !emptyProfile(result.Profile) ||
			result.ReferenceID != nil || result.Evaluated {
			return errors.New("incoherent profile result selection")
		}
		return nil
	}
	if status != StatusOK || envelopeProfile == nil || *envelopeProfile == "" ||
		result.Profile.ID == "" || result.Profile.Register == "" || *envelopeProfile != result.Profile.Register ||
		!contains(result.Available, result.Profile.Register) {
		return errors.New("incoherent profile result selection")
	}
	if result.Selection == workflow.SelectedSoleHead && (len(result.Available) != 1 || result.Available[0] != result.Profile.Register) {
		return errors.New("incoherent profile result selection")
	}
	if result.Evaluated && result.ReferenceID == nil {
		return errors.New("incoherent profile result evaluation")
	}
	return nil
}

func validEvalResult(status Status, reason Reason, result EvalResult) error {
	if status == StatusRefused {
		if reason != ReasonNoProfile || result.Reason != "" || result.ReleaseID != nil || result.ProfileID != nil || result.ReferenceID != nil || result.DistractorPoolID != nil || result.Discrimination != nil || result.Calibration != nil {
			return errors.New("incoherent eval refusal")
		}
		return nil
	}
	if reason != "" || result.ProfileID == nil || !contains(workflow.EvalReasons(), result.Reason) {
		return errors.New("incoherent eval result")
	}
	if result.Reason == workflow.EvalReasonNoReference {
		if result.Shippable || status != StatusAdverse || result.ReferenceID != nil || result.ReleaseID != nil || result.DistractorPoolID != nil || !emptyEvalReports(result) {
			return errors.New("incoherent eval result")
		}
		return nil
	}
	if result.Reason == "uncalibrated" {
		if result.Shippable || status != StatusAdverse || result.ReferenceID == nil || result.ReleaseID != nil || result.DistractorPoolID != nil || !emptyEvalReports(result) {
			return errors.New("incoherent eval result")
		}
		return nil
	}
	if result.ReferenceID == nil || result.ReleaseID == nil || result.DistractorPoolID == nil || result.Discrimination == nil || result.Calibration == nil || result.Shippable != (status == StatusOK) || result.Shippable != (result.Reason == "") {
		return errors.New("incoherent eval result")
	}
	return nil
}

func emptyEvalReports(result EvalResult) bool {
	return result.Discrimination != nil && *result.Discrimination == (EvalDiscrimination{}) &&
		result.Calibration != nil && !result.Calibration.Calibrated && result.Calibration.Reason == "" && len(result.Calibration.Bands) == 0
}

func validAvailableProfiles(available []string) bool {
	for i, register := range available {
		if register == "" || (i > 0 && available[i-1] >= register) {
			return false
		}
	}
	return true
}

func emptyProfile(profile Profile) bool {
	return profile.ID == "" && profile.SnapshotID == "" && profile.Register == "" &&
		!profile.ProductionReady && profile.NotReadyReason == "" && len(profile.Stats) == 0
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
	Getwd    func() (string, error)
	Service  workflow.Service
}

// Run executes one command and returns its process exit code.
func Run(ctx context.Context, args []string, deps Deps) int {
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

	if parsed.command == "index" {
		return runIndex(ctx, parsed, deps)
	}
	if parsed.command == "profile" {
		return runProfile(ctx, parsed, deps)
	}
	if parsed.command == "eval" {
		return runEval(ctx, parsed, deps)
	}
	if parsed.command == "score" {
		return runScore(ctx, parsed, deps)
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
	json                     bool
	localOnly                bool
	command, register, store string
	path, distractor         string
}

func parse(args []string) (invocation, error) {
	var positional []string
	flags := true
	result := invocation{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
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
			case "--profile", "--store", "--distractors":
				if i+1 == len(args) || args[i+1] == "--" {
					return invocation{}, fmt.Errorf("flag %q requires a value", arg)
				}
				i++
				if arg == "--profile" {
					result.register = args[i]
				} else if arg == "--store" {
					result.store = args[i]
				} else {
					result.distractor = args[i]
				}
			default:
				if strings.HasPrefix(arg, "--profile=") {
					result.register = strings.TrimPrefix(arg, "--profile=")
					if result.register == "" {
						return invocation{}, fmt.Errorf("flag %q requires a value", "--profile")
					}
				} else if strings.HasPrefix(arg, "--store=") {
					result.store = strings.TrimPrefix(arg, "--store=")
					if result.store == "" {
						return invocation{}, fmt.Errorf("flag %q requires a value", "--store")
					}
				} else if strings.HasPrefix(arg, "--distractors=") {
					result.distractor = strings.TrimPrefix(arg, "--distractors=")
					if result.distractor == "" {
						return invocation{}, fmt.Errorf("flag %q requires a value", "--distractors")
					}
				} else {
					return invocation{}, fmt.Errorf("invalid flag %q", arg)
				}
			}
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) == 0 {
		return invocation{}, errors.New("missing command")
	}
	result.command = positional[0]
	if !contains(Commands(), result.command) {
		available := Commands()
		sort.Strings(available)
		return invocation{}, fmt.Errorf("unknown command %q (available: %s)", positional[0], strings.Join(available, ", "))
	}
	if result.command == "index" {
		if result.register == "" || len(positional) != 2 {
			return invocation{}, errors.New("index requires --profile and exactly one corpus root")
		}
		result.path = positional[1]
		return result, nil
	}
	if result.command == "profile" {
		if len(positional) != 1 {
			return invocation{}, errors.New("profile takes no operands")
		}
		return result, nil
	}
	if result.command == "eval" {
		if len(positional) != 1 {
			return invocation{}, errors.New("eval takes no operands")
		}
		return result, nil
	}
	if result.command == "score" {
		if len(positional) != 2 {
			return invocation{}, errors.New("score requires exactly one draft")
		}
		result.path = positional[1]
		return result, nil
	}
	if len(positional) != 2 {
		return invocation{}, errors.New("tells requires exactly one file operand")
	}
	result.path = positional[1]
	return result, nil
}

type IndexResult struct {
	Store             string             `json:"store"`
	SnapshotID        string             `json:"snapshot_id"`
	Mode              workflow.IndexMode `json:"mode"`
	Adversity         workflow.Adversity `json:"adversity"`
	Documents         int                `json:"documents"`
	Eligible          int                `json:"eligible"`
	Nodes             int                `json:"nodes"`
	CalibrateSegments int                `json:"calibrate_segments"`
	TrainParagraphs   int                `json:"train_paragraphs"`
	ProfileID         *string            `json:"profile_id"`
	ReferenceID       *string            `json:"reference_id"`
	NotReadyReason    string             `json:"profile_not_ready_reason"`
	Checks            []workflow.Check   `json:"checks"`
	Pruned            workflow.Pruned    `json:"pruned"`
}
type ProfileResult struct {
	Store       string             `json:"store"`
	Selection   workflow.Selection `json:"selection"`
	Available   []string           `json:"available_profiles"`
	ReferenceID *string            `json:"reference_id"`
	Evaluated   bool               `json:"evaluated"`
	Profile     Profile            `json:"profile"`
}

// EvalResult is the complete evaluation report, including adverse evidence.
type EvalResult struct {
	Store              string              `json:"store"`
	ReleaseID          *string             `json:"release_id"`
	ProfileID          *string             `json:"profile_id"`
	ReferenceID        *string             `json:"reference_id"`
	DistractorPoolID   *string             `json:"distractor_pool_id"`
	DistractorMembers  int                 `json:"distractor_members"`
	AuthorSegments     int                 `json:"author_segments"`
	DistractorSegments int                 `json:"distractor_segments"`
	Split              string              `json:"split"`
	Shippable          bool                `json:"shippable"`
	Reason             string              `json:"reason"`
	Discrimination     *EvalDiscrimination `json:"discrimination"`
	Calibration        *EvalCalibration    `json:"calibration"`
}
type MeasuredDistance struct {
	Value   float64 `json:"value"`
	Defined bool    `json:"defined"`
	Reason  string  `json:"reason"`
	Partial bool    `json:"partial"`
}
type BandOutcome struct {
	Band     string  `json:"band"`
	Defined  bool    `json:"defined"`
	Reason   string  `json:"reason"`
	Distance float64 `json:"distance"`
}
type FeatureDelta struct {
	Feature   string  `json:"feature"`
	Deviation float64 `json:"deviation"`
	Defined   bool    `json:"defined"`
	Reason    string  `json:"reason"`
	Direction string  `json:"direction"`
}
type ScoredSegment struct {
	Index         int              `json:"index"`
	LexicalTokens int              `json:"lexical_tokens"`
	Distance      MeasuredDistance `json:"distance"`
	Band          BandOutcome      `json:"band"`
	Features      []FeatureDelta   `json:"features"`
}
type ScoreResult struct {
	Path                 string          `json:"path"`
	Store                string          `json:"store"`
	ProfileID            *string         `json:"profile_id"`
	ReferenceID          *string         `json:"reference_id"`
	ReleaseID            *string         `json:"release_id"`
	Calibrated           bool            `json:"calibrated"`
	ParagraphsBelowFloor int             `json:"paragraphs_below_floor"`
	Segments             []ScoredSegment `json:"segments"`
}

type EvalDiscrimination struct {
	AUC                float64 `json:"auc"`
	LowerBound         float64 `json:"lower_bound"`
	Floor              float64 `json:"floor"`
	Cap                float64 `json:"cap"`
	AuthorClusters     int     `json:"author_clusters"`
	DistractorClusters int     `json:"distractor_clusters"`
	MinClusters        int     `json:"min_clusters"`
	Passes             bool    `json:"passes"`
	Reason             string  `json:"reason"`
}

type EvalBand struct {
	Band      string  `json:"band"`
	Claims    string  `json:"claims"`
	Target    float64 `json:"target"`
	ErrorRate float64 `json:"error_rate"`
	Emitted   bool    `json:"emitted"`
	Reason    string  `json:"reason"`
}

type EvalCalibration struct {
	Calibrated bool       `json:"calibrated"`
	Reason     string     `json:"reason"`
	Bands      []EvalBand `json:"bands"`
}

// MarshalJSON omits profile when the workflow did not resolve one.  A zero
// profile object would falsely imply that a profile exists but lacks an ID.
func (r ProfileResult) MarshalJSON() ([]byte, error) {
	type result struct {
		Store       string             `json:"store"`
		Selection   workflow.Selection `json:"selection"`
		Available   []string           `json:"available_profiles"`
		ReferenceID *string            `json:"reference_id"`
		Evaluated   bool               `json:"evaluated"`
		Profile     *Profile           `json:"profile,omitempty"`
	}
	encoded := result{Store: r.Store, Selection: r.Selection, Available: r.Available, ReferenceID: r.ReferenceID, Evaluated: r.Evaluated}
	if !emptyProfile(r.Profile) {
		profile := r.Profile
		encoded.Profile = &profile
	}
	return json.Marshal(encoded)
}

type Profile struct {
	ID              string        `json:"id"`
	SnapshotID      string        `json:"snapshot_id"`
	Register        string        `json:"register"`
	ProductionReady bool          `json:"production_ready"`
	NotReadyReason  string        `json:"not_ready_reason"`
	Stats           []ProfileStat `json:"stats"`
}

type ProfileStat struct {
	Feature         string  `json:"feature"`
	N               int     `json:"n"`
	Mean            float64 `json:"mean"`
	Variance        float64 `json:"variance"`
	Defined         bool    `json:"defined"`
	VarianceDefined bool    `json:"variance_defined"`
	MinObservations int     `json:"min_observations"`
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func indexResultFrom(r workflow.IndexResult) IndexResult {
	return IndexResult{Store: r.StorePath, SnapshotID: r.SnapshotID, Mode: r.Mode, Adversity: r.Adversity, Documents: r.Documents, Eligible: r.Eligible, Nodes: r.Nodes, CalibrateSegments: r.CalibrateSegments, TrainParagraphs: r.TrainParagraphs, ProfileID: ptr(r.ProfileID), ReferenceID: ptr(r.ReferenceID), NotReadyReason: r.NotReadyReason, Checks: r.Checks, Pruned: r.Pruned}
}
func profileResultFrom(r workflow.ProfileResult) ProfileResult {
	if r.Selection != workflow.SelectedSoleHead && r.Selection != workflow.SelectedExplicit {
		return ProfileResult{Store: r.StorePath, Selection: r.Selection, Available: r.Available}
	}
	available := append([]string(nil), r.Available...)
	// A selected workflow result always has its resolved register available. The
	// fallback keeps this projection total for Service implementations that only
	// supply the resolved profile (including the composition-root test seam).
	if len(available) == 0 {
		available = []string{r.Profile.Register}
	}
	p := Profile{ID: r.Profile.ID, SnapshotID: r.Profile.SnapshotID, Register: r.Profile.Register, ProductionReady: r.Profile.ProductionReady, NotReadyReason: r.Profile.NotReadyReason}
	for _, stat := range r.Profile.Stats {
		p.Stats = append(p.Stats, ProfileStat{Feature: stat.Feature, N: stat.N, Mean: stat.Mean, Variance: stat.Variance, Defined: stat.Defined, VarianceDefined: stat.VarianceDefined, MinObservations: stat.MinObservations})
	}
	return ProfileResult{Store: r.StorePath, Selection: r.Selection, Available: available, ReferenceID: ptr(r.ReferenceID), Evaluated: r.Evaluated, Profile: p}
}
func evalResultFrom(r workflow.EvalResult) EvalResult {
	out := EvalResult{Store: r.StorePath, ReleaseID: ptr(r.ReleaseID), ProfileID: ptr(r.ProfileID), ReferenceID: ptr(r.ReferenceID), DistractorPoolID: ptr(r.DistractorPoolID), DistractorMembers: r.DistractorMembers, AuthorSegments: r.AuthorSegments, DistractorSegments: r.DistractorSegments, Split: r.Split, Shippable: r.Shippable, Reason: r.Reason}
	if r.Selection != workflow.SelectionNoProfile {
		out.Discrimination = &EvalDiscrimination{AUC: r.Discrimination.AUC, LowerBound: r.Discrimination.LowerBound, Floor: r.Discrimination.Floor, Cap: r.Discrimination.Cap, AuthorClusters: r.Discrimination.AuthorClusters, DistractorClusters: r.Discrimination.DistractorClusters, MinClusters: r.Discrimination.MinClusters, Passes: r.Discrimination.Passes, Reason: r.Discrimination.Reason}
		out.Calibration = &EvalCalibration{Calibrated: r.Calibration.Calibrated, Reason: r.Calibration.Reason}
		for _, band := range r.Calibration.Bands {
			out.Calibration.Bands = append(out.Calibration.Bands, EvalBand{Band: band.Band, Claims: band.Claims, Target: band.Target, ErrorRate: band.ErrorRate, Emitted: band.Emitted, Reason: band.Reason})
		}
	}
	return out
}
func scoreResultFrom(r workflow.ScoreResult) ScoreResult {
	out := ScoreResult{Path: r.Path, Store: r.StorePath, ProfileID: ptr(r.ProfileID), ReferenceID: ptr(r.ReferenceID), ReleaseID: ptr(r.ReleaseID), Calibrated: r.Calibrated, ParagraphsBelowFloor: r.ParagraphsBelowFloor}
	for _, s := range r.Segments {
		x := ScoredSegment{Index: s.Index, LexicalTokens: s.LexicalTokens, Distance: MeasuredDistance{Value: s.Distance.Value, Defined: s.Distance.Defined, Reason: s.Distance.Reason, Partial: s.Distance.Partial}, Band: BandOutcome{Band: s.Band.Band, Defined: s.Band.Defined, Reason: s.Band.Reason, Distance: s.Band.Distance}}
		for _, d := range s.Features {
			x.Features = append(x.Features, FeatureDelta{Feature: d.Feature, Deviation: d.Deviation, Defined: d.Defined, Reason: d.Reason, Direction: d.Direction})
		}
		out.Segments = append(out.Segments, x)
	}
	return out
}
func runIndex(ctx context.Context, parsed invocation, deps Deps) int {
	if deps.Service == nil {
		diagnostic(deps.Stderr, "index service unavailable")
		return 3
	}
	result, err := deps.Service.Index(ctx, workflow.IndexRequest{CorpusRoot: parsed.path, Register: parsed.register, StorePath: parsed.store})
	if err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	status, code := StatusOK, 0
	if result.Adverse {
		status, code = StatusAdverse, 1
	}
	if err = (Document{Schema: Schema, Command: "index", Status: status, Profile: &parsed.register, Result: indexResultFrom(result)}).Render(deps.Stdout, parsed.json); err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	return code
}
func runProfile(ctx context.Context, parsed invocation, deps Deps) int {
	if deps.Service == nil || deps.Getwd == nil {
		diagnostic(deps.Stderr, "profile service unavailable")
		return 3
	}
	cwd, err := deps.Getwd()
	if err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	result, err := deps.Service.Profile(ctx, workflow.ProfileRequest{StartDir: cwd, StorePath: parsed.store, Register: parsed.register})
	if err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	if result.Selection == workflow.SelectionAmbiguous || result.Selection == workflow.SelectionUnknownRegister {
		diagnostic(deps.Stderr, strings.Join(result.Available, ", "))
		return 2
	}
	status, reason, code := StatusOK, Reason(""), 0
	if result.Selection == workflow.SelectionNoProfile {
		status, reason, code = StatusRefused, ReasonNoProfile, 4
	}
	payload := profileResultFrom(result)
	if err = (Document{Schema: Schema, Command: "profile", Status: status, Reason: reason, Profile: ptr(payload.Profile.Register), Result: payload}).Render(deps.Stdout, parsed.json); err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	return code
}
func runEval(ctx context.Context, parsed invocation, deps Deps) int {
	if deps.Service == nil || deps.Getwd == nil {
		diagnostic(deps.Stderr, "eval service unavailable")
		return 3
	}
	cwd, err := deps.Getwd()
	if err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	result, err := deps.Service.Eval(ctx, workflow.EvalRequest{StartDir: cwd, StorePath: parsed.store, Register: parsed.register, DistractorRoot: parsed.distractor})
	if err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	status, reason, code := StatusOK, Reason(""), 0
	if result.Selection == workflow.SelectionNoProfile {
		status, reason, code = StatusRefused, ReasonNoProfile, 4
	} else if result.Adverse || !result.Shippable {
		status, code = StatusAdverse, 1
	}
	payload := evalResultFrom(result)
	if err = (Document{Schema: Schema, Command: "eval", Status: status, Reason: reason, Result: payload}).Render(deps.Stdout, parsed.json); err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	return code
}
func runScore(ctx context.Context, parsed invocation, deps Deps) int {
	if deps.Service == nil || deps.Getwd == nil {
		diagnostic(deps.Stderr, "score service unavailable")
		return 3
	}
	cwd, err := deps.Getwd()
	if err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	r, err := deps.Service.Score(ctx, workflow.ScoreRequest{StartDir: cwd, StorePath: parsed.store, Register: parsed.register, Path: parsed.path})
	if err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	if r.Selection == workflow.SelectionAmbiguous || r.Selection == workflow.SelectionUnknownRegister {
		diagnostic(deps.Stderr, strings.Join(r.Available, ", "))
		return 2
	}
	status, reason, code := StatusOK, Reason(""), 0
	if r.Refusal != "" {
		status, code = StatusRefused, 4
		reason = Reason(r.Refusal)
	} else if r.Adverse {
		status, code = StatusAdverse, 1
	}
	if err = (Document{Schema: Schema, Command: "score", Status: status, Reason: reason, Profile: ptr(parsed.register), Result: scoreResultFrom(r)}).Render(deps.Stdout, parsed.json); err != nil {
		diagnostic(deps.Stderr, err.Error())
		return 3
	}
	return code
}
func humanResult(result any) string {
	switch x := result.(type) {
	case TellsResult:
		return fmt.Sprintf("findings=%d", x.Count)
	case IndexResult:
		result := fmt.Sprintf("store=%s mode=%s", x.Store, x.Mode)
		if x.Adversity != "" {
			result += fmt.Sprintf(" adversity=%s", x.Adversity)
		}
		return result
	case ProfileResult:
		if x.Store == "" {
			return ""
		}
		return fmt.Sprintf("store=%s", x.Store)
	case EvalResult:
		result := fmt.Sprintf("store=%s shippable=%t", x.Store, x.Shippable)
		if x.Reason != "" {
			result += fmt.Sprintf(" reason=%s", x.Reason)
		}
		return result
	case ScoreResult:
		bands := []string{}
		for _, s := range x.Segments {
			if s.Band.Band != "" {
				bands = append(bands, s.Band.Band)
			}
		}
		return fmt.Sprintf("path=%s bands=%s below-floor=%d", x.Path, strings.Join(bands, ","), x.ParagraphsBelowFloor)
	default:
		return ""
	}
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
