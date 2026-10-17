package chain_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/consensus"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/miner"
	"github.com/entropychaindev/entropychain/go/internal/params"
)

func TestMain(m *testing.M) {
	// Instant mining: trivial difficulty and the minimum Argon2 memory.
	params.GenesisDifficulty = 1
	params.ArgonMemoryKB = 8
	os.Exit(m.Run())
}

type key struct {
	priv, pub []byte
	addr      string
}

func newKey(t *testing.T) key {
	t.Helper()
	priv, pub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return key{priv, pub, crypto.PubkeyToAddress(pub)}
}

func open(t *testing.T, dir string) *chain.Chain {
	t.Helper()
	c, err := chain.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// build mines a block on the current tip with exactly txs.
func build(t *testing.T, c *chain.Chain, to string, txs ...*core.Transaction) *core.Block {
	t.Helper()
	tpl := c.MiningTemplate(0)
	tpl.Txs, tpl.Fees = txs, 0
	for _, tx := range txs {
		tpl.Fees += tx.Fee
	}
	b := miner.BuildCandidate(tpl, to)
	if !miner.Solve(b, func() bool { return false }, nil) {
		t.Fatal("solve stopped")
	}
	return b
}

func mine(t *testing.T, c *chain.Chain, to string, txs ...*core.Transaction) *core.Block {
	t.Helper()
	b := build(t, c, to, txs...)
	if _, err := c.AddBlock(b); err != nil {
		t.Fatalf("AddBlock: %v", err)
	}
	return b
}

// signed returns a function that signs a constructor's result with k:
// signed(t, k)(core.NewTransfer(...)).
func signed(t *testing.T, k key) func(*core.Transaction, error) *core.Transaction {
	return func(tx *core.Transaction, err error) *core.Transaction {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.SignWith(k.priv, k.pub); err != nil {
			t.Fatal(err)
		}
		return tx
	}
}

var reward = consensus.BlockReward(1)

func TestGenesisAndPersistence(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir)
	if c.Height() != 0 || c.GenesisHash() != core.GenesisBlock().Hash() {
		t.Fatal("bad genesis")
	}
	a := newKey(t)
	for i := 0; i < 3; i++ {
		mine(t, c, a.addr)
	}
	tip := c.TipHash()
	c.Close()

	c = open(t, dir)
	defer c.Close()
	if c.Height() != 3 || c.TipHash() != tip {
		t.Fatalf("reopened at height %d", c.Height())
	}
	if got := c.Balance(a.addr, params.NativeToken); got != 3*reward {
		t.Fatalf("balance %d, want %d", got, 3*reward)
	}
}

func TestTransferAndFees(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a, b, m := newKey(t), newKey(t), newKey(t)
	mine(t, c, a.addr)

	tx := signed(t, a)(core.NewTransfer(a.addr, b.addr, 1000, "", 7))
	if err := c.Submit(tx); err != nil {
		t.Fatal(err)
	}
	if err := c.Submit(tx); err == nil {
		t.Fatal("duplicate mempool tx accepted")
	}
	tpl := c.MiningTemplate(miner.MaxTxsPerBlock)
	if len(tpl.Txs) != 1 || tpl.Fees != 7 {
		t.Fatalf("template txs=%d fees=%d", len(tpl.Txs), tpl.Fees)
	}
	blk := miner.BuildCandidate(tpl, m.addr)
	miner.Solve(blk, func() bool { return false }, nil)
	if _, err := c.AddBlock(blk); err != nil {
		t.Fatal(err)
	}
	if got := c.Balance(a.addr, "ENTRO"); got != reward-1000-7 {
		t.Errorf("sender %d", got)
	}
	if got := c.Balance(b.addr, "ENTRO"); got != 1000 {
		t.Errorf("recipient %d", got)
	}
	if got := c.Balance(m.addr, "ENTRO"); got != reward+7 {
		t.Errorf("miner %d", got)
	}
	if len(c.MempoolTxs()) != 0 {
		t.Error("mined tx still in mempool")
	}
	if h := c.History(b.addr, 10, -1); len(h) != 1 || h[0].Tx.Hash() != tx.Hash() {
		t.Errorf("history %v", h)
	}
	if err := c.Submit(tx); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("mined tx resubmitted: %v", err)
	}
}

func TestDoubleSpendInOneBlockRejected(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a, b := newKey(t), newKey(t)
	mine(t, c, a.addr)

	// Each transfer alone is affordable; together they overdraw.
	tx1 := signed(t, a)(core.NewTransfer(a.addr, b.addr, reward-10, "", 1))
	tx2 := signed(t, a)(core.NewTransfer(a.addr, b.addr, reward-10, "", 1))
	blk := build(t, c, a.addr, tx1, tx2)
	if _, err := c.AddBlock(blk); err == nil || !strings.Contains(err.Error(), "insufficient") {
		t.Fatalf("overdraft block accepted: %v", err)
	}
	if c.Height() != 1 || c.Balance(a.addr, "ENTRO") != reward {
		t.Fatal("state changed by rejected block")
	}

	// The miner's template must include only one of them.
	if err := c.Submit(tx1); err != nil {
		t.Fatal(err)
	}
	if err := c.Submit(tx2); err != nil {
		t.Fatal(err)
	}
	if tpl := c.MiningTemplate(10); len(tpl.Txs) != 1 {
		t.Fatalf("template has %d conflicting txs", len(tpl.Txs))
	}
}

func TestReplayRejected(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a, b := newKey(t), newKey(t)
	mine(t, c, a.addr)
	tx := signed(t, a)(core.NewTransfer(a.addr, b.addr, 5, "", 1))
	mine(t, c, a.addr, tx)
	blk := build(t, c, a.addr, tx)
	if _, err := c.AddBlock(blk); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("replayed tx accepted: %v", err)
	}
}

func TestBadBlocksRejected(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a := newKey(t)
	cases := map[string]func(b *core.Block){
		"coinbase amount": func(b *core.Block) {
			b.Transactions[0] = core.NewCoinbase(a.addr, reward+1, 1)
			b.MerkleRootHex = core.MerkleRoot(b.TxHashes())
		},
		"merkle":     func(b *core.Block) { b.MerkleRootHex = core.ZeroHash },
		"entropy":    func(b *core.Block) { b.EntropySeed = core.ZeroHash },
		"difficulty": func(b *core.Block) { b.Difficulty = 2 },
		"timestamp":  func(b *core.Block) { b.Timestamp = params.GenesisTimestamp - 1 },
	}
	for name, mutate := range cases {
		b := build(t, c, a.addr)
		mutate(b)
		miner.Solve(b, func() bool { return false }, nil)
		if _, err := c.AddBlock(b); err == nil {
			t.Errorf("%s: invalid block accepted", name)
		}
	}
	if c.Height() != 0 {
		t.Fatal("height changed")
	}
}

func TestOrphanAndKnown(t *testing.T) {
	src := open(t, t.TempDir())
	defer src.Close()
	dst := open(t, t.TempDir())
	defer dst.Close()
	a := newKey(t)
	b1 := mine(t, src, a.addr)
	b2 := mine(t, src, a.addr)
	if _, err := dst.AddBlock(b2); !errors.Is(err, chain.ErrOrphan) {
		t.Fatalf("want orphan, got %v", err)
	}
	if _, err := dst.AddBlock(b1); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.AddBlock(b1); !errors.Is(err, chain.ErrKnown) {
		t.Fatalf("want known, got %v", err)
	}
}

func TestReorg(t *testing.T) {
	dirA := t.TempDir()
	A := open(t, dirA)
	B := open(t, t.TempDir())
	defer B.Close()
	ka, kb, kc := newKey(t), newKey(t), newKey(t)

	// Shared history: two blocks, plus a transfer.
	for _, blk := range []*core.Block{mine(t, A, ka.addr), mine(t, A, ka.addr)} {
		if _, err := B.AddBlock(blk); err != nil {
			t.Fatal(err)
		}
	}
	// Fork: A mines 2 more (with a transfer), B mines 3 more.
	tx := signed(t, ka)(core.NewTransfer(ka.addr, kc.addr, 12345, "", 3))
	mine(t, A, ka.addr, tx)
	mine(t, A, ka.addr)
	var bBlocks []*core.Block
	for i := 0; i < 3; i++ {
		bBlocks = append(bBlocks, mine(t, B, kb.addr))
	}
	for i, blk := range bBlocks {
		res, err := A.AddBlock(blk)
		if err != nil {
			t.Fatalf("block %d: %v", i, err)
		}
		if i == 2 && !strings.Contains(res, "reorg") {
			t.Fatalf("expected reorg, got %q", res)
		}
	}
	check := func(c *chain.Chain) {
		t.Helper()
		if c.TipHash() != B.TipHash() || c.Height() != 5 {
			t.Fatalf("tip %d %s, want %s", c.Height(), c.TipHash(), B.TipHash())
		}
		if c.Balance(ka.addr, "ENTRO") != 2*reward || c.Balance(kb.addr, "ENTRO") != 3*reward {
			t.Fatal("balances do not match the winning branch")
		}
		if c.Balance(kc.addr, "ENTRO") != 0 {
			t.Fatal("transfer from losing branch still applied")
		}
	}
	check(A)
	// The transfer from the losing branch is back in the mempool.
	if A.MempoolTx(tx.Hash()) == nil {
		t.Error("disconnected tx not returned to mempool")
	}
	A.Close()

	// The reorg is durable.
	A = open(t, dirA)
	defer A.Close()
	check(A)
}

func TestTokensNFTsLottery(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a, b, m := newKey(t), newKey(t), newKey(t)
	mine(t, c, a.addr)
	mine(t, c, b.addr)

	mine(t, c, m.addr,
		signed(t, a)(core.NewTokenCreate(a.addr, "chaos", "Chaos", 1_000_000, 8, 100)),
		signed(t, a)(core.NewNFTCreate(a.addr, "cats", "Cats", "meow", 100)),
	)
	if tok, ok := c.Token("CHAOS"); !ok || tok.TotalSupply != 1_000_000 || c.Balance(a.addr, "CHAOS") != 1_000_000 {
		t.Fatalf("token create failed: %+v", tok)
	}
	h := c.Height()
	mine(t, c, m.addr,
		signed(t, a)(core.NewTokenMint(a.addr, "CHAOS", b.addr, 500, 10)),
		signed(t, a)(core.NewTransfer(a.addr, b.addr, 250, "CHAOS", 1)),
		signed(t, a)(core.NewNFTMint(a.addr, "cats", "001", b.addr, "ipfs://1", map[string]any{"fluff": int64(9)}, 5)),
		signed(t, a)(core.NewNFTRandomMint(a.addr, "cats", "002", a.addr, "", 5)),
		signed(t, a)(core.NewLotteryCreate(a.addr, "lot", 1000, 10, h+3, 1, "", 50)),
	)
	if c.Balance(b.addr, "CHAOS") != 750 {
		t.Errorf("CHAOS balance %d", c.Balance(b.addr, "CHAOS"))
	}
	if n, ok := c.NFT("cats", "002"); !ok || n.EntropySourceHeight == nil || *n.EntropySourceHeight != h+2 || !n.TraitsPending {
		t.Errorf("random mint should await block %d: %+v", h+2, n)
	}
	mine(t, c, m.addr,
		signed(t, b)(core.NewNFTTransfer(b.addr, "cats", "001", a.addr, 1)),
		signed(t, b)(core.NewLotteryEnter(b.addr, "lot", 1)),
	)
	if n, _ := c.NFT("cats", "001"); n.Owner != a.addr {
		t.Error("NFT transfer failed")
	}

	// Draw is not allowed in the draw block itself...
	lot, _ := c.Lottery("lot")
	for c.Height()+1 < lot.DrawHeight {
		mine(t, c, m.addr)
	}
	draw := signed(t, a)(core.NewLotteryDraw(a.addr, "lot", 1))
	if err := c.Submit(draw); err == nil {
		t.Fatal("draw accepted for the draw block itself")
	}
	mine(t, c, m.addr)
	// ...only after it.
	before := c.Balance(b.addr, "ENTRO")
	mine(t, c, m.addr, draw)
	lot, _ = c.Lottery("lot")
	if !lot.Drawn || lot.Winner == nil || *lot.Winner != b.addr {
		t.Fatalf("lottery not drawn correctly: %+v", lot)
	}
	if c.Balance(b.addr, "ENTRO") != before+1000+10 {
		t.Error("winner not paid prize + pool")
	}
	if *lot.EntropyUsed != consensus.Beacon(c.HashAtHeight(lot.DrawHeight)) {
		t.Error("lottery did not use the draw block's beacon")
	}

	// Token lotteries with a ticket price are rejected.
	bad := signed(t, a)(core.NewLotteryCreate(a.addr, "lot2", 10, 5, c.Height()+5, 1, "CHAOS", 50))
	if err := c.Submit(bad); err == nil {
		t.Error("token lottery with ticket price accepted")
	}
}

func TestStrictTypes(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a, b := newKey(t), newKey(t)
	mine(t, c, a.addr)
	for name, data := range map[string]map[string]any{
		"float amount":  {"recipient": b.addr, "amount": json.Number("1.5"), "token": "ENTRO"},
		"string amount": {"recipient": b.addr, "amount": "100", "token": "ENTRO"},
		"bool amount":   {"recipient": b.addr, "amount": true, "token": "ENTRO"},
		"huge amount":   {"recipient": b.addr, "amount": json.Number("99999999999999999999"), "token": "ENTRO"},
	} {
		tx := signed(t, a)(core.NewTx(core.TxTransfer, a.addr, data, 1))
		if err := c.Submit(tx); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	neg := signed(t, a)(core.NewTransfer(a.addr, b.addr, 5, "", -1))
	if err := c.Submit(neg); err == nil {
		t.Error("negative fee accepted")
	}
}

// Beacon v2: the randomness a block contributes is a function of its hash,
// which covers the proof-of-work nonce, so it cannot be chosen in advance.
func TestBeaconBoundToProofOfWork(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a := newKey(t)
	b1 := build(t, c, a.addr)
	b2 := *b1
	b2.Nonce++ // same header, same miner-chosen header seed
	if b1.EntropySeed != b2.EntropySeed {
		t.Fatal("test setup: header seeds should match")
	}
	if consensus.Beacon(b1.Hash()) == consensus.Beacon(b2.Hash()) {
		t.Fatal("beacon does not depend on the proof-of-work nonce")
	}
	if _, err := c.AddBlock(b1); err != nil {
		t.Fatal(err)
	}
	if c.Beacon(1) != consensus.Beacon(b1.Hash()) {
		t.Error("chain beacon differs from consensus.Beacon(hash)")
	}
}

// A random mint's traits come from the block after the one including it,
// which does not exist when the minter signs, so token ids cannot be ground
// against a known seed.
func TestRandomMintRevealedByNextBlock(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a, m := newKey(t), newKey(t)
	mine(t, c, a.addr)
	mine(t, c, m.addr, signed(t, a)(core.NewNFTCreate(a.addr, "cats", "Cats", "", 100)))

	old := signed(t, a)(core.NewTx(core.TxNFTRandomMint, a.addr, map[string]any{
		"collection_id": "cats", "token_id": "x", "recipient": a.addr, "entropy_height": int64(1),
	}, 5))
	if err := c.Submit(old); err == nil || !strings.Contains(err.Error(), "entropy_height") {
		t.Fatalf("mint choosing its own entropy block accepted: %v", err)
	}

	blk := mine(t, c, m.addr, signed(t, a)(core.NewNFTRandomMint(a.addr, "cats", "001", a.addr, "", 5)))
	n, _ := c.NFT("cats", "001")
	if !n.TraitsPending || n.Traits != nil || *n.EntropySourceHeight != blk.Height+1 {
		t.Fatalf("traits should be pending until block %d: %+v", blk.Height+1, n)
	}
	next := mine(t, c, m.addr)
	n, _ = c.NFT("cats", "001")
	want := consensus.DeriveRandomTraits(consensus.Beacon(next.Hash()), "cats", "001", next.Height)
	got, ok := n.Traits.(consensus.RandomTraits)
	if n.TraitsPending || !ok || got != want {
		t.Fatalf("traits %+v, want %+v from the next block's beacon", n.Traits, want)
	}
	if owned := c.NFTsByOwner(a.addr); len(owned) != 1 || owned[0].Traits == nil {
		t.Error("NFTsByOwner did not resolve traits")
	}
}

// draw_blocks aggregates consecutive beacons; the draw waits for all of them.
func TestLotteryAggregatesBeacons(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	org, p1, p2, m := newKey(t), newKey(t), newKey(t), newKey(t)
	mine(t, c, org.addr)
	mine(t, c, p1.addr)
	mine(t, c, p2.addr)
	d := c.Height() + 3
	mine(t, c, m.addr, signed(t, org)(core.NewLotteryCreate(org.addr, "big", 1000, 7, d, 3, "", 50)))
	mine(t, c, m.addr,
		signed(t, p1)(core.NewLotteryEnter(p1.addr, "big", 1)),
		signed(t, p2)(core.NewLotteryEnter(p2.addr, "big", 1)))
	if l, _ := c.Lottery("big"); l.DrawBlocks != 3 || len(l.Entrants) != 2 {
		t.Fatalf("lottery state %+v", l)
	}
	for c.Height() < d+2 { // mine through the whole window D..D+2
		draw := signed(t, org)(core.NewLotteryDraw(org.addr, "big", 1))
		if err := c.Submit(draw); err == nil {
			t.Fatalf("draw accepted at height %d, before the window ends at %d", c.Height()+1, d+2)
		}
		mine(t, c, m.addr)
	}
	mine(t, c, m.addr, signed(t, org)(core.NewLotteryDraw(org.addr, "big", 1)))
	l, _ := c.Lottery("big")
	seed := consensus.AggregateBeacons([]string{c.Beacon(d), c.Beacon(d + 1), c.Beacon(d + 2)})
	if !l.Drawn || *l.EntropyUsed != seed || *l.Winner != consensus.PickWinner(seed, "big", l.Entrants) {
		t.Fatalf("draw did not use the aggregated beacon: %+v", l)
	}

	tooMany := signed(t, org)(core.NewLotteryCreate(org.addr, "huge", 10, 0, c.Height()+5, 17, "", 50))
	if err := c.Submit(tooMany); err == nil {
		t.Error("draw_blocks above the maximum accepted")
	}
}

// Clients read lottery.entrants.length, so an empty lottery must serialize
// its entrants as [] rather than null.
func TestEmptyLotteryEntrantsSerializeAsArray(t *testing.T) {
	c := open(t, t.TempDir())
	defer c.Close()
	a := newKey(t)
	mine(t, c, a.addr)
	mine(t, c, a.addr, signed(t, a)(core.NewLotteryCreate(a.addr, "empty", 100, 1, c.Height()+5, 1, "", 50)))
	l, _ := c.Lottery("empty")
	raw, _ := json.Marshal(l)
	if !strings.Contains(string(raw), `"entrants":[]`) {
		t.Fatalf("entrants not an empty array: %s", raw)
	}
}

// A data directory from another network (a reset testnet) is reported with
// ErrWrongNetwork so callers such as the desktop app can move it aside.
func TestWrongNetworkDetected(t *testing.T) {
	dir := t.TempDir()
	c := open(t, dir)
	c.Close()
	saved := params.GenesisDifficulty
	params.GenesisDifficulty = 2 // a different genesis = a different network
	defer func() { params.GenesisDifficulty = saved }()
	if _, err := chain.Open(dir); !errors.Is(err, chain.ErrWrongNetwork) {
		t.Fatalf("want ErrWrongNetwork, got %v", err)
	}
}
