package workflow_test

// The fixture Execute is tested against, and why it is not the one B1 uses.
//
// # The plan fixture cannot express an acceptance, and it took a measurement to
// # notice
//
// plan_harness_test.go records that every paragraph of every draft it plans
// measures the same distance, 2.400036, because the template corpus is sixty
// copies of one sentence pattern: the reference distribution is degenerate, every
// feature's rank saturates at the same end, and the mean of the absolute
// deviations is therefore a constant.
//
// For planning that is merely inconvenient. For execution it is fatal.
// Acceptance requires d(candidate) <= d(current) - epsilon, and against a
// degenerate reference EVERY piece of real prose measures that same constant —
// so no candidate can ever improve, and the accepted path could not be reached
// at all. Every acceptance assertion would have been vacuous.
//
// So this file builds its own corpus out of paragraphs that genuinely differ in
// length, clause structure and comma density. Against its reference the same
// draft paragraph measures 1.3389 and a rewritten one 0.5580, which is what
// makes an acceptance a real event rather than a scripted one.
//
// # Nothing here is asserted on trust
//
// Band membership and acceptability are properties of the fixture rather than of
// hapax, and a fixture edit could move them without any test noticing. So both
// are CHECKED before they are relied on: requireTargets measures the draft
// against the release that is about to be installed, and requireCandidates
// measures every scripted candidate the way the loop will — distance, preserve
// and tells — and fails if the outcome the test needs is not the outcome the
// fixture produces. This is the discipline requireBoundariesProduce established
// in B1, applied to the thing B2b-1 actually decides.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/eval/evaltest"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/rewrite"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/tells"
	"github.com/fissible/hapax/internal/text"
	"github.com/fissible/hapax/internal/workflow"
)

// ---------------------------------------------------------------------------
// A corpus with a reference distribution that is not a single point
// ---------------------------------------------------------------------------

// variedShapes are paragraph forms that differ on the features the profile
// measures: sentence length, clause count, comma density, word length. Ten of
// them, rotated, so no document is a copy of its neighbour.
var variedShapes = []string{
	"The %d question here is a short one, and it is put plainly, without much in the way of hedging or apparatus.\n\n",
	"When the matter came up again in the %d meeting, which it did rather sooner than anyone had planned for, the argument that followed was long, circuitous, hedged about with qualifications, and in the end not especially conclusive about anything at all.\n\n",
	"Consider %d. Consider it again. The point is small. The point is repeated. Short sentences carry it.\n\n",
	"I had thought, before the %d revision, that the whole business would resolve itself; it did not, and the reasons why it did not are worth setting down at some length, because they recur.\n\n",
	"Numbers, commas, clauses, and a certain fondness for the list: these are the marks of the %d paragraph, and they are marks that a reader learns to recognise quickly enough.\n\n",
	"It is enough to say that the %d case failed, and that nobody involved was much surprised by it.\n\n",
	"Every so often a paragraph arrives that wants to be much longer than its neighbours, and this is one of them; it accumulates clauses, it doubles back on itself, it qualifies what it has just said, and it declines to stop until the reader has entirely lost the thread of the original claim about %d.\n\n",
	"Plain prose for %d, of moderate length, with one comma, and nothing else remarkable about it whatsoever.\n\n",
}

func writeVariedCorpusInto(root string, documents int) error {
	for i := 0; i < documents; i++ {
		body := ""
		for p := 0; p < 10; p++ {
			body += fmt.Sprintf(variedShapes[(i+p)%len(variedShapes)], i*100+p)
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("doc%03d.md", i)), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Indexed once for the whole package and copied per test, for the reason
// harness_test.go gives: indexing sixty documents per case was the most
// expensive thing in this package.
var variedTemplate = sync.OnceValues(func() (string, error) {
	root, err := os.MkdirTemp("", "hapax-varied-template")
	if err != nil {
		return "", err
	}
	if err := writeVariedCorpusInto(root, 60); err != nil {
		return "", err
	}
	// Two registers over one corpus. Profile identity includes the register, so
	// the second is a genuinely different profile sharing the same snapshot —
	// which is what makes "this selection belongs to another profile" testable
	// without a second corpus, and the selection binding distinguishable from a
	// profile that simply will not load.
	for _, register := range []string{"essays", "letters"} {
		if _, err := workflow.Default().Index(context.Background(), workflow.IndexRequest{
			CorpusRoot: root, Register: register,
		}); err != nil {
			return "", err
		}
	}
	return root, nil
})

func variedCorpus(t *testing.T) string {
	t.Helper()
	return copyOfTemplate(t, variedTemplate, "essays", "letters")
}

// anotherProfileID is the head of the second register: a real, loadable profile
// that the draft's exemplar selection does not belong to.
func anotherProfileID(t *testing.T, root string) string {
	t.Helper()
	bundle, err := openStore(t, defaultStorePath(root)).LoadProfileBundle(ctx(), "letters")
	if err != nil {
		t.Fatalf("LoadProfileBundle(letters): %v", err)
	}
	if bundle.Profile.ID == "" {
		t.Fatal("the second register has no profile head")
	}
	return bundle.Profile.ID
}

// ---------------------------------------------------------------------------
// The draft, and the candidates a provider will offer for it
// ---------------------------------------------------------------------------

const (
	// Two paragraphs, both admitted, neither carrying excisions, so both are
	// spliceable and both can be targets.
	paragraphOne = "A paragraph of ordinary prose that runs on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing."
	paragraphTwo = "A second paragraph doing likewise, at enough length to clear the floor and " +
		"be measured on its own terms rather than skipped."

	// A candidate for each that measures closer to the author, keeps every
	// protected surface form, and adds no tells.
	improvesOne = "A paragraph of ordinary prose runs on past a single sentence. The structure " +
		"pass reads it as prose rather than as a heading, and it says a thing."
	improvesTwo = "A second paragraph doing likewise. It has enough length to clear the floor, " +
		"and is measured on its own terms rather than skipped."

	// A candidate that measures EXACTLY what the current text measures. It is
	// rejected, and specifically as not-improved rather than by either guard,
	// which is what makes it useful: it separates "the loop ran and refused"
	// from "the loop never got that far".
	matchesOne = "A passage of plain prose that carries on past a single sentence so the " +
		"structure pass reads it as prose rather than as a heading; it says a thing."
)

// The heading is not admitted and never appears in a plan's segments, but it
// does put a node WITHOUT a vector in the draft's own snapshot — which is the
// thing a hand-built plan could point a target at, and cannot be built from a
// draft that is nothing but paragraphs.
func executableDraft() string {
	return "# A heading, which is a leaf and is not admitted\n\n" +
		paragraphOne + "\n\n" + paragraphTwo + "\n\n"
}

// repeatedDraft says the same paragraph twice. Which occurrence a rewrite lands
// on is invisible in a draft whose paragraphs are all distinct, and it is the
// byte-ownership question this slice exists to get right.
func repeatedDraft() string { return paragraphOne + "\n\n" + paragraphOne + "\n\n" }

// ---------------------------------------------------------------------------
// Stores, with the release CHOSEN and then checked
// ---------------------------------------------------------------------------

// targetStore is a varied corpus whose release places both draft paragraphs
// outside in-range, so a plan over the draft has two targets.
func targetStore(t *testing.T) (root, draft string) {
	t.Helper()
	root = installRelease(t, 0.05, 5.0)
	draft = writeDraft(t, root, executableDraft())
	requireDispositions(t, root, draft, workflow.DispositionTarget, workflow.DispositionTarget)
	return root, draft
}

// settledStore is the same corpus whose release places both paragraphs in-range,
// so a plan over the same draft has nothing to change.
func settledStore(t *testing.T) (root, draft string) {
	t.Helper()
	root = installRelease(t, 2.0, 8.0)
	draft = writeDraft(t, root, executableDraft())
	requireDispositions(t, root, draft, workflow.DispositionInRange, workflow.DispositionInRange)
	return root, draft
}

func installRelease(t *testing.T, authorCentre, distractorCentre float64) string {
	t.Helper()
	root := variedCorpus(t)
	opened := openStore(t, defaultStorePath(root))
	bundle, err := opened.LoadProfileBundle(ctx(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	if bundle.Reference.ID == "" {
		t.Fatal("the varied template indexed no reference, so nothing can be calibrated against it")
	}
	release := evaltest.ReleaseAround(t, bundle.Profile.ID, bundle.Reference.ID, authorCentre, distractorCentre)
	if !release.Shippable {
		t.Fatalf("the crafted release is not shippable (%s), so no plan built on it can have targets", release.Reason)
	}
	if err := opened.PutRelease(ctx(), release, "", store.AdvanceHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}
	return root
}

// requireDispositions fails unless planning the draft gives exactly these
// dispositions, in order. Without it a fixture edit could move a distance across
// a boundary and turn every downstream assertion vacuous while every test still
// passed — the release boundaries are quantiles of a crafted population and the
// draft's distances are whatever the corpus happens to produce, so their
// relationship is not something this file controls.
func requireDispositions(t *testing.T, root, draft string, want ...workflow.Disposition) {
	t.Helper()
	plan := planned(t, planRequest(root, draft))
	if plan.Refusal != "" {
		t.Fatalf("planning the draft refused %q; the fixture cannot exercise execution", plan.Refusal)
	}
	if len(plan.Segments) != len(want) {
		t.Fatalf("the plan has %d segments and the fixture needs %d", len(plan.Segments), len(want))
	}
	for i, disposition := range want {
		if plan.Segments[i].Disposition != disposition {
			t.Fatalf("segment %d is %q and the fixture needs %q; band %q against this release",
				i, plan.Segments[i].Disposition, disposition, plan.Segments[i].Band.Band)
		}
	}
}

// ---------------------------------------------------------------------------
// The candidates, measured the way the loop measures them
// ---------------------------------------------------------------------------

type verdict struct {
	distance, currentDistance float64
	improves, preserved       bool
	comparison                int
	comparable                bool
	segments                  int
	identifiers               []string
}

// judge measures a candidate against a current text exactly as the loop's three
// seams will: the real scorer bound to the store's release, real preserve, real
// tells. It is the oracle the fixture checks itself against.
func judge(t *testing.T, root, current, candidate string) verdict {
	t.Helper()
	currentReport := scored(t, workflow.ScoreRequest{
		StorePath: defaultStorePath(root), Register: "essays", Path: writeProbe(t, current),
	})
	candidateReport := scored(t, workflow.ScoreRequest{
		StorePath: defaultStorePath(root), Register: "essays", Path: writeProbe(t, candidate),
	})
	if len(currentReport.Segments) != 1 {
		t.Fatalf("the current text measures %d segments; the loop requires exactly one", len(currentReport.Segments))
	}
	out := verdict{segments: len(candidateReport.Segments), currentDistance: currentReport.Segments[0].Distance.Value}
	if out.segments != 1 {
		return out
	}
	out.distance = candidateReport.Segments[0].Distance.Value
	out.improves = out.distance <= currentReport.Segments[0].Distance.Value-rewrite.Epsilon

	preservation, err := preserve.Check(current, candidate)
	if err != nil {
		t.Fatalf("preserve.Check: %v", err)
	}
	out.preserved, out.identifiers = preservation.Preserved, preservation.Identifiers()

	currentDoc, err := text.Admit([]byte(current))
	if err != nil {
		t.Fatalf("admit current: %v", err)
	}
	candidateDoc, err := text.Admit([]byte(candidate))
	if err != nil {
		t.Fatalf("admit candidate: %v", err)
	}
	ruleset := tells.Default()
	options := tells.Options{Register: "essays"}
	comparison, err := ruleset.Check(candidateDoc, options).Comparison().
		Compare(ruleset.Check(currentDoc, options).Comparison())
	out.comparable = err == nil
	out.comparison = comparison
	return out
}

// writeProbe writes the text VERBATIM. It used to append a newline, which made
// the oracle measure something the loop never measures: the loop hands the
// scorer a paragraph's raw span, and a raw span has no trailing newline in it.
func writeProbe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return path
}

// requireCandidates fails unless every scripted candidate produces the loop
// verdict the tests below depend on. An accepted candidate must improve AND
// clear both guards; the matching one must clear both guards and NOT improve, so
// that its rejection is not-improved and nothing else.
func requireCandidates(t *testing.T, root string) {
	t.Helper()
	for _, c := range []struct {
		name               string
		current, candidate string
		wantImproves       bool
		// wantEqual pins the candidate at EXACTLY the current distance rather
		// than merely no better. Without it a candidate that measured worse
		// would satisfy "not improved" too, and the fixture would be asserting
		// something weaker than it claims: the rejection under test is the
		// tie, which is what epsilon exists to refuse.
		wantEqual bool
	}{
		{name: "improvesOne", current: paragraphOne, candidate: improvesOne, wantImproves: true},
		{name: "improvesTwo", current: paragraphTwo, candidate: improvesTwo, wantImproves: true},
		{name: "matchesOne", current: paragraphOne, candidate: matchesOne, wantEqual: true},
	} {
		got := judge(t, root, c.current, c.candidate)
		if got.segments != 1 {
			t.Fatalf("%s measures %d segments; the loop requires exactly one", c.name, got.segments)
		}
		if got.improves != c.wantImproves {
			t.Fatalf("%s improves = %v (distance %v), and the fixture needs %v",
				c.name, got.improves, got.distance, c.wantImproves)
		}
		if c.wantEqual && got.distance != got.currentDistance {
			t.Fatalf("%s measures %v against a current %v; the fixture needs them exactly equal",
				c.name, got.distance, got.currentDistance)
		}
		if !got.preserved {
			t.Fatalf("%s does not preserve (%v); its rejection would be not-preserved rather than the one under test",
				c.name, got.identifiers)
		}
		if !got.comparable || got.comparison > 0 {
			t.Fatalf("%s is tells %d comparable=%v; its rejection would come from the tells guard",
				c.name, got.comparison, got.comparable)
		}
	}
}

// ---------------------------------------------------------------------------
// A provider that answers, and records what it was asked
// ---------------------------------------------------------------------------

type providerCall struct {
	Prompt  string
	Passage string
	Request rewrite.RewriteRequest
}

// scriptedProvider answers per passage, so a two-target run can be given
// different behaviour for each paragraph without the test depending on call
// order to tell them apart.
type scriptedProvider struct {
	replies map[string][]string
	err     error
	// before runs immediately before the nth reply is returned, and is how a
	// test changes the world underneath a run in progress.
	before func(t *testing.T, call int)
	t      *testing.T
	calls  []providerCall
	served map[string]int
}

func newProvider(t *testing.T, replies map[string][]string) *scriptedProvider {
	return &scriptedProvider{replies: replies, served: map[string]int{}, t: t}
}

func (p *scriptedProvider) Rewrite(_ context.Context, request rewrite.RewriteRequest) (string, error) {
	passage := passageFrom(request.Prompt)
	p.calls = append(p.calls, providerCall{Prompt: request.Prompt, Passage: passage, Request: request})
	if p.before != nil {
		p.before(p.t, len(p.calls))
	}
	if p.err != nil {
		return "", p.err
	}
	offered := p.replies[passage]
	index := p.served[passage]
	p.served[passage]++
	if index >= len(offered) {
		return "", nil
	}
	return offered[index], nil
}

// passageFrom recovers what the prompt asks to be rewritten. Read out of the
// prompt rather than off a field, because the prompt is what a provider is
// actually sent.
func passageFrom(prompt string) string {
	marker := rewrite.PassageMarker + "\n"
	for i := 0; i+len(marker) <= len(prompt); i++ {
		if prompt[i:i+len(marker)] == marker {
			return prompt[i+len(marker):]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Reading the draft independently of the plan
// ---------------------------------------------------------------------------

// substitution is one ordered replacement: the NEXT occurrence of From becomes
// To. Ordered rather than a map because a draft may repeat a paragraph, and
// which occurrence a rewrite lands on is exactly the byte-ownership question
// this slice has to get right.
type substitution struct{ From, To string }

// spliced is the expected output built from the DRAFT'S OWN BYTES rather than
// from the plan's spans, so an implementation that assembled against the wrong
// offsets cannot agree with the expectation by using the same wrong numbers.
// Occurrences are consumed left to right, so a repeated paragraph is only
// matched by an implementation that replaced the same one.
func spliced(t *testing.T, path string, replacements ...substitution) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	out, at := make([]byte, 0, len(raw)), 0
	for i, replacement := range replacements {
		found := indexOf(raw[at:], replacement.From)
		if found < 0 {
			t.Fatalf("replacement %d: the draft has no further occurrence of %q after byte %d, "+
				"so the expectation is not built from it", i, replacement.From, at)
		}
		out = append(out, raw[at:at+found]...)
		out = append(out, replacement.To...)
		at += found + len(replacement.From)
	}
	return append(out, raw[at:]...)
}

func indexOf(raw []byte, needle string) int {
	for i := 0; i+len(needle) <= len(raw); i++ {
		if string(raw[i:i+len(needle)]) == needle {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Proving this slice writes nothing
// ---------------------------------------------------------------------------

// tree is every file under a root and its bytes, with the store excluded — the
// store is the one place Execute is allowed to write, and only audit records.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	store := filepath.Join(root, ".hapax")
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == store {
				return filepath.SkipDir
			}
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[relative] = string(body)
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// auditTables are the only tables Execute may write. Everything else in the
// database — heads, profiles, references, releases, snapshots, nodes, the
// exemplar selection — is something the plan RESOLVED and must find unchanged.
//
// `document` is deliberately NOT here even though rehydration touches it.
// Rehydrate records, per document it tried to read, whether the read succeeded,
// in `document.unavailable_at`; that is an observation `store` makes as a side
// effect, not an artifact Execute owns. Allowing the whole table would let
// Execute record observations about documents it never touched, so the column is
// held out of the census and checked separately against the documents the
// exemplar selection actually reaches.
var auditTables = map[string]bool{
	"rewrite_attempt":            true,
	"rewrite_attempt_identifier": true,
}

// availabilityColumn is the one column excluded from the contents census,
// because a correct run does change it and only for the documents it read.
const availabilityColumn = "unavailable_at"

// tables hashes the contents of every durable table, so a change is compared
// rather than counted — #70's lesson, where an implementation that moved a row
// left every count exactly where it was.
func tables(t *testing.T, root string) map[string]string {
	t.Helper()
	db := openRawStore(t, defaultStorePath(root))
	names, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var list []string
	for names.Next() {
		var name string
		if err := names.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		list = append(list, name)
	}
	if err := names.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	_ = names.Close()
	if len(list) == 0 {
		t.Fatal("the database reports no tables, so this census would pass over anything")
	}

	out := map[string]string{}
	for _, name := range list {
		rows, err := db.Query("SELECT * FROM " + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatalf("columns of %s: %v", name, err)
		}
		var lines []string
		for rows.Next() {
			cells := make([]any, len(columns))
			into := make([]any, len(columns))
			for i := range cells {
				into[i] = &cells[i]
			}
			if err := rows.Scan(into...); err != nil {
				t.Fatalf("scan %s: %v", name, err)
			}
			var line []string
			for i, column := range columns {
				if column == availabilityColumn {
					continue
				}
				line = append(line, fmt.Sprintf("%s=%v", column, cells[i]))
			}
			lines = append(lines, strings.Join(line, "\x00"))
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		_ = rows.Close()
		sort.Strings(lines)
		out[name] = identity.HashBytes([]byte(strings.Join(lines, "\n")))
	}
	return out
}

// unavailableIn is which of a snapshot's documents are marked unreadable, by
// path, read through the store's OWN accessor rather than out of the column.
// The census below reads raw tables on purpose — that is a persistence-boundary
// check, and #70 established it — but the read observation is a behaviour the
// store exposes, and a test that went to the column for it would be pinning
// where the value is kept rather than what it says.
func unavailableIn(t *testing.T, root string, snapshots ...string) map[string]bool {
	t.Helper()
	opened := openStore(t, defaultStorePath(root))
	out := map[string]bool{}
	for _, snapshot := range snapshots {
		marked, err := opened.Unavailable(ctx(), snapshot)
		if err != nil {
			t.Fatalf("Unavailable(%s): %v", snapshot[:8], err)
		}
		for path := range marked {
			out[path] = true
		}
	}
	return out
}

// exemplarPaths are the documents the PERSISTED selection's nodes live in, by
// path — the only ones a run has any business reading, and therefore the only
// ones whose read observation may change.
//
// Resolved through LoadExemplarSelection and Span rather than off
// plan.ExemplarNodes. The plan is caller-supplied and is the thing a tampered
// plan is checked against; taking the expectation from it would let the two
// share a mistake.
func exemplarPaths(t *testing.T, root, selectionID string) map[string]bool {
	t.Helper()
	opened := openStore(t, defaultStorePath(root))
	selection, err := opened.LoadExemplarSelection(ctx(), selectionID)
	if err != nil {
		t.Fatalf("LoadExemplarSelection: %v", err)
	}
	out := map[string]bool{}
	for _, node := range selection.Members {
		span, err := opened.Span(ctx(), node)
		if err != nil {
			t.Fatalf("Span(%s): %v", node[:8], err)
		}
		out[span.Path] = true
	}
	if len(out) == 0 {
		t.Fatalf("selection %s reaches no documents, so the permitted set permits nothing", selectionID[:8])
	}
	return out
}

// storeCensus is everything about the database a test compares before and after.
type storeCensus struct {
	contents    map[string]string
	unavailable map[string]bool
	snapshots   []string
}

func storeState(t *testing.T, root string, snapshots ...string) storeCensus {
	t.Helper()
	return storeCensus{
		contents:    tables(t, root),
		unavailable: unavailableIn(t, root, snapshots...),
		snapshots:   snapshots,
	}
}

// markExemplarsUnavailable leaves the store believing the selection's documents
// cannot be read, and then puts their exact bytes back.
//
// It exists because "not marked unavailable" is what an UNTOUCHED store says
// too. Starting from a marked store makes wasRead discriminating: only a run
// that actually went through store.Rehydrate clears the mark, so an
// implementation that read the exemplar files directly and only fell back to the
// store on failure is caught.
func markExemplarsUnavailable(t *testing.T, root string, plan workflow.RewritePlan) {
	t.Helper()
	paths := exemplarPaths(t, root, plan.ExemplarSelectionID)
	saved := map[string][]byte{}
	for path := range paths {
		full := filepath.Join(root, filepath.FromSlash(path))
		body, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		saved[full] = body
		if err := os.Remove(full); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
	if _, err := openStore(t, defaultStorePath(root)).Rehydrate(ctx(), root, plan.ExemplarNodes); err != nil {
		t.Fatalf("Rehydrate over a removed corpus: %v", err)
	}
	for full, body := range saved {
		if err := os.WriteFile(full, body, 0o644); err != nil {
			t.Fatalf("restore %s: %v", full, err)
		}
	}
	marked := unavailableIn(t, root, profileSnapshotID(t, root))
	for path := range paths {
		if !marked[path] {
			t.Fatalf("%s is not marked unavailable, so a later wasRead assertion would say nothing", path)
		}
	}
}

// giveExemplarsAByteOrderMark makes "the store rehydrated these" observably
// different from "something read the files".
//
// A content hash is taken over ADMITTED bytes, and admission strips a byte-order
// mark — so adding one to an exemplar's document leaves the hash intact and
// rehydration still verifies and returns the identical text. But a node's offset
// is into the admitted bytes, so anything slicing the RAW file at that offset
// now lands three bytes late.
//
// Without this, an implementation that called store.Rehydrate as ceremony and
// then read the files itself produced exactly the right prompt.
//
// Both halves are asserted rather than assumed: the rehydrated text must be
// unchanged, and the naive slice must differ. If either stopped holding, the
// test using this would be proving something else.
func giveExemplarsAByteOrderMark(t *testing.T, root string, plan workflow.RewritePlan) {
	t.Helper()
	opened := openStore(t, defaultStorePath(root))
	before, err := opened.Rehydrate(ctx(), root, plan.ExemplarNodes)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	for path := range exemplarPaths(t, root, plan.ExemplarSelectionID) {
		full := filepath.Join(root, filepath.FromSlash(path))
		body, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := os.WriteFile(full, append([]byte("\xef\xbb\xbf"), body...), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	after, err := opened.Rehydrate(ctx(), root, plan.ExemplarNodes)
	if err != nil {
		t.Fatalf("Rehydrate after adding a byte-order mark: %v", err)
	}
	for i := range after {
		if after[i].Outcome != store.OutcomeOK {
			t.Fatalf("exemplar %d no longer rehydrates (%s); adding a mark was supposed to be invisible to the hash",
				i, after[i].Outcome)
		}
		if after[i].Text != before[i].Text {
			t.Fatalf("exemplar %d rehydrates differently after a byte-order mark, so this fixture "+
				"changes what the prompt should contain rather than only how it must be obtained", i)
		}
		span, err := opened.Span(ctx(), plan.ExemplarNodes[i])
		if err != nil {
			t.Fatalf("Span: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(span.Path)))
		if err != nil {
			t.Fatalf("read %s: %v", span.Path, err)
		}
		if string(raw[span.Offset:span.Offset+span.Length]) == after[i].Text {
			t.Fatalf("slicing exemplar %d out of the raw file still gives the rehydrated text, "+
				"so a direct reader is indistinguishable and this fixture discriminates nothing", i)
		}
	}
}

// readability is what a run's exemplar documents must report afterwards.
type readability int

const (
	// wasRead: the exemplars rehydrated, so their documents read cleanly.
	wasRead readability = iota
	// wasUnreadable: the corpus had gone, so their documents are marked so.
	wasUnreadable
	// notChecked is for runs that never reach rehydration at all.
	notChecked
)

// requireOnlyAuditRecordsWereWritten fails if any table outside auditTables
// differs, or if a read observation changed for a document the run had no reason
// to open, or if the observation for a document it DID open says the wrong
// thing. Excluding the whole database would let Execute advance a head or
// rewrite a selection unnoticed; permitting any observation for the exemplars
// would let it stamp documents it never read.
func requireOnlyAuditRecordsWereWritten(t *testing.T, root string, before storeCensus, mayRead map[string]bool, want readability) {
	t.Helper()
	after := storeState(t, root, before.snapshots...)
	for name, digest := range after.contents {
		if auditTables[name] {
			continue
		}
		was, existed := before.contents[name]
		if !existed {
			t.Errorf("Execute created the table %s", name)
			continue
		}
		if was != digest {
			t.Errorf("Execute changed %s; the only tables it may write are the audit ones", name)
		}
	}
	for name := range before.contents {
		if _, ok := after.contents[name]; !ok {
			t.Errorf("Execute dropped the table %s", name)
		}
	}

	for path := range after.unavailable {
		if !mayRead[path] {
			t.Errorf("Execute marked %s unreadable, and no exemplar lives in it", path)
		}
	}
	for path := range before.unavailable {
		if !after.unavailable[path] && !mayRead[path] {
			t.Errorf("Execute cleared the read observation on %s, which it had no reason to open", path)
		}
	}
	for path := range mayRead {
		switch want {
		case wasRead:
			if after.unavailable[path] {
				t.Errorf("%s holds an exemplar this run read and is marked unreadable", path)
			}
			if !before.unavailable[path] {
				t.Errorf("%s was not marked unreadable beforehand, so clearing it proves nothing; "+
					"the caller should have used markExemplarsUnavailable", path)
			}
		case wasUnreadable:
			if !after.unavailable[path] {
				t.Errorf("%s holds an exemplar that could not be read and is not marked so", path)
			}
		case notChecked:
			if after.unavailable[path] != before.unavailable[path] {
				t.Errorf("%s changed its read observation on a run that never rehydrated", path)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Audit history that was already there
// ---------------------------------------------------------------------------

// seedAttempt writes an attempt under an EARLIER invocation, so a test can
// require that a later run leaves it exactly as it was. An audit record whose
// only value is being trustworthy must not be rewritten by the next run over the
// same paragraph.
func seedAttempt(t *testing.T, root string, plan workflow.RewritePlan) store.RewriteAttempt {
	t.Helper()
	targets := targetsOf(plan)
	if len(targets) == 0 {
		t.Fatal("the plan has no target to seed an attempt against")
	}
	seeded := store.RewriteAttempt{
		InvocationID:    strings.Repeat("ab", 32),
		Index:           0,
		ProfileID:       plan.ProfileID,
		ProviderID:      llm.ProviderOllama,
		NodeID:          targets[0].NodeID,
		CurrentHash:     identity.HashBytes([]byte("an earlier current")),
		CandidateHash:   identity.HashBytes([]byte("an earlier candidate")),
		CurrentDistance: 1.5, CandidateDistance: 1.75,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandNotYou,
		Preserved:       true,
		TellsComparable: true,
		Rejection:       rewrite.RejectionNotImproved,
	}
	if err := openStore(t, defaultStorePath(root)).PutRewriteAttempt(ctx(), seeded); err != nil {
		t.Fatalf("seed an earlier attempt: %v", err)
	}
	return seeded
}

// requireSeededAttemptSurvives reads the earlier record back and compares every
// member. Counting rows would not do it: an implementation that overwrote the
// row in place leaves the count exactly where it was.
func requireSeededAttemptSurvives(t *testing.T, root string, seeded store.RewriteAttempt) {
	t.Helper()
	got, err := openStore(t, defaultStorePath(root)).
		LoadRewriteAttempt(ctx(), seeded.InvocationID, seeded.NodeID, seeded.Index)
	if err != nil {
		t.Fatalf("the earlier attempt is gone: %v", err)
	}
	if !reflect.DeepEqual(got, seeded) {
		t.Errorf("the earlier attempt is now\n%+v\nand was\n%+v", got, seeded)
	}
}

// requireNothingWasSpent is the assertion every path that must not reach a
// provider makes about the store. "No attempts" is not enough on its own: a run
// that started from an empty table and tidied up after itself satisfies it. So
// the table starts with one record from an earlier run, and afterwards holds
// exactly that record, unchanged.
func requireNothingWasSpent(t *testing.T, root string, seeded store.RewriteAttempt) {
	t.Helper()
	requireSeededAttemptSurvives(t, root, seeded)
	if got := countAttempts(t, root); got != 1 {
		t.Errorf("the store holds %d attempts; it should hold only the one that was already there", got)
	}
}

// ---------------------------------------------------------------------------
// Files, and the drafts these tests write
// ---------------------------------------------------------------------------

// requireOnlyTheStoreChanged fails if any file outside the database differs.
// "The draft is unchanged" is the weaker claim, and this slice's whole premise
// is that it has no filesystem authority at all.
func requireOnlyTheStoreChanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := tree(t, root)
	for path, body := range after {
		was, existed := before[path]
		switch {
		case !existed:
			t.Errorf("Execute created %s; this slice writes nothing outside the store", path)
		case was != body:
			t.Errorf("Execute changed %s; this slice writes nothing outside the store", path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			t.Errorf("Execute removed %s", path)
		}
	}
}

// admittedHash is the content hash freshness is compared against: the hash of
// the bytes AFTER admission, which strips a byte-order mark. Tests that turn on
// a BOM assert this is unchanged, so the refusal they then expect can only come
// from a separate guard rather than from the hash noticing.
func admittedHash(t *testing.T, body string) string {
	t.Helper()
	document, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return identity.HashBytes(document.Raw())
}

// writeDraftBytes writes a draft body verbatim, including any byte-order mark,
// which writeDraft's string signature makes awkward to see at a call site.
func writeDraftBytes(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, "draft.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Reading a result and a prompt without panicking
// ---------------------------------------------------------------------------

// outcomeAt is every indexed read of a result's outcomes. A test that indexed
// directly would PANIC against a wrong implementation rather than fail, and a
// panic aborts the whole binary — so a broken Execute would hide every other
// failure behind one stack trace.
func outcomeAt(t *testing.T, result workflow.ExecuteResult, index int) workflow.TargetOutcome {
	t.Helper()
	if index >= len(result.Outcomes) {
		t.Fatalf("the result carries %d outcomes and this test needs at least %d", len(result.Outcomes), index+1)
	}
	return result.Outcomes[index]
}

// promptAt is the same guard for the provider's own record.
func promptAt(t *testing.T, provider *scriptedProvider, index int) string {
	t.Helper()
	if index >= len(provider.calls) {
		t.Fatalf("the provider was called %d times and this test needs at least %d", len(provider.calls), index+1)
	}
	return provider.calls[index].Prompt
}

// exemplarSection is the part of a prompt between the preamble and the passage
// marker: the fenced exemplars and nothing else.
func exemplarSection(t *testing.T, prompt string) string {
	t.Helper()
	at := indexOf([]byte(prompt), rewrite.PassageMarker)
	if at < 0 {
		t.Fatalf("the prompt carries no passage marker:\n%q", prompt)
	}
	return prompt[:at]
}

// ---------------------------------------------------------------------------
// Nodes and snapshots a hand-built plan could point at
// ---------------------------------------------------------------------------

// aCorpusNode is a vector-bearing node belonging to the PROFILE's snapshot
// rather than the draft's, so a tampered plan can name something real that it
// still has no business rewriting.
func aCorpusNode(t *testing.T, root string) string {
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
	for _, document := range written.Documents {
		for _, node := range document.Nodes {
			if node.Vector != nil {
				return node.ID
			}
		}
	}
	t.Fatal("the profile snapshot holds no vector-bearing node")
	return ""
}

// aDraftNodeWithoutAVector is a node of the DRAFT's own snapshot that carries no
// feature vector — the heading. A plan targeting it names something real, in the
// right document, that was never a paragraph.
func aDraftNodeWithoutAVector(t *testing.T, root, snapshotID string) string {
	t.Helper()
	written, err := openStore(t, defaultStorePath(root)).Snapshot(ctx(), snapshotID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, document := range written.Documents {
		for _, node := range document.Nodes {
			if node.Vector == nil {
				return node.ID
			}
		}
	}
	t.Fatal("the draft snapshot holds no node without a vector, so this case cannot be built")
	return ""
}

// profileSnapshotID is the profile's own snapshot: many documents, and not a
// draft, so a plan naming it as its draft snapshot must be refused.
func profileSnapshotID(t *testing.T, root string) string {
	t.Helper()
	bundle, err := openStore(t, defaultStorePath(root)).LoadProfileBundle(ctx(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	return bundle.Profile.SnapshotID
}

// moveTheHead installs a DIFFERENT release as the register's head, with
// boundaries that put the draft's paragraphs in another band. Execute must go on
// using the release the plan named: it executes an immutable plan and must not
// silently requalify it against whatever is current.
func moveTheHead(t *testing.T, root string, authorCentre, distractorCentre float64) eval.Release {
	t.Helper()
	opened := openStore(t, defaultStorePath(root))
	bundle, err := opened.LoadProfileBundle(ctx(), "essays")
	if err != nil {
		t.Fatalf("LoadProfileBundle: %v", err)
	}
	release := evaltest.ReleaseAround(t, bundle.Profile.ID, bundle.Reference.ID, authorCentre, distractorCentre)
	if err := opened.PutRelease(ctx(), release, "", store.AdvanceHead); err != nil {
		t.Fatalf("PutRelease: %v", err)
	}
	head, err := opened.ReleaseHead(ctx(), bundle.Profile.ID)
	if err != nil {
		t.Fatalf("ReleaseHead: %v", err)
	}
	if head != release.ID {
		t.Fatalf("the head is %s and the new release is %s; it did not move", head[:8], release.ID[:8])
	}
	return release
}

// ---------------------------------------------------------------------------
// Reading attempts back
// ---------------------------------------------------------------------------

func storedAttempt(t *testing.T, root, invocation, nodeID string, index int) store.RewriteAttempt {
	t.Helper()
	attempt, err := openStore(t, defaultStorePath(root)).LoadRewriteAttempt(ctx(), invocation, nodeID, index)
	if err != nil {
		t.Fatalf("LoadRewriteAttempt(%s, %s, %d): %v", invocation[:8], nodeID[:8], index, err)
	}
	return attempt
}

func countAttempts(t *testing.T, root string) int {
	t.Helper()
	var count int
	if err := openRawStore(t, defaultStorePath(root)).
		QueryRow("SELECT count(*) FROM rewrite_attempt").Scan(&count); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return count
}

// targetsOf is the plan's target segments, in plan order.
func targetsOf(plan workflow.RewritePlan) []workflow.PlannedSegment {
	var out []workflow.PlannedSegment
	for _, segment := range plan.Segments {
		if segment.Disposition == workflow.DispositionTarget {
			out = append(out, segment)
		}
	}
	return out
}

// declaredBand keeps a band string in a result honest: it must be one the domain
// declares.
func declaredBand(band string) bool {
	for _, declared := range eval.Bands() {
		if string(declared) == band {
			return true
		}
	}
	return false
}
