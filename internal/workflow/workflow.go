// Package workflow owns Hapax's indexing and profile-query composition.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/ingest"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/snapshot"
	"github.com/fissible/hapax/internal/store"
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
}

func fittedFrom(p store.Profile) (profile.Fitted, error) {
	f := profile.Fitted{ID: p.ID, Unit: p.Unit, FeatureSetVersion: p.FeatureSetVersion, FeatureManifestDigest: p.ManifestDigest, MinParagraphLexicalTokens: p.MinParagraphLexicalTokens, Stats: make([]profile.Stats, 0, len(p.Stats))}
	for _, s := range p.Stats {
		f.Stats = append(f.Stats, profile.Stats{Feature: s.Feature, N: s.N, Mean: s.Mean, Variance: s.Variance, Defined: s.Defined, VarianceDefined: s.VarianceDefined, MinObservations: s.MinObservations})
	}
	if f.ID == "" || f.Unit != profile.UnitParagraph || f.FeatureSetVersion != features.SetVersion || f.FeatureManifestDigest != features.ManifestDigest() || f.MinParagraphLexicalTokens <= 0 || len(f.Stats) == 0 {
		return profile.Fitted{}, errors.New("stored profile cannot score")
	}
	return f, nil
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
	fitted, err := fittedFrom(bundle.Profile)
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
	stored := store.EvalResult{ID: release.ID, ProfileID: fitted.ID, ReferenceID: bundle.Reference.ID, DistractorPoolID: pool.ID, Shippable: release.Shippable, Reason: eval.ReleaseReason(release.Reason), Discrimination: storeDiscrimination(discrimination), Calibration: storeCalibration(calibration)}
	if err = s.PutEvalResult(ctx, stored, store.HeadPolicy(release.Shippable)); err != nil {
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
	Requirements   profile.Requirements
	MinSegments    int
	Discrimination eval.DiscriminationSpec
	BandFloor      eval.BandFloor
	Bootstrap      eval.BootstrapSpec
}

func Default() *Runner { return New(profile.DefaultRequirements(), deviation.DefaultMinSegments()) }
func New(requirements profile.Requirements, minSegments int) *Runner {
	return &Runner{
		Requirements:   requirements,
		MinSegments:    minSegments,
		Discrimination: eval.DefaultDiscrimination(),
		BandFloor:      eval.DefaultBandFloor(),
		Bootstrap:      eval.DefaultBootstrap(),
	}
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
	available := make([]string, 0, len(heads))
	for register := range heads {
		available = append(available, register)
	}
	sort.Strings(available)
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
