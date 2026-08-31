package deviation_test

import (
	"errors"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/profile"
)

// DESIGN says deviation owns the reference minimum. It owned the CHECK and not
// the number: BuildReference takes minSegments from its caller and the package
// declares no default, so the first caller to build a reference would have had
// to invent one, and the second would have invented a different one. The
// declaration is the point — the value it happens to hold is deviation's to
// change.
func TestTheReferenceMinimumIsDeclaredByItsOwner(t *testing.T) {
	if got := deviation.DefaultMinSegments(); got <= 0 {
		t.Fatalf("DefaultMinSegments() = %d; a minimum that admits everything is not a minimum", got)
	}
}

// And the declared minimum is the one BuildReference actually enforces, so the
// constant cannot drift away from the check that uses it.
func TestTheDeclaredMinimumIsTheOneEnforced(t *testing.T) {
	minimum := deviation.DefaultMinSegments()
	built, segments := referenceOverSegments(t, minimum)
	if segments != minimum {
		t.Fatalf("built over %d segments, wanted exactly the minimum %d", segments, minimum)
	}
	if built == nil {
		t.Fatal("exactly the minimum was refused")
	}

	one, _ := referenceOverSegments(t, minimum-1)
	if one != nil {
		t.Errorf("one segment below the declared minimum was accepted")
	}
}

// referenceOverSegments builds a reference from exactly n calibrate segments,
// reporting how many it actually had so a fixture that quietly produced a
// different number cannot make the assertion above vacuous.
func referenceOverSegments(t *testing.T, n int) (*deviation.Reference, int) {
	t.Helper()
	built, standardizations := calibrateSegments(t, n)
	reference, err := deviation.BuildReference(built, corpus.Calibrate, standardizations, deviation.DefaultMinSegments())
	if err != nil {
		if errors.Is(err, deviation.ErrReferenceTooSmall) {
			return nil, len(standardizations)
		}
		t.Fatalf("BuildReference over %d segments: %v", n, err)
	}
	return reference, len(standardizations)
}

// calibrateSegments returns a profile and exactly n Calibrate standardizations
// over it, every manifest feature defined so nothing but the count is at issue.
func calibrateSegments(t *testing.T, n int) (*profile.Profile, []deviation.Standardization) {
	t.Helper()
	built := profileWith(stat(features.FunctionWordRate, 0.4, 0.01))
	segments := make([]deviation.Standardization, 0, n)
	for i := 0; i < n; i++ {
		values := make([]deviation.Standardized, 0, len(features.Definitions()))
		for _, definition := range features.Definitions() {
			values = append(values, deviation.Standardized{
				Feature: definition.ID, Value: float64(i%7) - 3, Defined: true,
			})
		}
		segments = append(segments, deviation.Standardization{
			ProfileID: built.ID, FeatureManifestDigest: built.FeatureManifestDigest,
			Split: corpus.Calibrate, LexicalTokens: 50, Values: values,
		})
	}
	return built, segments
}
