//go:build !windows

package application

import "os"

func makeInsecureFileForTest(path string) error {
	return os.Chmod(path, 0644)
}
