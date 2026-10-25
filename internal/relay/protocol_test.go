package relay

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Buffer.Write(p)
}
func TestFrameRoundTripShortWrites(t *testing.T) {
	var w shortWriter
	p := []byte("payload")
	if err := writeFrame(&w, OpOutput, p); err != nil {
		t.Fatal(err)
	}
	op, got, err := readFrame(&w)
	if err != nil || op != OpOutput || !bytes.Equal(got, p) {
		t.Fatalf("%d %q %v", op, got, err)
	}
}
func TestFrameRejectsOversizeAndTruncation(t *testing.T) {
	var h [5]byte
	binary.BigEndian.PutUint32(h[1:], maxFrameSize+1)
	if _, _, err := readFrame(bytes.NewReader(h[:])); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, _, err := readFrame(bytes.NewReader([]byte{1, 0})); err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
	if err := writeFrame(io.Discard, OpOutput, make([]byte, maxFrameSize+1)); err == nil {
		t.Fatal("oversize write accepted")
	}
}
