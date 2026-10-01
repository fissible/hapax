package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fissible/hapax/internal/assemble"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/score"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
)

type publicationExpectation struct {
	NodeID     string
	Span       text.Span // Admitted coordinates, translated after assembly.
	Tokens     []text.Token
	BelowFloor bool
	Measured   score.Segment
	// Execute always supplies the original role and container path, including
	// for changed leaves. Zero values describe a plain document paragraph.
	Role       text.Role
	Containers []text.ContainerKind
}

func sameTokenInterpretation(want, got []text.Token) bool {
	if len(want) != len(got) {
		return false
	}
	for i, w := range want {
		g := got[i]
		if w.Text != g.Text || w.Class != g.Class || w.Contraction != g.Contraction || w.Possessive != g.Possessive || w.Lexical != g.Lexical {
			return false
		}
	}
	return true
}

// checkPublication checks the combined document, collecting every mismatch in
// expectation order. Neither local attempt acceptance nor publication evidence
// is changed here: this verdict belongs to the invocation.
func checkPublication(assembled *text.Document, report score.Report, expected []publicationExpectation) ([]string, error) {
	if assembled == nil {
		return nil, errors.New("publication check has no document")
	}
	leaves := make(map[text.Span]*text.Node)
	for _, leaf := range assembled.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		leaves[leaf.Span] = leaf
	}
	measured := publicationMeasurements(assembled, report)
	var mismatches []string
	for _, want := range expected {
		leaf := leaves[want.Span]
		role, containers := want.Role, want.Containers
		if role == "" {
			role = text.RoleParagraph
		}
		if containers == nil {
			containers = []text.ContainerKind{text.ContainerDocument}
		}
		if leaf == nil || leaf.Role != role || !slices.Equal(leaf.Containers, containers) {
			mismatches = append(mismatches, want.NodeID)
			continue
		}
		tokens, err := assembled.RunTokens(leaf)
		if err != nil {
			return nil, err
		}
		got, exists := measured[want.Span]
		if !sameTokenInterpretation(want.Tokens, tokens) || (!want.BelowFloor && (!exists || !samePublicationMeasurement(want.Measured, got))) {
			mismatches = append(mismatches, want.NodeID)
		}
	}
	return mismatches, nil
}

func samePublicationMeasurement(want, got score.Segment) bool {
	if want.Distance.Defined != got.Distance.Defined || want.Distance.Value != got.Distance.Value ||
		want.Band.Band != got.Band.Band || want.Band.Defined != got.Band.Defined || want.Band.Reason != got.Band.Reason {
		return false
	}
	// Contributing features form a set; display deltas are keyed by identity.
	// Neither comparison depends on incidental slice ordering.
	if len(want.Distance.Features) != len(got.Distance.Features) || len(want.Features) != len(got.Features) {
		return false
	}
	contributing := make(map[features.ID]bool, len(want.Distance.Features))
	for _, id := range want.Distance.Features {
		contributing[id] = true
	}
	for _, id := range got.Distance.Features {
		if !contributing[id] {
			return false
		}
		delete(contributing, id)
	}
	deltas := make(map[features.ID]score.FeatureDelta, len(want.Features))
	for _, delta := range want.Features {
		deltas[delta.Feature] = delta
	}
	for _, delta := range got.Features {
		w, ok := deltas[delta.Feature]
		if !ok || w.Deviation != delta.Deviation || w.Defined != delta.Defined || w.Direction != delta.Direction {
			return false
		}
		delete(deltas, delta.Feature)
	}
	return true
}

func publicationBOMLength(doc *text.Document) int {
	if doc.HadBOM() {
		return len("\ufeff")
	}
	return 0
}

// Score offsets include the file BOM; structural spans never do.
func publicationMeasurements(doc *text.Document, report score.Report) map[text.Span]score.Segment {
	measured := make(map[text.Span]score.Segment, len(report.Segments))
	for _, segment := range report.Segments {
		at := text.Span{Offset: segment.Offset - publicationBOMLength(doc), Length: segment.Length}
		measured[at] = segment
	}
	return measured
}

// Retain the original document's context for EVERY included leaf. The plan
// names only leaves above the floor, so below-floor identities come from the
// same stored snapshot's structural nodes, even though they have no vector.
func originalPublicationExpectations(doc *text.Document, scorer executionScorer, planned []PlannedSegment, nodes []store.Node) ([]publicationExpectation, error) {
	source, err := assemble.Assemble(doc, nil)
	if err != nil {
		return nil, err
	}
	report, err := scorer.Score(source)
	if err != nil {
		return nil, err
	}
	measured := publicationMeasurements(doc, report)
	skipped := make(map[text.Span]bool, len(report.Skipped))
	for _, paragraph := range report.Skipped {
		skipped[text.Span{Offset: paragraph.Offset - publicationBOMLength(doc), Length: paragraph.Length}] = true
	}
	ids := make(map[text.Span]string)
	for _, node := range nodes {
		if node.Kind == text.KindLeaf && node.Included {
			ids[text.Span{Offset: node.Offset, Length: node.Length}] = node.ID
		}
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	bySpan := make(map[text.Span]publicationExpectation, len(leaves))
	for _, leaf := range leaves {
		tokens, err := doc.RunTokens(leaf)
		if err != nil {
			return nil, err
		}
		segment, scored := measured[leaf.Span]
		if ids[leaf.Span] == "" || (!scored && !skipped[leaf.Span]) {
			return nil, fmt.Errorf("publication baseline missing for leaf %d+%d", leaf.Span.Offset, leaf.Span.Length)
		}
		bySpan[leaf.Span] = publicationExpectation{
			NodeID: ids[leaf.Span], Span: leaf.Span, Tokens: tokens,
			BelowFloor: skipped[leaf.Span], Measured: segment,
			Role: leaf.Role, Containers: slices.Clone(leaf.Containers),
		}
	}
	expected := make([]publicationExpectation, 0, len(leaves))
	for _, target := range planned {
		at := text.Span{Offset: target.Offset, Length: target.Length}
		want, ok := bySpan[at]
		if !ok || want.NodeID != target.NodeID {
			return nil, fmt.Errorf("publication baseline disagrees with planned node %s", target.NodeID)
		}
		expected = append(expected, want)
		delete(bySpan, at)
	}
	// Unplanned below-floor leaves follow the plan, in source order.
	for _, leaf := range leaves {
		if want, ok := bySpan[leaf.Span]; ok {
			expected = append(expected, want)
		}
	}
	return expected, nil
}

func expectAcceptedPublication(expected []publicationExpectation, nodeID, candidate string, scorer executionScorer) error {
	doc, err := text.Admit([]byte(candidate))
	if err != nil {
		return err
	}
	report, err := scorer.Score([]byte(candidate))
	if err != nil {
		return err
	}
	leaves := doc.Structure(text.DefaultStructureOptions()).IncludedLeaves()
	if len(leaves) != 1 || len(report.Segments) != 1 {
		return errors.New("accepted publication candidate is not one scored leaf")
	}
	tokens, err := doc.RunTokens(leaves[0])
	if err != nil {
		return err
	}
	for i := range expected {
		if expected[i].NodeID == nodeID {
			expected[i].Tokens = tokens
			expected[i].Measured = report.Segments[0]
			expected[i].BelowFloor = false
			return nil
		}
	}
	return fmt.Errorf("accepted publication node %s has no expectation", nodeID)
}

func translatePublicationExpectations(expected []publicationExpectation, doc *text.Document, replacements []assemble.Replacement) {
	bySpan := make(map[text.Span]string, len(replacements))
	for _, replacement := range replacements {
		bySpan[replacement.Span] = replacement.Text
	}
	translated := make(map[text.Span]text.Span, len(expected))
	shift := 0
	for _, leaf := range doc.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		at := text.Span{Offset: leaf.Span.Offset + shift, Length: leaf.Span.Length}
		if candidate, changed := bySpan[leaf.Span]; changed {
			at.Offset += leadingSpace(candidate)
			at.Length = len(strings.TrimSpace(candidate))
			shift += len(candidate) - leaf.Span.Length
		}
		translated[leaf.Span] = at
	}
	for i := range expected {
		expected[i].Span = translated[expected[i].Span]
	}
}
