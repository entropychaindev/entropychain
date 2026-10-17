package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/params"
)

// Limits and timings (p2p.py values where they exist).
const (
	MaxMessageSize      = 1 << 20 // 1 MB
	BanThreshold        = 100
	BanDuration         = time.Hour
	MaxConnectionsPerIP = 3
	DefaultMaxPeers     = 16

	handshakeTimeout = 10 * time.Second
	dialTimeout      = 5 * time.Second
	pingInterval     = 20 * time.Second
	readTimeout      = 60 * time.Second
	writeTimeout     = 10 * time.Second
	sendQueue        = 512
	maxBlocksRange   = 100 // cap per get_blocks_range, as in Python
	syncBatch        = 100
	maxKnownAddrs    = 1000
)

type peer struct {
	node     *Node
	conn     *websocket.Conn
	addr     Addr // advertised listen address
	ip       string
	outbound bool
	nodeID   string
	send     chan []byte
	done     chan struct{}
	once     sync.Once

	mu          sync.Mutex
	tipHeight   int64
	totalWork   *big.Int
	misbehavior int
	syncedTo    int64 // end of the last range we requested from this peer
}

func (p *peer) close() {
	p.once.Do(func() {
		close(p.done)
		_ = p.conn.Close()
	})
}

// enqueue sends raw bytes without blocking; a peer that cannot keep up is
// disconnected.
func (p *peer) enqueue(raw []byte) {
	select {
	case p.send <- raw:
	case <-p.done:
	default:
		log.Printf("Peer %s send queue full, disconnecting", p.short())
		p.close()
	}
}

func (p *peer) sendMsg(msg any) {
	raw, err := json.Marshal(msg)
	if err != nil {
		log.Printf("encode message: %v", err)
		return
	}
	p.enqueue(raw)
}

func (p *peer) short() string {
	if len(p.nodeID) >= 8 {
		return p.nodeID[:8]
	}
	return p.addr.String()
}

func (p *peer) writeLoop() {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case raw := <-p.send:
			_ = p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := p.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				p.close()
				return
			}
		case <-ping.C:
			if err := p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				p.close()
				return
			}
		case <-p.done:
			return
		}
	}
}

// Node is a P2P participant.
type Node struct {
	chain      *chain.Chain
	listenPort int
	seeds      []Addr
	maxPeers   int
	nodeID     string
	orphans    *orphanPool
	ctx        context.Context

	accepted, rejected, orphaned atomic.Uint64

	mu      sync.Mutex
	peers   map[string]*peer // by node id
	known   map[Addr]bool
	self    map[Addr]bool
	dialing map[Addr]bool
	banned  map[string]time.Time
}

// New creates a node; call Start to run it.
func New(c *chain.Chain, listenPort int, seeds []Addr, maxPeers int) *Node {
	if maxPeers <= 0 {
		maxPeers = DefaultMaxPeers
	}
	n := &Node{
		chain: c, listenPort: listenPort, seeds: seeds, maxPeers: maxPeers,
		nodeID: newNodeID(), orphans: newOrphanPool(),
		peers: map[string]*peer{}, known: map[Addr]bool{}, self: map[Addr]bool{},
		dialing: map[Addr]bool{}, banned: map[string]time.Time{},
	}
	for _, s := range seeds {
		n.known[s] = true
	}
	c.Subscribe(func(ev chain.Event) {
		if ev.ReorgDepth > 0 {
			log.Printf("  ⟲ REORG: depth=%d, %s… -> %s…", ev.ReorgDepth, ev.OldTip[:12], ev.NewTip.Hash()[:12])
		}
		n.broadcast(map[string]any{"type": "inv_block", "hash": ev.NewTip.Hash(), "height": ev.NewTip.Height}, "")
	})
	return n
}

// NodeID returns this process's random node id.
func (n *Node) NodeID() string { return n.nodeID }

// PeerInfo describes a connected peer.
type PeerInfo struct {
	NodeID    string `json:"node_id"`
	Address   string `json:"address"`
	Outbound  bool   `json:"outbound"`
	TipHeight int64  `json:"tip_height"`
}

// Peers lists connected peers.
func (n *Node) Peers() []PeerInfo {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]PeerInfo, 0, len(n.peers))
	for _, p := range n.peers {
		p.mu.Lock()
		out = append(out, PeerInfo{p.nodeID, p.addr.String(), p.outbound, p.tipHeight})
		p.mu.Unlock()
	}
	return out
}

// BlockCounters returns how many blocks received from peers were accepted,
// rejected as invalid, or held as orphans since the node started.
func (n *Node) BlockCounters() (accepted, rejected, orphans uint64) {
	return n.accepted.Load(), n.rejected.Load(), n.orphaned.Load()
}

// PeerCount returns the number of connected peers.
func (n *Node) PeerCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.peers)
}

// Start listens for peers, dials seeds and runs background loops until
// ctx is cancelled.
func (n *Node) Start(ctx context.Context) error {
	n.ctx = ctx
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(n.listenPort))
	if err != nil {
		return err
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096,
		CheckOrigin: func(*http.Request) bool { return true },
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if reason := n.admit(ip); reason != "" {
				http.Error(w, reason, http.StatusForbidden)
				return
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			n.runPeer(ctx, conn, Addr{ip, 0}, ip, false)
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		n.mu.Lock()
		for _, p := range n.peers {
			p.close()
		}
		n.mu.Unlock()
	}()
	log.Printf("P2P node %s listening on ws://0.0.0.0:%d", n.nodeID[:8], n.listenPort)

	for _, s := range n.seeds {
		go n.dial(ctx, s)
	}
	go n.every(ctx, 5*time.Second, n.syncTick)
	go n.every(ctx, 30*time.Second, func() { n.peerExchange(ctx) })
	go n.every(ctx, 5*time.Minute, n.cleanBans)
	return nil
}

func (n *Node) every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

// admit applies the ban list and the per-IP connection cap.
func (n *Node) admit(ip string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if until, ok := n.banned[ip]; ok && time.Now().Before(until) {
		return "banned"
	}
	count := 0
	for _, p := range n.peers {
		if p.ip == ip {
			count++
		}
	}
	if count >= MaxConnectionsPerIP {
		return "too many connections from your IP"
	}
	return ""
}

func (n *Node) dial(ctx context.Context, a Addr) {
	n.mu.Lock()
	if n.dialing[a] || n.self[a] || len(n.peers) >= n.maxPeers {
		n.mu.Unlock()
		return
	}
	for _, p := range n.peers {
		if p.addr == a {
			n.mu.Unlock()
			return
		}
	}
	n.dialing[a] = true
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.dialing, a)
		n.mu.Unlock()
	}()

	d := websocket.Dialer{HandshakeTimeout: dialTimeout}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	conn, _, err := d.DialContext(dctx, "ws://"+a.String(), nil)
	cancel()
	if err != nil {
		return
	}
	ip := a.Host
	if host, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
		ip = host
	}
	n.runPeer(ctx, conn, a, ip, true)
}

// runPeer performs the handshake and then serves the connection until it
// closes. It blocks.
func (n *Node) runPeer(ctx context.Context, conn *websocket.Conn, a Addr, ip string, outbound bool) {
	conn.SetReadLimit(MaxMessageSize)
	p := &peer{node: n, conn: conn, addr: a, ip: ip, outbound: outbound,
		send: make(chan []byte, sendQueue), done: make(chan struct{}), totalWork: new(big.Int)}
	defer p.close()

	tip := n.chain.Tip()
	hello := helloMsg("hello", n.nodeID, n.listenPort, tip.Hash, tip.Height, tip.Work)
	raw, _ := json.Marshal(hello)
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var m message
	if err := json.Unmarshal(data, &m); err != nil || (m.Type != "hello" && m.Type != "hello_ack") {
		log.Printf("%s: expected hello", a)
		return
	}
	if m.NetworkID != params.NetworkID {
		log.Printf("%s: wrong network %q", a, m.NetworkID)
		return
	}
	if m.NodeID == "" {
		return
	}
	if m.NodeID == n.nodeID {
		n.mu.Lock()
		n.self[a] = true
		delete(n.known, a)
		n.mu.Unlock()
		return
	}
	work, ok := new(big.Int).SetString(m.TotalWork.String(), 10)
	if !ok {
		work = new(big.Int)
	}
	p.nodeID, p.tipHeight, p.totalWork = m.NodeID, m.TipHeight, work
	if !outbound && m.ListenPort > 0 && m.ListenPort <= 65535 {
		p.addr = Addr{ip, m.ListenPort}
	}

	if m.Type == "hello" {
		ack := helloMsg("hello_ack", n.nodeID, n.listenPort, tip.Hash, tip.Height, tip.Work)
		raw, _ := json.Marshal(ack)
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			return
		}
	}

	n.mu.Lock()
	if _, dup := n.peers[p.nodeID]; dup || len(n.peers) >= n.maxPeers {
		n.mu.Unlock()
		return
	}
	n.peers[p.nodeID] = p
	if p.addr.Port > 0 && len(n.known) < maxKnownAddrs {
		n.known[p.addr] = true
	}
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		if n.peers[p.nodeID] == p {
			delete(n.peers, p.nodeID)
		}
		n.mu.Unlock()
		log.Printf("Peer %s disconnected", p.short())
	}()
	log.Printf("Peer %s @ %s connected (their height=%d, ours=%d)", p.short(), p.addr, p.tipHeight, tip.Height)

	go p.writeLoop()
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
	if work.Cmp(tip.Work) > 0 {
		n.requestSync(p)
	}

	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m message
		if err := json.Unmarshal(data, &m); err != nil {
			if n.misbehaved(p, 10, "unparseable msg: "+err.Error()) {
				return
			}
			continue
		}
		n.dispatch(p, &m)
	}
}

// misbehaved adds points; at the threshold the IP is banned and the peer
// dropped. Returns true if banned.
func (n *Node) misbehaved(p *peer, points int, reason string) bool {
	p.mu.Lock()
	p.misbehavior += points
	score := p.misbehavior
	p.mu.Unlock()
	if score < BanThreshold {
		return false
	}
	n.mu.Lock()
	n.banned[p.ip] = time.Now().Add(BanDuration)
	n.mu.Unlock()
	log.Printf("Banning %s for %s (score=%d): %s", p.ip, BanDuration, score, reason)
	p.close()
	return true
}

func (n *Node) cleanBans() {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	for ip, until := range n.banned {
		if now.After(until) {
			delete(n.banned, ip)
		}
	}
}

func (n *Node) broadcast(msg any, exceptNodeID string) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	n.mu.Lock()
	peers := make([]*peer, 0, len(n.peers))
	for id, p := range n.peers {
		if id != exceptNodeID {
			peers = append(peers, p)
		}
	}
	n.mu.Unlock()
	for _, p := range peers {
		p.enqueue(raw)
	}
}

// AnnounceTx gossips a locally submitted transaction.
func (n *Node) AnnounceTx(tx *core.Transaction) {
	n.broadcast(map[string]any{"type": "inv_tx", "hash": tx.Hash()}, "")
}

// ---- Message handling ----

func (n *Node) dispatch(p *peer, m *message) {
	switch m.Type {
	case "inv_block":
		// The announced height is not trusted: a peer's height only rises
		// when it delivers a block with valid PoW (see processBlock).
		if m.Hash != "" && !n.chain.HasBlock(m.Hash) && !n.orphans.has(m.Hash) {
			p.sendMsg(map[string]any{"type": "get_block", "hash": m.Hash})
		}
	case "inv_tx":
		if m.Hash != "" && !n.chain.KnowsTx(m.Hash) {
			p.sendMsg(map[string]any{"type": "get_tx", "hash": m.Hash})
		}
	case "get_block":
		if b := n.chain.BlockByHash(m.Hash); b != nil {
			p.sendMsg(map[string]any{"type": "block", "block": b})
		}
	case "get_block_by_height":
		if b := n.chain.BlockByHeight(m.Height); b != nil {
			p.sendMsg(map[string]any{"type": "block", "block": b})
		}
	case "get_blocks_range":
		count := min(m.Count, maxBlocksRange)
		for h := m.StartHeight; h < m.StartHeight+count; h++ {
			b := n.chain.BlockByHeight(h)
			if b == nil {
				break
			}
			p.sendMsg(map[string]any{"type": "block", "block": b})
		}
	case "block":
		n.onBlock(p, m.Block)
	case "get_tx":
		if tx := n.chain.MempoolTx(m.Hash); tx != nil {
			p.sendMsg(map[string]any{"type": "tx", "tx": tx})
		}
	case "tx":
		n.onTx(p, m.Tx)
	case "get_peers":
		n.mu.Lock()
		var list [][]any
		for a := range n.known {
			if a != p.addr && len(list) < 32 {
				list = append(list, []any{a.Host, a.Port})
			}
		}
		n.mu.Unlock()
		if list == nil {
			list = [][]any{}
		}
		p.sendMsg(map[string]any{"type": "peers", "peers": list})
	case "peers":
		n.mu.Lock()
		for _, e := range m.Peers {
			if a, ok := parsePeerEntry(e); ok && !n.self[a] && len(n.known) < maxKnownAddrs {
				n.known[a] = true
			}
		}
		n.mu.Unlock()
	}
	// hello/hello_ack after the handshake and unknown types are ignored.
}

func (n *Node) onBlock(p *peer, raw json.RawMessage) {
	var b core.Block
	if err := json.Unmarshal(raw, &b); err != nil {
		log.Printf("Bad block from %s: %v", p.short(), err)
		n.misbehaved(p, 20, "malformed block: "+err.Error())
		return
	}
	n.processBlock(p, &b)
}

func (n *Node) processBlock(p *peer, b *core.Block) {
	res, err := n.chain.AddBlock(b)
	if err == nil || errors.Is(err, chain.ErrKnown) {
		p.mu.Lock()
		p.tipHeight = max(p.tipHeight, b.Height)
		p.mu.Unlock()
	}
	switch {
	case err == nil:
		n.accepted.Add(1)
		log.Printf("  ✓ accepted block %d %s… from %s (%s)", b.Height, b.Hash()[:12], p.short(), res)
		if res == "stored on side branch" {
			n.broadcast(map[string]any{"type": "inv_block", "hash": b.Hash(), "height": b.Height}, p.nodeID)
		}
		// Children that were waiting for this block can connect now.
		for _, child := range n.orphans.takeChildren(b.Hash()) {
			n.processBlock(p, child)
		}
		n.continueSync(p)
	case errors.Is(err, chain.ErrOrphan):
		n.orphaned.Add(1)
		if n.orphans.add(b) {
			p.sendMsg(map[string]any{"type": "get_block", "hash": b.PrevHash})
		}
	case errors.Is(err, chain.ErrKnown):
		n.continueSync(p)
	default:
		n.rejected.Add(1)
		log.Printf("  ✗ rejected block %d from %s: %v", b.Height, p.short(), err)
		n.misbehaved(p, 25, "invalid block: "+err.Error())
	}
}

func (n *Node) onTx(p *peer, raw json.RawMessage) {
	var tx core.Transaction
	if err := json.Unmarshal(raw, &tx); err != nil {
		n.misbehaved(p, 5, "malformed tx: "+err.Error())
		return
	}
	err := n.chain.Submit(&tx)
	if err == nil {
		n.broadcast(map[string]any{"type": "inv_tx", "hash": tx.Hash()}, p.nodeID)
		return
	}
	switch err.Error() {
	case "duplicate transaction", "transaction already in mempool", "mempool full and your fee is too low":
	default:
		n.misbehaved(p, 2, "invalid tx: "+err.Error())
	}
}

// ---- Sync ----

// requestSync asks p for the next batch of blocks. Like p2p.py it starts a
// few blocks below our tip so a short fork shows up as overlapping blocks;
// deeper forks resolve by fetching orphans' parents.
func (n *Node) requestSync(p *peer) {
	start := n.chain.Height() + 1
	if start > 10 {
		start -= 10
	}
	p.mu.Lock()
	p.syncedTo = start + syncBatch
	p.mu.Unlock()
	p.sendMsg(map[string]any{"type": "get_blocks_range", "start_height": start, "count": syncBatch})
}

// continueSync requests the next batch once the previous one is consumed.
func (n *Node) continueSync(p *peer) {
	p.mu.Lock()
	behind := p.syncedTo > 0 && n.chain.Height() >= p.syncedTo-1 && p.tipHeight > n.chain.Height()
	p.mu.Unlock()
	if behind {
		n.requestSync(p)
	}
}

// Synced reports whether the node has at least one peer and no peer claims
// a chain ahead of ours. Miners wait for this, so a node that is still
// connecting or catching up never builds a private chain that later
// reorganizes the network.
func (n *Node) Synced() bool {
	tip := n.chain.Tip()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.peers) == 0 {
		return false
	}
	for _, p := range n.peers {
		p.mu.Lock()
		ahead := p.totalWork.Cmp(tip.Work) > 0 || p.tipHeight > tip.Height
		p.mu.Unlock()
		if ahead {
			return false
		}
	}
	return true
}

func (n *Node) syncTick() {
	tip := n.chain.Tip()
	n.mu.Lock()
	if len(n.peers) == 0 && n.ctx != nil {
		// Alone: retry the seeds now rather than waiting for peer exchange.
		for _, s := range n.seeds {
			go n.dial(n.ctx, s)
		}
	}
	var best *peer
	for _, p := range n.peers {
		p.mu.Lock()
		ahead := p.totalWork.Cmp(tip.Work) > 0 || p.tipHeight > tip.Height
		p.mu.Unlock()
		if ahead && (best == nil || p.totalWork.Cmp(best.totalWork) > 0) {
			best = p
		}
	}
	n.mu.Unlock()
	if best != nil {
		n.requestSync(best)
	}
}

func (n *Node) peerExchange(ctx context.Context) {
	n.mu.Lock()
	peers := make([]*peer, 0, len(n.peers))
	connected := map[Addr]bool{}
	for _, p := range n.peers {
		peers = append(peers, p)
		connected[p.addr] = true
	}
	var targets []Addr
	for a := range n.known {
		if !connected[a] && !n.self[a] && len(peers)+len(targets) < n.maxPeers {
			targets = append(targets, a)
		}
	}
	n.mu.Unlock()
	for _, p := range peers {
		p.sendMsg(map[string]any{"type": "get_peers"})
	}
	for _, a := range targets {
		go n.dial(ctx, a)
	}
}

// ---- Orphans ----

const maxOrphans = 512

// orphanPool holds blocks whose parent we have not seen yet.
type orphanPool struct {
	mu       sync.Mutex
	byHash   map[string]*core.Block
	byParent map[string][]string
	order    []string
}

func newOrphanPool() *orphanPool {
	return &orphanPool{byHash: map[string]*core.Block{}, byParent: map[string][]string{}}
}

func (o *orphanPool) has(h string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.byHash[h]
	return ok
}

// add stores b; returns false if it was already present.
func (o *orphanPool) add(b *core.Block) bool {
	h := b.Hash()
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.byHash[h]; ok {
		return false
	}
	for len(o.order) >= maxOrphans {
		o.removeLocked(o.order[0])
	}
	o.byHash[h] = b
	o.byParent[b.PrevHash] = append(o.byParent[b.PrevHash], h)
	o.order = append(o.order, h)
	return true
}

func (o *orphanPool) removeLocked(h string) {
	b, ok := o.byHash[h]
	if !ok {
		return
	}
	delete(o.byHash, h)
	kids := o.byParent[b.PrevHash]
	for i, k := range kids {
		if k == h {
			kids = append(kids[:i], kids[i+1:]...)
			break
		}
	}
	if len(kids) == 0 {
		delete(o.byParent, b.PrevHash)
	} else {
		o.byParent[b.PrevHash] = kids
	}
	for i, k := range o.order {
		if k == h {
			o.order = append(o.order[:i], o.order[i+1:]...)
			break
		}
	}
}

// takeChildren removes and returns orphans whose parent is h.
func (o *orphanPool) takeChildren(h string) []*core.Block {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []*core.Block
	for _, k := range append([]string{}, o.byParent[h]...) {
		out = append(out, o.byHash[k])
		o.removeLocked(k)
	}
	return out
}
