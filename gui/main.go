// Command verity-desktop is the Verity desktop application, built with Wails v2.
//
// It is a thin shell over the same core library the CLI uses: the App struct
// (app.go) binds core/workspace operations to a React + TypeScript frontend:
// opening, validating, editing, and saving projects; runs and load tests;
// imports; environments and OS keychain secrets; OAuth sign-in; and Git
// source control. No protocol/auth/test/reporting logic lives here.
//
// Build (requires the Wails CLI — see gui/README.md):
//
//	wails build            # produces a single self-contained binary
//	wails dev              # hot-reload development
//
// Wails embeds the built frontend (frontend/dist) into the Go binary, so the
// result is one cross-platform executable with no JVM and no Electron.
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// frontend/dist holds a committed .gitkeep, so this compiles (and vet and
// tests run) before the first frontend build.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:  "Verity — API Contract Testing",
		Width:  1200,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []any{
			app, // exposes App methods to the frontend
		},
	})
	if err != nil {
		panic(err)
	}
}
