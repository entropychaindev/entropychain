package stats_test

import (
	"os"
	"testing"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/miner"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/stats"
)

func TestMain(m *testing.M) {
	params.GenesisDifficulty = 1
	params.ArgonMemoryKB = 8
	os.Exit(m.Run())
}

func mine(t *testing.T, c *chain.Chain, to string, txs ...*core.Transaction) {
	t.Helper()
	tpl := c.MiningTemplate(0)
	tpl.Txs = txs
	for _, tx := range txs {
		tpl.Fees += tx.Fee
	}
	b := miner.BuildCandidate(tpl, to)
	miner.Solve(b, func() bool { return false }, nil)
	if _, err := c.AddBlock(b); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshot(t *testing.T) {
	c, err := chain.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	priv, pub, _ := crypto.GenerateKeypair()
	a := crypto.PubkeyToAddress(pub)
	_, pubB, _ := crypto.GenerateKeypair()
	b := crypto.PubkeyToAddress(pubB)

	for i := 0; i < 30; i++ {
		mine(t, c, a)
	}
	tx, _ := core.NewTransfer(a, b, 1000, "", 1)
	_ = tx.SignWith(priv, pub)
	mine(t, c, b, tx)

	col := stats.New(c, nil)
	v := col.Snapshot(0, "all")
	if v.Blocks != 31 || v.ToHeight != 31 {
		t.Fatalf("blocks %d to %d", v.Blocks, v.ToHeight)
	}
	// Genesis is dated months before these blocks were mined; that gap
	// must not count as block time.
	if v.AvgBlock > 60 {
		t.Errorf("average block time %.1f s includes the genesis gap", v.AvgBlock)
	}
	if v.MinerCount != 2 || v.Miners[0].Miner != a || v.Miners[0].Blocks != 30 {
		t.Errorf("miners %+v", v.Miners)
	}
	if v.TxTotal != 1 || v.TxTotals[0] != 1 {
		t.Errorf("tx totals %v", v.TxTotals)
	}
	if v.BeaconN != 31*32 || v.PValue <= 0 || v.PValue > 1 {
		t.Errorf("beacon n=%d p=%f", v.BeaconN, v.PValue)
	}
	if len(v.Windows) != 2 || v.WindowSize != params.DifficultyWindow {
		t.Errorf("windows %d of size %d", len(v.Windows), v.WindowSize)
	}
	if last := col.Snapshot(10, "10"); last.Blocks != 10 || last.FromHeight != 22 {
		t.Errorf("range of 10 gave %d blocks from %d", last.Blocks, last.FromHeight)
	}
}
