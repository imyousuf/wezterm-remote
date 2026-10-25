package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testSession() *shimSession { return &shimSession{subs: make(map[*subscription]struct{})} }
func TestHistorySubscriptionOrderingAndSlowClient(t *testing.T) {
	s := testSession()
	s.broadcast(bytes.Repeat([]byte("a"), historyLimit+20))
	snap, sub := s.subscribe()
	if len(snap) != historyLimit {
		t.Fatal(len(snap))
	}
	s.broadcast([]byte("next"))
	if got := <-sub.data; string(got) != "next" {
		t.Fatal(string(got))
	}
	for i := 0; i < 129; i++ {
		s.broadcast([]byte("x"))
	}
	select {
	case <-sub.done:
	default:
		t.Fatal("slow client was not disconnected")
	}
	s.unsubscribe(sub)
}
func TestUnixSocketRefusesExistingPaths(t *testing.T) {
	dir := socketTestDir(t)
	path := filepath.Join(dir, "hub.sock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(path); err == nil {
		t.Fatal("replaced file")
	}
	p, _ := os.ReadFile(path)
	if string(p) != "keep" {
		t.Fatal("file modified")
	}
	os.Remove(path)
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	if _, err := listenUnix(path); err == nil {
		t.Fatal("replaced live socket")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(path, link)
	if _, err := listenUnix(link); err == nil {
		t.Fatal("followed symlink")
	}
}
func TestHTTPAuthOriginAndMethods(t *testing.T) {
	s := newRelayServer("secret")
	calls := 0
	s.cli = func(context.Context, []byte, ...string) ([]byte, error) { calls++; return []byte("[]"), nil }
	h := s.handler()
	for _, tc := range []struct {
		method, path, token, origin string
		status                      int
	}{{"GET", "/api/panes", "", "", 401}, {"GET", "/api/panes", "secret", "", 200}, {"GET", "/api/spawn", "secret", "", 405}, {"POST", "/api/spawn", "secret", "http://evil.test", 403}, {"POST", "/api/spawn", "secret", "http://example.com", 200}, {"POST", "/api/split?wez_pane_id=-1&dir=right", "secret", "", 400}, {"GET", "/ws?id=wez-1", "secret", "http://evil.test", 403}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestHTTPWithoutTokenPreservesOriginChecks(t *testing.T) {
	s := newRelayServer("")
	s.cli = func(context.Context, []byte, ...string) ([]byte, error) { return []byte("[]"), nil }
	for _, tc := range []struct {
		method, path, origin string
		status               int
	}{
		{"GET", "/api/panes", "", 200},
		{"POST", "/api/spawn", "http://desktop.tailnet:12620", 200},
		{"POST", "/api/spawn", "http://evil.test", 403},
		{"GET", "/ws?id=wez-1", "http://evil.test", 403},
	} {
		r := httptest.NewRequest(tc.method, "http://desktop.tailnet:12620"+tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("X-WezRelay-Auth") != "disabled" {
			t.Fatalf("%s %s: status=%d auth=%q", tc.method, tc.path, w.Code, w.Header().Get("X-WezRelay-Auth"))
		}
	}
}

func TestShimWebSocketReplayInputAndResize(t *testing.T) {
	s := newRelayServer("secret")
	a, b := net.Pipe()
	defer b.Close()
	go s.handleShim(a)
	meta := ShimMeta{ID: "shim-test", WezPaneID: 4, Cols: 80, Rows: 24}
	p, _ := json.Marshal(meta)
	if err := writeFrame(b, OpMeta, p); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(b, OpOutput, []byte("replay")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.RLock()
		sess := s.sessions[meta.ID]
		s.mu.RUnlock()
		if sess != nil {
			sess.mu.Lock()
			ready := len(sess.history) > 0
			sess.mu.Unlock()
			if ready {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("registration timeout")
		}
		time.Sleep(time.Millisecond)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?id=shim-test&token=secret", http.Header{"Origin": []string{srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(time.Second))
	kind, p, err := ws.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || string(p) != "replay" {
		t.Fatalf("%d %q %v", kind, p, err)
	}
	if err = ws.WriteMessage(websocket.BinaryMessage, []byte("input")); err != nil {
		t.Fatal(err)
	}
	b.SetReadDeadline(time.Now().Add(time.Second))
	op, p, err := readFrame(b)
	if err != nil || op != OpInput || string(p) != "input" {
		t.Fatalf("%d %q %v", op, p, err)
	}
	ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":120,"rows":40}`))
	op, p, err = readFrame(b)
	if err != nil || op != OpResize || !bytes.Equal(p, []byte{0, 120, 0, 40}) {
		t.Fatalf("%d %v %v", op, p, err)
	}
}
func TestCLIPaneDiscoveryMerge(t *testing.T) {
	s := newRelayServer("secret")
	s.cli = func(context.Context, []byte, ...string) ([]byte, error) {
		return []byte(`[{"pane_id":1,"tab_id":2,"workspace":"agents","title":"one","size":{"cols":90,"rows":30}},{"pane_id":2,"tab_id":2,"workspace":"other","title":"two","size":{"cols":80,"rows":24}}]`), nil
	}
	sess := testSession()
	sess.meta = ShimMeta{ID: "shim-1", WezPaneID: 1, Cols: 100, Rows: 40}
	s.sessions[sess.meta.ID] = sess
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/panes?token=secret", nil)
	s.handler().ServeHTTP(w, r)
	var panes []UnifiedPane
	if err := json.NewDecoder(w.Body).Decode(&panes); err != nil {
		t.Fatal(err)
	}
	if len(panes) != 2 || panes[0].Mode != "shim" || panes[0].Cols != 100 || panes[0].DesktopCols != 90 || panes[0].DesktopRows != 30 || panes[1].ID != "wez-2" || panes[0].Workspace != "agents" || panes[1].Workspace != "other" {
		t.Fatalf("%+v", panes)
	}
}

func TestCLIFallbackSnapshotAndInput(t *testing.T) {
	s := newRelayServer("secret")
	input := make(chan []byte, 1)
	s.cli = func(_ context.Context, p []byte, args ...string) ([]byte, error) {
		switch args[1] {
		case "get-text":
			return []byte("hello\nworld"), nil
		case "send-text":
			if strings.Join(args, " ") != "cli send-text --pane-id 7 --no-paste" {
				t.Errorf("unexpected args: %v", args)
			}
			input <- bytes.Clone(p)
		}
		return nil, nil
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?id=wez-7&token=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, p, err := ws.ReadMessage()
	if err != nil || !bytes.Contains(p, []byte("hello\r\nworld")) || !bytes.HasPrefix(p, []byte("\x1b[?2026h\x1b[H\x1b[2J")) {
		t.Fatalf("%q %v", p, err)
	}
	if err = ws.WriteMessage(websocket.BinaryMessage, []byte("test-input")); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-input:
		if string(p) != "test-input" {
			t.Fatal(string(p))
		}
	case <-time.After(time.Second):
		t.Fatal("CLI input not routed")
	}
}

func TestOriginBehindTLSProxy(t *testing.T) {
	for _, tc := range []struct {
		origin  string
		allowed bool
	}{
		{"https://relay.example", true}, {"http://relay.example", true},
		{"https://evil.example", false}, {"null", false}, {"https://relay.example/path", false},
		{"https://relay.example:8443", false}, {"file://relay.example", false},
	} {
		r := httptest.NewRequest("GET", "http://relay.example/ws", nil)
		r.Header.Set("Origin", tc.origin)
		if got := sameOrigin(r); got != tc.allowed {
			t.Errorf("origin %q allowed=%v", tc.origin, got)
		}
	}
}

func TestSpawnInSelectedPaneWindow(t *testing.T) {
	s := newRelayServer("")
	var args []string
	s.cli = func(_ context.Context, _ []byte, command ...string) ([]byte, error) {
		args = command
		return []byte("3"), nil
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/spawn?wez_pane_id=7", nil))
	if w.Code != 200 || strings.Join(args, " ") != "cli spawn --pane-id 7" {
		t.Fatalf("status %d args %v", w.Code, args)
	}
	w = httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/spawn?wez_pane_id=bad", nil))
	if w.Code != 400 {
		t.Fatalf("invalid pane status %d", w.Code)
	}
}

// Moving the embed package must preserve public URLs, including vendored assets
// needed when the browser has no internet connection.
func TestEmbeddedBrowserAssets(t *testing.T) {
	handler := newRelayServer("").handler()
	for _, path := range []string{"/", "/app.js", "/style.css", "/vendor/xterm-xterm.js", "/vendor/xterm-xterm.css", "/vendor/addon-fit-addon-fit.js"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("embedded asset %s: status %d, body length %d", path, response.Code, response.Body.Len())
			}
		})
	}
}
