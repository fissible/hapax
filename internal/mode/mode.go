// Package mode resolves the process-wide local-only decision at a composition root.
package mode

import "errors"

// ErrInvalid reports an HAPAX_LOCAL_ONLY value outside its closed grammar.
var ErrInvalid = errors.New("invalid HAPAX_LOCAL_ONLY")

// Mode is the immutable decision passed inward from a composition root.
type Mode struct {
	LocalOnly bool
}

// Resolve combines --local-only with HAPAX_LOCAL_ONLY. The environment reader
// is supplied by the composition root so this package never reads process state.
func Resolve(flagPresent bool, env func(string) (string, bool)) (Mode, error) {
	value, present := env("HAPAX_LOCAL_ONLY")
	if !present {
		return Mode{LocalOnly: flagPresent}, nil
	}

	switch value {
	case "0":
		return Mode{LocalOnly: flagPresent}, nil
	case "1":
		return Mode{LocalOnly: true}, nil
	default:
		return Mode{}, ErrInvalid
	}
}
