// Package profile_test defines the contract for building an author profile.
//
// # What a profile is, and is not
//
// Section 1: per-feature distribution statistics only — means and variances used
// to normalise feature deviations, so a writer whose sentence length naturally
// varies is not penalised for varying. It declares NO band boundaries. Every
// emitted band, and all fallback and collapse behaviour, belongs to `eval`.
//
// # Built on TRAIN only
//
// A profile is fitted state. Section 2 assigns all tuning to Train, thresholds
// to Calibrate and reported figures to Test, so building on anything else leaks
// into the numbers eval later reports.
//
// # The unit is the PARAGRAPH
//
// Section 2 puts Tier A features at paragraph scale, and text slice 2d supplies
// the unit: one feature vector per included leaf run. The earlier document-unit
// profile was a different statistic, not an approximation of this one —
// document-level means average away exactly the within-document variation the
// design measures, so its variances came out too small. That fence is gone.
//
// # Paragraphs are pooled UNWEIGHTED
//
// Every included paragraph is one observation, so a document with twenty
// paragraphs contributes twenty and a document with three contributes three.
// This estimates "a randomly chosen paragraph by this author", which is exactly
// what `score` measures: the estimator and the inference target match.
// Weighting each document equally would estimate a different quantity — pick a
// document, then a paragraph within it — and would inflate the influence of
// short documents. The cost, accepted knowingly, is that a few long documents
// can dominate.
//
// # Split assignment stays at DOCUMENT level
//
// Section 2 holds out whole source documents before profiling, because
// paragraphs from one document share topic, register and occasion. A paragraph
// inherits its document's split and never crosses one.
//
// # The size floor is DECLARED, not derived
//
// Section 2 requires a published minimum segment size per tier. None has been
// derived, so the profile applies a declared global floor on lexical tokens per
// paragraph, records paragraphs below it rather than dropping them silently,
// and flags the floor as underived — the same discipline `MinObservationsDerived`
// already applies.
package profile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

func requirements() profile.Requirements {
	return profile.Requirements{
		MinDocuments:              5,
		MinParagraphs:             5,
		MinObservationsPerFeature: 3,
		MinParagraphLexicalTokens: 1,
		OutlierMADs:               0, // trimming off unless a test asks for it
	}
}

func loose() profile.Requirements {
	return profile.Requirements{
		MinDocuments:              1,
		MinParagraphs:             1,
		MinObservationsPerFeature: 1,
		MinParagraphLexicalTokens: 1,
	}
}

func corpusPolicy() corpus.Policy {
	return corpus.Policy{
		Register: "essays",
		// A profile is built from the author's own writing, by definition.
		Role:             corpus.RoleAuthor,
		MinLexicalTokens: 3,
		SplitSeed:        "profile-test",
		// Every weight must be positive, so train cannot be made certain by
		// policy. Fixtures that need a specific document in train search for one.
		Splits: corpus.SplitWeights{Train: 98, Calibrate: 1, Test: 1},
	}
}

func writeCorpus(t *testing.T, files map[string]string, p corpus.Policy) (string, *corpus.Snapshot) {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := corpus.Walk(root, p)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return root, snap
}

// withTrainDocument writes `files` plus one document whose body lands it in the
// TRAIN split, varying a filler suffix until it does. Split assignment is
// content-derived, so a fixture that simply assumes placement is flaky.
func withTrainDocument(t *testing.T, files map[string]string, name, body string, p corpus.Policy) (string, *corpus.Snapshot) {
	t.Helper()
	for i := 0; i < 500; i++ {
		candidate := body + " " + pad(i)
		merged := mergeFiles(files, map[string]string{name: candidate})
		root, snap := writeCorpus(t, merged, p)
		for _, d := range snap.Eligible() {
			if d.Path == name && d.Split == corpus.Train {
				return root, snap
			}
		}
	}
	t.Fatalf("no variant of %q landed in train after 500 attempts", name)
	return "", nil
}

// multiParagraphCorpus writes documents whose paragraph counts DIFFER, so that
// unweighted pooling is distinguishable from document weighting. A fixture
// where every document holds one paragraph cannot tell the two apart.
func multiParagraphCorpus(n int) map[string]string {
	files := make(map[string]string, n)
	for i := 0; i < n; i++ {
		// Word LENGTH varies with the document. An earlier version varied only
		// the numeric index, and numbers are not lexical tokens, so every
		// document had an identical word-length mean and no test could tell one
		// population of documents from another.
		word := strings.Repeat("w", i%5+2)
		paragraphs := make([]string, 0, i%4+1)
		for j := 0; j <= i%4; j++ {
			// The "m<index>" token makes every document's body distinct for any
			// n. Word length and paragraph count both cycle — i%5 and i%4 —
			// and they repeat together every twenty documents, so varying only
			// those silently deduped multiParagraphCorpus(24) down to twenty.
			paragraphs = append(paragraphs,
				"Paragraph "+pad(j)+" of m"+pad(i)+" holds "+word+" and "+word+" and "+word+", which carries a comma.")
		}
		files["m"+pad(i)+".md"] = strings.Join(paragraphs, "\n\n") + "\n"
	}
	return files
}

// paragraphValues extracts every train document's per-paragraph feature values
// INDEPENDENTLY of the profile builder, through the same public path a consumer
// would use: structure, included leaves, run tokens, extract. Returned per
// document so a test can distinguish pooled from document-weighted statistics.
func paragraphValues(t *testing.T, root string, snap *corpus.Snapshot, floor int) map[features.ID][][]float64 {
	t.Helper()
	return valuesFor(t, root, trainDocs(snap), floor)
}

func valuesFor(t *testing.T, root string, docs []corpus.Document, floor int) map[features.ID][][]float64 {
	t.Helper()
	out := map[features.ID][][]float64{}
	for _, def := range features.Definitions() {
		out[def.ID] = nil
	}
	for _, d := range docs {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.Path)))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := text.Admit(raw)
		if err != nil {
			t.Fatal(err)
		}
		perDoc := map[features.ID][]float64{}
		for _, leaf := range doc.Structure(text.DefaultStructureOptions()).IncludedLeaves() {
			tokens, err := doc.RunTokens(leaf)
			if err != nil {
				t.Fatalf("RunTokens: %v", err)
			}
			lexical := 0
			for _, tok := range tokens {
				if tok.Lexical {
					lexical++
				}
			}
			if lexical < floor {
				continue
			}
			v := features.Extract(tokens)
			for _, def := range features.Definitions() {
				if fv, ok := v.Get(def.ID); ok && fv.Defined {
					perDoc[def.ID] = append(perDoc[def.ID], fv.Value)
				}
			}
		}
		for _, def := range features.Definitions() {
			if len(perDoc[def.ID]) > 0 {
				out[def.ID] = append(out[def.ID], perDoc[def.ID])
			}
		}
	}
	return out
}

// allEligibleParagraphValues is paragraphValues over EVERY eligible document,
// train or not. It exists only so a test can prove that the train-only figure
// differs — never as an expectation the profile should match.
func allEligibleParagraphValues(t *testing.T, root string, snap *corpus.Snapshot, floor int) map[features.ID][][]float64 {
	t.Helper()
	return valuesFor(t, root, snap.Eligible(), floor)
}

func pooled(groups [][]float64) []float64 {
	var all []float64
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// documentWeightedMean is the statistic this profile deliberately does NOT
// compute. It exists so a test can prove the two differ on its fixture.
func documentWeightedMean(groups [][]float64) float64 {
	var perDoc []float64
	for _, g := range groups {
		if len(g) > 0 {
			perDoc = append(perDoc, mean(g))
		}
	}
	return mean(perDoc)
}

// Each document carries a distinct lexical word, of a length that varies with
// the index. Without it the bodies collapse to three variants, corpus admission
// dedupes them, and a helper asked for eight documents silently yields three —
// which stayed invisible until a paragraph floor made the shortfall a refusal.
func prosaicCorpus(n int) map[string]string {
	files := make(map[string]string, n)
	for i := 0; i < n; i++ {
		files["d"+pad(i)+".md"] = "The subject " + strings.Repeat("q", i%7+3) + pad(i) +
			" was considered, and the matter that followed was settled because the parties agreed. " +
			strings.Repeat("Another sentence of ordinary prose follows here. ", i%3+1)
	}
	return files
}

func build(t *testing.T, files map[string]string, req profile.Requirements) *profile.Profile {
	t.Helper()
	root, snap := writeCorpus(t, files, corpusPolicy())
	p, err := profile.Build(root, snap, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

func stat(t *testing.T, p *profile.Profile, id features.ID) profile.Stats {
	t.Helper()
	s, ok := p.Stat(id)
	if !ok {
		t.Fatalf("profile has no statistics for %q", id)
	}
	return s
}

func trainDocs(snap *corpus.Snapshot) []corpus.Document {
	var out []corpus.Document
	for _, d := range snap.Eligible() {
		if d.Split == corpus.Train {
			out = append(out, d)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The unit
// ---------------------------------------------------------------------------

// The unit is now the one the design specifies. The OLD fence — wrong unit — is
// gone, and the reason must no longer say otherwise.
func TestProfileIsParagraphUnit(t *testing.T) {
	p := build(t, multiParagraphCorpus(8), requirements())

	if p.Unit != profile.UnitParagraph {
		t.Errorf("Unit = %q, want %q", p.Unit, profile.UnitParagraph)
	}
	if strings.Contains(strings.ToLower(p.NotProductionReason), "unit") {
		t.Errorf("reason %q still blames the unit; the unit is now correct", p.NotProductionReason)
	}
}

// Readiness still withheld, for a smaller and different reason.
//
// An earlier draft of this slice set ProductionReady once the unit was right.
// That overclaims: Section 2 requires each feature's minimum sample size to be
// DERIVED and published, and requires a published minimum segment size per
// tier. Neither derivation has run — the profile applies declared stand-ins and
// says so — and a flag named ProductionReady must not assert more than the
// artifact has earned. What changed is the reason, not the answer: it used to
// be "the unit is wrong", which was a defect in the statistic itself; it is now
// "the minimums are declared, not derived", which is a calibration gap.
func TestReadinessIsWithheldUntilTheMinimumsAreDerived(t *testing.T) {
	p := build(t, multiParagraphCorpus(8), requirements())

	if p.ProductionReady {
		t.Error("ProductionReady is set while every minimum is a declared stand-in; Section 2 requires them derived")
	}
	if p.NotProductionReason == "" {
		t.Fatal("no reason recorded for withholding production readiness")
	}
	if !strings.Contains(strings.ToLower(p.NotProductionReason), "deriv") {
		t.Errorf("reason %q does not name the missing derivation", p.NotProductionReason)
	}
}

// Sample adequacy is per feature. A feature short of its minimum is undefined
// and a consumer must refuse THAT feature; it does not change the artifact-wide
// readiness answer, which is about derivation.
func TestSampleAdequacyIsPerFeature(t *testing.T) {
	enough := build(t, multiParagraphCorpus(8), requirements())
	for _, d := range features.Definitions() {
		if !stat(t, enough, d.ID).Defined {
			t.Errorf("%s is undefined with %d observations against a minimum of %d",
				d.ID, stat(t, enough, d.ID).N, enough.Requirements.MinObservationsPerFeature)
		}
	}

	req := requirements()
	req.MinObservationsPerFeature = 100000
	short := build(t, multiParagraphCorpus(8), req)
	for _, d := range features.Definitions() {
		if stat(t, short, d.ID).Defined {
			t.Errorf("%s is defined against an unreachable minimum", d.ID)
		}
	}
	if short.NotProductionReason != enough.NotProductionReason {
		t.Errorf("readiness reason changed with the observation floor (%q vs %q); adequacy is per feature",
			short.NotProductionReason, enough.NotProductionReason)
	}
}

// The document unit is no longer produced. The constant survives so a profile
// persisted before this slice is recognisably not paragraph-scale rather than
// carrying an unknown unit string.
func TestDocumentUnitIsNeverProduced(t *testing.T) {
	if profile.UnitDocument == profile.UnitParagraph {
		t.Fatal("the two units are the same value")
	}
	for _, files := range []map[string]string{multiParagraphCorpus(8), prosaicCorpus(8)} {
		if p := build(t, files, requirements()); p.Unit == profile.UnitDocument {
			t.Error("Build produced a document-unit profile")
		}
	}
}

// ---------------------------------------------------------------------------
// Shape: what a profile carries, exactly
// ---------------------------------------------------------------------------

// An explicit allowlist rather than a forbidden-substring scan: a substring ban
// rejects harmless provenance names and misses whole APIs. This states the
// schema, so adding a field is a deliberate act reviewed here.
func TestProfileSchemaIsExactly(t *testing.T) {
	want := map[string]bool{
		"ID": true, "SnapshotID": true, "Register": true, "Split": true,
		"Unit": true, "ProductionReady": true, "NotProductionReason": true,
		"FeatureSetVersion": true, "FeatureManifestDigest": true,
		"SchemaVersion": true, "VarianceConvention": true,
		"OutlierAlgorithm": true, "Requirements": true,
		"Documents": true, "Paragraphs": true,
		"ParagraphsBelowFloor": true, "ParagraphFloorDerived": true,
		"Stats": true,
	}
	rt := reflect.TypeOf(profile.Profile{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if !want[name] {
			t.Errorf("Profile has unexpected field %q; bands, thresholds and verdicts belong to eval, and any new field needs a decision here", name)
		}
		delete(want, name)
	}
	for missing := range want {
		t.Errorf("Profile is missing field %q", missing)
	}
}

func TestStatsSchemaIsExactly(t *testing.T) {
	want := map[string]bool{
		"Feature": true, "N": true, "Undefined": true, "Excluded": true,
		"Mean": true, "Variance": true, "Defined": true, "VarianceDefined": true,
		"MinObservations": true, "MinObservationsDerived": true,
	}
	rt := reflect.TypeOf(profile.Stats{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if !want[name] {
			t.Errorf("Stats has unexpected field %q", name)
		}
		delete(want, name)
	}
	for missing := range want {
		t.Errorf("Stats is missing field %q", missing)
	}
}

// Section 2 requires each feature to declare its own minimum sample size, and
// requires that minimum to be DERIVED. Nothing has been derived, so the profile
// applies a global floor and says the minimum is not derived — rather than
// presenting a stand-in as an established figure.
func TestPerFeatureMinimumsAreRecordedAsUnderived(t *testing.T) {
	p := build(t, multiParagraphCorpus(8), requirements())
	for _, d := range features.Definitions() {
		s := stat(t, p, d.ID)
		if s.MinObservationsDerived {
			t.Errorf("%s claims a derived minimum; Section 2's derivation has not run", d.ID)
		}
		if s.MinObservations != p.Requirements.MinObservationsPerFeature {
			t.Errorf("%s uses minimum %d, want the global floor %d", d.ID, s.MinObservations, p.Requirements.MinObservationsPerFeature)
		}
	}
}

func TestProfileCoversExactlyTheFeatureSet(t *testing.T) {
	p := build(t, prosaicCorpus(8), requirements())
	defs := features.Definitions()
	if len(p.Stats) != len(defs) {
		t.Fatalf("%d statistics, %d features", len(p.Stats), len(defs))
	}
	for i, d := range defs {
		if p.Stats[i].Feature != d.ID {
			t.Errorf("statistic %d is %q, want %q — ordered to match the feature set", i, p.Stats[i].Feature, d.ID)
		}
	}
	if _, ok := p.Stat("no-such-feature"); ok {
		t.Error("Stat reported success for a feature that does not exist")
	}
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

func TestBuildRefusesBelowMinimumCorpusSize(t *testing.T) {
	req := requirements()
	req.MinDocuments = 10

	root, snap := writeCorpus(t, prosaicCorpus(4), corpusPolicy())
	p, err := profile.Build(root, snap, req)
	if err == nil {
		t.Fatal("Build produced a profile from 4 documents with a minimum of 10")
	}
	if p != nil {
		t.Error("Build returned a profile alongside an error")
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("refusal %q does not name the requirement", err)
	}
}

func TestBuildRefusesWhenNoDocumentIsInTrain(t *testing.T) {
	p := corpusPolicy()
	p.Splits = corpus.SplitWeights{Train: 1, Calibrate: 1, Test: 98}

	// Find a corpus where nothing landed in train.
	for i := 0; i < 200; i++ {
		root, snap := writeCorpus(t, map[string]string{"a.md": "Some ordinary prose here " + pad(i)}, p)
		if len(trainDocs(snap)) != 0 {
			continue
		}
		if _, err := profile.Build(root, snap, loose()); err == nil {
			t.Error("Build produced a profile with no train documents")
		}
		return
	}
	t.Skip("could not construct a corpus with no train documents")
}

// A corpus can hold enough documents and still not hold enough paragraphs, so
// the paragraph floor is its own refusal rather than a consequence of the
// document one.
func TestBuildRefusesBelowMinimumParagraphCount(t *testing.T) {
	root, snap := writeCorpus(t, prosaicCorpus(8), corpusPolicy())

	req := loose()
	req.MinParagraphs = 100000
	if _, err := profile.Build(root, snap, req); err == nil {
		t.Fatal("Build accepted a corpus far below the paragraph minimum")
	} else if !strings.Contains(strings.ToLower(err.Error()), "paragraph") {
		t.Errorf("refusal %q does not name the paragraph shortfall", err)
	}
}

func TestInvalidRequirementsAreRejected(t *testing.T) {
	root, snap := writeCorpus(t, prosaicCorpus(8), corpusPolicy())
	valid := loose()
	invalid := func(mutate func(*profile.Requirements)) profile.Requirements {
		req := valid
		mutate(&req)
		return req
	}
	for name, req := range map[string]profile.Requirements{
		"zero minimum documents":      invalid(func(r *profile.Requirements) { r.MinDocuments = 0 }),
		"negative minimum documents":  invalid(func(r *profile.Requirements) { r.MinDocuments = -1 }),
		"zero minimum paragraphs":     invalid(func(r *profile.Requirements) { r.MinParagraphs = 0 }),
		"negative minimum paragraphs": invalid(func(r *profile.Requirements) { r.MinParagraphs = -1 }),
		"zero observations":           invalid(func(r *profile.Requirements) { r.MinObservationsPerFeature = 0 }),
		// A floor of zero is not "no floor": Section 2 requires a DECLARED
		// minimum segment size, so omitting it must be an error rather than a
		// silent bypass of the discipline.
		"zero paragraph floor":     invalid(func(r *profile.Requirements) { r.MinParagraphLexicalTokens = 0 }),
		"negative paragraph floor": invalid(func(r *profile.Requirements) { r.MinParagraphLexicalTokens = -1 }),
		"negative MADs":            invalid(func(r *profile.Requirements) { r.OutlierMADs = -1 }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := profile.Build(root, snap, req); err == nil {
				t.Errorf("invalid requirements (%s) were accepted", name)
			}
		})
	}
}

// The corpus recorded a content hash per document. If a file changed since the
// walk, the profile would describe text the snapshot never saw.
func TestBuildRefusesWhenSourceContentChangedSinceTheSnapshot(t *testing.T) {
	root, snap := writeCorpus(t, prosaicCorpus(8), corpusPolicy())
	train := trainDocs(snap)
	if len(train) == 0 {
		t.Fatal("no train documents")
	}

	target := filepath.Join(root, filepath.FromSlash(train[0].Path))
	if err := os.WriteFile(target, []byte("Entirely different content written after the walk."), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := profile.Build(root, snap, loose())
	if err == nil {
		t.Fatal("Build accepted a document whose content no longer matches the snapshot")
	}
	if !strings.Contains(err.Error(), train[0].Path) {
		t.Errorf("refusal %q does not name the changed document", err)
	}
}

func TestEmptyEligibleCorpusIsRefused(t *testing.T) {
	root, snap := writeCorpus(t, map[string]string{"only.go": "package main"}, corpusPolicy())
	if _, err := profile.Build(root, snap, profile.Requirements{MinDocuments: 1, MinObservationsPerFeature: 1}); err == nil {
		t.Error("a corpus with no eligible documents produced a profile")
	}
}

// ---------------------------------------------------------------------------
// Which documents are used
// ---------------------------------------------------------------------------

func TestProfileUsesOnlyTrainSplitDocuments(t *testing.T) {
	p := corpusPolicy()
	p.Splits = corpus.SplitWeights{Train: 34, Calibrate: 33, Test: 33}

	root, snap := writeCorpus(t, prosaicCorpus(40), p)
	built, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	train := trainDocs(snap)
	if len(train) == len(snap.Eligible()) {
		t.Fatal("the fixture put every document in train; the test cannot detect the fault it exists for")
	}
	if built.Documents != len(train) {
		t.Errorf("profile used %d documents, want the %d in train", built.Documents, len(train))
	}
	if built.Split != corpus.Train {
		t.Errorf("Split = %q, want %q", built.Split, corpus.Train)
	}
}

func TestRejectedDocumentsAreNotUsed(t *testing.T) {
	files := prosaicCorpus(12)
	files["tiny.md"] = "no"
	files["dupe.md"] = files["d000.md"]
	root, snap := writeCorpus(t, files, corpusPolicy())

	built, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Eligible AND in train — not merely eligible.
	if want := len(trainDocs(snap)); built.Documents != want {
		t.Errorf("profile used %d documents, want the %d eligible train documents", built.Documents, want)
	}
	for _, d := range snap.Documents {
		if d.Admission != corpus.Eligible && d.Split != "" {
			t.Errorf("%s is rejected but carries split %q", d.Path, d.Split)
		}
	}
}

// ---------------------------------------------------------------------------
// The statistics
// ---------------------------------------------------------------------------

// Expected values are computed here from the train documents' PARAGRAPHS,
// independently of the profile builder, for EVERY feature rather than one.
func TestStatisticsMatchAnIndependentComputation(t *testing.T) {
	root, snap := writeCorpus(t, multiParagraphCorpus(24), corpusPolicy())

	req := loose()
	independent := paragraphValues(t, root, snap, req.MinParagraphLexicalTokens)

	built, err := profile.Build(root, snap, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, def := range features.Definitions() {
		values := pooled(independent[def.ID])
		s := stat(t, built, def.ID)

		if s.N != len(values) {
			t.Errorf("%s: N = %d, want %d paragraph observations", def.ID, s.N, len(values))
			continue
		}
		if len(values) == 0 {
			continue
		}
		wantMean := mean(values)
		if diff := s.Mean - wantMean; diff > 1e-12 || diff < -1e-12 {
			t.Errorf("%s: Mean = %v, want %v", def.ID, s.Mean, wantMean)
		}

		if len(values) < 2 {
			continue
		}
		var sq float64
		for _, v := range values {
			sq += (v - wantMean) * (v - wantMean)
		}
		// SAMPLE variance: the profile generalises to the author's future
		// writing, not just the corpus it saw. Population variance would be
		// systematically too small — 20% too small at N=5.
		wantVar := sq / float64(len(values)-1)
		if diff := s.Variance - wantVar; diff > 1e-12 || diff < -1e-12 {
			t.Errorf("%s: Variance = %v, want %v (sample, not population)", def.ID, s.Variance, wantVar)
		}
	}
}

// The one test whose expected numbers do not come from the extraction path the
// builder uses. Everything else in this file shares Structure, IncludedLeaves,
// RunTokens and features.Extract with production, so a fault in that shared
// path would agree with itself. Here the values are computed by hand.
//
// The document holds exactly two paragraphs:
//
//	"aa bb cc dd"      -> 4 lexical tokens, each 2 characters -> mean 2.0
//	"aaa bbb ccc ddd"  -> 4 lexical tokens, each 3 characters -> mean 3.0
//
// so pooled over both paragraphs word_length_mean has mean 2.5 and SAMPLE
// variance ((2-2.5)^2 + (3-2.5)^2) / 1 = 0.5. A document-unit builder would see
// one observation of 2.5 and no variance at all, which is exactly the failure
// this slice exists to end.
//
// withTrainDocument appends a numeric filler to place the document in train.
// That filler is a Number token, not a Word, so it is not lexical and cannot
// move a word-length mean.
func TestParagraphStatisticsAgainstHandComputedValues(t *testing.T) {
	root, snap := withTrainDocument(t, nil, "anchor.md", "aa bb cc dd\n\naaa bbb ccc ddd", corpusPolicy())
	if len(trainDocs(snap)) != 1 {
		t.Fatalf("%d train documents, want 1", len(trainDocs(snap)))
	}

	p, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if p.Documents != 1 {
		t.Errorf("Documents = %d, want 1", p.Documents)
	}
	if p.Paragraphs != 2 {
		t.Fatalf("Paragraphs = %d, want 2 — one document, two paragraphs", p.Paragraphs)
	}

	s := stat(t, p, features.WordLengthMean)
	if s.N != 2 {
		t.Fatalf("N = %d, want 2 paragraph observations", s.N)
	}
	if diff := s.Mean - 2.5; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("Mean = %v, want 2.5", s.Mean)
	}
	if !s.VarianceDefined {
		t.Fatal("variance undefined over two observations")
	}
	if diff := s.Variance - 0.5; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("Variance = %v, want 0.5 (sample variance over two paragraphs)", s.Variance)
	}
}

// The pooling decision, tested so that it can fail. A fixture where every
// document holds the same number of paragraphs cannot distinguish unweighted
// pooling from document weighting, so this one first PROVES the two statistics
// differ on its corpus, then asserts the profile computes the pooled one.
func TestParagraphsArePooledUnweightedAcrossDocuments(t *testing.T) {
	files := map[string]string{}
	// Documents with many comma-free paragraphs, each also carrying leaves that
	// are NOT in the feature population: a heading, a code block and a bare
	// list label. A builder that pooled every leaf rather than the included
	// ones would pass without them.
	for i := 0; i < 12; i++ {
		paragraphs := make([]string, 0, 6)
		for j := 0; j < 6; j++ {
			paragraphs = append(paragraphs, "Plain prose "+pad(i)+pad(j)+" without any punctuation at all here")
		}
		files["long"+pad(i)+".md"] = "# Heading, with a comma, excluded by role\n\n" +
			strings.Join(paragraphs, "\n\n") +
			"\n\n- Redis\n\n```\ncode, with, commas, is, not, prose\n```\n"
	}
	// Documents with a single comma-dense paragraph.
	for i := 0; i < 12; i++ {
		files["short"+pad(i)+".md"] = "Dense, prose, " + pad(i) + ", with, very, many, commas, indeed, here\n"
	}
	root, snap := writeCorpus(t, files, corpusPolicy())

	req := loose()
	independent := paragraphValues(t, root, snap, req.MinParagraphLexicalTokens)
	groups := independent[features.CommaDensity]

	wantPooled := mean(pooled(groups))
	wantWeighted := documentWeightedMean(groups)
	if diff := wantPooled - wantWeighted; diff < 1e-6 && diff > -1e-6 {
		t.Fatalf("fixture cannot discriminate: pooled mean %v and document-weighted mean %v are equal", wantPooled, wantWeighted)
	}

	built, err := profile.Build(root, snap, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := stat(t, built, features.CommaDensity).Mean

	if diff := got - wantPooled; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("Mean = %v, want the unweighted pooled mean %v", got, wantPooled)
	}
	if diff := got - wantWeighted; diff < 1e-12 && diff > -1e-12 {
		t.Errorf("Mean = %v is the document-weighted mean; paragraphs are pooled unweighted", got)
	}

	if built.Paragraphs != len(pooled(groups)) {
		t.Errorf("Paragraphs = %d, want %d", built.Paragraphs, len(pooled(groups)))
	}
	if built.Paragraphs <= built.Documents {
		t.Errorf("Paragraphs(%d) <= Documents(%d); this corpus has multi-paragraph documents", built.Paragraphs, built.Documents)
	}
}

// A paragraph inherits its document's split and never crosses one. Splitting at
// paragraph level would leak: paragraphs from one document share topic,
// register and occasion, which inflates every metric eval later reports.
func TestParagraphsComeOnlyFromTrainDocuments(t *testing.T) {
	// Balanced split weights, unlike the shared policy: this test needs
	// documents OUTSIDE train to have anything that could leak, and the shared
	// 98/1/1 weights put essentially everything in train.
	pol := corpusPolicy()
	pol.Splits = corpus.SplitWeights{Train: 1, Calibrate: 1, Test: 1}
	root, snap := writeCorpus(t, multiParagraphCorpus(24), pol)

	nonTrain := 0
	for _, d := range snap.Eligible() {
		if d.Split != corpus.Train {
			nonTrain++
		}
	}
	if nonTrain == 0 {
		t.Fatal("fixture has no non-train documents, so it cannot detect a leak")
	}

	built, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	trainOnly := pooled(paragraphValues(t, root, snap, loose().MinParagraphLexicalTokens)[features.WordLengthMean])
	if built.Paragraphs != len(trainOnly) {
		t.Errorf("Paragraphs = %d, want %d — the train documents' paragraphs only", built.Paragraphs, len(trainOnly))
	}
	if built.Documents != len(trainDocs(snap)) {
		t.Errorf("Documents = %d, want %d train documents", built.Documents, len(trainDocs(snap)))
	}

	// Counts alone would not catch a leak that happened to preserve them, so
	// the STATISTICS must be the train-only ones. The fixture is built so the
	// two populations give different means.
	everything := pooled(allEligibleParagraphValues(t, root, snap, loose().MinParagraphLexicalTokens)[features.WordLengthMean])
	if len(everything) == len(trainOnly) {
		t.Fatalf("fixture cannot discriminate: all %d eligible paragraphs are in train", len(everything))
	}
	if diff := mean(everything) - mean(trainOnly); diff < 1e-9 && diff > -1e-9 {
		t.Fatalf("fixture cannot discriminate: train-only and all-document means are equal (%v)", mean(trainOnly))
	}
	got := stat(t, built, features.WordLengthMean).Mean
	if diff := got - mean(trainOnly); diff > 1e-12 || diff < -1e-12 {
		t.Errorf("Mean = %v, want the train-only mean %v", got, mean(trainOnly))
	}
	if diff := got - mean(everything); diff < 1e-12 && diff > -1e-12 {
		t.Errorf("Mean = %v is the all-document mean; Calibrate and Test leaked into the profile", got)
	}
}

func TestVarianceConventionIsRecorded(t *testing.T) {
	p := build(t, prosaicCorpus(8), requirements())
	if p.VarianceConvention != profile.SampleVariance {
		t.Errorf("VarianceConvention = %q, want %q", p.VarianceConvention, profile.SampleVariance)
	}
}

func TestVarianceRequiresAtLeastTwoObservations(t *testing.T) {
	root, snap := withTrainDocument(t, nil, "only.md", "One short document of prose here.", corpusPolicy())
	if len(trainDocs(snap)) != 1 {
		t.Fatalf("%d train documents, want 1", len(trainDocs(snap)))
	}
	p, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	s := stat(t, p, features.WordLengthMean)
	if s.N != 1 {
		t.Fatalf("N = %d, want 1", s.N)
	}
	if s.VarianceDefined {
		t.Error("variance is defined over a single observation; one observation is no variance at all")
	}
	if !s.Defined {
		t.Error("the mean should still be defined over one observation")
	}
}

func TestFeatureWithTooFewObservationsIsUndefined(t *testing.T) {
	req := requirements()
	req.MinObservationsPerFeature = 1000

	p := build(t, prosaicCorpus(8), req)
	for _, d := range features.Definitions() {
		s := stat(t, p, d.ID)
		if s.Defined {
			t.Errorf("%s is defined with %d observations against a minimum of 1000", d.ID, s.N)
		}
		if s.Mean != 0 || s.Variance != 0 {
			t.Errorf("%s carries values (%v, %v) while undefined", d.ID, s.Mean, s.Variance)
		}
	}
}

// Every admitted paragraph is accounted for in exactly one of the three
// tallies, so a value can never go missing between them.
func TestEveryParagraphIsAccountedFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  profile.Requirements
	}{
		{"no trimming", loose()},
		{"with trimming", func() profile.Requirements { r := loose(); r.OutlierMADs = 3; return r }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A corpus carrying every kind of leaf the accounting must survive:
			// a role-excluded heading, a non-sentential list label, a fully
			// excised run, and a below-floor paragraph.
			files := map[string]string{}
			for i := 0; i < 16; i++ {
				files["acc"+pad(i)+".md"] = "# Heading " + pad(i) + "\n\n" +
					"A paragraph of ordinary prose that clears the floor comfortably.\n\n" +
					"- Redis\n\n" +
					"`code and nothing else`\n\n" +
					"Tiny " + pad(i) + "\n\n" +
					"Another paragraph of ordinary prose, also clearing the floor.\n"
			}
			root, snap := writeCorpus(t, files, corpusPolicy())
			req := tc.req
			req.MinParagraphLexicalTokens = 5

			built, err := profile.Build(root, snap, req)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if built.Paragraphs == 0 {
				t.Fatal("no paragraphs admitted; the identity below would hold vacuously")
			}
			if built.ParagraphsBelowFloor == 0 {
				t.Error("no below-floor paragraphs counted, though every document holds a two-word one")
			}

			want := len(pooled(paragraphValues(t, root, snap, req.MinParagraphLexicalTokens)[features.WordLengthMean]))
			if built.Paragraphs != want {
				t.Errorf("Paragraphs = %d, want %d — headings, labels and fully excised runs are not observations", built.Paragraphs, want)
			}
			for _, d := range features.Definitions() {
				s := stat(t, built, d.ID)
				if s.N+s.Undefined+s.Excluded != built.Paragraphs {
					t.Errorf("%s: N(%d) + Undefined(%d) + Excluded(%d) != Paragraphs(%d)",
						d.ID, s.N, s.Undefined, s.Excluded, built.Paragraphs)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The declared size floor
// ---------------------------------------------------------------------------

// A paragraph below the floor is not an observation, and it is COUNTED rather
// than dropped silently — the same discipline slice 2d applies to leaves it
// excludes. A four-word paragraph otherwise contributes a word-length mean at
// full weight, which is the noise the tiering scheme exists to keep out.
func TestParagraphsBelowTheFloorAreExcludedAndCounted(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 12; i++ {
		files["p"+pad(i)+".md"] = "A paragraph of ordinary prose that comfortably clears any floor here.\n\nTiny " + pad(i) + "\n"
	}
	root, snap := writeCorpus(t, files, corpusPolicy())

	low := loose()
	low.MinParagraphLexicalTokens = 1
	high := loose()
	high.MinParagraphLexicalTokens = 6

	below, err := profile.Build(root, snap, low)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	above, err := profile.Build(root, snap, high)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if below.ParagraphsBelowFloor != 0 {
		t.Errorf("floor 1 excluded %d paragraphs; every paragraph here has at least one lexical token", below.ParagraphsBelowFloor)
	}
	if above.ParagraphsBelowFloor == 0 {
		t.Fatal("floor 6 excluded nothing, though half the paragraphs are two words long")
	}
	if above.Paragraphs+above.ParagraphsBelowFloor != below.Paragraphs {
		t.Errorf("Paragraphs(%d) + ParagraphsBelowFloor(%d) != the unfiltered %d",
			above.Paragraphs, above.ParagraphsBelowFloor, below.Paragraphs)
	}

	// The floor is a separate tally from Undefined: a short paragraph is not an
	// undefined observation, it is not an observation at all.
	for _, d := range features.Definitions() {
		if got := stat(t, above, d.ID).Undefined; got != 0 {
			t.Errorf("%s counted %d undefined values; below-floor paragraphs belong in ParagraphsBelowFloor", d.ID, got)
		}
	}

	// Counters alone are not enough: a builder can populate them correctly and
	// still compute its statistics over the wrong population. The mean must
	// move, and must land on the above-floor value.
	survivors := pooled(paragraphValues(t, root, snap, high.MinParagraphLexicalTokens)[features.WordLengthMean])
	everything := pooled(paragraphValues(t, root, snap, low.MinParagraphLexicalTokens)[features.WordLengthMean])
	if len(survivors) != above.Paragraphs {
		t.Errorf("Paragraphs = %d, want %d above-floor paragraphs", above.Paragraphs, len(survivors))
	}
	if diff := mean(survivors) - mean(everything); diff < 1e-9 && diff > -1e-9 {
		t.Fatalf("fixture cannot discriminate: the floor does not move the mean (%v)", mean(survivors))
	}
	got := stat(t, above, features.WordLengthMean).Mean
	if diff := got - mean(survivors); diff > 1e-12 || diff < -1e-12 {
		t.Errorf("Mean = %v, want the above-floor mean %v", got, mean(survivors))
	}
	if diff := got - mean(everything); diff < 1e-12 && diff > -1e-12 {
		t.Errorf("Mean = %v includes the below-floor paragraphs", got)
	}
}

// The floor is declared, not derived, and says so.
func TestParagraphFloorIsRecordedAsUnderived(t *testing.T) {
	p := build(t, multiParagraphCorpus(8), requirements())
	if p.ParagraphFloorDerived {
		t.Error("the profile claims a derived paragraph floor; Section 2's derivation has not run")
	}
	if p.Requirements.MinParagraphLexicalTokens <= 0 {
		t.Error("no paragraph size floor declared; Section 2 requires a published minimum segment size")
	}
}

// ---------------------------------------------------------------------------
// Outliers
// ---------------------------------------------------------------------------

// Trimming changes the author's distribution, so it is an explicit recorded
// policy rather than a silent default.
func TestOutlierTrimmingIsOptInAndCounted(t *testing.T) {
	pol := corpusPolicy()
	files := prosaicCorpus(24)
	root, snap := withTrainDocument(t, files, "outlier.md", "a,,,,,,,,,,,,,,,,,,,, b c", pol)

	off, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := stat(t, off, features.CommaDensity).Excluded; got != 0 {
		t.Errorf("%d observations excluded with trimming disabled", got)
	}

	req := loose()
	req.OutlierMADs = 3
	on, err := profile.Build(root, snap, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	trimmed := stat(t, on, features.CommaDensity)
	if trimmed.Excluded == 0 {
		t.Error("trimming enabled but nothing was excluded, though train contains an extreme value")
	}
	if trimmed.Variance >= stat(t, off, features.CommaDensity).Variance {
		t.Error("trimming the extreme value did not reduce the variance")
	}
	// A versioned identifier, not merely a non-empty string: the identity
	// depends on it, so changing the algorithm must change the name.
	if on.OutlierAlgorithm != profile.MADMedianV1 {
		t.Errorf("OutlierAlgorithm = %q, want %q", on.OutlierAlgorithm, profile.MADMedianV1)
	}
	// With trimming off the algorithm field records that none was applied.
	if off.OutlierAlgorithm != profile.NoTrimming {
		t.Errorf("with trimming disabled OutlierAlgorithm = %q, want %q", off.OutlierAlgorithm, profile.NoTrimming)
	}
}

// A corpus where most documents share one value has a zero MAD, and every
// deviation is then infinitely many MADs away. Trimming must not empty the
// feature.
func TestZeroMADDoesNotTrimEverything(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 20; i++ {
		files["same"+pad(i)+".md"] = "alpha" + pad(i) + " beta gamma" // identical comma density: 0
	}
	root, snap := writeCorpus(t, files, corpusPolicy())

	req := loose()
	req.OutlierMADs = 3
	p, err := profile.Build(root, snap, req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	s := stat(t, p, features.CommaDensity)
	// Nothing may be trimmed: with every value identical, no value deviates.
	// Asserting only that something survived would permit trimming all but one.
	if s.Excluded != 0 {
		t.Errorf("a zero MAD excluded %d observations; identical values are not outliers", s.Excluded)
	}
	if s.N != p.Paragraphs {
		t.Errorf("N = %d, want all %d paragraphs", s.N, p.Paragraphs)
	}
	if !s.Defined {
		t.Error("a feature whose values are all identical is defined, with zero variance")
	}
	if s.Variance != 0 {
		t.Errorf("Variance = %v over identical values, want 0", s.Variance)
	}
}

// ---------------------------------------------------------------------------
// Provenance and identity
// ---------------------------------------------------------------------------

func TestProvenanceIsRecorded(t *testing.T) {
	root, snap := writeCorpus(t, prosaicCorpus(8), corpusPolicy())
	p, err := profile.Build(root, snap, requirements())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if p.SnapshotID != snap.ID {
		t.Errorf("SnapshotID = %q, want %q", p.SnapshotID, snap.ID)
	}
	if p.Register != "essays" {
		t.Errorf("Register = %q, want %q — carried from the corpus policy, never inferred", p.Register, "essays")
	}
	if p.FeatureSetVersion != features.SetVersion {
		t.Errorf("FeatureSetVersion = %d, want %d", p.FeatureSetVersion, features.SetVersion)
	}
	// Section 2 is explicit that a version integer is provenance, not identity.
	// The manifest digest is what actually pins which features were computed.
	if p.FeatureManifestDigest == "" {
		t.Error("no feature manifest digest recorded; the version integer alone cannot identify the feature set")
	}
	if p.SchemaVersion == 0 {
		t.Error("no profile schema version recorded")
	}
	if p.ID == "" {
		t.Error("profile has no ID")
	}
}

// The profile's recorded digest must be the manifest's own, not a private
// recomputation. `profile` used to derive it from the definition fields it
// happened to know about, which could not notice a field added elsewhere — so
// two profiles built under different sampling models would have collided on one
// identity while every test here passed.
func TestTheManifestDigestIsTheManifestsOwn(t *testing.T) {
	p := build(t, multiParagraphCorpus(8), requirements())

	if p.FeatureManifestDigest != features.ManifestDigest() {
		t.Errorf("profile records digest %q, the feature manifest reports %q",
			p.FeatureManifestDigest, features.ManifestDigest())
	}
	if p.FeatureManifestDigest == "" {
		t.Error("no digest recorded")
	}
	if got := p.IdentityInputs()["feature-manifest-digest"]; got != features.ManifestDigest() {
		t.Errorf("identity input feature-manifest-digest = %q, want the manifest's %q", got, features.ManifestDigest())
	}
}

func TestIdentityIsDeterministicAndCoversItsInputs(t *testing.T) {
	files := multiParagraphCorpus(8)
	root, snap := writeCorpus(t, files, corpusPolicy())

	first, err := profile.Build(root, snap, requirements())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := profile.Build(root, snap, requirements())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if first.ID != second.ID {
		t.Error("two builds of the same corpus produced different profile IDs")
	}

	t.Run("corpus content", func(t *testing.T) {
		changed := mergeFiles(files, map[string]string{"m000.md": "Entirely different prose, at some length, for this corpus.\n\nAnd a second paragraph that differs too.\n"})
		root2, snap2 := writeCorpus(t, changed, corpusPolicy())
		other, err := profile.Build(root2, snap2, requirements())
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if other.ID == first.ID {
			t.Error("changing corpus content did not change the profile ID")
		}
	})

	for name, mutate := range map[string]func(*profile.Requirements){
		"minimum documents":    func(r *profile.Requirements) { r.MinDocuments = 6 },
		"minimum paragraphs":   func(r *profile.Requirements) { r.MinParagraphs = 6 },
		"minimum observations": func(r *profile.Requirements) { r.MinObservationsPerFeature = 4 },
		"paragraph size floor": func(r *profile.Requirements) { r.MinParagraphLexicalTokens = 2 },
		"outlier rule":         func(r *profile.Requirements) { r.OutlierMADs = 3 },
	} {
		t.Run("requirements: "+name, func(t *testing.T) {
			req := requirements()
			mutate(&req)
			other, err := profile.Build(root, snap, req)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if other.ID == first.ID {
				t.Errorf("changing %s did not change the profile ID", name)
			}
		})
	}

	inputs := first.IdentityInputs()
	want := []string{
		"feature-manifest-digest",
		"min-documents",
		"min-observations-per-feature",
		"min-paragraph-lexical-tokens",
		"min-paragraphs",
		"outlier-algorithm",
		"outlier-mads",
		"profile-schema-version",
		"register",
		"snapshot-id",
		"split",
		"unit",
		"variance-convention",
	}
	var keys []string
	for k := range inputs {
		keys = append(keys, k)
	}
	sortStrings(keys)
	if !equalStrings(keys, want) {
		t.Errorf("identity inputs are\n  %v\nwant\n  %v", keys, want)
	}

	// Presence is not enough: a fixed unrelated value would satisfy a key check
	// while the identity described a different artifact than the one returned.
	for key, wantValue := range map[string]string{
		"snapshot-id":             first.SnapshotID,
		"register":                first.Register,
		"split":                   string(first.Split),
		"unit":                    string(first.Unit),
		"feature-manifest-digest": first.FeatureManifestDigest,
		"variance-convention":     string(first.VarianceConvention),
		"outlier-algorithm":       first.OutlierAlgorithm,
	} {
		if got := inputs[key]; got != wantValue {
			t.Errorf("identity input %q = %q, want the profile's own %q", key, got, wantValue)
		}
	}
	if inputs["profile-schema-version"] != itoa(first.SchemaVersion) {
		t.Errorf("identity input profile-schema-version = %q, want %q", inputs["profile-schema-version"], itoa(first.SchemaVersion))
	}
	if inputs["min-documents"] != itoa(first.Requirements.MinDocuments) {
		t.Errorf("identity input min-documents = %q, want %q", inputs["min-documents"], itoa(first.Requirements.MinDocuments))
	}
	if inputs["min-observations-per-feature"] != itoa(first.Requirements.MinObservationsPerFeature) {
		t.Errorf("identity input min-observations-per-feature = %q, want %q", inputs["min-observations-per-feature"], itoa(first.Requirements.MinObservationsPerFeature))
	}
	if inputs["min-paragraphs"] != itoa(first.Requirements.MinParagraphs) {
		t.Errorf("identity input min-paragraphs = %q, want %q", inputs["min-paragraphs"], itoa(first.Requirements.MinParagraphs))
	}
	if inputs["min-paragraph-lexical-tokens"] != itoa(first.Requirements.MinParagraphLexicalTokens) {
		t.Errorf("identity input min-paragraph-lexical-tokens = %q, want %q", inputs["min-paragraph-lexical-tokens"], itoa(first.Requirements.MinParagraphLexicalTokens))
	}
}

func mergeFiles(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
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

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
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
