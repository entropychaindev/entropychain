package questmaster

import (
	"os"
	"strings"
	"testing"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/miner"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/quest"
)

func TestMain(m *testing.M) {
	params.GenesisDifficulty = 1
	params.ArgonMemoryKB = 8
	os.Exit(m.Run())
}

type key struct {
	priv, pub []byte
	addr      string
}

func newKey(t *testing.T) key {
	priv, pub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return key{priv, pub, crypto.PubkeyToAddress(pub)}
}

// world is a chain plus the game master following it.
type world struct {
	t    *testing.T
	c    *chain.Chain
	gm   key
	m    *Master
	next int64 // next height the master has not applied
}

func newWorld(t *testing.T) *world {
	c, err := chain.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	w := &world{t: t, c: c, gm: newKey(t)}
	w.m = NewMaster(w.gm.addr, "eq-heroes", "eq-items", "https://example.test/quest/", 1)
	w.next = 1
	return w
}

// mine includes the mempool in a new block, paying the reward to addr.
func (w *world) mine(addr string) {
	w.t.Helper()
	tpl := w.c.MiningTemplate(1000)
	b := miner.BuildCandidate(tpl, addr)
	miner.Solve(b, func() bool { return false }, nil)
	if _, err := w.c.AddBlock(b); err != nil {
		w.t.Fatal(err)
	}
}

// sync applies every block that has a successor, as questd does.
func (w *world) sync() {
	for ; w.next+1 <= w.c.Height(); w.next++ {
		w.m.Apply(FromCore(w.c.BlockByHeight(w.next)), FromCore(w.c.BlockByHeight(w.next+1)))
	}
}

// deliver signs and submits everything the master owes, then mines it.
func (w *world) deliver() {
	w.t.Helper()
	for _, o := range w.m.Unsent() {
		if err := o.Tx.SignWith(w.gm.priv, w.gm.pub); err != nil {
			w.t.Fatal(err)
		}
		if err := w.c.Submit(o.Tx); err != nil {
			w.t.Fatalf("submit %s: %v", o.Kind, err)
		}
	}
	w.mine(w.gm.addr)
	w.mine(w.gm.addr)
	w.sync()
}

func (w *world) send(k key, data map[string]any, amount int64) string {
	w.t.Helper()
	d := map[string]any{"recipient": w.gm.addr, "amount": amount, "token": "ENTRO", "game": quest.GameTag}
	for kk, v := range data {
		d[kk] = v
	}
	tx, err := core.NewTx(core.TxTransfer, k.addr, d, 10)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := tx.SignWith(k.priv, k.pub); err != nil {
		w.t.Fatal(err)
	}
	if err := w.c.Submit(tx); err != nil {
		w.t.Fatal(err)
	}
	return tx.Hash()
}

func TestSummonRunPayoutAndReplay(t *testing.T) {
	w := newWorld(t)
	alice, bob := newKey(t), newKey(t)
	for range 3 {
		w.mine(alice.addr) // 150 ENTRO for alice
	}
	w.mine(w.gm.addr) // treasury
	w.mine(w.gm.addr)
	w.mine(w.gm.addr)
	for _, col := range []string{"eq-heroes", "eq-items"} {
		tx, _ := core.NewTx(core.TxNFTCreate, w.gm.addr, map[string]any{"collection_id": col, "name": col, "description": ""}, 10)
		_ = tx.SignWith(w.gm.priv, w.gm.pub)
		if err := w.c.Submit(tx); err != nil {
			t.Fatal(err)
		}
	}
	w.mine(w.gm.addr)

	// Summon: one block to include it, one more for its beacon.
	summon := w.send(alice, map[string]any{"action": "summon"}, quest.SummonFee)
	w.mine(bob.addr)
	w.mine(bob.addr)
	w.sync()
	hid := w.m.Summons[summon]
	hs := w.m.Heroes[hid]
	if hs == nil || hs.Owner != alice.addr {
		t.Fatalf("hero not summoned: %+v", w.m.Summons)
	}
	w.deliver()
	nft, ok := w.c.NFT("eq-heroes", hid)
	if !ok || nft.Owner != alice.addr {
		t.Fatalf("hero NFT not minted to alice: %+v", nft)
	}
	if h, ok := quest.HeroFromTraits(hid, nft.Traits.(map[string]any)); !ok || h != hs.Hero {
		t.Fatalf("on-chain traits differ from the hero: %+v", h)
	}

	// Run the first dungeon several times; payouts and items arrive.
	before := w.c.Balance(alice.addr, "ENTRO")
	var paid, fees int64
	var runs []string
	for range 6 {
		d, _ := quest.DungeonByID(1)
		runs = append(runs, w.send(alice, map[string]any{"action": "run", "hero": hid, "dungeon": 1}, d.Fee))
		fees += d.Fee + 10
		w.mine(bob.addr)
	}
	w.mine(bob.addr)
	w.sync()
	for _, r := range runs {
		rec := w.m.Runs[r]
		if rec == nil {
			t.Fatalf("run %s not resolved", r[:8])
		}
		got := quest.Run(rec.Hero, rec.Before, rec.Gear, quest.Dungeons[0], rec.Beacon, rec.Tx)
		if got.Payout != rec.Result.Payout || len(got.Events) != len(rec.Result.Events) {
			t.Fatal("recomputing the run from its record gave a different result")
		}
		paid += rec.Result.Payout
	}
	if w.m.Heroes[hid].Progress.Runs != 6 {
		t.Fatalf("hero has %d runs", w.m.Heroes[hid].Progress.Runs)
	}
	w.deliver()
	if got := w.c.Balance(alice.addr, "ENTRO"); got != before-fees+paid {
		t.Fatalf("alice has %d, want %d (paid %d)", got, before-fees+paid, paid)
	}
	for id, it := range w.m.Items {
		if n, ok := w.c.NFT("eq-items", id); !ok || n.Owner != alice.addr {
			t.Fatalf("item %s not minted", id)
		} else if _, ok := quest.ItemFromTraits(id, n.Traits.(map[string]any)); !ok {
			t.Fatalf("item %s has bad traits", id)
		}
		_ = it
	}
	if len(w.m.Unsent()) != 0 {
		t.Fatal("outputs still unsent after delivery")
	}

	// Sending an output again is rejected: nobody can be paid twice.
	for _, o := range w.m.Outputs {
		if err := w.c.Submit(o.Tx); err == nil {
			t.Fatalf("%s output accepted twice", o.Kind)
		}
	}

	// Wrong fee: refunded in full.
	bad := w.send(alice, map[string]any{"action": "run", "hero": hid, "dungeon": 2}, quest.Coin)
	// Hand the hero to bob; alice can no longer run it, bob can.
	tr, _ := core.NewTx(core.TxNFTTransfer, alice.addr, map[string]any{"collection_id": "eq-heroes", "token_id": hid, "recipient": bob.addr}, 10)
	_ = tr.SignWith(alice.priv, alice.pub)
	if err := w.c.Submit(tr); err != nil {
		t.Fatal(err)
	}
	w.mine(bob.addr)
	stolen := w.send(alice, map[string]any{"action": "run", "hero": hid, "dungeon": 1}, quest.Coin)
	ok2 := w.send(bob, map[string]any{"action": "run", "hero": hid, "dungeon": 1}, quest.Coin)
	w.mine(bob.addr)
	w.mine(bob.addr)
	w.sync()
	if r := w.m.Rejects[bad]; r == nil || !strings.Contains(r.Reason, "costs exactly") {
		t.Fatalf("wrong fee not refunded: %+v", r)
	}
	if r := w.m.Rejects[stolen]; r == nil || r.Reason != "you do not own this hero" {
		t.Fatalf("old owner was allowed to run: %+v", r)
	}
	if w.m.Runs[ok2] == nil {
		t.Fatal("new owner could not run the hero")
	}
	aliceBefore := w.c.Balance(alice.addr, "ENTRO")
	w.deliver()
	if got := w.c.Balance(alice.addr, "ENTRO"); got != aliceBefore+quest.Coin+quest.Coin {
		t.Fatalf("refunds not paid: %d -> %d", aliceBefore, got)
	}

	t.Logf("outputs %d (items %d), runs %d, rejects %d, paid to alice %d", len(w.m.Outputs), len(w.m.Items), len(w.m.Runs), len(w.m.Rejects), paid)

	// A fresh master rebuilt from the chain owes exactly what was sent.
	w2 := &world{t: t, c: w.c, gm: w.gm, m: NewMaster(w.gm.addr, "eq-heroes", "eq-items", "https://example.test/quest/", 1), next: 1}
	w2.sync()
	if len(w2.m.Outputs) != len(w.m.Outputs) {
		t.Fatalf("rebuild has %d outputs, original %d", len(w2.m.Outputs), len(w.m.Outputs))
	}
	for i, o := range w2.m.Outputs {
		if o.Hash != w.m.Outputs[i].Hash || o.Height == 0 {
			t.Fatalf("rebuilt output %d (%s) differs or is not marked delivered", i, o.Kind)
		}
	}
	if w2.m.Heroes[hid].Progress != w.m.Heroes[hid].Progress || w2.m.Heroes[hid].Owner != bob.addr {
		t.Fatal("rebuilt hero differs")
	}
}
