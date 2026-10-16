# Sabaody

Private peer-to-peer chat for the terminal, built on [libp2p](https://libp2p.io/). Peers talk directly when they can and through a small public relay when they cannot. There are no accounts and no central server: your identity is a key pair, and the relay never sees message contents.

See [architecture.md](architecture.md) for how it works and what each part can and cannot see.

## Features

- **Direct messaging** between peers, with NAT traversal (hole punching) and a relay fallback
- **Signed messages**: every message is signed with the sender's Ed25519 key and checked on receipt
- **Offline messages**: if a peer is unreachable, the message is sealed end to end and left on the relay for up to 7 days
- **Delivery receipts**: signed acknowledgements; `history` shows ✓ delivered
- **Group chat**: a group is a name plus a shared secret; messages are encrypted with a key derived from the secret
- **File transfer**: direct transfers with signed offers and SHA-256 verification
- **Contacts and history**: stored locally in an encrypted database
- **Access control**: optional allowlists for peers and for the relay

## Requirements

Go 1.27 or newer (see `go.mod`). One machine with a public IP runs the relay; a small VPS is enough. Open its port for **both TCP and UDP** (UDP carries QUIC).

## Quick start

### 1. Create keys

Every node needs a key pair. This needs no configuration:

```bash
go run . --genkey
```

```text
Private key (keep secret, goes in PEER_ID or RELAY_ID):
  CAESQ...
Peer ID (public, safe to share):
  12D3KooW...
```

Run it once for the relay and once for each peer.

### 2. Start the relay

On the public machine, create a `.env` file:

```bash
RELAY_ID=<the relay's private key>
RELAY_IP=<the relay's public IP>
RELAY_TCP_PORT=4001
```

```bash
go run . --mode relay
```

It prints the setting every peer needs:

```text
Relay started. Give peers this setting:
  RELAY_PEER_ID=12D3KooW...
```

### 3. Start a peer

On each peer, create a `.env` file. Peers only need the relay's **public** ID, never its private key:

```bash
PEER_ID=<this peer's private key>
RELAY_PEER_ID=<printed by the relay>
RELAY_IP=<the relay's public IP>
RELAY_TCP_PORT=4001
```

```bash
go run . --mode peer
```

### 4. Chat

Exchange peer IDs out of band (the ID is printed at startup), add each other as contacts, then discover and send:

```text
> contact add bob 12D3KooW...
Added contact bob
> dht
Successfully advertised. TTL: 3h0m0s
Total found peers (excluding self): 1
> ping bob
Reply from bob: time=762µs
> send bob hello
> history bob
2026-10-16 19:40:44  me: hello ✓ delivered
```

Incoming messages appear as `[bob] hello`. If Bob is offline, `send` queues the message on the relay and prints a note; Bob receives it when he next starts.

## Configuration

Settings come from the environment or a `.env` file in the working directory.

| Variable | Used by | Description |
|---|---|---|
| `RELAY_IP` | both | Public IP of the relay |
| `RELAY_TCP_PORT` | both | Relay port, used for TCP and for UDP (QUIC) |
| `RELAY_ID` | relay | The relay's private key |
| `RELAY_PEER_ID` | peers | The relay's public peer ID |
| `PEER_ID` | peers | This peer's private key |
| `PEER_ALLOWLIST` | peers | Comma-separated peer IDs allowed to connect to you and to send you offline messages (empty = anyone) |
| `RELAY_ALLOWLIST` | relay | Comma-separated peer IDs allowed to use the relay and mailbox (empty = open relay) |
| `SEND_RECEIPTS` | peers | `false` to stop sending delivery receipts (default: send) |
| `PEER_DOWNLOAD_DIR` | peers | Where received files are saved (default `downloads`) |
| `PEER_MAX_FILE_MB` | peers | Largest file accepted, in MiB (default 100) |
| `PEER_DB` | peers | Contacts and history database (default `peers_db/<peer id>.db`) |
| `RELAY_DB` | relay | Mailbox database (default `relay_db/mailbox.db`) |

Peers listen on random ports. Older setups that put the relay's private key in a peer's `RELAY_ID` still work but print a warning; switch to `RELAY_PEER_ID`.

## Commands

Type `help` in peer mode to list them.

| Command | What it does |
|---|---|
| `contact [list]`, `contact add <name> <peer_id>`, `contact rm <name>` | Manage contacts (names are unique, no spaces; history is kept when you remove one) |
| `send <name\|peer_id> <message>` | Send a signed message; queued on the relay if the peer is offline |
| `history <name\|peer_id> [n]` | Show the last n messages (default 20) |
| `sync` | Collect messages left for you on the relay (also done at startup and every 30 seconds) |
| `sendfile <name\|peer_id> <path>` | Send a file directly to a contact |
| `group join <name> [secret]`, `group say <name> <message>`, `group list`, `group peers <name>`, `group history <name> [n]`, `group leave <name>` | Group chat |
| `dht` | Advertise yourself and discover peers |
| `con <name\|peer_id>` | Connect to a peer (direct, else via the relay) |
| `ping <name\|peer_id>` | Measure round-trip time |
| `list`, `find <peer_id>` | Show known peers and a peer's addresses |
| `genkey` | Generate a new identity key |
| `exit` | Quit (Ctrl-D and Ctrl-C also exit cleanly) |

## Things to know

**Offline messages**
- The relay can see who leaves a message for whom and when, but not the contents.
- Each recipient has at most 200 waiting messages of up to 64 KiB, kept for 7 days.
- Set `RELAY_ALLOWLIST` so only your own peers can use the relay and mailbox.
- Only Ed25519 identities (what `--genkey` creates) can receive offline messages.
- Sealing uses the recipient's long-term key: someone who later steals that key could read mailbox traffic they captured earlier.

**Group chat**
- Everyone who uses the same name **and** secret is in the same group. With no secret, anyone who knows the name can read it.
- Members find each other through the private DHT, so the relay must be reachable. Group traffic goes directly between members, not through the relay.
- A message sent in the first moments after a new member appears can be dropped while connections are still being set up.
- Groups are not remembered across restarts: join again after starting. New members do not see earlier messages.
- Anyone who knows the secret can read the group, including after they leave. Change the secret to start a new group.

**File transfer**
- Files are never sent through the relay (relayed connections are capped at about 128 KiB). If hole punching fails, the transfer fails with a clear error.
- The recipient only accepts files from their contacts (and their allowlist, if set), up to `PEER_MAX_FILE_MB`.
- File names chosen by the sender are reduced to a plain name; existing files are never overwritten (`name (1).ext`). Received files are saved with mode 0600.

**Local data**
- Contact names and message text are encrypted in the database with a key derived from your identity key. This protects the file on its own, not against someone who also has your `.env`. If you change `PEER_ID`, the old data cannot be read.
- Only one process can open a database at a time.

## Development

```bash
make build   # bin/sabaody
make test    # go test -race ./...
make vet
make lint    # needs golangci-lint
make peer    # go run . --mode=peer
make relay   # go run . --mode=relay
```

The module path is `github.com/Chandra179/sabaody`. Tests start real libp2p nodes on localhost and need no network access.

## Resources

- [libp2p](https://libp2p.io/) and its [specifications](https://github.com/libp2p/specs)
- [Circuit relay v2 specification](https://github.com/libp2p/specs/blob/master/relay/circuit-v2.md)
