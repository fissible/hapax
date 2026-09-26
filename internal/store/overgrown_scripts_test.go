package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// ---------------------------------------------------------------------------
// The scripts a refused candidate grew out of proportion
// ---------------------------------------------------------------------------
//
// #107's sibling of `rewrite_attempt_script`. The same argument for persisting
// it: a refusal discards the prose, so the record is the only durable trace that
// the substitution was attempted. The same argument for its safety: a script
// name is one of the 163 keys of `unicode.Scripts`, drawn from a closed
// vocabulary rather than from the paragraph.
//
// Shape, specified rather than left open for the same reason as its sibling —
// the corruption probes below damage ordinals, and a joined column or a blob has
// none:
//
//	rewrite_attempt_overgrown_script(invocation_id, node_id, attempt_index, ordinal, script)
//
// with the `FOREIGN KEY ... ON DELETE CASCADE` its sibling carries. #91's review
// proved that FK is enforced by nothing unless a test says so: dropping it
// passed the entire store package.
//
// Three test files outside this slice must be widened, all sanctioned:
// `allowlist_test.go`'s `declaredSchema` AND its separate foreign-key map,
// and `vocabulary_test.go`'s `textualColumnGrammars` plus `grammarProbes`.
// The foreign-key map is the one whose omission produces no failure at all,
// which is how it survived two review rounds on #91.

// refusedForGrowth is the shape #107 produces: every other guard passed, and the
// candidate was refused for a script that grew out of proportion.
func refusedForGrowth(t *testing.T, profileID, nodeID string, scripts ...string) store.RewriteAttempt {
	t.Helper()
	attempt := attemptFixture(profileID, nodeID)
	attempt.Preserved, attempt.PreserveIdentifiers = true, nil
	attempt.TellsComparison, attempt.TellsComparable = -1, true
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionLanguageGrowth
	attempt.OvergrownScripts = scriptsOf(t, scripts...)
	return attempt
}

// The scripts survive a write and a read, in the order recorded.
//
// The fixture is deliberately NOT alphabetical. `Overgrown` sorts, so every
// natural fixture is sorted, and a loader that returned the set sorted rather
// than by ordinal read back identical to a correct one — proven reachable on the
// sibling column.
func TestTheOvergrownScriptsRoundTrip(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	want := refusedForGrowth(t, prof.ID, nodeID, "Han", "Cyrillic")

	if err := s.PutRewriteAttempt(ctx(), want); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.NodeID, want.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if !reflect.DeepEqual(got.OvergrownScripts, want.OvergrownScripts) {
		t.Errorf("read back %v, want %v", got.OvergrownScripts, want.OvergrownScripts)
	}
	if got.Rejection != rewrite.RejectionLanguageGrowth {
		t.Errorf("rejection = %q, want %q", got.Rejection, rewrite.RejectionLanguageGrowth)
	}
}

// The two columns are independent, and an attempt can carry both.
//
// Precedence reports one reason; the measurements are separate facts. A schema
// that pooled them, or a loader that read one table for both fields, disagrees.
func TestAnAttemptCanCarryIntroducedAndOvergrownScriptsAtOnce(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	want := refusedForLanguage(t, prof.ID, nodeID, "Greek")
	want.OvergrownScripts = scriptsOf(t, "Greek", "Han")

	if err := s.PutRewriteAttempt(ctx(), want); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.NodeID, want.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if !reflect.DeepEqual(got.IntroducedScripts, []string{"Greek"}) {
		t.Errorf("introduced read back %v, want [Greek]", got.IntroducedScripts)
	}
	if !reflect.DeepEqual(got.OvergrownScripts, []string{"Greek", "Han"}) {
		t.Errorf("overgrown read back %v, want [Greek Han]", got.OvergrownScripts)
	}
}

// Both forms of "none" are ordinary, since most attempts grow nothing.
func TestAnAttemptGrowingNoScriptsStoresEitherWay(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID

	empty := acceptedAttempt(prof.ID, nodeID)
	empty.OvergrownScripts = []string{}
	if err := s.PutRewriteAttempt(ctx(), empty); err != nil {
		t.Fatalf("the empty form: %v", err)
	}
	nilled := acceptedAttempt(prof.ID, nodeID)
	nilled.OvergrownScripts = nil
	if err := s.PutRewriteAttempt(ctx(), nilled); err != nil {
		t.Errorf("rewriting the nil form: %v", err)
	}
}

// Only script names, and nothing else. The column takes a string derived from
// the paragraph, so it is validated against the closed vocabulary it claims to
// hold — the lesson of the column that carried item text once (#42).
func TestTheAuditRecordRefusesAnythingButScriptNamesWhenOvergrown(t *testing.T) {
	for _, c := range []struct{ name, script string }{
		{"a paragraph", "The original sentence, which is what a leak looks like."},
		{"a word that is not a script", "Japanese"},
		{"the lowercase form", "han"},
		{"an empty name", ""},
		{"a name with a real one inside it", "Han script detected in 1979"},
		{"Common", "Common"},
		{"Inherited", "Inherited"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			// A rejection code that is ALREADY declared, so this test stays
			// sensitive to script validation rather than passing because
			// `language-growth` is not yet in the vocabulary.
			attempt := refusedForTells(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.OvergrownScripts = []string{c.script}

			if err := s.PutRewriteAttempt(ctx(), attempt); err == nil {
				t.Error("accepted")
			} else if !errors.Is(err, store.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// A script cannot overgrow twice. `Overgrown` ranges a map, so a duplicate did
// not come from it.
func TestTheAuditRecordRefusesARepeatedOvergrownScript(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := refusedForTells(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	attempt.OvergrownScripts = []string{"Han", "Han"}

	if err := s.PutRewriteAttempt(ctx(), attempt); err == nil {
		t.Error("accepted a script overgrown twice")
	} else if !errors.Is(err, store.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

// An accepted attempt grew nothing, because any overgrowth refuses.
func TestAnAcceptedAttemptGrewNothing(t *testing.T) {
	for _, c := range []struct {
		name     string
		accepted bool
		scripts  []string
		want     bool
	}{
		{"refused, naming what grew", false, []string{"Han"}, true},
		{"refused, naming none", false, nil, true},
		{"accepted, naming none", true, nil, true},
		{"accepted, yet naming a script", true, []string{"Han"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			nodeID := snapshot.Documents[0].Nodes[0].ID
			attempt := refusedForTells(prof.ID, nodeID)
			if c.accepted {
				attempt = acceptedAttempt(prof.ID, nodeID)
			}
			attempt.OvergrownScripts = c.scripts

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

// The record is immutable in this column too, and a reorder is a different
// decision rather than the same one written twice.
func TestRewritingAnAttemptWithDifferentOvergrownScriptsConflicts(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	first := refusedForGrowth(t, prof.ID, nodeID, "Han", "Cyrillic")
	if err := s.PutRewriteAttempt(ctx(), first); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	if err := s.PutRewriteAttempt(ctx(), first); err != nil {
		t.Errorf("rewriting the same decision: %v", err)
	}

	for _, c := range []struct {
		name    string
		scripts []string
	}{
		{"one dropped", []string{"Han"}},
		{"a different script", []string{"Han", "Greek"}},
		{"the same two reordered", []string{"Cyrillic", "Han"}},
		{"none at all", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			changed := first
			changed.OvergrownScripts = c.scripts
			if err := s.PutRewriteAttempt(ctx(), changed); !errors.Is(err, store.ErrConflict) {
				t.Errorf("err = %v, want ErrConflict", err)
			}
		})
	}
}

// The recorder carries the scripts across the seam.
//
// This is the hole #91's review found reachable: deleting one line from
// `recorder.RecordAttempt` passed the ENTIRE repository, because each half of
// the slice was tested against the other's fake. Nothing else drives a real
// `rewrite.Attempt` through the real recorder with this field set.
func TestTheRecorderCarriesTheOvergrownScriptsToTheStore(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	want := []string{"Han", "Cyrillic"}

	attempt := rewrite.Attempt{
		Index: 0, SpanRef: nodeID,
		CurrentHash:     identity.HashBytes([]byte("current")),
		CandidateHash:   identity.HashBytes([]byte("candidate")),
		CurrentDistance: 1.2, CandidateDistance: 0.4,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandInRange,
		Preserved: true, PreserveIdentifiers: nil,
		TellsComparison: -1, TellsComparable: true,
		OvergrownScripts: want,
		Accepted:         false, Rejection: rewrite.RejectionLanguageGrowth,
		ProfileID: prof.ID, ProviderID: string(llm.ProviderOllama),
		InvocationID: fakeID("invocation", "growth"),
	}
	if err := s.Recorder(ctx()).RecordAttempt(attempt); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}

	got, err := s.LoadRewriteAttempt(ctx(), attempt.InvocationID, nodeID, attempt.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if !reflect.DeepEqual(got.OvergrownScripts, want) {
		t.Errorf("read back %v, want %v", got.OvergrownScripts, want)
	}
	if got.Rejection != rewrite.RejectionLanguageGrowth || got.Accepted {
		t.Errorf("the record is %q/accepted=%v", got.Rejection, got.Accepted)
	}
}

// Damage to the column is corruption on READ, not only on write.
//
// The column cannot join `declaredVocabularies` — 163 values is not an enum a
// CHECK can hold — so this is one of the two places where an out-of-vocabulary
// value would otherwise be invisible to a reader.
func TestADamagedOvergrownScriptNameIsCorruptionOnRead(t *testing.T) {
	for _, damaged := range []string{"Japanese", "han", "Common", "not a script at all", ""} {
		t.Run(damaged, func(t *testing.T) {
			s := newStore(t)
			ids := seedEveryArtifact(t, s)

			relaxCheck(t, s, "rewrite_attempt_overgrown_script", "script")
			if _, err := openRaw(t, s).Exec(
				"UPDATE rewrite_attempt_overgrown_script SET script=? WHERE ordinal=0",
				damaged); err != nil {
				t.Fatalf("damaging rewrite_attempt_overgrown_script: %v", err)
			}

			_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, ids.AttemptNode, 0)
			if !errors.Is(err, store.ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}
		})
	}
}

// A duplicate on disk is corruption too, since the write rule rests on the same
// argument as the vocabulary rule and both need both halves.
func TestADuplicatedOvergrownScriptOnDiskIsCorruptionOnRead(t *testing.T) {
	s := newStore(t)
	ids := seedEveryArtifact(t, s)

	if _, err := openRaw(t, s).Exec(
		"UPDATE rewrite_attempt_overgrown_script SET script='Han'"); err != nil {
		t.Fatalf("damaging rewrite_attempt_overgrown_script: %v", err)
	}

	_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, ids.AttemptNode, 0)
	if !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// A gap in the ordinals is corruption, which is what makes ORDER BY ordinal
// load-bearing rather than incidental.
func TestDamagedOvergrownOrdinalsAreCorruptionOnRead(t *testing.T) {
	s := newStore(t)
	ids := seedEveryArtifact(t, s)

	if _, err := openRaw(t, s).Exec(
		"UPDATE rewrite_attempt_overgrown_script SET ordinal=7 WHERE ordinal=1"); err != nil {
		t.Fatalf("damaging ordinals: %v", err)
	}

	_, err := s.LoadRewriteAttempt(ctx(), ids.Invocation, ids.AttemptNode, 0)
	if !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// A repeated ordinal is unrepresentable, which is what the child table buys.
func TestTheSchemaRefusesARepeatedOvergrownOrdinal(t *testing.T) {
	s := newStore(t)
	seedEveryArtifact(t, s)

	if _, err := openRaw(t, s).Exec(
		"UPDATE rewrite_attempt_overgrown_script SET ordinal=0"); err == nil {
		t.Error("the schema accepted two scripts at the same ordinal")
	}
}

// The scripts go when the attempt goes.
//
// In the frozen set rather than left to the schema declaration, because the
// foreign-key map is iterated over itself: a table absent from it is never
// queried and its edges go unchecked. Dropping the sibling's FK passed the whole
// store package.
func TestTheOvergrownScriptsGoWhenTheAttemptGoes(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Fatalf("the fixture has %d nodes; this test needs two", len(nodes))
	}
	doomed := refusedForGrowth(t, prof.ID, nodes[0].ID, "Han", "Cyrillic")
	surviving := refusedForGrowth(t, prof.ID, nodes[1].ID, "Katakana")
	surviving.InvocationID = doomed.InvocationID
	surviving.CurrentHash = identity.HashBytes([]byte("the second paragraph"))
	for _, attempt := range []store.RewriteAttempt{doomed, surviving} {
		if err := s.PutRewriteAttempt(ctx(), attempt); err != nil {
			t.Fatalf("PutRewriteAttempt(%s): %v", attempt.NodeID, err)
		}
	}

	raw := openRaw(t, s)
	if _, err := raw.Exec("PRAGMA foreign_keys=1"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM node WHERE node_id=?", doomed.NodeID); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	var orphaned int
	if err := raw.QueryRow(
		"SELECT count(*) FROM rewrite_attempt_overgrown_script WHERE node_id=?",
		doomed.NodeID).Scan(&orphaned); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if orphaned != 0 {
		t.Errorf("%d rows outlived the attempt they belong to", orphaned)
	}

	got, err := s.LoadRewriteAttempt(ctx(),
		surviving.InvocationID, surviving.NodeID, surviving.Index)
	if err != nil {
		t.Fatalf("the surviving attempt went with it: %v", err)
	}
	if !reflect.DeepEqual(got.OvergrownScripts, []string{"Katakana"}) {
		t.Errorf("the surviving attempt records %v, want [Katakana]", got.OvergrownScripts)
	}
}

// A refused value is not echoed back in the error.
func TestARefusedOvergrownScriptNameIsNotEchoedBackInTheError(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := refusedForTells(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	secret := "the year 1979 in Warsaw"
	attempt.OvergrownScripts = []string{secret}

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

// An accepted attempt naming an overgrown script is corruption on READ too.
//
// Mirroring the sibling table copied its pattern and dropped this probe, and the
// omission was proven reachable: moving the accepted-implies-nothing-overgrown
// clause out of the shared validator into `PutRewriteAttempt` passed the whole
// store package. A row in that shape can only arrive underneath the store, and
// it says the gate was bypassed — which is exactly what a reader needs to see.
func TestAnAcceptedAttemptNamingAnOvergrownScriptIsCorruptionOnRead(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	accepted := acceptedAttempt(prof.ID, nodeID)
	if err := s.PutRewriteAttempt(ctx(), accepted); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}

	if _, err := openRaw(t, s).Exec(
		"INSERT INTO rewrite_attempt_overgrown_script "+
			"(invocation_id,node_id,attempt_index,ordinal,script) VALUES (?,?,?,0,'Han')",
		accepted.InvocationID, accepted.NodeID, accepted.Index); err != nil {
		t.Fatalf("damaging: %v", err)
	}

	_, err := s.LoadRewriteAttempt(ctx(), accepted.InvocationID, accepted.NodeID, accepted.Index)
	if !errors.Is(err, store.ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// The scripts belong to one attempt each.
//
// Two mutations survived without this: dropping `node_id` from the loader's
// WHERE clause, and dropping `invocation_id`. The cascade test seeds two nodes
// but DELETES one before loading the other, so it is structurally unable to
// observe the leak — a test that looks like it covers this and cannot.
//
// Three attempts: two nodes under one invocation, and a second invocation on the
// first node, so a loader missing either predicate reads back a union.
func TestOvergrownScriptsBelongToOneAttemptEach(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Skip("the seeded snapshot has one node")
	}

	first := refusedForGrowth(t, prof.ID, nodes[0].ID, "Han")
	second := refusedForGrowth(t, prof.ID, nodes[1].ID, "Cyrillic")
	second.InvocationID = first.InvocationID
	second.CurrentHash = identity.HashBytes([]byte("the second paragraph"))
	// Same node as `first`, different invocation.
	third := refusedForGrowth(t, prof.ID, nodes[0].ID, "Greek")
	third.InvocationID = fakeID("invocation", "second-run")
	third.CurrentHash = identity.HashBytes([]byte("a later run"))

	for _, want := range []store.RewriteAttempt{first, second, third} {
		if err := s.PutRewriteAttempt(ctx(), want); err != nil {
			t.Fatalf("PutRewriteAttempt(%s): %v", want.NodeID, err)
		}
	}

	for _, want := range []store.RewriteAttempt{first, second, third} {
		got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.NodeID, want.Index)
		if err != nil {
			t.Fatalf("LoadRewriteAttempt: %v", err)
		}
		if !reflect.DeepEqual(got.OvergrownScripts, want.OvergrownScripts) {
			t.Errorf("invocation %.8s node %.8s read back %v, want %v",
				want.InvocationID, want.NodeID, got.OvergrownScripts, want.OvergrownScripts)
		}
	}
}
