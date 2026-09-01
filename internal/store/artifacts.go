package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/rewrite"
)

// Profile is the persisted statistical profile for a snapshot and register.
type Profile struct {
	ID, SnapshotID, Register                     string
	Unit                                         profile.Unit
	VarianceConvention                           profile.VarianceConvention
	ManifestDigest                               string
	FeatureSetVersion, MinParagraphLexicalTokens int
	ProductionReady                              bool
	NotReadyReason                               string
	Stats                                        []ProfileStat
}

// ProfileStat is the summary statistic for one profile feature.
type ProfileStat struct {
	Feature                  features.ID
	N                        int
	Mean, Variance           float64
	Defined, VarianceDefined bool
	MinObservations          int
}

// Fitted projects a persisted profile into the one scoring representation.
func (p Profile) Fitted() (profile.Fitted, error) {
	f := profile.Fitted{ID: p.ID, Unit: p.Unit, FeatureSetVersion: p.FeatureSetVersion, FeatureManifestDigest: p.ManifestDigest, MinParagraphLexicalTokens: p.MinParagraphLexicalTokens, Stats: make([]profile.Stats, 0, len(p.Stats))}
	for _, stat := range p.Stats {
		f.Stats = append(f.Stats, profile.Stats{Feature: stat.Feature, N: stat.N, Mean: stat.Mean, Variance: stat.Variance, Defined: stat.Defined, VarianceDefined: stat.VarianceDefined, MinObservations: stat.MinObservations})
	}
	if f.ID == "" || f.Unit != profile.UnitParagraph || f.FeatureSetVersion != features.SetVersion || f.FeatureManifestDigest != features.ManifestDigest() || f.MinParagraphLexicalTokens <= 0 || len(f.Stats) == 0 {
		return profile.Fitted{}, errors.New("stored profile cannot score")
	}
	return f, nil
}

// Reference is the persisted reference distribution for a profile split.
type Reference struct {
	ID, ProfileID  string
	Split          corpus.Split
	MinSegments    int
	ManifestDigest string
	Values         map[features.ID][]float64
}

// Threshold is the persisted calibration threshold for a reference population.
type Threshold struct {
	ID, ProfileID, ReferenceID, PopulationID      string
	Low, High, AchievedAuthor, AchievedDistractor float64
	IntervalLow, IntervalHigh                     eval.Interval
	Verdict                                       eval.ThresholdVerdict
}

// EvalResult is the persisted release evaluation for a reference distribution.
type EvalResult struct {
	ID, ProfileID, ReferenceID, DistractorPoolID string
	Discrimination                               Discrimination
	Calibration                                  Calibration
	Shippable                                    bool
	Reason                                       eval.ReleaseReason
}

// DistractorPool records content-addressed membership without locations.
type DistractorPool struct {
	ID, PolicyDigest string
	Members          int
	ContentHashes    []string
}

func DistractorPoolID(policyDigest string, hashes []string) string {
	ordered := append([]string(nil), hashes...)
	sort.Strings(ordered)
	return identity.HashInputs(map[string]string{"policy": policyDigest, "members": string(identity.Frame(ordered...))})
}

func (s *Store) PutDistractorPool(ctx context.Context, pool DistractorPool) error {
	ordered := append([]string(nil), pool.ContentHashes...)
	sort.Strings(ordered)
	if !validHash(pool.ID) || !validHash(pool.PolicyDigest) || len(ordered) == 0 {
		return ErrInvalid
	}
	for i, h := range ordered {
		if !validHash(h) || i > 0 && h == ordered[i-1] {
			return ErrInvalid
		}
	}
	pool.ContentHashes, pool.Members = ordered, len(ordered)
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		got, err := s.loadDistractorPool(c, ctx, pool.ID)
		if err == nil {
			if got.PolicyDigest == pool.PolicyDigest && reflect.DeepEqual(got.ContentHashes, pool.ContentHashes) {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if pool.ID != DistractorPoolID(pool.PolicyDigest, ordered) {
			return ErrInvalid
		}
		if _, err = c.ExecContext(ctx, "INSERT INTO distractor_pool (id,policy_digest,members,created_at) VALUES (?,?,?,?)", pool.ID, pool.PolicyDigest, pool.Members, s.deps.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		for _, hash := range pool.ContentHashes {
			if _, err = c.ExecContext(ctx, "INSERT INTO distractor_pool_member (pool_id,content_hash) VALUES (?,?)", pool.ID, hash); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Store) LoadDistractorPool(ctx context.Context, id string) (DistractorPool, error) {
	return s.loadDistractorPool(s.db, ctx, id)
}
func (s *Store) loadDistractorPool(q queryer, ctx context.Context, id string) (DistractorPool, error) {
	var p DistractorPool
	if err := q.QueryRowContext(ctx, "SELECT id,policy_digest,members FROM distractor_pool WHERE id=?", id).Scan(&p.ID, &p.PolicyDigest, &p.Members); errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	} else if err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, "SELECT content_hash FROM distractor_pool_member WHERE pool_id=? ORDER BY content_hash", id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return p, err
		}
		p.ContentHashes = append(p.ContentHashes, h)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if p.Members != len(p.ContentHashes) || p.ID != DistractorPoolID(p.PolicyDigest, p.ContentHashes) {
		return p, ErrCorrupt
	}
	return p, nil
}

// Binding is the distance contract shared by both release gates.
type Binding struct {
	ManifestDigest                  string
	WeightScheme, DistanceAlgorithm string
	ScoredTiers                     []features.Tier
}
type Discrimination struct {
	ID, PopulationID                                                                    string
	Binding                                                                             Binding
	Split                                                                               corpus.Split
	Algorithm                                                                           string
	Clustering                                                                          eval.Clustering
	Floor, Confidence                                                                   float64
	Resamples                                                                           int
	Seed                                                                                uint64
	AUC, LowerBound, Cap                                                                float64
	AuthorSegments, DistractorSegments, AuthorClusters, DistractorClusters, MinClusters int
	Discriminates                                                                       bool
	Reason                                                                              string
}

// BandReport is the storage representation of eval.BandReport. Keeping this
// as a store-owned struct makes the persistence allowlist explicit.
type BandReport struct {
	Band                                           eval.Band
	Claims                                         eval.Class
	Target, ErrorRate, ErrorBound                  float64
	ClassSegments, ClassClusters, MinClassClusters int
	AuthorSegments, DistractorSegments             int
	Emitted                                        bool
	Reason                                         string
}
type Calibration struct {
	ID, ThresholdsID, PopulationID string
	Binding                        Binding
	Split                          corpus.Split
	Algorithm                      string
	Low, High, Confidence          float64
	Resamples                      int
	Seed                           uint64
	Bands                          []BandReport
	Calibrated                     bool
	Reason                         string
}
type ProfileBundle struct {
	Profile   Profile
	Reference Reference
	Release   EvalResult
	Evaluated bool
}

// ScoringBundle is the complete, domain-level input for one score invocation.
type ScoringBundle struct {
	Fitted     profile.Fitted
	Reference  deviation.Reference
	Release    eval.Release
	Calibrated bool
}

// ExemplarSelection is the ordered exemplar-node selection for a profile.
type ExemplarSelection struct {
	ID, ProfileID string
	N             int
	CertificateID string
	Members       []string
}

// RewriteAttempt is the privacy-preserving record of one rewrite decision.
type RewriteAttempt struct {
	InvocationID                       string
	Index                              int
	ProfileID                          string
	ProviderID                         llm.ProviderID
	NodeID, CurrentHash, CandidateHash string
	CurrentDistance, CandidateDistance float64
	CurrentBand, CandidateBand         eval.Band
	Preserved                          bool
	PreserveIdentifiers                []string
	TellsComparison                    int
	TellsComparable, Accepted          bool
	Rejection                          rewrite.RejectionCode
}

// HeadPolicy controls whether a profile write advances its register head.
type HeadPolicy bool

const (
	LeaveHead   HeadPolicy = false
	AdvanceHead HeadPolicy = true
)

func artifactTx(ctx context.Context, store *Store, write func(*sql.Conn) error) error {
	conn, err := store.immediate(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err = write(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
func one(query queryer, ctx context.Context, statement string, arguments ...any) (bool, error) {
	var count int
	if err := query.QueryRowContext(ctx, statement, arguments...).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}
func validFeature(feature features.ID) bool {
	for _, definition := range features.Definitions() {
		if feature == definition.ID {
			return true
		}
	}
	return false
}
func sameSet[T comparable](first, second []T) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
func finiteAll(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}
func known[T comparable](value T, values []T) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func invalidProfileField(stored Profile) string {
	switch {
	case !validHash(stored.ID):
		return "id"
	case !validHash(stored.SnapshotID):
		return "snapshot id"
	case !validRegister(stored.Register):
		return "register"
	case stored.ManifestDigest != features.ManifestDigest():
		return "manifest digest"
	case stored.FeatureSetVersion != features.SetVersion:
		return "feature set version"
	case stored.MinParagraphLexicalTokens < 0:
		return "minimum paragraph lexical tokens"
	case !known(stored.Unit, profile.Units()):
		return "unit"
	case !known(stored.VarianceConvention, profile.VarianceConventions()):
		return "variance convention"
	case stored.ProductionReady != (stored.NotReadyReason == ""):
		return "production readiness"
	case stored.NotReadyReason != "" && !known(stored.NotReadyReason, profile.NotReadyReasons()):
		return "not ready reason"
	case len(stored.Stats) != len(features.Definitions()):
		return "statistics"
	}
	seen := map[features.ID]bool{}
	for _, statistic := range stored.Stats {
		switch {
		case !validFeature(statistic.Feature):
			return "statistic feature"
		case seen[statistic.Feature]:
			return "statistic feature duplicate"
		case statistic.N < 0:
			return "statistic count"
		case statistic.MinObservations < 0:
			return "statistic minimum observations"
		case !finiteAll(statistic.Mean, statistic.Variance):
			return "statistic values"
		}
		seen[statistic.Feature] = true
	}
	return ""
}

func validProfile(stored Profile) bool { return invalidProfileField(stored) == "" }

func invalidArtifact(kind, field string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, kind, field)
}

// PutProfile creates an immutable profile and optionally advances its register head.
func (s *Store) PutProfile(ctx context.Context, p Profile, h HeadPolicy) error {
	if !validProfile(p) {
		return invalidArtifact("profile", invalidProfileField(p))
	}
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		stored, err := s.loadProfile(c, ctx, p.ID)
		if err == nil {
			if !sameProfile(stored, p) {
				return ErrConflict
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		} else {
			if err = s.putProfileConn(ctx, c, p, false); err != nil {
				return err
			}
		}
		if h {
			_, err = c.ExecContext(ctx, "INSERT INTO profile_head (register,profile_id,updated_at) VALUES (?,?,?) ON CONFLICT(register) DO UPDATE SET profile_id=excluded.profile_id,updated_at=excluded.updated_at", p.Register, p.ID, s.deps.Now().UTC().Format(time.RFC3339))
			return err
		}
		return nil
	})
}

// LoadProfile returns a profile and all of its feature statistics.
func (s *Store) LoadProfile(ctx context.Context, id string) (Profile, error) {
	return s.loadProfile(s.db, ctx, id)
}
func (s *Store) loadProfile(query queryer, ctx context.Context, id string) (Profile, error) {
	var stored Profile
	var unit, varianceConvention string
	var productionReady int
	err := query.QueryRowContext(ctx, "SELECT id,snapshot_id,register,unit,variance_convention,manifest_digest,feature_set_version,min_paragraph_lexical_tokens,production_ready,not_ready_reason FROM profile WHERE id=?", id).Scan(&stored.ID, &stored.SnapshotID, &stored.Register, &unit, &varianceConvention, &stored.ManifestDigest, &stored.FeatureSetVersion, &stored.MinParagraphLexicalTokens, &productionReady, &stored.NotReadyReason)
	if errors.Is(err, sql.ErrNoRows) {
		return stored, ErrNotFound
	}
	if err != nil {
		return stored, err
	}
	stored.Unit = profile.Unit(unit)
	stored.VarianceConvention = profile.VarianceConvention(varianceConvention)
	stored.ProductionReady = productionReady != 0
	rows, err := query.QueryContext(ctx, "SELECT feature,n,mean,variance,defined,variance_defined,min_observations FROM profile_stat WHERE profile_id=? ORDER BY feature", id)
	if err != nil {
		return stored, err
	}
	defer rows.Close()
	statistics := map[features.ID]ProfileStat{}
	for rows.Next() {
		var statistic ProfileStat
		var defined, varianceDefined int
		if err = rows.Scan(&statistic.Feature, &statistic.N, &statistic.Mean, &statistic.Variance, &defined, &varianceDefined, &statistic.MinObservations); err != nil {
			return stored, err
		}
		statistic.Defined = defined != 0
		statistic.VarianceDefined = varianceDefined != 0
		statistics[statistic.Feature] = statistic
	}
	if err = rows.Err(); err != nil {
		return stored, err
	}
	for _, definition := range features.Definitions() {
		statistic, ok := statistics[definition.ID]
		if !ok {
			return stored, ErrCorrupt
		}
		stored.Stats = append(stored.Stats, statistic)
	}
	if len(statistics) != len(stored.Stats) || !validProfile(stored) {
		return stored, ErrCorrupt
	}
	return stored, nil
}
func sameProfile(a, b Profile) bool {
	if a.ID != b.ID || a.SnapshotID != b.SnapshotID || a.Register != b.Register || a.Unit != b.Unit || a.VarianceConvention != b.VarianceConvention || a.ManifestDigest != b.ManifestDigest || a.FeatureSetVersion != b.FeatureSetVersion || a.MinParagraphLexicalTokens != b.MinParagraphLexicalTokens || a.ProductionReady != b.ProductionReady || a.NotReadyReason != b.NotReadyReason || len(a.Stats) != len(b.Stats) {
		return false
	}
	for index := range a.Stats {
		if a.Stats[index] != b.Stats[index] {
			return false
		}
	}
	return true
}

// ProfileHead returns the profile currently selected for a register.
func (s *Store) ProfileHead(ctx context.Context, register string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT profile_id FROM profile_head WHERE register=?", register).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func invalidReferenceField(stored Reference) string {
	if !validHash(stored.ID) || !validHash(stored.ProfileID) {
		return "identity"
	}
	if stored.MinSegments < 0 || stored.ManifestDigest != features.ManifestDigest() || !known(stored.Split, corpus.Splits()) {
		return "metadata"
	}
	for feature, values := range stored.Values {
		if !validFeature(feature) {
			return "feature"
		}
		for _, value := range values {
			if !finiteAll(value) {
				return "value"
			}
		}
	}
	return ""
}

func validReference(stored Reference) bool {
	return invalidReferenceField(stored) == ""
}

// PutReference creates an immutable reference distribution.
func (s *Store) PutReference(ctx context.Context, r Reference) error {
	if !validReference(r) {
		return invalidArtifact("reference", invalidReferenceField(r))
	}
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		stored, err := s.loadReference(c, ctx, r.ID)
		if err == nil {
			if !sameReference(stored, r) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		return s.putReferenceConn(ctx, c, r)
	})
}

// LoadReference returns a reference distribution and its ordered values.
func (s *Store) LoadReference(ctx context.Context, id string) (Reference, error) {
	return s.loadReference(s.db, ctx, id)
}
func (s *Store) loadReference(query queryer, ctx context.Context, id string) (Reference, error) {
	var r Reference
	var split string
	err := query.QueryRowContext(ctx, "SELECT id,profile_id,split,min_segments,manifest_digest FROM reference WHERE id=?", id).Scan(&r.ID, &r.ProfileID, &split, &r.MinSegments, &r.ManifestDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Split = corpus.Split(split)
	r.Values = map[features.ID][]float64{}
	rows, err := query.QueryContext(ctx, "SELECT feature,ordinal,value FROM reference_value WHERE reference_id=? ORDER BY feature,ordinal", id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	last := map[features.ID]int{}
	for rows.Next() {
		var feature features.ID
		var ordinal int
		var value float64
		if err = rows.Scan(&feature, &ordinal, &value); err != nil {
			return r, err
		}
		if last[feature] != ordinal {
			return r, ErrCorrupt
		}
		last[feature]++
		r.Values[feature] = append(r.Values[feature], value)
	}
	if err = rows.Err(); err != nil {
		return r, err
	}
	if !validReference(r) {
		return r, ErrCorrupt
	}
	return r, nil
}
func sameReference(a, b Reference) bool {
	if a.ID != b.ID || a.ProfileID != b.ProfileID || a.Split != b.Split || a.MinSegments != b.MinSegments || a.ManifestDigest != b.ManifestDigest || len(a.Values) != len(b.Values) {
		return false
	}
	for f, x := range a.Values {
		if !sameSet(x, b.Values[f]) {
			return false
		}
	}
	return true
}

func invalidThresholdField(stored Threshold) string {
	if !validHash(stored.ID) || !validHash(stored.ProfileID) || !validHash(stored.ReferenceID) || !validHash(stored.PopulationID) {
		return "identity"
	}
	if !finiteAll(stored.Low, stored.High, stored.AchievedAuthor, stored.AchievedDistractor, stored.IntervalLow.Lower, stored.IntervalLow.Upper, stored.IntervalHigh.Lower, stored.IntervalHigh.Upper) {
		return "threshold values"
	}
	if stored.IntervalLow.Lower > stored.IntervalLow.Upper || stored.IntervalHigh.Lower > stored.IntervalHigh.Upper || !known(stored.Verdict, eval.ThresholdVerdicts()) {
		return "interval or verdict"
	}
	if (stored.Verdict == eval.VerdictSeparated) != (stored.Low < stored.High) {
		return "verdict"
	}
	return ""
}
func validThreshold(stored Threshold) bool {
	return invalidThresholdField(stored) == ""
}

// PutThreshold creates an immutable calibration threshold.
func (s *Store) PutThreshold(ctx context.Context, x Threshold) error {
	if !validThreshold(x) {
		return invalidArtifact("threshold", invalidThresholdField(x))
	}
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		stored, err := s.loadThreshold(c, ctx, x.ID)
		if err == nil {
			if stored != x {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		exists, err := one(c, ctx, "SELECT count(*) FROM reference WHERE id=? AND profile_id=?", x.ReferenceID, x.ProfileID)
		if err != nil {
			return err
		}
		if !exists {
			return invalidArtifact("threshold", "reference id")
		}
		_, err = c.ExecContext(ctx, "INSERT INTO threshold (id,profile_id,reference_id,population_id,t_low,t_high,achieved_author,achieved_distractor,interval_low_lower,interval_low_upper,interval_high_lower,interval_high_upper,verdict) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)", x.ID, x.ProfileID, x.ReferenceID, x.PopulationID, x.Low, x.High, x.AchievedAuthor, x.AchievedDistractor, x.IntervalLow.Lower, x.IntervalLow.Upper, x.IntervalHigh.Lower, x.IntervalHigh.Upper, x.Verdict)
		return err
	})
}

// LoadThreshold returns a calibration threshold.
func (s *Store) LoadThreshold(ctx context.Context, id string) (Threshold, error) {
	return s.loadThreshold(s.db, ctx, id)
}
func (s *Store) loadThreshold(query queryer, ctx context.Context, id string) (Threshold, error) {
	var x Threshold
	err := query.QueryRowContext(ctx, "SELECT id,profile_id,reference_id,population_id,t_low,t_high,achieved_author,achieved_distractor,interval_low_lower,interval_low_upper,interval_high_lower,interval_high_upper,verdict FROM threshold WHERE id=?", id).Scan(&x.ID, &x.ProfileID, &x.ReferenceID, &x.PopulationID, &x.Low, &x.High, &x.AchievedAuthor, &x.AchievedDistractor, &x.IntervalLow.Lower, &x.IntervalLow.Upper, &x.IntervalHigh.Lower, &x.IntervalHigh.Upper, &x.Verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	if !validThreshold(x) {
		return x, ErrCorrupt
	}
	var n int
	if err = query.QueryRowContext(ctx, "SELECT count(*) FROM reference WHERE id=? AND profile_id=?", x.ReferenceID, x.ProfileID).Scan(&n); err != nil {
		return x, err
	}
	if n != 1 {
		return x, ErrCorrupt
	}
	return x, nil
}

func invalidEvalField(stored EvalResult) string {
	if !validHash(stored.ID) || !validHash(stored.ProfileID) || !validHash(stored.ReferenceID) {
		return "identity"
	}
	if invalidDiscrimination(stored.Discrimination) != "" || invalidCalibration(stored.Calibration) != "" {
		return "gate"
	}
	release, err := releaseFromStored(stored)
	if err != nil {
		return "gate composition"
	}
	if stored.ID != release.ID {
		return "release identity"
	}
	if stored.Shippable != release.Shippable {
		return "release state"
	}
	if stored.Reason != eval.ReleaseReason(release.Reason) {
		return "release reason"
	}
	return ""
}

func validBinding(binding Binding) bool {
	if binding.ManifestDigest != features.ManifestDigest() || binding.WeightScheme != deviation.WeightSchemeUniform || binding.DistanceAlgorithm != deviation.DistanceAlgorithm || len(binding.ScoredTiers) == 0 {
		return false
	}
	for index, tier := range binding.ScoredTiers {
		if tier != features.TierA || (index > 0 && binding.ScoredTiers[index-1] >= tier) {
			return false
		}
	}
	return true
}
func invalidDiscrimination(discrimination Discrimination) string {
	if !validHash(discrimination.ID) || !validHash(discrimination.PopulationID) || !validBinding(discrimination.Binding) || discrimination.Split != corpus.Test || discrimination.Algorithm != eval.DiscriminationAlgorithm || discrimination.Clustering != eval.ClusterByDocument {
		return "identity"
	}
	if discrimination.Resamples <= 0 || discrimination.Confidence <= 0 || discrimination.Confidence >= 1 || discrimination.Floor <= 0 || discrimination.Floor > 1 || discrimination.AuthorSegments < 0 || discrimination.DistractorSegments < 0 || discrimination.AuthorClusters < 0 || discrimination.DistractorClusters < 0 || discrimination.MinClusters < 0 || !finiteAll(discrimination.Floor, discrimination.Confidence, discrimination.AUC, discrimination.LowerBound, discrimination.Cap) {
		return "metrics"
	}
	if discrimination.Discriminates != (discrimination.LowerBound >= discrimination.Floor) || (discrimination.Discriminates && discrimination.Reason != "") || (!discrimination.Discriminates && discrimination.Reason != "lower-bound-below-floor") {
		return "decision"
	}
	return ""
}
func invalidCalibration(calibration Calibration) string {
	if !validHash(calibration.ID) || !validHash(calibration.ThresholdsID) || !validHash(calibration.PopulationID) || !validBinding(calibration.Binding) || calibration.Split != corpus.Test || calibration.Algorithm != eval.BandCalibrationAlgorithm || calibration.Resamples <= 0 || calibration.Confidence <= 0 || calibration.Confidence >= 1 || !finiteAll(calibration.Low, calibration.High, calibration.Confidence) {
		return "metadata"
	}
	if len(calibration.Bands) != 3 {
		return "bands"
	}
	seen := map[eval.Band]bool{}
	for _, report := range calibration.Bands {
		if seen[report.Band] {
			return "band duplicate"
		}
		if field := invalidBand(report); field != "" {
			return "band " + field
		}
		seen[report.Band] = true
	}
	if !seen[eval.BandInRange] || !seen[eval.BandDrifting] || !seen[eval.BandNotYou] {
		return "bands"
	}
	calibrated := false
	for _, report := range calibration.Bands {
		if (report.Band == eval.BandInRange || report.Band == eval.BandNotYou) && report.Emitted {
			calibrated = true
			break
		}
	}
	if calibration.Calibrated != calibrated || (calibration.Calibrated && calibration.Reason != "") || (!calibration.Calibrated && calibration.Reason != "no-claiming-band-emitted") {
		return "decision"
	}
	return ""
}
func invalidBand(report BandReport) string {
	if !known(report.Band, eval.Bands()) {
		return "name"
	}
	if report.AuthorSegments < 0 {
		return "author segments"
	}
	if report.DistractorSegments < 0 {
		return "distractor segments"
	}
	if report.Band == eval.BandDrifting {
		if !report.Emitted {
			return "emitted"
		}
		if report.Claims != "" {
			return "claims"
		}
		if report.Target != 0 {
			return "target"
		}
		if report.ErrorRate != 0 {
			return "error rate"
		}
		if report.ErrorBound != 0 {
			return "error bound"
		}
		if report.ClassSegments != 0 {
			return "class segments"
		}
		if report.ClassClusters != 0 {
			return "class clusters"
		}
		if report.MinClassClusters != 0 {
			return "minimum class clusters"
		}
		if report.Reason != "" {
			return "reason"
		}
		return ""
	}
	want := eval.ClassDistractor
	if report.Band == eval.BandNotYou {
		want = eval.ClassAuthor
	}
	if report.Claims != want {
		return "claims"
	}
	if report.Target <= 0 || report.Target >= 1 {
		return "target"
	}
	if report.ClassSegments < 0 {
		return "class segments"
	}
	if report.ClassClusters < 0 {
		return "class clusters"
	}
	if report.MinClassClusters != int(math.Ceil(3/report.Target)) {
		return "minimum class clusters"
	}
	if report.ErrorRate < 0 || report.ErrorRate > 1 {
		return "error rate"
	}
	if report.ErrorBound < 0 || report.ErrorBound > 1 || !finiteAll(report.Target, report.ErrorRate, report.ErrorBound) {
		return "error bound"
	}
	if report.ClassClusters == 0 {
		if report.ClassSegments != 0 {
			return "class segments"
		}
		if report.ErrorRate != 0 {
			return "error rate"
		}
		if report.ErrorBound != 1 {
			return "error bound"
		}
		if report.Emitted {
			return "emitted"
		}
		if report.Reason != "empty-error-class" {
			return "reason"
		}
		return ""
	}
	if report.ErrorBound < 3/float64(report.ClassClusters) {
		return "error bound"
	}
	if report.Emitted != (report.ErrorBound <= report.Target) {
		return "emitted"
	}
	if report.Emitted && report.Reason != "" || !report.Emitted && report.Reason != "error-bound-exceeds-target" {
		return "reason"
	}
	return ""
}
func validEval(stored EvalResult) bool {
	return invalidEvalField(stored) == ""
}

// PutEvalResult creates an immutable release evaluation.
func (s *Store) PutEvalResult(ctx context.Context, result EvalResult, headPolicy HeadPolicy) error {
	if !validEval(result) {
		return invalidArtifact("evaluation result", invalidEvalField(result))
	}
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		stored, err := s.loadEval(c, ctx, result.ID)
		if err == nil {
			if !sameEval(stored, result) {
				return ErrConflict
			}
			return s.advanceEvalHead(c, ctx, result, headPolicy)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		exists, err := one(c, ctx, "SELECT count(*) FROM reference WHERE id=? AND profile_id=?", result.ReferenceID, result.ProfileID)
		if err != nil {
			return err
		}
		if !exists {
			return invalidArtifact("evaluation result", "reference id")
		}
		threshold, err := s.loadThreshold(c, ctx, result.Calibration.ThresholdsID)
		if err != nil || threshold.ProfileID != result.ProfileID || threshold.ReferenceID != result.ReferenceID || threshold.Low != result.Calibration.Low || threshold.High != result.Calibration.High {
			return invalidArtifact("evaluation result", "calibration threshold")
		}
		discrimination, calibration := result.Discrimination, result.Calibration
		_, err = c.ExecContext(ctx, `INSERT INTO eval_result VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, result.ID, result.ProfileID, result.ReferenceID, discrimination.ID, discrimination.PopulationID, discrimination.Binding.ManifestDigest, discrimination.Binding.WeightScheme, discrimination.Binding.DistanceAlgorithm, strings.Join(tiers(discrimination.Binding.ScoredTiers), ","), discrimination.Split, discrimination.Algorithm, discrimination.Clustering, discrimination.Floor, discrimination.Confidence, discrimination.Resamples, discrimination.Seed, discrimination.AUC, discrimination.LowerBound, discrimination.Cap, discrimination.AuthorSegments, discrimination.DistractorSegments, discrimination.AuthorClusters, discrimination.DistractorClusters, discrimination.MinClusters, boolInt(discrimination.Discriminates), discrimination.Reason, calibration.ID, calibration.ThresholdsID, calibration.PopulationID, calibration.Binding.ManifestDigest, calibration.Binding.WeightScheme, calibration.Binding.DistanceAlgorithm, strings.Join(tiers(calibration.Binding.ScoredTiers), ","), calibration.Split, calibration.Algorithm, calibration.Low, calibration.High, calibration.Confidence, calibration.Resamples, calibration.Seed, boolInt(calibration.Calibrated), calibration.Reason, boolInt(result.Shippable), result.Reason, nullable(result.DistractorPoolID))
		if err != nil {
			return err
		}
		for _, report := range calibration.Bands {
			if _, err = c.ExecContext(ctx, "INSERT INTO calibration_band VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)", result.ID, report.Band, report.Claims, report.Target, report.ErrorRate, report.ErrorBound, report.ClassSegments, report.ClassClusters, report.MinClassClusters, report.AuthorSegments, report.DistractorSegments, boolInt(report.Emitted), report.Reason); err != nil {
				return err
			}
		}
		return s.advanceEvalHead(c, ctx, result, headPolicy)
	})
}

func (s *Store) advanceEvalHead(c *sql.Conn, ctx context.Context, result EvalResult, headPolicy HeadPolicy) error {
	if !headPolicy {
		return nil
	}
	if _, err := c.ExecContext(ctx, "INSERT INTO release_head(profile_id,eval_result_id,updated_at) VALUES (?,?,?) ON CONFLICT(profile_id) DO UPDATE SET eval_result_id=excluded.eval_result_id,updated_at=excluded.updated_at", result.ProfileID, result.ID, s.deps.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	_, err := c.ExecContext(ctx, "INSERT INTO profile_head(register,profile_id,updated_at) SELECT register,id,? FROM profile WHERE id=? ON CONFLICT(register) DO UPDATE SET profile_id=excluded.profile_id,updated_at=excluded.updated_at", s.deps.Now().UTC().Format(time.RFC3339), result.ProfileID)
	return err
}

func sameEval(first, second EvalResult) bool {
	return first.ID == second.ID && first.ProfileID == second.ProfileID && first.ReferenceID == second.ReferenceID && first.DistractorPoolID == second.DistractorPoolID && first.Shippable == second.Shippable && first.Reason == second.Reason && reflect.DeepEqual(first.Discrimination, second.Discrimination) && reflect.DeepEqual(first.Calibration, second.Calibration)
}
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) ReleaseHead(ctx context.Context, profileID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT eval_result_id FROM release_head WHERE profile_id=?", profileID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	x, err := s.LoadEvalResult(ctx, id)
	if err != nil {
		return "", err
	}
	if x.ProfileID != profileID {
		return "", ErrCorrupt
	}
	return id, nil
}
func (s *Store) ProfileHeads(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT register,profile_id FROM profile_head")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var r, id string
		if err = rows.Scan(&r, &id); err != nil {
			return nil, err
		}
		out[r] = id
	}
	return out, rows.Err()
}
func (s *Store) LoadProfileBundle(ctx context.Context, register string) (ProfileBundle, error) {
	var out ProfileBundle
	id, err := s.ProfileHead(ctx, register)
	if err != nil {
		return out, err
	}
	out.Profile, err = s.LoadProfile(ctx, id)
	if err != nil {
		return out, err
	}
	var ref string
	err = s.db.QueryRowContext(ctx, "SELECT id FROM reference WHERE profile_id=? ORDER BY id LIMIT 1", id).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Reference, err = s.LoadReference(ctx, ref)
	if err != nil {
		return out, err
	}
	rid, err := s.ReleaseHead(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Release, err = s.LoadEvalResult(ctx, rid)
	out.Evaluated = err == nil
	return out, err
}

// LoadScoringBundle resolves the current profile head and the artifacts that
// designate its scoring reference.  Raw bundles deliberately have no release.
func (s *Store) LoadScoringBundle(ctx context.Context, register string) (ScoringBundle, error) {
	var out ScoringBundle
	profileID, err := s.ProfileHead(ctx, register)
	if err != nil {
		return out, err
	}
	p, err := s.LoadProfile(ctx, profileID)
	if err != nil {
		return out, err
	}
	out.Fitted, err = p.Fitted()
	if err != nil {
		return out, ErrCorrupt
	}
	releaseID, err := s.ReleaseHead(ctx, profileID)
	if err == nil {
		stored, err := s.LoadEvalResult(ctx, releaseID)
		if err != nil {
			return out, err
		}
		reference, err := s.LoadReference(ctx, stored.ReferenceID)
		if err != nil {
			return out, err
		}
		out.Reference = deviation.Reference{ID: reference.ID, ProfileID: reference.ProfileID, FeatureManifestDigest: reference.ManifestDigest, Split: reference.Split, MinSegments: reference.MinSegments, Values: reference.Values}
		out.Release, err = releaseFromStored(stored)
		if err != nil {
			return out, ErrCorrupt
		}
		out.Calibrated = true
		return out, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM reference WHERE profile_id=?", profileID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return out, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(ids) == 0 {
		return out, ErrNoReference
	}
	if len(ids) != 1 {
		return out, ErrAmbiguousReference
	}
	r, err := s.LoadReference(ctx, ids[0])
	if err != nil {
		return out, err
	}
	out.Reference = deviation.Reference{ID: r.ID, ProfileID: r.ProfileID, FeatureManifestDigest: r.ManifestDigest, Split: r.Split, MinSegments: r.MinSegments, Values: r.Values}
	return out, nil
}

func releaseFromStored(x EvalResult) (eval.Release, error) {
	d := eval.Discrimination{ID: x.Discrimination.ID, PopulationID: x.Discrimination.PopulationID, ProfileID: x.ProfileID, ReferenceID: x.ReferenceID, FeatureManifestDigest: x.Discrimination.Binding.ManifestDigest, WeightScheme: x.Discrimination.Binding.WeightScheme, DistanceAlgorithm: x.Discrimination.Binding.DistanceAlgorithm, ScoredTiers: x.Discrimination.Binding.ScoredTiers, Split: x.Discrimination.Split, Spec: eval.DiscriminationSpec{Floor: x.Discrimination.Floor, Confidence: x.Discrimination.Confidence, Resamples: x.Discrimination.Resamples, Seed: x.Discrimination.Seed}, Algorithm: x.Discrimination.Algorithm, Clustering: x.Discrimination.Clustering, AUC: x.Discrimination.AUC, LowerBound: x.Discrimination.LowerBound, Cap: x.Discrimination.Cap, AuthorSegments: x.Discrimination.AuthorSegments, DistractorSegments: x.Discrimination.DistractorSegments, AuthorClusters: x.Discrimination.AuthorClusters, DistractorClusters: x.Discrimination.DistractorClusters, MinClusters: x.Discrimination.MinClusters, Discriminates: x.Discrimination.Discriminates, Reason: x.Discrimination.Reason}
	c := eval.Calibration{ID: x.Calibration.ID, ThresholdsID: x.Calibration.ThresholdsID, PopulationID: x.Calibration.PopulationID, ProfileID: x.ProfileID, ReferenceID: x.ReferenceID, FeatureManifestDigest: x.Calibration.Binding.ManifestDigest, WeightScheme: x.Calibration.Binding.WeightScheme, DistanceAlgorithm: x.Calibration.Binding.DistanceAlgorithm, ScoredTiers: x.Calibration.Binding.ScoredTiers, Split: x.Calibration.Split, Floor: eval.BandFloor{Confidence: x.Calibration.Confidence, Resamples: x.Calibration.Resamples, Seed: x.Calibration.Seed}, Algorithm: x.Calibration.Algorithm, Low: x.Calibration.Low, High: x.Calibration.High, Calibrated: x.Calibration.Calibrated, Reason: x.Calibration.Reason}
	for _, b := range x.Calibration.Bands {
		c.Bands = append(c.Bands, eval.BandReport{Band: b.Band, Claims: b.Claims, Target: b.Target, ErrorRate: b.ErrorRate, ErrorBound: b.ErrorBound, ClassSegments: b.ClassSegments, ClassClusters: b.ClassClusters, MinClassClusters: b.MinClassClusters, AuthorSegments: b.AuthorSegments, DistractorSegments: b.DistractorSegments, Emitted: b.Emitted, Reason: b.Reason})
	}
	return eval.NewRelease(d, c)
}

// LoadRelease returns a persisted release in the domain form used for scoring.
func (s *Store) LoadRelease(ctx context.Context, id string) (eval.Release, error) {
	x, err := s.LoadEvalResult(ctx, id)
	if err != nil {
		return eval.Release{}, err
	}
	return releaseFromStored(x)
}

// PutRelease persists the domain release through the same codec used by score.
func (s *Store) PutRelease(ctx context.Context, release eval.Release, poolID string, policy HeadPolicy) error {
	x := EvalResult{ID: release.ID, ProfileID: release.Discrimination.ProfileID, ReferenceID: release.Discrimination.ReferenceID, DistractorPoolID: poolID, Shippable: release.Shippable, Reason: eval.ReleaseReason(release.Reason)}
	x.Discrimination = storedDiscrimination(release.Discrimination)
	x.Calibration = storedCalibration(release.Calibration)

	stored, err := s.LoadEvalResult(ctx, x.ID)
	if err == nil {
		if !sameEval(stored, x) {
			return ErrConflict
		}
		return s.PutEvalResult(ctx, x, policy)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}

	threshold := Threshold{ID: release.Calibration.ThresholdsID, ProfileID: release.Calibration.ProfileID, ReferenceID: release.Calibration.ReferenceID, PopulationID: release.Calibration.PopulationID, Low: release.Calibration.Low, High: release.Calibration.High, Verdict: eval.VerdictPairIncompatible}
	if release.Calibration.Calibrated {
		threshold.Verdict = eval.VerdictSeparated
	}
	if err := s.PutThreshold(ctx, threshold); err != nil && !errors.Is(err, ErrConflict) {
		return err
	}
	return s.PutEvalResult(ctx, x, policy)
}
func storedDiscrimination(x eval.Discrimination) Discrimination {
	return Discrimination{ID: x.ID, PopulationID: x.PopulationID, Binding: Binding{ManifestDigest: x.FeatureManifestDigest, WeightScheme: x.WeightScheme, DistanceAlgorithm: x.DistanceAlgorithm, ScoredTiers: x.ScoredTiers}, Split: x.Split, Algorithm: x.Algorithm, Clustering: x.Clustering, Floor: x.Spec.Floor, Confidence: x.Spec.Confidence, Resamples: x.Spec.Resamples, Seed: x.Spec.Seed, AUC: x.AUC, LowerBound: x.LowerBound, Cap: x.Cap, AuthorSegments: x.AuthorSegments, DistractorSegments: x.DistractorSegments, AuthorClusters: x.AuthorClusters, DistractorClusters: x.DistractorClusters, MinClusters: x.MinClusters, Discriminates: x.Discriminates, Reason: x.Reason}
}
func storedCalibration(x eval.Calibration) Calibration {
	y := Calibration{ID: x.ID, ThresholdsID: x.ThresholdsID, PopulationID: x.PopulationID, Binding: Binding{ManifestDigest: x.FeatureManifestDigest, WeightScheme: x.WeightScheme, DistanceAlgorithm: x.DistanceAlgorithm, ScoredTiers: x.ScoredTiers}, Split: x.Split, Algorithm: x.Algorithm, Low: x.Low, High: x.High, Confidence: x.Floor.Confidence, Resamples: x.Floor.Resamples, Seed: x.Floor.Seed, Calibrated: x.Calibrated, Reason: x.Reason}
	for _, b := range x.Bands {
		y.Bands = append(y.Bands, BandReport{Band: b.Band, Claims: b.Claims, Target: b.Target, ErrorRate: b.ErrorRate, ErrorBound: b.ErrorBound, ClassSegments: b.ClassSegments, ClassClusters: b.ClassClusters, MinClassClusters: b.MinClassClusters, AuthorSegments: b.AuthorSegments, DistractorSegments: b.DistractorSegments, Emitted: b.Emitted, Reason: b.Reason})
	}
	return y
}

// LoadEvalResult returns a persisted release evaluation.
func (s *Store) LoadEvalResult(ctx context.Context, id string) (EvalResult, error) {
	return s.loadEval(s.db, ctx, id)
}
func (s *Store) loadEval(query queryer, ctx context.Context, id string) (EvalResult, error) {
	var x EvalResult
	var dd, ac, sh int
	var dtiers, atiers string
	var pool sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id,profile_id,reference_id,discrimination_id,discrimination_population_id,discrimination_manifest_digest,discrimination_weight_scheme,discrimination_distance_algorithm,discrimination_scored_tiers,discrimination_split,discrimination_algorithm,discrimination_clustering,discrimination_floor,discrimination_confidence,discrimination_resamples,discrimination_seed,auc,lower_bound,cap,author_segments,distractor_segments,author_clusters,distractor_clusters,min_clusters,discriminates,discrimination_reason,calibration_id,calibration_thresholds_id,calibration_population_id,calibration_manifest_digest,calibration_weight_scheme,calibration_distance_algorithm,calibration_scored_tiers,calibration_split,calibration_algorithm,calibration_low,calibration_high,calibration_confidence,calibration_resamples,calibration_seed,calibrated,calibration_reason,shippable,reason,distractor_pool_id FROM eval_result WHERE id=?`, id).Scan(&x.ID, &x.ProfileID, &x.ReferenceID, &x.Discrimination.ID, &x.Discrimination.PopulationID, &x.Discrimination.Binding.ManifestDigest, &x.Discrimination.Binding.WeightScheme, &x.Discrimination.Binding.DistanceAlgorithm, &dtiers, &x.Discrimination.Split, &x.Discrimination.Algorithm, &x.Discrimination.Clustering, &x.Discrimination.Floor, &x.Discrimination.Confidence, &x.Discrimination.Resamples, &x.Discrimination.Seed, &x.Discrimination.AUC, &x.Discrimination.LowerBound, &x.Discrimination.Cap, &x.Discrimination.AuthorSegments, &x.Discrimination.DistractorSegments, &x.Discrimination.AuthorClusters, &x.Discrimination.DistractorClusters, &x.Discrimination.MinClusters, &dd, &x.Discrimination.Reason, &x.Calibration.ID, &x.Calibration.ThresholdsID, &x.Calibration.PopulationID, &x.Calibration.Binding.ManifestDigest, &x.Calibration.Binding.WeightScheme, &x.Calibration.Binding.DistanceAlgorithm, &atiers, &x.Calibration.Split, &x.Calibration.Algorithm, &x.Calibration.Low, &x.Calibration.High, &x.Calibration.Confidence, &x.Calibration.Resamples, &x.Calibration.Seed, &ac, &x.Calibration.Reason, &sh, &x.Reason, &pool)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	x.Discrimination.Binding.ScoredTiers, x.Calibration.Binding.ScoredTiers = parseTiers(dtiers), parseTiers(atiers)
	x.Discrimination.Discriminates = dd != 0
	x.Calibration.Calibrated = ac != 0
	x.Shippable = sh != 0
	x.DistractorPoolID = pool.String
	rows, err := query.QueryContext(ctx, "SELECT band,claims,target,error_rate,error_bound,class_segments,class_clusters,min_class_clusters,author_segments,distractor_segments,emitted,reason FROM calibration_band WHERE eval_result_id=? ORDER BY CASE band WHEN 'in-range' THEN 0 WHEN 'drifting' THEN 1 ELSE 2 END", x.ID)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	for rows.Next() {
		var r BandReport
		var emitted int
		if err = rows.Scan(&r.Band, &r.Claims, &r.Target, &r.ErrorRate, &r.ErrorBound, &r.ClassSegments, &r.ClassClusters, &r.MinClassClusters, &r.AuthorSegments, &r.DistractorSegments, &emitted, &r.Reason); err != nil {
			return x, err
		}
		r.Emitted = emitted != 0
		x.Calibration.Bands = append(x.Calibration.Bands, r)
	}
	if err = rows.Err(); err != nil {
		return x, err
	}
	if !validEval(x) {
		return x, ErrCorrupt
	}
	var n int
	if err = query.QueryRowContext(ctx, "SELECT count(*) FROM reference WHERE id=? AND profile_id=?", x.ReferenceID, x.ProfileID).Scan(&n); err != nil {
		return x, err
	}
	if n != 1 {
		return x, ErrCorrupt
	}
	threshold, err := s.loadThreshold(query, ctx, x.Calibration.ThresholdsID)
	if err != nil || threshold.ProfileID != x.ProfileID || threshold.ReferenceID != x.ReferenceID || threshold.Low != x.Calibration.Low || threshold.High != x.Calibration.High {
		return x, ErrCorrupt
	}
	return x, nil
}

func tiers(tierValues []features.Tier) []string {
	out := make([]string, len(tierValues))
	for index, tier := range tierValues {
		out[index] = string(tier)
	}
	return out
}
func parseTiers(serialized string) []features.Tier {
	if serialized == "" {
		return nil
	}
	values := strings.Split(serialized, ",")
	out := make([]features.Tier, len(values))
	for index, value := range values {
		out[index] = features.Tier(value)
	}
	return out
}

func invalidSelectionField(stored ExemplarSelection) string {
	if !validHash(stored.ID) || !validHash(stored.ProfileID) || !validHash(stored.CertificateID) {
		return "identity"
	}
	if stored.N != len(stored.Members) || stored.N < 0 {
		return "member count"
	}
	members := map[string]bool{}
	for _, member := range stored.Members {
		if !validHash(member) {
			return "member id"
		}
		if members[member] {
			return "member id duplicate"
		}
		members[member] = true
	}
	return ""
}
func validSelection(stored ExemplarSelection) bool {
	return invalidSelectionField(stored) == ""
}

// PutExemplarSelection creates an immutable ordered exemplar selection.
func (s *Store) PutExemplarSelection(ctx context.Context, x ExemplarSelection) error {
	if !validSelection(x) {
		return invalidArtifact("exemplar selection", invalidSelectionField(x))
	}
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		stored, err := s.loadSelection(c, ctx, x.ID)
		if err == nil {
			if !sameSelection(stored, x) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		for _, nodeID := range x.Members {
			exists, err := one(c, ctx, "SELECT count(*) FROM node n JOIN document d ON d.document_id=n.document_id JOIN profile p ON p.snapshot_id=d.snapshot_id WHERE n.node_id=? AND p.id=?", nodeID, x.ProfileID)
			if err != nil {
				return err
			}
			if !exists {
				return invalidArtifact("exemplar selection", "member node id")
			}
		}
		if _, err = c.ExecContext(ctx, "INSERT INTO exemplar_selection (id,profile_id,n,certificate_id) VALUES (?,?,?,?)", x.ID, x.ProfileID, x.N, x.CertificateID); err != nil {
			return err
		}
		for ordinal, nodeID := range x.Members {
			if _, err = c.ExecContext(ctx, "INSERT INTO exemplar_member (selection_id,ordinal,node_id) VALUES (?,?,?)", x.ID, ordinal, nodeID); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadExemplarSelection returns an ordered exemplar selection.
func (s *Store) LoadExemplarSelection(ctx context.Context, id string) (ExemplarSelection, error) {
	return s.loadSelection(s.db, ctx, id)
}
func (s *Store) loadSelection(query queryer, ctx context.Context, id string) (ExemplarSelection, error) {
	var x ExemplarSelection
	err := query.QueryRowContext(ctx, "SELECT id,profile_id,n,certificate_id FROM exemplar_selection WHERE id=?", id).Scan(&x.ID, &x.ProfileID, &x.N, &x.CertificateID)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	rows, err := query.QueryContext(ctx, "SELECT node_id,ordinal FROM exemplar_member WHERE selection_id=? ORDER BY ordinal", id)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	for ordinal := 0; rows.Next(); ordinal++ {
		var nodeID string
		var storedOrdinal int
		if err = rows.Scan(&nodeID, &storedOrdinal); err != nil {
			return x, err
		}
		if ordinal != storedOrdinal {
			return x, ErrCorrupt
		}
		x.Members = append(x.Members, nodeID)
	}
	if err = rows.Err(); err != nil {
		return x, err
	}
	if !validSelection(x) {
		return x, ErrCorrupt
	}
	for _, nodeID := range x.Members {
		exists, err := one(query, ctx, "SELECT count(*) FROM node n JOIN document d ON d.document_id=n.document_id JOIN profile p ON p.snapshot_id=d.snapshot_id WHERE n.node_id=? AND p.id=?", nodeID, x.ProfileID)
		if err != nil {
			return x, err
		}
		if !exists {
			return x, ErrCorrupt
		}
	}
	return x, nil
}
func sameSelection(a, b ExemplarSelection) bool {
	return a.ID == b.ID && a.ProfileID == b.ProfileID && a.N == b.N && a.CertificateID == b.CertificateID && sameSet(a.Members, b.Members)
}

func invalidAttemptField(stored RewriteAttempt) string {
	if !validHash(stored.InvocationID) || stored.Index < 0 || !validHash(stored.ProfileID) || !validHash(stored.NodeID) || !validHash(stored.CurrentHash) || !validHash(stored.CandidateHash) {
		return "identity"
	}
	if !known(stored.ProviderID, llm.Providers()) || !known(stored.CurrentBand, eval.Bands()) || !known(stored.CandidateBand, eval.Bands()) || !known(stored.Rejection, rewrite.RejectionCodes()) || !finiteAll(stored.CurrentDistance, stored.CandidateDistance) {
		return "decision metadata"
	}
	if stored.Accepted != (stored.Rejection == rewrite.RejectionNone) || (stored.Accepted && (stored.CurrentBand == "" || stored.CandidateBand == "")) || (stored.Preserved && len(stored.PreserveIdentifiers) > 0) {
		return "decision state"
	}
	for _, identifier := range stored.PreserveIdentifiers {
		if !preserve.ValidIdentifier(identifier) {
			return "preserve identifiers"
		}
	}
	return ""
}
func validAttempt(stored RewriteAttempt) bool {
	return invalidAttemptField(stored) == ""
}

// PutRewriteAttempt creates an immutable rewrite decision record.
func (s *Store) PutRewriteAttempt(ctx context.Context, x RewriteAttempt) error {
	if !validAttempt(x) {
		return invalidArtifact("rewrite attempt", invalidAttemptField(x))
	}
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		stored, err := s.loadAttempt(c, ctx, x.InvocationID, x.NodeID, x.Index)
		if err == nil {
			if !sameAttempt(stored, x) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		exists, err := one(c, ctx, "SELECT count(*) FROM node WHERE node_id=?", x.NodeID)
		if err != nil {
			return err
		}
		if !exists {
			return invalidArtifact("rewrite attempt", "node id")
		}
		exists, err = one(c, ctx, "SELECT count(*) FROM profile WHERE id=?", x.ProfileID)
		if err != nil {
			return err
		}
		if !exists {
			return invalidArtifact("rewrite attempt", "profile id")
		}
		_, err = c.ExecContext(ctx, "INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,node_id,current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,preserved,tells_comparison,tells_comparable,accepted,rejection) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", x.InvocationID, x.Index, x.ProfileID, x.ProviderID, x.NodeID, x.CurrentHash, x.CandidateHash, x.CurrentDistance, x.CandidateDistance, x.CurrentBand, x.CandidateBand, boolInt(x.Preserved), x.TellsComparison, boolInt(x.TellsComparable), boolInt(x.Accepted), x.Rejection)
		if err != nil {
			return err
		}
		for ordinal, identifier := range x.PreserveIdentifiers {
			if _, err = c.ExecContext(ctx, "INSERT INTO rewrite_attempt_identifier (invocation_id,node_id,attempt_index,ordinal,identifier) VALUES (?,?,?,?,?)", x.InvocationID, x.NodeID, x.Index, ordinal, identifier); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadRewriteAttempt returns one stored rewrite decision record.
func (s *Store) LoadRewriteAttempt(ctx context.Context, id, nodeID string, i int) (RewriteAttempt, error) {
	return s.loadAttempt(s.db, ctx, id, nodeID, i)
}
func (s *Store) loadAttempt(query queryer, ctx context.Context, id, nodeID string, index int) (RewriteAttempt, error) {
	var x RewriteAttempt
	var preserved, tellsComparable, accepted int
	err := query.QueryRowContext(ctx, "SELECT invocation_id,attempt_index,profile_id,provider_id,node_id,current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,preserved,tells_comparison,tells_comparable,accepted,rejection FROM rewrite_attempt WHERE invocation_id=? AND node_id=? AND attempt_index=?", id, nodeID, index).Scan(&x.InvocationID, &x.Index, &x.ProfileID, &x.ProviderID, &x.NodeID, &x.CurrentHash, &x.CandidateHash, &x.CurrentDistance, &x.CandidateDistance, &x.CurrentBand, &x.CandidateBand, &preserved, &x.TellsComparison, &tellsComparable, &accepted, &x.Rejection)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	x.Preserved = preserved != 0
	x.TellsComparable = tellsComparable != 0
	x.Accepted = accepted != 0
	rows, err := query.QueryContext(ctx, "SELECT ordinal,identifier FROM rewrite_attempt_identifier WHERE invocation_id=? AND node_id=? AND attempt_index=? ORDER BY ordinal", id, nodeID, index)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	for ordinal := 0; rows.Next(); ordinal++ {
		var storedOrdinal int
		var identifier string
		if err = rows.Scan(&storedOrdinal, &identifier); err != nil {
			return x, err
		}
		if storedOrdinal != ordinal {
			return x, ErrCorrupt
		}
		x.PreserveIdentifiers = append(x.PreserveIdentifiers, identifier)
	}
	if err = rows.Err(); err != nil {
		return x, err
	}
	if !validAttempt(x) {
		return x, ErrCorrupt
	}
	return x, nil
}
func sameAttempt(a, b RewriteAttempt) bool {
	return a.InvocationID == b.InvocationID && a.Index == b.Index && a.ProfileID == b.ProfileID && a.ProviderID == b.ProviderID && a.NodeID == b.NodeID && a.CurrentHash == b.CurrentHash && a.CandidateHash == b.CandidateHash && a.CurrentDistance == b.CurrentDistance && a.CandidateDistance == b.CandidateDistance && a.CurrentBand == b.CurrentBand && a.CandidateBand == b.CandidateBand && a.Preserved == b.Preserved && a.TellsComparison == b.TellsComparison && a.TellsComparable == b.TellsComparable && a.Accepted == b.Accepted && a.Rejection == b.Rejection && sameSet(a.PreserveIdentifiers, b.PreserveIdentifiers)
}

type recorder struct {
	s   *Store
	ctx context.Context
}

// Recorder returns a rewrite store backed by this artifact store.
func (s *Store) Recorder(ctx context.Context) rewrite.Store { return recorder{s: s, ctx: ctx} }

func (r recorder) RecordAttempt(attempt rewrite.Attempt) error {
	return r.s.PutRewriteAttempt(r.ctx, RewriteAttempt{
		InvocationID:        attempt.InvocationID,
		Index:               attempt.Index,
		ProfileID:           attempt.ProfileID,
		ProviderID:          llm.ProviderID(attempt.ProviderID),
		NodeID:              attempt.SpanRef,
		CurrentHash:         attempt.CurrentHash,
		CandidateHash:       attempt.CandidateHash,
		CurrentDistance:     attempt.CurrentDistance,
		CandidateDistance:   attempt.CandidateDistance,
		CurrentBand:         attempt.CurrentBand,
		CandidateBand:       attempt.CandidateBand,
		Preserved:           attempt.Preserved,
		PreserveIdentifiers: attempt.PreserveIdentifiers,
		TellsComparison:     attempt.TellsComparison,
		TellsComparable:     attempt.TellsComparable,
		Accepted:            attempt.Accepted,
		Rejection:           attempt.Rejection,
	})
}
