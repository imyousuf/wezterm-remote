// Package relay implements the WezRelay server and PTY shim.
package relay

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
		var roots []fileRoot
		fs.Func("file-root", "file browser root directory (repeatable; replaces Home/server/tmp defaults)", func(path string) error {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			info, err := os.Stat(absolute)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return errors.New("file root must be a directory")
			}
			roots = append(roots, fileRoot{filepath.Base(absolute), absolute})
			return nil
		})
		addr := fs.String("addr", ":12620", "HTTP listen address (all interfaces by default)")
		token := fs.String("token", os.Getenv("WEZRELAY_TOKEN"), "optional access token (or WEZRELAY_TOKEN environment variable)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		return runServerWithRoots(*addr, *token, *sock, roots)
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
