package rewrite_test

// #143. A candidate may not be more than `ExpansionCeiling` times the ORIGINAL
// paragraph's lexical tokens.
//
// # Contract
//
// Stated on `ExpansionCeiling`; nothing here restates it. What these tests pin:
//
//   - the bound is RELATIVE and measured in lexical tokens;
//   - the anchor is the ORIGINAL paragraph, not the advancing current, so N
//     accepted passes cannot compound to ceiling^N;
//   - it is ONE-SIDED: no candidate is refused for being shorter;
//   - a candidate exactly on the bound is accepted;
//   - the attempt records both counts and the bound that was applied.
//
// # Evidence: what the score does and does not do about length
//
// Measured through the real `workflow.Runner` against a real store and release,
// reading the distances the loop recorded out of `LoadRewriteAttempt` — not
// inferred from feature values, which was an earlier error of mine that measured
// the wrong layer. `paragraphOne` sits at d = 1.338914001.
//
//	duplicating the ORIGINAL        duplicating an IMPROVING candidate
//	  2.0x   1.338914001  refused     1.9x   0.686365334  accepted
//	  5.0x   1.553020197  refused     4.8x   0.787894687  accepted
//	 20.0x   1.553020197  refused    19.3x   1.002000883  accepted
//	100.0x   1.553020197  refused    48.3x   1.002000883  accepted
//	                                 96.6x   1.002000883  accepted
//	                                386.2x   1.002000883  accepted
//
// Two things follow. Length can reach d — `Standardize` divides by
// sqrt(V + S(n)), so a fixed per-token value CAN yield a different standardized
// value when its supplied sampling variance changes and its numerator is
// non-zero, and in both columns above the score moved WORSE.
// And along these trajectories it PLATEAUS: past roughly 19x the value is
// constant to nine decimal places through 386x, because `Reference.Transform`
// ranks against a finite reference and the insertion positions stop moving.
//
// So d's sensitivity to length runs out along these trajectories, and past the
// plateau it is exactly indifferent. Whether such an expansion is refused turns
// on whether the plateau value still improves on what it is compared against — a
// property of the candidate and the reference rather than a policy. That is the
// gap this ceiling fills.
//
// Note the two guards compare against different things: the distance guard is
// `candidate <= current - Epsilon` against the ADVANCING current, while this
// ceiling anchors on the ORIGINAL. The columns above are all first passes, so
// current and original coincide there.
//
// # Evidence: length can also make the score BETTER
//
// Added after the implementation, because the two columns above invite the
// reading that expansion is self-penalizing and so barely needs a gate. That
// reading is not supported.
//
// `Standardize` CONSUMES a supplied sampling variance rather than deriving one
// from the token count. Where that model-estimated uncertainty shrinks as the
// segment grows AND the numerator (x - mu) is non-zero, the standardized value
// moves away from zero toward (x - mu)/sqrt(V), which is finite while V > 0.
// Neither condition is universal: a feature at zero density can carry zero
// sampling variance and not move at all. Whether any movement helps depends on
// where the reference's own values sit.
//
// Measured through the real `Standardize` and `Reference.Transform`, holding every
// per-token value fixed and growing only the segment:
//
//	reference values centred on 0.0     reference values centred on 1.0
//	  n=  25  z 0.707107  d 0.356866      n=  25  z 0.707107  d 0.356866
//	  n= 100  z 0.894427  d 0.356866      n= 100  z 0.894427  d 0.356866
//	  n= 400  z 0.970143  d 0.356866      n= 400  z 0.970143  d 0.054882
//	  n=1600  z 0.992278  d 0.356866      n=1600  z 0.992278  d 0.013491
//
// Centred on zero the query is already past the whole cluster and the rank cannot
// move — the plateau again. Centred on 1.0 the query is BELOW the cluster, so
// lengthening carries it toward the middle ranks and d falls by a factor of 26,
// bought with length alone.
//
// # Consequence: the pipeline PERMITS off-centre references
//
// `profile.Build` fits on `corpus.Train`, and `deviation.BuildReference` requires
// `corpus.Calibrate` and standardizes those paragraphs against the Train-fitted
// statistics. Separate splits PERMIT an off-centre reference; they do not
// guarantee one, and skew and segment-dependent denominators contribute too. The
// medians below do not isolate the split difference as their cause.
//
// Measured on this package's own fixture reference, the median z per feature:
//
//	semicolon_density  -0.5458   (its minimum too: most paragraphs carry none)
//	colon_density      -0.3837   (the same)
//	word_length_mean   -0.2658
//	clause_marker_rate -0.1336
//	comma_density      -0.0410
//	function_word_rate +0.2075
//
// So these are not centred, and the SIGN of any length-driven movement depends on
// which side of the cluster a paragraph sits. Either way no per-token rate
// changed: the movement records shrinking model-estimated sampling uncertainty,
// not a change in how the author writes. That supplies an additional reason for
// caution about generous expansion allowances; it does not derive the chosen
// multiplier.
//
// What is NOT established here: that a real paragraph from a real corpus improves
// by lengthening. The 26x case uses a CONSTRUCTED reference, it spans 25 to 1600
// tokens and so says nothing about behaviour near 1.5, and the two real paragraphs
// measured above both got worse. #143 carries this measurement and the protocol
// for the part that is still missing.
//
// # Decision
//
// 1.5, chosen by the maintainer from measured alternatives rather than derived;
// `ExpansionCeilingDerived` says so. The reviewer recommended the gate and left
// the multiplier explicitly as a product decision. The existing suite's one
// accepted expansion, `lengthensOne`, is about 1.1x, so 1.5 leaves it alone.
//
// # Unresolved
//
// The multiplier itself. Nothing has measured how often a useful rewrite is lost
// to it, or how often a harmful expansion still escapes beneath it, and the
// corpus cannot supply either without real provider candidates and author
// judgments made without showing scores. #143 records what such a measurement
// would need.
//
// Also unresolved, and deliberately not fixed here: whether the plateaus are
// themselves a scoring limitation. Replacing the empirical rank transform could
// change them, but would not by itself establish an acceptable expansion policy —
// that is a relation between original and candidate, where the score measures
// each against a profile.

import (
	"reflect"
	"testing"

	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/score"
)

// scoredTokens is `scored` with the lexical token count under the test's control,
// which the shipped helper fixes at 12.
func scoredTokens(distance float64, tokens int) score.Report {
	return score.Report{
		ProfileID: "profile-under-test", Calibrated: true,
		Segments: []score.Segment{{
			Index: 0, LexicalTokens: tokens,
			Distance: deviation.Distance{
				Value: distance, Defined: true,
				Features:    []features.ID{features.WordLengthMean, features.CommaDensity},
				ScoredTiers: []features.Tier{features.TierA},
			},
			Band: eval.BandOutcome{Band: eval.BandDrifting, Defined: true, Distance: distance},
		}},
	}
}

// withTokens copies a report with the segment's lexical count replaced, so a
// fixture built by `scored`, `banded` or `inRangeAt` can vary its length without
// losing the band those helpers set.
func withTokens(report score.Report, tokens int) score.Report {
	out := report
	out.Segments = append([]score.Segment(nil), report.Segments...)
	out.Segments[0].LexicalTokens = tokens
	return out
}

// The declared figure, and that it is declared.
func TestTheExpansionCeilingIsDeclaredAndNotDerived(t *testing.T) {
	if rewrite.ExpansionCeiling != 1.5 {
		t.Errorf("ExpansionCeiling = %v, want 1.5", rewrite.ExpansionCeiling)
	}
	if rewrite.ExpansionCeilingDerived {
		t.Error("ExpansionCeilingDerived is true, so a measurement derives the " +
			"multiplier; nothing has measured the rewrites it loses or the " +
			"expansions that still escape beneath it")
	}
}

// The boundary. Exactly on the ceiling is accepted; one token past it is refused.
//
// Token counts are chosen so the bound lands on an integer: 20 original tokens at
// 1.5 is exactly 30, so 30 is the last accepted count and 31 the first refused.
func TestTheCeilingIsInclusiveAndRefusesTheTokenPastIt(t *testing.T) {
	for _, c := range []struct {
		name           string
		originalTokens int
		tokens         int
		accepted       bool
	}{
		// 20 tokens puts the bound on exactly 30.
		{"well under", 20, 21, true},
		{"one token under the bound", 20, 29, true},
		{"exactly on the bound", 20, 30, true},
		{"one token past the bound", 20, 31, false},
		{"far past the bound", 20, 400, false},
		// 21 tokens puts it on 31.5, which no candidate can land on. The
		// permissible maximum is 31: rounding the bound UP to 32 would admit a
		// candidate above 1.5x, and rounding down to 31 is the same as truncating
		// here, so these two rows separate truncation from round-to-nearest and
		// from a ceiling.
		{"the token below a fractional bound", 21, 31, true},
		{"the token above a fractional bound", 21, 32, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			originalTokens := c.originalTokens
			// Every candidate IMPROVES, so the distance guard cannot be what
			// refuses it and the outcome isolates the ceiling.
			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{
					original: scoredTokens(0.50, originalTokens),
					better:   scoredTokens(0.40, c.tokens),
				},
				[]string{better}, passingGate())

			got := run(t, loop)

			if got.Changed != c.accepted {
				t.Fatalf("changed = %v, want %v (%d tokens against an original of "+
					"%d, a ratio of %.3f and a ceiling of %v)",
					got.Changed, c.accepted, c.tokens, originalTokens,
					float64(c.tokens)/float64(originalTokens), rewrite.ExpansionCeiling)
			}
			if len(store.attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			want := rewrite.RejectionNone
			if !c.accepted {
				want = rewrite.RejectionExpanded
			}
			if code := store.attempts[0].Rejection; code != want {
				t.Errorf("rejection = %q, want %q", code, want)
			}
		})
	}
}

// One-sided. A shorter candidate is never refused by this gate, however much
// shorter, so long as it is still scoreable.
func TestNoCandidateIsRefusedForBeingShorter(t *testing.T) {
	const originalTokens = 60
	for _, tokens := range []int{59, 30, 12, 1} {
		loop, _, _, _, store := loopOver(t,
			map[string]score.Report{
				original: scoredTokens(0.50, originalTokens),
				better:   scoredTokens(0.40, tokens),
			},
			[]string{better}, passingGate())

		got := run(t, loop)

		if len(store.attempts) == 0 {
			t.Fatalf("%d tokens against an original of %d recorded no attempt",
				tokens, originalTokens)
		}
		if !got.Changed {
			t.Errorf("%d tokens against an original of %d was refused as %q; the "+
				"ceiling is one-sided", tokens, originalTokens, store.attempts[0].Rejection)
		}
		if store.attempts[0].Rejection == rewrite.RejectionExpanded {
			t.Errorf("%d tokens, which is SHORTER than %d, was refused as %q",
				tokens, originalTokens, rewrite.RejectionExpanded)
		}
	}
}

// The anchor is the ORIGINAL, so accepted passes cannot compound.
//
// Without this, a ceiling applied against the advancing current permits
// ceiling^N over N acceptances — the same defect the language-growth guard
// anchors on the original to avoid. Two passes at 1.4x of their predecessor
// would reach 1.96x of the original.
func TestTheCeilingIsMeasuredAgainstTheOriginalAndNotTheAdvancingCurrent(t *testing.T) {
	const originalTokens = 20
	// 28 is 1.40x of 20 and is accepted. 39 is 1.39x of 28 — under the ceiling
	// against the ADVANCING current — and 1.95x of the original, so it must be
	// refused.
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original:  scoredTokens(0.50, originalTokens),
			better:    scoredTokens(0.40, 28),
			betterYet: scoredTokens(0.30, 39),
		},
		[]string{better, betterYet}, passingGate())

	got := run(t, loop)

	if len(store.attempts) < 2 {
		t.Fatalf("%d attempts recorded; this needs two", len(store.attempts))
	}
	if !store.attempts[0].Accepted {
		t.Fatalf("the first candidate at 1.40x was refused as %q; the fixture needs "+
			"it accepted so the second is measured against an ADVANCED current",
			store.attempts[0].Rejection)
	}
	if store.attempts[1].Accepted {
		t.Error("the second candidate was accepted; at 39 tokens it is 1.95x of the " +
			"original 20 and must be refused, even though it is only 1.39x of the " +
			"28-token current")
	}
	if code := store.attempts[1].Rejection; code != rewrite.RejectionExpanded {
		t.Errorf("the second rejection = %q, want %q", code, rewrite.RejectionExpanded)
	}
	// The AUDIT must anchor on the original too. A correct rejection does not
	// establish that the recorded original count is the original's rather than
	// the advancing current's — 28 here would be the current.
	if store.attempts[1].OriginalLexicalTokens != originalTokens {
		t.Errorf("the second attempt records an original of %d, want %d; the audit "+
			"is anchored on the advancing current",
			store.attempts[1].OriginalLexicalTokens, originalTokens)
	}
	if store.attempts[1].CandidateLexicalTokens != 39 {
		t.Errorf("the second attempt records a candidate of %d, want 39",
			store.attempts[1].CandidateLexicalTokens)
	}
	// And the accepted text is the FIRST candidate, not the second.
	if got.Text != better {
		t.Errorf("text = %q, want the first candidate", got.Text)
	}
}

// TWO acceptances at different lengths, which nothing else covers.
//
// Every other multi-pass fixture here refuses its second candidate, so two
// mutants survived them: one enforcing the ceiling against the advancing current
// IN ADDITION to the original, and one assigning the original count from
// `currentScored` only on acceptance. Both need a second ACCEPTED pass to show.
//
// 20 -> 10 -> 29 is the shape that kills the first: 29 is 2.9x the accepted
// 10-token current and well inside 1.5x of the original 20, so a ceiling applied
// to the current would refuse it. 20 -> 28 -> 29 keeps both passes inside both
// readings, so it isolates the recorded counts instead.
func TestTwoAcceptedPassesAtDifferentLengths(t *testing.T) {
	for _, c := range []struct {
		name          string
		first, second int
	}{
		{"shortening then lengthening", 10, 29},
		{"lengthening twice", 28, 29},
	} {
		t.Run(c.name, func(t *testing.T) {
			const originalTokens = 20
			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{
					original:  scoredTokens(0.50, originalTokens),
					better:    scoredTokens(0.40, c.first),
					betterYet: scoredTokens(0.30, c.second),
				},
				[]string{better, betterYet}, passingGate())

			got := run(t, loop)

			if len(store.attempts) != 2 {
				t.Fatalf("%d attempts recorded; this fixture offers two candidates "+
					"and both must be ACCEPTED for the test to say anything",
					len(store.attempts))
			}
			for i, attempt := range store.attempts {
				if !attempt.Accepted {
					t.Fatalf("attempt %d was refused as %q; both passes improve and "+
						"both are inside the ceiling against the ORIGINAL",
						i, attempt.Rejection)
				}
				// The original's count on BOTH records, never the advancing
				// current's — which would be 10 or 28 on the second.
				if attempt.OriginalLexicalTokens != originalTokens {
					t.Errorf("attempt %d records an original of %d, want %d",
						i, attempt.OriginalLexicalTokens, originalTokens)
				}
			}
			if store.attempts[0].CandidateLexicalTokens != c.first {
				t.Errorf("the first candidate count is %d, want %d",
					store.attempts[0].CandidateLexicalTokens, c.first)
			}
			if store.attempts[1].CandidateLexicalTokens != c.second {
				t.Errorf("the second candidate count is %d, want %d",
					store.attempts[1].CandidateLexicalTokens, c.second)
			}
			if got.Text != betterYet {
				t.Errorf("text = %q, want the second candidate", got.Text)
			}
		})
	}
}

// An expansion refusal refuses the CANDIDATE, not the run.
//
// Every other over-bound fixture here offers its refused candidate last, so a
// `break` after recording an expansion refusal passed the whole package. The loop
// must ask again.
func TestAnExpansionRefusalDoesNotEndTheLoop(t *testing.T) {
	const originalTokens = 20
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original:  scoredTokens(0.50, originalTokens),
			better:    scoredTokens(0.40, 31), // over the bound, refused
			betterYet: scoredTokens(0.30, 29), // inside it, accepted
		},
		[]string{better, betterYet}, passingGate())

	got := run(t, loop)

	if len(store.attempts) != 2 {
		t.Fatalf("%d attempts recorded; the loop stopped at the expansion refusal "+
			"instead of asking again", len(store.attempts))
	}
	if store.attempts[0].Rejection != rewrite.RejectionExpanded {
		t.Fatalf("the first attempt was refused as %q, want %q",
			store.attempts[0].Rejection, rewrite.RejectionExpanded)
	}
	if !store.attempts[1].Accepted {
		t.Errorf("the second attempt was refused as %q; at 29 tokens it is inside "+
			"the ceiling and it improves", store.attempts[1].Rejection)
	}
	// A refusal advances nothing, so the second attempt's current is still the
	// original's — including its count.
	if store.attempts[1].CurrentDistance != 0.50 {
		t.Errorf("the second attempt's current distance is %v, want the original's "+
			"0.50; the refused candidate advanced the current",
			store.attempts[1].CurrentDistance)
	}
	for i, attempt := range store.attempts {
		if attempt.OriginalLexicalTokens != originalTokens {
			t.Errorf("attempt %d records an original of %d, want %d",
				i, attempt.OriginalLexicalTokens, originalTokens)
		}
	}
	if got.Text != betterYet {
		t.Errorf("text = %q, want the second candidate", got.Text)
	}
}

// Expansion is reported before the DISTANCE comparison too.
//
// An implementation that only consulted the ceiling for candidates that already
// improve passed everything: an over-bound candidate that ties or regresses then
// reports `not-improved`, which is the wrong complaint and lower in the declared
// precedence.
func TestExpansionIsReportedBeforeTheDistanceRefusal(t *testing.T) {
	for _, c := range []struct {
		name     string
		distance float64
	}{
		{"an exact tie", 0.50},
		{"a regression", 0.60},
	} {
		t.Run(c.name, func(t *testing.T) {
			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{
					original: scoredTokens(0.50, 20),
					better:   scoredTokens(c.distance, 31),
				},
				[]string{better}, passingGate())

			run(t, loop)

			if len(store.attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			if code := store.attempts[0].Rejection; code != rewrite.RejectionExpanded {
				t.Errorf("rejection = %q, want %q: the candidate is over the bound "+
					"AND does not improve, and the ceiling is reported first",
					code, rewrite.RejectionExpanded)
			}
		})
	}
}

// The counts are MEASUREMENTS, so they are recorded wherever they are known —
// including on the refusals that never reach the gate block.
//
// Moving the three assignments inside that block passed everything, and those
// paths then recorded 0 -> 0 for candidates whose lexical counts the scorer had
// already supplied.
//
// The convention this pins, because it is a decision and not an accident: the two
// COUNTS are recorded whenever the candidate yields exactly one segment, whatever
// refusal wins; and `ExpansionCeiling` records the policy IN FORCE, so it is the
// constant on every attempt rather than a trace of whether the gate ran. Unlike
// `Splice`, which has a not-recorded value because it is a gate's ANSWER, the
// ceiling is not an answer — it is what the decision was measured against.
func TestTheCountsAreRecordedOnRefusalsThatNeverReachTheGate(t *testing.T) {
	const originalTokens = 20
	for _, c := range []struct {
		name      string
		candidate score.Report
		want      rewrite.RejectionCode
	}{
		{"candidate-unscoreable", withTokens(unscoreable(), 26),
			rewrite.RejectionCandidateUnscoreable},
		{"uncalibrated", withTokens(uncalibratedAt(0.40), 27),
			rewrite.RejectionUncalibrated},
		{"different-features", withTokens(
			scored(0.40, features.WordLengthMean, features.ColonDensity), 28),
			rewrite.RejectionDifferentFeatures},
	} {
		t.Run(c.name, func(t *testing.T) {
			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{
					original: withTokens(
						scored(0.50, features.WordLengthMean, features.CommaDensity),
						originalTokens),
					better: c.candidate,
				},
				[]string{better}, passingGate())

			got := run(t, loop)

			if len(store.attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			attempt := store.attempts[0]
			if attempt.Rejection != c.want {
				t.Fatalf("rejection = %q, want %q; this fixture exists to exercise "+
					"that path", attempt.Rejection, c.want)
			}
			wantCandidate := c.candidate.Segments[0].LexicalTokens
			if attempt.OriginalLexicalTokens != originalTokens ||
				attempt.CandidateLexicalTokens != wantCandidate {
				t.Errorf("counts recorded %d -> %d, want %d -> %d; the scorer supplied "+
					"both and the refusal does not make them unknown",
					attempt.OriginalLexicalTokens, attempt.CandidateLexicalTokens,
					originalTokens, wantCandidate)
			}
			if attempt.ExpansionCeiling != rewrite.ExpansionCeiling {
				t.Errorf("ExpansionCeiling = %v, want %v: it records the policy in "+
					"force, not whether the gate ran",
					attempt.ExpansionCeiling, rewrite.ExpansionCeiling)
			}
			// And ALL THREE reach the caller, not only the store. Zeroing either
			// of the other two in the outcome alone passed the whole package.
			if len(got.Attempts) == 0 {
				t.Fatal("the outcome carries no attempt")
			}
			reported := got.Attempts[0]
			if reported.OriginalLexicalTokens != originalTokens ||
				reported.CandidateLexicalTokens != wantCandidate {
				t.Errorf("the outcome reports counts %d -> %d, want %d -> %d",
					reported.OriginalLexicalTokens, reported.CandidateLexicalTokens,
					originalTokens, wantCandidate)
			}
			if reported.ExpansionCeiling != rewrite.ExpansionCeiling {
				t.Errorf("the outcome reports a ceiling of %v, want %v",
					reported.ExpansionCeiling, rewrite.ExpansionCeiling)
			}
		})
	}
}

// And the other half of that convention: with no single candidate segment there
// are no counts to record, so both are zero — while the ceiling is still the
// policy in force.
//
// The suite accepted both `20 -> 0 at 1.5` and `0 -> 0 at 0` until this ran. The
// original's count is zero here too: the record describes a comparison that never
// happened, and reporting the original's 20 beside a candidate's 0 would read as
// a measured 20-to-nothing shortening.
func TestNoCountsAreRecordedWhenTheCandidateIsNotOneSegment(t *testing.T) {
	for _, n := range []int{0, 2, 3} {
		t.Run(segments(n), func(t *testing.T) {
			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{
					original: scoredTokens(0.50, 20),
					better:   paragraphs(n),
				},
				[]string{better}, passingGate())

			got := run(t, loop)

			if len(store.attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			if store.attempts[0].Rejection != rewrite.RejectionNotOneSegment {
				t.Fatalf("rejection = %q, want %q; this fixture exists to exercise "+
					"that path", store.attempts[0].Rejection, rewrite.RejectionNotOneSegment)
			}
			if len(got.Attempts) == 0 {
				t.Fatal("the outcome carries no attempt")
			}
			for name, attempt := range map[string]rewrite.Attempt{
				"the store": store.attempts[0], "the outcome": got.Attempts[0],
			} {
				if attempt.OriginalLexicalTokens != 0 || attempt.CandidateLexicalTokens != 0 {
					t.Errorf("%s records counts %d -> %d, want 0 -> 0: the candidate "+
						"admits %d segments, so there is no single count to compare",
						name, attempt.OriginalLexicalTokens,
						attempt.CandidateLexicalTokens, n)
				}
				if attempt.ExpansionCeiling != rewrite.ExpansionCeiling {
					t.Errorf("%s records a ceiling of %v, want %v: the policy in force "+
						"is knowable even when nothing was measured against it",
						name, attempt.ExpansionCeiling, rewrite.ExpansionCeiling)
				}
			}
		})
	}
}

// The attempt records the evidence, so a decision can be read back without
// rerunning the gate.
func TestTheAttemptRecordsBothCountsAndTheBound(t *testing.T) {
	const originalTokens, candidateTokens = 20, 31
	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original: scoredTokens(0.50, originalTokens),
			better:   scoredTokens(0.40, candidateTokens),
		},
		[]string{better}, passingGate())

	run(t, loop)

	if len(store.attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	attempt := store.attempts[0]
	if attempt.OriginalLexicalTokens != originalTokens {
		t.Errorf("OriginalLexicalTokens = %d, want %d",
			attempt.OriginalLexicalTokens, originalTokens)
	}
	if attempt.CandidateLexicalTokens != candidateTokens {
		t.Errorf("CandidateLexicalTokens = %d, want %d",
			attempt.CandidateLexicalTokens, candidateTokens)
	}
	if attempt.ExpansionCeiling != rewrite.ExpansionCeiling {
		t.Errorf("ExpansionCeiling = %v, want %v",
			attempt.ExpansionCeiling, rewrite.ExpansionCeiling)
	}
}

// Precedence, declared rather than incidental.
//
// `expanded` is reported after `not-preserved` and before `language`. Both anchor
// on the ORIGINAL, and losing the author's content is the more specific complaint
// than adding bulk; everything below reads the advancing current, all three texts,
// or the scores.
func TestExpansionIsReportedAfterPreservationAndBeforeLanguage(t *testing.T) {
	codes := rewrite.RejectionCodes()
	index := map[rewrite.RejectionCode]int{}
	for i, code := range codes {
		index[code] = i
	}
	for _, code := range []rewrite.RejectionCode{
		rewrite.RejectionExpanded, rewrite.RejectionNotPreserved, rewrite.RejectionLanguage,
	} {
		if _, ok := index[code]; !ok {
			t.Fatalf("%q is not in RejectionCodes()", code)
		}
	}
	if index[rewrite.RejectionNotPreserved] >= index[rewrite.RejectionExpanded] {
		t.Errorf("not-preserved is at %d and expanded at %d; expanded must come after",
			index[rewrite.RejectionNotPreserved], index[rewrite.RejectionExpanded])
	}
	if index[rewrite.RejectionExpanded] >= index[rewrite.RejectionLanguage] {
		t.Errorf("expanded is at %d and language at %d; expanded must come before",
			index[rewrite.RejectionExpanded], index[rewrite.RejectionLanguage])
	}
}

// Expansion is reported BEFORE language, in execution and not only in the list.
//
// Both halves of the chosen position need a behavioral case: the half below is
// preservation winning over expansion, and this is expansion winning over the two
// language refusals.
//
// The whole attempt is compared, not just the code. An implementation that
// short-circuited on the ceiling and skipped the language, tells and splice
// measurements would report `expanded` correctly and still destroy the evidence
// the loop is required to retain whichever refusal wins.
func TestExpansionIsReportedBeforeEitherLanguageRefusal(t *testing.T) {
	for _, c := range []struct {
		name       string
		introduced []string
		overgrown  []string
	}{
		{"introducing a script", []string{"Cyrillic"}, nil},
		{"growing one out of proportion", nil, []string{"Han"}},
		{"both at once", []string{"Cyrillic"}, []string{"Han"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			gate := passingGate()
			gate.fallback.introduced = c.introduced
			gate.fallback.overgrown = c.overgrown

			loop, _, _, _, store := loopOver(t,
				map[string]score.Report{
					original: scoredTokens(0.50, 20),
					better:   scoredTokens(0.40, 400),
				},
				[]string{better}, gate)

			got := run(t, loop)

			if got.Changed {
				t.Fatal("a candidate 20x the original was accepted")
			}
			if len(store.attempts) == 0 {
				t.Fatal("no attempt was recorded")
			}
			// The WHOLE attempt, built independently rather than field by field.
			// Checking selected fields let an implementation drop the bound — or
			// all three expansion fields — whenever expansion collided with
			// language, and still pass.
			want := identities(rewrite.Attempt{
				Index:                  0,
				CurrentHash:            hashOf(original),
				CandidateHash:          hashOf(better),
				CurrentDistance:        0.50,
				CandidateDistance:      0.40,
				CurrentBand:            eval.BandDrifting,
				CandidateBand:          eval.BandDrifting,
				Preserved:              true,
				TellsComparison:        -1,
				TellsComparable:        true,
				IntroducedScripts:      c.introduced,
				OvergrownScripts:       c.overgrown,
				Splice:                 rewrite.SpliceIntact,
				OriginalLexicalTokens:  20,
				CandidateLexicalTokens: 400,
				ExpansionCeiling:       rewrite.ExpansionCeiling,
				Rejection:              rewrite.RejectionExpanded,
			})
			if got := store.attempts[0]; !reflect.DeepEqual(got, want) {
				t.Errorf("the recorded attempt is not the expected one:\n got  %+v\n want %+v",
					got, want)
			}
		})
	}
}

// And the precedence is what the loop actually applies, not only what the
// vocabulary lists. A candidate that is BOTH over-long and unpreserved reports
// the preservation failure.
func TestAnOverLongUnpreservedCandidateReportsPreservationFirst(t *testing.T) {
	const identifier = "preserve-v1:number:lost:3d4c981bf761d9b8"
	gate := passingGate()
	gate.verdicts = map[string]gateVerdict{
		better: {preserved: false, identifiers: []string{identifier},
			comparison: -1, comparable: true},
	}

	loop, _, _, _, store := loopOver(t,
		map[string]score.Report{
			original: scoredTokens(0.50, 20),
			better:   scoredTokens(0.40, 400),
		},
		[]string{better}, gate)

	run(t, loop)

	if len(store.attempts) == 0 {
		t.Fatal("no attempt was recorded")
	}
	// The whole attempt again. Checking only the rejection code let an
	// implementation drop all three expansion fields on the preservation path.
	want := identities(rewrite.Attempt{
		Index:                  0,
		CurrentHash:            hashOf(original),
		CandidateHash:          hashOf(better),
		CurrentDistance:        0.50,
		CandidateDistance:      0.40,
		CurrentBand:            eval.BandDrifting,
		CandidateBand:          eval.BandDrifting,
		Preserved:              false,
		PreserveIdentifiers:    []string{identifier},
		TellsComparison:        -1,
		TellsComparable:        true,
		Splice:                 rewrite.SpliceIntact,
		OriginalLexicalTokens:  20,
		CandidateLexicalTokens: 400,
		ExpansionCeiling:       rewrite.ExpansionCeiling,
		Rejection:              rewrite.RejectionNotPreserved,
	})
	if got := store.attempts[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("the candidate is 20x the original AND unpreserved; preservation is "+
			"the more specific complaint and the expansion evidence must still be "+
			"recorded:\n got  %+v\n want %+v", got, want)
	}
}
