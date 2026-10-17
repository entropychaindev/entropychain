package chain

import (
	"errors"
	"sort"

	"github.com/entropychaindev/entropychain/go/internal/core"
)

const (
	// MempoolMaxSize matches blockchain.py MEMPOOL_MAX_SIZE.
	MempoolMaxSize = 10_000
	// maxTxPayload bounds a single transaction (policy, not consensus).
	maxTxPayload = 32 * 1024
)

// mempool holds pending transactions in arrival order. Every transaction
// in it has had its signature verified once.
type mempool struct {
	txs   []*core.Transaction
	byKey map[string]*core.Transaction
}

func newMempool() *mempool { return &mempool{byKey: map[string]*core.Transaction{}} }

func (m *mempool) has(h string) bool {
	_, ok := m.byKey[h]
	return ok
}

func (m *mempool) add(tx *core.Transaction) {
	m.txs = append(m.txs, tx)
	m.byKey[tx.Hash()] = tx
}

func (m *mempool) filter(keep func(*core.Transaction) bool) {
	out := m.txs[:0]
	for _, tx := range m.txs {
		if keep(tx) {
			out = append(out, tx)
		} else {
			delete(m.byKey, tx.Hash())
		}
	}
	for i := len(out); i < len(m.txs); i++ {
		m.txs[i] = nil
	}
	m.txs = out
}

func (m *mempool) removeIncluded(b *core.Block) {
	inc := make(map[string]bool, len(b.Transactions))
	for _, tx := range b.Transactions {
		inc[tx.Hash()] = true
	}
	m.filter(func(tx *core.Transaction) bool { return !inc[tx.Hash()] })
}

// byFee returns pending txs sorted by fee, highest first (stable).
func (m *mempool) byFee() []*core.Transaction {
	out := append([]*core.Transaction{}, m.txs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Fee > out[j].Fee })
	return out
}

// Submit validates tx against the tip state and queues it.
func (c *Chain) Submit(tx *core.Transaction) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submitLocked(tx)
}

func (c *Chain) submitLocked(tx *core.Transaction) error {
	if len(tx.SigningPayload()) > maxTxPayload {
		return errors.New("transaction too large")
	}
	if err := c.state.checkTx(tx, ctx{height: c.height() + 1, beaconAt: c.beaconAt}); err != nil {
		return err
	}
	if c.mempool.has(tx.Hash()) {
		return errors.New("transaction already in mempool")
	}
	if len(c.mempool.txs) >= MempoolMaxSize {
		lowest := c.mempool.txs[0]
		for _, p := range c.mempool.txs {
			if p.Fee < lowest.Fee {
				lowest = p
			}
		}
		if tx.Fee <= lowest.Fee {
			return errors.New("mempool full and your fee is too low")
		}
		c.mempool.filter(func(p *core.Transaction) bool { return p != lowest })
	}
	c.mempool.add(tx)
	return nil
}

// revalidateMempool drops transactions the new tip state makes invalid.
func (c *Chain) revalidateMempool() {
	cx := ctx{height: c.height() + 1, beaconAt: c.beaconAt, skipSig: true}
	c.mempool.filter(func(tx *core.Transaction) bool { return c.state.checkTx(tx, cx) == nil })
}

// Template is what the miner needs to build a candidate block.
type Template struct {
	PrevHash     string
	Height       int64
	Difficulty   uint64
	MinTimestamp float64 // block timestamp must be greater than this
	Txs          []*core.Transaction
	Fees         int64
}

// MiningTemplate selects up to maxTxs mempool transactions, highest fee
// first, that are valid when applied in sequence on the tip state.
func (c *Chain) MiningTemplate(maxTxs int) Template {
	c.mu.Lock()
	defer c.mu.Unlock()
	tip := c.tipNode()
	t := Template{
		PrevHash:     tip.hash,
		Height:       tip.header.Height + 1,
		Difficulty:   c.difficultyFor(tip, tip.header.Height+1),
		MinTimestamp: c.medianTimePast(tip),
	}
	cx := ctx{height: t.Height, beaconAt: c.beaconAt, skipSig: true}
	start := c.state.mark()
	for _, tx := range c.mempool.byFee() {
		if len(t.Txs) >= maxTxs {
			break
		}
		m := c.state.mark()
		if c.state.checkTx(tx, cx) != nil {
			continue
		}
		fees, err := addChecked(t.Fees, tx.Fee)
		if err != nil || c.state.applyTx(tx, cx) != nil {
			c.state.rollback(m)
			continue
		}
		t.Fees = fees
		t.Txs = append(t.Txs, tx)
	}
	c.state.rollback(start)
	return t
}
