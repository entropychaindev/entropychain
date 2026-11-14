// Package questmaster is the Entropy Quest game master: it replays the chain,
// applies the rules in package quest, and lists the transactions it owes.
package questmaster

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/quest"
)

// The game master's state is rebuilt by replaying the chain from the game's
// start height. Everything it owes players (hero and item NFTs, ENTRO payouts
// and refunds) is a deterministic transaction: same inputs, same hash. Sending
// one twice is harmless, because the chain rejects the duplicate.

// OutputFee is the fee the game master pays on each transaction it sends.
const OutputFee = 10

// Block is what the master needs from one block.
type Block struct {
	Height    int64
	Hash      string
	Timestamp float64
	Txs       []*core.Transaction
}

// Output is a transaction the game master owes.
type Output struct {
	Kind   string            `json:"kind"` // hero, item, payout, refund
	For    string            `json:"for"`  // the player transaction it answers
	Tx     *core.Transaction `json:"-"`
	Hash   string            `json:"hash"`
	Height int64             `json:"height"` // block that included it; 0 = not yet
}

type HeroState struct {
	Hero     quest.Hero     `json:"hero"`
	Owner    string         `json:"owner"`
	Progress quest.Progress `json:"progress"`
	Born     int64          `json:"born"` // height of the summon transaction
	Runs     []string       `json:"-"`    // run tx hashes, oldest first
}

type ItemState struct {
	Item  quest.Item `json:"item"`
	Owner string     `json:"owner"`
	Run   string     `json:"run"`
}

// RunRecord is everything needed to recompute and display one expedition.
type RunRecord struct {
	Tx           string         `json:"tx"`
	Height       int64          `json:"height"`
	BeaconHeight int64          `json:"beacon_height"`
	BeaconHash   string         `json:"beacon_block_hash"`
	Beacon       string         `json:"beacon"`
	Player       string         `json:"player"`
	Hero         quest.Hero     `json:"hero"`
	Before       quest.Progress `json:"progress_before"`
	Gear         []quest.Item   `json:"gear"`
	Dungeon      int            `json:"dungeon"`
	Result       quest.Result   `json:"result"`
	Outputs      []*Output      `json:"outputs"`
}

// Rejected is a player transaction that was refunded.
type Rejected struct {
	Tx     string  `json:"tx"`
	Height int64   `json:"height"`
	Player string  `json:"player"`
	Reason string  `json:"reason"`
	Refund *Output `json:"refund"`
}

// Master is the game state.
type Master struct {
	Addr    string // game wallet: players pay here, outputs come from here
	HeroCol string
	ItemCol string
	Start   int64 // first height the game reads
	SiteURL string

	Heroes  map[string]*HeroState
	Items   map[string]*ItemState
	Runs    map[string]*RunRecord
	Summons map[string]string // summon tx -> hero id
	Rejects map[string]*Rejected
	Order   []string // run tx hashes in chain order
	Outputs []*Output
	byHash  map[string]*Output
	seen    map[string]int64 // tx hash -> height, for every master-sent tx
	Height  int64            // last block applied
}

func NewMaster(addr, heroCol, itemCol, siteURL string, start int64) *Master {
	return &Master{Addr: addr, HeroCol: heroCol, ItemCol: itemCol, SiteURL: siteURL, Start: start,
		Heroes: map[string]*HeroState{}, Items: map[string]*ItemState{}, Runs: map[string]*RunRecord{},
		Summons: map[string]string{}, Rejects: map[string]*Rejected{},
		byHash: map[string]*Output{}, seen: map[string]int64{}, Height: start - 1}
}

// Apply processes block b. next must be the block after it: its beacon
// decides every game transaction in b.
func (m *Master) Apply(b, next Block) {
	beacon := beaconOf(next.Hash)
	for _, tx := range b.Txs {
		if tx.Sender == m.Addr {
			m.seen[tx.Hash()] = b.Height
			if o := m.byHash[tx.Hash()]; o != nil {
				o.Height = b.Height
			}
		}
		switch tx.TxType {
		case core.TxNFTTransfer:
			m.applyNFTTransfer(tx)
		case core.TxTransfer:
			if rec, _ := tx.Data["recipient"].(string); rec == m.Addr && tx.Data["game"] == quest.GameTag {
				m.applyPlayerTx(b, tx, next, beacon)
			}
		}
	}
	m.Height = b.Height
}

func (m *Master) applyNFTTransfer(tx *core.Transaction) {
	col, _ := tx.Data["collection_id"].(string)
	tid, _ := tx.Data["token_id"].(string)
	to, _ := tx.Data["recipient"].(string)
	switch col {
	case m.HeroCol:
		if h := m.Heroes[tid]; h != nil && h.Owner == tx.Sender {
			h.Owner = to
		}
	case m.ItemCol:
		if it := m.Items[tid]; it != nil && it.Owner == tx.Sender {
			it.Owner = to
		}
	}
}

func (m *Master) applyPlayerTx(b Block, tx *core.Transaction, next Block, beacon string) {
	d := tx.Data
	amount := int64(num(d["amount"]))
	if tok, ok := d["token"].(string); ok && tok != "" && tok != params.NativeToken {
		return // only ENTRO plays; other tokens are treated as gifts
	}
	reject := func(reason string) {
		r := &Rejected{Tx: tx.Hash(), Height: b.Height, Player: tx.Sender, Reason: reason}
		r.Refund = m.owe("refund", tx.Hash(), 0, b.Timestamp, core.TxTransfer, map[string]any{
			"recipient": tx.Sender, "amount": amount, "token": params.NativeToken,
			"game": quest.GameTag, "kind": "refund", "for": tx.Hash(), "reason": reason,
		})
		m.Rejects[tx.Hash()] = r
	}
	switch d["action"] {
	case "summon":
		if amount != quest.SummonFee {
			reject(fmt.Sprintf("summoning costs exactly %s ENTRO", coins(quest.SummonFee)))
			return
		}
		h := quest.Summon(beacon, tx.Hash())
		m.Heroes[h.ID] = &HeroState{Hero: h, Owner: tx.Sender, Born: b.Height}
		m.Summons[tx.Hash()] = h.ID
		m.owe("hero", tx.Hash(), 0, b.Timestamp, core.TxNFTMint, map[string]any{
			"collection_id": m.HeroCol, "token_id": h.ID, "recipient": tx.Sender,
			"metadata_uri": m.SiteURL + "#hero=" + h.ID, "traits": h.Traits(),
		})

	case "run":
		hid, _ := d["hero"].(string)
		hs := m.Heroes[hid]
		if hs == nil {
			reject("unknown hero")
			return
		}
		if hs.Owner != tx.Sender {
			reject("you do not own this hero")
			return
		}
		dg, ok := quest.DungeonByID(num(d["dungeon"]))
		if !ok {
			reject("unknown dungeon")
			return
		}
		if amount != dg.Fee {
			reject(fmt.Sprintf("%s costs exactly %s ENTRO", dg.Name, coins(dg.Fee)))
			return
		}
		gear := m.gearFor(tx.Sender, d["gear"])
		rec := &RunRecord{Tx: tx.Hash(), Height: b.Height, BeaconHeight: next.Height, BeaconHash: next.Hash,
			Beacon: beacon, Player: tx.Sender, Hero: hs.Hero, Before: hs.Progress, Gear: gear, Dungeon: dg.ID}
		rec.Result = quest.Run(hs.Hero, hs.Progress, gear, dg, beacon, tx.Hash())
		res := rec.Result
		hs.Progress.XP += res.XP
		hs.Progress.Runs++
		hs.Progress.Gold += res.Payout
		if res.Cleared {
			hs.Progress.Clears++
		}
		if !res.Survived {
			hs.Progress.Deaths++
		}
		hs.Runs = append(hs.Runs, tx.Hash())
		if res.Payout > 0 {
			rec.Outputs = append(rec.Outputs, m.owe("payout", tx.Hash(), 0, b.Timestamp, core.TxTransfer, map[string]any{
				"recipient": tx.Sender, "amount": res.Payout, "token": params.NativeToken,
				"game": quest.GameTag, "kind": "payout", "for": tx.Hash(),
			}))
		}
		for n, it := range res.Items {
			m.Items[it.ID] = &ItemState{Item: it, Owner: tx.Sender, Run: tx.Hash()}
			rec.Outputs = append(rec.Outputs, m.owe("item", tx.Hash(), n+1, b.Timestamp, core.TxNFTMint, map[string]any{
				"collection_id": m.ItemCol, "token_id": it.ID, "recipient": tx.Sender,
				"metadata_uri": m.SiteURL + "#item=" + it.ID, "traits": it.Traits(),
			}))
		}
		m.Runs[tx.Hash()] = rec
		m.Order = append(m.Order, tx.Hash())

	default:
		reject("unknown action")
	}
}

// gearFor keeps the items the player owns right now, first one per slot.
func (m *Master) gearFor(owner string, raw any) []quest.Item {
	ids, _ := raw.([]any)
	used := map[string]bool{}
	gear := []quest.Item{}
	for _, v := range ids {
		id, _ := v.(string)
		it := m.Items[id]
		if it == nil || it.Owner != owner || used[it.Item.Slot] || len(gear) == quest.MaxGear {
			continue
		}
		used[it.Item.Slot] = true
		gear = append(gear, it.Item)
	}
	return gear
}

// owe records a deterministic output transaction. Its timestamp and nonce come
// from the player transaction it answers, so rebuilding the state from the
// chain recreates exactly the same hash.
func (m *Master) owe(kind, forTx string, n int, ts float64, txType string, data map[string]any) *Output {
	seed := sha256.Sum256([]byte(forTx + "|" + kind + "|" + fmt.Sprint(n)))
	tx, err := core.NewTxAt(txType, m.Addr, data, OutputFee, ts, hex.EncodeToString(seed[:8]))
	if err != nil {
		panic(err) // only ints, strings, bools and maps of them: cannot fail
	}
	o := &Output{Kind: kind, For: forTx, Tx: tx, Hash: tx.Hash(), Height: m.seen[tx.Hash()]}
	m.Outputs = append(m.Outputs, o)
	m.byHash[o.Hash] = o
	return o
}

// Peek notes the game master's own transactions in a block that is not yet
// applied, so a delivered payout shows as delivered right away. Apply marks
// them again later; a reorg rebuilds the state from scratch.
func (m *Master) Peek(b Block) {
	for _, tx := range b.Txs {
		if tx.Sender != m.Addr {
			continue
		}
		m.seen[tx.Hash()] = b.Height
		if o := m.byHash[tx.Hash()]; o != nil {
			o.Height = b.Height
		}
	}
}

// Unsent lists outputs not yet seen in a block, oldest first.
func (m *Master) Unsent() []*Output {
	var out []*Output
	for _, o := range m.Outputs {
		if o.Height == 0 {
			out = append(out, o)
		}
	}
	return out
}

// HeroesOf lists a player's heroes, strongest first.
func (m *Master) HeroesOf(addr string) []*HeroState {
	var out []*HeroState
	for _, h := range m.Heroes {
		if h.Owner == addr {
			out = append(out, h)
		}
	}
	sortHeroes(out)
	return out
}

// ItemsOf lists a player's items, best first.
func (m *Master) ItemsOf(addr string) []*ItemState {
	var out []*ItemState
	for _, it := range m.Items {
		if it.Owner == addr {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Item, out[j].Item
		if a.Power != b.Power {
			return a.Power > b.Power
		}
		return a.ID < b.ID
	})
	return out
}

// Leaderboard lists the top heroes by XP.
func (m *Master) Leaderboard(n int) []*HeroState {
	var out []*HeroState
	for _, h := range m.Heroes {
		out = append(out, h)
	}
	sortHeroes(out)
	return out[:min(n, len(out))]
}

func sortHeroes(hs []*HeroState) {
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].Progress.XP != hs[j].Progress.XP {
			return hs[i].Progress.XP > hs[j].Progress.XP
		}
		return hs[i].Hero.ID < hs[j].Hero.ID
	})
}

// beaconOf is SHA256d("entropy" ‖ block hash), as in consensus.Beacon.
func beaconOf(blockHash string) string {
	h, _ := hex.DecodeString(blockHash)
	a := sha256.Sum256(append([]byte("entropy"), h...))
	b := sha256.Sum256(a[:])
	return hex.EncodeToString(b[:])
}

func num(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case interface{ Int64() (int64, error) }:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func coins(units int64) string {
	s := fmt.Sprintf("%d.%08d", units/quest.Coin, units%quest.Coin)
	for s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	return trimDot(s)
}

func trimDot(s string) string {
	if s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

// FromCore converts a chain block.
func FromCore(b *core.Block) Block {
	return Block{Height: b.Height, Hash: b.Hash(), Timestamp: b.Timestamp, Txs: b.Transactions}
}
