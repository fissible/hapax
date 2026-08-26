package text_test

import (
	"testing"

	"github.com/fissible/hapax/internal/text"
)

// NFC is a real algorithm, not a table of accented Latin letters. These cases
// each defeat a different shortcut implementation.
func TestNormalizedImplementsNFCProperly(t *testing.T) {
	cases := map[string]struct {
		in, want string
	}{
		"composes latin combining mark": {
			in:   "café",
			want: "café",
		},
		"composes hangul jamo into a syllable": {
			// choseong kiyeok + jungseong a + jongseong kiyeok -> 각
			in:   "각",
			want: "각",
		},
		"resolves a singleton decomposition": {
			// ANGSTROM SIGN normalizes to LATIN CAPITAL LETTER A WITH RING ABOVE
			in:   "Å",
			want: "Å",
		},
		"reorders combining marks by canonical class": {
			// dot above (ccc 230) then dot below (ccc 220) must come out reordered
			in:   "q̣̇",
			want: "q̣̇",
		},
		"honours composition exclusions": {
			// DEVANAGARI KA + NUKTA must NOT compose to U+0958, which is excluded
			in:   "क़",
			want: "क़",
		},
		"decomposes an excluded precomposed character": {
			// U+0958 itself decomposes, because it is composition-excluded
			in:   "क़",
			want: "क़",
		},
		"leaves already-composed text alone": {
			in:   "café résumé",
			want: "café résumé",
		},
		"leaves ascii alone": {
			in:   "plain ascii, nothing to do",
			want: "plain ascii, nothing to do",
		},
		"does not touch line endings": {
			in:   "a\r\nb\rc\nd",
			want: "a\r\nb\rc\nd",
		},
		"does not apply compatibility mappings": {
			// NFKC would turn the ligature into "fi"; NFC must not
			in:   "ﬁn",
			want: "ﬁn",
		},
		"does not fold a full-width digit": {
			// another NFKC-only mapping
			in:   "１",
			want: "１",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := mustAdmit(t, c.in)
			if got := doc.Normalized(); got != c.want {
				t.Errorf("Normalized(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizedIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"café",
		"각",
		"Å",
		"q̣̇",
		"क़",
		"क़",
		snapDoc,
		family,
		"",
	} {
		first := mustAdmit(t, in).Normalized()
		second := mustAdmit(t, first).Normalized()
		if first != second {
			t.Errorf("Normalized not idempotent for %q: %q then %q", in, first, second)
		}
	}
}

// Normalization never touches the stored bytes: spans address raw bytes, so
// altering them would invalidate every offset in the store.
func TestNormalizationLeavesRawUntouched(t *testing.T) {
	for _, in := range []string{"café", "각", "Å", "क़"} {
		doc := mustAdmit(t, in)
		if got := string(doc.Raw()); got != in {
			t.Errorf("Raw() = %q, want %q; normalization must not rewrite raw bytes", got, in)
		}
	}
}

// Resolving a span that covers a decomposed sequence returns the composed form,
// which is what lets the store hold spans instead of prose.
func TestResolveComposesAcrossClusterSpans(t *testing.T) {
	cases := []struct{ in, want string }{
		{"café", "café"},
		{"각", "각"},
		{"Å", "Å"},
	}
	for _, c := range cases {
		doc := mustAdmit(t, c.in)
		got, err := doc.Resolve(text.Span{Offset: 0, Length: len(doc.Raw())})
		if err != nil {
			t.Fatalf("Resolve(%q) returned error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
