package workflow

import (
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// The gate that actually measures
// ---------------------------------------------------------------------------
//
// #91's policy is only a policy if something feeds it. `text.Scripts` and
// `ScriptSet.Introduced` landed as a separate slice and had NO production
// caller — which is precisely the condition that kept the incident live after
// the measurement existed. `executionGate.Language` is the one place that
// changes, and without these tests the whole acceptance policy can go green
// with `hapax rewrite` behaving exactly as it did on the day of the incident.
//
// These are internal tests because `executionGate` is unexported and the seam
// is the point: the assertion is that this gate reports what the measurement
// says, not that the loop refuses — the loop's half is `internal/rewrite`.

func TestTheExecutionGateReportsTheScriptsACandidateIntroduces(t *testing.T) {
	for _, c := range []struct {
		name               string
		current, candidate string
		want               []string
	}{
		{
			// The incident: English prose returned in Han.
			name:      "the reported incident",
			current:   "The argument turns on a distinction the author never draws.",
			candidate: "論点は著者が引かない区別にかかっている。",
			want:      []string{"Han", "Hiragana"},
		},
		{
			// A Japanese writer's paragraph gaining a script it did not use.
			// Hiragana and Han are already present; Katakana is not.
			name:      "one script added to a paragraph that already mixes two",
			current:   "犬が好きです。",
			candidate: "イヌが好きです。",
			want:      []string{"Katakana"},
		},
		{
			name:      "an ordinary rewrite introduces nothing",
			current:   "The argument turns on a distinction the author never draws.",
			candidate: "The argument rests on a distinction the author never makes.",
			want:      nil,
		},
		{
			// Dropping a script is not introducing one. The policy is about
			// what arrives, not about what leaves.
			name:      "a script removed is not a script introduced",
			current:   "The author, 著者, never draws it.",
			candidate: "The author never draws it.",
			want:      nil,
		},
		{
			// Punctuation, digits and spaces are Common, and Common is not a
			// script a text is written in. A candidate differing only in them
			// introduces nothing.
			name:      "common characters are not a script",
			current:   "The author never draws it",
			candidate: "The author (1979) never draws it — twice!",
			want:      nil,
		},
		{
			// THE LOW-SHARE CASE, and the one this file exists to pin.
			//
			// `internal/rewrite` specifies "any introduction, no threshold" as
			// a refusal rule — but a threshold would never be written there.
			// It would be written HERE, in the measurement, and every other
			// fixture in this table introduces at least 5.9% of the letters,
			// so a `Share(name) >= 0.05` filter passed the entire repository.
			//
			// Measured: 78 letters, one of them Han, 1.28%. Below any
			// threshold anyone would pick, and the policy's own example — "a
			// single foreign name is around 2%" is the argument the slice
			// makes for refusing to have a threshold at all.
			name:      "one foreign character is still an introduction",
			current:   "The quick brown fox jumps over the lazy dog again and again, and the author never draws it at all.",
			candidate: "The quick brown fox jumps over the lazy dog again and again, and the author 著 never draws it at all.",
			want:      []string{"Han"},
		},
		{
			name:      "two scripts at once, named in sorted order",
			current:   "The author never draws it.",
			candidate: "The author, автор 著者, never draws it.",
			want:      []string{"Cyrillic", "Han"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The original and the current coincide on a first attempt, which is
			// what these introduction cases are about; #107's growth anchor is
			// exercised separately.
			got, err := executionGate{}.Language(c.current, c.current, c.candidate)
			if err != nil {
				t.Fatalf("Language: %v", err)
			}
			if len(got.Introduced) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got.Introduced, c.want) {
				t.Errorf("Introduced = %v, want %v", got.Introduced, c.want)
			}
		})
	}
}

// The gate is not a stub.
//
// A `Language` method returning an empty verdict satisfies the interface, keeps
// the whole repository green, and leaves the binary behaving as it did before
// #91 was filed. This asserts the one thing a stub cannot do.
func TestTheExecutionGateIsNotAHardcodedEmptyVerdict(t *testing.T) {
	const only = "A paragraph written in one script only."
	got, err := executionGate{}.Language(only, only, only+" 著者.")
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(got.Introduced) == 0 {
		t.Error("the gate reports no introduction for a candidate that adds Han; " +
			"it is not consulting the measurement")
	}
}

// ---------------------------------------------------------------------------
// #107: growth, and which argument it is measured against
// ---------------------------------------------------------------------------

// The gate reports the scripts whose count grew out of proportion.
func TestTheExecutionGateReportsOvergrownScripts(t *testing.T) {
	for _, c := range []struct {
		name, original, candidate string
		want                      []string
	}{
		{
			// #107 itself: Han was already present, so nothing is introduced.
			name:      "one character becomes the paragraph",
			original:  "The author 著 never draws it.",
			candidate: "作者從不畫它，著者亦然，他從未真正描繪過它，也不曾提起。",
			want:      []string{"Han"},
		},
		{
			// The case the discarded corpus design refused. A longer rewrite in
			// the same script grows nothing, because the script's share of the
			// original is exactly one.
			name:      "lengthening in the same script",
			original:  "The author never draws it.",
			candidate: "The author never draws it, and the reader never thinks to ask him why that is.",
			want:      nil,
		},
		{
			// Keeping one foreign character while the rest shortens. A SHARE
			// rule refuses this for a change it did not make.
			name:      "keeping one character while the rest shortens",
			original:  "The author 著 never draws it at all, he said.",
			candidate: "The author 著 never draws it.",
			want:      nil,
		},
		{
			name:      "removing a script grows nothing",
			original:  "The author 著 never draws it at all, he said.",
			candidate: "The argument turns on a distinction the author never draws.",
			want:      nil,
		},
		{
			// Symmetric: it guards whatever the paragraph was written in, not a
			// privileged script. A Greek-dominant original rewritten mostly into
			// Latin overgrows Latin.
			name:      "the original's own dominant script can overgrow",
			original:  "αβγδ ab 漢",
			candidate: "The author, автор 著者, never draws it.",
			want:      []string{"Cyrillic", "Latin"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// original and current coincide here; the anchor is under test below.
			got, err := executionGate{}.Language(c.original, c.original, c.candidate)
			if err != nil {
				t.Fatalf("Language: %v", err)
			}
			if len(got.Overgrown) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got.Overgrown, c.want) {
				t.Errorf("Overgrown = %v, want %v", got.Overgrown, c.want)
			}
		})
	}
}

// Growth is measured against the FIRST argument, not the second.
//
// This is the one behaviour a three-argument gate exists for, and the fixtures
// are chosen so the two anchors disagree: six Han letters are admissible in a
// long candidate against a 22-letter original, and admissible again in a short
// candidate against THAT long one — the ratchet — but refused against the
// original. A gate that measured growth from `current` reports nothing here.
func TestTheExecutionGateMeasuresGrowthFromTheOriginal(t *testing.T) {
	const origin = "The author 著 never draws it."
	const long = "The author 著著著著著著 never draws it, and the reader never thinks to ask him why that is, nor does he ever say, and the argument turns on a distinction he does not draw at all here."
	const short = "The author 著著著著著著 never draws."

	// The long text is admissible against the original: 6 Han against a bound
	// of 1/22 * 138 = 6.27.
	admissible, err := executionGate{}.Language(origin, origin, long)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(admissible.Overgrown) != 0 {
		t.Fatalf("the long fixture must be admissible against the original or this "+
			"test proves nothing: %v", admissible.Overgrown)
	}

	// And the short one is admissible against the long one, which is the rung a
	// moving anchor would allow.
	ratchet, err := executionGate{}.Language(long, long, short)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(ratchet.Overgrown) != 0 {
		t.Fatalf("against the long text the short one must be admissible, or the "+
			"ladder this guards against does not exist: %v", ratchet.Overgrown)
	}

	// The real call: anchored on the original, with current at the long text.
	got, err := executionGate{}.Language(origin, long, short)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if !reflect.DeepEqual(got.Overgrown, []string{"Han"}) {
		t.Errorf("Overgrown = %v, want [Han] — growth is measured from the first "+
			"argument, and measuring from the second reports nothing", got.Overgrown)
	}
}

// Introduction is still measured against the CURRENT text, not the original.
//
// The two facts use different anchors on purpose, so one call cannot be
// collapsed into a single comparison. A gate measuring both from the original
// would report an introduction for a script the current text already carries.
func TestTheExecutionGateMeasuresIntroductionFromTheCurrentText(t *testing.T) {
	const origin = "The argument turns on a distinction."
	const current = "The argument turns 著 on a distinction."
	const candidate = "The argument turns 著 on a finer distinction."

	got, err := executionGate{}.Language(origin, current, candidate)
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	// Han is in the current text, so it is not introduced...
	if len(got.Introduced) != 0 {
		t.Errorf("Introduced = %v, want none — Han is already in the current text",
			got.Introduced)
	}
	// ...but it is absent from the ORIGINAL, so it is overgrowth.
	if !reflect.DeepEqual(got.Overgrown, []string{"Han"}) {
		t.Errorf("Overgrown = %v, want [Han] — Han is absent from the original", got.Overgrown)
	}
}
