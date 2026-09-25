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
			name:      "one script added to a paragraph that already mixes three",
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
			name:      "two scripts at once, named in sorted order",
			current:   "The author never draws it.",
			candidate: "The author, автор 著者, never draws it.",
			want:      []string{"Cyrillic", "Han"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := executionGate{}.Language(c.current, c.candidate)
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
	got, err := executionGate{}.Language(
		"A paragraph written in one script only.",
		"A paragraph written in one script only, 著者.")
	if err != nil {
		t.Fatalf("Language: %v", err)
	}
	if len(got.Introduced) == 0 {
		t.Error("the gate reports no introduction for a candidate that adds Han; " +
			"it is not consulting the measurement")
	}
}
