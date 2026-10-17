// Package stats measures the chain and the node: per-block summaries
// derived from the chain itself (kept incrementally, reorg-safe), and
// runtime health sampled while the node runs (peers, mempool, RPC latency,
// reorgs, rejected blocks). The explorer's /stats page renders it.
package stats

import (
	"encoding/hex"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/consensus"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/params"
)

// TxTypes lists non-coinbase transaction types in display order.
var TxTypes = []string{
	core.TxTransfer, core.TxTokenCreate, core.TxTokenMint, core.TxNFTCreate,
	core.TxNFTMint, core.TxNFTRandomMint, core.TxNFTTransfer,
	core.TxLotteryCreate, core.TxLotteryEnter, core.TxLotteryDraw,
}

func txIndex(t string) int {
	for i, x := range TxTypes {
		if x == t {
			return i
		}
	}
	return -1
}

// Block is a compact summary of one main-chain block.
type Block struct {
	Height     int64
	Hash       string
	Time       float64
	Difficulty uint64
	Miner      string
	Txs        [10]uint16 // counts per TxTypes entry
	Fees       int64
	Beacon     [32]byte
}

// PeerSource reports live P2P figures (implemented by *p2p.Node).
type PeerSource interface {
	PeerCount() int
	BlockCounters() (accepted, rejected, orphans uint64)
}

// Sample is one minute of node health.
type Sample struct {
	Time     time.Time
	Height   int64
	Peers    int
	Mempool  int
	Requests int
	P50, P95 float64 // RPC latency, milliseconds
}

// Reorg is one chain reorganization seen by this node.
type Reorg struct {
	Time   time.Time
	Depth  int
	OldTip string
	NewTip string
}

const (
	maxSamples = 24 * 60 // one day of minute samples
	maxReorgs  = 100
)

// Collector gathers everything the stats page shows.
type Collector struct {
	chain   *chain.Chain
	peers   PeerSource
	started time.Time

	mu     sync.Mutex
	blocks []Block // index = height

	hmu     sync.Mutex
	samples []Sample
	reorgs  []Reorg
	lat     []float64 // RPC durations (ms) since the last sample
}

// New creates a collector; peers may be nil.
func New(c *chain.Chain, peers PeerSource) *Collector {
	col := &Collector{chain: c, peers: peers, started: time.Now()}
	c.Subscribe(func(ev chain.Event) {
		if ev.ReorgDepth > 0 {
			col.hmu.Lock()
			col.reorgs = append(col.reorgs, Reorg{time.Now(), ev.ReorgDepth, ev.OldTip, ev.NewTip.Hash()})
			if len(col.reorgs) > maxReorgs {
				col.reorgs = col.reorgs[len(col.reorgs)-maxReorgs:]
			}
			col.hmu.Unlock()
		}
	})
	return col
}

// Started returns when collection began (node start).
func (col *Collector) Started() time.Time { return col.started }

// ObserveRPC records one RPC request's duration.
func (col *Collector) ObserveRPC(d time.Duration) {
	col.hmu.Lock()
	if len(col.lat) < 100_000 {
		col.lat = append(col.lat, float64(d.Microseconds())/1000)
	}
	col.hmu.Unlock()
}

// Run samples node health every minute until stop is closed.
func (col *Collector) Run(stop <-chan struct{}) {
	go col.Refresh() // warm the block cache in the background
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	col.sample()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			col.sample()
		}
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

func (col *Collector) sample() {
	s := Sample{Time: time.Now(), Height: col.chain.Height(), Mempool: len(col.chain.MempoolTxs())}
	if col.peers != nil {
		s.Peers = col.peers.PeerCount()
	}
	col.hmu.Lock()
	lat := col.lat
	col.lat = nil
	sort.Float64s(lat)
	s.Requests, s.P50, s.P95 = len(lat), percentile(lat, 0.5), percentile(lat, 0.95)
	col.samples = append(col.samples, s)
	if len(col.samples) > maxSamples {
		col.samples = col.samples[len(col.samples)-maxSamples:]
	}
	col.hmu.Unlock()
}

// Refresh brings the block cache up to the current tip, rolling back any
// cached blocks a reorg replaced.
func (col *Collector) Refresh() {
	col.mu.Lock()
	defer col.mu.Unlock()
	tip := col.chain.Height()
	n := int64(len(col.blocks))
	if n > tip+1 {
		n = tip + 1
	}
	for n > 0 && col.blocks[n-1].Hash != col.chain.HashAtHeight(n-1) {
		n--
	}
	col.blocks = col.blocks[:n]
	for h := n; h <= tip; h++ {
		b := col.chain.BlockByHeight(h)
		if b == nil {
			break
		}
		col.blocks = append(col.blocks, summarize(b))
	}
}

func summarize(b *core.Block) Block {
	hash := b.Hash()
	s := Block{Height: b.Height, Hash: hash, Time: b.Timestamp, Difficulty: b.Difficulty}
	if len(b.Transactions) > 0 {
		if r, ok := b.Transactions[0].Data["recipient"].(string); ok {
			s.Miner = r
		}
	}
	for _, tx := range b.Transactions[1:] {
		if i := txIndex(tx.TxType); i >= 0 {
			s.Txs[i]++
		}
		s.Fees += tx.Fee
	}
	raw, _ := hex.DecodeString(consensus.Beacon(hash))
	copy(s.Beacon[:], raw)
	return s
}

// ---- Views ----

// Window aggregates a run of consecutive blocks.
type Window struct {
	From, To   int64
	Start, End float64 // timestamps of the block before From, and of To
	AvgBlock   float64 // seconds per block
	Difficulty uint64  // difficulty of the window's first block
	Hashrate   float64 // estimated network hashes per second
	Txs        [10]int
}

// Share is one miner's portion of blocks.
type Share struct {
	Miner  string
	Blocks int
	Pct    float64
}

// View is everything the stats page draws for a range of blocks.
type View struct {
	Range       string
	FromHeight  int64
	ToHeight    int64
	Blocks      int
	AvgBlock    float64
	MedianBlock float64
	Hashrate    float64 // latest window
	Difficulty  uint64
	Windows     []Window
	WindowSize  int64
	Miners      []Share
	MinerCount  int
	TxTotals    [10]int
	TxTotal     int
	Fees        int64
	Issued      int64
	MaxSupply   int64

	ByteCounts [256]int
	BeaconN    int
	ChiSquare  float64
	PValue     float64
	OnesPct    float64

	Samples        []Sample
	Reorgs         []Reorg
	Started        time.Time
	Accepted       uint64
	Rejected       uint64
	Orphans        uint64
	Peers          int
	LatestP95      float64
	LatestP50      float64
	SampleRequests int
	RecentMaxReorg int
}

// Snapshot computes a View over the last n blocks (n <= 0: all).
func (col *Collector) Snapshot(n int64, label string) View {
	col.Refresh()
	col.mu.Lock()
	all := col.blocks
	if len(all) == 0 {
		col.mu.Unlock()
		return View{Range: label}
	}
	from := int64(1)
	if n > 0 && int64(len(all))-n > from {
		from = int64(len(all)) - n
	}
	bs := append([]Block(nil), all[from:]...)
	prevTime := all[from-1].Time
	col.mu.Unlock()

	v := View{Range: label, FromHeight: from, ToHeight: bs[len(bs)-1].Height, Blocks: len(bs),
		Difficulty: bs[len(bs)-1].Difficulty, Issued: consensus.IssuedSupply(bs[len(bs)-1].Height),
		MaxSupply: consensus.IssuedSupply(params.HalvingInterval * 64)}

	// Block times. The genesis timestamp is fixed in advance rather than
	// mined, so the gap before block 1 says nothing about block production:
	// when the range starts at block 1, time is measured from block 1.
	anchor, skip := prevTime, 0
	if from == 1 && len(bs) > 1 {
		anchor, skip = bs[0].Time, 1
	}
	gaps := make([]float64, 0, len(bs))
	last := anchor
	for _, b := range bs[skip:] {
		gaps = append(gaps, b.Time-last)
		last = b.Time
	}
	sorted := append([]float64(nil), gaps...)
	sort.Float64s(sorted)
	v.MedianBlock = percentile(sorted, 0.5)
	if timed := len(bs) - skip; timed > 0 {
		v.AvgBlock = (bs[len(bs)-1].Time - anchor) / float64(timed)
	}

	// Windows: multiples of the 20-block retarget window, at most ~60 bars.
	w := int64(params.DifficultyWindow)
	for int64(len(bs))/w > 60 {
		w += params.DifficultyWindow
	}
	v.WindowSize = w
	for i := 0; i < len(bs); i += int(w) {
		j := min(i+int(w), len(bs))
		start, timedFrom := anchor, i
		if i > 0 {
			start = bs[i-1].Time
		} else {
			timedFrom = skip // block 1's own interval is not measured
		}
		win := Window{From: bs[i].Height, To: bs[j-1].Height, Start: start, End: bs[j-1].Time, Difficulty: bs[i].Difficulty}
		span := win.End - win.Start
		var work float64
		for k, b := range bs[i:j] {
			if i+k >= timedFrom {
				work += float64(b.Difficulty)
			}
			for t, c := range b.Txs {
				win.Txs[t] += int(c)
			}
		}
		if n := j - timedFrom; span > 0 && n > 0 {
			win.AvgBlock = span / float64(n)
			win.Hashrate = work / span
		}
		v.Windows = append(v.Windows, win)
	}
	if len(v.Windows) > 0 {
		// The newest window may be partial; prefer the last complete one.
		lw := v.Windows[len(v.Windows)-1]
		if len(v.Windows) > 1 && lw.To-lw.From+1 < w {
			lw = v.Windows[len(v.Windows)-2]
		}
		v.Hashrate = lw.Hashrate
	}

	// Miners, activity, beacon.
	counts := map[string]int{}
	for _, b := range bs {
		counts[b.Miner]++
		for k, c := range b.Txs {
			v.TxTotals[k] += int(c)
			v.TxTotal += int(c)
		}
		v.Fees += b.Fees
		ones := 0
		for _, x := range b.Beacon {
			v.ByteCounts[x]++
			for ; x != 0; x &= x - 1 {
				ones++
			}
		}
		v.OnesPct += float64(ones)
		v.BeaconN += 32
	}
	for m, c := range counts {
		v.Miners = append(v.Miners, Share{m, c, float64(c) / float64(len(bs)) * 100})
	}
	sort.Slice(v.Miners, func(i, j int) bool {
		if v.Miners[i].Blocks != v.Miners[j].Blocks {
			return v.Miners[i].Blocks > v.Miners[j].Blocks
		}
		return v.Miners[i].Miner < v.Miners[j].Miner
	})
	v.MinerCount = len(v.Miners)
	if v.BeaconN > 0 {
		v.OnesPct = v.OnesPct / float64(v.BeaconN*8) * 100
		exp := float64(v.BeaconN) / 256
		for _, c := range v.ByteCounts {
			v.ChiSquare += (float64(c) - exp) * (float64(c) - exp) / exp
		}
		v.PValue = chiSquareP(v.ChiSquare, 255)
	}

	// Node health.
	col.hmu.Lock()
	v.Samples = append([]Sample(nil), col.samples...)
	v.Reorgs = append([]Reorg(nil), col.reorgs...)
	col.hmu.Unlock()
	v.Started = col.started
	if col.peers != nil {
		v.Peers = col.peers.PeerCount()
		v.Accepted, v.Rejected, v.Orphans = col.peers.BlockCounters()
	}
	for i := len(v.Samples) - 1; i >= 0; i-- {
		if v.Samples[i].Requests > 0 {
			v.LatestP50, v.LatestP95, v.SampleRequests = v.Samples[i].P50, v.Samples[i].P95, v.Samples[i].Requests
			break
		}
	}
	for _, r := range v.Reorgs {
		v.RecentMaxReorg = max(v.RecentMaxReorg, r.Depth)
	}
	return v
}

// chiSquareP is the upper-tail probability of a chi-square statistic, using
// the Wilson–Hilferty normal approximation (accurate for large df).
func chiSquareP(x float64, df float64) float64 {
	z := (math.Cbrt(x/df) - (1 - 2/(9*df))) / math.Sqrt(2/(9*df))
	return 0.5 * math.Erfc(z/math.Sqrt2)
}
