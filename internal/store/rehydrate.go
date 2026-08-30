package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"time"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/snapshot"
	"github.com/fissible/hapax/internal/text"
)

// Span is the persisted, location-independent reference to a node's bytes.
type Span struct {
	NodeID, DocumentID, SnapshotID, Path, ContentHash string
	Offset, Length                                    int
}

// Outcome is the closed set of ordinary rehydration results.
type Outcome string

const (
	OutcomeOK             Outcome = "ok"
	OutcomeMissing        Outcome = "missing"
	OutcomeUnreadable     Outcome = "unreadable"
	OutcomeContentChanged Outcome = "content-changed"
)

// Rehydrated is one requested node and its current rehydration result.
type Rehydrated struct {
	NodeID  string
	Outcome Outcome
	Text    string
}

// Span loads a structured stored reference.
func (s *Store) Span(ctx context.Context, nodeID string) (Span, error) {
	return spanFrom(s.db, ctx, nodeID)
}

func spanFrom(query queryer, ctx context.Context, nodeID string) (Span, error) {
	var span Span
	var ordinal int
	err := query.QueryRowContext(ctx, `SELECT node.node_id,node.document_id,document.snapshot_id,document.path,document.content_hash,node.offset,node.length,node.ordinal
		FROM node JOIN document ON document.document_id=node.document_id JOIN snapshot ON snapshot.id=document.snapshot_id WHERE node.node_id=?`, nodeID).
		Scan(&span.NodeID, &span.DocumentID, &span.SnapshotID, &span.Path, &span.ContentHash, &span.Offset, &span.Length, &ordinal)
	if errors.Is(err, sql.ErrNoRows) {
		var nodes int
		if err := query.QueryRowContext(ctx, "SELECT count(*) FROM node WHERE node_id=?", nodeID).Scan(&nodes); err != nil {
			return span, err
		}
		if nodes != 0 {
			return span, ErrCorrupt
		}
		return span, ErrNotFound
	}
	if err != nil {
		return span, err
	}
	if !validHash(span.SnapshotID) {
		return Span{}, ErrCorrupt
	}
	if !validPath(span.Path) {
		return Span{}, ErrCorrupt
	}
	if !validHash(span.ContentHash) {
		return Span{}, ErrCorrupt
	}
	if span.DocumentID != identity.HashInputs(map[string]string{"snapshot": span.SnapshotID, "path": span.Path}) {
		return Span{}, ErrCorrupt
	}
	if span.NodeID != identity.HashInputs(map[string]string{"document": span.DocumentID, "ordinal": strconv.Itoa(ordinal)}) {
		return Span{}, ErrCorrupt
	}
	if span.Offset < 0 {
		return Span{}, ErrCorrupt
	}
	if span.Length <= 0 {
		return Span{}, ErrCorrupt
	}
	return span, nil
}

// Rehydrate reads each requested document once, verifies admitted bytes, then slices them.
func (s *Store) Rehydrate(ctx context.Context, root string, nodeIDs []string) ([]Rehydrated, error) {
	if len(nodeIDs) == 0 {
		return []Rehydrated{}, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	spans := make([]Span, len(nodeIDs))
	for index, nodeID := range nodeIDs {
		if spans[index], err = spanFrom(tx, ctx, nodeID); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}

	byDocument := map[string][]int{}
	for index, span := range spans {
		byDocument[span.DocumentID] = append(byDocument[span.DocumentID], index)
	}
	results := make([]Rehydrated, len(spans))
	updates := map[string]Outcome{}
	for _, indexes := range byDocument {
		span := spans[indexes[0]]
		raw, readErr := s.deps.ReadFile(filepath.Join(root, filepath.FromSlash(span.Path)))
		outcome := OutcomeOK
		var admitted []byte
		if readErr != nil {
			if errors.Is(readErr, fs.ErrNotExist) {
				outcome = OutcomeMissing
			} else {
				outcome = OutcomeUnreadable
			}
		} else if document, verifyErr := snapshot.VerifyAdmitted(raw, span.ContentHash); verifyErr != nil {
			var admissionErr *text.AdmissionError
			switch {
			case errors.As(verifyErr, &admissionErr), errors.Is(verifyErr, snapshot.ErrContentChanged):
				outcome = OutcomeContentChanged
			default:
				return nil, verifyErr
			}
		} else {
			admitted = document.Raw()
		}
		for _, index := range indexes {
			result := Rehydrated{NodeID: spans[index].NodeID, Outcome: outcome}
			if outcome == OutcomeOK {
				end := spans[index].Offset + spans[index].Length
				if end < spans[index].Offset || end > len(admitted) {
					// A corrupt span invalidates the whole operation; do not record partial availability observations.
					return nil, ErrCorrupt
				}
				result.Text = string(admitted[spans[index].Offset:end])
			}
			results[index] = result
		}
		updates[span.DocumentID] = outcome
	}
	if err := s.applyAvailability(ctx, updates); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) applyAvailability(ctx context.Context, updates map[string]Outcome) error {
	if len(updates) == 0 {
		return nil
	}
	return artifactTx(ctx, s, func(connection *sql.Conn) error {
		for documentID, outcome := range updates {
			switch outcome {
			case OutcomeMissing, OutcomeUnreadable:
				if _, err := connection.ExecContext(ctx, "UPDATE document SET unavailable_at=COALESCE(unavailable_at, ?) WHERE document_id=?", s.deps.Now().UTC().Format(time.RFC3339), documentID); err != nil {
					return err
				}
			case OutcomeOK:
				if _, err := connection.ExecContext(ctx, "UPDATE document SET unavailable_at=NULL WHERE document_id=?", documentID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Unavailable returns documents whose first read failure has not been cleared.
func (s *Store) Unavailable(ctx context.Context, snapshotID string) (map[string]time.Time, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM snapshot WHERE id=?", snapshotID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, "SELECT path,unavailable_at FROM document WHERE snapshot_id=? AND unavailable_at IS NOT NULL", snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var path, when string
		if err := rows.Scan(&path, &when); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339, when)
		if err != nil {
			return nil, ErrCorrupt
		}
		out[path] = at
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
