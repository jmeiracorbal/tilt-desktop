package application

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceLockExclusionAndRelease(t *testing.T) {
	root := t.TempDir()
	first, err := acquireInstance(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := acquireInstance(root)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("expected exclusive lock conflict, got %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireInstance(root)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if _, err := os.Stat(filepath.Join(root, "instance.lock")); err != nil {
		t.Fatalf("stable lock file missing: %v", err)
	}
}

func TestInstanceLockRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "instance.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireInstance(root)
	if lock != nil {
		_ = lock.Close()
	}
	if err == nil || errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("expected invalid lock path error, got %v", err)
	}
}
