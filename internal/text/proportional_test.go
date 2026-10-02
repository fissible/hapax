package text_test

// #136. The growth arm is CONJUNCTIVE: a script is named for growth only when
// its count AND its share both rise.
//
// # Contract
//
// Stated once, on `ScriptSet.Exceeding`. Nothing here restates it.
//
// # Evidence: the count arm refused rewrites that reduced a script's prominence
//
// The arm it replaces named a script whenever its absolute count rose, whatever
// happened to its share. Measured exhaustively over two-script paragraphs — Latin
// plus one other, original 10..100 Latin x 1..40 Greek, candidate 5..200 x 1..60,
// 42,806,400 pairs — comparing the COMPLETE old and new predicates at the declared
// 0.25 and 0.05, not their growth arms in isolation: 2,211,794 pairs are named by
// the old predicate and not the new one when Greek alone is scored, 2,213,415 when
// either script may be named, the extra cases being originals where LATIN is the
// unestablished script. In every one of the 2,211,794 GREEK differences Greek's
// share factor is at most 1.0000 — the share never rose, and fell as low as
// 0.2065884980. That does not extend to the extra 1,621 cases, which are Latin
// differences and say nothing about Greek: at (10 Latin, 31 Greek) -> (11, 35),
// Latin's refusal drops while Greek's share factor is 1.0063113604.
//
// A witness from real prose, already in this package's fixture texts:
// "αβγδ ab 漢" to "The author, автор 著者, never draws it." takes Han from 1 letter
// of 7 to 2 of 28 — the count doubles and the share falls from 0.1429 to 0.0714.
// The old arm named Han there. Cyrillic is named either way, by introduction.
//
// # Consequence: the change only ever PERMITS more
//
// The new growth arm implies the old one — `(c' > c AND p' > p)` implies
// `c' > c`, and the other two lines are untouched — so every candidate named
// now was named before. No rewrite that is accepted today becomes refused.
// Measured as well as argued: over the 42,806,400 pairs above, the number named
// by the new arm and not the old is ZERO.
//
// # Decision
//
// Proportional growth is EXEMPT, which is the contract #136 asked for, and so is
// dilution, which is the same family: both have a share factor at or below one.
// A share arm alone was refuted — it refuses shortening the prose around an
// unchanged quotation, which `Exceeding` documents as tolerated.
//
// # Unresolved
//
// Absolute growth at constant share is permitted BY THIS GUARD without bound: 90
// Latin + 10 Greek to 900 + 100 keeps Greek at 0.1000 and is not named, where the
// old arm refused it. No dedicated expansion refusal exists to decline it on those
// grounds: `rewrite.RejectionCodes()` has none, and the provider's response and
// token limits are transport bounds rather than prose ones. #143 carries that
// decision.
//
// These tests exercise `Exceeding` alone and establish nothing about what the
// rewrite loop would accept. TestTenfoldGrowthAtAConstantShareIsPermitted records
// only that `Exceeding` does not name that candidate; adding an expansion
// safeguard elsewhere would leave it valid.
//
// The exposure is WIDER than before rather than new: the guard already permitted
// arbitrary script growth below the ceiling, and exempts established scripts
// entirely.
//
// The rejection rate on legitimate rewrites and the escape rate on incidents stay
// unmeasured: the corpus carries two non-Latin paragraphs of 1959, both below the
// ceiling, so the affected band cannot be sampled. #136 stays open for them.

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fissible/hapax/internal/text"
)

const (
	proportionalEstablished = 0.25
	proportionalCeiling     = 0.05
)

// counted builds a set whose per-script counts are exactly the numbers named,
// the way TestNeitherGuardContainsTheOtherWithProductionAnchors does.
func counted(latin, greek, han int) text.ScriptSet {
	return text.Scripts(strings.Repeat("a", latin) + " " +
		strings.Repeat("α", greek) + " " + strings.Repeat("著", han))
}

func exceeding(t *testing.T, original, candidate text.ScriptSet) []string {
	t.Helper()
	return candidate.Exceeding(original, proportionalEstablished, proportionalCeiling)
}

// requireInBand is a FIXTURE REQUIREMENT, not a claim about the predicate: it
// pins a script's share strictly between the ceiling and establishment on the
// side named.
//
// It exists because every self-check in this file was once satisfiable by a
// fixture that proved nothing. An original share at or above establishment is
// exempted by the predicate's first line, so the growth arm never runs and the
// case passes under the old arm and the new one alike — which is how a changed
// fixture goes quiet.
//
// What it establishes is only that the fixture is the one the test describes. It
// does NOT establish which line of the predicate decided the case: the ceiling
// line reads the candidate alone, so a share outside the band in the ORIGINAL
// says nothing about whether the ceiling screened the candidate off. At
// (96 Latin, 4 Greek) -> (90, 10) the original share is 0.04, under the ceiling,
// and Greek is named anyway. The messages name the side and the requirement, and
// claim nothing more.
func requireInBand(t *testing.T, s text.ScriptSet, where, name string) {
	t.Helper()
	share := s.Share(name)
	if share <= proportionalCeiling {
		t.Fatalf("%s is at %.4f in the %s, at or below the ceiling %.2f, outside "+
			"the band this fixture requires on that side", name, share, where,
			proportionalCeiling)
	}
	if share >= proportionalEstablished {
		t.Fatalf("%s is at %.4f in the %s, at or above establishment %.2f, outside "+
			"the band this fixture requires on that side", name, share, where,
			proportionalEstablished)
	}
}

// Scaling a paragraph without changing its script mix names nothing. This is
// #136's contract question, and the answer is that proportional growth is exempt.
func TestProportionalGrowthNamesNothing(t *testing.T) {
	for _, c := range []struct {
		name              string
		latin, greek, han int
		factor            int
	}{
		{"doubled", 60, 20, 20, 2},
		{"tripled", 60, 20, 20, 3},
		{"doubled with the band occupied on one script only", 80, 12, 0, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			original := counted(c.latin, c.greek, c.han)
			candidate := counted(c.latin*c.factor, c.greek*c.factor, c.han*c.factor)

			// The fixture has to EXIST, not merely satisfy a conditional. An
			// earlier version asked "if a script is in the band then did its
			// count grow", which is vacuously true when no script is in the
			// band: with every original script established, all three subtests
			// passed against the OLD count arm, self-checks included.
			reaching := 0
			for _, name := range original.Names() {
				if original.Share(name) != candidate.Share(name) {
					t.Fatalf("%s share %.6f -> %.6f: the fixture is not proportional",
						name, original.Share(name), candidate.Share(name))
				}
				inBand := original.Share(name) > proportionalCeiling &&
					original.Share(name) < proportionalEstablished
				if inBand && candidate.Count(name) > original.Count(name) {
					reaching++
				}
			}
			if reaching == 0 {
				t.Fatal("no script has a share strictly inside the band with a " +
					"grown count, so nothing here reaches the growth arm and the " +
					"case would pass under either predicate")
			}

			if got := exceeding(t, original, candidate); len(got) != 0 {
				t.Errorf("Exceeding() = %v, want nothing named: every share is "+
					"unchanged, so no script grew out of proportion", got)
			}
		})
	}
}

// Two scripts moving in OPPOSITE directions in one call. Greek grows but dilutes,
// 0.20 to 0.15; Han grows and concentrates, 0.20 to 0.225. Both were present in
// the original and both stay inside the band, so the conjunctive growth arm is
// what separates them — and it must name Han alone.
//
// The real-prose witness cannot cover this: its second script is ABSENT from the
// original, so it does not exercise the interaction between two scripts the
// paragraph already carried. (It is named by this same conjunction, not by a
// different line — `Exceeding` has no introduction arm; the separate introduction
// guard is `Introduced`, which the rewrite loop consults on its own.)
func TestDilutionAndConcentrationInTheSameCall(t *testing.T) {
	original, candidate := counted(60, 20, 20), counted(125, 30, 45)

	for _, c := range []struct {
		name       string
		wantRising bool
	}{{"Greek", false}, {"Han", true}} {
		requireInBand(t, original, "original", c.name)
		requireInBand(t, candidate, "candidate", c.name)

		p0, p1 := original.Share(c.name), candidate.Share(c.name)
		if candidate.Count(c.name) <= original.Count(c.name) {
			t.Fatalf("%s count %d -> %d: both scripts must GROW for the "+
				"conjunction to be what separates them",
				c.name, original.Count(c.name), candidate.Count(c.name))
		}
		// Strict in both directions: `rising == false` would admit equality, and
		// an unchanged share is a different case from a falling one.
		if c.wantRising && p1 <= p0 {
			t.Fatalf("%s share %.4f -> %.4f, want a STRICT rise", c.name, p0, p1)
		}
		if !c.wantRising && p1 >= p0 {
			t.Fatalf("%s share %.4f -> %.4f, want a STRICT fall", c.name, p0, p1)
		}
	}

	got := exceeding(t, original, candidate)
	if want := []string{"Han"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Exceeding() = %v, want %v: Greek diluted and Han concentrated",
			got, want)
	}
}

// An established script stays exempt under proportional growth. Its count
// doubles, 25 to 50; what is unchanged is its SHARE, exactly on the threshold.
func TestAnEstablishedScriptIsExemptUnderProportionalGrowth(t *testing.T) {
	original, candidate := counted(75, 25, 0), counted(150, 50, 0)

	if original.Share("Greek") != proportionalEstablished {
		t.Fatalf("Greek sits at %.4f, not exactly on establishment",
			original.Share("Greek"))
	}
	if candidate.Share("Greek") != original.Share("Greek") {
		t.Fatal("the fixture is not proportional")
	}
	// Without this the candidate could equal the original and nothing would grow,
	// which passes whatever the growth arm says.
	if candidate.Count("Greek") != 2*original.Count("Greek") {
		t.Fatalf("Greek count %d -> %d, want it DOUBLED; an ungrown candidate "+
			"cannot test an exemption from a growth arm",
			original.Count("Greek"), candidate.Count("Greek"))
	}

	if got := exceeding(t, original, candidate); len(got) != 0 {
		t.Errorf("Exceeding() = %v, want nothing named: Greek was already "+
			"established, so the first line exempts it whatever its count does", got)
	}
}

// A count that rises while the share falls is a dilution, and dilution names
// nothing. The old arm named these.
func TestDilutionAboveTheCeilingNamesNothing(t *testing.T) {
	for _, c := range []struct {
		name           string
		ol, og, cl, cg int
	}{
		{"share 0.0909 -> 0.0870 on a doubled count", 10, 1, 21, 2},
		{"share 0.0909 -> 0.0800 on a doubled count", 10, 1, 23, 2},
		{"share 0.2000 -> 0.1569 on a doubled count, mid-band", 80, 20, 215, 40},
	} {
		t.Run(c.name, func(t *testing.T) {
			original, candidate := counted(c.ol, c.og, 0), counted(c.cl, c.cg, 0)

			requireInBand(t, original, "original", "Greek")
			requireInBand(t, candidate, "candidate", "Greek")
			if candidate.Count("Greek") <= original.Count("Greek") {
				t.Fatal("the count did not rise, so this is not the case under test")
			}
			if candidate.Share("Greek") >= original.Share("Greek") {
				t.Fatalf("the share did not fall, %.4f -> %.4f: this is not a dilution",
					original.Share("Greek"), candidate.Share("Greek"))
			}

			if got := exceeding(t, original, candidate); len(got) != 0 {
				t.Errorf("Exceeding() = %v, want nothing named: Greek's share fell "+
					"from %.4f to %.4f", got, original.Share("Greek"),
					candidate.Share("Greek"))
			}
		})
	}
}

// The real-prose witness, from this package's own fixture texts. Han's count
// doubles while its share halves exactly, 1 letter of 7 to 2 of 28.
//
// Cyrillic is a genuine introduction and stays named. That is asserted because it
// is part of the expected result, NOT because it makes the case non-vacuous —
// Han's own band membership is what does that.
func TestTheDilutionWitnessFromRealProse(t *testing.T) {
	original := text.Scripts("αβγδ ab 漢")
	candidate := text.Scripts("The author, автор 著者, never draws it.")

	if original.Letters() != 7 || candidate.Letters() != 28 {
		t.Fatalf("letters %d -> %d, want 7 -> 28; the witness texts have moved",
			original.Letters(), candidate.Letters())
	}
	if original.Count("Han") != 1 || candidate.Count("Han") != 2 {
		t.Fatalf("Han counts %d -> %d, want 1 -> 2", original.Count("Han"),
			candidate.Count("Han"))
	}
	// Exactly halved: 1/7 to 2/28. Asserted as the relationship rather than as
	// two decimals, so it cannot drift into "merely fell".
	if candidate.Share("Han")*2 != original.Share("Han") {
		t.Fatalf("Han share %.6f -> %.6f, which is not an exact halving",
			original.Share("Han"), candidate.Share("Han"))
	}
	requireInBand(t, original, "original", "Han")
	requireInBand(t, candidate, "candidate", "Han")
	// Pinned to keep the witness the scenario it describes — an introduction
	// alongside a dilution — rather than because [Cyrillic] depends on it. It does
	// not: at an original of "αβγ ab 漢 а", Cyrillic merely GROWS, 1 letter of 7 to
	// 5 of 28, and the result is still exactly [Cyrillic]. Han's own band
	// membership above is what keeps this case non-vacuous.
	if original.Count("Cyrillic") != 0 || candidate.Count("Cyrillic") != 5 {
		t.Fatalf("Cyrillic counts %d -> %d, want 0 -> 5: it must be an INTRODUCTION",
			original.Count("Cyrillic"), candidate.Count("Cyrillic"))
	}

	got := exceeding(t, original, candidate)
	if want := []string{"Cyrillic"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Exceeding() = %v, want %v: Han diluted and Cyrillic is introduced",
			got, want)
	}
}

// What the conjunctive arm must NOT have changed. Every one of these is an
// outcome `Exceeding` documents, and a share arm alone would have broken the
// first of them.
func TestTheDocumentedOutcomesAreUnchanged(t *testing.T) {
	for _, c := range []struct {
		name           string
		ol, og, cl, cg int
		want           []string
	}{
		// The case that refutes a share arm: shortening the prose around an
		// unchanged quotation. The share rises past the ceiling, 0.0400 to
		// 0.0513, and nothing is named because the count never moved.
		{"crossing the ceiling on an unchanged count", 96, 4, 74, 4, nil},
		// The takeover. Named on an unchanged count, which is why the
		// establishment arm is not redundant under a conjunctive growth arm.
		{"a takeover on an unchanged count", 80, 20, 60, 20, []string{"Greek"}},
		{"exactly on the ceiling", 96, 4, 114, 6, nil},
		{"a hair over the ceiling", 96, 4, 113, 6, []string{"Greek"}},
		{"shortening that keeps the share under establishment", 80, 20, 60, 15, nil},
		{"shortening that pushes the share past establishment", 80, 20, 30, 15,
			[]string{"Greek"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			original, candidate := counted(c.ol, c.og, 0), counted(c.cl, c.cg, 0)

			got := exceeding(t, original, candidate)
			want := c.want
			sort.Strings(want)
			if len(got) == 0 && len(want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Exceeding() = %v, want %v (Greek %.4f -> %.4f, count %d -> %d)",
					got, want, original.Share("Greek"), candidate.Share("Greek"),
					original.Count("Greek"), candidate.Count("Greek"))
			}
		})
	}
}

// The establishment arm carries weight on its own. A takeover on an unchanged
// count DOES satisfy the rising-share half — 0.2000 to 0.2500 — and fails the
// conjunction only because the count did not grow, so deleting the establishment
// arm would change this outcome.
func TestTheEstablishedArmIsNotRedundant(t *testing.T) {
	original, candidate := counted(80, 20, 0), counted(60, 20, 0)

	requireInBand(t, original, "original", "Greek")
	// EQUAL, not merely "not grown": a falling count would also reach the
	// establishment arm, and then this would not be the takeover it claims.
	if candidate.Count("Greek") != original.Count("Greek") {
		t.Fatalf("Greek count %d -> %d, want it UNCHANGED",
			original.Count("Greek"), candidate.Count("Greek"))
	}
	if candidate.Share("Greek") < proportionalEstablished {
		t.Fatalf("Greek lands at %.4f, under establishment, so the second arm "+
			"cannot name it either", candidate.Share("Greek"))
	}

	if got := exceeding(t, original, candidate); !reflect.DeepEqual(got, []string{"Greek"}) {
		t.Errorf("Exceeding() = %v, want [Greek] from the establishment arm alone", got)
	}
}

// The cost of the decision, recorded.
//
// Ten times the Greek at an unchanged share is not named. The old arm named it.
// This calls `Exceeding` and nothing else, so what it establishes is exactly
// that: `Exceeding` does not name this candidate. It says nothing about whether
// the rewrite loop would accept it, and adding an expansion safeguard elsewhere
// would leave this assertion green.
//
// If a later change makes this named again, that is a policy reversal and this
// test is where it surfaces.
func TestTenfoldGrowthAtAConstantShareIsPermitted(t *testing.T) {
	original, candidate := counted(90, 10, 0), counted(900, 100, 0)

	requireInBand(t, original, "original", "Greek")
	requireInBand(t, candidate, "candidate", "Greek")
	if original.Share("Greek") != candidate.Share("Greek") {
		t.Fatalf("Greek %.6f -> %.6f: the fixture is not constant-share",
			original.Share("Greek"), candidate.Share("Greek"))
	}
	if candidate.Count("Greek") != 10*original.Count("Greek") {
		t.Fatalf("Greek count %d -> %d, want tenfold",
			original.Count("Greek"), candidate.Count("Greek"))
	}

	if got := exceeding(t, original, candidate); len(got) != 0 {
		t.Errorf("Exceeding() = %v; this is the recorded cost of #136 and it is "+
			"NOT NAMED. A change here is a policy reversal, not a bug fix", got)
	}
}
