package peer

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	messageVersion = 1
	// maxClockSkew bounds how far a message timestamp may differ from our clock.
	maxClockSkew = 5 * time.Minute
	signDomain   = "p2pchat-msg-v1"
)

// Message is a signed chat message. The signature covers the sender, the
// recipient, the sequence number, the timestamp and the text, so a message
// cannot be altered, re-addressed or replayed to someone else.
type Message struct {
	Version   int    `json:"v"`
	From      string `json:"from"`
	To        string `json:"to"`
	Seq       uint64 `json:"seq"`
	Timestamp int64  `json:"ts"` // unix milliseconds
	Text      string `json:"text"`
	Sig       []byte `json:"sig"`
}

func (m *Message) signedBytes() []byte {
	var b bytes.Buffer
	b.WriteString(signDomain)
	for _, s := range []string{m.From, m.To, m.Text} {
		var l [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(l[:], uint64(len(s)))
		b.Write(l[:n])
		b.WriteString(s)
	}
	var num [16]byte
	binary.BigEndian.PutUint64(num[:8], m.Seq)
	binary.BigEndian.PutUint64(num[8:], uint64(m.Timestamp))
	b.Write(num[:])
	return b.Bytes()
}

// NewSignedMessage creates a message from `from` to `to` signed with priv.
func NewSignedMessage(priv crypto.PrivKey, from, to peer.ID, seq uint64, text string) (*Message, error) {
	m := &Message{
		Version:   messageVersion,
		From:      from.String(),
		To:        to.String(),
		Seq:       seq,
		Timestamp: time.Now().UnixMilli(),
		Text:      text,
	}
	sig, err := priv.Sign(m.signedBytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign message: %w", err)
	}
	m.Sig = sig
	return m, nil
}

// Verify checks the signature and that the message is addressed to `self`.
// The sender is taken from the message and must match `remote`, the peer
// authenticated by the transport.
func (m *Message) Verify(self, remote peer.ID, now time.Time) error {
	if m.Version != messageVersion {
		return fmt.Errorf("unsupported message version %d", m.Version)
	}
	from, err := peer.Decode(m.From)
	if err != nil || from != remote {
		return errors.New("sender does not match connection peer")
	}
	if m.To != self.String() {
		return errors.New("message is addressed to another peer")
	}
	ts := time.UnixMilli(m.Timestamp)
	if d := now.Sub(ts); d > maxClockSkew || d < -maxClockSkew {
		return errors.New("message timestamp out of range")
	}
	pub, err := from.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("cannot get sender public key: %w", err)
	}
	ok, err := pub.Verify(m.signedBytes(), m.Sig)
	if err != nil || !ok {
		return errors.New("invalid signature")
	}
	return nil
}

// writeFrame writes m as a uvarint length prefix followed by JSON.
func writeFrame(w io.Writer, m *Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > maxMessageSize {
		return fmt.Errorf("message too large (%d bytes, max %d)", len(data), maxMessageSize)
	}
	var l [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(l[:], uint64(len(data)))
	_, err = w.Write(append(l[:n], data...))
	return err
}

// readFrame reads one length-prefixed message, refusing frames over maxMessageSize.
func readFrame(r io.Reader) (*Message, error) {
	br := bufio.NewReader(r)
	size, err := binary.ReadUvarint(br)
	if err != nil {
		return nil, err
	}
	if size == 0 || size > maxMessageSize {
		return nil, fmt.Errorf("invalid frame size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(br, buf); err != nil {
		return nil, err
	}
	var m Message
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// replayGuard rejects messages whose sequence number is not greater than the
// highest one already accepted from the same sender.
type replayGuard struct {
	mu   sync.Mutex
	last map[peer.ID]uint64
}

func newReplayGuard() *replayGuard {
	return &replayGuard{last: make(map[peer.ID]uint64)}
}

func (g *replayGuard) accept(id peer.ID, seq uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if seq <= g.last[id] {
		return false
	}
	g.last[id] = seq
	return true
}
