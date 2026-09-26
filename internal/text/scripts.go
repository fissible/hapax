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

// Count returns the letters attributed to name. An absent script, an undeclared
// name and the zero value all count zero.
//
// Read from the counts directly rather than recovered from Share: the round trip
// through float64 truncates, and 15 of 22 comes back 14.
func (s ScriptSet) Count(name string) int { return s.counts[name] }

// Exceeding returns the sorted scripts of s that grew out of proportion to
// original: not established there, over ceiling of s, and either using more
// letters than original did or taking at least established of s.
//
// A script at or above established in original is one of the languages that
// paragraph is written in and is not constrained — a bilingual paragraph may be
// rewritten in either of its languages. The count arm keeps a SHORTENING rewrite
// admissible; the share arm stops a candidate spending a banked count by
// deleting everything else. Both comparisons against established are inclusive,
// so a rewrite may keep a script at the threshold and may never climb to it.
//
// The ceiling is a parameter because the measurement owns no policy. Neither set
// is disturbed by asking.
func (s ScriptSet) Exceeding(original ScriptSet, established, ceiling float64) []string {
	var names []string
	for name, count := range s.counts {
		if original.counts[name] > 0 && original.Share(name) >= established {
			continue
		}
		if s.Share(name) <= ceiling {
			continue
		}
		if count > original.counts[name] || s.Share(name) >= established {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

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
