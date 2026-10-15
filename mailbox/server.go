// Package mailbox implements store-and-forward delivery of sealed messages for
// peers that are offline. A relay runs the Server; peers use Put to leave a
// message for someone and Fetch to collect their own. Blobs are end-to-end
// sealed by the sender, so the server cannot read them.
package mailbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"p2p/access"
	"p2p/wire"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	bolt "go.etcd.io/bbolt"
)

const (
	Protocol = "/p2pchat/mailbox/1.0.0"

	// MaxBlob is the largest sealed message accepted.
	MaxBlob = 70 << 10
	// MaxPerRecipient bounds how many messages wait for one recipient.
	MaxPerRecipient = 200
	// TTL is how long an undelivered message is kept.
	TTL = 7 * 24 * time.Hour

	maxRequest      = 128 << 10
	maxResponse     = 1 << 20
	fetchBatchBytes = 512 << 10
	sessionTimeout  = 30 * time.Second
)

var (
	ErrFull     = errors.New("recipient mailbox is full")
	ErrTooLarge = errors.New("message too large")
	ErrDenied   = errors.New("not allowed")
)

var rootBucket = []byte("mailbox")

type Request struct {
	Op   string   `json:"op"` // "put", "fetch" or "ack"
	To   string   `json:"to,omitempty"`
	Blob []byte   `json:"blob,omitempty"`
	IDs  []uint64 `json:"ids,omitempty"`
}

type Item struct {
	ID   uint64 `json:"id"`
	Blob []byte `json:"blob"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Items []Item `json:"items,omitempty"`
}

type Server struct {
	db    *bolt.DB
	allow access.Allowlist
	now   func() time.Time
	stop  chan struct{}
}

// NewServer opens the mailbox database at path. An empty allowlist lets everyone use it.
func NewServer(path string, allow access.Allowlist) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("cannot open mailbox database: %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(rootBucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Server{db: db, allow: allow, now: time.Now, stop: make(chan struct{})}, nil
}

// Register serves the mailbox protocol on h and starts hourly cleanup of expired messages.
func (s *Server) Register(h host.Host) {
	h.SetStreamHandler(Protocol, s.handle)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := s.prune(); err != nil {
					log.Printf("mailbox prune failed: %v", err)
				}
			case <-s.stop:
				return
			}
		}
	}()
}

func (s *Server) Close() error {
	close(s.stop)
	return s.db.Close()
}

func (s *Server) handle(st network.Stream) {
	defer st.Close()
	if err := st.SetDeadline(s.now().Add(sessionTimeout)); err != nil {
		st.Reset()
		return
	}
	remote := st.Conn().RemotePeer()
	r := wire.NewReader(st)
	for {
		var req Request
		if err := wire.Read(r, &req, maxRequest); err != nil {
			return // EOF, timeout or bad frame
		}
		if err := wire.Write(st, s.serve(remote, req), maxResponse); err != nil {
			return
		}
	}
}

func (s *Server) serve(remote peer.ID, req Request) Response {
	if !s.allow.Allows(remote) {
		return Response{Error: ErrDenied.Error()}
	}
	switch req.Op {
	case "put":
		to, err := peer.Decode(req.To)
		if err != nil {
			return Response{Error: "invalid recipient"}
		}
		if !s.allow.Allows(to) {
			return Response{Error: ErrDenied.Error()}
		}
		if err := s.put(to, req.Blob); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true}
	case "fetch":
		items, err := s.fetch(remote, fetchBatchBytes)
		if err != nil {
			return Response{Error: "internal error"}
		}
		return Response{OK: true, Items: items}
	case "ack":
		if err := s.ack(remote, req.IDs); err != nil {
			return Response{Error: "internal error"}
		}
		return Response{OK: true}
	default:
		return Response{Error: "unknown operation"}
	}
}

func seqKey(seq uint64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, seq)
	return k
}

func (s *Server) expired(created int64) bool {
	return s.now().Sub(time.Unix(created, 0)) > TTL
}

func (s *Server) put(to peer.ID, blob []byte) error {
	if len(blob) == 0 || len(blob) > MaxBlob {
		return ErrTooLarge
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket(rootBucket).CreateBucketIfNotExists([]byte(to))
		if err != nil {
			return err
		}
		n := 0
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if len(v) >= 8 && !s.expired(int64(binary.BigEndian.Uint64(v))) {
				n++
			}
		}
		if n >= MaxPerRecipient {
			return ErrFull
		}
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		val := make([]byte, 8, 8+len(blob))
		binary.BigEndian.PutUint64(val, uint64(s.now().Unix()))
		return b.Put(seqKey(seq), append(val, blob...))
	})
}

// fetch returns the oldest unexpired messages for id, up to about maxBytes.
func (s *Server) fetch(id peer.ID, maxBytes int) ([]Item, error) {
	var items []Item
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(rootBucket).Bucket([]byte(id))
		if b == nil {
			return nil
		}
		total := 0
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if len(v) < 8 || s.expired(int64(binary.BigEndian.Uint64(v))) {
				if err := c.Delete(); err != nil {
					return err
				}
				continue
			}
			blob := append([]byte(nil), v[8:]...)
			if len(items) > 0 && total+len(blob) > maxBytes {
				break
			}
			items = append(items, Item{ID: binary.BigEndian.Uint64(k), Blob: blob})
			total += len(blob)
		}
		return nil
	})
	return items, err
}

func (s *Server) ack(id peer.ID, ids []uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(rootBucket).Bucket([]byte(id))
		if b == nil {
			return nil
		}
		for _, seq := range ids {
			if err := b.Delete(seqKey(seq)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Server) prune() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(rootBucket)
		var empty [][]byte
		err := root.ForEach(func(name, _ []byte) error {
			b := root.Bucket(name)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				if len(v) < 8 || s.expired(int64(binary.BigEndian.Uint64(v))) {
					if err := c.Delete(); err != nil {
						return err
					}
				}
			}
			if k, _ := b.Cursor().First(); k == nil {
				empty = append(empty, append([]byte(nil), name...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, name := range empty {
			if err := root.DeleteBucket(name); err != nil {
				return err
			}
		}
		return nil
	})
}
