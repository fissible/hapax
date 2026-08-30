package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
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
	ID, ProfileID, ReferenceID                                             string
	AUC, LowerBound, Cap                                                   float64
	AuthorSegments, DistractorSegments, AuthorClusters, DistractorClusters int
	Discriminates, Calibrated, Shippable                                   bool
	Reason                                                                 eval.ReleaseReason
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
			exists, err := one(c, ctx, "SELECT count(*) FROM snapshot WHERE id=?", p.SnapshotID)
			if err != nil {
				return err
			}
			if !exists {
				return invalidArtifact("profile", "snapshot id")
			}
			if _, err = c.ExecContext(ctx, "INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,feature_set_version,min_paragraph_lexical_tokens) VALUES (?,?,?,?,?,?,?,?)", p.ID, p.SnapshotID, p.Register, p.Unit, p.VarianceConvention, p.ManifestDigest, p.FeatureSetVersion, p.MinParagraphLexicalTokens); err != nil {
				return err
			}
			for _, statistic := range p.Stats {
				if _, err = c.ExecContext(ctx, "INSERT INTO profile_stat (profile_id,feature,n,mean,variance,defined,variance_defined,min_observations) VALUES (?,?,?,?,?,?,?,?)", p.ID, statistic.Feature, statistic.N, statistic.Mean, statistic.Variance, boolInt(statistic.Defined), boolInt(statistic.VarianceDefined), statistic.MinObservations); err != nil {
					return err
				}
			}
		}
		if h {
			_, err = c.ExecContext(ctx, "INSERT INTO profile_head (register,profile_id,updated_at) VALUES (?,?,?) ON CONFLICT(register) DO UPDATE SET profile_id=excluded.profile_id,updated_at=excluded.updated_at", p.Register, p.ID, time.Now().UTC().Format(time.RFC3339))
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
	err := query.QueryRowContext(ctx, "SELECT id,snapshot_id,register,unit,variance_convention,manifest_digest,feature_set_version,min_paragraph_lexical_tokens FROM profile WHERE id=?", id).Scan(&stored.ID, &stored.SnapshotID, &stored.Register, &unit, &varianceConvention, &stored.ManifestDigest, &stored.FeatureSetVersion, &stored.MinParagraphLexicalTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return stored, ErrNotFound
	}
	if err != nil {
		return stored, err
	}
	stored.Unit = profile.Unit(unit)
	stored.VarianceConvention = profile.VarianceConvention(varianceConvention)
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
	if a.ID != b.ID || a.SnapshotID != b.SnapshotID || a.Register != b.Register || a.Unit != b.Unit || a.VarianceConvention != b.VarianceConvention || a.ManifestDigest != b.ManifestDigest || a.FeatureSetVersion != b.FeatureSetVersion || a.MinParagraphLexicalTokens != b.MinParagraphLexicalTokens || len(a.Stats) != len(b.Stats) {
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
		exists, err := one(c, ctx, "SELECT count(*) FROM profile WHERE id=?", r.ProfileID)
		if err != nil {
			return err
		}
		if !exists {
			return invalidArtifact("reference", "profile id")
		}
		if _, err = c.ExecContext(ctx, "INSERT INTO reference (id,profile_id,split,min_segments,manifest_digest) VALUES (?,?,?,?,?)", r.ID, r.ProfileID, r.Split, r.MinSegments, r.ManifestDigest); err != nil {
			return err
		}
		for feature, values := range r.Values {
			for ordinal, value := range values {
				if _, err = c.ExecContext(ctx, "INSERT INTO reference_value (reference_id,feature,ordinal,value) VALUES (?,?,?,?)", r.ID, feature, ordinal, value); err != nil {
					return err
				}
			}
		}
		return nil
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
	if stored.AuthorSegments < 0 || stored.DistractorSegments < 0 || stored.AuthorClusters < 0 || stored.DistractorClusters < 0 || !finiteAll(stored.AUC, stored.LowerBound, stored.Cap) {
		return "metrics"
	}
	if !known(stored.Reason, eval.ReleaseReasons()) || stored.Shippable != (stored.Discriminates && stored.Calibrated) {
		return "release state"
	}
	if stored.Shippable != (stored.Reason == eval.ReleaseReasonNone) {
		return "release reason"
	}
	return ""
}
func validEval(stored EvalResult) bool {
	return invalidEvalField(stored) == ""
}

// PutEvalResult creates an immutable release evaluation.
func (s *Store) PutEvalResult(ctx context.Context, x EvalResult) error {
	if !validEval(x) {
		return invalidArtifact("evaluation result", invalidEvalField(x))
	}
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		stored, err := s.loadEval(c, ctx, x.ID)
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
			return invalidArtifact("evaluation result", "reference id")
		}
		_, err = c.ExecContext(ctx, "INSERT INTO eval_result (id,profile_id,reference_id,auc,lower_bound,cap,author_segments,distractor_segments,author_clusters,distractor_clusters,discriminates,calibrated,shippable,reason) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", x.ID, x.ProfileID, x.ReferenceID, x.AUC, x.LowerBound, x.Cap, x.AuthorSegments, x.DistractorSegments, x.AuthorClusters, x.DistractorClusters, boolInt(x.Discriminates), boolInt(x.Calibrated), boolInt(x.Shippable), x.Reason)
		return err
	})
}

// LoadEvalResult returns a persisted release evaluation.
func (s *Store) LoadEvalResult(ctx context.Context, id string) (EvalResult, error) {
	return s.loadEval(s.db, ctx, id)
}
func (s *Store) loadEval(query queryer, ctx context.Context, id string) (EvalResult, error) {
	var x EvalResult
	var discriminates, calibrated, shippable int
	err := query.QueryRowContext(ctx, "SELECT id,profile_id,reference_id,auc,lower_bound,cap,author_segments,distractor_segments,author_clusters,distractor_clusters,discriminates,calibrated,shippable,reason FROM eval_result WHERE id=?", id).Scan(&x.ID, &x.ProfileID, &x.ReferenceID, &x.AUC, &x.LowerBound, &x.Cap, &x.AuthorSegments, &x.DistractorSegments, &x.AuthorClusters, &x.DistractorClusters, &discriminates, &calibrated, &shippable, &x.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	x.Discriminates = discriminates != 0
	x.Calibrated = calibrated != 0
	x.Shippable = shippable != 0
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
	return x, nil
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
		stored, err := s.loadAttempt(c, ctx, x.InvocationID, x.Index)
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
			if _, err = c.ExecContext(ctx, "INSERT INTO rewrite_attempt_identifier (invocation_id,attempt_index,ordinal,identifier) VALUES (?,?,?,?)", x.InvocationID, x.Index, ordinal, identifier); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadRewriteAttempt returns one stored rewrite decision record.
func (s *Store) LoadRewriteAttempt(ctx context.Context, id string, i int) (RewriteAttempt, error) {
	return s.loadAttempt(s.db, ctx, id, i)
}
func (s *Store) loadAttempt(query queryer, ctx context.Context, id string, index int) (RewriteAttempt, error) {
	var x RewriteAttempt
	var preserved, tellsComparable, accepted int
	err := query.QueryRowContext(ctx, "SELECT invocation_id,attempt_index,profile_id,provider_id,node_id,current_hash,candidate_hash,current_distance,candidate_distance,current_band,candidate_band,preserved,tells_comparison,tells_comparable,accepted,rejection FROM rewrite_attempt WHERE invocation_id=? AND attempt_index=?", id, index).Scan(&x.InvocationID, &x.Index, &x.ProfileID, &x.ProviderID, &x.NodeID, &x.CurrentHash, &x.CandidateHash, &x.CurrentDistance, &x.CandidateDistance, &x.CurrentBand, &x.CandidateBand, &preserved, &x.TellsComparison, &tellsComparable, &accepted, &x.Rejection)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	x.Preserved = preserved != 0
	x.TellsComparable = tellsComparable != 0
	x.Accepted = accepted != 0
	rows, err := query.QueryContext(ctx, "SELECT ordinal,identifier FROM rewrite_attempt_identifier WHERE invocation_id=? AND attempt_index=? ORDER BY ordinal", id, index)
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
