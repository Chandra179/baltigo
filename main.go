package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"p2p/config"
	"p2p/cryptoutils"
	mypeer "p2p/peer"
	"p2p/relay"
	"strings"

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
	p, err := mypeer.InitPeerHost(cli.config.PeerPrivKey, cli.config.PeerAllowlist, relayInfo)
	if err != nil {
		return fmt.Errorf("failed to init peer: %v", err)
	}
	cli.peer = p
	if err := p.Host.Connect(context.Background(), relayInfo); err != nil {
		log.Printf("Failed to connect to relay: %v", err)
	}
	return nil
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
		fmt.Println("Usage: con <peer_id>")
		return
	}

	idStr := args[0]
	pID, err := peer.Decode(idStr)
	if err != nil {
		fmt.Printf("Invalid peer ID: %v\n", err)
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
	relayInfo, err := cli.config.RelayAddrInfo()
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	dm, err := cli.peer.InitDHT(context.Background(), cli.peer.Host, relayInfo)
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
		fmt.Println("Usage: send <peer_id> <message>")
		return
	}

	targetPeerIDStr := args[0]
	msg := strings.Join(args[1:], " ")

	decodedPeerID, err := peer.Decode(targetPeerIDStr)
	if err != nil {
		fmt.Printf("Invalid peer ID: %v\n", err)
		return
	}

	if err = cli.peer.SendSimple(decodedPeerID, msg); err != nil {
		fmt.Println("error sending message: ", err)
		return
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
	fmt.Println("  con <peer_id>               - Connect to a peer")
	fmt.Println("  dht                         - Discover peers via DHT")
	fmt.Println("  send <peer_id> <message>    - Send message to peer")
	fmt.Println("  genkey                      - Generate new Ed25519 key")
	fmt.Println("  list                        - List discovered peers")
	fmt.Println("  help                        - Show this help")
	fmt.Println("  exit                        - Exit the program")
}

func (cli *CLIManager) runPeerMode() {
	if err := cli.initPeer(); err != nil {
		log.Fatalf("Failed to initialize peer: %v", err)
	}

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
