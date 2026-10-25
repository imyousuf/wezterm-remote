// WezRelay exposes native WezTerm panes through a browser.
package main

import (
	"fmt"
	"os"

	"github.com/imyousuf/wezterm-remote/internal/relay"
)

func main() {
	if err := relay.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wezrelay:", err)
		os.Exit(1)
	}
}
