package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	OpOutput     byte = 1
	OpInput      byte = 2
	OpResize     byte = 3
	OpMeta       byte = 4
	maxFrameSize      = 4 * 1024 * 1024
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

func defaultSockPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("wezrelay-%d.sock", os.Getuid()))
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func writeFrame(w io.Writer, op byte, p []byte) error {
	if len(p) > maxFrameSize {
		return fmt.Errorf("frame too large")
	}
	var h [5]byte
	h[0] = op
	binary.BigEndian.PutUint32(h[1:], uint32(len(p)))
	if err := writeAll(w, h[:]); err != nil {
		return err
	}
	return writeAll(w, p)
}
func readFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > maxFrameSize {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	p := make([]byte, n)
	_, err := io.ReadFull(r, p)
	return h[0], p, err
}
