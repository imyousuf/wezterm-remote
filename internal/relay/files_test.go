package relay

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFileBrowserConfinementAndContent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	for path, content := range map[string]string{filepath.Join(root, "code.go"): "package main\n", filepath.Join(root, "page.html"): "<script>alert(1)</script>", filepath.Join(root, "binary"): "\x00\x01", filepath.Join(outside, "private"): "outside"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large"), make([]byte, textReadLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	s := newRelayServer("secret")
	s.fileRoots = []fileRoot{{"Fixture", root}}
	for _, tc := range []struct {
		path   string
		status int
		kind   string
	}{
		{"/api/files/list?root=0", 200, "application/json"},
		{"/api/files/read?root=0&path=code.go", 200, "text/plain; charset=utf-8"},
		{"/api/files/read?root=0&path=page.html", 200, "text/plain; charset=utf-8"},
		{"/api/files/read?root=0&path=../private", 400, ""},
		{"/api/files/read?root=0&path=escape/private", 404, ""},
		{"/api/files/read?root=0&path=binary", 415, ""},
		{"/api/files/read?root=0&path=large", 413, ""},
		{"/api/files/list?root=1", 400, ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			s.handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.kind != "" && w.Header().Get("Content-Type") != tc.kind {
				t.Fatal(w.Header())
			}
			if strings.Contains(tc.path, "/list?root=0") && strings.Contains(w.Body.String(), "escape") {
				t.Fatal("symlink listed")
			}
		})
	}
	r := httptest.NewRequest("GET", "/api/files/roots", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}

func TestFileDownload(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "private"), []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{
		"binary.bin": {0, 255, 1}, "page.html": []byte("<script>alert(1)</script>"),
		"image.svg": []byte("<svg onload='alert(1)'/>"), "quote\" café.txt": []byte("unicode filename"),
		"large.bin": bytes.Repeat([]byte{0, 255, 123, 42}, (fileReadLimit/4)+1),
	}
	for name, data := range payloads {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := newRelayServer("secret")
	s.fileRoots = []fileRoot{{"Fixture", root}}
	handler := s.handler()
	request := func(path string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/files/download?root=0&path="+url.QueryEscape(path), nil)
		if auth {
			r.Header.Set("Authorization", "Bearer secret")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for name, data := range payloads {
		t.Run(name, func(t *testing.T) {
			w := request(name, true)
			if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
				t.Fatalf("download status %d, bytes %d", w.Code, w.Body.Len())
			}
			kind, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
			if err != nil || kind != "attachment" || params["filename"] != name {
				t.Fatalf("disposition %q: %v", w.Header().Get("Content-Disposition"), err)
			}
			if w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal(w.Header())
			}
		})
	}
	for _, tc := range []struct {
		path   string
		auth   bool
		status int
	}{
		{"binary.bin", false, 401}, {"../private", true, 400}, {"escape/private", true, 404}, {".", true, 400}, {"pipe", true, 400}, {"missing", true, 404},
	} {
		if w := request(tc.path, tc.auth); w.Code != tc.status {
			t.Fatalf("%s auth=%v: %d, want %d", tc.path, tc.auth, w.Code, tc.status)
		}
	}
	r := httptest.NewRequest("GET", "/api/files/list?root=0", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var entries []fileEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Modified.IsZero() {
			t.Fatalf("missing modified time: %s", entry.Name)
		}
	}
}
