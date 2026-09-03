// Package workflow owns Hapax's indexing and profile-query composition.
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fissible/hapax/internal/assemble"
	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/exemplar"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/ingest"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/mode"
	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
	"github.com/fissible/hapax/internal/snapshot"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/text"
)

type IndexMode string

const (
	IndexSnapshotOnly        IndexMode = "snapshot-only"
	IndexProfile             IndexMode = "profile"
	IndexProfileAndReference IndexMode = "profile-and-reference"
)

func IndexModes() []IndexMode {
	return []IndexMode{IndexSnapshotOnly, IndexProfile, IndexProfileAndReference}
}

type Adversity string

const (
	AdversityCorpusTooSmall    Adversity = "corpus-too-small"
	AdversityReferenceTooSmall Adversity = "reference-too-small"
)

func Adversities() []Adversity {
	return []Adversity{AdversityCorpusTooSmall, AdversityReferenceTooSmall}
}

type Selection string

const (
	SelectedSoleHead         Selection = "sole-head"
	SelectedExplicit         Selection = "explicit"
	SelectionAmbiguous       Selection = "ambiguous"
	SelectionUnknownRegister Selection = "unknown-register"
	SelectionNoProfile       Selection = "no-profile"
)

func Selections() []Selection {
	return []Selection{SelectedSoleHead, SelectedExplicit, SelectionAmbiguous, SelectionUnknownRegister, SelectionNoProfile}
}

var checkNames = []string{"contamination", "language", "structure", "git-provenance", "near-duplicate-detection"}

func CheckNames() []string { return append([]string(nil), checkNames...) }

type Check struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Version string `json:"version"`
}

type Pruned struct {
	Snapshots int `json:"snapshots"`
	Documents int `json:"documents"`
	Nodes     int `json:"nodes"`
	Profiles  int `json:"profiles"`
}
type IndexRequest struct {
	CorpusRoot, Register, StorePath string
	LockWait                        time.Duration
}

func DefaultLockWait() time.Duration { return store.DefaultIndexLockWait() }

type IndexResult struct {
	StorePath, SnapshotID                                          string
	Mode                                                           IndexMode
	Adverse                                                        bool
	Adversity                                                      Adversity
	Documents, Eligible, Nodes, CalibrateSegments, TrainParagraphs int
	ProfileID, ReferenceID, NotReadyReason                         string
	Checks                                                         []Check
	Pruned                                                         Pruned
}

type ProfileRequest struct{ StartDir, StorePath, Register string }
type StoredStat struct {
	Feature                  string
	N                        int
	Mean, Variance           float64
	Defined, VarianceDefined bool
	MinObservations          int
}
type StoredProfile struct {
	ID, SnapshotID, Register string
	ProductionReady          bool
	NotReadyReason           string
	Stats                    []StoredStat
}
type ProfileResult struct {
	StorePath   string
	Selection   Selection
	Available   []string
	ReferenceID string
	Evaluated   bool
	Profile     StoredProfile
}
type EvalRequest struct{ StartDir, StorePath, Register, DistractorRoot string }
type ScoreRequest struct{ StartDir, StorePath, Register, Path string }
type MeasuredDistance struct {
	Value   float64
	Defined bool
	Reason  string
	Partial bool
}
type BandOutcome struct {
	Band     string
	Defined  bool
	Reason   string
	Distance float64
}
type FeatureDelta struct {
	Feature   string
	Deviation float64
	Defined   bool
	Reason    string
	Direction string
}
type ScoredSegment struct {
	Index, LexicalTokens int
	Distance             MeasuredDistance
	Band                 BandOutcome
	Features             []FeatureDelta
}
type ScoreResult struct {
	StorePath, Path, ProfileID, ReferenceID, ReleaseID string
	Selection                                          Selection
	Available                                          []string
	Calibrated, Adverse                                bool
	Refusal                                            string
	ParagraphsBelowFloor                               int
	Segments                                           []ScoredSegment
}

// PlanRequest contains the inputs used to qualify a draft for rewriting.
type PlanRequest struct {
	StartDir, StorePath, CorpusRoot, Register, Path string
	Paragraphs                                      []int
}

// RewriteRequest is retained as a source-compatible name for callers compiled
// against the B1 planning API. New callers use PlanRequest.
type RewriteRequest = PlanRequest
type Disposition string

const (
	DispositionTarget            Disposition = "target"
	DispositionInRange           Disposition = "in-range"
	DispositionUnmeasurable      Disposition = "unmeasurable"
	DispositionContainsExcisions Disposition = "contains-excisions"
	DispositionNotSelected       Disposition = "not-selected"
)

func Dispositions() []Disposition {
	return []Disposition{DispositionTarget, DispositionInRange, DispositionUnmeasurable, DispositionContainsExcisions, DispositionNotSelected}
}

type Targeting string

const (
	TargetingAutomatic Targeting = "automatic"
	TargetingExplicit  Targeting = "explicit"
)

func Targetings() []Targeting { return []Targeting{TargetingAutomatic, TargetingExplicit} }

type Claim string

const (
	ClaimCalibratedBand   Claim = "calibrated-band"
	ClaimCloserByDistance Claim = "closer-by-distance"
)

func Claims() []Claim { return []Claim{ClaimCalibratedBand, ClaimCloserByDistance} }

type PlanState string

const (
	StateNothingToChange PlanState = "nothing-to-change"
	StateTargetsPlanned  PlanState = "targets-planned"
)

func PlanStates() []PlanState { return []PlanState{StateNothingToChange, StateTargetsPlanned} }

type PlannedSegment struct {
	Index          int
	NodeID         string
	Offset, Length int
	LexicalTokens  int
	Band           BandOutcome
	Disposition    Disposition
}
type RewritePlan struct {
	StorePath, CorpusRoot, Path                string
	Selection                                  Selection
	Available                                  []string
	Refusal, ProfileID, ReferenceID, ReleaseID string
	DraftSnapshotID                            string
	ParagraphsBelowFloor                       int
	Targeting                                  Targeting
	Claim                                      Claim
	CalibrationAvailable                       bool
	Segments                                   []PlannedSegment
	Targets                                    int
	State                                      PlanState
	ExemplarSelectionID                        string
	ExemplarCertificateID                      string
	ExemplarNodes                              []string
}

const (
	RefusalNoProfile                = "no-profile"
	RefusalNoReference              = "no-reference"
	RefusalAmbiguousReference       = "ambiguous-reference"
	RefusalUncalibrated             = "uncalibrated"
	RefusalInsufficientEvidence     = "insufficient-evidence"
	RefusalStaleDraft               = "stale-draft"
	RefusalStaleExemplars           = "stale-exemplars"
	RefusalLocalOnlyForbidsProvider = "local-only-forbids-provider"
	RefusalNoSuchParagraph          = "no-such-paragraph"
)

func Refusals() []string {
	return []string{RefusalNoProfile, RefusalNoReference, RefusalAmbiguousReference, RefusalUncalibrated, RefusalInsufficientEvidence, RefusalStaleDraft, RefusalStaleExemplars, RefusalLocalOnlyForbidsProvider, RefusalNoSuchParagraph}
}

func Terminals() []string {
	out := make([]string, 0, len(rewrite.Terminals()))
	for _, x := range rewrite.Terminals() {
		out = append(out, string(x))
	}
	return out
}
func RejectionCodes() []string {
	out := make([]string, 0, len(rewrite.RejectionCodes()))
	for _, x := range rewrite.RejectionCodes() {
		out = append(out, string(x))
	}
	return out
}

type RewriteState string

const (
	RewriteNoTargets    RewriteState = "no-targets"
	RewriteImproved     RewriteState = "improved"
	RewriteNoneImproved RewriteState = "none-improved"
)

func RewriteStates() []RewriteState {
	return []RewriteState{RewriteNoTargets, RewriteImproved, RewriteNoneImproved}
}

type ExecuteRequest struct {
	Plan     RewritePlan
	Choice   ProviderChoice
	Mode     mode.Mode
	Attempts int
}
type TargetOutcome struct {
	Index      int
	NodeID     string
	Changed    bool
	Terminal   string
	Rejections []string
}
type ExecuteResult struct {
	Bytes             []byte
	InvocationID      string
	State             RewriteState
	Targets, Improved int
	Refusal           string
	Outcomes          []TargetOutcome
}

// RewriteInput is the one request the composition root may use to rewrite a
// document. Keeping the planning and execution inputs together prevents a
// caller from opening a freshness window between the two operations.
type RewriteInput struct {
	StartDir, StorePath, CorpusRoot, Register, Path string
	Paragraphs                                      []int
	Choice                                          ProviderChoice
	Mode                                            mode.Mode
	Attempts                                        int
}

// RewriteReport is the public, prose-free account of a rewrite.
type RewriteReport struct {
	PlanState            PlanState
	State                RewriteState
	Targets, Improved    int
	Refusal              string
	Outcomes             []TargetOutcome
	Targeting            Targeting
	Claim                Claim
	CalibrationAvailable bool
}

// RewriteOutcome keeps assembled document bytes private to workflow. Content
// returns a copy so publication cannot mutate the result retained by a caller.
type RewriteOutcome struct {
	report  RewriteReport
	content []byte
}

func NewRewriteOutcome(report RewriteReport, content []byte) RewriteOutcome {
	report.Outcomes = append([]TargetOutcome(nil), report.Outcomes...)
	return RewriteOutcome{report: report, content: append([]byte(nil), content...)}
}

func (o RewriteOutcome) Report() RewriteReport {
	r := o.report
	r.Outcomes = append([]TargetOutcome(nil), r.Outcomes...)
	return r
}

func (o RewriteOutcome) Content() []byte { return append([]byte(nil), o.content...) }

func Bands() []string { return []string{"in-range", "drifting", "not-you"} }

// EvalReasonNoReference names a completed evaluation that cannot measure a
// profile because indexing retained it without a reference distribution.
const EvalReasonNoReference = "no-reference"

// EvalReasons is the closed vocabulary for completed evaluation outcomes.
// Release reasons are persisted; no-reference is deliberately not, because no
// release exists for that outcome.
var evalReasons = []string{string(eval.ReleaseReasonNone), string(eval.ReleaseReasonDiscriminationFailed), string(eval.ReleaseReasonUncalibrated), EvalReasonNoReference}

func EvalReasons() []string {
	return append([]string(nil), evalReasons...)
}

type DiscriminationReport struct {
	AUC, LowerBound, Floor, Cap                     float64
	AuthorClusters, DistractorClusters, MinClusters int
	Passes                                          bool
	Reason                                          string
}
type BandReport struct {
	Band, Claims      string
	Target, ErrorRate float64
	Emitted           bool
	Reason            string
}
type CalibrationReport struct {
	Calibrated bool
	Reason     string
	Bands      []BandReport
}
type EvalResult struct {
	StorePath                                             string
	Selection                                             Selection
	ReleaseID, ProfileID, ReferenceID, DistractorPoolID   string
	DistractorMembers, AuthorSegments, DistractorSegments int
	AuthorClusters, DistractorClusters                    int
	Split                                                 string
	Shippable, Adverse                                    bool
	Reason                                                string
	Discrimination                                        DiscriminationReport
	Calibration                                           CalibrationReport
}

// Service is the sole execution seam required by the CLI.
type Service interface {
	Index(context.Context, IndexRequest) (IndexResult, error)
	Profile(context.Context, ProfileRequest) (ProfileResult, error)
	Eval(context.Context, EvalRequest) (EvalResult, error)
	Score(context.Context, ScoreRequest) (ScoreResult, error)
	Rewrite(context.Context, RewriteInput) (RewriteOutcome, error)
}

func heldOutSegments(ctx context.Context, s *store.Store, snapshotID string, fitted profile.Fitted) ([]eval.Segment, error) {
	return storedSegments(ctx, s, snapshotID, corpus.Test, eval.ClassAuthor, fitted)
}
func storedSegments(ctx context.Context, s *store.Store, snapshotID string, split corpus.Split, class eval.Class, fitted profile.Fitted) ([]eval.Segment, error) {
	w, err := s.Snapshot(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	var out []eval.Segment
	for _, d := range w.Documents {
		if d.Split != split {
			continue
		}
		index := 0
		for _, n := range d.Nodes {
			if n.Vector == nil {
				continue
			}
			out = append(out, eval.Segment{Class: class, DocumentHash: d.ContentHash, DocumentPath: d.Path, Index: index, LexicalTokens: n.Vector.LexicalTokens, Vector: *n.Vector})
			index++
		}
	}
	return out, nil
}

func (r *Runner) Eval(ctx context.Context, request EvalRequest) (EvalResult, error) {
	path, found, err := discover(request.StartDir, request.StorePath)
	if err != nil {
		return EvalResult{}, err
	}
	if !found {
		return EvalResult{Selection: SelectionNoProfile}, nil
	}
	s, err := store.Open(path)
	if err != nil {
		return EvalResult{}, err
	}
	defer s.Close()
	bundle, err := s.LoadProfileBundle(ctx, request.Register)
	if errors.Is(err, store.ErrNotFound) {
		return EvalResult{StorePath: path, Selection: SelectionNoProfile}, nil
	}
	if err != nil {
		return EvalResult{}, err
	}
	fitted, err := bundle.Profile.Fitted()
	if err != nil {
		return EvalResult{}, err
	}
	result := EvalResult{StorePath: path, Selection: SelectedExplicit, ProfileID: fitted.ID, ReferenceID: bundle.Reference.ID, Split: string(corpus.Test)}
	if bundle.Reference.ID == "" {
		result.Adverse = true
		result.Reason = EvalReasonNoReference
		return result, nil
	}
	if request.DistractorRoot == "" {
		result.Adverse = true
		result.Reason = "uncalibrated"
		return result, nil
	}
	policy := corpus.DefaultPolicy(bundle.Profile.Register)
	policy.Role = corpus.RoleDistractor
	dsnap, err := corpus.Walk(request.DistractorRoot, policy)
	if err != nil {
		return EvalResult{}, err
	}
	var hashes []string
	for _, d := range dsnap.Eligible() {
		hashes = append(hashes, d.ContentHash)
	}
	pool := store.DistractorPool{PolicyDigest: identity.HashInputs(dsnap.IdentityInputs()), ContentHashes: hashes}
	pool.ID = store.DistractorPoolID(pool.PolicyDigest, hashes)
	if err = s.PutDistractorPool(ctx, pool); err != nil {
		return EvalResult{}, err
	}
	result.DistractorPoolID, result.DistractorMembers = pool.ID, len(hashes)
	authorTest, err := heldOutSegments(ctx, s, bundle.Profile.SnapshotID, fitted)
	if err != nil {
		return EvalResult{}, err
	}
	// The author corpus has a Train/Calibrate/Test partition. A distractor pool
	// does not: every eligible member is comparison material, so applying that
	// partition here silently throws away most of the not-the-author class.
	distractorTest, err := diskSegments(request.DistractorRoot, dsnap, eval.ClassDistractor, fitted)
	if err != nil {
		return EvalResult{}, err
	}
	authorMembers, err := hashesForStored(ctx, s, bundle.Profile.SnapshotID, corpus.Test)
	if err != nil {
		return EvalResult{}, err
	}
	set, err := eval.NewSet(eval.SetIdentity{Fitted: fitted, AuthorSnapshotID: bundle.Profile.SnapshotID, AuthorMembers: authorMembers, DistractorPoolID: pool.ID, DistractorMembers: hashes}, authorTest, distractorTest, eval.Requirements{Split: corpus.Test, MinAuthorSegments: 1, MinDistractorSegments: 1})
	if err != nil {
		return EvalResult{}, err
	}
	test, err := distances(set.Segments, fitted, &bundle.Reference, corpus.Test)
	if err != nil {
		return EvalResult{}, err
	}
	authorCal, err := storedSegments(ctx, s, bundle.Profile.SnapshotID, corpus.Calibrate, eval.ClassAuthor, fitted)
	if err != nil {
		return EvalResult{}, err
	}
	distractorCal, err := diskSegments(request.DistractorRoot, dsnap, eval.ClassDistractor, fitted)
	if err != nil {
		return EvalResult{}, err
	}
	calSeg := append(authorCal, distractorCal...)
	calibrationDistances, err := distances(calSeg, fitted, &bundle.Reference, corpus.Calibrate)
	if err != nil {
		return EvalResult{}, err
	}
	discrimination, err := eval.Discriminate(test, r.Discrimination)
	if err != nil {
		return EvalResult{}, err
	}
	threshold, err := eval.Calibrate(calibrationDistances, eval.Source{Cohort: bundle.Profile.SnapshotID, DistractorPool: pool.ID}, eval.DefaultTargets())
	var calibration eval.Calibration
	if err != nil {
		if !errors.Is(err, eval.ErrNoQualifyingThreshold) {
			return EvalResult{}, err
		}
		calibration = uncalibratedCalibration(discrimination, pool.ID, r.BandFloor, r.Bootstrap)
		if err = s.PutThreshold(ctx, store.Threshold{ID: calibration.ThresholdsID, ProfileID: calibration.ProfileID, ReferenceID: calibration.ReferenceID, PopulationID: calibration.PopulationID, Verdict: eval.VerdictPairIncompatible}); err != nil {
			return EvalResult{}, err
		}
	} else {
		intervals, err := threshold.Bootstrap(calibrationDistances, r.Bootstrap)
		if err != nil {
			return EvalResult{}, err
		}
		calibration, err = threshold.CalibrateBands(test, r.BandFloor)
		if err != nil {
			return EvalResult{}, err
		}
		// The interval is part of the release evidence: its declared bootstrap
		// controls whether the calibrated thresholds are shippable. Bind its
		// identity into the calibration artifact so two runs that differ only in
		// that procedure cannot name the same release.
		calibration.ID = identity.HashInputs(map[string]string{
			"band-calibration-id": calibration.ID,
			"intervals-id":        intervals.ID,
		})
		if err = s.PutThreshold(ctx, store.Threshold{ID: threshold.ID, ProfileID: threshold.ProfileID, ReferenceID: threshold.ReferenceID, PopulationID: intervals.ID, Low: threshold.Low, High: threshold.High, AchievedAuthor: threshold.AchievedAuthor, AchievedDistractor: threshold.AchievedDistractor, IntervalLow: intervals.Low, IntervalHigh: intervals.High, Verdict: func() eval.ThresholdVerdict {
			if intervals.Shippable {
				return eval.VerdictSeparated
			}
			return eval.VerdictPairIncompatible
		}()}); err != nil {
			return EvalResult{}, err
		}
	}
	release, err := eval.NewRelease(discrimination, calibration)
	if err != nil {
		return EvalResult{}, err
	}
	if err = s.PutRelease(ctx, release, pool.ID, store.HeadPolicy(release.Shippable)); err != nil {
		return EvalResult{}, err
	}
	result.ReleaseID, result.Shippable, result.Adverse, result.Reason = release.ID, release.Shippable, !release.Shippable, release.Reason
	result.AuthorSegments, result.DistractorSegments = set.AuthorSegments, set.DistractorSegments
	result.AuthorClusters, result.DistractorClusters = discrimination.AuthorClusters, discrimination.DistractorClusters
	result.Discrimination = DiscriminationReport{
		AUC: discrimination.AUC, LowerBound: discrimination.LowerBound, Floor: discrimination.Spec.Floor, Cap: discrimination.Cap,
		AuthorClusters: discrimination.AuthorClusters, DistractorClusters: discrimination.DistractorClusters, MinClusters: discrimination.MinClusters,
		Passes: discrimination.Discriminates, Reason: discrimination.Reason,
	}
	result.Calibration = calibrationReport(calibration)
	return result, nil
}

// Score discovers the store exactly as profile and eval do, then either bands
// a measurement through its release or returns the raw, uncalibrated measure.
func (r *Runner) Score(ctx context.Context, request ScoreRequest) (ScoreResult, error) {
	path, found, err := discover(request.StartDir, request.StorePath)
	if errors.Is(err, os.ErrNotExist) {
		return ScoreResult{Path: request.Path, Selection: SelectionNoProfile, Refusal: RefusalNoProfile}, nil
	}
	if err != nil {
		return ScoreResult{}, err
	}
	if !found {
		return ScoreResult{Path: request.Path, Selection: SelectionNoProfile, Refusal: RefusalNoProfile}, nil
	}
	s, err := store.Open(path)
	if err != nil {
		return ScoreResult{}, err
	}
	defer s.Close()
	heads, err := s.ProfileHeads(ctx)
	if err != nil {
		return ScoreResult{}, err
	}
	available := availableRegisters(heads)
	result := ScoreResult{StorePath: path, Path: request.Path, Available: available}
	register := request.Register
	if register == "" {
		switch len(available) {
		case 0:
			result.Selection, result.Refusal = SelectionNoProfile, RefusalNoProfile
			return result, nil
		case 1:
			register, result.Selection = available[0], SelectedSoleHead
		default:
			result.Selection = SelectionAmbiguous
			return result, nil
		}
	} else {
		if _, ok := heads[register]; !ok {
			result.Selection = SelectionUnknownRegister
			return result, nil
		}
		result.Selection = SelectedExplicit
	}
	bundle, err := s.LoadScoringBundle(ctx, register)
	if errors.Is(err, store.ErrNotFound) {
		return ScoreResult{StorePath: path, Path: request.Path, Selection: SelectionNoProfile, Refusal: RefusalNoProfile}, nil
	}
	if errors.Is(err, store.ErrNoReference) {
		return ScoreResult{StorePath: path, Path: request.Path, Selection: result.Selection, Available: available, Refusal: RefusalNoReference}, nil
	}
	if errors.Is(err, store.ErrAmbiguousReference) {
		return ScoreResult{StorePath: path, Path: request.Path, Selection: result.Selection, Available: available, Refusal: RefusalAmbiguousReference}, nil
	}
	if err != nil {
		return ScoreResult{}, err
	}
	source, err := os.ReadFile(request.Path)
	if err != nil {
		return ScoreResult{}, err
	}
	var report score.Report
	if bundle.Calibrated {
		report, err = score.Score(source, bundle.Fitted, &bundle.Reference, bundle.Release)
	} else {
		report, err = score.Measure(source, bundle.Fitted, &bundle.Reference)
	}
	if err != nil {
		return ScoreResult{}, err
	}
	out := ScoreResult{StorePath: path, Path: request.Path, Selection: result.Selection, Available: available, ProfileID: report.ProfileID, ReferenceID: report.ReferenceID, ReleaseID: report.ReleaseID, Calibrated: report.Calibrated, ParagraphsBelowFloor: report.ParagraphsBelowFloor}
	if !bundle.Calibrated {
		out.Refusal = RefusalUncalibrated
	}
	if len(report.Segments) == 0 {
		out.Refusal = RefusalInsufficientEvidence
	}
	for _, segment := range report.Segments {
		x := ScoredSegment{Index: segment.Index, LexicalTokens: segment.LexicalTokens, Distance: MeasuredDistance{Value: segment.Distance.Value, Defined: segment.Distance.Defined, Reason: string(segment.Distance.Reason), Partial: segment.Distance.Partial}, Band: BandOutcome{Band: string(segment.Band.Band), Defined: segment.Band.Defined, Reason: string(segment.Band.Reason), Distance: segment.Band.Distance}}
		for _, d := range segment.Features {
			x.Features = append(x.Features, FeatureDelta{Feature: string(d.Feature), Deviation: d.Deviation, Defined: d.Defined, Reason: string(d.Reason), Direction: string(d.Direction)})
		}
		out.Segments = append(out.Segments, x)
		if x.Band.Band == "drifting" || x.Band.Band == "not-you" {
			out.Adverse = true
		}
	}
	return out, nil
}

// Plan resolves and records every offline rewrite decision before a provider is
// involved. It deliberately stays off Service: B1 has no CLI surface.
func (r *Runner) Plan(ctx context.Context, request PlanRequest) (RewritePlan, error) {
	if request.Path == "" {
		return RewritePlan{}, errors.New("rewrite draft path is required")
	}
	path, found, err := discover(request.StartDir, request.StorePath)
	if errors.Is(err, os.ErrNotExist) || !found {
		return RewritePlan{Path: request.Path, Selection: SelectionNoProfile, Refusal: RefusalNoProfile}, nil
	}
	if err != nil {
		return RewritePlan{}, err
	}
	s, err := store.Open(path)
	if err != nil {
		return RewritePlan{}, err
	}
	defer s.Close()
	heads, err := s.ProfileHeads(ctx)
	if err != nil {
		return RewritePlan{}, err
	}
	available := availableRegisters(heads)
	base := RewritePlan{StorePath: path, Path: request.Path, Available: available}
	register := request.Register
	selection := SelectedExplicit
	if register == "" {
		switch len(available) {
		case 0:
			base.Selection, base.Refusal = SelectionNoProfile, RefusalNoProfile
			return base, nil
		case 1:
			register, selection = available[0], SelectedSoleHead
		default:
			base.Selection = SelectionAmbiguous
			return base, nil
		}
	} else if _, ok := heads[register]; !ok {
		base.Selection = SelectionUnknownRegister
		return base, nil
	}
	base.Selection = selection
	bundle, err := s.LoadScoringBundle(ctx, register)
	if errors.Is(err, store.ErrNotFound) {
		base.Selection, base.Refusal = SelectionNoProfile, RefusalNoProfile
		return base, nil
	}
	if errors.Is(err, store.ErrNoReference) {
		base.Refusal = RefusalNoReference
		return base, nil
	}
	if errors.Is(err, store.ErrAmbiguousReference) {
		base.Refusal = RefusalAmbiguousReference
		return base, nil
	}
	if err != nil {
		return RewritePlan{}, err
	}
	root := request.CorpusRoot
	if root == "" {
		root = filepath.Dir(filepath.Dir(path))
	}
	base.CorpusRoot, base.ProfileID, base.ReferenceID, base.ReleaseID = root, bundle.Fitted.ID, bundle.Reference.ID, bundle.Release.ID
	explicit := len(request.Paragraphs) != 0
	base.CalibrationAvailable = bundle.Calibrated
	if explicit {
		base.Targeting, base.Claim = TargetingExplicit, ClaimCloserByDistance
	} else {
		base.Targeting, base.Claim = TargetingAutomatic, ClaimCalibratedBand
	}
	if !bundle.Calibrated && !explicit {
		base.Refusal = RefusalUncalibrated
		return base, nil
	}
	source, err := os.ReadFile(request.Path)
	if err != nil {
		return RewritePlan{}, err
	}
	var report score.Report
	if bundle.Calibrated {
		report, err = score.Score(source, bundle.Fitted, &bundle.Reference, bundle.Release)
	} else {
		report, err = score.Measure(source, bundle.Fitted, &bundle.Reference)
	}
	if err != nil {
		return RewritePlan{}, err
	}
	base.ParagraphsBelowFloor = report.ParagraphsBelowFloor
	if len(report.Segments) == 0 {
		base.Refusal = RefusalInsufficientEvidence
		return base, nil
	}
	draftRequirements := r.Requirements
	draftRequirements.MinParagraphLexicalTokens = bundle.Fitted.MinParagraphLexicalTokens
	write, leaves, err := draftWrite(request.Path, register, draftRequirements)
	if err != nil {
		return RewritePlan{}, err
	}
	indexed, err := s.Index(ctx, store.IndexWrite{Mode: store.IndexSnapshotOnly, Snapshot: write})
	if err != nil {
		return RewritePlan{}, err
	}
	base.DraftSnapshotID = indexed.Snapshot.ID
	var nodes []store.Node
	for _, document := range indexed.Snapshot.Documents {
		for _, node := range document.Nodes {
			if node.Vector != nil {
				nodes = append(nodes, node)
			}
		}
	}
	if len(nodes) != len(report.Segments) || len(leaves) != len(report.Segments) {
		return RewritePlan{}, errors.New("draft score and indexed paragraphs disagree")
	}
	named := make(map[int]bool, len(request.Paragraphs))
	for _, index := range request.Paragraphs {
		if index < 0 || index >= len(report.Segments) {
			base.Refusal = RefusalNoSuchParagraph
			return base, nil
		}
		named[index] = true
	}
	for i, segment := range report.Segments {
		var disposition Disposition
		if explicit {
			switch {
			case !named[segment.Index]:
				disposition = DispositionNotSelected
			case !segment.Distance.Defined:
				disposition = DispositionUnmeasurable
			case leaves[i].excisions:
				disposition = DispositionContainsExcisions
			default:
				disposition = DispositionTarget
				base.Targets++
			}
		} else {
			switch {
			case !segment.Distance.Defined:
				disposition = DispositionUnmeasurable
			case segment.Band.Band == eval.BandInRange:
				disposition = DispositionInRange
			case leaves[i].excisions:
				disposition = DispositionContainsExcisions
			case segment.Band.Band == eval.BandDrifting || segment.Band.Band == eval.BandNotYou:
				disposition = DispositionTarget
				base.Targets++
			case !segment.Band.Defined:
				disposition = DispositionUnmeasurable
			default:
				return RewritePlan{}, fmt.Errorf("draft segment %d has an unknown band %q", segment.Index, segment.Band.Band)
			}
		}
		base.Segments = append(base.Segments, PlannedSegment{Index: segment.Index, NodeID: nodes[i].ID, Offset: nodes[i].Offset, Length: nodes[i].Length, LexicalTokens: segment.LexicalTokens, Band: BandOutcome{Band: string(segment.Band.Band), Defined: segment.Band.Defined, Reason: string(segment.Band.Reason), Distance: segment.Band.Distance}, Disposition: disposition})
	}
	if base.Targets == 0 {
		base.State = StateNothingToChange
		return base, nil
	}
	storedProfile, err := s.LoadProfile(ctx, bundle.Fitted.ID)
	if err != nil {
		return RewritePlan{}, err
	}
	profileSnapshot, err := s.Snapshot(ctx, storedProfile.SnapshotID)
	if err != nil {
		return RewritePlan{}, err
	}
	var candidates []exemplar.Candidate
	nodeFor := map[string]string{}
	for _, document := range profileSnapshot.Documents {
		if document.Split != corpus.Train {
			continue
		}
		for _, node := range document.Nodes {
			if node.Vector == nil {
				continue
			}
			candidate := exemplar.Candidate{DocumentDigest: document.ContentHash, Span: text.Span{Offset: node.Offset, Length: node.Length}, Role: node.Role, Containers: node.Containers, Split: document.Split, Vector: *node.Vector}
			candidates = append(candidates, candidate)
			nodeFor[candidate.Identity()] = node.ID
		}
	}
	chosen, err := exemplar.Select(bundle.Fitted, candidates, exemplar.DefaultConfig())
	if err != nil {
		return RewritePlan{}, err
	}
	for _, candidate := range chosen.Exemplars {
		base.ExemplarNodes = append(base.ExemplarNodes, nodeFor[candidate.Identity()])
	}
	base.ExemplarSelectionID, base.ExemplarCertificateID = chosen.ID, chosen.Certificate.ID
	if err = s.PutExemplarSelection(ctx, store.ExemplarSelection{ID: chosen.ID, ProfileID: bundle.Fitted.ID, N: len(base.ExemplarNodes), CertificateID: chosen.Certificate.ID, Members: base.ExemplarNodes}); err != nil {
		return RewritePlan{}, err
	}
	base.State = StateTargetsPlanned
	return base, nil
}

type draftLeaf struct{ excisions bool }

func draftWrite(path, register string, requirements profile.Requirements) (store.SnapshotWrite, []draftLeaf, error) {
	root := filepath.Dir(path)
	snap, err := corpus.Walk(root, corpus.DefaultPolicy(register))
	if err != nil {
		return store.SnapshotWrite{}, nil, err
	}
	name := filepath.Base(path)
	var document corpus.Document
	found := false
	for _, candidate := range snap.Documents {
		if candidate.Path == name {
			document, found = candidate, true
			break
		}
	}
	if !found || document.Admission != corpus.Eligible {
		return store.SnapshotWrite{}, nil, errors.New("rewrite draft is not an eligible corpus document")
	}
	document.Split = corpus.Draft
	snap.Documents = []corpus.Document{document}
	snap.ID = identity.HashInputs(snap.IdentityInputs())
	write, err := ingest.SnapshotWithRequirements(root, snap, requirements)
	if err != nil {
		return store.SnapshotWrite{}, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return store.SnapshotWrite{}, nil, err
	}
	doc, err := text.Admit(raw)
	if err != nil {
		return store.SnapshotWrite{}, nil, err
	}
	paragraphs, _, err := profile.ParagraphLeaves(doc, doc.Structure(text.DefaultStructureOptions()), requirements.MinParagraphLexicalTokens)
	if err != nil {
		return store.SnapshotWrite{}, nil, err
	}
	leaves := make([]draftLeaf, len(paragraphs))
	for i, paragraph := range paragraphs {
		leaves[i].excisions = len(paragraph.Node.Excisions) != 0
	}
	return write, leaves, nil
}

// uncalibratedCalibration records a completed Test measurement when Calibrate
// could not select either observed boundary. It deliberately emits no claiming
// bands: a made-up threshold would turn insufficient calibration evidence into
// a label. The release remains auditable and, because Calibrated is false,
// cannot advance the head.
func uncalibratedCalibration(discrimination eval.Discrimination, poolID string, floor eval.BandFloor, bootstrap eval.BootstrapSpec) eval.Calibration {
	thresholdsID := identity.HashInputs(map[string]string{"kind": "no-qualifying-threshold", "pool": poolID, "population": discrimination.PopulationID})
	calibration := eval.Calibration{
		ThresholdsID: thresholdsID, PopulationID: discrimination.PopulationID,
		ProfileID: discrimination.ProfileID, ReferenceID: discrimination.ReferenceID,
		FeatureManifestDigest: discrimination.FeatureManifestDigest, WeightScheme: discrimination.WeightScheme,
		DistanceAlgorithm: discrimination.DistanceAlgorithm, ScoredTiers: append([]features.Tier(nil), discrimination.ScoredTiers...),
		Split: corpus.Test, Floor: floor, Algorithm: eval.BandCalibrationAlgorithm,
		Bands: []eval.BandReport{
			{Band: eval.BandInRange, Claims: eval.ClassDistractor, Target: 0.10, ErrorBound: 1, MinClassClusters: 30, Reason: "empty-error-class"},
			{Band: eval.BandDrifting, Emitted: true},
			{Band: eval.BandNotYou, Claims: eval.ClassAuthor, Target: 0.05, ErrorBound: 1, MinClassClusters: 60, Reason: "empty-error-class"},
		},
		Reason: "no-claiming-band-emitted",
	}
	calibration.ID = identity.HashInputs(map[string]string{
		"bootstrap":  fmt.Sprintf("%g,%d,%d,%g", bootstrap.Confidence, bootstrap.Resamples, bootstrap.Seed, bootstrap.MinQualified),
		"kind":       "uncalibrated-calibration",
		"pool":       poolID,
		"population": calibration.PopulationID,
		"thresholds": calibration.ThresholdsID,
	})
	return calibration
}

func hashesForStored(ctx context.Context, s *store.Store, id string, split corpus.Split) ([]string, error) {
	w, err := s.Snapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range w.Documents {
		if d.Split == split {
			out = append(out, d.ContentHash)
		}
	}
	return out, nil
}
func diskSegments(root string, snap *corpus.Snapshot, class eval.Class, f profile.Fitted) ([]eval.Segment, error) {
	var out []eval.Segment
	for _, d := range snap.Eligible() {
		doc, err := snapshot.ReadVerified(root, d.Path, d.ContentHash)
		if err != nil {
			return nil, err
		}
		p, err := profile.ParagraphVectors(doc, f.MinParagraphLexicalTokens)
		if err != nil {
			return nil, err
		}
		for i, v := range p.Vectors {
			out = append(out, eval.Segment{Class: class, DocumentHash: d.ContentHash, DocumentPath: d.Path, Index: i, LexicalTokens: v.LexicalTokens, Vector: v})
		}
	}
	return out, nil
}
func distances(segments []eval.Segment, f profile.Fitted, ref *store.Reference, split corpus.Split) ([]eval.ClassedDistance, error) {
	r := &deviation.Reference{ID: ref.ID, ProfileID: ref.ProfileID, FeatureManifestDigest: ref.ManifestDigest, Split: ref.Split, MinSegments: ref.MinSegments, Values: ref.Values}
	var out []eval.ClassedDistance
	for _, s := range segments {
		z, err := deviation.Standardize(s.Vector, f, split)
		if err != nil {
			return nil, err
		}
		d, err := r.Transform(z)
		if err != nil {
			return nil, err
		}
		x, err := d.Distance()
		if err != nil {
			return nil, err
		}
		out = append(out, eval.ClassedDistance{Class: s.Class, Document: s.DocumentHash, Distance: x})
	}
	return out, nil
}
func storeDiscrimination(x eval.Discrimination) store.Discrimination {
	return store.Discrimination{ID: x.ID, PopulationID: x.PopulationID, Binding: store.Binding{ManifestDigest: x.FeatureManifestDigest, WeightScheme: x.WeightScheme, DistanceAlgorithm: x.DistanceAlgorithm, ScoredTiers: x.ScoredTiers}, Split: x.Split, Algorithm: x.Algorithm, Clustering: x.Clustering, Floor: x.Spec.Floor, Confidence: x.Spec.Confidence, Resamples: x.Spec.Resamples, Seed: x.Spec.Seed, AUC: x.AUC, LowerBound: x.LowerBound, Cap: x.Cap, AuthorSegments: x.AuthorSegments, DistractorSegments: x.DistractorSegments, AuthorClusters: x.AuthorClusters, DistractorClusters: x.DistractorClusters, MinClusters: x.MinClusters, Discriminates: x.Discriminates, Reason: x.Reason}
}
func storeCalibration(x eval.Calibration) store.Calibration {
	y := store.Calibration{ID: x.ID, ThresholdsID: x.ThresholdsID, PopulationID: x.PopulationID, Binding: store.Binding{ManifestDigest: x.FeatureManifestDigest, WeightScheme: x.WeightScheme, DistanceAlgorithm: x.DistanceAlgorithm, ScoredTiers: x.ScoredTiers}, Split: x.Split, Algorithm: x.Algorithm, Low: x.Low, High: x.High, Confidence: x.Floor.Confidence, Resamples: x.Floor.Resamples, Seed: x.Floor.Seed, Calibrated: x.Calibrated, Reason: x.Reason}
	for _, b := range x.Bands {
		y.Bands = append(y.Bands, store.BandReport{Band: b.Band, Claims: b.Claims, Target: b.Target, ErrorRate: b.ErrorRate, ErrorBound: b.ErrorBound, ClassSegments: b.ClassSegments, ClassClusters: b.ClassClusters, MinClassClusters: b.MinClassClusters, AuthorSegments: b.AuthorSegments, DistractorSegments: b.DistractorSegments, Emitted: b.Emitted, Reason: b.Reason})
	}
	return y
}
func calibrationReport(x eval.Calibration) CalibrationReport {
	r := CalibrationReport{Calibrated: x.Calibrated, Reason: x.Reason}
	for _, b := range x.Bands {
		r.Bands = append(r.Bands, BandReport{Band: string(b.Band), Claims: string(b.Claims), Target: b.Target, ErrorRate: b.ErrorRate, Emitted: b.Emitted, Reason: b.Reason})
	}
	return r
}

type Runner struct {
	Requirements    profile.Requirements
	MinSegments     int
	Discrimination  eval.DiscriminationSpec
	BandFloor       eval.BandFloor
	Bootstrap       eval.BootstrapSpec
	Providers       ProviderFactory
	NewInvocationID func() (string, error)
}

type ProviderChoice struct{ Provider, Model, Endpoint string }
type ProviderFactory struct {
	Local func(ProviderChoice) (rewrite.Provider, error)
	Cloud func(ProviderChoice) (rewrite.Provider, error)
}

var (
	ErrLocalOnlyForbidsProvider = errors.New("local-only mode forbids this provider")
	ErrUnknownProvider          = errors.New("unknown provider")
	ErrNoProviderFactory        = errors.New("provider factory is not configured")
)

func (r *Runner) Provider(m mode.Mode, choice ProviderChoice) (rewrite.Provider, error) {
	switch llm.ProviderID(choice.Provider) {
	case llm.ProviderOllama:
		if r.Providers.Local == nil {
			return nil, ErrNoProviderFactory
		}
		return r.Providers.Local(choice)
	case llm.ProviderAnthropic:
		if m.LocalOnly {
			return nil, ErrLocalOnlyForbidsProvider
		}
		if r.Providers.Cloud == nil {
			return nil, ErrNoProviderFactory
		}
		return r.Providers.Cloud(choice)
	default:
		return nil, ErrUnknownProvider
	}
}

func Default() *Runner { return New(profile.DefaultRequirements(), deviation.DefaultMinSegments()) }
func New(requirements profile.Requirements, minSegments int) *Runner {
	return &Runner{
		Requirements:    requirements,
		MinSegments:     minSegments,
		Discrimination:  eval.DefaultDiscrimination(),
		BandFloor:       eval.DefaultBandFloor(),
		Bootstrap:       eval.DefaultBootstrap(),
		NewInvocationID: newInvocationID,
	}
}

func newInvocationID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type executionScorer struct {
	fitted     profile.Fitted
	reference  *deviation.Reference
	release    eval.Release
	calibrated bool
}

func (s executionScorer) Score(source []byte) (score.Report, error) {
	if !s.calibrated {
		return score.Measure(source, s.fitted, s.reference)
	}
	return score.Score(source, s.fitted, s.reference, s.release)
}

type executionSelector struct{ texts []string }

func (s executionSelector) Exemplars(n int) ([]string, error) {
	if n != len(s.texts) {
		return nil, errors.New("unexpected exemplar count")
	}
	return append([]string(nil), s.texts...), nil
}

type executionGate struct{ register string }

func (g executionGate) Preserve(current, candidate string) (rewrite.Preservation, error) {
	x, err := preserve.Check(current, candidate)
	return rewrite.Preservation{Preserved: x.Preserved, Identifiers: x.Identifiers()}, err
}
func (g executionGate) Tells(current, candidate string) (rewrite.TellsVerdict, error) {
	a, e := text.Admit([]byte(current))
	if e != nil {
		return rewrite.TellsVerdict{}, e
	}
	b, e := text.Admit([]byte(candidate))
	if e != nil {
		return rewrite.TellsVerdict{}, e
	}
	rs := tells.Default()
	comparison, e := rs.Check(b, tells.Options{Register: g.register}).Comparison().Compare(rs.Check(a, tells.Options{Register: g.register}).Comparison())
	if errors.Is(e, tells.ErrIncomparable) {
		return rewrite.TellsVerdict{Comparison: comparison, Comparable: false}, nil
	}
	if e != nil {
		return rewrite.TellsVerdict{}, e
	}
	return rewrite.TellsVerdict{Comparison: comparison, Comparable: true}, nil
}

// Execute runs a previously qualified plan, retaining no authority to write its
// assembled bytes anywhere.
func (r *Runner) Execute(ctx context.Context, request ExecuteRequest) (ExecuteResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecuteResult{}, err
	}
	p := request.Plan
	if err := validateExecutePlan(p); err != nil {
		return ExecuteResult{}, err
	}
	// A public plan is a capability: prove its stored references before even
	// constructing a provider.
	s, err := store.Open(p.StorePath)
	if err != nil {
		return ExecuteResult{}, err
	}
	if err := validateStoredExecutePlan(ctx, s, p); err != nil {
		_ = s.Close()
		return ExecuteResult{}, err
	}
	defer s.Close()
	result := ExecuteResult{Targets: p.Targets}
	var provider rewrite.Provider
	if p.Targets != 0 {
		var err error
		provider, err = r.Provider(request.Mode, request.Choice)
		if errors.Is(err, ErrLocalOnlyForbidsProvider) {
			result.Refusal = RefusalLocalOnlyForbidsProvider
			return result, nil
		}
		if err != nil {
			return ExecuteResult{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ExecuteResult{}, err
	}
	prof, err := s.LoadProfile(ctx, p.ProfileID)
	if err != nil {
		return ExecuteResult{}, err
	}
	fitted, err := prof.Fitted()
	if err != nil {
		return ExecuteResult{}, err
	}
	storedRef, err := s.LoadReference(ctx, p.ReferenceID)
	if err != nil {
		return ExecuteResult{}, err
	}
	ref := &deviation.Reference{ID: storedRef.ID, ProfileID: storedRef.ProfileID, FeatureManifestDigest: storedRef.ManifestDigest, Split: storedRef.Split, MinSegments: storedRef.MinSegments, Values: storedRef.Values}
	var release eval.Release
	if p.CalibrationAvailable {
		release, err = s.LoadRelease(ctx, p.ReleaseID)
		if err != nil {
			return ExecuteResult{}, err
		}
	}
	draftSnapshot, err := s.Snapshot(ctx, p.DraftSnapshotID)
	if err != nil {
		return ExecuteResult{}, err
	}
	if len(draftSnapshot.Documents) != 1 || draftSnapshot.Documents[0].Split != corpus.Draft {
		return ExecuteResult{}, errors.New("invalid draft snapshot")
	}
	doc, fresh, err := readFresh(p.Path, draftSnapshot.Documents[0].ContentHash)
	if err != nil {
		return ExecuteResult{}, err
	}
	if !fresh {
		result.Refusal = RefusalStaleDraft
		return result, nil
	}
	if p.Targets == 0 {
		after, fresh, err := readFresh(p.Path, draftSnapshot.Documents[0].ContentHash)
		if err != nil {
			return ExecuteResult{}, err
		}
		if !fresh || after.HadBOM() != doc.HadBOM() {
			result.Refusal = RefusalStaleDraft
			return result, nil
		}
		bytes, err := assemble.Assemble(doc, nil)
		if err != nil {
			return ExecuteResult{}, err
		}
		result.Bytes, result.State = bytes, RewriteNoTargets
		return result, nil
	}
	selection, err := s.LoadExemplarSelection(ctx, p.ExemplarSelectionID)
	if err != nil {
		return ExecuteResult{}, err
	}
	rehydrated, err := s.Rehydrate(ctx, p.CorpusRoot, selection.Members)
	if err != nil {
		return ExecuteResult{}, err
	}
	texts := make([]string, len(rehydrated))
	for i, x := range rehydrated {
		if x.Outcome != store.OutcomeOK {
			result.Refusal = RefusalStaleExemplars
			return result, nil
		}
		texts[i] = x.Text
	}
	if r.NewInvocationID == nil {
		return ExecuteResult{}, errors.New("invocation id generator is required")
	}
	id, err := r.NewInvocationID()
	if err != nil || id == "" {
		if err == nil {
			err = errors.New("empty invocation id")
		}
		return ExecuteResult{}, err
	}
	result.InvocationID = id
	scorer := executionScorer{fitted: fitted, reference: ref, release: release, calibrated: p.CalibrationAvailable}
	for _, target := range p.Segments {
		if target.Disposition != DispositionTarget {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		report, err := scorer.Score(doc.Raw()[target.Offset : target.Offset+target.Length])
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("preflight segment %d: %w", target.Index, err)
		}
		if len(report.Segments) != 1 || !report.Segments[0].Distance.Defined || (p.Targeting != TargetingExplicit && (!report.Calibrated || !report.Segments[0].Band.Defined)) {
			return ExecuteResult{}, fmt.Errorf("preflight segment %d cannot score", target.Index)
		}
	}
	var replacements []assemble.Replacement
	options := rewrite.DefaultOptions()
	options.ProfileID = p.ProfileID
	options.InvocationID = id
	options.ProviderID = request.Choice.Provider
	options.LocalOnly = request.Mode.LocalOnly
	options.AllowUncalibrated = p.Targeting == TargetingExplicit
	if request.Attempts < 0 {
		return ExecuteResult{}, errors.New("negative attempts")
	}
	if request.Attempts > 0 {
		options.Attempts = request.Attempts
	}
	options.Exemplars = len(texts)
	for _, target := range p.Segments {
		if target.Disposition != DispositionTarget {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		passage := string(doc.Raw()[target.Offset : target.Offset+target.Length])
		loop := rewrite.Loop{Scorer: scorer, Selector: executionSelector{texts}, Gate: executionGate{prof.Register}, Provider: provider, Store: s.Recorder(ctx), Options: options}
		out, err := loop.Rewrite(ctx, rewrite.Segment{Text: passage, SpanRef: target.NodeID})
		if err != nil {
			return ExecuteResult{}, err
		}
		if out.Terminal == rewrite.TerminalNotEntered {
			return ExecuteResult{}, errors.New("preflight target was not entered")
		}
		x := TargetOutcome{Index: target.Index, NodeID: target.NodeID, Changed: out.Changed, Terminal: string(out.Terminal)}
		for _, a := range out.Attempts {
			x.Rejections = append(x.Rejections, string(a.Rejection))
		}
		result.Outcomes = append(result.Outcomes, x)
		if out.Changed {
			result.Improved++
			replacements = append(replacements, assemble.Replacement{Span: text.Span{Offset: target.Offset, Length: target.Length}, Text: out.Text})
		}
	}
	after, fresh, err := readFresh(p.Path, draftSnapshot.Documents[0].ContentHash)
	if err != nil {
		return ExecuteResult{}, err
	}
	if !fresh || after.HadBOM() != doc.HadBOM() {
		result.Refusal = RefusalStaleDraft
		return result, nil
	}
	bytes, err := assemble.Assemble(doc, replacements)
	if err != nil {
		return ExecuteResult{}, err
	}
	result.Bytes = bytes
	if result.Improved > 0 {
		result.State = RewriteImproved
	} else {
		result.State = RewriteNoneImproved
	}
	return result, nil
}

// Rewrite validates provider choice before Plan can persist a draft snapshot,
// then executes the resulting plan without exposing either intermediate state
// or assembled bytes to the composition root.
func (r *Runner) Rewrite(ctx context.Context, request RewriteInput) (RewriteOutcome, error) {
	if _, err := r.Provider(request.Mode, request.Choice); err != nil {
		if errors.Is(err, ErrLocalOnlyForbidsProvider) {
			return NewRewriteOutcome(RewriteReport{Refusal: RefusalLocalOnlyForbidsProvider}, nil), nil
		}
		return RewriteOutcome{}, err
	}
	plan, err := r.Plan(ctx, PlanRequest{
		StartDir: request.StartDir, StorePath: request.StorePath, CorpusRoot: request.CorpusRoot,
		Register: request.Register, Path: request.Path, Paragraphs: request.Paragraphs,
	})
	if err != nil {
		return RewriteOutcome{}, err
	}
	if plan.Refusal != "" {
		return NewRewriteOutcome(RewriteReport{PlanState: plan.State, Refusal: plan.Refusal, Targeting: plan.Targeting, Claim: plan.Claim, CalibrationAvailable: plan.CalibrationAvailable}, nil), nil
	}
	executed, err := r.Execute(ctx, ExecuteRequest{Plan: plan, Choice: request.Choice, Mode: request.Mode, Attempts: request.Attempts})
	if err != nil {
		return RewriteOutcome{}, err
	}
	return NewRewriteOutcome(RewriteReport{
		PlanState: plan.State, State: executed.State, Targets: executed.Targets,
		Improved: executed.Improved, Refusal: executed.Refusal, Outcomes: executed.Outcomes,
		Targeting: plan.Targeting, Claim: plan.Claim, CalibrationAvailable: plan.CalibrationAvailable,
	}, executed.Bytes), nil
}

// readFresh reports whether the draft on disk is still the one that was planned.
//
// Only the two ways a draft can have MOVED are a refusal: it no longer admits,
// or its admitted bytes hash differently. Any other failure propagates, because
// a future error class silently becoming stale-draft would turn an operational
// problem into a refusal the user is told is their file's fault.
func readFresh(path, hash string) (*text.Document, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	doc, err := snapshot.VerifyAdmitted(raw, hash)
	if err == nil {
		return doc, true, nil
	}
	var admission *text.AdmissionError
	if errors.As(err, &admission) || errors.Is(err, snapshot.ErrContentChanged) {
		return nil, false, nil
	}
	return nil, false, err
}
func sameStrings(a, b []string) bool {
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
func validateExecutePlan(p RewritePlan) error {
	if p.Refusal != "" || (p.State != StateNothingToChange && p.State != StateTargetsPlanned) {
		return errors.New("invalid rewrite plan")
	}
	n := 0
	seen := map[string]bool{}
	for _, x := range p.Segments {
		if x.Disposition == DispositionTarget {
			n++
			if x.NodeID == "" || x.Length <= 0 || seen[x.NodeID] {
				return errors.New("invalid rewrite target")
			}
			seen[x.NodeID] = true
		}
	}
	if n != p.Targets || (p.State == StateNothingToChange) != (p.Targets == 0) {
		return errors.New("invalid rewrite plan")
	}
	return nil
}

func validateStoredExecutePlan(ctx context.Context, s *store.Store, p RewritePlan) error {
	snap, err := s.Snapshot(ctx, p.DraftSnapshotID)
	if err != nil {
		return err
	}
	if len(snap.Documents) != 1 || snap.Documents[0].Split != corpus.Draft {
		return errors.New("invalid draft snapshot")
	}
	nodes := map[string]store.Node{}
	for _, n := range snap.Documents[0].Nodes {
		nodes[n.ID] = n
	}
	for _, segment := range p.Segments {
		if segment.Disposition != DispositionTarget {
			continue
		}
		n, ok := nodes[segment.NodeID]
		if !ok || n.Vector == nil || n.Offset != segment.Offset || n.Length != segment.Length {
			return errors.New("invalid rewrite target")
		}
	}
	if p.Targets == 0 {
		return nil
	}
	selection, err := s.LoadExemplarSelection(ctx, p.ExemplarSelectionID)
	if err != nil {
		return err
	}
	if selection.ProfileID != p.ProfileID || !sameStrings(selection.Members, p.ExemplarNodes) {
		return errors.New("invalid exemplar selection")
	}
	return nil
}

func (r *Runner) Index(ctx context.Context, request IndexRequest) (IndexResult, error) {
	if request.CorpusRoot == "" {
		return IndexResult{}, errors.New("index corpus root is required")
	}
	if request.Register == "" {
		return IndexResult{}, errors.New("index register is required")
	}
	snapshot, err := corpus.Walk(request.CorpusRoot, corpus.DefaultPolicy(request.Register))
	if err != nil {
		return IndexResult{}, err
	}
	write, err := ingest.SnapshotWithRequirements(request.CorpusRoot, snapshot, r.Requirements)
	if err != nil {
		return IndexResult{}, err
	}
	result := indexResult(snapshot, write, request)
	mode := IndexSnapshotOnly
	var built *profile.Profile
	built, err = profile.Build(request.CorpusRoot, snapshot, r.Requirements)
	if err != nil && !errors.Is(err, profile.ErrCorpusTooSmall) {
		return IndexResult{}, err
	}
	if errors.Is(err, profile.ErrCorpusTooSmall) {
		result.Adverse, result.Adversity = true, AdversityCorpusTooSmall
		return r.commit(ctx, request, mode, write, nil, nil, result)
	}
	// Ingest owns the persisted snapshot identity. Profile owns the rule that
	// derives its identity from that snapshot.
	built.RebindSnapshot(write.ID)
	mode = IndexProfile
	result.ProfileID, result.NotReadyReason = built.ID, built.NotProductionReason
	segments, err := ingest.CalibrateStandardizations(request.CorpusRoot, snapshot, built)
	if err != nil {
		return IndexResult{}, err
	}
	result.CalibrateSegments = len(segments)
	reference, err := deviation.BuildReference(built, corpus.Calibrate, segments, r.MinSegments)
	if err != nil && !errors.Is(err, deviation.ErrReferenceTooSmall) {
		return IndexResult{}, err
	}
	if errors.Is(err, deviation.ErrReferenceTooSmall) {
		result.Adverse, result.Adversity = true, AdversityReferenceTooSmall
		return r.commit(ctx, request, mode, write, built, nil, result)
	}
	mode, result.ReferenceID = IndexProfileAndReference, reference.ID
	return r.commit(ctx, request, mode, write, built, reference, result)
}

func (r *Runner) commit(ctx context.Context, request IndexRequest, mode IndexMode, snapshot store.SnapshotWrite, p *profile.Profile, reference *deviation.Reference, result IndexResult) (IndexResult, error) {
	path := request.StorePath
	if path == "" {
		path = filepath.Join(request.CorpusRoot, ".hapax", "hapax.sqlite3")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return IndexResult{}, err
		}
	}
	s, err := store.Open(path)
	if err != nil {
		return IndexResult{}, err
	}
	defer s.Close()
	w := store.IndexWrite{Mode: store.IndexMode(mode), Snapshot: snapshot}
	if p != nil {
		w.Profile = storedProfile(p)
	}
	if reference != nil {
		w.Reference = storedReference(reference)
	}
	w.LockWait = request.LockWait
	indexed, err := s.Index(ctx, w)
	if err != nil {
		return IndexResult{}, err
	}
	result.StorePath, result.Mode = path, mode
	result.Pruned = Pruned{Snapshots: indexed.Pruned.Snapshots, Documents: indexed.Pruned.Documents, Nodes: indexed.Pruned.Nodes, Profiles: indexed.Pruned.Profiles}
	return result, nil
}

func (r *Runner) Profile(ctx context.Context, request ProfileRequest) (ProfileResult, error) {
	path, found, err := discover(request.StartDir, request.StorePath)
	if err != nil {
		return ProfileResult{}, err
	}
	if !found {
		return ProfileResult{Selection: SelectionNoProfile}, nil
	}
	s, err := store.Open(path)
	if err != nil {
		return ProfileResult{}, err
	}
	defer s.Close()
	heads, err := s.ProfileHeads(ctx)
	if err != nil {
		return ProfileResult{}, err
	}
	available := availableRegisters(heads)
	result := ProfileResult{StorePath: path, Available: available}
	register := request.Register
	if register == "" {
		switch len(available) {
		case 0:
			result.Selection = SelectionNoProfile
			return result, nil
		case 1:
			register, result.Selection = available[0], SelectedSoleHead
		default:
			result.Selection = SelectionAmbiguous
			return result, nil
		}
	} else {
		if _, ok := heads[register]; !ok {
			result.Selection = SelectionUnknownRegister
			return result, nil
		}
		result.Selection = SelectedExplicit
	}
	bundle, err := s.LoadProfileBundle(ctx, register)
	if err != nil {
		return ProfileResult{}, err
	}
	result.Profile = workflowProfile(bundle.Profile)
	result.Evaluated = bundle.Evaluated
	result.ReferenceID = bundle.Reference.ID
	return result, nil
}

func availableRegisters(heads map[string]string) []string {
	available := make([]string, 0, len(heads))
	for register := range heads {
		available = append(available, register)
	}
	sort.Strings(available)
	return available
}

func discover(startDir, storePath string) (string, bool, error) {
	if storePath != "" {
		if _, err := os.Stat(storePath); err != nil {
			return "", false, err
		}
		return storePath, true, nil
	}
	dir := startDir
	if dir == "" {
		return "", false, errors.New("profile start directory is required")
	}
	for {
		marker := filepath.Join(dir, ".hapax")
		info, err := os.Stat(marker)
		if err == nil {
			if !info.IsDir() {
				return "", false, fmt.Errorf("%s is not a directory", marker)
			}
			path := filepath.Join(marker, "hapax.sqlite3")
			if _, err := os.Stat(path); err == nil {
				return path, true, nil
			} else if errors.Is(err, os.ErrNotExist) {
				return "", false, nil
			} else {
				return "", false, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

func indexResult(snap *corpus.Snapshot, write store.SnapshotWrite, request IndexRequest) IndexResult {
	r := IndexResult{SnapshotID: write.ID, Documents: len(snap.Documents), Checks: checks(snap)}
	r.Eligible = len(snap.Eligible())
	for _, d := range write.Documents {
		r.Nodes += len(d.Nodes)
		if d.Split == corpus.Train {
			for _, n := range d.Nodes {
				if n.Vector != nil {
					r.TrainParagraphs++
				}
			}
		}
	}
	return r
}
func checks(s *corpus.Snapshot) []Check {
	check := func(name string, value corpus.CheckStatus) Check {
		return Check{Name: name, State: string(value.State), Reason: value.Reason, Version: value.Version}
	}
	return []Check{
		check("contamination", s.Contamination),
		check("language", s.Language),
		check("structure", s.Structure),
		check("git-provenance", s.GitProvenance),
		check("near-duplicate-detection", s.NearDuplicateDetection),
	}
}
func storedProfile(p *profile.Profile) store.Profile {
	x := store.Profile{ID: p.ID, SnapshotID: p.SnapshotID, Register: p.Register, Unit: p.Unit, VarianceConvention: p.VarianceConvention, ManifestDigest: p.FeatureManifestDigest, FeatureSetVersion: p.FeatureSetVersion, MinParagraphLexicalTokens: p.Requirements.MinParagraphLexicalTokens, ProductionReady: p.ProductionReady, NotReadyReason: p.NotProductionReason}
	for _, s := range p.Stats {
		x.Stats = append(x.Stats, store.ProfileStat{Feature: s.Feature, N: s.N, Mean: s.Mean, Variance: s.Variance, Defined: s.Defined, VarianceDefined: s.VarianceDefined, MinObservations: s.MinObservations})
	}
	return x
}
func storedReference(r *deviation.Reference) store.Reference {
	return store.Reference{ID: r.ID, ProfileID: r.ProfileID, Split: r.Split, MinSegments: r.MinSegments, ManifestDigest: r.FeatureManifestDigest, Values: r.Values}
}
func workflowProfile(p store.Profile) StoredProfile {
	x := StoredProfile{ID: p.ID, SnapshotID: p.SnapshotID, Register: p.Register, ProductionReady: p.ProductionReady, NotReadyReason: p.NotReadyReason}
	for _, s := range p.Stats {
		x.Stats = append(x.Stats, StoredStat{Feature: string(s.Feature), N: s.N, Mean: s.Mean, Variance: s.Variance, Defined: s.Defined, VarianceDefined: s.VarianceDefined, MinObservations: s.MinObservations})
	}
	return x
}
