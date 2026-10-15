package peer

import (
	"context"
	"fmt"
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
		if err := msg.Verify(p.Host.ID(), remote, time.Now()); err != nil {
			fmt.Printf("Rejected msg from %s: %v\n", remote, err)
			s.Reset()
			return
		}
		if !p.replay.accept(remote, msg.Seq) {
			fmt.Printf("Rejected replayed msg from %s\n", remote)
			s.Reset()
			return
		}
		p.record(remote, true, msg.Text)
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

// SendSimple sends a signed text message to targetPeerID.
func (p *PeerInfo) SendSimple(targetPeerID peer.ID, text string) error {
	priv := p.Host.Peerstore().PrivKey(p.Host.ID())
	if priv == nil {
		return fmt.Errorf("no private key for local peer")
	}
	msg, err := NewSignedMessage(priv, p.Host.ID(), targetPeerID, p.nextSeq(), text)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := p.Host.NewStream(
		network.WithAllowLimitedConn(ctx, "reason"),
		targetPeerID,
		CHAT_PROTOCOL,
	)
	if err != nil {
		fmt.Printf("Failed to send msg: %v\n", err)
		return err
	}
	defer stream.Close()
	if err := writeFrame(stream, msg); err != nil {
		return err
	}
	p.record(targetPeerID, false, text)
	return nil
}
