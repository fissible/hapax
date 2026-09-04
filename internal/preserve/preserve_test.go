package preserve_test

// The deterministic semantic-preservation gate of ADR 0006: numbers, named
// entities, negations, URLs and quoted strings must survive an edit.
//
// Deterministic, with no model — so every one of those five is a SURFACE PROXY
// for the thing it stands for, and what matters here is that each proxy's
// failures are written down rather than discovered.
//
// # Equality, not survival
//
// The rule is stated as "must survive", which catches loss and permits
// invention. A rewrite that adds a number, a URL or a quotation fabricates a
// fact; one that adds a negation inverts a claim. Each class is therefore
// compared as a multiset equality in BOTH directions.
//
// The cost is real and accepted: a meaning-preserving rephrasing that removes a
// negation — "not unusual" becoming "common" — is rejected. That is the
// conservative direction. A gate permitting negation changes would permit a
// rewrite to invert what the author said, and this tool edits people's own
// writing.
//
// # Surface forms, with no normalisation
//
// 5 and five are different, and so are 1,000 and 1000. Normalising means
// deciding what a numeral means — currencies, ranges, percentages, ordinals —
// which is the semantic work this gate exists to avoid.
//
// # Where each proxy fails
//
//	numbers    the tokenizer keeps 1,000 whole, so these are token-level
//	entities   capitalised and not a function word. OVER-collects Monday and
//	           January, which makes the gate stricter; UNDER-collects lower-case
//	           entities like danah boyd, which is the dangerous direction and is
//	           a stated limitation
//	negations  a closed declared list; contractions survive tokenisation whole,
//	           so Don't is matched as one token
//	urls       NOT tokens: the tokenizer splits https://example.com/x into
//	           eleven pieces, so these are matched over the text
//	quotes     double-quoted spans only, straight or curly. Single quotes are
//	           excluded because an apostrophe and a closing single quote are the
//	           same character

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/features"
	"github.com/fissible/hapax/internal/preserve"
	"github.com/fissible/hapax/internal/text"
)

func check(t *testing.T, current, candidate string) preserve.Result {
	t.Helper()
	got, err := preserve.Check(current, candidate)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return got
}

func hexDigest(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func mustPass(t *testing.T, current, candidate string) {
	t.Helper()
	if got := check(t, current, candidate); !got.Preserved {
		t.Errorf("rejected:\n  current:   %q\n  candidate: %q\n  missing:   %v", current, candidate, got.Differences)
	}
}

func mustFail(t *testing.T, current, candidate string, class preserve.Class, item string) {
	t.Helper()
	got := check(t, current, candidate)
	if got.Preserved {
		t.Fatalf("accepted, want a %s failure on %q:\n  current:   %q\n  candidate: %q",
			class, item, current, candidate)
	}
	for _, difference := range got.Differences {
		if difference.Class == class && difference.Item == item {
			return
		}
	}
	t.Errorf("no %s failure on %q; got %v", class, item, got.Differences)
}

// ---------------------------------------------------------------------------
// Nothing changed, everything changed
// ---------------------------------------------------------------------------

// A rewrite that alters only style passes: none of the five classes moves.
func TestAPureStyleRewritePasses(t *testing.T) {
	mustPass(t,
		"The argument put forward by Anthropic in 2024 was not, in the end, persuasive.",
		"The 2024 argument from Anthropic was not persuasive, in the end.")
}

// Identical text passes trivially, which is the floor.
func TestIdenticalTextPasses(t *testing.T) {
	mustPass(t, "Anthropic said \"no\" to 5 things at https://example.com.", "Anthropic said \"no\" to 5 things at https://example.com.")
}

// Empty on both sides is preservation of nothing, which is preservation.
func TestEmptyTextPasses(t *testing.T) {
	mustPass(t, "", "")
}

// ---------------------------------------------------------------------------
// Each class, lost and invented
// ---------------------------------------------------------------------------

// Both directions for every class. Testing only loss would let a rewrite
// fabricate a number, a citation or a negation — inventions at least as bad as
// the losses the rule was written for.
func TestEachClassIsCheckedInBothDirections(t *testing.T) {
	cases := []struct {
		name    string
		class   preserve.Class
		with    string
		without string
		item    string
	}{
		{
			name: "a number", class: preserve.ClassNumber,
			with: "The model was released in 2024 after review.", without: "The model was released after review.",
			item: "2024",
		},
		{
			name: "a named entity", class: preserve.ClassEntity,
			with: "The paper from Anthropic was reviewed.", without: "The paper from the lab was reviewed.",
			// #93: entity items are folded, so the reported item is the canonical key.
			item: "anthropic",
		},
		{
			name: "a negation", class: preserve.ClassNegation,
			with: "The result was not surprising to anyone.", without: "The result was surprising to anyone.",
			item: "not",
		},
		{
			name: "a URL", class: preserve.ClassURL,
			with: "See https://example.com/paper for the details.", without: "See the paper for the details.",
			item: "https://example.com/paper",
		},
		{
			name: "a quoted string", class: preserve.ClassQuote,
			with: "He called it \"a categorical error\" in his reply.", without: "He called it a mistake in his reply.",
			item: "\"a categorical error\"",
		},
	}

	for _, c := range cases {
		t.Run(c.name+" lost", func(t *testing.T) {
			mustFail(t, c.with, c.without, c.class, c.item)
		})
		t.Run(c.name+" invented", func(t *testing.T) {
			mustFail(t, c.without, c.with, c.class, c.item)
		})
	}
}

// A class is reported by name, so a caller can say which kind of thing went
// missing rather than only that something did.
func TestTheMissingItemNamesItsClass(t *testing.T) {
	got := check(t, "Anthropic released 5 models.", "The lab released models.")

	if got.Preserved {
		t.Fatalf("accepted a candidate that lost both an entity and a number")
	}
	classes := map[preserve.Class]bool{}
	for _, difference := range got.Differences {
		classes[difference.Class] = true
	}
	if !classes[preserve.ClassEntity] || !classes[preserve.ClassNumber] {
		t.Errorf("missing classes = %v, want both an entity and a number", got.Differences)
	}
}

// Losing one of two identical items is a loss: the comparison is over multisets,
// not sets. A set comparison accepts dropping the second "not" and the meaning
// changes with it.
func TestTheComparisonIsOverMultisetsNotSets(t *testing.T) {
	mustFail(t,
		"It was not clear and it was not settled.",
		"It was not clear and it was settled.",
		preserve.ClassNegation, "not")

	mustFail(t,
		"There were 3 objections and 3 replies.",
		"There were 3 objections and replies.",
		preserve.ClassNumber, "3")
}

// ---------------------------------------------------------------------------
// Surface forms, with no normalisation
// ---------------------------------------------------------------------------

// Spelling out a numeral, or reformatting one, changes the surface form and is
// rejected. Normalising would mean deciding what a numeral means, which is the
// semantic work this gate exists to avoid — and the cost is stated rather than
// discovered by someone whose rewrite was refused.
func TestNumbersAreComparedAsSurfaceForms(t *testing.T) {
	cases := []struct{ name, current, candidate, item string }{
		{name: "a numeral spelled out", current: "There were 5 objections.", candidate: "There were five objections.", item: "5"},
		{name: "a separator removed", current: "It cost 1,000 dollars.", candidate: "It cost 1000 dollars.", item: "1,000"},
		// The tokenizer produces "." and "5" for ".5" rather than one numeric
		// token, so this is a loss of 0.5 and an invention of 5 rather than a
		// comparison of two numeric surface forms. Either way the rewrite is
		// refused, which is the behaviour under test.
		{name: "a decimal reformatted", current: "The rate was 0.5 per cent.", candidate: "The rate was .5 per cent.", item: "0.5"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustFail(t, c.current, c.candidate, preserve.ClassNumber, c.item)
		})
	}
}

// And the tokenizer keeps a separated number whole, which is why this is a
// token-level comparison rather than a scan for digits.
func TestASeparatedNumberIsOneItem(t *testing.T) {
	got := check(t, "It cost 1,000 dollars.", "For 1,000 dollars, it was bought.")
	if !got.Preserved {
		t.Fatalf("a reordering that keeps 1,000 intact was rejected: %v", got.Differences)
	}
}

// ---------------------------------------------------------------------------
// The entity proxy, and both of its failure modes
// ---------------------------------------------------------------------------

// Capitalised and not a function word. Excluding function words is what lets a
// sentence-initial entity be seen at all: without it the gate would either miss
// every entity that opens a sentence, or demand that every sentence keep its
// first word.
func TestASentenceInitialEntityIsSeen(t *testing.T) {
	mustFail(t,
		"Anthropic published the result last year.",
		"The lab published the result last year.",
		preserve.ClassEntity, "anthropic")
}

// And a sentence-initial function word is not an entity, so ordinary rewording
// of a sentence opening is not a preservation failure.
func TestASentenceInitialFunctionWordIsNotAnEntity(t *testing.T) {
	mustPass(t,
		"The result was reviewed carefully.",
		"This result was reviewed carefully.")
}

// The over-collecting failure, stated as a test so it is a known behaviour
// rather than a surprise: an ordinary capitalised noun is treated as an entity,
// which makes the gate stricter than it needs to be. That is the safe direction.
func TestTheEntityProxyOverCollects(t *testing.T) {
	mustFail(t,
		"The meeting was moved to Monday.",
		"The meeting was moved to the start of the week.",
		preserve.ClassEntity, "monday")
}

// The under-collecting failure, which is the dangerous one and is therefore
// written down rather than left implicit. A lower-case entity is invisible to
// the proxy, so losing it is NOT caught. If this test ever starts failing, the
// proxy has improved and the design note should be revisited.
func TestTheEntityProxyUnderCollectsLowerCaseNames(t *testing.T) {
	got := check(t,
		"The argument follows danah boyd on this point.",
		"The argument follows earlier work on this point.")

	if !got.Preserved {
		t.Fatalf("the proxy now catches a lower-case entity; DESIGN records that it does not, and should be updated: %v", got.Differences)
	}
}

// ---------------------------------------------------------------------------
// URLs are not tokens
// ---------------------------------------------------------------------------

// The tokenizer splits https://example.com/x into eleven pieces, so a URL does
// not exist as a token and is matched over the text instead. A token-level
// implementation would see the punctuation rearranged and report nothing.
func TestAURLIsMatchedWholeAndNotAsTokens(t *testing.T) {
	got := check(t,
		"See https://example.com/paper for details.",
		"See https://example.com/other for details.")

	if got.Preserved {
		t.Fatalf("a changed URL path was accepted")
	}
	for _, difference := range got.Differences {
		if difference.Class == preserve.ClassURL && difference.Item == "https://example.com/paper" {
			return
		}
	}
	t.Errorf("the whole URL was not reported as missing; got %v", got.Differences)
}

// Each recognised form, and a bare domain that is not one.
func TestTheURLFormsRecognised(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		recognise string
	}{
		{name: "https", text: "At https://example.com/a here.", recognise: "https://example.com/a"},
		{name: "http", text: "At http://example.com/a here.", recognise: "http://example.com/a"},
		{name: "www", text: "At www.example.com/a here.", recognise: "www.example.com/a"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustFail(t, c.text, "At the site here.", preserve.ClassURL, c.recognise)
		})
	}

	// A bare domain with no scheme and no www is not recognised, which is a
	// stated limit of the matcher rather than an accident: any dotted token
	// would otherwise be a URL.
	// Absence of a URL diagnostic is not the same as preservation: the gate must
	// actually pass, or it is rejecting for some other reason and the exclusion
	// proves nothing.
	t.Run("a bare domain is not a URL", func(t *testing.T) {
		mustPass(t, "At example.com here.", "At example.com there.")
	})
}

// A URL runs to whitespace, and trailing sentence punctuation is not part of it
// — otherwise moving a URL to the end of a sentence would fail the gate.
func TestAURLDoesNotSwallowTrailingPunctuation(t *testing.T) {
	mustPass(t,
		"The details are at https://example.com/paper, which is short.",
		"This is short: https://example.com/paper.")
}

// ---------------------------------------------------------------------------
// Quoted strings
// ---------------------------------------------------------------------------

// A quotation must survive verbatim, including its quote marks, since altering
// the words inside a quotation misattributes them.
func TestAQuotationMustSurviveVerbatim(t *testing.T) {
	mustFail(t,
		"She called it \"a categorical error\" in reply.",
		"She called it \"a category error\" in reply.",
		preserve.ClassQuote, "\"a categorical error\"")
}

// Curly quotes are recognised as well as straight ones.
func TestCurlyQuotesAreRecognised(t *testing.T) {
	mustFail(t,
		"She called it “a categorical error\u201d in reply.",
		"She called it a mistake in reply.",
		preserve.ClassQuote, "“a categorical error\u201d")
}

// Single quotes are excluded, because an apostrophe and a closing single quote
// are the same character. Written as a test so the exclusion is a decision
// rather than an omission: an apostrophe in ordinary prose must not be read as
// an unterminated quotation.
func TestSingleQuotesAreNotQuotations(t *testing.T) {
	mustPass(t,
		"It wasn't the author's argument, it was Anthropic's.",
		"It wasn't Anthropic's argument, it was the author's.")
}

// An unterminated double quote is not a quotation either. Treating the rest of
// the text as quoted would make one stray character reject every rewrite.
func TestAnUnterminatedQuoteIsNotAQuotation(t *testing.T) {
	// The stray quote and the negation both survive; only the quotation reading
	// is at issue, so the check must PASS rather than merely stay silent about
	// quotations.
	mustPass(t,
		"He said \"this and nothing else followed.",
		"\"He said this and nothing else followed.")
}

// ---------------------------------------------------------------------------
// Negations
// ---------------------------------------------------------------------------

// The closed list, each form, and a contraction — which survives tokenisation
// whole, so it is matched as one token rather than reconstructed from pieces.
func TestTheNegationVocabulary(t *testing.T) {
	for _, negation := range preserve.Negations() {
		t.Run(negation, func(t *testing.T) {
			current := "It was " + negation + " settled at the time."
			candidate := "It was settled at the time."
			mustFail(t, current, candidate, preserve.ClassNegation, negation)
		})
	}
}

func TestAContractionIsOneNegation(t *testing.T) {
	mustFail(t,
		"They don't agree with the finding.",
		"They agree with the finding.",
		preserve.ClassNegation, "don't")
}

// The vocabulary is declared and non-empty, and includes the forms the design
// names. A gate whose word list were empty would pass everything.
func TestTheNegationVocabularyIsDeclared(t *testing.T) {
	got := preserve.Negations()
	if len(got) == 0 {
		t.Fatalf("the negation vocabulary is empty; the gate would pass every negation change")
	}
	have := map[string]bool{}
	for _, n := range got {
		have[n] = true
	}
	for _, want := range []string{"not", "no", "never", "none", "neither", "nor", "cannot", "nothing"} {
		if !have[want] {
			t.Errorf("the negation vocabulary does not contain %q", want)
		}
	}
}

// Case does not hide a negation: "Not" opening a sentence is the same negation
// as "not" inside one.
func TestNegationsAreMatchedCaseInsensitively(t *testing.T) {
	mustFail(t,
		"Not every reading survives the objection.",
		"Every reading survives the objection.",
		preserve.ClassNegation, "not")
}

// ---------------------------------------------------------------------------
// The report
// ---------------------------------------------------------------------------

// A passing check reports nothing missing, and a failing one reports every
// item, not the first. A gate that stopped at the first failure would make a
// user fix one thing at a time.
func TestEveryMissingItemIsReported(t *testing.T) {
	got := check(t,
		"Anthropic published 5 papers at https://example.com and said \"no more\".",
		"The lab published papers.")

	if got.Preserved {
		t.Fatalf("accepted")
	}
	classes := map[preserve.Class]int{}
	for _, difference := range got.Differences {
		classes[difference.Class]++
	}
	for _, want := range []preserve.Class{
		preserve.ClassEntity, preserve.ClassNumber, preserve.ClassURL, preserve.ClassQuote,
		// The fixture's quotation contains "no more", so a negation is lost too.
		// Omitting it here would let an implementation that reports four classes
		// and silently drops the fifth pass a completeness test.
		preserve.ClassNegation,
	} {
		if classes[want] == 0 {
			t.Errorf("nothing reported for %s; got %v", want, got.Differences)
		}
	}
}

func TestAPassingCheckReportsNothing(t *testing.T) {
	got := check(t, "The sentence was plain and unremarkable.", "The sentence, plainly put, was unremarkable.")
	if !got.Preserved {
		t.Fatalf("rejected: %v", got.Differences)
	}
	if len(got.Differences) != 0 {
		t.Errorf("a passing check reported %v", got.Differences)
	}
}

// Whether an item was lost or invented is reported, since the two call for
// different action: one is a deletion to restore, the other a fabrication to
// remove.
func TestLossAndInventionAreDistinguished(t *testing.T) {
	lost := check(t, "There were 5 objections.", "There were objections.")
	invented := check(t, "There were objections.", "There were 5 objections.")

	if lost.Preserved || invented.Preserved {
		t.Fatalf("one of the two was accepted")
	}
	if lost.Differences[0].Direction == invented.Differences[0].Direction {
		t.Errorf("loss and invention report the same direction %q", lost.Differences[0].Direction)
	}
	if lost.Differences[0].Direction != preserve.DirectionLost {
		t.Errorf("a lost item reports %q, want %q", lost.Differences[0].Direction, preserve.DirectionLost)
	}
	if invented.Differences[0].Direction != preserve.DirectionInvented {
		t.Errorf("an invented item reports %q, want %q", invented.Differences[0].Direction, preserve.DirectionInvented)
	}
}

// ---------------------------------------------------------------------------
// Determinism and shape
// ---------------------------------------------------------------------------

// The same pair gives the same answer, and the report is ordered, so a caller
// can compare two runs and a store can hash the result.
func TestTheCheckIsDeterministic(t *testing.T) {
	current := "Anthropic published 5 papers at https://example.com and said \"no more\" in 2024."
	candidate := "The lab published papers."

	first, second := check(t, current, candidate), check(t, current, candidate)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two checks of the same pair differ:\n%+v\n%+v", first, second)
	}
}

// Class names are asserted against their literals: they reach a user as the
// explanation for a refused rewrite.
func TestClassNames(t *testing.T) {
	for _, c := range []struct {
		class preserve.Class
		want  string
	}{
		{preserve.ClassNumber, "number"},
		{preserve.ClassEntity, "entity"},
		{preserve.ClassNegation, "negation"},
		{preserve.ClassURL, "url"},
		{preserve.ClassQuote, "quote"},
	} {
		if string(c.class) != c.want {
			t.Errorf("class = %q, want %q", c.class, c.want)
		}
	}
}

// Text the tokenizer refuses is an error rather than a silent pass. A gate that
// answered "preserved" for input it could not read would be worse than one that
// failed.
func TestUnreadableTextIsAnError(t *testing.T) {
	invalid := string([]byte{0xff, 0xfe, 0xfd})
	if _, err := preserve.Check("valid text here", invalid); err == nil {
		t.Errorf("an unreadable candidate was accepted rather than reported")
	}
	// And on the current side, where the danger is worse: unreadable text that
	// scanned as containing nothing would make every candidate preserve
	// everything.
	if _, err := preserve.Check(invalid, "valid text here"); err == nil {
		t.Errorf("an unreadable current text was accepted rather than reported")
	}
}

// The gate applies to every mutation, mechanical or otherwise, so it must cope
// with a candidate that is merely whitespace-different without calling that a
// change.
func TestWhitespaceAloneIsNotAChange(t *testing.T) {
	mustPass(t,
		"Anthropic released 5 models in 2024.",
		"Anthropic  released 5 models\nin 2024.")
}

func TestVersionIsDeclared(t *testing.T) {
	if preserve.Version == "" {
		t.Errorf("the gate declares no version; its vocabularies are a versioned contract")
	}
	if !strings.HasSuffix(preserve.Version, "-v1") {
		t.Errorf("Version = %q, want a v1 identifier", preserve.Version)
	}
}

// ---------------------------------------------------------------------------
// Multisets, for every class
// ---------------------------------------------------------------------------

// The duplicate cases above cover numbers and negations. A set comparison used
// for one class only would survive those, so every class is exercised in both
// directions with a duplicate.
func TestEveryClassComparesMultisets(t *testing.T) {
	cases := []struct {
		name  string
		class preserve.Class
		two   string
		one   string
		item  string
	}{
		{
			name: "numbers", class: preserve.ClassNumber,
			two: "There were 3 objections and 3 replies.", one: "There were 3 objections and replies.",
			item: "3",
		},
		{
			name: "entities", class: preserve.ClassEntity,
			two: "Anthropic replied to Anthropic in public.", one: "Anthropic replied to the lab in public.",
			item: "anthropic",
		},
		{
			name: "negations", class: preserve.ClassNegation,
			two: "It was not clear and not settled.", one: "It was not clear and settled.",
			item: "not",
		},
		{
			name: "urls", class: preserve.ClassURL,
			two:  "See https://example.com/a and https://example.com/a again.",
			one:  "See https://example.com/a again.",
			item: "https://example.com/a",
		},
		{
			name: "quotes", class: preserve.ClassQuote,
			two:  "He said \"the same\" and then \"the same\" once more.",
			one:  "He said \"the same\" once more.",
			item: "\"the same\"",
		},
	}

	for _, c := range cases {
		t.Run(c.name+" losing a duplicate", func(t *testing.T) {
			mustFail(t, c.two, c.one, c.class, c.item)
		})
		t.Run(c.name+" inventing a duplicate", func(t *testing.T) {
			mustFail(t, c.one, c.two, c.class, c.item)
		})
	}
}

// ---------------------------------------------------------------------------
// The entity proxy, exercised against its declared rule
// ---------------------------------------------------------------------------

// The proxy is "upper-case first rune, and not a function word". Both halves are
// checked against the real vocabulary rather than a local guess: an
// implementation carrying its own partial copy of the function words would
// diverge from the manifest silently.
func TestEntityMembershipFollowsTheFunctionWordVocabulary(t *testing.T) {
	vocabulary := features.FunctionWords()
	if len(vocabulary) == 0 {
		t.Fatalf("the function-word vocabulary is empty")
	}

	// A capitalised function word is not an entity, so replacing it is not a
	// preservation failure.
	for _, word := range []string{vocabulary[0], vocabulary[len(vocabulary)/2], vocabulary[len(vocabulary)-1]} {
		capitalised := strings.ToUpper(word[:1]) + word[1:]
		t.Run(capitalised, func(t *testing.T) {
			mustPass(t,
				capitalised+" settled the question entirely.",
				"The question was settled entirely.")
		})
	}
}

// A lower-case first rune is not an entity however proper the name, which is the
// under-collecting failure written down. iPhone is the design's own example.
func TestALowerCaseInitialIsNotAnEntity(t *testing.T) {
	for _, name := range []string{"iPhone", "danah"} {
		t.Run(name, func(t *testing.T) {
			mustPass(t,
				"The review mentioned "+name+" in passing.",
				"The review mentioned something in passing.")
		})
	}
}

// And upper case is not an ASCII question: a non-ASCII capital opens an entity
// exactly as an ASCII one does.
func TestANonASCIICapitalOpensAnEntity(t *testing.T) {
	mustFail(t,
		"The paper cites Émile on this point.",
		"The paper cites an earlier writer on this point.",
		preserve.ClassEntity, "Émile")
}

// ---------------------------------------------------------------------------
// The URL trim rule, in full
// ---------------------------------------------------------------------------

// Every character the design says cannot end a URL. Each is placed immediately
// after the URL in the current text and absent in the candidate; the matched
// item must be the URL without it, so the rewrite passes.
func TestEveryTrailingCharacterIsTrimmed(t *testing.T) {
	for _, trailing := range []string{".", ",", ";", ":", "!", "?", ")", "]", "}", "\"", "'"} {
		t.Run(trailing, func(t *testing.T) {
			mustPass(t,
				"Read https://example.com/a"+trailing+" and then stop.",
				"Read https://example.com/a for more.")
		})
	}
}

// A character that CAN end a URL is not trimmed, or the gate would accept a
// changed address.
func TestAPathCharacterIsNotTrimmed(t *testing.T) {
	mustFail(t,
		"Read https://example.com/a/ for more.",
		"Read https://example.com/a for more.",
		preserve.ClassURL, "https://example.com/a/")
}

// ---------------------------------------------------------------------------
// Quotations, in full
// ---------------------------------------------------------------------------

// Several spans in one text are separate items, so losing one is caught even
// when another survives.
func TestMultipleQuotationsAreSeparateItems(t *testing.T) {
	mustFail(t,
		"He said \"the first\" and she said \"the second\" in turn.",
		"He said \"the first\" and she replied in turn.",
		preserve.ClassQuote, "\"the second\"")
}

// An unmatched curly opener is not a quotation, for the same reason an unmatched
// straight one is not.
func TestAnUnmatchedCurlyQuoteIsNotAQuotation(t *testing.T) {
	mustPass(t,
		"He said “this and nothing else followed.",
		"“He said this and nothing else followed.")
}

// ---------------------------------------------------------------------------
// The negation vocabulary as a contract
// ---------------------------------------------------------------------------

// Every declared negation must be a single token under the real tokenizer, or
// the gate would look for something the token stream never produces.
func TestEveryDeclaredNegationIsOneToken(t *testing.T) {
	for _, negation := range preserve.Negations() {
		t.Run(negation, func(t *testing.T) {
			doc, err := text.Admit([]byte(negation))
			if err != nil {
				t.Fatalf("Admit(%q): %v", negation, err)
			}
			tokens := doc.Tokens()
			if len(tokens) != 1 || tokens[0].Text != negation {
				t.Errorf("%q tokenises to %d tokens, want one", negation, len(tokens))
			}
		})
	}
}

// The contraction forms the design names, and a curly apostrophe, which is a
// different rune from the straight one and would otherwise be a silent gap.
func TestContractionFormsAreNegations(t *testing.T) {
	for _, contraction := range []string{"don't", "doesn't", "isn't", "wasn't", "won't", "can't"} {
		t.Run(contraction, func(t *testing.T) {
			mustFail(t,
				"They "+contraction+" agree with the finding.",
				"They agree with the finding.",
				preserve.ClassNegation, contraction)
		})
	}

	t.Run("a curly apostrophe", func(t *testing.T) {
		mustFail(t,
			"They don’t agree with the finding.",
			"They agree with the finding.",
			preserve.ClassNegation, "don’t")
	})
}

// ---------------------------------------------------------------------------
// The report is ordered, and its audit form carries no prose
// ---------------------------------------------------------------------------

// Ordering is an API promise, not an accident of iteration: a caller comparing
// two runs, or hashing a result, needs a stable sequence. Declared order is by
// class, then direction with losses first, then item.
func TestTheReportIsOrdered(t *testing.T) {
	got := check(t, "Anthropic 5", "Bureau 7")

	if got.Preserved {
		t.Fatalf("accepted")
	}
	type key struct {
		class     preserve.Class
		item      string
		direction preserve.Direction
	}
	order := make([]key, 0, len(got.Differences))
	for _, difference := range got.Differences {
		order = append(order, key{difference.Class, difference.Item, difference.Direction})
	}
	want := []key{
		{preserve.ClassNumber, "5", preserve.DirectionLost},
		{preserve.ClassNumber, "7", preserve.DirectionInvented},
		{preserve.ClassEntity, "anthropic", preserve.DirectionLost},
		{preserve.ClassEntity, "bureau", preserve.DirectionInvented},
	}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("report order =\n%+v\nwant\n%+v", order, want)
	}
}

// The item text is for the caller, who is about to be told why their rewrite was
// refused. The store may not have it: its privacy invariant forbids any
// reversible prose representation, and rewrite's audit whitelist is where these
// land.
func TestTheAuditIdentifiersCarryNoProse(t *testing.T) {
	current := "Anthropic published 5 papers at https://example.com and said \"no more\"."
	got := check(t, current, "The bureau published papers.")

	if got.Preserved {
		t.Fatalf("accepted")
	}
	identifiers := got.Identifiers()
	if len(identifiers) != len(got.Differences) {
		t.Fatalf("got %d identifiers for %d differences", len(identifiers), len(got.Differences))
	}

	// The digest is sixteen lower-case hex characters, which is what rules out an
	// encoding of the item for every item that is not itself sixteen hex
	// characters. A containment check cannot do that job alone: a hex digest
	// contains hex characters, so "does it contain 5" is a coincidence rather
	// than a finding.
	for _, identifier := range identifiers {
		parts := strings.Split(identifier, ":")
		if len(parts) != 4 {
			t.Fatalf("identifier %q has %d parts, want four", identifier, len(parts))
		}
		if !hexDigest(parts[3]) {
			t.Errorf("identifier %q does not end in sixteen lower-case hex characters", identifier)
		}
	}
	joined := strings.Join(identifiers, "\n")
	for _, difference := range got.Differences {
		if len(difference.Item) > 4 && strings.Contains(joined, difference.Item) {
			t.Errorf("an identifier contains the item text %q", difference.Item)
		}
	}
	// And no word of the source survives either, since a fragment is a
	// reversible derivative too.
	for _, word := range strings.Fields(current) {
		if len(word) > 5 && strings.Contains(joined, word) {
			t.Errorf("an identifier contains the word %q from the text", word)
		}
	}

	// They still say enough to tell two failures apart and to count them.
	for _, identifier := range identifiers {
		if !strings.HasPrefix(identifier, preserve.Version+":") {
			t.Errorf("identifier %q is not bound to the gate version", identifier)
		}
	}
	distinct := map[string]bool{}
	for _, identifier := range identifiers {
		distinct[identifier] = true
	}
	if len(distinct) != len(identifiers) {
		t.Errorf("identifiers collide: %v", identifiers)
	}
}

// The same difference gives the same identifier every time, or an audit record
// could not be compared across runs.
func TestTheAuditIdentifiersAreStable(t *testing.T) {
	first := check(t, "Anthropic released 5 models.", "The bureau released models.")
	second := check(t, "Anthropic released 5 models.", "The bureau released models.")

	if !reflect.DeepEqual(first.Identifiers(), second.Identifiers()) {
		t.Errorf("identifiers differ between runs:\n%v\n%v", first.Identifiers(), second.Identifiers())
	}
}

func TestDirectionNames(t *testing.T) {
	if preserve.DirectionLost != "lost" {
		t.Errorf("DirectionLost = %q", preserve.DirectionLost)
	}
	if preserve.DirectionInvented != "invented" {
		t.Errorf("DirectionInvented = %q", preserve.DirectionInvented)
	}
}

// ---------------------------------------------------------------------------
// The identifier is derived, not positional
// ---------------------------------------------------------------------------

// A positional identifier — the version and an index — satisfies every property
// asserted so far while telling an auditor nothing and changing whenever an
// unrelated failure appears beside it. The same difference must therefore carry
// the same identifier in different company.
func TestAnIdentifierFollowsItsDifferenceNotItsPosition(t *testing.T) {
	alone := check(t, "There were 5 objections.", "There were objections.")
	crowded := check(t, "Anthropic said \"no more\" about 5 objections at https://example.com.", "The bureau spoke about objections.")

	if alone.Preserved || crowded.Preserved {
		t.Fatalf("one of the two was accepted")
	}

	find := func(r preserve.Result) string {
		t.Helper()
		for i, difference := range r.Differences {
			if difference.Class == preserve.ClassNumber && difference.Item == "5" && difference.Direction == preserve.DirectionLost {
				return r.Identifiers()[i]
			}
		}
		t.Fatalf("the lost number 5 is not in the report")
		return ""
	}

	if find(alone) != find(crowded) {
		t.Errorf("the same lost number has identifier %q alone and %q among other failures; the identifier is positional",
			find(alone), find(crowded))
	}
}

// And its shape is declared: the gate version, the class, the direction, and a
// digest of the item. Checked against every difference in a report spanning all
// five classes in both directions, so a gate cannot bind the parts correctly for
// one class and arbitrarily for the rest.
func TestEveryIdentifierIsBoundToItsDifference(t *testing.T) {
	got := check(t,
		"Anthropic 5 not https://example.com/a \"one\"",
		"Bureau 7 never https://example.com/b \"two\"")

	identifiers := got.Identifiers()
	if len(identifiers) != len(got.Differences) {
		t.Fatalf("got %d identifiers for %d differences", len(identifiers), len(got.Differences))
	}
	if len(got.Differences) != 10 {
		t.Fatalf("got %d differences, want 10 — the fixture is not exercising every class", len(got.Differences))
	}

	seen := map[string]int{}
	for i, difference := range got.Differences {
		parts := strings.Split(identifiers[i], ":")
		if len(parts) != 4 {
			t.Errorf("identifier %q has %d parts, want version:class:direction:digest", identifiers[i], len(parts))
			continue
		}
		if parts[0] != preserve.Version {
			t.Errorf("%v: identifier version = %q, want %q", difference, parts[0], preserve.Version)
		}
		if parts[1] != string(difference.Class) {
			t.Errorf("%v: identifier class = %q, want %q", difference, parts[1], difference.Class)
		}
		if parts[2] != string(difference.Direction) {
			t.Errorf("%v: identifier direction = %q, want %q", difference, parts[2], difference.Direction)
		}
		if !hexDigest(parts[3]) {
			t.Errorf("%v: digest %q is not sixteen lower-case hex characters", difference, parts[3])
		}
		if len(difference.Item) > 4 && strings.Contains(identifiers[i], difference.Item) {
			t.Errorf("identifier %q contains the item text %q", identifiers[i], difference.Item)
		}
		if first, ok := seen[identifiers[i]]; ok {
			t.Errorf("differences %d and %d share identifier %q", first, i, identifiers[i])
		}
		seen[identifiers[i]] = i
	}
}

// A digest that is constant per class and direction would pass every assertion
// above — the parts would all be correct and each identifier distinct, because
// no two of those ten differences share a class and a direction. Two items that
// do must not collide.
func TestTwoItemsOfOneClassAndDirectionDoNotCollide(t *testing.T) {
	got := check(t, "It took 3 or 5 tries.", "It took several tries.")

	if len(got.Identifiers()) != 2 {
		t.Fatalf("got %d identifiers, want 2 (lost 3 and lost 5): %v", len(got.Identifiers()), got.Differences)
	}
	if got.Identifiers()[0] == got.Identifiers()[1] {
		t.Errorf("the lost 3 and the lost 5 share identifier %q; the digest does not depend on the item",
			got.Identifiers()[0])
	}
}

// ---------------------------------------------------------------------------
// Direction, for every class
// ---------------------------------------------------------------------------

// A gate labelling every difference `lost` passes every direction assertion made
// so far except the two on numbers and entities. All five, both ways.
func TestDirectionIsCorrectForEveryClass(t *testing.T) {
	cases := []struct {
		name  string
		class preserve.Class
		with  string
		item  string
	}{
		{name: "number", class: preserve.ClassNumber, with: "It took 5 tries.", item: "5"},
		{name: "entity", class: preserve.ClassEntity, with: "It named Anthropic once.", item: "anthropic"},
		{name: "negation", class: preserve.ClassNegation, with: "It was not settled.", item: "not"},
		{name: "url", class: preserve.ClassURL, with: "It cited https://example.com/a here.", item: "https://example.com/a"},
		{name: "quote", class: preserve.ClassQuote, with: "It said \"the thing\" plainly.", item: "\"the thing\""},
	}
	const without = "It was reported."

	direction := func(r preserve.Result, class preserve.Class, item string) preserve.Direction {
		t.Helper()
		for _, difference := range r.Differences {
			if difference.Class == class && difference.Item == item {
				return difference.Direction
			}
		}
		t.Fatalf("%s %q is not in the report: %v", class, item, r.Differences)
		return ""
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := direction(check(t, c.with, without), c.class, c.item); got != preserve.DirectionLost {
				t.Errorf("dropping %s reports %q, want %q", c.name, got, preserve.DirectionLost)
			}
			if got := direction(check(t, without, c.with), c.class, c.item); got != preserve.DirectionInvented {
				t.Errorf("adding %s reports %q, want %q", c.name, got, preserve.DirectionInvented)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Ordering, across every class and within one
// ---------------------------------------------------------------------------

// The declared order is class, then direction with losses first, then item. This
// exercises all five classes in both directions at once, which the two-class
// case above cannot.
func TestTheReportIsOrderedAcrossEveryClass(t *testing.T) {
	got := check(t,
		"Anthropic 5 not https://example.com/a \"one\"",
		"Bureau 7 never https://example.com/b \"two\"")

	if got.Preserved {
		t.Fatalf("accepted")
	}
	type key struct {
		class     preserve.Class
		item      string
		direction preserve.Direction
	}
	order := make([]key, 0, len(got.Differences))
	for _, difference := range got.Differences {
		order = append(order, key{difference.Class, difference.Item, difference.Direction})
	}
	want := []key{
		{preserve.ClassNumber, "5", preserve.DirectionLost},
		{preserve.ClassNumber, "7", preserve.DirectionInvented},
		{preserve.ClassEntity, "anthropic", preserve.DirectionLost},
		{preserve.ClassEntity, "bureau", preserve.DirectionInvented},
		{preserve.ClassNegation, "not", preserve.DirectionLost},
		{preserve.ClassNegation, "never", preserve.DirectionInvented},
		{preserve.ClassURL, "https://example.com/a", preserve.DirectionLost},
		{preserve.ClassURL, "https://example.com/b", preserve.DirectionInvented},
		{preserve.ClassQuote, "\"one\"", preserve.DirectionLost},
		{preserve.ClassQuote, "\"two\"", preserve.DirectionInvented},
	}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("report order =\n%+v\nwant\n%+v", order, want)
	}
}

// And within one class and direction, items are ordered by their own text rather
// than by where they appeared.
func TestItemsAreOrderedWithinAClass(t *testing.T) {
	got := check(t, "It took 9 then 3 then 7 tries.", "It took several tries.")

	items := make([]string, 0, len(got.Differences))
	for _, difference := range got.Differences {
		if difference.Class == preserve.ClassNumber {
			items = append(items, difference.Item)
		}
	}
	if want := []string{"3", "7", "9"}; !reflect.DeepEqual(items, want) {
		t.Errorf("numbers reported in order %v, want %v — ordered by item, not by position", items, want)
	}
}

// ---------------------------------------------------------------------------
// The vocabulary really is the closed list
// ---------------------------------------------------------------------------

// Inclusion is not enough. The design calls this a *closed* list, and a gate free
// to recognise an undeclared word would refuse valid rewrites for a reason no
// reader could look up. So the whole vocabulary is pinned, exactly.
func TestTheDeclaredVocabularyIsExactlyTheClosedList(t *testing.T) {
	bare := []string{"cannot", "neither", "never", "no", "nobody", "none", "nor", "not", "nothing", "nowhere", "without"}
	contractions := []string{"aren't", "can't", "couldn't", "didn't", "doesn't", "don't", "hadn't",
		"hasn't", "haven't", "isn't", "shouldn't", "wasn't", "weren't", "won't", "wouldn't"}

	want := map[string]bool{}
	for _, word := range bare {
		want[word] = true
	}
	for _, word := range contractions {
		want[word] = true
		want[strings.ReplaceAll(word, "'", "’")] = true
	}

	got := map[string]bool{}
	for _, word := range preserve.Negations() {
		if got[word] {
			t.Errorf("the declared vocabulary lists %q twice", word)
		}
		got[word] = true
	}

	for word := range want {
		if !got[word] {
			t.Errorf("the declared vocabulary is missing %q", word)
		}
	}
	for word := range got {
		if !want[word] {
			t.Errorf("the declared vocabulary contains undeclared %q", word)
		}
	}
	if len(preserve.Negations()) != len(want) {
		t.Errorf("the declared vocabulary has %d entries, want %d", len(preserve.Negations()), len(want))
	}
}

// And the boundary holds in behavior, not only in the list: a word that weakens a
// claim without reversing it is not a negation, so dropping it is a style edit.
func TestDiminishersAreNotNegations(t *testing.T) {
	for _, word := range []string{"hardly", "barely", "scarcely", "rarely"} {
		t.Run(word, func(t *testing.T) {
			mustPass(t, "The claim was "+word+" supported by evidence.", "The claim lacked support.")
		})
	}
}

// Case folding applies to contractions too, not only to bare words.
func TestContractionsAreMatchedCaseInsensitively(t *testing.T) {
	mustFail(t,
		"Don't take the finding at face value.",
		"Take the finding at face value.",
		preserve.ClassNegation, "don't")
}

// ---------------------------------------------------------------------------
// The list bounds what is recognised, not only what is declared
// ---------------------------------------------------------------------------

// Closing Negations() closes the declaration. A gate could return exactly those
// forty-one entries and still match others, which would refuse a valid rewrite
// for a reason no reader could look up. So: every declared form is matched in
// every case, and nothing else is matched at all.
func TestEveryDeclaredFormIsMatchedInEveryCase(t *testing.T) {
	for _, declared := range preserve.Negations() {
		if declared != strings.ToLower(declared) {
			t.Errorf("the declared vocabulary contains %q, which is not lower case", declared)
			continue
		}
		for _, form := range []string{
			declared,
			strings.ToUpper(declared),
			strings.ToUpper(declared[:1]) + declared[1:],
		} {
			t.Run(form, func(t *testing.T) {
				// The reported item is folded, or a current "Not" and a candidate
				// "not" would be two different items and the folding would be undone
				// by the multiset comparison that follows it.
				mustFail(t, "alpha "+form+" omega", "alpha omega", preserve.ClassNegation, declared)
			})
		}
	}
}

// And a word outside the list is not a negation, however much it reads like one.
// `ain't` is the case that matters: an implementation matching every `n't` token
// by shape rather than by the list would treat it as declared.
func TestUndeclaredNegatorsAreNotMatched(t *testing.T) {
	for _, word := range []string{"ain't", "nope", "nay", "nix", "lacked", "absent", "n't"} {
		t.Run(word, func(t *testing.T) {
			got := check(t, "alpha "+word+" omega", "alpha omega")
			for _, difference := range got.Differences {
				if difference.Class == preserve.ClassNegation {
					t.Errorf("%q is reported as a negation but is not in the declared list", difference.Item)
				}
			}
		})
	}
}

// A capitalised negation is also a capitalised token, so it is reported twice —
// once by each proxy. That is the entity proxy's declared over-collection, and
// the two differences are independent rather than one masking the other.
func TestACapitalisedNegationIsAlsoAnEntity(t *testing.T) {
	got := check(t, "alpha Nothing omega", "alpha omega")

	var negation, entity bool
	for _, difference := range got.Differences {
		switch {
		case difference.Class == preserve.ClassNegation && difference.Item == "nothing":
			negation = true
		case difference.Class == preserve.ClassEntity && difference.Item == "nothing":
			entity = true
		}
	}
	if !negation {
		t.Errorf("no folded negation difference for Nothing: %v", got.Differences)
	}
	if !entity {
		t.Errorf("no entity difference for Nothing: %v — the negation match suppressed the entity proxy", got.Differences)
	}
}

// ---------------------------------------------------------------------------
// The digest is one-way, not an encoding
// ---------------------------------------------------------------------------

// "A digest of the item" is satisfied by hex or base64 of the item itself, which
// contains no literal prose, is stable, is distinct per item, and is trivially
// reversible — prose in a costume, and a breach of the store's invariant that
// every other identifier test would pass. So the algorithm is pinned, by vectors
// computed outside this package.
func TestTheIdentifierDigestIsPinned(t *testing.T) {
	cases := []struct {
		current, candidate string
		want               string
	}{
		{"It took 5 tries.", "It took tries.", "preserve-v1:number:lost:3d4c981bf761d9b8"},
		// #93 changed this value. Entity items are folded before comparison, so
		// the digest preimage is now `bureau` rather than `Bureau`.
		//
		// The consequence is worth knowing: preserve identifiers for ENTITY
		// differences are not comparable across that change. An identifier
		// recorded before it and one recorded after it describe the same logical
		// loss with different digests. Every identifier remains well formed and
		// non-reversible; they simply no longer collide. Acceptable because
		// `rewrite` has never shipped a release, so there are no audit records in
		// the wild to invalidate — but it is a one-way door and belongs on the
		// record rather than in a diff.
		{"alpha omega", "alpha Bureau omega", "preserve-v1:entity:invented:7f1cc0d8aee219dd"},
		{"alpha don’t omega", "alpha omega", "preserve-v1:negation:lost:e5e4a9f9def6b49d"},
		{"alpha \"one\" omega", "alpha omega", "preserve-v1:quote:lost:8979dd3e31925881"},
		{"alpha https://example.com/a omega", "alpha omega", "preserve-v1:url:lost:4d897f4d66e152ae"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := check(t, c.current, c.candidate)
			var found bool
			for _, identifier := range got.Identifiers() {
				if identifier == c.want {
					found = true
				}
			}
			if !found {
				t.Errorf("identifiers %v do not contain %q\n  differences: %v", got.Identifiers(), c.want, got.Differences)
			}
		})
	}
}

// Making a name possessive changes its surface form, so it is a preservation
// failure — the tokenizer keeps `Anthropic's` whole, and the no-normalisation
// rule that makes 1,000 differ from 1000 makes `Anthropic's` differ from
// `Anthropic`. Found while auditing a fixture that assumed otherwise. It is the
// same accepted cost, written down rather than discovered by a user.
func TestAPossessiveIsADifferentEntity(t *testing.T) {
	got := check(t, "The argument from Anthropic was brief.", "Anthropic's argument was brief.")

	if got.Preserved {
		t.Fatalf("accepted; a possessive is a different surface form")
	}
	var lost, invented bool
	for _, difference := range got.Differences {
		if difference.Class != preserve.ClassEntity {
			continue
		}
		if difference.Item == "anthropic" && difference.Direction == preserve.DirectionLost {
			lost = true
		}
		if difference.Item == "anthropic's" && difference.Direction == preserve.DirectionInvented {
			invented = true
		}
	}
	if !lost || !invented {
		t.Errorf("want Anthropic lost and Anthropic's invented, got %v", got.Differences)
	}
}

// ---------------------------------------------------------------------------
// The identifier grammar, owned here
// ---------------------------------------------------------------------------

// rewrite's audit record holds these identifiers, and nothing enforced that it
// really did — the item text sat in that record for two slices. The consumer
// cannot check the shape without duplicating this package's grammar, so the
// grammar is exported and this package owns it.
func TestValidIdentifierAcceptsWhatIdentifiersProduces(t *testing.T) {
	got := check(t,
		"Anthropic 5 not https://example.com/a \"one\"",
		"Bureau 7 never https://example.com/b \"two\"")
	if len(got.Identifiers()) != 10 {
		t.Fatalf("got %d identifiers, want 10", len(got.Identifiers()))
	}
	for _, identifier := range got.Identifiers() {
		if !preserve.ValidIdentifier(identifier) {
			t.Errorf("ValidIdentifier(%q) = false for an identifier this package produced", identifier)
		}
	}
}

func TestValidIdentifierRejectsEverythingElse(t *testing.T) {
	for _, c := range []struct{ name, value string }{
		{"the item text the audit record used to hold", "number:1979"},
		{"a bare url", "url:example.com"},
		{"prose", "the author wrote 1979"},
		{"empty", ""},
		{"too few parts", "preserve-v1:number:lost"},
		{"too many parts", "preserve-v1:number:lost:3d4c981bf761d9b8:extra"},
		{"unknown version", "preserve-v2:number:lost:3d4c981bf761d9b8"},
		{"unknown class", "preserve-v1:sentiment:lost:3d4c981bf761d9b8"},
		{"unknown direction", "preserve-v1:number:moved:3d4c981bf761d9b8"},
		{"digest too short", "preserve-v1:number:lost:3d4c98"},
		{"digest too long", "preserve-v1:number:lost:3d4c981bf761d9b8a"},
		{"digest not hex", "preserve-v1:number:lost:zzzzzzzzzzzzzzzz"},
		{"digest upper case", "preserve-v1:number:lost:3D4C981BF761D9B8"},
		{"whitespace in the digest", "preserve-v1:number:lost:3d4c981b f761d9b"},
		{"leading space", " preserve-v1:number:lost:3d4c981bf761d9b8"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if preserve.ValidIdentifier(c.value) {
				t.Errorf("ValidIdentifier(%q) = true", c.value)
			}
		})
	}
}

// Every declared class and direction is accepted, so the grammar cannot drift
// from the vocabulary it is supposed to describe.
func TestValidIdentifierCoversEveryClassAndDirection(t *testing.T) {
	for _, class := range []preserve.Class{
		preserve.ClassNumber, preserve.ClassEntity, preserve.ClassNegation, preserve.ClassURL, preserve.ClassQuote,
	} {
		for _, direction := range []preserve.Direction{preserve.DirectionLost, preserve.DirectionInvented} {
			identifier := preserve.Version + ":" + string(class) + ":" + string(direction) + ":3d4c981bf761d9b8"
			if !preserve.ValidIdentifier(identifier) {
				t.Errorf("ValidIdentifier(%q) = false", identifier)
			}
		}
	}
}
