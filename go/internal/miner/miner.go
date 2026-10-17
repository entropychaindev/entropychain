// Package miner assembles candidate blocks and searches for Proof-of-Entropy
// nonces on multiple CPU threads.
package miner

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/consensus"
	"github.com/entropychaindev/entropychain/go/internal/core"
)

// refreshInterval is how often a candidate is rebuilt to pick up new
// mempool transactions even if the tip has not moved.
const refreshInterval = 20 * time.Second

// MaxTxsPerBlock matches miner.py's default.
const MaxTxsPerBlock = 100

// BuildCandidate assembles an unsolved block on top of the template.
func BuildCandidate(t chain.Template, address string) *core.Block {
	prev, _ := hex.DecodeString(t.PrevHash)
	minerNonce := make([]byte, 16)
	_, _ = rand.Read(minerNonce)

	coinbase := core.NewCoinbase(address, consensus.BlockReward(t.Height)+t.Fees, t.Height)
	txs := append([]*core.Transaction{coinbase}, t.Txs...)
	ts := core.Now()
	if ts <= t.MinTimestamp {
		ts = t.MinTimestamp + 0.001
	}
	b := &core.Block{
		Height:       t.Height,
		PrevHash:     t.PrevHash,
		Timestamp:    ts,
		Difficulty:   t.Difficulty,
		EntropySeed:  hex.EncodeToString(consensus.DeriveEntropySeed(prev, minerNonce)),
		MinerNonce:   hex.EncodeToString(minerNonce),
		Transactions: txs,
	}
	b.MerkleRootHex = core.MerkleRoot(b.TxHashes())
	return b
}

func randomNonce() uint64 {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return binary.LittleEndian.Uint64(buf[:])
}

// Solve searches nonces on one goroutine until a solution is found or stop
// returns true. It returns false if stopped. hashes counts attempts.
func Solve(b *core.Block, stop func() bool, hashes *atomic.Uint64) bool {
	header := b.HeaderBytes()
	target := consensus.Target(b.Difficulty)
	for !stop() {
		nonce := randomNonce()
		h := consensus.PowHash(header, nonce)
		if hashes != nil {
			hashes.Add(1)
		}
		if consensus.HashMeetsTarget(h, target) {
			b.Nonce = nonce
			return true
		}
		runtime.Gosched()
	}
	return false
}

// Miner mines continuously on top of the chain tip.
type Miner struct {
	chain   *chain.Chain
	address string
	threads int
	ready   func() bool

	hashes  atomic.Uint64
	blocks  atomic.Uint64
	waiting atomic.Bool
}

// New creates a miner paying rewards to address. If ready is non-nil the
// miner only works while it returns true (typically "synced with peers"),
// so it never extends a stale or private chain.
func New(c *chain.Chain, address string, threads int, ready func() bool) *Miner {
	if threads < 1 {
		threads = 1
	}
	return &Miner{chain: c, address: address, threads: threads, ready: ready}
}

// Waiting reports whether the miner is paused waiting for sync.
func (m *Miner) Waiting() bool { return m.waiting.Load() }

// HashCount returns total hash attempts so far.
func (m *Miner) HashCount() uint64 { return m.hashes.Load() }

// BlocksFound returns how many blocks this miner got accepted.
func (m *Miner) BlocksFound() uint64 { return m.blocks.Load() }

// Run mines until ctx is cancelled.
func (m *Miner) Run(ctx context.Context) {
	log.Printf("Mining to %s on %d thread(s)", m.address, m.threads)
	go m.reportRate(ctx)
	for ctx.Err() == nil {
		if m.ready != nil && !m.ready() {
			if !m.waiting.Swap(true) {
				log.Printf("Waiting to sync with peers before mining…")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if m.waiting.Swap(false) {
			log.Printf("Synced; mining resumed")
		}
		t := m.chain.MiningTemplate(MaxTxsPerBlock)
		cand := BuildCandidate(t, m.address)
		started := time.Now()

		var found atomic.Pointer[core.Block]
		stop := func() bool {
			return ctx.Err() != nil || found.Load() != nil ||
				m.chain.TipHash() != t.PrevHash || time.Since(started) > refreshInterval ||
				(m.ready != nil && !m.ready())
		}
		var wg sync.WaitGroup
		for i := 0; i < m.threads; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Each worker needs its own copy: Solve writes the nonce.
				b := *cand
				if Solve(&b, stop, &m.hashes) {
					found.CompareAndSwap(nil, &b)
				}
			}()
		}
		wg.Wait()

		b := found.Load()
		if b == nil {
			continue // tip moved, refresh due, or shutting down
		}
		res, err := m.chain.AddBlock(b)
		switch {
		case err == nil:
			m.blocks.Add(1)
			log.Printf("⛏  Mined block %d %s… in %s (%s)", b.Height, b.Hash()[:12],
				time.Since(started).Round(time.Millisecond), res)
		case errors.Is(err, chain.ErrKnown):
		default:
			log.Printf("Local block rejected: %v", err)
		}
	}
}

func (m *Miner) reportRate(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	last := m.hashes.Load()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := m.hashes.Load()
			log.Printf("Hash rate: %.1f H/s, blocks found: %d", float64(now-last)/60, m.blocks.Load())
			last = now
		}
	}
}
