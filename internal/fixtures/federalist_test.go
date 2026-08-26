// Package fixtures_test defines the contract for hapax's vendored public-domain
// corpus.
//
// Nothing here depends on the maintainer's writing, on credentials, or on the
// network: a fresh clone must be able to run every one of these offline. The
// Federalist Papers are used because they are public domain, carry per-document
// author labels, and include a documented disputed set — which later gives the
// Burrows' Delta implementation an attribution problem with a known answer.
//
// Facts asserted below were extracted from the source text and verified before
// being written down, not recalled. They are properties of ONE edition, pinned
// by hash; a different edition attributes papers differently.
package fixtures_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/fixtures"
	"github.com/fissible/hapax/internal/text"
)

// sourceSHA256 and corpusDigest are provenance and regression controls, not a
// security boundary: anyone able to edit the corpus can edit these too. What
// they buy is that an accidental change, a re-download of a different edition,
// or a silently different parse cannot pass unnoticed.
//
// They live in the test rather than only in the manifest because a manifest
// recording a hash of its own current contents establishes nothing.
const sourceSHA256 = "a6c9d1135a04d10955fe11d210b7f642e1c2341d4f2c8369b9a832cc97839d94"

// corpusDigest covers Number, Roman, Attribution, Title and Text for every
// record in order, each field length-framed so that content cannot migrate
// between fields without changing the digest.
const corpusDigest = "54daa3856ae807eaba31d29dd1324b7372704372b29e7969e9989c19617df0b3"

// records pins the identity of every paper: its position, number, numeral,
// attribution, and a prefix of the SHA-256 of its body text. Totals alone would
// let the Hamilton and Madison labels be swapped wholesale.
var records = []struct {
	Index       int
	Number      int
	Roman       string
	Attribution fixtures.Attribution
	BodySHA     string
}{
	{0, 1, "I", fixtures.Hamilton, "95e6db7e60f27ae3"},
	{1, 2, "II", fixtures.Jay, "dd006af3bdd2e6fc"},
	{2, 3, "III", fixtures.Jay, "f4095af2b835db58"},
	{3, 4, "IV", fixtures.Jay, "6e39220a2b68938c"},
	{4, 5, "V", fixtures.Jay, "d263d40718b3ac59"},
	{5, 6, "VI", fixtures.Hamilton, "ca0b23012b2022ea"},
	{6, 7, "VII", fixtures.Hamilton, "726da4b71e665feb"},
	{7, 8, "VIII", fixtures.Hamilton, "249ffc151997974e"},
	{8, 9, "IX", fixtures.Hamilton, "cd0136c58f544a7b"},
	{9, 10, "X", fixtures.Madison, "7b0a0faf47a083c9"},
	{10, 11, "XI", fixtures.Hamilton, "0d08b9e85d19d46f"},
	{11, 12, "XII", fixtures.Hamilton, "f212d108021fcebf"},
	{12, 13, "XIII", fixtures.Hamilton, "2704e13bb00d6255"},
	{13, 14, "XIV", fixtures.Madison, "d4823529d6295295"},
	{14, 15, "XV", fixtures.Hamilton, "1e0dd6b0d602eb4b"},
	{15, 16, "XVI", fixtures.Hamilton, "3e8600fd04f40d69"},
	{16, 17, "XVII", fixtures.Hamilton, "0ff0990b280da8d2"},
	{17, 18, "XVIII", fixtures.HamiltonAndMadison, "45a7be74fd476bce"},
	{18, 19, "XIX", fixtures.HamiltonAndMadison, "a1c4dd601aebe3d8"},
	{19, 20, "XX", fixtures.HamiltonAndMadison, "8f5410c1dd6296f6"},
	{20, 21, "XXI", fixtures.Hamilton, "ea2076318d67f04e"},
	{21, 22, "XXII", fixtures.Hamilton, "b231fd7f1a9ef18c"},
	{22, 23, "XXIII", fixtures.Hamilton, "1d77feb4c94fc446"},
	{23, 24, "XXIV", fixtures.Hamilton, "87ca603c249e9614"},
	{24, 25, "XXV", fixtures.Hamilton, "a95fb6ef70c2910d"},
	{25, 26, "XXVI", fixtures.Hamilton, "106d3a2212673e20"},
	{26, 27, "XXVII", fixtures.Hamilton, "4b6126c4fefef7b5"},
	{27, 28, "XXVIII", fixtures.Hamilton, "f5a4933807a9878b"},
	{28, 29, "XXIX", fixtures.Hamilton, "fe394b73d99201ab"},
	{29, 30, "XXX", fixtures.Hamilton, "9a42c35b66172613"},
	{30, 31, "XXXI", fixtures.Hamilton, "76b03ff9298cea6f"},
	{31, 32, "XXXII", fixtures.Hamilton, "e9ebef5b12ff6282"},
	{32, 33, "XXXIII", fixtures.Hamilton, "a74969700345b734"},
	{33, 34, "XXXIV", fixtures.Hamilton, "2d907dff7d455312"},
	{34, 35, "XXXV", fixtures.Hamilton, "6db43319d38f6090"},
	{35, 36, "XXXVI", fixtures.Hamilton, "2ec36b061c81e279"},
	{36, 37, "XXXVII", fixtures.Madison, "6104742747d300f8"},
	{37, 38, "XXXVIII", fixtures.Madison, "769e4451cae5f483"},
	{38, 39, "XXXIX", fixtures.Madison, "14add9329c3bbfea"},
	{39, 40, "XL", fixtures.Madison, "d7be7b7d73e9feda"},
	{40, 41, "XLI", fixtures.Madison, "3629cf78390d6ebf"},
	{41, 42, "XLII", fixtures.Madison, "bcf98d6356a614e9"},
	{42, 43, "XLIII", fixtures.Madison, "e09bed5943f8335a"},
	{43, 44, "XLIV", fixtures.Madison, "c5914f50ad70e203"},
	{44, 45, "XLV", fixtures.Madison, "5444e82d4898375d"},
	{45, 46, "XLVI", fixtures.Madison, "d9c4b14f9a2199cb"},
	{46, 47, "XLVII", fixtures.Madison, "461af7b4f1db91e2"},
	{47, 48, "XLVIII", fixtures.Madison, "a3a9deaf934cd41f"},
	{48, 49, "XLIX", fixtures.Disputed, "270ac644a044ffc0"},
	{49, 50, "L", fixtures.Disputed, "e8607126689377cc"},
	{50, 51, "LI", fixtures.Disputed, "32a962c25e82536c"},
	{51, 52, "LII", fixtures.Disputed, "4dbd464f6c9cf861"},
	{52, 53, "LIII", fixtures.Disputed, "68f4499f1fea74fb"},
	{53, 54, "LIV", fixtures.Disputed, "1f0e9e559626266f"},
	{54, 55, "LV", fixtures.Disputed, "e8f79455c4da336e"},
	{55, 56, "LVI", fixtures.Disputed, "607563c1ca00d076"},
	{56, 57, "LVII", fixtures.Disputed, "4ee3ce46613234c3"},
	{57, 58, "LVIII", fixtures.Madison, "789f975137cfa41c"},
	{58, 59, "LIX", fixtures.Hamilton, "a0632754de2cf303"},
	{59, 60, "LX", fixtures.Hamilton, "03c071433b24281e"},
	{60, 61, "LXI", fixtures.Hamilton, "ccf3c95d1f6ae34d"},
	{61, 62, "LXII", fixtures.Disputed, "114f692cca45a3f0"},
	{62, 63, "LXIII", fixtures.Disputed, "2e33a33804f4bc63"},
	{63, 64, "LXIV", fixtures.Jay, "9c935629b8adcd18"},
	{64, 65, "LXV", fixtures.Hamilton, "0c6869a889dca418"},
	{65, 66, "LXVI", fixtures.Hamilton, "ee50db0348b22a2f"},
	{66, 67, "LXVII", fixtures.Hamilton, "ba1cae8bf392c1fd"},
	{67, 68, "LXVIII", fixtures.Hamilton, "52fb4388f05c4fe5"},
	{68, 69, "LXIX", fixtures.Hamilton, "a869d24eb9b61b5d"},
	{69, 70, "LXX", fixtures.Hamilton, "ba36d40856d39a2e"},
	{70, 70, "LXX", fixtures.Hamilton, "2354e771d18fe59f"},
	{71, 71, "LXXI", fixtures.Hamilton, "49fa70f81355d8e4"},
	{72, 72, "LXXII", fixtures.Hamilton, "002567ba011e60fd"},
	{73, 73, "LXXIII", fixtures.Hamilton, "27843e0c164c7601"},
	{74, 74, "LXXIV", fixtures.Hamilton, "06f5ba072a262d4e"},
	{75, 75, "LXXV", fixtures.Hamilton, "7ec428a2e1f169ae"},
	{76, 76, "LXXVI", fixtures.Hamilton, "c786b43446f8646a"},
	{77, 77, "LXXVII", fixtures.Hamilton, "59e6b37a9c469ce6"},
	{78, 78, "LXXVIII", fixtures.Hamilton, "326d52051cfde6af"},
	{79, 79, "LXXIX", fixtures.Hamilton, "59bb44380458a61c"},
	{80, 80, "LXXX", fixtures.Hamilton, "ae3c7e754b79fe7b"},
	{81, 81, "LXXXI", fixtures.Hamilton, "9ec0d3c47ebf5c32"},
	{82, 82, "LXXXII", fixtures.Hamilton, "db189ac852a727d9"},
	{83, 83, "LXXXIII", fixtures.Hamilton, "7a66a3ae7a92758b"},
	{84, 84, "LXXXIV", fixtures.Hamilton, "56423dc19d3de31b"},
	{85, 85, "LXXXV", fixtures.Hamilton, "a8d794dcab6f4aff"},
}

// This edition's attribution counts. They sum to 86 across 85 distinct numbers
// because No. 70 appears twice.
const (
	wantTotal    = 86
	wantDistinct = 85
	wantHamilton = 52
	wantMadison  = 15
	wantJay      = 5
	wantJoint    = 3
	wantDisputed = 11
)

// The disputed set here is 49–57 plus 62 and 63. Note this is ELEVEN papers,
// not the twelve of the commonly cited 49–58 range: this edition attributes
// No. 58 to Madison outright. Pinning the edition is what makes the difference
// checkable instead of a source of silent disagreement.
var wantDisputedNumbers = []int{49, 50, 51, 52, 53, 54, 55, 56, 57, 62, 63}

var (
	wantJointNumbers = []int{18, 19, 20}
	wantJayNumbers   = []int{2, 3, 4, 5, 64}
)

func load(t *testing.T) []fixtures.Paper {
	t.Helper()
	papers, err := fixtures.Federalist()
	if err != nil {
		t.Fatalf("Federalist() returned error: %v", err)
	}
	return papers
}

func TestSourceProvenanceIsRecorded(t *testing.T) {
	src := fixtures.FederalistSource()

	if src.SHA256 != sourceSHA256 {
		t.Errorf("manifest records source SHA256 %q, want %q", src.SHA256, sourceSHA256)
	}
	for name, value := range map[string]string{
		"URL":       src.URL,
		"Edition":   src.Edition,
		"Retrieved": src.Retrieved,
		"License":   src.License,
	} {
		if strings.TrimSpace(value) == "" {
			t.Errorf("manifest field %s is empty; a fixture that cannot name its origin is not provenance", name)
		}
	}
	if !strings.Contains(src.URL, "gutenberg.org") {
		t.Errorf("manifest URL %q does not name the source used", src.URL)
	}
}

func TestPaperCountsMatchThisEdition(t *testing.T) {
	papers := load(t)

	if got := len(papers); got != wantTotal {
		t.Fatalf("loaded %d papers, want %d", got, wantTotal)
	}

	seen := map[int]int{}
	for _, p := range papers {
		seen[p.Number]++
	}
	if got := len(seen); got != wantDistinct {
		t.Errorf("%d distinct paper numbers, want %d", got, wantDistinct)
	}
	for n := 1; n <= wantDistinct; n++ {
		if seen[n] == 0 {
			t.Errorf("paper %d is missing", n)
		}
	}
}

// No. 70 appears twice in this edition, in two different versions. Silently
// dropping one would discard authored text; silently keeping both unlabelled
// would seed a duplicate into any corpus built from this fixture.
func TestDuplicatePaperIsPresentAndDistinct(t *testing.T) {
	papers := load(t)

	var seventy []fixtures.Paper
	for _, p := range papers {
		if p.Number == 70 {
			seventy = append(seventy, p)
		}
	}
	if len(seventy) != 2 {
		t.Fatalf("paper 70 appears %d times, want 2", len(seventy))
	}
	if seventy[0].Text == seventy[1].Text {
		t.Error("both copies of paper 70 have identical text; they are distinct versions in this edition")
	}
	for _, p := range seventy {
		if !p.Duplicate {
			t.Error("paper 70 is not flagged as a duplicate; consumers must be able to exclude it")
		}
	}
}

func TestAttributionDistribution(t *testing.T) {
	papers := load(t)

	counts := map[fixtures.Attribution]int{}
	for _, p := range papers {
		counts[p.Attribution]++
	}
	for attribution, want := range map[fixtures.Attribution]int{
		fixtures.Hamilton:           wantHamilton,
		fixtures.Madison:            wantMadison,
		fixtures.Jay:                wantJay,
		fixtures.HamiltonAndMadison: wantJoint,
		fixtures.Disputed:           wantDisputed,
	} {
		if got := counts[attribution]; got != want {
			t.Errorf("attribution %q: %d papers, want %d", attribution, got, want)
		}
	}
}

func TestAttributionSetsAreExact(t *testing.T) {
	papers := load(t)

	numbersFor := func(a fixtures.Attribution) []int {
		var out []int
		for _, p := range papers {
			if p.Attribution == a {
				out = append(out, p.Number)
			}
		}
		sort.Ints(out)
		return out
	}

	for _, c := range []struct {
		attribution fixtures.Attribution
		want        []int
	}{
		{fixtures.Disputed, wantDisputedNumbers},
		{fixtures.HamiltonAndMadison, wantJointNumbers},
		{fixtures.Jay, wantJayNumbers},
	} {
		got := numbersFor(c.attribution)
		if !equalInts(got, c.want) {
			t.Errorf("attribution %q covers %v, want %v", c.attribution, got, c.want)
		}
	}
}

// The disputed papers are the reason this corpus was chosen. Losing them to a
// parsing change would remove the only attribution problem with a known answer.
func TestDisputedPapersAreNotAttributedToAnIndividual(t *testing.T) {
	papers := load(t)
	disputed := map[int]bool{}
	for _, n := range wantDisputedNumbers {
		disputed[n] = true
	}
	for _, p := range papers {
		if !disputed[p.Number] {
			continue
		}
		if p.Attribution != fixtures.Disputed {
			t.Errorf("paper %d is attributed %q; this edition marks it disputed", p.Number, p.Attribution)
		}
	}
}

func TestEveryPaperHasCompleteMetadata(t *testing.T) {
	papers := load(t)
	for _, p := range papers {
		if p.Number < 1 || p.Number > wantDistinct {
			t.Errorf("paper number %d out of range", p.Number)
		}
		if strings.TrimSpace(p.Roman) == "" {
			t.Errorf("paper %d has no roman numeral", p.Number)
		}
		if got := fixtures.RomanToInt(p.Roman); got != p.Number {
			t.Errorf("paper %d has roman %q which is %d", p.Number, p.Roman, got)
		}
		if strings.TrimSpace(p.Title) == "" {
			t.Errorf("paper %d has no title", p.Number)
		}
		if p.Attribution == "" {
			t.Errorf("paper %d has no attribution", p.Number)
		}
	}
}

// Boilerplate is not prose. Leaving it in would put the same licence text into
// every document, which is a strong spurious signal for any stylometry built
// on this corpus.
func TestNoGutenbergBoilerplateInPaperText(t *testing.T) {
	papers := load(t)
	forbidden := []string{
		"Project Gutenberg",
		"gutenberg.org",
		"START OF THE PROJECT",
		"END OF THE PROJECT",
		"eBook is for the use of anyone",
	}
	for _, p := range papers {
		for _, bad := range forbidden {
			if strings.Contains(p.Text, bad) {
				t.Errorf("paper %d contains boilerplate %q", p.Number, bad)
			}
		}
	}
}

// Headers and the attribution line are metadata, already exposed as fields.
// Leaving them in the text would double-count them as prose.
func TestPaperTextExcludesHeadersAndAttribution(t *testing.T) {
	papers := load(t)
	for _, p := range papers {
		trimmed := strings.TrimSpace(p.Text)
		if strings.HasPrefix(trimmed, "No.") || strings.HasPrefix(trimmed, "THE FEDERALIST") {
			t.Errorf("paper %d text begins with a header: %q", p.Number, first(trimmed, 40))
		}
		for _, line := range strings.Split(p.Text, "\n") {
			switch strings.TrimSpace(line) {
			case "HAMILTON", "MADISON", "JAY", "HAMILTON AND MADISON", "HAMILTON OR MADISON":
				t.Errorf("paper %d text contains the attribution line %q", p.Number, strings.TrimSpace(line))
			}
		}
	}
}

func TestPaperTextIsSubstantial(t *testing.T) {
	papers := load(t)
	var total int
	for _, p := range papers {
		if n := len(p.Text); n < 1000 {
			t.Errorf("paper %d has only %d bytes of text; a parse failure looks like this", p.Number, n)
		} else {
			total += n
		}
	}
	// The public-domain body of this edition is ~1.17MB before headers are
	// stripped, so the prose alone should land comfortably inside this range.
	if total < 900_000 || total > 1_200_000 {
		t.Errorf("total prose is %d bytes, outside the expected 900k–1.2M for this edition", total)
	}
}

// The corpus must survive the admission contract it will actually be fed
// through, or it cannot serve as an end-to-end fixture.
func TestEveryPaperIsAdmissible(t *testing.T) {
	for _, p := range load(t) {
		doc, err := text.Admit([]byte(p.Text))
		if err != nil {
			t.Errorf("paper %d is not admissible: %v", p.Number, err)
			continue
		}
		if doc.HadBOM() {
			t.Errorf("paper %d carries a BOM", p.Number)
		}
	}
}

// Embedded, not read from disk: loading must not depend on the working
// directory, or the fixture breaks for any consumer that is not run from the
// repository root.
func TestLoadingIsIndependentOfWorkingDirectory(t *testing.T) {
	before := load(t)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(os.TempDir()); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	after := load(t)
	if len(before) != len(after) {
		t.Fatalf("loaded %d papers from repo root, %d from a temp dir", len(before), len(after))
	}
	if hashPapers(before) != hashPapers(after) {
		t.Error("corpus content depends on the working directory; it must be embedded")
	}
}

func TestLoadingIsDeterministic(t *testing.T) {
	if hashPapers(load(t)) != hashPapers(load(t)) {
		t.Error("two loads produced different corpora")
	}
}

// A corpus that loads consistently but holds different text than intended would
// satisfy every structural assertion above. This pins the actual content.
func TestCorpusMatchesPinnedDigest(t *testing.T) {
	if got := hashPapers(load(t)); got != corpusDigest {
		t.Errorf("corpus digest is\n  %s\nwant\n  %s\n\nThe vendored text, its parse, or the field boundaries changed.", got, corpusDigest)
	}
}

// Per-record identity. Totals and subset membership cannot catch a wholesale
// swap of the Hamilton and Madison labels; this can.
func TestEveryRecordMatchesItsPinnedIdentity(t *testing.T) {
	papers := load(t)
	if len(papers) != len(records) {
		t.Fatalf("loaded %d papers, pinned table has %d", len(papers), len(records))
	}
	for i, want := range records {
		got := papers[i]
		if got.Index != want.Index {
			t.Errorf("record %d has Index %d, want %d", i, got.Index, want.Index)
		}
		if got.Number != want.Number {
			t.Errorf("record %d is paper %d, want %d", i, got.Number, want.Number)
		}
		if got.Roman != want.Roman {
			t.Errorf("paper %d has roman %q, want %q", want.Number, got.Roman, want.Roman)
		}
		if got.Attribution != want.Attribution {
			t.Errorf("paper %d (record %d) is attributed %q, want %q", want.Number, i, got.Attribution, want.Attribution)
		}
		if sum := bodySHA(got.Text); sum != want.BodySHA {
			t.Errorf("paper %d (record %d) body hashes to %s, want %s", want.Number, i, sum, want.BodySHA)
		}
	}
}

// Records are ordered as they appear in the source, so downstream selection is
// reproducible.
func TestRecordsAreInSourceOrder(t *testing.T) {
	for i, p := range load(t) {
		if p.Index != i {
			t.Errorf("record at position %d reports Index %d", i, p.Index)
		}
	}
}

func bodySHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// hashPapers must mirror the framing used to produce corpusDigest exactly:
// every field length-framed, Number included, records in order.
func hashPapers(papers []fixtures.Paper) string {
	h := sha256.New()
	for _, p := range papers {
		for _, f := range []string{
			strconv.Itoa(p.Number),
			p.Roman,
			string(p.Attribution),
			p.Title,
			p.Text,
		} {
			h.Write([]byte(strconv.Itoa(len(f))))
			h.Write([]byte(":"))
			h.Write([]byte(f))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func equalInts(a, b []int) bool {
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

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Duplicate must be true for exactly the two No. 70 records and false elsewhere,
// so later profile and eval work can exclude them deliberately rather than
// discovering the repetition as a spurious signal.
func TestDuplicateFlagIsExact(t *testing.T) {
	for _, p := range load(t) {
		want := p.Number == 70
		if p.Duplicate != want {
			t.Errorf("paper %d (record %d) has Duplicate=%v, want %v", p.Number, p.Index, p.Duplicate, want)
		}
	}
}

// The header in this edition takes four shapes: title alone, wrapped title,
// title plus a parenthetical subtitle, and — for one record — an editorial note
// about the duplicate. Titles must absorb the subtitle and the wrap, and must
// exclude the publication line, the date, and the note.
func TestTitleExtractionHandlesEveryHeaderShape(t *testing.T) {
	papers := load(t)

	titlesFor := func(number int) []string {
		var out []string
		for _, p := range papers {
			if p.Number == number {
				out = append(out, p.Title)
			}
		}
		return out
	}

	for _, c := range []struct {
		name   string
		number int
		want   []string
	}{
		{"plain title", 1, []string{"General Introduction"}},
		{"title with parenthetical subtitle", 10, []string{"The Same Subject Continued (The Union as a Safeguard Against Domestic Faction and Insurrection)"}},
		{"title wrapped across source lines", 14, []string{"Objections to the Proposed Constitution From Extent of Territory Answered"}},
		// The fourth shape: an editorial note precedes the title on the first of
		// the two No. 70 records, and must not become part of it.
		{"publication line without \"the\"", 85, []string{"Concluding Remarks"}},
		{"weekday date with no comma", 36, []string{"The Same Subject Continued (Concerning the General Power of Taxation)"}},
		{"header carrying an editorial note", 70, []string{
			"The Executive Department Further Considered",
			"The Executive Department Further Considered",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := titlesFor(c.number)
			if len(got) != len(c.want) {
				t.Fatalf("paper %d has %d records, want %d", c.number, len(got), len(c.want))
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("paper %d record %d title is\n  %q\nwant\n  %q", c.number, i, got[i], c.want[i])
				}
			}
		})
	}
}

// Publication lines, dates and the editorial note are metadata. Left in the body
// they would appear in every document as a strong spurious stylometric signal.
func TestBodyExcludesPublicationMetadata(t *testing.T) {
	for _, p := range load(t) {
		head := p.Text
		if len(head) > 300 {
			head = head[:300]
		}
		for _, marker := range []string{"For the Independent Journal", "From the New York Packet", "From the Daily Advertiser"} {
			if strings.Contains(head, marker) {
				t.Errorf("paper %d (record %d) body opens with the publication line %q", p.Number, p.Index, marker)
			}
		}
		// The parenthesized form is the header note and must be excluded. The
		// asterisked form is a genuine footnote inside the first No. 70's prose,
		// so forbidding the phrase outright would forbid real authored text.
		if strings.Contains(p.Text, "(There are two slightly different versions") {
			t.Errorf("paper %d (record %d) body contains the parenthesized header note", p.Number, p.Index)
		}
	}
}
