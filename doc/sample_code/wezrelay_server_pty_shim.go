//go:build ignore

package main

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

//go:embed index.html
var assets embed.FS

// Wire protocol opcodes between `wezrelay shim` and `wezrelay serve` over Unix socket
const (
	OpOutput byte = 1 // Shim -> Server: PTY stdout bytes
	OpInput  byte = 2 // Server -> Shim: Browser stdin bytes
	OpResize byte = 3 // Shim <-> Server: uint16 cols + uint16 rows
	OpMeta   byte = 4 // Shim -> Server: JSON metadata
)

type ShimMeta struct {
	ID        string `json:"id"`
	WezPaneID int    `json:"wez_pane_id"`
	PID       int    `json:"pid"`
	Shell     string `json:"shell"`
	Cwd       string `json:"cwd"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

type UnifiedPane struct {
	ID        string `json:"id"`          // "shim-<pid>" or "wez-<pane_id>"
	Mode      string `json:"mode"`        // "shim" (0ms PTY stream) or "cli" (wezterm cli fallback)
	WezPaneID int    `json:"wez_pane_id"` // Native WEZTERM_PANE id if available
	TabID     int    `json:"tab_id"`
	Title     string `json:"title"`
	Cwd       string `json:"cwd"`
	IsActive  bool   `json:"is_active"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type WezCLISize struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type WezCLIPane struct {
	WindowID int        `json:"window_id"`
	TabID    int        `json:"tab_id"`
	PaneID   int        `json:"pane_id"`
	Title    string     `json:"title"`
	Cwd      string     `json:"cwd"`
	IsActive bool       `json:"is_active"`
	Size     WezCLISize `json:"size"`
}

// RingBuffer stores the recent raw ANSI byte stream for each shim PTY so joining browsers render immediately
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	max  int
}

func NewRingBuffer(max int) *RingBuffer {
	return &RingBuffer{max: max, buf: make([]byte, 0, max)}
}

func (r *RingBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) >= r.max {
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return
	}
	if len(r.buf)+len(p) > r.max {
		drop := (len(r.buf) + len(p)) - r.max
		r.buf = append(r.buf[:0], r.buf[drop:]...)
	}
	r.buf = append(r.buf, p...)
}

func (r *RingBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

type ShimSession struct {
	Meta    ShimMeta
	Conn    net.Conn
	WriteMu sync.Mutex
	History *RingBuffer
	SubsMu  sync.Mutex
	Subs    map[chan []byte]struct{}
}

func (s *ShimSession) SendFrame(op byte, payload []byte) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	s.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return writeFrame(s.Conn, op, payload)
}

func (s *ShimSession) Broadcast(data []byte) {
	s.History.Write(data)
	s.SubsMu.Lock()
	defer s.SubsMu.Unlock()
	for ch := range s.Subs {
		cp := make([]byte, len(data))
		copy(cp, data)
		select {
		case ch <- cp:
		default:
			// Drop slow browser frame rather than blocking the local terminal
		}
	}
}

var (
	sessionsMu sync.RWMutex
	sessions   = make(map[string]*ShimSession)
	upgrader   = websocket.Upgrader{
		ReadBufferSize:  8192,
		WriteBufferSize: 8192,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
	authToken string
)

func writeFrame(w io.Writer, op byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = op
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := hdr[0]
	length := binary.BigEndian.Uint32(hdr[1:5])
	if length > 4*1024*1024 {
		return 0, nil, fmt.Errorf("frame too large: %d", length)
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return op, payload, nil
}

func defaultSockPath() string {
	return filepath.Join("/tmp", fmt.Sprintf("wezrelay-%d.sock", os.Getuid()))
}

// Auto-discovers the active WezTerm GUI socket even when run inside a systemd background service
func weztermCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("wezterm", args...)
	env := os.Environ()
	hasSock := false
	for _, e := range env {
		if strings.HasPrefix(e, "WEZTERM_UNIX_SOCKET=") {
			hasSock = true
			break
		}
	}
	if !hasSock {
		pattern := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "wezterm", "gui-sock-*")
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			// Pick newest socket
			sort.Slice(matches, func(i, j int) bool {
				fi, _ := os.Stat(matches[i])
				fj, _ := os.Stat(matches[j])
				if fi != nil && fj != nil {
					return fi.ModTime().After(fj.ModTime())
				}
				return matches[i] > matches[j]
			})
			env = append(env, "WEZTERM_UNIX_SOCKET="+matches[0])
		}
	}
	cmd.Env = env
	return cmd
}

// ============================================================================
// MODE 1: `wezrelay shim` (Runs inside each WezTerm pane as default_prog)
// ============================================================================
func runShim(sockPath string, shellOverride string) {
	shell := shellOverride
	if shell == "" {
		shell = os.Getenv("SHELL")
		if shell == "" || strings.Contains(shell, "wezrelay") {
			shell = "/bin/bash"
		}
	}

	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Fatalf("failed to start pty: %v", err)
	}
	defer ptmx.Close()

	// Put local WezTerm stdin into raw mode so all Ctrl keys, mouse clicks, and TUIs work natively
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	wezPaneID := -1
	if pStr := os.Getenv("WEZTERM_PANE"); pStr != "" {
		if v, err := strconv.Atoi(pStr); err == nil {
			wezPaneID = v
		}
	}

	var hubMu sync.Mutex
	var hubConn net.Conn

	sendToHub := func(op byte, payload []byte) {
		hubMu.Lock()
		c := hubConn
		hubMu.Unlock()
		if c != nil {
			_ = writeFrame(c, op, payload)
		}
	}

	syncSize := func() (uint16, uint16) {
		ws, err := pty.GetsizeFull(os.Stdin)
		if err == nil && ws.Cols > 0 && ws.Rows > 0 {
			_ = pty.Setsize(ptmx, ws)
			var b [4]byte
			binary.BigEndian.PutUint16(b[0:2], ws.Cols)
			binary.BigEndian.PutUint16(b[2:4], ws.Rows)
			sendToHub(OpResize, b[:])
			return ws.Cols, ws.Rows
		}
		return 80, 24
	}

	cols, rows := syncSize()

	// Forward local WezTerm window resize signals (SIGWINCH) to the PTY and browser
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			syncSize()
		}
	}()

	// Reconnecting background bridge to `wezrelay serve`
	go func() {
		shimID := fmt.Sprintf("shim-%d", os.Getpid())
		for {
			c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
			if err != nil {
				time.Sleep(1500 * time.Millisecond)
				continue
			}
			cwd, _ := os.Getwd()
			cCols, cRows := syncSize()
			meta := ShimMeta{
				ID:        shimID,
				WezPaneID: wezPaneID,
				PID:       os.Getpid(),
				Shell:     filepath.Base(shell),
				Cwd:       cwd,
				Cols:      cCols,
				Rows:      cRows,
			}
			metaBytes, _ := json.Marshal(meta)
			if err := writeFrame(c, OpMeta, metaBytes); err != nil {
				c.Close()
				time.Sleep(1500 * time.Millisecond)
				continue
			}

			hubMu.Lock()
			hubConn = c
			hubMu.Unlock()

			// Read remote inputs/resizes from the web server
			for {
				op, payload, err := readFrame(c)
				if err != nil {
					break
				}
				switch op {
				case OpInput:
					_, _ = ptmx.Write(payload)
				case OpResize:
					if len(payload) == 4 {
						rCols := binary.BigEndian.Uint16(payload[0:2])
						rRows := binary.BigEndian.Uint16(payload[2:4])
						if rCols > 0 && rRows > 0 {
							_ = pty.Setsize(ptmx, &pty.Winsize{Cols: rCols, Rows: rRows})
						}
					}
				}
			}

			hubMu.Lock()
			hubConn = nil
			hubMu.Unlock()
			c.Close()
			time.Sleep(1500 * time.Millisecond)
		}
	}()
	_ = cols
	_ = rows

	// Local WezTerm stdin -> PTY
	go func() {
		_, _ = io.Copy(ptmx, os.Stdin)
	}()

	// PTY stdout -> Local WezTerm stdout + Web Hub
	buf := make([]byte, 8192)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n])
			sendToHub(OpOutput, buf[:n])
		}
		if err != nil {
			break
		}
	}

	_ = cmd.Wait()
}

// ============================================================================
// MODE 2: `wezrelay serve` (Unix Socket Hub + Web/Tailscale Server)
// ============================================================================
func startUnixHub(sockPath string) {
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		log.Fatalf("failed to listen on unix socket %s: %v", sockPath, err)
	}
	_ = os.Chmod(sockPath, 0600)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				continue
			}
			go handleShimConn(conn)
		}
	}()
}

func handleShimConn(conn net.Conn) {
	defer conn.Close()
	op, payload, err := readFrame(conn)
	if err != nil || op != OpMeta {
		return
	}
	var meta ShimMeta
	if err := json.Unmarshal(payload, &meta); err != nil || meta.ID == "" {
		return
	}

	sess := &ShimSession{
		Meta:    meta,
		Conn:    conn,
		History: NewRingBuffer(64 * 1024),
		Subs:    make(map[chan []byte]struct{}),
	}

	sessionsMu.Lock()
	sessions[meta.ID] = sess
	sessionsMu.Unlock()
	log.Printf("[+] Registered native WezTerm pane %s (WezPane=%d, PID=%d)", meta.ID, meta.WezPaneID, meta.PID)

	defer func() {
		sessionsMu.Lock()
		delete(sessions, meta.ID)
		sessionsMu.Unlock()
		sess.SubsMu.Lock()
		for ch := range sess.Subs {
			close(ch)
			delete(sess.Subs, ch)
		}
		sess.SubsMu.Unlock()
		log.Printf("[-] Disconnected pane %s", meta.ID)
	}()

	for {
		op, data, err := readFrame(conn)
		if err != nil {
			return
		}
		switch op {
		case OpOutput:
			sess.Broadcast(data)
		case OpResize:
			if len(data) == 4 {
				sessionsMu.Lock()
				sess.Meta.Cols = binary.BigEndian.Uint16(data[0:2])
				sess.Meta.Rows = binary.BigEndian.Uint16(data[2:4])
				sessionsMu.Unlock()
			}
		}
	}
}

func checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if authToken == "" {
		return true
	}
	qTok := r.URL.Query().Get("token")
	hTok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(qTok), []byte(authToken)) == 1 ||
		subtle.ConstantTimeCompare([]byte(hTok), []byte(authToken)) == 1 {
		return true
	}
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return false
}

// Merges live `wezrelay shim` sessions with `wezterm cli list` metadata
func handleListPanes(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(w, r) {
		return
	}

	var cliPanes []WezCLIPane
	if out, err := weztermCommand("cli", "list", "--format", "json").Output(); err == nil {
		_ = json.Unmarshal(out, &cliPanes)
	}

	cliByPaneID := make(map[int]WezCLIPane)
	for _, cp := range cliPanes {
		cliByPaneID[cp.PaneID] = cp
	}

	var unified []UnifiedPane
	matchedWezPanes := make(map[int]bool)

	sessionsMu.RLock()
	for _, s := range sessions {
		up := UnifiedPane{
			ID:        s.Meta.ID,
			Mode:      "shim",
			WezPaneID: s.Meta.WezPaneID,
			TabID:     0,
			Title:     s.Meta.Shell,
			Cwd:       s.Meta.Cwd,
			Cols:      int(s.Meta.Cols),
			Rows:      int(s.Meta.Rows),
		}
		if cp, ok := cliByPaneID[s.Meta.WezPaneID]; ok {
			matchedWezPanes[cp.PaneID] = true
			up.TabID = cp.TabID
			if cp.Title != "" {
				up.Title = cp.Title
			}
			if cp.Cwd != "" {
				up.Cwd = cp.Cwd
			}
			up.IsActive = cp.IsActive
			if cp.Size.Cols > 0 {
				up.Cols = cp.Size.Cols
				up.Rows = cp.Size.Rows
			}
		}
		unified = append(unified, up)
	}
	sessionsMu.RUnlock()

	// Append any pre-existing WezTerm panes that aren't running the shim (uses CLI fallback)
	for _, cp := range cliPanes {
		if matchedWezPanes[cp.PaneID] {
			continue
		}
		unified = append(unified, UnifiedPane{
			ID:        fmt.Sprintf("wez-%d", cp.PaneID),
			Mode:      "cli",
			WezPaneID: cp.PaneID,
			TabID:     cp.TabID,
			Title:     cp.Title,
			Cwd:       cp.Cwd,
			IsActive:  cp.IsActive,
			Cols:      cp.Size.Cols,
			Rows:      cp.Size.Rows,
		})
	}

	sort.Slice(unified, func(i, j int) bool {
		if unified[i].TabID != unified[j].TabID {
			return unified[i].TabID < unified[j].TabID
		}
		if unified[i].WezPaneID != unified[j].WezPaneID {
			return unified[i].WezPaneID < unified[j].WezPaneID
		}
		return unified[i].ID < unified[j].ID
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(unified)
}

func handleSplitPane(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(w, r) {
		return
	}
	paneID := r.URL.Query().Get("wez_pane_id")
	dir := r.URL.Query().Get("dir")

	args := []string{"cli", "split-pane"}
	if paneID != "" && paneID != "-1" {
		args = append(args, "--pane-id", paneID)
	}
	switch dir {
	case "bottom":
		args = append(args, "--bottom")
	default:
		args = append(args, "--right")
	}

	out, err := weztermCommand(args...).CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("%v: %s", err, string(out)), http.StatusInternalServerError)
		return
	}
	w.Write(out)
}

func handleSpawnTab(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(w, r) {
		return
	}
	out, err := weztermCommand("cli", "spawn").CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("%v: %s", err, string(out)), http.StatusInternalServerError)
		return
	}
	w.Write(out)
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	sendWS := func(msgType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
		return conn.WriteMessage(msgType, data)
	}

	// Keepalive Ping every 25s for Tailscale/Mobile NAT
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := sendWS(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Branch 1: Zero-Latency Shim PTY Stream
	if strings.HasPrefix(id, "shim-") {
		sessionsMu.RLock()
		sess, ok := sessions[id]
		sessionsMu.RUnlock()
		if !ok {
			_ = sendWS(websocket.BinaryMessage, []byte("\r\n\x1b[31m[Shim session not found]\x1b[0m\r\n"))
			return
		}

		// Replay 64KB ring buffer snapshot so TUI renders immediately
		if snap := sess.History.Snapshot(); len(snap) > 0 {
			_ = sendWS(websocket.BinaryMessage, snap)
		}

		subCh := make(chan []byte, 128)
		sess.SubsMu.Lock()
		sess.Subs[subCh] = struct{}{}
		sess.SubsMu.Unlock()

		defer func() {
			sess.SubsMu.Lock()
			delete(sess.Subs, subCh)
			sess.SubsMu.Unlock()
		}()

		go func() {
			for {
				select {
				case <-done:
					return
				case chunk, ok := <-subCh:
					if !ok {
						return
					}
					if err := sendWS(websocket.BinaryMessage, chunk); err != nil {
						return
					}
				}
			}
		}()

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType == websocket.BinaryMessage && len(data) > 0 {
				_ = sess.SendFrame(OpInput, data)
			} else if msgType == websocket.TextMessage {
				var msg struct {
					Type string `json:"type"`
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
					var b [4]byte
					binary.BigEndian.PutUint16(b[0:2], msg.Cols)
					binary.BigEndian.PutUint16(b[2:4], msg.Rows)
					_ = sess.SendFrame(OpResize, b[:])
				}
			}
		}
	}

	// Branch 2: `wezterm cli` Fallback Stream (for panes opened without the shim)
	paneIDStr := strings.TrimPrefix(id, "wez-")
	if _, err := strconv.Atoi(paneIDStr); err != nil {
		return
	}

	go func() {
		ticker := time.NewTicker(45 * time.Millisecond)
		defer ticker.Stop()
		var lastFrame []byte
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				out, err := weztermCommand("cli", "get-text", "--pane-id", paneIDStr, "--escapes").Output()
				if err != nil {
					continue
				}
				if !bytes.Equal(out, lastFrame) {
					lastFrame = out
					// Convert bare \n to \r\n for xterm.js and wrap in DECSET 2026 synchronized output
					normalized := bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
					normalized = bytes.ReplaceAll(normalized, []byte("\n"), []byte("\r\n"))
					frame := fmt.Sprintf("\x1b[?2026h\x1b[H\x1b[2J%s\x1b[?2026l", string(normalized))
					if err := sendWS(websocket.BinaryMessage, []byte(frame)); err != nil {
						return
					}
				}
			}
		}
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType == websocket.BinaryMessage && len(data) > 0 {
			cmd := weztermCommand("cli", "send-text", "--pane-id", paneIDStr, "--no-paste")
			cmd.Stdin = bytes.NewReader(data)
			_ = cmd.Run()
		}
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "shim" {
		fs := flag.NewFlagSet("shim", flag.ExitOnError)
		sock := fs.String("sock", defaultSockPath(), "Unix socket path")
		shell := fs.String("shell", "", "Shell binary override")
		_ = fs.Parse(os.Args[2:])
		runShim(*sock, *shell)
		return
	}

	// Default / `serve` subcommand
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0:8080", "HTTP listen address")
	sock := fs.String("sock", defaultSockPath(), "Unix socket path")
	token := fs.String("token", os.Getenv("WEZRELAY_TOKEN"), "Optional bearer/query token for LAN auth")
	_ = fs.Parse(args)

	authToken = *token
	startUnixHub(*sock)

	http.Handle("/", http.FileServer(http.FS(assets)))
	http.HandleFunc("/api/panes", handleListPanes)
	http.HandleFunc("/api/split", handleSplitPane)
	http.HandleFunc("/api/spawn", handleSpawnTab)
	http.HandleFunc("/ws", handleWS)

	log.Printf("WezRelay listening on http://%s (Unix hub: %s)", *addr, *sock)
	log.Fatal(http.ListenAndServe(*addr, nil))
}