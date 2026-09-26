package text

import (
	"sort"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ScriptSet records letter counts by Unicode script. Its zero value is empty.
type ScriptSet struct {
	counts  map[string]int
	letters int
}

// Scripts counts letters after NFC normalization, attributing them using
// unicode.Scripts. Common and Inherited letters contribute neither a script
// count nor a letter to the total. This measures scripts, not languages.
func Scripts(input string) ScriptSet {
	set := ScriptSet{counts: make(map[string]int)}
	for _, r := range norm.NFC.String(input) {
		if !unicode.IsLetter(r) {
			continue
		}
		for name, table := range unicode.Scripts {
			if name == "Common" || name == "Inherited" {
				continue
			}
			if unicode.Is(table, r) {
				set.counts[name]++
				set.letters++
				break
			}
		}
	}
	return set
}

// Letters returns the total number of letters attributed to counted scripts.
func (s ScriptSet) Letters() int {
	return s.letters
}

// Share returns the fraction of counted letters attributed to name.
// An absent script or an empty set has a share of zero.
func (s ScriptSet) Share(name string) float64 {
	if s.letters == 0 {
		return 0
	}
	return float64(s.counts[name]) / float64(s.letters)
}

// Names returns the scripts present, sorted and without duplicates.
func (s ScriptSet) Names() []string {
	var names []string
	for name := range s.counts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Count is a STUB for phase-1 verification only.
func (s ScriptSet) Count(name string) int { return 0 }

// Exceeding is a STUB for phase-1 verification only.
func (s ScriptSet) Exceeding(original ScriptSet, ceiling float64) []string { return nil }

// Introduced returns the sorted scripts present in s and absent from current,
// regardless of their shares. It does not change either set.
func (s ScriptSet) Introduced(current ScriptSet) []string {
	var names []string
	for name := range s.counts {
		if current.counts[name] == 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
