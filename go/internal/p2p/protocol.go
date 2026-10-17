// Package p2p implements the Entropy Chain websocket gossip protocol
// (protocol.py / p2p.py). Messages are JSON objects, one per text frame,
// with a "type" field. The Go node speaks exactly the Python wire format
// so both implementations can peer with each other.
package p2p

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"

	"github.com/entropychaindev/entropychain/go/internal/params"
)

// message is the union of all message fields; unused ones stay zero.
type message struct {
	Type string `json:"type"`

	// hello / hello_ack
	Version    int         `json:"version"`
	NetworkID  string      `json:"network_id"`
	NodeID     string      `json:"node_id"`
	ListenPort int         `json:"listen_port"`
	TipHash    string      `json:"tip_hash"`
	TipHeight  int64       `json:"tip_height"`
	TotalWork  json.Number `json:"total_work"`

	// inv_block / inv_tx / get_block / get_tx / get_block_by_height
	Hash   string `json:"hash"`
	Height int64  `json:"height"`

	// block / tx
	Block json.RawMessage `json:"block"`
	Tx    json.RawMessage `json:"tx"`

	// peers
	Peers [][]json.RawMessage `json:"peers"`

	// get_blocks_range
	StartHeight int64 `json:"start_height"`
	Count       int64 `json:"count"`
}

func newNodeID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func helloMsg(typ, nodeID string, port int, tipHash string, tipHeight int64, work *big.Int) map[string]any {
	return map[string]any{
		"type":        typ,
		"version":     params.ProtocolVersion,
		"network_id":  params.NetworkID,
		"node_id":     nodeID,
		"listen_port": port,
		"tip_hash":    tipHash,
		"tip_height":  tipHeight,
		"total_work":  work,
	}
}

// Addr is a peer's listen address.
type Addr struct {
	Host string
	Port int
}

func (a Addr) String() string { return net.JoinHostPort(a.Host, strconv.Itoa(a.Port)) }

// ParseAddrs parses "host:port,host:port".
func ParseAddrs(s string) ([]Addr, error) {
	var out []Addr
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		host, port, err := net.SplitHostPort(part)
		if err != nil {
			return nil, fmt.Errorf("bad seed %q: %w", part, err)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("bad seed port %q", part)
		}
		out = append(out, Addr{host, p})
	}
	return out, nil
}

// parsePeerEntry decodes a [host, port] pair from a peers message.
func parsePeerEntry(e []json.RawMessage) (Addr, bool) {
	if len(e) != 2 {
		return Addr{}, false
	}
	var host string
	var port float64
	if json.Unmarshal(e[0], &host) != nil || json.Unmarshal(e[1], &port) != nil {
		return Addr{}, false
	}
	if host == "" || len(host) > 255 || port < 1 || port > 65535 || port != float64(int(port)) {
		return Addr{}, false
	}
	return Addr{host, int(port)}, true
}
