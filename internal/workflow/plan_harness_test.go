package workflow_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval/evaltest"
	"github.com/fissible/hapax/internal/exemplar"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/text"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// Draft bodies
// ---------------------------------------------------------------------------

const (
	// twoParagraphs is the ordinary case: two admitted paragraphs, no excisions,
	// no structure to confuse the leaf sequence with the admitted one.
	twoParagraphs = "A paragraph of ordinary prose that runs on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing.\n\n" +
		"A second paragraph doing likewise, at enough length to clear the floor and " +
		"be measured on its own terms rather than skipped.\n\n"

	// interleavedDraft puts excluded leaves between the admitted ones, so an
	// implementation that used a node's ordinal where a segment index belongs
	// picks the wrong paragraph rather than getting away with it.
	interleavedDraft = "# A heading, which is a leaf and is not admitted\n\n" +
		"The first admitted paragraph, running past a single sentence so that the " +
		"structure pass reads it as prose; it carries an ordinary thought.\n\n" +
		"```\ncode block, also a leaf, also not admitted\n```\n\n" +
		"## Another heading\n\n" +
		"The second admitted paragraph, likewise long enough to be measured and " +
		"deliberately placed after two excluded leaves.\n\n" +
		"A third admitted paragraph so the sequence is long enough to go wrong in " +
		"more than one way, and long enough to clear the lexical floor.\n\n"

	// excisionDraft mixes a paragraph assemble can splice with one it cannot. The
	// DESIGN table measured exactly two constructs that produce excisions in an
	// included leaf: inline code, and a footnote reference. One each, so an
	// implementation that special-cased a single syntax is caught.
	excisionDraft = "An ordinary paragraph with no inline syntax at all, long enough to be " +
		"admitted and measured, and spliceable by assemble without complaint.\n\n" +
		"A paragraph with `inline code` in it, which the run text drops, so splicing " +
		"a rewrite over its raw span would delete what the user wrote.\n\n" +
		"A third ordinary paragraph, again with nothing inline, so a plan that " +
		"refused every paragraph would be caught out.\n\n"

	// footnoteDraft is the other excision construct on its own, because inline
	// code and a footnote reference reach the refusal by different routes through
	// the structure parser and an implementation can get one without the other.
	footnoteDraft = "An ordinary paragraph carrying no inline syntax whatever, long enough to " +
		"be admitted and measured and spliced without complaint.\n\n" +
		"A paragraph that cites something and carries a footnote reference for it,[^1] " +
		"which the run text drops exactly as inline code is dropped.\n\n" +
		"[^1]: The note itself, which is a leaf of its own and not admitted.\n\n"
)

// ---------------------------------------------------------------------------
// Driving Plan
// ---------------------------------------------------------------------------

func planRequest(root, path string) workflow.RewriteRequest {
	return workflow.RewriteRequest{
		StorePath: defaultStorePath(root), CorpusRoot: root, Register: "essays", Path: path,
	}
}

func planned(t *testing.T, request workflow.RewriteRequest) workflow.RewritePlan {
	t.Helper()
	plan, err := workflow.Default().Plan(ctx(), request)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return plan
}

// ---------------------------------------------------------------------------
// Bands, chosen rather than accepted
// ---------------------------------------------------------------------------

// bandedStore is an indexed corpus with a shippable release whose boundaries are
// placed so that every measured draft distance lands in the named band.
//
// Band membership is otherwise whatever the fixture corpus happens to measure,
// which makes "this paragraph is a rewrite target" an assertion about an
// accident. So the boundaries are chosen, and then CHECKED against what the
// draft actually measures: the helper scores the draft before installing the
// release, and fails if the distances do not sit where the band rule needs them.
// Choosing centres without checking is how the first version of this helper
// produced two populations that overlapped, an AUC of 0.26, and a fixture that
// died on the discrimination floor.
//
// One measured fact this rests on, and it is a property of the fixture corpus
// rather than of hapax: every paragraph of every draft here measures the same
// distance, 2.400036. The template corpus is sixty copies of one sentence
// pattern, so its reference distribution is degenerate, and any real prose falls
// outside it on every feature at once — Reference.Transform maps each feature to
// a rank quantile, every rank saturates at the same end, and the mean of the
// absolute deviations is therefore a constant. A test that needs one paragraph
// in-range and another not-you cannot be built on this corpus at all; a mixed
// plan has to come from dispositions, which is what the excision fixture does.
func bandedStore(t *testing.T, band string) string {
	t.Helper()
	// The band rule is: distance <= Low is in-range, distance >= High is not-you,
	// anything between is drifting.
	var authorCentre, distractorCentre float64
	switch band {
	case "in-range":
		authorCentre, distractorCentre = 1e5, 1e6
	case "drifting":
		authorCentre, distractorCentre = 1.0, 5.0
	case "not-you":
		authorCentre, distractorCentre = 1e-4, 1e-3
	default:
		t.Fatalf("bandedStore does not build %q", band)
	}

	root := indexedCorpus(t)
	opened := openStore(t, defaultStorePath(root))
	bundle, err := opened.LoadProfileBundle(ctx(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	if bundle.Reference.ID == "" {
		t.Fatal("the indexed template has no reference to calibrate against")
	}
	release := evaltest.ReleaseAround(t, bundle.Profile.ID, bundle.Reference.ID, authorCentre, distractorCentre)
	requireBoundariesProduce(t, root, band, release.Calibration.Low, release.Calibration.High)
	if err := opened.PutRelease(ctx(), release, "", store.AdvanceHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}
	return root
}

// requireBoundariesProduce measures every draft body these tests use against the
// release that is about to be installed, and fails unless the band rule sends
// all of them to the wanted band. Without it, a change to the corpus fixture, to
// the feature manifest or to the quantile arithmetic would move the distances
// and leave every band assertion downstream passing vacuously.
func requireBoundariesProduce(t *testing.T, root, band string, low, high float64) {
	t.Helper()
	if !(low < high) {
		t.Fatalf("the release has Low=%g and High=%g, which is not an ordered pair", low, high)
	}
	for name, body := range draftBodies() {
		draft := writeDraft(t, filepath.Join(t.TempDir()), body)
		report := scored(t, workflow.ScoreRequest{
			StorePath: defaultStorePath(root), Register: "essays", Path: draft,
		})
		if len(report.Segments) == 0 {
			t.Fatalf("%s measured no segments", name)
		}
		for _, segment := range report.Segments {
			if !segment.Distance.Defined {
				t.Fatalf("%s segment %d has no distance (%s)", name, segment.Index, segment.Distance.Reason)
			}
			value := segment.Distance.Value
			var ok bool
			switch band {
			case "in-range":
				ok = value <= low
			case "drifting":
				ok = value > low && value < high
			case "not-you":
				ok = value >= high
			}
			if !ok {
				t.Fatalf("%s segment %d measures %g, which is not %s against Low=%g High=%g; "+
					"the fixture would assert nothing", name, segment.Index, value, band, low, high)
			}
		}
	}
}

// draftBodies is every body these tests plan, so the boundary check covers all
// of them rather than whichever one a caller happens to use.
func draftBodies() map[string]string {
	return map[string]string{
		"twoParagraphs":    twoParagraphs,
		"interleavedDraft": interleavedDraft,
		"excisionDraft":    excisionDraft,
		"footnoteDraft":    footnoteDraft,
	}
}

// ---------------------------------------------------------------------------
// Probes
// ---------------------------------------------------------------------------

// openRawStore is a second connection for counting rows no API exposes.
func openRawStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// removeCorpusDocuments deletes every corpus file, leaving the draft and the
// store. An implementation that rehydrates in order to select exemplars cannot
// survive it, which is a stronger statement than counting calls to a reader.
func removeCorpusDocuments(t *testing.T, root, draft string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if path == draft {
			continue
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
		removed++
	}
	if removed == 0 {
		t.Fatal("no corpus documents were removed, so nothing was proved by removing them")
	}
}

// ---------------------------------------------------------------------------
// An independent reading of a draft
// ---------------------------------------------------------------------------

// admittedLeaf is one admitted paragraph of a draft, read from the draft itself.
type admittedLeaf struct {
	Text           string
	Offset, Length int
	HasExcisions   bool
}

// admittedLeaves re-derives the admitted paragraph sequence from a draft's own
// bytes, so a plan can be checked against the file rather than against score.
// Both the plan and score reach their sequence through
// profile.ParagraphLeaves; comparing them to each other would let one shared
// ordinal mistake agree with itself.
//
// HasExcisions is the same question assemble asks — whether the leaf's raw span
// contains bytes its run text drops — so a test can assert the disposition of
// each paragraph rather than merely that some paragraph got each disposition.
func admittedLeaves(t *testing.T, path string, floor int) []admittedLeaf {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	document, err := text.Admit(raw)
	if err != nil {
		t.Fatalf("admit %s: %v", path, err)
	}
	var out []admittedLeaf
	for _, leaf := range document.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
		tokens, err := document.RunTokens(leaf)
		if err != nil {
			t.Fatalf("run tokens: %v", err)
		}
		if features.Extract(tokens).LexicalTokens < floor {
			continue
		}
		out = append(out, admittedLeaf{
			Text:         string(document.Raw()[leaf.Span.Offset : leaf.Span.Offset+leaf.Span.Length]),
			Offset:       leaf.Span.Offset,
			Length:       leaf.Span.Length,
			HasExcisions: len(leaf.Excisions) != 0,
		})
	}
	if len(out) == 0 {
		t.Fatalf("%s admitted no paragraphs at floor %d", path, floor)
	}
	return out
}

// storedFloor is the floor the profile in this store was fitted under, which is
// the one that decides which paragraphs are admitted.
func storedFloor(t *testing.T, root string) int {
	t.Helper()
	return persistedBundle(t, defaultStorePath(root), "essays").Profile.MinParagraphLexicalTokens
}

// ---------------------------------------------------------------------------
// An independent exemplar selection
// ---------------------------------------------------------------------------

type expectedSelection struct {
	selectionID string
	nodes       []string
}

// selectFromStore runs exemplar.Select over the same persisted metadata the
// implementation has, and reports which nodes it picks.
//
// This is an oracle rather than a reimplementation of the feature: the algorithm
// under test is exemplar's, and what B1 has to get right is the plumbing —
// which candidates the pool is built from, how a candidate maps back to a node,
// and that the order survives. All three are things a plausible implementation
// gets subtly wrong, and none is visible from "three nodes were selected".
//
// Candidates come from the TRAIN split. The calibrate and test splits are held
// out to measure the profile with, and putting held-out paragraphs in the prompt
// would pull a rewrite toward the very text the release's gates measured against.
func selectFromStore(t *testing.T, root string) expectedSelection {
	t.Helper()
	opened := openStore(t, defaultStorePath(root))
	bundle, err := opened.LoadProfileBundle(ctx(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	written, err := opened.Snapshot(ctx(), bundle.Profile.SnapshotID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	var candidates []exemplar.Candidate
	nodeFor := map[string]string{}
	for _, document := range written.Documents {
		if document.Split != corpus.Train {
			continue
		}
		for _, leaf := range document.Nodes {
			if leaf.Vector == nil {
				continue
			}
			candidate := exemplar.Candidate{
				DocumentDigest: document.ContentHash,
				Span:           text.Span{Offset: leaf.Offset, Length: leaf.Length},
				Role:           leaf.Role,
				Containers:     leaf.Containers,
				Split:          document.Split,
				Vector:         *leaf.Vector,
			}
			candidates = append(candidates, candidate)
			nodeFor[candidate.Identity()] = leaf.ID
		}
	}
	if len(candidates) == 0 {
		t.Fatal("the profile's snapshot holds no train paragraphs to select from")
	}

	fitted, err := bundle.Profile.Fitted()
	if err != nil {
		t.Fatalf("Fitted: %v", err)
	}
	selection, err := exemplar.Select(fitted, candidates, exemplar.DefaultConfig())
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	out := expectedSelection{selectionID: selection.ID}
	for _, chosen := range selection.Exemplars {
		node, ok := nodeFor[chosen.Identity()]
		if !ok {
			t.Fatalf("selected candidate %q maps to no node", chosen.Identity())
		}
		out.nodes = append(out.nodes, node)
	}
	return out
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

// census fingerprints the whole database: for every table, its schema and the
// full contents of every row, hashed.
//
// Counting rows was not enough, and the gap is a real escape rather than a
// refinement: an implementation that UPDATES a profile, moves a head, or
// replaces one row with another leaves every count where it was, and "this plan
// wrote nothing" would still pass. Rows are sorted before hashing because SQLite
// makes no promise about the order it returns them in.
func census(t *testing.T, root string) map[string]string {
	t.Helper()
	raw := openRawStore(t, defaultStorePath(root))
	names, err := raw.Query("SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	schema := map[string]string{}
	for names.Next() {
		var name string
		var ddl sql.NullString
		if err := names.Scan(&name, &ddl); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		schema[name] = ddl.String
	}
	if err := names.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	_ = names.Close()
	if len(schema) == 0 {
		t.Fatal("the store has no tables, so a census of it proves nothing")
	}

	out := map[string]string{}
	for table, ddl := range schema {
		out[table] = identity.HashInputs(map[string]string{
			"schema": ddl,
			"rows":   string(identity.Frame(tableRows(t, raw, table)...)),
		})
	}
	return out
}

func tableRows(t *testing.T, raw *sql.DB, table string) []string {
	t.Helper()
	rows, err := raw.Query("SELECT * FROM " + table)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		into := make([]any, len(columns))
		for i := range values {
			into[i] = &values[i]
		}
		if err := rows.Scan(into...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = fmt.Sprintf("%v", value)
		}
		out = append(out, string(identity.Frame(parts...)))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	sort.Strings(out)
	return out
}

// assertUnchanged fails naming the tables whose contents a plan altered when it
// should have altered none.
func assertUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return
	}
	for table, was := range before {
		now, present := after[table]
		if !present {
			t.Errorf("table %s disappeared", table)
			continue
		}
		if now != was {
			t.Errorf("the contents of %s changed", table)
		}
	}
	for table := range after {
		if _, existed := before[table]; !existed {
			t.Errorf("table %s appeared", table)
		}
	}
}

// assertSegmentsResolveIntoTheDraftSnapshot checks that every node a plan names
// is in the snapshot the plan says it indexed, and spans the bytes the plan
// reported. Shared, because it has to hold on the no-op path too: a plan that
// reports segments has to have somewhere for their nodes to live, whether or not
// any of them is going to be rewritten.
func assertSegmentsResolveIntoTheDraftSnapshot(t *testing.T, root, draft string, plan workflow.RewritePlan) {
	t.Helper()
	if plan.DraftSnapshotID == "" {
		t.Fatalf("the plan reports %d segments and no snapshot to find their nodes in", len(plan.Segments))
	}
	if len(plan.Segments) == 0 {
		t.Fatal("no segments, so this assertion proves nothing")
	}
	opened := openStore(t, defaultStorePath(root))

	// The snapshot holds THIS draft. Consistency between the reported nodes and
	// some snapshot is not enough: a same-shaped snapshot from a previous run, or
	// a corpus document with the same paragraph count, satisfies it.
	written, err := opened.Snapshot(ctx(), plan.DraftSnapshotID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(written.Documents) != 1 {
		t.Fatalf("the draft snapshot holds %d documents", len(written.Documents))
	}
	document := written.Documents[0]
	if want := admittedContentHash(t, draft); document.ContentHash != want {
		t.Errorf("the snapshot holds content %s; %s hashes to %s", document.ContentHash, draft, want)
	}
	if base := filepath.Base(draft); document.Path != base {
		t.Errorf("the stored draft is at path %q, want %q relative to its own root", document.Path, base)
	}
	if document.Split != corpus.Draft {
		t.Errorf("the draft is stored in the %q split", document.Split)
	}
	for _, segment := range plan.Segments {
		span, err := opened.Span(ctx(), segment.NodeID)
		if err != nil {
			t.Fatalf("segment %d names node %q, which the store does not have: %v",
				segment.Index, segment.NodeID, err)
		}
		if span.SnapshotID != plan.DraftSnapshotID {
			t.Errorf("segment %d belongs to snapshot %s, not the planned draft %s",
				segment.Index, span.SnapshotID, plan.DraftSnapshotID)
		}
		if span.Offset != segment.Offset || span.Length != segment.Length {
			t.Errorf("segment %d spans %d+%d; its stored node spans %d+%d",
				segment.Index, segment.Offset, segment.Length, span.Offset, span.Length)
		}
	}
}

// admittedContentHash is what the store records for a document: the hash of the
// ADMITTED bytes, which is not the hash of the file — Admit strips a BOM.
func admittedContentHash(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	document, err := text.Admit(raw)
	if err != nil {
		t.Fatalf("admit %s: %v", path, err)
	}
	return identity.HashBytes(document.Raw())
}
