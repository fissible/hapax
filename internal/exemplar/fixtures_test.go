package exemplar_test

import (
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/exemplar"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

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

// Deliberately varied in length, punctuation and clause structure: a flat
// population makes every pairwise distance zero, which would let a broken
// selector look correct because everything ties.
var prose = []string{
	"The argument rests on a distinction the record does not support, and the record is all we have.",
	"It is not that the claim is false; it is that nothing in the material would tell us either way.",
	"Every reading of the passage runs into the same wall, which is that the author never says it.",
	"We can grant the premise and still find that the conclusion does not follow from it.",
	"There is a version of this argument that works, but it is not the one on the page.",
	"A reader who wanted the stronger claim would have to supply the missing step themselves.",
	"The objection is fair, and it does not touch the part of the argument that matters.",
	"Nothing in the surrounding paragraphs settles which of the two readings was intended.",
	"He wrote quickly. The result shows it.",
	"Short sentences accumulate. They do not always add up. Sometimes they simply stop.",
	"The distinction between what was meant and what was written is, in this case, the whole difficulty; and it will not resolve itself.",
	"One might suppose, reading only the summary, that the matter had been settled; the body of the report says otherwise, at length, and with considerable care.",
	"Consider the alternative: that the passage means exactly what it appears to mean.",
	"What follows is not a refutation but a request for the evidence that would make it one.",
	"The claim is modest and the support is thin, which is an uncomfortable combination.",
	"Readers have taken it both ways, and neither camp has produced a decisive citation.",
	"I do not think the author intended the reading that has become standard.",
	"The footnote is doing more work than the sentence it hangs from, which is a bad sign.",
	"Precision here costs almost nothing and buys a great deal of subsequent clarity.",
	"An argument that survives only under its most charitable reading has not survived.",
	"The data were collected carefully; the inference drawn from them was not.",
	"Two things are true at once, and the paper insists on choosing between them.",
	"It would be easy to overstate this, so let me put the weaker version first.",
	"The revision removed the hedge, and with it the only accurate part of the sentence.",
	"Nobody disputes the measurement. The dispute is entirely about what it means.",
	"A stronger claim was available and the author declined to make it, which tells us something.",
	"The structure of the chapter promises an argument the chapter never delivers.",
	"There is a gap between paragraphs four and five that no amount of rereading closes.",
	"The terminology shifts halfway through, quietly, and the conclusion depends on the shift.",
	"Given the constraints described, the result is unsurprising; given the abstract, it is not.",
	"This is a small point about a large claim, which is usually where the trouble is.",
	"The author anticipates the objection and answers a different one instead.",
	"Method and motive are conflated throughout, and separating them takes most of a page.",
	"Where the evidence is strongest the prose is weakest, and the reverse also holds.",
	"A single counterexample would settle it, and none has been offered in twenty years.",
	"The summary is accurate. The emphasis is not. Together they mislead.",
	"Everything turns on a word that appears exactly twice and is defined neither time.",
	"He is right about the mechanism and wrong about what the mechanism implies.",
	"The reader is asked to accept a great deal before the first piece of evidence arrives.",
	"It reads as though two drafts were merged and nobody reconciled the seams.",
}

// admit turns one source into one candidate per included leaf, giving each
// source its own digest as a corpus document would.
func admit(t *testing.T, source string) []exemplar.Candidate {
	t.Helper()
	doc, err := text.Admit([]byte(source))
	if err != nil {
		t.Fatalf("Admit(%q): %v", source, err)
	}
	digest := identity.HashBytes([]byte(source))
	paragraphs, err := profile.ParagraphVectors(doc, 5)
	if err != nil {
		t.Fatalf("ParagraphVectors: %v", err)
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) != len(paragraphs.Vectors) {
		t.Fatalf("%d leaves but %d vectors for %q", len(leaves), len(paragraphs.Vectors), source)
	}
	out := make([]exemplar.Candidate, 0, len(leaves))
	for i, leaf := range leaves {
		run, err := doc.RunText(leaf)
		if err != nil {
			t.Fatalf("RunText: %v", err)
		}
		out = append(out, exemplar.Candidate{
			DocumentDigest: digest, Span: leaf.Span, Role: leaf.Role,
			Containers: leaf.Containers, Split: corpus.Train,
			Text: run, Vector: paragraphs.Vectors[i],
		})
	}
	return out
}

// population builds n plain-paragraph candidates from the prose above.
func population(t *testing.T, n int) []exemplar.Candidate {
	t.Helper()
	if n > len(prose) {
		t.Fatalf("only %d fixtures available, wanted %d", len(prose), n)
	}
	out := make([]exemplar.Candidate, 0, n)
	for _, source := range prose[:n] {
		out = append(out, admit(t, source)...)
	}
	if len(out) != n {
		t.Fatalf("built %d candidates from %d sources; a fixture yielded more than one leaf", len(out), n)
	}
	return out
}

// selectWith is the only route to exemplar.Select in this package. It deep
// copies the candidates and the profile first — including Containers and Vector.Values, the two
// slice fields that would otherwise alias — and requires them unchanged
// afterwards, on the error path as well as the success path. Every expectation
// here is built from the caller's slice, so a selector that mutated it after
// computing a correct result could otherwise emit a certificate that follows
// the mutation and still pass.
func selectWith(t *testing.T, prof *profile.Profile, candidates []exemplar.Candidate, cfg exemplar.Config) (exemplar.Selection, error) {
	t.Helper()
	before := make([]exemplar.Candidate, len(candidates))
	for i, c := range candidates {
		before[i] = c
		before[i].Containers = append([]text.ContainerKind(nil), c.Containers...)
		before[i].Vector.Values = append([]features.FeatureValue(nil), c.Vector.Values...)
	}

	// Select takes the fitted projection, not the build artifact: it reads only
	// the profile's ID and its Fitted(), and taking the artifact forced every
	// caller holding a stored profile to reconstruct one it does not have. The
	// fixtures project here so the cases below keep reading as profiles.
	//
	// A nil profile becomes the zero projection, which says the same thing in the
	// narrowed signature's terms: Select was given nothing usable.
	var fitted profile.Fitted
	if prof != nil {
		var err error
		if fitted, err = prof.Fitted(); err != nil {
			t.Fatalf("Fitted: %v", err)
		}
	}
	// Fitted is a value, but its Stats is a slice: Select can still write through
	// the caller's backing array, which is what this guards.
	fittedBefore := fitted
	fittedBefore.Stats = append([]profile.Stats(nil), fitted.Stats...)

	got, err := exemplar.Select(fitted, candidates, cfg)
	if len(candidates) > 0 && !reflect.DeepEqual(candidates, before) {
		t.Fatalf("Select modified the caller's candidates")
	}
	if !reflect.DeepEqual(fitted, fittedBefore) {
		t.Fatalf("Select modified the caller's projection")
	}
	return got, err
}

func mustSelect(t *testing.T, candidates []exemplar.Candidate, n int) exemplar.Selection {
	t.Helper()
	got, err := selectWith(t, testProfile(), candidates, exemplar.Config{N: n})
	if err != nil {
		t.Fatalf("Select(n=%d) over %d candidates: %v", n, len(candidates), err)
	}
	return got
}

func identities(selection exemplar.Selection) []string {
	out := make([]string, 0, len(selection.Exemplars))
	for _, e := range selection.Exemplars {
		out = append(out, e.Identity())
	}
	return out
}

func strata(candidates []exemplar.Candidate) map[string]int {
	out := map[string]int{}
	for _, c := range candidates {
		out[c.Stratum()]++
	}
	return out
}

func isErr(err, want error) bool { return err != nil && errors.Is(err, want) }

func itoa(v int) string { return strconv.Itoa(v) }

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// numberID mirrors the float formatting the other artifact IDs use.
func numberID(value float64) string {
	if value == 0 {
		value = 0
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// largePopulation synthesises n candidates by varying the prose, for tests that
// need a population larger than the fixture list.
func largePopulation(t *testing.T, n int) []exemplar.Candidate {
	t.Helper()
	out := make([]exemplar.Candidate, 0, n)
	for i := 0; len(out) < n; i++ {
		source := prose[i%len(prose)]
		if extra := i / len(prose); extra > 0 {
			source += " " + strings.Repeat("A further clause follows here. ", extra)
		}
		out = append(out, admit(t, source)...)
	}
	return out[:n]
}

// defineOnly undefines every feature except those listed, so a candidate can be
// pushed below the shared-feature floor with a chosen part of the population.
func defineOnly(c *exemplar.Candidate, keep ...int) {
	wanted := map[int]bool{}
	for _, index := range keep {
		wanted[index] = true
	}
	for i := range c.Vector.Values {
		c.Vector.Values[i].Defined = wanted[i]
	}
}

// twinStrata pairs each prose entry as a plain paragraph and as a list item.
// The run text is identical, so the two carry identical feature vectors while
// sitting in different strata with different identities — which makes the
// eligible counts equal and forces a stratum-order tie.
func twinStrata(t *testing.T) []exemplar.Candidate { return twinPairs(t, 20) }

// twinPairs builds n plain/list-item pairs sharing identical run text, so
// densities come in equal pairs.
func twinPairs(t *testing.T, n int) []exemplar.Candidate {
	t.Helper()
	var out []exemplar.Candidate
	for _, source := range prose[:n] {
		out = append(out, admit(t, source)...)
		out = append(out, admit(t, "- "+source)...)
	}
	return out
}

// Standardization divides by sqrt(profileVariance + samplingVariance), and
// sampling variance is per CANDIDATE — 0 to 0.95 across this fixture — so
// neither statistic is a map shared by every candidate, and both can reorder
// the population. These two profiles are where each one bites.
func narrowProfile() *profile.Profile {
	p := testProfile()
	for i := range p.Stats {
		p.Stats[i].Variance = 1e-6
	}
	return p
}

// heterogeneousProfile gives every feature a DIFFERENT mean and variance, so a
// selector that maps one stat onto every feature — or reuses Stats[0] — cannot
// reproduce it. The variances are small so the per-feature means bite.
func heterogeneousProfile() *profile.Profile {
	p := testProfile()
	for i := range p.Stats {
		p.Stats[i].Mean = 1 + float64(i)
		p.Stats[i].Variance = 0.001 * float64(i+1)
	}
	return p
}

func meanProfile(mean float64) *profile.Profile {
	p := testProfile()
	for i := range p.Stats {
		p.Stats[i].Mean = mean
	}
	return p
}

// densityID formats a density value for the record digest at nine fixed
// decimals rather than full precision. The oracle's probit is Python's
// NormalDist.inv_cdf and the implementation's is math.Sqrt2*math.Erfinv; they
// are the same function computed by different algorithms and agree to about one
// unit in the last place. Hashing full-precision strings across the two pins
// that last bit, which is not a property this component claims — nine decimals
// still catches fabricated evidence by a wide margin.
func densityID(value float64) string { return strconv.FormatFloat(value, 'f', 9, 64) }
