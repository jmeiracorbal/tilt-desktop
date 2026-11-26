package environment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCheckToolsProbesRequiredExecutables(t *testing.T) {
	var calls []string
	checker := NewChecker()
	checker.lookup = func(name string) (string, error) {
		return filepath.Join(t.TempDir(), name), nil
	}
	checker.run = func(_ context.Context, path string, args ...string) (string, error) {
		name := filepath.Base(path)
		calls = append(calls, name+" "+strings.Join(args, " "))
		return name + " version 1.2.3\n", nil
	}
	results := checker.CheckTools(context.Background())
	want := []string{"docker --version", "k3d version", "tilt version"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("probes: %v, want %v", calls, want)
	}
	if len(results) != len(want) {
		t.Fatalf("unexpected results: %+v", results)
	}
	for _, result := range results {
		if !result.Available() || result.Version != string(result.Tool)+" version 1.2.3" {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
}

func TestCheckToolsReportsIndependentFailures(t *testing.T) {
	checker := NewChecker()
	checker.lookup = func(name string) (string, error) {
		if name == "docker" {
			return "", exec.ErrNotFound
		}
		return name, nil
	}
	probeErr := errors.New("cannot execute binary")
	checker.run = func(_ context.Context, path string, _ ...string) (string, error) {
		switch path {
		case "k3d":
			return "", probeErr
		case "tilt":
			return " \n ", nil
		default:
			t.Fatalf("unexpected command: %s", path)
			return "", nil
		}
	}
	results := checker.CheckTools(context.Background())
	if len(results) != 3 {
		t.Fatalf("unexpected results: %+v", results)
	}
	want := []error{exec.ErrNotFound, probeErr, ErrEmptyVersion}
	for i, result := range results {
		if result.Available() || !errors.Is(result.Err, want[i]) {
			t.Fatalf("result %d: %+v, want %v", i, result, want[i])
		}
	}
}

func TestCheckToolsTimeoutAndCancellation(t *testing.T) {
	checker := NewChecker()
	checker.timeout = 10 * time.Millisecond
	checker.lookup = func(name string) (string, error) { return name, nil }
	checker.run = func(ctx context.Context, path string, _ ...string) (string, error) {
		if path == "docker" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "version 1.2.3", nil
	}
	results := checker.CheckTools(context.Background())
	if !errors.Is(results[0].Err, context.DeadlineExceeded) || !results[1].Available() || !results[2].Available() {
		t.Fatalf("individual timeout prevented subsequent probes: %+v", results)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checker.lookup = func(string) (string, error) {
		t.Fatal("lookup after cancellation")
		return "", nil
	}
	for _, result := range checker.CheckTools(ctx) {
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("expected cancellation: %+v", result)
		}
	}
}

// Reuse the test executable to exercise the real process runner without
// requiring Docker, k3d or Tilt on the unit-test host.
func TestDiagnosticProcessHelper(t *testing.T) {
	if os.Getenv("TILT_DESKTOP_DIAGNOSTIC_TEST") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "version":
		fmt.Fprintln(os.Stdout, "tool version 1.2.3")
		os.Exit(0)
	case "failure":
		fmt.Fprintln(os.Stderr, "probe failed")
		os.Exit(7)
	case "large":
		fmt.Fprint(os.Stdout, strings.Repeat("x", outputLimit+1))
		os.Exit(0)
	case "slow":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func TestRunCommand(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TILT_DESKTOP_DIAGNOSTIC_TEST", "1")
	run := func(ctx context.Context, mode string) (string, error) {
		return runCommand(ctx, path, "-test.run=^TestDiagnosticProcessHelper$", "--", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := run(ctx, "version")
	if err != nil || version != "tool version 1.2.3" {
		t.Fatalf("version: %q, %v", version, err)
	}
	_, err = run(ctx, "failure")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("failure: %v", err)
	}
	if _, err := run(ctx, "large"); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("oversized output: %v", err)
	}
	probe, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if _, err := run(probe, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
}
