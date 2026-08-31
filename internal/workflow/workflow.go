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
	"github.com/fissible/hapax/internal/ingest"
	"github.com/fissible/hapax/internal/profile"
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

// Service is the sole execution seam required by the CLI.
type Service interface {
	Index(context.Context, IndexRequest) (IndexResult, error)
	Profile(context.Context, ProfileRequest) (ProfileResult, error)
}
type Runner struct {
	Requirements profile.Requirements
	MinSegments  int
}

func Default() *Runner { return New(profile.DefaultRequirements(), deviation.DefaultMinSegments()) }
func New(requirements profile.Requirements, minSegments int) *Runner {
	return &Runner{Requirements: requirements, MinSegments: minSegments}
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
	path, found, err := discover(request)
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

func discover(request ProfileRequest) (string, bool, error) {
	if request.StorePath != "" {
		if _, err := os.Stat(request.StorePath); err != nil {
			return "", false, err
		}
		return request.StorePath, true, nil
	}
	dir := request.StartDir
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
