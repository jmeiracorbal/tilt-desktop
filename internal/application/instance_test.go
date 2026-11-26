package application

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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

const instanceTestRootEnv = "TILT_DESKTOP_TEST_INSTANCE_ROOT"

func TestInstanceLockProcessHelper(t *testing.T) {
	root := os.Getenv(instanceTestRootEnv)
	if root == "" {
		return
	}
	lock, err := acquireInstance(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := fmt.Fprintln(os.Stdout, "LOCKED"); err != nil {
		t.Fatal(err)
	}
	// The parent keeps stdin open until it terminates the child. No sleep
	// or polling is needed to keep the child alive while holding the lock.
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceLockReleasedAfterProcessTermination(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestInstanceLockProcessHelper$")
	cmd.Env = append(os.Environ(), instanceTestRootEnv+"="+root)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "LOCKED\n" {
		t.Fatalf("child did not acquire the lock: %q, %v", line, err)
	}

	competing, err := acquireInstance(root)
	if competing != nil {
		_ = competing.Close()
	}
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("another process acquired the lock: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Success() {
		t.Fatalf("child did not terminate abruptly: %v", err)
	}

	recovered, err := acquireInstance(root)
	if err != nil {
		t.Fatalf("lock remained held after process termination: %v", err)
	}
	defer recovered.Close()
}
