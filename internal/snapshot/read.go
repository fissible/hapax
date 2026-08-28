// Package snapshot reads corpus files against their recorded content hashes.
package snapshot

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fissible/hapax/internal/identity"
	"github.com/fissible/hapax/internal/text"
)

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
	document, err := text.Admit(raw)
	if err != nil {
		return nil, fmt.Errorf("admit snapshot document %q: %w", path, err)
	}
	if identity.HashBytes(document.Raw()) != contentHash {
		return nil, fmt.Errorf("snapshot document %q changed since the snapshot", path)
	}
	return document, nil
}
