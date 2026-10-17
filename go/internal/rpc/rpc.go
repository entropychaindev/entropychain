// Package rpc serves the node's HTTP JSON API. Endpoints and response
// shapes match rpc_server.py so the web wallet, desktop miner, faucet and
// CLI work unchanged against a Go node.
package rpc

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/p2p"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
)

const maxBody = 1 << 20

// Server is the RPC HTTP server.
type Server struct {
	chain *chain.Chain
	node  *p2p.Node // may be nil
	cors  string
	mux   *http.ServeMux

	// Observe, if set, is called with each request's duration.
	Observe func(time.Duration)
}

// New builds the server. cors, if non-empty, is sent as
// Access-Control-Allow-Origin.
func New(c *chain.Chain, node *p2p.Node, cors string) *Server {
	s := &Server{chain: c, node: node, cors: cors, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ListenAndServe runs the server until it fails.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Observe != nil {
		start := time.Now()
		defer func() { s.Observe(time.Since(start)) }()
	}
	if s.cors != "" {
		w.Header().Set("Access-Control-Allow-Origin", s.cors)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
	// Always answer CORS preflights, as Flask did for rpc_server.py. In
	// production the CORS headers come from the reverse proxy (Caddy), but
	// the preflight still reaches us and must get a 2xx, or browsers block
	// every POST /rpc/submit_tx from the web wallet.
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("rpc encode: %v", err)
	}
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
}

// merge flattens v's JSON fields into a map that already has some keys,
// like Python's {"symbol": s, **token}.
func merge(fields map[string]any, v any) map[string]any {
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = pyjson.Decode(raw, &m) // keeps large integers exact
	for k, val := range fields {
		m[k] = val
	}
	return m
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /rpc/info", s.info)
	m.HandleFunc("GET /rpc/balance/{addr}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.chain.Balances(r.PathValue("addr")))
	})
	m.HandleFunc("GET /rpc/balance/{addr}/{token}", s.balanceOne)
	m.HandleFunc("GET /rpc/tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.chain.Tokens())
	})
	m.HandleFunc("GET /rpc/token/{symbol}", func(w http.ResponseWriter, r *http.Request) {
		sym := strings.ToUpper(r.PathValue("symbol"))
		t, ok := s.chain.Token(sym)
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, 200, merge(map[string]any{"symbol": sym}, t))
	})
	m.HandleFunc("GET /rpc/nft/{collection}/{token_id}", func(w http.ResponseWriter, r *http.Request) {
		col, tid := r.PathValue("collection"), r.PathValue("token_id")
		n, ok := s.chain.NFT(col, tid)
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, 200, merge(map[string]any{"collection": col, "token_id": tid}, n))
	})
	m.HandleFunc("GET /rpc/nfts/{owner}", func(w http.ResponseWriter, r *http.Request) {
		owner := r.PathValue("owner")
		list := []map[string]any{}
		for _, n := range s.chain.NFTsByOwner(owner) {
			list = append(list, merge(map[string]any{"collection": n.Collection, "token_id": n.TokenID}, n.NFT))
		}
		writeJSON(w, 200, map[string]any{"owner": owner, "count": len(list), "nfts": list})
	})
	m.HandleFunc("GET /rpc/collections", func(w http.ResponseWriter, r *http.Request) {
		list := []map[string]any{}
		for _, c := range s.chain.Collections(r.URL.Query().Get("creator")) {
			list = append(list, merge(map[string]any{"collection_id": c.ID, "minted_count": c.MintedCount}, c.Collection))
		}
		writeJSON(w, 200, map[string]any{"count": len(list), "collections": list})
	})
	m.HandleFunc("GET /rpc/lottery/{lid}", func(w http.ResponseWriter, r *http.Request) {
		lid := r.PathValue("lid")
		l, ok := s.chain.Lottery(lid)
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, 200, merge(map[string]any{"lottery_id": lid}, l))
	})
	m.HandleFunc("GET /rpc/lotteries", func(w http.ResponseWriter, r *http.Request) {
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil {
			limit = 50
		}
		limit = max(1, min(limit, 200))
		list := []map[string]any{}
		for _, l := range s.chain.Lotteries() {
			if len(list) == limit {
				break
			}
			list = append(list, merge(map[string]any{"lottery_id": l.ID}, l.Lottery))
		}
		writeJSON(w, 200, map[string]any{"height": s.chain.Height(), "count": len(list), "lotteries": list})
	})
	m.HandleFunc("GET /rpc/entropy/{height}", func(w http.ResponseWriter, r *http.Request) {
		h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
		var b *core.Block
		if err == nil {
			b = s.chain.BlockByHeight(h)
		}
		if b == nil {
			notFound(w)
			return
		}
		// "seed" is the beacon v2 value that lotteries and random mints use;
		// "header_seed" is the legacy, miner-chosen header field.
		writeJSON(w, 200, map[string]any{
			"height": h, "seed": s.chain.Beacon(h), "header_seed": b.EntropySeed,
			"timestamp": pyjson.FloatNumber(b.Timestamp), "block_hash": b.Hash(), "miner_nonce": b.MinerNonce,
		})
	})
	m.HandleFunc("GET /rpc/mempool", func(w http.ResponseWriter, r *http.Request) {
		txs := s.chain.MempoolTxs()
		hashes := make([]string, len(txs))
		for i, tx := range txs {
			hashes[i] = tx.Hash()
		}
		writeJSON(w, 200, map[string]any{"size": len(txs), "tx_hashes": hashes})
	})
	m.HandleFunc("GET /rpc/history/{addr}", s.history)
	m.HandleFunc("POST /rpc/submit_tx", s.submitTx)

	// Additions over the Python node.
	m.HandleFunc("GET /rpc/block/{height}", func(w http.ResponseWriter, r *http.Request) {
		h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
		var b *core.Block
		if err == nil {
			b = s.chain.BlockByHeight(h)
		}
		s.writeBlock(w, b)
	})
	m.HandleFunc("GET /rpc/block_by_hash/{hash}", func(w http.ResponseWriter, r *http.Request) {
		s.writeBlock(w, s.chain.BlockByHash(r.PathValue("hash")))
	})
	m.HandleFunc("GET /rpc/peers", func(w http.ResponseWriter, r *http.Request) {
		peers := []p2p.PeerInfo{}
		if s.node != nil {
			peers = s.node.Peers()
		}
		writeJSON(w, 200, map[string]any{"count": len(peers), "peers": peers})
	})
}

func (s *Server) writeBlock(w http.ResponseWriter, b *core.Block) {
	if b == nil {
		notFound(w)
		return
	}
	raw, err := json.Marshal(b)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	var m map[string]any
	_ = pyjson.Decode(raw, &m)
	m["hash"] = b.Hash()
	writeJSON(w, 200, m)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	tip := s.chain.Tip()
	nfts, lotteries := s.chain.Counts()
	peers := 0
	if s.node != nil {
		peers = s.node.PeerCount()
	}
	writeJSON(w, 200, map[string]any{
		"network_id":      params.NetworkID,
		"network_tag":     params.GenesisNetworkTag,
		"genesis_message": params.GenesisMessage,
		"genesis_hash":    s.chain.GenesisHash(),
		"height":          tip.Height,
		"tip_hash":        tip.Hash,
		"total_work":      tip.Work,
		"difficulty":      tip.Difficulty,
		"tokens":          s.chain.TokenSymbols(),
		"nft_count":       nfts,
		"lottery_count":   lotteries,
		"entropy_seed":    tip.EntropySeed,
		"mempool_size":    len(s.chain.MempoolTxs()),
		"peer_count":      peers,
		"node":            "entropyd-go",
	})
}

func (s *Server) balanceOne(w http.ResponseWriter, r *http.Request) {
	addr, token := r.PathValue("addr"), r.PathValue("token")
	up := strings.ToUpper(token)
	if up == params.NativeToken {
		token = up
	} else if _, ok := s.chain.Token(up); ok {
		token = up
	}
	writeJSON(w, 200, map[string]any{"address": addr, "token": token, "balance": s.chain.Balance(addr, token)})
}

// history mirrors rpc_server.py, including stringifying large integers
// for JavaScript clients.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 50
	}
	limit = max(1, min(limit, 200))
	before := int64(-1)
	if v, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
		before = v
	}
	entries := []map[string]any{}
	for _, it := range s.chain.History(addr, limit, before) {
		tx := it.Tx
		data := make(map[string]any, len(tx.Data))
		for k, v := range tx.Data {
			data[k] = v
		}
		for _, k := range []string{"amount", "prize_amount", "ticket_price", "total_supply"} {
			if i, ok := pyjson.AsInt(data[k]); ok {
				data[k] = strconv.FormatInt(i, 10)
			}
		}
		entries = append(entries, map[string]any{
			"tx_type": tx.TxType, "sender": tx.Sender, "timestamp": tx.Timestamp,
			"nonce": tx.Nonce, "data": data, "public_key": tx.PublicKey,
			"signature": tx.Signature, "fee": strconv.FormatInt(tx.Fee, 10),
			"height": it.Height, "tx_hash": tx.Hash(),
		})
	}
	writeJSON(w, 200, map[string]any{"address": addr, "history": entries, "count": len(entries)})
}

func (s *Server) submitTx(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "reason": "unreadable body"})
		return
	}
	var req struct {
		Tx json.RawMessage `json:"tx"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Tx) == 0 || string(req.Tx) == "null" {
		writeJSON(w, 400, map[string]any{"ok": false, "reason": "missing 'tx' field"})
		return
	}
	var tx core.Transaction
	if err := json.Unmarshal(req.Tx, &tx); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "reason": "bad tx: " + err.Error()})
		return
	}
	if err := s.chain.Submit(&tx); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "reason": err.Error(), "tx_hash": tx.Hash()})
		return
	}
	if s.node != nil {
		s.node.AnnounceTx(&tx)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "reason": "queued", "tx_hash": tx.Hash()})
}

// Client is a minimal RPC client used by the CLI.
type Client struct {
	Base string
	HTTP *http.Client
}

// NewClient returns a client for base URL (e.g. http://127.0.0.1:8001).
func NewClient(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Get fetches path and decodes the JSON response into out.
func (c *Client) Get(path string, out any) error {
	resp, err := c.HTTP.Get(c.Base + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("not found")
	}
	return decodeNumbers(resp.Body, out)
}

// Post sends body as JSON and decodes the response (any status) into out.
func (c *Client) Post(path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Post(c.Base+path, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeNumbers(resp.Body, out)
}

func decodeNumbers(r io.Reader, out any) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	return dec.Decode(out)
}
