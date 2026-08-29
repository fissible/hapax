package eval_test

// Clustered bootstrap confidence intervals on the two band thresholds.
//
// DESIGN Section 2 has required these throughout — "a threshold whose interval
// is too wide to be actionable is not shipped" — and left every parameter of the
// procedure unstated, which for an interval means the reported width was
// whatever a default produced. REVIEW Round 10 declares them.
//
// # Clusters are resampled, not segments
//
// Paragraphs from one document share topic, register and occasion, so resampling
// them independently manufactures precision the data does not have. Clusters are
// drawn with replacement to the original cluster count, independently within
// each class, and every segment in a drawn cluster comes with it.
//
// # The unit differs by class, and that is the point
//
// The author's own distances all come from one author, so clustering them by
// author would collapse the class into a single cluster and leave nothing to
// resample. They cluster by DOCUMENT — the within-author variation this side
// measures. The distractor distances cluster by AUTHOR — the between-author
// variation that side measures — with documents nested inside.
//
// Issue #2 closed without a distractor corpus carrying per-author labels, so the
// distractor side usually falls back to the document. That is recorded, not
// silently skipped: document-only clustering UNDERSTATES uncertainty, because it
// counts two documents by one unlabelled author as independent evidence.
//
// # Declared parameters
//
//	confidence   0.95, two-sided, percentile method
//	resamples    2000, so each endpoint is the 50th order statistic
//	seed         fixed and recorded; a bootstrap is the only randomness here and
//	             Section 2 forbids a score that changes on re-run
//	minQualified 0.90
//
// # A failed resample is an outcome
//
// A resample can draw clusters that leave no value meeting its target. Those are
// counted and excluded from the percentiles rather than aborting the estimate —
// but an interval assembled from a heavily degenerate resample distribution
// describes a different population than the one asked about, so at least 90% of
// resamples must qualify.
//
// # The minimum to compute a boundary is not the minimum to ship one
//
// Round 9 derived ceil(1/p) as the sample size at which a threshold exists. A
// threshold at that size rests on a single tail observation, and any resample
// drawing it twice qualifies nothing. Measured at p_author = 0.05: twenty author
// distances qualify about 58% of the time even when every distance sits in its
// own document — the cluster count is not what is short, the tail is. Sixty
// reach roughly 98%, a hundred reach 100%.
//
// No second sample-size minimum is declared, because the 90% floor already
// enforces it against the population actually supplied.

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/fissible/hapax/internal/eval"
)

// ---------------------------------------------------------------------------
// Populations
// ---------------------------------------------------------------------------

// clustered builds one class of distances spread over documents in round-robin,
// so consecutive values land in different documents. Interleaving matters: if a
// document held a contiguous block, the whole tail would live in one cluster and
// every resample would move it together.
// Labels are zero-padded so that lexicographic order — the declared cluster
// order — is also the numeric one, which keeps the fixtures readable.
func clustered(class eval.Class, values []float64, documents int) []eval.ClassedDistance {
	out := make([]eval.ClassedDistance, 0, len(values))
	for i, v := range values {
		in := scored(class, v)
		in.Document = label("doc", i%documents)
		out = append(out, in)
	}
	return out
}

func label(prefix string, n int) string {
	if n < 10 {
		return prefix + "-0" + itoa(n)
	}
	return prefix + "-" + itoa(n)
}

func span(from, to int) []float64 {
	out := make([]float64, 0, to-from+1)
	for v := from; v <= to; v++ {
		out = append(out, float64(v))
	}
	return out
}

// comfortable is a population with room in both tails.
//
//	author     1..100 over 20 documents — A = 96, since exactly five of a
//	           hundred are >= 96 and six are >= 95
//	distractor 201..250 over 10 documents — D = 205, since exactly five of fifty
//	           are <= 205 and six are <= 206
//
// A = 96 < D = 205, so t_low = 96 and t_high = 205, and roughly 99.8% of
// resamples qualify.
func comfortable() []eval.ClassedDistance {
	return append(
		clustered(eval.ClassAuthor, span(1, 100), 20),
		clustered(eval.ClassDistractor, span(201, 250), 10)...,
	)
}

// thinAuthor sits the author side exactly at its derived threshold minimum:
// twenty distances at p_author = 0.05, so the threshold is the single largest
// value and any resample duplicating its document qualifies nothing.
//
// Every distance gets its OWN document, which is the point. The published
// finding is that the cluster count is not what is short — the tail is — so the
// fixture that demonstrates it must give the clustering every advantage and
// still fail.
func thinAuthor() []eval.ClassedDistance {
	return append(
		clustered(eval.ClassAuthor, span(1, 20), 20),
		clustered(eval.ClassDistractor, span(201, 250), 10)...,
	)
}

// crowded separates the two verdicts: the resample distribution qualifies almost
// every draw, so the interval is USABLE, but the two populations sit close
// enough that the intervals overlap, so it is not ACTIONABLE.
//
//	author     1..100 over 20 documents — A = 96
//	distractor 90..139 over 10 documents — D = 94
//
// A = 96 > D = 94, so this is the overlapping case: t_low = 94, t_high = 96.
func crowded() []eval.ClassedDistance {
	return append(
		clustered(eval.ClassAuthor, span(1, 100), 20),
		clustered(eval.ClassDistractor, span(90, 139), 10)...,
	)
}

// touching sits the two intervals exactly against each other: t_low's upper
// endpoint equals t_high's lower endpoint. Closed intervals that merely touch
// are not disjoint, so this must be usable and NOT actionable — the case that
// separates a strict comparison from a non-strict one.
//
//	author     1..100 over 20 documents
//	distractor 87..136 over 10 documents
//
// Under the declared seed t_low spans [88, 93] and t_high spans [93, 99].
func touching() []eval.ClassedDistance {
	return append(
		clustered(eval.ClassAuthor, span(1, 100), 20),
		clustered(eval.ClassDistractor, span(87, 136), 10)...,
	)
}

// thinDistractor is the same failure on the other side: ten distractor distances
// at p_distractor = 0.10.
func thinDistractor() []eval.ClassedDistance {
	return append(
		clustered(eval.ClassAuthor, span(1, 100), 20),
		clustered(eval.ClassDistractor, span(201, 210), 10)...,
	)
}

func bootstrapOf(t *testing.T, in []eval.ClassedDistance) eval.Intervals {
	t.Helper()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	got, err := th.Bootstrap(in, eval.DefaultBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// The declared parameters
// ---------------------------------------------------------------------------

// Asserted against literal values because they are stand-ins that enter the
// interval's identity: changing any of them must fail a test first.
func TestDeclaredBootstrapParameters(t *testing.T) {
	got := eval.DefaultBootstrap()

	if got.Confidence != 0.95 {
		t.Errorf("confidence = %v, want 0.95", got.Confidence)
	}
	if got.Resamples != 2000 {
		t.Errorf("resamples = %d, want 2000", got.Resamples)
	}
	if got.MinQualified != 0.90 {
		t.Errorf("minQualified = %v, want 0.90", got.MinQualified)
	}
	if got.Seed != 0x68617061785F7631 {
		t.Errorf("seed = %#x, want the declared %#x", got.Seed, uint64(0x68617061785F7631))
	}
	if eval.BootstrapAlgorithm != "clustered-percentile-bootstrap-v1" {
		t.Errorf("BootstrapAlgorithm = %q", eval.BootstrapAlgorithm)
	}
}

// ---------------------------------------------------------------------------
// Reproducibility
// ---------------------------------------------------------------------------

// Section 2 forbids a score that changes on re-run, and the bootstrap is the
// only place randomness enters this pipeline.
func TestBootstrapIsDeterministic(t *testing.T) {
	first := bootstrapOf(t, comfortable())
	second := bootstrapOf(t, comfortable())

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two runs over the same population differ:\n%+v\n%+v", first, second)
	}
}

// The seed is recorded and reaches the identity, so two intervals drawn under
// different seeds are different artifacts.
//
// What is deliberately NOT asserted is that two seeds produce different
// endpoints. An endpoint is an order statistic of a resample distribution over a
// handful of observed values, so it is a stable quantile of a discrete
// distribution and two seeds agreeing on it is ordinary rather than suspicious.
// A test asserting otherwise would be flaky. That resampling happens at all is
// established by the interval having width, below.
func TestBootstrapSeedIsRecordedAndIdentifying(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	spec := eval.DefaultBootstrap()
	base, err := th.Bootstrap(in, spec)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if base.Spec.Seed != spec.Seed {
		t.Errorf("seed = %d, want the declared %d", base.Spec.Seed, spec.Seed)
	}

	other := spec
	other.Seed = spec.Seed + 1
	moved, err := th.Bootstrap(in, other)
	if err != nil {
		t.Fatalf("Bootstrap at another seed: %v", err)
	}
	if moved.ID == base.ID {
		t.Errorf("two seeds produced the same interval ID %q", base.ID)
	}
}

// Every declared parameter reaches the identity, not only the seed.
func TestIntervalIdentityCoversTheWholeSpec(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	base, err := th.Bootstrap(in, eval.DefaultBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*eval.BootstrapSpec)
	}{
		{name: "the confidence level", mutate: func(s *eval.BootstrapSpec) { s.Confidence = 0.99 }},
		{name: "the resample count", mutate: func(s *eval.BootstrapSpec) { s.Resamples = 1000 }},
		{name: "the qualification floor", mutate: func(s *eval.BootstrapSpec) { s.MinQualified = 0.80 }},
		{name: "the seed", mutate: func(s *eval.BootstrapSpec) { s.Seed = 12345 }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := eval.DefaultBootstrap()
			c.mutate(&spec)
			moved, err := th.Bootstrap(in, spec)
			if err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			if moved.ID == base.ID {
				t.Errorf("changing %s left the ID at %q", c.name, base.ID)
			}
		})
	}
}

// The interval names the boundaries it describes.
func TestIntervalsCarryTheirThresholds(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	got, err := th.Bootstrap(in, eval.DefaultBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if got.ThresholdsID != th.ID {
		t.Errorf("ThresholdsID = %q, want %q", got.ThresholdsID, th.ID)
	}
	if got.Algorithm != eval.BootstrapAlgorithm {
		t.Errorf("Algorithm = %q, want %q", got.Algorithm, eval.BootstrapAlgorithm)
	}
	if got.Resamples != eval.DefaultBootstrap().Resamples {
		t.Errorf("Resamples = %d, want %d", got.Resamples, eval.DefaultBootstrap().Resamples)
	}
}

// ---------------------------------------------------------------------------
// Clusters, not segments
// ---------------------------------------------------------------------------

// The test that separates cluster resampling from segment resampling.
//
// Every author distance is put in ONE document, so the author class has exactly
// one cluster. Drawing one cluster with replacement can only ever draw that same
// cluster, so every resample sees the identical author population and A is 96
// every time. Since A = 96 is below every distractor distance, t_low = A in
// every resample, and its interval collapses to the single point 96.
//
// An implementation that resampled individual segments would see the author
// values vary and produce an interval with width here. The distractor side still
// has ten documents, so t_high does vary — which is what makes this a test of
// clustering rather than of the bootstrap being switched off.
func TestResamplingIsByClusterNotBySegment(t *testing.T) {
	in := append(
		clustered(eval.ClassAuthor, span(1, 100), 1),
		clustered(eval.ClassDistractor, span(201, 250), 10)...,
	)

	got := bootstrapOf(t, in)

	if got.Low.Lower != 96 || got.Low.Upper != 96 {
		t.Errorf("t_low interval = [%v, %v] with the whole author class in one document; want the single point 96, since every resample draws the same cluster", got.Low.Lower, got.Low.Upper)
	}
	if got.AuthorClusters != 1 {
		t.Errorf("author clusters = %d, want 1", got.AuthorClusters)
	}
}

// The complement: with the author distances spread over twenty documents the
// resamples differ and the interval has width. Without this, the test above is
// also satisfied by a bootstrap that never resamples anything.
func TestAnIntervalHasWidthWhenTheClustersDiffer(t *testing.T) {
	got := bootstrapOf(t, comfortable())

	if got.Low.Lower >= got.Low.Upper {
		t.Errorf("t_low interval = [%v, %v] over twenty author documents; want width", got.Low.Lower, got.Low.Upper)
	}
	if got.High.Lower >= got.High.Upper {
		t.Errorf("t_high interval = [%v, %v] over ten distractor documents; want width", got.High.Lower, got.High.Upper)
	}
}

// ---------------------------------------------------------------------------
// The draw is a specified function of the seed
// ---------------------------------------------------------------------------

// The endpoints below were produced by an independent implementation of the
// declared procedure — SplitMix64 per class, seeded at seed+1 and seed+2, one
// draw per cluster per resample, index modulo the cluster count, percentile
// endpoints taken as order statistics — and not read back out of this package.
// That is what makes them a specification rather than a snapshot.
//
// They are also the only assertions here that prove the seed is USED rather than
// merely recorded and hashed. Everything structural is satisfiable by an
// implementation that draws from a fixed hidden stream.
func TestExactIntervalsUnderTheDeclaredSeed(t *testing.T) {
	got := bootstrapOf(t, comfortable())

	if got.Low.Lower != 93 || got.Low.Upper != 99 {
		t.Errorf("t_low interval = [%v, %v], want [93, 99]", got.Low.Lower, got.Low.Upper)
	}
	if got.High.Lower != 202 || got.High.Upper != 207 {
		t.Errorf("t_high interval = [%v, %v], want [202, 207]", got.High.Lower, got.High.Upper)
	}
	if got.Qualified != 1997 || got.Failed != 3 {
		t.Errorf("qualified %d, failed %d; want 1997 and 3", got.Qualified, got.Failed)
	}
}

// The percentile rank formula, pinned by three nested confidence levels on one
// population. Wider must strictly contain narrower at every endpoint, and the
// exact values rule out the common wrong ranks — 5th/95th instead of
// 2.5th/97.5th produces [95, 98] at the 0.95 level, which is the 0.50 answer.
func TestExactIntervalsAcrossConfidenceLevels(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	cases := []struct {
		confidence     float64
		lowLo, lowHi   float64
		highLo, highHi float64
	}{
		{confidence: 0.50, lowLo: 95, lowHi: 98, highLo: 203, highHi: 205},
		{confidence: 0.95, lowLo: 93, lowHi: 99, highLo: 202, highHi: 207},
		{confidence: 0.99, lowLo: 91, lowHi: 100, highLo: 201, highHi: 208},
	}

	for _, c := range cases {
		t.Run(strconv.FormatFloat(c.confidence, 'f', -1, 64), func(t *testing.T) {
			spec := eval.DefaultBootstrap()
			spec.Confidence = c.confidence
			got, err := th.Bootstrap(in, spec)
			if err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			if got.Low.Lower != c.lowLo || got.Low.Upper != c.lowHi {
				t.Errorf("t_low interval = [%v, %v], want [%v, %v]", got.Low.Lower, got.Low.Upper, c.lowLo, c.lowHi)
			}
			if got.High.Lower != c.highLo || got.High.Upper != c.highHi {
				t.Errorf("t_high interval = [%v, %v], want [%v, %v]", got.High.Lower, got.High.Upper, c.highLo, c.highHi)
			}
			if got.Spec.Confidence != c.confidence {
				t.Errorf("the returned spec reports confidence %v, want %v", got.Spec.Confidence, c.confidence)
			}
		})
	}
}

// The resample count is used, not merely recorded: halving it changes both the
// accounting and the draws behind the endpoints.
func TestTheResampleCountIsUsed(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	spec := eval.DefaultBootstrap()
	spec.Resamples = 500
	got, err := th.Bootstrap(in, spec)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got.Resamples != 500 {
		t.Errorf("Resamples = %d, want 500", got.Resamples)
	}
	if got.Qualified+got.Failed != 500 {
		t.Errorf("qualified %d plus failed %d is not 500", got.Qualified, got.Failed)
	}
	if got.Spec.Resamples != 500 {
		t.Errorf("the returned spec reports %d resamples, want 500", got.Spec.Resamples)
	}
}

// The author class clusters by document even when author labels are present,
// because clustering it by author would collapse it to one cluster and leave
// nothing to resample. Labelling every author distance with the same author must
// not change the author cluster count.
func TestTheAuthorClassAlwaysClustersByDocument(t *testing.T) {
	in := comfortable()
	for i := range in {
		if in[i].Class == eval.ClassAuthor {
			in[i].Author = "the-author"
		}
	}

	got := bootstrapOf(t, in)
	if got.AuthorClusters != 20 {
		t.Errorf("author clusters = %d with one author label over twenty documents; want 20, since the author side clusters by document", got.AuthorClusters)
	}
}

// The distractor class clusters by author when labels exist. Ten documents
// carrying two author labels is two clusters, not ten — and the recorded
// clustering unit says so.
func TestTheDistractorClassClustersByAuthorWhenLabelled(t *testing.T) {
	unlabelled := bootstrapOf(t, comfortable())
	if unlabelled.DistractorClusters != 10 {
		t.Errorf("distractor clusters = %d over ten unlabelled documents, want 10", unlabelled.DistractorClusters)
	}
	if unlabelled.Clustering != eval.ClusterByDocument {
		t.Errorf("clustering = %q, want %q", unlabelled.Clustering, eval.ClusterByDocument)
	}

	labelled := comfortable()
	for i := range labelled {
		if labelled[i].Class != eval.ClassDistractor {
			continue
		}
		if labelled[i].Document < "doc-05" {
			labelled[i].Author = "author-one"
			continue
		}
		labelled[i].Author = "author-two"
	}

	got := bootstrapOf(t, labelled)
	if got.DistractorClusters != 2 {
		t.Errorf("distractor clusters = %d over ten documents by two authors; want 2", got.DistractorClusters)
	}
	if got.Clustering != eval.ClusterByDocumentAndAuthor {
		t.Errorf("clustering = %q, want %q", got.Clustering, eval.ClusterByDocumentAndAuthor)
	}
}

// Document-only clustering is flagged, because it understates uncertainty rather
// than failing. A narrower interval that looks fine is the dangerous outcome.
func TestDocumentOnlyClusteringIsFlagged(t *testing.T) {
	got := bootstrapOf(t, comfortable())

	if got.Clustering != eval.ClusterByDocument {
		t.Fatalf("clustering = %q, want %q", got.Clustering, eval.ClusterByDocument)
	}
	if !got.UnderstatesUncertainty {
		t.Errorf("document-only clustering is not flagged as understating uncertainty")
	}

	labelled := comfortable()
	for i := range labelled {
		if labelled[i].Class == eval.ClassDistractor {
			labelled[i].Author = "author-" + labelled[i].Document
		}
	}
	full := bootstrapOf(t, labelled)
	if full.UnderstatesUncertainty {
		t.Errorf("author-clustered intervals are flagged as understating uncertainty")
	}
}

// ---------------------------------------------------------------------------
// The qualification floor
// ---------------------------------------------------------------------------

// A population with room in both tails qualifies nearly every resample.
func TestAComfortablePopulationIsUsable(t *testing.T) {
	got := bootstrapOf(t, comfortable())

	if !got.Usable {
		t.Fatalf("not usable: %v (qualified %d of %d)", got.Reason, got.Qualified, got.Resamples)
	}
	if got.Reason != "" {
		t.Errorf("a usable interval carries the reason %q", got.Reason)
	}
	floor := eval.DefaultBootstrap().MinQualified
	if rate := float64(got.Qualified) / float64(got.Resamples); rate < floor {
		t.Errorf("qualification rate %v is below the floor %v", rate, floor)
	}
}

// A population sitting exactly at its derived threshold minimum computes a
// boundary and cannot support an interval, on either side. This is the finding
// Round 10 published: ceil(1/p) is the minimum to compute a boundary, not to
// ship one.
func TestAPopulationAtTheThresholdMinimumIsNotUsable(t *testing.T) {
	cases := []struct {
		name      string
		in        []eval.ClassedDistance
		qualified int
	}{
		// Exact counts under the declared seed, from the same independent
		// reference that produced the endpoints above.
		{name: "twenty author distances at p_author 0.05", in: thinAuthor(), qualified: 1160},
		{name: "ten distractor distances at p_distractor 0.10", in: thinDistractor(), qualified: 1161},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bootstrapOf(t, c.in)

			if got.Usable {
				t.Fatalf("usable with %d of %d resamples qualifying", got.Qualified, got.Resamples)
			}
			if got.Qualified != c.qualified {
				t.Errorf("qualified = %d, want %d", got.Qualified, c.qualified)
			}
			if got.Reason == "" {
				t.Errorf("an unusable interval states no reason")
			}
			floor := eval.DefaultBootstrap().MinQualified
			if rate := float64(got.Qualified) / float64(got.Resamples); rate >= floor {
				t.Errorf("qualification rate %v is at or above the floor %v; this fixture must fall below it", rate, floor)
			}
		})
	}
}

// Failed resamples are counted, not silently dropped, and the accounting closes.
// A count that did not add up would hide resamples an implementation skipped
// for some other reason.
func TestResampleAccountingIsComplete(t *testing.T) {
	for _, in := range [][]eval.ClassedDistance{comfortable(), thinAuthor()} {
		got := bootstrapOf(t, in)
		if got.Qualified > got.Resamples {
			t.Errorf("qualified %d exceeds resamples %d", got.Qualified, got.Resamples)
		}
		if got.Qualified+got.Failed != got.Resamples {
			t.Errorf("qualified %d plus failed %d is not resamples %d", got.Qualified, got.Failed, got.Resamples)
		}
		if got.Resamples != eval.DefaultBootstrap().Resamples {
			t.Errorf("resamples = %d, want %d", got.Resamples, eval.DefaultBootstrap().Resamples)
		}
	}
}

// The floor is a parameter, not a constant: the same unusable population becomes
// usable when the declared floor is lowered below its qualification rate. An
// implementation that hardcoded 0.90 passes every test above and fails this one.
func TestTheQualificationFloorIsTheDeclaredOne(t *testing.T) {
	in := thinAuthor()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	strict, err := th.Bootstrap(in, eval.DefaultBootstrap())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if strict.Usable {
		t.Fatalf("this fixture must be unusable at the declared floor")
	}

	lax := eval.DefaultBootstrap()
	lax.MinQualified = 0.10
	got, err := th.Bootstrap(in, lax)
	if err != nil {
		t.Fatalf("Bootstrap at a lowered floor: %v", err)
	}
	if !got.Usable {
		t.Errorf("not usable at a floor of 0.10 with %d of %d qualifying", got.Qualified, got.Resamples)
	}
}

// ---------------------------------------------------------------------------
// The intervals themselves
// ---------------------------------------------------------------------------

func TestIntervalsAreOrdered(t *testing.T) {
	got := bootstrapOf(t, comfortable())

	if got.Low.Lower > got.Low.Upper {
		t.Errorf("t_low interval is inverted: [%v, %v]", got.Low.Lower, got.Low.Upper)
	}
	if got.High.Lower > got.High.Upper {
		t.Errorf("t_high interval is inverted: [%v, %v]", got.High.Lower, got.High.Upper)
	}
}

// Every resample's threshold is a value some segment produced, so a percentile
// of them taken as an order statistic is one too. No interpolation, matching
// Section 2's refusal of a boundary the population never contained.
func TestIntervalEndpointsAreObservedValues(t *testing.T) {
	in := comfortable()
	got := bootstrapOf(t, in)

	observed := make(map[float64]bool, len(in))
	for _, d := range in {
		observed[d.Distance.Value] = true
	}
	for _, endpoint := range []struct {
		name  string
		value float64
	}{
		{"t_low lower", got.Low.Lower}, {"t_low upper", got.Low.Upper},
		{"t_high lower", got.High.Lower}, {"t_high upper", got.High.Upper},
	} {
		if !observed[endpoint.value] {
			t.Errorf("%s = %v is not a value any segment produced", endpoint.name, endpoint.value)
		}
	}
}

// Nothing reaches a non-finite endpoint, whatever the qualification rate.
func TestIntervalEndpointsAreFinite(t *testing.T) {
	for _, in := range [][]eval.ClassedDistance{comfortable(), thinAuthor(), thinDistractor()} {
		got := bootstrapOf(t, in)
		for _, v := range []float64{got.Low.Lower, got.Low.Upper, got.High.Lower, got.High.Upper} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("endpoint %v cannot be persisted or hashed", v)
			}
		}
	}
}

// A wider confidence level cannot give a narrower interval. This is the property
// that would break if the percentile indices were computed from the wrong tail.
func TestAWiderConfidenceGivesAWiderInterval(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	narrow := eval.DefaultBootstrap()
	narrow.Confidence = 0.50
	wide := eval.DefaultBootstrap()
	wide.Confidence = 0.99

	gotNarrow, err := th.Bootstrap(in, narrow)
	if err != nil {
		t.Fatalf("Bootstrap at 0.50: %v", err)
	}
	gotWide, err := th.Bootstrap(in, wide)
	if err != nil {
		t.Fatalf("Bootstrap at 0.99: %v", err)
	}

	if gotWide.Low.Lower > gotNarrow.Low.Lower || gotWide.Low.Upper < gotNarrow.Low.Upper {
		t.Errorf("the 0.99 t_low interval [%v, %v] does not contain the 0.50 interval [%v, %v]",
			gotWide.Low.Lower, gotWide.Low.Upper, gotNarrow.Low.Lower, gotNarrow.Low.Upper)
	}
	if gotWide.High.Lower > gotNarrow.High.Lower || gotWide.High.Upper < gotNarrow.High.Upper {
		t.Errorf("the 0.99 t_high interval [%v, %v] does not contain the 0.50 interval [%v, %v]",
			gotWide.High.Lower, gotWide.High.Upper, gotNarrow.High.Lower, gotNarrow.High.Upper)
	}
}

// ---------------------------------------------------------------------------
// What the interval must be given
// ---------------------------------------------------------------------------

// An interval drawn from a population other than the one calibrated is a
// statement about a boundary nobody drew. The threshold identity already covers
// the populations, the targets and every binding, so reproducing it is the check.
func TestBootstrapRefusesAnotherPopulation(t *testing.T) {
	th, err := eval.Calibrate(comfortable(), testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	other := comfortable()
	other[7].Distance.Value = 4242

	if _, err := th.Bootstrap(other, eval.DefaultBootstrap()); !errors.Is(err, eval.ErrPopulationMismatch) {
		t.Errorf("err = %v, want %v", err, eval.ErrPopulationMismatch)
	}
}

// Cluster labels are required here and nowhere else: Calibrate needs no clusters
// and is unchanged, which is why the labels live on ClassedDistance rather than
// on the distance itself.
func TestBootstrapRequiresClusterLabels(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	unlabelled := comfortable()
	unlabelled[3].Document = ""

	if _, err := th.Bootstrap(unlabelled, eval.DefaultBootstrap()); !errors.Is(err, eval.ErrMissingCluster) {
		t.Errorf("err = %v, want %v", err, eval.ErrMissingCluster)
	}
}

// Calibration itself must still accept a population with no cluster labels at
// all, which is what the frozen threshold tests supply.
func TestCalibrationDoesNotRequireClusterLabels(t *testing.T) {
	if _, err := eval.Calibrate(separated(), testSource(), eval.DefaultTargets()); err != nil {
		t.Errorf("Calibrate refused an unlabelled population: %v", err)
	}
}

// A partially labelled distractor class cannot be clustered by author: some
// documents would be their own cluster and others would be pooled, which is
// neither of the two declared units.
func TestPartialAuthorLabellingIsRefused(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	partial := comfortable()
	for i := range partial {
		if partial[i].Class == eval.ClassDistractor && partial[i].Document == "doc-00" {
			partial[i].Author = "author-one"
		}
	}

	if _, err := th.Bootstrap(partial, eval.DefaultBootstrap()); !errors.Is(err, eval.ErrPartialAuthorLabels) {
		t.Errorf("err = %v, want %v", err, eval.ErrPartialAuthorLabels)
	}
}

func TestBootstrapRefusesAnInvalidSpec(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*eval.BootstrapSpec)
	}{
		{name: "a zero confidence", mutate: func(s *eval.BootstrapSpec) { s.Confidence = 0 }},
		{name: "a negative confidence", mutate: func(s *eval.BootstrapSpec) { s.Confidence = -0.5 }},
		{name: "a confidence of one", mutate: func(s *eval.BootstrapSpec) { s.Confidence = 1 }},
		{name: "a confidence above one", mutate: func(s *eval.BootstrapSpec) { s.Confidence = 1.5 }},
		{name: "a NaN confidence", mutate: func(s *eval.BootstrapSpec) { s.Confidence = math.NaN() }},
		{name: "zero resamples", mutate: func(s *eval.BootstrapSpec) { s.Resamples = 0 }},
		{name: "negative resamples", mutate: func(s *eval.BootstrapSpec) { s.Resamples = -1 }},
		{name: "a zero qualification floor", mutate: func(s *eval.BootstrapSpec) { s.MinQualified = 0 }},
		{name: "a negative qualification floor", mutate: func(s *eval.BootstrapSpec) { s.MinQualified = -0.1 }},
		{name: "a qualification floor above one", mutate: func(s *eval.BootstrapSpec) { s.MinQualified = 1.5 }},
		{name: "a NaN qualification floor", mutate: func(s *eval.BootstrapSpec) { s.MinQualified = math.NaN() }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := eval.DefaultBootstrap()
			c.mutate(&spec)
			if _, err := th.Bootstrap(in, spec); !errors.Is(err, eval.ErrInvalidBootstrap) {
				t.Errorf("err = %v, want %v", err, eval.ErrInvalidBootstrap)
			}
		})
	}

	// A qualification floor of exactly one is valid: it demands that every
	// resample qualify, which is stringent but not nonsensical.
	t.Run("a qualification floor of one", func(t *testing.T) {
		spec := eval.DefaultBootstrap()
		spec.MinQualified = 1
		if _, err := th.Bootstrap(in, spec); err != nil {
			t.Errorf("a floor of 1 was refused: %v", err)
		}
	})

	t.Run("a nil artifact", func(t *testing.T) {
		var none *eval.Thresholds
		if _, err := none.Bootstrap(in, eval.DefaultBootstrap()); !errors.Is(err, eval.ErrMissingInput) {
			t.Errorf("err = %v, want %v", err, eval.ErrMissingInput)
		}
	})
}

// ---------------------------------------------------------------------------
// Clusters carry their nested documents
// ---------------------------------------------------------------------------

// The distractor-side counterpart of the author collapse test. Reporting a
// cluster count of one is not the same as actually drawing whole authors: an
// implementation could count authors and still resample documents.
//
// Every distractor document is labelled with the SAME author, so the distractor
// class has one cluster and every resample sees the identical distractor
// population. D is then 205 in every resample, and since it is above every
// author distance, t_high = D and its interval collapses to the single point.
func TestDrawingAnAuthorCarriesItsDocuments(t *testing.T) {
	in := comfortable()
	for i := range in {
		if in[i].Class == eval.ClassDistractor {
			in[i].Author = "the-only-distractor"
		}
	}

	got := bootstrapOf(t, in)

	if got.DistractorClusters != 1 {
		t.Fatalf("distractor clusters = %d with one author label, want 1", got.DistractorClusters)
	}
	if got.High.Lower != 205 || got.High.Upper != 205 {
		t.Errorf("t_high interval = [%v, %v] with the whole distractor class under one author; want the single point 205, since every resample draws the same cluster", got.High.Lower, got.High.Upper)
	}
	// The author side still varies, so this is a test of clustering rather than
	// of the bootstrap being switched off.
	if got.Low.Lower >= got.Low.Upper {
		t.Errorf("t_low interval = [%v, %v]; the author side should still vary", got.Low.Lower, got.Low.Upper)
	}
}

// ---------------------------------------------------------------------------
// Actionability, and the combined verdict
// ---------------------------------------------------------------------------

// ADR 0005 says a threshold whose interval is too wide is not shipped, and never
// said how wide. The geometry answers it: if the interval on t_low reaches into
// the interval on t_high, the data does not resolve the two boundaries from each
// other and the three regions are not distinguishable.
//
// The `crowded` population separates this from usability. Nearly every resample
// qualifies, so it is usable; the two populations sit close enough that the
// intervals overlap, so it is not actionable. A single verdict conflating the two
// could not tell these apart, and their remedies differ — more writing versus
// better separation.
func TestActionabilityIsIntervalOverlap(t *testing.T) {
	t.Run("separated populations are actionable", func(t *testing.T) {
		got := bootstrapOf(t, comfortable())
		if !got.Usable {
			t.Fatalf("not usable: %v", got.Reason)
		}
		if !got.Actionable {
			t.Errorf("intervals [%v, %v] and [%v, %v] do not overlap but are not actionable",
				got.Low.Lower, got.Low.Upper, got.High.Lower, got.High.Upper)
		}
		if !got.Shippable {
			t.Errorf("usable and actionable but not shippable")
		}
	})

	t.Run("crowded populations are usable but not actionable", func(t *testing.T) {
		got := bootstrapOf(t, crowded())

		if !got.Usable {
			t.Fatalf("not usable: %v (qualified %d of %d)", got.Reason, got.Qualified, got.Resamples)
		}
		// Exact under the declared seed: t_low spans [91, 96] and t_high spans
		// [93, 99], so they share [93, 96].
		if got.Low.Lower != 91 || got.Low.Upper != 96 {
			t.Errorf("t_low interval = [%v, %v], want [91, 96]", got.Low.Lower, got.Low.Upper)
		}
		if got.High.Lower != 93 || got.High.Upper != 99 {
			t.Errorf("t_high interval = [%v, %v], want [93, 99]", got.High.Lower, got.High.Upper)
		}
		if got.Low.Upper < got.High.Lower {
			t.Fatalf("intervals [%v, %v] and [%v, %v] do not overlap; this fixture needs them to",
				got.Low.Lower, got.Low.Upper, got.High.Lower, got.High.Upper)
		}
		if got.Actionable {
			t.Errorf("overlapping intervals [%v, %v] and [%v, %v] are reported actionable",
				got.Low.Lower, got.Low.Upper, got.High.Lower, got.High.Upper)
		}
		if got.Shippable {
			t.Errorf("not actionable but shippable")
		}
		if got.Reason == "" {
			t.Errorf("an unshippable interval states no reason")
		}
	})

	t.Run("unusable populations are not shippable", func(t *testing.T) {
		got := bootstrapOf(t, thinAuthor())
		if got.Usable {
			t.Fatalf("this fixture must be unusable")
		}
		if got.Shippable {
			t.Errorf("unusable but shippable")
		}
	})
}

// ---------------------------------------------------------------------------
// Symmetric guards
// ---------------------------------------------------------------------------

// A missing document label is refused on either side. The author class needs it
// because that is its cluster unit; the distractor class needs it because it is
// the fallback when no author label exists.
func TestMissingDocumentLabelsAreRefusedOnBothSides(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	cases := []struct {
		name  string
		class eval.Class
	}{
		{name: "an author distance", class: eval.ClassAuthor},
		{name: "a distractor distance", class: eval.ClassDistractor},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			broken := comfortable()
			for i := range broken {
				if broken[i].Class == c.class {
					broken[i].Document = ""
					break
				}
			}
			if _, err := th.Bootstrap(broken, eval.DefaultBootstrap()); !errors.Is(err, eval.ErrMissingCluster) {
				t.Errorf("err = %v, want %v", err, eval.ErrMissingCluster)
			}
		})
	}
}

// A fully author-labelled distractor class still needs its documents, because
// the document is what the interval reports as the fallback unit and what a
// later partially-labelled corpus would fall back to.
func TestAuthorLabelsDoNotExcuseAMissingDocument(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	broken := comfortable()
	for i := range broken {
		if broken[i].Class == eval.ClassDistractor {
			broken[i].Author = "author-" + broken[i].Document
		}
	}
	for i := range broken {
		if broken[i].Class == eval.ClassDistractor {
			broken[i].Document = ""
			break
		}
	}

	if _, err := th.Bootstrap(broken, eval.DefaultBootstrap()); !errors.Is(err, eval.ErrMissingCluster) {
		t.Errorf("err = %v, want %v", err, eval.ErrMissingCluster)
	}
}

// Partial author labels are refused on the distractor class, where the author is
// the cluster unit and a half-labelled class is neither declared unit. They are
// IGNORED on the author class, where the document is the declared unit — one
// author cannot partition its own class, so a stray label there carries no
// information and must not change the clustering or the numbers.
func TestPartialAuthorLabelsAreRefusedOnlyOnTheDistractorClass(t *testing.T) {
	in := comfortable()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	t.Run("on the distractor class", func(t *testing.T) {
		partial := comfortable()
		for i := range partial {
			if partial[i].Class == eval.ClassDistractor && partial[i].Document == "doc-00" {
				partial[i].Author = "author-one"
			}
		}
		if _, err := th.Bootstrap(partial, eval.DefaultBootstrap()); !errors.Is(err, eval.ErrPartialAuthorLabels) {
			t.Errorf("err = %v, want %v", err, eval.ErrPartialAuthorLabels)
		}
	})

	t.Run("on the author class", func(t *testing.T) {
		base := bootstrapOf(t, comfortable())

		partial := comfortable()
		for i := range partial {
			if partial[i].Class == eval.ClassAuthor && partial[i].Document == "doc-00" {
				partial[i].Author = "a-stray-label"
			}
		}
		got, err := th.Bootstrap(partial, eval.DefaultBootstrap())
		if err != nil {
			t.Fatalf("a stray author label on the author class was refused: %v", err)
		}
		if got.AuthorClusters != base.AuthorClusters {
			t.Errorf("author clusters changed from %d to %d", base.AuthorClusters, got.AuthorClusters)
		}
		if got.Low != base.Low || got.High != base.High {
			t.Errorf("the intervals moved from %+v/%+v to %+v/%+v", base.Low, base.High, got.Low, got.High)
		}
		if got.Clustering != base.Clustering {
			t.Errorf("clustering changed from %q to %q", base.Clustering, got.Clustering)
		}
	})
}

// ---------------------------------------------------------------------------
// The two streams are independent
// ---------------------------------------------------------------------------

// Each class draws from its own SplitMix64 stream, seeded at seed+1 and seed+2,
// so one class's cluster count cannot shift the other's draws. A single shared
// stream satisfies every exact-endpoint assertion above and violates this: the
// author class consuming a different number of draws per resample would slide
// the distractor's draws along with it.
//
// Collapsing the author class to one document changes how many author draws
// happen per resample, and the distractor interval must not move. Then the same
// in reverse.
func TestTheTwoClassStreamsAreIndependent(t *testing.T) {
	base := bootstrapOf(t, comfortable())

	t.Run("collapsing the author class leaves t_high alone", func(t *testing.T) {
		in := append(
			clustered(eval.ClassAuthor, span(1, 100), 1),
			clustered(eval.ClassDistractor, span(201, 250), 10)...,
		)
		got := bootstrapOf(t, in)

		if got.AuthorClusters == base.AuthorClusters {
			t.Fatalf("this fixture must change the author cluster count; both are %d", got.AuthorClusters)
		}
		if got.High != base.High {
			t.Errorf("t_high interval moved from %+v to %+v when only the author clustering changed", base.High, got.High)
		}
	})

	t.Run("collapsing the distractor class leaves t_low alone", func(t *testing.T) {
		in := comfortable()
		for i := range in {
			if in[i].Class == eval.ClassDistractor {
				in[i].Author = "the-only-distractor"
			}
		}
		got := bootstrapOf(t, in)

		if got.DistractorClusters == base.DistractorClusters {
			t.Fatalf("this fixture must change the distractor cluster count; both are %d", got.DistractorClusters)
		}
		if got.Low != base.Low {
			t.Errorf("t_low interval moved from %+v to %+v when only the distractor clustering changed", base.Low, got.Low)
		}
	})
}

// ---------------------------------------------------------------------------
// Order of presentation is not information
// ---------------------------------------------------------------------------

// Clusters are ordered lexicographically by label before any index is taken, so
// the same population supplied in a different order is the same population. An
// implementation ordering clusters by first appearance passes every exact
// assertion above — the fixtures happen to arrive in label order — and fails
// here.
func TestTheIntervalDoesNotDependOnInputOrder(t *testing.T) {
	forward := comfortable()
	reversed := make([]eval.ClassedDistance, len(forward))
	for i := range forward {
		reversed[len(forward)-1-i] = forward[i]
	}

	base := bootstrapOf(t, forward)
	got := bootstrapOf(t, reversed)

	if got.Low != base.Low || got.High != base.High {
		t.Errorf("reversing the input moved the intervals from %+v/%+v to %+v/%+v", base.Low, base.High, got.Low, got.High)
	}
	if got.ID != base.ID {
		t.Errorf("reversing the input changed the interval ID")
	}
	if got.Qualified != base.Qualified {
		t.Errorf("reversing the input changed the qualified count from %d to %d", base.Qualified, got.Qualified)
	}
}

// The author-labelled branch, which the test above does not reach: it clusters
// distractors by document, so an implementation could order document clusters
// lexicographically and still keep AUTHOR clusters in first-seen order.
//
// Invariance alone would not settle it either — any canonical, input-independent
// order gives the same answer forwards and backwards. So this fixture is chosen
// to DISCRIMINATE, and asserts the exact figure the declared order produces.
//
//	doc-00           -> "zeta"
//	doc-01, doc-02   -> "mu"
//	doc-03 .. doc-09 -> "alpha"
//
// First-seen order is zeta, mu, alpha, which is also reverse-lexicographic here,
// and lexicographic is alpha, mu, zeta. Under the declared seed the declared
// order qualifies 1927 resamples; both wrong orders qualify 1935. The endpoints
// coincide, so the qualified count is the statistic that separates them.
func TestAuthorClustersAreOrderedLexicographically(t *testing.T) {
	labelled := func() []eval.ClassedDistance {
		out := comfortable()
		for i := range out {
			if out[i].Class != eval.ClassDistractor {
				continue
			}
			switch {
			case out[i].Document == "doc-00":
				out[i].Author = "zeta"
			case out[i].Document < "doc-03":
				out[i].Author = "mu"
			default:
				out[i].Author = "alpha"
			}
		}
		return out
	}

	forward := labelled()
	base := bootstrapOf(t, forward)

	if base.DistractorClusters != 3 {
		t.Fatalf("distractor clusters = %d, want 3", base.DistractorClusters)
	}
	if base.Clustering != eval.ClusterByDocumentAndAuthor {
		t.Fatalf("clustering = %q, want %q", base.Clustering, eval.ClusterByDocumentAndAuthor)
	}
	if base.Low.Lower != 93 || base.Low.Upper != 99 {
		t.Errorf("t_low interval = [%v, %v], want [93, 99]", base.Low.Lower, base.Low.Upper)
	}
	if base.High.Lower != 201 || base.High.Upper != 206 {
		t.Errorf("t_high interval = [%v, %v], want [201, 206]", base.High.Lower, base.High.Upper)
	}
	if base.Qualified != 1927 {
		t.Errorf("qualified = %d, want 1927; 1935 is what first-seen and reverse-lexicographic author ordering both give", base.Qualified)
	}

	// And the same population supplied backwards is the same population.
	reversed := make([]eval.ClassedDistance, len(forward))
	for i := range forward {
		reversed[len(forward)-1-i] = forward[i]
	}
	got := bootstrapOf(t, reversed)
	if got.Low != base.Low || got.High != base.High || got.Qualified != base.Qualified {
		t.Errorf("reversing an author-labelled population changed the result: %+v/%+v/%d became %+v/%+v/%d",
			base.Low, base.High, base.Qualified, got.Low, got.High, got.Qualified)
	}
	if got.ID != base.ID {
		t.Errorf("reversing an author-labelled population changed the interval ID")
	}
}

// The partition into clusters is part of what produced the interval, so it is
// part of the interval's identity. The threshold artifact's ID covers the
// distances but says nothing about how they are grouped, and the cluster COUNTS
// say only how many groups there are.
//
// The same hundred author distances grouped round-robin over twenty documents
// and grouped as twenty contiguous blocks of five give the same threshold ID,
// the same cluster counts and the same clustering unit — and different resample
// distributions, so different intervals. An identity covering only the counts
// would serve one grouping's interval for the other's, and since the interval
// decides Usable, Actionable and therefore Shippable, that is a shipping
// decision taken on the wrong evidence.
//
// Added by consensus: the first implementation hashed the cluster counts without
// their membership, and nothing here could see it.
func TestIntervalIdentityCoversTheClustering(t *testing.T) {
	blocked := func() []eval.ClassedDistance {
		out := make([]eval.ClassedDistance, 0, 150)
		for i, v := range span(1, 100) {
			in := scored(eval.ClassAuthor, v)
			in.Document = label("doc", i/5)
			out = append(out, in)
		}
		return append(out, clustered(eval.ClassDistractor, span(201, 250), 10)...)
	}

	roundRobin := bootstrapOf(t, comfortable())
	contiguous := bootstrapOf(t, blocked())

	if roundRobin.ThresholdsID != contiguous.ThresholdsID {
		t.Fatalf("the two groupings have different threshold IDs; this test needs them identical")
	}
	if roundRobin.AuthorClusters != contiguous.AuthorClusters || roundRobin.DistractorClusters != contiguous.DistractorClusters {
		t.Fatalf("cluster counts differ: %d/%d and %d/%d; this test needs them identical",
			roundRobin.AuthorClusters, roundRobin.DistractorClusters,
			contiguous.AuthorClusters, contiguous.DistractorClusters)
	}
	if roundRobin.Clustering != contiguous.Clustering {
		t.Fatalf("clustering units differ; this test needs them identical")
	}
	if roundRobin.ID == contiguous.ID {
		t.Errorf("two different cluster partitions of the same distances share the interval ID %q", roundRobin.ID)
	}
}

// ---------------------------------------------------------------------------
// Both floors are inclusive
// ---------------------------------------------------------------------------

// "At least 90% must qualify" admits equality. thinAuthor qualifies exactly 1160
// of 2000, which is 0.58 to the last bit, so a floor of exactly that must pass.
// An implementation comparing with > rather than >= fails only here.
func TestTheQualificationFloorIsInclusive(t *testing.T) {
	in := thinAuthor()
	th, err := eval.Calibrate(in, testSource(), eval.DefaultTargets())
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}

	spec := eval.DefaultBootstrap()
	spec.MinQualified = 1160.0 / 2000.0
	got, err := th.Bootstrap(in, spec)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got.Qualified != 1160 {
		t.Fatalf("qualified = %d, want 1160; this test needs the floor to sit exactly on it", got.Qualified)
	}
	if !got.Usable {
		t.Errorf("a qualification rate exactly at the declared floor was refused")
	}
}

// Intervals that merely touch are not disjoint, so they are not actionable. The
// crowded fixture overlaps strictly and the comfortable one is strictly apart;
// only this one distinguishes a strict comparison from a non-strict one.
func TestTouchingIntervalsAreNotActionable(t *testing.T) {
	got := bootstrapOf(t, touching())

	if got.Low.Upper != 93 || got.High.Lower != 93 {
		t.Fatalf("intervals are [%v, %v] and [%v, %v]; this fixture needs them to touch at 93",
			got.Low.Lower, got.Low.Upper, got.High.Lower, got.High.Upper)
	}
	if !got.Usable {
		t.Fatalf("not usable: %v", got.Reason)
	}
	if got.Actionable {
		t.Errorf("intervals touching at %v are reported actionable", got.Low.Upper)
	}
	if got.Shippable {
		t.Errorf("not actionable but shippable")
	}
}
