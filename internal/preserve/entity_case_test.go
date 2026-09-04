package preserve_test

// #93. The preservation guard refuses almost every rewrite, and almost always
// for a reason that is not a loss.
//
// `extract` counts any capitalized lexical token that is not a function word as
// an entity, with no sentence-position awareness. So a word is an entity purely
// for starting a sentence, and any candidate that merges or splits a sentence
// changes which words are sentence-initial:
//
//	before: One of the pieces is a library. Every record is signed.
//	after:  One of the pieces is a library, and every record is signed.
//
// `Every` became `every`. Nothing was lost — the word is still there, in lower
// case, because it is no longer opening a sentence — but the guard reports
// `entity:lost` and the candidate is refused.
//
// Measured across two models and nine recorded attempts on one real corpus:
//
//	entity:lost       5
//	entity:invented   6
//	negation:invented 2
//
// Eleven of thirteen were this. The guard was doing almost all of its work on
// false positives, and it refused every candidate a 20B model produced.
//
// # The fix
//
// Not "key the multiplicity map by the folded token", which was my first
// specification and cannot work: the INSERTION condition also requires an
// uppercase first rune, so a lowercase `every` is never inserted at all and its
// count stays at zero whatever the key is.
//
// Two steps, and they are separate:
//
//  1. The WATCH SET is the folded union of the capitalized, non-function tokens
//     of BOTH texts. The union matters: a name capitalized only in the
//     candidate would otherwise go unwatched, and an invention would be missed.
//  2. Occurrences are counted over ALL lexical tokens of each text, in either
//     case. So `Every` in the current text and `every` in the candidate are one
//     occurrence each and the item is preserved.
//
// "Folded" means Unicode default case folding — `cases.Fold`, which the package
// already uses for the function-word and negation lookups — and NOT
// `strings.ToLower`. The two differ, and the difference is asserted below,
// because ToLower passes every ASCII test in this file while violating the
// contract.
//
// And the reported `Difference.Item` is the folded key, digested as-is. Keeping
// a surface form would be underspecified as soon as two spellings contribute to
// one folded difference — `Postgres POSTGRES` collapsing to `postgres` is a loss
// of one occurrence, and there is no principled answer to which spelling to
// report. Folded is deterministic, and it keeps the existing model in which the
// identifier derives from the difference.
//
// Step 2 is the change. The watch set still comes only from capitalized tokens,
// which is what keeps this from degenerating into a bag-of-words check — a
// lowercase word in both texts is not watched, and substituting it is allowed.
// That is asserted below.
//
// # What it gives up
//
// More than product-name styling, and worth stating plainly rather than
// discovering later. Capitalization-dependent meanings collapse:
// `Polish`/`polish`, `Turkey`/`turkey`, `March`/`march`. After this change the
// guard cannot tell them apart.
//
// So this guard is explicitly a COARSE LEXICAL-PRESENCE PROXY: it answers "is
// the word still here", not "does it still mean the same thing". Case fidelity
// is not its job and needs to be owned somewhere else if it is wanted. The
// alternative — a sentence splitter, so capitalization at position zero can be
// discounted — trades these false positives for missed sentence-initial names,
// and needs a splitter in a package that has none.
//
// # What this does NOT change
//
// Acronyms were a worry and are not affected. The function-word filter already
// folds before testing, so `IT` was excluded before this change and is excluded
// after it: `it` is one of the 147 function words. Verified rather than assumed.

import (
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/preserve"
)

func checked(t *testing.T, current, candidate string) preserve.Result {
	t.Helper()
	got, err := preserve.Check(current, candidate)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return got
}

// classes reports the class:direction pairs a result found, for assertions that
// care which guard fired rather than merely that one did.
func classes(r preserve.Result) []string {
	out := make([]string, 0, len(r.Differences))
	for _, d := range r.Differences {
		out = append(out, string(d.Class)+":"+string(d.Direction))
	}
	return out
}

// ---------------------------------------------------------------------------
// The defect
// ---------------------------------------------------------------------------

// Restructuring across a sentence boundary preserves. This is the reported bug
// and the case that blocks real use: merging and splitting sentences is most of
// what a prose rewrite does.
func TestRestructuringSentencesPreserves(t *testing.T) {
	for _, c := range []struct{ name, current, candidate string }{
		{
			"merge two sentences",
			"One of the pieces is a library. Every record is signed.",
			"One of the pieces is a library, and every record is signed.",
		},
		{
			"split one sentence",
			"Every record is signed, and verification points at the entry that changed.",
			"Every record is signed. Verification points at the entry that changed.",
		},
		{
			"reorder two sentences",
			"Every record is signed. Verification points at the entry that changed.",
			"Verification points at the entry that changed. Every record is signed.",
		},
		{
			"the real case from the corpus",
			"One of the open source pieces I have been building is a library for evidence chains. " +
				"Every record is signed and linked to the one before it.",
			"One of the open source pieces I have been building is a library for evidence chains, " +
				"and every record is signed and linked to the one before it.",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := checked(t, c.current, c.candidate)
			if !got.Preserved {
				t.Errorf("refused a restructuring that lost nothing: %v", classes(got))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// What the guard must still catch
// ---------------------------------------------------------------------------

// A named thing dropped from the text is still a loss. This is what the guard
// is for, and the fix must not buy the case above by giving it up.
func TestALostEntityIsStillCaught(t *testing.T) {
	got := checked(t,
		"The library stores every record in Postgres and verifies them on read.",
		"The library stores every record and verifies them on read.")

	if got.Preserved {
		t.Fatal("a dropped entity was preserved")
	}
	if want := "entity:lost"; !contains(classes(got), want) {
		t.Errorf("differences = %v, want one of %q", classes(got), want)
	}
}

// And a named thing the candidate invented is still caught, in the other
// direction. A rewrite that adds a product name nobody mentioned is fabricating.
func TestAnInventedEntityIsStillCaught(t *testing.T) {
	got := checked(t,
		"The library stores every record and verifies them on read.",
		"The library stores every record in Postgres and verifies them on read.")

	if got.Preserved {
		t.Fatal("an invented entity was preserved")
	}
	if want := "entity:invented"; !contains(classes(got), want) {
		t.Errorf("differences = %v, want one of %q", classes(got), want)
	}
}

// Multiplicity still counts. Two mentions collapsing to one is a loss, and
// folding case must not turn the comparison into mere set membership.
func TestLosingOneOfTwoMentionsIsStillCaught(t *testing.T) {
	got := checked(t,
		"Postgres holds the records and Postgres verifies them.",
		"Postgres holds the records and verifies them.")

	if got.Preserved {
		t.Fatal("losing one of two mentions was preserved")
	}
	if want := "entity:lost"; !contains(classes(got), want) {
		t.Errorf("differences = %v, want one of %q", classes(got), want)
	}
}

// ---------------------------------------------------------------------------
// The deliberate consequence
// ---------------------------------------------------------------------------

// Lowercasing a name mid-sentence is NOT a preservation failure after this
// change. Asserted rather than left as a side effect, because it is a real
// loosening and the next person should find it stated.
//
// The guard's property is that a named thing present in the current text is
// present in the candidate. Case is a style decision a rewrite may make, and the
// failure this guard exists to prevent — the word disappearing — is unaffected.
func TestChangingTheCaseOfANameIsNotALoss(t *testing.T) {
	got := checked(t,
		"The records live in Postgres and are verified on read.",
		"The records live in postgres and are verified on read.")

	if !got.Preserved {
		t.Errorf("case-folding a name was reported as a loss: %v", classes(got))
	}
}

// ---------------------------------------------------------------------------
// The watch set is still only capitalized tokens
// ---------------------------------------------------------------------------

// A word that is lower case in BOTH texts is not watched, so substituting it is
// allowed. This is the boundary that stops the fix becoming a bag-of-words
// check.
//
// Counting occurrences over all lexical tokens (step 2) is only safe because the
// watch set still comes from capitalized tokens (step 1). An implementation that
// dropped the uppercase requirement entirely would make every content word an
// entity, and then no rewrite could change any word — which refuses more than
// the bug being fixed.
func TestALowercaseWordIsNotWatched(t *testing.T) {
	got := checked(t,
		"The library stores every record and verifies them on read.",
		"The library keeps every entry and checks them on load.")

	if !got.Preserved {
		t.Errorf("substituting lower-case content words was refused: %v\n"+
			"the watch set must come from capitalized tokens only, or this guard "+
			"forbids rewriting at all", classes(got))
	}
}

// And a capitalized word still IS watched even when the candidate lower-cases
// it somewhere it could not be a sentence opener. The pairing of the two steps
// is what makes the guard mean anything: watch the capitalized words, then find
// them in either case.
func TestACapitalizedWordIsWatchedInEitherCase(t *testing.T) {
	// Present, lower-cased, mid-sentence: preserved.
	kept := checked(t,
		"The records live in Postgres and are verified on read.",
		"The records live in postgres and are verified on read.")
	if !kept.Preserved {
		t.Errorf("a watched word present in lower case was called lost: %v", classes(kept))
	}

	// Absent altogether: lost, in either case.
	gone := checked(t,
		"The records live in Postgres and are verified on read.",
		"The records live somewhere and are verified on read.")
	if gone.Preserved {
		t.Error("a watched word absent from the candidate was called preserved")
	}
}

// ---------------------------------------------------------------------------
// The reported item, and mixed-case multiplicity
// ---------------------------------------------------------------------------

// The reported item is the folded key, not a surface spelling. Pinned because
// the identifier digests it, and because a surface-selecting implementation
// would be non-deterministic as soon as two spellings collapse into one item.
func TestTheReportedEntityItemIsTheFoldedKey(t *testing.T) {
	got := checked(t,
		"The records live in Postgres and are verified on read.",
		"The records live somewhere and are verified on read.")

	if got.Preserved {
		t.Fatal("the fixture preserved; it is meant to lose the name")
	}
	found := false
	for _, d := range got.Differences {
		if d.Class != preserve.ClassEntity {
			continue
		}
		found = true
		if d.Item != "postgres" {
			t.Errorf("entity item = %q, want the folded key %q", d.Item, "postgres")
		}
	}
	if !found {
		t.Fatalf("no entity difference: %v", classes(got))
	}
}

// Two spellings of one name are two occurrences of one item, so collapsing them
// to a single mention is a loss.
//
// This is the case a set-based implementation gets wrong: `postgres` is present
// in both texts, so set membership says preserved, and the guard would miss a
// mention genuinely disappearing. It is also the case that makes reporting a
// surface spelling underspecified, which is why the item is folded.
func TestMixedCaseMentionsCountTowardsMultiplicity(t *testing.T) {
	got := checked(t,
		"Postgres holds the records and POSTGRES verifies them.",
		"postgres holds the records and verifies them.")

	if got.Preserved {
		t.Fatal("two mentions collapsing to one was preserved; multiplicity is not set membership")
	}
	for _, d := range got.Differences {
		if d.Class == preserve.ClassEntity && d.Item != "postgres" {
			t.Errorf("entity item = %q, want %q", d.Item, "postgres")
		}
	}
}

// And the union half of the watch set: a name capitalized only in the CANDIDATE
// is still watched, or inventions go unnoticed.
func TestAnInventionIsWatchedEvenThoughItIsAbsentFromTheCurrentText(t *testing.T) {
	got := checked(t,
		"The records live somewhere and are verified on read.",
		"The records live in Postgres and are verified on read.")

	if got.Preserved {
		t.Fatal("an invented name was preserved; the watch set must be the union of both texts")
	}
	if !contains(classes(got), "entity:invented") {
		t.Errorf("differences = %v, want an entity:invented", classes(got))
	}
}

// Folding is Unicode case folding, not lower-casing.
//
// `cases.Fold` maps ß to ss, so `Straße` and `STRASSE` are the same item.
// `strings.ToLower` does not: it gives `straße` and `strasse`, which are
// different items, and the guard would report a loss and an invention for a word
// that is still there.
//
// Every other test in this file is ASCII and passes under either, so without
// this one an implementation could reach for ToLower — the obvious choice — and
// look correct. Measured: fold("Straße") == fold("STRASSE") is true;
// ToLower("Straße") == ToLower("STRASSE") is false.
func TestFoldingIsUnicodeCaseFoldingAndNotLowerCasing(t *testing.T) {
	got := checked(t,
		"The office on Straße 12 was reviewed.",
		"The office on STRASSE 12 was reviewed.")

	if !got.Preserved {
		t.Fatalf("case folding did not treat Straße and STRASSE as one item: %v\n"+
			"strings.ToLower gives straße and strasse; cases.Fold gives strasse for both",
			got.Differences)
	}

	// And when it is genuinely lost, the item is the FOLDED key, which is the
	// value the identifier digests.
	lost := checked(t,
		"The office on Straße 12 was reviewed.",
		"The office there was reviewed.")
	found := false
	for _, d := range lost.Differences {
		if d.Class != preserve.ClassEntity {
			continue
		}
		found = true
		if d.Item != "strasse" {
			t.Errorf("entity item = %q, want %q — the Unicode fold, not the lower-cased form",
				d.Item, "strasse")
		}
	}
	if !found {
		t.Errorf("no entity difference for a dropped name: %v", classes(lost))
	}
}

// The symmetric seam: a name lower case in the CURRENT text and capitalized in
// the candidate preserves.
//
// The watch set is the union of both texts, so `postgres` enters it from the
// candidate. The count then has to find the lower-case occurrence in the current
// text — which only happens if occurrences are counted over all lexical tokens
// on BOTH sides, not just on the side the key came from.
//
// The other direction and the absent-current invention are covered above; this
// is the seam between them and an implementation can get it alone wrong.
func TestALowerCaseCurrentAndCapitalizedCandidatePreserves(t *testing.T) {
	got := checked(t,
		"The records live in postgres and are verified on read.",
		"The records live in Postgres and are verified on read.")

	if !got.Preserved {
		t.Errorf("capitalizing a name the current text spelled in lower case was refused: %v\n"+
			"the key enters the watch set from the candidate, and its occurrence in the "+
			"current text must still be counted", classes(got))
	}
}

// ---------------------------------------------------------------------------
// Folding is scoped to entities
// ---------------------------------------------------------------------------

// A URL path is case-sensitive and must not fold. Changing one is a real change
// to where a reader is sent.
func TestURLsAreStillCompared(t *testing.T) {
	got := checked(t,
		"The spec is at https://example.com/Docs/Spec and worth reading.",
		"The spec is at https://example.com/docs/spec and worth reading.")

	if got.Preserved {
		t.Fatal("a changed URL path was preserved; paths are case-sensitive")
	}
	if !contains(classes(got), "url:lost") {
		t.Errorf("differences = %v, want a url:lost", classes(got))
	}
}

// A quotation is the author's own words, and changing their case changes what
// is being attributed.
//
// The quoted words are chosen so that ONLY the quote class can fire. An earlier
// version used "Break it" -> "break it", where `Break` is also a capitalized
// non-function token — so the test would have passed on an entity:lost while
// quote comparison was broken. `it` is a function word, so nothing here reaches
// the entity class.
func TestQuotesAreStillCompared(t *testing.T) {
	got := checked(t,
		`The reviewer said "it broke" and meant it.`,
		`The reviewer said "It broke" and meant it.`)

	if got.Preserved {
		t.Fatal("a changed quotation was preserved")
	}
	if !contains(classes(got), "quote:lost") {
		t.Errorf("differences = %v, want a quote:lost; a pass here on some other "+
			"class would not be evidence about quotations", classes(got))
	}
}

// Negations were already folded before this change and must stay caught. This
// is the guard that matters most for meaning: a rewrite that drops a `not`
// inverts the sentence.
func TestNegationsAreStillCaught(t *testing.T) {
	got := checked(t,
		"Nothing depends on the old table except the identifier child table.",
		"Everything depends on the old table except the identifier child table.")

	if got.Preserved {
		t.Fatal("a dropped negation was preserved")
	}
	if !contains(classes(got), "negation:lost") {
		t.Errorf("differences = %v, want a negation:lost", classes(got))
	}
}

// Numbers are unaffected. They carry no case, so folding cannot reach them, but
// a regression here would be silent.
func TestNumbersAreStillCaught(t *testing.T) {
	got := checked(t,
		"The release held for 30 days before the first report.",
		"The release held for 60 days before the first report.")

	if got.Preserved {
		t.Fatal("a changed number was preserved")
	}
	if !contains(classes(got), "number:lost") {
		t.Errorf("differences = %v, want a number:lost", classes(got))
	}
}

// ---------------------------------------------------------------------------
// The audit record
// ---------------------------------------------------------------------------

// Identifiers stay well formed and stay non-reversible. They are what the store
// persists, and its schema pins the shape.
func TestIdentifiersRemainValidAndOpaque(t *testing.T) {
	got := checked(t,
		"The library stores every record in Postgres and verifies them on read.",
		"The library stores every record and verifies them on read.")

	identifiers := got.Identifiers()
	if len(identifiers) == 0 {
		t.Fatal("a refused result produced no identifiers")
	}
	for _, id := range identifiers {
		if !preserve.ValidIdentifier(id) {
			t.Errorf("identifier %q is not well formed", id)
		}
		if strings.Contains(strings.ToLower(id), "postgres") {
			t.Errorf("identifier %q carries the item it describes; these must not be reversible", id)
		}
	}
}

// The same difference always digests to the same identifier, whatever case the
// text carried. Two texts that differ only in the case of the lost name are the
// same loss, and a store that recorded them as two different identifiers would
// be reporting a distinction the guard no longer makes.
func TestTheIdentifierDoesNotDependOnTheCaseOfTheItem(t *testing.T) {
	upper := checked(t,
		"The records live in Postgres and are verified.",
		"The records live and are verified.")
	lower := checked(t,
		"The records live in POSTGRES and are verified.",
		"The records live and are verified.")

	a, b := upper.Identifiers(), lower.Identifiers()
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("one of the fixtures preserved; both are meant to lose the name")
	}
	if !sameStrings(a, b) {
		t.Errorf("identifiers %v and %v differ for the same loss in different case", a, b)
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func sameStrings(a, b []string) bool {
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
