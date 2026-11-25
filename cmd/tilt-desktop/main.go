package main

import (
	"errors"
	"fmt"
	"log"

	"fyne.io/fyne/v2/app"

	"github.com/jmeiracorbal/tilt-desktop/internal/application"
	"github.com/jmeiracorbal/tilt-desktop/internal/desktop"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
	lock, err := application.AcquireLocalInstance()
	if err != nil {
		return fmt.Errorf("initialize Tilt Desktop: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()

	backend, err := application.NewLocalManager()
	if err != nil {
		return fmt.Errorf("initialize Tilt Desktop: %w", err)
	}
	gui := app.NewWithID("dev.tilt-desktop")
	window, err := desktop.NewWindow(gui, backend)
	if err != nil {
		return fmt.Errorf("create Tilt Desktop window: %w", err)
	}
	window.ShowAndRun()
	return nil
}
