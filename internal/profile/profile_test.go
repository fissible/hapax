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
// # Fenced: document-unit, NOT production
//
// Section 2 puts Tier A features at PARAGRAPH scale. Paragraph segmentation
// needs the structural tree from text slice 2d, which does not exist, so this
// slice computes one feature vector per DOCUMENT.
//
// That is a different statistic, not an approximation of the same one:
// document-level means average away exactly the within-document variation the
// design wants to measure, so the variances come out too small and any
// normalisation built on them would be overconfident. The profile therefore
// declares `ProductionReady = false` with a reason, and a paragraph-scale
// consumer must refuse it. It exists so the corpus → features → statistics →
// identity path is real and tested; the unit is an input to it, not its shape.
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
		MinObservationsPerFeature: 3,
		OutlierMADs:               0, // trimming off unless a test asks for it
	}
}

func loose() profile.Requirements {
	return profile.Requirements{MinDocuments: 1, MinObservationsPerFeature: 1}
}

func corpusPolicy() corpus.Policy {
	return corpus.Policy{
		Register:         "essays",
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

func prosaicCorpus(n int) map[string]string {
	files := make(map[string]string, n)
	for i := 0; i < n; i++ {
		files["d"+pad(i)+".md"] = "The subject was considered, and the matter that followed was settled because the parties agreed. " +
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
// The fence
// ---------------------------------------------------------------------------

// A document-unit profile is a plumbing artifact. It must say so, because a
// consumer that treated it as the paragraph-scale profile the design specifies
// would normalise against variances that are systematically too small.
func TestDocumentUnitProfileIsNotProductionReady(t *testing.T) {
	p := build(t, prosaicCorpus(8), requirements())

	if p.Unit != profile.UnitDocument {
		t.Errorf("Unit = %q, want %q", p.Unit, profile.UnitDocument)
	}
	if p.ProductionReady {
		t.Error("a document-unit profile reports ProductionReady; paragraph segmentation does not exist yet")
	}
	if p.NotProductionReason == "" {
		t.Error("no reason recorded for withholding production readiness")
	}
	if !strings.Contains(strings.ToLower(p.NotProductionReason), "paragraph") {
		t.Errorf("reason %q does not name the missing paragraph unit", p.NotProductionReason)
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
		"Documents": true, "Stats": true,
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
	p := build(t, prosaicCorpus(8), requirements())
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

func TestInvalidRequirementsAreRejected(t *testing.T) {
	root, snap := writeCorpus(t, prosaicCorpus(8), corpusPolicy())
	for name, req := range map[string]profile.Requirements{
		"zero minimum documents":     {MinDocuments: 0, MinObservationsPerFeature: 1},
		"negative minimum documents": {MinDocuments: -1, MinObservationsPerFeature: 1},
		"zero observations":          {MinDocuments: 1, MinObservationsPerFeature: 0},
		"negative MADs":              {MinDocuments: 1, MinObservationsPerFeature: 1, OutlierMADs: -1},
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

// Expected values are computed here from the train documents using the merged
// text and features packages, independently of the profile builder, for EVERY
// feature rather than one.
func TestStatisticsMatchAnIndependentComputation(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 24; i++ {
		files["z"+pad(i)+"a.md"] = "alpha" + pad(i) + " beta gamma delta"
		files["z"+pad(i)+"b.md"] = "alpha" + pad(i) + ", beta gamma; delta"
		files["z"+pad(i)+"c.md"] = "alpha" + pad(i) + ", beta, gamma: delta because that"
	}
	root, snap := writeCorpus(t, files, corpusPolicy())

	// Gather every train document's feature vector independently.
	perFeature := map[features.ID][]float64{}
	for _, d := range trainDocs(snap) {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.Path)))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := text.Admit(raw)
		if err != nil {
			t.Fatal(err)
		}
		v := features.Extract(doc.Tokens())
		for _, def := range features.Definitions() {
			fv, ok := v.Get(def.ID)
			if ok && fv.Defined {
				perFeature[def.ID] = append(perFeature[def.ID], fv.Value)
			}
		}
	}
	if len(perFeature) != len(features.Definitions()) {
		t.Fatalf("independent computation produced %d features, want %d", len(perFeature), len(features.Definitions()))
	}

	built, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, def := range features.Definitions() {
		values := perFeature[def.ID]
		s := stat(t, built, def.ID)

		if s.N != len(values) {
			t.Errorf("%s: N = %d, want %d", def.ID, s.N, len(values))
			continue
		}
		var sum float64
		for _, v := range values {
			sum += v
		}
		wantMean := sum / float64(len(values))
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

// Undefined values are excluded and counted, never treated as zero. Every
// document must be accounted for in exactly one of the three tallies.
func TestUndefinedValuesAreExcludedAndCounted(t *testing.T) {
	pol := corpusPolicy()
	pol.MinLexicalTokens = 0
	root, snap := withTrainDocument(t, prosaicCorpus(10), "punct.md", ", ; : . , ; : .", pol)

	built, err := profile.Build(root, snap, loose())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	s := stat(t, built, features.CommaDensity)
	if s.Undefined == 0 {
		t.Error("no undefined values counted, though a document with no lexical tokens is in train")
	}
	if s.N+s.Undefined+s.Excluded != built.Documents {
		t.Errorf("N(%d) + Undefined(%d) + Excluded(%d) != Documents(%d)", s.N, s.Undefined, s.Excluded, built.Documents)
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
	if s.N != p.Documents {
		t.Errorf("N = %d, want all %d documents", s.N, p.Documents)
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

func TestIdentityIsDeterministicAndCoversItsInputs(t *testing.T) {
	files := prosaicCorpus(8)
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
		changed := mergeFiles(files, map[string]string{"d000.md": "Entirely different prose, at some length, for this corpus."})
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
		"minimum observations": func(r *profile.Requirements) { r.MinObservationsPerFeature = 4 },
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
