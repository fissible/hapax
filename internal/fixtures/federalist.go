// Package fixtures provides vendored corpora used by end-to-end tests.
package fixtures

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

//go:embed federalist.txt
var federalistText string

// Attribution identifies the author label printed by this edition.
type Attribution string

const (
	Hamilton           Attribution = "Hamilton"
	Madison            Attribution = "Madison"
	Jay                Attribution = "Jay"
	HamiltonAndMadison Attribution = "HamiltonAndMadison"
	Disputed           Attribution = "Disputed"
)

// Paper is one paper as it occurs in the vendored edition.
type Paper struct {
	Index       int
	Number      int
	Roman       string
	Title       string
	Text        string
	Attribution Attribution
	Duplicate   bool
}

// Source records the provenance of a vendored corpus.
type Source struct {
	SHA256    string
	URL       string
	Edition   string
	Retrieved string
	License   string
}

// FederalistSource describes the exact edition from which federalist.txt was
// extracted. The file contains only the public-domain work, not Gutenberg's
// header or licence boilerplate.
func FederalistSource() Source {
	return Source{
		SHA256:    "a6c9d1135a04d10955fe11d210b7f642e1c2341d4f2c8369b9a832cc97839d94",
		URL:       "https://www.gutenberg.org/cache/epub/18/pg18.txt",
		Edition:   "Project Gutenberg ebook 18",
		Retrieved: "2026-08-26",
		License:   "The underlying work is public domain in the United States.",
	}
}

var (
	federalistOnce   sync.Once
	federalistPapers []Paper
	federalistErr    error
	paperLine        = regexp.MustCompile(`^No[.] ([IVXLCDM]+)[.]$`)
	dateLine         = regexp.MustCompile(`^(?:(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday),? )?(January|February|March|April|May|June|July|August|September|October|November|December) [0-9]{1,2}, [0-9]{4}[.]?$`)
)

// Federalist returns the papers in source order. Its data is embedded, so it
// neither reads from the working directory nor performs network I/O.
func Federalist() ([]Paper, error) {
	federalistOnce.Do(func() {
		federalistPapers, federalistErr = parseFederalist(federalistText)
	})
	if federalistErr != nil {
		return nil, federalistErr
	}
	return append([]Paper(nil), federalistPapers...), nil
}

type sourceLine struct {
	text string
}

func parseFederalist(source string) ([]Paper, error) {
	lines := splitLines(source)
	type paperStart struct {
		header int
		no     int
		roman  string
	}
	var starts []paperStart
	for i, line := range lines {
		matches := paperLine.FindStringSubmatch(strings.TrimSpace(line.text))
		if matches == nil {
			continue
		}
		if i == 0 || strings.TrimSpace(lines[i-1].text) != "THE FEDERALIST." {
			continue
		}
		starts = append(starts, paperStart{header: i - 1, no: i, roman: matches[1]})
	}
	if len(starts) == 0 {
		return nil, fmt.Errorf("federalist corpus contains no paper headers")
	}

	papers := make([]Paper, 0, len(starts))
	for index, start := range starts {
		bodyEnd := len(lines)
		if index+1 < len(starts) {
			bodyEnd = starts[index+1].header
		}

		attributionLine, attribution, err := findAttribution(lines, start.no+1, bodyEnd)
		if err != nil {
			return nil, fmt.Errorf("paper %s: %w", start.roman, err)
		}
		title := titleFrom(lines[start.no+1 : attributionLine])
		if title == "" {
			return nil, fmt.Errorf("paper %s has no title", start.roman)
		}

		papers = append(papers, Paper{
			Index:       index,
			Number:      RomanToInt(start.roman),
			Roman:       start.roman,
			Title:       title,
			Text:        bodyFrom(lines[attributionLine+1 : bodyEnd]),
			Attribution: attribution,
		})
	}

	occurrences := make(map[int]int, len(papers))
	for _, paper := range papers {
		occurrences[paper.Number]++
	}
	for i := range papers {
		papers[i].Duplicate = occurrences[papers[i].Number] > 1
	}
	return papers, nil
}

// bodyFrom preserves every non-trailing line verbatim, including indentation.
// The recurring edition header separates papers and belongs to neither body.
func bodyFrom(lines []sourceLine) string {
	end := len(lines)
	for end > 0 {
		line := lines[end-1].text
		if line != "" && line != "THE FEDERALIST." {
			break
		}
		end--
	}

	body := make([]string, end)
	for i := range body {
		body[i] = lines[i].text
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}

func splitLines(s string) []sourceLine {
	lines := make([]sourceLine, 0)
	for start := 0; start < len(s); {
		end := strings.IndexByte(s[start:], '\n')
		if end < 0 {
			lines = append(lines, sourceLine{text: s[start:]})
			break
		}
		end += start
		lines = append(lines, sourceLine{text: s[start:end]})
		start = end + 1
	}
	return lines
}

func findAttribution(lines []sourceLine, from, until int) (int, Attribution, error) {
	for i := from; i < until; i++ {
		switch strings.TrimSpace(lines[i].text) {
		case "HAMILTON":
			return i, Hamilton, nil
		case "MADISON":
			return i, Madison, nil
		case "JAY":
			return i, Jay, nil
		case "HAMILTON AND MADISON":
			return i, HamiltonAndMadison, nil
		case "HAMILTON OR MADISON":
			return i, Disputed, nil
		}
	}
	return 0, "", fmt.Errorf("has no attribution line")
}

func titleFrom(lines []sourceLine) string {
	title := make([]string, 0, len(lines))
	for _, line := range lines {
		value := strings.TrimSpace(line.text)
		if value == "" || strings.HasPrefix(value, "For ") || strings.HasPrefix(value, "From ") || dateLine.MatchString(value) || strings.HasPrefix(value, "(There are two slightly different versions") {
			continue
		}
		title = append(title, value)
	}
	return strings.Join(title, " ")
}

// RomanToInt converts a Roman numeral to its integer value. It returns zero
// for the empty string or a numeral containing an unsupported character.
func RomanToInt(roman string) int {
	values := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}
	total := 0
	previous := 0
	for i := len(roman) - 1; i >= 0; i-- {
		value, ok := values[roman[i]]
		if !ok {
			return 0
		}
		if value < previous {
			total -= value
		} else {
			total += value
			previous = value
		}
	}
	return total
}
