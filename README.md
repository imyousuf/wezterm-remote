# wezterm-remote (WezRelay)

WezRelay aims to make native WezTerm sessions on an Ubuntu desktop accessible from desktop and mobile browsers over LAN or Tailscale. The goal is two-way interaction with shells and rich terminal applications such as Claude Code, Codex CLI, and Antigravity (`agvty`), while keeping WezTerm's tabs and split panes and requiring no `tmux`.

**Status:** design and reference prototype. The repository contains an architecture guide and sample Go server/PTY shim and browser UI under `doc/`. There is no root Go module, packaged application, automated test suite, or release workflow yet. Features described below are prototype behavior or design goals, not verified production guarantees.

## Design

The proposed application is a single Go binary with an embedded HTML client and two cooperating modes:

- `wezrelay shim` runs inside a WezTerm pane, starts a login shell in a PTY, and forwards output to both the local terminal and the server. Input can come from either the local terminal or a browser.
- `wezrelay serve` combines shim sessions with pane metadata from `wezterm cli`, serves the browser UI, and relays terminal traffic over WebSockets. Shim connections use a per-user Unix socket, `/tmp/wezrelay-<uid>.sock`, and each session keeps up to 64 KiB of recent output for reconnecting clients.
- Panes opened without a shim use a fallback that polls `wezterm cli get-text` and sends input with `wezterm cli send-text`. This is a snapshot-based fallback rather than a full PTY stream.

The sample UI includes a desktop pane grid, a mobile pane switcher, a touch keybar, and controls to create actual WezTerm splits and tabs. **Match Desktop** preserves reported desktop terminal dimensions by default; **Fit Browser** requests remote resizing for shim panes. Its grid arranges panes responsively; it does not reconstruct the exact desktop split geometry. Shift+Enter sends an escape-plus-carriage-return sequence whose behavior should be checked with the target application.

## Repository guide

| Path | Purpose |
| --- | --- |
| [Architecture and setup guide](doc/wezrelay_architecture_setup_guide.md) | Project intent, proposed architecture, deployment sketches, and roadmap |
| [Go prototype](doc/sample_code/wezrelay_server_pty_shim.go) | Server, Unix socket protocol, PTY shim, and WezTerm CLI fallback |
| [Browser prototype](doc/sample_code/wezrelay_web_mobile_ui.html) | HTML/CSS/JavaScript client using xterm.js |
| [AGENTS.md](AGENTS.md) | Guidance for contributors working with coding agents |
| [LICENSE](LICENSE) | GNU Affero General Public License, version 3 |

## Try the reference prototype

Prerequisites are Ubuntu/Linux, Go, WezTerm with a running GUI session, and a browser. Run the server as the same user as WezTerm, with `wezterm` on `PATH`. The sample uses `github.com/creack/pty`, `github.com/gorilla/websocket`, and `golang.org/x/term`; dependency versions are not pinned in this repository. The browser fetches xterm.js and its fit addon from jsDelivr, so the prototype also needs access to that CDN.

The Go file embeds `index.html`; the sample files must first be copied under the expected names. From the repository root, build in a separate temporary directory:

```bash
prototype_dir=$(mktemp -d)
cp doc/sample_code/wezrelay_server_pty_shim.go "$prototype_dir/main.go"
cp doc/sample_code/wezrelay_web_mobile_ui.html "$prototype_dir/index.html"
cd "$prototype_dir"
go mod init wezrelay
go get github.com/creack/pty github.com/gorilla/websocket golang.org/x/term
go build -o wezrelay .
WEZRELAY_TOKEN='replace-with-a-private-token' ./wezrelay serve -addr 127.0.0.1:8080
```

Open `http://127.0.0.1:8080/?token=replace-with-a-private-token`. Existing WezTerm panes are discovered through the CLI fallback. The server uses `WEZTERM_UNIX_SOCKET` when set, otherwise it looks for the newest GUI socket under `/run/user/<uid>/wezterm/`.

For PTY streaming in newly opened panes, copy the built binary to a stable location and merge this setting into your existing `~/.config/wezterm/wezterm.lua` configuration, using its absolute path:

```lua
config.default_prog = { '/absolute/path/to/wezrelay', 'shim' }
```

The shim uses `$SHELL`, falling back to `/bin/bash`; `shim -shell /path/to/shell` overrides it. Both modes accept `-sock` to select a shared socket path. The server accepts `-addr` and `-token` (defaulting to `WEZRELAY_TOKEN`).

The sample defaults to listening on `0.0.0.0:8080` and allows unauthenticated access when no token is configured. Browser access can execute commands in your desktop terminals. Keep experimental use on loopback or a trusted private network with a token; the example above binds to loopback explicitly. The UI remembers a supplied token in browser local storage. The setup guide outlines LAN and Tailscale deployment, but authentication and network exposure need review before broader use; the prototype accepts any WebSocket origin and does not provide TLS itself.

## Development

Start with the architecture guide, then compare its claims with both samples. Keep reference material in `doc/` until application code is deliberately introduced. A future implementation should add a Go module with pinned dependencies, embed or package browser assets, and establish build and test commands.

For changes to Go behavior, validate in an isolated module using the file mapping above; once a real application module exists, document and run its build, tests, and vet checks. Manually exercise local terminal input, browser input, reconnects, CLI fallback, pane/tab creation, and resize behavior on a running WezTerm session. Desktop/mobile behavior and agent TUI compatibility require integration testing and have not been established by this documentation change.

The design roadmap includes Tailscale identity authentication, a systemd user service, and an Android WebView wrapper. These are future work.

Licensed under the [GNU Affero General Public License, version 3](LICENSE).
