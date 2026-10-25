# WezRelay: Architecture & System Design

## 1. Objective & Core Requirements
Stream interactive, multi-pane **WezTerm** terminal sessions running on an **Ubuntu desktop** to any web or mobile browser over **LAN and Tailscale**, with full two-way keyboard/touch input and **zero reliance on `tmux`**.

### Target Workflow
* **Multi-Agent CLI Support:** Seamlessly interact with rich TUI applications (**Claude Code**, **Codex CLI**, and **Antigravity / `agvty` CLI**) running side-by-side inside native WezTerm split panes and tabs.
* **No `tmux` Required:** Preserve 100% native WezTerm splits, tabs, scrollback, mouse behavior, and keybindings on the Ubuntu host without introducing `tmux` prefix keys or status bars.
* **Responsive Desktop & Mobile UX:**
  * **Desktop Web (`Grid View`):** View and interact with all split panes simultaneously side-by-side in a CSS Grid, mirroring the Ubuntu workspace.
  * **Mobile Web (`Single Pane View`):** Switch cleanly between individual agent panes (`T0:#0 claude`, `T0:#1 codex`, `T0:#2 agvty`) using a top switcher bar and a touch-friendly modifier keybar (`ESC`, `TAB`, `S-TAB`, `CTRL`, `Shift+Enter`, arrow keys).
  * **Geometry Preservation (`Match Desktop` vs. `Fit Browser`):** Prevent a small mobile viewport from shrinking the desktop WezTerm split panes and wrapping/corrupting TUI diff boxes unless explicitly toggled.

---

## 2. System Architecture

`wezrelay` is compiled as a **single self-contained Go binary** (embedding `index.html` via `go:embed`) that operates in two cooperating modes:

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Ubuntu Desktop (WezTerm GUI)                                                 │
│                                                                              │
│  ┌───────────────────────┐  ┌───────────────────────┐  ┌──────────────────┐  │
│  │ Native Split Pane #0  │  │ Native Split Pane #1  │  │ Pre-existing     │  │
│  │  `wezrelay shim`      │  │  `wezrelay shim`      │  │ Pane #2 (No Shim)│  │
│  │  └─ /dev/ptmx (bash)  │  │  └─ /dev/ptmx (bash)  │  │  └─ bash / agvty │  │
│  │      └─ claude        │  │      └─ codex         │  │                  │  │
│  └───────────┬───────────┘  └───────────┬───────────┘  └────────┬─────────┘  │
└──────────────┼──────────────────────────┼───────────────────────┼────────────┘
               │ Raw PTY Binary Frames    │                       │
               │ (/tmp/wezrelay-UID.sock) │                       │ `wezterm cli`
               ▼                          ▼                       ▼  IPC Socket
┌──────────────────────────────────────────────────────────────────────────────┐
│ `wezrelay serve` (Go Daemon — HTTP / WebSocket + Unix Socket Hub)            │
│  • Maintains 64KB ANSI RingBuffer per pane for instant browser render        │
│  • Merges live Shim PTY streams with `wezterm cli list` tab/pane hierarchy   │
│  • Executes remote splits/tabs via `wezterm cli split-pane` & `spawn`        │
└─────────────────────────────────────┬────────────────────────────────────────┘
                                      │ Binary WebSockets (/ws?id=...)
                                      │ Over Local LAN (0.0.0.0:8080) & Tailscale
                                      ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│ Web / Mobile Client (`index.html` + `xterm.js`)                              │
│  • Multi-Pane CSS Grid (Desktop) or Single-Pane Switcher (Phone)             │
│  • Intercepts Shift+Enter (\x1b\r) for multi-line prompts in Claude / Codex  │
│  • Touch Keybar for ESC, TAB, CTRL, Arrow Keys, and ^C                       │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Dual-Engine Streaming Design

1. **Mode A: Zero-Latency PTY Shim (`LIVE` Badge — Recommended)**
   * Configured via `default_prog = { "/usr/local/bin/wezrelay", "shim" }` in `~/.config/wezterm/wezterm.lua`.
   * When WezTerm opens any tab or split pane, `wezrelay shim` allocates a real PTY (`creack/pty`) for your shell and sets local stdin to raw mode (`term.MakeRaw`). Locally inside WezTerm, it feels 100% indistinguishable from running `/bin/bash` or `/bin/zsh` directly.
   * In the background, the shim dials `/tmp/wezrelay-<uid>.sock` and streams raw PTY output (`OpOutput`), input (`OpInput`), and window resize events (`OpResize` / `SIGWINCH`) to `wezrelay serve` with **0ms polling overhead**.
   * `wezrelay serve` caches the last **64 KB** of raw ANSI output in a thread-safe `RingBuffer` per pane so newly connected browsers immediately render the current TUI screen without waiting for a redraw.

2. **Mode B: `wezterm cli` Fallback (`CLI` Badge — Zero Config)**
   * If you already have WezTerm panes open before configuring `default_prog`, `wezrelay serve` automatically discovers them via `wezterm cli list --format json` (auto-locating `/run/user/<uid>/wezterm/gui-sock-*`).
   * It streams screen updates using differential snapshots (`wezterm cli get-text --pane-id <id> --escapes`) wrapped in `DECSET 2026` synchronized output (`\x1b[?2026h ... \x1b[?2026l`) and injects keystrokes via `wezterm cli send-text --no-paste`.

---

## 3. Solving TUI Quirks (Claude Code, Codex, Antigravity)

| Challenge | How WezRelay Solves It |
|---|---|
| **Desktop vs. Mobile Screen Size Clash** | By default, `Match Desktop` (`resizeRemotePty = false`) keeps `xterm.js` locked to the exact `cols x rows` of your Ubuntu WezTerm split pane so connecting from a phone never crushes your 3 desktop columns into 25-char strips. Toggling `Fit Browser` sends `OpResize` to dynamically fit your current screen. |
| **Multi-Line Prompts (`Shift+Enter`)** | Standard mobile/web keyboards send `\r` for both `Enter` and `Shift+Enter`. WezRelay intercepts `Shift+Enter` via `attachCustomKeyEventHandler` (and provides a dedicated `Shift+Enter` keybar button) to emit `\x1b\r`, which Claude Code and Codex CLI recognize as a newline without submitting. |
| **Remote Splits & Tab Creation** | Clicking `+ Split →`, `+ Split ↓`, or `+ Tab` in the web UI calls `/api/split` and `/api/spawn`, which invoke `wezterm cli split-pane` on the Ubuntu host—splitting your actual desktop WezTerm window in real time. |
| **Tailscale & Mobile Sleep Drops** | The server sends WebSocket `PingMessage` keepalives every 25s, and the browser client automatically reconnects on `onclose` and replays the 64 KB ring buffer when waking a phone. |

---

## 4. Build & Deployment Guide on Ubuntu

### Step 1: Build the Single Binary
Place `main.go` and `index.html` in the same directory on your Ubuntu machine:

```bash
mkdir -p ~/wezrelay && cd ~/wezrelay
# (Save main.go and index.html here)

go mod init wezrelay
go get github.com/creack/pty github.com/gorilla/websocket golang.org/x/term
go build -o wezrelay main.go
sudo install -m 755 wezrelay /usr/local/bin/wezrelay
```

### Step 2: Configure WezTerm (`~/.config/wezterm/wezterm.lua`)
Add `default_prog` so every new tab and split pane uses the zero-latency PTY shim while preserving your normal login shell:

```lua
local wezterm = require 'wezterm'
local config = wezterm.config_builder()

-- Spawn shells through the transparent WezRelay PTY shim
config.default_prog = { '/usr/local/bin/wezrelay', 'shim' }

return config
```
*(Even before restarting WezTerm, `wezrelay serve` will immediately work with your existing panes in `CLI` fallback mode!)*

### Step 3: Run the Server & Expose via Tailscale
Start the server locally (optionally setting a token for LAN security):

```bash
WEZRELAY_TOKEN="my-secret-token" wezrelay serve -addr 0.0.0.0:8080
```

To expose it with automatic HTTPS certificates across your Tailscale tailnet:
```bash
tailscale serve --bg 8080
```

* **Over LAN:** Open `http://<ubuntu-lan-ip>:8080/?token=my-secret-token`
* **Over Tailscale:** Open `https://<ubuntu-machine>.<tailnet>.ts.net/?token=my-secret-token` (the token is automatically saved to `localStorage` after the first visit).

---

## 5. Future Roadmap / Optional Enhancements
1. **Tailscale `WhoIs` Identity Auth:** Use `tailscale.com/client/tailscale.LocalClient` in `checkAuth` to automatically authenticate connections coming from `100.64.0.0/10` without requiring a token.
2. **Systemd User Service:** Run `wezrelay serve` automatically on Ubuntu login via `~/.config/systemd/user/wezrelay.service`.
3. **Android APK Wrapper:** Wrap the UI in a lightweight Android WebView shell that maps physical **Volume Up / Volume Down** keys to `CTRL` / `ESC` and sets `TYPE_TEXT_FLAG_NO_SUGGESTIONS` to eliminate Android keyboard autocorrect interference.