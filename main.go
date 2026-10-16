package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"github.com/Chandra179/sabaody/config"
	"github.com/Chandra179/sabaody/cryptoutils"
	mypeer "github.com/Chandra179/sabaody/peer"
	"github.com/Chandra179/sabaody/relay"
	"github.com/Chandra179/sabaody/store"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type CLIManager struct {
	peer   *mypeer.PeerInfo
	config *config.Config
}

func NewCLIManager(cfg *config.Config) *CLIManager {
	return &CLIManager{
		config: cfg,
	}
}

func (cli *CLIManager) initPeer() error {
	relayInfo, err := cli.config.RelayAddrInfo()
	if err != nil {
		return err
	}
	st, err := cli.openStore()
	if err != nil {
		return err
	}
	p, err := mypeer.InitPeerHost(cli.config.PeerPrivKey, relayInfo, st, mypeer.Options{
		Allowlist:       cli.config.PeerAllowlist,
		DisableReceipts: cli.config.DisableReceipts,
		DownloadDir:     cli.config.DownloadDir,
		MaxFileSize:     cli.config.MaxFileSize,
	})
	if err != nil {
		st.Close()
		return fmt.Errorf("failed to init peer: %v", err)
	}
	cli.peer = p
	if err := p.Host.Connect(context.Background(), relayInfo); err != nil {
		log.Printf("Failed to connect to relay: %v", err)
	}
	return nil
}

func (cli *CLIManager) openStore() (*store.Store, error) {
	path := cli.config.PeerDB
	if path == "" {
		id, err := peer.IDFromPrivateKey(cli.config.PeerPrivKey)
		if err != nil {
			return nil, err
		}
		path = filepath.Join("peers_db", id.String()+".db")
	}
	st, err := store.Open(path, cli.config.PeerPrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to open store: %w", err)
	}
	return st, nil
}

// resolvePeer accepts a contact name or a peer ID.
func (cli *CLIManager) resolvePeer(s string) (peer.ID, error) {
	return cli.peer.Store.Resolve(s)
}

func (cli *CLIManager) handleContact(args []string) {
	st := cli.peer.Store
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "add":
		if len(args) != 3 {
			fmt.Println("Usage: contact add <name> <peer_id>")
			return
		}
		id, err := peer.Decode(args[2])
		if err != nil {
			fmt.Printf("Invalid peer ID: %v\n", err)
			return
		}
		if err := st.AddContact(args[1], id); err != nil {
			fmt.Printf("Failed to add contact: %v\n", err)
			return
		}
		fmt.Printf("Added contact %s\n", args[1])
	case "rm":
		if len(args) != 2 {
			fmt.Println("Usage: contact rm <name>")
			return
		}
		if err := st.RemoveContact(args[1]); err != nil {
			fmt.Printf("Failed to remove contact: %v\n", err)
			return
		}
		fmt.Printf("Removed contact %s\n", args[1])
	case "list":
		contacts, err := st.Contacts()
		if err != nil {
			fmt.Printf("Failed to list contacts: %v\n", err)
			return
		}
		if len(contacts) == 0 {
			fmt.Println("No contacts. Use: contact add <name> <peer_id>")
		}
		for _, c := range contacts {
			fmt.Printf("  %-20s %s\n", c.Name, c.ID)
		}
	default:
		fmt.Println("Usage: contact [list | add <name> <peer_id> | rm <name>]")
	}
}

func (cli *CLIManager) handleHistory(args []string) {
	if len(args) < 1 || len(args) > 2 {
		fmt.Println("Usage: history <name|peer_id> [count]")
		return
	}
	id, err := cli.resolvePeer(args[0])
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	limit := 20
	if len(args) == 2 {
		if limit, err = strconv.Atoi(args[1]); err != nil || limit < 1 {
			fmt.Println("count must be a positive number")
			return
		}
	}
	entries, err := cli.peer.Store.History(id, limit)
	if err != nil {
		fmt.Printf("Failed to read history: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Println("No messages")
	}
	for _, e := range entries {
		who := "me"
		if e.Incoming {
			who = cli.peer.DisplayName(id)
		}
		note := ""
		if e.Queued {
			note = " (sent via relay mailbox)"
		}
		if !e.Incoming && e.Delivered {
			note += " ✓ delivered"
		}
		fmt.Printf("%s  %s: %s%s\n", e.Time.Format("2006-01-02 15:04:05"), who, e.Text, note)
	}
}

func (cli *CLIManager) handleGroup(args []string) {
	usage := func() {
		fmt.Println("Usage: group join <name> [secret] | leave <name> | list | say <name> <message> | peers <name> | history <name> [n]")
	}
	if len(args) == 0 {
		usage()
		return
	}
	p := cli.peer
	switch args[0] {
	case "join":
		if len(args) < 2 || len(args) > 3 {
			usage()
			return
		}
		secret := ""
		if len(args) == 3 {
			secret = args[2]
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := p.JoinGroup(ctx, args[1], secret); err != nil {
			fmt.Printf("Failed to join group: %v\n", err)
			return
		}
		fmt.Printf("Joined #%s\n", args[1])
		if secret == "" {
			fmt.Println("WARNING: no secret given, anyone who knows the group name can read it")
		}
	case "leave":
		if len(args) != 2 {
			usage()
			return
		}
		if err := p.LeaveGroup(args[1]); err != nil {
			fmt.Printf("Failed to leave group: %v\n", err)
			return
		}
		fmt.Printf("Left #%s\n", args[1])
	case "list":
		groups := p.Groups()
		if len(groups) == 0 {
			fmt.Println("Not in any group. Use: group join <name> [secret]")
		}
		names := make([]string, 0, len(groups))
		for name := range groups {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("  #%-20s %d other member(s) connected\n", name, groups[name])
		}
	case "say":
		if len(args) < 3 {
			usage()
			return
		}
		n, err := p.SendGroup(args[1], strings.Join(args[2:], " "))
		if err != nil {
			fmt.Printf("Failed to send: %v\n", err)
			return
		}
		if n == 0 {
			fmt.Println("No other members are connected right now, nobody will see this message")
		}
	case "peers":
		if len(args) != 2 {
			usage()
			return
		}
		peers, err := p.GroupPeers(args[1])
		if err != nil {
			fmt.Printf("%v\n", err)
			return
		}
		for _, id := range peers {
			fmt.Printf("  %s\n", p.DisplayName(id))
		}
		fmt.Printf("%d other member(s) connected\n", len(peers))
	case "history":
		if len(args) < 2 || len(args) > 3 {
			usage()
			return
		}
		limit := 20
		if len(args) == 3 {
			var err error
			if limit, err = strconv.Atoi(args[2]); err != nil || limit < 1 {
				fmt.Println("count must be a positive number")
				return
			}
		}
		entries, err := p.Store.GroupHistory(args[1], limit)
		if err != nil {
			fmt.Printf("Failed to read history: %v\n", err)
			return
		}
		if len(entries) == 0 {
			fmt.Println("No messages")
		}
		for _, e := range entries {
			who := "me"
			if e.Incoming {
				who = p.DisplayName(e.Author)
			}
			fmt.Printf("%s  %s: %s\n", e.Time.Format("2006-01-02 15:04:05"), who, e.Text)
		}
	default:
		usage()
	}
}

func (cli *CLIManager) handleSendFile(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: sendfile <name|peer_id> <path>")
		return
	}
	id, err := cli.resolvePeer(args[0])
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	path := strings.Join(args[1:], " ")
	fmt.Printf("Sending %s to %s ...\n", path, cli.peer.DisplayName(id))
	if err := cli.peer.SendFile(context.Background(), id, path); err != nil {
		fmt.Printf("Failed to send file: %v\n", err)
		return
	}
	fmt.Println("File delivered and verified")
}

func (cli *CLIManager) handleSync() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	n, err := cli.peer.FetchOffline(ctx)
	if err != nil {
		fmt.Printf("Failed to check mailbox: %v\n", err)
		return
	}
	fmt.Printf("Received %d offline message(s)\n", n)
}

// pollMailbox collects offline messages at startup and then periodically.
func (cli *CLIManager) pollMailbox() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cli.peer.FetchOffline(ctx) // errors are expected while the relay is unreachable
		cancel()
		time.Sleep(30 * time.Second)
	}
}

func (cli *CLIManager) handlePing(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: ping <peer_id> <peer_addr>")
		return
	}

	idStr := args[0]
	addr := args[1]

	pID, err := peer.Decode(idStr)
	if err != nil {
		fmt.Printf("Invalid peer ID: %v\n", err)
		return
	}

	cli.peer.Ping(pID, addr)
}

func (cli *CLIManager) handleConnect(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: con <name|peer_id>")
		return
	}

	idStr := args[0]
	pID, err := cli.resolvePeer(idStr)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	addrs := cli.peer.PeerStore.GetPeer(pID)
	if len(addrs) <= 0 {
		fmt.Println("No address for given peer")
	}

	peerInfo := peer.AddrInfo{
		ID:    pID,
		Addrs: addrs,
	}

	if err := cli.peer.Connect(context.Background(), peerInfo, cli.config.RelayID); err != nil {
		fmt.Printf("Failed to connect to peer: %v\n", err)
		return
	}
	protocols, _ := cli.peer.Host.Peerstore().GetProtocols(peerInfo.ID)
	fmt.Println("Remote supports:", protocols)
	fmt.Printf("Successfully connected to peer: %s\n", idStr)
}

func (cli *CLIManager) handleDHT() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dm, err := cli.peer.DHT(ctx)
	if err != nil {
		fmt.Printf("Failed to init DHT: %v\n", err)
		return
	}

	err = dm.AdvertiseHost(context.Background(), mypeer.CHAT_PROTOCOL)
	if err != nil {
		fmt.Printf("Failed to advertise host: %v\n", err)
		return
	}

	peers, err := dm.FindPeers(context.Background(), mypeer.CHAT_PROTOCOL)
	if err != nil {
		fmt.Printf("Failed to find peers: %v\n", err)
		return
	}

	// Store found peers in memory, excluding self
	peerCount := 0
	for peer := range peers {
		if peer.ID == cli.peer.Host.ID() {
			continue // skip self
		}
		cli.peer.PeerStore.AddPeer(peer.ID, peer.Addrs)
		peerCount++
	}

	fmt.Printf("Total found peers (excluding self): %d\n", peerCount)
}

func (cli *CLIManager) handleSend(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: send <name|peer_id> <message>")
		return
	}

	targetPeerIDStr := args[0]
	msg := strings.Join(args[1:], " ")

	decodedPeerID, err := cli.resolvePeer(targetPeerIDStr)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	queued, err := cli.peer.SendSimple(decodedPeerID, msg)
	if err != nil {
		fmt.Println("error sending message: ", err)
		return
	}
	if queued {
		fmt.Println("Peer is offline: message sealed and queued on the relay for delivery")
	}
}

func (cli *CLIManager) handleGenkey() {
	key, err := cryptoutils.GenerateEd25519Key()
	if err != nil {
		fmt.Printf("Failed to generate key: %v\n", err)
		return
	}
	fmt.Println("Generated key:", key)
}

func (cli *CLIManager) handleList() {
	fmt.Println(cli.peer.PeerStore.GetAllPeers())
}

func (cli *CLIManager) find(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: find <peer_id>")
		return
	}

	id := args[0]
	i, err := peer.Decode(id)
	if err != nil {
		fmt.Printf("Invalid peer ID: %v\n", err)
		return
	}
	fmt.Println(cli.peer.PeerStore.GetPeer(i))
}

func (cli *CLIManager) handleHelp() {
	fmt.Println("Available commands:")
	fmt.Println("  ping <peer_id> <peer_addr>  - Ping a peer")
	fmt.Println("  con <name|peer_id>          - Connect to a peer")
	fmt.Println("  dht                         - Discover peers via DHT")
	fmt.Println("  send <name|peer_id> <msg>   - Send message to peer")
	fmt.Println("  sendfile <name|peer_id> <path> - Send a file directly (recipient must have you as a contact)")
	fmt.Println("  group join <name> [secret]  - Join a group chat (same name + secret = same group)")
	fmt.Println("  group say <name> <message>  - Send to a group")
	fmt.Println("  group list|peers|history|leave - Manage groups")
	fmt.Println("  sync                        - Check the relay for offline messages")
	fmt.Println("  contact [list]              - List contacts")
	fmt.Println("  contact add <name> <peer_id> - Save a contact")
	fmt.Println("  contact rm <name>           - Remove a contact")
	fmt.Println("  history <name|peer_id> [n]  - Show last n messages (default 20)")
	fmt.Println("  genkey                      - Generate new Ed25519 key")
	fmt.Println("  list                        - List discovered peers")
	fmt.Println("  help                        - Show this help")
	fmt.Println("  exit                        - Exit the program")
}

func (cli *CLIManager) runPeerMode() {
	if err := cli.initPeer(); err != nil {
		log.Fatalf("Failed to initialize peer: %v", err)
	}

	go cli.pollMailbox()
	fmt.Println("Peer mode started. Type 'help' for available commands.")
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		if input == "exit" {
			fmt.Println("Exiting...")
			cli.peer.Close()
			cli.peer.Store.Close()
			os.Exit(0)
		}

		fields := strings.Fields(input)
		command := fields[0]
		args := fields[1:]

		switch command {
		case "ping":
			cli.handlePing(args)
		case "con":
			cli.handleConnect(args)
		case "dht":
			cli.handleDHT()
		case "send":
			cli.handleSend(args)
		case "sendfile":
			cli.handleSendFile(args)
		case "group":
			cli.handleGroup(args)
		case "sync":
			cli.handleSync()
		case "contact":
			cli.handleContact(args)
		case "history":
			cli.handleHistory(args)
		case "genkey":
			cli.handleGenkey()
		case "list":
			cli.handleList()
		case "find":
			cli.find(args)
		case "help":
			cli.handleHelp()
		default:
			fmt.Printf("Unknown command: %s. Type 'help' for available commands.\n", command)
		}
	}
}

func main() {
	mode := flag.String("mode", "", "Mode to run: 'relay' or 'peer' (required for startup)")
	flag.Parse()

	if *mode == "" {
		fmt.Fprintln(os.Stderr, "Error: --mode flag is required ('relay' or 'peer')")
		os.Exit(1)
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	switch *mode {
	case "relay":
		fmt.Println("Running in relay mode...")
		relay.RunRelay(cfg)
	case "peer":
		fmt.Println("Running in peer mode...")
		cli := NewCLIManager(cfg)
		cli.runPeerMode()
	default:
		fmt.Fprintf(os.Stderr, "Unknown mode: %s\n", *mode)
		os.Exit(1)
	}

	select {}
}
