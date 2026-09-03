package workflow_test

// #81, at the layer that decides which paragraphs get rewritten.
//
// Plan today does two separable things and refuses if it cannot do both:
//
//	1. pick targets       — needs a band, so needs a calibrated release
//	2. qualify the draft   — needs a distance, so needs a profile and reference
//
// A corpus of fifty documents can do (2) and not (1). Plan refuses anyway, so
// the tool builds a profile and then declines to use it. Naming the paragraphs
// supplies (1) directly, and (2) is unaffected.
//
// # Indices are zero-based because `hapax score` prints them that way
//
// This was nearly frozen one-based. `hapax score` reports segment index 0 for
// the first paragraph, so a one-based flag would silently name the WRONG
// paragraph for anyone who read the score output and passed those numbers back
// — and still name a valid one, so nothing would report an error. Zero-based
// costs a moment's surprise; one-based costs a wrong rewrite nobody can see.
//
// # What an explicit target set must not become
//
// A licence to skip measurement. The distance still has to exist, the guards
// still run, and the claim the result makes has to say which question was
// actually answered. Under explicit selection the tool measured that a
// paragraph moved closer; it did NOT measure that the paragraph was drifting,
// because that is the band's job and no band was consulted. So the claim is
// "closer-by-distance" whether or not a release happens to exist — a calibrated
// store does not upgrade a claim about a paragraph the user chose by hand.

import (
	"testing"

	"github.com/fissible/hapax/internal/workflow"
)

// explicitRequest is planRequest with a named target set.
func explicitRequest(root, path string, paragraphs ...int) workflow.RewriteRequest {
	request := planRequest(root, path)
	request.Paragraphs = paragraphs
	return request
}

// targetIndices reports the segment indices a plan actually chose to rewrite.
func targetIndices(plan workflow.RewritePlan) []int {
	var out []int
	for _, segment := range plan.Segments {
		if segment.Disposition == workflow.DispositionTarget {
			out = append(out, segment.Index)
		}
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// The vocabulary
// ---------------------------------------------------------------------------

// Both axes are closed sets, so the CLI can render them and the store can
// check them without either inventing a member.
func TestTargetingAndClaimAreClosedSets(t *testing.T) {
	t.Parallel()
	// Membership and closure, not order. What matters is that the set is
	// exactly these two — a third member nobody declared is what a renderer or
	// a schema check would then have to guess about.
	wantTargeting := []workflow.Targeting{workflow.TargetingAutomatic, workflow.TargetingExplicit}
	if got := workflow.Targetings(); !sameSet(toStrings(got), toStrings(wantTargeting)) {
		t.Errorf("Targetings() = %v, want exactly %v in any order", got, wantTargeting)
	}
	wantClaim := []workflow.Claim{workflow.ClaimCalibratedBand, workflow.ClaimCloserByDistance}
	if got := workflow.Claims(); !sameSet(toStrings(got), toStrings(wantClaim)) {
		t.Errorf("Claims() = %v, want exactly %v in any order", got, wantClaim)
	}
	// A paragraph the user did not name has a disposition of its own. Reusing
	// in-range would assert a band nobody measured.
	if !containsDisposition(workflow.Dispositions(), workflow.DispositionNotSelected) {
		t.Errorf("Dispositions() = %v, missing %q", workflow.Dispositions(), workflow.DispositionNotSelected)
	}
	if !containsString(workflow.Refusals(), workflow.RefusalNoSuchParagraph) {
		t.Errorf("Refusals() is missing %q", workflow.RefusalNoSuchParagraph)
	}
}

func toStrings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = string(x)
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func containsDisposition(haystack []workflow.Disposition, needle workflow.Disposition) bool {
	for _, x := range haystack {
		if x == needle {
			return true
		}
	}
	return false
}

func containsString(haystack []string, needle string) bool {
	for _, x := range haystack {
		if x == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Automatic targeting is untouched
// ---------------------------------------------------------------------------

// No named paragraphs, a calibrated store: the band picks the targets and the
// claim says so. This is the path that exists today and it must not move.
func TestAutomaticTargetingIsStillBandDriven(t *testing.T) {
	t.Parallel()
	root := bandedStore(t, "drifting")
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, planRequest(root, draft))

	if plan.Refusal != "" {
		t.Fatalf("refusal = %q on a calibrated store", plan.Refusal)
	}
	if plan.Targeting != workflow.TargetingAutomatic {
		t.Errorf("targeting = %q, want %q", plan.Targeting, workflow.TargetingAutomatic)
	}
	if plan.Claim != workflow.ClaimCalibratedBand {
		t.Errorf("claim = %q, want %q", plan.Claim, workflow.ClaimCalibratedBand)
	}
	if !plan.CalibrationAvailable {
		t.Error("a calibrated store reported no calibration available")
	}
	// Every drifting paragraph is a target, which is what makes this the
	// band-driven path rather than a path that happens to agree with it.
	if plan.Targets != len(plan.Segments) || len(plan.Segments) == 0 {
		t.Errorf("%d targets over %d segments; the fixture bands every paragraph drifting",
			plan.Targets, len(plan.Segments))
	}
	if containsDispositionIn(plan, workflow.DispositionNotSelected) {
		t.Error("automatic targeting produced a not-selected segment")
	}
}

func containsDispositionIn(plan workflow.RewritePlan, want workflow.Disposition) bool {
	for _, segment := range plan.Segments {
		if segment.Disposition == want {
			return true
		}
	}
	return false
}

// And with no named paragraphs an uncalibrated store still refuses. Nothing in
// this slice makes the weaker claim reachable without asking for it.
func TestAutomaticTargetingStillRefusesAnUncalibratedStore(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, planRequest(root, draft))

	if plan.Refusal != workflow.RefusalUncalibrated {
		t.Fatalf("refusal = %q, want %q", plan.Refusal, workflow.RefusalUncalibrated)
	}
	if plan.Targets != 0 {
		t.Errorf("%d targets under a refusal", plan.Targets)
	}
	if plan.Claim == workflow.ClaimCloserByDistance {
		t.Error("a refusal claimed closer-by-distance")
	}
}

// ---------------------------------------------------------------------------
// What naming paragraphs buys
// ---------------------------------------------------------------------------

// The headline: an uncalibrated store plans a rewrite when the user says which
// paragraphs. This is the case #81 exists for and the one a real fifty-document
// corpus is in.
func TestNamedParagraphsPlanARewriteOnAnUncalibratedStore(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, explicitRequest(root, draft, 0))

	if plan.Refusal != "" {
		t.Fatalf("refusal = %q; naming a paragraph must lift the calibration gate", plan.Refusal)
	}
	if plan.State != workflow.StateTargetsPlanned {
		t.Fatalf("state = %q, want %q", plan.State, workflow.StateTargetsPlanned)
	}
	if plan.Targets != 1 {
		t.Errorf("targets = %d, want 1", plan.Targets)
	}
	if plan.Targeting != workflow.TargetingExplicit {
		t.Errorf("targeting = %q, want %q", plan.Targeting, workflow.TargetingExplicit)
	}
	if plan.Claim != workflow.ClaimCloserByDistance {
		t.Errorf("claim = %q, want %q", plan.Claim, workflow.ClaimCloserByDistance)
	}
	if plan.CalibrationAvailable {
		t.Error("an uncalibrated store reported calibration available")
	}
	// The measurement is still real: the planned target carries a defined
	// distance even though it carries no band.
	for _, segment := range plan.Segments {
		if segment.Disposition != workflow.DispositionTarget {
			continue
		}
		if segment.Band.Defined {
			t.Errorf("segment %d has a defined band on an uncalibrated store", segment.Index)
		}
	}
	assertSegmentsResolveIntoTheDraftSnapshot(t, root, draft, plan)
}

// Exactly what was named, and nothing else. An implementation that read the
// flag and then targeted everything would satisfy a test that only counted
// targets, so the SET is asserted, and a paragraph left out is asserted to
// carry a disposition that says it was not chosen rather than one that claims
// a band nobody measured.
func TestNamedParagraphsTargetOnlyWhatWasNamed(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)
	all := planned(t, explicitRequest(root, draft, 0, 1))
	if len(all.Segments) != 2 {
		t.Fatalf("the fixture produced %d admitted paragraphs, want 2", len(all.Segments))
	}

	plan := planned(t, explicitRequest(root, draft, 1))

	if got := targetIndices(plan); !sameInts(got, []int{1}) {
		t.Fatalf("targets = %v, want exactly [1]", got)
	}
	if plan.Targets != 1 {
		t.Errorf("Targets = %d, want 1", plan.Targets)
	}
	for _, segment := range plan.Segments {
		if segment.Index == 1 {
			continue
		}
		if segment.Disposition != workflow.DispositionNotSelected {
			t.Errorf("unnamed segment %d has disposition %q, want %q",
				segment.Index, segment.Disposition, workflow.DispositionNotSelected)
		}
	}
}

// Order is the caller's convenience, not a contract. Naming them backwards
// selects the same set.
func TestNamedParagraphsMayBeGivenInAnyOrder(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	forward := planned(t, explicitRequest(root, draft, 0, 1))
	backward := planned(t, explicitRequest(root, draft, 1, 0))

	if !sameInts(targetIndices(forward), targetIndices(backward)) {
		t.Errorf("order changed the target set: %v vs %v",
			targetIndices(forward), targetIndices(backward))
	}
	if forward.Targets != 2 {
		t.Errorf("targets = %d, want 2", forward.Targets)
	}
}

// On a calibrated store the user's choice still wins, and the claim does NOT
// upgrade. The band was not consulted, so nothing measured that these
// paragraphs were drifting — only that a rewrite moved them closer.
//
// The fixture bands every paragraph in-range, so band-driven selection would
// target nothing at all. A plan with one target here can only have come from
// the named set.
func TestNamedParagraphsWinOnACalibratedStoreWithoutUpgradingTheClaim(t *testing.T) {
	t.Parallel()
	root := bandedStore(t, "in-range")
	draft := writeDraft(t, root, twoParagraphs)

	automatic := planned(t, planRequest(root, draft))
	if automatic.Targets != 0 {
		t.Fatalf("band-driven selection targeted %d paragraphs; the fixture bands them all in-range",
			automatic.Targets)
	}

	plan := planned(t, explicitRequest(root, draft, 0))

	if got := targetIndices(plan); !sameInts(got, []int{0}) {
		t.Fatalf("targets = %v, want exactly [0]; the named paragraph was overridden by its band", got)
	}
	if plan.Targeting != workflow.TargetingExplicit {
		t.Errorf("targeting = %q, want %q", plan.Targeting, workflow.TargetingExplicit)
	}
	if plan.Claim != workflow.ClaimCloserByDistance {
		t.Errorf("claim = %q, want %q; a release does not turn a hand-picked paragraph into a banded one",
			plan.Claim, workflow.ClaimCloserByDistance)
	}
	// And the fact that a release existed is still reported, separately, so a
	// reader can tell this store from the uncalibrated one.
	if !plan.CalibrationAvailable {
		t.Error("a calibrated store reported no calibration available")
	}
}

// ---------------------------------------------------------------------------
// What naming paragraphs must not buy
// ---------------------------------------------------------------------------

// A paragraph that cannot be spliced is not rewritten because it was named.
// The excision rule protects what the user wrote; nothing in an explicit
// selection overrides it.
func TestANamedParagraphWithExcisionsIsStillNotATarget(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, excisionDraft)
	leaves := admittedLeaves(t, draft, storedFloor(t, root))

	excised := -1
	for i, leaf := range leaves {
		if leaf.HasExcisions {
			excised = i
			break
		}
	}
	if excised < 0 {
		t.Fatal("the excision fixture produced no paragraph with excisions")
	}

	plan := planned(t, explicitRequest(root, draft, excised))

	for _, segment := range plan.Segments {
		if segment.Index != excised {
			continue
		}
		if segment.Disposition != workflow.DispositionContainsExcisions {
			t.Errorf("named excised segment %d has disposition %q, want %q",
				segment.Index, segment.Disposition, workflow.DispositionContainsExcisions)
		}
	}
	if plan.Targets != 0 {
		t.Errorf("targets = %d; a named paragraph with excisions was planned for rewriting", plan.Targets)
	}
	// Naming only unrewritable paragraphs leaves nothing to do, and the plan
	// says that rather than pretending it will act.
	if plan.State != workflow.StateNothingToChange {
		t.Errorf("state = %q, want %q", plan.State, workflow.StateNothingToChange)
	}
}

// Naming a paragraph that does not exist is the user's mistake and is reported
// as one, with a reason a script can read. It cannot be caught at parse time,
// because the paragraph count is not known until the draft is scored.
func TestNamingAParagraphThatDoesNotExistIsRefused(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	for _, index := range []int{2, 99} {
		plan := planned(t, explicitRequest(root, draft, index))
		if plan.Refusal != workflow.RefusalNoSuchParagraph {
			t.Errorf("naming paragraph %d: refusal = %q, want %q",
				index, plan.Refusal, workflow.RefusalNoSuchParagraph)
		}
		if plan.Targets != 0 {
			t.Errorf("naming paragraph %d planned %d targets under a refusal", index, plan.Targets)
		}
	}
}

// One bad index poisons the whole invocation rather than being dropped. A run
// that silently rewrote paragraph 0 because paragraph 9 did not exist would be
// doing something the user did not ask for.
func TestOneBadIndexRefusesTheWholeSelection(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, explicitRequest(root, draft, 0, 9))

	if plan.Refusal != workflow.RefusalNoSuchParagraph {
		t.Fatalf("refusal = %q, want %q", plan.Refusal, workflow.RefusalNoSuchParagraph)
	}
	if plan.Targets != 0 {
		t.Errorf("%d targets planned alongside an index that does not exist", plan.Targets)
	}
}

// A named paragraph on a store with no profile at all is still a no-profile
// refusal. The selection does not reach past the gates that precede it.
func TestNamedParagraphsDoNotBypassTheEarlierGates(t *testing.T) {
	t.Parallel()
	root := profileOnlyStore(t)
	draft := writeDraft(t, root, twoParagraphs)

	plan := planned(t, explicitRequest(root, draft, 0))

	if plan.Refusal != workflow.RefusalNoReference {
		t.Fatalf("refusal = %q, want %q; a profile without a reference cannot measure",
			plan.Refusal, workflow.RefusalNoReference)
	}
	if plan.Targets != 0 {
		t.Errorf("%d targets planned without a reference to measure against", plan.Targets)
	}
}

// ---------------------------------------------------------------------------
// What the index counts
// ---------------------------------------------------------------------------

// The indices count ADMITTED paragraphs, which is what `hapax score` numbers —
// not leaves of the document.
//
// interleavedDraft exists for exactly this confusion: it puts a heading and a
// code block between the prose, so leaf ordinal and segment index diverge. An
// implementation that took `--paragraphs 1` to mean "the second leaf" would
// pass every other test in this file, because every other fixture here is prose
// all the way down and the two numberings coincide.
func TestNamedParagraphsCountAdmittedParagraphsNotLeaves(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, interleavedDraft)

	all := planned(t, explicitRequest(root, draft, 0, 1, 2))
	if len(all.Segments) != 3 {
		t.Fatalf("the interleaved fixture produced %d segments, want 3 admitted paragraphs",
			len(all.Segments))
	}
	// The fixture's whole point: the admitted paragraphs are not the first three
	// leaves, so the two numberings cannot silently agree.
	leaves := admittedLeaves(t, draft, storedFloor(t, root))
	if len(leaves) != 3 {
		t.Fatalf("admitted %d leaves, want 3", len(leaves))
	}

	plan := planned(t, explicitRequest(root, draft, 1))

	if got := targetIndices(plan); !sameInts(got, []int{1}) {
		t.Fatalf("targets = %v, want exactly [1]", got)
	}
	// And the target's span is the SECOND admitted paragraph's span, which is
	// the assertion a leaf-ordinal implementation fails: it would resolve to the
	// first admitted paragraph instead.
	var target workflow.PlannedSegment
	for _, segment := range plan.Segments {
		if segment.Disposition == workflow.DispositionTarget {
			target = segment
		}
	}
	if target.Offset != leaves[1].Offset || target.Length != leaves[1].Length {
		t.Errorf("target span is offset %d length %d, want the second admitted paragraph at offset %d length %d",
			target.Offset, target.Length, leaves[1].Offset, leaves[1].Length)
	}
	assertSegmentsResolveIntoTheDraftSnapshot(t, root, draft, plan)
}

// ---------------------------------------------------------------------------
// Disposition precedence
// ---------------------------------------------------------------------------

// Selection is decided FIRST. A paragraph the user did not name is
// not-selected, whatever else is true of it — it may contain excisions, it may
// be unmeasurable, and neither is the reason it was left alone.
//
// The alternative, reporting excisions on a paragraph nobody asked about, tells
// a user their inline code blocked something they never requested. The
// disposition answers "why was this not rewritten?", and for an unnamed
// paragraph the whole answer is "you did not name it".
func TestUnnamedParagraphsAreNotSelectedWhateverElseIsTrueOfThem(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, excisionDraft)
	leaves := admittedLeaves(t, draft, storedFloor(t, root))

	excised, ordinary := -1, -1
	for i, leaf := range leaves {
		if leaf.HasExcisions && excised < 0 {
			excised = i
		}
		if !leaf.HasExcisions && ordinary < 0 {
			ordinary = i
		}
	}
	if excised < 0 || ordinary < 0 {
		t.Fatalf("the excision fixture needs one paragraph of each kind (excised=%d ordinary=%d)",
			excised, ordinary)
	}

	// Name only the ordinary one. The excised paragraph is now unnamed.
	plan := planned(t, explicitRequest(root, draft, ordinary))

	for _, segment := range plan.Segments {
		switch segment.Index {
		case ordinary:
			if segment.Disposition != workflow.DispositionTarget {
				t.Errorf("the named ordinary segment %d has disposition %q, want %q",
					segment.Index, segment.Disposition, workflow.DispositionTarget)
			}
		case excised:
			if segment.Disposition != workflow.DispositionNotSelected {
				t.Errorf("the UNNAMED excised segment %d has disposition %q, want %q; "+
					"excisions are not why an unnamed paragraph was left alone",
					segment.Index, segment.Disposition, workflow.DispositionNotSelected)
			}
		default:
			if segment.Disposition != workflow.DispositionNotSelected {
				t.Errorf("unnamed segment %d has disposition %q, want %q",
					segment.Index, segment.Disposition, workflow.DispositionNotSelected)
			}
		}
	}
	if plan.Targets != 1 {
		t.Errorf("targets = %d, want 1", plan.Targets)
	}
}

// And among the paragraphs that WERE named, the existing precedence still
// applies: excisions beat targeting, because splicing over them would delete
// what the user wrote. Naming a paragraph asks for a rewrite; it does not
// authorise damage.
func TestAmongNamedParagraphsExcisionsStillWin(t *testing.T) {
	t.Parallel()
	root := uncalibratedStore(t)
	draft := writeDraft(t, root, excisionDraft)
	leaves := admittedLeaves(t, draft, storedFloor(t, root))

	var named []int
	excised := -1
	for i, leaf := range leaves {
		named = append(named, i)
		if leaf.HasExcisions && excised < 0 {
			excised = i
		}
	}
	if excised < 0 {
		t.Fatal("the excision fixture produced no paragraph with excisions")
	}

	plan := planned(t, explicitRequest(root, draft, named...))

	for _, segment := range plan.Segments {
		if segment.Index != excised {
			continue
		}
		if segment.Disposition != workflow.DispositionContainsExcisions {
			t.Errorf("the named excised segment %d has disposition %q, want %q",
				segment.Index, segment.Disposition, workflow.DispositionContainsExcisions)
		}
	}
	if plan.Targets != len(named)-1 {
		t.Errorf("targets = %d over %d named paragraphs, want %d; the excised one is not a target",
			plan.Targets, len(named), len(named)-1)
	}
	if containsDispositionIn(plan, workflow.DispositionNotSelected) {
		t.Error("a paragraph that was named is reported not-selected")
	}
}
