package application

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrInstanceRunning = errors.New("another Tilt Desktop instance is running")

func localStateDirectory() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(root, "tilt-desktop"), nil
}

// AcquireLocalInstance acquires an exclusive, nonblocking OS lock for the
// desktop's local state directory. The caller must hold it for the entire
// desktop lifetime and close it only after all state operations have ceased.
// The lock file is never removed; its stable identity prevents split locks.
func AcquireLocalInstance() (io.Closer, error) {
	root, err := localStateDirectory()
	if err != nil {
		return nil, err
	}
	return acquireInstance(root)
}

func acquireInstance(root string) (io.Closer, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("absolute instance state directory is required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create instance state directory: %w", err)
	}
	path := filepath.Join(root, "instance.lock")
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("instance lock %q must be a regular file", path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect instance lock: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock: %w", err)
	}
	fail := func(err error) (io.Closer, error) {
		return nil, errors.Join(err, file.Close())
	}
	opened, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect opened instance lock: %w", err))
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fail(fmt.Errorf("inspect instance lock path: %w", err))
	}
	if !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return fail(errors.New("instance lock path does not identify the opened regular file"))
	}
	if err := lockInstanceFile(file); err != nil {
		return fail(fmt.Errorf("acquire instance lock: %w", err))
	}
	return file, nil
}
