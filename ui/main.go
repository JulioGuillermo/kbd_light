// kbdlight-ui: the window for kbdlight. It only talks to the daemon through its socket, so it can be
// closed at any time and the light keeps doing what was chosen.
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"kbdlight/light"
)

//go:embed all:frontend
var assets embed.FS

// App is what the page can call (window.go.main.App.*)
type App struct{}

// Status returns the daemon's configuration and the intensity being shown right now.
func (a *App) Status() (light.Response, error) { return light.Status() }

// Set applies a configuration; the daemon keeps it across reboots.
func (a *App) Set(c light.Config) (light.Response, error) {
	c, err := c.Clamp()
	if err != nil {
		return light.Response{}, err
	}
	return light.Set(c)
}

// Flash gives one notification flash, to try it.
func (a *App) Flash() error {
	_, err := light.Call(light.Request{Cmd: "flash"})
	return err
}

func main() {
	err := wails.Run(&options.App{
		Title: "Keyboard light",
		// A fixed size makes tiling window managers (Hyprland, sway) open it floating, like a dialog;
		// Brasa centres floating windows
		Width:            560,
		Height:           1010,
		DisableResize:    true,
		BackgroundColour: &options.RGBA{R: 0, G: 4, B: 15, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		Bind:             []interface{}{&App{}},
		Linux:            &linux.Options{ProgramName: "kbdlight-ui", WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand},
	})
	if err != nil {
		log.Fatal(err)
	}
}
