package store_test

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// fakeID is a well-formed identity for an artifact whose owning component is
// not exercised here. Store carries these; it does not invent or recompute them.
func fakeID(parts ...string) string {
	inputs := map[string]string{}
	for i, part := range parts {
		inputs[string(rune('a'+i))] = part
	}
	return identity.HashInputs(inputs)
}

// storedGraph writes one snapshot and returns it with its derived IDs filled
// in, so a test can reference real document and node keys.
func storedGraph(t *testing.T, s *store.Store) store.SnapshotWrite {
	t.Helper()
	leaf := node(0, 0, 12)
	leaf.Vector = &features.Vector{
		SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: vectorValues(),
	}
	write := snapshotWrite(
		document("essays/a.md", hashA, leaf, node(1, 12, 20)),
		document("essays/b.md", hashB, node(0, 0, 30)),
	)
	mustPutSnapshot(t, s, write)
	return withDerivedIDs(write)
}

func profileStats() []store.ProfileStat {
	out := make([]store.ProfileStat, 0, len(features.Definitions()))
	for i, definition := range features.Definitions() {
		stat := store.ProfileStat{Feature: definition.ID, MinObservations: 30}
		// Leave one undefined, so a round trip covers both states.
		if i != 1 {
			stat.N, stat.Mean, stat.Variance = 40+i, float64(i)+0.25, 0.5
			stat.Defined, stat.VarianceDefined = true, true
		}
		out = append(out, stat)
	}
	return out
}

func profileFixture(snapshotID string) store.Profile {
	return store.Profile{
		ID:                        fakeID("profile", snapshotID),
		SnapshotID:                snapshotID,
		Register:                  "essays",
		Unit:                      profile.UnitParagraph,
		VarianceConvention:        profile.SampleVariance,
		ManifestDigest:            features.ManifestDigest(),
		FeatureSetVersion:         features.SetVersion,
		MinParagraphLexicalTokens: 40,
		// Not ready with its declared reason: Build has never produced a ready
		// profile, so a fixture claiming otherwise would not be one the tool
		// can make. Ready-and-silent is exercised where readiness is the
		// subject, not everywhere as a default.
		ProductionReady: false,
		NotReadyReason:  aDeclaredNotReadyReason,
		Stats:           profileStats(),
	}
}

func referenceValues() map[features.ID][]float64 {
	out := map[features.ID][]float64{}
	for i, definition := range features.Definitions() {
		// One feature carries no values: a reference need not cover the manifest.
		if i == 1 {
			continue
		}
		out[definition.ID] = []float64{-1.5, -0.25, 0, 0.75, float64(i)}
	}
	return out
}

func referenceFixture(profileID string) store.Reference {
	return store.Reference{
		ID:             fakeID("reference", profileID),
		ProfileID:      profileID,
		Split:          corpus.Calibrate,
		MinSegments:    30,
		ManifestDigest: features.ManifestDigest(),
		Values:         referenceValues(),
	}
}

func thresholdFixture(profileID, referenceID string) store.Threshold {
	return store.Threshold{
		ID:                 fakeID("threshold", profileID, referenceID),
		ProfileID:          profileID,
		ReferenceID:        referenceID,
		PopulationID:       fakeID("population", profileID),
		Low:                0.4,
		High:               0.9,
		AchievedAuthor:     0.05,
		AchievedDistractor: 0.1,
		IntervalLow:        eval.Interval{Lower: 0.35, Upper: 0.45},
		IntervalHigh:       eval.Interval{Lower: 0.85, Upper: 0.95},
		Verdict:            eval.VerdictSeparated,
	}
}

// bindingFixture is the runtime binding both gates must carry, and must agree
// on: Calibration.Band rejects a distance that contradicts any of it.
func bindingFixture() store.Binding {
	return store.Binding{
		ManifestDigest:    features.ManifestDigest(),
		WeightScheme:      deviation.WeightSchemeUniform,
		DistanceAlgorithm: deviation.DistanceAlgorithm,
		ScoredTiers:       []features.Tier{features.TierA},
	}
}

// bandReports are the three eval always produces, in the order it produces
// them. drifting is the special one: always emitted, no claiming class, no
// error gate, because it is what a distance falls into when neither claim holds.
func bandReports() []store.BandReport {
	return []store.BandReport{
		// The cluster counts are not decorative: eval floors the bound at
		// 3/ClassClusters, so 14 clusters could never produce a bound of 0.08
		// and the first draft of this fixture described a release eval cannot
		// emit. 40 clusters floor it at 0.075, 80 at 0.0375.
		{
			Band: eval.BandInRange, Claims: eval.ClassDistractor,
			Target: 0.10, ErrorRate: 0.04, ErrorBound: 0.08,
			ClassSegments: 400, ClassClusters: 40, MinClassClusters: 30,
			AuthorSegments: 90, DistractorSegments: 20, Emitted: true,
		},
		{Band: eval.BandDrifting, Emitted: true, AuthorSegments: 20, DistractorSegments: 40},
		{
			Band: eval.BandNotYou, Claims: eval.ClassAuthor,
			Target: 0.05, ErrorRate: 0.02, ErrorBound: 0.04,
			ClassSegments: 800, ClassClusters: 80, MinClassClusters: 60,
			AuthorSegments: 10, DistractorSegments: 80, Emitted: true,
		},
	}
}

func evalResultFixture(profileID, referenceID string) store.EvalResult {
	population := fakeID("population", profileID)
	discrimination := store.Discrimination{
		ID: fakeID("discrimination", profileID), PopulationID: population,
		Binding: bindingFixture(), Split: corpus.Test,
		Algorithm: eval.DiscriminationAlgorithm, Clustering: eval.ClusterByDocument,
		Floor: 0.65, Confidence: 0.95, Resamples: 2000, Seed: 7,
		AUC: 0.82, LowerBound: 0.71, Cap: 2.5,
		AuthorSegments: 120, DistractorSegments: 140,
		AuthorClusters: 12, DistractorClusters: 14, MinClusters: 10,
		Discriminates: true,
	}
	calibration := store.Calibration{
		ID: fakeID("calibration", profileID), ThresholdsID: thresholdFixture(profileID, referenceID).ID,
		PopulationID: population, Binding: bindingFixture(), Split: corpus.Test,
		Algorithm: eval.BandCalibrationAlgorithm,
		Low:       0.4, High: 0.9, Confidence: 0.95, Resamples: 2000, Seed: 11,
		Bands: bandReports(), Calibrated: true,
	}
	return store.EvalResult{
		ID:             releaseID(calibration.ID, discrimination.ID),
		ProfileID:      profileID,
		ReferenceID:    referenceID,
		Discrimination: discrimination,
		Calibration:    calibration,
		Shippable:      true,
	}
}

// releaseID is what eval.NewRelease computes, so the fixture cannot hand the
// store an identity unrelated to the gates it carries.
func releaseID(calibrationID, discriminationID string) string {
	return identity.HashInputs(map[string]string{
		"calibration-id": calibrationID, "discrimination-id": discriminationID,
	})
}

func selectionFixture(profileID string, members ...string) store.ExemplarSelection {
	return store.ExemplarSelection{
		ID:            fakeID("selection", profileID),
		ProfileID:     profileID,
		N:             len(members),
		CertificateID: fakeID("certificate", profileID),
		Members:       members,
	}
}

// identifierFor is a real preserve audit identifier, which is the only grammar
// the preserve_identifiers column admits.
func identifierFor(class preserve.Class, direction preserve.Direction, item string) string {
	result := preserve.Result{Differences: []preserve.Difference{
		{Class: class, Direction: direction, Item: item},
	}}
	return result.Identifiers()[0]
}

func attemptFixture(profileID, nodeID string) store.RewriteAttempt {
	return store.RewriteAttempt{
		InvocationID:      fakeID("invocation", profileID),
		Index:             0,
		ProfileID:         profileID,
		ProviderID:        llm.ProviderOllama,
		NodeID:            nodeID,
		CurrentHash:       identity.HashBytes([]byte("current")),
		CandidateHash:     identity.HashBytes([]byte("candidate")),
		CurrentDistance:   1.2,
		CandidateDistance: 1.4,
		CurrentBand:       eval.BandDrifting,
		CandidateBand:     eval.BandNotYou,
		Preserved:         false,
		PreserveIdentifiers: []string{
			identifierFor(preserve.ClassNumber, preserve.DirectionLost, "1979"),
			identifierFor(preserve.ClassURL, preserve.DirectionInvented, "example.com"),
		},
		TellsComparison: 2,
		TellsComparable: true,
		Accepted:        false,
		Rejection:       rewrite.RejectionNotPreserved,
	}
}

// acceptedAttempt is the shape whose rejection code is deliberately empty.
func acceptedAttempt(profileID, nodeID string) store.RewriteAttempt {
	attempt := attemptFixture(profileID, nodeID)
	attempt.Preserved, attempt.PreserveIdentifiers = true, nil
	attempt.TellsComparison, attempt.CandidateDistance = -1, 0.8
	attempt.Accepted, attempt.Rejection = true, rewrite.RejectionNone
	return attempt
}

// ---------------------------------------------------------------------------
// Put helpers
// ---------------------------------------------------------------------------

func mustPutProfile(t *testing.T, s *store.Store, p store.Profile) {
	t.Helper()
	if err := s.PutProfile(ctx(), p, store.LeaveHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
}

// mustPutThreshold writes the threshold a release over this reference will
// name. A calibration's bounds must equal a STORED threshold's, so a release
// fixture is not writable until one exists.
func mustPutThreshold(t *testing.T, s *store.Store, profileID, referenceID string) {
	t.Helper()
	if err := s.PutThreshold(ctx(), thresholdFixture(profileID, referenceID)); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
}

func mustPutReference(t *testing.T, s *store.Store, r store.Reference) {
	t.Helper()
	if err := s.PutReference(ctx(), r); err != nil {
		t.Fatalf("PutReference: %v", err)
	}
}

// seededProfile writes a snapshot and a profile on it, returning both.
func seededProfile(t *testing.T, s *store.Store) (store.SnapshotWrite, store.Profile) {
	t.Helper()
	snapshot := storedGraph(t, s)
	prof := profileFixture(snapshot.ID)
	mustPutProfile(t, s, prof)
	return snapshot, prof
}

func infinity() float64   { return math.Inf(1) }
func notANumber() float64 { return math.NaN() }

// seededIDs names every artifact seedEveryArtifact wrote, so a corruption case
// can load exactly the one it damaged.
type seededIDs struct {
	Snapshot, Profile, Reference, Threshold, EvalResult, Selection, Invocation string
	// AttemptNode is the node the seeded rewrite attempt is about. An attempt is
	// identified by its invocation AND its node, so a loader cannot be reached
	// without it; Nodes below is the exemplar selection's membership, which is
	// not the same set.
	AttemptNode string
	Nodes       []string
}

// seedEveryArtifact writes one valid instance of each artifact this slice owns.
func seedEveryArtifact(t *testing.T, s *store.Store) seededIDs {
	t.Helper()
	snapshot := storedGraph(t, s)
	prof := profileFixture(snapshot.ID)
	// AdvanceHead, because profile_head is one of the artifacts and a table with
	// no row in it makes any probe against that table vacuous.
	if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	mustPutThreshold(t, s, prof.ID, ref.ID)

	threshold := thresholdFixture(prof.ID, ref.ID)
	if err := s.PutThreshold(ctx(), threshold); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	result := evalResultFixture(prof.ID, ref.ID)
	pool := poolFixture(hashA, hashB)
	if err := s.PutDistractorPool(ctx(), pool); err != nil {
		t.Fatalf("PutDistractorPool: %v", err)
	}
	result.DistractorPoolID = pool.ID
	if err := s.PutEvalResult(ctx(), result, store.AdvanceHead); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}
	selection := selectionFixture(prof.ID,
		snapshot.Documents[0].Nodes[0].ID,
		snapshot.Documents[0].Nodes[1].ID,
		snapshot.Documents[1].Nodes[0].ID,
	)
	if err := s.PutExemplarSelection(ctx(), selection); err != nil {
		t.Fatalf("PutExemplarSelection: %v", err)
	}
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	return seededIDs{
		Snapshot: snapshot.ID, Profile: prof.ID, Reference: ref.ID,
		Threshold: threshold.ID, EvalResult: result.ID, Selection: selection.ID,
		Invocation: attempt.InvocationID, AttemptNode: attempt.NodeID, Nodes: selection.Members,
	}
}

// walkTypes descends a persistence struct's fields, failing on any named type
// that is not in the permitted set.
func walkTypes(t *testing.T, declared reflect.Type, permitted map[string]bool, seen map[reflect.Type]bool) {
	t.Helper()
	if seen[declared] {
		return
	}
	seen[declared] = true
	if pkg := declared.PkgPath(); pkg != "" {
		parts := strings.Split(pkg, "/")
		qualified := parts[len(parts)-1] + "." + declared.Name()
		if !permitted[qualified] {
			t.Errorf("a persistence struct reaches %s, which is not permitted", qualified)
			return
		}
	}
	switch declared.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walkTypes(t, declared.Elem(), permitted, seen)
	case reflect.Map:
		walkTypes(t, declared.Key(), permitted, seen)
		walkTypes(t, declared.Elem(), permitted, seen)
	case reflect.Struct:
		for i := 0; i < declared.NumField(); i++ {
			walkTypes(t, declared.Field(i).Type, permitted, seen)
		}
	}
}

// aDeclaredNotReadyReason is profile's own first declared reason. An empty one
// would pair with ProductionReady false to make the combination the coupling
// refuses, and every fixture would fail loudly rather than quietly.
var aDeclaredNotReadyReason = func() string {
	reasons := profile.NotReadyReasons()
	if len(reasons) == 0 {
		return ""
	}
	return reasons[0]
}()

// seededRegister is the register every profile fixture here belongs to.
const seededRegister = "essays"

// The three states a scoring bundle has to tell apart, seeded separately so a
// test does not have to damage a full graph to reach one of them.

// seedProfileOnly leaves a headed profile with nothing to transform against.
func seedProfileOnly(t *testing.T, s *store.Store) seededIDs {
	t.Helper()
	snapshot, prof := seededProfile(t, s)
	if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	return seededIDs{Snapshot: snapshot.ID, Profile: prof.ID}
}

// seedProfileAndReference is the uncalibrated state: everything score needs to
// measure, and no release to band with.
func seedProfileAndReference(t *testing.T, s *store.Store) seededIDs {
	t.Helper()
	ids := seedProfileOnly(t, s)
	reference := referenceFixture(ids.Profile)
	mustPutReference(t, s, reference)
	ids.Reference = reference.ID
	return ids
}

// seedProfileAndTwoReferences is the state no release names a way out of: two
// references for one profile, which the schema permits and which #62 currently
// resolves by hash order.
func seedProfileAndTwoReferences(t *testing.T, s *store.Store) seededIDs {
	t.Helper()
	ids := seedProfileAndReference(t, s)
	second := referenceFixture(ids.Profile)
	second.ID = fakeID("reference", "second")
	second.MinSegments++
	mustPutReference(t, s, second)
	return ids
}
