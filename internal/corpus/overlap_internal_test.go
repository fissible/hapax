package corpus

// Guards in NonOverlappingWith that the public producer cannot currently
// reach.
//
// ScreenOverlap is the only thing that stores an attestation today, and it
// always names its own receiver as the distractor side and always uses the
// supported algorithm — so from outside the package those two conditions can
// never fail, and a reviewer cannot tell whether they are enforced or merely
// written down. This seeds malformed attestations directly and proves each
// guard fires.
//
// The seam is deliberately small: storeOverlap is the one unexported hook this
// file relies on, and ScreenOverlap is expected to use it too.

import "testing"

func passing(distractorID, authorID string) OverlapReport {
	return OverlapReport{
		AuthorSnapshotID:     authorID,
		DistractorSnapshotID: distractorID,
		Algorithm:            OverlapAlgorithm,
		State:                CheckPassed,
		AuthorEligible:       3,
		DistractorEligible:   4,
	}
}

func TestNonOverlappingWithRejectsMalformedAttestations(t *testing.T) {
	const distractorID, authorID = "distractor-1", "author-1"

	// The control: a well-formed passing attestation is accepted, so every
	// rejection below is attributable to the one thing it changes.
	t.Run("well formed", func(t *testing.T) {
		s := &Snapshot{ID: distractorID}
		s.storeOverlap(passing(distractorID, authorID))
		if err := s.NonOverlappingWith(authorID); err != nil {
			t.Fatalf("a well-formed passing attestation was rejected: %v", err)
		}
	})

	for name, mutate := range map[string]func(*OverlapReport){
		// An attestation taken on a different distractor set says nothing
		// about this one, however clean it was.
		"names another distractor snapshot": func(r *OverlapReport) {
			r.DistractorSnapshotID = "some-other-distractor"
		},
		// An outcome from an algorithm this build does not know cannot be
		// interpreted, so it must not be treated as evidence.
		"unsupported algorithm": func(r *OverlapReport) {
			r.Algorithm = "overlap-from-the-future-v9"
		},
		"blank algorithm": func(r *OverlapReport) {
			r.Algorithm = ""
		},
		// State only. Adding a shared document here as well would confound this
		// with the inconsistency guard below: an implementation that ignored
		// State entirely but rejected a nonempty Shared would still pass.
		"failed state": func(r *OverlapReport) {
			r.State = CheckFailed
		},
		"not performed": func(r *OverlapReport) {
			r.State = CheckNotPerformed
		},
		// Only `passed` is evidence. The fourth state exists for checks a
		// policy disables, and a disabled check has established nothing.
		"skipped by policy": func(r *OverlapReport) {
			r.State = CheckSkippedByPolicy
		},
		// Internally inconsistent evidence: passed, yet naming a shared
		// document. Whichever half is wrong, it cannot be relied on.
		"passed while naming a shared document": func(r *OverlapReport) {
			r.Shared = []SharedDocument{{ContentHash: "h", AuthorPath: "a.md", DistractorPath: "b.md"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			report := passing(distractorID, authorID)
			mutate(&report)

			s := &Snapshot{ID: distractorID}
			s.storeOverlap(report)

			if err := s.NonOverlappingWith(authorID); err == nil {
				t.Errorf("an attestation that %s was accepted as evidence of non-overlap", name)
			}
		})
	}
}

// The identifier is a contract with anything that persists an attestation, so
// it is pinned as a literal. Comparing the report's algorithm to the constant
// proves only that they agree with each other.
func TestOverlapAlgorithmIdentifierIsPinned(t *testing.T) {
	if OverlapAlgorithm != "overlap-exact-hash-v1" {
		t.Errorf("OverlapAlgorithm = %q, want %q — the name states the method and its version, and changing the method must change the name",
			OverlapAlgorithm, "overlap-exact-hash-v1")
	}
}
