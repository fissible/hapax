package text_test

import (
	"testing"

	"github.com/fissible/hapax/internal/text"
)

// snapDoc is laid out so every boundary rule has a hand-computable case.
//
//	byte:  0    1    2    3    4    5    6
//	       'a'  'e'  0xCC 0x81 '\r' '\n' 'b'
//	       └─a─┘└────é────┘└──CRLF──┘└─b─┘
//
// Grapheme clusters are "a"[0,1), "é"[1,4), "\r\n"[4,6), "b"[6,7).
// The only valid boundaries are therefore 0, 1, 4, 6 and 7.
const snapDoc = "a" + "é" + "\r\n" + "b"

func mustAdmit(t *testing.T, s string) *text.Document {
	t.Helper()
	doc, err := text.Admit([]byte(s))
	if err != nil {
		t.Fatalf("Admit(%q) returned error: %v", s, err)
	}
	return doc
}

func TestSnapLeavesValidBoundariesAlone(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	for _, s := range []text.Span{
		{Offset: 0, Length: 7},
		{Offset: 1, Length: 3},
		{Offset: 4, Length: 2},
		{Offset: 0, Length: 1},
		{Offset: 6, Length: 1},
		{Offset: 1, Length: 0}, // zero length on a valid boundary stays zero length
	} {
		if got := doc.Snap(s); got != s {
			t.Errorf("Snap(%+v) = %+v, want unchanged", s, got)
		}
	}
}

// A start boundary snaps backward and an end boundary snaps forward, so a span
// only ever grows to the nearest representable edge and can never silently drop
// authored content.
func TestSnapGrowsOutward(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	cases := []struct {
		name string
		in   text.Span
		want text.Span
	}{
		{"start inside combining sequence", text.Span{Offset: 2, Length: 1}, text.Span{Offset: 1, Length: 3}},
		{"start on combining mark", text.Span{Offset: 3, Length: 1}, text.Span{Offset: 1, Length: 3}},
		{"end inside combining sequence", text.Span{Offset: 0, Length: 2}, text.Span{Offset: 0, Length: 4}},
		{"end splits CRLF", text.Span{Offset: 0, Length: 5}, text.Span{Offset: 0, Length: 6}},
		{"start splits CRLF", text.Span{Offset: 5, Length: 2}, text.Span{Offset: 4, Length: 3}},
		{"both ends invalid", text.Span{Offset: 2, Length: 3}, text.Span{Offset: 1, Length: 5}},
		{"zero length mid-grapheme grows", text.Span{Offset: 2, Length: 0}, text.Span{Offset: 1, Length: 3}},
		{"zero length inside CRLF grows", text.Span{Offset: 5, Length: 0}, text.Span{Offset: 4, Length: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := doc.Snap(c.in); got != c.want {
				t.Errorf("Snap(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestSnapClampsToDocumentBounds(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	n := len(doc.Raw())
	cases := []struct {
		name string
		in   text.Span
		want text.Span
	}{
		{"negative offset", text.Span{Offset: -3, Length: 2}, text.Span{Offset: 0, Length: 0}}, // [-3,-1) clamps to the empty range at 0; there is nothing to grow around
		{"length past end", text.Span{Offset: 6, Length: 99}, text.Span{Offset: 6, Length: 1}},
		{"entirely past end", text.Span{Offset: 50, Length: 5}, text.Span{Offset: n, Length: 0}},
		{"negative length", text.Span{Offset: 4, Length: -2}, text.Span{Offset: 4, Length: 0}},
		{"spans whole doc and beyond", text.Span{Offset: -5, Length: 200}, text.Span{Offset: 0, Length: n}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := doc.Snap(c.in); got != c.want {
				t.Errorf("Snap(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

// Properties that must hold for every span over a document containing
// multi-byte sequences, combining marks, CRLF and a ZWJ emoji cluster.
func TestSnapProperties(t *testing.T) {
	docs := map[string]string{
		"mixed":      snapDoc,
		"emoji":      "x" + family + "y",
		"decomposed": "café résumé",
		"crlf only":  "\r\n\r\n",
		"ascii":      "plain ascii text",
		"empty":      "",
	}
	for name, src := range docs {
		t.Run(name, func(t *testing.T) {
			doc := mustAdmit(t, src)
			n := len(doc.Raw())
			for start := -2; start <= n+2; start++ {
				for length := -2; length <= n+2; length++ {
					in := text.Span{Offset: start, Length: length}
					got := doc.Snap(in)

					if got.Offset < 0 || got.Length < 0 || got.Offset+got.Length > n {
						t.Fatalf("Snap(%+v) = %+v, outside document [0,%d]", in, got, n)
					}

					// Never shrinks: the snapped span covers every byte of the
					// clamped input span.
					cs, ce := clampRange(in, n)
					if got.Offset > cs || got.Offset+got.Length < ce {
						t.Fatalf("Snap(%+v) = %+v, does not cover clamped input [%d,%d)", in, got, cs, ce)
					}

					// Idempotent: snapping an already-snapped span changes nothing.
					if again := doc.Snap(got); again != got {
						t.Fatalf("Snap not idempotent: Snap(%+v) = %+v, then %+v", in, got, again)
					}

					// A snapped span always resolves.
					if _, err := doc.Resolve(got); err != nil {
						t.Fatalf("Resolve(%+v) after Snap(%+v) returned error: %v", got, in, err)
					}
				}
			}
		})
	}
}

func clampRange(s text.Span, n int) (start, end int) {
	start = s.Offset
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	end = s.Offset + s.Length
	if end < start {
		end = start
	}
	if end > n {
		end = n
	}
	return start, end
}

// Resolve rehydrates raw bytes and applies NFC, which is what makes it safe to
// store spans instead of a second copy of the prose.
func TestResolveNormalizes(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	got, err := doc.Resolve(text.Span{Offset: 1, Length: 3})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != eAcuteComposed {
		t.Errorf("Resolve = %q, want %q (composed form)", got, eAcuteComposed)
	}
}

func TestResolveWholeDocumentEqualsNormalized(t *testing.T) {
	for _, src := range []string{snapDoc, "caf́e", "plain", "", "\r\n"} {
		doc := mustAdmit(t, src)
		got, err := doc.Resolve(text.Span{Offset: 0, Length: len(doc.Raw())})
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if want := doc.Normalized(); got != want {
			t.Errorf("Resolve(whole) = %q, want Normalized() = %q", got, want)
		}
	}
}

func TestResolveIsDeterministic(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	s := text.Span{Offset: 1, Length: 3}
	first, err := doc.Resolve(s)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := doc.Resolve(s)
		if err != nil {
			t.Fatalf("Resolve returned error on call %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("Resolve not deterministic: %q then %q", first, again)
		}
	}
}

// Resolve refuses spans it cannot honour rather than clamping silently. Callers
// are expected to Snap first; a bad span is a bug worth surfacing.
func TestResolveRejectsOutOfRangeSpans(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	n := len(doc.Raw())
	for _, s := range []text.Span{
		{Offset: -1, Length: 2},
		{Offset: 0, Length: n + 1},
		{Offset: n + 1, Length: 0},
		{Offset: 2, Length: -1},
	} {
		if _, err := doc.Resolve(s); err == nil {
			t.Errorf("Resolve(%+v) succeeded, want error", s)
		}
	}
}

// Resolve rejects any span whose boundaries are not grapheme-cluster boundaries.
// Returning partial content for an unsnapped span would silently hand back a
// broken cluster, which is exactly what the boundary rule exists to prevent.
func TestResolveRejectsNonBoundarySpans(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	for _, s := range []text.Span{
		{Offset: 2, Length: 1}, // starts inside the combining sequence
		{Offset: 0, Length: 2}, // ends inside the combining sequence
		{Offset: 0, Length: 5}, // ends between CR and LF
		{Offset: 5, Length: 2}, // starts between CR and LF
		{Offset: 3, Length: 0}, // zero length, mid-cluster
	} {
		got, err := doc.Resolve(s)
		if err == nil {
			t.Errorf("Resolve(%+v) = %q, want error: span boundaries split a grapheme cluster", s, got)
		}
	}
}

// Adjacent snapped spans tile the document: concatenating the resolved text of
// consecutive grapheme-aligned spans reconstructs the normalized document. This
// is what guarantees exemplar rehydration cannot lose or duplicate content.
func TestSnappedSpansTileTheDocument(t *testing.T) {
	doc := mustAdmit(t, snapDoc)
	n := len(doc.Raw())

	var out string
	for off := 0; off < n; {
		s := doc.Snap(text.Span{Offset: off, Length: 1})
		if s.Length == 0 {
			t.Fatalf("Snap produced a zero-length span at offset %d, cannot make progress", off)
		}
		part, err := doc.Resolve(s)
		if err != nil {
			t.Fatalf("Resolve(%+v) returned error: %v", s, err)
		}
		out += part
		off = s.Offset + s.Length
	}
	if want := doc.Normalized(); out != want {
		t.Errorf("tiled spans produced %q, want %q", out, want)
	}
}
