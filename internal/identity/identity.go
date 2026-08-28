// Package identity provides canonical framing and hashing for artifact IDs.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// HashInputs deterministically hashes a map of named identity inputs.
func HashInputs(inputs map[string]string) string {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		parts = append(parts, key, inputs[key])
	}
	return HashBytes(Frame(parts...))
}

// Frame length-prefixes each part so concatenated fields remain unambiguous.
func Frame(parts ...string) []byte {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(strconv.Itoa(len(part)))
		builder.WriteByte(':')
		builder.WriteString(part)
	}
	return []byte(builder.String())
}

// HashBytes returns the lowercase hexadecimal SHA-256 digest of data.
func HashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
