package peer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/Chandra179/sabaody/wire"
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
	b := []byte(signDomain)
	for _, s := range []string{m.From, m.To, m.Text} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	b = binary.BigEndian.AppendUint64(b, m.Seq)
	return binary.BigEndian.AppendUint64(b, uint64(m.Timestamp))
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
// authenticated by the transport (or, for mailbox messages, the sender
// named in the message). The timestamp may be at most maxAge in the past and
// maxClockSkew in the future.
func (m *Message) Verify(self, remote peer.ID, now time.Time, maxAge time.Duration) error {
	if m.Version != messageVersion {
		return fmt.Errorf("unsupported message version %d", m.Version)
	}
	return verifySigned(m.From, m.To, self, remote, m.Timestamp, now, maxAge, m.signedBytes(), m.Sig)
}

// verifySigned holds the checks shared by every signed object: the claimed
// sender matches remote, it is addressed to self, the timestamp is in range
// and the signature is valid for the sender's key.
func verifySigned(fromStr, toStr string, self, remote peer.ID, tsMillis int64, now time.Time, maxAge time.Duration, data, sig []byte) error {
	from, err := peer.Decode(fromStr)
	if err != nil || from != remote {
		return errors.New("sender does not match connection peer")
	}
	if toStr != self.String() {
		return errors.New("message is addressed to another peer")
	}
	if d := now.Sub(time.UnixMilli(tsMillis)); d > maxAge || d < -maxClockSkew {
		return errors.New("message timestamp out of range")
	}
	pub, err := from.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("cannot get sender public key: %w", err)
	}
	ok, err := pub.Verify(data, sig)
	if err != nil || !ok {
		return errors.New("invalid signature")
	}
	return nil
}

// writeFrame writes m as a length-prefixed JSON frame.
func writeFrame(w io.Writer, m *Message) error {
	return wire.Write(w, m, maxMessageSize)
}

// readFrame reads one length-prefixed message, refusing frames over maxMessageSize.
func readFrame(r io.Reader) (*Message, error) {
	var m Message
	if err := wire.Read(r, &m, maxMessageSize); err != nil {
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
