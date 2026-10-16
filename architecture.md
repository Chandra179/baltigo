# Baltigo: Private Peer-to-Peer Chat

Terminal chat where peers talk directly when they can and through a small public relay when they cannot. Identity is a key pair, there are no accounts, and the relay never sees message contents.

## Algorithms and approach

```text
Identity (Ed25519 key pair; peer ID = hash of the public key)
   |
   v
Connect --- direct (TCP / QUIC) ------------------------------+
   |                                                          |
   +-- behind a NAT -> reserve a slot on the relay            |
   |                   -> relayed circuit (short, capped)     |
   |                   -> hole punching upgrades it to direct-+
   v
Discover (private Kademlia DHT, bootstrapped from our relay)
   |
   v
Message (signed, then carried over an encrypted connection)
   |
   +-- peer reachable   -> deliver live
   +-- peer unreachable -> seal end to end -> relay mailbox -> peer collects later
   |
   v
Receipt (signed acknowledgement, live or via the mailbox)
```

1. **Identify:** Each node has an Ed25519 key pair. The peer ID is derived from the public key, so a peer ID proves possession of the key.
2. **Connect:** Peers dial each other over TCP or QUIC. Every connection is encrypted and authenticated with Noise or TLS 1.3, and the remote peer ID is verified during the handshake.
3. **Traverse NATs:** A peer behind a NAT reserves a slot on the public relay (circuit relay v2). Others reach it through a relayed circuit, then a hole-punching exchange (DCUtR) tries to upgrade the path to a direct connection.
4. **Discover:** Peers advertise themselves in a private Kademlia DHT and look each other up there. The relay is the bootstrap node, so the public IPFS network is never used.
5. **Message:** Each message is signed by its author on top of the encrypted connection. The receiver checks the sender, the recipient, the clock, the signature and replays.
6. **Fall back:** If the recipient cannot be reached, the message is sealed to the recipient's key and stored on the relay until the recipient collects it.
7. **Acknowledge:** The recipient returns a signed receipt, which the sender records as "delivered".

## Connectivity

```text
Peer A (behind NAT)                  Relay (public IP)                  Peer B (behind NAT)
   |-- reserve a slot ----------------->|                                  |
   |                                    |<----------------- reserve a slot-|
   |<============ encrypted circuit through the relay =====================>|
   |                                    |                                  |
   |<-- exchange observed addresses over the circuit ---------------------->|
   |== both dial each other at the same moment (hole punch) ===============>|
   |<============== direct encrypted connection ===========================>|
        (the circuit is dropped once the direct path works)
```

- **Relay as rendezvous, not as middleman:** The relay is a public meeting point. A relayed circuit is end-to-end encrypted between the two peers, so the relay forwards bytes it cannot read.
- **Limited circuits:** Relayed connections are deliberately short and small (a couple of minutes and roughly 128 KiB). They are enough to talk and to coordinate hole punching, not to move files.
- **Transports:** TCP and QUIC share one port number on the relay, so a peer can use whichever its network allows.

## Discovery

- **Private DHT:** The DHT uses its own protocol name, so it never mixes with the public IPFS DHT. The relay runs as a DHT server and is the only bootstrap address a peer needs.
- **Rendezvous:** To be found, a peer publishes a provider record under a rendezvous string; to find others, it looks that string up. The chat uses one string, and each group uses a hashed topic string.
- **Consequence:** A new peer needs exactly one piece of out-of-band information: the relay's address and public ID.

## Messaging

```text
author:   signature = Ed25519( domain tag | sender | recipient | text | sequence | timestamp )
receiver: sender == authenticated connection peer?        no -> reject
          recipient == me?                                no -> reject
          |now - timestamp| within allowed window?        no -> reject
          signature valid for the sender's public key?    no -> reject
          sequence higher than the last from this sender? no -> reject (replay)
          -> deliver, store, send receipt
```

- **Two layers:** The encrypted connection protects the content in transit and authenticates the peer. The signature is end-to-end and survives storage and forwarding, which is what lets the same message be delivered through the mailbox.
- **Domain separation:** A fixed tag in front of every signed object ensures a signature made for a message can never be replayed as a receipt or a file offer.
- **Binding to the recipient:** The recipient is inside the signed data, so a captured message cannot be redirected to someone else.
- **Replay defence:** Sequence numbers come from the clock and only ever increase. The receiver keeps the highest value seen per sender while online, and a persistent set of seen messages for mailbox deliveries. A timestamp window bounds how old a message can be.

## Offline delivery

```text
Sender                          Relay mailbox                          Recipient
  |  recipient unreachable                                                  |
  |  convert recipient Ed25519 key -> X25519                                |
  |  fresh ephemeral X25519 key; box = seal(signed message)                 |
  |-- put(recipient, blob) ------>|  per-recipient queue, size and count    |
  |                               |  limits, expiry after 7 days            |
  |                               |<----- fetch (at start, every 30 s) -----|
  |                               |------ blobs ----------------------------|
  |                               |   open -> verify -> de-duplicate -> store
  |                               |<----- acknowledge ids ------------------|
  |                               |  delete only after acknowledgement      |
```

- **Sealed box with an anonymous sender:** The sender uses a fresh ephemeral key for every message and throws it away. The relay sees an opaque blob, and not even the sender can decrypt it afterwards. Authorship is proven by the signature inside, not by the box.
- **Delete after acknowledgement:** A blob stays on the relay until the recipient confirms it, so a crash between fetching and saving loses nothing. A blob that can never be used (bad encryption, forged signature, too old) is acknowledged and dropped so it does not block the queue.
- **At-least-once with idempotent receivers:** A message can arrive twice (for example live and through the mailbox). The persistent seen set makes processing idempotent, so the recipient shows it once.
- **Quotas and access:** Each recipient has a fixed number of waiting messages of bounded size, and the relay can restrict the mailbox to an allowlist of peers.

## Delivery receipts

- **What it proves:** A receipt is signed by the recipient over the sender, the recipient and the message's sequence number. It proves the recipient received that message without repeating its content.
- **Two paths:** It travels live when the sender is reachable, and otherwise sealed through the mailbox, exactly like a message.
- **Idempotent:** Marking a message delivered twice changes nothing, so duplicates and retries are harmless. A receipt that does not match one of our own messages is ignored, and a forged one fails the signature check.
- **Optional:** A peer can choose not to send receipts.

## Group chat

```text
STEP 0. SHARE THE SECRET (outside the app, the app never sends it)

   one person picks a group name + secret, e.g. "friends" + "purple-tiger-42"
              |  tells the others in person, by phone, or in another trusted chat
              v
   every member now knows the same name + secret

STEP 1. JOIN (each member's app computes this locally and gets the same two results)

   group name + shared secret
              |
              |  Argon2id  (deliberately slow, so guessing a weak secret is costly)
              v
   GROUP KEY, 32 bytes                       <- stays private; encrypts and decrypts
              |
              |  HKDF  (one-way: the key cannot be recovered from the result)
              v
   TOPIC ID                                  <- a public label for the "room";
                                                reveals neither the name nor the secret

STEP 2. SEND

   message text
        |  encrypt with the GROUP KEY (XChaCha20-Poly1305)
        |  a fresh random nonce is used each time, and the TOPIC ID is
        |  bound in, so the ciphertext only works in this room
        v
   nonce + ciphertext
        |  signed by the author and published on the TOPIC ID
        v
   GossipSub mesh: the members subscribed to that topic pass it along

STEP 3. RECEIVE (a message is dropped at the first check it fails)

   1. Signature valid?        checked by the mesh, so the shown author is real
   2. Decrypts with my key?   fails if the sender used a different secret
   3. Timestamp recent?       old messages are dropped
   4. Nonce not seen before?  a repeated message is dropped
        |
        v
   display and store
```

- **A group is a shared secret:** The name and secret together derive both the encryption key and the topic. A peer with the wrong secret lands on a different, empty topic and cannot read or inject messages into yours.
- **Slow key derivation:** Argon2id makes guessing a weak secret expensive for someone who captures traffic.
- **Authorship:** Each pub/sub message is signed by its author and verified by the mesh before delivery, so the author shown is real. The encrypted payload additionally carries a timestamp.
- **Membership discovery:** Members find each other through the private DHT using the hashed topic as the rendezvous string, then GossipSub builds a mesh among them.
- **Replay defence:** Messages outside a short time window are dropped, and a recently seen nonce is never delivered twice.

## File transfer

```text
Sender                                                        Recipient
  | hash the file (SHA-256)                                      |
  |-- signed offer: name, size, hash, sender, recipient, time -->|
  |                                  checks, in order:           |
  |                                  signature and addressing    |
  |                                  sender allowed?             |
  |                                  sender is a contact?        |
  |                                  size within the limit?      |
  |                                  free transfer slot?         |
  |<-- accept / refuse (with reason) ----------------------------|
  |== stream exactly <size> bytes (idle timeout, direct only) ==>|
  |                                  hash matches the offer?     |
  |                                  commit under a safe name,   |
  |                                  never replacing a file      |
  |<-- verdict --------------------------------------------------|
```

- **Deny by default:** Only contacts may send files, up to a size limit, and only a few transfers run at once.
- **Integrity:** The hash in the signed offer is checked after the transfer. A mismatch or a truncated transfer deletes the partial file.
- **Safe storage:** The sender chooses the name, so it is reduced to a plain file name, and every file operation is confined to the download folder by the operating system, so names and symbolic links cannot reach outside it. Existing files are never overwritten.
- **Direct only:** Relayed circuits are too small for files, so a transfer needs a direct connection and fails clearly if hole punching did not succeed.

## Local storage

- **Contacts and history** live in an embedded key-value database. Contact names and message text are encrypted with XChaCha20-Poly1305 under a key derived from the identity key with HKDF-SHA256. Each record is bound to its owner, so records cannot be moved between conversations.
- **Visible in the file:** Peer IDs and the order of messages. Names, text and group names are not (group names are hashed).
- **The relay's mailbox** stores only sealed blobs, so it needs no encryption of its own.

## What each party can see

| Party | Content | Metadata |
|---|---|---|
| Network observer | Nothing (encrypted transport) | IP addresses, timing, volume |
| Relay | Nothing, for live or offline messages | Which peers are connected, who leaves messages for whom, when, and sizes |
| Other DHT participants | Nothing | Which peers advertise the chat, and hashed group topics |
| Group member (knows the secret) | Everything said in the group | Who is in the group and who wrote what |
| Someone who knows only a group's name | Nothing (the secret is needed) | Nothing |
| Recipient of a file | The file | Its name, size and hash |

## Patterns used

- **Store and forward:** the relay holds sealed messages for peers that are offline.
- **Sealed box with an ephemeral key:** the relay and the sender cannot read stored messages.
- **Signed envelope with a replay window:** authorship survives storage, and old or repeated messages are refused.
- **At-least-once delivery with idempotent receivers:** duplicates are expected and harmless.
- **Acknowledge, then delete:** nothing is removed until the recipient confirms it.
- **Defence in depth:** transport encryption between hops plus end-to-end signatures and sealing.
- **Deny by default:** files only from contacts, mailbox and relay restrictable to an allowlist.
- **Domain separation:** every signed object type has its own tag.
- **Rendezvous through a DHT:** peers find each other by looking up a shared string, with no central directory.

## Limits

- **Hole punching depends on the NAT.** Strict or symmetric NATs often stay on the relay, where only short messages work and files do not.
- **The relay sees metadata** (who talks to whom and when) even though it cannot read content, and it is a single point of availability.
- **Stolen long-term keys:** an attacker who later steals a recipient's identity key can read mailbox traffic they captured earlier. Live connections do not have this weakness because they use fresh session keys.
- **Groups are shared-secret rooms:** anyone with the secret can read the room, including after leaving, and there is no per-member revocation. Change the secret to start a new group.
- **Early group messages can be lost:** a message sent right after a new member appears can be dropped while mesh connections settle.
- **Groups are not remembered** across restarts, and new members do not receive history.
- **Clocks matter:** messages are rejected if a clock differs by more than a few minutes.
- **Offline messages need Ed25519 identities.**

## Technologies used

libp2p for transports, relaying, hole punching, the DHT and pub/sub; Noise and TLS 1.3 for connection security; QUIC and TCP; Ed25519 for identity and signatures; X25519 with NaCl box (XSalsa20-Poly1305) for sealed messages; XChaCha20-Poly1305 for stored data and group messages; HKDF-SHA256 and Argon2id for key derivation; SHA-256 for file integrity; an embedded key-value database for local state.

## References

- [libp2p circuit relay v2](https://github.com/libp2p/specs/blob/master/relay/circuit-v2.md)
- [libp2p direct connection upgrade through relay (DCUtR)](https://github.com/libp2p/specs/blob/master/relay/DCUtR.md)
- [libp2p Kademlia DHT](https://github.com/libp2p/specs/tree/master/kad-dht)
- [libp2p GossipSub](https://github.com/libp2p/specs/blob/master/pubsub/gossipsub/README.md)
- [Noise Protocol Framework](https://noiseprotocol.org/noise.html)
- [RFC 8032: Ed25519](https://www.rfc-editor.org/rfc/rfc8032)
- [RFC 7748: X25519](https://www.rfc-editor.org/rfc/rfc7748)
- [RFC 5869: HKDF](https://www.rfc-editor.org/rfc/rfc5869)
- [RFC 8439: ChaCha20-Poly1305](https://www.rfc-editor.org/rfc/rfc8439)
- [RFC 9106: Argon2](https://www.rfc-editor.org/rfc/rfc9106)
- [NaCl: crypto_box](https://nacl.cr.yp.to/box.html)
