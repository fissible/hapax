package profile_test

import (
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
)

// A profile is built against the corpus snapshot's identity and persisted
// against the graph's, which are not the same value. Rebinding is therefore
// something a composition root has to do — and it must be done HERE, because
// recomputing a profile's ID outside the package that defines what a profile's
// ID is made of will diverge silently the first time another input joins it.
// The divergence it works around is recorded separately.
func TestRebindingASnapshotChangesTheIdentityAndNothingElse(t *testing.T) {
	// Distinct content: identical files are rejected as duplicates and would
	// leave too little to fit a profile over.
	root, snapshot := corpusOf(t, map[string]string{
		"a.md": paragraph + "\nThe first one also says this.\n",
		"b.md": paragraph + "\nThe second one says something else.\n",
		"c.md": paragraph + "\nThe third one differs again.\n",
	})
	requirements := profile.DefaultRequirements()
	requirements.MinDocuments, requirements.MinParagraphs, requirements.MinObservationsPerFeature = 1, 1, 1

	built, err := profile.Build(root, snapshot, requirements)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	before := deepCopy(*built)
	const persisted = "0f4a2c9b8e7d6a5b4c3d2e1f00112233445566778899aabbccddeeff00112233"
	if before.SnapshotID == persisted {
		t.Fatal("the fixture already carries the identity being rebound to")
	}

	built.RebindSnapshot(persisted)

	if built.SnapshotID != persisted {
		t.Errorf("snapshot id = %q, want %q", built.SnapshotID, persisted)
	}
	if built.ID == before.ID {
		t.Error("the identity did not move with the snapshot it is derived from")
	}
	// What the new identity IS, stated by the rule this package publishes. A
	// caller reproducing this arithmetic is the thing RebindSnapshot exists to
	// stop; a contract test for the method stating it is how the published
	// behaviour gets specified at all.
	wantID := identity.HashInputs(built.IdentityInputs())
	if built.ID != wantID {
		t.Errorf("identity = %q, want %q for the snapshot it was rebound to", built.ID, wantID)
	}

	// Nothing else moved, compared as whole values rather than as a list of
	// fields a later one could be added outside. The identity itself is taken
	// from the result, because what is being asserted here is that it is the
	// ONLY thing that changed; what it should be is asserted below.
	want := deepCopy(before)
	want.SnapshotID, want.ID = persisted, built.ID
	if !reflect.DeepEqual(*built, want) {
		t.Errorf("rebinding changed more than the identity:\n before %+v\n after  %+v", before, *built)
	}

	// And it is recomputed by the package's own rule rather than by arithmetic a
	// caller could reproduce differently: rebinding back to the snapshot it was
	// built against must restore exactly the identity Build gave it. Asserting
	// the formula by reproducing it in the test would only prove the test and
	// the code agree, which is what a caller doing its own hashing also proves.
	built.RebindSnapshot(before.SnapshotID)
	if built.ID != before.ID {
		t.Errorf("rebound to its original snapshot the identity is %q, and Build made it %q",
			built.ID, before.ID)
	}
	if !reflect.DeepEqual(*built, before) {
		t.Error("a round trip through another snapshot did not return the profile it started as")
	}
}

// deepCopy so a mutation inside Stats cannot hide behind shared backing storage
// and read as "nothing changed".
func deepCopy(p profile.Profile) profile.Profile {
	out := p
	out.Stats = append([]profile.Stats(nil), p.Stats...)
	return out
}
