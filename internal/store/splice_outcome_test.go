package store_test

// #135's half of the record: the column, and what it will and will not accept.
//
// # Contract
//
// `rewrite_attempt.splice` names a member of `rewrite.SpliceOutcomes()`. The
// empty member means no verdict is recorded, and is the value every row written
// before this slice carries — the migration adds the column with `DEFAULT ''`
// rather than deriving a verdict from `rejection`, because a row's rejection says
// which code won and not what the gate answered.
//
// # Unknown evidence is allowed; contradictory evidence is not
//
// Those are different things and the column has to tell them apart. `''` with any
// rejection is admissible, including `accepted=1` and `not-spliceable`, because
// that is exactly the shape of every historical row. But two combinations cannot
// both be true of one attempt and are refused on write AND on read:
//
//	accepted=1   with  not-intact      an accepted candidate spliced
//	not-spliceable with intact         that rejection IS the gate saying no
//	a gate-SKIPPING rejection with either verdict   the gate never ran
//
// The third was found reviewing the implementation, after the first freeze, and
// amended in by consensus rather than left to a follow-up: the other two are about
// acceptance and about the splice rejection, and refusing two of three derivable
// contradictions while admitting the third is arbitrary rather than principled.
//
// Refused on read as well because the write path is not the only way a row
// arrives: a migration, a restore, or another process could put one there, and a
// loader that returns a self-contradictory record hands it to a caller as fact.

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// The verdict survives a round trip.
func TestTheSpliceVerdictIsStoredAndLoaded(t *testing.T) {
	for _, outcome := range rewrite.SpliceOutcomes() {
		t.Run(string(outcome)+"|", func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.Splice = outcome
			// Refused and not accepted, so no contradiction rule applies to any
			// of the three values this table walks.
			attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotImproved

			if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
				t.Fatalf("PutRewriteAttempt: %v", err)
			}
			got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
			if err != nil {
				t.Fatalf("LoadRewriteAttempt: %v", err)
			}
			if got.Splice != outcome {
				t.Errorf("stored %q and loaded %q", outcome, got.Splice)
			}
		})
	}
}

// A value outside the vocabulary is refused.
func TestASpliceValueOutsideTheVocabularyIsRefused(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.Splice = "spliceable"

	if err := s.PutRewriteAttempt(ctx(), attempt); err == nil {
		t.Error("a splice outcome outside SpliceOutcomes() was accepted")
	}
}

// The RECORDER carries the verdict, unchanged, on every shape the loop emits.
//
// This is the only test that crosses the adapter the production loop writes
// through: every other test here builds a `store.RewriteAttempt` by hand or calls
// `PutRewriteAttempt` directly, and the loop's own tests inspect a fake store. So
// the adapter is where a verdict can be dropped, narrowed or invented with both
// ends correct, and three separate mutations of it survived earlier versions of
// this table, each measured by codex:
//
//	Splice omitted from the mapping entirely
//	Splice cleared when Accepted or the rejection is not-spliceable
//	Splice set to intact when it arrives empty            — fabricating evidence
//
// The last is why the EMPTY row is here. A table of non-empty verdicts cannot see
// a value being invented, and inventing one is worse than losing one: a lost
// verdict reads as "not recorded", which is true of most of this table's history,
// while an invented one asserts the gate said something it never said.
//
// All five shapes the loop can produce, because those mutations were each
// conditioned on a different one.
func TestTheRecorderCarriesTheSpliceVerdict(t *testing.T) {
	for _, c := range []struct {
		name      string
		accepted  bool
		rejection rewrite.RejectionCode
		splice    rewrite.SpliceOutcome
	}{
		{"refused elsewhere, intact", false, rewrite.RejectionNotImproved, rewrite.SpliceIntact},
		{"refused elsewhere, not-intact", false, rewrite.RejectionNotImproved, rewrite.SpliceNotIntact},
		{"accepted, intact", true, "", rewrite.SpliceIntact},
		{"not-spliceable, not-intact", false, rewrite.RejectionNotSpliceable, rewrite.SpliceNotIntact},
		// The gates never ran. Nothing may put a verdict here.
		{"gates skipped, no verdict", false, rewrite.RejectionCandidateUnscoreable, rewrite.SpliceNotRecorded},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := rewrite.Attempt{
				InvocationID: fakeID("invocation", prof.ID), Index: 0,
				ProfileID: prof.ID, ProviderID: string(llm.ProviderOllama),
				SpanRef:     snapshot.Documents[0].Nodes[0].ID,
				CurrentHash: identity.HashBytes([]byte("current")),
				// A DISTINCT candidate hash per case, so one case cannot read back
				// another's row.
				CandidateHash:   identity.HashBytes([]byte("candidate " + c.name)),
				CurrentDistance: 1.2, CandidateDistance: 1.4,
				CurrentBand: eval.BandDrifting, CandidateBand: eval.BandNotYou,
				Preserved: true, TellsComparable: true, TellsComparison: -1,
				Accepted: c.accepted, Rejection: c.rejection, Splice: c.splice,
			}

			if err := s.Recorder(ctx()).RecordAttempt(attempt); err != nil {
				t.Fatalf("RecordAttempt: %v", err)
			}

			got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.SpanRef, 0)
			if err != nil {
				t.Fatalf("LoadRewriteAttempt: %v", err)
			}
			if got.Splice != c.splice {
				t.Errorf("the loop handed the recorder splice %q and the row holds %q — the "+
					"adapter changed the evidence between the loop and the database",
					c.splice, got.Splice)
			}
		})
	}
}

// gateSkippingRejections are the codes that reach an attempt WITHOUT the gate
// block running, so a verdict beside one of them is unreachable by any legitimate
// path: the loop assigns them before the gates, and a row written before this
// slice has no verdict at all.
//
// Four, not five. `unscoreable` is the current text's own and returns
// `TerminalNotEntered` before any attempt exists, so no row can carry it and a
// rule about its verdict would constrain nothing. Stated because its absence from
// this list is a decision rather than an oversight.
var gateSkippingRejections = []rewrite.RejectionCode{
	rewrite.RejectionNotOneSegment,
	rewrite.RejectionCandidateUnscoreable,
	rewrite.RejectionUncalibrated,
	rewrite.RejectionDifferentFeatures,
}

// A rejection that skipped the gates cannot carry a verdict.
//
// The third derivable contradiction, found in review after the first freeze and
// amended in by consensus. The other two are about acceptance and about the
// splice rejection; this one is about the rejections that mean the gate never
// ran. Validating two of three while leaving the third silent would be arbitrary.
//
// Every rejection crossed with both non-empty verdicts, and the empty verdict as a
// passing control on each — because a rule that refused `”` here would refuse
// every row the loop writes on these paths, which is the opposite of the intent.
func TestAGateSkippingRejectionCannotCarryAVerdict(t *testing.T) {
	for _, rejection := range gateSkippingRejections {
		for _, c := range []struct {
			splice rewrite.SpliceOutcome
			admit  bool
		}{
			{rewrite.SpliceIntact, false},
			{rewrite.SpliceNotIntact, false},
			// The control: this is what the loop actually writes on these paths.
			{rewrite.SpliceNotRecorded, true},
		} {
			t.Run(string(rejection)+" with "+string(c.splice)+"|", func(t *testing.T) {
				s := newStore(t)
				snapshot, prof := seededProfile(t, s)
				attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
				attempt.Accepted, attempt.Rejection, attempt.Splice = false, rejection, c.splice

				err := s.PutRewriteAttempt(ctx(), attempt)

				if c.admit && err != nil {
					t.Errorf("the row the loop writes on this path was refused: %v", err)
				}
				if !c.admit && !errors.Is(err, store.ErrInvalid) {
					t.Errorf("a %q attempt carrying splice %q returned %v, want ErrInvalid — "+
						"the gates never ran, so there is no verdict to have",
						rejection, c.splice, err)
				}
			})
		}
	}
}

// The schema refuses it too.
func TestTheDatabaseRefusesAVerdictBesideAGateSkippingRejection(t *testing.T) {
	for _, rejection := range gateSkippingRejections {
		for _, c := range []struct {
			splice string
			admit  bool
		}{
			{"intact", false}, {"not-intact", false}, {"", true},
		} {
			t.Run(string(rejection)+" with "+c.splice+"|", func(t *testing.T) {
				s := newStore(t)
				snapshot, prof := seededProfile(t, s)
				attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)

				err := insertAttemptRaw(t, openRaw(t, s), attempt, prof.ID,
					snapshot.Documents[0].Nodes[0].ID, 0, string(rejection), c.splice)

				if c.admit && err != nil {
					t.Errorf("the schema refused an admissible row: %v", err)
				}
				if !c.admit && err == nil {
					t.Errorf("the schema accepted a %q attempt carrying splice %q",
						rejection, c.splice)
				}
			})
		}
	}
}

// And the loader calls it corruption, with the CHECK stood down to get it in.
func TestTheLoaderRefusesAVerdictBesideAGateSkippingRejection(t *testing.T) {
	for _, rejection := range gateSkippingRejections {
		for _, c := range []struct {
			splice  string
			corrupt bool
		}{
			{"intact", true}, {"not-intact", true}, {"", false},
		} {
			t.Run(string(rejection)+" with "+c.splice+"|", func(t *testing.T) {
				s := newStore(t)
				snapshot, prof := seededProfile(t, s)
				attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
				node := snapshot.Documents[0].Nodes[0].ID

				insertAttemptPastTheChecks(t, s, attempt, prof.ID, node,
					0, string(rejection), c.splice)

				_, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, node, 0)

				if c.corrupt && !errors.Is(err, store.ErrCorrupt) {
					t.Errorf("a %q attempt carrying splice %q loaded with %v, want ErrCorrupt",
						rejection, c.splice, err)
				}
				if !c.corrupt && err != nil {
					t.Errorf("the row the loop writes on this path failed to load: %v", err)
				}
			})
		}
	}
}

// Contradictory evidence is refused on write; unknown evidence is not.
//
// The three admissible rows are the historical shapes: a row from before this
// slice carries no verdict whatever its rejection, and refusing those would make
// the store unable to hold its own past.
func TestContradictorySpliceEvidenceIsRefusedOnWrite(t *testing.T) {
	for _, c := range []struct {
		name      string
		accepted  bool
		rejection rewrite.RejectionCode
		splice    rewrite.SpliceOutcome
		admit     bool
	}{
		{"accepted and intact", true, "", rewrite.SpliceIntact, true},
		{"accepted with no verdict", true, "", rewrite.SpliceNotRecorded, true},
		{"not-spliceable and not-intact", false, rewrite.RejectionNotSpliceable, rewrite.SpliceNotIntact, true},
		{"not-spliceable with no verdict", false, rewrite.RejectionNotSpliceable, rewrite.SpliceNotRecorded, true},
		{"refused elsewhere and intact", false, rewrite.RejectionNotImproved, rewrite.SpliceIntact, true},
		{"refused elsewhere and not-intact", false, rewrite.RejectionNotImproved, rewrite.SpliceNotIntact, true},

		// An accepted candidate reached assembly, so it spliced.
		{"accepted and NOT intact", true, "", rewrite.SpliceNotIntact, false},
		// That rejection is the gate saying no; intact contradicts it outright.
		{"not-spliceable and intact", false, rewrite.RejectionNotSpliceable, rewrite.SpliceIntact, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.Accepted, attempt.Rejection, attempt.Splice = c.accepted, c.rejection, c.splice
			if c.accepted {
				// The schema already requires both bands on an accepted row.
				attempt.Preserved, attempt.PreserveIdentifiers = true, nil
			}

			err := s.PutRewriteAttempt(ctx(), attempt)

			if c.admit && err != nil {
				t.Fatalf("an admissible row was refused: %v", err)
			}
			// ErrInvalid specifically. A bare non-nil check is satisfied by a
			// missing-node error, an incidental SQL failure, or ErrConflict from a
			// fixture collision — three ways to pass for a reason that is not the
			// contradiction.
			if !c.admit {
				if !errors.Is(err, store.ErrInvalid) {
					t.Errorf("a row whose acceptance and splice verdict contradict each "+
						"other returned %v, want ErrInvalid", err)
				}
				return
			}
			// And READ BACK, because accepting a row is not keeping its verdict:
			// clearing `Splice` after validation whenever the row is accepted or
			// refused as not-spliceable passes every write assertion above. The
			// round-trip test next door only walks `not-improved`, so these two
			// shapes — accepted+intact and not-spliceable+not-intact — are loaded
			// nowhere else. Measured by codex as a surviving mutant.
			got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
			if err != nil {
				t.Fatalf("LoadRewriteAttempt: %v", err)
			}
			if got.Splice != c.splice {
				t.Errorf("stored splice %q and loaded %q", c.splice, got.Splice)
			}
		})
	}
}

// The DATABASE refuses the contradiction too, which is the layer a raw write meets.
//
// Both rules are CHECK constraints as well as Go validation, so a contradictory row
// cannot arrive by any ordinary path — a migration, a restore, another process.
// Added in place with the column, which SQLite permits and which
// `TestTheSpliceCheckIsEnforcedAfterTheColumnIsAdded` shows actually binds.
func TestTheDatabaseRefusesContradictorySpliceEvidence(t *testing.T) {
	for _, c := range []struct {
		name      string
		accepted  int
		rejection string
		splice    string
		admit     bool
	}{
		{"accepted and not-intact", 1, "", "not-intact", false},
		{"not-spliceable and intact", 0, "not-spliceable", "intact", false},
		// The historical shapes, which the schema must keep admitting.
		{"accepted with no verdict", 1, "", "", true},
		{"not-spliceable with no verdict", 0, "not-spliceable", "", true},
		{"accepted and intact", 1, "", "intact", true},
		{"refused elsewhere and not-intact", 0, "not-improved", "not-intact", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)

			err := insertAttemptRaw(t, openRaw(t, s), attempt, prof.ID,
				snapshot.Documents[0].Nodes[0].ID, c.accepted, c.rejection, c.splice)

			if c.admit && err != nil {
				t.Errorf("the schema refused an admissible row: %v", err)
			}
			if !c.admit && err == nil {
				t.Error("the schema accepted a row whose acceptance and splice verdict " +
					"contradict each other")
			}
		})
	}
}

// And the LOADER refuses it, with the CHECK stood down to get the row in.
//
// Defence in depth needs both layers tested, and a CHECK makes the read rule
// unreachable by ordinary means — which is the fork I could not settle alone.
// `PRAGMA ignore_check_constraints` is the way through: it is per-connection, so
// the write happens on one pinned connection with enforcement off and the load
// happens through the store with enforcement untouched.
//
// Without this the read-side validation would be untestable dead code, and
// untested code that exists to catch corruption is the worst kind to have.
func TestTheLoaderRefusesContradictorySpliceEvidence(t *testing.T) {
	for _, c := range []struct {
		name      string
		accepted  int
		rejection string
		splice    string
		corrupt   bool
	}{
		{"accepted and not-intact", 1, "", "not-intact", true},
		{"not-spliceable and intact", 0, "not-spliceable", "intact", true},
		// Controls, written the same way, so the test is not satisfied by a
		// loader that refuses everything seeded through this route.
		{"accepted with no verdict", 1, "", "", false},
		{"not-spliceable with no verdict", 0, "not-spliceable", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			node := snapshot.Documents[0].Nodes[0].ID

			insertAttemptPastTheChecks(t, s, attempt, prof.ID, node,
				c.accepted, c.rejection, c.splice)

			_, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, node, 0)

			// ErrCorrupt specifically: a bare non-nil check is satisfied by
			// ErrNotFound, which is what a loader that never saw the row returns
			// and is a different claim entirely.
			if c.corrupt && !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("a self-contradictory row loaded with %v, want ErrCorrupt", err)
			}
			if !c.corrupt && err != nil {
				t.Errorf("a row carrying no verdict failed to load: %v", err)
			}
		})
	}
}

// insertAttemptRaw writes one attempt row with raw SQL and returns what the
// database said, so a test can ask what the SCHEMA permits.
func insertAttemptRaw(t *testing.T, db *sql.DB, attempt store.RewriteAttempt,
	profileID, node string, accepted int, rejection, splice string) error {
	t.Helper()
	_, err := db.ExecContext(ctx(),
		`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,
			node_id,current_hash,candidate_hash,current_distance,candidate_distance,
			current_band,candidate_band,preserved,tells_comparison,tells_comparable,
			accepted,rejection,splice)
			VALUES (?,0,?,'ollama',?,?,?,1.2,0.4,'drifting','in-range',1,0,1,?,?,?)`,
		attempt.InvocationID, profileID, node, attempt.CurrentHash, attempt.CandidateHash,
		accepted, rejection, splice)
	return err
}

// insertAttemptPastTheChecks writes a row the CHECK constraints forbid, on ONE
// pinned connection with them stood down.
//
// Pinned, because `ignore_check_constraints` is a per-connection setting and a
// pooled `*sql.DB` would be free to run the INSERT on a different connection than
// the PRAGMA. Turned back on afterwards for the same reason: the connection
// returns to the pool.
func insertAttemptPastTheChecks(t *testing.T, s *store.Store, attempt store.RewriteAttempt,
	profileID, node string, accepted int, rejection, splice string) {
	t.Helper()
	conn, err := openRaw(t, s).Conn(ctx())
	if err != nil {
		t.Fatalf("pinning a connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx(), "PRAGMA ignore_check_constraints=ON"); err != nil {
		t.Fatalf("standing down the checks: %v", err)
	}
	if _, err := conn.ExecContext(ctx(),
		`INSERT INTO rewrite_attempt (invocation_id,attempt_index,profile_id,provider_id,
			node_id,current_hash,candidate_hash,current_distance,candidate_distance,
			current_band,candidate_band,preserved,tells_comparison,tells_comparable,
			accepted,rejection,splice)
			VALUES (?,0,?,'ollama',?,?,?,1.2,0.4,'drifting','in-range',1,0,1,?,?,?)`,
		attempt.InvocationID, profileID, node, attempt.CurrentHash, attempt.CandidateHash,
		accepted, rejection, splice); err != nil {
		t.Fatalf("seeding past the checks: %v", err)
	}
	if _, err := conn.ExecContext(ctx(), "PRAGMA ignore_check_constraints=OFF"); err != nil {
		t.Fatalf("restoring the checks: %v", err)
	}
}

// The verdict is part of the record's identity, and the empty value is not a
// wildcard.
//
// Attempts are immutable: re-recording one with a different value is a conflict,
// not an update. Two ways to get that wrong, and the second is the dangerous one.
//
// Omitting `Splice` from the equality lets a second write change the evidence
// while every other column matches. Treating the empty value as "matches
// anything" —
//
//	a.Splice == "" || b.Splice == "" || a.Splice == b.Splice
//
// — is worse, and was measured by codex as a surviving mutant: the caller is told
// the write SUCCEEDED while the stored row keeps its old value. On this column
// that is not a corner case. Empty is what every row written before this slice
// carries, so the first thing anyone re-records against is an empty one, and the
// failure is silent in exactly the place the data is oldest.
//
// Five transitions, each in a fresh store, because the stored value afterwards is
// as much the assertion as the error is.
func TestChangingOnlyTheSpliceVerdictIsAConflict(t *testing.T) {
	for _, c := range []struct {
		name          string
		first, second rewrite.SpliceOutcome
	}{
		{"intact to not-intact", rewrite.SpliceIntact, rewrite.SpliceNotIntact},
		// Empty on either side. A wildcard comparison admits all four.
		{"no verdict to intact", rewrite.SpliceNotRecorded, rewrite.SpliceIntact},
		{"no verdict to not-intact", rewrite.SpliceNotRecorded, rewrite.SpliceNotIntact},
		{"intact to no verdict", rewrite.SpliceIntact, rewrite.SpliceNotRecorded},
		{"not-intact to no verdict", rewrite.SpliceNotIntact, rewrite.SpliceNotRecorded},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			// Refused for an unrelated reason, so no contradiction rule applies to
			// any value this table walks and the only thing changing is the
			// evidence.
			attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotImproved
			attempt.Splice = c.first
			if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
				t.Fatalf("PutRewriteAttempt: %v", err)
			}

			// Byte-identical but for the verdict.
			attempt.Splice = c.second
			err := s.PutRewriteAttempt(ctx(), attempt)

			if !errors.Is(err, store.ErrConflict) {
				t.Errorf("re-recording with splice %q over %q returned %v, want ErrConflict",
					c.second, c.first, err)
			}
			// And the stored value is the FIRST one. Without this, a conflict that
			// overwrote before refusing would pass the assertion above.
			got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, attempt.NodeID, attempt.Index)
			if err != nil {
				t.Fatalf("LoadRewriteAttempt: %v", err)
			}
			if got.Splice != c.first {
				t.Errorf("the refused write changed the stored verdict from %q to %q",
					c.first, got.Splice)
			}
		})
	}
}

// Re-recording an IDENTICAL attempt, verdict included, is not a conflict.
//
// The other half of immutability: the loop's recorder is idempotent, and a field
// added to the equality comparison must not break that.
func TestReRecordingTheSameAttemptIncludingItsVerdictIsNotAConflict(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := attemptFixture(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionNotImproved
	attempt.Splice = rewrite.SpliceNotIntact

	for i := range 2 {
		if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
			t.Fatalf("PutRewriteAttempt call %d: %v", i+1, err)
		}
	}
}
