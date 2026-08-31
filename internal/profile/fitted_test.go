package profile_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
)

// store persists six of a Profile's fields and cannot fill the rest, so handing
// scoring a *profile.Profile read back from a database means handing it zeroes
// where facts belong — Documents, ParagraphFloorDerived, the build provenance.
// Fitted is what scoring actually needs, and naming it makes any new dependency
// on the broad artifact a compile error rather than a silent zero.
func TestFittedCarriesWhatScoringNeedsAndNothingElse(t *testing.T) {
	built := builtProfile(t)

	fitted, err := built.Fitted()
	if err != nil {
		t.Fatalf("Fitted: %v", err)
	}

	if fitted.ID != built.ID {
		t.Errorf("id = %q, want %q", fitted.ID, built.ID)
	}
	if fitted.Unit != built.Unit {
		t.Errorf("unit = %q, want %q", fitted.Unit, built.Unit)
	}
	if fitted.FeatureSetVersion != built.FeatureSetVersion {
		t.Errorf("set version = %d, want %d", fitted.FeatureSetVersion, built.FeatureSetVersion)
	}
	if fitted.FeatureManifestDigest != built.FeatureManifestDigest {
		t.Errorf("manifest digest = %q, want %q", fitted.FeatureManifestDigest, built.FeatureManifestDigest)
	}
	if fitted.MinParagraphLexicalTokens != built.Requirements.MinParagraphLexicalTokens {
		t.Errorf("floor = %d, want %d", fitted.MinParagraphLexicalTokens, built.Requirements.MinParagraphLexicalTokens)
	}
	if !reflect.DeepEqual(fitted.Stats, built.Stats) {
		t.Errorf("statistics did not survive the projection")
	}
}

// The projection copies rather than aliases: a Fitted handed to a scorer must
// not be a window onto a Profile someone else can still change.
func TestFittedDoesNotShareStorageWithTheProfile(t *testing.T) {
	built := builtProfile(t)
	fitted, err := built.Fitted()
	if err != nil {
		t.Fatalf("Fitted: %v", err)
	}
	if len(fitted.Stats) == 0 {
		t.Fatal("no statistics to alias")
	}

	before := fitted.Stats[0].Mean
	built.Stats[0].Mean = before + 1
	if fitted.Stats[0].Mean != before {
		t.Error("changing the profile changed a projection already taken from it")
	}
}

// A profile that cannot be scored against does not yield a projection that
// pretends otherwise. The manifest and set version are the compatibility
// contract, and a projection is where a mismatch would stop being visible.
func TestAProfileScoringCannotUseYieldsNoProjection(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(*profile.Profile)
	}{
		{"a unit that is not the paragraph", func(p *profile.Profile) { p.Unit = "document" }},
		{"a feature set version from another build", func(p *profile.Profile) { p.FeatureSetVersion = features.SetVersion + 1 }},
		{"a manifest digest from another build", func(p *profile.Profile) { p.FeatureManifestDigest = "0f4a2c9b" }},
		{"no statistics at all", func(p *profile.Profile) { p.Stats = nil }},
		// Non-positive, not merely zero: Build validates the floor, so one
		// arriving here at all is malformed however it got that way. Both
		// values moved here from score, where they can no longer be passed.
		{"a floor of zero", func(p *profile.Profile) { p.Requirements.MinParagraphLexicalTokens = 0 }},
		{"a negative floor", func(p *profile.Profile) { p.Requirements.MinParagraphLexicalTokens = -1 }},
		{"no identity", func(p *profile.Profile) { p.ID = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			built := builtProfile(t)
			c.damage(built)
			if _, err := built.Fitted(); err == nil {
				t.Error("projected a profile scoring cannot use")
			}
		})
	}
}

// "And nothing else" is a claim about the TYPE, so it is checked against the
// type. A provenance field added to Fitted later is a field a scorer can reach,
// which is the access this projection exists to remove — and every test above
// would still pass with one there.
func TestFittedDeclaresExactlyTheseFields(t *testing.T) {
	want := []string{
		"ID", "Unit", "FeatureSetVersion", "FeatureManifestDigest",
		"MinParagraphLexicalTokens", "Stats",
	}
	value := reflect.TypeOf(profile.Fitted{})
	got := make([]string, 0, value.NumField())
	for i := 0; i < value.NumField(); i++ {
		got = append(got, value.Field(i).Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Fitted carries %v, and scoring needs %v", got, want)
	}
}

// And a nil profile, which is not a mutation of a valid one and so is not
// covered by the table above. It matters because deviation.Standardize used to
// reject nil itself and now never sees one: this is where that rejection went.
func TestANilProfileYieldsNoProjection(t *testing.T) {
	var absent *profile.Profile
	if _, err := absent.Fitted(); err == nil {
		t.Error("projected a profile that is not there")
	}
}

// builtProfile is a real fitted profile, not a literal: the projection's whole
// job is to agree with what Build produces.
func builtProfile(t *testing.T) *profile.Profile {
	t.Helper()
	root, snapshot := corpusOf(t, map[string]string{
		"a.md": paragraph + "\nThe first one also says this.\n",
		"b.md": paragraph + "\nThe second one says something else.\n",
		"c.md": paragraph + "\nThe third one differs again.\n",
	})
	requirements := profile.DefaultRequirements()
	requirements.MinDocuments, requirements.MinParagraphs, requirements.MinObservationsPerFeature = 1, 1, 1
	built, err := profile.Build(root, snapshot, requirements)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return built
}
