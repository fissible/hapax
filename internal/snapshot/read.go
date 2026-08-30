// Package snapshot reads corpus files against their recorded content hashes.
package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
)

// ErrContentChanged reports that admitted bytes no longer match a snapshot.
var ErrContentChanged = errors.New("snapshot: content changed")

// VerifyAdmitted admits raw bytes and verifies the hash of their admitted form.
func VerifyAdmitted(raw []byte, contentHash string) (*text.Document, error) {
	document, err := text.Admit(raw)
	if err != nil {
		return nil, err
	}
	if identity.HashBytes(document.Raw()) != contentHash {
		return nil, ErrContentChanged
	}
	return document, nil
}

type changedError struct{ path string }

func (e changedError) Error() string {
	return fmt.Sprintf("snapshot document %q changed since the snapshot", e.path)
}
func (changedError) Unwrap() error { return ErrContentChanged }

// ReadVerified admits the recorded document and verifies its admitted bytes
// still match the snapshot content hash.
func ReadVerified(root, path, contentHash string) (*text.Document, error) {
	pathOnDisk := filepath.FromSlash(path)
	if filepath.IsAbs(pathOnDisk) || !filepath.IsLocal(pathOnDisk) {
		return nil, fmt.Errorf("snapshot document path %q is not local to the snapshot root", path)
	}

	raw, err := os.ReadFile(filepath.Join(root, pathOnDisk))
	if err != nil {
		return nil, fmt.Errorf("read snapshot document %q: %w", path, err)
	}
	document, err := VerifyAdmitted(raw, contentHash)
	if err != nil {
		if errors.Is(err, ErrContentChanged) {
			return nil, changedError{path: path}
		}
		return nil, fmt.Errorf("admit snapshot document %q: %w", path, err)
	}
	return document, nil
}
