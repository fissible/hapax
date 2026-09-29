package corpus_test

// #109. This tool's own output re-enters the corpus that defines the author.
//
// `Walk` reads every `.md` and `.txt` under the root with no exclusion for
// artifacts this tool wrote, and `splitFor` assigns train, calibrate or test by
// content hash — never `draft`. So a rewrite left in the corpus becomes the
// author's measured style on the next `hapax index`.
//
// # What this repairs, measured rather than asserted
//
// The issue named five files in the maintainer's corpus root as "hapax output or
// input scratch". Measured against the accepted candidates recorded in that
// store, statistics only:
//
//	revised.md       11 paragraphs, 1 matches an accepted candidate   SCREENED
//	revised20.md     11 paragraphs, 0 match                           kept
//	revised93.md     11 paragraphs, 0 match                           kept
//	draft.md         11 paragraphs, 0 match                           kept
//	stdin-probe.md    1 paragraph,  0 match                           kept
//
// Produced by hashing every included leaf of each file and testing membership in
// `SELECT candidate_hash FROM rewrite_attempt WHERE accepted=1` of that store —
// the screen's own predicate. Not reproducible from this repository: the corpus
// is the maintainer's and lives outside it.
//
// One of five, which looks like a half-fix until the harm is counted instead of
// the files. `revised.md` holds 63 Han letters — the 63 of the corpus's 64 that
// the issue measured — and ALL 63 sit in the single paragraph the screen
// catches. So the screen removes the outlier that corrupted #107's max-based
// corpus bound, which is the whole of the measured contamination.
//
// What it leaves is NOT untidiness, and calling it that both understated it and
// filed it wrongly. `revised20.md`, `revised93.md` and `draft.md` are three
// near-identical documents, each contributing a full set of paragraph vectors to
// a 60-document profile — a threefold overweighting of one document's style, and
// a real distortion. It is simply not this one: it belongs to
// `near-duplicate-detection`, still `not-performed`, and `Walk`'s dedupe is
// exact-hash so it cannot see near-copies. `stdin-probe.md` is one paragraph and
// inert.
//
// What it does not catch: a paragraph the author edits after this tool wrote it
// hashes differently and stays. That is the same limit #111 has, and it is the
// right one — anything the author touched is the author's.
//
// # Why the first design was abandoned
//
// It refused to publish into a corpus root. Review killed it on evidence in the
// repository: four shipped smoke tests put the draft in the corpus root and
// `--out` beside it, and `rewrite_smoke_test.go:222` asserts the result SHOULD
// land there. It was also a no-op for anyone using `--store`, because `commit`
// only creates the `.hapax` marker when the store path is defaulted.
//
// Excluding at index time repairs the existing corpus, keeps the workflow, and
// needs no marker. It became the cheaper fix only with #111, which made "text
// this tool published" an exact content-addressed question.
//
// # A NEW check, not the reserved contamination one
//
// `Snapshot.Contamination` exists and says "not implemented in corpus v1", so it
// looks like the home for this. It is not. `DESIGN.md:58` defines that check as
// "AI-contamination screening at ingest", and `DESIGN.md:2563` rests on it:
// "`ok` from `index` cannot mean a clean corpus" BECAUSE contamination is
// not-performed. Setting it to `passed` here would attest that a corpus of some
// other model's prose had been screened for AI contamination, which this does
// not do and cannot.
//
// What this screens for is narrower and exactly nameable: paragraphs THIS TOOL
// published, recorded in THIS store. So it is its own check, `tool-output`, and
// the contamination claim stays unmade.
//
// # The schema has to move with the vocabulary
//
// `document.admission` is CHECK-constrained to four values, so adding a fifth is
// a migration. Nothing here writes one — that is production code — but nothing
// here needs to point at it either: `store/vocabulary_test.go` derives
// `document.admission` from `Admissions()`, so declaring the value without
// migrating fails by name. Verified rather than assumed, by declaring it and
// running the suite:
//
//	--- FAIL: TestEveryDeclaredEnumValueIsAcceptedByTheSchema
//	    --- FAIL: .../document.admission/rejected-tool-output
//
// # Why an Admission and not a per-document CheckStatus
//
// `Document.Contamination` exists too, and the schema settles it: the `document`
// table has columns for `admission` and `language` and none for contamination,
// so a per-document verdict there could not survive into the store. `Admission`
// is already the field meaning "this document is not corpus material", it is
// persisted, and `membership()` folds it into the snapshot ID — so a corpus
// screened differently is a different snapshot without anything extra.

import (
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
)

// paragraphHash is the hash this tool records for a published paragraph:
// `identity.HashBytes` over the ADMITTED bytes of the leaf's span, which is what
// `rewrite.Loop` stores as `candidate_hash` and what `ProducedByRewrite`
// answers about.
func paragraphHash(t *testing.T, body, paragraph string) string {
	t.Helper()
	doc, err := text.Admit([]byte(body))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	raw := doc.Raw()
	i := strings.Index(string(raw), paragraph)
	if i < 0 {
		t.Fatalf("the fixture does not contain the paragraph being published")
	}
	return identity.HashBytes(raw[i : i+len(paragraph)])
}

// screened is a policy that knows what this tool published.
func screened(hashes ...string) corpus.Policy {
	p := policy()
	p.PublishedParagraphs = map[string]bool{}
	for _, h := range hashes {
		p.PublishedParagraphs[h] = true
	}
	return p
}

const (
	// Short enough that a paragraph floor would skip it.
	shortToolParagraph = "It does not."

	authorParagraph = "The argument turns on a distinction the author never draws, and the " +
		"omission is what makes the rest of it work."
	toolParagraph = "The argument turns on a distinction the author does not draw. That " +
		"omission is what makes the remainder work."
)

func document(paragraphs ...string) string {
	return strings.Join(paragraphs, "\n\n") + "\n"
}

// A document holding a paragraph this tool published is not corpus material.
//
// Both halves in one fixture: the contaminated document is rejected and its
// untouched neighbour is not, so a screen that rejected everything fails here.
func TestADocumentHoldingPublishedTextIsRejected(t *testing.T) {
	contaminated := document(toolParagraph, authorParagraph)
	clean := document(authorParagraph, authorParagraph)
	root := write(t, map[string]string{"revised.md": contaminated, "essay.md": clean})

	s := walk(t, root, screened(paragraphHash(t, contaminated, toolParagraph)))

	if got := mustDoc(t, s, "revised.md").Admission; got != corpus.RejectedToolOutput {
		t.Errorf("revised.md is %q, want %q", got, corpus.RejectedToolOutput)
	}
	if got := mustDoc(t, s, "essay.md").Admission; got != corpus.Eligible {
		t.Errorf("essay.md is %q, want %q — nothing in it was ever published", got,
			corpus.Eligible)
	}
	if n := len(s.Eligible()); n != 1 {
		t.Errorf("%d eligible, want 1", n)
	}
}

// A corpus nothing was published into is screened and passes.
//
// The check must run and SAY it ran. An implementation that only ever sets the
// status when it rejects something leaves a clean corpus reporting
// `not-performed`, which is what it said before this existed — indistinguishable
// from a screen nobody asked for.
func TestACleanCorpusIsScreenedAndPasses(t *testing.T) {
	clean := document(authorParagraph, authorParagraph)
	root := write(t, map[string]string{"essay.md": clean})

	s := walk(t, root, screened(paragraphHash(t, document(toolParagraph), toolParagraph)))

	if s.ToolOutput.State != corpus.CheckPassed {
		t.Errorf("the tool-output check is %q, want %q", s.ToolOutput.State, corpus.CheckPassed)
	}
	if s.ToolOutput.Version != corpus.ToolOutputCheckVersion {
		t.Errorf("Version = %q, want the declared %q — the CLI fixture and this test "+
			"must not each invent one", s.ToolOutput.Version, corpus.ToolOutputCheckVersion)
	}
	if got := mustDoc(t, s, "essay.md").Admission; got != corpus.Eligible {
		t.Errorf("essay.md is %q, want %q", got, corpus.Eligible)
	}
}

// And a contaminated one reports the check as failed, with a reason.
func TestACorpusHoldingPublishedTextReportsTheCheckFailed(t *testing.T) {
	contaminated := document(toolParagraph, authorParagraph)
	root := write(t, map[string]string{"revised.md": contaminated})

	s := walk(t, root, screened(paragraphHash(t, contaminated, toolParagraph)))

	if s.ToolOutput.State != corpus.CheckFailed {
		t.Fatalf("the tool-output check is %q, want %q", s.ToolOutput.State, corpus.CheckFailed)
	}
	// Named, not merely non-blank: the reason is what tells a reader this is
	// about THIS TOOL'S output rather than the AI-contamination screen the
	// reserved `Contamination` field is for.
	if !strings.Contains(s.ToolOutput.Reason, "published") {
		t.Errorf("Reason = %q and does not say what was found", s.ToolOutput.Reason)
	}
}

// Without a screen the check is not performed, and nothing is rejected.
//
// The backward-compatible half, and the one that keeps every existing caller
// honest: a `Walk` given no published set must behave exactly as it did, and
// must NOT claim to have screened.
func TestWithoutAScreenTheCheckIsNotPerformed(t *testing.T) {
	contaminated := document(toolParagraph, authorParagraph)
	root := write(t, map[string]string{"revised.md": contaminated})

	s := walk(t, root, policy())

	if s.ToolOutput.State != corpus.CheckNotPerformed {
		t.Errorf("the tool-output check is %q, want %q — no screen was supplied",
			s.ToolOutput.State, corpus.CheckNotPerformed)
	}
	// A reason, not just a state. `TestUnavailableChecksAreTypedNotBlank` states
	// the rule this field now inherits: a blank reason "would read as clean to
	// any caller that did not know better".
	if strings.TrimSpace(s.ToolOutput.Reason) == "" {
		t.Error("an unscreened tool-output check carries no reason")
	}
	if got := mustDoc(t, s, "revised.md").Admission; got != corpus.Eligible {
		t.Errorf("revised.md is %q, want %q — an unscreened walk rejects nothing new",
			got, corpus.Eligible)
	}
}

// `RequiresChecksBeforeUse` is deliberately not wired to this check.
//
// It reads the five checks that are UNIMPLEMENTED, and its doc says so. This one
// is implemented; a not-performed `tool-output` means no screen was supplied,
// which is a caller's choice rather than a missing feature. Adding it would also
// be untestable — the other five are permanently not-performed, so the function
// returns true either way and no fixture could show the difference.
//
// Two comments in an earlier draft claimed this assertion tested the
// distinction. It cannot, and they were written for a design where this check
// WAS `Contamination`.
func TestRequiresChecksBeforeUseStillReportsTheUnimplementedFive(t *testing.T) {
	root := write(t, map[string]string{"essay.md": document(authorParagraph, authorParagraph)})

	screenedSnapshot := walk(t, root, screened())

	if screenedSnapshot.ToolOutput.State != corpus.CheckPassed {
		t.Fatalf("the tool-output check is %q, want %q", screenedSnapshot.ToolOutput.State,
			corpus.CheckPassed)
	}
	if !screenedSnapshot.RequiresChecksBeforeUse() {
		t.Error("a screened snapshot stopped reporting outstanding checks; the other " +
			"five are still not-performed and `ok` still cannot mean `clean`")
	}
}

// The hash is over the ADMITTED bytes, so a byte order mark does not hide a
// document.
//
// `readDocument` holds both the file's `raw` and the admitted `analysis` in
// scope, one identifier apart, and `text.Admit` strips a leading BOM — so the
// wrong one is the easy mistake. #111 shipped a defective test for exactly this
// and caught it only because review asked for a BOM row; this is that row.
func TestAByteOrderMarkDoesNotHideADocument(t *testing.T) {
	contaminated := document(toolParagraph, authorParagraph)
	root := write(t, map[string]string{"revised.md": "\ufeff" + contaminated})

	s := walk(t, root, screened(paragraphHash(t, contaminated, toolParagraph)))

	if got := mustDoc(t, s, "revised.md").Admission; got != corpus.RejectedToolOutput {
		t.Errorf("Admission = %q, want %q — the screen hashes the file's bytes rather "+
			"than the admitted document's", got, corpus.RejectedToolOutput)
	}
}

// A screen that is supplied and empty is still a screen.
//
// This is the state of every FIRST index: the store holds no accepted candidate,
// so the set is empty, and the check must report `passed` rather than
// `not-performed`. An implementation keyed on `len(...) > 0` passes every other
// test here and never reports a clean corpus as screened, for anyone, ever.
func TestAnEmptyScreenIsStillAScreen(t *testing.T) {
	root := write(t, map[string]string{"essay.md": document(authorParagraph, authorParagraph)})

	p := policy()
	p.PublishedParagraphs = map[string]bool{}
	s := walk(t, root, p)

	if s.ToolOutput.State != corpus.CheckPassed {
		t.Errorf("the tool-output check is %q for an empty screen, want %q",
			s.ToolOutput.State, corpus.CheckPassed)
	}
	if n := len(s.Eligible()); n != 1 {
		t.Errorf("%d eligible, want 1 — an empty screen rejects nothing", n)
	}
}

// A document already rejected keeps the rejection it had.
//
// `Walk`'s second pass assigns duplicate, too-short and eligible in a fixed
// order, and each carries evidence `membership()` also hashes — `DuplicateOf`
// for a duplicate, `RejectionOffset` for a bad encoding. Overwriting one with
// this screen would discard that evidence and change the snapshot ID for a
// reason nobody could reconstruct.
//
// The not-UTF8 row is the one that cannot be a matter of taste: `readDocument`
// returns before `Admit` succeeds, so such a file has no paragraphs to screen at
// all, and an implementation that tried would fail rather than skip.
func TestADocumentAlreadyRejectedKeepsItsRejection(t *testing.T) {
	contaminated := document(toolParagraph, authorParagraph)
	hash := paragraphHash(t, contaminated, toolParagraph)
	root := write(t, map[string]string{
		"first.md":  contaminated,
		"second.md": contaminated,
	})
	writeInto(t, root, map[string]string{"bad.md": string([]byte{0xff, 0xfe, 'a'})})

	s := walk(t, root, screened(hash))

	// The duplicate is a duplicate, not tool output, and still names its winner.
	duplicate := mustDoc(t, s, "second.md")
	if duplicate.Admission != corpus.RejectedDuplicate {
		t.Errorf("second.md is %q, want %q — the screen overwrote an earlier rejection",
			duplicate.Admission, corpus.RejectedDuplicate)
	}
	if duplicate.DuplicateOf != "first.md" {
		t.Errorf("second.md names %q as its winner, want first.md", duplicate.DuplicateOf)
	}
	// And the first copy, which IS screenable, is screened.
	if got := mustDoc(t, s, "first.md").Admission; got != corpus.RejectedToolOutput {
		t.Errorf("first.md is %q, want %q", got, corpus.RejectedToolOutput)
	}
	// A file that cannot be decoded has no paragraphs to screen.
	if got := mustDoc(t, s, "bad.md").Admission; got != corpus.RejectedNotUTF8 {
		t.Errorf("bad.md is %q, want %q", got, corpus.RejectedNotUTF8)
	}
}

// One published paragraph anywhere in the document is enough.
//
// The screen is per-PARAGRAPH and the verdict is per-DOCUMENT, so the position
// of the published paragraph must not matter. An implementation testing only the
// first leaf passes the headline test above, where the tool's paragraph happens
// to be first.
func TestAPublishedParagraphIsFoundWhereverItSits(t *testing.T) {
	for name, c := range map[string]struct{ body, published string }{
		"first":  {document(toolParagraph, authorParagraph, authorParagraph), toolParagraph},
		"middle": {document(authorParagraph, toolParagraph, authorParagraph), toolParagraph},
		"last":   {document(authorParagraph, authorParagraph, toolParagraph), toolParagraph},
		// A SHORT one. The hashes in the store come from `ParagraphLeaves` under
		// `MinParagraphLexicalTokens`, a floor this package cannot see, while
		// `Policy.MinLexicalTokens` is a DOCUMENT floor of 1. An implementer
		// reaching for the floor that is in scope gets a screen that silently
		// misses short published paragraphs — which is the failure this issue is
		// about, wearing a different hat.
		"short": {document(authorParagraph, shortToolParagraph), shortToolParagraph},
	} {
		t.Run(name, func(t *testing.T) {
			root := write(t, map[string]string{"revised.md": c.body})

			s := walk(t, root, screened(paragraphHash(t, c.body, c.published)))

			if got := mustDoc(t, s, "revised.md").Admission; got != corpus.RejectedToolOutput {
				t.Errorf("Admission = %q, want %q", got, corpus.RejectedToolOutput)
			}
		})
	}
}

// The screen changes the snapshot identity, because it changes what the corpus
// claims to be.
//
// `membership` folds each document's Admission into the ID, so this is a
// property of using Admission rather than something the implementation has to
// remember. Asserted because it is the reason Admission was chosen over the
// reserved per-document Contamination status, which the schema cannot persist.
func TestScreeningChangesTheSnapshotIdentity(t *testing.T) {
	contaminated := document(toolParagraph, authorParagraph)
	root := write(t, map[string]string{"revised.md": contaminated})

	unscreened := walk(t, root, policy())
	rejected := walk(t, root, screened(paragraphHash(t, contaminated, toolParagraph)))

	if unscreened.ID == rejected.ID {
		t.Error("screening a document out left the snapshot identity unchanged; a " +
			"corpus that excluded a document is not the corpus that included it")
	}
}

// A screened walk and an unscreened one are different snapshots, even when
// nothing is screened out.
//
// `TestScreeningChangesTheSnapshotIdentity` covers the case where a document
// drops out — `membership` carries that. This is the case it cannot: a clean
// corpus, screened against an empty set, admits exactly what it admitted before,
// so the only thing that differs is THAT the screen ran. Without the version key
// in the identity those two are the same snapshot, and a profile fitted before
// the screen existed is silently reused after it.
func TestAScreenedWalkIsADifferentSnapshotEvenWhenNothingIsScreened(t *testing.T) {
	root := write(t, map[string]string{"essay.md": document(authorParagraph, authorParagraph)})

	unscreened := walk(t, root, policy())
	screenedClean := walk(t, root, screened())

	if len(unscreened.Eligible()) != len(screenedClean.Eligible()) {
		t.Fatalf("the screen changed what was eligible (%d to %d); this test is about "+
			"the case where it does not", len(unscreened.Eligible()),
			len(screenedClean.Eligible()))
	}
	if unscreened.ID == screenedClean.ID {
		t.Error("a screened walk and an unscreened one produced the same snapshot ID; " +
			"the check version is not in the identity")
	}
	// And the version is what differs, named rather than inferred.
	if got := screenedClean.IdentityInputs()["tool-output-check-version"]; got !=
		corpus.ToolOutputCheckVersion {
		t.Errorf("a screened walk names %q as its check version, want %q", got,
			corpus.ToolOutputCheckVersion)
	}
	if got := unscreened.IdentityInputs()["tool-output-check-version"]; got != "" {
		t.Errorf("an unscreened walk names %q as its check version, want empty", got)
	}
}

// The new admission is in the declared vocabulary.
func TestRejectedToolOutputIsDeclared(t *testing.T) {
	if string(corpus.RejectedToolOutput) != "rejected-tool-output" {
		t.Errorf("RejectedToolOutput = %q, want %q", corpus.RejectedToolOutput,
			"rejected-tool-output")
	}
	seen := 0
	for _, a := range corpus.Admissions() {
		if a == corpus.RejectedToolOutput {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%q appears %d times in Admissions(): %v", corpus.RejectedToolOutput,
			seen, corpus.Admissions())
	}
}
