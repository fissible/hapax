package exemplar_test

import (
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"

	"github.com/fissible/hapax/internal/exemplar"
)

// Expected values below are computed by an independent Python implementation of
// the procedure in docs/DESIGN.md — standardization, the self-referential
// mid-rank/probit transform, valid-pair Delta, k-nearest density, the 75%
// eligibility cut and round-robin stratum medoids — never read back from Go.
// Consistency between certificate fields proves nothing; these pin the numbers.

// expect is everything a pinned fixture asserts about one selection.
type expect struct {
	k, eligible int
	ids         []string
	sums        []float64
	rounds      []int
}

// snapshot records the input identities BEFORE Select runs, so a selector that
// reorders or mutates the caller's slice cannot make the expectation follow it.
func snapshot(candidates []exemplar.Candidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Identity())
	}
	sort.Strings(out)
	return out
}

func assertSelection(t *testing.T, got exemplar.Selection, input []string, want expect) {
	t.Helper()
	if got.Certificate.K != want.k {
		t.Errorf("k = %d, want %d", got.Certificate.K, want.k)
	}
	if len(got.Certificate.Eligible) != want.eligible {
		t.Errorf("eligible = %d, want %d", len(got.Certificate.Eligible), want.eligible)
	}
	if !reflect.DeepEqual(identities(got), want.ids) {
		t.Errorf("selection =\n%#v\nwant\n%#v", identities(got), want.ids)
	}

	// Every certificate section is bound to the INPUT, not merely to the other
	// sections: a duplicated row cannot stand in for an omitted candidate.
	inDensity := make([]string, 0, len(got.Certificate.Density))
	for _, d := range got.Certificate.Density {
		inDensity = append(inDensity, d.Identity)
	}
	inStrata := make([]string, 0, len(got.Certificate.Strata))
	for _, entry := range got.Certificate.Strata {
		inStrata = append(inStrata, entry.Identity)
	}
	for _, section := range []struct {
		name string
		got  []string
	}{{"population", got.Certificate.Population}, {"density", inDensity}, {"strata", inStrata}} {
		if !reflect.DeepEqual(section.got, input) {
			t.Errorf("%s is not the input population in canonical order:\n%v\nwant\n%v", section.name, section.got, input)
		}
	}

	listed := map[string]bool{}
	for _, id := range got.Certificate.Eligible {
		if listed[id] {
			t.Errorf("eligible lists %s twice", id)
		}
		listed[id] = true
	}
	if len(listed) != want.eligible {
		t.Errorf("eligible has %d distinct identities for %d entries", len(listed), want.eligible)
	}
	for _, d := range got.Certificate.Density {
		if d.Eligible != listed[d.Identity] {
			t.Errorf("%s: density row says eligible=%v, the eligible list says %v",
				d.Identity, d.Eligible, listed[d.Identity])
		}
		delete(listed, d.Identity)
	}
	for id := range listed {
		t.Errorf("eligible lists %s, which has no density row", id)
	}

	if len(got.Certificate.Medoids) != len(want.sums) {
		t.Fatalf("%d medoid records, want %d", len(got.Certificate.Medoids), len(want.sums))
	}
	rounds := make([]int, 0, len(got.Certificate.Medoids))
	for i, row := range got.Certificate.Medoids {
		if diff := math.Abs(row.Sum - want.sums[i]); diff > 1e-6 {
			t.Errorf("medoid %d sum = %.12f, want %.12f (diff %g)", i, row.Sum, want.sums[i], diff)
		}
		if i < len(got.Exemplars) {
			if row.Identity != got.Exemplars[i].Identity() {
				t.Errorf("medoid row %d names %s, exemplar %d is %s", i, row.Identity, i, got.Exemplars[i].Identity())
			}
			if row.Stratum != got.Exemplars[i].Stratum() {
				t.Errorf("medoid row %d stratum %q, exemplar stratum %q", i, row.Stratum, got.Exemplars[i].Stratum())
			}
		}
		rounds = append(rounds, row.Round)
	}
	if !reflect.DeepEqual(rounds, want.rounds) {
		t.Errorf("rounds = %v, want %v", rounds, want.rounds)
	}
}

// One stratum, so this isolates the transform, the density and the medoid rule
// from allocation. A selector taking the first eligible candidate per stratum
// fails here.
func TestASingleStratumMatchesTheOracle(t *testing.T) {
	candidates := population(t, 40)
	before := snapshot(candidates)
	assertSelection(t, mustSelect(t, candidates, 4), before, expect{k: 6, eligible: 30,
		ids: []string{
			"b7ba960b1d8f90235eb154061009fe43e1911c03739c778611e97c3309e0d5f0:0:68",
			"2c9c5a6620b2c5652b77ab2237b0d488aa72f705a3c80a85cbcfbb19581ee4e9:0:92",
			"b875ec308c98dea01481c05bf52c6f5989fde533d2a57d14772fd298f78b1f7e:0:83",
			"b17b148020b79942b7aa541e4ad8b59bf1aa4142217693bec24d6fb333d5f4b5:0:88",
		},
		sums:   []float64{13.535588920810, 13.327373266001, 13.403895304123, 13.097115394411},
		rounds: []int{1, 2, 3, 4}})
}

// The medoid is recomputed against each round's REMAINING candidates: the sums
// above are not monotonic, which a one-shot ranking would make them.
func TestTheMedoidIsRecomputedEachRound(t *testing.T) {
	got := mustSelect(t, population(t, 40), 4)
	sums := make([]float64, 0, len(got.Certificate.Medoids))
	for _, m := range got.Certificate.Medoids {
		sums = append(sums, m.Sum)
	}
	if len(sums) < 3 {
		t.Fatalf("got %d medoid records", len(sums))
	}
	if sums[1] <= sums[0] && sums[2] <= sums[1] {
		t.Errorf("medoid sums %v are monotonically decreasing; the domain was not reduced between rounds", sums)
	}
}

// N = 30 exercises a non-integral eligibility cut: ceil(0.75 x 30) = 23, and
// k = floor(sqrt(30)) = 5.
func TestTheEligibilityCutRoundsUp(t *testing.T) {
	candidates := population(t, 30)
	before := snapshot(candidates)
	assertSelection(t, mustSelect(t, candidates, 3), before, expect{k: 5, eligible: 23,
		ids: []string{
			"2c9c5a6620b2c5652b77ab2237b0d488aa72f705a3c80a85cbcfbb19581ee4e9:0:92",
			"4978c3d73171bc58e5495f7d082a8696d8397da949d93fd71ad03620960fdfd3:0:83",
			"b875ec308c98dea01481c05bf52c6f5989fde533d2a57d14772fd298f78b1f7e:0:83",
		},
		sums:   []float64{10.862204062772, 10.810235153496, 10.308390460580},
		rounds: []int{1, 2, 3}})
}

// Three strata, one slot each: this is allocation, and the identities only come
// out right if the transform and medoid rule underneath are also right.
func TestThreeStrataMatchTheOracle(t *testing.T) {
	candidates := mixed(t)
	before := snapshot(candidates)
	assertSelection(t, mustSelect(t, candidates, 3), before, expect{k: 6, eligible: 36,
		ids: []string{
			"2de5f50b8eb5a0ad5b1e0cae8dcc2a5dc447b73bec80c000e51add9735068441:0:72",
			"096935f717e5bfdd680b5c12628739e0f73d765aa8a2c202563a1b69b4ac6274:2:83",
			"00062267705b0febacbed30c352321e82bd32c643c90d7e97906e6c21cd71f2c:89:84",
		},
		sums:   []float64{7.949879121646, 4.269278964821, 3.453271450791},
		rounds: []int{1, 1, 1}})
}

// THE discriminator for eligible-count ordering. In this population the two
// orderings disagree:
//
//	by population: paragraph|document (24), footnote (12), list-item (12)
//	by eligible:   paragraph|document (18), list-item (10), footnote (8)
//
// so at n=2 an implementation that ranks strata before applying eligibility
// picks the footnote and fails.
func TestStrataAreOrderedByEligibleCountNotPopulation(t *testing.T) {
	got := mustSelect(t, mixed(t), 2)
	want := []string{"paragraph|document", "paragraph|document/list/list-item"}
	strata := []string{got.Exemplars[0].Stratum(), got.Exemplars[1].Stratum()}
	if !reflect.DeepEqual(strata, want) {
		t.Errorf("strata %v, want %v — ordered by population rather than eligible count", strata, want)
	}
}

// Round two returns to the largest stratum rather than stopping when every
// stratum has had one.
func TestAllocationReturnsToTheLargestStratumInRoundTwo(t *testing.T) {
	got := mustSelect(t, mixed(t), 4)
	if len(got.Certificate.Medoids) != 4 {
		t.Fatalf("%d medoid records, want 4", len(got.Certificate.Medoids))
	}
	rounds := []int{}
	strata := []string{}
	for _, m := range got.Certificate.Medoids {
		rounds = append(rounds, m.Round)
		strata = append(strata, m.Stratum)
	}
	if want := []int{1, 1, 1, 2}; !reflect.DeepEqual(rounds, want) {
		t.Errorf("rounds %v, want %v", rounds, want)
	}
	if strata[3] != "paragraph|document" {
		t.Errorf("round two went to %q, want the largest stratum", strata[3])
	}
	if got.Exemplars[3].Identity() != "1863efd043be7ce69d38457db10bed956d6b14c11e7c64804a0e9246907eb5fe:0:96" {
		t.Errorf("round-two pick = %q", got.Exemplars[3].Identity())
	}
}

// ---------------------------------------------------------------------------
// The certificate is the declared function of its declared fields
// ---------------------------------------------------------------------------

// Sensitivity is not enough: a hash of unrelated inputs is also sensitive. This
// recomputes the ID from the eight declared keys with their declared framing.
func TestTheCertificateIDIsTheDeclaredHash(t *testing.T) {
	got := mustSelect(t, population(t, 40), 4)
	c := got.Certificate

	density := make([]string, 0, len(c.Density))
	for _, d := range c.Density {
		density = append(density, d.Identity+"="+numberID(d.Value))
	}
	strata := make([]string, 0, len(c.Strata))
	for _, s := range c.Strata {
		strata = append(strata, s.Identity+"="+s.Stratum)
	}
	medoids := make([]string, 0, len(c.Medoids))
	for _, m := range c.Medoids {
		medoids = append(medoids, itoa(m.Round)+":"+m.Stratum+":"+m.Identity+":"+numberID(m.Sum))
	}
	config := []string{
		"n=" + itoa(c.Config.N), "k=" + itoa(c.K),
		"min-population-absolute=30", "min-population-multiple=10",
		"shared-feature-fraction=0.5", "k-min=3", "k-max=15",
		"eligibility-fraction=0.75",
	}
	binding := []string{
		"profile=" + testProfile().ID,
		"text=" + text.ContractVersion,
		"structure=" + text.StructureVersion,
		"manifest=" + features.ManifestDigest(),
	}
	want := identity.HashInputs(map[string]string{
		"binding":    string(identity.Frame(binding...)),
		"selection":  c.SelectionID,
		"population": string(identity.Frame(c.Population...)),
		"eligible":   string(identity.Frame(c.Eligible...)),
		"density":    string(identity.Frame(density...)),
		"strata":     string(identity.Frame(strata...)),
		"medoids":    string(identity.Frame(medoids...)),
		"ties":       string(identity.Frame(c.Ties...)),
		"config":     string(identity.Frame(config...)),
	})
	if c.ID != want {
		t.Errorf("certificate ID = %q, want %q", c.ID, want)
	}
}

// The certificate must describe exactly the population, with the density values
// that were actually computed. Counting rows and checking order lets a
// duplicate stand in for an omission, and lets invented evidence hash cleanly.
func TestTheCertificateDescribesExactlyThePopulation(t *testing.T) {
	candidates := population(t, 40)
	got := mustSelect(t, candidates, 4)
	c := got.Certificate

	want := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		want = append(want, candidate.Identity())
	}
	sort.Strings(want)

	density := make([]string, 0, len(c.Density))
	for _, d := range c.Density {
		density = append(density, d.Identity)
	}
	strata := make([]string, 0, len(c.Strata))
	for _, entry := range c.Strata {
		strata = append(strata, entry.Identity)
	}

	// Compared UNSORTED against the sorted expectation: sorting first would let
	// a non-canonical emission order pass.
	for _, section := range []struct {
		name string
		got  []string
	}{{"population", c.Population}, {"density", density}, {"strata", strata}} {
		if !reflect.DeepEqual(section.got, want) {
			t.Errorf("%s is not exactly the population in canonical order:\n%v\nwant\n%v", section.name, section.got, want)
		}
	}
	if !sort.StringsAreSorted(c.Eligible) {
		t.Errorf("eligible is not in canonical order")
	}
	inPopulation := map[string]bool{}
	for _, id := range want {
		inPopulation[id] = true
	}
	for _, id := range c.Eligible {
		if !inPopulation[id] {
			t.Errorf("eligible contains %s, which is not in the population", id)
		}
	}
}

// The density values themselves, from the oracle. Without these the certificate
// can carry any monotonic sequence and still pass every structural check.
func TestDensityValuesMatchTheOracle(t *testing.T) {
	got := mustSelect(t, population(t, 40), 4)
	byIdentity := map[string]exemplar.Density{}
	for _, d := range got.Certificate.Density {
		byIdentity[d.Identity] = d
	}
	for _, want := range []struct {
		identity string
		value    float64
	}{
		{"236e25e276e5c39b286f0288f9a84125e1381919b5ef00668c7196f9fa1e298e:0:85", 0.182205694187},
		{"467b1a24055681751cd05b33d4c876749f65d600a46b767c3b8cf37cadc6d061:0:83", 0.189154136119},
		{"4978c3d73171bc58e5495f7d082a8696d8397da949d93fd71ad03620960fdfd3:0:83", 0.194985135656},
		{"8d2a37d087a481e7b0a8d89887d0870c7daab6872e74932a1cfa8e2464455c47:0:89", 0.605755483596},
		{"fcddd07b4d4ee7f41f0d5a8df0d80aa25a56088c22cf97e5c7ccae0b4184879a:0:156", 0.611337233426},
		{"0a7ab968f34a157e5032cc6d20855adf9422c610740a1aa1369543d63c7dc77a:0:95", 0.710714699768},
	} {
		row, ok := byIdentity[want.identity]
		if !ok {
			t.Errorf("no density row for %s", want.identity)
			continue
		}
		if diff := math.Abs(row.Value - want.value); diff > 1e-6 {
			t.Errorf("%s density = %.12f, want %.12f", want.identity, row.Value, want.value)
		}
		if row.Neighbours != 39 {
			t.Errorf("%s has %d valid neighbours, want 39", want.identity, row.Neighbours)
		}
	}
}

// k caps at 15 however large the population grows.
func TestKCapsAtFifteen(t *testing.T) {
	got := mustSelect(t, largePopulation(t, 240), 3)
	if got.Certificate.K != 15 {
		t.Errorf("k = %d for N = 240, want the cap 15", got.Certificate.K)
	}
}

// A candidate with some valid neighbours but fewer than k is ineligible; "has
// any valid neighbour" is the easy wrong reading.
func TestFewerThanKValidNeighboursIsIneligible(t *testing.T) {
	candidates := population(t, 40)
	// Split the manifest: the subject defines the first three features, most of
	// the population defines only the last three, so they share nothing. Four
	// candidates keep every feature and are the subject's only valid pairs.
	subject := &candidates[0]
	defineOnly(subject, 0, 1, 2)
	for i := 5; i < len(candidates); i++ {
		defineOnly(&candidates[i], 3, 4, 5)
	}

	got := mustSelect(t, candidates, 3)
	for _, d := range got.Certificate.Density {
		if d.Identity != subject.Identity() {
			continue
		}
		if d.Neighbours == 0 || d.Neighbours >= got.Certificate.K {
			t.Fatalf("subject has %d valid neighbours and k is %d; the fixture is not testing 1..k-1",
				d.Neighbours, got.Certificate.K)
		}
		if d.Eligible {
			t.Errorf("a candidate with %d valid neighbours is eligible, k is %d", d.Neighbours, got.Certificate.K)
		}
		return
	}
	t.Error("the subject is absent from the density record")
}

// The cut is 75% of the POPULATION, not of the candidates that qualify. Here 32
// of 40 qualify: the contract keeps ceil(0.75 x 40) = 30, not ceil(0.75 x 32).
func TestTheCutIsThreeQuartersOfThePopulationNotOfQualifiers(t *testing.T) {
	candidates := population(t, 40)
	for i := 0; i < 8; i++ {
		defineOnly(&candidates[i], 0, 1)
	}
	got := mustSelect(t, candidates, 3)
	if len(got.Certificate.Eligible) != 30 {
		t.Errorf("kept %d eligible with 32 qualifiers, want 30 (0.75 of the population, not of the qualifiers)",
			len(got.Certificate.Eligible))
	}
}

// The k-cap fixture must be 240 distinct candidates, or the cap is being read
// off a smaller population than the test claims.
func TestKCapFixtureIsWhatItClaims(t *testing.T) {
	got := mustSelect(t, largePopulation(t, 240), 3)
	if len(got.Certificate.Population) != 240 {
		t.Fatalf("certificate accounts for %d candidates, want 240", len(got.Certificate.Population))
	}
	seen := map[string]bool{}
	for _, id := range got.Certificate.Population {
		if seen[id] {
			t.Errorf("duplicate identity %s in a population of supposedly distinct candidates", id)
		}
		seen[id] = true
	}
}

// The stratum-order tie site, which nothing else exercises. Verified with the
// oracle: both strata hold exactly 15 eligible candidates, so the order is
// decided by key ascending and "paragraph|document" sorts before
// "paragraph|document/list/list-item".
func TestEqualStrataAreOrderedByKeyAndTheTieIsRecorded(t *testing.T) {
	got := mustSelect(t, twinStrata(t), 2)

	counts := map[string]int{}
	for _, id := range got.Certificate.Eligible {
		for _, c := range twinStrata(t) {
			if c.Identity() == id {
				counts[c.Stratum()]++
			}
		}
	}
	if len(counts) != 2 || counts["paragraph|document"] != counts["paragraph|document/list/list-item"] {
		t.Fatalf("the fixture no longer ties: %v", counts)
	}

	want := []string{"paragraph|document", "paragraph|document/list/list-item"}
	if strata := []string{got.Exemplars[0].Stratum(), got.Exemplars[1].Stratum()}; !reflect.DeepEqual(strata, want) {
		t.Errorf("strata %v, want %v", strata, want)
	}

	var found bool
	for _, tie := range got.Certificate.Ties {
		if !strings.HasPrefix(tie, "stratum-order:") {
			continue
		}
		found = true
		if !strings.HasSuffix(tie, "paragraph|document") {
			t.Errorf("stratum-order tie %q was not won by the lower key", tie)
		}
	}
	if !found {
		t.Errorf("two strata of equal size recorded no stratum-order tie: %v", got.Certificate.Ties)
	}
}

// ---------------------------------------------------------------------------
// The whole evidence record, pinned
// ---------------------------------------------------------------------------

// Six pinned density values leave thirty-four unpinned, and a selector can get
// the exemplars right while fabricating the rest. These digests are computed by
// the Python oracle over every row, so nothing is left unchecked.
func TestTheWholeEvidenceRecordMatchesTheOracle(t *testing.T) {
	for _, c := range []struct {
		name          string
		candidates    []exemplar.Candidate
		n             int
		density, elig string
		ties          []string
	}{
		{"plain40", population(t, 40), 4,
			"92d3fff9b50b8899200170f52861dc1f86c49584d0baa17f9481d4c74500a53d",
			"4b3367aa69e1273f9d4677ebc7a977282f22e9b0f0f78942b6dcb5ddd1158113", nil},
		{"mixed", mixed(t), 3,
			"075be94cc6ef044f1214f5b31efef477d974b9a93243fb4a475718dcda8fc6df",
			"5d704151ce74134753e4d8ec519b77c2a3bb6758869ceb431a6641e4244ec537", nil},
		{"twinStrata", twinStrata(t), 2,
			"b122e8844bbf3b03ef2e748510e260a5e7072a209016bd11f2d62e83ba3cba88",
			"f8d308e79a496df9c59d32e2a764bdfb7b5687661df0d3048c5f1e6ded618b81",
			[]string{"stratum-order:-:paragraph|document"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := mustSelect(t, c.candidates, c.n)

			rows := make([]string, 0, len(got.Certificate.Density))
			for _, d := range got.Certificate.Density {
				value := "-"
				if d.Neighbours >= got.Certificate.K {
					value = numberID(d.Value)
				}
				eligible := "0"
				if d.Eligible {
					eligible = "1"
				}
				rows = append(rows, d.Identity+"="+value+":"+itoa(d.Neighbours)+":"+eligible)
			}
			if digest := identity.HashBytes(identity.Frame(rows...)); digest != c.density {
				t.Errorf("density record digest = %s, want %s", digest, c.density)
			}
			if digest := identity.HashBytes(identity.Frame(got.Certificate.Eligible...)); digest != c.elig {
				t.Errorf("eligible digest = %s, want %s", digest, c.elig)
			}

			// The exact tie sequence, not a syntactic check: the oracle says
			// these populations produce these ties and no others.
			want := c.ties
			if want == nil {
				want = []string{}
			}
			have := got.Certificate.Ties
			if have == nil {
				have = []string{}
			}
			if !reflect.DeepEqual(have, want) {
				t.Errorf("ties = %v, want %v", have, want)
			}
		})
	}
}

// Every stratum assignment, with its value, on the multi-stratum fixture.
func TestStratumAssignmentsAreExactOnAMixedPopulation(t *testing.T) {
	candidates := mixed(t)
	// Built before the call, so a mutated role or container path on an
	// unselected candidate cannot make a wrong assignment look right.
	want := make([]exemplar.StratumAssignment, 0, len(candidates))
	for _, c := range candidates {
		want = append(want, exemplar.StratumAssignment{Identity: c.Identity(), Stratum: c.Stratum()})
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Identity < want[j].Identity })

	got := mustSelect(t, candidates, 3)

	if !reflect.DeepEqual(got.Certificate.Strata, want) {
		t.Errorf("stratum assignments differ from the population's own:\n%v\nwant\n%v", got.Certificate.Strata, want)
	}
}

// An exemplar must BE its input candidate, not a reconstruction carrying the
// right identity with altered text, structure, split or vector.
func TestExemplarsAreTheInputCandidates(t *testing.T) {
	candidates := mixed(t)
	byIdentity := map[string]exemplar.Candidate{}
	for _, c := range candidates {
		byIdentity[c.Identity()] = c
	}
	for _, e := range mustSelect(t, candidates, 3).Exemplars {
		original, ok := byIdentity[e.Identity()]
		if !ok {
			t.Errorf("exemplar %s is not one of the candidates", e.Identity())
			continue
		}
		if !reflect.DeepEqual(e, original) {
			t.Errorf("exemplar %s differs from its input candidate:\n%#v\nwant\n%#v", e.Identity(), e, original)
		}
	}
}

// The remaining two tie sites, each with an independently computed fixture.
func TestEligibilityAndMedoidTiesAreRecorded(t *testing.T) {
	t.Run("eligibility", func(t *testing.T) {
		// 19 twin pairs: densities come in equal pairs and ceil(0.75 x 38) = 29
		// is odd, so the cut lands inside one of them.
		got := mustSelect(t, twinPairs(t, 19), 2)
		want := []string{"eligibility:-:64f6d66180be8f81244771a90bac169c1c6e49522319dfa997caac5105ed6a4b:0:82"}
		if !reflect.DeepEqual(got.Certificate.Ties, want) {
			t.Errorf("ties = %v, want %v", got.Certificate.Ties, want)
		}
	})

	t.Run("medoid", func(t *testing.T) {
		// A stratum of exactly two: each member's intra-stratum sum is the one
		// distance between them, so they always tie.
		candidates := population(t, 38)
		candidates = append(candidates, admit(t, "- "+prose[38])...)
		candidates = append(candidates, admit(t, "- "+prose[39])...)

		got := mustSelect(t, candidates, 2)
		want := []string{"medoid:1:c4900f354c9b73b74d65c7aa6e4ea2d4839fdf9e28eef74ed5985585116d2b9b:2:86"}
		if !reflect.DeepEqual(got.Certificate.Ties, want) {
			t.Errorf("ties = %v, want %v", got.Certificate.Ties, want)
		}
	})
}

// The certificate must bind the profile and the parsers that produced its
// candidates, or a cache keyed on it serves a selection fitted to another
// author. Only the ID changes: the selection itself is unaffected.
func TestTheCertificateBindsTheProfileAndContractVersions(t *testing.T) {
	candidates := population(t, 40)
	base := mustSelect(t, candidates, 3)

	other := testProfile()
	other.ID = "a-different-profile"
	got, err := selectWith(t, other, candidates, exemplar.Config{N: 3})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Certificate.ID == base.Certificate.ID {
		t.Error("a different profile ID produced the same certificate ID")
	}
	if got.ID != base.ID {
		t.Errorf("a different profile ID changed the selection itself: %q != %q", got.ID, base.ID)
	}
}

// The profile is not decoration, and NEITHER statistic is absorbed by the rank
// transform: standardization's denominator is per candidate, so subtracting the
// mean and dividing by the variance are not maps shared across the population.
// Measured: mean 50 and variance 1e-6 each change the selection.
func TestTheProfileStatisticsChangeTheSelection(t *testing.T) {
	candidates := population(t, 40)
	before := snapshot(candidates)
	base := mustSelect(t, candidates, 3)

	for _, c := range []struct {
		name string
		// sameSelection marks the subtlest case: the exemplars are unchanged
		// but the evidence behind them is not, so an implementation ignoring
		// the statistic returns the right answer with the wrong working.
		sameSelection bool
		profile       *profile.Profile
		want          []string
		sums          []float64
		digest        string
	}{
		{name: "mean 1.5, same selection", sameSelection: true, profile: meanProfile(1.5),
			want: []string{
				"b7ba960b1d8f90235eb154061009fe43e1911c03739c778611e97c3309e0d5f0:0:68",
				"2c9c5a6620b2c5652b77ab2237b0d488aa72f705a3c80a85cbcfbb19581ee4e9:0:92",
				"b875ec308c98dea01481c05bf52c6f5989fde533d2a57d14772fd298f78b1f7e:0:83",
			},
			sums:   []float64{13.641518791849, 13.381160558697, 13.318353094047},
			digest: "e2d2b6c41b7cf69028c798d8259644ebd4d5f69653ab3e785372a5136c4f1ad1"},
		{name: "mean 5", profile: meanProfile(5),
			want: []string{
				"b17b148020b79942b7aa541e4ad8b59bf1aa4142217693bec24d6fb333d5f4b5:0:88",
				"b7ba960b1d8f90235eb154061009fe43e1911c03739c778611e97c3309e0d5f0:0:68",
				"1692a13e1c83a33a82bc71ac4b456cc47fc0b466abc13432f1caf7a199e15724:0:38",
			},
			sums:   []float64{13.456315302098, 13.466382064510, 13.178389797754},
			digest: "cc6911f75610e3578cbd89952ed423df1342829be3fafc2ed57d311920488e80"},
		{name: "mean 50", profile: meanProfile(50),
			want: []string{
				"b17b148020b79942b7aa541e4ad8b59bf1aa4142217693bec24d6fb333d5f4b5:0:88",
				"d236d49bbc7f4b02becb7243bb545862a4edb73b7af2172b6a920a8b6fdc8de8:0:95",
				"29fc5c91ae2f3838fa61012ca1b9134e92df095fbc5e9c5045fa96b80224fb24:0:87",
			},
			sums:   []float64{13.330644445456, 13.587651227760, 13.583856959508},
			digest: "cf917d4697f5b936cce29902201f2a2dd6c95dee7bfe7d16fdce4cfa88540841"},
		{name: "per-feature stats", profile: heterogeneousProfile(),
			want: []string{
				"bc69218fa265b4dde77db8142fd0aa9a298ba0fb82b6a4acf9fd17ad489d2d18:0:82",
				"372166e31641c32e8be845b4da5acae1666aea9f0c4ec8e98c2d2d2a2937c964:0:77",
				"9bd62527991ab14f7137a4f416a77872a06b13fd17272041f1154089569a0c3a:0:82",
			},
			sums:   []float64{12.620224447318, 13.591882816135, 13.339269627243},
			digest: "8d5091d84013e3826cf9f7b628c28e44036bd386a29c141656c58437f5f9df43"},
		{name: "variance", profile: narrowProfile(),
			want: []string{
				"1780a20bb836810aa7df7bcf2d526a6881d37cf90a07e8cb8a3d02c865f25ba1:0:77",
				"372166e31641c32e8be845b4da5acae1666aea9f0c4ec8e98c2d2d2a2937c964:0:77",
				"b17b148020b79942b7aa541e4ad8b59bf1aa4142217693bec24d6fb333d5f4b5:0:88",
			},
			sums:   []float64{12.994313067811, 13.843081047796, 13.990644574672},
			digest: "e2c09de2a3b8690377a6182ae03d9038461b8ea1523860544c08de0479f86f72"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := selectWith(t, c.profile, candidates, exemplar.Config{N: 3})
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if c.sameSelection {
				if got.ID != base.ID {
					t.Errorf("selection changed at %s; the oracle says it should not", c.name)
				}
			} else if got.ID == base.ID {
				t.Fatalf("changing the profile (%s) gave the same selection; the statistic is being ignored", c.name)
			}
			assertSelection(t, got, before, expect{k: 6, eligible: 30,
				ids: c.want, sums: c.sums, rounds: []int{1, 2, 3}})

			rows := make([]string, 0, len(got.Certificate.Density))
			for _, d := range got.Certificate.Density {
				value := "-"
				if d.Neighbours >= got.Certificate.K {
					value = numberID(d.Value)
				}
				eligible := "0"
				if d.Eligible {
					eligible = "1"
				}
				rows = append(rows, d.Identity+"="+value+":"+itoa(d.Neighbours)+":"+eligible)
			}
			if digest := identity.HashBytes(identity.Frame(rows...)); digest != c.digest {
				t.Errorf("density digest = %s, want %s", digest, c.digest)
			}
		})
	}
}

// Each medoid row must describe the exemplar it produced, in order, with the
// round and the sum the oracle computed — otherwise the certificate's central
// evidence is detached from the output, and the ID merely hashes the fabrication.
func TestMedoidRowsDescribeTheSelectedExemplars(t *testing.T) {
	for _, c := range []struct {
		name       string
		candidates []exemplar.Candidate
		n          int
		rounds     []int
		sums       []float64
	}{
		{"single stratum", population(t, 40), 4, []int{1, 2, 3, 4},
			[]float64{13.535588920810, 13.327373266001, 13.403895304123, 13.097115394411}},
		{"three strata", mixed(t), 4, []int{1, 1, 1, 2},
			[]float64{7.949879121646, 4.269278964821, 3.453271450791, 7.627989975073}},
		{"stratum of two", append(append(population(t, 38), admit(t, "- "+prose[38])...), admit(t, "- "+prose[39])...), 2,
			[]int{1, 1}, []float64{12.404784413686, 0.367235399967}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := mustSelect(t, c.candidates, c.n)
			if len(got.Certificate.Medoids) != len(got.Exemplars) {
				t.Fatalf("%d medoid rows for %d exemplars", len(got.Certificate.Medoids), len(got.Exemplars))
			}
			rounds := make([]int, 0, len(got.Certificate.Medoids))
			for i, m := range got.Certificate.Medoids {
				e := got.Exemplars[i]
				if m.Identity != e.Identity() {
					t.Errorf("medoid row %d names %s, exemplar %d is %s", i, m.Identity, i, e.Identity())
				}
				if m.Stratum != e.Stratum() {
					t.Errorf("medoid row %d stratum %q, exemplar stratum %q", i, m.Stratum, e.Stratum())
				}
				if diff := math.Abs(m.Sum - c.sums[i]); diff > 1e-6 {
					t.Errorf("medoid row %d sum = %.12f, want %.12f", i, m.Sum, c.sums[i])
				}
				rounds = append(rounds, m.Round)
			}
			// Exact, not merely non-decreasing: [1,1,1,1] would otherwise pass
			// the single-stratum case, which must be [1,2,3,4].
			if !reflect.DeepEqual(rounds, c.rounds) {
				t.Errorf("rounds = %v, want %v", rounds, c.rounds)
			}
		})
	}
}

// Fewer qualifiers than the eligibility cut, but still enough for n. DESIGN
// says keep all of them and let the n refusal decide; a selector that insists
// on filling all ceil(0.75 x N) slots refuses instead.
func TestFewerQualifiersThanTheCutStillSucceeds(t *testing.T) {
	candidates := population(t, 40)
	// Two defined features can never reach the shared-feature floor of three,
	// so these thirty-three have no valid pair with anybody. The remaining
	// seven keep all six and are mutually valid — exactly k = 6 neighbours each.
	for i := 7; i < len(candidates); i++ {
		defineOnly(&candidates[i], 0, 1)
	}

	// Snapshotted before Select, so a selector that reorders or mutates the
	// caller's slice cannot make the expectation follow its corruption.
	before := snapshot(candidates)
	want := make([]string, 0, 7)
	for _, c := range candidates[:7] {
		want = append(want, c.Identity())
	}
	sort.Strings(want)

	got := mustSelect(t, candidates, 3)
	assertSelection(t, got, before, expect{k: 6, eligible: 7,
		ids: []string{
			"2b258e021dc8d83c6df1491e8485cf9a84f87b22f4a96644a8a3d2769d6f43ff:0:84",
			"ef3cf96ccb60d10716806151d770b9185f60738f5bde9e85ce595ee8808f07e0:0:83",
			"d236d49bbc7f4b02becb7243bb545862a4edb73b7af2172b6a920a8b6fdc8de8:0:95",
		},
		sums:   []float64{2.844348511459, 2.705273056506, 2.723327738929},
		rounds: []int{1, 2, 3}})
	if !reflect.DeepEqual(got.Certificate.Eligible, want) {
		t.Errorf("eligible = %v\nwant %v", got.Certificate.Eligible, want)
	}

	full := map[string]bool{}
	for _, id := range want {
		full[id] = true
	}
	for _, d := range got.Certificate.Density {
		if wantNeighbours := 0; !full[d.Identity] && d.Neighbours != wantNeighbours {
			t.Errorf("%s keeps two features but has %d valid neighbours, want 0", d.Identity, d.Neighbours)
		}
		if full[d.Identity] && d.Neighbours != 6 {
			t.Errorf("%s keeps six features but has %d valid neighbours, want 6", d.Identity, d.Neighbours)
		}
	}
}

// The singleton rule, evaluated against each round's REMAINING candidates. The
// list stratum holds two eligible candidates, so at n=4 allocation must go
// plain, list, plain, list — and the second list pick is the singleton case,
// whose medoid sum over an empty set is zero. A selector that exhausts a
// stratum once one candidate remains returns three exemplars and a wrong
// fourth.
func TestAStratumOfTwoYieldsItsSecondBySingletonRule(t *testing.T) {
	candidates := append(append(population(t, 38), admit(t, "- "+prose[38])...), admit(t, "- "+prose[39])...)
	before := snapshot(candidates)
	got := mustSelect(t, candidates, 4)

	assertSelection(t, got, before, expect{k: 6, eligible: 30,
		ids: []string{
			"b7ba960b1d8f90235eb154061009fe43e1911c03739c778611e97c3309e0d5f0:0:68",
			"c4900f354c9b73b74d65c7aa6e4ea2d4839fdf9e28eef74ed5985585116d2b9b:2:86",
			"2c9c5a6620b2c5652b77ab2237b0d488aa72f705a3c80a85cbcfbb19581ee4e9:0:92",
			"df39054d5237429c0e398ac7462756cbc7bdaba8c0e0384a5159691cf790a7b6:2:74",
		},
		sums:   []float64{12.404784413686, 0.367235399967, 12.479611772447, 0},
		rounds: []int{1, 1, 2, 2}})

	want := []string{
		"paragraph|document", "paragraph|document/list/list-item",
		"paragraph|document", "paragraph|document/list/list-item",
	}
	strata := make([]string, 0, 4)
	rounds := make([]int, 0, 4)
	for _, m := range got.Certificate.Medoids {
		strata = append(strata, m.Stratum)
		rounds = append(rounds, m.Round)
	}
	if !reflect.DeepEqual(strata, want) {
		t.Errorf("allocation %v, want %v", strata, want)
	}
	if r := []int{1, 1, 2, 2}; !reflect.DeepEqual(rounds, r) {
		t.Errorf("rounds %v, want %v", rounds, r)
	}
}
