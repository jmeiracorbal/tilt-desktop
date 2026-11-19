//go:build !windows

package application

import (
	"fmt"
	"os"
)

func protectPrivateFile(path string) error {
	return os.Chmod(path, 0600)
}

func requirePrivateFile(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("file %q permits access by other users", path)
	}
	return nil
}
