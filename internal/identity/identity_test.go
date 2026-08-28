package identity_test

// Canonical framing and hashing for artifact IDs.
//
// This package was extracted during the eval slice, from three byte-identical
// copies in `corpus`, `profile` and `eval`. None of the three was ever tested,
// and the property that justifies the design — length prefixing, so that
// concatenated fields cannot be repartitioned into the same byte string — was
// tested nowhere. It now decides the identity of every artifact the tool
// produces, so it is tested here.

import (
	"regexp"
	"testing"

	"github.com/fissible/hapax/internal/identity"
)

// The reason Frame exists. Without length prefixes, ("a","bc") and ("ab","c")
// both concatenate to "abc" and two different artifacts collide on one ID.
func TestFramingIsUnambiguous(t *testing.T) {
	collisions := [][2][]string{
		{{"a", "bc"}, {"ab", "c"}},
		{{"", "abc"}, {"abc", ""}},
		{{"a:b", "c"}, {"a", "b:c"}},
		{{"1:x"}, {"1", "x"}},
		{{"key", "value"}, {"keyvalue", ""}},
		// No parts at all versus one empty part. Both concatenate to nothing,
		// so only the framing distinguishes them.
		{{}, {""}},
		{{"", ""}, {""}},
	}
	for i, pair := range collisions {
		left, right := identity.Frame(pair[0]...), identity.Frame(pair[1]...)
		if string(left) == string(right) {
			t.Errorf("case %d: %q and %q frame identically as %q", i, pair[0], pair[1], left)
		}
		if identity.HashBytes(left) == identity.HashBytes(right) {
			t.Errorf("case %d: %q and %q hash identically", i, pair[0], pair[1])
		}
	}
}

// The exact framing, pinned. Injectivity alone would be satisfied by a
// different-but-compatible scheme, and this format is written into every
// artifact ID the project has already produced — changing it silently would
// invalidate every cached result without any test noticing.
//
// The expected digest was computed independently of this package, by hashing
// the documented byte string with a separate tool, so it is not merely a record
// of what the code currently does.
func TestFramingFormatIsPinned(t *testing.T) {
	if got := string(identity.Frame("a", "bc")); got != "1:a2:bc" {
		t.Errorf("Frame(\"a\", \"bc\") = %q, want %q — length, colon, part", got, "1:a2:bc")
	}
	if got := string(identity.Frame()); got != "" {
		t.Errorf("Frame() = %q, want empty", got)
	}
	if got := string(identity.Frame("")); got != "0:" {
		t.Errorf("Frame(\"\") = %q, want %q", got, "0:")
	}
	const framedAbc = "5310a58788781ab25d5ad7c3f85035824b4eb7bdfa394e0ac2186271472b5492"
	if got := identity.HashBytes(identity.Frame("a", "bc")); got != framedAbc {
		t.Errorf("HashBytes(Frame(\"a\", \"bc\")) = %q, want %q", got, framedAbc)
	}
}

// A fixed vector for HashInputs, so order independence is proved by a known
// answer rather than only by repetition. {"b":"2","a":"1"} sorts to keys a,b
// and frames as "1:a1:11:b1:2"; the digest below is that string hashed by a
// separate tool.
func TestHashInputsMatchesAPinnedVector(t *testing.T) {
	const want = "4016e0316f40793b933598c4fcbcd0b472413e3ffe9f725829aef85184e9b679"
	if got := identity.HashInputs(map[string]string{"b": "2", "a": "1"}); got != want {
		t.Errorf("HashInputs = %q, want %q — keys sorted, each key and value framed separately", got, want)
	}
}

// Map iteration order is random in Go, so an identity built from a map must
// sort before framing or the same inputs would produce different IDs run to
// run — and every cache in the project would miss.
func TestHashInputsIsOrderIndependent(t *testing.T) {
	inputs := map[string]string{
		"alpha": "one", "beta": "two", "gamma": "three",
		"delta": "four", "epsilon": "five", "zeta": "six",
	}
	first := identity.HashInputs(inputs)
	for i := 0; i < 64; i++ {
		if got := identity.HashInputs(inputs); got != first {
			t.Fatalf("iteration %d produced %q, want %q — the inputs must be sorted before framing", i, got, first)
		}
	}

	rebuilt := map[string]string{}
	for k, v := range inputs {
		rebuilt[k] = v
	}
	if got := identity.HashInputs(rebuilt); got != first {
		t.Errorf("an equal map hashed to %q, want %q", got, first)
	}
}

// Keys and values are framed as separate parts, so a value cannot impersonate
// the next key.
func TestHashInputsDistinguishesKeysFromValues(t *testing.T) {
	cases := [][2]map[string]string{
		{{"a": "b"}, {"ab": ""}},
		{{"a": "b", "c": "d"}, {"a": "bcd"}},
		{{"split": "train"}, {"split": "traint"}},
		{{"x": ""}, {}},
	}
	for i, pair := range cases {
		if identity.HashInputs(pair[0]) == identity.HashInputs(pair[1]) {
			t.Errorf("case %d: %v and %v share an ID", i, pair[0], pair[1])
		}
	}
}

// A changed value must change the ID. Stated because the whole cache-identity
// discipline in DESIGN Section 2 rests on it.
func TestEveryInputAffectsTheHash(t *testing.T) {
	base := map[string]string{"one": "1", "two": "2", "three": "3"}
	baseline := identity.HashInputs(base)

	for key := range base {
		changed := map[string]string{}
		for k, v := range base {
			changed[k] = v
		}
		changed[key] = changed[key] + "x"
		if identity.HashInputs(changed) == baseline {
			t.Errorf("changing %q did not change the identity", key)
		}
	}

	added := map[string]string{"one": "1", "two": "2", "three": "3", "four": "4"}
	if identity.HashInputs(added) == baseline {
		t.Error("adding an input did not change the identity")
	}
}

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// A pinned vector, so the digest cannot change encoding — to uppercase, to
// base64, or to a different hash — without a deliberate decision. Every
// persisted artifact ID in the project is one of these strings.
func TestHashBytesIsLowercaseHexSHA256(t *testing.T) {
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := identity.HashBytes(nil); got != emptySHA256 {
		t.Errorf("HashBytes(nil) = %q, want the SHA-256 of no bytes %q", got, emptySHA256)
	}
	if got := identity.HashBytes([]byte("hapax")); !hexDigest.MatchString(got) {
		t.Errorf("HashBytes = %q, want 64 lowercase hex characters", got)
	}
	if identity.HashBytes([]byte("a")) == identity.HashBytes([]byte("b")) {
		t.Error("different bytes hashed identically")
	}
}
