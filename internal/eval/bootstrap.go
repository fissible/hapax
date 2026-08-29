package eval

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/fissible/hapax/internal/identity"
)

// BootstrapAlgorithm identifies the declared clustered percentile procedure.
const BootstrapAlgorithm = "clustered-percentile-bootstrap-v1"

// Clustering identifies the weakest clustering unit used by an interval.
type Clustering string

const (
	ClusterByDocument          Clustering = "document"
	ClusterByDocumentAndAuthor Clustering = "document-and-author"
)

// BootstrapSpec declares a reproducible clustered bootstrap.
type BootstrapSpec struct {
	Confidence   float64
	Resamples    int
	Seed         uint64
	MinQualified float64
}

// Interval is a closed percentile interval.
type Interval struct {
	Lower, Upper float64
}

// Intervals records bootstrap uncertainty for both ordered thresholds.
type Intervals struct {
	ID, ThresholdsID       string
	Low, High              Interval
	Spec                   BootstrapSpec
	Algorithm              string
	Clustering             Clustering
	UnderstatesUncertainty bool
	AuthorClusters         int
	DistractorClusters     int
	Resamples, Qualified   int
	Failed                 int
	Usable, Actionable     bool
	Shippable              bool
	Reason                 string
}

var (
	ErrPopulationMismatch  = errors.New("eval bootstrap population mismatch")
	ErrMissingCluster      = errors.New("eval bootstrap missing cluster")
	ErrPartialAuthorLabels = errors.New("eval bootstrap partial author labels")
	ErrInvalidBootstrap    = errors.New("eval bootstrap invalid specification")
)

// DefaultBootstrap returns the v1 declared interval parameters.
func DefaultBootstrap() BootstrapSpec {
	return BootstrapSpec{Confidence: 0.95, Resamples: 2000, Seed: 0x68617061785F7631, MinQualified: 0.90}
}

// Bootstrap derives clustered percentile intervals from the calibrated population.
func (t *Thresholds) Bootstrap(distances []ClassedDistance, spec BootstrapSpec) (Intervals, error) {
	if t == nil {
		return Intervals{}, fmt.Errorf("bootstrap: %w: thresholds", ErrMissingInput)
	}
	if !validBootstrap(spec) {
		return Intervals{}, fmt.Errorf("bootstrap: %w", ErrInvalidBootstrap)
	}
	population, err := Calibrate(distances, t.Source, t.Targets)
	if err != nil || population.ID != t.ID {
		return Intervals{}, fmt.Errorf("bootstrap: %w", ErrPopulationMismatch)
	}

	author, distractor, clustering, err := bootstrapClusters(distances)
	if err != nil {
		return Intervals{}, err
	}

	low := make([]float64, 0, spec.Resamples)
	high := make([]float64, 0, spec.Resamples)
	authorRandom := splitMix64{state: spec.Seed + 1}
	distractorRandom := splitMix64{state: spec.Seed + 2}
	for resample := 0; resample < spec.Resamples; resample++ {
		trial := make([]ClassedDistance, 0, len(distances))
		trial = appendDrawnClusters(trial, author, &authorRandom)
		trial = appendDrawnClusters(trial, distractor, &distractorRandom)
		// Re-running Calibrate for every resample is deliberate: it makes the
		// resample threshold rule identical to the real one by construction.
		// The redundant validation is the price; without it a later optimization
		// could silently remove that guarantee.
		thresholds, err := Calibrate(trial, t.Source, t.Targets)
		if err != nil {
			continue
		}
		low = append(low, thresholds.Low)
		high = append(high, thresholds.High)
	}

	out := Intervals{
		ThresholdsID: t.ID, Spec: spec, Algorithm: BootstrapAlgorithm, Clustering: clustering,
		UnderstatesUncertainty: clustering == ClusterByDocument, AuthorClusters: len(author),
		DistractorClusters: len(distractor), Resamples: spec.Resamples, Qualified: len(low),
		Failed: spec.Resamples - len(low),
	}
	// A valid population's original cluster selection is reachable, but need not
	// occur in this finite draw; a sufficiently degenerate population can leave
	// every resample unqualified. This guard leaves those intervals uncomputed.
	if len(low) > 0 {
		out.Low = percentileInterval(low, spec.Confidence)
		out.High = percentileInterval(high, spec.Confidence)
	}
	out.Usable = float64(out.Qualified)/float64(out.Resamples) >= spec.MinQualified
	out.Actionable = out.Low.Upper < out.High.Lower
	out.Shippable = out.Usable && out.Actionable
	if !out.Usable {
		out.Reason = "insufficient-qualified-resamples"
	} else if !out.Actionable {
		out.Reason = "overlapping-intervals"
	}
	out.ID = intervalID(&out, author, distractor)
	return out, nil
}

func validBootstrap(spec BootstrapSpec) bool {
	return spec.Resamples > 0 && spec.Confidence > 0 && spec.Confidence < 1 && finite(spec.Confidence) &&
		spec.MinQualified > 0 && spec.MinQualified <= 1 && finite(spec.MinQualified)
}

type bootstrapCluster struct {
	label string
	items []ClassedDistance
}

func bootstrapClusters(distances []ClassedDistance) ([]bootstrapCluster, []bootstrapCluster, Clustering, error) {
	authorItems := make([]ClassedDistance, 0)
	distractorItems := make([]ClassedDistance, 0)
	for _, item := range distances {
		if item.Document == "" {
			return nil, nil, "", fmt.Errorf("bootstrap: %w: %s distance", ErrMissingCluster, item.Class)
		}
		switch item.Class {
		case ClassAuthor:
			authorItems = append(authorItems, item)
		case ClassDistractor:
			distractorItems = append(distractorItems, item)
		}
	}

	anyAuthor, allAuthor := false, true
	for _, item := range distractorItems {
		if item.Author != "" {
			anyAuthor = true
		} else {
			allAuthor = false
		}
	}
	if anyAuthor && !allAuthor {
		return nil, nil, "", fmt.Errorf("bootstrap: %w", ErrPartialAuthorLabels)
	}
	author := groupClusters(authorItems, func(item ClassedDistance) string { return item.Document })
	if allAuthor {
		return author, groupClusters(distractorItems, func(item ClassedDistance) string { return item.Author }), ClusterByDocumentAndAuthor, nil
	}
	return author, groupClusters(distractorItems, func(item ClassedDistance) string { return item.Document }), ClusterByDocument, nil
}

func groupClusters(items []ClassedDistance, label func(ClassedDistance) string) []bootstrapCluster {
	byLabel := make(map[string][]ClassedDistance)
	for _, item := range items {
		byLabel[label(item)] = append(byLabel[label(item)], item)
	}
	out := make([]bootstrapCluster, 0, len(byLabel))
	for name, members := range byLabel {
		out = append(out, bootstrapCluster{label: name, items: members})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out
}

func appendDrawnClusters(out []ClassedDistance, clusters []bootstrapCluster, random *splitMix64) []ClassedDistance {
	for range clusters {
		cluster := clusters[random.next()%uint64(len(clusters))]
		out = append(out, cluster.items...)
	}
	return out
}

type splitMix64 struct{ state uint64 }

func (s *splitMix64) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ z>>30) * 0xBF58476D1CE4E5B9
	z = (z ^ z>>27) * 0x94D049BB133111EB
	return z ^ z>>31
}

func percentileInterval(values []float64, confidence float64) Interval {
	sort.Float64s(values)
	n := len(values)
	alpha := 1 - confidence
	lower := int(math.Floor(alpha / 2 * float64(n)))
	upper := int(math.Floor((1 - alpha/2) * float64(n)))
	if upper >= n {
		upper = n - 1
	}
	return Interval{Lower: values[lower], Upper: values[upper]}
}

func intervalID(intervals *Intervals, author, distractor []bootstrapCluster) string {
	return identity.HashInputs(map[string]string{
		"algorithm": BootstrapAlgorithm, "author-clusters": strconv.Itoa(intervals.AuthorClusters),
		"author-membership": clusterMembershipID(author),
		"clustering":        string(intervals.Clustering), "confidence": numberID(intervals.Spec.Confidence),
		"distractor-clusters": strconv.Itoa(intervals.DistractorClusters), "min-qualified": numberID(intervals.Spec.MinQualified),
		"distractor-membership": clusterMembershipID(distractor),
		"resamples":             strconv.Itoa(intervals.Spec.Resamples), "seed": strconv.FormatUint(intervals.Spec.Seed, 10),
		"thresholds-id": intervals.ThresholdsID,
	})
}

// clusterMembershipID canonically records the partition that generated an interval.
func clusterMembershipID(clusters []bootstrapCluster) string {
	canonical := append([]bootstrapCluster(nil), clusters...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].label < canonical[j].label })

	parts := make([]string, len(canonical))
	for index, cluster := range canonical {
		members := append([]ClassedDistance(nil), cluster.items...)
		sort.Slice(members, func(i, j int) bool {
			left, right := clusterMemberID(members[i]), clusterMemberID(members[j])
			return left < right
		})
		memberIDs := make([]string, len(members))
		for memberIndex, member := range members {
			memberIDs[memberIndex] = clusterMemberID(member)
		}
		parts[index] = string(identity.Frame(append([]string{cluster.label}, memberIDs...)...))
	}

	return string(identity.Frame(parts...))
}

func clusterMemberID(item ClassedDistance) string {
	return string(identity.Frame(string(item.Class), item.Document, item.Author, numberID(item.Distance.Value)))
}
