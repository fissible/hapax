package score_test

// `score` measures a draft against a profile.
//
// Per paragraph it emits a calibrated band, the distance behind it, and the
// per-feature deltas with their direction — or insufficient evidence. Almost all
// of that is assembly: the deviation, the distance, the two release gates and
// the release verdict already exist. What this package adds is the orchestration
// and the report.
//
// # One report shape, not two
//
// ADR 0005 says an uncalibrated profile still emits the raw distance and the
// per-feature deltas, and withholds only the band. That is not a second format.
// The band carries its own definedness and reason, exactly as a distance and a
// deviation do, so a reader asks whether the band is defined rather than
// discovering a field that is sometimes absent.
//
// # Paragraph admission is the profile's, floor included
//
// A draft is split by the same shared path the profile was fitted with, under
// THE PROFILE'S OWN lexical-token floor. There is deliberately no scoring-level
// floor: a caller given one could measure a draft under a different admission
// rule than the profile was fitted with, which is exactly the error the shared
// path exists to prevent and which would not show up in the output.
//
// # A draft is not corpus material
//
// Every standardized segment records its split, and before this slice only
// train, calibrate and test were nameable. A draft is none of them. Without a
// fourth value a draft would have to claim one, and the only survivable lie is
// `test` — the split both release gates draw their evidence from. `draft` means
// scored, never fitted, never evidence: the corpus never assigns it, a reference
// admits only calibrate, and both gates require test.
//
// # Tier A only, because that is what the manifest declares
//
// ADR 0003 gives score Tier A at paragraph scale and Tier B over rolling
// windows. Tier B has no features and the window is not built, so every score is
// a paragraph-scale Tier A score until it is.
//
// # A segment carries the artifacts, not copies of their fields
//
// Segment.Distance is the deviation.Distance itself and Segment.Band is the
// eval.BandOutcome itself, so each carries its own definedness, its own reason,
// and — for the distance — the contributing feature set, the scored tiers, the
// partial flag and the split it was measured under. Flattening those into
// parallel fields would duplicate state that can disagree, and would hide from
// `rewrite` the contributing set it needs in order to refuse an incomparable
// pair under ADR 0006.
//
// It also makes the split observable. A score that measured a draft as `test`
// internally and labelled only the outer report `draft` would be invisible
// otherwise; here the distance carries the split it was actually standardized
// under.

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/score"
	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// A profile, a reference and a release to score against
// ---------------------------------------------------------------------------

const testProfileID = "profile-under-test"

func testProfile() *profile.Profile {
	stats := make([]profile.Stats, 0, len(features.Definitions()))
	for _, definition := range features.Definitions() {
		stats = append(stats, profile.Stats{
			Feature: definition.ID, N: 50, Mean: 1, Variance: 1,
			Defined: true, VarianceDefined: true, MinObservations: 20,
		})
	}
	return &profile.Profile{
		ID: testProfileID, SnapshotID: "snapshot-under-test",
		Split: corpus.Train, Unit: profile.UnitParagraph,
		FeatureSetVersion: features.SetVersion, FeatureManifestDigest: features.ManifestDigest(),
		VarianceConvention: profile.SampleVariance, Stats: stats,
		Requirements: profile.Requirements{MinParagraphLexicalTokens: 5},
	}
}

// calibrateProse is deliberately varied so the reference has spread; a
// degenerate reference would make every deviation land on the same rank.
var calibrateProse = []string{
	"The argument rests on a distinction that the record does not support, and the record is all we have.",
	"It is not that the claim is false; it is that nothing in the material would tell us either way.",
	"Every reading of the passage runs into the same wall, which is that the author never says it.",
	"We can grant the premise and still find that the conclusion does not follow from it at all.",
	"There is a version of this argument that works, but it is not the one on the page here.",
	"A reader who wanted the stronger claim would have to supply the missing step themselves.",
	"The objection is fair, and it does not touch the part of the argument that matters most.",
	"Nothing in the surrounding paragraphs settles which of the two readings was intended.",
}

func testReference(t *testing.T, prof *profile.Profile) *deviation.Reference {
	t.Helper()
	segments := make([]deviation.Standardization, 0, len(calibrateProse))
	for _, src := range calibrateProse {
		segments = append(segments, standardize(t, prof, src, corpus.Calibrate))
	}
	ref, err := deviation.BuildReference(prof, corpus.Calibrate, segments, 3)
	if err != nil {
		t.Fatalf("BuildReference: %v", err)
	}
	if ref.ID == "" {
		t.Fatalf("the reference has no ID")
	}
	return ref
}

func standardize(t *testing.T, prof *profile.Profile, src string, split corpus.Split) deviation.Standardization {
	t.Helper()
	doc, err := text.Admit([]byte(src))
	if err != nil {
		t.Fatalf("Admit(%q): %v", src, err)
	}
	out, err := deviation.Standardize(features.Extract(doc.Tokens()), mustFit(t, prof), split)
	if err != nil {
		t.Fatalf("Standardize(%q): %v", src, err)
	}
	return out
}

// held builds one Test-split classed distance for the gates. The reference ID is
// the real, content-derived one: a literal would not match the reference score
// is given, and the compatibility check would then refuse every ordinary score.
func held(class eval.Class, value float64, referenceID, document string) eval.ClassedDistance {
	return eval.ClassedDistance{
		Class:    class,
		Document: document,
		Distance: deviation.Distance{
			ProfileID: testProfileID, ReferenceID: referenceID,
			FeatureManifestDigest: features.ManifestDigest(), Split: corpus.Test,
			Value: value, Defined: true,
			Features:     []features.ID{features.WordLengthMean},
			ScoredTiers:  []features.Tier{features.TierA},
			WeightScheme: deviation.WeightSchemeUniform, Algorithm: deviation.DistanceAlgorithm,
		},
	}
}

func heldOut(ref *deviation.Reference, class eval.Class, from, to int) []eval.ClassedDistance {
	out := make([]eval.ClassedDistance, 0, to-from+1)
	for v := from; v <= to; v++ {
		out = append(out, held(class, float64(v), ref.ID, label(v)))
	}
	return out
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string([]byte{byte('0' + n)})
	}
	return itoa(n/10) + string([]byte{byte('0' + n%10)})
}

func label(n int) string {
	digits := "0123456789"
	return "doc-" + string([]byte{digits[(n/100)%10], digits[(n/10)%10], digits[n%10]})
}

// release builds a release that passes both gates: authors far below
// distractors, eighty and forty clusters.
func testRelease(t *testing.T, ref *deviation.Reference) eval.Release {
	t.Helper()
	return releaseOver(t, append(
		heldOut(ref, eval.ClassAuthor, 1, 80),
		heldOut(ref, eval.ClassDistractor, 201, 240)...,
	))
}

func releaseOver(t *testing.T, population []eval.ClassedDistance) eval.Release {
	t.Helper()
	thresholds, err := eval.Calibrate(calibrationPopulation(population), eval.Source{
		Cohort: "cohort-under-test", DistractorPool: "pool-under-test",
	}, eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	calibration, err := thresholds.CalibrateBands(population, eval.DefaultBandFloor())
	if err != nil {
		t.Fatalf("CalibrateBands: %v", err)
	}
	discrimination, err := eval.Discriminate(population, eval.DefaultDiscrimination())
	if err != nil {
		t.Fatalf("Discriminate: %v", err)
	}
	out, err := eval.NewRelease(discrimination, calibration)
	if err != nil {
		t.Fatalf("NewRelease: %v", err)
	}
	return out
}

// calibrationPopulation relabels a held-out population onto the Calibrate split,
// since thresholds are fitted there and the gates are measured on Test.
func calibrationPopulation(population []eval.ClassedDistance) []eval.ClassedDistance {
	out := make([]eval.ClassedDistance, 0, len(population))
	for _, d := range population {
		d.Distance.Split = corpus.Calibrate
		out = append(out, d)
	}
	return out
}

const draft = `The argument rests on a distinction the record does not support at all.

It is not that the claim is false; nothing in the material would tell us either way.

Every reading of this passage runs into the same wall, which is that it never says it.`

func scoreDraft(t *testing.T, source string) score.Report {
	t.Helper()
	prof := testProfile()
	ref := testReference(t, prof)
	got, err := score.Score([]byte(source), mustFit(t, prof), ref, testRelease(t, ref))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	return got
}

// withFloor returns a profile fitted under a different paragraph floor. The
// floor is the PROFILE'S, not a parameter of scoring: measuring a draft under a
// different admission rule than the profile was fitted with is the error the
// shared path exists to prevent, and giving a caller its own floor would make
// that error reachable by accident.
func withFloor(prof *profile.Profile, floor int) *profile.Profile {
	out := *prof
	out.Requirements.MinParagraphLexicalTokens = floor
	return &out
}

// ---------------------------------------------------------------------------
// A draft is not corpus material
// ---------------------------------------------------------------------------

// The split vocabulary needed a fourth word, and the word matters: without it a
// draft has to claim one of the three, and the only one it could survive is
// `test` — which is where both release gates draw their evidence.
func TestTheDraftSplitIsDeclaredAndDistinct(t *testing.T) {
	if corpus.Draft != "draft" {
		t.Errorf("corpus.Draft = %q, want %q", corpus.Draft, "draft")
	}
	for _, other := range []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Test} {
		if corpus.Draft == other {
			t.Errorf("corpus.Draft collides with %q", other)
		}
	}
}

// The corpus never assigns `draft` either — it partitions only among the other
// three — but that is a property of the corpus package's own assignment and is
// tested there rather than reached around from here.

// Every segment score produces records that it came from a draft.
func TestScoredSegmentsCarryTheDraftSplit(t *testing.T) {
	got := scoreDraft(t, draft)

	if got.Split != corpus.Draft {
		t.Errorf("Split = %q, want %q", got.Split, corpus.Draft)
	}
	if len(got.Segments) == 0 {
		t.Fatalf("no segments")
	}
	// And the summary is not the only witness: the measured artifact carries the
	// split it was actually standardized under, so a score that measured as
	// `test` and labelled the report `draft` is visible here.
	for i, segment := range got.Segments {
		if segment.Distance.Split != corpus.Draft {
			t.Errorf("segment %d was measured under split %q, want %q", i, segment.Distance.Split, corpus.Draft)
		}
	}
}

// ---------------------------------------------------------------------------
// A reference has to survive being stored
// ---------------------------------------------------------------------------

// score is the first consumer that loads a reference rather than building one,
// and the failure it would otherwise hit is the worst shape available: not a
// crash and not a corrupt artifact, but every paragraph reporting insufficient
// evidence — a legitimate verdict, indistinguishable from a real one, on a
// profile that is fine.
func TestAReferenceSurvivesBeingPersisted(t *testing.T) {
	prof := testProfile()
	live := testReference(t, prof)

	encoded, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var restored deviation.Reference
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if restored.ID != live.ID {
		t.Errorf("ID = %q, want %q", restored.ID, live.ID)
	}
	for _, id := range manifestOrder() {
		if restored.Size(id) != live.Size(id) {
			t.Errorf("%s: restored size %d, want %d", id, restored.Size(id), live.Size(id))
		}
		if restored.Available(id) != live.Available(id) {
			t.Errorf("%s: restored availability %v, want %v", id, restored.Available(id), live.Available(id))
		}
	}

	// Sizes matching is not enough: a distribution altered but the same length
	// would preserve them while moving every deviation, reason, direction and
	// band. So the transform itself is compared, feature by feature, and then
	// the whole report.
	for _, src := range calibrateProse {
		query := standardize(t, prof, src, corpus.Draft)
		want, err := live.Transform(query)
		if err != nil {
			t.Fatalf("Transform against the live reference: %v", err)
		}
		got, err := restored.Transform(query)
		if err != nil {
			t.Fatalf("Transform against the restored reference: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("the transform changed after a round trip:\n got %+v\nwant %+v", got, want)
		}
	}

	release := testRelease(t, live)
	before, err := score.Score([]byte(draft), mustFit(t, prof), live, release)
	if err != nil {
		t.Fatalf("Score against the live reference: %v", err)
	}
	after, err := score.Score([]byte(draft), mustFit(t, prof), &restored, release)
	if err != nil {
		t.Fatalf("Score against the restored reference: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the report changed after a round trip:\n got %+v\nwant %+v", after, before)
	}
}

func manifestOrder() []features.ID {
	out := make([]features.ID, 0, len(features.Definitions()))
	for _, definition := range features.Definitions() {
		out = append(out, definition.ID)
	}
	return out
}

// ---------------------------------------------------------------------------
// Segments
// ---------------------------------------------------------------------------

// One segment per admitted paragraph, in document order, using the profile's own
// admission path and floor.
func TestOneSegmentPerAdmittedParagraph(t *testing.T) {
	got := scoreDraft(t, draft)

	if len(got.Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(got.Segments))
	}
	for i, segment := range got.Segments {
		if segment.Index != i {
			t.Errorf("segment %d reports index %d", i, segment.Index)
		}
		if segment.LexicalTokens <= 0 {
			t.Errorf("segment %d reports %d lexical tokens", i, segment.LexicalTokens)
		}
		if segment.Distance.Split != corpus.Draft {
			t.Errorf("segment %d was measured under split %q, want %q", i, segment.Distance.Split, corpus.Draft)
		}
	}
}

// A paragraph below the floor is not a segment, and the count of what was
// dropped is reported rather than left to be inferred from a short list.
func TestParagraphsBelowTheFloorAreExcludedAndCounted(t *testing.T) {
	source := draft + "\n\nToo short.\n\nAlso brief here.\n"
	got := scoreDraft(t, source)

	if got.ParagraphsBelowFloor != 2 {
		t.Errorf("paragraphs below the floor = %d, want 2", got.ParagraphsBelowFloor)
	}
	if len(got.Segments) != 3 {
		t.Errorf("got %d segments, want the 3 that clear the floor", len(got.Segments))
	}
}

// The floor is the PROFILE'S, and it is used: raising the profile's own floor
// past every paragraph leaves no segments at all. There is no scoring-level
// floor to disagree with it.
func TestTheFloorIsTheProfilesOwn(t *testing.T) {
	prof := withFloor(testProfile(), 500)
	ref := testReference(t, testProfile())
	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, testRelease(t, ref))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(got.Segments) != 0 {
		t.Errorf("got %d segments at a floor of 500 lexical tokens, want none", len(got.Segments))
	}
	if got.ParagraphsBelowFloor != 3 {
		t.Errorf("paragraphs below the floor = %d, want 3", got.ParagraphsBelowFloor)
	}
}

// A draft with nothing in it is a report with no segments, not an error. There
// is nothing wrong with an empty draft; there is simply nothing to say about it.
func TestAnEmptyDraftScoresToNoSegments(t *testing.T) {
	got := scoreDraft(t, "")

	if len(got.Segments) != 0 {
		t.Errorf("got %d segments for an empty draft", len(got.Segments))
	}
	if got.ProfileID != testProfileID {
		t.Errorf("an empty report lost its provenance")
	}
}

// ---------------------------------------------------------------------------
// The report has one shape
// ---------------------------------------------------------------------------

// Every segment carries a delta for every manifest feature, in manifest order,
// whether or not the feature could be measured. Omitting the unmeasurable ones
// would make a reader who does not know the manifest by heart unable to tell
// "typical" from "not measured".
func TestEverySegmentReportsEveryFeature(t *testing.T) {
	got := scoreDraft(t, draft)

	want := manifestOrder()
	for i, segment := range got.Segments {
		order := make([]features.ID, 0, len(segment.Features))
		for _, delta := range segment.Features {
			order = append(order, delta.Feature)
		}
		if len(order) != len(want) {
			t.Fatalf("segment %d reports %d features, want %d", i, len(order), len(want))
		}
		for j := range want {
			if order[j] != want[j] {
				t.Fatalf("segment %d feature %d is %q, want %q", i, j, order[j], want[j])
			}
		}
	}
}

// Direction is the sign of the deviation, reported beside it. That is why the
// deviation was kept signed.
func TestDirectionFollowsTheSignOfTheDeviation(t *testing.T) {
	got := scoreDraft(t, draft)

	seen := map[score.Direction]bool{}
	for i, segment := range got.Segments {
		for _, delta := range segment.Features {
			if !delta.Defined {
				if delta.Direction != "" {
					t.Errorf("segment %d: an undefined %s has direction %q; there is no sign to take",
						i, delta.Feature, delta.Direction)
				}
				continue
			}
			seen[delta.Direction] = true
			switch {
			case delta.Deviation > 0 && delta.Direction != score.DirectionAbove:
				t.Errorf("segment %d: %s deviates by %v and reports %q", i, delta.Feature, delta.Deviation, delta.Direction)
			case delta.Deviation < 0 && delta.Direction != score.DirectionBelow:
				t.Errorf("segment %d: %s deviates by %v and reports %q", i, delta.Feature, delta.Deviation, delta.Direction)
			case delta.Deviation == 0 && delta.Direction != score.DirectionTypical:
				t.Errorf("segment %d: %s deviates by 0 and reports %q", i, delta.Feature, delta.Direction)
			}
		}
	}
	if !seen[score.DirectionAbove] || !seen[score.DirectionBelow] {
		t.Errorf("this draft produced only %v; the fixture must exercise both directions", seen)
	}
}

func TestDirectionNames(t *testing.T) {
	if score.DirectionAbove != "above" || score.DirectionBelow != "below" || score.DirectionTypical != "typical" {
		t.Errorf("directions are %q, %q and %q", score.DirectionAbove, score.DirectionBelow, score.DirectionTypical)
	}
}

// An unmeasurable feature says why, and carries no number it did not earn.
func TestAnUnmeasurableFeatureStatesItsReason(t *testing.T) {
	prof := testProfile()
	for i := range prof.Stats {
		if prof.Stats[i].Feature == features.SemicolonDensity {
			prof.Stats[i] = profile.Stats{Feature: features.SemicolonDensity, N: 3, MinObservations: 20}
		}
	}

	ref := testReference(t, prof)
	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, testRelease(t, ref))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(got.Segments) == 0 {
		t.Fatalf("no segments")
	}

	for _, delta := range got.Segments[0].Features {
		if delta.Feature != features.SemicolonDensity {
			continue
		}
		if delta.Defined {
			t.Fatalf("semicolon_density is defined as %v against an unfit profile statistic", delta.Deviation)
		}
		if delta.Reason == "" {
			t.Errorf("an undefined delta states no reason")
		}
		if delta.Deviation != 0 {
			t.Errorf("an undefined delta carries %v; it must be zero, never a sentinel", delta.Deviation)
		}
		return
	}
	t.Fatalf("semicolon_density is missing from the report")
}

// ---------------------------------------------------------------------------
// Bands come from the release, and only from the release
// ---------------------------------------------------------------------------

// A scored segment gets its band from the composed release verdict, so a band
// the gates refused cannot appear in a report.
func TestBandsComeFromTheRelease(t *testing.T) {
	got := scoreDraft(t, draft)

	if !got.Calibrated {
		t.Fatalf("this fixture must be calibrated")
	}
	for i, segment := range got.Segments {
		if !segment.Distance.Defined {
			continue
		}
		if !segment.Band.Defined {
			t.Errorf("segment %d is scored but not banded: %v", i, segment.Band.Reason)
		}
		switch segment.Band.Band {
		case eval.BandInRange, eval.BandDrifting, eval.BandNotYou:
		default:
			t.Errorf("segment %d has band %q", i, segment.Band.Band)
		}
	}
}

// Below the discrimination floor the distance and the deltas are still reported
// and the band is withheld — which is one report shape, not two. A reader asks
// whether the band is defined.
func TestAnUncalibratedProfileStillReportsDistancesAndDeltas(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)

	// Author and distractor distances interleaved, so discrimination fails.
	population := append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 2, 41)...)
	release := releaseOver(t, population)
	if release.Discrimination.Discriminates {
		t.Fatalf("this fixture must fail discrimination; the bound is %v", release.Discrimination.LowerBound)
	}

	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, release)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	if got.Calibrated {
		t.Errorf("the report claims calibration below the discrimination floor")
	}
	if len(got.Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(got.Segments))
	}
	for i, segment := range got.Segments {
		if !segment.Distance.Defined {
			t.Errorf("segment %d has no distance; an uncalibrated profile still measures", i)
		}
		if segment.Band.Defined {
			t.Errorf("segment %d was banded as %q below the discrimination floor", i, segment.Band.Band)
		}
		if segment.Band.Reason != eval.ReasonUncalibrated {
			t.Errorf("segment %d: band reason %q, want %q", i, segment.Band.Reason, eval.ReasonUncalibrated)
		}
		if len(segment.Features) != len(manifestOrder()) {
			t.Errorf("segment %d reports %d deltas; an uncalibrated profile still reports every one", i, len(segment.Features))
		}
	}
}

// A segment with too few available features gets no distance and therefore no
// band, and says so with its own reason rather than the profile's.
func TestASegmentWithInsufficientEvidenceIsReportedAsSuch(t *testing.T) {
	prof := testProfile()
	// Four of six features cannot be fitted, leaving two available against a
	// required majority of four.
	unfit := []features.ID{
		features.SemicolonDensity, features.ColonDensity,
		features.ClauseMarkerRate, features.CommaDensity,
	}
	for _, id := range unfit {
		for i := range prof.Stats {
			if prof.Stats[i].Feature == id {
				prof.Stats[i] = profile.Stats{Feature: id, N: 3, MinObservations: 20}
			}
		}
	}

	ref := testReference(t, prof)
	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, testRelease(t, ref))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	for i, segment := range got.Segments {
		if segment.Distance.Defined {
			t.Errorf("segment %d has a distance of %v on two available features", i, segment.Distance.Value)
		}
		if segment.Distance.Reason != deviation.ReasonInsufficientEvidence {
			t.Errorf("segment %d: reason %q, want %q", i, segment.Distance.Reason, deviation.ReasonInsufficientEvidence)
		}
		if segment.Band.Defined {
			t.Errorf("segment %d was banded without a distance", i)
		}
		if segment.Distance.Value != 0 {
			t.Errorf("segment %d carries a distance of %v; an unscored segment carries zero", i, segment.Distance.Value)
		}
		// The deltas are still there: not measuring the distance does not mean
		// not measuring the features.
		if len(segment.Features) != len(manifestOrder()) {
			t.Errorf("segment %d reports %d deltas, want every one", i, len(segment.Features))
		}
	}
}

// ---------------------------------------------------------------------------
// v1 scores Tier A only
// ---------------------------------------------------------------------------

// The tier accounting is reported per segment, and today resolves to the single
// tier the manifest declares. Asserting the concrete position as well as the
// derivation means a manifest that grows a tier cannot quietly change what a
// score means without this failing.
func TestScoringIsTierAOnlyWhileTheManifestSaysSo(t *testing.T) {
	got := scoreDraft(t, draft)

	for i, segment := range got.Segments {
		if len(segment.Distance.Tiers) != 1 {
			t.Fatalf("segment %d reports %d tiers; v1's manifest declares one", i, len(segment.Distance.Tiers))
		}
		if segment.Distance.Tiers[0].Tier != features.TierA {
			t.Errorf("segment %d reports tier %q, want %q", i, segment.Distance.Tiers[0].Tier, features.TierA)
		}
		if segment.Distance.Partial {
			t.Errorf("segment %d is flagged partial against a one-tier manifest", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Provenance
// ---------------------------------------------------------------------------

func TestTheReportCarriesItsProvenance(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)
	release := testRelease(t, ref)

	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, release)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	if got.ProfileID != prof.ID {
		t.Errorf("ProfileID = %q, want %q", got.ProfileID, prof.ID)
	}
	if got.ReferenceID != ref.ID {
		t.Errorf("ReferenceID = %q, want %q", got.ReferenceID, ref.ID)
	}
	if got.ReleaseID != release.ID {
		t.Errorf("ReleaseID = %q, want %q", got.ReleaseID, release.ID)
	}
	if got.FeatureManifestDigest != features.ManifestDigest() {
		t.Errorf("FeatureManifestDigest = %q", got.FeatureManifestDigest)
	}
	if got.Algorithm != score.Algorithm {
		t.Errorf("Algorithm = %q, want %q", got.Algorithm, score.Algorithm)
	}
	if score.Algorithm != "score-paragraph-v1" {
		t.Errorf("score.Algorithm = %q", score.Algorithm)
	}
}

// A report that cannot name what produced it is not a result, and one assembled
// from artifacts that do not describe the same thing is worse than none.
func TestScoreRefusesMismatchedArtifacts(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)
	release := testRelease(t, ref)

	t.Run("a reference from another profile", func(t *testing.T) {
		other := testProfile()
		other.ID = "another-profile"
		if _, err := score.Score([]byte(draft), mustFit(t, other), ref, release); !errors.Is(err, score.ErrProfileMismatch) {
			t.Errorf("err = %v, want %v", err, score.ErrProfileMismatch)
		}
	})

	t.Run("a release from another reference", func(t *testing.T) {
		population := append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 201, 240)...)
		for i := range population {
			population[i].Distance.ReferenceID = "another-reference"
		}
		if _, err := score.Score([]byte(draft), mustFit(t, prof), ref, releaseOver(t, population)); !errors.Is(err, score.ErrReferenceMismatch) {
			t.Errorf("err = %v, want %v", err, score.ErrReferenceMismatch)
		}
	})

	// "no profile" left this table when Score narrowed to the fitted
	// projection: there is no nil to pass. The rejection moved to where the
	// projection is made — profile's TestANilProfileYieldsNoProjection — and a
	// caller that gets that far is holding a Fitted that was produced, not
	// invented.
	t.Run("a profile whose statistics are not the manifest", func(t *testing.T) {
		short := mustFit(t, prof)
		short.Stats = short.Stats[:len(short.Stats)-1]
		if _, err := score.Score([]byte(draft), short, ref, release); err == nil {
			t.Error("scored against a profile missing a manifest feature")
		}
	})

	t.Run("no reference", func(t *testing.T) {
		if _, err := score.Score([]byte(draft), mustFit(t, prof), nil, release); !errors.Is(err, score.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, score.ErrMissingInput)
		}
	})

	t.Run("a document-unit profile", func(t *testing.T) {
		other := testProfile()
		other.Unit = profile.UnitDocument
		if _, err := score.Score([]byte(draft), mustFit(t, other), ref, release); !errors.Is(err, deviation.ErrProfileUnit) {
			t.Errorf("err = %v, want %v", err, deviation.ErrProfileUnit)
		}
	})

	// Non-positive, not merely zero: profile.Build validates the floor, so one
	// arriving here at all is malformed however it got that way.
	for _, floor := range []int{0, -1} {
		t.Run("a profile whose paragraph floor is "+itoa(floor), func(t *testing.T) {
			if _, err := score.Score([]byte(draft), mustFit(t, withFloor(prof), floor), ref, release); !errors.Is(err, score.ErrInvalidRequirements) {
				t.Errorf("err = %v, want %v", err, score.ErrInvalidRequirements)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Determinism, and nothing else
// ---------------------------------------------------------------------------

// Scoring the same draft against the same artifacts twice gives the same report.
// Section 2 forbids a score that changes on re-run, and nothing in this path is
// allowed to introduce randomness — the bootstrap lives behind the release,
// which is already fixed by the time score sees it.
func TestScoringIsDeterministic(t *testing.T) {
	first := scoreDraft(t, draft)
	second := scoreDraft(t, draft)

	if len(first.Segments) != len(second.Segments) {
		t.Fatalf("segment counts differ")
	}
	for i := range first.Segments {
		if first.Segments[i].Distance.Value != second.Segments[i].Distance.Value {
			t.Errorf("segment %d: distance %v then %v", i, first.Segments[i].Distance.Value, second.Segments[i].Distance.Value)
		}
		if first.Segments[i].Band.Band != second.Segments[i].Band.Band {
			t.Errorf("segment %d: band %q then %q", i, first.Segments[i].Band.Band, second.Segments[i].Band.Band)
		}
	}
}

// Nothing scored is a NaN or an infinity, whatever the draft. These values are
// reported and persisted.
func TestNothingScoredIsNonFinite(t *testing.T) {
	for _, source := range []string{draft, "One short paragraph that clears the floor easily enough.", strings.Repeat("word ", 400)} {
		got := scoreDraft(t, source)
		for i, segment := range got.Segments {
			if math.IsNaN(segment.Distance.Value) || math.IsInf(segment.Distance.Value, 0) {
				t.Errorf("segment %d distance = %v", i, segment.Distance.Value)
			}
			for _, delta := range segment.Features {
				if math.IsNaN(delta.Deviation) || math.IsInf(delta.Deviation, 0) {
					t.Errorf("segment %d %s = %v", i, delta.Feature, delta.Deviation)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The composition oracle
// ---------------------------------------------------------------------------

// The strongest assertion available without hand-computed floats, and the one
// that subsumes most of the shape tests above.
//
// The expected report is assembled here from the SAME PUBLIC BUILDING BLOCKS
// score is required to use — profile.ParagraphVectors for admission, then
// Standardize under the draft split, Transform, Distance, and the release's own
// Band — and every field of every segment must match. That is not circular: the
// test reaches for the documented contract, not for score's internals, so an
// implementation that split paragraphs its own way, standardized under another
// split, dropped an unmeasurable feature, computed a direction from something
// other than the sign, or reached past the release for a band all fail here.
func TestTheReportIsTheCompositionOfItsParts(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)
	release := testRelease(t, ref)

	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, release)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	doc, err := text.Admit([]byte(draft))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	paragraphs, err := profile.ParagraphVectors(doc, prof.Requirements.MinParagraphLexicalTokens)
	if err != nil {
		t.Fatalf("ParagraphVectors: %v", err)
	}

	if len(got.Segments) != len(paragraphs.Vectors) {
		t.Fatalf("got %d segments, want the %d vectors the shared admission path yields",
			len(got.Segments), len(paragraphs.Vectors))
	}
	if got.ParagraphsBelowFloor != paragraphs.BelowFloor {
		t.Errorf("paragraphs below the floor = %d, want the shared path's %d",
			got.ParagraphsBelowFloor, paragraphs.BelowFloor)
	}

	for i, vector := range paragraphs.Vectors {
		segment := got.Segments[i]

		standardized, err := deviation.Standardize(vector, mustFit(t, prof), corpus.Draft)
		if err != nil {
			t.Fatalf("Standardize %d: %v", i, err)
		}
		deviations, err := ref.Transform(standardized)
		if err != nil {
			t.Fatalf("Transform %d: %v", i, err)
		}
		distance, err := deviations.Distance()
		if err != nil {
			t.Fatalf("Distance %d: %v", i, err)
		}
		band, err := release.Band(distance)
		if err != nil {
			t.Fatalf("Band %d: %v", i, err)
		}

		if segment.LexicalTokens != vector.LexicalTokens {
			t.Errorf("segment %d: %d lexical tokens, want %d", i, segment.LexicalTokens, vector.LexicalTokens)
		}
		if !reflect.DeepEqual(segment.Distance, distance) {
			t.Errorf("segment %d distance:\n got %+v\nwant %+v", i, segment.Distance, distance)
		}
		if segment.Band != band {
			t.Errorf("segment %d band: got %+v, want %+v", i, segment.Band, band)
		}

		if len(segment.Features) != len(deviations.Values) {
			t.Fatalf("segment %d reports %d deltas, want %d", i, len(segment.Features), len(deviations.Values))
		}
		for j, want := range deviations.Values {
			delta := segment.Features[j]
			if delta.Feature != want.Feature {
				t.Errorf("segment %d delta %d is %q, want %q", i, j, delta.Feature, want.Feature)
			}
			if delta.Defined != want.Defined || delta.Deviation != want.Value || delta.Reason != want.Reason {
				t.Errorf("segment %d %s: got defined=%v value=%v reason=%q, want defined=%v value=%v reason=%q",
					i, delta.Feature, delta.Defined, delta.Deviation, delta.Reason,
					want.Defined, want.Value, want.Reason)
			}
			wantDirection := score.Direction("")
			switch {
			case !want.Defined:
			case want.Value > 0:
				wantDirection = score.DirectionAbove
			case want.Value < 0:
				wantDirection = score.DirectionBelow
			default:
				wantDirection = score.DirectionTypical
			}
			if delta.Direction != wantDirection {
				t.Errorf("segment %d %s: direction %q, want %q", i, delta.Feature, delta.Direction, wantDirection)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// A refused band cannot reach a report
// ---------------------------------------------------------------------------

// The uncalibrated case above shows total refusal. This shows the harder one: a
// release that passes discrimination and keeps one claiming band while refusing
// the other. A segment landing geometrically in the refused band must come back
// `drifting`, not the label the thresholds alone would give.
//
// An implementation reaching for Thresholds.Band, or for Calibration's
// boundaries directly, emits the refused label here.
func TestARefusedBandNeverReachesAReport(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)

	// Refusing a claiming band on RATE while discrimination passes is not
	// constructible: in-range leaks only when distractors fall below t_low,
	// which is the separation discrimination measures. The floor is the
	// independent lever. Eighty author clusters clear not-you's 3/80 = 0.0375
	// against 0.05; twenty distractor clusters fail in-range's 3/20 = 0.15
	// against 0.10; and twenty clusters per class clear discrimination's cap of
	// 1 - 3/20 = 0.85 against a floor of 0.80.
	population := append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 201, 220)...)
	release := releaseOver(t, population)

	if !release.Discrimination.Discriminates {
		t.Fatalf("this fixture needs discrimination to pass; the bound is %v", release.Discrimination.LowerBound)
	}
	refused := map[eval.Band]bool{}
	for _, report := range release.Calibration.Bands {
		if !report.Emitted {
			refused[report.Band] = true
		}
	}
	if !refused[eval.BandInRange] && !refused[eval.BandNotYou] {
		t.Fatalf("this fixture needs a refused CLAIMING band; refused = %v", refused)
	}
	if !release.Calibration.Calibrated {
		t.Fatalf("this fixture needs the calibration to survive")
	}

	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, release)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	sawCollapse := false
	for i, segment := range got.Segments {
		if !segment.Distance.Defined || !segment.Band.Defined {
			continue
		}
		if refused[segment.Band.Band] {
			t.Errorf("segment %d reports %q, which the calibration refused", i, segment.Band.Band)
		}
		// The band the boundaries alone would have given.
		geometric := eval.BandDrifting
		switch {
		case segment.Distance.Value <= release.Calibration.Low:
			geometric = eval.BandInRange
		case segment.Distance.Value >= release.Calibration.High:
			geometric = eval.BandNotYou
		}
		if refused[geometric] {
			sawCollapse = true
			if segment.Band.Band != eval.BandDrifting {
				t.Errorf("segment %d sits geometrically in the refused %q and reports %q, want %q",
					i, geometric, segment.Band.Band, eval.BandDrifting)
			}
		}
	}
	if !sawCollapse {
		t.Fatalf("no segment landed in the refused band; this fixture cannot demonstrate the collapse")
	}
}

// ---------------------------------------------------------------------------
// Every split that is not a draft
// ---------------------------------------------------------------------------

// The full boundary, table-driven, rather than the one direction that happened
// to be convenient. A reference is fitted only on calibrate; both gates measure
// only on test; thresholds are fitted only on calibrate.
func TestTheSplitBoundariesAreCompleteInBothDirections(t *testing.T) {
	prof := testProfile()
	every := []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Test, corpus.Draft, "holdout", ""}

	t.Run("a reference admits only calibrate", func(t *testing.T) {
		for _, split := range every {
			t.Run(name(split), func(t *testing.T) {
				segments := make([]deviation.Standardization, 0, len(calibrateProse))
				for _, src := range calibrateProse {
					if split == "holdout" || split == "" {
						// Standardize refuses these outright, which is the
						// boundary one level down and is tested in deviation.
						return
					}
					segments = append(segments, standardize(t, prof, src, split))
				}
				_, err := deviation.BuildReference(prof, corpus.Calibrate, segments, 3)
				if split == corpus.Calibrate {
					if err != nil {
						t.Errorf("calibrate segments were refused: %v", err)
					}
					return
				}
				if !errors.Is(err, deviation.ErrReferenceSplit) {
					t.Errorf("err = %v, want %v", err, deviation.ErrReferenceSplit)
				}
			})
		}
	})

	ref := testReference(t, prof)
	population := append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 201, 240)...)
	thresholds, err := eval.Calibrate(calibrationPopulation(population), eval.Source{
		Cohort: "cohort-under-test", DistractorPool: "pool-under-test",
	}, eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Draft} {
		t.Run("the gates refuse "+name(split), func(t *testing.T) {
			moved := make([]eval.ClassedDistance, 0, len(population))
			for _, d := range population {
				d.Distance.Split = split
				moved = append(moved, d)
			}
			if _, err := thresholds.CalibrateBands(moved, eval.DefaultBandFloor()); !errors.Is(err, eval.ErrTestSplit) {
				t.Errorf("band gate: err = %v, want %v", err, eval.ErrTestSplit)
			}
			if _, err := eval.Discriminate(moved, eval.DefaultDiscrimination()); !errors.Is(err, eval.ErrTestSplit) {
				t.Errorf("discrimination gate: err = %v, want %v", err, eval.ErrTestSplit)
			}
		})
	}

	for _, split := range []corpus.Split{corpus.Train, corpus.Test, corpus.Draft} {
		t.Run("thresholds refuse "+name(split), func(t *testing.T) {
			moved := make([]eval.ClassedDistance, 0, len(population))
			for _, d := range population {
				d.Distance.Split = split
				moved = append(moved, d)
			}
			if _, err := eval.Calibrate(moved, eval.Source{
				Cohort: "cohort-under-test", DistractorPool: "pool-under-test",
			}, eval.DefaultTargets()); !errors.Is(err, eval.ErrCalibrationSplit) {
				t.Errorf("err = %v, want %v", err, eval.ErrCalibrationSplit)
			}
		})
	}
}

func name(split corpus.Split) string {
	if split == "" {
		return "no split"
	}
	return string(split)
}

// ---------------------------------------------------------------------------
// The gates are independent, and the report says which failed
// ---------------------------------------------------------------------------

// Discrimination failing is tested above. This is the other direction, and it is
// constructible precisely because the two gates ask different questions with
// different minimums.
//
// Twenty author clusters and twenty distractor clusters, perfectly separated.
// Discrimination needs fifteen clusters per class and gets twenty, so its bound
// is the cap 1 - 3/20 = 0.85 and it passes. The band gate needs sixty author
// clusters for not-you and thirty distractor clusters for in-range, so BOTH
// claiming bands are refused on the rule-of-three floor however clean the
// observation — and the profile is uncalibrated with discrimination intact.
func TestAReportIsUncalibratedWhenTheBandGateFailsAlone(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)

	population := append(heldOut(ref, eval.ClassAuthor, 1, 20), heldOut(ref, eval.ClassDistractor, 201, 220)...)
	release := releaseOver(t, population)

	if !release.Discrimination.Discriminates {
		t.Fatalf("this fixture needs discrimination to pass; the bound is %v", release.Discrimination.LowerBound)
	}
	if release.Calibration.Calibrated {
		t.Fatalf("this fixture needs the band gate to fail")
	}

	got, err := score.Score([]byte(draft), mustFit(t, prof), ref, release)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	if got.Calibrated {
		t.Errorf("the report claims calibration while the band gate refused every claiming band")
	}
	if len(got.Segments) != 3 {
		t.Fatalf("got %d segments, want 3", len(got.Segments))
	}
	for i, segment := range got.Segments {
		if !segment.Distance.Defined {
			t.Errorf("segment %d has no distance; an uncalibrated profile still measures", i)
		}
		if segment.Band.Defined {
			t.Errorf("segment %d was banded as %q with no claiming band emitted", i, segment.Band.Band)
		}
	}
}

// Report.Calibrated tracks the release's own verdict rather than being computed
// a second time, across every combination this design can produce.
func TestReportedCalibrationFollowsTheRelease(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)

	cases := []struct {
		name       string
		population []eval.ClassedDistance
	}{
		{name: "both gates pass", population: append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 201, 240)...)},
		{name: "the band gate fails alone", population: append(heldOut(ref, eval.ClassAuthor, 1, 20), heldOut(ref, eval.ClassDistractor, 201, 220)...)},
		{name: "discrimination fails", population: append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 2, 41)...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			release := releaseOver(t, c.population)
			got, err := score.Score([]byte(draft), mustFit(t, prof), ref, release)
			if err != nil {
				t.Fatalf("Score: %v", err)
			}
			if got.Calibrated != release.Shippable {
				t.Errorf("report calibrated = %v, want the release's %v", got.Calibrated, release.Shippable)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Mismatches are refused before anything is measured
// ---------------------------------------------------------------------------

// A draft with nothing in it still has to be refused if its artifacts do not
// describe the same thing. Validating lazily — only when a segment first reaches
// the release — would let an empty or entirely below-floor draft through and
// produce a report stamped with provenance that was never checked.
func TestMismatchedArtifactsAreRefusedWithNothingToScore(t *testing.T) {
	prof := testProfile()
	ref := testReference(t, prof)
	release := testRelease(t, ref)

	empty := [][]byte{[]byte(""), []byte("Too short.\n\nAlso brief.\n")}

	for _, source := range empty {
		t.Run("another profile", func(t *testing.T) {
			other := testProfile()
			other.ID = "another-profile"
			if _, err := score.Score(source, mustFit(t, other), ref, release); !errors.Is(err, score.ErrProfileMismatch) {
				t.Errorf("err = %v, want %v", err, score.ErrProfileMismatch)
			}
		})

		t.Run("another reference", func(t *testing.T) {
			population := append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 201, 240)...)
			for i := range population {
				population[i].Distance.ReferenceID = "another-reference"
			}
			if _, err := score.Score(source, mustFit(t, prof), ref, releaseOver(t, population)); !errors.Is(err, score.ErrReferenceMismatch) {
				t.Errorf("err = %v, want %v", err, score.ErrReferenceMismatch)
			}
		})
	}

	// And the same for a draft that admits nothing because the profile's own
	// floor excludes every paragraph — on both mismatch classes, since lazy
	// validation could be specific to one.
	t.Run("everything below the profile's floor", func(t *testing.T) {
		t.Run("another profile", func(t *testing.T) {
			other := withFloor(testProfile(), 500)
			other.ID = "another-profile"
			if _, err := score.Score([]byte(draft), mustFit(t, other), ref, release); !errors.Is(err, score.ErrProfileMismatch) {
				t.Errorf("err = %v, want %v", err, score.ErrProfileMismatch)
			}
		})

		t.Run("another reference", func(t *testing.T) {
			population := append(heldOut(ref, eval.ClassAuthor, 1, 80), heldOut(ref, eval.ClassDistractor, 201, 240)...)
			for i := range population {
				population[i].Distance.ReferenceID = "another-reference"
			}
			if _, err := score.Score([]byte(draft), mustFit(t, withFloor(prof), 500), ref, releaseOver(t, population)); !errors.Is(err, score.ErrReferenceMismatch) {
				t.Errorf("err = %v, want %v", err, score.ErrReferenceMismatch)
			}
		})
	})
}

// mustFit projects a built profile the way store and the workflow do, so these
// tests reach Score through the same narrow input production uses.
func mustFit(t *testing.T, p *profile.Profile) profile.Fitted {
	t.Helper()
	fitted, err := p.Fitted()
	if err != nil {
		t.Fatalf("Fitted: %v", err)
	}
	return fitted
}
