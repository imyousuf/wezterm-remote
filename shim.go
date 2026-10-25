package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// shimBridge decouples the local PTY from network latency. A saturated bridge
// disconnects and reconnects rather than dropping bytes in a live ANSI stream.
type shimBridge struct {
	mu   sync.Mutex
	conn net.Conn
	out  chan []byte
}

func (b *shimBridge) send(op byte, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return
	}
	frame := make([]byte, 1+len(data))
	frame[0] = op
	copy(frame[1:], data)
	select {
	case b.out <- frame:
	default:
		b.conn.Close()
		b.conn = nil
	}
}
func runShim(sockPath, shell string) error {
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" || strings.Contains(filepath.Base(shell), "wezrelay") {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	size := &pty.Winsize{Cols: 80, Rows: 24}
	if ws, err := pty.GetsizeFull(os.Stdin); err == nil && ws.Cols > 0 && ws.Rows > 0 {
		size = ws
	}
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return fmt.Errorf("start shell: %w", err)
	}
	defer ptmx.Close()
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			return err
		}
		defer term.Restore(fd, state)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge := &shimBridge{}
	defer func() {
		bridge.mu.Lock()
		if bridge.conn != nil {
			bridge.conn.Close()
		}
		bridge.mu.Unlock()
	}()
	var sizeMu sync.Mutex
	reportSize := func(ws *pty.Winsize) {
		sizeMu.Lock()
		size = &pty.Winsize{Cols: ws.Cols, Rows: ws.Rows}
		sizeMu.Unlock()
		var data [4]byte
		binary.BigEndian.PutUint16(data[:2], ws.Cols)
		binary.BigEndian.PutUint16(data[2:], ws.Rows)
		bridge.send(OpResize, data[:])
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-sigs:
				if sig == syscall.SIGWINCH {
					if ws, e := pty.GetsizeFull(os.Stdin); e == nil && ws.Cols > 0 && ws.Rows > 0 {
						pty.Setsize(ptmx, ws)
						reportSize(ws)
					}
				} else {
					cmd.Process.Signal(sig)
				}
			}
		}
	}()
	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
	go func() {
		paneID := -1
		if value, e := strconv.Atoi(os.Getenv("WEZTERM_PANE")); e == nil {
			paneID = value
		}
		for ctx.Err() == nil {
			c, e := net.DialTimeout("unix", sockPath, time.Second)
			if e == nil {
				cwd, _ := os.Getwd()
				sizeMu.Lock()
				cols, rows := size.Cols, size.Rows
				sizeMu.Unlock()
				meta, _ := json.Marshal(ShimMeta{ID: fmt.Sprintf("shim-%d", os.Getpid()), WezPaneID: paneID, PID: os.Getpid(), Shell: filepath.Base(shell), Cwd: cwd, Cols: cols, Rows: rows})
				c.SetWriteDeadline(time.Now().Add(2 * time.Second))
				e = writeFrame(c, OpMeta, meta)
				if e == nil {
					queue := make(chan []byte, 128)
					bridge.mu.Lock()
					bridge.conn = c
					bridge.out = queue
					bridge.mu.Unlock()
					connectionDone := make(chan struct{})
					go func() {
						defer c.Close()
						for {
							select {
							case <-ctx.Done():
								return
							case <-connectionDone:
								return
							case frame := <-queue:
								c.SetWriteDeadline(time.Now().Add(2 * time.Second))
								if writeFrame(c, frame[0], frame[1:]) != nil {
									return
								}
							}
						}
					}()
					for {
						op, data, e := readFrame(c)
						if e != nil {
							break
						}
						switch op {
						case OpInput:
							if _, e = ptmx.Write(data); e != nil {
								break
							}
						case OpResize:
							if len(data) == 4 {
								cols, rows := binary.BigEndian.Uint16(data[:2]), binary.BigEndian.Uint16(data[2:])
								if cols > 0 && rows > 0 {
									ws := &pty.Winsize{Cols: cols, Rows: rows}
									if pty.Setsize(ptmx, ws) == nil {
										reportSize(ws)
									}
								}
							}
						}
					}
					close(connectionDone)
					bridge.mu.Lock()
					if bridge.conn == c {
						bridge.conn = nil
					}
					bridge.mu.Unlock()
				}
				c.Close()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	buf := make([]byte, 8192)
	for {
		n, e := ptmx.Read(buf)
		if n > 0 {
			if _, err := os.Stdout.Write(buf[:n]); err != nil {
				cmd.Process.Signal(syscall.SIGHUP)
				break
			}
			bridge.send(OpOutput, buf[:n])
		}
		if e != nil {
			break
		}
	}
	return cmd.Wait()
}
