package text_test

// RunTokens — the primitive the paragraph unit needs.
//
// `profile` measures features per paragraph, and `features.Extract` consumes
// tokens. Without this, a consumer has to re-Admit each run's text, which
// rebuilds a whole Document per paragraph and throws away the raw-byte
// provenance that slice 2a-1 exists to preserve.
//
// The contract: RunTokens returns the document's own tokens that fall inside a
// leaf's span and outside its excisions. It does not retokenize, so a token can
// never disagree with Tokens() about its class, flags or span.

import (
	"testing"

	"github.com/fissible/hapax/internal/text"
)

// mustAdmit and structure live in span_test.go and structure_test.go.

func runTokens(t *testing.T, doc *text.Document, leaf *text.Node) []text.Token {
	t.Helper()
	got, err := doc.RunTokens(leaf)
	if err != nil {
		t.Fatalf("RunTokens(%+v) returned error: %v", leaf.Span, err)
	}
	return got
}

func tokenTexts(tokens []text.Token) []string {
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		out = append(out, tok.Text)
	}
	return out
}

// equalStrings lives in token_test.go.

// The tokens of a run are the document's tokens, not a fresh tokenization of
// the run's text. Re-tokenizing would let a run's token disagree with the
// document's about its class or its span.
func TestRunTokensAreTheDocumentsOwnTokens(t *testing.T) {
	const src = "First paragraph, with a comma.\n\nSecond paragraph; with a semicolon.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	leaves := root.Leaves()
	if len(leaves) != 2 {
		t.Fatalf("got %d leaves, want 2", len(leaves))
	}

	all := doc.Tokens()
	byOffset := make(map[int]text.Token, len(all))
	for _, tok := range all {
		byOffset[tok.Span.Offset] = tok
	}

	for i, leaf := range leaves {
		got := runTokens(t, doc, leaf)
		if len(got) == 0 {
			t.Fatalf("leaf %d produced no tokens", i)
		}
		for _, tok := range got {
			from, ok := byOffset[tok.Span.Offset]
			if !ok {
				t.Errorf("leaf %d: token %q at %+v is not one of the document's tokens", i, tok.Text, tok.Span)
				continue
			}
			if from != tok {
				t.Errorf("leaf %d: token at %+v = %+v, but the document has %+v", i, tok.Span, tok, from)
			}
			if tok.Span.Offset < leaf.Span.Offset || tok.Span.Offset+tok.Span.Length > leaf.Span.Offset+leaf.Span.Length {
				t.Errorf("leaf %d: token %q at %+v escapes the run %+v", i, tok.Text, tok.Span, leaf.Span)
			}
		}
	}

	if got, want := tokenTexts(runTokens(t, doc, leaves[0])), []string{"First", "paragraph", ",", "with", "a", "comma", "."}; !equalStrings(got, want) {
		t.Errorf("first run = %q, want %q", got, want)
	}
	if got, want := tokenTexts(runTokens(t, doc, leaves[1])), []string{"Second", "paragraph", ";", "with", "a", "semicolon", "."}; !equalStrings(got, want) {
		t.Errorf("second run = %q, want %q", got, want)
	}
}

// Tokens are ordered and each appears once. A consumer computing a mean over
// them would otherwise double-count.
func TestRunTokensAreOrderedAndUnique(t *testing.T) {
	doc, root := structure(t, "A paragraph of ordinary prose that runs on for a while here.\n", text.DefaultStructureOptions())
	got := runTokens(t, doc, root.Leaves()[0])

	prev := -1
	for i, tok := range got {
		if tok.Span.Offset <= prev {
			t.Errorf("token %d (%q) at offset %d does not follow the previous token ending at %d", i, tok.Text, tok.Span.Offset, prev)
		}
		prev = tok.Span.Offset
	}
}

// Excised bytes are not prose, so their tokens are not the run's tokens. This
// is the case that makes RunTokens more than a span filter.
func TestExcisedTokensAreNotInTheRun(t *testing.T) {
	const src = "Call `doc.RunTokens(leaf)` before measuring anything here.\n"

	doc, root := structure(t, src, text.DefaultStructureOptions())
	leaf := root.Leaves()[0]

	if len(leaf.Excisions) != 1 {
		t.Fatalf("expected one excision, got %d", len(leaf.Excisions))
	}
	got := runTokens(t, doc, leaf)
	want := []string{"Call", "before", "measuring", "anything", "here", "."}
	if !equalStrings(tokenTexts(got), want) {
		t.Errorf("run tokens = %q, want %q", tokenTexts(got), want)
	}

	excision := leaf.Excisions[0]
	for _, tok := range got {
		if tok.Span.Offset >= excision.Offset && tok.Span.Offset < excision.Offset+excision.Length {
			t.Errorf("token %q at %+v lies inside the excision %+v", tok.Text, tok.Span, excision)
		}
	}
}

// No token may PARTIALLY overlap an excision: a token is either wholly inside
// the run's prose or wholly excised. A partial token has no meaning, and
// splitting one would invent a token the document never had.
//
// An earlier version of this test asserted the property on a single fixture
// where no token actually straddled anything, so it passed vacuously. It is
// now an invariant over every fixture that produces excisions — including ones
// where the construct abuts a word with no separating space, which is where a
// straddle would arise if excision boundaries could fall inside a word.
func TestNoTokenPartiallyOverlapsAnExcision(t *testing.T) {
	sources := []string{
		"The measured rates[^1] are published here.\n\n[^1]: A body.\n",
		"Prefix`code`suffix with no spaces at all here.\n",
		"Word![alt](x.png)word joined directly together here.\n",
		"A``double``B and C`single`D run together.\n",
		"> Wrapped `code\n> across` a quote marker here.\n",
		"- Wrapped `code\n  across` a list indent here.\n",
		kitchenSink,
	}
	sawExcision := false
	for _, src := range sources {
		doc, root := structure(t, src, text.DefaultStructureOptions())
		for i, leaf := range root.Leaves() {
			if len(leaf.Excisions) > 0 {
				sawExcision = true
			}
			for _, tok := range runTokens(t, doc, leaf) {
				for _, e := range leaf.Excisions {
					if tok.Span.Offset < e.Offset+e.Length && e.Offset < tok.Span.Offset+tok.Span.Length {
						t.Errorf("%q leaf %d: token %q at %+v partially overlaps excision %+v", src, i, tok.Text, tok.Span, e)
					}
				}
			}
		}
	}
	if !sawExcision {
		t.Fatal("no fixture produced an excision, so the invariant held vacuously")
	}
}

// A container's marker is not authored prose, so it must not appear as a token
// on a continuation line. Exact token lists, because a test that only compared
// two policies against each other would pass if BOTH wrongly included the ">".
func TestContainerMarkersAreNotTokens(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		tokens  []string
		lexical int
	}{
		{
			"wrapped block quote",
			"> Run the tests\n> before pushing here.\n",
			[]string{"Run", "the", "tests", "before", "pushing", "here", "."},
			6,
		},
		{
			"wrapped list item",
			"- Run the tests\n  before pushing here.\n",
			[]string{"Run", "the", "tests", "before", "pushing", "here", "."},
			6,
		},
		{
			"wrapped item inside a quote",
			"> - Run the tests\n>   before pushing here.\n",
			[]string{"Run", "the", "tests", "before", "pushing", "here", "."},
			6,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, root := structure(t, c.src, text.DefaultStructureOptions())
			leaf := root.Leaves()[0]
			got := runTokens(t, doc, leaf)

			if !equalStrings(tokenTexts(got), c.tokens) {
				t.Errorf("tokens = %q, want %q", tokenTexts(got), c.tokens)
			}
			lexical := 0
			for _, tok := range got {
				if tok.Lexical {
					lexical++
				}
			}
			if lexical != c.lexical {
				t.Errorf("lexical count = %d, want %d", lexical, c.lexical)
			}
			for _, tok := range got {
				if tok.Text == ">" || tok.Text == "-" {
					t.Errorf("container marker %q became a token", tok.Text)
				}
			}
		})
	}
}

// A Node is an exported struct a caller can build, so a malformed span or
// malformed excisions must be refused rather than silently producing a
// plausible-looking token list.
//
// What this does NOT cover, stated rather than implied: a node built from a
// DIFFERENT document whose span happens to be valid in this one is
// indistinguishable from a native node, because Node carries no document
// provenance. That is slice 2d's pinned decision — Node's field schema is
// frozen and holds only recorded evidence, so there is nowhere for a document
// reference to live. Detecting it would mean reopening that schema, which is a
// larger decision than this slice; it is recorded here rather than papered over.
func TestRunTokensRejectsAMalformedNode(t *testing.T) {
	doc, root := structure(t, "A paragraph of ordinary prose here.\n", text.DefaultStructureOptions())
	leaf := root.Leaves()[0]

	for name, node := range map[string]*text.Node{
		"span past the end": {
			Kind: text.KindLeaf, Role: text.RoleParagraph,
			Span: text.Span{Offset: leaf.Span.Offset, Length: len(doc.Raw()) + 10},
		},
		"negative offset": {
			Kind: text.KindLeaf, Role: text.RoleParagraph,
			Span: text.Span{Offset: -1, Length: 4},
		},
		"excision outside the span": {
			Kind: text.KindLeaf, Role: text.RoleParagraph,
			Span:      leaf.Span,
			Excisions: []text.Span{{Offset: leaf.Span.Offset + leaf.Span.Length + 1, Length: 1}},
		},
		"excisions out of order": {
			Kind: text.KindLeaf, Role: text.RoleParagraph,
			Span: leaf.Span,
			Excisions: []text.Span{
				{Offset: leaf.Span.Offset + 4, Length: 2},
				{Offset: leaf.Span.Offset + 1, Length: 2},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := doc.RunTokens(node); err == nil {
				t.Errorf("RunTokens accepted a node with %s", name)
			}
		})
	}
}

// The lexical count must agree with the Words the leaf already recorded.
// Two numbers derived from the same bytes by different paths that disagree
// would make the sententiality verdict unexplainable from the run itself.
func TestRunTokensAgreeWithRecordedWordCount(t *testing.T) {
	for _, src := range []string{
		"A plain paragraph of prose that carries several words.\n",
		"Prose with `code()` and ![alt](x.png) and a note[^1] in it.\n\n[^1]: A body.\n",
		"> Quoted prose here.\n> Continued on a second line.\n",
		"- A list item that is a whole sentence for once.\n- Redis\n",
		"| a | b |\n|---|---|\n| c | d |\n",
		"Term\n: A definition description that is a whole sentence.\n",
		kitchenSink,
	} {
		t.Run(src[:min(len(src), 28)], func(t *testing.T) {
			doc, root := structure(t, src, text.DefaultStructureOptions())
			for i, leaf := range root.Leaves() {
				lexical := 0
				for _, tok := range runTokens(t, doc, leaf) {
					if tok.Lexical {
						lexical++
					}
				}
				if lexical != leaf.Words {
					t.Errorf("leaf %d (%s): RunTokens has %d lexical tokens, but Words = %d", i, leaf.Role, lexical, leaf.Words)
				}
			}
		})
	}
}

// A run whose prose is entirely excised has no tokens, and says so with an
// empty slice rather than an error: an empty run is a valid parse, not a fault.
func TestFullyExcisedRunHasNoTokens(t *testing.T) {
	doc, root := structure(t, "`code and nothing else`\n", text.DefaultStructureOptions())
	leaf := root.Leaves()[0]

	got, err := doc.RunTokens(leaf)
	if err != nil {
		t.Fatalf("RunTokens returned error for a fully excised run: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d tokens, want none: %q", len(got), tokenTexts(got))
	}
}

// Containers are not feature-bearing, so asking for their tokens is a caller
// error rather than a silently empty answer.
func TestRunTokensRejectsAnythingButALeaf(t *testing.T) {
	doc, root := structure(t, "> A quoted sentence that runs on here.\n", text.DefaultStructureOptions())

	if _, err := doc.RunTokens(root); err == nil {
		t.Error("RunTokens accepted the document container")
	}
	if _, err := doc.RunTokens(nil); err == nil {
		t.Error("RunTokens accepted a nil node")
	}
}

// The block-quote policy changes a verdict, not the bytes, so it must not
// change the tokens either.
func TestRunTokensAreIndependentOfInclusionPolicy(t *testing.T) {
	const src = "> A quoted sentence that runs on here.\n"

	on := text.DefaultStructureOptions()
	on.IncludeBlockQuotes = true

	offDoc, offRoot := structure(t, src, text.DefaultStructureOptions())
	onDoc, onRoot := structure(t, src, on)

	offTokens := tokenTexts(runTokens(t, offDoc, offRoot.Leaves()[0]))
	onTokens := tokenTexts(runTokens(t, onDoc, onRoot.Leaves()[0]))
	if !equalStrings(offTokens, onTokens) {
		t.Errorf("policy changed the tokens: %q vs %q", offTokens, onTokens)
	}
}
