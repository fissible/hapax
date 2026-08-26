// Package text admits UTF-8 documents and preserves raw-byte span coordinates.
package text

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sync"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

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
