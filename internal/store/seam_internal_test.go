package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

// ---------------------------------------------------------------------------
// Fixtures. Deliberately minimal duplicates of the package_test ones: these
// tests need the unexported seam, so they cannot live in store_test.
// ---------------------------------------------------------------------------

const seamBody = "The first paragraph is the span.\n\nAnd a second paragraph follows it.\n"

func seamAdmitted(t *testing.T, body string) []byte {
	t.Helper()
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return doc.Raw()
}

func seamDocument(t *testing.T, root, path string, spans ...text.Span) Document {
	t.Helper()
	onDisk := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(onDisk), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(onDisk, []byte(seamBody), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	nodes := make([]Node, len(spans))
	for i, span := range spans {
		nodes[i] = Node{
			Ordinal: i, Kind: text.KindLeaf, Role: text.RoleParagraph,
			Containers: []text.ContainerKind{text.ContainerDocument},
			Offset:     span.Offset, Length: span.Length, Included: true,
		}
	}
	return Document{
		Path: path, ContentHash: identity.HashBytes(seamAdmitted(t, seamBody)),
		Register: "essays", Split: corpus.Train, Admission: corpus.Eligible,
		Language: corpus.CheckNotPerformed, Nodes: nodes,
	}
}

func seamSnapshot(documents ...Document) SnapshotWrite {
	policy := identity.HashInputs(map[string]string{"policy": "seam"})
	members := make([]string, 0, len(documents))
	for _, document := range documents {
		members = append(members, document.Path+"="+document.ContentHash)
	}
	sort.Strings(members)
	id := identity.HashInputs(map[string]string{
		"policy": policy, "documents": string(identity.Frame(members...)),
	})
	return SnapshotWrite{ID: id, PolicyDigest: policy, Documents: documents}
}

func seamNodeID(write SnapshotWrite, document, ordinal int) string {
	documentID := identity.HashInputs(map[string]string{
		"snapshot": write.ID, "path": write.Documents[document].Path,
	})
	return identity.HashInputs(map[string]string{
		"document": documentID, "ordinal": strconv.Itoa(ordinal),
	})
}

// countingReads records every path the store asks for.
type countingReads struct {
	mu    sync.Mutex
	paths []string
}

func (c *countingReads) ReadFile(path string) ([]byte, error) {
	c.mu.Lock()
	c.paths = append(c.paths, path)
	c.mu.Unlock()
	return os.ReadFile(path)
}

func (c *countingReads) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}

// ---------------------------------------------------------------------------

// "Rehydration opens the file once" is a claim about IO, so it is measured
// against a counted reader rather than argued. Two spans in one document is one
// read; spans in two documents is two.
func TestRehydrationReadsEachDocumentOncePerCall(t *testing.T) {
	for _, c := range []struct {
		name  string
		spans [][]text.Span
		want  int
	}{
		{"two spans in one document", [][]text.Span{{{Offset: 0, Length: 31}, {Offset: 33, Length: 34}}}, 1},
		{"one span in each of two documents", [][]text.Span{{{Offset: 0, Length: 31}}, {{Offset: 0, Length: 31}}}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			reads := &countingReads{}
			s := openSeamed(t, deps{ReadFile: reads.ReadFile, Now: time.Now})

			documents := make([]Document, len(c.spans))
			for i, spans := range c.spans {
				documents[i] = seamDocument(t, root, "essays/"+strconv.Itoa(i)+".md", spans...)
			}
			write := seamSnapshot(documents...)
			if err := s.PutSnapshot(context.Background(), write); err != nil {
				t.Fatalf("PutSnapshot: %v", err)
			}
			var nodeIDs []string
			for i, spans := range c.spans {
				for ordinal := range spans {
					nodeIDs = append(nodeIDs, seamNodeID(write, i, ordinal))
				}
			}

			if _, err := s.Rehydrate(context.Background(), root, nodeIDs); err != nil {
				t.Fatalf("Rehydrate: %v", err)
			}
			if got := reads.count(); got != c.want {
				t.Errorf("%d reads for %d spans, want %d", got, len(nodeIDs), c.want)
			}
		})
	}
}

// unavailable_at is when the document FIRST could not be read. A second failure
// must not restamp it, or "how long has this been gone" becomes unanswerable.
func TestTheFirstUnavailableTimestampIsPreserved(t *testing.T) {
	root := t.TempDir()
	first := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	var now = first
	s := openSeamed(t, deps{ReadFile: os.ReadFile, Now: func() time.Time { return now }})

	write := seamSnapshot(seamDocument(t, root, "essays/a.md", text.Span{Offset: 0, Length: 31}))
	if err := s.PutSnapshot(context.Background(), write); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	nodeID := seamNodeID(write, 0, 0)
	if err := os.Remove(filepath.Join(root, "essays/a.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := s.Rehydrate(context.Background(), root, []string{nodeID}); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	now = first.Add(72 * time.Hour)
	if _, err := s.Rehydrate(context.Background(), root, []string{nodeID}); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	unavailable, err := s.Unavailable(context.Background(), write.ID)
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if got := unavailable["essays/a.md"]; !got.Equal(first) {
		t.Errorf("unavailable_at = %v, want the first failure at %v", got, first)
	}
}

// And it is stamped from the seam, not from the wall clock.
func TestTheUnavailableTimestampComesFromTheClockSeam(t *testing.T) {
	root := t.TempDir()
	stamped := time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC)
	s := openSeamed(t, deps{ReadFile: os.ReadFile, Now: func() time.Time { return stamped }})

	write := seamSnapshot(seamDocument(t, root, "essays/a.md", text.Span{Offset: 0, Length: 31}))
	if err := s.PutSnapshot(context.Background(), write); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "essays/a.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s.Rehydrate(context.Background(), root, []string{seamNodeID(write, 0, 0)}); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	unavailable, err := s.Unavailable(context.Background(), write.ID)
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if got := unavailable["essays/a.md"]; !got.Equal(stamped) {
		t.Errorf("unavailable_at = %v, want the seam's %v", got, stamped)
	}
}

func openSeamed(t *testing.T, d deps) *Store {
	t.Helper()
	s, err := open(filepath.Join(t.TempDir(), "hapax.db"), "sqlite", d)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// The gap slice 2a left
// ---------------------------------------------------------------------------

// A row stream that fails PART WAY THROUGH returns fewer rows and no error
// unless rows.Err() is checked, and a loader that skips that check reads a
// truncated artifact back as a smaller valid one — the exact failure the
// aggregate-integrity rule exists to prevent. Slice 2a fixed four loaders on
// argument; this is the test that was missing.
//
// It does NOT require a loader to stream. An implementation that reads an
// aggregate in a single row has no truncatable cursor and never sees the fault;
// that is correct, and it passes here by returning a COMPLETE artifact. What
// fails is a loader that streams, loses rows, and says nothing — or one that
// reports the loss as corruption, which would tell a user their evidence is
// damaged when a read was merely interrupted.
func TestATruncatedRowStreamIsAnErrorAndNotCorruption(t *testing.T) {
	for _, c := range []struct {
		name string
		// table names the collection to truncate. Every case aims: an unaimed
		// fault is absorbed by whichever nested cursor a loader happens to open
		// first, which for a snapshot is a node's feature values, not the
		// documents the case claims to be about.
		table string
		// load reports whether what came back is complete, alongside its error.
		load func(s *Store, ids seamIDs) (bool, error)
	}{
		{"profile stats", "profile_stat", func(s *Store, ids seamIDs) (bool, error) {
			got, err := s.LoadProfile(context.Background(), ids.Profile)
			return len(got.Stats) == len(features.Definitions()), err
		}},
		{"reference values", "reference_value", func(s *Store, ids seamIDs) (bool, error) {
			got, err := s.LoadReference(context.Background(), ids.Reference)
			total := 0
			for _, values := range got.Values {
				total += len(values)
			}
			return total == 3*len(features.Definitions()), err
		}},
		{"exemplar members", "exemplar_member", func(s *Store, ids seamIDs) (bool, error) {
			got, err := s.LoadExemplarSelection(context.Background(), ids.Selection)
			return len(got.Members) == 3, err
		}},
		{"preserve identifiers", "rewrite_attempt_identifier", func(s *Store, ids seamIDs) (bool, error) {
			got, err := s.LoadRewriteAttempt(context.Background(), ids.Invocation, 0)
			return len(got.PreserveIdentifiers) == 3, err
		}},
		{"a snapshot's documents", "document", func(s *Store, ids seamIDs) (bool, error) {
			got, err := s.Snapshot(context.Background(), ids.Snapshot)
			return wholeSnapshot(got), err
		}},
		{"a document's nodes", "node", func(s *Store, ids seamIDs) (bool, error) {
			got, err := s.Snapshot(context.Background(), ids.Snapshot)
			return wholeSnapshot(got), err
		}},
		{"a node's feature values", "feature_value", func(s *Store, ids seamIDs) (bool, error) {
			got, err := s.Snapshot(context.Background(), ids.Snapshot)
			return wholeSnapshot(got), err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			faults := &rowFaults{}
			name := registerFaultDriver(t, faults)
			s, ids := seededSeamStore(t, name)

			requireNoIndirection(t, s, c.table)
			faults.armRowsIn(c.table, 2)
			complete, err := c.load(s, ids)
			opened, fired := faults.observed()
			faults.disarm()

			// Two branches, because there are two correct implementations. A
			// loader that streams the collection reaches a second row, is
			// truncated, and must say so. One that reads it in a single row —
			// an aggregate — never reaches a second row, is not truncated, and
			// must simply return everything. Anchoring the aim on the table
			// name is what makes those the only two cases: a streaming loader
			// cannot avoid naming the table it streams, however it spells the
			// query, so it cannot dodge the fault and pass as the second kind.
			//
			// That premise holds only while nothing stands between the loader
			// and the table, which requireNoIndirection above checks: a cursor
			// over a VIEW would name the view rather than the table, stream
			// anyway, and never be faulted.
			if fired {
				if errors.Is(err, ErrCorrupt) {
					t.Errorf("an interrupted read was reported as corruption: %v", err)
				} else if !errors.Is(err, errInjectedRowFault) {
					t.Errorf("error = %v, want the injected fault", err)
				}
				return
			}
			if err != nil {
				t.Errorf("a read that lost nothing returned %v", err)
			} else if !complete {
				t.Errorf("a cursor over %s (opened: %v) came back short without "+
					"reporting anything", c.table, opened)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The seam cannot be bypassed
// ---------------------------------------------------------------------------

// A seam that the package can reach around is not a seam. Corpus content may be
// read only through deps.ReadFile, and every stored timestamp must come from
// deps.Now.
//
// os.Stat is deliberately allowed: it reads metadata about the DATABASE file,
// cannot read corpus content, and is the ordering that lets a refused open
// leave the file byte-identical. Stat-then-read is still unrepresentable,
// because the read half has nowhere to go but the seam.
func TestTheStoreCannotReachAroundItsSeam(t *testing.T) {
	forbidden := map[string][]string{
		"os":   {"ReadFile", "Open", "OpenFile", "ReadDir", "WriteFile"},
		"time": {"Now"},
		"io":   {"ReadAll"},
	}
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	var scanned int
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				selector, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				for _, banned := range forbidden[ident.Name] {
					if selector.Sel.Name != banned {
						continue
					}
					if enclosingFunc(file, selector.Pos()) == "realDeps" {
						continue
					}
					t.Errorf("%s uses %s.%s outside realDeps; the seam can be bypassed",
						name, ident.Name, selector.Sel.Name)
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test source was scanned; this guard is vacuous")
	}
}

func enclosingFunc(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Pos() <= pos && pos <= fn.End() {
			return fn.Name.Name
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The fault driver: a thin delegate that fails the Nth Next().
// ---------------------------------------------------------------------------

var errInjectedRowFault = errors.New("injected row fault")

type rowFaults struct {
	mu        sync.Mutex
	remaining int
	armed     bool
	onCommit  bool
	// inTable, when set, aims the fault at cursors that read one table. It is
	// matched on the table NAME with word boundaries rather than on a fragment
	// of SQL: a loader is free to spell, quote or join its query differently,
	// but it cannot read profile_stat without naming profile_stat. Matching a
	// fragment let a renamed query dodge the fault and pass.
	inTable *regexp.Regexp
	opened  bool // a cursor over that table was run
	fired   bool // and the fault actually landed in one
}

// armRowsMatching aims the fault at ONE stream, named by a fragment of its SQL.
// Without it the outermost stream a loader opens always absorbs the fault, and
// the nested ones — a document's nodes, a node's feature values — are never
// reached.
func (f *rowFaults) armRowsIn(table string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remaining, f.armed, f.onCommit = n-1, true, false
	f.inTable = regexp.MustCompile(`\b` + regexp.QuoteMeta(table) + `\b`)
	f.opened, f.fired = false, false
}

// observed reports whether a cursor over the aimed table ran, and whether the
// fault landed in one. Without this a loader that never opened such a cursor is
// indistinguishable from one whose query the aim failed to match.
func (f *rowFaults) observed() (opened, fired bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened, f.fired
}

func (f *rowFaults) noteOpened(query string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.armed && f.inTable != nil && f.inTable.MatchString(query) {
		f.opened = true
	}
}

// armCommit fails the transaction COMMIT. That is the one position every
// transactional implementation must pass through, and it is unambiguously
// AFTER whatever the transaction did — so a graph that is unchanged afterwards
// was restored by a rollback rather than never touched. Faulting the nth Exec
// instead would have assumed a statement count Prune is free to choose.
func (f *rowFaults) armCommit() { f.armCommitAfter(0) }

// armCommitAfter lets n commits through and fails the next. Faulting the FIRST
// commit cannot tell one transaction from four composed ones: at that point
// neither has persisted anything. Letting one through and failing the next is
// what separates them.
func (f *rowFaults) armCommitAfter(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armed, f.onCommit, f.remaining = true, true, n
}

func (f *rowFaults) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armed, f.onCommit = false, false
}

func (f *rowFaults) failsCommit() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.armed || !f.onCommit {
		return false
	}
	if f.remaining > 0 {
		f.remaining--
		return false
	}
	return true
}

// failsRowAt reports whether the nth read OF ONE STREAM should fail. Counting
// per stream rather than globally keeps the fault where it is aimed: a global
// countdown lands somewhere that depends on how many rows the loader happened
// to read first, which differs per loader and moves whenever a fixture grows.
func (f *rowFaults) failsRowAt(nth int, query string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.armed || f.onCommit || nth != f.remaining+1 {
		return false
	}
	if f.inTable != nil && !f.inTable.MatchString(query) {
		return false
	}
	f.fired = true
	return true
}

type faultDriver struct {
	inner  driver.Driver
	faults *rowFaults
}

func (d faultDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return faultConn{conn, d.faults}, nil
}

type faultConn struct {
	inner  driver.Conn
	faults *rowFaults
}

func (c faultConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return faultStmt{inner: stmt, faults: c.faults, query: query}, nil
}
func (c faultConn) Close() error { return c.inner.Close() }
func (c faultConn) Begin() (driver.Tx, error) {
	tx, err := c.inner.Begin()
	if err != nil {
		return nil, err
	}
	return faultTx{tx, c.faults}, nil
}
func (c faultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.inner.(driver.ConnBeginTx)
	if !ok {
		return c.Begin()
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return faultTx{tx, c.faults}, nil
}

type faultTx struct {
	inner  driver.Tx
	faults *rowFaults
}

func (t faultTx) Commit() error {
	if t.faults.failsCommit() {
		_ = t.inner.Rollback()
		return errInjectedRowFault
	}
	return t.inner.Commit()
}
func (t faultTx) Rollback() error { return t.inner.Rollback() }

type faultStmt struct {
	inner  driver.Stmt
	faults *rowFaults
	query  string
}

func (s faultStmt) Close() error  { return s.inner.Close() }
func (s faultStmt) NumInput() int { return s.inner.NumInput() }
func (s faultStmt) Exec(args []driver.Value) (driver.Result, error) {
	// A store that commits by executing the word rather than through the
	// driver's Tx is faulted here; one that uses Tx.Commit is faulted in faultTx.
	if strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(s.query, ";")), "COMMIT") &&
		s.faults.failsCommit() {
		return nil, errInjectedRowFault
	}
	return s.inner.Exec(args)
}
func (s faultStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.faults.noteOpened(s.query)
	rows, err := s.inner.Query(args)
	if err != nil {
		return nil, err
	}
	return &faultRows{inner: rows, faults: s.faults, query: s.query}, nil
}

type faultRows struct {
	inner  driver.Rows
	faults *rowFaults
	query  string
	seen   int
}

func (r *faultRows) Columns() []string { return r.inner.Columns() }
func (r *faultRows) Close() error      { return r.inner.Close() }
func (r *faultRows) Next(dest []driver.Value) error {
	r.seen++
	if r.faults.failsRowAt(r.seen, r.query) {
		return errInjectedRowFault
	}
	return r.inner.Next(dest)
}

var faultDriverNames sync.Map

func registerFaultDriver(t *testing.T, faults *rowFaults) string {
	t.Helper()
	name := "sqlite-fault-" + strconv.Itoa(len(driverNamesSoFar()))
	for {
		if _, taken := faultDriverNames.LoadOrStore(name, true); !taken {
			break
		}
		name += "x"
	}
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("resolving the sqlite driver: %v", err)
	}
	inner := base.Driver()
	_ = base.Close()
	sql.Register(name, faultDriver{inner: inner, faults: faults})
	return name
}

func driverNamesSoFar() []string { return sql.Drivers() }

type seamIDs struct {
	Snapshot, Profile, Reference, Selection, Invocation, Orphan string
}

// seededSeamStore builds one of every artifact whose loader iterates a row
// stream, on a store opened through the fault driver.
func seededSeamStore(t *testing.T, driverName string) (*Store, seamIDs) {
	t.Helper()
	root := t.TempDir()
	s, err := open(filepath.Join(t.TempDir(), "hapax.db"), driverName, realDeps())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	// Three documents AND three spans in the first, so every stream this test
	// truncates has a row after the one the fault lands on — including the
	// snapshot's own document stream.
	write := seamSnapshot(
		seamDocument(t, root, "essays/a.md",
			text.Span{Offset: 0, Length: 31}, text.Span{Offset: 33, Length: 34},
			text.Span{Offset: 0, Length: 10}),
		seamDocument(t, root, "essays/b.md", text.Span{Offset: 0, Length: 31}),
		seamDocument(t, root, "essays/c.md", text.Span{Offset: 0, Length: 31}),
	)
	// A vector on the first node, so the feature_value stream exists to be
	// truncated and so wholeSnapshot has something to find missing.
	write.Documents[0].Nodes[0].Vector = &features.Vector{
		SetVersion: features.SetVersion, Tokens: 9, LexicalTokens: 7, Values: seamVector(),
	}
	if err := s.PutSnapshot(ctx, write); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}
	nodeID := seamNodeID(write, 0, 0)

	prof := Profile{
		ID: identity.HashInputs(map[string]string{"a": "profile"}), SnapshotID: write.ID,
		Register: "essays", Unit: "paragraph", VarianceConvention: "sample",
		ManifestDigest: features.ManifestDigest(), FeatureSetVersion: features.SetVersion,
		MinParagraphLexicalTokens: 40, Stats: seamStats(),
		ProductionReady: false, NotReadyReason: seamNotReadyReason(),
	}
	if err := s.PutProfile(ctx, prof, AdvanceHead); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}
	ref := Reference{
		ID: identity.HashInputs(map[string]string{"a": "reference"}), ProfileID: prof.ID,
		Split: corpus.Calibrate, MinSegments: 30, ManifestDigest: features.ManifestDigest(),
		Values: seamReferenceValues(),
	}
	if err := s.PutReference(ctx, ref); err != nil {
		t.Fatalf("PutReference: %v", err)
	}
	selection := ExemplarSelection{
		ID: identity.HashInputs(map[string]string{"a": "selection"}), ProfileID: prof.ID,
		N: 3, CertificateID: identity.HashInputs(map[string]string{"a": "certificate"}),
		Members: []string{nodeID, seamNodeID(write, 0, 1), seamNodeID(write, 0, 2)},
	}
	if err := s.PutExemplarSelection(ctx, selection); err != nil {
		t.Fatalf("PutExemplarSelection: %v", err)
	}
	attempt := RewriteAttempt{
		InvocationID: identity.HashInputs(map[string]string{"a": "invocation"}),
		ProfileID:    prof.ID, ProviderID: "ollama", NodeID: nodeID,
		CurrentHash:   identity.HashBytes([]byte("current")),
		CandidateHash: identity.HashBytes([]byte("candidate")),
		CurrentBand:   "drifting", CandidateBand: "not-you",
		PreserveIdentifiers: seamIdentifiers(),
		TellsComparison:     1, TellsComparable: true,
		Rejection: "not-preserved",
	}
	if err := s.PutRewriteAttempt(ctx, attempt); err != nil {
		t.Fatalf("PutRewriteAttempt: %v", err)
	}
	// A profile no root reaches, so a Prune told to keep only prof HAS something
	// to remove. Without it a failed prune could roll back nothing and pass.
	orphanWrite := seamSnapshot(seamDocument(t, root, "orphan/a.md", text.Span{Offset: 0, Length: 31}))
	if err := s.PutSnapshot(ctx, orphanWrite); err != nil {
		t.Fatalf("PutSnapshot orphan: %v", err)
	}
	orphan := prof
	orphan.ID = identity.HashInputs(map[string]string{"a": "orphan-profile"})
	orphan.SnapshotID, orphan.Register = orphanWrite.ID, "orphan"
	if err := s.PutProfile(ctx, orphan, AdvanceHead); err != nil {
		t.Fatalf("PutProfile orphan: %v", err)
	}

	return s, seamIDs{
		Snapshot: write.ID, Profile: prof.ID, Reference: ref.ID,
		Selection: selection.ID, Invocation: attempt.InvocationID, Orphan: orphan.ID,
	}
}

func seamStats() []ProfileStat {
	out := make([]ProfileStat, 0, len(features.Definitions()))
	for i, definition := range features.Definitions() {
		out = append(out, ProfileStat{
			Feature: definition.ID, N: 40 + i, Mean: float64(i) + 0.25, Variance: 0.5,
			Defined: true, VarianceDefined: true, MinObservations: 30,
		})
	}
	return out
}

func seamReferenceValues() map[features.ID][]float64 {
	out := map[features.ID][]float64{}
	for i, definition := range features.Definitions() {
		out[definition.ID] = []float64{-1, 0, float64(i)}
	}
	return out
}

func seamIdentifiers() []string {
	result := preserve.Result{Differences: []preserve.Difference{
		{Class: preserve.ClassNumber, Direction: preserve.DirectionLost, Item: "1979"},
		{Class: preserve.ClassURL, Direction: preserve.DirectionInvented, Item: "example.com"},
		{Class: preserve.ClassEntity, Direction: preserve.DirectionLost, Item: "Warsaw"},
	}}
	return result.Identifiers()
}

// Marking and deletion commit in ONE write transaction, so a prune that fails
// after it has already removed something must leave the graph as it found it.
// Cancelling before the call proves nothing about that. The fault is placed on
// the COMMIT, which every transactional implementation must reach and which is
// unambiguously after whatever the transaction did — so a graph that is
// unchanged afterwards was ROLLED BACK, not merely never touched. The prune
// that follows, unfaulted, proves there was something to roll back.
func TestAPruneThatFailsPartWayThroughRemovesNothing(t *testing.T) {
	faults := &rowFaults{}
	name := registerFaultDriver(t, faults)
	s, ids := seededSeamStore(t, name)
	before := graphCensus(t, s)

	faults.armCommit()
	_, err := s.Prune(context.Background(), []string{ids.Profile})
	faults.disarm()
	if !errors.Is(err, errInjectedRowFault) {
		t.Fatalf("error = %v, want the fault injected into Prune's commit", err)
	}

	if after := graphCensus(t, s); !reflect.DeepEqual(after, before) {
		t.Errorf("a failed prune left the graph as\n%v\nwant\n%v", after, before)
	}
	// And the orphan really was removable, so the rollback above meant something.
	faults.disarm()
	pruned, err := s.Prune(context.Background(), []string{ids.Profile})
	if err != nil {
		t.Fatalf("Prune after disarming: %v", err)
	}
	if pruned.Profiles == 0 {
		t.Error("nothing was prunable; the rollback proved nothing")
	}
	if _, err := s.LoadProfile(context.Background(), ids.Orphan); !errors.Is(err, ErrNotFound) {
		t.Errorf("the orphan survived an unfaulted prune: %v", err)
	}
}

// graphCensus counts every table, so a partial commit anywhere is visible.
func graphCensus(t *testing.T, s *Store) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{
		"snapshot", "document", "node", "feature_vector", "feature_value",
		"profile", "profile_stat", "profile_head", "reference", "reference_value",
		"threshold", "eval_result", "exemplar_selection", "exemplar_member",
		"rewrite_attempt", "rewrite_attempt_identifier",
	} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		out[table] = count
	}
	return out
}

// wholeSnapshot is the aggregate seededSeamStore stores, all the way down: a
// count of documents alone would accept a tree whose nodes or vectors were lost.
func wholeSnapshot(got SnapshotWrite) bool {
	if len(got.Documents) != 3 {
		return false
	}
	first := got.Documents[0] // ordered by path, so essays/a.md
	if len(first.Nodes) != 3 {
		return false
	}
	// The vector is REQUIRED, not merely checked when present: a lost one would
	// otherwise read back as a leaf that never had features.
	vector := first.Nodes[0].Vector
	return vector != nil && len(vector.Values) == len(features.Definitions())
}

func seamVector() []features.FeatureValue {
	out := make([]features.FeatureValue, 0, len(features.Definitions()))
	for i, definition := range features.Definitions() {
		out = append(out, features.FeatureValue{
			ID: definition.ID, Value: float64(i) + 0.5, Defined: true,
			SamplingVariance: 0.25, SamplingVarianceDefined: true,
		})
	}
	return out
}

// requireNoIndirection fails if anything could stand between a loader and the
// table it reads — a view, or a virtual table — which would let a streaming
// cursor name something other than the table the fault is aimed at.
func requireNoIndirection(t *testing.T, s *Store, table string) {
	t.Helper()
	for _, catalogue := range []string{"sqlite_master", "sqlite_temp_master"} {
		var count int
		err := s.db.QueryRow(
			"SELECT count(*) FROM " + catalogue + " WHERE type = 'view' OR sql LIKE '%VIRTUAL%'").Scan(&count)
		if err != nil {
			t.Fatalf("reading %s: %v", catalogue, err)
		}
		if count != 0 {
			t.Fatalf("%s declares %d views or virtual tables; a cursor could read %s "+
				"without naming it", catalogue, count, table)
		}
	}
}

// Index is ONE transaction, not four writers composed. Faulting its FIRST
// commit proves nothing — neither shape has persisted anything yet. So one
// commit is let through and the next is failed: a single-transaction Index has
// only one commit, never reaches the fault, and completes; a composed Index
// commits its snapshot and then fails, leaving a graph that is neither what it
// was nor what it should be.
//
// The assertion is therefore the property itself: afterwards the store is
// EITHER fully indexed OR exactly as it was, and never in between.
func TestAnIndexIsAllOrNothing(t *testing.T) {
	faults := &rowFaults{}
	name := registerFaultDriver(t, faults)
	s, ids := seededSeamStore(t, name)
	before := graphCensus(t, s)

	root := t.TempDir()
	write := seamSnapshot(seamDocument(t, root, "indexed/a.md", text.Span{Offset: 0, Length: 31}))
	indexed := seamProfile(write.ID)

	faults.armCommitAfter(1)
	_, err := s.Index(context.Background(), IndexWrite{
		Mode: IndexProfile, Snapshot: write, Profile: indexed,
	})
	faults.disarm()

	after := graphCensus(t, s)
	if err == nil {
		// One transaction: it never reached the fault, so it committed whole.
		if _, err := s.Snapshot(context.Background(), write.ID); err != nil {
			t.Errorf("Index reported success without its snapshot: %v", err)
		}
		if _, err := s.LoadProfile(context.Background(), indexed.ID); err != nil {
			t.Errorf("Index reported success without its profile: %v", err)
		}
		return
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("a failed index left the graph part way:\n%v\nwant\n%v", after, before)
	}
	if _, err := s.LoadProfile(context.Background(), ids.Orphan); err != nil {
		t.Errorf("a failed index pruned the orphan anyway: %v", err)
	}
}

// seamProfile is a valid profile over a seeded snapshot.
func seamProfile(snapshotID string) Profile {
	return Profile{
		ID: identity.HashInputs(map[string]string{"a": "indexed-profile"}), SnapshotID: snapshotID,
		Register: "indexed", Unit: "paragraph", VarianceConvention: "sample",
		ManifestDigest: features.ManifestDigest(), FeatureSetVersion: features.SetVersion,
		MinParagraphLexicalTokens: 40, Stats: seamStats(),
		ProductionReady: false, NotReadyReason: seamNotReadyReason(),
	}
}

func seamNotReadyReason() string {
	reasons := profile.NotReadyReasons()
	if len(reasons) == 0 {
		return ""
	}
	return reasons[0]
}
