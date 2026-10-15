// Package store persists contacts and message history in an embedded bbolt
// database. Contact names and message contents are encrypted with a key
// derived from the node's identity key, so the database file alone does not
// reveal them. Peer IDs are stored in clear.
package store

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

var (
	contactsBucket = []byte("contacts") // peer ID -> sealed name
	messagesBucket = []byte("messages") // peer ID -> bucket(seq -> sealed entry)
	seenBucket     = []byte("seen")     // peer ID || message seq -> unix time first seen
)

// seenRetention is how long delivered message IDs are remembered for de-duplication.
const seenRetention = 30 * 24 * time.Hour

const (
	maxNameLen = 32
	kdfInfo    = "p2pchat-store-v1"
)

var (
	ErrNotFound      = errors.New("contact not found")
	ErrNameTaken     = errors.New("a contact with this name already exists")
	ErrPeerExists    = errors.New("this peer is already a contact")
	ErrInvalidName   = fmt.Errorf("name must be 1-%d characters without spaces", maxNameLen)
	ErrCannotDecrypt = errors.New("cannot decrypt stored data (wrong identity key?)")
)

type Store struct {
	db   *bolt.DB
	aead cipher.AEAD
}

type Contact struct {
	Name string
	ID   peer.ID
}

type Entry struct {
	Peer     peer.ID   `json:"-"`
	Incoming bool      `json:"in"`
	Queued   bool      `json:"q,omitempty"` // sent to the relay mailbox, not delivered live
	Time     time.Time `json:"t"`
	Text     string    `json:"text"`
}

// Open opens (creating if needed) the database at path. priv is the node's
// identity key, from which the encryption key is derived.
func Open(path string, priv crypto.PrivKey) (*Store, error) {
	raw, err := priv.Raw()
	if err != nil {
		return nil, fmt.Errorf("cannot read identity key: %w", err)
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, raw, nil, []byte(kdfInfo)), key); err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("cannot open database (is another instance using it?): %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{contactsBucket, messagesBucket, seenBucket} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	st := &Store{db: db, aead: aead}
	st.pruneSeen(time.Now())
	return st, nil
}

// MarkSeen records that message seq from id was handled. It reports whether
// the message is new; false means it is a duplicate or a replay.
func (s *Store) MarkSeen(id peer.ID, seq uint64) (bool, error) {
	key := make([]byte, 0, len(id)+8)
	key = append(key, id...)
	key = binary.BigEndian.AppendUint64(key, seq)
	isNew := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(seenBucket)
		if b.Get(key) != nil {
			return nil
		}
		isNew = true
		return b.Put(key, binary.BigEndian.AppendUint64(nil, uint64(time.Now().Unix())))
	})
	return isNew, err
}

func (s *Store) pruneSeen(now time.Time) {
	s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(seenBucket)
		var old [][]byte
		b.ForEach(func(k, v []byte) error {
			if len(v) == 8 && now.Sub(time.Unix(int64(binary.BigEndian.Uint64(v)), 0)) > seenRetention {
				old = append(old, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range old {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Close() error { return s.db.Close() }

// seal encrypts plaintext, binding it to ad (the record's owner) so records
// cannot be moved between peers.
func (s *Store) seal(plaintext, ad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize(), s.aead.NonceSize()+len(plaintext)+s.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plaintext, ad), nil
}

func (s *Store) open(sealed, ad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrCannotDecrypt
	}
	pt, err := s.aead.Open(nil, sealed[:n], sealed[n:], ad)
	if err != nil {
		return nil, ErrCannotDecrypt
	}
	return pt, nil
}

func validName(name string) bool {
	return name != "" && len(name) <= maxNameLen && !strings.ContainsAny(name, " \t\r\n")
}

// AddContact saves a contact. Names are unique, case-insensitively.
func (s *Store) AddContact(name string, id peer.ID) error {
	if !validName(name) {
		return ErrInvalidName
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(contactsBucket)
		if b.Get([]byte(id)) != nil {
			return ErrPeerExists
		}
		taken := false
		err := b.ForEach(func(k, v []byte) error {
			existing, err := s.open(v, k)
			if err != nil {
				return err
			}
			if strings.EqualFold(string(existing), name) {
				taken = true
			}
			return nil
		})
		if err != nil {
			return err
		}
		if taken {
			return ErrNameTaken
		}
		sealed, err := s.seal([]byte(name), []byte(id))
		if err != nil {
			return err
		}
		return b.Put([]byte(id), sealed)
	})
}

// RemoveContact deletes a contact by name (history is kept).
func (s *Store) RemoveContact(name string) error {
	id, err := s.contactByName(name)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(contactsBucket).Delete([]byte(id))
	})
}

func (s *Store) Contacts() ([]Contact, error) {
	var out []Contact
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(contactsBucket).ForEach(func(k, v []byte) error {
			name, err := s.open(v, k)
			if err != nil {
				return err
			}
			out = append(out, Contact{Name: string(name), ID: peer.ID(k)})
			return nil
		})
	})
	return out, err
}

func (s *Store) contactByName(name string) (peer.ID, error) {
	contacts, err := s.Contacts()
	if err != nil {
		return "", err
	}
	for _, c := range contacts {
		if strings.EqualFold(c.Name, name) {
			return c.ID, nil
		}
	}
	return "", ErrNotFound
}

// Resolve turns a contact name or a raw peer ID string into a peer ID.
func (s *Store) Resolve(nameOrID string) (peer.ID, error) {
	if id, err := s.contactByName(nameOrID); err == nil {
		return id, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	id, err := peer.Decode(nameOrID)
	if err != nil {
		return "", fmt.Errorf("%q is neither a contact nor a valid peer ID", nameOrID)
	}
	return id, nil
}

// NameOf returns the contact name for id, or "" if it is not a contact.
func (s *Store) NameOf(id peer.ID) string {
	var name string
	s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(contactsBucket).Get([]byte(id)); v != nil {
			if pt, err := s.open(v, []byte(id)); err == nil {
				name = string(pt)
			}
		}
		return nil
	})
	return name
}

// AppendMessage records a sent or received message.
func (s *Store) AppendMessage(e Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket(messagesBucket).CreateBucketIfNotExists([]byte(e.Peer))
		if err != nil {
			return err
		}
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], seq)
		sealed, err := s.seal(data, []byte(e.Peer))
		if err != nil {
			return err
		}
		return b.Put(key[:], sealed)
	})
}

// History returns up to limit of the most recent messages with id, oldest first.
// A limit <= 0 returns everything.
func (s *Store) History(id peer.ID, limit int) ([]Entry, error) {
	var out []Entry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(messagesBucket).Bucket([]byte(id))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			pt, err := s.open(v, []byte(id))
			if err != nil {
				return err
			}
			var e Entry
			if err := json.NewDecoder(bytes.NewReader(pt)).Decode(&e); err != nil {
				return err
			}
			e.Peer = id
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return nil
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}
