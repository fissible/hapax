// Package text admits UTF-8 documents and preserves raw-byte span coordinates.
package text

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// ContractVersion is asserted provenance for the admission and tokenization
// contract consumed by downstream corpus snapshots. It does not enforce a
// bump: the CI guard that would require contract changes to touch this
// constant is not built yet.
const ContractVersion = "text-v1"

// ErrSpanOutOfBounds reports a span that does not fit within the raw document.
var ErrSpanOutOfBounds = errors.New("span is outside document bounds")

// ErrSpanNotGraphemeBoundary reports a span that does not align to grapheme
// cluster boundaries. Call Snap before Resolve when expanding the span is
// acceptable.
var ErrSpanNotGraphemeBoundary = errors.New("span is not a grapheme boundary")

// Span identifies a half-open range of raw document bytes.
type Span struct {
	Offset int
	Length int
}

// TokenClass identifies the role a token has in the text.
type TokenClass string

const (
	Word        TokenClass = "word"
	Number      TokenClass = "number"
	Punctuation TokenClass = "punctuation"
	Symbol      TokenClass = "symbol"
)

// Token is a raw-byte span and its NFC text. Only word tokens are lexical.
type Token struct {
	Span        Span
	Text        string
	Class       TokenClass
	Contraction bool
	Possessive  bool
	Lexical     bool
}

// AdmissionError reports malformed UTF-8 at an offset in the original input.
type AdmissionError struct {
	Offset int
}

func (e *AdmissionError) Error() string {
	return fmt.Sprintf("invalid UTF-8 at byte offset %d", e.Offset)
}

// Document is an admitted UTF-8 document. Its bytes are immutable to callers.
//
// Document must not be copied after first use: it contains a sync.Once. Pass
// and retain *Document values instead.
type Document struct {
	raw        []byte
	hadBOM     bool
	normalized string
	normalize  sync.Once
	tokens     []Token
	tokenize   sync.Once
	boundaries []byte
}

// Admit validates raw as UTF-8, strips one leading UTF-8 BOM, and records
// grapheme-cluster boundaries in the resulting byte coordinate system.
func Admit(raw []byte) (*Document, error) {
	if offset, ok := invalidUTF8Offset(raw); ok {
		return nil, &AdmissionError{Offset: offset}
	}

	hadBOM := bytes.HasPrefix(raw, utf8BOM)
	if hadBOM {
		raw = raw[len(utf8BOM):]
	}

	doc := &Document{
		raw:        append([]byte(nil), raw...),
		hadBOM:     hadBOM,
		boundaries: make([]byte, (len(raw)+8)/8),
	}
	doc.indexBoundaries()

	return doc, nil
}

func invalidUTF8Offset(raw []byte) (int, bool) {
	for offset := 0; offset < len(raw); {
		_, size := utf8.DecodeRune(raw[offset:])
		if size == 1 && raw[offset] >= utf8.RuneSelf {
			return offset, true
		}
		offset += size
	}
	return 0, false
}

func (d *Document) indexBoundaries() {
	d.addBoundary(0)
	rest := d.raw
	state := -1
	for len(rest) > 0 {
		cluster, next, _, nextState := uniseg.FirstGraphemeCluster(rest, state)
		start := len(d.raw) - len(rest)
		end := start + len(cluster)
		d.addBoundary(start)
		d.addBoundary(end)
		rest, state = next, nextState
	}
}

func (d *Document) addBoundary(offset int) {
	d.boundaries[offset/8] |= 1 << uint(offset%8)
}

func (d *Document) isBoundary(offset int) bool {
	return d.boundaries[offset/8]&(1<<uint(offset%8)) != 0
}

// Raw returns a copy of the BOM-stripped admitted bytes.
func (d *Document) Raw() []byte {
	return append([]byte(nil), d.raw...)
}

// HadBOM reports whether Admit stripped a leading UTF-8 BOM.
func (d *Document) HadBOM() bool {
	return d.hadBOM
}

// Normalized returns the document's NFC form without changing its raw bytes.
func (d *Document) Normalized() string {
	d.normalize.Do(func() {
		d.normalized = norm.NFC.String(string(d.raw))
	})
	return d.normalized
}

// Tokens returns the document's Slice 2a tokens. Spans address raw bytes while
// Text is NFC-normalized. The returned slice is a copy and may be changed by
// the caller without affecting the document's cached tokenization.
func (d *Document) Tokens() []Token {
	return append([]Token(nil), d.cachedTokens()...)
}

// cachedTokens returns the document-owned tokenization. Callers must treat the
// returned slice as immutable; public consumers should use Tokens instead.
func (d *Document) cachedTokens() []Token {
	d.tokenize.Do(func() {
		d.tokens = d.tokenizeRaw()
	})
	return d.tokens
}

func (d *Document) tokenizeRaw() []Token {
	var tokens []Token
	for offset := 0; offset < len(d.raw); {
		r, size := utf8.DecodeRune(d.raw[offset:])
		if unicode.IsSpace(r) {
			offset += size
			continue
		}

		if isASCIIDigit(r) {
			if end := d.numberEnd(offset); end > offset {
				if next, _ := runeAt(d.raw, end); !isWordRune(next) {
					tokens = append(tokens, d.token(offset, end, Number))
					offset = end
					continue
				}
			}
		}

		if isWordRune(r) {
			end := d.wordEnd(offset)
			token := d.token(offset, end, Word)
			token.Lexical = true
			token.Contraction, token.Possessive = apostropheFlags(string(d.raw[offset:end]))
			tokens = append(tokens, token)
			offset = end
			continue
		}

		class := Punctuation
		// Other_Number contains standalone numeric forms such as ½ and ⅓.
		// They are numbers but never lexical words.
		if unicode.Is(unicode.No, r) {
			class = Number
			// Symbols are limited to So and Sc. Sm (including U+2212 MINUS SIGN)
			// and Sk deliberately remain punctuation separators, as required by the
			// Slice 2a token boundary contract.
		} else if unicode.Is(unicode.So, r) || unicode.Is(unicode.Sc, r) {
			class = Symbol
		}
		tokens = append(tokens, d.token(offset, offset+size, class))
		offset += size
	}
	return tokens
}

func (d *Document) token(start, end int, class TokenClass) Token {
	return Token{
		Span:  Span{Offset: start, Length: end - start},
		Text:  norm.NFC.String(string(d.raw[start:end])),
		Class: class,
	}
}

func (d *Document) wordEnd(offset int) int {
	end := offset
	for end < len(d.raw) {
		r, size := utf8.DecodeRune(d.raw[end:])
		if isWordRune(r) {
			end += size
			continue
		}
		if isApostrophe(r) {
			next, _ := runeAt(d.raw, end+size)
			if isWordRune(next) {
				end += size
				continue
			}
			// A final apostrophe belongs to a possessive word.
			previous, _ := runeBefore(d.raw, offset)
			if isApostrophe(previous) {
				return end
			}
			return end + size
		}
		if isJoiningHyphen(r) {
			next, _ := runeAt(d.raw, end+size)
			if isWordRune(next) {
				end += size
				continue
			}
		}
		break
	}
	return end
}

// numberEnd recognizes the complete numeric grammar at offset. A malformed
// comma or decimal continuation is deliberately left for the main scanner.
func (d *Document) numberEnd(offset int) int {
	i := offset
	for i < len(d.raw) && d.raw[i] >= '0' && d.raw[i] <= '9' {
		i++
	}
	digitsEnd := i
	firstLen := digitsEnd - offset
	if i < len(d.raw) && d.raw[i] == ',' && firstLen <= 3 {
		for i < len(d.raw) && d.raw[i] == ',' {
			if i+4 > len(d.raw) || !threeDigits(d.raw[i+1:i+4]) {
				return digitsEnd
			}
			i += 4
		}
	}
	allowDecimal := !(offset >= 2 && d.raw[offset-1] == '.' && isASCIIDigit(rune(d.raw[offset-2])))
	if allowDecimal && i < len(d.raw) && d.raw[i] == '.' {
		start := i + 1
		for i = start; i < len(d.raw) && d.raw[i] >= '0' && d.raw[i] <= '9'; i++ {
		}
		if i == start {
			return start - 1
		}
		if i+1 < len(d.raw) && d.raw[i] == '.' && isASCIIDigit(rune(d.raw[i+1])) {
			return digitsEnd
		}
	}
	if i < len(d.raw) && (d.raw[i] == ',' || isASCIIDigit(rune(d.raw[i]))) {
		return digitsEnd
	}
	return i
}

func threeDigits(raw []byte) bool {
	return len(raw) == 3 && isASCIIDigit(rune(raw[0])) && isASCIIDigit(rune(raw[1])) && isASCIIDigit(rune(raw[2]))
}

func runeAt(raw []byte, offset int) (rune, int) {
	if offset >= len(raw) {
		return utf8.RuneError, 0
	}
	return utf8.DecodeRune(raw[offset:])
}

func runeBefore(raw []byte, offset int) (rune, int) {
	if offset == 0 {
		return utf8.RuneError, 0
	}
	r, size := utf8.DecodeLastRune(raw[:offset])
	return r, size
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// isWordRune treats non-ASCII decimal digits as lexical word characters. The
// numeric grammar is deliberately ASCII-only in English-only v1, so ٤٢ is one
// lexical Word rather than a Number. This is a known script-dependent
// limitation to revisit with a Unicode numeric grammar.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r)
}

func isApostrophe(r rune) bool { return r == '\'' || r == '’' }

func isJoiningHyphen(r rune) bool { return r == '-' || r == '‑' }

func apostropheFlags(raw string) (contraction, possessive bool) {
	if len(raw) == 0 {
		return false, false
	}
	canonical := strings.ReplaceAll(raw, "’", "'")
	if strings.HasSuffix(canonical, "'") {
		return false, true
	}
	lower := strings.ToLower(canonical)
	for _, suffix := range []string{"n't", "'re", "'ve", "'ll", "'d", "'m"} {
		if strings.HasSuffix(lower, suffix) {
			return true, false
		}
	}
	if strings.HasSuffix(lower, "'s") {
		host := strings.TrimSuffix(lower, "'s")
		if contractionSHosts[host] {
			return true, false
		}
		return false, true
	}
	return false, false
}

var contractionSHosts = map[string]bool{
	"it": true, "he": true, "she": true, "that": true, "there": true,
	"here": true, "what": true, "who": true, "where": true, "when": true,
	"how": true, "let": true,
}

// Snap clamps span to the document and expands it to grapheme boundaries.
func (d *Document) Snap(span Span) Span {
	start, end := d.clamp(span)
	start = d.boundaryAtOrBefore(start)
	end = d.boundaryAtOrAfter(end)
	return Span{Offset: start, Length: end - start}
}

func (d *Document) clamp(span Span) (int, int) {
	n := len(d.raw)
	start := clamp(span.Offset, 0, n)
	end := saturatingAdd(span.Offset, span.Length)
	if end < start {
		end = start
	}
	end = clamp(end, 0, n)
	return start, end
}

func clamp(value, lower, upper int) int {
	if value < lower {
		return lower
	}
	if value > upper {
		return upper
	}
	return value
}

func saturatingAdd(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	if b < 0 && a < math.MinInt-b {
		return math.MinInt
	}
	return a + b
}

func (d *Document) boundaryAtOrBefore(offset int) int {
	for offset >= 0 {
		if d.isBoundary(offset) {
			return offset
		}
		offset--
	}
	return 0
}

func (d *Document) boundaryAtOrAfter(offset int) int {
	for n := len(d.raw); offset <= n; offset++ {
		if d.isBoundary(offset) {
			return offset
		}
	}
	return len(d.raw)
}

// Resolve returns NFC text for a valid grapheme-aligned raw span.
func (d *Document) Resolve(span Span) (string, error) {
	if span.Offset < 0 || span.Length < 0 || span.Offset > len(d.raw) || span.Length > len(d.raw)-span.Offset {
		return "", ErrSpanOutOfBounds
	}
	end := span.Offset + span.Length
	if !d.isBoundary(span.Offset) {
		return "", ErrSpanNotGraphemeBoundary
	}
	if !d.isBoundary(end) {
		return "", ErrSpanNotGraphemeBoundary
	}
	return norm.NFC.String(string(d.raw[span.Offset:end])), nil
}
