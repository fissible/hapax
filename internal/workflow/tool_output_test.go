package workflow_test

// #109, the wiring. `corpus.Walk` can screen out this tool's own prose; `Index`
// is what has to tell it what this tool published.
//
// Without this layer the screen is a capability nothing exercises — the shape
// that kept #91's script measurement inert for a whole issue after it shipped
// correct and tested, and the shape #117 was filed about.
//
// # Why the count is `tool_output_documents`
//
// `Snapshot.ToolOutput` is a `CheckStatus` and this is an `int`, and
// `indexResult` reads the first to set the second within a few lines. A shared
// identifier there is a trap. On the wire it matters more: all five sibling
// counts name the counted thing as a plural noun — `documents`, `eligible`,
// `nodes`, `calibrate_segments`, `train_paragraphs` — and `tool_output: 3` says
// nothing about what three is. The human line keeps the short `tool-output=N`,
// which is that line's convention and reads as the check's finding.
//
// # Why the whole set, and not the query #111 already has
//
// `store.ProducedByRewrite` asks about a bounded list of hashes, which suits a
// plan: one draft, a handful of paragraphs. A corpus screen has the opposite
// shape — it must test every paragraph of every document — and that query binds
// one parameter per hash, against the SQLite variable ceiling its own doc
// comment records.
//
// So the screen takes the published SET and tests membership locally. The two
// accessors exist because the two callers genuinely differ in shape, not because
// nobody looked.
//
// # The screen is fed here and nowhere else
//
// `DefaultPolicy` supplies none, so `draftWrite` (which walks the draft's own
// directory on every rewrite) and the distractor walk both take the nil path and
// pay no parse. That is not only a cost argument: feeding the screen into
// `draftWrite` would make a previously-rewritten draft fail
// `Admission != corpus.Eligible` at workflow.go:937, so `hapax rewrite
// --in-place` would die with "not an eligible corpus document" instead of
// reporting #111's `already-rewritten`. The round-trip test in
// already_rewritten_test.go goes red the moment anyone tries, which is why this needs no
// seam of its own.
//
// An earlier draft of this comment gave that ceiling and two corpus node counts
// as "measured". They are not measured here — the ceiling is #111's recorded
// claim and the counts are of a corpus outside this repository — and restating
// either as this slice's own measurement is the habit these headers exist to
// break.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/eval"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/store"
	"github.com/fissible/hapax/internal/workflow"
)

// publishParagraph records that this tool accepted `text` for some paragraph,
// which is the only fact the screen consults.
func publishParagraph(t *testing.T, root, text string) {
	t.Helper()
	publishInto(t, publisherFor(t, root), text)
}

// publisher holds what every publication needs, resolved once.
//
// `aCorpusNode` does a profile-bundle load plus a full snapshot read, and
// `profileHead` opens the store — per call, they made the screened-to-nothing
// test the most expensive thing in the package by an order of magnitude for no
// property it was asserting.
type publisher struct {
	store   *store.Store
	profile string
	node    string
}

func publisherFor(t *testing.T, root string) publisher {
	t.Helper()
	return publisher{
		store:   openStore(t, defaultStorePath(root)),
		profile: profileHead(t, root),
		node:    aCorpusNode(t, root),
	}
}

func publishInto(t *testing.T, p publisher, text string) {
	t.Helper()
	attempt := store.RewriteAttempt{
		InvocationID:    identity.HashBytes([]byte("invocation for " + text)),
		Index:           0,
		ProfileID:       p.profile,
		ProviderID:      llm.ProviderOllama,
		NodeID:          p.node,
		CurrentHash:     identity.HashBytes([]byte("whatever the author wrote first")),
		CandidateHash:   identity.HashBytes([]byte(text)),
		CurrentDistance: 1.5, CandidateDistance: 0.5,
		CurrentBand: eval.BandDrifting, CandidateBand: eval.BandInRange,
		Preserved:       true,
		TellsComparable: true,
		Accepted:        true,
	}
	if err := p.store.PutRewriteAttempt(ctx(), attempt); err != nil {
		t.Fatalf("record an accepted attempt: %v", err)
	}
	// #134. The screen reads publication evidence, not the attempt audit, so
	// "publish" has to mean both halves here.
	if err := p.store.RecordPublication(ctx(), store.Publication{
		InvocationID: attempt.InvocationID,
		Paragraphs: []store.PublishedParagraph{
			{NodeID: attempt.NodeID, ParagraphHash: attempt.CandidateHash},
		},
	}); err != nil {
		t.Fatalf("record the publication: %v", err)
	}
}

// aCorpusDocument is an eligible document's path on disk, so a test can take a
// paragraph the way the screen will: from the file, admitted.
func aCorpusDocument(t *testing.T, root string) string {
	t.Helper()
	snap, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	eligible := snap.Eligible()
	if len(eligible) == 0 {
		t.Fatal("the corpus has no eligible document to publish a paragraph from")
	}
	return filepath.Join(root, eligible[0].Path)
}

// storedAdmission reads back what the persisted snapshot says about a document,
// so an assertion is against the record rather than against a count.
func storedAdmission(t *testing.T, root, snapshotID, name string) string {
	t.Helper()
	snap, err := openStore(t, defaultStorePath(root)).Snapshot(ctx(), snapshotID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, d := range snap.Documents {
		if d.Path == name {
			return string(d.Admission)
		}
	}
	t.Fatalf("the stored snapshot has no document %q", name)
	return ""
}

// checkNamed returns the named check from an index result.
func checkNamed(t *testing.T, result workflow.IndexResult, name string) workflow.Check {
	t.Helper()
	for _, c := range result.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("the result has no %q check: %+v", name, result.Checks)
	return workflow.Check{}
}

// A corpus document holding a paragraph this tool published stops being corpus.
//
// End to end through the real Index: the screen is fed from the store, the
// document is rejected, the check reports it, and the count says how many. An
// implementation that built the screen but never handed it to `Walk` passes
// every test in `internal/corpus` and fails here.
func TestIndexScreensOutDocumentsHoldingPublishedText(t *testing.T) {
	t.Parallel()
	root := indexedCorpus(t)
	before := indexed(t, indexRequest(root))
	if before.ToolOutputDocuments != 0 {
		t.Fatalf("ToolOutputDocuments = %d before anything was published", before.ToolOutputDocuments)
	}
	if got := checkNamed(t, before, "tool-output").State; got != string(corpus.CheckPassed) {
		t.Fatalf("the tool-output check is %q before anything was published, want %q",
			got, corpus.CheckPassed)
	}

	// A paragraph of a real corpus document, taken as the screen will take it.
	document := aCorpusDocument(t, root)
	leaves := admittedLeaves(t, document, storedFloor(t, root))
	publishParagraph(t, root, leaves[0].Text)

	after := indexed(t, indexRequest(root))

	if after.ToolOutputDocuments != 1 {
		t.Errorf("ToolOutputDocuments = %d, want 1", after.ToolOutputDocuments)
	}
	if after.Eligible != before.Eligible-1 {
		t.Errorf("Eligible = %d, want %d — one document left the corpus",
			after.Eligible, before.Eligible-1)
	}
	// WHICH document. Counting one screened document is satisfied by an
	// implementation that rejects an arbitrary eligible one whenever the
	// published set is non-empty; the store says which it actually was.
	if got := storedAdmission(t, root, after.SnapshotID, filepath.Base(document)); got !=
		string(corpus.RejectedToolOutput) {
		t.Errorf("%s is stored as %q, want %q", filepath.Base(document), got,
			corpus.RejectedToolOutput)
	}
	check := checkNamed(t, after, "tool-output")
	if check.State != string(corpus.CheckFailed) {
		t.Errorf("the tool-output check is %q, want %q", check.State, corpus.CheckFailed)
	}
	if strings.TrimSpace(check.Reason) == "" {
		t.Error("a failed tool-output check gave no reason")
	}
}

// A corpus nothing was published into is screened and says so.
//
// The other half, and the one that catches a screen wired backwards: an index
// that rejected everything, or that never ran the check, both fail here while
// the test above passes.
func TestACleanCorpusIsScreenedAndReportsItPassed(t *testing.T) {
	t.Parallel()
	root := indexedCorpus(t)

	result := indexed(t, indexRequest(root))

	if result.ToolOutputDocuments != 0 {
		t.Errorf("ToolOutputDocuments = %d, want 0", result.ToolOutputDocuments)
	}
	check := checkNamed(t, result, "tool-output")
	if check.State != string(corpus.CheckPassed) {
		t.Errorf("the tool-output check is %q, want %q — it must run and say so, not "+
			"stay not-performed", check.State, corpus.CheckPassed)
	}
	if check.Version != corpus.ToolOutputCheckVersion {
		t.Errorf("Version = %q, want the declared %q — `checks()` copies it verbatim, "+
			"so any non-empty string would satisfy a non-blank assertion",
			check.Version, corpus.ToolOutputCheckVersion)
	}
	if result.Eligible == 0 {
		t.Fatal("nothing was eligible, so this proves nothing about the screen")
	}
}

// A published paragraph screens its document out; an accepted one does not.
//
// Three rows over the SAME paragraph. The refused row alone asserts a zero
// against an implementation that never sets the field — it was green under the
// stub, which is the shape of an assertion that proves nothing — so the published
// row is the positive control that makes it mean something. The middle row is
// #134: accepted, recorded, never published, and so still the author's as far as
// this screen is concerned.
func TestOnlyAPublishedParagraphScreensADocumentOut(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name               string
		accepted, recorded bool
		want               int
	}{
		{"refused, and so never published", false, false, 0},
		{"accepted and never published", true, false, 0},
		{"published", true, true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := indexedCorpus(t)
			document := aCorpusDocument(t, root)
			leaves := admittedLeaves(t, document, storedFloor(t, root))
			attempt := store.RewriteAttempt{
				InvocationID:    identity.HashBytes([]byte("invocation " + c.name)),
				Index:           0,
				ProfileID:       profileHead(t, root),
				ProviderID:      llm.ProviderOllama,
				NodeID:          aCorpusNode(t, root),
				CurrentHash:     identity.HashBytes([]byte("an earlier current")),
				CandidateHash:   identity.HashBytes([]byte(leaves[0].Text)),
				CurrentDistance: 1.5, CandidateDistance: 0.5,
				CurrentBand: eval.BandDrifting, CandidateBand: eval.BandInRange,
				Preserved:       true,
				TellsComparable: true,
				Accepted:        c.accepted,
			}
			if !c.accepted {
				attempt.CandidateDistance, attempt.CandidateBand = 1.75, eval.BandNotYou
				attempt.Rejection = "not-improved"
			}
			db := openStore(t, defaultStorePath(root))
			if err := db.PutRewriteAttempt(ctx(), attempt); err != nil {
				t.Fatalf("record the attempt: %v", err)
			}
			if c.recorded {
				if err := db.RecordPublication(ctx(), store.Publication{
					InvocationID: attempt.InvocationID,
					Paragraphs: []store.PublishedParagraph{
						{NodeID: attempt.NodeID, ParagraphHash: attempt.CandidateHash},
					},
				}); err != nil {
					t.Fatalf("record the publication: %v", err)
				}
			}

			result := indexed(t, indexRequest(root))

			if result.ToolOutputDocuments != c.want {
				t.Errorf("ToolOutputDocuments = %d, want %d", result.ToolOutputDocuments, c.want)
			}
		})
	}
}

// The first index of a corpus that has no store yet is still screened.
//
// `Index` walks before `commit` opens — and `commit` is what CREATES `.hapax`
// when the store path is defaulted. So on a first run there is no store to ask,
// and the screen must report `passed` on an empty set rather than erroring or
// falling back to `not-performed`. Every other test here starts from a corpus
// that has already been indexed once.
func TestTheFirstIndexOfAStorelessCorpusIsScreened(t *testing.T) {
	t.Parallel()
	root := corpusOf(t, 60)

	result := indexed(t, indexRequest(root))

	if result.ToolOutputDocuments != 0 {
		t.Errorf("ToolOutputDocuments = %d on a corpus with no store, want 0", result.ToolOutputDocuments)
	}
	if got := checkNamed(t, result, "tool-output").State; got != string(corpus.CheckPassed) {
		t.Errorf("the tool-output check is %q on a first index, want %q — there is no "+
			"store to ask, which is an empty screen rather than no screen", got,
			corpus.CheckPassed)
	}
}

// A corpus screened down to nothing says why it is too small.
//
// Per-document rejection retires a whole file for one rewritten paragraph, so an
// author who ran `--in-place` across their corpus can screen it out from under
// themselves. `corpus-too-small` on its own reads as "you have not written
// enough"; beside a non-zero count it reads as "this tool wrote most of what you
// indexed", which is the actionable fact and the difference between the two
// designs this issue has had.
func TestACorpusScreenedDownToNothingSaysWhy(t *testing.T) {
	t.Parallel()
	root := indexedCorpus(t)
	snap, err := corpus.Walk(root, corpus.DefaultPolicy("essays"))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	eligible := snap.Eligible()
	if len(eligible) == 0 {
		t.Fatal("nothing eligible to screen out")
	}
	into, floor := publisherFor(t, root), storedFloor(t, root)
	for _, d := range eligible {
		leaves := admittedLeaves(t, filepath.Join(root, d.Path), floor)
		publishInto(t, into, leaves[0].Text)
	}

	result := indexed(t, indexRequest(root))

	if result.Eligible != 0 {
		t.Fatalf("Eligible = %d after publishing a paragraph of every document, want 0",
			result.Eligible)
	}
	if result.ToolOutputDocuments != len(eligible) {
		t.Errorf("ToolOutputDocuments = %d, want %d", result.ToolOutputDocuments, len(eligible))
	}
	if !result.Adverse || result.Adversity != workflow.AdversityCorpusTooSmall {
		t.Errorf("Adverse = %v and Adversity = %q, want true and %q", result.Adverse,
			result.Adversity, workflow.AdversityCorpusTooSmall)
	}
}

// The screened document's paragraphs do not reach the profile.
//
// The count and the admission are bookkeeping; this is the bug. A document that
// is rejected but whose nodes were still written is contamination with a label
// on it.
func TestAScreenedDocumentContributesNoParagraphs(t *testing.T) {
	t.Parallel()
	root := indexedCorpus(t)
	before := indexed(t, indexRequest(root))
	document := aCorpusDocument(t, root)
	leaves := admittedLeaves(t, document, storedFloor(t, root))
	publishParagraph(t, root, leaves[0].Text)

	after := indexed(t, indexRequest(root))

	if after.Nodes >= before.Nodes {
		t.Errorf("Nodes = %d after screening a document out, was %d — its nodes were "+
			"written anyway", after.Nodes, before.Nodes)
	}
}
