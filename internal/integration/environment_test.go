//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jmeiracorbal/tilt-desktop/internal/environment"
)

func TestEnvironmentPreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := environment.NewChecker().Check(ctx)
	if !report.Ready || len(report.Checks) != 4 {
		t.Fatalf("environment preflight failed: %+v", report)
	}
	want := []string{"docker", "k3d", "tilt", "Docker daemon"}
	for i, check := range report.Checks {
		if check.Name != want[i] || !check.Available || check.Path == "" || check.Version == "" || check.Error != "" {
			t.Fatalf("unexpected diagnostic: %+v", check)
		}
		t.Logf("%s: %s (%s)", check.Name, check.Version, check.Path)
	}
	if report.Checks[3].OSType != "linux" {
		t.Fatalf("unsupported container OS: %q", report.Checks[3].OSType)
	}
}
