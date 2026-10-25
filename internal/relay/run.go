// Package relay implements the WezRelay server and PTY shim.
package relay

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

// Run executes a serve or shim command.
func Run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: wezrelay serve [-addr :12620] [-token TOKEN] | shim [-shell PATH] [-sock PATH]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	sock := fs.String("sock", defaultSockPath(), "per-user relay Unix socket")
	switch args[0] {
	case "serve":
		addr := fs.String("addr", ":12620", "HTTP listen address (all interfaces by default)")
		token := fs.String("token", os.Getenv("WEZRELAY_TOKEN"), "optional access token (or WEZRELAY_TOKEN environment variable)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		return runServer(*addr, *token, *sock)
	case "shim":
		shell := fs.String("shell", "", "shell executable (defaults to SHELL or /bin/bash)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		return runShim(*sock, *shell)
	default:
		return fmt.Errorf("unknown command %q; use serve or shim", args[0])
	}
}
