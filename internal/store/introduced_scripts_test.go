package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
)

// ---------------------------------------------------------------------------
// The scripts a refused candidate introduced
// ---------------------------------------------------------------------------
//
// #91 refuses a candidate that introduces a script the current text does not
// use, and records which scripts those were. The refusal discards the prose, so
// this column is the only durable trace that the substitution happened at all —
// an unpersisted one would leave a writer no way to learn that their paragraph
// came back in another script.
//
// It is safe to persist because it is not prose. A script name is one of the
// 163 keys of `unicode.Scripts`, chosen from a closed vocabulary rather than
// derived from the text, and the same name results from any paragraph written
// in that script. That is the same standing `PreserveIdentifiers` has, except
// that identifiers carry a digest of the item and these carry nothing at all.

// scriptsOf returns two real script names, taken from `unicode.Scripts` rather
// than written out here, so the fixtures cannot drift from the vocabulary the
// producing code uses.
func scriptsOf(t *testing.T, names ...string) []string {
	t.Helper()
	for _, name := range names {
		if _, ok := unicode.Scripts[name]; !ok {
			t.Fatalf("%q is not a script in this Go's unicode tables", name)
		}
	}
	return names
}

// refusedForTells is a coherent refusal whose rejection code is ALREADY
// declared, carrying scripts it also measured. Round 8 of #91's review
// established that the measurement survives losing the precedence contest, so
// this shape is real — and using it keeps the validation tests below sensitive
// to script validation alone, rather than passing because `language` is not yet
// in `rewrite.RejectionCodes()`.
func refusedForTells(profileID, nodeID string) store.RewriteAttempt {
	attempt := attemptFixture(profileID, nodeID)
	attempt.Preserved, attempt.PreserveIdentifiers = true, nil
	attempt.TellsComparison, attempt.TellsComparable = 1, true
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionTellsWorse
	return attempt
}

// refusedForLanguage is the shape #91 produces: every other guard passed, and
// the candidate was refused for the scripts it introduced.
func refusedForLanguage(t *testing.T, profileID, nodeID string, scripts ...string) store.RewriteAttempt {
	t.Helper()
	attempt := attemptFixture(profileID, nodeID)
	attempt.Preserved, attempt.PreserveIdentifiers = true, nil
	attempt.TellsComparison, attempt.TellsComparable = -1, true
	attempt.Accepted, attempt.Rejection = false, rewrite.RejectionLanguage
	attempt.IntroducedScripts = scriptsOf(t, scripts...)
	return attempt
}

// The scripts survive a write and a read, in the order they were recorded.
//
// Order is part of the record rather than incidental: `text.ScriptSet.Names`
// sorts, so a set read back in a different order did not come from the same
// measurement.
func TestTheIntroducedScriptsRoundTrip(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	want := refusedForLanguage(t, prof.ID, nodeID, "Cyrillic", "Han")

	if err := s.PutRewriteAttempt(ctx(), want); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.NodeID, want.Index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt: %v", err)
	}
	if !reflect.DeepEqual(got.IntroducedScripts, want.IntroducedScripts) {
		t.Errorf("read back %v, want %v", got.IntroducedScripts, want.IntroducedScripts)
	}
	if got.Rejection != rewrite.RejectionLanguage {
		t.Errorf("rejection = %q, want %q", got.Rejection, rewrite.RejectionLanguage)
	}
}

// Both forms of "none" are ordinary. Most attempts introduce nothing, and a
// store that distinguished nil from empty would make the common path depend on
// which one the loop happened to build.
func TestAnAttemptIntroducingNoScriptsStoresEitherWay(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID

	empty := acceptedAttempt(prof.ID, nodeID)
	empty.IntroducedScripts = []string{}
	if err := s.PutRewriteAttempt(ctx(), empty); err != nil {
		t.Fatalf("the empty form: %v", err)
	}
	nilled := acceptedAttempt(prof.ID, nodeID)
	nilled.IntroducedScripts = nil
	if err := s.PutRewriteAttempt(ctx(), nilled); err != nil {
		t.Errorf("rewriting the nil form: %v", err)
	}
}

// Only script names, and nothing else.
//
// This is the column's whole safety argument. `preserve_identifiers` is the
// column that carried item text once (#42), and the lesson generalizes: a
// column whose values are derived from the paragraph must be validated against
// the closed vocabulary it claims to hold, or prose reaches the database
// through it.
func TestTheAuditRecordRefusesAnythingButScriptNames(t *testing.T) {
	for _, c := range []struct {
		name   string
		script string
	}{
		{"a paragraph", "The original sentence, which is what a leak looks like."},
		{"a word that is not a script", "Japanese"},
		{"the lowercase form", "han"},
		{"an empty name", ""},
		{"a name with a real one inside it", "Han script detected in 1979"},
		// Common and Inherited are script TABLES but not scripts a text is
		// written in, and `text.Scripts` never attributes a letter to either.
		// A record naming one did not come from that function.
		{"Common", "Common"},
		{"Inherited", "Inherited"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			snapshot, prof := seededProfile(t, s)
			attempt := refusedForTells(prof.ID, snapshot.Documents[0].Nodes[0].ID)
			attempt.IntroducedScripts = []string{c.script}

			if err := s.PutRewriteAttempt(ctx(), attempt); err == nil {
				t.Error("accepted")
			} else if !errors.Is(err, store.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// An accepted attempt introduced nothing.
//
// The same shape as `TestPreservationAgreesWithWhatItNames`: the contradiction
// that matters has a direction. "Refused and naming scripts" is the ordinary
// record, "refused and naming none" is every other rejection, and "accepted and
// naming scripts" is impossible under a policy that refuses any introduction —
// so a record in that shape means the gate was bypassed, which is exactly what
// an audit trail exists to make visible.
func TestAnAcceptedAttemptIntroducedNothing(t *testing.T) {
	for _, c := range []struct {
		name     string
		accepted bool
		scripts  []string
		want     bool
	}{
		{"refused, naming what it found", false, []string{"Han"}, true},
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
			attempt.IntroducedScripts = c.scripts

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

// The record is immutable in this column too.
//
// `sameAttempt` decides whether a repeated write is the same decision or a
// conflict. A comparison that ignored the scripts would let a second write
// silently replace the evidence of which substitution happened, and report
// success.
func TestRewritingAnAttemptWithDifferentScriptsConflicts(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodeID := snapshot.Documents[0].Nodes[0].ID
	first := refusedForLanguage(t, prof.ID, nodeID, "Cyrillic", "Han")
	if err := s.PutRewriteAttempt(ctx(), first); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}

	// Identical is idempotent.
	if err := s.PutRewriteAttempt(ctx(), first); err != nil {
		t.Errorf("rewriting the same decision: %v", err)
	}

	for _, c := range []struct {
		name    string
		scripts []string
	}{
		{"one script dropped", []string{"Cyrillic"}},
		{"a different script", []string{"Cyrillic", "Greek"}},
		{"the same two reordered", []string{"Han", "Cyrillic"}},
		{"none at all", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			changed := first
			changed.IntroducedScripts = c.scripts
			if err := s.PutRewriteAttempt(ctx(), changed); !errors.Is(err, store.ErrConflict) {
				t.Errorf("err = %v, want ErrConflict", err)
			}
		})
	}
}

// A refused value is not echoed back in the error.
//
// The privacy invariant covers diagnostic output, and this column takes a
// string built from the paragraph. If a leak ever reaches it, the error message
// must not be the thing that publishes it — which is the lesson
// `TestARefusedIdentifierIsNotEchoedBackInTheError` already records for the
// column that leaked once.
func TestARefusedScriptNameIsNotEchoedBackInTheError(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	attempt := refusedForTells(prof.ID, snapshot.Documents[0].Nodes[0].ID)
	secret := "the year 1979 in Warsaw"
	attempt.IntroducedScripts = []string{secret}

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

// The scripts belong to one attempt each.
//
// `PreserveIdentifiers` is keyed by (invocation, node, index) because a list
// column keyed less specifically would pool rows across paragraphs. The same
// applies here, and the failure is quiet: both attempts read back the union.
func TestIntroducedScriptsBelongToOneAttemptEach(t *testing.T) {
	s := newStore(t)
	snapshot, prof := seededProfile(t, s)
	nodes := snapshot.Documents[0].Nodes
	if len(nodes) < 2 {
		t.Skip("the seeded snapshot has one node")
	}
	first := refusedForLanguage(t, prof.ID, nodes[0].ID, "Han")
	second := refusedForLanguage(t, prof.ID, nodes[1].ID, "Cyrillic")
	for _, want := range []store.RewriteAttempt{first, second} {
		if err := s.PutRewriteAttempt(ctx(), want); err != nil {
			t.Fatalf("PutRewriteAttempt(%s): %v", want.NodeID, err)
		}
	}

	for _, want := range []store.RewriteAttempt{first, second} {
		got, err := s.LoadRewriteAttempt(ctx(), want.InvocationID, want.NodeID, want.Index)
		if err != nil {
			t.Fatalf("LoadRewriteAttempt(%s): %v", want.NodeID, err)
		}
		if !reflect.DeepEqual(got.IntroducedScripts, want.IntroducedScripts) {
			t.Errorf("%s read back %v, want %v",
				want.NodeID, got.IntroducedScripts, want.IntroducedScripts)
		}
	}
}
