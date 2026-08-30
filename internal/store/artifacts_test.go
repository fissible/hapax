package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// ---------------------------------------------------------------------------
// Round trips
// ---------------------------------------------------------------------------

func TestAProfileRoundTrips(t *testing.T) {
	s := newStore(t)
	_, want := seededProfile(t, s)

	got, err := s.LoadProfile(ctx(), want.ID)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("profile =\n%+v\nwant\n%+v", got, want)
	}
}

// Stats are read back in manifest order regardless of the order written, so a
// caller cannot depend on the order of the write.
func TestProfileStatsAreReadBackInManifestOrder(t *testing.T) {
	s := newStore(t)
	snapshot := storedGraph(t, s)
	prof := profileFixture(snapshot.ID)
	reversed := append([]store.ProfileStat(nil), prof.Stats...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	prof.Stats = reversed
	mustPutProfile(t, s, prof)

	got, err := s.LoadProfile(ctx(), prof.ID)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	for i, stat := range got.Stats {
		if stat.Feature != features.Definitions()[i].ID {
			t.Fatalf("stat %d is %q, want manifest order %q", i, stat.Feature, features.Definitions()[i].ID)
		}
	}
}

// A profile whose statistics do not cover the manifest is not a smaller valid
// profile; every downstream standardization would silently skip the gap.
func TestAProfileStatSetMustCoverTheManifestExactly(t *testing.T) {
	for _, c := range []struct {
		name  string
		stats func() []store.ProfileStat
	}{
		{"a missing feature", func() []store.ProfileStat { return profileStats()[1:] }},
		{"a duplicated feature", func() []store.ProfileStat {
			stats := profileStats()
			return append(stats, stats[0])
		}},
		{"a feature outside the manifest", func() []store.ProfileStat {
			stats := profileStats()
			stats[0].Feature = "invented"
			return stats
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			prof.Stats = c.stats()
			if err := s.PutProfile(ctx(), prof, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestAReferenceRoundTrips(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	want := referenceFixture(prof.ID)
	mustPutReference(t, s, want)

	got, err := s.LoadReference(ctx(), want.ID)
	if err != nil {
		t.Fatalf("LoadReference: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reference =\n%+v\nwant\n%+v", got, want)
	}
}

// Reference values are per-feature ordered lists; the order is the artifact.
func TestReferenceValuesKeepTheirOrder(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	feature := features.Definitions()[0].ID
	ref.Values[feature] = []float64{3, 1, 2, -4, 0}
	mustPutReference(t, s, ref)

	got, err := s.LoadReference(ctx(), ref.ID)
	if err != nil {
		t.Fatalf("LoadReference: %v", err)
	}
	if !reflect.DeepEqual(got.Values[feature], []float64{3, 1, 2, -4, 0}) {
		t.Errorf("values = %v, want the order written", got.Values[feature])
	}
}

// "Identical content" has one definition, and an absent list is the empty list.
// A caller that writes the two forms in either order must not see a conflict.
func TestAnAbsentListIsTheEmptyList(t *testing.T) {
	feature := features.Definitions()[0].ID

	t.Run("reference values", func(t *testing.T) {
		s := newStore(t)
		_, prof := seededProfile(t, s)
		absent := referenceFixture(prof.ID)
		delete(absent.Values, feature)
		empty := referenceFixture(prof.ID)
		empty.Values[feature] = []float64{}

		mustPutReference(t, s, empty)
		if err := s.PutReference(ctx(), absent); err != nil {
			t.Errorf("rewriting the absent form: %v", err)
		}
		got, err := s.LoadReference(ctx(), absent.ID)
		if err != nil {
			t.Fatalf("LoadReference: %v", err)
		}
		if values, ok := got.Values[feature]; ok && len(values) != 0 {
			t.Errorf("empty list read back as %v", values)
		}
	})

	t.Run("preserve identifiers", func(t *testing.T) {
		s := newStore(t)
		snapshot, prof := seededProfile(t, s)
		nodeID := snapshot.Documents[0].Nodes[0].ID
		empty := acceptedAttempt(prof.ID, nodeID)
		empty.PreserveIdentifiers = []string{}
		nilled := acceptedAttempt(prof.ID, nodeID)
		nilled.PreserveIdentifiers = nil

		if err := s.PutRewriteAttempt(ctx(), empty); err != nil {
			t.Fatalf("PutRewriteAttempt: %v", err)
		}
		if err := s.PutRewriteAttempt(ctx(), nilled); err != nil {
			t.Errorf("rewriting the nil form: %v", err)
		}
	})
}

func TestAThresholdRoundTrips(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	want := thresholdFixture(prof.ID, ref.ID)
	if err := s.PutThreshold(ctx(), want); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}

	got, err := s.LoadThreshold(ctx(), want.ID)
	if err != nil {
		t.Fatalf("LoadThreshold: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("threshold =\n%+v\nwant\n%+v", got, want)
	}
}

// The two intervals are four numbers, not two. Actionability is
// interval_low.upper < interval_high.lower, so a schema that stored one bound
// per threshold would lose exactly the quantity the rule is computed from.
func TestAThresholdKeepsBothBoundsOfBothIntervals(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	want := thresholdFixture(prof.ID, ref.ID)
	want.IntervalLow = eval.Interval{Lower: 0.11, Upper: 0.22}
	want.IntervalHigh = eval.Interval{Lower: 0.33, Upper: 0.44}
	if err := s.PutThreshold(ctx(), want); err != nil {
		t.Fatalf("PutThreshold: %v", err)
	}

	got, err := s.LoadThreshold(ctx(), want.ID)
	if err != nil {
		t.Fatalf("LoadThreshold: %v", err)
	}
	if got.IntervalLow != want.IntervalLow || got.IntervalHigh != want.IntervalHigh {
		t.Errorf("intervals = %+v %+v, want %+v %+v",
			got.IntervalLow, got.IntervalHigh, want.IntervalLow, want.IntervalHigh)
	}
}

func TestAnEvalResultRoundTrips(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	ref := referenceFixture(prof.ID)
	mustPutReference(t, s, ref)
	want := evalResultFixture(prof.ID, ref.ID)
	if err := s.PutEvalResult(ctx(), want); err != nil {
		t.Fatalf("PutEvalResult: %v", err)
	}

	got, err := s.LoadEvalResult(ctx(), want.ID)
	if err != nil {
		t.Fatalf("LoadEvalResult: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("eval result =\n%+v\nwant\n%+v", got, want)
	}
}

// A shippable release carries no reason; an unshippable one must carry one, or
// the record cannot say why the tool refused.
func TestAnEvalResultsReasonAgreesWithItsShippability(t *testing.T) {
	for _, c := range []struct {
		name      string
		shippable bool
		reason    eval.ReleaseReason
		want      bool
	}{
		{"shippable and silent", true, eval.ReleaseReasonNone, true},
		{"shippable with a reason", true, eval.ReleaseReasonDiscriminationFailed, false},
		{"unshippable with a reason", false, eval.ReleaseReasonDiscriminationFailed, true},
		{"unshippable and silent", false, eval.ReleaseReasonNone, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			result := evalResultFixture(prof.ID, ref.ID)
			result.Shippable, result.Reason = c.shippable, c.reason
			if !c.shippable {
				result.Discriminates = false
			}
			err := s.PutEvalResult(ctx(), result)
			if c.want && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.want && err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestAnExemplarSelectionRoundTripsInOrder(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	// Deliberately not sorted: the selection order is the artifact.
	members := []string{
		snapshot.Documents[1].Nodes[0].ID,
		snapshot.Documents[0].Nodes[1].ID,
		snapshot.Documents[0].Nodes[0].ID,
	}
	want := selectionFixture(prof.ID, members...)
	if err := s.PutExemplarSelection(ctx(), want); err != nil {
		t.Fatalf("PutExemplarSelection: %v", err)
	}

	got, err := s.LoadExemplarSelection(ctx(), want.ID)
	if err != nil {
		t.Fatalf("LoadExemplarSelection: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selection =\n%+v\nwant\n%+v", got, want)
	}
}

// N is not a free number beside the member list: a selection whose declared
// size disagrees with its membership is a silently reduced exemplar set, which
// is the one thing the rewrite contract forbids.
func TestASelectionsDeclaredSizeMustEqualItsMembership(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	selection := selectionFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	selection.N = 3
	if err := s.PutExemplarSelection(ctx(), selection); err == nil {
		t.Error("accepted")
	}
}

func TestASelectionMustNotRepeatAMember(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	member := snapshot.Documents[0].Nodes[0].ID
	if err := s.PutExemplarSelection(ctx(), selectionFixture(prof.ID, member, member)); err == nil {
		t.Error("accepted")
	}
}

func TestARewriteAttemptRoundTrips(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	want := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	if err := s.PutRewriteAttempt(ctx(), want); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}

	got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("attempt =\n%+v\nwant\n%+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// The audit record that already held prose once
// ---------------------------------------------------------------------------

// preserve_identifiers is the column that carried item text for two slices
// (#42). Every value must be a preserve audit identifier and nothing else.
func TestTheAuditRecordRefusesAnythingButPreserveIdentifiers(t *testing.T) {
	valid := identifierFor("number", "lost", "1979")
	for _, c := range []struct {
		name       string
		identifier string
	}{
		{"the item text the column used to hold", "1979"},
		{"the old class-prefixed form", "number:1979"},
		{"an identifier of an undeclared class", strings.Replace(valid, ":number:", ":sentiment:", 1)},
		{"an identifier of an undeclared direction", strings.Replace(valid, ":lost:", ":softened:", 1)},
		{"a digest of the wrong length", valid + "ab"},
		{"a digest that is not hex", valid[:len(valid)-1] + "z"},
		{"a different gate version", strings.Replace(valid, "preserve-v1", "preserve-v2", 1)},
		{"an empty identifier", ""},
		{"prose that merely starts with the prefix", "preserve-v1:number:lost:the year 1979"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.PreserveIdentifiers = []string{c.identifier}
			if err := s.PutRewriteAttempt(ctx(), attempt); err == nil {
				t.Error("accepted")
			} else if !errors.Is(err, store.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// The gate is only consulted once a candidate is scoreable, so "not preserved
// and nothing named" is an ordinary attempt. "Preserved and something named" is
// a contradiction, and it is the direction that would hide a leak.
func TestPreservationAgreesWithWhatItNames(t *testing.T) {
	for _, c := range []struct {
		name        string
		preserved   bool
		identifiers []string
		want        bool
	}{
		{"preserved and silent", true, nil, true},
		{"not preserved and naming what it found", false, []string{identifierFor("number", "lost", "1979")}, true},
		{"not preserved before the gate ran", false, nil, true},
		{"preserved yet naming a difference", true, []string{identifierFor("number", "lost", "1979")}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.Preserved, attempt.PreserveIdentifiers = c.preserved, c.identifiers
			err := s.PutRewriteAttempt(ctx(), attempt)
			if c.want && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.want && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// An accepted attempt has no rejection code and a rejected one has one. Storing
// them independently would let the audit record contradict itself.
func TestAcceptanceAgreesWithTheRejectionCode(t *testing.T) {
	for _, c := range []struct {
		name      string
		accepted  bool
		rejection rewrite.RejectionCode
		want      bool
	}{
		{"accepted with no code", true, rewrite.RejectionNone, true},
		{"rejected with a code", false, rewrite.RejectionNotImproved, true},
		{"accepted with a code", true, rewrite.RejectionNotImproved, false},
		{"rejected with no code", false, rewrite.RejectionNone, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := acceptedAttempt(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.Accepted, attempt.Rejection = c.accepted, c.rejection
			err := s.PutRewriteAttempt(ctx(), attempt)
			if c.want && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.want && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// An attempt that was never scored has no band. The column is closed over the
// declared bands PLUS the empty one, because an unscoreable attempt is a real
// record the loop writes.
func TestAnUnscoreableAttemptStoresNoBand(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.CurrentBand, attempt.CandidateBand = "", ""
	attempt.Preserved, attempt.PreserveIdentifiers = false, nil
	attempt.TellsComparison, attempt.TellsComparable = 0, false
	attempt.Rejection = rewrite.RejectionUnscoreable
	if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}

	got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if got.CurrentBand != "" || got.CandidateBand != "" {
		t.Errorf("bands = %q %q, want empty", got.CurrentBand, got.CandidateBand)
	}
}

// The store is what rewrite records into, so it has to satisfy the interface
// rewrite declared — with the caller's context, not a background one.
func TestARecorderSatisfiesTheRewriteStore(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID

	var recorder rewrite.Store = s.Recorder(ctx())
	attempt := rewrite.Attempt{
		Index: 3, SpanRef: nodeID,
		CurrentHash: identity.HashBytes([]byte("a")), CandidateHash: identity.HashBytes([]byte("b")),
		CurrentDistance: 1.1, CandidateDistance: 0.9,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandInRange,
		Preserved: true, TellsComparison: -1, TellsComparable: true, Accepted: true,
		Rejection: rewrite.RejectionNone,
		ProfileID: prof.ID, ProviderID: string(llm.ProviderAnthropic),
		InvocationID: fakeID("invocation", "recorder"),
	}
	if err := recorder.RecordAttempt(attempt); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}

	got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if got.NodeID != nodeID || got.ProviderID != llm.ProviderAnthropic || !got.Accepted {
		t.Errorf("recorded %+v", got)
	}
}

// A span reference that is not a node key would make the audit record point at
// nothing, which is different from pointing at a node whose file is gone.
func TestARecorderRefusesASpanRefThatIsNotANode(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)
	recorder := s.Recorder(ctx())
	for _, spanRef := range []string{"", "essays/a.md#0", identity.HashBytes([]byte("no such node"))} {
		attempt := rewrite.Attempt{
			Index: 0, SpanRef: spanRef,
			CurrentHash: identity.HashBytes([]byte("a")), CandidateHash: identity.HashBytes([]byte("b")),
			CurrentBand: eval.BandDrifting, CandidateBand: eval.BandInRange,
			Preserved: true, TellsComparable: true, Accepted: true, Rejection: rewrite.RejectionNone,
			ProfileID: prof.ID, ProviderID: string(llm.ProviderOllama),
			InvocationID: fakeID("invocation", spanRef),
		}
		if err := recorder.RecordAttempt(attempt); err == nil {
			t.Errorf("accepted span ref %q", spanRef)
		}
	}
}

// Adding a field to rewrite.Attempt must be a decision here, not something a
// struct literal absorbs. Every field is either persisted or named as not.
func TestEveryRewriteAttemptFieldIsDecidedOnPurpose(t *testing.T) {
	persisted := map[string]bool{
		"Index": true, "SpanRef": true, "CurrentHash": true, "CandidateHash": true,
		"CurrentDistance": true, "CandidateDistance": true,
		"CurrentBand": true, "CandidateBand": true,
		"Preserved": true, "PreserveIdentifiers": true,
		"TellsComparison": true, "TellsComparable": true,
		"Accepted": true, "Rejection": true,
		"ProfileID": true, "ProviderID": true, "InvocationID": true,
	}
	declared := reflect.TypeOf(rewrite.Attempt{})
	for i := 0; i < declared.NumField(); i++ {
		name := declared.Field(i).Name
		if _, decided := persisted[name]; !decided {
			t.Errorf("rewrite.Attempt.%s is neither persisted nor declared unpersisted", name)
		}
		delete(persisted, name)
	}
	for name := range persisted {
		t.Errorf("%s is declared persisted but rewrite.Attempt has no such field", name)
	}
}

// ---------------------------------------------------------------------------
// The head
// ---------------------------------------------------------------------------

// There must be no window in which the head names a profile that does not
// exist, so advancing it is not a second call.
func TestTheHeadAdvancesInTheSameTransactionAsItsProfile(t *testing.T) {
	s := newStore(t)
	snapshot := storedGraph(t, s)
	prof := profileFixture(snapshot.ID)

	if _, err := s.ProfileHead(ctx(), prof.Register); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("head before any profile = %v, want ErrNotFound", err)
	}
	if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	head, err := s.ProfileHead(ctx(), prof.Register)
	if err != nil {
		t.Fatalf("ProfileHead: %v", err)
	}
	if head != prof.ID {
		t.Errorf("head = %q, want %q", head, prof.ID)
	}
}

func TestLeavingTheHeadAloneLeavesItAlone(t *testing.T) {
	s := newStore(t)
	snapshot := storedGraph(t, s)
	first := profileFixture(snapshot.ID)
	if err := s.PutProfile(ctx(), first, store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	second := profileFixture(snapshot.ID)
	second.ID = fakeID("profile", "second")
	second.MinParagraphLexicalTokens = 55
	if err := s.PutProfile(ctx(), second, store.LeaveHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	head, err := s.ProfileHead(ctx(), first.Register)
	if err != nil {
		t.Fatalf("ProfileHead: %v", err)
	}
	if head != first.ID {
		t.Errorf("head = %q, want the profile that advanced it, %q", head, first.ID)
	}
}

// A refused profile leaves no head, because a head naming a profile that was
// never written is exactly the window the same-transaction rule closes.
func TestARefusedProfileLeavesNoHead(t *testing.T) {
	s := newStore(t)
	snapshot := storedGraph(t, s)
	prof := profileFixture(snapshot.ID)
	prof.Stats = prof.Stats[1:]

	if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err == nil {
		t.Fatal("accepted an incomplete profile")
	}
	if _, err := s.ProfileHead(ctx(), prof.Register); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("head = %v, want ErrNotFound", err)
	}
	if _, err := s.LoadProfile(ctx(), prof.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("profile = %v, want ErrNotFound", err)
	}
}

// Two registers are two heads.
func TestHeadsAreKeyedByRegister(t *testing.T) {
	s := newStore(t)
	snapshot := storedGraph(t, s)
	essays := profileFixture(snapshot.ID)
	letters := profileFixture(snapshot.ID)
	letters.ID, letters.Register = fakeID("profile", "letters"), "letters"

	for _, prof := range []store.Profile{essays, letters} {
		if err := s.PutProfile(ctx(), prof, store.AdvanceHead); err != nil {
			t.Fatalf("PutProfile %s: %v", prof.Register, err)
		}
	}
	for _, prof := range []store.Profile{essays, letters} {
		head, err := s.ProfileHead(ctx(), prof.Register)
		if err != nil {
			t.Fatalf("ProfileHead %s: %v", prof.Register, err)
		}
		if head != prof.ID {
			t.Errorf("%s head = %q, want %q", prof.Register, head, prof.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Idempotency and conflict
// ---------------------------------------------------------------------------

// Writing a key that already exists succeeds if the content is identical and
// conflicts if it is not. A retried write is safe; a changed one is corruption.
func TestRewritingAnIdenticalArtifactSucceedsAndADifferentOneConflicts(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(t *testing.T, s *store.Store, change bool) error
	}{
		{"profile", func(t *testing.T, s *store.Store, change bool) error {
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			mustPutProfile(t, s, prof)
			if change {
				prof.Stats[0].Mean += 1
			}
			return s.PutProfile(ctx(), prof, store.LeaveHead)
		}},
		{"reference", func(t *testing.T, s *store.Store, change bool) error {
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			if change {
				ref.Values[features.Definitions()[0].ID] = []float64{9}
			}
			return s.PutReference(ctx(), ref)
		}},
		{"threshold", func(t *testing.T, s *store.Store, change bool) error {
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			threshold := thresholdFixture(prof.ID, ref.ID)
			if err := s.PutThreshold(ctx(), threshold); err != nil {
				t.Fatalf("PutThreshold: %v", err)
			}
			if change {
				threshold.High += 0.01
			}
			return s.PutThreshold(ctx(), threshold)
		}},
		{"eval result", func(t *testing.T, s *store.Store, change bool) error {
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			result := evalResultFixture(prof.ID, ref.ID)
			if err := s.PutEvalResult(ctx(), result); err != nil {
				t.Fatalf("PutEvalResult: %v", err)
			}
			if change {
				result.AUC += 0.01
			}
			return s.PutEvalResult(ctx(), result)
		}},
		{"exemplar selection", func(t *testing.T, s *store.Store, change bool) error {
			snapshot, prof := seededProfile(t, s)
			first, second := snapshot.Documents[0].Nodes[0].ID, snapshot.Documents[0].Nodes[1].ID
			selection := selectionFixture(prof.ID, first, second)
			if err := s.PutExemplarSelection(ctx(), selection); err != nil {
				t.Fatalf("PutExemplarSelection: %v", err)
			}
			if change {
				// The same members in a different order are different content.
				selection.Members = []string{second, first}
			}
			return s.PutExemplarSelection(ctx(), selection)
		}},
		{"rewrite attempt", func(t *testing.T, s *store.Store, change bool) error {
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
				t.Fatalf("PutRewriteAttempt: %v", err)
			}
			if change {
				attempt.PreserveIdentifiers = attempt.PreserveIdentifiers[:1]
			}
			return s.PutRewriteAttempt(ctx(), attempt)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Run("identical", func(t *testing.T) {
				if err := c.write(t, newStore(t), false); err != nil {
					t.Errorf("a retried write failed: %v", err)
				}
			})
			t.Run("changed", func(t *testing.T) {
				err := c.write(t, newStore(t), true)
				if !errors.Is(err, store.ErrConflict) {
					t.Errorf("error = %v, want ErrConflict", err)
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Referential closure and absence
// ---------------------------------------------------------------------------

// Every artifact hangs from a parent. Writing one whose parent does not exist
// would make a graph that reads back as a smaller valid one.
func TestAnArtifactWhoseParentIsMissingIsRefused(t *testing.T) {
	absent := identity.HashBytes([]byte("no such artifact"))
	for _, c := range []struct {
		name  string
		write func(s *store.Store, snapshot store.SnapshotWrite, prof store.Profile) error
	}{
		{"a profile on no snapshot", func(s *store.Store, _ store.SnapshotWrite, _ store.Profile) error {
			return s.PutProfile(ctx(), profileFixture(absent), store.LeaveHead)
		}},
		{"a reference on no profile", func(s *store.Store, _ store.SnapshotWrite, _ store.Profile) error {
			return s.PutReference(ctx(), referenceFixture(absent))
		}},
		{"a threshold on no reference", func(s *store.Store, _ store.SnapshotWrite, prof store.Profile) error {
			return s.PutThreshold(ctx(), thresholdFixture(prof.ID, absent))
		}},
		{"an eval result on no reference", func(s *store.Store, _ store.SnapshotWrite, prof store.Profile) error {
			return s.PutEvalResult(ctx(), evalResultFixture(prof.ID, absent))
		}},
		{"a selection naming no node", func(s *store.Store, _ store.SnapshotWrite, prof store.Profile) error {
			return s.PutExemplarSelection(ctx(), selectionFixture(prof.ID, absent))
		}},
		{"an attempt on no profile", func(s *store.Store, snapshot store.SnapshotWrite, _ store.Profile) error {
			return s.PutRewriteAttempt(ctx(), attemptFixture(absent, snapshot.Documents[0].Nodes[0].ID))
		}},
		{"an attempt on no node", func(s *store.Store, _ store.SnapshotWrite, prof store.Profile) error {
			return s.PutRewriteAttempt(ctx(), attemptFixture(prof.ID, absent))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			if err := c.write(s, snapshot, prof); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestLoadingAnArtifactThatIsNotThereIsNotFound(t *testing.T) {
	s := newStore(t)
	absent := identity.HashBytes([]byte("no such artifact"))
	for _, c := range []struct {
		name string
		load func() error
	}{
		{"profile", func() error { _, err := s.LoadProfile(ctx(), absent); return err }},
		{"reference", func() error { _, err := s.LoadReference(ctx(), absent); return err }},
		{"threshold", func() error { _, err := s.LoadThreshold(ctx(), absent); return err }},
		{"eval result", func() error { _, err := s.LoadEvalResult(ctx(), absent); return err }},
		{"exemplar selection", func() error { _, err := s.LoadExemplarSelection(ctx(), absent); return err }},
		{"rewrite attempt", func() error { _, err := s.LoadRewriteAttempt(ctx(), absent, 0); return err }},
		{"profile head", func() error { _, err := s.ProfileHead(ctx(), "essays"); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.load(); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Validation on the way in
// ---------------------------------------------------------------------------

func TestNonFiniteFloatsAreRefused(t *testing.T) {
	inf, nan := infinity(), notANumber()
	for _, c := range []struct {
		name  string
		write func(t *testing.T, s *store.Store) error
	}{
		{"a profile mean", func(t *testing.T, s *store.Store) error {
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			prof.Stats[0].Mean = nan
			return s.PutProfile(ctx(), prof, store.LeaveHead)
		}},
		{"a profile variance", func(t *testing.T, s *store.Store) error {
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			prof.Stats[0].Variance = inf
			return s.PutProfile(ctx(), prof, store.LeaveHead)
		}},
		{"a reference value", func(t *testing.T, s *store.Store) error {
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			ref.Values[features.Definitions()[0].ID] = []float64{0, nan}
			return s.PutReference(ctx(), ref)
		}},
		{"a threshold bound", func(t *testing.T, s *store.Store) error {
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			threshold := thresholdFixture(prof.ID, ref.ID)
			threshold.IntervalHigh.Upper = inf
			return s.PutThreshold(ctx(), threshold)
		}},
		{"an eval result figure", func(t *testing.T, s *store.Store) error {
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			result := evalResultFixture(prof.ID, ref.ID)
			result.Cap = inf
			return s.PutEvalResult(ctx(), result)
		}},
		{"an attempt distance", func(t *testing.T, s *store.Store) error {
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.CandidateDistance = nan
			return s.PutRewriteAttempt(ctx(), attempt)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.write(t, newStore(t)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Every identity this slice writes is hex of the declared length. Store carries
// identities; carrying one that is not an identity is not carrying it.
func TestIdentitiesMustBeTheDeclaredDigestForm(t *testing.T) {
	for _, malformed := range []string{"", "not-a-hash", "abc123", strings.ToUpper(hashA)} {
		t.Run(malformed, func(t *testing.T) {
			s := newStore(t)
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			prof.ID = malformed
			if err := s.PutProfile(ctx(), prof, store.LeaveHead); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// Registers are user-named. The grammar is what keeps "no free text" true, and
// it is the grammar, not a list of the registers this author happens to use.
func TestRegistersAreUserNamedWithinADeclaredGrammar(t *testing.T) {
	for _, c := range []struct {
		register string
		want     bool
	}{
		{"essays", true},
		{"letters", true},
		{"work-email", true},
		{"a", true},
		{"9lives", true},
		{strings.Repeat("a", 32), true},
		{strings.Repeat("a", 33), false},
		{"", false},
		{"-leading", false},
		{"Essays", false},
		{"my essays", false},
		{"essays/2026", false},
	} {
		t.Run(c.register, func(t *testing.T) {
			s := newStore(t)
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			prof.Register = c.register
			err := s.PutProfile(ctx(), prof, store.AdvanceHead)
			if c.want && err != nil {
				t.Errorf("refused %q: %v", c.register, err)
			}
			if !c.want && err == nil {
				t.Errorf("accepted %q", c.register)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Validation on the way out
// ---------------------------------------------------------------------------

// Corruption must never present as insufficient evidence, which is a verdict
// this system emits legitimately and would therefore be believed.
//
// Only damage the schema CANNOT prevent is applied here. An undeclared enum
// value, a non-finite float and a contradictory pair of columns are all refused
// by CHECK constraints, and those are asserted where they live — in
// TestTheDatabaseItselfRefusesAValueOutsideEachClosedSet and the schema shape
// test. What is left for the reader to validate is what SQLite cannot express:
// membership of the feature manifest, ordinal contiguity, and a declared count
// agreeing with what is actually stored.
func TestDamageTheSchemaCannotPreventIsCorruptNotSmaller(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage string
		load   func(s *store.Store, ids seededIDs) error
	}{
		{"a profile stat outside the manifest", "UPDATE profile_stat SET feature = 'invented' WHERE rowid = 1",
			func(s *store.Store, ids seededIDs) error { _, err := s.LoadProfile(ctx(), ids.Profile); return err }},
		{"a profile stat deleted", "DELETE FROM profile_stat WHERE rowid = 1",
			func(s *store.Store, ids seededIDs) error { _, err := s.LoadProfile(ctx(), ids.Profile); return err }},
		{"a reference value ordinal with a gap", "DELETE FROM reference_value WHERE ordinal = 1",
			func(s *store.Store, ids seededIDs) error { _, err := s.LoadReference(ctx(), ids.Reference); return err }},
		{"a selection member removed", "DELETE FROM exemplar_member WHERE ordinal = 1",
			func(s *store.Store, ids seededIDs) error {
				_, err := s.LoadExemplarSelection(ctx(), ids.Selection)
				return err
			}},
		{"a selection naming the same node twice",
			"UPDATE exemplar_member SET node_id = (SELECT node_id FROM exemplar_member WHERE ordinal = 0)",
			func(s *store.Store, ids seededIDs) error {
				_, err := s.LoadExemplarSelection(ctx(), ids.Selection)
				return err
			}},
		{"a selection whose declared size no longer matches its members", "UPDATE exemplar_selection SET n = 99",
			func(s *store.Store, ids seededIDs) error {
				_, err := s.LoadExemplarSelection(ctx(), ids.Selection)
				return err
			}},
		{"an attempt claiming preservation while naming a difference", "UPDATE rewrite_attempt SET preserved = 1",
			func(s *store.Store, ids seededIDs) error {
				_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
				return err
			}},
		{"an attempt identifier naming an undeclared class",
			"UPDATE rewrite_attempt_identifier SET identifier = 'preserve-v1:sentiment:lost:0123456789abcdef'",
			func(s *store.Store, ids seededIDs) error {
				_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
				return err
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			ids := seedEveryArtifact(t, s)
			if _, err := openRaw(t, s).Exec(c.damage); err != nil {
				t.Fatalf("damaging: %v", err)
			}
			if err := c.load(s, ids); !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}

// The contradictions SQLite CAN express are constraints, not just validation on
// the way in, so a row written around the API is refused by the database too.
func TestTheDatabaseItselfRefusesTheContradictionsItCanExpress(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage string
	}{
		{"a shippable release carrying a reason", "UPDATE eval_result SET reason = 'discrimination-failed'"},
		{"an accepted attempt carrying a rejection code", "UPDATE rewrite_attempt SET accepted = 1"},
		{"a separated threshold whose bounds are not ordered", "UPDATE threshold SET t_low = 0.9, t_high = 0.4"},
		{"an interval whose lower bound is above its upper", "UPDATE threshold SET interval_low_lower = 0.9"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			seedEveryArtifact(t, s)
			if _, err := openRaw(t, s).Exec(c.damage); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The codec allowlist
// ---------------------------------------------------------------------------

// A codec per artifact, never a marshalled owner struct: every field of every
// persistence struct is declared here, so a field added to one is a decision a
// reviewer must make rather than something a struct literal absorbs.
func TestTheCodecFieldSetsAreExactlyTheAllowlist(t *testing.T) {
	declared := map[any][]string{
		store.Profile{}: {
			"ID", "SnapshotID", "Register", "Unit", "VarianceConvention",
			"ManifestDigest", "FeatureSetVersion", "MinParagraphLexicalTokens", "Stats",
		},
		store.ProfileStat{}: {
			"Feature", "N", "Mean", "Variance", "Defined", "VarianceDefined", "MinObservations",
		},
		store.Reference{}: {"ID", "ProfileID", "Split", "MinSegments", "ManifestDigest", "Values"},
		store.Threshold{}: {
			"ID", "ProfileID", "ReferenceID", "PopulationID", "Low", "High",
			"AchievedAuthor", "AchievedDistractor", "IntervalLow", "IntervalHigh", "Verdict",
		},
		store.EvalResult{}: {
			"ID", "ProfileID", "ReferenceID", "AUC", "LowerBound", "Cap",
			"AuthorSegments", "DistractorSegments", "AuthorClusters", "DistractorClusters",
			"Discriminates", "Calibrated", "Shippable", "Reason",
		},
		store.ExemplarSelection{}: {"ID", "ProfileID", "N", "CertificateID", "Members"},
		store.RewriteAttempt{}: {
			"InvocationID", "Index", "ProfileID", "ProviderID", "NodeID",
			"CurrentHash", "CandidateHash", "CurrentDistance", "CandidateDistance",
			"CurrentBand", "CandidateBand", "Preserved", "PreserveIdentifiers",
			"TellsComparison", "TellsComparable", "Accepted", "Rejection",
		},
	}
	for value, want := range declared {
		declaredType := reflect.TypeOf(value)
		var got []string
		for i := 0; i < declaredType.NumField(); i++ {
			got = append(got, declaredType.Field(i).Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s fields =\n%v\nwant\n%v", declaredType.Name(), got, want)
		}
	}
}

// A blacklist of prose-bearing types would pass anything newly written, so the
// rule is inverted: every named type a persistence struct transitively reaches
// must appear here. Widening it is a decision a reviewer makes.
//
// This cannot catch a plain string field holding prose — nothing about `string`
// says which — which is what TestTheCodecFieldSetsAreExactlyTheAllowlist and
// the column grammars are for.
func TestEveryTypeAPersistenceStructReachesIsPermitted(t *testing.T) {
	permitted := map[string]bool{
		"features.ID": true, "features.Vector": true, "features.FeatureValue": true,
		"corpus.Split": true, "corpus.Admission": true,
		"text.Kind": true, "text.Role": true, "text.ContainerKind": true,
		"text.ExclusionReason": true,
		"profile.Unit":         true, "profile.VarianceConvention": true,
		"eval.Band": true, "eval.Interval": true,
		"eval.ThresholdVerdict": true, "eval.ReleaseReason": true,
		"llm.ProviderID": true, "rewrite.RejectionCode": true,
		"store.Profile": true, "store.ProfileStat": true, "store.Reference": true,
		"store.Threshold": true, "store.EvalResult": true, "store.ExemplarSelection": true,
		"store.RewriteAttempt": true, "store.SnapshotWrite": true,
		"store.Document": true, "store.Node": true,
	}
	for _, value := range []any{
		store.Profile{}, store.ProfileStat{}, store.Reference{}, store.Threshold{},
		store.EvalResult{}, store.ExemplarSelection{}, store.RewriteAttempt{},
		store.SnapshotWrite{}, store.Document{}, store.Node{},
	} {
		walkTypes(t, reflect.TypeOf(value), permitted, map[reflect.Type]bool{})
	}
}

// ---------------------------------------------------------------------------
// What a refusal, a reopen and a race must leave behind
// ---------------------------------------------------------------------------

// ErrConflict is not enough on its own: an implementation that overwrites and
// then reports a conflict destroys the artifact it was meant to protect. Every
// artifact is reloaded after its refused write and must be unchanged.
func TestARefusedConflictLeavesTheStoredArtifactUntouched(t *testing.T) {
	s := newStore(t)
	ids := seedEveryArtifact(t, s)

	profBefore, err := s.LoadProfile(ctx(), ids.Profile)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	refBefore, err := s.LoadReference(ctx(), ids.Reference)
	if err != nil {
		t.Fatalf("LoadReference: %v", err)
	}
	thresholdBefore, err := s.LoadThreshold(ctx(), ids.Threshold)
	if err != nil {
		t.Fatalf("LoadThreshold: %v", err)
	}
	resultBefore, err := s.LoadEvalResult(ctx(), ids.EvalResult)
	if err != nil {
		t.Fatalf("LoadEvalResult: %v", err)
	}
	selectionBefore, err := s.LoadExemplarSelection(ctx(), ids.Selection)
	if err != nil {
		t.Fatalf("LoadExemplarSelection: %v", err)
	}
	attemptBefore, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}

	changedProfile := profileFixture(ids.Snapshot)
	changedProfile.Stats[0].Mean += 1
	changedReference := referenceFixture(ids.Profile)
	changedReference.Values[features.Definitions()[0].ID] = []float64{9}
	changedThreshold := thresholdFixture(ids.Profile, ids.Reference)
	changedThreshold.High += 0.01
	changedResult := evalResultFixture(ids.Profile, ids.Reference)
	changedResult.AUC += 0.01
	changedSelection := selectionFixture(ids.Profile, ids.Nodes[1], ids.Nodes[0], ids.Nodes[2])
	changedAttempt := attemptFixture(ids.Profile, ids.Nodes[0])
	changedAttempt.PreserveIdentifiers = changedAttempt.PreserveIdentifiers[:1]

	for name, write := range map[string]func() error{
		"profile":            func() error { return s.PutProfile(ctx(), changedProfile, store.LeaveHead) },
		"reference":          func() error { return s.PutReference(ctx(), changedReference) },
		"threshold":          func() error { return s.PutThreshold(ctx(), changedThreshold) },
		"eval result":        func() error { return s.PutEvalResult(ctx(), changedResult) },
		"exemplar selection": func() error { return s.PutExemplarSelection(ctx(), changedSelection) },
		"rewrite attempt":    func() error { return s.PutRewriteAttempt(ctx(), changedAttempt) },
	} {
		if err := write(); !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: error = %v, want ErrConflict", name, err)
		}
	}

	for name, check := range map[string]func() (any, any){
		"profile": func() (any, any) { got, _ := s.LoadProfile(ctx(), ids.Profile); return got, profBefore },
		"reference": func() (any, any) {
			got, _ := s.LoadReference(ctx(), ids.Reference)
			return got, refBefore
		},
		"threshold": func() (any, any) {
			got, _ := s.LoadThreshold(ctx(), ids.Threshold)
			return got, thresholdBefore
		},
		"eval result": func() (any, any) {
			got, _ := s.LoadEvalResult(ctx(), ids.EvalResult)
			return got, resultBefore
		},
		"exemplar selection": func() (any, any) {
			got, _ := s.LoadExemplarSelection(ctx(), ids.Selection)
			return got, selectionBefore
		},
		"rewrite attempt": func() (any, any) {
			got, _ := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
			return got, attemptBefore
		},
	} {
		if got, want := check(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s survived the conflict as\n%+v\nwant\n%+v", name, got, want)
		}
	}
}

// The store is a file, not a process. A cache that never reaches SQLite would
// pass every round trip above and lose everything on the next invocation.
func TestEveryArtifactSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hapax.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ids := seedEveryArtifact(t, first)
	head := "essays"
	if err := first.PutProfile(ctx(), profileFixture(ids.Snapshot), store.AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	before := loadEverything(t, first, ids)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	if got := loadEverything(t, second, ids); !reflect.DeepEqual(got, before) {
		t.Errorf("after reopen =\n%+v\nwant\n%+v", got, before)
	}
	if got, err := second.ProfileHead(ctx(), head); err != nil || got != ids.Profile {
		t.Errorf("head after reopen = %q, %v, want %q", got, err, ids.Profile)
	}
}

// Two writers of the SAME artifact both succeed: the loser of a unique-key race
// has compared nothing yet, so it rereads the winning row and accepts identical
// content. Returning ErrConflict on the collision itself would fail a safe retry.
func TestConcurrentIdenticalArtifactWritersBothSucceed(t *testing.T) {
	s := newStore(t)
	snapshot := storedGraph(t, s)
	prof := profileFixture(snapshot.ID)
	nodes := []string{
		snapshot.Documents[0].Nodes[0].ID,
		snapshot.Documents[0].Nodes[1].ID,
	}

	const writers = 8
	for _, c := range []struct {
		name  string
		setUp func()
		write func() error
	}{
		{"profile", func() {}, func() error { return s.PutProfile(ctx(), prof, store.LeaveHead) }},
		{"reference", func() { mustPutProfile(t, s, prof) },
			func() error { return s.PutReference(ctx(), referenceFixture(prof.ID)) }},
		{"exemplar selection", func() { mustPutProfile(t, s, prof) },
			func() error { return s.PutExemplarSelection(ctx(), selectionFixture(prof.ID, nodes...)) }},
		{"rewrite attempt", func() { mustPutProfile(t, s, prof) },
			func() error { return s.PutRewriteAttempt(ctx(), attemptFixture(prof.ID, nodes[0])) }},
		{"threshold", func() { mustPutProfile(t, s, prof); mustPutReference(t, s, referenceFixture(prof.ID)) },
			func() error {
				return s.PutThreshold(ctx(), thresholdFixture(prof.ID, referenceFixture(prof.ID).ID))
			}},
		{"eval result", func() { mustPutProfile(t, s, prof); mustPutReference(t, s, referenceFixture(prof.ID)) },
			func() error {
				return s.PutEvalResult(ctx(), evalResultFixture(prof.ID, referenceFixture(prof.ID).ID))
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.setUp()
			errs := make(chan error, writers)
			var start sync.WaitGroup
			start.Add(1)
			for i := 0; i < writers; i++ {
				go func() {
					start.Wait()
					errs <- c.write()
				}()
			}
			start.Done()
			for i := 0; i < writers; i++ {
				if err := <-errs; err != nil {
					t.Errorf("writer %d: %v", i, err)
				}
			}
		})
	}
}

func loadEverything(t *testing.T, s *store.Store, ids seededIDs) []any {
	t.Helper()
	prof, err := s.LoadProfile(ctx(), ids.Profile)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	ref, err := s.LoadReference(ctx(), ids.Reference)
	if err != nil {
		t.Fatalf("LoadReference: %v", err)
	}
	threshold, err := s.LoadThreshold(ctx(), ids.Threshold)
	if err != nil {
		t.Fatalf("LoadThreshold: %v", err)
	}
	result, err := s.LoadEvalResult(ctx(), ids.EvalResult)
	if err != nil {
		t.Fatalf("LoadEvalResult: %v", err)
	}
	selection, err := s.LoadExemplarSelection(ctx(), ids.Selection)
	if err != nil {
		t.Fatalf("LoadExemplarSelection: %v", err)
	}
	attempt, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, 0)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	return []any{prof, ref, threshold, result, selection, attempt}
}

// ---------------------------------------------------------------------------
// Semantics the artifacts carry
// ---------------------------------------------------------------------------

// Shippability is not an independent flag. A result that claims to ship while a
// gate failed is the stale-reuse failure the whole release contract exists to
// prevent, and it is expressible as a constraint.
func TestShippabilityIsTheConjunctionOfTheGates(t *testing.T) {
	for _, c := range []struct {
		discriminates, calibrated, shippable bool
	}{
		{true, true, true}, {true, false, false}, {false, true, false}, {false, false, false},
		{true, true, false}, {true, false, true}, {false, true, true}, {false, false, true},
	} {
		want := c.shippable == (c.discriminates && c.calibrated)
		t.Run(fmt.Sprintf("%v-%v-%v", c.discriminates, c.calibrated, c.shippable), func(t *testing.T) {
			s := newStore(t)
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			result := evalResultFixture(prof.ID, ref.ID)
			result.Discriminates, result.Calibrated, result.Shippable = c.discriminates, c.calibrated, c.shippable
			if !c.shippable {
				result.Reason = eval.ReleaseReasonDiscriminationFailed
			}
			err := s.PutEvalResult(ctx(), result)
			if want && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !want && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// The verdict is not an opinion beside the numbers: separated MEANS the bounds
// are ordered. Both directions, because only asserting one lets a verdict that
// is always "pair-incompatible" pass.
func TestTheThresholdVerdictIsItsOrdering(t *testing.T) {
	for _, c := range []struct {
		name      string
		verdict   eval.ThresholdVerdict
		low, high float64
		want      bool
	}{
		{"separated and ordered", eval.VerdictSeparated, 0.4, 0.9, true},
		{"separated but crossed", eval.VerdictSeparated, 0.9, 0.4, false},
		{"separated but equal", eval.VerdictSeparated, 0.5, 0.5, false},
		{"incompatible and crossed", eval.VerdictPairIncompatible, 0.9, 0.4, true},
		{"incompatible and equal", eval.VerdictPairIncompatible, 0.5, 0.5, true},
		{"incompatible yet ordered", eval.VerdictPairIncompatible, 0.4, 0.9, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			mustPutReference(t, s, ref)
			threshold := thresholdFixture(prof.ID, ref.ID)
			threshold.Verdict, threshold.Low, threshold.High = c.verdict, c.low, c.high
			threshold.IntervalLow = eval.Interval{Lower: c.low - 0.05, Upper: c.low + 0.05}
			threshold.IntervalHigh = eval.Interval{Lower: c.high - 0.05, Upper: c.high + 0.05}
			err := s.PutThreshold(ctx(), threshold)
			if c.want && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.want && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// An accepted attempt was scored in both bands. Storing acceptance beside an
// absent band would record a decision no measurement supports.
func TestAnAcceptedAttemptWasScoredOnBothSides(t *testing.T) {
	for _, c := range []struct {
		name               string
		current, candidate eval.Band
		want               bool
	}{
		{"both bands present", eval.BandDrifting, eval.BandInRange, true},
		{"no current band", "", eval.BandInRange, false},
		{"no candidate band", eval.BandDrifting, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := acceptedAttempt(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.CurrentBand, attempt.CandidateBand = c.current, c.candidate
			err := s.PutRewriteAttempt(ctx(), attempt)
			if c.want && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !c.want && err == nil {
				t.Error("accepted")
			}
		})
	}
}

// A digest that is well formed but not this binary's manifest would let a
// profile built under one feature contract be read as if it were built under
// another — the stale-reuse failure cache identity exists to prevent.
func TestAWellFormedButForeignFeatureContractIsRefused(t *testing.T) {
	foreign := identity.HashBytes([]byte("a manifest from another version"))
	for _, c := range []struct {
		name  string
		write func(t *testing.T, s *store.Store) error
	}{
		{"a profile manifest digest", func(t *testing.T, s *store.Store) error {
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			prof.ManifestDigest = foreign
			return s.PutProfile(ctx(), prof, store.LeaveHead)
		}},
		{"a profile feature set version", func(t *testing.T, s *store.Store) error {
			snapshot := storedGraph(t, s)
			prof := profileFixture(snapshot.ID)
			prof.FeatureSetVersion = features.SetVersion + 1
			return s.PutProfile(ctx(), prof, store.LeaveHead)
		}},
		{"a reference manifest digest", func(t *testing.T, s *store.Store) error {
			_, prof := seededProfile(t, s)
			ref := referenceFixture(prof.ID)
			ref.ManifestDigest = foreign
			return s.PutReference(ctx(), ref)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.write(t, newStore(t)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// An exemplar of a profile comes from that profile's own corpus. A member from
// another snapshot is a representative of a pool the profile never saw.
func TestASelectionMayOnlyNameNodesInItsProfilesSnapshot(t *testing.T) {
	s := newStore(t)
	_, prof := seededProfile(t, s)

	other := snapshotWrite(document("letters/c.md", identity.HashBytes([]byte("elsewhere")), node(0, 0, 9)))
	mustPutSnapshot(t, s, other)
	foreign := withDerivedIDs(other).Documents[0].Nodes[0].ID

	if err := s.PutExemplarSelection(ctx(), selectionFixture(prof.ID, foreign)); err == nil {
		t.Error("accepted a member from another snapshot")
	}
}

// The error a refused identifier produces must not carry the identifier. The
// invariant's scope covers diagnostic output, and this is the one column that
// has already leaked once.
func TestARefusedIdentifierIsNotEchoedBackInTheError(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	secret := "preserve-v1:number:lost:the year 1979 in Warsaw"
	attempt.PreserveIdentifiers = []string{secret}

	err := s.PutRewriteAttempt(ctx(), attempt)
	if err == nil {
		t.Fatal("accepted")
	}
	for _, fragment := range []string{secret, "1979", "Warsaw"} {
		if strings.Contains(err.Error(), fragment) {
			t.Errorf("the error repeats %q: %v", fragment, err)
		}
	}
}

// Read integrity closes over the profile graph, not only over ingestion. A
// threshold or eval result that combines one profile with another profile's
// reference measures a population it was never fitted against — and would
// present as an ordinary, smaller-evidence result rather than as damage.
func TestAnArtifactMayNotCombineAProfileWithAnotherProfilesReference(t *testing.T) {
	setUp := func(t *testing.T) (*store.Store, store.Profile, store.Reference) {
		t.Helper()
		s := newStore(t)
		snapshot, first := seededProfile(t, s)
		second := profileFixture(snapshot.ID)
		second.ID, second.Register = fakeID("profile", "second"), "letters"
		mustPutProfile(t, s, second)
		foreign := referenceFixture(second.ID)
		mustPutReference(t, s, foreign)
		return s, first, foreign
	}

	t.Run("a threshold", func(t *testing.T) {
		s, prof, foreign := setUp(t)
		if err := s.PutThreshold(ctx(), thresholdFixture(prof.ID, foreign.ID)); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("an eval result", func(t *testing.T) {
		s, prof, foreign := setUp(t)
		if err := s.PutEvalResult(ctx(), evalResultFixture(prof.ID, foreign.ID)); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("and a stored threshold that already does is corrupt", func(t *testing.T) {
		s, prof, foreign := setUp(t)
		own := referenceFixture(prof.ID)
		mustPutReference(t, s, own)
		threshold := thresholdFixture(prof.ID, own.ID)
		if err := s.PutThreshold(ctx(), threshold); err != nil {
			t.Fatalf("PutThreshold: %v", err)
		}
		if _, err := openRaw(t, s).Exec("UPDATE threshold SET reference_id = ?", foreign.ID); err != nil {
			t.Fatalf("damaging: %v", err)
		}
		if _, err := s.LoadThreshold(ctx(), threshold.ID); !errors.Is(err, store.ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
	})
	t.Run("and a stored eval result that already does is corrupt", func(t *testing.T) {
		s, prof, foreign := setUp(t)
		own := referenceFixture(prof.ID)
		mustPutReference(t, s, own)
		result := evalResultFixture(prof.ID, own.ID)
		if err := s.PutEvalResult(ctx(), result); err != nil {
			t.Fatalf("PutEvalResult: %v", err)
		}
		if _, err := openRaw(t, s).Exec("UPDATE eval_result SET reference_id = ?", foreign.ID); err != nil {
			t.Fatalf("damaging: %v", err)
		}
		if _, err := s.LoadEvalResult(ctx(), result.ID); !errors.Is(err, store.ErrCorrupt) {
			t.Errorf("error = %v, want ErrCorrupt", err)
		}
	})
}

// Store carries identities; it does not recompute them, because only the
// snapshot, document and node preimages are stored. That is a claim about the
// SCHEMA, so it is checked against the schema: every input to a profile
// identity is mapped to the column that holds it, or to nothing, and at least
// one must hold nothing. When the map says everything is stored, the ID has
// become recomputable and must be verified rather than carried.
func TestAProfileIdentityIsNotRecomputableFromWhatIsStored(t *testing.T) {
	// "" means the schema deliberately does not hold this input.
	heldBy := map[string]string{
		"feature-manifest-digest":      "profile.manifest_digest",
		"register":                     "profile.register",
		"snapshot-id":                  "profile.snapshot_id",
		"unit":                         "profile.unit",
		"variance-convention":          "profile.variance_convention",
		"min-paragraph-lexical-tokens": "profile.min_paragraph_lexical_tokens",
		"min-documents":                "",
		"min-observations-per-feature": "",
		"min-paragraphs":               "",
		"outlier-algorithm":            "",
		"outlier-mads":                 "",
		"profile-schema-version":       "",
		"split":                        "",
	}
	inputs := (&profile.Profile{}).IdentityInputs()
	for key := range inputs {
		if _, mapped := heldBy[key]; !mapped {
			t.Errorf("profile identity hashes %q, which this map does not account for", key)
		}
	}
	for key, column := range heldBy {
		if _, present := inputs[key]; !present {
			t.Errorf("%q is mapped to %q but is no longer a profile identity input", key, column)
			continue
		}
		if column == "" {
			continue
		}
		table, name, _ := strings.Cut(column, ".")
		if !declaredColumn(table, name) {
			t.Errorf("%q is mapped to %s, which the schema does not declare", key, column)
		}
	}

	unheld := 0
	for _, column := range heldBy {
		if column == "" {
			unheld++
		}
	}
	if unheld == 0 {
		t.Error("every profile identity input is now stored; the ID must be verified, not carried")
	}
}

func declaredColumn(table, column string) bool {
	for _, declared := range declaredSchema[table] {
		if declared == column {
			return true
		}
	}
	return false
}
