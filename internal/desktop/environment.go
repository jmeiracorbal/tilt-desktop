package desktop

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/jmeiracorbal/tilt-desktop/internal/environment"
)

func (v *view) showEnvironment() {
	var report environment.Report
	v.execute("Checking environment", func(ctx context.Context) error {
		report = environment.NewChecker().Check(ctx)
		return nil
	}, func() {
		label := widget.NewLabel(formatEnvironmentReport(report))
		label.Wrapping = fyne.TextWrapWord
		panel := dialog.NewCustom("Environment", "Close", container.NewVScroll(label), v.window)
		panel.Resize(fyne.NewSize(620, 420))
		panel.Show()
	})
}

func formatEnvironmentReport(report environment.Report) string {
	var out strings.Builder
	if report.Ready {
		out.WriteString("Required tools and Docker daemon available.\n")
	} else {
		out.WriteString("Environment requirements are not satisfied.\n")
	}
	for _, check := range report.Checks {
		status := "Unavailable"
		if check.Available { status = "Available" }
		fmt.Fprintf(&out, "\n%s: %s\n", check.Name, status)
		if check.Path != "" { fmt.Fprintf(&out, "Path: %s\n",check.Path) }
		if check.Version != "" { fmt.Fprintf(&out, "Version: %s\n",check.Version) }
		if check.OSType != "" { fmt.Fprintf(&out, "Container OS: %s\n",check.OSType) }
		if check.Error != "" { fmt.Fprintf(&out, "Error: %s\n",check.Error) }
	}
	out.WriteString("\nThis check does not install dependencies or guarantee cluster provisioning.")
	return out.String()
}
