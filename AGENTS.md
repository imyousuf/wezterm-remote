# Agent guidance

## Project intent and current state

`wezterm-remote`, called **WezRelay** in its design, aims to expose native WezTerm terminal panes on an Ubuntu desktop to desktop and mobile browsers over LAN and Tailscale. Preserve local WezTerm interaction and native tabs/splits; do not introduce a `tmux` dependency.

This repository contains a root Go application, embedded browser UI under `web/`, tests, and historical design/reference material under `doc/`. See README for current build/run commands and verified limitations; do not describe design goals as verified features. No CI configuration is provided.

Read these files before implementation:

1. `README.md` for repository status and prototype setup.
2. `doc/wezrelay_architecture_setup_guide.md` for objectives and architectural intent.
3. `doc/sample_code/wezrelay_server_pty_shim.go` and `doc/sample_code/wezrelay_web_mobile_ui.html` for the actual prototype behavior.

Treat the guide as design intent and the samples as reference code. Resolve discrepancies explicitly in documentation and implementation rather than assuming every claim in the guide is implemented or tested.

## Architectural constraints

- Intended delivery is a single Go binary with an embedded browser UI. The Go sample embeds `index.html`; the reference HTML has a different filename and must be mapped to that name when building the sample.
- `shim` starts a shell in a PTY inside each WezTerm pane. `serve` connects shims over a per-user Unix socket and relays output/input through browser WebSockets.
- Preserve the existing-pane fallback based on `wezterm cli`, while distinguishing snapshot polling from PTY streaming.
- Preserve desktop terminal dimensions by default. Browser-driven resizing is an explicit user choice and currently applies to shim panes.
- Browser split/tab actions affect the actual desktop workspace. Preserve local input, terminal state restoration, shell lifecycle, and reconnect behavior when changing the relay.
- The Unix protocol uses a one-byte opcode followed by a four-byte big-endian payload length. Opcodes cover output, input, resize, and metadata; resize payloads contain big-endian uint16 columns and rows. Change both endpoints together.

## Working practices

- Inspect `git status` first. Preserve unrelated and untracked user material; stage only files within the requested scope.
- Keep this task's source material in `doc/` intact unless a change to it is requested or needed for the task. Do not silently promote samples to production code.
- Keep README status, paths, setup instructions, and architecture claims consistent with actual repository contents after changes.
- Follow surrounding Go and browser code conventions. Format changed Go code with `gofmt`; avoid adding frameworks or dependencies without a concrete need.
- Follow the repository's GNU AGPL v3 license in `LICENSE`.

## Validation

For documentation changes, check relative links, filenames, commands, and feature claims against the samples. No automated documentation checks are currently configured. Root Go checks are `go build ./...`, `go test ./...`, `go test -race ./...`, and `go vet ./...`; the historical Go sample is excluded with a build tag.

To compile the reference sample, copy the Go file as `main.go` and the HTML file as `index.html` into an isolated temporary directory, initialize a module there, and resolve the three dependencies listed in the README. Avoid creating incidental module files or binaries in this repository. Dependency resolution is currently unpinned; report the Go/dependency versions used if a build is performed.

Once application code and a module are introduced, establish reproducible dependency versions and meaningful tests, then run the applicable build, `go test ./...`, and `go vet ./...` from that module. For relay changes, consider frame parsing/serialization, bounded history, concurrent clients, reconnects, authentication, pane discovery, and resize/input routing. Integration checks need a real WezTerm GUI and PTY; state clearly when they were not run.

## Remote terminal access

The prototype grants terminal control, uses optional token authentication, accepts any WebSocket origin, serves HTTP without TLS, and defaults to all network interfaces. Use loopback plus a token for local experiments. Treat authentication, origin validation, Unix socket handling, and network exposure as implementation concerns when preparing a deployable application. Do not add real tokens, terminal transcripts, or personal shell configuration to commits.
