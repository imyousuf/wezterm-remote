package relay

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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

	"github.com/gorilla/websocket"

	"github.com/imyousuf/wezterm-remote/web"
)

const historyLimit = 64 * 1024

type subscription struct {
	data chan []byte
	done chan struct{}
}
type shimSession struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	meta    ShimMeta
	conn    net.Conn
	history []byte
	subs    map[*subscription]struct{}
	closed  bool
}

func (s *shimSession) subscribe() ([]byte, *subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := &subscription{make(chan []byte, 128), make(chan struct{})}
	if s.closed {
		close(sub.done)
	} else {
		s.subs[sub] = struct{}{}
	}
	return bytes.Clone(s.history), sub
}
func (s *shimSession) unsubscribe(sub *subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; ok {
		delete(s.subs, sub)
		close(sub.done)
	}
}
func (s *shimSession) broadcast(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.history = append(s.history, p...)
	if len(s.history) > historyLimit {
		s.history = bytes.Clone(s.history[len(s.history)-historyLimit:])
	}
	p = bytes.Clone(p)
	for sub := range s.subs {
		select {
		case sub.data <- p:
		default:
			delete(s.subs, sub)
			close(sub.done)
		}
	}
}
func (s *shimSession) close() {
	s.conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		for sub := range s.subs {
			close(sub.done)
			delete(s.subs, sub)
		}
	}
}
func (s *shimSession) send(op byte, p []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return writeFrame(s.conn, op, p)
}

type relayServer struct {
	mu       sync.RWMutex
	sessions map[string]*shimSession
	token    string
	cli      func(context.Context, []byte, ...string) ([]byte, error)
}

func newRelayServer(token string) *relayServer {
	return &relayServer{sessions: make(map[string]*shimSession), token: token, cli: runWezterm}
}
func (s *relayServer) handleShim(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	op, p, err := readFrame(conn)
	if err != nil || op != OpMeta {
		return
	}
	var meta ShimMeta
	if json.Unmarshal(p, &meta) != nil || !strings.HasPrefix(meta.ID, "shim-") || len(meta.ID) > 128 || meta.Cols == 0 || meta.Rows == 0 {
		return
	}
	conn.SetReadDeadline(time.Time{})
	sess := &shimSession{meta: meta, conn: conn, subs: make(map[*subscription]struct{})}
	s.mu.Lock()
	old := s.sessions[meta.ID]
	s.sessions[meta.ID] = sess
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	defer func() {
		s.mu.Lock()
		if s.sessions[meta.ID] == sess {
			delete(s.sessions, meta.ID)
		}
		s.mu.Unlock()
		sess.close()
	}()
	for {
		op, p, err = readFrame(conn)
		if err != nil {
			return
		}
		switch op {
		case OpOutput:
			sess.broadcast(p)
		case OpResize:
			if len(p) != 4 {
				return
			}
			cols, rows := binary.BigEndian.Uint16(p[:2]), binary.BigEndian.Uint16(p[2:])
			if cols == 0 || rows == 0 {
				return
			}
			sess.mu.Lock()
			sess.meta.Cols = cols
			sess.meta.Rows = rows
			sess.mu.Unlock()
		default:
			return
		}
	}
}

// Refuse every preexisting path, including stale sockets: operators can inspect
// and remove a stale socket explicitly without risking another running server.
func listenUnix(path string) (net.Listener, error) {
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("socket path already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
func runServer(addr, token, sockPath string) error {
	ln, err := listenUnix(sockPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	s := newRelayServer(token)
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, sess := range s.sessions {
			sess.close()
		}
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handleShim(conn)
		}
	}()
	httpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer httpLn.Close()
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(httpLn) }()
	log.Printf("WezRelay listening on http://%s", httpLn.Addr())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	// Match the authority without trusting forwarded headers. TLS may terminate
	// at a reverse proxy (for example tailscale serve) before reaching Go.
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host) && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}
func (s *relayServer) authorized(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		w.Header().Set("X-WezRelay-Auth", "disabled")
		return true
	}
	w.Header().Set("X-WezRelay-Auth", "required")
	tok := r.URL.Query().Get("token")
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimPrefix(h, "Bearer ")
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "Unauthorized", 401)
		return false
	}
	return true
}
func (s *relayServer) handler() http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"/api/panes", "/api/split", "/api/spawn", "/ws"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if !s.authorized(w, r) {
				return
			}
			method := "GET"
			if r.URL.Path == "/api/split" || r.URL.Path == "/api/spawn" {
				method = "POST"
			}
			if r.Method != method {
				w.Header().Set("Allow", method)
				http.Error(w, "Method not allowed", 405)
				return
			}
			if (method == "POST" || r.URL.Path == "/ws") && !sameOrigin(r) {
				http.Error(w, "Origin rejected", 403)
				return
			}
			switch r.URL.Path {
			case "/api/panes":
				s.listPanes(w, r)
			case "/api/split", "/api/spawn":
				s.action(w, r)
			case "/ws":
				s.websocket(w, r)
			}
		})
	}
	mux.Handle("/", http.FileServer(http.FS(web.Assets)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

type UnifiedPane struct {
	ID          string `json:"id"`
	Mode        string `json:"mode"`
	WezPaneID   int    `json:"wez_pane_id"`
	TabID       int    `json:"tab_id"`
	Workspace   string `json:"workspace"`
	Title       string `json:"title"`
	Cwd         string `json:"cwd"`
	IsActive    bool   `json:"is_active"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	DesktopCols int    `json:"desktop_cols"`
	DesktopRows int    `json:"desktop_rows"`
}
type WezCLIPane struct {
	PaneID    int    `json:"pane_id"`
	TabID     int    `json:"tab_id"`
	Workspace string `json:"workspace"`
	Title     string `json:"title"`
	Cwd       string `json:"cwd"`
	IsActive  bool   `json:"is_active"`
	Size      struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	} `json:"size"`
}

func (s *relayServer) command(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.cli(ctx, input, args...)
}
func (s *relayServer) listPanes(w http.ResponseWriter, r *http.Request) {
	out, cliErr := s.command(r.Context(), nil, "cli", "list", "--format", "json")
	var panes []WezCLIPane
	if cliErr == nil {
		cliErr = json.Unmarshal(out, &panes)
	}
	if cliErr != nil {
		w.Header().Set("X-WezRelay-Discovery", "unavailable")
	}
	byID := make(map[int]WezCLIPane)
	for _, p := range panes {
		byID[p.PaneID] = p
	}
	result := make([]UnifiedPane, 0)
	s.mu.RLock()
	for _, sess := range s.sessions {
		sess.mu.Lock()
		m := sess.meta
		sess.mu.Unlock()
		p := UnifiedPane{ID: m.ID, Mode: "shim", WezPaneID: m.WezPaneID, Title: m.Shell, Cwd: m.Cwd, Cols: int(m.Cols), Rows: int(m.Rows), DesktopCols: int(m.Cols), DesktopRows: int(m.Rows)}
		if cli, ok := byID[m.WezPaneID]; ok {
			p.TabID = cli.TabID
			p.Workspace = cli.Workspace
			p.Title = cli.Title
			p.Cwd = cli.Cwd
			p.IsActive = cli.IsActive
			if cli.Size.Cols > 0 && cli.Size.Rows > 0 {
				p.DesktopCols = cli.Size.Cols
				p.DesktopRows = cli.Size.Rows
			}
			delete(byID, m.WezPaneID)
		}
		result = append(result, p)
	}
	s.mu.RUnlock()
	for _, p := range byID {
		result = append(result, UnifiedPane{ID: fmt.Sprintf("wez-%d", p.PaneID), Mode: "cli", WezPaneID: p.PaneID, TabID: p.TabID, Workspace: p.Workspace, Title: p.Title, Cwd: p.Cwd, IsActive: p.IsActive, Cols: p.Size.Cols, Rows: p.Size.Rows, DesktopCols: p.Size.Cols, DesktopRows: p.Size.Rows})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TabID != result[j].TabID {
			return result[i].TabID < result[j].TabID
		}
		return result[i].ID < result[j].ID
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
func paneNumber(s string) (string, bool) {
	n, err := strconv.Atoi(s)
	return strconv.Itoa(n), err == nil && n >= 0
}
func (s *relayServer) action(w http.ResponseWriter, r *http.Request) {
	args := []string{"cli", "spawn"}
	if r.URL.Path == "/api/spawn" && r.URL.Query().Has("wez_pane_id") {
		id, ok := paneNumber(r.URL.Query().Get("wez_pane_id"))
		if !ok {
			http.Error(w, "Invalid pane ID", 400)
			return
		}
		args = append(args, "--pane-id", id)
	}
	if r.URL.Path == "/api/split" {
		id, ok := paneNumber(r.URL.Query().Get("wez_pane_id"))
		if !ok {
			http.Error(w, "Invalid pane ID", 400)
			return
		}
		dir := r.URL.Query().Get("dir")
		if dir != "right" && dir != "bottom" {
			http.Error(w, "Invalid split direction", 400)
			return
		}
		args = []string{"cli", "split-pane", "--pane-id", id, "--" + dir}
	}
	out, err := s.command(r.Context(), nil, args...)
	if err != nil {
		http.Error(w, "WezTerm command failed", 502)
		return
	}
	w.Write(out)
}
func (s *relayServer) websocket(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.mu.RLock()
	sess := s.sessions[id]
	s.mu.RUnlock()
	paneID, cli := paneNumber(strings.TrimPrefix(id, "wez-"))
	cli = cli && strings.HasPrefix(id, "wez-")
	if sess == nil && !cli {
		http.Error(w, "Pane not found", 404)
		return
	}
	up := websocket.Upgrader{CheckOrigin: sameOrigin}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(64 * 1024)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var mu sync.Mutex
	send := func(kind int, p []byte) error {
		mu.Lock()
		defer mu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteMessage(kind, p)
	}
	var snap []byte
	var sub *subscription
	if sess != nil {
		snap, sub = sess.subscribe()
		defer sess.unsubscribe(sub)
	}
	go func() {
		defer conn.Close()
		if len(snap) > 0 {
			if send(websocket.BinaryMessage, snap) != nil {
				return
			}
		}
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		poll := time.NewTicker(250 * time.Millisecond)
		defer poll.Stop()
		var previous []byte
		for {
			var data <-chan []byte
			var stopped <-chan struct{}
			if sub != nil {
				data = sub.data
				stopped = sub.done
			}
			select {
			case <-ctx.Done():
				return
			case <-stopped:
				return
			case p := <-data:
				if send(websocket.BinaryMessage, p) != nil {
					return
				}
			case <-ping.C:
				if send(websocket.PingMessage, nil) != nil {
					return
				}
			case <-poll.C:
				if !cli {
					continue
				}
				out, err := s.command(ctx, nil, "cli", "get-text", "--pane-id", paneID, "--escapes")
				if err != nil {
					return
				}
				if !bytes.Equal(previous, out) {
					previous = bytes.Clone(out)
					out = bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
					out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
					frame := append([]byte("\x1b[?2026h\x1b[H\x1b[2J"), out...)
					frame = append(frame, []byte("\x1b[?2026l")...)
					if send(websocket.BinaryMessage, frame) != nil {
						return
					}
				}
			}
		}
	}()
	for {
		kind, p, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if kind == websocket.BinaryMessage {
			if len(p) == 0 {
				continue
			}
			if sess != nil {
				err = sess.send(OpInput, p)
			} else {
				_, err = s.command(ctx, p, "cli", "send-text", "--pane-id", paneID, "--no-paste")
			}
			if err != nil {
				return
			}
		} else if kind == websocket.TextMessage {
			var msg struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(p, &msg) != nil || msg.Type != "resize" || msg.Cols == 0 || msg.Rows == 0 {
				return
			}
			if sess != nil {
				var size [4]byte
				binary.BigEndian.PutUint16(size[:2], msg.Cols)
				binary.BigEndian.PutUint16(size[2:], msg.Rows)
				if sess.send(OpResize, size[:]) != nil {
					return
				}
			}
		}
	}
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxFrameSize {
		return 0, errors.New("WezTerm output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func runWezterm(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "wezterm", args...)
	cmd.WaitDelay = time.Second
	cmd.Env = os.Environ()
	if os.Getenv("WEZTERM_UNIX_SOCKET") == "" {
		filtered := cmd.Env[:0]
		for _, entry := range cmd.Env {
			if !strings.HasPrefix(entry, "WEZTERM_UNIX_SOCKET=") {
				filtered = append(filtered, entry)
			}
		}
		cmd.Env = filtered
		matches, _ := filepath.Glob(filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "wezterm", "gui-sock-*"))
		sort.Slice(matches, func(i, j int) bool {
			a, ae := os.Stat(matches[i])
			b, be := os.Stat(matches[j])
			if ae != nil || be != nil {
				return matches[i] > matches[j]
			}
			return a.ModTime().After(b.ModTime())
		})
		if len(matches) > 0 {
			cmd.Env = append(cmd.Env, "WEZTERM_UNIX_SOCKET="+matches[0])
		}
	}
	cmd.Stdin = bytes.NewReader(input)
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return out.Bytes(), err
}
