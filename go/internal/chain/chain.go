// Package chain is the Entropy Chain state machine: block validation,
// fork choice by cumulative work, reorgs, and the mempool.
package chain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/consensus"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/store"
)

var (
	// ErrKnown means the block is already in the index.
	ErrKnown = errors.New("already known")
	// ErrOrphan means the block's parent is unknown; fetch it from a peer.
	ErrOrphan = errors.New("unknown parent (request from peer)")
	// ErrWrongNetwork means the data directory belongs to another network
	// (a different genesis), e.g. a testnet that has since been reset.
	ErrWrongNetwork = errors.New("data directory holds a different network")
	// errFuture is not a permanent failure: the block may become valid.
	errFuture = errors.New("timestamp too far in the future")
)

// undoDepth is how many recent main-chain blocks keep in-memory undo
// records. Deeper reorgs (or reorgs right after a restart) rebuild state
// by replaying from genesis.
const undoDepth = 200

// node is a block header in the index. Bodies live in the store.
type node struct {
	hash   string
	header *core.Block // Transactions stripped
	work   *big.Int    // cumulative work including this block
}

// Event is emitted after the chain changes (outside the lock).
type Event struct {
	NewTip     *core.Block
	ReorgDepth int // >0 when the tip moved to a different branch
	OldTip     string
}

// Chain is safe for concurrent use.
type Chain struct {
	mu      sync.RWMutex
	store   *store.Store
	index   map[string]*node
	invalid map[string]bool
	main    []string
	state   *State
	undo    map[string][]func() // block hash -> undo record (recent blocks only)
	mempool *mempool

	subsMu sync.Mutex
	subs   []func(Event)
}

// Open loads the chain from dataDir, creating genesis on first run.
func Open(dataDir string) (*Chain, error) {
	st, err := store.Open(dataDir)
	if err != nil {
		return nil, err
	}
	c := &Chain{
		store:   st,
		index:   map[string]*node{},
		invalid: map[string]bool{},
		undo:    map[string][]func(){},
		mempool: newMempool(),
	}
	if err := c.load(); err != nil {
		st.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the store.
func (c *Chain) Close() error { return c.store.Close() }

func stripTxs(b *core.Block) *core.Block {
	h := *b
	h.Transactions = nil
	return &h
}

func (c *Chain) load() error {
	genesis := core.GenesisBlock()
	gh := genesis.Hash()

	main, err := c.store.MainChain()
	if err != nil {
		return err
	}
	if len(main) == 0 {
		b := store.NewBatch()
		b.PutBlock(gh, genesis)
		b.SetMain(0, gh)
		if err := c.store.Commit(b); err != nil {
			return err
		}
		main = []string{gh}
		log.Printf("Created genesis block %s… (network %s)", gh[:16], params.GenesisNetworkTag)
	}
	if main[0] != gh {
		return fmt.Errorf("%w (genesis %s…, expected %s…)", ErrWrongNetwork, main[0][:16], gh[:16])
	}

	// Index every stored block. Parents have lower heights, so processing
	// by height lets cumulative work be computed in one pass.
	var headers []*core.Block
	hashes := map[*core.Block]string{}
	err = c.store.ForEachBlock(func(h string, b *core.Block) error {
		hb := stripTxs(b)
		headers = append(headers, hb)
		hashes[hb] = h
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].Height < headers[j].Height })
	for _, hb := range headers {
		h := hashes[hb]
		work := new(big.Int).SetUint64(hb.Difficulty)
		if hb.Height > 0 {
			parent, ok := c.index[hb.PrevHash]
			if !ok {
				continue // orphaned in storage; ignore
			}
			work.Add(work, parent.work)
		}
		c.index[h] = &node{hash: h, header: hb, work: work}
	}
	for _, h := range main {
		if _, ok := c.index[h]; !ok {
			return fmt.Errorf("main chain references missing block %s", h)
		}
	}
	c.main = main
	if err := c.rebuildState(); err != nil {
		return err
	}
	log.Printf("Loaded %d blocks (%d in index), tip %d %s…", len(c.main), len(c.index), c.height(), c.tipHash()[:16])
	return nil
}

// rebuildState replays the main chain from genesis without re-validating
// (every block on it was validated when connected).
func (c *Chain) rebuildState() error {
	c.state = newState()
	c.undo = map[string][]func(){}
	for height, h := range c.main {
		b, err := c.store.GetBlock(h)
		if err != nil {
			return err
		}
		cx := ctx{height: int64(height), beaconAt: c.beaconAt}
		for _, tx := range b.Transactions {
			if err := c.state.applyTx(tx, cx); err != nil {
				return fmt.Errorf("replaying block %d: %w", height, err)
			}
		}
		c.state.journal = c.state.journal[:0]
	}
	return nil
}

// beaconAt returns the beacon v2 randomness of the main-chain block at height.
func (c *Chain) beaconAt(height int64) string {
	return consensus.Beacon(c.main[height])
}

// resolveNFT fills in a random mint's traits once its source block exists.
// Traits are a pure function of the chain, so computing them on read keeps
// state small and stays correct across reorgs.
func (c *Chain) resolveNFT(k nftKey, n NFT) NFT {
	if n.Traits != nil || n.EntropySourceHeight == nil {
		return n
	}
	src := *n.EntropySourceHeight
	if src > c.height() {
		n.TraitsPending = true
		return n
	}
	n.Traits = consensus.DeriveRandomTraits(c.beaconAt(src), k.collection, k.token, src)
	return n
}

func (c *Chain) height() int64   { return int64(len(c.main) - 1) }
func (c *Chain) tipHash() string { return c.main[len(c.main)-1] }
func (c *Chain) tipNode() *node  { return c.index[c.tipHash()] }

// Subscribe registers fn to receive chain events. Callbacks run on the
// goroutine that changed the chain, after the lock is released.
func (c *Chain) Subscribe(fn func(Event)) {
	c.subsMu.Lock()
	c.subs = append(c.subs, fn)
	c.subsMu.Unlock()
}

func (c *Chain) emit(ev Event) {
	c.subsMu.Lock()
	subs := append([]func(Event){}, c.subs...)
	c.subsMu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

// ---- Header rules ----

// difficultyFor returns the required difficulty for a child of parent.
func (c *Chain) difficultyFor(parent *node, newHeight int64) uint64 {
	// The first window would measure from the genesis timestamp, which is
	// fixed in advance rather than mined, so it does not retarget.
	if newHeight <= params.DifficultyWindow || newHeight%params.DifficultyWindow != 0 {
		return parent.header.Difficulty
	}
	cursor := parent
	for i := 0; i < params.DifficultyWindow-1; i++ {
		cursor = c.index[cursor.header.PrevHash]
	}
	actual := parent.header.Timestamp - cursor.header.Timestamp
	return consensus.AdjustDifficulty(parent.header.Difficulty, int64(actual))
}

// medianTimePast returns the median timestamp of up to MedianTimeSpan
// blocks ending at n.
func (c *Chain) medianTimePast(n *node) float64 {
	ts := make([]float64, 0, params.MedianTimeSpan)
	for cur := n; cur != nil && len(ts) < params.MedianTimeSpan; {
		ts = append(ts, cur.header.Timestamp)
		if cur.header.Height == 0 {
			break
		}
		cur = c.index[cur.header.PrevHash]
	}
	sort.Float64s(ts)
	return ts[len(ts)/2]
}

// checkStandalone verifies everything that needs no chain context. It is
// the expensive part (Argon2), so it runs before taking the lock.
func checkStandalone(b *core.Block) error {
	if err := b.CheckFormat(); err != nil {
		return err
	}
	if !consensus.HashMeetsDifficulty(consensus.PowHash(b.HeaderBytes(), b.Nonce), b.Difficulty) {
		return errors.New("PoW does not meet difficulty")
	}
	if len(b.Transactions) == 0 || b.Transactions[0].TxType != core.TxCoinbase {
		return errors.New("first tx must be coinbase")
	}
	for _, tx := range b.Transactions[1:] {
		if tx.TxType == core.TxCoinbase {
			return errors.New("multiple coinbase txs")
		}
	}
	if b.MerkleRootHex != core.MerkleRoot(b.TxHashes()) {
		return errors.New("merkle root mismatch")
	}
	return nil
}

// checkHeader verifies the header against its parent.
func (c *Chain) checkHeader(b *core.Block, parent *node, live bool) error {
	if b.Height != parent.header.Height+1 {
		return fmt.Errorf("expected height %d, got %d", parent.header.Height+1, b.Height)
	}
	if want := c.difficultyFor(parent, b.Height); b.Difficulty != want {
		return fmt.Errorf("wrong difficulty (expected %d)", want)
	}
	prev, _ := hex.DecodeString(parent.hash)
	mn, _ := hex.DecodeString(b.MinerNonce)
	if b.EntropySeed != hex.EncodeToString(consensus.DeriveEntropySeed(prev, mn)) {
		return errors.New("entropy_seed not derived correctly")
	}
	if b.Timestamp <= c.medianTimePast(parent) {
		return errors.New("timestamp not after median of recent blocks")
	}
	if live && b.Timestamp > float64(time.Now().Unix()+params.MaxFutureBlockTime) {
		return errFuture
	}
	return nil
}

// connect validates b's transactions against the current tip state and
// applies them. On failure the state is left unchanged.
func (c *Chain) connect(b *core.Block) error {
	mark := c.state.mark()
	cx := ctx{height: b.Height, beaconAt: c.beaconAt}
	var fees int64
	for _, tx := range b.Transactions[1:] {
		if err := c.state.checkTx(tx, cx); err != nil {
			c.state.rollback(mark)
			return fmt.Errorf("tx %s… invalid: %w", tx.Hash()[:12], err)
		}
		var err error
		if fees, err = addChecked(fees, tx.Fee); err != nil {
			c.state.rollback(mark)
			return err
		}
		if err := c.state.applyTx(tx, cx); err != nil {
			c.state.rollback(mark)
			return fmt.Errorf("tx %s… invalid: %w", tx.Hash()[:12], err)
		}
	}
	cb := b.Transactions[0]
	if err := checkCoinbase(cb, b.Height, fees); err != nil {
		c.state.rollback(mark)
		return err
	}
	if c.state.seen(cb.Hash()) {
		c.state.rollback(mark)
		return errors.New("duplicate coinbase")
	}
	if err := c.state.applyTx(cb, cx); err != nil {
		c.state.rollback(mark)
		return err
	}
	h := b.Hash()
	c.undo[h] = c.state.take(mark)
	c.main = append(c.main, h)
	if old := len(c.main) - 1 - undoDepth; old > 0 {
		delete(c.undo, c.main[old])
	}
	return nil
}

// disconnectTip undoes the tip block. Returns false if no undo record is
// available (caller must rebuild).
func (c *Chain) disconnectTip() bool {
	h := c.tipHash()
	j, ok := c.undo[h]
	if !ok {
		return false
	}
	undo(j)
	delete(c.undo, h)
	c.main = c.main[:len(c.main)-1]
	return true
}

// AddBlock validates and stores a block, extending or reorganizing the
// main chain when it has the most work. It returns a short description of
// what happened.
func (c *Chain) AddBlock(b *core.Block) (string, error) {
	return c.addBlock(b, true)
}

func (c *Chain) addBlock(b *core.Block, live bool) (string, error) {
	h := b.Hash()
	c.mu.RLock()
	_, known := c.index[h]
	bad := c.invalid[h] || c.invalid[b.PrevHash]
	c.mu.RUnlock()
	if known {
		return "", ErrKnown
	}
	if bad {
		return "", errors.New("block or its parent is known invalid")
	}
	if err := checkStandalone(b); err != nil {
		return "", err
	}

	c.mu.Lock()
	var events []Event
	result, err := func() (string, error) {
		if _, ok := c.index[h]; ok {
			return "", ErrKnown
		}
		parent, ok := c.index[b.PrevHash]
		if !ok {
			return "", ErrOrphan
		}
		if err := c.checkHeader(b, parent, live); err != nil {
			if !errors.Is(err, errFuture) {
				c.invalid[h] = true
			}
			return "", err
		}
		n := &node{hash: h, header: stripTxs(b), work: new(big.Int).Add(parent.work, new(big.Int).SetUint64(b.Difficulty))}

		if b.PrevHash == c.tipHash() {
			if err := c.connect(b); err != nil {
				c.invalid[h] = true
				return "", err
			}
			batch := store.NewBatch()
			batch.PutBlock(h, b)
			batch.SetMain(b.Height, h)
			if err := c.store.Commit(batch); err != nil {
				undo(c.undo[h])
				delete(c.undo, h)
				c.main = c.main[:len(c.main)-1]
				return "", fmt.Errorf("storage: %w", err)
			}
			c.index[h] = n
			c.mempool.removeIncluded(b)
			c.revalidateMempool()
			events = append(events, Event{NewTip: b})
			return "extended tip", nil
		}

		// Side branch: store it, and reorg if it now has the most work.
		batch := store.NewBatch()
		batch.PutBlock(h, b)
		if err := c.store.Commit(batch); err != nil {
			return "", fmt.Errorf("storage: %w", err)
		}
		c.index[h] = n
		if n.work.Cmp(c.tipNode().work) <= 0 {
			return "stored on side branch", nil
		}
		oldTip := c.tipHash()
		depth, applied, err := c.reorgTo(h)
		if err != nil {
			return "", err
		}
		tip, _ := c.store.GetBlock(c.tipHash())
		events = append(events, Event{NewTip: tip, ReorgDepth: depth, OldTip: oldTip})
		return fmt.Sprintf("reorg succeeded (depth %d -> %d)", depth, applied), nil
	}()
	c.mu.Unlock()

	for _, ev := range events {
		c.emit(ev)
	}
	return result, err
}

// reorgTo switches the main chain to the branch ending at newTip.
func (c *Chain) reorgTo(newTip string) (depth, applied int, err error) {
	// Walk the new branch back to the main chain.
	onMain := make(map[string]bool, len(c.main))
	for _, h := range c.main {
		onMain[h] = true
	}
	var branch []string // newest first
	cur := newTip
	for !onMain[cur] {
		branch = append(branch, cur)
		cur = c.index[cur].header.PrevHash
	}
	ancestor := cur
	ancestorHeight := c.index[ancestor].header.Height
	depth = int(c.height() - ancestorHeight)

	oldMain := append([]string{}, c.main...)
	var disconnected []*core.Block
	for _, h := range oldMain[ancestorHeight+1:] {
		if b, err := c.store.GetBlock(h); err == nil {
			disconnected = append(disconnected, b)
		}
	}

	// Rewind to the ancestor, using undo records where possible.
	for c.height() > ancestorHeight {
		if !c.disconnectTip() {
			c.main = c.main[:ancestorHeight+1]
			if err := c.rebuildState(); err != nil {
				return 0, 0, err
			}
			break
		}
	}

	// Apply the new branch oldest first.
	restore := func(cause error) error {
		c.main = oldMain
		if err := c.rebuildState(); err != nil {
			return fmt.Errorf("%v; restoring old chain also failed: %w", cause, err)
		}
		return cause
	}
	var connected []*core.Block
	for i := len(branch) - 1; i >= 0; i-- {
		b, err := c.store.GetBlock(branch[i])
		if err != nil {
			return 0, 0, restore(err)
		}
		if err := c.connect(b); err != nil {
			for _, h := range branch[:i+1] {
				c.invalid[h] = true
			}
			return 0, 0, restore(fmt.Errorf("reorg aborted at %s…: %w", branch[i][:12], err))
		}
		connected = append(connected, b)
	}

	batch := store.NewBatch()
	batch.TruncateMain(ancestorHeight + 1)
	for i, h := range c.main[ancestorHeight+1:] {
		batch.SetMain(ancestorHeight+1+int64(i), h)
	}
	if err := c.store.Commit(batch); err != nil {
		return 0, 0, restore(fmt.Errorf("storage: %w", err))
	}

	// Mempool: drop what the new branch included, return what only the
	// old branch had.
	for _, b := range connected {
		c.mempool.removeIncluded(b)
	}
	for _, b := range disconnected {
		for _, tx := range b.Transactions[1:] {
			_ = c.submitLocked(tx)
		}
	}
	c.revalidateMempool()
	log.Printf("REORG depth=%d %s… -> %s…", depth, oldMain[len(oldMain)-1][:12], newTip[:12])
	return depth, len(connected), nil
}
