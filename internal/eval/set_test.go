package eval_test

import (
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
)

// Extract reads both corpora off disk. The author's held-out segments are
// already in the graph index wrote, and re-reading them would evaluate whatever
// is on disk NOW rather than what the profile was fitted against — so a caller
// that already holds its segments needs a way in that does not go through the
// filesystem. NewSet is that way in, and it must apply the same rules Extract
// applies after reading, or the two paths are two different contracts.
func TestASetCanBeMadeFromSegmentsAlreadyHeld(t *testing.T) {
	author, distractor := setSegments(t)
	built, err := eval.NewSet(setIdentity(), author, distractor, setRequirements())
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	if built.AuthorSegments != len(author) || built.DistractorSegments != len(distractor) {
		t.Errorf("counted %d author and %d distractor segments, given %d and %d",
			built.AuthorSegments, built.DistractorSegments, len(author), len(distractor))
	}
	if len(built.Segments) != len(author)+len(distractor) {
		t.Errorf("the set holds %d segments, given %d", len(built.Segments), len(author)+len(distractor))
	}
	if built.ID == "" {
		t.Error("the set has no identity")
	}
	if built.Split != setRequirements().Split {
		t.Errorf("split = %q", built.Split)
	}
}

// Extract derives a set's identity from the two SNAPSHOT identities rather than
// from the segments, and gets away with it because it read the segments out of
// those snapshots itself. NewSet cannot: the caller supplies both, so nothing
// stops a set claiming one population and holding another.
//
// The fix is not a second identity rule — two ways to compute one ID is worse
// than the gap. It is a consistency check: every distractor segment must be a
// member of the pool the set declares, and the pool's identity is already over
// its members' content hashes, so membership reaches the set identity the same
// way it always did.
func TestASetRefusesSegmentsFromOutsideWhatItDeclares(t *testing.T) {
	author, distractor := setSegments(t)

	t.Run("a distractor the pool does not contain", func(t *testing.T) {
		stranger := append([]eval.Segment(nil), distractor...)
		stranger[0].DocumentHash = strings.Repeat("f", 64)
		if _, err := eval.NewSet(setIdentity(), author, stranger, setRequirements()); err == nil {
			t.Error("accepted a distractor the declared pool does not contain")
		}
	})

	// The author side has exactly the same gap and it is the more dangerous
	// one. Note what AuthorMembers must be: the HELD-OUT documents, not the
	// snapshot's. "Belongs to the snapshot" would admit a Train document, which
	// is text the profile was fitted on — a training-set measurement wearing a
	// held-out measurement's identity, and nothing else here would notice.
	t.Run("an author segment outside the held-out set", func(t *testing.T) {
		stranger := append([]eval.Segment(nil), author...)
		stranger[0].DocumentHash = strings.Repeat("e", 64)
		if _, err := eval.NewSet(setIdentity(), stranger, distractor, setRequirements()); err == nil {
			t.Error("accepted an author segment the declared held-out set does not contain")
		}
	})

	// The specific case that a snapshot-wide check would let through: a
	// document that IS in the author's corpus but was used to fit the profile.
	// It is indistinguishable from a held-out one except by membership.
	t.Run("an author segment from a document the profile was fitted on", func(t *testing.T) {
		trained := append([]eval.Segment(nil), author...)
		trained[0].DocumentHash = trainedHash
		if _, err := eval.NewSet(setIdentity(), trained, distractor, setRequirements()); err == nil {
			t.Error("accepted a segment from a document the profile was fitted on")
		}
	})

	// And a declaration that contradicts itself is refused before anything is
	// measured against it.
	t.Run("a pool declaring the same member twice", func(t *testing.T) {
		declared := setIdentity()
		declared.DistractorMembers = []string{distractorHashA, distractorHashA, distractorHashB}
		if _, err := eval.NewSet(declared, author, distractor, setRequirements()); err == nil {
			t.Error("accepted a pool declaring one member twice")
		}
	})
	t.Run("a declaration with no members at all", func(t *testing.T) {
		declared := setIdentity()
		declared.DistractorMembers = nil
		if _, err := eval.NewSet(declared, author, distractor, setRequirements()); err == nil {
			t.Error("accepted a set whose declared pool is empty")
		}
	})
}

// And the identity follows what was declared, which is what the pool ID and the
// snapshot ID make content-derived on the caller's behalf.
func TestASetsIdentityFollowsWhatItDeclares(t *testing.T) {
	author, distractor := setSegments(t)
	first, err := eval.NewSet(setIdentity(), author, distractor, setRequirements())
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	again, err := eval.NewSet(setIdentity(), author, distractor, setRequirements())
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	if first.ID != again.ID {
		t.Errorf("the same declaration produced two identities, %q and %q", first.ID, again.ID)
	}

	other := setIdentity()
	other.DistractorPoolID = strings.Repeat("a", 64)
	changed, err := eval.NewSet(other, author, distractor, setRequirements())
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	if changed.ID == first.ID {
		t.Error("a different distractor pool produced the same set identity")
	}
}

// A set that cannot support the measurement is refused rather than measured:
// the minimums are declared, and a bootstrap over too few clusters produces a
// confident-looking number about nothing.
func TestASetRefusesAPopulationTooSmallToMeasure(t *testing.T) {
	author, distractor := setSegments(t)
	for _, c := range []struct {
		name               string
		author, distractor []eval.Segment
	}{
		{"no author segments", nil, distractor},
		{"no distractor segments", author, nil},
		{"fewer author segments than declared", author[:1], distractor},
		{"fewer distractor segments than declared", author, distractor[:1]},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := eval.NewSet(setIdentity(), c.author, c.distractor, setRequirements()); err == nil {
				t.Error("built a set too small to measure over")
			}
		})
	}
}

// A segment carries the class it belongs to, so handing NewSet an author
// segment among the distractors is a caller error rather than a relabelling
// opportunity: the class is evidence about where the text came from.
func TestASetRefusesASegmentInTheWrongClass(t *testing.T) {
	author, distractor := setSegments(t)
	misfiled := append([]eval.Segment(nil), distractor...)
	misfiled[0].Class = eval.ClassAuthor

	if _, err := eval.NewSet(setIdentity(), author, misfiled, setRequirements()); err == nil {
		t.Error("accepted an author segment among the distractors")
	}
}

// The clustering key is the document hash — a distractor has no path to group
// by, and the bootstrap resamples documents rather than paragraphs. A segment
// without one cannot be clustered and must not be silently pooled with the
// others under an empty key.
func TestASegmentWithoutADocumentHashIsRefused(t *testing.T) {
	author, distractor := setSegments(t)
	anonymous := append([]eval.Segment(nil), distractor...)
	anonymous[0].DocumentHash = ""

	if _, err := eval.NewSet(setIdentity(), author, anonymous, setRequirements()); err == nil {
		t.Error("accepted a segment with nothing to cluster it by")
	}
}

func setRequirements() eval.Requirements {
	return eval.Requirements{Split: corpus.Test, MinAuthorSegments: 2, MinDistractorSegments: 2}
}

// setIdentity is what a caller declares about a set: whose profile, which
// author snapshot, which distractor pool, and the pool's membership so the
// segments can be checked against it. The pool carries no paths, so its members
// are content hashes and nothing else.
func setIdentity() eval.SetIdentity {
	return eval.SetIdentity{
		Fitted:           fittedFixture(),
		AuthorSnapshotID: strings.Repeat("1", 64),
		// The HELD-OUT documents only. A snapshot also holds train and
		// calibrate documents, and validating against the snapshot rather than
		// against this set would admit text the profile was fitted on.
		AuthorMembers:     []string{authorHashA, authorHashB},
		DistractorPoolID:  strings.Repeat("2", 64),
		DistractorMembers: []string{distractorHashA, distractorHashB},
	}
}

const (
	distractorHashA = "aa11111111111111111111111111111111111111111111111111111111111111"
	distractorHashB = "bb22222222222222222222222222222222222222222222222222222222222222"
	authorHashA     = "cc33333333333333333333333333333333333333333333333333333333333333"
	authorHashB     = "dd44444444444444444444444444444444444444444444444444444444444444"
	// A document of the author's corpus that is NOT held out: the profile was
	// fitted on it, so measuring against it measures the training set.
	trainedHash = "ee55555555555555555555555555555555555555555555555555555555555555"
)

// setSegments returns two author documents and two distractor documents, one
// segment each, so a per-class minimum of two is met by two CLUSTERS rather
// than by two paragraphs of one document.
func setSegments(t *testing.T) (author, distractor []eval.Segment) {
	t.Helper()
	for i, hash := range []string{authorHashA, authorHashB} {
		author = append(author, eval.Segment{
			Class: eval.ClassAuthor, DocumentHash: hash, DocumentPath: "essays/a.md",
			Index: 0, LexicalTokens: 50, Vector: vectorFixture(t, float64(i)),
		})
	}
	for i, hash := range []string{distractorHashA, distractorHashB} {
		distractor = append(distractor, eval.Segment{
			// No path: the pool keeps no path representation, so a distractor
			// segment has nothing to carry there.
			Class: eval.ClassDistractor, DocumentHash: hash,
			Index: 0, LexicalTokens: 50, Vector: vectorFixture(t, float64(i)+10),
		})
	}
	return author, distractor
}

func vectorFixture(t *testing.T, offset float64) features.Vector {
	t.Helper()
	vector := features.Vector{SetVersion: features.SetVersion, Tokens: 60, LexicalTokens: 50}
	for i, definition := range features.Definitions() {
		vector.Values = append(vector.Values, features.FeatureValue{
			ID: definition.ID, Value: offset + float64(i), Defined: true,
			SamplingVariance: 0.01, SamplingVarianceDefined: true,
		})
	}
	return vector
}

func fittedFixture() profile.Fitted {
	fitted := profile.Fitted{
		ID: strings.Repeat("3", 64), Unit: profile.UnitParagraph,
		FeatureSetVersion: features.SetVersion, FeatureManifestDigest: features.ManifestDigest(),
		MinParagraphLexicalTokens: 1,
	}
	for _, definition := range features.Definitions() {
		fitted.Stats = append(fitted.Stats, profile.Stats{
			Feature: definition.ID, N: 40, Mean: 1, Variance: 1, Defined: true,
			VarianceDefined: true, MinObservations: 30,
		})
	}
	return fitted
}
