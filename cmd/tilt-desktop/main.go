package main

import (
	"log"

	"fyne.io/fyne/v2/app"

	"github.com/jmeiracorbal/tilt-desktop/internal/application"
	"github.com/jmeiracorbal/tilt-desktop/internal/desktop"
)

func main() {
	backend, err := application.NewLocalManager()
	if err != nil {
		log.Fatalf("initialize Tilt Desktop: %v", err)
	}
	application := app.NewWithID("dev.tilt-desktop")
	window, err := desktop.NewWindow(application, backend)
	if err != nil {
		log.Fatalf("create Tilt Desktop window: %v", err)
	}
	window.ShowAndRun()
}
