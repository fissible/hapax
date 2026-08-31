package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/fissible/hapax/internal/features"
)

// IndexMode declares the artifacts an indexing pass commits.
type IndexMode string

const (
	IndexSnapshotOnly        IndexMode = "snapshot-only"
	IndexProfile             IndexMode = "profile"
	IndexProfileAndReference IndexMode = "profile-and-reference"
)

type IndexWrite struct {
	Mode      IndexMode
	Snapshot  SnapshotWrite
	Profile   Profile
	Reference Reference
}
type Indexed struct {
	Snapshot SnapshotWrite
	Pruned   Pruned
}

// Index performs the complete selected write in one immediate transaction.
func (s *Store) Index(ctx context.Context, w IndexWrite) (Indexed, error) {
	if err := indexValid(w); err != nil {
		return Indexed{}, err
	}
	w.Snapshot = writeCopy(w.Snapshot)
	if err := validateWrite(&w.Snapshot); err != nil {
		return Indexed{}, err
	}
	conn, err := s.immediate(ctx)
	if err != nil {
		return Indexed{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err = s.putSnapshotConn(ctx, conn, w.Snapshot); err != nil {
		return Indexed{}, err
	}
	if w.Mode != IndexSnapshotOnly {
		if err = s.putProfileConn(ctx, conn, w.Profile, true); err != nil {
			return Indexed{}, err
		}
		if w.Mode == IndexProfileAndReference {
			if err = s.putReferenceConn(ctx, conn, w.Reference); err != nil {
				return Indexed{}, err
			}
		}
	}
	result := Indexed{Snapshot: w.Snapshot}
	if w.Mode != IndexSnapshotOnly {
		if result.Pruned, err = s.pruneHeadsConn(ctx, conn); err != nil {
			return Indexed{}, err
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Indexed{}, err
	}
	return result, nil
}
func indexValid(w IndexWrite) error {
	switch w.Mode {
	case IndexSnapshotOnly:
		if w.Profile.ID != "" || w.Reference.ID != "" {
			return ErrInvalid
		}
	case IndexProfile:
		if !validProfile(w.Profile) || w.Reference.ID != "" {
			return ErrInvalid
		}
	case IndexProfileAndReference:
		if !validProfile(w.Profile) || !validReference(w.Reference) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func (s *Store) putSnapshotConn(ctx context.Context, c *sql.Conn, w SnapshotWrite) error {
	if _, err := c.ExecContext(ctx, "INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,?)", w.ID, w.PolicyDigest, s.deps.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	for _, d := range w.Documents {
		if _, err := c.ExecContext(ctx, "INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language,unavailable_at) VALUES (?,?,?,?,?,?,?,?,NULL)", d.ID, w.ID, d.Path, d.ContentHash, d.Register, d.Split, d.Admission, d.Language); err != nil {
			return err
		}
		for _, n := range d.Nodes {
			if _, err := c.ExecContext(ctx, "INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion) VALUES (?,?,?,?,?,?,?,?,?,?)", n.ID, d.ID, n.Ordinal, n.Kind, n.Role, joinContainers(n.Containers), n.Offset, n.Length, boolInt(n.Included), n.Exclusion); err != nil {
				return err
			}
			if n.Vector != nil {
				md := features.ManifestDigest()
				if _, err := c.ExecContext(ctx, "INSERT INTO feature_vector (node_id,manifest_digest,set_version,tokens,lexical_tokens) VALUES (?,?,?,?,?)", n.ID, md, n.Vector.SetVersion, n.Vector.Tokens, n.Vector.LexicalTokens); err != nil {
					return err
				}
				for _, v := range n.Vector.Values {
					if _, err := c.ExecContext(ctx, "INSERT INTO feature_value (node_id,manifest_digest,feature,value,defined,sampling_variance,sampling_variance_defined) VALUES (?,?,?,?,?,?,?)", n.ID, md, v.ID, v.Value, boolInt(v.Defined), v.SamplingVariance, boolInt(v.SamplingVarianceDefined)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func (s *Store) putProfileConn(ctx context.Context, c *sql.Conn, p Profile, head bool) error {
	if _, err := c.ExecContext(ctx, "INSERT INTO profile (id,snapshot_id,register,unit,variance_convention,manifest_digest,feature_set_version,min_paragraph_lexical_tokens,production_ready,not_ready_reason) VALUES (?,?,?,?,?,?,?,?,?,?)", p.ID, p.SnapshotID, p.Register, p.Unit, p.VarianceConvention, p.ManifestDigest, p.FeatureSetVersion, p.MinParagraphLexicalTokens, boolInt(p.ProductionReady), p.NotReadyReason); err != nil {
		return err
	}
	for _, x := range p.Stats {
		if _, err := c.ExecContext(ctx, "INSERT INTO profile_stat (profile_id,feature,n,mean,variance,defined,variance_defined,min_observations) VALUES (?,?,?,?,?,?,?,?)", p.ID, x.Feature, x.N, x.Mean, x.Variance, boolInt(x.Defined), boolInt(x.VarianceDefined), x.MinObservations); err != nil {
			return err
		}
	}
	if head {
		_, err := c.ExecContext(ctx, "INSERT INTO profile_head(register,profile_id,updated_at) VALUES(?,?,?) ON CONFLICT(register) DO UPDATE SET profile_id=excluded.profile_id,updated_at=excluded.updated_at", p.Register, p.ID, s.deps.Now().UTC().Format(time.RFC3339))
		return err
	}
	return nil
}
func (s *Store) putReferenceConn(ctx context.Context, c *sql.Conn, r Reference) error {
	if _, err := c.ExecContext(ctx, "INSERT INTO reference (id,profile_id,split,min_segments,manifest_digest) VALUES (?,?,?,?,?)", r.ID, r.ProfileID, r.Split, r.MinSegments, r.ManifestDigest); err != nil {
		return err
	}
	for f, vs := range r.Values {
		for i, v := range vs {
			if _, err := c.ExecContext(ctx, "INSERT INTO reference_value(reference_id,feature,ordinal,value) VALUES(?,?,?,?)", r.ID, f, i, v); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) pruneHeadsConn(ctx context.Context, c *sql.Conn) (Pruned, error) {
	var p Pruned
	if err := c.QueryRowContext(ctx, "SELECT count(*) FROM profile WHERE id NOT IN (SELECT profile_id FROM profile_head) AND id NOT IN (SELECT profile_id FROM eval_result) AND id NOT IN (SELECT profile_id FROM rewrite_attempt)").Scan(&p.Profiles); err != nil {
		return p, err
	}
	if _, err := c.ExecContext(ctx, "DELETE FROM profile WHERE id NOT IN (SELECT profile_id FROM profile_head) AND id NOT IN (SELECT profile_id FROM eval_result) AND id NOT IN (SELECT profile_id FROM rewrite_attempt)"); err != nil {
		return p, err
	}
	return p, nil
}
