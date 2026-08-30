package store

import (
	"context"
	"database/sql"
)

// Pruned counts parent artifact rows removed by Prune.
type Pruned struct {
	Snapshots, Documents, Nodes                         int
	Profiles, References, Thresholds, Selections, Heads int
}

// Prune removes graph components unreachable from the supplied profile roots
// and from the unconditional audit roots.
func (s *Store) Prune(ctx context.Context, keepProfiles []string) (Pruned, error) {
	for _, id := range keepProfiles {
		if !validHash(id) {
			return Pruned{}, invalidArtifact("profile", "id")
		}
	}
	var result Pruned
	err := artifactTx(ctx, s, func(connection *sql.Conn) error {
		for _, id := range keepProfiles {
			var count int
			if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM profile WHERE id=?", id).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				return ErrNotFound
			}
		}
		// This scratch graph is connection-local and must not become persisted schema: a non-TEMP table would make it visible across connections and violate the schema allowlist.
		if _, err := connection.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS hapax_reachable (kind TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY(kind,id))"); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, "DELETE FROM hapax_reachable"); err != nil {
			return err
		}
		for _, id := range keepProfiles {
			if _, err := connection.ExecContext(ctx, "INSERT INTO hapax_reachable(kind,id) VALUES ('profile',?)", id); err != nil {
				return err
			}
		}
		// Audit rows are roots themselves; their parents and named nodes must survive.
		for _, statement := range []string{
			"INSERT OR IGNORE INTO hapax_reachable(kind,id) SELECT 'profile',profile_id FROM eval_result",
			"INSERT OR IGNORE INTO hapax_reachable(kind,id) SELECT 'profile',profile_id FROM rewrite_attempt",
			"INSERT OR IGNORE INTO hapax_reachable(kind,id) SELECT 'node',node_id FROM rewrite_attempt",
		} {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		for {
			execution, err := connection.ExecContext(ctx, `INSERT OR IGNORE INTO hapax_reachable(kind,id)
				SELECT 'snapshot',profile.snapshot_id FROM profile JOIN hapax_reachable reachability ON reachability.kind='profile' AND reachability.id=profile.id
				UNION SELECT 'reference',reference.id FROM reference JOIN hapax_reachable reachability ON reachability.kind='profile' AND reachability.id=reference.profile_id
				UNION SELECT 'selection',exemplar_selection.id FROM exemplar_selection JOIN hapax_reachable reachability ON reachability.kind='profile' AND reachability.id=exemplar_selection.profile_id
				UNION SELECT 'document',document.document_id FROM document JOIN hapax_reachable reachability ON reachability.kind='snapshot' AND reachability.id=document.snapshot_id
				UNION SELECT 'node',node.node_id FROM node JOIN hapax_reachable reachability ON reachability.kind='document' AND reachability.id=node.document_id
				UNION SELECT 'document',node.document_id FROM node JOIN hapax_reachable reachability ON reachability.kind='node' AND reachability.id=node.node_id
				UNION SELECT 'snapshot',document.snapshot_id FROM document JOIN hapax_reachable reachability ON reachability.kind='document' AND reachability.id=document.document_id
				UNION SELECT 'node',exemplar_member.node_id FROM exemplar_member JOIN hapax_reachable reachability ON reachability.kind='selection' AND reachability.id=exemplar_member.selection_id`)
			if err != nil {
				return err
			}
			rowsAdded, err := execution.RowsAffected()
			if err != nil {
				return err
			}
			if rowsAdded == 0 {
				break
			}
		}
		count := func(table, where string) (int, error) {
			var rowCount int
			err := connection.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE "+where).Scan(&rowCount)
			return rowCount, err
		}
		var err error
		if result.Heads, err = count("profile_head", "profile_id NOT IN (SELECT id FROM hapax_reachable WHERE kind='profile')"); err != nil {
			return err
		}
		if result.Thresholds, err = count("threshold", "profile_id NOT IN (SELECT id FROM hapax_reachable WHERE kind='profile')"); err != nil {
			return err
		}
		if result.Selections, err = count("exemplar_selection", "profile_id NOT IN (SELECT id FROM hapax_reachable WHERE kind='profile')"); err != nil {
			return err
		}
		if result.References, err = count("reference", "profile_id NOT IN (SELECT id FROM hapax_reachable WHERE kind='profile')"); err != nil {
			return err
		}
		if result.Profiles, err = count("profile", "id NOT IN (SELECT id FROM hapax_reachable WHERE kind='profile')"); err != nil {
			return err
		}
		if result.Nodes, err = count("node", "document_id IN (SELECT document_id FROM document WHERE snapshot_id NOT IN (SELECT id FROM hapax_reachable WHERE kind='snapshot'))"); err != nil {
			return err
		}
		if result.Documents, err = count("document", "snapshot_id NOT IN (SELECT id FROM hapax_reachable WHERE kind='snapshot')"); err != nil {
			return err
		}
		if result.Snapshots, err = count("snapshot", "id NOT IN (SELECT id FROM hapax_reachable WHERE kind='snapshot')"); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, "DELETE FROM profile WHERE id NOT IN (SELECT id FROM hapax_reachable WHERE kind='profile')"); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, "DELETE FROM snapshot WHERE id NOT IN (SELECT id FROM hapax_reachable WHERE kind='snapshot')"); err != nil {
			return err
		}
		return nil
	})
	return result, err
}
