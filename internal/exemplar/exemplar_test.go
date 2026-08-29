package exemplar_test

import (
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/exemplar"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
)

// Component 7 (ADR 0004): exemplars representative of the AUTHOR, never
// nearest-neighbour to the draft — retrieving segments near a failing draft
// returns the author's most AI-adjacent writing and teaches the tool the defect
// it exists to remove. Select therefore takes no draft at all.

// ---------------------------------------------------------------------------
// Counts and refusals
// ---------------------------------------------------------------------------

func TestItReturnsExactlyN(t *testing.T) {
	candidates := population(t, 40)
	for _, n := range []int{1, 3, 4} {
		got := mustSelect(t, candidates, n)
		if len(got.Exemplars) != n {
			t.Errorf("n=%d returned %d exemplars", n, len(got.Exemplars))
		}
	}
}

// N >= max(30, 10n). rewrite requires exactly n back, so returning something
// from a population too small to be representative is the harmful answer.
func TestAPopulationBelowTheFloorIsRefused(t *testing.T) {
	for _, c := range []struct {
		name    string
		size, n int
	}{
		{"below the absolute floor", 29, 1},
		{"below 10n", 39, 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := selectWith(t, testProfile(), population(t, c.size), exemplar.Config{N: c.n})
			if err == nil {
				t.Fatalf("accepted %d candidates for n=%d", c.size, c.n)
			}
			if !isErr(err, exemplar.ErrPopulationTooSmall) {
				t.Errorf("error = %v, want ErrPopulationTooSmall", err)
			}
		})
	}
}

// At the boundary it must succeed, or the floor is really a different number.
func TestTheFloorIsInclusive(t *testing.T) {
	if got := mustSelect(t, population(t, 30), 3); len(got.Exemplars) != 3 {
		t.Errorf("30 candidates and n=3 returned %d exemplars", len(got.Exemplars))
	}
}

func TestConfigAndInputsAreValidated(t *testing.T) {
	candidates := population(t, 30)
	for _, c := range []struct {
		name string
		run  func() error
		want error
	}{
		{"nil profile", func() error {
			_, err := selectWith(t, nil, candidates, exemplar.Config{N: 3})
			return err
		}, exemplar.ErrMissingInput},
		{"zero n", func() error {
			_, err := selectWith(t, testProfile(), candidates, exemplar.Config{N: 0})
			return err
		}, exemplar.ErrInvalidConfig},
		{"negative n", func() error {
			_, err := selectWith(t, testProfile(), candidates, exemplar.Config{N: -1})
			return err
		}, exemplar.ErrInvalidConfig},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.run(); !isErr(err, c.want) {
				t.Errorf("error = %v, want %v", err, c.want)
			}
		})
	}
}

// Exemplars are the author's own prose. A distractor segment reaching a prompt
// would teach the model somebody else's voice.
func TestOnlyTrainCandidatesAreAccepted(t *testing.T) {
	for _, split := range []corpus.Split{corpus.Calibrate, corpus.Test, corpus.Draft} {
		t.Run(string(split), func(t *testing.T) {
			candidates := population(t, 30)
			candidates[7].Split = split
			_, err := selectWith(t, testProfile(), candidates, exemplar.Config{N: 3})
			if !isErr(err, exemplar.ErrSplit) {
				t.Errorf("error = %v, want ErrSplit", err)
			}
		})
	}
}

func TestAManifestMismatchIsRefused(t *testing.T) {
	candidates := population(t, 30)
	candidates[3].Vector.SetVersion = features.SetVersion + 1
	_, err := selectWith(t, testProfile(), candidates, exemplar.Config{N: 3})
	if !isErr(err, exemplar.ErrManifestMismatch) {
		t.Errorf("error = %v, want ErrManifestMismatch", err)
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

func TestSelectionIsRepeatable(t *testing.T) {
	candidates := population(t, 40)
	first := mustSelect(t, candidates, 4)
	for i := 0; i < 5; i++ {
		again := mustSelect(t, candidates, 4)
		if first.ID != again.ID {
			t.Fatalf("run %d: selection ID %q != %q", i, again.ID, first.ID)
		}
		if !reflect.DeepEqual(identities(first), identities(again)) {
			t.Fatalf("run %d: %v != %v", i, identities(again), identities(first))
		}
		if first.Certificate.ID != again.Certificate.ID {
			t.Fatalf("run %d: certificate ID differs", i)
		}
	}
}

// The sharpest determinism test: Go randomizes map iteration, so a selector
// that ranks, groups or counts through a map passes the repeat test above while
// failing this one.
func TestCandidateOrderDoesNotChangeTheResult(t *testing.T) {
	candidates := population(t, 40)
	want := mustSelect(t, candidates, 4)

	for _, permute := range []struct {
		name string
		fn   func([]exemplar.Candidate)
	}{
		{"reversed", func(c []exemplar.Candidate) {
			for i, j := 0, len(c)-1; i < j; i, j = i+1, j-1 {
				c[i], c[j] = c[j], c[i]
			}
		}},
		{"rotated", func(c []exemplar.Candidate) {
			first := c[0]
			copy(c, c[1:])
			c[len(c)-1] = first
		}},
		{"sorted by text", func(c []exemplar.Candidate) {
			sort.Slice(c, func(i, j int) bool { return c[i].Text < c[j].Text })
		}},
	} {
		t.Run(permute.name, func(t *testing.T) {
			shuffled := append([]exemplar.Candidate(nil), candidates...)
			permute.fn(shuffled)
			got := mustSelect(t, shuffled, 4)
			if got.ID != want.ID {
				t.Errorf("selection ID %q != %q", got.ID, want.ID)
			}
			if !reflect.DeepEqual(identities(got), identities(want)) {
				t.Errorf("%v != %v", identities(got), identities(want))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Identity and stratum formats
// ---------------------------------------------------------------------------

// A text digest collides on duplicate leaves; the document digest plus the raw
// span does not.
func TestIdentityIsDocumentDigestAndSpan(t *testing.T) {
	c := population(t, 30)[0]
	want := c.DocumentDigest + ":" + itoa(c.Span.Offset) + ":" + itoa(c.Span.Length)
	if c.Identity() != want {
		t.Errorf("Identity() = %q, want %q", c.Identity(), want)
	}
}

func TestStratumIsRoleAndContainerPath(t *testing.T) {
	for _, c := range []struct{ source, want string }{
		{"A plain paragraph with several words in it.\n", "paragraph|document"},
		{"- A list item paragraph with several words.\n", "paragraph|document/list/list-item"},
	} {
		t.Run(c.want, func(t *testing.T) {
			got := admit(t, c.source)
			if len(got) != 1 {
				t.Fatalf("got %d candidates", len(got))
			}
			if got[0].Stratum() != c.want {
				t.Errorf("Stratum() = %q, want %q", got[0].Stratum(), c.want)
			}
		})
	}
}

func TestTheSelectionIDIsFramedIdentitiesInOrder(t *testing.T) {
	got := mustSelect(t, population(t, 40), 4)
	want := identity.HashBytes(identity.Frame(identities(got)...))
	if got.ID != want {
		t.Errorf("selection ID = %q, want %q", got.ID, want)
	}
}

// ---------------------------------------------------------------------------
// Strata and allocation
// ---------------------------------------------------------------------------

// mixed builds a population spread over three strata: plain paragraphs, list
// items and footnotes.
func mixed(t *testing.T) []exemplar.Candidate {
	t.Helper()
	var out []exemplar.Candidate
	for i, source := range prose[:12] {
		out = append(out, admit(t, source)...)
		out = append(out, admit(t, "- "+prose[12+i])...)
		out = append(out, admit(t, prose[24+i]+"[^1]\n\n[^1]: "+prose[(i+1)%12]+"\n")...)
	}
	return out
}

// Round-robin gives every stratum a slot before any stratum gets a second.
func TestEveryStratumIsRepresentedBeforeAnyRepeats(t *testing.T) {
	candidates := mixed(t)
	counts := strata(candidates)
	if len(counts) < 3 {
		t.Fatalf("the fixture yields %d strata, want at least 3: %v", len(counts), counts)
	}

	got := mustSelect(t, candidates, len(counts))
	seen := map[string]bool{}
	for _, e := range got.Exemplars {
		if seen[e.Stratum()] {
			t.Errorf("stratum %q appears twice before every stratum had one: %v", e.Stratum(), got.Exemplars)
		}
		seen[e.Stratum()] = true
	}
	if len(seen) != len(counts) {
		t.Errorf("covered %d strata of %d", len(seen), len(counts))
	}
}

// If allocation cannot reach n, refuse — never return fewer, never substitute.
func TestItRefusesRatherThanReturningFewer(t *testing.T) {
	// The population floor is satisfied, so this is about qualification rather
	// than headcount: strip nearly every candidate below the shared-feature
	// floor and too few can reach k valid neighbours.
	candidates := population(t, 40)
	for i := range candidates[:38] {
		for j := range candidates[i].Vector.Values {
			if j >= 2 {
				candidates[i].Vector.Values[j].Defined = false
			}
		}
	}
	got, err := selectWith(t, testProfile(), candidates, exemplar.Config{N: 3})
	if err == nil {
		t.Fatalf("accepted, returning %d exemplars", len(got.Exemplars))
	}
	if !isErr(err, exemplar.ErrInsufficientEligible) {
		t.Errorf("error = %v, want ErrInsufficientEligible", err)
	}
	if len(got.Exemplars) != 0 {
		t.Errorf("refused but returned %d exemplars", len(got.Exemplars))
	}
}

// ---------------------------------------------------------------------------
// Density and eligibility
// ---------------------------------------------------------------------------

func TestKFollowsTheDeclaredFormula(t *testing.T) {
	for _, c := range []struct{ size, want int }{
		{30, 5}, // floor(sqrt(30)) = 5
		{36, 6}, // exact square
		{40, 6}, // floor(sqrt(40)) = 6
	} {
		t.Run(itoa(c.size), func(t *testing.T) {
			got := mustSelect(t, population(t, c.size), 3)
			if got.Certificate.K != c.want {
				t.Errorf("k = %d for N = %d, want %d", got.Certificate.K, c.size, c.want)
			}
		})
	}
}

func TestEligibilityKeepsThreeQuartersOfTheDensest(t *testing.T) {
	got := mustSelect(t, population(t, 40), 3)
	if want := 30; len(got.Certificate.Eligible) != want { // ceil(0.75 * 40)
		t.Errorf("kept %d eligible of 40, want %d", len(got.Certificate.Eligible), want)
	}
	if len(got.Certificate.Population) != 40 {
		t.Errorf("population = %d, want 40", len(got.Certificate.Population))
	}

	// The dropped quarter must be the SPARSEST, or eligibility is inverted.
	density := map[string]float64{}
	for _, d := range got.Certificate.Density {
		density[d.Identity] = d.Value
	}
	eligible := map[string]bool{}
	for _, id := range got.Certificate.Eligible {
		eligible[id] = true
	}
	var worstKept, bestDropped = math.Inf(-1), math.Inf(1)
	for id, value := range density {
		if eligible[id] && value > worstKept {
			worstKept = value
		}
		if !eligible[id] && value < bestDropped {
			bestDropped = value
		}
	}
	if worstKept > bestDropped {
		t.Errorf("a denser candidate was dropped (%v) than one kept (%v); eligibility is inverted", bestDropped, worstKept)
	}
}

// Every exemplar comes from the eligible set, never from the dropped tail.
func TestExemplarsAreDrawnFromTheEligibleSet(t *testing.T) {
	got := mustSelect(t, population(t, 40), 4)
	eligible := map[string]bool{}
	for _, id := range got.Certificate.Eligible {
		eligible[id] = true
	}
	for _, e := range got.Exemplars {
		if !eligible[e.Identity()] {
			t.Errorf("exemplar %q is not in the eligible set", e.Identity())
		}
	}
}

// A pair sharing too few defined features is not a neighbour: two segments
// agreeing on three features must not look closer than two agreeing on thirty.
func TestAPairBelowTheSharedFeatureFloorIsNotValid(t *testing.T) {
	candidates := population(t, 40)
	// Undefine all but two features on one candidate; it can no longer reach
	// the shared-feature floor with anybody, so it has no valid neighbours.
	stripped := &candidates[5]
	for i := range stripped.Vector.Values {
		if i >= 2 {
			stripped.Vector.Values[i].Defined = false
		}
	}
	got := mustSelect(t, candidates, 3)

	for _, d := range got.Certificate.Density {
		if d.Identity == stripped.Identity() {
			if d.Neighbours != 0 {
				t.Errorf("the stripped candidate has %d valid neighbours, want 0", d.Neighbours)
			}
			if d.Eligible {
				t.Error("the stripped candidate is eligible despite having no valid neighbours")
			}
			return
		}
	}
	t.Error("the stripped candidate is absent from the density record")
}

// ---------------------------------------------------------------------------
// Ties
// ---------------------------------------------------------------------------

// The verified-varied population has no equal comparisons, so the tie list must
// be EMPTY — not merely well-formed. A selector recording a tie per comparison
// would pass a prefix check.
func TestAVariedPopulationRecordsNoTies(t *testing.T) {
	got := mustSelect(t, population(t, 40), 4)
	if len(got.Certificate.Ties) != 0 {
		t.Errorf("recorded %d ties on a population with no equal comparisons: %v",
			len(got.Certificate.Ties), got.Certificate.Ties)
	}
}

// Identity must be unique: it breaks every tie in this procedure, so a
// population naming one twice has no tie-break at all.
func TestADuplicateIdentityIsRefused(t *testing.T) {
	candidates := population(t, 40)
	candidates[7].DocumentDigest = candidates[3].DocumentDigest
	candidates[7].Span = candidates[3].Span
	_, err := selectWith(t, testProfile(), candidates, exemplar.Config{N: 3})
	if !isErr(err, exemplar.ErrDuplicateIdentity) {
		t.Errorf("error = %v, want ErrDuplicateIdentity", err)
	}
}

// ---------------------------------------------------------------------------
// The certificate
// ---------------------------------------------------------------------------

// It binds candidates to strata rather than only counting them, or it is not
// evidence of the allocation it claims to record.
func TestTheCertificateBindsEveryCandidateToItsStratum(t *testing.T) {
	candidates := mixed(t)
	got := mustSelect(t, candidates, 3)

	if len(got.Certificate.Strata) != len(candidates) {
		t.Fatalf("certificate records %d stratum assignments for %d candidates",
			len(got.Certificate.Strata), len(candidates))
	}
	want := map[string]string{}
	for _, c := range candidates {
		want[c.Identity()] = c.Stratum()
	}
	for _, entry := range got.Certificate.Strata {
		if want[entry.Identity] != entry.Stratum {
			t.Errorf("%s assigned to %q, want %q", entry.Identity, entry.Stratum, want[entry.Identity])
		}
	}
}

// Every declared input changes the ID, or the cache can serve a stale selection.
func TestTheCertificateIDCoversItsInputs(t *testing.T) {
	candidates := population(t, 40)
	base := mustSelect(t, candidates, 3)

	changed := func(name string, mutate func([]exemplar.Candidate) exemplar.Selection) {
		t.Helper()
		next := mutate(append([]exemplar.Candidate(nil), candidates...))
		if next.Certificate.ID == base.Certificate.ID {
			t.Errorf("%s did not change the certificate ID", name)
		}
	}

	changed("n", func(c []exemplar.Candidate) exemplar.Selection { return mustSelect(t, c, 4) })
	changed("a document digest", func(c []exemplar.Candidate) exemplar.Selection {
		c[0].DocumentDigest = identity.HashBytes([]byte("a different document"))
		return mustSelect(t, c, 3)
	})
	changed("a span", func(c []exemplar.Candidate) exemplar.Selection {
		c[1].Span = text.Span{Offset: c[1].Span.Offset + 1, Length: c[1].Span.Length}
		return mustSelect(t, c, 3)
	})
	changed("the population", func(c []exemplar.Candidate) exemplar.Selection {
		return mustSelect(t, c[:39], 3)
	})
}

// The selection ID is one of the certificate's inputs, so a different selection
// is a different certificate even if nothing else moved.
func TestTheCertificateCarriesTheSelectionID(t *testing.T) {
	got := mustSelect(t, population(t, 40), 3)
	if got.Certificate.SelectionID != got.ID {
		t.Errorf("certificate selection ID %q != selection ID %q", got.Certificate.SelectionID, got.ID)
	}
}

// ---------------------------------------------------------------------------
// What comes out
// ---------------------------------------------------------------------------

func TestExemplarsCarryTheirRunTextAndAreDistinct(t *testing.T) {
	got := mustSelect(t, population(t, 40), 4)
	seen := map[string]bool{}
	for _, e := range got.Exemplars {
		if strings.TrimSpace(e.Text) == "" {
			t.Errorf("exemplar %s carries no text", e.Identity())
		}
		if seen[e.Identity()] {
			t.Errorf("exemplar %s selected twice", e.Identity())
		}
		seen[e.Identity()] = true
	}
}

func TestDefaultConfigMatchesRewrite(t *testing.T) {
	if got := exemplar.DefaultConfig().N; got != 3 {
		t.Errorf("DefaultConfig().N = %d, want 3 to match rewrite.DefaultOptions()", got)
	}
}
