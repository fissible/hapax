package corpus

import (
	"errors"
	"fmt"
	"sort"
)

// ScreenOverlap records whether this distractor snapshot shares eligible
// documents with the supplied author snapshot. It mutates the receiver and is
// not safe for concurrent use on one snapshot.
func (s *Snapshot) ScreenOverlap(author *Snapshot) (OverlapReport, error) {
	if s == nil {
		return OverlapReport{}, errors.New("cannot screen overlap on a nil distractor snapshot")
	}
	if author == nil {
		return OverlapReport{}, errors.New("author snapshot must not be nil")
	}
	if s.Policy.Role != RoleDistractor {
		return OverlapReport{}, fmt.Errorf("overlap screen receiver role must be %q, got %q", RoleDistractor, s.Policy.Role)
	}
	if author.Policy.Role != RoleAuthor {
		return OverlapReport{}, fmt.Errorf("overlap screen author role must be %q, got %q", RoleAuthor, author.Policy.Role)
	}

	authorEligible := author.Eligible()
	if len(authorEligible) == 0 {
		return OverlapReport{}, errors.New("author snapshot has no eligible documents")
	}
	distractorEligible := s.Eligible()
	if len(distractorEligible) == 0 {
		return OverlapReport{}, errors.New("distractor snapshot has no eligible documents")
	}
	authorByHash := make(map[string]Document, len(authorEligible))
	for _, document := range authorEligible {
		authorByHash[document.ContentHash] = document
	}

	report := OverlapReport{
		AuthorSnapshotID:     author.ID,
		DistractorSnapshotID: s.ID,
		Algorithm:            OverlapAlgorithm,
		AuthorEligible:       len(authorEligible),
		DistractorEligible:   len(distractorEligible),
	}
	for _, document := range distractorEligible {
		if authorDocument, shared := authorByHash[document.ContentHash]; shared {
			report.Shared = append(report.Shared, SharedDocument{
				ContentHash:    document.ContentHash,
				AuthorPath:     authorDocument.Path,
				DistractorPath: document.Path,
			})
		}
	}
	sort.Slice(report.Shared, func(i, j int) bool {
		return report.Shared[i].ContentHash < report.Shared[j].ContentHash
	})
	if len(report.Shared) == 0 {
		report.State = CheckPassed
	} else {
		report.State = CheckFailed
	}

	s.storeOverlap(report)
	return copyOverlap(report), nil
}

// OverlapScreen returns the stored attestation for one author snapshot.
func (s *Snapshot) OverlapScreen(authorSnapshotID string) (OverlapReport, bool) {
	if s == nil {
		return OverlapReport{}, false
	}
	report, ok := s.overlaps[authorSnapshotID]
	if !ok {
		return OverlapReport{}, false
	}
	return copyOverlap(report), true
}

// OverlapScreens returns stored attestations in stable author-snapshot order.
func (s *Snapshot) OverlapScreens() []OverlapReport {
	if s == nil {
		return nil
	}
	reports := make([]OverlapReport, 0, len(s.overlaps))
	for _, report := range s.overlaps {
		reports = append(reports, copyOverlap(report))
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].AuthorSnapshotID < reports[j].AuthorSnapshotID
	})
	return reports
}

// NonOverlappingWith verifies that a current, successful attestation exists
// for the author a consumer is about to use.
func (s *Snapshot) NonOverlappingWith(authorSnapshotID string) error {
	report, ok := s.OverlapScreen(authorSnapshotID)
	if !ok {
		return fmt.Errorf("no overlap attestation for author snapshot %q", authorSnapshotID)
	}
	if report.DistractorSnapshotID != s.ID {
		return fmt.Errorf("overlap attestation for author snapshot %q names distractor snapshot %q, not %q", authorSnapshotID, report.DistractorSnapshotID, s.ID)
	}
	if report.Algorithm != OverlapAlgorithm {
		return fmt.Errorf("overlap attestation for author snapshot %q uses unsupported algorithm %q", authorSnapshotID, report.Algorithm)
	}
	if report.State != CheckPassed {
		return fmt.Errorf("overlap attestation for author snapshot %q has state %q, not %q", authorSnapshotID, report.State, CheckPassed)
	}
	if len(report.Shared) != 0 {
		return fmt.Errorf("overlap attestation for author snapshot %q names %d shared documents", authorSnapshotID, len(report.Shared))
	}
	return nil
}

// storeOverlap centralizes replacement by author ID so an author has one
// current attestation rather than an ambiguous history of screen results.
func (s *Snapshot) storeOverlap(report OverlapReport) {
	if s.overlaps == nil {
		s.overlaps = make(map[string]OverlapReport)
	}
	s.overlaps[report.AuthorSnapshotID] = copyOverlap(report)
}

func copyOverlap(report OverlapReport) OverlapReport {
	cloned := report
	cloned.Shared = append([]SharedDocument(nil), report.Shared...)
	return cloned
}
