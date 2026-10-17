// Command entropyd runs an Entropy Chain full node: P2P, RPC and an
// optional multi-threaded miner. Flags mirror node.py.
//
//	entropyd --port 9001 --datadir ./node1 --mine
//	entropyd --port 9002 --datadir ./node2 --seeds 127.0.0.1:9001
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/explorer"
	"github.com/entropychaindev/entropychain/go/internal/miner"
	"github.com/entropychaindev/entropychain/go/internal/p2p"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/rpc"
	"github.com/entropychaindev/entropychain/go/internal/stats"
	"github.com/entropychaindev/entropychain/go/internal/wallet"
)

var version = "dev"

func main() {
	port := flag.Int("port", 9001, "websocket P2P listen port")
	datadir := flag.String("datadir", "./node_data", "directory for chaindata/ and wallet.json")
	seeds := flag.String("seeds", "", "comma-separated host:port seed nodes")
	mine := flag.Bool("mine", false, "run the miner")
	threads := flag.Int("threads", 1, "mining threads (each uses 16 MB per hash)")
	mineSolo := flag.Bool("mine-solo", false, "mine even with no peers or while behind (bootstrap/seed nodes and local tests only)")
	minerAddr := flag.String("miner-address", "", "address to mine to (default: first address in wallet.json)")
	maxPeers := flag.Int("max-peers", p2p.DefaultMaxPeers, "maximum connected peers")
	rpcPort := flag.Int("rpc-port", 8001, "HTTP RPC port (0 disables)")
	rpcHost := flag.String("rpc-host", "127.0.0.1", "RPC bind host (0.0.0.0 to expose)")
	rpcCORS := flag.String("rpc-cors", "", `Access-Control-Allow-Origin for RPC (e.g. "*"); empty disables`)
	explorerAddr := flag.String("explorer-addr", "", "serve the HTML block explorer on host:port (e.g. 127.0.0.1:8080)")
	importPy := flag.String("import-python", "", "import and validate a Python node's chain.json, then continue")
	fast := flag.Bool("testnet-fast", false, "lower Argon2 memory for local dev (breaks consensus with real peers)")
	quiet := flag.Bool("quiet", false, "suppress most logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("entropyd %s (%s, network %s)\n", version, runtime.Version(), params.NetworkID)
		return
	}
	log.SetFlags(log.LstdFlags)
	if *quiet {
		log.SetOutput(io.Discard)
	}
	if *fast {
		params.EnableTestnetFast()
		fmt.Println("⚠  TESTNET-FAST mode: relaxed Argon2 memory cost. Will not interoperate with production nodes.")
	}

	if err := run(*port, *datadir, *seeds, *mine, *mineSolo, *threads, *minerAddr, *maxPeers,
		*rpcPort, *rpcHost, *rpcCORS, *explorerAddr, *importPy); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(port int, datadir, seeds string, mine, mineSolo bool, threads int, minerAddr string, maxPeers int,
	rpcPort int, rpcHost, rpcCORS, explorerAddr, importPy string) error {
	seedAddrs, err := p2p.ParseAddrs(seeds)
	if err != nil {
		return err
	}

	c, err := chain.Open(filepath.Join(datadir, "chaindata"))
	if err != nil {
		return err
	}
	defer c.Close()

	if importPy != "" {
		log.Printf("Importing %s with full validation…", importPy)
		n, err := c.ImportPythonChain(importPy)
		if err != nil {
			return fmt.Errorf("import stopped after %d blocks: %w", n, err)
		}
		log.Printf("Imported %d blocks; tip is now %d", n, c.Height())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	node := p2p.New(c, port, seedAddrs, maxPeers)
	if err := node.Start(ctx); err != nil {
		return fmt.Errorf("p2p: %w", err)
	}

	api := rpc.New(c, node, rpcCORS)
	collector := stats.New(c, node)
	api.Observe = collector.ObserveRPC
	go collector.Run(ctx.Done())
	if rpcPort != 0 {
		addr := net.JoinHostPort(rpcHost, strconv.Itoa(rpcPort))
		srv := api
		go func() {
			if err := srv.ListenAndServe(addr); err != nil {
				log.Printf("RPC server stopped: %v", err)
				stop()
			}
		}()
		log.Printf("RPC listening on http://%s", addr)
	}

	if explorerAddr != "" {
		go func() {
			if err := explorer.New(c, api, collector).ListenAndServe(explorerAddr); err != nil {
				log.Printf("Explorer stopped: %v", err)
				stop()
			}
		}()
		log.Printf("Explorer listening on http://%s", explorerAddr)
	}

	if mine {
		addr, err := miningAddress(datadir, minerAddr)
		if err != nil {
			return err
		}
		ready := node.Synced
		if mineSolo {
			ready = nil
		}
		go miner.New(c, addr, threads, ready).Run(ctx)
	}

	<-ctx.Done()
	log.Printf("Shutting down.")
	return nil
}

// miningAddress returns the explicit address, or the wallet's first
// address, creating a wallet with one address if none exists (as node.py
// does).
func miningAddress(datadir, explicit string) (string, error) {
	if explicit != "" {
		if !crypto.AddressIsValid(explicit) {
			return "", fmt.Errorf("invalid --miner-address %q", explicit)
		}
		return explicit, nil
	}
	w, err := wallet.Open(filepath.Join(datadir, "wallet.json"))
	if err != nil {
		return "", err
	}
	if addrs := w.Addresses(); len(addrs) > 0 {
		return addrs[0], nil
	}
	if !w.IsUnlocked() {
		return "", fmt.Errorf("wallet is encrypted and empty; pass --miner-address")
	}
	addr, err := w.CreateAddress("default")
	if err != nil {
		return "", err
	}
	log.Printf("Created mining address %s in %s", addr, w.Path())
	return addr, nil
}
