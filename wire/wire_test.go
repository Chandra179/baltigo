package wire

import (
	"bytes"
	"testing"
)

type msg struct {
	N int    `json:"n"`
	S string `json:"s"`
}

func TestRoundTripMultipleFrames(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		if err := Write(&buf, msg{i, "x"}, 1024); err != nil {
			t.Fatal(err)
		}
	}
	r := NewReader(&buf)
	for i := 0; i < 3; i++ {
		var m msg
		if err := Read(r, &m, 1024); err != nil || m.N != i {
			t.Fatalf("frame %d: %+v, %v", i, m, err)
		}
	}
}

func TestLimits(t *testing.T) {
	if err := Write(&bytes.Buffer{}, msg{1, string(make([]byte, 100))}, 50); err == nil {
		t.Error("oversized write accepted")
	}
	var m msg
	if err := Read(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0x7f}), &m, 1024); err == nil {
		t.Error("oversized frame accepted")
	}
	if err := Read(bytes.NewReader([]byte{0}), &m, 1024); err == nil {
		t.Error("zero-length frame accepted")
	}
	if err := Read(bytes.NewReader([]byte{5, '{'}), &m, 1024); err == nil {
		t.Error("truncated frame accepted")
	}
}
