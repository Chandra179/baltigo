package peer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"time"

	"p2p/wire"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// RECEIPT_PROTOCOL carries signed delivery receipts.
	RECEIPT_PROTOCOL = "/private-chat/receipt/1.0.0"

	kindReceipt    = "receipt"
	receiptVersion = 1
	receiptDomain  = "p2pchat-receipt-v1"
	maxReceiptSize = 4 << 10
)

// Receipt is a signed acknowledgement that the sender of the original message
// received it. It carries the sequence number of that message, so it proves
// delivery without repeating the content.
type Receipt struct {
	Kind      string `json:"kind"` // always "receipt"; lets mailbox blobs be told apart from messages
	Version   int    `json:"v"`
	From      string `json:"from"` // the recipient of the original message
	To        string `json:"to"`   // the original sender
	Seq       uint64 `json:"seq"`
	Timestamp int64  `json:"ts"` // unix milliseconds
	Sig       []byte `json:"sig"`
}

func (r *Receipt) signedBytes() []byte {
	var b bytes.Buffer
	b.WriteString(receiptDomain)
	for _, s := range []string{r.From, r.To} {
		b.Write(binary.AppendUvarint(nil, uint64(len(s))))
		b.WriteString(s)
	}
	b.Write(binary.BigEndian.AppendUint64(nil, r.Seq))
	b.Write(binary.BigEndian.AppendUint64(nil, uint64(r.Timestamp)))
	return b.Bytes()
}

// NewSignedReceipt acknowledges message seq, which `to` sent to `from`.
func NewSignedReceipt(priv crypto.PrivKey, from, to peer.ID, seq uint64) (*Receipt, error) {
	r := &Receipt{
		Kind:      kindReceipt,
		Version:   receiptVersion,
		From:      from.String(),
		To:        to.String(),
		Seq:       seq,
		Timestamp: time.Now().UnixMilli(),
	}
	sig, err := priv.Sign(r.signedBytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign receipt: %w", err)
	}
	r.Sig = sig
	return r, nil
}

// Verify applies the same checks as Message.Verify.
func (r *Receipt) Verify(self, remote peer.ID, now time.Time, maxAge time.Duration) error {
	if r.Kind != kindReceipt || r.Version != receiptVersion {
		return fmt.Errorf("unsupported receipt version %d", r.Version)
	}
	return verifySigned(r.From, r.To, self, remote, r.Timestamp, now, maxAge, r.signedBytes(), r.Sig)
}

// receiptHandler registers the stream handler that receives receipts.
func (p *PeerInfo) receiptHandler() {
	p.Host.SetStreamHandler(RECEIPT_PROTOCOL, func(s network.Stream) {
		defer s.Close()
		if err := s.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			s.Reset()
			return
		}
		remote := s.Conn().RemotePeer()
		var r Receipt
		if err := wire.Read(s, &r, maxReceiptSize); err != nil {
			s.Reset()
			return
		}
		if err := r.Verify(p.Host.ID(), remote, time.Now(), maxClockSkew); err != nil {
			log.Printf("Rejected receipt from %s: %v", remote, err)
			s.Reset()
			return
		}
		p.handleReceipt(remote, r.Seq)
	})
}

// handleReceipt marks our message seq to `from` as delivered.
func (p *PeerInfo) handleReceipt(from peer.ID, seq uint64) {
	if p.Store == nil {
		return
	}
	found, err := p.Store.MarkDelivered(from, seq)
	if err != nil {
		log.Printf("Failed to record receipt: %v", err)
		return
	}
	if found && p.OnReceipt != nil {
		p.OnReceipt(from, seq)
	}
}

// sendReceipt tells `to` that we received their message seq. It goes out live
// if possible and otherwise through the relay mailbox. Failures are only logged:
// the sender simply keeps showing the message as not yet delivered.
func (p *PeerInfo) sendReceipt(to peer.ID, seq uint64) {
	if p.DisableReceipts {
		return
	}
	priv := p.Host.Peerstore().PrivKey(p.Host.ID())
	if priv == nil {
		return
	}
	r, err := NewSignedReceipt(priv, p.Host.ID(), to, seq)
	if err != nil {
		log.Printf("Failed to create receipt: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := p.Host.NewStream(network.WithAllowLimitedConn(ctx, "receipt"), to, RECEIPT_PROTOCOL)
	if err == nil {
		defer s.Close()
		if err = wire.Write(s, r, maxReceiptSize); err == nil {
			return
		}
	}
	if p.Relay.ID == "" {
		return
	}
	if qerr := p.queue(to, r); qerr != nil {
		log.Printf("Could not deliver receipt to %s: %v", to, qerr)
	}
}
