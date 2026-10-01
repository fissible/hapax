package workflow

// #133's two comparisons, isolated so their failure directions are reachable.
//
// The end-to-end fixtures next door establish that a reinterpreted candidate is
// refused, and that a shifted neighbour is not. What they cannot reach is most of
// what the check has to compare: an untouched leaf whose interpretation changes, a
// leaf that disappears, and each measurement field perturbed on its own. Reference
// resolution needs a definition block, and a candidate containing one is two
// leaves the splice gate already refuses — so those cases arrive here, built
// directly, rather than through a candidate that passed every upstream gate.
//
// That is stated plainly because it is the limit of this file: a synthetic
// assembled document does NOT demonstrate that a candidate can reach these states
// through the real loop. It demonstrates that the check catches them if it does.
//
// # The comparison is of INTERPRETATION, not of counts or of the score
//
// Both weaker predicates are refuted by real fixtures next door, with numbers:
//
//   - `![A paragraph][ref]` spliced into `improvesOne` keeps the paragraph
//     SCOREABLE — 26 lexical tokens in context against 29 in isolation — and still
//     improving, 1.338914 to 0.729584 isolated and 0.628054 in context. "Still
//     scoreable and still improved" admits it.
//   - `"![?][!] " + improvesOne` keeps the lexical count IDENTICAL at 28 and the
//     distance identical at 0.5580165378445097, with the same feature deltas and
//     the same band, while the ordered token stream shrinks from 38 to 31. Every
//     count-based and score-based predicate admits it.
//
// Both were constructed by codex in review after I had claimed the second was not
// constructible. It is, and the token stream is what separates them.

import (
	"slices"
	"testing"

	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/score"
	"github.com/fissible/hapax/internal/text"
)

// tokensOf builds a token sequence to compare, with spans that do not matter.
func tokensOf(words ...string) []text.Token {
	out := make([]text.Token, len(words))
	for i, word := range words {
		out[i] = text.Token{
			Span: text.Span{Offset: i * 10, Length: len(word)},
			Text: word, Class: text.Word, Lexical: true,
		}
	}
	return out
}

// Equal-length sequences that differ in ANY recorded property do not match.
//
// The comparison is over text, class and the three flags, and each is perturbed on
// its own — a comparator checking only `Text`, or only `Class`, passes every case
// but its own. Spans are deliberately excluded: a leaf that moved is the ordinary
// case and comparing offsets would report every shifted paragraph as changed.
func TestSameTokenInterpretationComparesEveryRecordedProperty(t *testing.T) {
	base := tokensOf("the", "market", "was", "busy")

	for _, c := range []struct {
		name   string
		mutate func([]text.Token)
	}{
		{"different text", func(x []text.Token) { x[2].Text = "is" }},
		{"different class", func(x []text.Token) { x[1].Class = text.Punctuation }},
		{"contraction set", func(x []text.Token) { x[0].Contraction = true }},
		{"possessive set", func(x []text.Token) { x[1].Possessive = true }},
		{"lexical cleared", func(x []text.Token) { x[3].Lexical = false }},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := tokensOf("the", "market", "was", "busy")
			c.mutate(got)
			if sameTokenInterpretation(base, got) {
				t.Errorf("a sequence differing only in %s compared equal", c.name)
			}
		})
	}

	// Reordered, same multiset. A comparator built on sets or on sorted text
	// passes everything above and fails here.
	reordered := tokensOf("market", "the", "was", "busy")
	if sameTokenInterpretation(base, reordered) {
		t.Error("a reordered sequence compared equal; the comparison is over an ORDER")
	}

	// A different LENGTH, which is the case the real fixtures exhibit: 38 tokens
	// against 31.
	if sameTokenInterpretation(base, tokensOf("the", "market", "was")) {
		t.Error("sequences of different length compared equal")
	}
}

// Identical interpretation at shifted spans matches.
//
// The direction that refuses honest work. Every replacement moves the paragraphs
// after it, so a comparator including spans reports a mismatch for every run with
// a length-changing acceptance.
func TestSameTokenInterpretationIgnoresSpans(t *testing.T) {
	base := tokensOf("the", "market", "was", "busy")
	shifted := tokensOf("the", "market", "was", "busy")
	for i := range shifted {
		shifted[i].Span.Offset += 16
	}
	if !sameTokenInterpretation(base, shifted) {
		t.Error("the same tokens at shifted offsets compared unequal; a +16 shift is " +
			"what an earlier length-changing replacement does to every later paragraph")
	}
	if !sameTokenInterpretation(base, base) {
		t.Error("a sequence did not compare equal to itself")
	}
	// Both empty is equal, and empty against non-empty is not. A paragraph can
	// legitimately tokenize to nothing only if it is not a scoreable leaf, so the
	// asymmetry matters more than the symmetric case.
	if !sameTokenInterpretation(nil, nil) {
		t.Error("two empty sequences compared unequal")
	}
	if sameTokenInterpretation(base, nil) {
		t.Error("a sequence compared equal to nothing")
	}
}

// checkPublication reports the nodes whose published state is not what was
// measured, and reports nothing when every expectation holds.
//
// Driven directly, because most of these states cannot be reached through a
// candidate that passes the upstream gates — see the file comment. The
// expectations for a CHANGED leaf come from the last accepted candidate; those for
// an UNTOUCHED leaf come from the original document in context, including leaves
// that were already below the floor.
func TestCheckPublicationNamesTheNodesThatDoNotReproduce(t *testing.T) {
	const body = "The market was busy and the sellers were loud about it all day.\n\n" +
		"A second paragraph, long enough to be read as prose rather than a heading.\n"
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) != 2 {
		t.Fatalf("the fixture admitted %d leaves; this test needs two", len(leaves))
	}

	// The expectations that DO hold, read from the document itself, so the clean
	// case is not an expectation derived from the thing under test by accident:
	// the tokens come from the document and the measurement is stated here.
	expected := make([]publicationExpectation, 0, 2)
	report := score.Report{Calibrated: true}
	for i, leaf := range leaves {
		tokens, err := doc.RunTokens(leaf)
		if err != nil {
			t.Fatalf("RunTokens: %v", err)
		}
		segment := score.Segment{
			Index: i, LexicalTokens: len(tokens),
			Offset: leaf.Span.Offset, Length: leaf.Span.Length,
			Distance: deviation.Distance{
				Defined: true, Value: 0.5 + float64(i),
				// The CONTRIBUTING set, which is what the acceptance guard
				// compares — `Segment.Features` below is the per-feature delta
				// for display and is a different thing.
				Features: []features.ID{features.WordLengthMean, features.CommaDensity},
			},
			Features: []score.FeatureDelta{
				{Feature: features.WordLengthMean, Deviation: 1.5, Defined: true, Direction: "above"},
				{Feature: features.CommaDensity, Deviation: -0.5, Defined: true, Direction: "below"},
			},
			Band: eval.BandOutcome{Defined: true, Band: eval.BandDrifting},
		}
		report.Segments = append(report.Segments, segment)
		expected = append(expected, publicationExpectation{
			NodeID: "node-" + string(rune('a'+i)), Span: leaf.Span,
			Tokens: tokens, Measured: segment,
			// EXPLICIT, so the two cases below perturb a value that was set
			// rather than one the implementation defaulted. The documented
			// default is still exercised, by the uncalibrated and below-floor
			// tests further down, which leave both fields zero.
			Role: leaf.Role, Containers: slices.Clone(leaf.Containers),
		})
	}

	t.Run("every expectation holds", func(t *testing.T) {
		mismatches, err := checkPublication(doc, report, expected)
		if err != nil {
			t.Fatalf("checkPublication: %v", err)
		}
		if len(mismatches) != 0 {
			t.Errorf("a document that reproduces every expectation reported %v", mismatches)
		}
	})

	for _, c := range []struct {
		name   string
		mutate func([]publicationExpectation, *score.Report)
		want   string
	}{
		{
			// An UNTOUCHED leaf whose interpretation changed. Unreachable through
			// a real candidate as far as I could construct, and the case
			// obligation 5 exists for.
			name: "an untouched leaf is interpreted differently",
			mutate: func(e []publicationExpectation, _ *score.Report) {
				e[1].Tokens = append([]text.Token(nil), e[1].Tokens...)
				e[1].Tokens[0].Text = "Another"
			},
			want: "node-b",
		},
		{
			// The ROLE alone. The splice gate checks this per candidate against
			// the ORIGINAL document; it cannot establish it for the combined one,
			// which is what this checker owns.
			name: "the leaf's role changed",
			mutate: func(e []publicationExpectation, _ *score.Report) {
				e[0].Role = text.RoleHeading
			},
			want: "node-a",
		},
		{
			// The container PATH's identity at the same LENGTH, so a comparison
			// on length alone cannot pass: one container either way.
			name: "the leaf's container path changed at the same length",
			mutate: func(e []publicationExpectation, _ *score.Report) {
				e[0].Containers = []text.ContainerKind{text.ContainerList}
				if len(e[0].Containers) != 1 {
					panic("this case needs a one-element path to keep the length equal")
				}
			},
			want: "node-a",
		},
		{
			name: "a leaf disappeared",
			mutate: func(e []publicationExpectation, _ *score.Report) {
				e[1].Span = text.Span{Offset: len(body) + 100, Length: 10}
			},
			want: "node-b",
		},
		{
			name: "the distance is no longer defined",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Distance.Defined = false
			},
			want: "node-a",
		},
		{
			name: "the distance moved",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Distance.Value += 0.25
			},
			want: "node-a",
		},
		{
			// The CONTRIBUTING set's identity, at the same length. Measured by
			// codex: a checker ignoring `Distance.Features` entirely passed the
			// whole suite, because nothing here looked at it — the earlier version
			// of this case perturbed `Segment.Features`, which is the display
			// delta and a different field.
			name: "a contributing feature identity changed",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Distance.Features = []features.ID{
					features.WordLengthMean, features.SemicolonDensity,
				}
			},
			want: "node-a",
		},
		{
			// A delta IDENTITY changed without changing the slice length, which a
			// checker comparing only `len(Segment.Features)` cannot see.
			name: "a feature delta identity changed at the same length",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Features = []score.FeatureDelta{
					{Feature: features.WordLengthMean, Deviation: 1.5, Defined: true, Direction: "above"},
					{Feature: features.SemicolonDensity, Deviation: -0.5, Defined: true, Direction: "below"},
				}
			},
			want: "node-a",
		},
		{
			// The delta VALUE, identities preserved. Measured by codex: a
			// comparator checking feature ids and ignoring `Deviation`, `Defined`
			// and `Direction` passed the whole suite, because every case here
			// perturbed only the id.
			name: "a feature delta's deviation changed",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Features[0].Deviation = 2.25
			},
			want: "node-a",
		},
		{
			name: "a feature delta is no longer defined",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Features[1].Defined = false
			},
			want: "node-a",
		},
		{
			name: "a feature delta's direction changed",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Features[1].Direction = "above"
			},
			want: "node-a",
		},
		{
			name: "the band label changed",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Band.Band = eval.BandNotYou
			},
			want: "node-a",
		},
		{
			// DEFINEDNESS ALONE, with the label and the reason untouched. The
			// earlier version of this case moved `Defined` and `Reason` together,
			// so a comparator reading label and reason and ignoring definedness
			// passed it — measured by codex.
			name: "the band is no longer defined",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Band.Defined = false
			},
			want: "node-a",
		},
		{
			// And the reason alone, so neither field stands in for the other.
			name: "the band reason changed",
			mutate: func(_ []publicationExpectation, r *score.Report) {
				r.Segments[0].Band.Reason = eval.ReasonUncalibrated
			},
			want: "node-a",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			perturbed := append([]publicationExpectation(nil), expected...)
			moved := report
			moved.Segments = append([]score.Segment(nil), report.Segments...)
			// DEEP copy of each segment's deltas. `append` copies the segment
			// structs and leaves their `Features` slices sharing one backing array
			// with `expected[].Measured.Features`, so perturbing a delta field
			// changes BOTH sides and the comparison sees no difference — the three
			// delta cases below would then fail against a correct comparator
			// rather than catching a weak one.
			for i := range moved.Segments {
				moved.Segments[i].Features = append([]score.FeatureDelta(nil),
					moved.Segments[i].Features...)
			}
			c.mutate(perturbed, &moved)

			mismatches, err := checkPublication(doc, moved, perturbed)
			if err != nil {
				t.Fatalf("checkPublication: %v", err)
			}
			if len(mismatches) != 1 || mismatches[0] != c.want {
				t.Errorf("mismatches = %v, want exactly [%s]", mismatches, c.want)
			}
		})
	}
}

// An uncalibrated run is not a mismatch.
//
// `--paragraphs` on an uncalibrated store measures distances with no band, which
// is legitimate operation rather than a reinterpretation. A check demanding a
// defined band refuses it, and that is the over-refusal this case exists to stop.
func TestCheckPublicationAcceptsLegitimateUncalibratedOperation(t *testing.T) {
	const body = "The market was busy and the sellers were loud about it all day.\n"
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	leaf := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()[0]
	tokens, err := doc.RunTokens(leaf)
	if err != nil {
		t.Fatalf("RunTokens: %v", err)
	}
	segment := score.Segment{
		Index: 0, LexicalTokens: len(tokens),
		Offset: leaf.Span.Offset, Length: leaf.Span.Length,
		Distance: deviation.Distance{Defined: true, Value: 0.5},
		// No band, because nothing calibrated one.
		Band: eval.BandOutcome{Defined: false, Reason: eval.ReasonUncalibrated},
	}

	mismatches, err := checkPublication(doc,
		score.Report{Calibrated: false, Segments: []score.Segment{segment}},
		[]publicationExpectation{{
			NodeID: "node-a", Span: leaf.Span, Tokens: tokens, Measured: segment,
		}})
	if err != nil {
		t.Fatalf("checkPublication: %v", err)
	}
	if len(mismatches) != 0 {
		t.Errorf("an uncalibrated run reported %v; measuring without a band is how "+
			"--paragraphs works on an uncalibrated store", mismatches)
	}
}

// A paragraph that was below the floor before and after is not a mismatch.
//
// Untouched leaves take their expectations from the original document in context,
// and that includes the ones the floor already skipped. Expecting a measurement
// for them would report every draft with a short paragraph as a mismatch.
func TestCheckPublicationAcceptsAParagraphThatWasAlreadyBelowTheFloor(t *testing.T) {
	const body = "Too short.\n\n" +
		"A second paragraph, long enough to be read as prose rather than a heading.\n"
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) != 2 {
		t.Fatalf("the fixture admitted %d leaves; this test needs two", len(leaves))
	}
	short, err := doc.RunTokens(leaves[0])
	if err != nil {
		t.Fatalf("RunTokens: %v", err)
	}

	mismatches, err := checkPublication(doc, score.Report{Calibrated: true},
		[]publicationExpectation{{
			NodeID: "node-a", Span: leaves[0].Span, Tokens: short, BelowFloor: true,
		}})
	if err != nil {
		t.Fatalf("checkPublication: %v", err)
	}
	if len(mismatches) != 0 {
		t.Errorf("a paragraph below the floor before and after reported %v", mismatches)
	}
}
