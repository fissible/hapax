// Package store persists the privacy-preserving Hapax artifact graph.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound         = errors.New("store: not found")
	ErrConflict         = errors.New("store: conflict")
	ErrCorrupt          = errors.New("store: corrupt")
	ErrInvalid          = errors.New("store: invalid")
	ErrSchemaAhead      = errors.New("store: schema ahead")
	ErrSchemaChecksum   = errors.New("store: schema checksum")
	ErrSchemaIncomplete = errors.New("store: schema incomplete")
	ErrSchemaForeign    = errors.New("store: schema foreign")
)

var errSnapshotIdentity = fmt.Errorf("%w: snapshot identity", ErrInvalid)

type Node struct {
	ID             string
	Ordinal        int
	Kind           text.Kind
	Role           text.Role
	Containers     []text.ContainerKind
	Offset, Length int
	Included       bool
	Exclusion      text.ExclusionReason
	Vector         *features.Vector
}

type Document struct {
	ID, Path, ContentHash, Register string
	Split                           corpus.Split
	Admission                       corpus.Admission
	Language                        string
	Nodes                           []Node
}

type SnapshotWrite struct {
	ID, PolicyDigest string
	Documents        []Document
}

type Store struct {
	db   *sql.DB
	path string
	deps deps
}

type deps struct {
	ReadFile func(string) ([]byte, error)
	Now      func() time.Time
}

func realDeps() deps { return deps{ReadFile: os.ReadFile, Now: time.Now} }

// Migrations returns exact SQL payloads, ordered by their zero-based version.
func Migrations() []string { return append([]string(nil), migrations...) }

func Open(path string) (*Store, error) { return open(path, "sqlite", realDeps()) }

func open(path, driverName string, d deps) (*Store, error) {
	newFile := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		newFile = true
	} else if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=foreign_keys(1)&_pragma=busy_timeout(10)"}).String()
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, deps: d}
	// Do only reads until we know the file belongs to us: refusing must leave it unchanged.
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		db.Close()
		return nil, err
	}
	if !newFile && tables != 0 {
		var ledger int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='migration'").Scan(&ledger); err != nil {
			db.Close()
			return nil, err
		}
		if ledger == 0 {
			db.Close()
			return nil, ErrSchemaForeign
		}
		if err := s.checkLedger(context.Background()); err != nil {
			db.Close()
			return nil, err
		}
	} else {
		for version, ddl := range migrations {
			if err := s.applyMigration(context.Background(), version, ddl); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT max(version) FROM migration").Scan(&n)
	return n, err
}

func (s *Store) checkLedger(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT version, checksum FROM migration ORDER BY version")
	if err != nil {
		return err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var v int
		var c string
		if err := rows.Scan(&v, &c); err != nil {
			return err
		}
		if v >= len(migrations) {
			return ErrSchemaAhead
		}
		if v != i {
			return ErrSchemaIncomplete
		}
		if c != identity.HashBytes([]byte(migrations[v])) {
			return ErrSchemaChecksum
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if i != len(migrations) {
		return ErrSchemaIncomplete
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, version int, ddl string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, ddl); err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO migration (version, checksum, applied_at) VALUES (?, ?, ?)", version, identity.HashBytes([]byte(ddl)), s.deps.Now().UTC().Format(time.RFC3339))
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) PutSnapshot(ctx context.Context, w SnapshotWrite) error {
	// SnapshotWrite is caller-owned. The aggregate contains slices, so derive
	// IDs in a copy rather than changing elements in the caller's backing arrays.
	w = writeCopy(w)
	validationErr := validateWrite(&w)
	if validationErr != nil && !errors.Is(validationErr, errSnapshotIdentity) {
		return validationErr
	}
	conn, err := s.immediate(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	stored, err := snapshotFrom(conn, ctx, w.ID)
	if err == nil {
		// A row at the supplied ID takes conflict precedence over a mismatched
		// payload identity. This keeps a retry or competing write at that key a
		// conflict rather than reporting it as a missing, invalid snapshot.
		if validationErr != nil {
			return ErrConflict
		}
		if sameSnapshot(stored, w) {
			_, err = conn.ExecContext(ctx, "COMMIT")
			return err
		}
		return ErrConflict
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if validationErr != nil {
		return validationErr
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO snapshot (id,policy_digest,created_at) VALUES (?,?,?)", w.ID, w.PolicyDigest, s.deps.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	for _, d := range w.Documents {
		if _, err = conn.ExecContext(ctx, "INSERT INTO document (document_id,snapshot_id,path,content_hash,register,split,admission,language,unavailable_at) VALUES (?,?,?,?,?,?,?,?,NULL)", d.ID, w.ID, d.Path, d.ContentHash, d.Register, d.Split, d.Admission, d.Language); err != nil {
			return err
		}
		for _, n := range d.Nodes {
			if _, err = conn.ExecContext(ctx, "INSERT INTO node (node_id,document_id,ordinal,kind,role,containers,offset,length,included,exclusion) VALUES (?,?,?,?,?,?,?,?,?,?)", n.ID, d.ID, n.Ordinal, n.Kind, n.Role, joinContainers(n.Containers), n.Offset, n.Length, boolInt(n.Included), n.Exclusion); err != nil {
				return err
			}
			if n.Vector != nil {
				md := features.ManifestDigest()
				if _, err = conn.ExecContext(ctx, "INSERT INTO feature_vector (node_id,manifest_digest,set_version,tokens,lexical_tokens) VALUES (?,?,?,?,?)", n.ID, md, n.Vector.SetVersion, n.Vector.Tokens, n.Vector.LexicalTokens); err != nil {
					return err
				}
				for _, v := range n.Vector.Values {
					if _, err = conn.ExecContext(ctx, "INSERT INTO feature_value (node_id,manifest_digest,feature,value,defined,sampling_variance,sampling_variance_defined) VALUES (?,?,?,?,?,?,?)", n.ID, md, v.ID, v.Value, boolInt(v.Defined), v.SamplingVariance, boolInt(v.SamplingVarianceDefined)); err != nil {
						return err
					}
				}
			}
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) immediate(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	for {
		_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			conn.Close()
			return nil, ctx.Err()
		}
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *Store) Snapshot(ctx context.Context, id string) (SnapshotWrite, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SnapshotWrite{}, err
	}
	defer tx.Rollback()
	w, err := snapshotFrom(tx, ctx, id)
	if err != nil {
		return SnapshotWrite{}, err
	}
	if err = validateStored(&w); err != nil {
		return SnapshotWrite{}, err
	}
	if err = tx.Commit(); err != nil {
		return SnapshotWrite{}, err
	}
	return w, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func snapshotFrom(q queryer, ctx context.Context, id string) (SnapshotWrite, error) {
	var w SnapshotWrite
	if err := q.QueryRowContext(ctx, "SELECT id,policy_digest FROM snapshot WHERE id=?", id).Scan(&w.ID, &w.PolicyDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return w, ErrNotFound
		}
		return w, err
	}
	// A raw connection can disable foreign-key enforcement.  A node whose
	// well-formed parent key names no document would otherwise disappear from
	// the document-driven traversal below and make a corrupt graph look like a
	// smaller, valid snapshot.
	var orphans int
	if err := q.QueryRowContext(ctx, `
		SELECT count(*)
		FROM node AS n
		LEFT JOIN document AS d ON d.document_id = n.document_id
		WHERE d.document_id IS NULL`,
	).Scan(&orphans); err != nil {
		return w, err
	}
	if orphans != 0 {
		return w, ErrCorrupt
	}
	docs, err := q.QueryContext(ctx, "SELECT document_id,path,content_hash,register,split,admission,language FROM document WHERE snapshot_id=? ORDER BY path", id)
	if err != nil {
		return w, err
	}
	defer docs.Close()
	for docs.Next() {
		var d Document
		if err := docs.Scan(&d.ID, &d.Path, &d.ContentHash, &d.Register, &d.Split, &d.Admission, &d.Language); err != nil {
			return w, ErrCorrupt
		}
		nodes, err := q.QueryContext(ctx, "SELECT node_id,ordinal,kind,role,containers,offset,length,included,exclusion FROM node WHERE document_id=? ORDER BY ordinal", d.ID)
		if err != nil {
			return w, err
		}
		for nodes.Next() {
			var n Node
			var cs string
			var inc int
			if err := nodes.Scan(&n.ID, &n.Ordinal, &n.Kind, &n.Role, &cs, &n.Offset, &n.Length, &inc, &n.Exclusion); err != nil {
				nodes.Close()
				return w, ErrCorrupt
			}
			n.Included = inc != 0
			n.Containers = stringsToContainers(cs)
			var count int
			if err := q.QueryRowContext(ctx, "SELECT count(*) FROM feature_vector WHERE node_id=?", n.ID).Scan(&count); err != nil {
				nodes.Close()
				return w, err
			}
			if count > 1 {
				nodes.Close()
				return w, ErrCorrupt
			}
			if count == 1 {
				v, err := vectorFrom(q, ctx, n.ID)
				if err != nil {
					nodes.Close()
					return w, err
				}
				n.Vector = v
			}
			d.Nodes = append(d.Nodes, n)
		}
		if err := nodes.Err(); err != nil {
			nodes.Close()
			return w, err
		}
		nodes.Close()
		w.Documents = append(w.Documents, d)
	}
	if err := docs.Err(); err != nil {
		return w, err
	}
	return w, nil
}

func vectorFrom(q queryer, ctx context.Context, nodeID string) (*features.Vector, error) {
	var md string
	v := new(features.Vector)
	if err := q.QueryRowContext(ctx, "SELECT manifest_digest,set_version,tokens,lexical_tokens FROM feature_vector WHERE node_id=?", nodeID).Scan(&md, &v.SetVersion, &v.Tokens, &v.LexicalTokens); err != nil {
		return nil, err
	}
	if md != features.ManifestDigest() {
		return nil, ErrCorrupt
	}
	rows, err := q.QueryContext(ctx, "SELECT feature,value,defined,sampling_variance,sampling_variance_defined FROM feature_value WHERE node_id=? AND manifest_digest=?", nodeID, md)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[features.ID]features.FeatureValue{}
	for rows.Next() {
		var x features.FeatureValue
		var d, sd int
		if err := rows.Scan(&x.ID, &x.Value, &d, &x.SamplingVariance, &sd); err != nil {
			return nil, ErrCorrupt
		}
		x.Defined = d != 0
		x.SamplingVarianceDefined = sd != 0
		if _, ok := got[x.ID]; ok {
			return nil, ErrCorrupt
		}
		got[x.ID] = x
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	for _, def := range features.Definitions() {
		x, ok := got[def.ID]
		if !ok {
			return nil, ErrCorrupt
		}
		v.Values = append(v.Values, x)
	}
	if len(got) != len(v.Values) {
		return nil, ErrCorrupt
	}
	return v, nil
}

func validateWrite(w *SnapshotWrite) error {
	if !validHash(w.PolicyDigest) {
		return invalidSnapshot("policy digest")
	}
	if len(w.Documents) == 0 {
		return invalidSnapshot("documents: empty")
	}
	paths := map[string]bool{}
	members := make([]string, 0, len(w.Documents))
	for di := range w.Documents {
		d := &w.Documents[di]
		if !validPath(d.Path) {
			return invalidDocument(d.Path, "path")
		}
		if !validHash(d.ContentHash) {
			return invalidDocument(d.Path, "content hash")
		}
		if !validRegister(d.Register) {
			return invalidDocument(d.Path, "register")
		}
		if !validSplit(d.Split) {
			return invalidDocument(d.Path, "split")
		}
		if !validAdmission(d.Admission) {
			return invalidDocument(d.Path, "admission")
		}
		if !validLanguage(d.Language) {
			return invalidDocument(d.Path, "language")
		}
		if paths[d.Path] {
			return invalidDocument(d.Path, "path: duplicate")
		}
		paths[d.Path] = true
		d.ID = identity.HashInputs(map[string]string{"snapshot": w.ID, "path": d.Path})
		members = append(members, d.Path+"="+d.ContentHash)
		for ni := range d.Nodes {
			n := &d.Nodes[ni]
			if n.Ordinal != ni {
				return invalidNode(d.Path, n.Ordinal, "ordinal")
			}
			if !validNode(n) {
				return invalidNode(d.Path, n.Ordinal, invalidNodeField(n))
			}
			n.ID = identity.HashInputs(map[string]string{"document": d.ID, "ordinal": strconv.Itoa(n.Ordinal)})
			if n.Vector != nil {
				if field := invalidVectorField(n.Vector); field != "" {
					return invalidNode(d.Path, n.Ordinal, "vector "+field)
				}
			}
		}
	}
	sort.Strings(members)
	if w.ID != identity.HashInputs(map[string]string{"policy": w.PolicyDigest, "documents": string(identity.Frame(members...))}) {
		return errSnapshotIdentity
	}
	return nil
}

func writeCopy(w SnapshotWrite) SnapshotWrite {
	copy := w
	copy.Documents = append([]Document(nil), w.Documents...)
	for i := range copy.Documents {
		copy.Documents[i].Nodes = append([]Node(nil), w.Documents[i].Nodes...)
	}
	return copy
}

func invalidSnapshot(field string) error {
	return fmt.Errorf("%w: snapshot %s", ErrInvalid, field)
}

func invalidDocument(path, field string) error {
	return fmt.Errorf("%w: document %q %s", ErrInvalid, path, field)
}

func invalidNode(path string, ordinal int, field string) error {
	return fmt.Errorf("%w: document %q node %d %s", ErrInvalid, path, ordinal, field)
}

func invalidNodeField(n *Node) string {
	switch {
	case n.Ordinal < 0:
		return "ordinal"
	case n.Offset < 0:
		return "offset"
	case n.Length <= 0:
		return "length"
	case !validKind(n.Kind):
		return "kind"
	case !validRole(n.Role):
		return "role"
	case !validExclusion(n.Exclusion):
		return "exclusion"
	default:
		return "containers"
	}
}
func validateStored(w *SnapshotWrite) error {
	if err := validateWrite(w); err != nil {
		return ErrCorrupt
	}
	return nil
}
func sameSnapshot(a, b SnapshotWrite) bool { // all stored fields are represented in the public aggregate.
	if a.ID != b.ID || a.PolicyDigest != b.PolicyDigest || len(a.Documents) != len(b.Documents) {
		return false
	}
	for i := range a.Documents {
		ad, bd := a.Documents[i], b.Documents[i]
		if ad.ID != bd.ID || ad.Path != bd.Path || ad.ContentHash != bd.ContentHash || ad.Register != bd.Register || ad.Split != bd.Split || ad.Admission != bd.Admission || ad.Language != bd.Language || len(ad.Nodes) != len(bd.Nodes) {
			return false
		}
		for j := range ad.Nodes {
			an, bn := ad.Nodes[j], bd.Nodes[j]
			if an.ID != bn.ID || an.Ordinal != bn.Ordinal || an.Kind != bn.Kind || an.Role != bn.Role || an.Offset != bn.Offset || an.Length != bn.Length || an.Included != bn.Included || an.Exclusion != bn.Exclusion || joinContainers(an.Containers) != joinContainers(bn.Containers) || !sameVector(an.Vector, bn.Vector) {
				return false
			}
		}
	}
	return true
}
func sameVector(a, b *features.Vector) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.SetVersion != b.SetVersion || a.Tokens != b.Tokens || a.LexicalTokens != b.LexicalTokens || len(a.Values) != len(b.Values) {
		return false
	}
	for i := range a.Values {
		if a.Values[i] != b.Values[i] {
			return false
		}
	}
	return true
}
func invalidVectorField(v *features.Vector) string {
	if v.SetVersion != features.SetVersion {
		return "set version"
	}
	if v.Tokens < 0 {
		return "tokens"
	}
	if v.LexicalTokens < 0 {
		return "lexical tokens"
	}
	if len(v.Values) != len(features.Definitions()) {
		return "values"
	}
	seen := map[features.ID]bool{}
	defs := features.Definitions()
	for _, x := range v.Values {
		if !finite(x.Value) {
			return "feature " + string(x.ID) + " value"
		}
		if !finite(x.SamplingVariance) {
			return "feature " + string(x.ID) + " sampling variance"
		}
		if seen[x.ID] {
			return "feature " + string(x.ID) + ": duplicate"
		}
		seen[x.ID] = true
	}
	for _, d := range defs {
		if !seen[d.ID] {
			return "feature " + string(d.ID) + ": missing"
		}
	}
	return ""
}
func validNode(n *Node) bool {
	return n.Ordinal >= 0 && n.Offset >= 0 && n.Length > 0 && validKind(n.Kind) && validRole(n.Role) && validExclusion(n.Exclusion) && validContainers(n.Containers)
}
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func validHash(x string) bool {
	if len(x) != 64 {
		return false
	}
	for _, r := range x {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validPath(x string) bool {
	return x != "" && !strings.HasPrefix(x, "/") && !strings.Contains(x, "\\") && !strings.Contains(x, "//") && !strings.Contains(x, "../") && x != ".."
}
func validRegister(x string) bool {
	if len(x) == 0 || len(x) > 32 {
		return false
	}
	for i, r := range x {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || (i > 0 && r == '-')) {
			return false
		}
	}
	return true
}
func validSplit(x corpus.Split) bool {
	return x == corpus.Train || x == corpus.Calibrate || x == corpus.Test || x == corpus.Draft
}
func validAdmission(x corpus.Admission) bool {
	return x == corpus.Eligible || x == corpus.RejectedTooShort || x == corpus.RejectedNotUTF8 || x == corpus.RejectedDuplicate
}
func validLanguage(x string) bool {
	if len(x) < 2 || len(x) > 35 {
		return false
	}
	for _, r := range x {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func validKind(x text.Kind) bool { return x == text.KindContainer || x == text.KindLeaf }
func validRole(x text.Role) bool {
	for _, v := range []text.Role{text.RoleParagraph, text.RoleHeading, text.RoleCodeBlock, text.RoleFrontMatter, text.RoleTableCell, text.RoleFootnote, text.RoleCaption, text.RoleImage, text.RoleHTMLBlock, text.RoleDefinitionTerm, text.RoleDefinitionDescription} {
		if x == v {
			return true
		}
	}
	return false
}
func validExclusion(x text.ExclusionReason) bool {
	return x == text.NotExcluded || x == text.ExcludedByRole || x == text.ExcludedNotSentential || x == text.ExcludedByBlockQuotePolicy
}
func validContainers(xs []text.ContainerKind) bool {
	for _, x := range xs {
		switch x {
		case text.ContainerDocument, text.ContainerBlockQuote, text.ContainerList, text.ContainerListItem, text.ContainerTable, text.ContainerFootnoteSection, text.ContainerFootnote, text.ContainerDefinitionList:
		default:
			return false
		}
	}
	return true
}
func joinContainers(xs []text.ContainerKind) string {
	p := make([]string, len(xs))
	for i, x := range xs {
		p[i] = string(x)
	}
	return strings.Join(p, "|")
}
func stringsToContainers(x string) []text.ContainerKind {
	if x == "" {
		return nil
	}
	p := strings.Split(x, "|")
	out := make([]text.ContainerKind, len(p))
	for i := range p {
		out[i] = text.ContainerKind(p[i])
	}
	return out
}
