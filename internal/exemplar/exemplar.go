// Package exemplar selects structurally diverse, representative Train leaves.
package exemplar

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/fissible/hapax/internal/corpus"
	"github.com/fissible/hapax/internal/deviation"
	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/profile"
	"github.com/fissible/hapax/internal/text"
)

var (
	ErrMissingInput         = errors.New("exemplar missing input")
	ErrInvalidConfig        = errors.New("exemplar invalid config")
	ErrPopulationTooSmall   = errors.New("exemplar population too small")
	ErrInsufficientEligible = errors.New("exemplar insufficient eligible candidates")
	ErrSplit                = errors.New("exemplar candidates must use train split")
	ErrManifestMismatch     = errors.New("exemplar feature manifest mismatch")
	ErrDuplicateIdentity    = errors.New("exemplar duplicate identity")
)

type Config struct{ N int }

func DefaultConfig() Config { return Config{N: 3} }

type Candidate struct {
	DocumentDigest string
	Span           text.Span
	Role           text.Role
	Containers     []text.ContainerKind
	Split          corpus.Split
	Text           string
	Vector         features.Vector
}

func (c Candidate) Identity() string {
	return c.DocumentDigest + ":" + strconv.Itoa(c.Span.Offset) + ":" + strconv.Itoa(c.Span.Length)
}

func (c Candidate) Stratum() string {
	parts := make([]string, len(c.Containers))
	for i, container := range c.Containers {
		parts[i] = string(container)
	}
	return string(c.Role) + "|" + strings.Join(parts, "/")
}

type Density struct {
	Identity   string
	Value      float64
	Neighbours int
	Eligible   bool
}
type StratumAssignment struct{ Identity, Stratum string }
type Medoid struct {
	Round             int
	Stratum, Identity string
	Sum               float64
}
type Certificate struct {
	ID, SelectionID      string
	K                    int
	Population, Eligible []string
	Density              []Density
	Strata               []StratumAssignment
	Medoids              []Medoid
	Ties                 []string
	Config               Config
}
type Selection struct {
	ID          string
	Exemplars   []Candidate
	Certificate Certificate
}

type item struct {
	candidate   Candidate
	id, stratum string
	values      []float64
	defined     []bool
}
type pair struct {
	valid    bool
	distance float64
}
type neighbour struct {
	index    int
	distance float64
}
type stratum struct {
	key     string
	members []int
}

// Select produces exactly cfg.N representative leaves or refuses the pool.
func Select(prof *profile.Profile, candidates []Candidate, cfg Config) (Selection, error) {
	if prof == nil {
		return Selection{}, fmt.Errorf("select: %w", ErrMissingInput)
	}
	if cfg.N <= 0 {
		return Selection{}, fmt.Errorf("select: %w", ErrInvalidConfig)
	}
	if len(candidates) < max(30, 10*cfg.N) {
		return Selection{}, fmt.Errorf("select: %w: have %d need %d", ErrPopulationTooSmall, len(candidates), max(30, 10*cfg.N))
	}
	fitted, err := prof.Fitted()
	if err != nil {
		return Selection{}, fmt.Errorf("select: %w", err)
	}

	items := make([]item, len(candidates))
	for i, candidate := range candidates {
		if candidate.Split != corpus.Train {
			return Selection{}, fmt.Errorf("select: %w: %q", ErrSplit, candidate.Split)
		}
		if candidate.Vector.SetVersion != features.SetVersion {
			return Selection{}, fmt.Errorf("select: %w", ErrManifestMismatch)
		}
		items[i] = item{candidate: candidate, id: candidate.Identity(), stratum: candidate.Stratum()}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
	for i := 1; i < len(items); i++ {
		if items[i-1].id == items[i].id {
			return Selection{}, fmt.Errorf("select: %w: %s", ErrDuplicateIdentity, items[i].id)
		}
	}

	// Standardize is the shared, profile-bound formula. It returns manifest order.
	for i := range items {
		standardized, err := deviation.Standardize(items[i].candidate.Vector, fitted, corpus.Train)
		if err != nil {
			if errors.Is(err, deviation.ErrManifestMismatch) {
				return Selection{}, fmt.Errorf("select: %w", ErrManifestMismatch)
			}
			return Selection{}, fmt.Errorf("select standardize %s: %w", items[i].id, err)
		}
		items[i].values = make([]float64, len(standardized.Values))
		items[i].defined = make([]bool, len(standardized.Values))
		for j, value := range standardized.Values {
			items[i].values[j], items[i].defined[j] = value.Value, value.Defined
		}
	}

	definitions := features.Definitions()

	// Rank each manifest feature against the defined values in this closed pool.
	for feature := range definitions {
		refs := make([]float64, 0, len(items))
		for i := range items {
			if items[i].defined[feature] {
				refs = append(refs, items[i].values[feature])
			}
		}
		sort.Float64s(refs)
		for i := range items {
			if !items[i].defined[feature] {
				continue
			}
			value := items[i].values[feature]
			lower := sort.SearchFloat64s(refs, value)
			upper := sort.Search(len(refs), func(j int) bool { return refs[j] > value })
			u := (float64(lower) + float64(upper-lower)/2 + .5) / float64(len(refs)+1)
			items[i].values[feature] = math.Sqrt2 * math.Erfinv(2*u-1)
		}
	}

	k := max(3, min(15, int(math.Floor(math.Sqrt(float64(len(items)))))))
	floor := (len(definitions) + 1) / 2
	pairs := make([][]pair, len(items))
	for i := range pairs {
		pairs[i] = make([]pair, len(items))
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			shared, sum := 0, 0.0
			for f := range definitions {
				if items[i].defined[f] && items[j].defined[f] {
					shared++
					sum += math.Abs(items[i].values[f] - items[j].values[f])
				}
			}
			if shared >= floor {
				pairs[i][j] = pair{true, sum / float64(shared)}
				pairs[j][i] = pairs[i][j]
			}
		}
	}

	density := make([]Density, len(items))
	qualifiers := make([]int, 0, len(items))
	for i := range items {
		near := make([]neighbour, 0, len(items)-1)
		for j := range items {
			if pairs[i][j].valid {
				near = append(near, neighbour{j, pairs[i][j].distance})
			}
		}
		sort.Slice(near, func(a, b int) bool {
			if near[a].distance != near[b].distance {
				return near[a].distance < near[b].distance
			}
			return items[near[a].index].id < items[near[b].index].id
		})
		density[i] = Density{Identity: items[i].id, Neighbours: len(near)}
		if len(near) >= k {
			for j := 0; j < k; j++ {
				density[i].Value += near[j].distance
			}
			density[i].Value /= float64(k)
			qualifiers = append(qualifiers, i)
		}
	}
	sort.Slice(qualifiers, func(a, b int) bool {
		da, db := density[qualifiers[a]].Value, density[qualifiers[b]].Value
		if da != db {
			return da < db
		}
		return items[qualifiers[a]].id < items[qualifiers[b]].id
	})
	keep := min(len(qualifiers), int(math.Ceil(.75*float64(len(items)))))
	ties := []string{}
	if keep > 0 && keep < len(qualifiers) && density[qualifiers[keep-1]].Value == density[qualifiers[keep]].Value {
		ties = append(ties, "eligibility:-:"+items[qualifiers[keep-1]].id)
	}
	for i := 0; i < keep; i++ {
		density[qualifiers[i]].Eligible = true
	}
	eligible := make([]string, 0, keep)
	for i := range items {
		if density[i].Eligible {
			eligible = append(eligible, items[i].id)
		}
	}
	if len(eligible) < cfg.N {
		return Selection{}, fmt.Errorf("select: %w: have %d need %d", ErrInsufficientEligible, len(eligible), cfg.N)
	}

	groups := make([]stratum, 0)
	for i := range items {
		if !density[i].Eligible {
			continue
		}
		at := -1
		for j := range groups {
			if groups[j].key == items[i].stratum {
				at = j
				break
			}
		}
		if at < 0 {
			groups = append(groups, stratum{key: items[i].stratum})
			at = len(groups) - 1
		}
		groups[at].members = append(groups[at].members, i)
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].members) != len(groups[j].members) {
			return len(groups[i].members) > len(groups[j].members)
		}
		return groups[i].key < groups[j].key
	})
	for begin := 0; begin < len(groups); {
		end := begin + 1
		for end < len(groups) && len(groups[end].members) == len(groups[begin].members) {
			end++
		}
		if end-begin > 1 {
			ties = append(ties, "stratum-order:-:"+groups[begin].key)
		}
		begin = end
	}

	remaining := make([][]int, len(groups))
	for i := range groups {
		remaining[i] = append([]int(nil), groups[i].members...)
	}
	chosen := make([]int, 0, cfg.N)
	medoids := make([]Medoid, 0, cfg.N)
	for round := 1; len(chosen) < cfg.N; round++ {
		picked := false
		for g := range groups {
			if len(chosen) == cfg.N {
				break
			}
			winner, sum, tied, ok := medoid(remaining[g], items, pairs)
			if !ok {
				continue
			}
			if tied {
				ties = append(ties, "medoid:"+strconv.Itoa(round)+":"+items[winner].id)
			}
			chosen = append(chosen, winner)
			medoids = append(medoids, Medoid{Round: round, Stratum: groups[g].key, Identity: items[winner].id, Sum: sum})
			for i, index := range remaining[g] {
				if index == winner {
					remaining[g] = append(remaining[g][:i], remaining[g][i+1:]...)
					break
				}
			}
			picked = true
		}
		if !picked {
			return Selection{}, fmt.Errorf("select: %w", ErrInsufficientEligible)
		}
	}

	population := make([]string, len(items))
	assignments := make([]StratumAssignment, len(items))
	rows := make([]Density, len(items))
	for i := range items {
		population[i] = items[i].id
		assignments[i] = StratumAssignment{items[i].id, items[i].stratum}
		rows[i] = density[i]
	}
	exemplars := make([]Candidate, len(chosen))
	selectedIDs := make([]string, len(chosen))
	for i, index := range chosen {
		exemplars[i] = items[index].candidate
		selectedIDs[i] = items[index].id
	}
	selectionID := identity.HashBytes(identity.Frame(selectedIDs...))
	certificate := Certificate{SelectionID: selectionID, K: k, Population: population, Eligible: eligible, Density: rows, Strata: assignments, Medoids: medoids, Ties: ties, Config: cfg}
	certificate.ID = certificateID(certificate, prof.ID)
	return Selection{ID: selectionID, Exemplars: exemplars, Certificate: certificate}, nil
}

func medoid(remaining []int, items []item, pairs [][]pair) (int, float64, bool, bool) {
	if len(remaining) == 1 {
		return remaining[0], 0, false, true
	}
	winner, best, tied, found := -1, 0.0, false, false
	for _, candidate := range remaining {
		sum, count := 0.0, 0
		for _, other := range remaining {
			if candidate != other && pairs[candidate][other].valid {
				sum += pairs[candidate][other].distance
				count++
			}
		}
		if count == 0 {
			continue
		}
		if !found || sum < best {
			winner, best, tied, found = candidate, sum, false, true
		} else if sum == best {
			tied = true
			if items[candidate].id < items[winner].id {
				winner = candidate
			}
		}
	}
	return winner, best, tied, found
}

func certificateID(c Certificate, profileID string) string {
	density := make([]string, len(c.Density))
	for i, d := range c.Density {
		density[i] = d.Identity + "=" + numberID(d.Value)
	}
	strata := make([]string, len(c.Strata))
	for i, s := range c.Strata {
		strata[i] = s.Identity + "=" + s.Stratum
	}
	medoids := make([]string, len(c.Medoids))
	for i, m := range c.Medoids {
		medoids[i] = strconv.Itoa(m.Round) + ":" + m.Stratum + ":" + m.Identity + ":" + numberID(m.Sum)
	}
	binding := []string{"profile=" + profileID, "text=" + text.ContractVersion, "structure=" + text.StructureVersion, "manifest=" + features.ManifestDigest()}
	config := []string{"n=" + strconv.Itoa(c.Config.N), "k=" + strconv.Itoa(c.K), "min-population-absolute=30", "min-population-multiple=10", "shared-feature-fraction=0.5", "k-min=3", "k-max=15", "eligibility-fraction=0.75"}
	return identity.HashInputs(map[string]string{"selection": c.SelectionID, "population": string(identity.Frame(c.Population...)), "eligible": string(identity.Frame(c.Eligible...)), "density": string(identity.Frame(density...)), "strata": string(identity.Frame(strata...)), "medoids": string(identity.Frame(medoids...)), "binding": string(identity.Frame(binding...)), "ties": string(identity.Frame(c.Ties...)), "config": string(identity.Frame(config...))})
}

func numberID(value float64) string {
	if value == 0 {
		value = 0
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
