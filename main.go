// Tailtray is a menu bar app for Tailscale on macOS and Linux: exit nodes,
// devices, Serve and Funnel, Taildrop and accounts, through tailscaled's
// local API.
package main

import (
	"flag"
	"fmt"
	"os"

	"fyne.io/systray"
	"tailscale.com/client/local"
)

func main() {
	theme := flag.String("theme", "dark", "Linux tray icon colors for a dark or light bar: dark or light")
	socket := flag.String("socket", "", "tailscaled's socket, if not the default")
	flag.Parse()

	p := dark
	switch *theme {
	case "dark":
	case "light":
		p = light
	default:
		fmt.Fprintf(os.Stderr, "tailtray: unknown theme %q\n", *theme)
		os.Exit(2)
	}

	// Linux trays identify the app by the title set before Run.
	systray.SetTitle("tailtray")
	a, err := newApp(&local.Client{Socket: *socket}, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tailtray: %v\n", err)
		os.Exit(1)
	}
	systray.Run(a.ready, nil)
}
