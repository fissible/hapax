package workflow_test

// #92's integration half, and it exists because the profile package alone does
// not prove the fix.
//
// Demonstrated rather than assumed: changing workflow's score-result mapping to
// report `ParagraphsBelowFloor: 0` leaves the ENTIRE profile and workflow suites
// green. The floor can be raised correctly in `profile` and then dropped on the
// way out, and nothing above notices.
//
// So this asserts the floor through the two surfaces a person actually uses —
// what `score` reports, and what `rewrite` will agree to target — against a
// freshly indexed profile at the declared defaults.

import (
	"os"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/workflow"
)

// floorDraft straddles the floor deliberately: one token, nine tokens, ten
// tokens, and a long paragraph. Nine and ten sit either side of the boundary, so
// an off-by-one is caught rather than only the obviously tiny case.
func floorDraft() string {
	return "Yes.\n\n" +
		tokensOf(9) + "\n\n" +
		tokensOf(10) + "\n\n" +
		tokensOf(40) + "\n"
}

func tokensOf(n int) string {
	words := make([]string, 0, n)
	for i := 0; i < n; i++ {
		words = append(words, "word"+string(rune('a'+i%26)))
	}
	return strings.Join(words, " ") + "."
}

// score reports the paragraphs it could not measure, and does not measure them.
func TestScoreExcludesAndCountsParagraphsBelowTheFloor(t *testing.T) {
	t.Parallel()
	if got := profile.DefaultRequirements().MinParagraphLexicalTokens; got != 10 {
		t.Fatalf("this fixture assumes a floor of 10; it is %d", got)
	}
	root := indexedCorpus(t)
	draft := writeDraft(t, root, floorDraft())

	result := scored(t, scoreRequest(root, draft))

	if result.ParagraphsBelowFloor != 2 {
		t.Errorf("paragraphs_below_floor = %d, want 2 — the one-token and nine-token "+
			"paragraphs", result.ParagraphsBelowFloor)
	}
	// EXACTLY these token counts, in order. A count and a minimum are both
	// satisfied by a mapping that returned the forty-token paragraph twice.
	var got []int
	for _, segment := range result.Segments {
		got = append(got, segment.LexicalTokens)
	}
	if len(got) != 2 || got[0] != 10 || got[1] != 40 {
		t.Errorf("segment lexical tokens = %v, want [10 40]", got)
	}
}

// And a rewrite plan agrees with score about which paragraphs exist, and about
// WHICH ONE each segment is.
//
// The first version of this checked that no span contained "Yes." and that each
// span had at least ten words. Both hold when every segment points at the SAME
// forty-token paragraph, which is what a mapping that skipped the wrong number
// of nodes would produce — and codex demonstrated exactly that mutation passing.
// So this asserts the exact bytes, in order, and that each span agrees with the
// node the plan recorded.
func TestARewritePlanTargetsOnlyParagraphsThatClearedTheFloor(t *testing.T) {
	t.Parallel()
	root := bandedStore(t, "drifting")
	draft := writeDraft(t, root, floorDraft())

	plan := planned(t, planRequest(root, draft))

	if plan.Refusal != "" {
		t.Fatalf("refusal = %q", plan.Refusal)
	}
	if plan.ParagraphsBelowFloor != 2 {
		t.Errorf("plan reports %d paragraphs below floor, want 2", plan.ParagraphsBelowFloor)
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("planned %d segments, want 2", len(plan.Segments))
	}

	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read the draft: %v", err)
	}
	// The two paragraphs that cleared the floor, in document order.
	want := []string{tokensOf(10), tokensOf(40)}
	seen := map[string]bool{}
	for i, segment := range plan.Segments {
		if segment.Offset < 0 || segment.Offset+segment.Length > len(raw) {
			t.Fatalf("segment %d spans [%d,%d) outside a %d-byte draft",
				segment.Index, segment.Offset, segment.Offset+segment.Length, len(raw))
		}
		text := strings.TrimSpace(string(raw[segment.Offset : segment.Offset+segment.Length]))
		if text != want[i] {
			t.Errorf("segment %d spans\n  %q\nwant\n  %q", segment.Index, text, want[i])
		}
		// And no two segments are the same paragraph, which is the failure a
		// per-segment check cannot see.
		if seen[text] {
			t.Errorf("segment %d repeats a paragraph an earlier segment already spans",
				segment.Index)
		}
		seen[text] = true
		if segment.NodeID == "" {
			t.Errorf("segment %d records no node", segment.Index)
		}
	}
	assertSegmentsResolveIntoTheDraftSnapshot(t, root, draft, plan)
}

// ---------------------------------------------------------------------------
// The persisted floor, not the runner's default
// ---------------------------------------------------------------------------

// A profile is the instrument it was fitted as. Scoring uses the floor stored
// with it, and a runner whose default has since moved must not override that.
//
// Unprotected until now: codex overwrote the persisted floor with the default in
// both score and plan, and both suites stayed green. So both directions are
// pinned — a profile fitted BELOW today's default and one fitted ABOVE it.
func TestScoringUsesThePersistedFloorAndNotTheDefault(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		floor      int
		wantBelow  int
		wantTokens []int
	}{
		// Fitted before #92 raised the default: everything is measurable, which
		// is the state every existing profile on disk is in.
		{"a profile fitted at one", 1, 0, []int{1, 9, 10, 40}},
		// Fitted above today's default: the ten-token paragraph is excluded too.
		{"a profile fitted at twelve", 12, 3, []int{40}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			requirements := profile.DefaultRequirements()
			requirements.MinParagraphLexicalTokens = c.floor
			root := indexedCorpusWith(t, requirements)
			draft := writeDraft(t, root, floorDraft())

			result := scored(t, scoreRequest(root, draft))

			if result.ParagraphsBelowFloor != c.wantBelow {
				t.Errorf("paragraphs_below_floor = %d, want %d at a persisted floor of %d",
					result.ParagraphsBelowFloor, c.wantBelow, c.floor)
			}
			var got []int
			for _, segment := range result.Segments {
				got = append(got, segment.LexicalTokens)
			}
			if len(got) != len(c.wantTokens) {
				t.Fatalf("segment lexical tokens = %v, want %v", got, c.wantTokens)
			}
			for i := range got {
				if got[i] != c.wantTokens[i] {
					t.Errorf("segment lexical tokens = %v, want %v", got, c.wantTokens)
					break
				}
			}
		})
	}
}

// indexedCorpusWith indexes a fresh corpus under the given requirements, so the
// PERSISTED floor is the one under test rather than whatever the runner's
// default happens to be.
func indexedCorpusWith(t *testing.T, requirements profile.Requirements) string {
	t.Helper()
	root := corpusOf(t, 60)
	runner := workflow.New(requirements, deviation.DefaultMinSegments())
	result, err := runner.Index(ctx(), indexRequest(root))
	if err != nil {
		t.Fatalf("Index at a floor of %d: %v", requirements.MinParagraphLexicalTokens, err)
	}
	if result.ProfileID == "" {
		t.Fatalf("no profile fitted at a floor of %d", requirements.MinParagraphLexicalTokens)
	}
	if result.Mode != "profile-and-reference" {
		t.Fatalf("index mode = %q at a floor of %d; scoring needs a reference",
			result.Mode, requirements.MinParagraphLexicalTokens)
	}
	return root
}

// And planning uses the persisted floor too.
//
// Score and Plan read the floor from the same bundle but assign it separately —
// Plan copies it into its draft requirements — so covering one does not cover
// the other. Codex mutated Plan's assignment to use the runner default and both
// complete suites stayed green, which is how this gap was found rather than
// reasoned about.
//
// Explicit paragraph selection is used so an uncalibrated store can be planned
// against, which is what a corpus indexed at a chosen floor is.
func TestPlanningUsesThePersistedFloorAndNotTheDefault(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name         string
		floor        int
		wantSegments int
		wantFirst    string
	}{
		{"a profile fitted at one", 1, 4, "Yes."},
		{"a profile fitted at twelve", 12, 1, tokensOf(40)},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			requirements := profile.DefaultRequirements()
			requirements.MinParagraphLexicalTokens = c.floor
			root := indexedCorpusWith(t, requirements)
			draft := writeDraft(t, root, floorDraft())

			request := planRequest(root, draft)
			request.Paragraphs = []int{0}
			plan := planned(t, request)

			if plan.Refusal != "" {
				t.Fatalf("refusal = %q at a persisted floor of %d", plan.Refusal, c.floor)
			}
			if len(plan.Segments) != c.wantSegments {
				t.Fatalf("planned %d segments at a persisted floor of %d, want %d",
					len(plan.Segments), c.floor, c.wantSegments)
			}

			// And segment zero — the one the request named — is the paragraph the
			// floor makes first. A plan that used the default floor would name a
			// different one.
			raw, err := os.ReadFile(draft)
			if err != nil {
				t.Fatalf("read the draft: %v", err)
			}
			first := plan.Segments[0]
			if first.Offset+first.Length > len(raw) {
				t.Fatalf("segment 0 spans outside the draft")
			}
			got := strings.TrimSpace(string(raw[first.Offset : first.Offset+first.Length]))
			if got != c.wantFirst {
				t.Errorf("segment 0 spans\n  %q\nwant\n  %q", got, c.wantFirst)
			}
		})
	}
}
