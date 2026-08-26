package text_test

import (
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/fissible/hapax/internal/text"
)

// Tokenization, slice 2a: words, punctuation, apostrophes, hyphens and dashes,
// numbers, and spans.
//
// URL, email and file-path recognition and their terminal-punctuation peeling
// are slice 2b. They are a subsystem of their own — "leaves a string still valid
// as that class" is only decidable once each class has a pinned grammar — and
// the vendored corpus is 18th-century prose containing none of them.
//
// Whitespace produces no token; it is the gap between spans.

func tokenize(t *testing.T, src string) []text.Token {
	t.Helper()
	return mustAdmit(t, src).Tokens()
}

func texts(toks []text.Token) []string {
	out := make([]string, len(toks))
	for i, tk := range toks {
		out[i] = tk.Text
	}
	return out
}

func requireTokens(t *testing.T, src string, want ...string) []text.Token {
	t.Helper()
	got := tokenize(t, src)
	if g := texts(got); !equalStrings(g, want) {
		t.Fatalf("tokenize(%q) = %v, want %v", src, g, want)
	}
	return got
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Apostrophes
// ---------------------------------------------------------------------------

// Suffix contractions are unambiguous: n't, 're, 've, 'll, 'd, 'm attach to
// nothing else.
func TestContractionSuffixesAreOneTokenFlaggedContraction(t *testing.T) {
	for _, src := range []string{"don't", "we're", "they've", "we'll", "I'd", "I'm", "can't", "shouldn't"} {
		t.Run(src, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if tk.Class != text.Word {
				t.Errorf("class = %q, want %q", tk.Class, text.Word)
			}
			if !tk.Contraction {
				t.Error("Contraction = false, want true")
			}
			if tk.Possessive {
				t.Error("Possessive = true, want false")
			}
		})
	}
}

// `'s` is genuinely ambiguous — "John's" is possessive, "it's" is a contraction,
// and no surface inspection separates them. Deciding it needs syntax we do not
// have, so the contract pins a SURFACE HEURISTIC and says so:
//
//	a closed, case-folded list of hosts takes a contracted is/has;
//	every other `'s` is possessive; a trailing bare apostrophe is possessive.
//
// This is a policy, not a semantic judgement. It is knowingly wrong on
// "John's been away" (contraction, classified possessive) and would be wrong on
// "one's duties" (possessive) if `one` were listed, which is why it is not.
//
// It exists because Section 2 requires contraction rate to measure a habit
// rather than how often an author writes about people's things, and a
// consistent surface rule serves that even where it misreads an instance.
func TestApostropheSUsesThePinnedSurfaceHeuristic(t *testing.T) {
	contractions := []string{
		"it's", "he's", "she's", "that's", "there's", "here's",
		"what's", "who's", "where's", "when's", "how's", "let's",
	}
	possessives := []string{
		"John's", "author's", "dog's", "company's", "Madison's",
		"one's", // deliberately NOT a listed host: possessive is the commoner reading
	}

	for _, src := range contractions {
		t.Run("contraction/"+src, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if !tk.Contraction || tk.Possessive {
				t.Errorf("%q: Contraction=%v Possessive=%v, want true/false", src, tk.Contraction, tk.Possessive)
			}
		})
	}
	for _, src := range possessives {
		t.Run("possessive/"+src, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if !tk.Possessive || tk.Contraction {
				t.Errorf("%q: Possessive=%v Contraction=%v, want true/false", src, tk.Possessive, tk.Contraction)
			}
		})
	}
}

// The host list is matched case-folded, or sentence-initial forms would classify
// differently from mid-sentence ones and the rate would track capitalisation.
func TestHostListIsCaseFolded(t *testing.T) {
	for _, pair := range [][2]string{{"it's", "It's"}, {"that's", "That's"}, {"who's", "Who's"}} {
		lower := requireTokens(t, pair[0], pair[0])[0]
		upper := requireTokens(t, pair[1], pair[1])[0]
		if lower.Contraction != upper.Contraction || lower.Possessive != upper.Possessive {
			t.Errorf("%q and %q classify differently: %+v vs %+v", pair[0], pair[1], lower, upper)
		}
	}
}

// An apostrophe inside a name is part of the word and flags nothing.
func TestInternalApostropheInNames(t *testing.T) {
	for _, src := range []string{"O'Brien", "D'Angelo", "O'Connor"} {
		t.Run(src, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if tk.Class != text.Word {
				t.Errorf("class = %q, want %q", tk.Class, text.Word)
			}
			if tk.Contraction || tk.Possessive {
				t.Errorf("%q: Contraction=%v Possessive=%v, want false/false", src, tk.Contraction, tk.Possessive)
			}
		})
	}
}

// A name with an internal apostrophe can still take a possessive suffix; the
// internal one must not be mistaken for the terminal one.
func TestPossessiveOnNameWithInternalApostrophe(t *testing.T) {
	for _, src := range []string{"O'Brien's", "D'Angelo's"} {
		t.Run(src, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if !tk.Possessive || tk.Contraction {
				t.Errorf("%q: Possessive=%v Contraction=%v, want true/false", src, tk.Possessive, tk.Contraction)
			}
		})
	}
}

func TestPossessiveFormsEndingInS(t *testing.T) {
	for _, src := range []string{"boss's", "James'", "authors'", "Jones's"} {
		t.Run(src, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if !tk.Possessive || tk.Contraction {
				t.Errorf("%q: Possessive=%v Contraction=%v, want true/false", src, tk.Possessive, tk.Contraction)
			}
		})
	}
}

// KNOWN LIMITATION, pinned so it is visible rather than accidental.
//
// Archaic leading-apostrophe elisions ('tis, 'twas, o'er) are NOT recognised in
// this slice. A leading apostrophe is quotation punctuation, so "'tis" becomes
// two tokens. This matters for the vendored 18th-century corpus and is the first
// candidate for a versioned whole-token elision lexicon; it is deferred rather
// than guessed at.
func TestArchaicElisionsAreNotRecognisedYet(t *testing.T) {
	got := requireTokens(t, "'tis", "'", "tis")
	if got[0].Class != text.Punctuation {
		t.Errorf("leading apostrophe classified %q, want %q", got[0].Class, text.Punctuation)
	}
	if got[1].Contraction {
		t.Error("\"tis\" flagged as a contraction; the elision is not recognised in this slice")
	}
	// o'er reads as an internal apostrophe, so it is one word with no flags.
	oer := requireTokens(t, "o'er", "o'er")[0]
	if oer.Contraction || oer.Possessive {
		t.Errorf("o'er: Contraction=%v Possessive=%v, want false/false", oer.Contraction, oer.Possessive)
	}
}

func TestTypographicAndASCIIApostrophesAreEquivalent(t *testing.T) {
	for _, pair := range [][2]string{
		{"don't", "don’t"},
		{"John's", "John’s"},
		{"authors'", "authors’"},
		{"O'Brien", "O’Brien"},
	} {
		ascii := requireTokens(t, pair[0], pair[0])[0]
		typo := requireTokens(t, pair[1], pair[1])[0]
		if ascii.Class != typo.Class || ascii.Contraction != typo.Contraction || ascii.Possessive != typo.Possessive {
			t.Errorf("%q and %q classify differently: %+v vs %+v", pair[0], pair[1], ascii, typo)
		}
	}
}

func TestApostropheAsQuotationIsPunctuation(t *testing.T) {
	got := requireTokens(t, "'hello'", "'", "hello", "'")
	assertClasses(t, got, text.Punctuation, text.Word, text.Punctuation)
	if got[1].Possessive || got[1].Contraction {
		t.Errorf("quoted word carries flags: %+v", got[1])
	}
}

// ---------------------------------------------------------------------------
// Hyphens and dashes, by codepoint
// ---------------------------------------------------------------------------

func TestHyphensJoinCompoundsIntoOneToken(t *testing.T) {
	for name, src := range map[string]string{
		"ascii hyphen-minus U+002D":  "well-known",
		"non-breaking hyphen U+2011": "well‑known",
		"multiple hyphens":           "state-of-the-art",
	} {
		t.Run(name, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if tk.Class != text.Word {
				t.Errorf("class = %q, want %q", tk.Class, text.Word)
			}
		})
	}
}

func TestDashesSeparateTokens(t *testing.T) {
	for name, dash := range map[string]string{
		"en dash U+2013":    "–",
		"em dash U+2014":    "—",
		"minus sign U+2212": "−",
	} {
		t.Run(name, func(t *testing.T) {
			got := requireTokens(t, "well"+dash+"known", "well", dash, "known")
			assertClasses(t, got, text.Word, text.Punctuation, text.Word)
		})
	}
}

// A hyphen that joins nothing is punctuation, not part of a word.
func TestDanglingHyphensArePunctuation(t *testing.T) {
	requireTokens(t, "-lead", "-", "lead")
	requireTokens(t, "trail-", "trail", "-")
	requireTokens(t, "a - b", "a", "-", "b")
}

// ---------------------------------------------------------------------------
// Numbers
// ---------------------------------------------------------------------------

func TestNumberForms(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"42", "42"},
		{"3.14", "3.14"},
		{"1,000", "1,000"},
		{"1,000,000", "1,000,000"},
		{"0", "0"},
	} {
		t.Run(c.src, func(t *testing.T) {
			tk := requireTokens(t, c.src, c.want)[0]
			if tk.Class != text.Number {
				t.Errorf("class = %q, want %q", tk.Class, text.Number)
			}
			if tk.Lexical {
				t.Error("Lexical = true; numbers are excluded from word-length and diversity features")
			}
		})
	}
}

func TestDecimalPointDoesNotSplitANumber(t *testing.T) {
	got := requireTokens(t, "pi is 3.14 exactly", "pi", "is", "3.14", "exactly")
	assertClasses(t, got, text.Word, text.Word, text.Number, text.Word)
}

// A number never ends in a separator, so a trailing period is sentence
// punctuation rather than part of the number.
func TestTrailingPeriodIsNotPartOfANumber(t *testing.T) {
	got := requireTokens(t, "Call 555.", "Call", "555", ".")
	assertClasses(t, got, text.Word, text.Number, text.Punctuation)

	got = requireTokens(t, "It cost 3.14.", "It", "cost", "3.14", ".")
	assertClasses(t, got, text.Word, text.Word, text.Number, text.Punctuation)
}

// Malformed numeric forms must not be over-accepted into one Number token.
// The pinned grammar: one to three digits, then zero or more comma-separated
// groups of EXACTLY three digits, optionally followed by a single decimal point
// and one or more digits. An ungrouped run of digits of any length is also a
// number. A number begins and ends with a digit.
//
// The first group is bounded at three, or "1234,567" would read as one number
// under conventional grouping rules that it plainly violates.
func TestGroupedAndDecimalNumbers(t *testing.T) {
	for _, src := range []string{"1,000.00", "1,234,567.89", "12.5", "999"} {
		t.Run(src, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if tk.Class != text.Number {
				t.Errorf("class = %q, want %q", tk.Class, text.Number)
			}
		})
	}
}

func TestMalformedNumbersAreNotNumbers(t *testing.T) {
	for _, c := range []struct {
		src  string
		want []string
	}{
		{"3..14", []string{"3", ".", ".", "14"}},
		{"1,,000", []string{"1", ",", ",", "000"}},
		{".5", []string{".", "5"}},
		{"12,34", []string{"12", ",", "34"}},         // group of two, not three
		{"1.2.3", []string{"1", ".", "2", ".", "3"}}, // two decimal points
		{"1,234.", []string{"1,234", "."}},           // trailing separator is punctuation
		{"1234,567", []string{"1234", ",", "567"}},   // first group longer than three
		{"1,2345", []string{"1", ",", "2345"}},       // later group longer than three
	} {
		t.Run(c.src, func(t *testing.T) {
			requireTokens(t, c.src, c.want...)
		})
	}
}

// ---------------------------------------------------------------------------
// Classes and lexical status, asserted directly
// ---------------------------------------------------------------------------

// Asserted against expected values rather than against whatever the tokenizer
// reports, so a tokenizer that classified punctuation as some other non-Word
// class could not pass.
func TestClassAndLexicalAreAssignedExplicitly(t *testing.T) {
	got := requireTokens(t,
		"Alpha, beta 42; don't.",
		"Alpha", ",", "beta", "42", ";", "don't", ".")

	want := []struct {
		class   text.TokenClass
		lexical bool
	}{
		{text.Word, true},
		{text.Punctuation, false},
		{text.Word, true},
		{text.Number, false},
		{text.Punctuation, false},
		{text.Word, true},
		{text.Punctuation, false},
	}
	for i, w := range want {
		if got[i].Class != w.class {
			t.Errorf("token %d (%q) class = %q, want %q", i, got[i].Text, got[i].Class, w.class)
		}
		if got[i].Lexical != w.lexical {
			t.Errorf("token %d (%q) Lexical = %v, want %v", i, got[i].Text, got[i].Lexical, w.lexical)
		}
	}
}

func TestEveryTerminalPunctuationCharacterIsItsOwnToken(t *testing.T) {
	for _, p := range []string{".", ",", ";", ":", "!", "?", "\"", ")", "]", "}", "(", "[", "{"} {
		t.Run(p, func(t *testing.T) {
			got := requireTokens(t, "a"+p+"b", "a", p, "b")
			assertClasses(t, got, text.Word, text.Punctuation, text.Word)
		})
	}
}

func TestRepeatedPunctuationYieldsSeparateTokens(t *testing.T) {
	requireTokens(t, "wait...", "wait", ".", ".", ".")
	requireTokens(t, "really?!", "really", "?", "!")
}

func assertClasses(t *testing.T, got []text.Token, want ...text.TokenClass) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d tokens (%v), want %d classes", len(got), texts(got), len(want))
	}
	for i := range want {
		if got[i].Class != want[i] {
			t.Errorf("token %d (%q) class = %q, want %q", i, got[i].Text, got[i].Class, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Spans
// ---------------------------------------------------------------------------

// Exact byte offsets, including across multibyte characters, since spans are
// what the store persists.
func TestTokenSpansHaveExactByteOffsets(t *testing.T) {
	// "Café, 42" — the é is two bytes, so "Café" spans [0,5).
	got := requireTokens(t, "Café, 42", "Café", ",", "42")
	want := []text.Span{
		{Offset: 0, Length: 5},
		{Offset: 5, Length: 1},
		{Offset: 7, Length: 2},
	}
	for i := range want {
		if got[i].Span != want[i] {
			t.Errorf("token %q span = %+v, want %+v", got[i].Text, got[i].Span, want[i])
		}
	}
}

// A decomposed word occupies more raw bytes than its NFC text, so the span must
// cover the raw form while Text is normalized.
func TestSpansCoverRawBytesWhileTextIsNFC(t *testing.T) {
	const decomposed = "re\u0301sume\u0301" // "résumé" decomposed: 10 raw bytes, 6 NFC characters
	doc := mustAdmit(t, decomposed)
	toks := doc.Tokens()
	if len(toks) != 1 {
		t.Fatalf("tokenize(%q) produced %v, want one token", decomposed, texts(toks))
	}
	if got, want := toks[0].Span, (text.Span{Offset: 0, Length: len(decomposed)}); got != want {
		t.Errorf("span = %+v, want %+v (raw bytes)", got, want)
	}
	if got := toks[0].Text; got != "r\u00e9sum\u00e9" {
		t.Errorf("Text = %q, want the NFC form %q", got, "r\u00e9sum\u00e9")
	}
}

func TestTokenSpansResolveToTokenText(t *testing.T) {
	doc := mustAdmit(t, "Don't stop — John's résumé cost 3.14, really!")
	for _, tk := range doc.Tokens() {
		got, err := doc.Resolve(tk.Span)
		if err != nil {
			t.Errorf("Resolve(%+v) for token %q returned error: %v", tk.Span, tk.Text, err)
			continue
		}
		if got != tk.Text {
			t.Errorf("token %q has a span resolving to %q", tk.Text, got)
		}
	}
}

func TestTokenSpansAreOrderedAndDisjoint(t *testing.T) {
	doc := mustAdmit(t, "one two-three, four 5 — six")
	prevEnd := 0
	for _, tk := range doc.Tokens() {
		if tk.Span.Offset < prevEnd {
			t.Fatalf("token %q at %d overlaps the previous token ending at %d", tk.Text, tk.Span.Offset, prevEnd)
		}
		if tk.Span.Length <= 0 {
			t.Fatalf("token %q has non-positive length %d", tk.Text, tk.Span.Length)
		}
		prevEnd = tk.Span.Offset + tk.Span.Length
	}
	if n := len(doc.Raw()); prevEnd > n {
		t.Fatalf("last token ends at %d, past the document end %d", prevEnd, n)
	}
}

// No token may contain whitespace, and every non-whitespace raw byte must fall
// inside exactly one token. Together these prove coverage without relying on
// concatenated text.
func TestTokensCoverEveryNonWhitespaceByte(t *testing.T) {
	// Whitespace fixtures use explicit escapes: literal exotic spaces in source
	// are easily mangled by an editor, leaving the fixture no longer testing what
	// its name claims.
	for name, src := range map[string]string{
		"ascii":            "Alpha, beta\u2014gamma; don't drop 3.14 (or 1,000)!",
		"no-break space":   "caf\u00e9\u00a0r\u00e9sum\u00e9",
		"em and thin":      "a\u2003b\u2009c",
		"narrow no-break":  "x\u202fy",
		"ascii whitespace": "tabs\tand\nnewlines",
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustAdmit(t, src)
			raw := doc.Raw()
			covered := make([]bool, len(raw))

			for _, tk := range doc.Tokens() {
				for i := tk.Span.Offset; i < tk.Span.Offset+tk.Span.Length; i++ {
					if covered[i] {
						t.Fatalf("byte %d covered by more than one token", i)
					}
					covered[i] = true
				}
				for _, r := range tk.Text {
					if unicode.IsSpace(r) {
						t.Errorf("token %q contains whitespace", tk.Text)
						break
					}
				}
			}

			// Ranging a string yields each rune's FIRST byte only, so checking
			// covered[i] alone would let an uncovered continuation byte pass.
			for i, r := range string(raw) {
				if unicode.IsSpace(r) {
					continue
				}
				for b := i; b < i+utf8.RuneLen(r); b++ {
					if !covered[b] {
						t.Errorf("byte %d of non-whitespace rune %q (starting at %d) is in no token", b, string(r), i)
					}
				}
			}
		})
	}
}

func TestWhitespaceProducesNoTokens(t *testing.T) {
	for _, src := range []string{"", "   ", "\n\n", "\t \r\n", "  "} {
		if got := tokenize(t, src); len(got) != 0 {
			t.Errorf("tokenize(%q) = %v, want no tokens", src, texts(got))
		}
	}
}

func TestTokenizationIsDeterministic(t *testing.T) {
	doc := mustAdmit(t, "Repeatable? Yes — don't doubt it: 1,000 times.")
	first := texts(doc.Tokens())
	for i := 0; i < 3; i++ {
		if again := texts(doc.Tokens()); !equalStrings(first, again) {
			t.Fatalf("run %d differs: %v then %v", i, first, again)
		}
	}
}

// Non-Latin scripts must tokenize as words rather than falling through to
// punctuation or being dropped. The language gate is a separate component; the
// tokenizer must not silently lose text it does not recognise.
func TestNonLatinScriptsProduceWordTokens(t *testing.T) {
	for name, src := range map[string]string{
		"greek":    "λόγος",
		"cyrillic": "слово",
		"hebrew":   "מילה",
	} {
		t.Run(name, func(t *testing.T) {
			tk := requireTokens(t, src, src)[0]
			if tk.Class != text.Word {
				t.Errorf("class = %q, want %q", tk.Class, text.Word)
			}
			if !tk.Lexical {
				t.Error("Lexical = false, want true")
			}
		})
	}
}

// Symbols are neither words nor numbers, but must still be emitted with a
// pinned class so coverage holds and they cannot silently become words.
func TestSymbolsAreEmittedWithSymbolClass(t *testing.T) {
	got := requireTokens(t, "cost \u00a35 \U0001F600 ok", "cost", "\u00a3", "5", "\U0001F600", "ok")

	want := []struct {
		text    string
		class   text.TokenClass
		lexical bool
	}{
		{"cost", text.Word, true},
		{"\u00a3", text.Symbol, false},
		{"5", text.Number, false},
		{"\U0001F600", text.Symbol, false},
		{"ok", text.Word, true},
	}
	for i, w := range want {
		if got[i].Class != w.class {
			t.Errorf("token %d (%q) class = %q, want %q", i, got[i].Text, got[i].Class, w.class)
		}
		if got[i].Lexical != w.lexical {
			t.Errorf("token %d (%q) Lexical = %v, want %v", i, got[i].Text, got[i].Lexical, w.lexical)
		}
	}
}
