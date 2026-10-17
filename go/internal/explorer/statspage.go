package explorer

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/stats"
)

type statsRange struct {
	Key, Label string
	N          int64
	Active     bool
}

type tile struct{ Label, Value, Note string }

type chartPanel struct {
	Chart template.HTML
	Note  string
}

type statsData struct {
	V          stats.View
	Ranges     []statsRange
	Tiles      []tile
	Chain      []chartPanel
	Activity   []chartPanel
	Randomness chartPanel
	Health     []chartPanel
	Uptime     string
}

func (s *Server) statsPage(w http.ResponseWriter, r *http.Request) {
	if s.stats == nil {
		s.render(w, http.StatusNotFound, "Stats", "error", "Stats are not enabled on this node.")
		return
	}
	ranges := []statsRange{{"200", "Last 200 blocks", 200, false}, {"1000", "Last 1,000", 1000, false},
		{"5000", "Last 5,000", 5000, false}, {"all", "All blocks", 0, false}}
	key := r.URL.Query().Get("range")
	sel := 3
	for i, rg := range ranges {
		if rg.Key == key {
			sel = i
		}
	}
	ranges[sel].Active = true
	v := s.stats.Snapshot(ranges[sel].N, ranges[sel].Label)
	d := statsData{V: v, Ranges: ranges, Uptime: friendlyDuration(time.Since(v.Started))}
	if v.Blocks == 0 {
		s.render(w, 200, "Stats", "stats", d)
		return
	}
	span := fmt.Sprintf("blocks %d–%d", v.FromHeight, v.ToHeight)

	// ---- Headline tiles ----
	top := v.Miners[0]
	d.Tiles = []tile{
		{"Block height", group(strconv.FormatInt(v.ToHeight, 10)), fmt.Sprintf("%s of %s ENTRO issued", human(float64(v.Issued)/1e8), human(float64(v.MaxSupply)/1e8))},
		{"Avg block time", fmt.Sprintf("%.1f s", v.AvgBlock), fmt.Sprintf("target %d s · median %.1f s", params.TargetBlockTime, v.MedianBlock)},
		{"Network hashrate", human(v.Hashrate) + " H/s", "estimated, latest full window"},
		{"Miners", strconv.Itoa(v.MinerCount), fmt.Sprintf("top miner %.0f%% of blocks", top.Pct)},
		{"Transactions", group(strconv.Itoa(v.TxTotal)), fmt.Sprintf("plus %s block rewards", group(strconv.Itoa(v.Blocks)))},
		{"Reorgs seen", strconv.Itoa(len(v.Reorgs)), reorgNote(v)},
	}

	// ---- Chain behaviour ----
	var bt []stats.Window = v.Windows
	bars := make([]barDatum, len(bt))
	for i, win := range bt {
		bars[i] = barDatum{Label: "#" + strconv.FormatInt(win.From, 10), Value: win.AvgBlock,
			Tip: fmt.Sprintf("Blocks %d–%d: %.1f s per block, difficulty %s", win.From, win.To, win.AvgBlock, group(strconv.FormatUint(win.Difficulty, 10)))}
	}
	d.Chain = append(d.Chain, chartPanel{
		barChart(wideW, fmt.Sprintf("Blocks averaged %.1f s against a %d s target", v.AvgBlock, params.TargetBlockTime),
			fmt.Sprintf("Average seconds per block in %d-block windows · %s", v.WindowSize, span),
			bars, func(x float64) string { return human(x) + " s" }, params.TargetBlockTime, fmt.Sprintf("%d s target", params.TargetBlockTime)),
		"Bars above the line mean blocks came slower than planned, usually because a miner went offline. Difficulty re-adjusts every 20 blocks to pull the average back to 30 s."})

	var hr, df []linePoint
	for _, win := range bt {
		hr = append(hr, linePoint{float64(win.To), win.Hashrate, fmt.Sprintf("Blocks %d–%d: about %s H/s", win.From, win.To, human(win.Hashrate))})
		df = append(df, linePoint{float64(win.To), float64(win.Difficulty), fmt.Sprintf("Blocks %d–%d: difficulty %s", win.From, win.To, group(strconv.FormatUint(win.Difficulty, 10)))})
	}
	heightFmt := func(x float64) string { return "#" + group(strconv.FormatInt(int64(x), 10)) }
	d.Chain = append(d.Chain, chartPanel{
		lineChart(fmt.Sprintf("The network computes about %s hashes per second", human(v.Hashrate)),
			"Estimated from difficulty and block times · each point is one window", [][]linePoint{hr},
			[]string{colAccent}, nil, heightFmt, func(x float64) string { return human(x) + " H/s" }, false),
		"Total mining power across all miners. It rises when miners join or add threads and falls when they leave."})
	d.Chain = append(d.Chain, chartPanel{
		lineChart(fmt.Sprintf("Difficulty is %s", group(strconv.FormatUint(v.Difficulty, 10))),
			"Difficulty at the start of each window", [][]linePoint{df},
			[]string{colAccent2}, nil, heightFmt, human, false),
		"How hard each block is to find. It follows hashrate so that blocks keep arriving about every 30 s."})

	// ---- Who mines ----
	var mh []hbarDatum
	for i, m := range v.Miners {
		if i == 10 {
			break
		}
		name := shortAddr(m.Miner)
		if m.Miner == "" {
			name = "(none)"
		}
		mh = append(mh, hbarDatum{Label: name, Value: fmt.Sprintf("%s blocks · %.1f%%", group(strconv.Itoa(m.Blocks)), m.Pct),
			Tip: m.Miner, Frac: m.Pct / 100, Accent: i == 0})
	}
	minerTitle := fmt.Sprintf("%d miners found blocks; the largest found %.0f%%", v.MinerCount, top.Pct)
	if v.MinerCount == 1 {
		minerTitle = "One miner found every block in this range"
	}
	minerNote := "Healthy decentralization means no single miner above 50%. A miner with a majority could rewrite recent blocks."
	if top.Pct > 50 {
		minerNote = fmt.Sprintf("One miner has %.0f%% of blocks, a majority: it could rewrite recent history if it wanted to. Expected on a young testnet; it shrinks as more people mine.", top.Pct)
	}
	d.Activity = append(d.Activity, chartPanel{hbarChart(minerTitle, "Share of blocks by mining address · "+span, mh), minerNote})

	// ---- Transactions ----
	labels := make([]string, len(bt))
	tips := make([]string, len(bt))
	stacks := make([][]int, len(bt))
	for i, win := range bt {
		labels[i] = "#" + strconv.FormatInt(win.From, 10)
		tips[i] = fmt.Sprintf("Blocks %d–%d", win.From, win.To)
		stacks[i] = win.Txs[:]
	}
	d.Activity = append(d.Activity, chartPanel{
		stackedBars(fmt.Sprintf("%s transactions besides block rewards", group(strconv.Itoa(v.TxTotal))),
			fmt.Sprintf("Per %d-block window, by type · %s", v.WindowSize, span), labels, stacks, stats.TxTypes, tips),
		"What people actually do on the chain. Every block also has a reward transaction, which is not counted here."})

	// ---- Randomness ----
	verdict := fmt.Sprintf("Beacon bytes look evenly spread (p = %.2f)", v.PValue)
	note := "Each beacon byte should take every value from 0 to 255 equally often. A p-value above 0.01 means the spread is consistent with true randomness; a very small one would be a red flag worth investigating."
	if v.PValue < 0.01 {
		verdict = fmt.Sprintf("Beacon bytes are less even than expected (p = %.4f)", v.PValue)
		note = "A p-value below 0.01 happens by chance about 1 time in 100, so check again over a different range. If it persists, something may be biasing the beacon."
	}
	d.Randomness = chartPanel{
		histogram256(verdict, fmt.Sprintf("How often each byte value appears across %s beacon bytes · chi-square %.0f on 255 degrees of freedom · %.2f%% of bits are 1",
			group(strconv.Itoa(v.BeaconN)), v.ChiSquare, v.OnesPct), v.ByteCounts, v.BeaconN),
		note}

	// ---- Node health ----
	var peers, mem, p50, p95 []linePoint
	for _, sm := range v.Samples {
		x := float64(sm.Time.Unix())
		t := sm.Time.UTC().Format("15:04 UTC")
		peers = append(peers, linePoint{x, float64(sm.Peers), fmt.Sprintf("%s: %d peers", t, sm.Peers)})
		mem = append(mem, linePoint{x, float64(sm.Mempool), fmt.Sprintf("%s: %d pending", t, sm.Mempool)})
		if sm.Requests > 0 {
			p50 = append(p50, linePoint{x, sm.P50, fmt.Sprintf("%s: median %.0f ms over %d requests", t, sm.P50, sm.Requests)})
			p95 = append(p95, linePoint{x, sm.P95, fmt.Sprintf("%s: 95%% under %.0f ms", t, sm.P95)})
		}
	}
	clock := func(x float64) string { return time.Unix(int64(x), 0).UTC().Format("15:04") }
	latTitle := "RPC response times"
	if v.SampleRequests > 0 {
		latTitle = fmt.Sprintf("RPC answers in %.0f ms (95%% of requests, last minute)", v.LatestP95)
	}
	d.Health = append(d.Health, chartPanel{
		lineChart(latTitle, "Response time of this node's API, per minute · median and 95th percentile",
			[][]linePoint{p50, p95}, []string{colAccent, colWarn}, []string{"median", "95th percentile"}, clock,
			func(x float64) string { return human(x) + " ms" }, false),
		"How quickly wallets and the explorer get answers. Under 300 ms is good; seconds means the node is overloaded."})
	d.Health = append(d.Health, chartPanel{
		lineChart(peersTitle(v.Peers), "Connected peers and pending transactions, per minute",
			[][]linePoint{peers, mem}, []string{colAccent, colAccent2}, []string{"peers", "pending txs"}, clock, human, true),
		"Peers are other nodes this one talks to. Pending transactions are waiting for the next block."})

	s.render(w, 200, "Stats", "stats", d)
}

func peersTitle(n int) string {
	switch n {
	case 0:
		return "No peers connected right now"
	case 1:
		return "1 peer connected now"
	}
	return fmt.Sprintf("%d peers connected now", n)
}

func friendlyDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

func reorgNote(v stats.View) string {
	if len(v.Reorgs) == 0 {
		return "since node start"
	}
	return fmt.Sprintf("deepest %d blocks, since node start", v.RecentMaxReorg)
}
