// tilt-desktop-check is the headless dependency preflight for installers.
// It requires no GUI libraries and never opens the project catalog.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jmeiracorbal/tilt-desktop/internal/environment"
)

func main() {
	ctx,cancel := context.WithTimeout(context.Background(),30*time.Second)
	defer cancel()
	os.Exit(run(ctx, os.Stdout, os.Stderr, os.Args[1:], environment.NewChecker().Check))
}

func run(ctx context.Context, out, stderr io.Writer, args []string, check func(context.Context) environment.Report) int {
	if len(args)!=0 {
		fmt.Fprintln(stderr,"usage: tilt-desktop-check")
		return 2
	}
	report := check(ctx)
	encoder := json.NewEncoder(out)
	encoder.SetIndent("","  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr,"write environment report: %v\n",err)
		return 2
	}
	if !report.Ready { return 1 }
	return 0
}
