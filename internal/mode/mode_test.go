package mode_test

import (
	"errors"
	"testing"

	"github.com/fissible/hapax/internal/mode"
)

// env builds an environment reader over a fixed map. A nil value means unset,
// which is a different thing from set-and-empty.
func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

// The grammar is closed. "1" and "0" and nothing else, because a permissive
// reader of a security-relevant switch is a reader that guesses.
func TestTheEnvironmentGrammarIsExactlyZeroAndOne(t *testing.T) {
	for _, c := range []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"0", false},
	} {
		t.Run(c.value, func(t *testing.T) {
			got, err := mode.Resolve(false, env(map[string]string{"HAPAX_LOCAL_ONLY": c.value}))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.LocalOnly != c.want {
				t.Errorf("LocalOnly = %v, want %v", got.LocalOnly, c.want)
			}
		})
	}
}

func TestAnythingElseIsInvalid(t *testing.T) {
	for _, value := range []string{"", "true", "false", "yes", "no", "2", "-1", " 1", "1 ", "01", "TRUE"} {
		t.Run("value:"+value, func(t *testing.T) {
			if _, err := mode.Resolve(false, env(map[string]string{"HAPAX_LOCAL_ONLY": value})); !errors.Is(err, mode.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// Unset is absent, and absent is not the same as "0": with the flag present it
// still resolves local-only.
func TestUnsetLeavesTheFlagInCharge(t *testing.T) {
	for _, flagPresent := range []bool{false, true} {
		got, err := mode.Resolve(flagPresent, env(nil))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.LocalOnly != flagPresent {
			t.Errorf("flag %v gave LocalOnly %v", flagPresent, got.LocalOnly)
		}
	}
}

// The flag wins over a well-formed environment value, in the safe direction and
// in the other one: --local-only with HAPAX_LOCAL_ONLY=0 is still local-only.
func TestTheFlagWinsOverAWellFormedValue(t *testing.T) {
	got, err := mode.Resolve(true, env(map[string]string{"HAPAX_LOCAL_ONLY": "0"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.LocalOnly {
		t.Error("--local-only was overridden by HAPAX_LOCAL_ONLY=0")
	}
}

// Failing closed means the value can never SELECT CLOUD. It does not mean the
// classification softens when the flag happens to agree: a malformed value is a
// malformed invocation whether or not --local-only is also present.
func TestAMalformedValueIsInvalidEvenWhenTheFlagAgrees(t *testing.T) {
	if _, err := mode.Resolve(true, env(map[string]string{"HAPAX_LOCAL_ONLY": "yes"})); !errors.Is(err, mode.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

// Resolve reads only through the reader it is given. A composition root is the
// one place the environment may be touched, and mode is not a composition root.
func TestResolveReadsOnlyTheNameItNeeds(t *testing.T) {
	var asked []string
	reader := func(name string) (string, bool) {
		asked = append(asked, name)
		return "", false
	}
	if _, err := mode.Resolve(false, reader); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, name := range asked {
		if name != "HAPAX_LOCAL_ONLY" {
			t.Errorf("read %q from the environment", name)
		}
	}
}
