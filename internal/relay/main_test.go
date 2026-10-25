package relay

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestServeAllowsNoToken(t *testing.T) {
	t.Setenv("WEZRELAY_TOKEN", "")
	if err := Run([]string{"serve", "-sock", filepath.Join(socketTestDir(t), "missing", "relay.sock")}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected socket creation error after accepting empty token, got %v", err)
	}
}

// Launch the real shim in a subprocess and drive it over both its outer PTY
// and Unix protocol. This checks shell lifecycle, raw local input and resizing.
func TestShimPTY(t *testing.T) {
	if os.Getenv("WEZRELAY_SHIM_TEST") == "1" {
		if err := runShim(os.Getenv("WEZRELAY_TEST_SOCKET"), "/bin/sh"); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	path := filepath.Join(socketTestDir(t), "relay.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestShimPTY$")
	child.Env = append(os.Environ(), "WEZRELAY_SHIM_TEST=1", "WEZRELAY_TEST_SOCKET="+path, "WEZTERM_PANE=77")
	terminal, err := pty.StartWithSize(child, &pty.Winsize{Cols: 100, Rows: 35})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer func() { child.Process.Kill(); child.Wait() }()
	go io.Copy(io.Discard, terminal)
	ln.(*net.UnixListener).SetDeadline(time.Now().Add(8 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	op, _, err := readFrame(conn)
	if err != nil || op != OpMeta {
		t.Fatalf("metadata: %d %v", op, err)
	}
	if err = writeFrame(conn, OpInput, []byte("printf 'REMOTE_%s\\n' OK\n")); err != nil {
		t.Fatal(err)
	}
	awaitOutput := func(want string) {
		t.Helper()
		var output string
		for !contains(output, want) {
			op, data, err := readFrame(conn)
			if err != nil {
				t.Fatalf("await %q: %v (%q)", want, err, output)
			}
			if op == OpOutput {
				output += string(data)
			}
		}
	}
	awaitOutput("REMOTE_OK")
	terminal.Write([]byte("printf 'LOCAL_%s\\n' OK\n"))
	awaitOutput("LOCAL_OK")
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data[:2], 91)
	binary.BigEndian.PutUint16(data[2:], 27)
	writeFrame(conn, OpResize, data)
	writeFrame(conn, OpInput, []byte("stty size\n"))
	awaitOutput("27 91")
	writeFrame(conn, OpInput, []byte("exit\n"))
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shell did not exit")
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Keep Unix socket names below macOS's sockaddr_un limit, including when its
// default temporary directory already has a long /var/folders prefix.
func socketTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
