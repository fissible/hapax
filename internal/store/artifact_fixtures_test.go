package store_test

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
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
		Stats:                     profileStats(),
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

func evalResultFixture(profileID, referenceID string) store.EvalResult {
	return store.EvalResult{
		ID:                 fakeID("eval", profileID, referenceID),
		ProfileID:          profileID,
		ReferenceID:        referenceID,
		AUC:                0.82,
		LowerBound:         0.71,
		Cap:                2.5,
		AuthorSegments:     120,
		DistractorSegments: 140,
		AuthorClusters:     12,
		DistractorClusters: 14,
		Discriminates:      true,
		Calibrated:         true,
		Shippable:          true,
		Reason:             eval.ReleaseReasonNone,
	}
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
	Nodes                                                                      []string
}

// seedEveryArtifact writes one valid instance of each artifact this slice owns.
func seedEveryArtifact(t *testing.T, s *store.Store) seededIDs {
	t.Helper()
	snapshot, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)

	threshold := thresholdFixture(prof.ID, ref.ID)
	if err := s.PutThreshold(ctx(), threshold); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}
	result := evalResultFixture(prof.ID, ref.ID)
	if err := s.PutEvalResult(ctx(), result); err != nil {
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
		Invocation: attempt.InvocationID, Nodes: selection.Members,
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
