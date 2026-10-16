package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Chandra179/sabaody/cryptoutils"
	"github.com/Chandra179/sabaody/mailbox"
	"github.com/Chandra179/sabaody/wire"
	"github.com/libp2p/go-libp2p/core/crypto"
	"log"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// CHAT_PROTOCOL carries signed, length-prefixed messages (see Message).
const CHAT_PROTOCOL = "/private-chat/2.0.0"

const (
	maxMessageSize = 64 << 10 // 64 KiB
	readTimeout    = 10 * time.Second
)

// ChatHandler registers the stream handler that receives and verifies messages.
func (p *PeerInfo) ChatHandler() {
	p.replay = newReplayGuard()
	p.receiptHandler()
	p.fileHandler()
	p.Host.SetStreamHandler(CHAT_PROTOCOL, func(s network.Stream) {
		defer s.Close()
		if err := s.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			s.Reset()
			return
		}
		remote := s.Conn().RemotePeer()
		msg, err := readFrame(s)
		if err != nil {
			fmt.Printf("Failed to receive msg from %s: %v\n", remote, err)
			s.Reset()
			return
		}
		if err := msg.Verify(p.Host.ID(), remote, time.Now(), maxClockSkew); err != nil {
			fmt.Printf("Rejected msg from %s: %v\n", remote, err)
			s.Reset()
			return
		}
		if !p.replay.accept(remote, msg.Seq) {
			fmt.Printf("Rejected replayed msg from %s\n", remote)
			s.Reset()
			return
		}
		if p.Store != nil {
			// Remember it so the same message arriving via the mailbox is dropped.
			p.Store.MarkSeen(remote, msg.Seq)
		}
		p.record(remote, true, false, time.Now(), msg.Seq, msg.Text)
		go p.sendReceipt(remote, msg.Seq)
		if p.OnMessage != nil {
			p.OnMessage(remote, msg.Text)
		}
	})
}

// nextSeq returns a strictly increasing sequence number, based on the clock
// so that it also keeps increasing across restarts.
func (p *PeerInfo) nextSeq() uint64 {
	for {
		last := p.seq.Load()
		next := uint64(time.Now().UnixNano())
		if next <= last {
			next = last + 1
		}
		if p.seq.CompareAndSwap(last, next) {
			return next
		}
	}
}

// SendSimple sends a signed text message to targetPeerID. If the peer cannot
// be reached it is sealed end-to-end and left in the relay's mailbox instead,
// and queued is true.
func (p *PeerInfo) SendSimple(targetPeerID peer.ID, text string) (queued bool, err error) {
	priv := p.Host.Peerstore().PrivKey(p.Host.ID())
	if priv == nil {
		return false, errors.New("no private key for local peer")
	}
	msg, err := NewSignedMessage(priv, p.Host.ID(), targetPeerID, p.nextSeq(), text)
	if err != nil {
		return false, err
	}

	liveErr := p.sendLive(targetPeerID, msg)
	if liveErr == nil {
		p.record(targetPeerID, false, false, time.Now(), msg.Seq, text)
		return false, nil
	}
	if p.Relay.ID == "" {
		return false, liveErr
	}
	if err := p.queue(targetPeerID, msg); err != nil {
		return false, fmt.Errorf("peer unreachable (%v) and could not queue message: %w", liveErr, err)
	}
	p.record(targetPeerID, false, true, time.Now(), msg.Seq, text)
	return true, nil
}

func (p *PeerInfo) sendLive(target peer.ID, msg *Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := p.Host.NewStream(
		network.WithAllowLimitedConn(ctx, "reason"),
		target,
		CHAT_PROTOCOL,
	)
	if err != nil {
		return err
	}
	defer stream.Close()
	return writeFrame(stream, msg)
}

// queue seals v (a *Message or *Receipt) for target and leaves it in the relay mailbox.
func (p *PeerInfo) queue(target peer.ID, v any) error {
	pub, err := target.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("cannot get recipient public key: %w", err)
	}
	var buf bytes.Buffer
	if err := wire.Write(&buf, v, maxMessageSize); err != nil {
		return err
	}
	blob, err := cryptoutils.Seal(buf.Bytes(), pub)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Host.Connect(ctx, p.Relay); err != nil {
		return fmt.Errorf("cannot reach relay: %w", err)
	}
	return mailbox.Put(ctx, p.Host, p.Relay.ID, target, blob)
}

// FetchOffline collects messages that were left for us in the relay mailbox
// and returns how many were delivered.
func (p *PeerInfo) FetchOffline(ctx context.Context) (int, error) {
	if p.Relay.ID == "" {
		return 0, nil
	}
	priv := p.Host.Peerstore().PrivKey(p.Host.ID())
	if priv == nil {
		return 0, errors.New("no private key for local peer")
	}
	if err := p.Host.Connect(ctx, p.Relay); err != nil {
		return 0, fmt.Errorf("cannot reach relay: %w", err)
	}
	delivered := 0
	_, err := mailbox.Fetch(ctx, p.Host, p.Relay.ID, func(blob []byte) bool {
		ack, ok := p.receiveSealed(priv, blob)
		if ok {
			delivered++
		}
		return ack
	})
	return delivered, err
}

// receiveSealed opens, verifies and delivers one mailbox blob. ack is false
// only when the blob should be kept on the relay and retried later.
func (p *PeerInfo) receiveSealed(priv crypto.PrivKey, blob []byte) (ack, delivered bool) {
	plain, err := cryptoutils.Open(blob, priv)
	if err != nil {
		log.Printf("Dropping undecryptable mailbox message: %v", err)
		return true, false
	}
	raw, err := wire.ReadRaw(bytes.NewReader(plain), maxMessageSize)
	if err != nil {
		log.Printf("Dropping malformed mailbox message: %v", err)
		return true, false
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	json.Unmarshal(raw, &kind)
	if kind.Kind == kindReceipt {
		p.receiveSealedReceipt(raw)
		return true, false
	}
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("Dropping malformed mailbox message: %v", err)
		return true, false
	}
	from, err := peer.Decode(msg.From)
	if err != nil {
		return true, false
	}
	if err := msg.Verify(p.Host.ID(), from, time.Now(), mailbox.TTL); err != nil {
		log.Printf("Dropping mailbox message from %s: %v", from, err)
		return true, false
	}
	if !p.allow.Allows(from) {
		log.Printf("Dropping mailbox message from %s: not on allowlist", from)
		return true, false
	}
	if p.Store != nil {
		isNew, err := p.Store.MarkSeen(from, msg.Seq)
		if err != nil {
			log.Printf("Failed to record mailbox message, will retry: %v", err)
			return false, false
		}
		if !isNew {
			return true, false // duplicate or replay
		}
	}
	p.record(from, true, false, time.UnixMilli(msg.Timestamp), msg.Seq, msg.Text)
	go p.sendReceipt(from, msg.Seq)
	if p.OnMessage != nil {
		p.OnMessage(from, msg.Text)
	}
	return true, true
}

// receiveSealedReceipt handles a delivery receipt that came through the mailbox.
func (p *PeerInfo) receiveSealedReceipt(raw []byte) {
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return
	}
	from, err := peer.Decode(r.From)
	if err != nil {
		return
	}
	if err := r.Verify(p.Host.ID(), from, time.Now(), mailbox.TTL); err != nil {
		log.Printf("Dropping mailbox receipt from %s: %v", from, err)
		return
	}
	if !p.allow.Allows(from) {
		return
	}
	p.handleReceipt(from, r.Seq)
}
