//go:build wails

package main

import (
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails v3 desktop shell (beta API per https://v3.wails.io/migration/v2-to-v3/).
//
// This file is excluded from default builds. To use it:
//
//	go get github.com/wailsapp/wails/v3@latest   # pin the beta in go.mod
//	cp ../../frontend/dist/index.html frontend/dist/index.html
//	go build -tags wails .
//
// Full desktop flow (hot reload, bindings, packaging) needs the v3 CLI:
//	go install github.com/wailsapp/wails/v3/cmd/wails3@latest
//	wails3 setup        # checks OS webview deps, see README.md
//	wails3 dev | wails3 build
//
// NOTE: the -tags wails build needs a desktop host with OS webview dev
// headers and cannot be verified in headless CI; default `go build ./...`
// and `go vet ./...` intentionally skip this file.

//go:embed frontend/dist
var assets embed.FS

func main() {
	svc := NewGuiService(os.Getenv("LANTERND_URL"), os.Getenv("LANTERN_DAEMON_TOKEN"))
	runWails(svc)
}

func runWails(svc *GuiService) {
	app := application.New(application.Options{
		Name:        "Lantern",
		Description: "Peer-to-peer file transfer",
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Lantern",
		Width:  1024,
		Height: 768,
	})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
