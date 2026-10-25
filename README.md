# WezRelay

Access your native WezTerm desktop panes from a desktop or mobile browser, including already-running terminal CLI agents. WezRelay is a Linux Go application with an embedded, offline browser UI. It uses native WezTerm tabs and splits, with no tmux dependency.

Existing panes work immediately through `wezterm cli`: the browser receives screen snapshots and sends keyboard input to the real pane. For full PTY output streaming, start new panes through `wezrelay shim`. The desktop and browser can both type into the same shell.

## Build and run

Requires Linux, Go 1.24 or newer, and a running WezTerm GUI belonging to the same user. `wezterm` must be on `PATH`.

```bash
go build -o wezrelay .
./wezrelay serve
```

Open **http://127.0.0.1:12620/** to connect without a token. Choose an existing pane from the dropdown, then type directly into the terminal. **+ View tab** opens another application tab; each view remembers its selected pane and can choose any discovered pane from the dropdown. Close a view with **×**. Only the active view renders a terminal and opens a WebSocket; discovery does not activate other panes. Switching views reconnects to the selected pane using its snapshot or bounded live replay. **+ Terminal tab** creates an actual WezTerm tab in the selected pane’s desktop window. Existing CLI agents do not need restarting. `LIVE` marks streaming shim panes; `SNAPSHOT` marks existing panes polled every 250 ms. Snapshot mode supports input but does not reproduce full terminal state, mouse behavior or cursor placement as accurately as a PTY stream.

The default view preserves desktop dimensions. On smaller displays, swipe sideways across the terminal or use its horizontal position slider and pan buttons to read wide output; vertical swipes retain terminal scrolling. These viewing controls do not resize the desktop pane. **Resize to browser** explicitly resizes live shim PTYs; **Restore desktop size** restores desktop dimensions. Snapshot panes retain their desktop size, so their resize button is disabled. The touch bar provides Escape, Tab, arrows, Ctrl, Ctrl+C and Shift+Enter. Shift+Enter sends ESC plus carriage return; interpretation depends on the terminal application. Browser split/tab buttons create actual desktop WezTerm panes.

## Connect from another device

The default listener is **:12620**, covering all desktop network interfaces. Open **http://YOUR-DESKTOP-TAILSCALE-IP:12620/** from another device on your tailnet, or use the desktop's LAN IP. No application token is required by default; access follows your network and Tailscale access controls.

`serve -addr HOST:PORT` changes the bind address; for example, `-addr 127.0.0.1:12620` restricts it to loopback. Set `WEZRELAY_TOKEN` or pass `-token TOKEN` to enable optional bearer authentication. When enabled, the browser displays a token entry form. Tokens are held in browser session storage; a `?token=...` URL is accepted and removed from the address bar. Pass `-token ''` to override an existing token environment variable and disable authentication.

The application serves HTTP. You can optionally use `tailscale serve --bg http://127.0.0.1:12620` for a Tailscale HTTPS URL; this proxy deployment path has not been exercised by this repository's integration checks. WebSocket and mutation origins must match the request host, including when using the desktop's Tailscale address. Configure reverse proxies to preserve the original Host header.

## Full streaming for new panes

Build/install the binary at a stable absolute path, then merge this into your existing WezTerm Lua configuration (do not replace your other settings):

```lua
config.default_prog = { '/absolute/path/to/wezrelay', 'shim' }
```

New tabs and splits launch your `$SHELL` as a login shell inside a PTY. `shim -shell /bin/bash` selects another executable. Local raw terminal settings are restored on exit, desktop resize signals propagate, and shims reconnect if the server restarts. Browser reconnects replay up to 64 KiB of recent ANSI output; this is bounded history, not a complete terminal screen-state emulator. Output produced while the server is absent is not replayed.

Both modes accept `-sock PATH` (default `/tmp/wezrelay-<uid>.sock`). The server creates its socket with mode 0600 and refuses any existing path; if an unclean exit leaves a stale socket, verify no server owns it before removing it manually. Never share this socket with other users. `WEZTERM_UNIX_SOCKET` selects a WezTerm GUI; otherwise the newest GUI socket under `/run/user/<uid>/wezterm/` is used.

## Development and validation

```bash
go build ./...
go test ./...
go test -race ./...
go vet ./...
node --check web/app.js
```

The optional `python3 tests/mobile_browser.py --run` browser regression check requires an already running ATR browser. It opens a dedicated mobile fixture tab with fake panes and sockets, tests initial tail position, scrollback, simulated keyboard viewport/focus recovery, horizontal panning and explicit fit/restoration, and leaves the test tab open. It does not exercise a native phone keyboard.

Go dependencies are pinned in `go.mod`/`go.sum`. Browser assets are embedded and vendored: xterm.js 5.5.0 and addon-fit 0.10.0, with upstream MIT licenses in `web/vendor/`. No CDN, Node build or frontend framework is needed to run the binary.

Tests cover bounded/partial protocol framing, authenticated APIs and origin checks, Unix socket safety, concurrent subscriptions and replay, WebSocket input/resize routing, snapshot input/output, and an actual shell subprocess driven through both a local PTY and Unix bridge. Real WezTerm/browser validation was performed on 2026-10-25 using Go 1.27.3: browser input reached live and existing snapshot panes, New tab and Split created native panes, touch Ctrl+C interrupted a shell command, 390×844 mobile fit/restoration worked, and browser plus shim reconnected after a server restart. CLI agent compatibility varies by application; actual Claude/Codex agent sessions were not altered or tested.

## Design references

The [architecture guide](doc/wezrelay_architecture_setup_guide.md) records original design intent; claims such as zero latency, full TUI fidelity and complete screen restoration are goals, not guarantees. The [Go prototype](doc/sample_code/wezrelay_server_pty_shim.go) and [HTML prototype](doc/sample_code/wezrelay_web_mobile_ui.html) remain historical samples; they retain their original network and authentication behavior. The Go sample has a build exclusion to keep root module checks focused on the application. To try the sample, copy it as `main.go` and the HTML as `index.html` into an isolated module, resolve its three Go dependencies, and build with `go build main.go`.

See [AGENTS.md](AGENTS.md) for contributor guidance and [LICENSE](LICENSE) for the GNU Affero General Public License v3.
