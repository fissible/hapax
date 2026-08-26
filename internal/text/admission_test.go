package text_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/text"
)

// Byte-level constants used across the admission and span tests.
const (
	bom = "\xef\xbb\xbf" // UTF-8 byte order mark, U+FEFF

	eAcuteComposed   = "é"                     // é as a single code point
	eAcuteDecomposed = "é"                    // e + COMBINING ACUTE ACCENT
	family           = "\U0001F469‍\U0001F467" // woman + ZWJ + girl: one grapheme, multiple code points
)

func TestAdmitAcceptsValidUTF8(t *testing.T) {
	for name, in := range map[string]string{
		"ascii":       "The quick brown fox.",
		"multibyte":   "Café — naïve, résumé.",
		"astral":      "Emoji: " + family + " end.",
		"empty":       "",
		"combining":   eAcuteDecomposed,
		"mixed lines": "one\ntwo\r\nthree\rfour",
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := text.Admit([]byte(in))
			if err != nil {
				t.Fatalf("Admit(%q) returned error: %v", in, err)
			}
			if doc == nil {
				t.Fatal("Admit returned nil document with nil error")
			}
			if got := string(doc.Raw()); got != in {
				t.Errorf("Raw() = %q, want %q", got, in)
			}
		})
	}
}

// AdmissionError.Offset is a byte offset into the ORIGINAL input passed to
// Admit, including any BOM. That is the coordinate system a user can act on:
// it matches what they see opening the file, not an internal stripped view.
func TestAdmitRejectsInvalidUTF8AtExactOffset(t *testing.T) {
	cases := map[string]struct {
		in         []byte
		wantOffset int
	}{
		"lone continuation":   {[]byte{'a', 0x80, 'b'}, 1},
		"truncated 2-byte":    {[]byte{'a', 0xc3}, 1},
		"truncated 3-byte":    {[]byte{0xe2, 0x82}, 0},
		"invalid start":       {[]byte{0xff, 0xfe}, 0},
		"surrogate half":      {[]byte{0xed, 0xa0, 0x80}, 0},
		"overlong slash":      {[]byte{0xc0, 0xaf}, 0},
		"overlong nul":        {[]byte{0xc0, 0x80}, 0},
		"bad continuation":    {[]byte{'x', 'y', 0xe2, 0x28, 0xa1}, 2},
		"invalid after valid": {[]byte("héllo\xff"), 6},
		// Offsets are relative to the original input, so the 3-byte BOM counts.
		"invalid after BOM": {append([]byte(bom), 0xff), 3},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc, err := text.Admit(c.in)
			if err == nil {
				t.Fatalf("Admit(% x) succeeded, want rejection", c.in)
			}
			if doc != nil {
				t.Error("Admit returned a document alongside an error; want nil")
			}
			var ae *text.AdmissionError
			if !errors.As(err, &ae) {
				t.Fatalf("error %T is not *text.AdmissionError", err)
			}
			if ae.Offset != c.wantOffset {
				t.Errorf("AdmissionError.Offset = %d, want %d (offset into original input)", ae.Offset, c.wantOffset)
			}
		})
	}
}

// Documents are never repaired: repairing invalid UTF-8 would shift every
// subsequent offset while the file on disk stays unchanged.
func TestAdmitDoesNotRepairInvalidUTF8(t *testing.T) {
	in := []byte{'a', 0xff, 'b'}
	if _, err := text.Admit(in); err == nil {
		t.Fatal("Admit accepted invalid UTF-8; repair is forbidden, rejection is required")
	}
	if in[1] != 0xff {
		t.Error("Admit mutated its input")
	}
}

func TestAdmitStripsBOMAndRecordsIt(t *testing.T) {
	body := "First paragraph.\n"
	doc, err := text.Admit([]byte(bom + body))
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if !doc.HadBOM() {
		t.Error("HadBOM() = false, want true")
	}
	if got := string(doc.Raw()); got != body {
		t.Errorf("Raw() = %q, want %q (BOM must be stripped)", got, body)
	}
	// Offsets are relative to the stripped content: byte 0 is 'F'.
	got, err := doc.Resolve(text.Span{Offset: 0, Length: 5})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != "First" {
		t.Errorf("Resolve(0,5) = %q, want %q", got, "First")
	}
}

func TestAdmitWithoutBOM(t *testing.T) {
	doc, err := text.Admit([]byte("no bom here"))
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if doc.HadBOM() {
		t.Error("HadBOM() = true for input with no BOM")
	}
}

// Only a leading BOM is a BOM. U+FEFF elsewhere is a zero-width no-break space
// and is content.
func TestAdmitLeavesInteriorU_FEFF(t *testing.T) {
	in := "a" + bom + "b"
	doc, err := text.Admit([]byte(in))
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if doc.HadBOM() {
		t.Error("HadBOM() = true for a non-leading U+FEFF")
	}
	if got := string(doc.Raw()); got != in {
		t.Errorf("Raw() = %q, want %q (interior U+FEFF is content)", got, in)
	}
}

// A doubled BOM: the first is a mark, the second is content.
func TestAdmitStripsOnlyOneBOM(t *testing.T) {
	doc, err := text.Admit([]byte(bom + bom + "x"))
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if !doc.HadBOM() {
		t.Error("HadBOM() = false, want true")
	}
	if got, want := string(doc.Raw()), bom+"x"; got != want {
		t.Errorf("Raw() = %q, want %q (exactly one BOM stripped)", got, want)
	}
}

// Line endings are preserved exactly. Normalizing CRLF to LF would shift every
// subsequent offset while the file on disk is unchanged.
func TestLineEndingsPreservedExactly(t *testing.T) {
	in := "alpha\r\nbeta\nkappa\rdelta\n\r\n"
	doc, err := text.Admit([]byte(in))
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if got := string(doc.Raw()); got != in {
		t.Errorf("Raw() = %q, want %q", got, in)
	}
	if got := doc.Normalized(); got != in {
		t.Errorf("Normalized() = %q, want %q; NFC must not alter line endings", got, in)
	}
	if strings.Count(doc.Normalized(), "\r\n") != 2 {
		t.Errorf("expected 2 CRLF sequences preserved, got %d", strings.Count(doc.Normalized(), "\r\n"))
	}
}

func TestNormalizedIsNFC(t *testing.T) {
	doc, err := text.Admit([]byte("caf" + eAcuteDecomposed))
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if got, want := doc.Normalized(), "caf"+eAcuteComposed; got != want {
		t.Errorf("Normalized() = %q, want %q", got, want)
	}
	// Raw is untouched: normalization is applied after span capture, never to
	// the stored bytes.
	if got, want := string(doc.Raw()), "caf"+eAcuteDecomposed; got != want {
		t.Errorf("Raw() = %q, want %q; normalization must not alter raw bytes", got, want)
	}
}

func TestNormalizedIsIdempotentForAlreadyComposedText(t *testing.T) {
	in := "caf" + eAcuteComposed
	doc, err := text.Admit([]byte(in))
	if err != nil {
		t.Fatalf("Admit returned error: %v", err)
	}
	if got := doc.Normalized(); got != in {
		t.Errorf("Normalized() = %q, want %q", got, in)
	}
}

// Raw() hands back bytes the caller may keep and slice. Mutating that slice must
// not corrupt the document, or a caller could silently invalidate every span.
func TestRawIsNotAliasedToDocumentState(t *testing.T) {
	const in = "caf" + eAcuteDecomposed + " tail"
	doc := mustAdmit(t, in)

	before := doc.Normalized()
	got := doc.Raw()
	for i := range got {
		got[i] = 'Z'
	}

	if after := doc.Normalized(); after != before {
		t.Errorf("Normalized() changed after mutating Raw(): %q then %q", before, after)
	}
	if second := string(doc.Raw()); second != in {
		t.Errorf("Raw() = %q after caller mutation, want %q", second, in)
	}
	resolved, err := doc.Resolve(text.Span{Offset: 0, Length: 3})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != "caf" {
		t.Errorf("Resolve = %q after caller mutation, want %q", resolved, "caf")
	}
}
