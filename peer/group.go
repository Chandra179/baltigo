package peer

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/Chandra179/sabaody/store"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	groupPayloadVersion = 1
	maxGroupMessage     = 60 << 10 // plaintext; GossipSub's own limit is set to maxMessageSize
	nonceCacheWindow    = 15 * time.Minute
)

var groupNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

var (
	ErrBadGroupName  = errors.New("group name must be 1-32 characters: letters, digits, '-' or '_'")
	ErrAlreadyJoined = errors.New("already joined this group")
	ErrNotJoined     = errors.New("not in this group")
)

// groupPayload is what is encrypted inside each group message. The author is
// not part of it: GossipSub signs every message with the author's key and
// verifies it before delivery, so the author is taken from there.
type groupPayload struct {
	Version   int    `json:"v"`
	Timestamp int64  `json:"ts"` // unix milliseconds
	Text      string `json:"text"`
}

type group struct {
	name   string
	topic  *pubsub.Topic
	sub    *pubsub.Subscription
	aead   cipher.AEAD
	cancel context.CancelFunc

	mu    sync.Mutex
	nonce map[string]time.Time // recently seen nonces, to drop replayed ciphertexts
}

// deriveGroup turns a group name and shared secret into the pubsub topic name
// and the key that encrypts the group's messages. The topic name is a hash, so
// neither the group name nor the secret is visible on the network. With an
// empty secret anyone who knows the group name can read it.
func deriveGroup(name, secret string) (topic string, aead cipher.AEAD, err error) {
	key := argon2.IDKey([]byte(secret), []byte("p2pchat-group-v1:"+name), 1, 64*1024, 4, 32)
	tid := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256.New, key, nil, []byte("p2pchat-group-topic")), tid); err != nil {
		return "", nil, err
	}
	aead, err = chacha20poly1305.NewX(key)
	if err != nil {
		return "", nil, err
	}
	return "p2pchat/group/" + hex.EncodeToString(tid), aead, nil
}

// seal encrypts text for the group; ad binds the ciphertext to the topic.
func (g *group) seal(text string, ts time.Time) ([]byte, error) {
	data, err := json.Marshal(groupPayload{Version: groupPayloadVersion, Timestamp: ts.UnixMilli(), Text: text})
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.aead.NonceSize(), g.aead.NonceSize()+len(data)+g.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.aead.Seal(nonce, nonce, data, []byte(g.topic.String())), nil
}

func (g *group) open(data []byte) (nonce string, pl groupPayload, err error) {
	n := g.aead.NonceSize()
	if len(data) < n+g.aead.Overhead() {
		return "", pl, errors.New("message too short")
	}
	pt, err := g.aead.Open(nil, data[:n], data[n:], []byte(g.topic.String()))
	if err != nil {
		return "", pl, errors.New("cannot decrypt (wrong secret?)")
	}
	if err := json.Unmarshal(pt, &pl); err != nil {
		return "", pl, err
	}
	return string(data[:n]), pl, nil
}

// firstSight reports whether nonce has not been seen within the replay window.
func (g *group) firstSight(nonce string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.nonce) > 4096 {
		for k, t := range g.nonce {
			if now.Sub(t) > nonceCacheWindow {
				delete(g.nonce, k)
			}
		}
	}
	if _, dup := g.nonce[nonce]; dup {
		return false
	}
	g.nonce[nonce] = now
	return true
}

// ensurePubSub starts GossipSub on first use. Members find each other through
// the private DHT, using the topic name as the rendezvous point.
func (p *PeerInfo) ensurePubSub(ctx context.Context) error {
	if p.ps != nil {
		return nil
	}
	opts := []pubsub.Option{pubsub.WithMaxMessageSize(maxMessageSize)}
	if p.Relay.ID != "" {
		dm, err := p.DHT(ctx)
		if err != nil {
			return fmt.Errorf("cannot start peer discovery: %w", err)
		}
		opts = append(opts, pubsub.WithDiscovery(dm.Discovery))
	}
	p.psCtx, p.psCancel = context.WithCancel(context.Background())
	ps, err := pubsub.NewGossipSub(p.psCtx, p.Host, opts...)
	if err != nil {
		p.psCancel()
		return err
	}
	p.ps = ps
	return nil
}

// JoinGroup joins (or creates) the group `name`. Everyone using the same name
// and secret ends up in the same group.
func (p *PeerInfo) JoinGroup(ctx context.Context, name, secret string) error {
	if !groupNameRe.MatchString(name) {
		return ErrBadGroupName
	}
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	if _, ok := p.groups[name]; ok {
		return ErrAlreadyJoined
	}
	topicName, aead, err := deriveGroup(name, secret)
	if err != nil {
		return err
	}
	if err := p.ensurePubSub(ctx); err != nil {
		return err
	}
	topic, err := p.ps.Join(topicName)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		topic.Close()
		return err
	}
	gctx, cancel := context.WithCancel(p.psCtx)
	g := &group{name: name, topic: topic, sub: sub, aead: aead, cancel: cancel, nonce: make(map[string]time.Time)}
	if p.groups == nil {
		p.groups = make(map[string]*group)
	}
	p.groups[name] = g
	go p.runGroup(gctx, g)
	return nil
}

// LeaveGroup stops receiving the group's messages.
func (p *PeerInfo) LeaveGroup(name string) error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	g, ok := p.groups[name]
	if !ok {
		return ErrNotJoined
	}
	delete(p.groups, name)
	g.cancel()
	g.sub.Cancel()
	return g.topic.Close()
}

// Groups lists the groups we are in, with the number of other members currently connected.
func (p *PeerInfo) Groups() map[string]int {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	out := make(map[string]int, len(p.groups))
	for name, g := range p.groups {
		out[name] = len(g.topic.ListPeers())
	}
	return out
}

// GroupPeers returns the other members of a group that we are connected to.
func (p *PeerInfo) GroupPeers(name string) ([]peer.ID, error) {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	g, ok := p.groups[name]
	if !ok {
		return nil, ErrNotJoined
	}
	peers := g.topic.ListPeers()
	sort.Slice(peers, func(i, j int) bool { return peers[i] < peers[j] })
	return peers, nil
}

// SendGroup sends text to the group and returns how many other members are
// currently connected (0 means nobody will receive it right now).
func (p *PeerInfo) SendGroup(name, text string) (members int, err error) {
	if text == "" {
		return 0, errors.New("empty message")
	}
	if len(text) > maxGroupMessage {
		return 0, fmt.Errorf("message too large (%d bytes, max %d)", len(text), maxGroupMessage)
	}
	p.groupMu.Lock()
	g, ok := p.groups[name]
	p.groupMu.Unlock()
	if !ok {
		return 0, ErrNotJoined
	}
	now := time.Now()
	data, err := g.seal(text, now)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.topic.Publish(ctx, data); err != nil {
		return 0, err
	}
	if p.Store != nil {
		e := store.Entry{Author: p.Host.ID(), Time: now, Text: text}
		if err := p.Store.AppendGroupMessage(name, e); err != nil {
			log.Printf("Failed to save group message: %v", err)
		}
	}
	return len(g.topic.ListPeers()), nil
}

func (p *PeerInfo) runGroup(ctx context.Context, g *group) {
	for {
		m, err := g.sub.Next(ctx)
		if err != nil {
			return // cancelled or left
		}
		from := m.GetFrom()
		if from == p.Host.ID() {
			continue
		}
		p.handleGroupMessage(g, from, m.Data)
	}
}

// handleGroupMessage decrypts and delivers one message. from is the author as
// verified by GossipSub's signature check.
func (p *PeerInfo) handleGroupMessage(g *group, from peer.ID, data []byte) {
	if !p.allow.Allows(from) {
		return
	}
	nonce, pl, err := g.open(data)
	if err != nil {
		return // not from a member who knows the secret
	}
	now := time.Now()
	if pl.Version != groupPayloadVersion || pl.Text == "" {
		return
	}
	if d := now.Sub(time.UnixMilli(pl.Timestamp)); d > maxClockSkew || d < -maxClockSkew {
		return
	}
	if !g.firstSight(nonce, now) {
		return
	}
	if p.Store != nil {
		e := store.Entry{Author: from, Incoming: true, Time: now, Text: pl.Text}
		if err := p.Store.AppendGroupMessage(g.name, e); err != nil {
			log.Printf("Failed to save group message: %v", err)
		}
	}
	if p.OnGroupMessage != nil {
		p.OnGroupMessage(g.name, from, pl.Text)
	}
}

// Close stops group chat. The libp2p host is closed separately.
func (p *PeerInfo) Close() {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	for name, g := range p.groups {
		g.cancel()
		delete(p.groups, name)
	}
	if p.psCancel != nil {
		p.psCancel()
	}
}
