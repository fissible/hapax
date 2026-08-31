package workflow_test

import (
	"testing"

	"github.com/fissible/hapax/internal/workflow"
)

// Inline code makes a paragraph unspliceable, not a target. A paragraph already
// in the author's band was never a target, so its user-facing disposition must
// remain in-range.
func TestInRangeParagraphWithExcisionsStaysInRange(t *testing.T) {
	root := bandedStore(t, "in-range")
	draft := writeDraft(t, root, excisionDraft)
	plan := planned(t, planRequest(root, draft))
	leaves := admittedLeaves(t, draft, storedFloor(t, root))

	if len(plan.Segments) != len(leaves) {
		t.Fatalf("planned %d segments over %d admitted paragraphs", len(plan.Segments), len(leaves))
	}
	excised := 0
	for i, segment := range plan.Segments {
		if segment.Band.Band != "in-range" {
			t.Fatalf("segment %d banded %q, want in-range", segment.Index, segment.Band.Band)
		}
		if segment.Disposition != workflow.DispositionInRange {
			t.Errorf("segment %d has disposition %q, want %q", segment.Index, segment.Disposition, workflow.DispositionInRange)
		}
		if leaves[i].HasExcisions {
			excised++
			if segment.Disposition == workflow.DispositionContainsExcisions {
				t.Errorf("excised in-range segment %d has disposition %q", segment.Index, segment.Disposition)
			}
		}
	}
	if excised == 0 {
		t.Fatal("the fixture produced no in-range segment with excisions")
	}
	if plan.Targets != 0 {
		t.Errorf("%d targets among in-range segments", plan.Targets)
	}
}
