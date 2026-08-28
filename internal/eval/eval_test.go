package eval_test

// eval slice 1 — held-out segment extraction.
//
// `eval` measures discrimination: how well a profile separates the author's
// held-out writing from other people's. Before any of that arithmetic exists
// there has to be a labelled, provenance-stamped population to compute it over.
// This slice produces exactly that and nothing else: no distance, no AUC, no
// bands, no thresholds.
//
// # Why extraction is the leaf
//
// Three things have to be true before a discrimination figure means anything,
// and all three are decided here rather than in the arithmetic:
//
//  1. The author's segments are HELD OUT. Section 2 assigns tuning to Train,
//     thresholds to Calibrate and reported figures to Test. Evaluating on Train
//     reports the fit, not the generalisation.
//  2. The distractor set does not contain the author's own writing. `corpus`
//     gained the screen for this in #19, and this is its first caller —
//     `NonOverlappingWith` is required to pass, for THIS author, before any
//     segment is produced.
//  3. Held-out vectors are computed by the same code as the profile's own. If
//     the two paths ever diverge, every deviation is measured against a
//     distribution fitted by a different definition of the same feature, and
//     nothing downstream would reveal it.
//
// # Pinned decisions
//
//   - Distractor segments are drawn from the SAME-NAMED split as the author's,
//     so Calibrate and Test are disjoint on both sides of the comparison.
//   - The size floor is the profile's own `MinParagraphLexicalTokens`, not a
//     separate knob. A segment must be admitted by the same rule the profile
//     was fitted under, or the comparison is between differently-filtered
//     populations.
//   - Every refusal returns no set at all. A partially built evaluation
//     population is worse than none: it would silently under-count one class.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// balanced weights, so documents actually land in Calibrate and Test rather
// than everything piling into Train.
func policy(role corpus.Role) corpus.Policy {
	return corpus.Policy{
		Register:         "essays",
		Role:             role,
		MinLexicalTokens: 5,
		SplitSeed:        "eval-test",
		Splits:           corpus.SplitWeights{Train: 1, Calibrate: 1, Test: 1},
	}
}

func requirements() profile.Requirements {
	return profile.Requirements{
		MinDocuments:              1,
		MinParagraphs:             1,
		MinObservationsPerFeature: 1,
		MinParagraphLexicalTokens: 4,
	}
}

func evalRequirements(split corpus.Split) eval.Requirements {
	return eval.Requirements{
		Split:                 split,
		MinAuthorSegments:     1,
		MinDistractorSegments: 1,
	}
}

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func pad(i int) string {
	s := "00" + itoa(i)
	return s[len(s)-3:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// Paragraph lexical-token counts in the fixture below, verified against the
// tokenizer. A floor of 9 therefore admits exactly the second and third
// paragraphs of every document, and also exercises the boundary: a paragraph of
// exactly 9 must be admitted, not excluded.
const (
	shortParagraphTokens  = 5
	mediumParagraphTokens = 9
	longParagraphTokens   = 16
	paragraphsPerDocument = 3
)

// multiParagraph writes n documents of three paragraphs each, of DIFFERING
// lengths so a size floor can be shown to change the population, and distinct
// per document so admission does not dedupe them. The index is carried in a
// word rather than a bare number so every body differs for any n.
func multiParagraph(prefix string, n int) map[string]string {
	files := make(map[string]string, n)
	for i := 0; i < n; i++ {
		word := strings.Repeat("w", i%5+2)
		files[prefix+pad(i)+".md"] = "Short one holds " + word + " here.\n\n" +
			"Medium paragraph two holds " + word + " and " + word + " again, here.\n\n" +
			"The long third paragraph d" + pad(i) + " holds " + word + " and " + word +
			" and " + word + " once more, with a comma.\n"
	}
	return files
}

// eligibleIn returns the eligible documents of one snapshot in that snapshot's
// own order, restricted to a split.
func eligibleIn(snap *corpus.Snapshot, split corpus.Split) []corpus.Document {
	var out []corpus.Document
	for _, d := range snap.Eligible() {
		if d.Split == split {
			out = append(out, d)
		}
	}
	return out
}

func walk(t *testing.T, root string, role corpus.Role) *corpus.Snapshot {
	t.Helper()
	snap, err := corpus.Walk(root, policy(role))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return snap
}

// fixture builds an author corpus with a profile, a screened distractor corpus,
// and returns everything Extract needs.
type fixture struct {
	authorRoot, distractorRoot string
	author, distractor         *corpus.Snapshot
	prof                       *profile.Profile
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	authorRoot := write(t, multiParagraph("mine", 18))
	distractorRoot := write(t, multiParagraph("theirs", 18))

	author := walk(t, authorRoot, corpus.RoleAuthor)
	distractor := walk(t, distractorRoot, corpus.RoleDistractor)

	prof, err := profile.Build(authorRoot, author, requirements())
	if err != nil {
		t.Fatalf("profile.Build: %v", err)
	}
	if _, err := distractor.ScreenOverlap(author); err != nil {
		t.Fatalf("ScreenOverlap: %v", err)
	}
	return fixture{authorRoot, distractorRoot, author, distractor, prof}
}

func (f fixture) extract(t *testing.T, req eval.Requirements) *eval.Set {
	t.Helper()
	set, err := eval.Extract(f.authorRoot, f.author, f.distractorRoot, f.distractor, f.prof, req)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if set == nil {
		t.Fatal("Extract returned a nil set and no error")
	}
	return set
}

func splitsPresent(snap *corpus.Snapshot) map[corpus.Split]int {
	out := map[corpus.Split]int{}
	for _, d := range snap.Eligible() {
		out[d.Split]++
	}
	return out
}

// ---------------------------------------------------------------------------
// The population
// ---------------------------------------------------------------------------

func TestSegmentsAreLabelledAndCounted(t *testing.T) {
	f := newFixture(t)
	set := f.extract(t, evalRequirements(corpus.Calibrate))

	author, distractor := 0, 0
	for i, s := range set.Segments {
		switch s.Class {
		case eval.ClassAuthor:
			author++
		case eval.ClassDistractor:
			distractor++
		default:
			t.Fatalf("segment %d has class %q, which is neither", i, s.Class)
		}
		if s.DocumentHash == "" {
			t.Errorf("segment %d carries no document hash; it is the clustering unit every interval depends on", i)
		}
		if s.LexicalTokens < f.prof.Requirements.MinParagraphLexicalTokens {
			t.Errorf("segment %d has %d lexical tokens, below the profile's floor of %d",
				i, s.LexicalTokens, f.prof.Requirements.MinParagraphLexicalTokens)
		}
	}
	if author == 0 || distractor == 0 {
		t.Fatalf("got %d author and %d distractor segments; both classes are required", author, distractor)
	}
	if set.AuthorSegments != author || set.DistractorSegments != distractor {
		t.Errorf("counts say %d/%d, the segment list says %d/%d",
			set.AuthorSegments, set.DistractorSegments, author, distractor)
	}
}

// The load-bearing one. Author segments come only from the requested held-out
// split — never Train, which is what the profile was fitted on.
func TestAuthorSegmentsComeOnlyFromTheRequestedSplit(t *testing.T) {
	f := newFixture(t)

	present := splitsPresent(f.author)
	for _, split := range []corpus.Split{corpus.Train, corpus.Calibrate, corpus.Test} {
		if present[split] == 0 {
			t.Fatalf("fixture has no %s documents; it cannot detect a leak", split)
		}
	}

	for _, split := range []corpus.Split{corpus.Calibrate, corpus.Test} {
		t.Run(string(split), func(t *testing.T) {
			set := f.extract(t, evalRequirements(split))

			wanted := eligibleIn(f.author, split)
			if len(wanted) == 0 {
				t.Fatalf("no author documents in %s", split)
			}

			// Not merely "nothing from the wrong split": EVERY eligible
			// document of the requested split, and every admitted paragraph of
			// it. Checking only for absence of the wrong ones passes on an
			// implementation that silently drops half the population.
			got := map[string]int{}
			for _, s := range set.Segments {
				if s.Class == eval.ClassAuthor {
					got[s.DocumentHash]++
				}
			}
			for _, d := range wanted {
				if got[d.ContentHash] != paragraphsPerDocument {
					t.Errorf("author document %q contributed %d segments, want %d",
						d.Path, got[d.ContentHash], paragraphsPerDocument)
				}
				delete(got, d.ContentHash)
			}
			for hash := range got {
				t.Errorf("author segment from document hash %q, which is not an eligible %s document", hash, split)
			}
			if set.Split != split {
				t.Errorf("set records split %q, want %q", set.Split, split)
			}
		})
	}
}

// Distractors are split too, so Calibrate and Test are disjoint on both sides.
// Reusing one distractor pool across both would let the thresholds estimated on
// Calibrate be validated against the very segments that set them.
func TestDistractorSegmentsComeFromTheSameNamedSplit(t *testing.T) {
	f := newFixture(t)

	for _, split := range []corpus.Split{corpus.Calibrate, corpus.Test} {
		t.Run(string(split), func(t *testing.T) {
			set := f.extract(t, evalRequirements(split))

			wanted := eligibleIn(f.distractor, split)
			if len(wanted) == 0 {
				t.Fatalf("no distractor documents in %s", split)
			}
			got := map[string]int{}
			for _, s := range set.Segments {
				if s.Class == eval.ClassDistractor {
					got[s.DocumentHash]++
				}
			}
			for _, d := range wanted {
				if got[d.ContentHash] != paragraphsPerDocument {
					t.Errorf("distractor document %q contributed %d segments, want %d",
						d.Path, got[d.ContentHash], paragraphsPerDocument)
				}
				delete(got, d.ContentHash)
			}
			for hash := range got {
				t.Errorf("distractor segment from document hash %q, which is not an eligible %s document", hash, split)
			}
		})
	}

	calibrate := f.extract(t, evalRequirements(corpus.Calibrate))
	test := f.extract(t, evalRequirements(corpus.Test))
	seen := map[string]bool{}
	for _, s := range calibrate.Segments {
		seen[s.DocumentHash] = true
	}
	for _, s := range test.Segments {
		if seen[s.DocumentHash] {
			t.Errorf("document %q appears in both the Calibrate and Test populations", s.DocumentPath)
		}
	}
}

// The path that produces held-out vectors must be the path that produced the
// profile's. If they ever diverge, every deviation is measured against a
// distribution fitted by a different definition of the same feature, and
// nothing downstream would show it.
func TestVectorsComeFromTheProfilesOwnParagraphPath(t *testing.T) {
	f := newFixture(t)
	set := f.extract(t, evalRequirements(corpus.Calibrate))

	byDocument := map[string][]features.Vector{}
	for _, s := range set.Segments {
		byDocument[s.DocumentPath] = append(byDocument[s.DocumentPath], s.Vector)
	}
	if len(byDocument) == 0 {
		t.Fatal("no segments to compare")
	}

	checked := 0
	for path, got := range byDocument {
		root := f.authorRoot
		if strings.HasPrefix(path, "theirs") {
			root = f.distractorRoot
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := text.Admit(raw)
		if err != nil {
			t.Fatal(err)
		}
		paragraphs, err := profile.ParagraphVectors(doc, f.prof.Requirements.MinParagraphLexicalTokens)
		if err != nil {
			t.Fatalf("ParagraphVectors(%q): %v", path, err)
		}
		if !reflect.DeepEqual(got, paragraphs.Vectors) {
			t.Errorf("%s: eval's vectors differ from profile.ParagraphVectors\n  eval    %+v\n  profile %+v", path, got, paragraphs.Vectors)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("compared nothing")
	}
}

// The floor is the profile's, not a separate knob: a segment must be admitted
// by the same rule the profile was fitted under.
func TestTheSizeFloorComesFromTheProfile(t *testing.T) {
	authorRoot := write(t, multiParagraph("mine", 18))
	distractorRoot := write(t, multiParagraph("theirs", 18))
	author := walk(t, authorRoot, corpus.RoleAuthor)
	distractor := walk(t, distractorRoot, corpus.RoleDistractor)
	if _, err := distractor.ScreenOverlap(author); err != nil {
		t.Fatal(err)
	}

	// Floor 2 admits all three paragraphs of every document. Floor 9 admits the
	// medium and long ones only — and a paragraph of exactly 9 must be
	// admitted, so an implementation using <= instead of < is caught by the
	// exact counts below rather than by a vaguer "fewer segments" check.
	low, high := requirements(), requirements()
	low.MinParagraphLexicalTokens = 2
	high.MinParagraphLexicalTokens = mediumParagraphTokens

	lowProfile, err := profile.Build(authorRoot, author, low)
	if err != nil {
		t.Fatalf("Build(low): %v", err)
	}
	highProfile, err := profile.Build(authorRoot, author, high)
	if err != nil {
		t.Fatalf("Build(high): %v", err)
	}

	lowSet, err := eval.Extract(authorRoot, author, distractorRoot, distractor, lowProfile, evalRequirements(corpus.Calibrate))
	if err != nil {
		t.Fatalf("Extract(low): %v", err)
	}
	highSet, err := eval.Extract(authorRoot, author, distractorRoot, distractor, highProfile, evalRequirements(corpus.Calibrate))
	if err != nil {
		t.Fatalf("Extract(high): %v", err)
	}

	// Expectations built from EVERY eligible document of the split, not from
	// the documents that happen to appear in the result. Iterating only what
	// came back passes on a population that silently dropped whole documents.
	expect := func(set *eval.Set, want int) {
		t.Helper()
		got := map[string]int{}
		for _, s := range set.Segments {
			got[s.DocumentHash]++
		}
		for _, snap := range []*corpus.Snapshot{author, distractor} {
			for _, d := range eligibleIn(snap, corpus.Calibrate) {
				if got[d.ContentHash] != want {
					t.Errorf("document %q contributed %d segments, want %d", d.Path, got[d.ContentHash], want)
				}
				delete(got, d.ContentHash)
			}
		}
		for hash, n := range got {
			t.Errorf("%d segments from document hash %q, which is not an eligible Calibrate document", n, hash)
		}
	}
	expect(lowSet, paragraphsPerDocument)
	expect(highSet, paragraphsPerDocument-1)
	if len(highSet.Segments) >= len(lowSet.Segments) {
		t.Errorf("raising the profile's floor from %d to %d did not reduce the population: %d then %d",
			low.MinParagraphLexicalTokens, high.MinParagraphLexicalTokens, len(lowSet.Segments), len(highSet.Segments))
	}
	if lowSet.MinParagraphLexicalTokens != low.MinParagraphLexicalTokens {
		t.Errorf("set records floor %d, want the profile's %d", lowSet.MinParagraphLexicalTokens, low.MinParagraphLexicalTokens)
	}
	for i, s := range highSet.Segments {
		if s.LexicalTokens < high.MinParagraphLexicalTokens {
			t.Errorf("segment %d survived below the profile's floor", i)
		}
	}
}

// Build's own reported population must agree with the shared helper. This does
// not prove Build calls ParagraphVectors — no test can prove a call — but it
// constrains Build's paragraph count and below-floor count to the helper's, so
// a divergent inline path shows up as a number rather than as a silently
// different definition of a paragraph.
//
// The genuinely independent oracle remains
// profile.TestParagraphStatisticsAgainstHandComputedValues, whose expected mean
// and variance are computed by hand and share no code with either path.
func TestProfileCountsAgreeWithTheSharedParagraphPath(t *testing.T) {
	f := newFixture(t)

	paragraphs, belowFloor := 0, 0
	for _, d := range f.author.Eligible() {
		if d.Split != corpus.Train {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(f.authorRoot, filepath.FromSlash(d.Path)))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := text.Admit(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := profile.ParagraphVectors(doc, f.prof.Requirements.MinParagraphLexicalTokens)
		if err != nil {
			t.Fatalf("ParagraphVectors(%q): %v", d.Path, err)
		}
		paragraphs += len(got.Vectors)
		belowFloor += got.BelowFloor
	}

	if paragraphs == 0 {
		t.Fatal("the shared path admitted nothing, so the comparison is vacuous")
	}
	if f.prof.Paragraphs != paragraphs {
		t.Errorf("profile reports %d paragraphs, the shared path finds %d", f.prof.Paragraphs, paragraphs)
	}
	if f.prof.ParagraphsBelowFloor != belowFloor {
		t.Errorf("profile reports %d below the floor, the shared path finds %d", f.prof.ParagraphsBelowFloor, belowFloor)
	}
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// Every refusal returns no set. A partially built population would silently
// under-count one class, which is worse than no population at all.
func TestExtractRefuses(t *testing.T) {
	f := newFixture(t)

	unscreenedRoot := write(t, multiParagraph("other", 18))
	unscreened := walk(t, unscreenedRoot, corpus.RoleDistractor)

	otherAuthorRoot := write(t, multiParagraph("someone", 18))
	otherAuthor := walk(t, otherAuthorRoot, corpus.RoleAuthor)

	// A distractor set screened against a DIFFERENT author. The attestation
	// exists, but not for this one.
	wrongScreenRoot := write(t, multiParagraph("wrong", 18))
	wrongScreen := walk(t, wrongScreenRoot, corpus.RoleDistractor)
	if _, err := wrongScreen.ScreenOverlap(otherAuthor); err != nil {
		t.Fatal(err)
	}

	// A distractor set that actually contains the author's writing.
	dirtyRoot := write(t, multiParagraph("mine", 18))
	dirty := walk(t, dirtyRoot, corpus.RoleDistractor)
	if _, err := dirty.ScreenOverlap(f.author); err != nil {
		t.Fatal(err)
	}

	// Sentinel errors rather than substrings. Matching on wording proves a
	// refusal happened somewhere near the right place; errors.Is proves which
	// guard fired, and does not break when a message is reworded.
	for name, tc := range map[string]struct {
		authorRoot     string
		author         *corpus.Snapshot
		distractorRoot string
		distractor     *corpus.Snapshot
		prof           *profile.Profile
		req            eval.Requirements
		wants          error
	}{
		"evaluating on train": {
			f.authorRoot, f.author, f.distractorRoot, f.distractor, f.prof,
			evalRequirements(corpus.Train), eval.ErrSplitNotHeldOut,
		},
		"distractor never screened": {
			f.authorRoot, f.author, unscreenedRoot, unscreened, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrNotOverlapScreened,
		},
		"screened against another author": {
			f.authorRoot, f.author, wrongScreenRoot, wrongScreen, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrNotOverlapScreened,
		},
		"distractor contains the author's writing": {
			f.authorRoot, f.author, dirtyRoot, dirty, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrNotOverlapScreened,
		},
		"profile fitted on another corpus": {
			otherAuthorRoot, otherAuthor, f.distractorRoot, f.distractor, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrProfileMismatch,
		},
		// Role refusals, each with the OTHER linkage intact, so a role failure
		// cannot be satisfied by an implementation that is really refusing for
		// a different reason.
		"author snapshot has the distractor role": {
			f.distractorRoot, f.distractor, f.distractorRoot, f.distractor, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrAuthorRole,
		},
		"distractor snapshot has the author role": {
			f.authorRoot, f.author, f.authorRoot, f.author, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrDistractorRole,
		},
		"too few author segments": {
			f.authorRoot, f.author, f.distractorRoot, f.distractor, f.prof,
			eval.Requirements{Split: corpus.Calibrate, MinAuthorSegments: 100000, MinDistractorSegments: 1}, eval.ErrTooFewAuthorSegments,
		},
		"too few distractor segments": {
			f.authorRoot, f.author, f.distractorRoot, f.distractor, f.prof,
			eval.Requirements{Split: corpus.Calibrate, MinAuthorSegments: 1, MinDistractorSegments: 100000}, eval.ErrTooFewDistractorSegments,
		},
		"nil profile": {
			f.authorRoot, f.author, f.distractorRoot, f.distractor, nil,
			evalRequirements(corpus.Calibrate), eval.ErrMissingInput,
		},
		"nil author snapshot": {
			f.authorRoot, nil, f.distractorRoot, f.distractor, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrMissingInput,
		},
		"nil distractor snapshot": {
			f.authorRoot, f.author, f.distractorRoot, nil, f.prof,
			evalRequirements(corpus.Calibrate), eval.ErrMissingInput,
		},
	} {
		t.Run(name, func(t *testing.T) {
			set, err := eval.Extract(tc.authorRoot, tc.author, tc.distractorRoot, tc.distractor, tc.prof, tc.req)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if set != nil {
				t.Errorf("%s returned a set alongside its error", name)
			}
			// Branch-specific: a refusal from the wrong guard means the case
			// proves nothing about the one it targets.
			if !errors.Is(err, tc.wants) {
				t.Errorf("refusal is %v, want one matching %v — it may be refusing for a different reason", err, tc.wants)
			}
		})
	}
}

// A snapshot attests to specific bytes. If a source file changes after the
// snapshot is taken, extraction would score prose the snapshot never covered,
// and every provenance stamp on the result would be a lie. `profile` already
// refuses this; `eval` must too, or the two halves of a comparison can be drawn
// from different bytes.
//
// Both sides, because an implementation that verified only the author's inputs
// would pass a one-sided test while scoring unattested distractor bytes — and
// the distractor side is the one a user assembles from their own folders, so it
// is the more likely to move under the tool's feet.
func TestExtractRefusesWhenASourceDocumentChanged(t *testing.T) {
	// Same shape, different bytes, so only the content hash betrays it.
	const changed = "Short one holds zzzz here.\n\nMedium paragraph two holds zzzz and zzzz again, here.\n\n" +
		"The long third paragraph zzzz holds zzzz and zzzz and zzzz once more, with a comma.\n"

	for _, side := range []string{"author", "distractor"} {
		t.Run(side, func(t *testing.T) {
			f := newFixture(t)

			root, snap := f.authorRoot, f.author
			if side == "distractor" {
				root, snap = f.distractorRoot, f.distractor
			}
			calibrate := eligibleIn(snap, corpus.Calibrate)
			if len(calibrate) == 0 {
				t.Fatalf("no %s documents in Calibrate", side)
			}
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(calibrate[0].Path)), []byte(changed), 0o644); err != nil {
				t.Fatal(err)
			}

			set, err := eval.Extract(f.authorRoot, f.author, f.distractorRoot, f.distractor, f.prof, evalRequirements(corpus.Calibrate))
			if err == nil {
				t.Fatalf("extraction accepted a %s document that changed after the snapshot", side)
			}
			if set != nil {
				t.Error("a refused extraction returned a set")
			}
			if !errors.Is(err, eval.ErrSourceChanged) {
				t.Errorf("refusal is %v, want one matching %v", err, eval.ErrSourceChanged)
			}
		})
	}
}

// Requirements are validated before anything is read, and a bad requirement is
// distinguishable from an ordinary insufficient-population refusal — otherwise
// a caller who passed a zero minimum would read it as "your corpus is too
// small" and go looking for more writing.
func TestInvalidRequirementsAreRejected(t *testing.T) {
	f := newFixture(t)

	for name, req := range map[string]eval.Requirements{
		"zero author minimum":         {Split: corpus.Calibrate, MinAuthorSegments: 0, MinDistractorSegments: 1},
		"negative author minimum":     {Split: corpus.Calibrate, MinAuthorSegments: -1, MinDistractorSegments: 1},
		"zero distractor minimum":     {Split: corpus.Calibrate, MinAuthorSegments: 1, MinDistractorSegments: 0},
		"negative distractor minimum": {Split: corpus.Calibrate, MinAuthorSegments: 1, MinDistractorSegments: -1},
		"empty split":                 {Split: "", MinAuthorSegments: 1, MinDistractorSegments: 1},
		"unknown split":               {Split: corpus.Split("holdout"), MinAuthorSegments: 1, MinDistractorSegments: 1},
	} {
		t.Run(name, func(t *testing.T) {
			set, err := eval.Extract(f.authorRoot, f.author, f.distractorRoot, f.distractor, f.prof, req)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if set != nil {
				t.Error("a refused extraction returned a set")
			}
			if !errors.Is(err, eval.ErrInvalidRequirements) {
				t.Errorf("refusal is %v, want one matching %v", err, eval.ErrInvalidRequirements)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Provenance and determinism
// ---------------------------------------------------------------------------

func TestProvenanceIsRecorded(t *testing.T) {
	f := newFixture(t)
	set := f.extract(t, evalRequirements(corpus.Calibrate))

	if set.ProfileID != f.prof.ID {
		t.Errorf("ProfileID = %q, want %q", set.ProfileID, f.prof.ID)
	}
	if set.AuthorSnapshotID != f.author.ID {
		t.Errorf("AuthorSnapshotID = %q, want %q", set.AuthorSnapshotID, f.author.ID)
	}
	if set.DistractorSnapshotID != f.distractor.ID {
		t.Errorf("DistractorSnapshotID = %q, want %q", set.DistractorSnapshotID, f.distractor.ID)
	}
	if set.FeatureSetVersion != features.SetVersion {
		t.Errorf("FeatureSetVersion = %d, want %d", set.FeatureSetVersion, features.SetVersion)
	}
	if set.FeatureManifestDigest == "" {
		t.Error("no feature manifest digest; the version integer alone cannot identify the feature set")
	}
	if set.FeatureManifestDigest != f.prof.FeatureManifestDigest {
		t.Errorf("manifest digest %q differs from the profile's %q; they must describe the same feature set",
			set.FeatureManifestDigest, f.prof.FeatureManifestDigest)
	}
	if set.ID == "" {
		t.Error("the set has no ID")
	}
}

func TestExtractionIsDeterministic(t *testing.T) {
	f := newFixture(t)

	first := f.extract(t, evalRequirements(corpus.Calibrate))
	second := f.extract(t, evalRequirements(corpus.Calibrate))

	if !reflect.DeepEqual(first, second) {
		t.Error("two extractions of the same inputs produced different sets")
	}
	if first.ID != second.ID {
		t.Errorf("set IDs differ: %q and %q", first.ID, second.ID)
	}

	other := f.extract(t, evalRequirements(corpus.Test))
	if other.ID == first.ID {
		t.Error("the Calibrate and Test populations share an ID")
	}
}

// Segment order is the snapshots' own order: every author segment first, in the
// author snapshot's eligible order, then every distractor segment in the
// distractor snapshot's. Contiguity alone is not enough — an implementation
// iterating a map would produce contiguous groups in an arbitrary order, and a
// clustered bootstrap that resamples documents deserves a population it can
// reproduce.
func TestSegmentOrderFollowsTheSnapshots(t *testing.T) {
	f := newFixture(t)
	set := f.extract(t, evalRequirements(corpus.Calibrate))

	var want []string
	for _, d := range eligibleIn(f.author, corpus.Calibrate) {
		for i := 0; i < paragraphsPerDocument; i++ {
			want = append(want, string(eval.ClassAuthor)+"|"+d.ContentHash+"|"+itoa(i))
		}
	}
	for _, d := range eligibleIn(f.distractor, corpus.Calibrate) {
		for i := 0; i < paragraphsPerDocument; i++ {
			want = append(want, string(eval.ClassDistractor)+"|"+d.ContentHash+"|"+itoa(i))
		}
	}

	var got []string
	for _, s := range set.Segments {
		got = append(got, string(s.Class)+"|"+s.DocumentHash+"|"+itoa(s.Index))
	}

	if len(got) != len(want) {
		t.Fatalf("got %d segments, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment %d is %q, want %q — order must follow the snapshots", i, got[i], want[i])
		}
	}
}
