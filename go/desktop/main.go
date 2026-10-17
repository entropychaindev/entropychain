// Command desktop is the EntropyChain Miner desktop app (Wails). It embeds
// a full Go node, the miner and the wallet; the UI is plain HTML/JS in
// frontend/dist.
//
//	cd go/desktop && wails build      # -> build/bin/EntropyChainMiner.exe
//	cd go/desktop && wails dev        # live-reload development
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

var version = "v0.5.0 testnet-4"

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "EntropyChain Miner",
		Width:            980,
		Height:           760,
		MinWidth:         760,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 7, G: 9, B: 13, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Windows:          &windows.Options{Theme: windows.Dark},
	})
	if err != nil {
		log.Fatal(err)
	}
}
