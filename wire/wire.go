// Package wire implements size-limited, length-prefixed JSON framing for streams.
package wire

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Write writes v as a uvarint length prefix followed by JSON. It fails if the
// encoded value is larger than max bytes.
func Write(w io.Writer, v any, max int) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > max {
		return fmt.Errorf("message too large (%d bytes, max %d)", len(data), max)
	}
	var l [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(l[:], uint64(len(data)))
	_, err = w.Write(append(l[:n], data...))
	return err
}

// Read reads one frame into v. Frames larger than max are refused before the
// body is read.
func Read(r io.Reader, v any, max int) error {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	size, err := binary.ReadUvarint(br)
	if err != nil {
		return err
	}
	if size == 0 || size > uint64(max) {
		return fmt.Errorf("invalid frame size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(br, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}

// NewReader wraps r so that several frames can be read from one stream
// without losing buffered bytes between calls.
func NewReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }
