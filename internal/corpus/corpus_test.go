// Package corpus_test defines the contract for building a corpus snapshot.
//
// SCOPE. Walk a directory, admit files, content-address them, deduplicate,
// record provenance, apply a minimum-length gate, assign a held-out split, and
// compute a snapshot identity.
//
// THREE DECLARED HOLES. Section 1 lists corpus as depending on `tells`
// (component 1), which is unbuilt, and Section 3 adds a language gate and a
// structural tree that need text slices that do not exist. Section 1 also asks
// for git-date provenance, which is deferred: a per-file `git log` shell-out is
// a performance decision that deserves its own slice rather than a hasty one.
//
// All three are surfaced as typed check statuses in a NotPerformed state, never
// as empty fields. Fingerprinting a contaminated corpus reproduces the register
// being removed, so a blank that a caller could read as "clean" is the worst
// available failure. For the same reason a document that passes only the
// mechanical gates is ELIGIBLE, not admitted: it is not yet qualified prose,
// and downstream components must refuse a snapshot whose required checks are
// still NotPerformed.
package corpus_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
)

func policy() corpus.Policy {
	return corpus.Policy{
		Register:         "essays",
		Role:             corpus.RoleAuthor,
		MinLexicalTokens: 5,
		SplitSeed:        "test-seed",
		Splits:           corpus.SplitWeights{Train: 60, Calibrate: 20, Test: 20},
	}
}

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeInto(t, root, files)
	return root
}

func writeInto(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func walk(t *testing.T, root string, p corpus.Policy) *corpus.Snapshot {
	t.Helper()
	s, err := corpus.Walk(root, p)
	if err != nil {
		t.Fatalf("Walk(%q): %v", root, err)
	}
	return s
}

func prose(word string, n int) string { return strings.TrimSpace(strings.Repeat(word+" ", n)) }

func byPath(s *corpus.Snapshot, name string) (corpus.Document, bool) {
	for _, d := range s.Documents {
		if d.Path == name {
			return d, true
		}
	}
	return corpus.Document{}, false
}

func mustDoc(t *testing.T, s *corpus.Snapshot, name string) corpus.Document {
	t.Helper()
	d, ok := byPath(s, name)
	if !ok {
		t.Fatalf("%s missing from snapshot", name)
	}
	return d
}

// ---------------------------------------------------------------------------
// Declared holes
// ---------------------------------------------------------------------------

// Unavailable checks are a typed state carrying a reason, distinguishable from
// a pass, a failure and a deliberate policy skip. An empty field would read as
// "clean" to any caller that did not know better.
func TestUnavailableChecksAreTypedNotBlank(t *testing.T) {
	s := walk(t, write(t, map[string]string{"a.md": prose("alpha", 20)}), policy())

	for name, got := range map[string]corpus.CheckStatus{
		"contamination": s.Contamination,
		"language":      s.Language,
		"structure":     s.Structure,
		"git date":      s.GitProvenance,
	} {
		if got.State != corpus.CheckNotPerformed {
			t.Errorf("snapshot %s check: State = %q, want %q", name, got.State, corpus.CheckNotPerformed)
		}
		if got.Reason == "" {
			t.Errorf("snapshot %s check carries no reason; an unavailable check must say why", name)
		}
	}

	// The four states must be distinct values, or NotPerformed can be confused
	// with a pass.
	states := map[corpus.CheckState]bool{
		corpus.CheckNotPerformed: true, corpus.CheckPassed: true,
		corpus.CheckFailed: true, corpus.CheckSkippedByPolicy: true,
	}
	if len(states) != 4 {
		t.Error("check states are not four distinct values")
	}

	d := mustDoc(t, s, "a.md")
	for name, got := range map[string]corpus.CheckStatus{
		"contamination": d.Contamination,
		"language":      d.Language,
		"structure":     d.Structure,
	} {
		if got.State != corpus.CheckNotPerformed {
			t.Errorf("document %s check: State = %q, want %q", name, got.State, corpus.CheckNotPerformed)
		}
		if got.Reason == "" {
			t.Errorf("document %s check carries no reason", name)
		}
	}

	if s.NearDuplicateDetection.State != corpus.CheckNotPerformed {
		t.Errorf("NearDuplicateDetection: State = %q, want %q", s.NearDuplicateDetection.State, corpus.CheckNotPerformed)
	}
	if s.NearDuplicateDetection.Reason == "" {
		t.Error("NearDuplicateDetection carries no reason")
	}
}

// A document passing only the mechanical gates is ELIGIBLE. Calling it admitted
// would claim a qualification the unbuilt gates have not granted.
func TestMechanicallyPassingDocumentsAreEligibleNotAdmitted(t *testing.T) {
	s := walk(t, write(t, map[string]string{"a.md": prose("alpha", 20)}), policy())
	d := mustDoc(t, s, "a.md")
	if d.Admission != corpus.Eligible {
		t.Errorf("Admission = %q, want %q — language and structure gates have not run", d.Admission, corpus.Eligible)
	}
	if len(s.Eligible()) != 1 {
		t.Errorf("%d eligible, want 1", len(s.Eligible()))
	}
	if !s.RequiresChecksBeforeUse() {
		t.Error("snapshot does not report that required checks are outstanding; downstream must be able to refuse it")
	}
}

// ---------------------------------------------------------------------------
// Walking and admission
// ---------------------------------------------------------------------------

func TestWalkReadsProseFilesAndIgnoresOthers(t *testing.T) {
	root := write(t, map[string]string{
		"a.md":          prose("alpha", 20),
		"b.txt":         prose("beta", 20),
		"c.go":          "package main",
		"d.json":        `{"x":1}`,
		"nested/e.md":   prose("epsilon", 20),
		".hidden/f.md":  prose("hidden", 20),
		".dotfile.md":   prose("dot", 20),
		"sub/.git/g.md": prose("git", 20),
	})
	s := walk(t, root, policy())

	var got []string
	for _, d := range s.Eligible() {
		got = append(got, d.Path)
	}
	want := []string{"a.md", "b.txt", "nested/e.md"}
	if !equalStrings(got, want) {
		t.Errorf("eligible %v, want %v", got, want)
	}
}

// Paths are recorded slash-separated and relative, so ordering, dedupe
// tie-breaks and snapshot identity mean the same thing on every platform.
func TestPathsAreRelativeAndSlashSeparated(t *testing.T) {
	s := walk(t, write(t, map[string]string{"deep/nested/a.md": prose("alpha", 20)}), policy())
	d := mustDoc(t, s, "deep/nested/a.md")
	if filepath.IsAbs(d.Path) {
		t.Errorf("Path %q is absolute", d.Path)
	}
	if strings.Contains(d.Path, "\\") {
		t.Errorf("Path %q is not slash-separated", d.Path)
	}
}

func TestRejectionsAreRecordedNotDropped(t *testing.T) {
	root := write(t, map[string]string{
		"good.md":  prose("alpha", 20),
		"short.md": "too short",
		"bad.md":   "valid start \xff\xfe invalid",
	})
	s := walk(t, root, policy())

	if len(s.Documents) != 3 {
		t.Fatalf("%d documents, want 3 — rejections are recorded", len(s.Documents))
	}
	if len(s.Eligible()) != 1 {
		t.Fatalf("%d eligible, want 1", len(s.Eligible()))
	}
	if got := mustDoc(t, s, "short.md").Admission; got != corpus.RejectedTooShort {
		t.Errorf("short.md: %q, want %q", got, corpus.RejectedTooShort)
	}

	bad := mustDoc(t, s, "bad.md")
	if bad.Admission != corpus.RejectedNotUTF8 {
		t.Errorf("bad.md: %q, want %q", bad.Admission, corpus.RejectedNotUTF8)
	}
	// The fault offset is carried structurally, not buried in prose: a substring
	// check for "12" would also match offsets 120 or 512.
	if bad.RejectionOffset != 12 {
		t.Errorf("bad.md: RejectionOffset = %d, want 12 (the first invalid byte)", bad.RejectionOffset)
	}
	if bad.RejectionDetail == "" {
		t.Error("bad.md carries no rejection detail")
	}
	// Offsets are meaningless for other rejections and must not read as byte 0.
	if got := mustDoc(t, s, "short.md").RejectionOffset; got != -1 {
		t.Errorf("short.md: RejectionOffset = %d, want -1 (not applicable)", got)
	}
}

// ---------------------------------------------------------------------------
// The length gate
// ---------------------------------------------------------------------------

// The gate counts LEXICAL tokens. Punctuation, numbers and symbols must not
// satisfy a prose-length requirement.
func TestLengthGateCountsLexicalTokensOnly(t *testing.T) {
	p := policy()
	p.MinLexicalTokens = 5
	root := write(t, map[string]string{
		"punct.md":   "a , . ; : ! ? , . ; : !",  // 1 lexical, many tokens
		"numbers.md": "one 1 2 3 4 5 6 7 8 9 10", // 1 lexical
		"prose.md":   "one two three four five",  // 5 lexical
	})
	s := walk(t, root, p)

	if got := mustDoc(t, s, "punct.md").Admission; got != corpus.RejectedTooShort {
		t.Errorf("punctuation-heavy file: %q, want %q — punctuation is not prose length", got, corpus.RejectedTooShort)
	}
	if got := mustDoc(t, s, "numbers.md").Admission; got != corpus.RejectedTooShort {
		t.Errorf("number-heavy file: %q, want %q", got, corpus.RejectedTooShort)
	}
	if got := mustDoc(t, s, "prose.md").Admission; got != corpus.Eligible {
		t.Errorf("five-word file: %q, want %q", got, corpus.Eligible)
	}
}

// Both counts are recorded: the raw count is preliminary evidence, the lexical
// count is what the gate uses. A tokenizer that split on whitespace would give
// the same number for both here, so the two must differ.
func TestBothTokenCountsAreRecordedAndDiffer(t *testing.T) {
	// Whitespace splitting gives 3; the tokenizer gives 5 tokens, 3 lexical.
	s := walk(t, write(t, map[string]string{"a.md": "alpha, beta; gamma"}), func() corpus.Policy {
		p := policy()
		p.MinLexicalTokens = 1
		return p
	}())
	d := mustDoc(t, s, "a.md")

	if d.LexicalTokens != 3 {
		t.Errorf("LexicalTokens = %d, want 3", d.LexicalTokens)
	}
	if d.Tokens != 5 {
		t.Errorf("Tokens = %d, want 5 — punctuation is tokenized, so this cannot be a whitespace split", d.Tokens)
	}
}

// ---------------------------------------------------------------------------
// Provenance and content addressing
// ---------------------------------------------------------------------------

func TestProvenanceIsCaptured(t *testing.T) {
	body := prose("alpha", 20)
	s := walk(t, write(t, map[string]string{"a.md": body}), policy())
	d := mustDoc(t, s, "a.md")

	if d.ContentHash == "" {
		t.Error("no content hash")
	}
	if d.Bytes != len(body) {
		t.Errorf("Bytes = %d, want %d", d.Bytes, len(body))
	}
	if d.ModTime.IsZero() {
		t.Error("ModTime is zero")
	}
	if d.Register != "essays" {
		t.Errorf("Register = %q, want %q — declared by policy, never inferred", d.Register, "essays")
	}
}

// The hash covers the bytes analysis actually sees, so a leading BOM — stripped
// at admission — cannot let identical prose escape deduplication.
func TestContentHashIgnoresStrippedBOM(t *testing.T) {
	body := prose("alpha", 20)
	// Named so the lexicographic tie-break makes the plain file the winner; with
	// "bom.md" and "plain.md" the BOM file would sort first and win, which
	// contradicts nothing but makes the test read as though it were testing the
	// tie-break rather than the hash.
	s := walk(t, write(t, map[string]string{
		"a-plain.md": body,
		"b-bom.md":   "\xef\xbb\xbf" + body,
	}), policy())

	plain := mustDoc(t, s, "a-plain.md")
	bom := mustDoc(t, s, "b-bom.md")
	if plain.ContentHash != bom.ContentHash {
		t.Error("a BOM changed the content hash; identical prose would evade dedupe")
	}
	if plain.Admission != corpus.Eligible {
		t.Errorf("a-plain.md: %q, want %q (first path wins the tie-break)", plain.Admission, corpus.Eligible)
	}
	if bom.Admission != corpus.RejectedDuplicate {
		t.Errorf("b-bom.md: %q, want %q", bom.Admission, corpus.RejectedDuplicate)
	}
	if bom.DuplicateOf != "a-plain.md" {
		t.Errorf("b-bom.md: DuplicateOf = %q, want %q", bom.DuplicateOf, "a-plain.md")
	}
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

// Duplicates are a leakage hazard: the same text in Train and Test inflates
// every measurement taken across the split.
func TestDuplicatesAreRejectedDeterministically(t *testing.T) {
	body := prose("alpha", 20)
	s := walk(t, write(t, map[string]string{
		"zeta.md": body, "alpha.md": body, "mid.md": body,
	}), policy())

	if n := len(s.Eligible()); n != 1 {
		t.Fatalf("%d eligible, want 1", n)
	}
	// A provenance tie-break, not a quality judgement: the lexicographically
	// first slash-separated relative path wins, so the outcome cannot depend on
	// filesystem walk order.
	if got := s.Eligible()[0].Path; got != "alpha.md" {
		t.Errorf("winner %q, want %q", got, "alpha.md")
	}
	for _, name := range []string{"mid.md", "zeta.md"} {
		d := mustDoc(t, s, name)
		if d.Admission != corpus.RejectedDuplicate {
			t.Errorf("%s: %q, want %q", name, d.Admission, corpus.RejectedDuplicate)
		}
		if d.DuplicateOf != "alpha.md" {
			t.Errorf("%s: DuplicateOf = %q, want %q", name, d.DuplicateOf, "alpha.md")
		}
	}
}

// Exact dedupe only. Near-duplicate detection is a separate, versioned leakage
// control per issue #3 and must not be implied by this hash.
func TestNearDuplicatesAreNotDetected(t *testing.T) {
	s := walk(t, write(t, map[string]string{
		"a.md": prose("alpha", 20),
		"b.md": prose("alpha", 20) + " extra",
	}), policy())
	if n := len(s.Eligible()); n != 2 {
		t.Errorf("%d eligible, want 2 — this slice detects exact duplicates only", n)
	}
	if s.NearDuplicateDetection.State != corpus.CheckNotPerformed {
		t.Errorf("NearDuplicateDetection = %q, want %q", s.NearDuplicateDetection.State, corpus.CheckNotPerformed)
	}
}

// ---------------------------------------------------------------------------
// Split assignment
// ---------------------------------------------------------------------------

// THE discriminating test. A path-derived split would satisfy determinism and
// stability-under-growth just as well; only a rename separates the two. The
// split follows the content, so moving a file must not move it between splits.
func TestSplitFollowsContentNotPath(t *testing.T) {
	bodies := map[string]string{}
	for i := 0; i < 30; i++ {
		bodies["f"+pad(i)+".md"] = prose("word"+pad(i), 20)
	}
	before := walk(t, write(t, bodies), policy())

	renamed := map[string]string{}
	for name, body := range bodies {
		renamed["renamed/"+strings.TrimSuffix(name, ".md")+"-moved.md"] = body
	}
	after := walk(t, write(t, renamed), policy())

	splitByBody := map[string]corpus.Split{}
	for _, d := range before.Eligible() {
		splitByBody[bodies[d.Path]] = d.Split
	}
	for _, d := range after.Eligible() {
		body := renamed[d.Path]
		if want, ok := splitByBody[body]; ok && d.Split != want {
			t.Errorf("%s moved from %q to %q after renaming; the split must follow content, not path", d.Path, want, d.Split)
		}
	}
}

// Conversely, editing a document's body may move it. That is correct: it is a
// different document for analysis, and it invalidates the old snapshot anyway.
func TestEditingContentMayMoveTheSplit(t *testing.T) {
	bodies := map[string]string{}
	for i := 0; i < 60; i++ {
		bodies["f"+pad(i)+".md"] = prose("word"+pad(i), 20)
	}
	before := walk(t, write(t, bodies), policy())

	edited := map[string]string{}
	for name, body := range bodies {
		edited[name] = body + " revised"
	}
	after := walk(t, write(t, edited), policy())

	moved := 0
	for _, d := range before.Eligible() {
		if other, ok := byPath(after, d.Path); ok && other.Split != d.Split {
			moved++
		}
	}
	if moved == 0 {
		t.Error("editing every document moved none between splits; the assignment is not content-derived")
	}
}

func TestSplitIsDeterministicAcrossRuns(t *testing.T) {
	bodies := map[string]string{}
	for i := 0; i < 40; i++ {
		bodies["f"+pad(i)+".md"] = prose("word"+pad(i), 20)
	}
	root := write(t, bodies)
	first, second := walk(t, root, policy()), walk(t, root, policy())
	for _, d := range first.Eligible() {
		if other := mustDoc(t, second, d.Path); other.Split != d.Split {
			t.Errorf("%s moved from %q to %q between runs", d.Path, d.Split, other.Split)
		}
	}
}

// Adding documents must not reassign existing ones, or every calibration taken
// before the addition is silently invalidated.
func TestSplitIsStableWhenTheCorpusGrows(t *testing.T) {
	base := map[string]string{}
	for i := 0; i < 20; i++ {
		base["f"+pad(i)+".md"] = prose("word"+pad(i), 20)
	}
	root := write(t, base)
	before := walk(t, root, policy())

	extra := map[string]string{}
	for i := 100; i < 120; i++ {
		extra["f"+pad(i)+".md"] = prose("other"+pad(i), 20)
	}
	writeInto(t, root, extra)
	after := walk(t, root, policy())

	for _, d := range before.Eligible() {
		if other := mustDoc(t, after, d.Path); other.Split != d.Split {
			t.Errorf("%s moved from %q to %q when unrelated documents were added", d.Path, d.Split, other.Split)
		}
	}
}

func TestSplitSeedChangesThePartition(t *testing.T) {
	bodies := map[string]string{}
	for i := 0; i < 40; i++ {
		bodies["f"+pad(i)+".md"] = prose("word"+pad(i), 20)
	}
	root := write(t, bodies)
	a := walk(t, root, policy())
	p := policy()
	p.SplitSeed = "different-seed"
	b := walk(t, root, p)

	moved := 0
	for _, d := range a.Eligible() {
		if other := mustDoc(t, b, d.Path); other.Split != d.Split {
			moved++
		}
	}
	if moved == 0 {
		t.Error("changing the split seed moved no document")
	}
}

func TestOnlyEligibleDocumentsGetASplit(t *testing.T) {
	s := walk(t, write(t, map[string]string{
		"good.md": prose("alpha", 20), "short.md": "nope",
	}), policy())
	if got := mustDoc(t, s, "short.md").Split; got != "" {
		t.Errorf("rejected document carries split %q", got)
	}
	if mustDoc(t, s, "good.md").Split == "" {
		t.Error("eligible document has no split")
	}
}

func TestSplitWeightsAreApproximatelyHonoured(t *testing.T) {
	bodies := map[string]string{}
	for i := 0; i < 300; i++ {
		bodies["d/f"+pad(i)+".md"] = prose("word"+pad(i), 20)
	}
	s := walk(t, write(t, bodies), policy())
	counts := map[corpus.Split]int{}
	for _, d := range s.Eligible() {
		counts[d.Split]++
	}
	total := len(s.Eligible())
	if total < 250 {
		t.Fatalf("%d eligible of 300", total)
	}
	for split, want := range map[corpus.Split]float64{
		corpus.Train: 0.60, corpus.Calibrate: 0.20, corpus.Test: 0.20,
	} {
		if got := float64(counts[split]) / float64(total); got < want-0.12 || got > want+0.12 {
			t.Errorf("%s holds %.2f, want about %.2f", split, got, want)
		}
	}
}

func TestInvalidPolicyIsRejected(t *testing.T) {
	for name, mutate := range map[string]func(*corpus.Policy){
		"empty seed":       func(p *corpus.Policy) { p.SplitSeed = "" },
		"negative minimum": func(p *corpus.Policy) { p.MinLexicalTokens = -1 },
		"zero weights":     func(p *corpus.Policy) { p.Splits = corpus.SplitWeights{} },
		"negative weight":  func(p *corpus.Policy) { p.Splits = corpus.SplitWeights{Train: -1, Calibrate: 50, Test: 51} },
		"empty register":   func(p *corpus.Policy) { p.Register = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p := policy()
			mutate(&p)
			if _, err := corpus.Walk(write(t, map[string]string{"a.md": prose("alpha", 20)}), p); err == nil {
				t.Errorf("invalid policy (%s) was accepted", name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Snapshot identity
// ---------------------------------------------------------------------------

// Identity must name every input that can change a result, so what it covers is
// reviewable rather than buried in a hash function.
func TestSnapshotIdentityInputsAreEnumerated(t *testing.T) {
	s := walk(t, write(t, map[string]string{"a.md": prose("alpha", 20)}), policy())
	got := s.IdentityInputs()

	want := []string{
		"admission-schema-version",
		"dedupe-algorithm-version",
		"extensions",
		"hidden-file-policy",
		"membership",
		"min-lexical-tokens",
		"register",
		"role",
		"split-algorithm-version",
		"split-seed",
		"split-weights",
		"text-contract-version",
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !equalStrings(keys, want) {
		t.Errorf("identity inputs are\n  %v\nwant\n  %v", keys, want)
	}

	// Provenance is not an analysis input and must stay out, or two checkouts of
	// the same corpus would never share cached work.
	for _, absent := range []string{"mod-time", "git-date", "root", "absolute-path"} {
		if _, ok := got[absent]; ok {
			t.Errorf("identity includes %q; provenance must not affect identity", absent)
		}
	}
}

func TestSnapshotIDIsDeterministicAndLocationIndependent(t *testing.T) {
	files := map[string]string{"a.md": prose("alpha", 20), "b.md": prose("beta", 20)}
	root := write(t, files)
	if walk(t, root, policy()).ID != walk(t, root, policy()).ID {
		t.Error("two walks of the same corpus produced different IDs")
	}
	if walk(t, write(t, files), policy()).ID != walk(t, write(t, files), policy()).ID {
		t.Error("the same corpus at two roots produced different IDs")
	}
}

func TestSnapshotIDChangesWithMembershipContentAndPolicy(t *testing.T) {
	base := map[string]string{"a.md": prose("alpha", 20), "b.md": prose("beta", 20)}
	original := walk(t, write(t, base), policy()).ID

	for name, files := range map[string]map[string]string{
		"content change":   {"a.md": prose("gamma", 20), "b.md": base["b.md"]},
		"document added":   {"a.md": base["a.md"], "b.md": base["b.md"], "c.md": prose("delta", 20)},
		"document removed": {"a.md": base["a.md"]},
		"renamed":          {"a.md": base["a.md"], "renamed.md": base["b.md"]},
	} {
		t.Run(name, func(t *testing.T) {
			if walk(t, write(t, files), policy()).ID == original {
				t.Errorf("%s did not change the snapshot ID", name)
			}
		})
	}

	for name, mutate := range map[string]func(*corpus.Policy){
		"register":   func(p *corpus.Policy) { p.Register = "email" },
		"min tokens": func(p *corpus.Policy) { p.MinLexicalTokens = 999 },
		"split seed": func(p *corpus.Policy) { p.SplitSeed = "other" },
		"weights":    func(p *corpus.Policy) { p.Splits = corpus.SplitWeights{Train: 80, Calibrate: 10, Test: 10} },
	} {
		t.Run("policy: "+name, func(t *testing.T) {
			p := policy()
			mutate(&p)
			if walk(t, write(t, base), p).ID == original {
				t.Errorf("policy field %q did not change the snapshot ID", name)
			}
		})
	}
}

// Rejected documents are part of membership: a corpus that rejected half its
// files is not the same corpus as one that rejected none.
func TestRejectedDocumentsAffectIdentity(t *testing.T) {
	a := walk(t, write(t, map[string]string{"a.md": prose("alpha", 20)}), policy()).ID
	b := walk(t, write(t, map[string]string{"a.md": prose("alpha", 20), "short.md": "no"}), policy()).ID
	if a == b {
		t.Error("adding a rejected document did not change the snapshot ID")
	}
}

func TestDeletedDocumentsDoNotPersist(t *testing.T) {
	root := write(t, map[string]string{"a.md": prose("alpha", 20), "b.md": prose("beta", 20)})
	if n := len(walk(t, root, policy()).Documents); n != 2 {
		t.Fatalf("%d documents, want 2", n)
	}
	if err := os.Remove(filepath.Join(root, "b.md")); err != nil {
		t.Fatal(err)
	}
	s := walk(t, root, policy())
	if _, ok := byPath(s, "b.md"); ok {
		t.Error("a deleted document survived into the new snapshot")
	}
}

// ---------------------------------------------------------------------------
// Ordering and edge cases
// ---------------------------------------------------------------------------

func TestDocumentsAreOrderedByPathBytewise(t *testing.T) {
	s := walk(t, write(t, map[string]string{
		"Zeta.md": prose("z", 20), "alpha.md": prose("a", 20),
		"Mid.md": prose("m", 20), "beta.md": prose("b", 20),
	}), policy())

	var paths []string
	for _, d := range s.Documents {
		paths = append(paths, d.Path)
	}
	// Bytewise, so upper case sorts before lower case. A case-insensitive sort
	// would order these differently and change the dedupe tie-break with it.
	want := []string{"Mid.md", "Zeta.md", "alpha.md", "beta.md"}
	if !equalStrings(paths, want) {
		t.Errorf("ordered %v, want %v (bytewise)", paths, want)
	}
}

func TestEmptyCorpusIsValid(t *testing.T) {
	s := walk(t, t.TempDir(), policy())
	if len(s.Documents) != 0 {
		t.Errorf("%d documents in an empty directory", len(s.Documents))
	}
	if s.ID == "" {
		t.Error("an empty corpus has no ID; it is still a snapshot")
	}
}

func TestMissingRootIsAnError(t *testing.T) {
	if _, err := corpus.Walk(filepath.Join(t.TempDir(), "nope"), policy()); err == nil {
		t.Error("nonexistent root returned no error")
	}
}

func TestRootThatIsAFileIsAnError(t *testing.T) {
	root := write(t, map[string]string{"a.md": prose("alpha", 20)})
	if _, err := corpus.Walk(filepath.Join(root, "a.md"), policy()); err == nil {
		t.Error("a file as root returned no error")
	}
}

// Symlinks are not followed: following them invites cycles and admits the same
// content twice under two paths.
func TestSymlinksAreNotFollowed(t *testing.T) {
	root := write(t, map[string]string{"real.md": prose("alpha", 20), "dir/inner.md": prose("beta", 20)})
	if err := os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "dir"), filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := walk(t, root, policy())
	for _, bad := range []string{"link.md", "linkdir/inner.md"} {
		if _, ok := byPath(s, bad); ok {
			t.Errorf("%s was walked; symlinks are not followed", bad)
		}
	}
}

func pad(i int) string {
	s := "00" + itoa(i)
	return s[len(s)-3:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
