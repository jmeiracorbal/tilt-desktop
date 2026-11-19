package application

import (
    "crypto/sha256"
    "encoding/hex"
    "path/filepath"
)

func projectID(root string) string {
    digest := sha256.Sum256([]byte(filepath.Clean(root)))
    return "td-" + hex.EncodeToString(digest[:10])
}
