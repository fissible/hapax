package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Publication records the paragraph identities of one published document.
// Node IDs are historical identities, independent of the current snapshot.
type Publication struct {
	InvocationID string
	Paragraphs   []PublishedParagraph
}

type PublishedParagraph struct {
	NodeID, ParagraphHash string
}

// RecordPublication atomically records the whole publication. An identical
// retry is harmless; conflicting evidence leaves the existing record intact.
func (s *Store) RecordPublication(ctx context.Context, publication Publication) error {
	return artifactTx(ctx, s, func(c *sql.Conn) error {
		for _, paragraph := range publication.Paragraphs {
			var hash string
			err := c.QueryRowContext(ctx,
				"SELECT paragraph_hash FROM published_paragraph WHERE invocation_id=? AND node_id=?",
				publication.InvocationID, paragraph.NodeID).Scan(&hash)
			if err == nil {
				if hash != paragraph.ParagraphHash {
					return fmt.Errorf("%w: publication for invocation %s node %s", ErrConflict, publication.InvocationID, paragraph.NodeID)
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := c.ExecContext(ctx,
				"INSERT INTO published_paragraph (invocation_id,node_id,paragraph_hash) VALUES (?,?,?)",
				publication.InvocationID, paragraph.NodeID, paragraph.ParagraphHash); err != nil {
				return err
			}
		}
		return nil
	})
}

// PublicationEvidenceGap reads the historical disclosure written by migration,
// which later attempts, publications, and pruning cannot repair.
func (s *Store) PublicationEvidenceGap(ctx context.Context) (bool, error) {
	var gap bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM publication_evidence_gap)").Scan(&gap)
	return gap, err
}
