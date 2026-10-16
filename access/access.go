// Package access implements peer allowlists for inbound connections and relay use.
package access

import (
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// Allowlist is a set of permitted peers. An empty list permits everyone.
type Allowlist map[peer.ID]struct{}

// ParseList parses a comma-separated list of peer IDs.
func ParseList(s string) ([]peer.ID, error) {
	var ids []peer.ID
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := peer.Decode(part)
		if err != nil {
			return nil, fmt.Errorf("invalid peer id %q: %w", part, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func NewAllowlist(ids ...peer.ID) Allowlist {
	a := Allowlist{}
	for _, id := range ids {
		a[id] = struct{}{}
	}
	return a
}

// Allows reports whether id is permitted.
func (a Allowlist) Allows(id peer.ID) bool {
	if len(a) == 0 {
		return true
	}
	_, ok := a[id]
	return ok
}

// Gater rejects inbound connections from peers outside the allowlist.
// Outbound dials are left to the user.
type Gater struct {
	allow Allowlist
}

// NewGater builds a Gater. alwaysAllow (e.g. the relay) is permitted in addition to ids.
// With no ids, all peers are allowed.
func NewGater(ids []peer.ID, alwaysAllow ...peer.ID) *Gater {
	if len(ids) == 0 {
		return &Gater{allow: Allowlist{}}
	}
	return &Gater{allow: NewAllowlist(append(ids, alwaysAllow...)...)}
}

func (g *Gater) InterceptPeerDial(peer.ID) bool               { return true }
func (g *Gater) InterceptAddrDial(peer.ID, ma.Multiaddr) bool { return true }
func (g *Gater) InterceptAccept(network.ConnMultiaddrs) bool  { return true }
func (g *Gater) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) {
	return true, 0
}

// InterceptSecured runs once the remote peer ID is authenticated.
func (g *Gater) InterceptSecured(dir network.Direction, id peer.ID, _ network.ConnMultiaddrs) bool {
	if dir == network.DirOutbound {
		return true
	}
	return g.allow.Allows(id)
}

// RelayACL restricts who may reserve a slot on, or be reached through, the relay.
type RelayACL struct {
	allow Allowlist
}

func NewRelayACL(ids []peer.ID) *RelayACL {
	return &RelayACL{allow: NewAllowlist(ids...)}
}

func (r *RelayACL) AllowReserve(p peer.ID, _ ma.Multiaddr) bool {
	return r.allow.Allows(p)
}

func (r *RelayACL) AllowConnect(src peer.ID, _ ma.Multiaddr, dest peer.ID) bool {
	return r.allow.Allows(src) && r.allow.Allows(dest)
}
