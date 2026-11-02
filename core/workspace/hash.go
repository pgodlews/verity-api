package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/pgodlews/verity-api/core/model"
)

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashStep is the sha256 of step as canonical JSON. Inspect stores it and
// SaveStep compares it to the step now on disk. Map keys are sorted by
// encoding/json, so two loads of the same step hash the same.
func HashStep(step model.Step) (string, error) {
	b, err := json.Marshal(step)
	if err != nil {
		return "", err
	}
	return contentHash(b), nil
}

func lookupBaseline(base map[string]string, file string) (string, bool) {
	if base == nil {
		return "", false
	}
	if h, ok := base[file]; ok {
		return h, true
	}
	clean := filepath.Clean(file)
	if h, ok := base[clean]; ok {
		return h, true
	}
	return "", false
}
