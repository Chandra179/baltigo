# P2P Chat Application

A decentralized peer-to-peer chat application built with libp2p that supports direct connections, relay connections, and DHT-based peer discovery. Docs: https://nothin.gitbook.io/computing/p2p-chat

![Screenshot](./test.png)

## Features

- **Direct P2P Messaging**: Send messages directly between peers using custom protocol
- **DHT Discovery**: Discover peers using Kademlia DHT with automatic bootstrap
- **NAT Traversal**: Built-in hole punching and NAT port mapping for firewall bypass
- **Circuit Relay**: Support for relay nodes to facilitate connections between NAT'd peers
- **Peer Store**: In-memory peer management with address caching
- **Signed Messages**: Every message is signed with the sender's Ed25519 key and verified (sender, recipient, clock skew, replay) on receipt
- **Access Control**: Optional peer allowlist and relay allowlist
- **Offline Messages**: If a peer is unreachable, the message is sealed end-to-end (X25519 key agreement with a NaCl box (XSalsa20-Poly1305), fresh ephemeral key per message) and left in the relay's mailbox for up to 7 days; the relay cannot read it
- **Delivery Receipts**: Recipients return a signed receipt (live or via the mailbox); `history` shows ✓ delivered
- **Group Chat**: GossipSub groups; a name plus shared secret defines the group, messages are encrypted with a key derived from the secret, and members find each other through the private DHT
- **File Transfer**: Direct peer-to-peer transfers with signed offers, SHA-256 verification, size limit and sanitized file names
- **Contacts & History**: Named contacts and message history, stored in an embedded database with names and text encrypted using a key derived from your identity key

## Configuration

### Environment Variables

Create a `.env` file in the project root with the following configuration:

```bash
# Relay server configuration
RELAY_IP=99.0.0.5
RELAY_TCP_PORT=2222   # the relay listens on this port for both TCP and UDP (QUIC)
RELAY_ID=<base64 private key of the relay>

# Peer identity (base64 private key, see `genkey`)
PEER_ID=<base64 private key of the peer>

# Optional
PEER_ALLOWLIST=12D3KooW...,12D3KooW...   # only these peers may connect to you (empty = anyone)
RELAY_ALLOWLIST=12D3KooW...,12D3KooW... # only these peers may use the relay (empty = open relay)
SEND_RECEIPTS=false                     # do not send delivery receipts (default: send)
PEER_DOWNLOAD_DIR=downloads             # where received files are saved (default downloads)
PEER_MAX_FILE_MB=100                    # largest file accepted (default 100)
PEER_DB=peers_db/me.db                  # contacts/history database (default peers_db/<peer id>.db)
RELAY_DB=relay_db/mailbox.db            # relay mailbox database (default relay_db/mailbox.db)
```

### Configuration Parameters

- **RELAY_IP**: IP address of the relay server (publicly accessible)
- **RELAY_TCP_PORT**: TCP port for relay server
- **RELAY_ID**: Peer ID of the relay server (generated from relay's private key)

## Usage

### Running as a Relay Server

```bash
./p2p-chat --mode relay
```

### Running as a Peer

```bash
./p2p-chat --mode peer
```

## CLI Commands

Once running in peer mode, you can use the following commands:

### Key Management
- `genkey` - Generate a new Ed25519 private key (use this to derive peer IDs)
- `help` - Show all available commands

> **Note**: The `genkey` command generates the private key that determines your peer ID. Save this key securely as it represents your peer's identity on the network.

### Peer Discovery
- `dht` - Discover peers via DHT and advertise yourself
- `list` - List all discovered peers
- `find <peer_id>` - Find addresses for a specific peer

### Connection Management
- `ping <peer_id> <peer_addr>` - Test connectivity to a peer
- `con <name|peer_id>` - Connect to a peer (tries direct, falls back to relay)

### Contacts and history
- `contact [list]` - List saved contacts
- `contact add <name> <peer_id>` - Save a contact (names are unique, no spaces)
- `contact rm <name>` - Remove a contact (history is kept)
- `history <name|peer_id> [n]` - Show the last n messages (default 20)

### Groups
- `group join <name> [secret]` - Join (or create) a group. Everyone using the same name **and** secret is in the same group
- `group say <name> <message>` - Send to a group
- `group list` / `group peers <name>` / `group history <name> [n]` / `group leave <name>`

### Files
- `sendfile <name|peer_id> <path>` - Send a file directly to a peer

### Messaging
- `send <name|peer_id> <message>` - Send a signed message to a peer. If the peer is offline it is queued on the relay
- `sync` - Check the relay for messages received while you were offline (also done at startup and every 30 seconds)

## Example Workflow

### Initial Setup

1. **Generate keys for relay server**:
   ```bash
   ./p2p-chat --mode peer
   > genkey
   Generated key: CAESIBx7hGtQvnKCqGnNlLzKHgVXQZXPQ3vBJ6VGpJLGnNlZ
   ```
   Save this key `RELAY_ID` for your relay server configuration.

2. **Generate keys for peer**:
   ```bash
   ./p2p-chat --mode peer
   > genkey  
   Generated key: CAESIDy8iHuRwoMDrHoOmMmLKhgWYRZYQ4wCK7WHqKMHoOmM
   ```
   Save this key `PEER_ID` for your peer configuration.

3. **Set up relay server**:
   ```bash
   # Configure relay with generated key
   ./p2p-chat --mode relay
   ```

### Peer Communication

1. **Start a peer**:
   ```bash
   ./p2p-chat --mode peer
   Peer initialized - ID: 12D3KooWExamplePeerID, Protocols: [/private-chat/2.0.0]
   Successfully connected to relay: 12D3KooWExampleRelayID
   ```

2. **Discover peers via DHT**:
   ```
   > dht
   Successfully advertised. TTL: 24h0m0s
   Total found peers (excluding self): 3
   ```

3. **List discovered peers**:
   ```
   > list
   [12D3KooW... 12D3KooX... 12D3KooY...]
   ```

4. **Connect to a peer**:
   ```
   > con 12D3KooWExampleTargetPeer
   success direct connection
   Remote supports: [/private-chat/2.0.0 /libp2p/circuit/relay/0.2.0/hop]
   Successfully connected to peer: 12D3KooWExampleTargetPeer
   ```

5. **Send a message**:
   ```
   > send 12D3KooWExampleTargetPeer Hello, this is a test message!
   ```

6. **Receive messages**:
   ```
   Hello, this is a test message!
   ```

## Resources

- [libp2p Documentation](https://docs.libp2p.io/)
- [Kademlia DHT Specification](https://pdos.csail.mit.edu/~petar/papers/maymounkov-kademlia-lncs.pdf)
- [Circuit Relay Specification](https://github.com/libp2p/specs/tree/master/relay)

## Offline delivery and its limits

- Messages to an unreachable peer are signed by you, sealed to the recipient's public key and stored on the relay. The recipient verifies the signature, checks the message is addressed to them and drops duplicates and replays.
- The relay can see who leaves a message for whom and when, but not the contents.
- Each recipient has at most 200 waiting messages of up to 64 KiB, kept for 7 days. Set `RELAY_ALLOWLIST` so only your own peers can use the mailbox.
- Sealing uses the recipient's long-term key, so someone who later steals that key could read captured mailbox traffic. Live messages are protected by the transport handshake.
- Only Ed25519 identities (the `genkey` default) can receive offline messages.

## Group chat notes

- The GossipSub topic is a hash of the name and secret, and every message is encrypted with XChaCha20-Poly1305 using a key derived from the secret (argon2id). Without a secret the group is effectively public: anyone who knows the name can read it. Peers that join with the wrong secret end up in a different, empty group.
- The author of each message is verified by GossipSub's signature check. Messages older than 5 minutes and replayed ciphertexts are dropped.
- Members find each other through the private DHT, so you must be able to reach the relay. Group traffic needs direct connections between members; it does not go through the relay.
- GossipSub can drop a message sent in the first moments after a new member appears, while connections are still being set up.
- Groups are not remembered across restarts: join again after starting. New members do not see earlier messages.
- Anyone who knows the secret can read the group, including after they leave. Change the secret to start a new group.

## File transfer notes

- Files are sent straight to the recipient and never through the relay (relayed connections are capped at about 128 KiB). If hole punching fails, the transfer fails with a clear error.
- The recipient only accepts files from peers in their contacts (and on their allowlist, if set), up to `PEER_MAX_FILE_MB`. The offer is signed, the SHA-256 is checked at the end, and a file that does not match is deleted.
- File names chosen by the sender are reduced to a plain name, so nothing can be written outside the download directory. Existing files are never overwritten (`name (1).ext`). Received files are saved with mode 0600.
