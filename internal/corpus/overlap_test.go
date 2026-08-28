package corpus_test

// Snapshot roles, and the author/distractor non-overlap screen.
//
// # Why this exists
//
// Issue #2's fallback makes `--distractors <dir>` the primary calibration path:
// a user assembles a distractor set from their own saved reading. That is
// better register-matched than anything shippable, and it introduces a failure
// mode the bundled-corpus design did not have — if the author's own writing
// lands in the distractor directory, author text is labelled "not the author",
// discrimination is driven toward chance, and the reported figure understates
// the tool. With a user pointing hapax at their own folders that is not a
// remote possibility; it is the obvious mistake.
//
// # This is NOT the contamination check
//
// `Snapshot.Contamination` means AI-text contamination screening at ingest,
// served by `tells` — DESIGN Section 1: "Fingerprinting a contaminated corpus
// faithfully reproduces the slop being removed". An earlier draft of this slice
// recorded the overlap outcome there. That was wrong twice over: it would have
// claimed a check that has not run, and "passed" on a general flag cannot mean
// "clean with respect to one particular author", which is the only thing an
// overlap screen establishes.
//
// The outcome is therefore a structured attestation naming BOTH snapshots, and
// a consumer must query it for the specific author it intends to use. A
// distractor set cleared against author A is not cleared for author B, and no
// boolean on the snapshot can express that.
//
// # What this screen does NOT do
//
// Exact content-hash equality only. The author's essay with one word changed
// passes. Near-duplicate detection is a separate check that remains
// unimplemented, and conflating the two would let a v1 exact-match screen
// masquerade as a contamination guarantee. TestNearDuplicatesSurviveTheScreen
// pins that as a recorded decision rather than an accident.
//
// # Pinned decisions
//
//  1. Role is required. The zero value is an error, not a silent default —
//     the same discipline `profile` applies to its paragraph floor.
//  2. Role is in the snapshot identity, so the same bytes ingested as author
//     and as distractor are different artifacts.
//  3. Check outcomes are NOT in the identity. Screening must not change the ID.
//     The discipline downstream is refuse, not re-key.
//  3a. ScreenOverlap MUTATES its receiver and is not safe for concurrent use on
//     the same snapshot, consistent with the rest of the package — Walk returns
//     a snapshot and nothing else in `corpus` mutates one. Recorded rather than
//     implied: if parallel calibration ever screens one distractor set against
//     several authors at once, that needs a guard and its own decision.
//  3b. Screening touches no existing check. Contamination, Language, Structure,
//     GitProvenance and NearDuplicateDetection are all left exactly as Walk
//     left them.
//  4. Ineligible documents are not screened: they never enter the feature
//     population, so overlap among them cannot corrupt a measurement.
//  5. Screening against an author snapshot with no eligible documents is an
//     error, and so is screening a distractor snapshot that has none. Screening
//     against nothing is not evidence of cleanliness, and neither is screening
//     nothing against something: an empty distractor directory would otherwise
//     pass vacuously and be handed a clearance.

import (
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
)

// policy() lives in corpus_test.go and now carries a role.

func authorPolicy() corpus.Policy {
	p := policy()
	p.Role = corpus.RoleAuthor
	return p
}

func distractorPolicy() corpus.Policy {
	p := policy()
	p.Role = corpus.RoleDistractor
	return p
}

// twoCorpora writes an author corpus and a distractor corpus into separate
// roots and returns both snapshots.
func twoCorpora(t *testing.T, author, distractor map[string]string) (*corpus.Snapshot, *corpus.Snapshot) {
	t.Helper()
	a := walk(t, write(t, author), authorPolicy())
	d := walk(t, write(t, distractor), distractorPolicy())
	return a, d
}

func screen(t *testing.T, distractor, author *corpus.Snapshot) corpus.OverlapReport {
	t.Helper()
	report, err := distractor.ScreenOverlap(author)
	if err != nil {
		t.Fatalf("ScreenOverlap: %v", err)
	}
	// Invariants that must hold on every screen, so no individual test has to
	// remember them: passed exactly when nothing is shared, both sides counted,
	// and the algorithm named exactly.
	if report.Algorithm != corpus.OverlapAlgorithm {
		t.Errorf("Algorithm = %q, want %q", report.Algorithm, corpus.OverlapAlgorithm)
	}
	if passed := report.State == corpus.CheckPassed; passed != (len(report.Shared) == 0) {
		t.Errorf("State = %q with %d shared documents; passed must mean exactly nothing shared", report.State, len(report.Shared))
	}
	if report.AuthorEligible <= 0 || report.DistractorEligible <= 0 {
		t.Errorf("eligible counts = %d author, %d distractor; both must be positive for the screen to mean anything",
			report.AuthorEligible, report.DistractorEligible)
	}
	return report
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

func TestRoleIsRequired(t *testing.T) {
	root := write(t, map[string]string{"a.md": prose("alpha", 40)})

	p := policy()
	p.Role = ""
	if _, err := corpus.Walk(root, p); err == nil {
		t.Error("Walk accepted a policy with no role; a corpus that does not know what it is for cannot be screened")
	}

	p.Role = corpus.Role("neither")
	if _, err := corpus.Walk(root, p); err == nil {
		t.Error("Walk accepted an unknown role")
	}

	for _, role := range []corpus.Role{corpus.RoleAuthor, corpus.RoleDistractor} {
		p.Role = role
		s, err := corpus.Walk(root, p)
		if err != nil {
			t.Fatalf("Walk(%q): %v", role, err)
		}
		if s.Policy.Role != role {
			t.Errorf("snapshot records role %q, want %q", s.Policy.Role, role)
		}
	}
}

// The same bytes ingested as author and as distractor are different artifacts.
// A consumer must not be able to reach for one and receive the other from a
// cache keyed on identity.
func TestRoleIsPartOfSnapshotIdentity(t *testing.T) {
	files := map[string]string{"a.md": prose("alpha", 40), "b.md": prose("beta", 40)}
	root := write(t, files)

	author := walk(t, root, authorPolicy())
	distractor := walk(t, root, distractorPolicy())

	if author.ID == distractor.ID {
		t.Error("the same bytes produced the same ID under different roles")
	}
	if got, ok := author.IdentityInputs()["role"]; !ok {
		t.Error("identity inputs do not include the role")
	} else if got != string(corpus.RoleAuthor) {
		t.Errorf("identity input role = %q, want %q", got, corpus.RoleAuthor)
	}
}

// ---------------------------------------------------------------------------
// The screen
// ---------------------------------------------------------------------------

func TestDisjointCorporaPass(t *testing.T) {
	author, distractor := twoCorpora(t,
		map[string]string{"mine1.md": prose("alpha", 40), "mine2.md": prose("beta", 40)},
		map[string]string{"theirs1.md": prose("gamma", 40), "theirs2.md": prose("delta", 40)},
	)

	report := screen(t, distractor, author)

	if len(report.Shared) != 0 {
		t.Errorf("disjoint corpora reported %d shared documents: %+v", len(report.Shared), report.Shared)
	}
	if report.State != corpus.CheckPassed {
		t.Errorf("State = %q, want %q", report.State, corpus.CheckPassed)
	}
	if report.Algorithm == "" {
		t.Error("no algorithm version recorded; a check that cannot say what ran is not provenance")
	}
	if report.AuthorSnapshotID != author.ID || report.DistractorSnapshotID != distractor.ID {
		t.Errorf("report names snapshots %q/%q, want %q/%q",
			report.AuthorSnapshotID, report.DistractorSnapshotID, author.ID, distractor.ID)
	}

	// The attestation is retrievable by the author it was taken against. This
	// is the machine-checkable provenance: a consumer asks "is this distractor
	// set cleared against THIS author", not "is this snapshot clean".
	stored, ok := distractor.OverlapScreen(author.ID)
	if !ok {
		t.Fatalf("no attestation stored for author %q", author.ID)
	}
	if !reflect.DeepEqual(stored, report) {
		t.Errorf("stored attestation %+v differs from the returned report %+v", stored, report)
	}
	if _, ok := distractor.OverlapScreen("some-other-author"); ok {
		t.Error("an attestation was returned for an author snapshot that was never screened against")
	}
	if report.AuthorEligible != 2 || report.DistractorEligible != 2 {
		t.Errorf("report counted %d author and %d distractor eligible documents, want 2 and 2",
			report.AuthorEligible, report.DistractorEligible)
	}
}

// The failure this whole check exists for.
func TestSharedDocumentFailsTheScreen(t *testing.T) {
	shared := prose("shared", 40)
	author, distractor := twoCorpora(t,
		map[string]string{"mine.md": prose("alpha", 40), "essay.md": shared},
		map[string]string{"theirs.md": prose("gamma", 40), "essay.md": shared},
	)

	report := screen(t, distractor, author)

	if len(report.Shared) != 1 {
		t.Fatalf("got %d shared documents, want 1: %+v", len(report.Shared), report.Shared)
	}
	got := report.Shared[0]
	if got.AuthorPath != "essay.md" || got.DistractorPath != "essay.md" {
		t.Errorf("shared document paths = %q/%q, want essay.md/essay.md", got.AuthorPath, got.DistractorPath)
	}
	if got.ContentHash == "" {
		t.Error("shared document carries no content hash")
	}
	if report.State != corpus.CheckFailed {
		t.Errorf("State = %q, want %q", report.State, corpus.CheckFailed)
	}
	if report.AuthorEligible != 2 || report.DistractorEligible != 2 {
		t.Errorf("report counted %d author and %d distractor eligible documents, want 2 and 2 — counts are reported on failure too",
			report.AuthorEligible, report.DistractorEligible)
	}
	stored, ok := distractor.OverlapScreen(author.ID)
	if !ok {
		t.Fatal("a failed screen stored no attestation; the failure is the thing worth recording")
	}
	if !reflect.DeepEqual(stored, report) {
		t.Errorf("stored attestation %+v differs from the returned report %+v", stored, report)
	}
}

// Overlap is by content, not by path. Renaming the file on the way into the
// distractor directory is exactly what a user reorganising their folders does.
func TestSameContentAtDifferentPathsIsCaught(t *testing.T) {
	shared := prose("shared", 40)
	author, distractor := twoCorpora(t,
		map[string]string{"drafts/2019-essay.md": shared},
		map[string]string{"reading/saved/untitled.md": shared},
	)

	report := screen(t, distractor, author)

	if len(report.Shared) != 1 {
		t.Fatalf("got %d shared documents, want 1", len(report.Shared))
	}
	if report.Shared[0].AuthorPath != "drafts/2019-essay.md" {
		t.Errorf("AuthorPath = %q", report.Shared[0].AuthorPath)
	}
	if report.Shared[0].DistractorPath != "reading/saved/untitled.md" {
		t.Errorf("DistractorPath = %q", report.Shared[0].DistractorPath)
	}
}

// Several shared documents come back in a stable order, so a report can be
// diffed between runs and a fix can be checked off against it.
func TestSharedDocumentsAreOrderedDeterministically(t *testing.T) {
	files := map[string]string{}
	for _, w := range []string{"one", "two", "three", "four", "five"} {
		files[w+".md"] = prose(w, 40)
	}
	author, distractor := twoCorpora(t, files, files)

	first := screen(t, distractor, author)
	if len(first.Shared) != 5 {
		t.Fatalf("got %d shared documents, want 5", len(first.Shared))
	}

	// Every entry is attributed, not merely counted and sorted: a report that
	// paired the right hashes with the wrong paths would be useless for fixing
	// the problem it reports.
	want := map[string]corpus.SharedDocument{}
	for name := range files {
		doc := mustDoc(t, author, name)
		want[doc.ContentHash] = corpus.SharedDocument{
			ContentHash:    doc.ContentHash,
			AuthorPath:     name,
			DistractorPath: name,
		}
	}
	for i, got := range first.Shared {
		expected, ok := want[got.ContentHash]
		if !ok {
			t.Errorf("shared document %d has hash %q, which is not in the author corpus", i, got.ContentHash)
			continue
		}
		if got != expected {
			t.Errorf("shared document %d = %+v, want %+v", i, got, expected)
		}
		delete(want, got.ContentHash)
	}
	for hash, missing := range want {
		t.Errorf("shared document %q (%s) was not reported", hash, missing.AuthorPath)
	}

	for i := 1; i < len(first.Shared); i++ {
		if first.Shared[i-1].ContentHash >= first.Shared[i].ContentHash {
			t.Errorf("shared documents are not in ascending content-hash order at %d: %q then %q",
				i, first.Shared[i-1].ContentHash, first.Shared[i].ContentHash)
		}
	}
}

// A document that never enters the feature population cannot contaminate a
// measurement, so it is not screened. Reporting it would train the user to
// ignore the report.
//
// The counterargument, recorded rather than dismissed: an ineligible shared
// document is still evidence of a sloppily assembled corpus, and it can become
// eligible if the admission policy changes. If that is ever worth surfacing it
// belongs as a non-blocking warning — it must not fail this screen, which would
// overstate contamination of the measurement actually being taken.
func TestIneligibleDocumentsAreNotScreened(t *testing.T) {
	tooShort := "three words only"
	author, distractor := twoCorpora(t,
		map[string]string{"real.md": prose("alpha", 40), "stub.md": tooShort},
		map[string]string{"theirs.md": prose("gamma", 40), "stub.md": tooShort},
	)

	// Guard: the fixture only means anything if the stub really is ineligible.
	if doc, ok := byPath(author, "stub.md"); !ok {
		t.Fatal("stub.md missing from the author snapshot")
	} else if doc.Admission == corpus.Eligible {
		t.Fatalf("stub.md is eligible in the author corpus; the fixture cannot test what it claims")
	}
	if doc, ok := byPath(distractor, "stub.md"); !ok {
		t.Fatal("stub.md missing from the distractor snapshot")
	} else if doc.Admission == corpus.Eligible {
		t.Fatalf("stub.md is eligible in the distractor corpus; the fixture relies on both sides being ineligible")
	}

	report := screen(t, distractor, author)
	if len(report.Shared) != 0 {
		t.Errorf("an ineligible shared document was reported: %+v", report.Shared)
	}
	if report.State != corpus.CheckPassed {
		t.Errorf("State = %q, want %q", report.State, corpus.CheckPassed)
	}
}

// The limitation, pinned so it is a decision on the record and not something
// discovered later by a user whose calibration was quietly wrong.
func TestNearDuplicatesSurviveTheScreen(t *testing.T) {
	original := prose("alpha", 40)
	altered := original + " coda"

	author, distractor := twoCorpora(t,
		map[string]string{"mine.md": original},
		map[string]string{"theirs.md": altered},
	)

	report := screen(t, distractor, author)
	if len(report.Shared) != 0 {
		t.Errorf("the exact-match screen reported a near duplicate: %+v", report.Shared)
	}
	if report.State != corpus.CheckPassed {
		t.Errorf("State = %q, want %q", report.State, corpus.CheckPassed)
	}
	// And the separate check that WOULD catch it must still say it has not run,
	// so a passed overlap screen cannot be mistaken for a contamination
	// guarantee.
	if distractor.NearDuplicateDetection.State != corpus.CheckNotPerformed {
		t.Errorf("NearDuplicateDetection.State = %q, want %q", distractor.NearDuplicateDetection.State, corpus.CheckNotPerformed)
	}
}

// ---------------------------------------------------------------------------
// What the screen must refuse
// ---------------------------------------------------------------------------

// A refused screen must leave the snapshot exactly as it was — every case, not
// just the nil one. A refusal that half-applied would be worse than no screen,
// because the snapshot would then carry an attestation nobody asked for.
func TestARefusedScreenLeavesTheSnapshotUntouched(t *testing.T) {
	author, distractor := twoCorpora(t,
		map[string]string{"mine.md": prose("alpha", 40)},
		map[string]string{"theirs.md": prose("gamma", 40)},
	)
	otherDistractor := walk(t, write(t, map[string]string{"more.md": prose("delta", 40)}), distractorPolicy())
	emptyAuthor := walk(t, write(t, map[string]string{"stub.md": "three words only"}), authorPolicy())

	emptyDistractor := walk(t, write(t, map[string]string{"stub.md": "three words only"}), distractorPolicy())

	type refusal struct {
		subject *corpus.Snapshot
		call    func() (corpus.OverlapReport, error)
	}
	for name, tc := range map[string]refusal{
		"nil author":           {distractor, func() (corpus.OverlapReport, error) { return distractor.ScreenOverlap(nil) }},
		"self screen":          {author, func() (corpus.OverlapReport, error) { return author.ScreenOverlap(author) }},
		"roles reversed":       {author, func() (corpus.OverlapReport, error) { return author.ScreenOverlap(distractor) }},
		"distractor as author": {distractor, func() (corpus.OverlapReport, error) { return distractor.ScreenOverlap(otherDistractor) }},
		"author with nothing eligible": {distractor, func() (corpus.OverlapReport, error) {
			return distractor.ScreenOverlap(emptyAuthor)
		}},
		// The symmetric case. An empty distractor directory shares nothing with
		// anything, so an unguarded screen calls it clean and hands out a
		// clearance for a set that contains no writing at all.
		"distractor with nothing eligible": {emptyDistractor, func() (corpus.OverlapReport, error) {
			return emptyDistractor.ScreenOverlap(author)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			subject, call := tc.subject, tc.call
			before := snapshotState(subject)

			report, err := call()
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if !reflect.DeepEqual(report, corpus.OverlapReport{}) {
				t.Errorf("%s returned a report alongside its error: %+v", name, report)
			}
			if got := snapshotState(subject); !reflect.DeepEqual(got, before) {
				t.Errorf("%s changed the snapshot:\n  before %+v\n  after  %+v", name, before, got)
			}
			// The consequence that matters: a refused screen must never leave
			// the subject able to claim non-overlap.
			if err := subject.NonOverlappingWith(author.ID); err == nil {
				t.Errorf("%s was refused, yet the snapshot reports non-overlap with the author", name)
			}
		})
	}
}

// snapshotState captures everything a screen could legitimately or
// illegitimately touch, so a test can assert nothing moved.
func snapshotState(s *corpus.Snapshot) map[string]any {
	return map[string]any{
		"id":                       s.ID,
		"identity":                 s.IdentityInputs(),
		"contamination":            s.Contamination,
		"language":                 s.Language,
		"structure":                s.Structure,
		"git-provenance":           s.GitProvenance,
		"near-duplicate-detection": s.NearDuplicateDetection,
		"overlap-screens":          s.OverlapScreens(),
	}
}

// The failure a boolean on the snapshot could never express: a distractor set
// screened clean against author A must not read as cleared for author B.
//
// This is why the outcome is a per-author attestation rather than a flag. Both
// results are kept, each attributable to the author it was taken against, and
// neither can stand in for the other.
func TestAttestationsArePerAuthorAndDoNotStandInForEachOther(t *testing.T) {
	shared := prose("shared", 40)

	authorA := walk(t, write(t, map[string]string{"a.md": prose("alpha", 40)}), authorPolicy())
	authorB := walk(t, write(t, map[string]string{"b.md": shared}), authorPolicy())
	distractor := walk(t, write(t, map[string]string{
		"theirs.md": prose("gamma", 40),
		"essay.md":  shared,
	}), distractorPolicy())

	if authorA.ID == authorB.ID {
		t.Fatal("the two author snapshots have the same ID; the fixture cannot discriminate")
	}

	cleanReport := screen(t, distractor, authorA)
	if cleanReport.State != corpus.CheckPassed {
		t.Fatalf("screen against author A = %q, want %q", cleanReport.State, corpus.CheckPassed)
	}

	dirtyReport := screen(t, distractor, authorB)
	if dirtyReport.State != corpus.CheckFailed {
		t.Fatalf("screen against author B = %q, want %q — B's document is in the distractor set", dirtyReport.State, corpus.CheckFailed)
	}

	// The earlier clean result survives, still attributable to A.
	gotA, ok := distractor.OverlapScreen(authorA.ID)
	if !ok {
		t.Fatal("the attestation for author A was lost when author B was screened")
	}
	if gotA.State != corpus.CheckPassed || gotA.AuthorSnapshotID != authorA.ID {
		t.Errorf("attestation for A = %+v, want a pass naming %q", gotA, authorA.ID)
	}

	gotB, ok := distractor.OverlapScreen(authorB.ID)
	if !ok {
		t.Fatal("no attestation stored for author B")
	}
	if gotB.State != corpus.CheckFailed || gotB.AuthorSnapshotID != authorB.ID {
		t.Errorf("attestation for B = %+v, want a failure naming %q", gotB, authorB.ID)
	}

	if len(distractor.OverlapScreens()) != 2 {
		t.Errorf("got %d attestations, want 2 — one per author screened against", len(distractor.OverlapScreens()))
	}

	// Enumeration is deterministic, so a report over several authors can be
	// diffed between runs.
	screens := distractor.OverlapScreens()
	for i := 1; i < len(screens); i++ {
		if screens[i-1].AuthorSnapshotID >= screens[i].AuthorSnapshotID {
			t.Errorf("attestations are not ordered by author snapshot ID at %d", i)
		}
	}
}

// Stored evidence must not be reachable through anything a caller can mutate.
// An attestation a consumer can edit is not evidence.
func TestStoredAttestationsAreNotMutableThroughTheAPI(t *testing.T) {
	shared := prose("shared", 40)
	author, distractor := twoCorpora(t,
		map[string]string{"essay.md": shared, "mine.md": prose("alpha", 40)},
		map[string]string{"essay.md": shared, "theirs.md": prose("gamma", 40)},
	)

	original := screen(t, distractor, author)

	got, ok := distractor.OverlapScreen(author.ID)
	if !ok {
		t.Fatal("no attestation stored")
	}
	got.State = corpus.CheckPassed
	got.AuthorSnapshotID = "tampered"
	if len(got.Shared) > 0 {
		got.Shared[0].AuthorPath = "tampered"
	}

	list := distractor.OverlapScreens()
	if len(list) > 0 {
		list[0].State = corpus.CheckPassed
		if len(list[0].Shared) > 0 {
			list[0].Shared[0].ContentHash = "tampered"
		}
	}

	after, ok := distractor.OverlapScreen(author.ID)
	if !ok {
		t.Fatal("the attestation disappeared after a caller edited its copy")
	}
	if !reflect.DeepEqual(after, original) {
		t.Errorf("stored attestation was changed through a returned copy:\n  %+v\n  %+v", original, after)
	}
}

// The consumer protocol, encoded once rather than reimplemented by every
// caller. The name says what it establishes and no more: exact-hash
// non-overlap with one author, NOT near-duplicate clearance and NOT the
// AI-contamination screening that Contamination refers to.
func TestNonOverlappingWithEncodesTheConsumerChecks(t *testing.T) {
	shared := prose("shared", 40)
	cleanAuthor := walk(t, write(t, map[string]string{"a.md": prose("alpha", 40)}), authorPolicy())
	dirtyAuthor := walk(t, write(t, map[string]string{"b.md": shared}), authorPolicy())
	distractor := walk(t, write(t, map[string]string{
		"theirs.md": prose("gamma", 40),
		"essay.md":  shared,
	}), distractorPolicy())

	t.Run("never screened", func(t *testing.T) {
		if err := distractor.NonOverlappingWith(cleanAuthor.ID); err == nil {
			t.Error("a snapshot that was never screened reported non-overlap")
		}
	})

	screen(t, distractor, cleanAuthor)
	screen(t, distractor, dirtyAuthor)

	t.Run("screened clean", func(t *testing.T) {
		if err := distractor.NonOverlappingWith(cleanAuthor.ID); err != nil {
			t.Errorf("a clean screen reported %v", err)
		}
	})

	t.Run("screened dirty", func(t *testing.T) {
		if err := distractor.NonOverlappingWith(dirtyAuthor.ID); err == nil {
			t.Error("a failed screen reported non-overlap")
		}
	})

	t.Run("a different author", func(t *testing.T) {
		other := walk(t, write(t, map[string]string{"c.md": prose("delta", 40)}), authorPolicy())
		if err := distractor.NonOverlappingWith(other.ID); err == nil {
			t.Error("an author never screened against reported non-overlap; a clearance for one author is not a clearance for another")
		}
	})
}

// ---------------------------------------------------------------------------
// What screening does and does not change
// ---------------------------------------------------------------------------

// The ID identifies membership and policy. Check outcomes are provenance, and
// running a check must not silently produce a different artifact — the
// discipline downstream is to refuse an unscreened snapshot, not to key a cache
// on two versions of it.
func TestScreeningDoesNotChangeTheSnapshotID(t *testing.T) {
	shared := prose("shared", 40)
	author, distractor := twoCorpora(t,
		map[string]string{"essay.md": shared},
		map[string]string{"essay.md": shared},
	)
	beforeID := distractor.ID
	beforeInputs := distractor.IdentityInputs()

	screen(t, distractor, author)

	if distractor.ID != beforeID {
		t.Errorf("screening changed the snapshot ID from %q to %q", beforeID, distractor.ID)
	}
	// The whole map, not one key: an outcome leaking into the identity inputs
	// without the ID being recomputed is the subtler and worse version of the
	// same bug.
	if !reflect.DeepEqual(distractor.IdentityInputs(), beforeInputs) {
		t.Errorf("screening changed the identity inputs:\n  %v\n  %v", beforeInputs, distractor.IdentityInputs())
	}
	for _, absent := range []string{"contamination", "checks", "overlap", "screened-against"} {
		if _, ok := distractor.IdentityInputs()[absent]; ok {
			t.Errorf("identity inputs include %q; check outcomes are provenance, not identity", absent)
		}
	}
}

// Screening twice is the same as screening once.
func TestScreeningIsIdempotent(t *testing.T) {
	shared := prose("shared", 40)
	author, distractor := twoCorpora(t,
		map[string]string{"essay.md": shared, "mine.md": prose("alpha", 40)},
		map[string]string{"essay.md": shared, "theirs.md": prose("gamma", 40)},
	)

	firstID := distractor.ID
	first := screen(t, distractor, author)
	firstScreens := distractor.OverlapScreens()
	second := screen(t, distractor, author)

	if got := distractor.OverlapScreens(); !reflect.DeepEqual(got, firstScreens) {
		t.Errorf("the second screen changed the stored attestations:\n  %+v\n  %+v", firstScreens, got)
	}
	if len(distractor.OverlapScreens()) != 1 {
		t.Errorf("screening the same author twice stored %d attestations, want 1", len(distractor.OverlapScreens()))
	}
	if distractor.ID != firstID {
		t.Errorf("the second screen changed the snapshot ID from %q to %q", firstID, distractor.ID)
	}
	// Whole reports, not cardinality: a second screen that kept the count but
	// changed the hashes, the paths, the order or the snapshot IDs it names is
	// not the same answer.
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two screens produced different reports:\n  %+v\n  %+v", first, second)
	}
}

// One passed check does not qualify a corpus. The other checks are still
// unimplemented, and a caller must not read a passed overlap screen as
// permission to proceed.
func TestPassingTheScreenDoesNotQualifyTheCorpus(t *testing.T) {
	author, distractor := twoCorpora(t,
		map[string]string{"mine.md": prose("alpha", 40)},
		map[string]string{"theirs.md": prose("gamma", 40)},
	)
	before := map[string]corpus.CheckStatus{
		"contamination":            distractor.Contamination,
		"language":                 distractor.Language,
		"structure":                distractor.Structure,
		"git-provenance":           distractor.GitProvenance,
		"near-duplicate-detection": distractor.NearDuplicateDetection,
	}

	screen(t, distractor, author)

	// Assert it PASSED first. Without this, an implementation that failed every
	// clean pair would satisfy everything below.
	stored, ok := distractor.OverlapScreen(author.ID)
	if !ok || stored.State != corpus.CheckPassed {
		t.Fatalf("expected a passed attestation on a disjoint pair, got %+v (present=%v)", stored, ok)
	}

	if !distractor.RequiresChecksBeforeUse() {
		t.Error("a passed overlap screen made the snapshot claim it needs no further checks")
	}

	// Whole CheckStatus values, not just states: a screen that rewrote another
	// check's reason or version while leaving it not-performed is still a screen
	// touching what it has no business touching. Contamination is in this list
	// deliberately — it means AI-text screening, which has not run.
	for name, got := range map[string]corpus.CheckStatus{
		"contamination":            distractor.Contamination,
		"language":                 distractor.Language,
		"structure":                distractor.Structure,
		"git-provenance":           distractor.GitProvenance,
		"near-duplicate-detection": distractor.NearDuplicateDetection,
	} {
		if got != before[name] {
			t.Errorf("the overlap screen changed the %s check from %+v to %+v; it screens overlap and nothing else", name, before[name], got)
		}
	}
}
