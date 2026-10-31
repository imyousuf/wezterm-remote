package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const fileReadLimit = 16 << 20
const textReadLimit = 2 << 20

type fileRoot struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type fileEntry struct {
	Name      string    `json:"name"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
}

func defaultFileRoots() []fileRoot {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	var roots []fileRoot
	for _, candidate := range []fileRoot{{"Home", home}, {"Server directory", cwd}, {"Temporary files", os.TempDir()}} {
		if candidate.Path == "" {
			continue
		}
		duplicate := false
		for _, root := range roots {
			if root.Path == candidate.Path {
				duplicate = true
			}
		}
		if !duplicate {
			roots = append(roots, candidate)
		}
	}
	return roots
}

func (s *relayServer) files(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/files/roots" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.fileRoots)
		return
	}
	id, err := strconv.Atoi(r.URL.Query().Get("root"))
	if err != nil || id < 0 || id >= len(s.fileRoots) {
		http.Error(w, "Invalid file root", 400)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "."
	}
	if !filepath.IsLocal(path) {
		http.Error(w, "Path must stay within the selected root", 400)
		return
	}
	root, err := os.OpenRoot(s.fileRoots[id].Path)
	if err != nil {
		http.Error(w, "File root unavailable", 404)
		return
	}
	defer root.Close()
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		http.Error(w, "File unavailable or outside root", 404)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "File unavailable", 404)
		return
	}
	if r.URL.Path == "/api/files/list" {
		if !info.IsDir() {
			http.Error(w, "Choose a directory", 400)
			return
		}
		// Bound memory even for very large generated artifact directories.
		entries, err := f.ReadDir(2001)
		if err != nil && err != io.EOF {
			http.Error(w, "Cannot read directory", 403)
			return
		}
		if len(entries) > 2000 {
			http.Error(w, "Directory exceeds 2000 entries; enter a more specific path", 413)
			return
		}
		result := make([]fileEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			item, err := entry.Info()
			if err != nil || (!item.IsDir() && !item.Mode().IsRegular()) {
				continue
			}
			result = append(result, fileEntry{entry.Name(), item.IsDir(), item.Size(), item.ModTime()})
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].Directory != result[j].Directory {
				return result[i].Directory
			}
			return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "Only regular files can be viewed", 400)
		return
	}
	if r.URL.Path == "/api/files/download" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
		return
	}
	if info.Size() > fileReadLimit {
		http.Error(w, "File exceeds 16 MiB preview limit", 413)
		return
	}
	content, err := io.ReadAll(io.LimitReader(f, fileReadLimit+1))
	if err != nil {
		http.Error(w, "Cannot read file", 403)
		return
	}
	if len(content) > fileReadLimit {
		http.Error(w, "File exceeds preview limit", 413)
		return
	}
	kind := http.DetectContentType(content)
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		if len(content) > textReadLimit {
			http.Error(w, "Text exceeds 2 MiB preview limit", 413)
			return
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			http.Error(w, "Preview supports UTF-8 text and PNG, JPEG, GIF or WebP images", 415)
			return
		}
		kind = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", "inline")
	w.Write(content)
}
