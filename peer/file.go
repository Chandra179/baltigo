package peer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Chandra179/sabaody/wire"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// FILE_PROTOCOL transfers files directly between two peers.
	FILE_PROTOCOL = "/private-chat/file/1.0.0"

	fileOfferVersion = 1
	fileDomain       = "p2pchat-file-v1"
	maxFrameSize     = 4 << 10 // offers and replies
	maxFileNameLen   = 100

	// DefaultMaxFileSize is the largest file we accept unless Options say otherwise.
	DefaultMaxFileSize = 100 << 20
	// maxConcurrentReceives bounds simultaneous incoming transfers.
	maxConcurrentReceives = 2

	fileIdleTimeout = 30 * time.Second
)

// FileOffer is a signed announcement of a file the sender is about to send.
// The hash lets the receiver verify the content, and the signature ties the
// name, size and hash to the sender and recipient.
type FileOffer struct {
	Version   int    `json:"v"`
	From      string `json:"from"`
	To        string `json:"to"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"` // hex
	Timestamp int64  `json:"ts"`     // unix milliseconds
	Sig       []byte `json:"sig"`
}

// fileReply answers an offer (accept or refuse) and, after the transfer, reports the outcome.
type fileReply struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func (o *FileOffer) signedBytes() []byte {
	b := []byte(fileDomain)
	for _, s := range []string{o.From, o.To, o.Name, o.SHA256} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	b = binary.BigEndian.AppendUint64(b, uint64(o.Size))
	return binary.BigEndian.AppendUint64(b, uint64(o.Timestamp))
}

// NewSignedFileOffer creates an offer from `from` to `to`, signed with priv.
func NewSignedFileOffer(priv crypto.PrivKey, from, to peer.ID, name string, size int64, sum [sha256.Size]byte) (*FileOffer, error) {
	o := &FileOffer{
		Version:   fileOfferVersion,
		From:      from.String(),
		To:        to.String(),
		Name:      name,
		Size:      size,
		SHA256:    hex.EncodeToString(sum[:]),
		Timestamp: time.Now().UnixMilli(),
	}
	sig, err := priv.Sign(o.signedBytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign file offer: %w", err)
	}
	o.Sig = sig
	return o, nil
}

// Verify checks the offer is well formed, addressed to self and signed by remote.
func (o *FileOffer) Verify(self, remote peer.ID, now time.Time) error {
	if o.Version != fileOfferVersion {
		return fmt.Errorf("unsupported file offer version %d", o.Version)
	}
	if len(o.SHA256) != 2*sha256.Size {
		return errors.New("invalid file hash")
	}
	if _, err := hex.DecodeString(o.SHA256); err != nil {
		return errors.New("invalid file hash")
	}
	return verifySigned(o.From, o.To, self, remote, o.Timestamp, now, maxClockSkew, o.signedBytes(), o.Sig)
}

// safeFileName reduces a name chosen by the sender to a plain file name that
// cannot point outside the download directory.
func safeFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == ':' {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if len(name) > maxFileNameLen {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		stem := strings.TrimSuffix(name, ext)
		for len(stem) > maxFileNameLen-len(ext) {
			_, size := utf8.DecodeLastRuneInString(stem)
			stem = stem[:len(stem)-size]
		}
		name = stem + ext
	}
	if name == "" {
		return "file"
	}
	return name
}

// idleReader / idleWriter refresh the stream deadline on every operation, so a
// transfer may take as long as it likes provided data keeps moving.
type idleReader struct {
	s network.Stream
	r io.Reader
}

func (d idleReader) Read(b []byte) (int, error) {
	d.s.SetReadDeadline(time.Now().Add(fileIdleTimeout))
	return d.r.Read(b)
}

type idleWriter struct{ s network.Stream }

func (d idleWriter) Write(b []byte) (int, error) {
	d.s.SetWriteDeadline(time.Now().Add(fileIdleTimeout))
	return d.s.Write(b)
}

func (p *PeerInfo) maxFileSize() int64 {
	if p.MaxFileSize > 0 {
		return p.MaxFileSize
	}
	return DefaultMaxFileSize
}

func (p *PeerInfo) downloadDir() string {
	if p.DownloadDir != "" {
		return p.DownloadDir
	}
	return "downloads"
}

// fileHandler registers the stream handler that receives files.
func (p *PeerInfo) fileHandler() {
	p.fileSlots = make(chan struct{}, maxConcurrentReceives)
	p.Host.SetStreamHandler(FILE_PROTOCOL, func(s network.Stream) {
		defer s.Close()
		remote := s.Conn().RemotePeer()
		if err := p.receiveFile(s, remote); err != nil {
			log.Printf("File from %s not received: %v", remote, err)
			// After a refusal, close gracefully so the reason reaches the sender;
			// a reset could discard it. Other failures abort the stream.
			if !errors.As(err, new(errRefused)) {
				s.Reset()
			}
		}
	})
}

// errRefused marks a transfer we turned down after telling the sender why.
type errRefused struct{ reason string }

func (e errRefused) Error() string { return e.reason }

func refuse(s network.Stream, reason string) error {
	wire.Write(s, fileReply{Reason: reason}, maxFrameSize)
	return errRefused{reason}
}

func (p *PeerInfo) receiveFile(s network.Stream, remote peer.ID) error {
	s.SetReadDeadline(time.Now().Add(readTimeout))
	r := bufio.NewReader(s)
	var offer FileOffer
	if err := wire.Read(r, &offer, maxFrameSize); err != nil {
		return fmt.Errorf("bad offer: %w", err)
	}
	if err := offer.Verify(p.Host.ID(), remote, time.Now()); err != nil {
		return refuse(s, "invalid offer: "+err.Error())
	}
	if !p.allow.Allows(remote) {
		return refuse(s, "sender is not allowed")
	}
	if p.Store == nil || p.Store.NameOf(remote) == "" {
		return refuse(s, "sender is not in the recipient's contacts")
	}
	if offer.Size <= 0 {
		return refuse(s, "empty file")
	}
	if offer.Size > p.maxFileSize() {
		return refuse(s, fmt.Sprintf("file too large (%d bytes, limit %d)", offer.Size, p.maxFileSize()))
	}
	select {
	case p.fileSlots <- struct{}{}:
		defer func() { <-p.fileSlots }()
	default:
		return refuse(s, "busy, too many transfers in progress")
	}

	dir := p.downloadDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return refuse(s, "recipient cannot store files")
	}
	name := safeFileName(offer.Name)
	part, dest, err := reserveFile(dir, name)
	if err != nil {
		return refuse(s, "recipient cannot store files")
	}
	partPath := part.Name()
	committed := false
	defer func() {
		part.Close()
		if !committed {
			os.Remove(partPath)
		}
	}()

	if err := wire.Write(s, fileReply{OK: true}, maxFrameSize); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.CopyN(io.MultiWriter(part, h), idleReader{s, r}, offer.Size)
	if err != nil {
		return fmt.Errorf("transfer interrupted after %d of %d bytes: %w", n, offer.Size, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != offer.SHA256 {
		return refuse(s, "checksum mismatch")
	}
	if err := part.Close(); err != nil {
		return refuse(s, "recipient cannot store files")
	}
	final, err := commitFile(partPath, dest)
	if err != nil {
		return refuse(s, "recipient cannot store files")
	}
	committed = true
	s.SetWriteDeadline(time.Now().Add(readTimeout))
	wire.Write(s, fileReply{OK: true}, maxFrameSize) // best effort: the file is already safe

	p.recordFile(remote, true, filepath.Base(final), offer.Size)
	if p.OnFile != nil {
		p.OnFile(remote, final, offer.Size)
	}
	return nil
}

// reserveFile picks a destination name inside dir that does not exist yet and
// opens a private ".part" file for it. Existing files are never overwritten.
func reserveFile(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		dest := filepath.Join(dir, candidate)
		if _, err := os.Lstat(dest); err == nil {
			continue
		}
		f, err := os.OpenFile(dest+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return f, dest, nil
	}
	return nil, "", errors.New("too many files with this name")
}

// commitFile moves the finished ".part" file to dest without replacing an
// existing file.
func commitFile(partPath, dest string) (string, error) {
	if err := os.Link(partPath, dest); err != nil {
		return "", err
	}
	os.Remove(partPath)
	return dest, nil
}

func (p *PeerInfo) recordFile(id peer.ID, incoming bool, name string, size int64) {
	verb := "sent"
	if incoming {
		verb = "received"
	}
	p.record(id, incoming, false, time.Now(), 0, fmt.Sprintf("[file] %s %s (%d bytes)", verb, name, size))
}

// SendFile sends the file at path directly to target. Files are never sent
// through the relay, so a direct connection (possibly via hole punching) is
// required. The recipient must have us as a contact.
func (p *PeerInfo) SendFile(ctx context.Context, target peer.ID, path string) error {
	priv := p.Host.Peerstore().PrivKey(p.Host.ID())
	if priv == nil {
		return errors.New("no private key for local peer")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if st.Size() == 0 {
		return errors.New("file is empty")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	offer, err := NewSignedFileOffer(priv, p.Host.ID(), target, filepath.Base(path), st.Size(), sum)
	if err != nil {
		return err
	}
	if err := p.transferFile(ctx, target, offer, f); err != nil {
		return err
	}
	p.recordFile(target, false, offer.Name, offer.Size)
	return nil
}

// transferFile runs the sender side of the protocol: offer, wait for the
// reply, stream the bytes, wait for the verdict.
func (p *PeerInfo) transferFile(ctx context.Context, target peer.ID, offer *FileOffer, body io.Reader) error {
	// No WithAllowLimitedConn: a relayed connection is capped at ~128 KiB, so we insist on a direct one.
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s, err := p.Host.NewStream(dialCtx, target, FILE_PROTOCOL)
	if err != nil {
		return fmt.Errorf("no direct connection to peer (files are not sent through the relay): %w", err)
	}
	defer s.Close()

	s.SetWriteDeadline(time.Now().Add(readTimeout))
	if err := wire.Write(s, offer, maxFrameSize); err != nil {
		return err
	}
	r := bufio.NewReader(s)
	readReply := func(timeout time.Duration) error {
		s.SetReadDeadline(time.Now().Add(timeout))
		var reply fileReply
		if err := wire.Read(r, &reply, maxFrameSize); err != nil {
			return fmt.Errorf("no answer from peer: %w", err)
		}
		if !reply.OK {
			return fmt.Errorf("peer refused the file: %s", reply.Reason)
		}
		return nil
	}
	if err := readReply(readTimeout); err != nil {
		return err
	}
	n, err := io.Copy(idleWriter{s}, io.LimitReader(body, offer.Size))
	if err != nil {
		s.Reset()
		return fmt.Errorf("transfer interrupted: %w", err)
	}
	if n != offer.Size {
		// e.g. the file shrank after it was hashed; the receiver is still waiting for the rest
		s.Reset()
		return fmt.Errorf("file changed during transfer (sent %d of %d bytes)", n, offer.Size)
	}
	return readReply(fileIdleTimeout)
}
