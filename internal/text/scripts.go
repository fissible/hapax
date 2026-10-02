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
// original. This doc comment is the authoritative statement of the predicate;
// callers and tests link here rather than restating it.
//
// # Contract
//
// A script is named when ALL THREE hold:
//
//	original.Share(name) < established    OR original.Count(name) == 0
//	s.Share(name) > ceiling
//	(s.Count(name) > original.Count(name) AND s.Share(name) > original.Share(name))
//	    OR s.Share(name) >= established
//
// Five consequences that follow from that and are easy to get wrong. Every
// number below is measured at the declared parameters, established=0.25 and
// ceiling=0.05:
//
//   - Crossing the ceiling is NOT sufficient. The third line must also hold, so
//     a script whose count is unchanged and whose share stays under established
//     may cross the ceiling freely: 96 Latin + 4 Greek rewritten to 74 + 4
//     takes Greek from 0.0400 to 0.0513 and is not named.
//   - Refusal does NOT require growth. The second arm of the third line is a
//     takeover: 80 Latin + 20 Greek rewritten to 60 + 20 names Greek on an
//     unchanged count, because the paragraph shrank around it.
//   - The ceiling is a threshold on a ratio and moves with either term. 96 + 4
//     to 114 + 6 is exactly 0.0500 and is not named; to 113 + 6 is 0.0504 and
//     is.
//   - The conjunctive growth arm permits dilution: a rising count whose share
//     does not rise is not named. 10 Latin + 1 Greek to 23 + 2 takes Greek
//     from 0.0909 to 0.0800 on a doubled count and is not named. Both shares
//     exceed the ceiling, so its line does not screen the candidate off.
//     Proportional scaling is the boundary case.
//   - SHORTENING may remove script letters too: 80 Latin + 20 Greek to 60 + 15
//     is 0.2000 and is not named. To 30 + 15 is 0.3333 and Greek is named on a
//     count that fell. The takeover arm prevents spending a banked count by
//     deleting everything else.
//
// Both comparisons against established are inclusive, and they point opposite
// ways: a script already AT the threshold in the original is exempt, and a
// candidate arriving exactly AT it is named. The second half holds only while
// ceiling < established — with the two equal the ceiling line admits the
// candidate first, so at established = ceiling = 0.25, 80 Latin + 20 Greek to
// 60 + 20 lands on 0.2500 and is not named.
//
// # Scripts, not languages
//
// A script at or above established in original is EXEMPT FROM THIS GUARD. It is
// not thereby identified as a language the paragraph is written in — this counts
// scripts and cannot tell Japanese from Chinese — and it is not exempt from the
// separate introduction guard, which has its own anchor.
//
// The two thresholds are parameters because the measurement owns no policy; the
// declared values and the argument for them live with the rejection codes in
// internal/rewrite. Neither set is disturbed by asking.
func (s ScriptSet) Exceeding(original ScriptSet, established, ceiling float64) []string {
	var names []string
	for name, count := range s.counts {
		if original.counts[name] > 0 && original.Share(name) >= established {
			continue
		}
		if s.Share(name) <= ceiling {
			continue
		}
		if (count > original.counts[name] && s.Share(name) > original.Share(name)) || s.Share(name) >= established {
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
