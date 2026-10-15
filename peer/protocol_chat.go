package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const CHAT_PROTOCOL = "/private-chat/1.0.0"

const (
	maxMessageSize = 64 << 10 // 64 KiB
	readTimeout    = 10 * time.Second
)

func (p *PeerInfo) ChatHandler() {
	p.Host.SetStreamHandler(CHAT_PROTOCOL, func(s network.Stream) {
		defer s.Close()
		if err := s.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			s.Reset()
			return
		}
		var msg string
		decoder := json.NewDecoder(io.LimitReader(s, maxMessageSize))
		if err := decoder.Decode(&msg); err != nil {
			fmt.Printf("Failed to receive msg: %v\n", err)
			s.Reset()
			return
		}
		// The remote peer ID is authenticated by the transport, so it cannot be spoofed.
		fmt.Printf("[%s] %s\n", s.Conn().RemotePeer(), msg)
	})
}

func (p *PeerInfo) SendSimple(targetPeerID peer.ID, text string) error {
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
	encoder := json.NewEncoder(stream)
	if err := encoder.Encode(text); err != nil {
		return err
	}
	return nil
}
