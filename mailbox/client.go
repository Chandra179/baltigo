package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"p2p/wire"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

func call(ctx context.Context, h host.Host, relay peer.ID, fn func(do func(Request) (Response, error)) error) error {
	st, err := h.NewStream(ctx, relay, Protocol)
	if err != nil {
		return fmt.Errorf("cannot reach mailbox: %w", err)
	}
	defer st.Close()
	if dl, ok := ctx.Deadline(); ok {
		st.SetDeadline(dl)
	} else {
		st.SetDeadline(time.Now().Add(sessionTimeout))
	}
	r := wire.NewReader(st)
	do := func(req Request) (Response, error) {
		var resp Response
		if err := wire.Write(st, req, maxRequest); err != nil {
			return resp, err
		}
		if err := wire.Read(r, &resp, maxResponse); err != nil {
			return resp, err
		}
		if !resp.OK {
			return resp, errors.New(resp.Error)
		}
		return resp, nil
	}
	return fn(do)
}

// Put leaves a sealed blob for recipient `to` on the relay's mailbox.
func Put(ctx context.Context, h host.Host, relay, to peer.ID, blob []byte) error {
	return call(ctx, h, relay, func(do func(Request) (Response, error)) error {
		_, err := do(Request{Op: "put", To: to.String(), Blob: blob})
		return err
	})
}

// Fetch collects the local peer's waiting messages. handle is called for each
// blob and returns true once the blob has been dealt with (delivered or
// permanently unusable), which acknowledges it so the relay deletes it. It
// returns how many blobs were acknowledged.
func Fetch(ctx context.Context, h host.Host, relay peer.ID, handle func(blob []byte) bool) (int, error) {
	total := 0
	err := call(ctx, h, relay, func(do func(Request) (Response, error)) error {
		for {
			resp, err := do(Request{Op: "fetch"})
			if err != nil {
				return err
			}
			if len(resp.Items) == 0 {
				return nil
			}
			var acked []uint64
			for _, it := range resp.Items {
				if handle(it.Blob) {
					acked = append(acked, it.ID)
				}
			}
			if len(acked) == 0 {
				return nil // nothing progressed; avoid refetching the same blobs forever
			}
			if _, err := do(Request{Op: "ack", IDs: acked}); err != nil {
				return err
			}
			total += len(acked)
		}
	})
	return total, err
}
