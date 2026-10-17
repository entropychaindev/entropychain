package chain

import (
	"math/big"
	"sort"

	"github.com/entropychaindev/entropychain/go/internal/core"
)

// Read-only accessors. All return copies or immutable values.

// Height returns the tip height.
func (c *Chain) Height() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.height()
}

// TipHash returns the tip block hash.
func (c *Chain) TipHash() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tipHash()
}

// GenesisHash returns the genesis block hash.
func (c *Chain) GenesisHash() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.main[0]
}

// TipInfo is a consistent snapshot of the tip.
type TipInfo struct {
	Hash        string
	Height      int64
	Work        *big.Int
	Difficulty  uint64
	EntropySeed string
	Timestamp   float64
}

// Tip returns a snapshot of the tip.
func (c *Chain) Tip() TipInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := c.tipNode()
	return TipInfo{Hash: n.hash, Height: n.header.Height, Work: new(big.Int).Set(n.work),
		Difficulty: n.header.Difficulty, EntropySeed: n.header.EntropySeed, Timestamp: n.header.Timestamp}
}

// HasBlock reports whether the block is in the index.
func (c *Chain) HasBlock(hash string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.index[hash]
	return ok
}

// BlockByHash loads any indexed block.
func (c *Chain) BlockByHash(hash string) *core.Block {
	c.mu.RLock()
	_, ok := c.index[hash]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	b, err := c.store.GetBlock(hash)
	if err != nil {
		return nil
	}
	return b
}

// HashAtHeight returns the main-chain hash at height, or "".
func (c *Chain) HashAtHeight(height int64) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if height < 0 || height >= int64(len(c.main)) {
		return ""
	}
	return c.main[height]
}

// BlockByHeight loads the main-chain block at height.
func (c *Chain) BlockByHeight(height int64) *core.Block {
	h := c.HashAtHeight(height)
	if h == "" {
		return nil
	}
	return c.BlockByHash(h)
}

// Balance returns one token balance.
func (c *Chain) Balance(addr, token string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state.balance(addr, token)
}

// Balances returns all token balances of an address.
func (c *Chain) Balances(addr string) map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]int64{}
	for k, v := range c.state.balances[addr] {
		out[k] = v
	}
	return out
}

// TokenSymbols returns token symbols in creation order.
func (c *Chain) TokenSymbols() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string{}, c.state.tokenOrder...)
}

// Tokens returns all tokens.
func (c *Chain) Tokens() map[string]Token {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]Token, len(c.state.tokens))
	for k, v := range c.state.tokens {
		out[k] = *v
	}
	return out
}

// Token returns one token.
func (c *Chain) Token(sym string) (Token, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.state.tokens[sym]
	if !ok {
		return Token{}, false
	}
	return *t, true
}

// NFTRef is an NFT with its identifiers.
type NFTRef struct {
	Collection string
	TokenID    string
	NFT
}

// NFT returns one NFT.
func (c *Chain) NFT(collection, tokenID string) (NFT, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	k := nftKey{collection, tokenID}
	n, ok := c.state.nfts[k]
	if !ok {
		return NFT{}, false
	}
	return c.resolveNFT(k, *n), true
}

// NFTsByOwner returns an owner's NFTs, newest mint first.
func (c *Chain) NFTsByOwner(owner string) []NFTRef {
	c.mu.RLock()
	var out []NFTRef
	for k, n := range c.state.nfts {
		if n.Owner == owner {
			out = append(out, NFTRef{k.collection, k.token, c.resolveNFT(k, *n)})
		}
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.MintedAtHeight != b.MintedAtHeight {
			return a.MintedAtHeight > b.MintedAtHeight
		}
		if a.Collection != b.Collection {
			return a.Collection > b.Collection
		}
		return a.TokenID > b.TokenID
	})
	return out
}

// CollectionRef is a collection with its id and mint count.
type CollectionRef struct {
	ID          string
	MintedCount int
	Collection
}

// Collections returns collections (optionally by creator), newest first.
func (c *Chain) Collections(creator string) []CollectionRef {
	c.mu.RLock()
	counts := map[string]int{}
	for k := range c.state.nfts {
		counts[k.collection]++
	}
	var out []CollectionRef
	for id, col := range c.state.collections {
		if creator != "" && col.Creator != creator {
			continue
		}
		out = append(out, CollectionRef{id, counts[id], *col})
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtHeight != out[j].CreatedAtHeight {
			return out[i].CreatedAtHeight > out[j].CreatedAtHeight
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Lottery returns one lottery.
func (c *Chain) Lottery(id string) (Lottery, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	l, ok := c.state.lotteries[id]
	if !ok {
		return Lottery{}, false
	}
	return *l.clone(), true
}

// Beacon returns the beacon of the main-chain block at height, or "".
func (c *Chain) Beacon(height int64) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if height < 0 || height >= int64(len(c.main)) {
		return ""
	}
	return c.beaconAt(height)
}

// Counts returns (nft count, lottery count).
func (c *Chain) Counts() (int, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.state.nfts), len(c.state.lotteries)
}

// MempoolTxs returns pending transactions in arrival order.
func (c *Chain) MempoolTxs() []*core.Transaction {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]*core.Transaction{}, c.mempool.txs...)
}

// MempoolTx returns a pending transaction by hash.
func (c *Chain) MempoolTx(hash string) *core.Transaction {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mempool.byKey[hash]
}

// KnowsTx reports whether a tx is pending or already on chain.
func (c *Chain) KnowsTx(hash string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mempool.has(hash) || c.state.seen(hash)
}

// HistoryItem is a transaction with the height of its block.
type HistoryItem struct {
	Height int64
	Tx     *core.Transaction
}

// History returns up to limit txs involving addr, newest first, optionally
// only from blocks below beforeHeight (>= 0).
func (c *Chain) History(addr string, limit int, beforeHeight int64) []HistoryItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := c.state.txIndex[addr]
	var out []HistoryItem
	for i := len(entries) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		e := entries[i]
		if beforeHeight >= 0 && e.Height >= beforeHeight {
			continue
		}
		if loc, ok := c.state.txByHash[e.Hash]; ok {
			out = append(out, HistoryItem{loc.height, loc.tx})
		}
	}
	return out
}

// TxByHash returns a main-chain transaction and its block height.
func (c *Chain) TxByHash(hash string) (HistoryItem, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	loc, ok := c.state.txByHash[hash]
	if !ok {
		return HistoryItem{}, false
	}
	return HistoryItem{loc.height, loc.tx}, true
}

// IsMainChain reports whether hash is on the active chain.
func (c *Chain) IsMainChain(hash string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.index[hash]
	if !ok {
		return false
	}
	h := n.header.Height
	return h < int64(len(c.main)) && c.main[h] == hash
}

// Work returns the cumulative work of an indexed block (nil if unknown).
func (c *Chain) Work(hash string) *big.Int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if n, ok := c.index[hash]; ok {
		return new(big.Int).Set(n.work)
	}
	return nil
}

// Holder is an address and its balance of one token.
type Holder struct {
	Address string
	Balance int64
}

// Holders returns addresses with a positive balance of token, largest
// first, up to limit, plus the total number of holders.
func (c *Chain) Holders(token string, limit int) ([]Holder, int) {
	c.mu.RLock()
	var out []Holder
	for addr, bals := range c.state.balances {
		if v := bals[token]; v > 0 {
			out = append(out, Holder{addr, v})
		}
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Balance != out[j].Balance {
			return out[i].Balance > out[j].Balance
		}
		return out[i].Address < out[j].Address
	})
	total := len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total
}

// LotteryRef is a lottery with its id.
type LotteryRef struct {
	ID string
	Lottery
}

// Lotteries returns every lottery, newest first.
func (c *Chain) Lotteries() []LotteryRef {
	c.mu.RLock()
	out := make([]LotteryRef, 0, len(c.state.lotteries))
	for id, l := range c.state.lotteries {
		out = append(out, LotteryRef{id, *l.clone()})
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtHeight != out[j].CreatedAtHeight {
			return out[i].CreatedAtHeight > out[j].CreatedAtHeight
		}
		return out[i].ID < out[j].ID
	})
	return out
}
