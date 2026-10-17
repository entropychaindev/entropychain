package explorer

// templates holds every page. html/template escapes all values by context
// (HTML text, attributes and URLs), so chain data cannot inject markup.
const templates = `
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — Entropy Chain</title>
<style>
:root {
  --bg: #0a0e14; --panel: #111821; --panel-2: #161f2c; --border: #1f2a3a;
  --text: #c8d3e0; --muted: #6b7a8e; --accent: #00d4aa; --accent-2: #ff6b9d; --warn: #ffb454;
  --mono: 'SF Mono', 'Cascadia Mono', Consolas, Menlo, monospace;
  color-scheme: dark;
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text); font: 14px/1.5 -apple-system, BlinkMacSystemFont, 'Segoe UI', Inter, sans-serif; }
header { background: var(--panel); border-bottom: 1px solid var(--border); padding: 14px 24px; display: flex; flex-wrap: wrap; align-items: center; gap: 12px 24px; }
header .logo { font-weight: 700; font-size: 18px; color: var(--accent); letter-spacing: .5px; text-decoration: none; }
header .logo span { color: var(--accent-2); }
header nav { display: flex; gap: 18px; }
header nav a { color: var(--muted); text-decoration: none; font-size: 13px; }
header nav a:hover { color: var(--text); }
header form { margin-left: auto; flex: 1 1 280px; max-width: 420px; }
header input { width: 100%; background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 7px 10px; border-radius: 4px; font: 12px var(--mono); }
.container { max-width: 1200px; margin: 0 auto; padding: 24px 16px; }
h1 { margin-top: 0; font-weight: 600; overflow-wrap: anywhere; }
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; margin-bottom: 24px; }
.stat { background: var(--panel); border: 1px solid var(--border); border-radius: 6px; padding: 14px 16px; }
.stat .label { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: .5px; }
.stat .value { font-size: 22px; font-weight: 600; margin-top: 4px; color: var(--accent); font-variant-numeric: tabular-nums; }
.panel { background: var(--panel); border: 1px solid var(--border); border-radius: 6px; margin-bottom: 24px; overflow-x: auto; }
.panel h2 { margin: 0; padding: 14px 18px; border-bottom: 1px solid var(--border); font-size: 13px; text-transform: uppercase; letter-spacing: .5px; color: var(--muted); font-weight: 600; }
.panel-body { padding: 14px 18px; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { text-align: left; padding: 10px 18px; border-bottom: 1px solid var(--border); white-space: nowrap; }
th { color: var(--muted); font-weight: 500; font-size: 11px; text-transform: uppercase; letter-spacing: .5px; }
tr:last-child td { border-bottom: none; }
tr:hover td { background: var(--panel-2); }
td.num { text-align: right; font-family: var(--mono); font-size: 12px; font-variant-numeric: tabular-nums; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
.mono { font-family: var(--mono); font-size: 12px; }
.hash { font-family: var(--mono); font-size: 12px; color: var(--accent); }
.addr { font-family: var(--mono); font-size: 12px; color: var(--accent-2); }
.dim { color: var(--muted); }
.kv { display: grid; grid-template-columns: 200px 1fr; gap: 8px 18px; padding: 6px 0; }
@media (max-width: 640px) { .kv { grid-template-columns: 1fr; gap: 2px; } }
.kv .k { color: var(--muted); }
.kv .v { font-family: var(--mono); font-size: 12px; word-break: break-all; }
.tag { display: inline-block; padding: 2px 8px; border-radius: 3px; font-size: 11px; background: var(--panel-2); border: 1px solid var(--border); color: var(--muted); text-transform: uppercase; letter-spacing: .5px; }
.tag.coinbase, .tag.net { color: var(--warn); border-color: var(--warn); }
.tag.lottery { color: var(--accent-2); border-color: var(--accent-2); }
.tag.nft { color: var(--accent); border-color: var(--accent); }
.entropy { font-family: var(--mono); font-size: 11px; color: var(--accent-2); word-break: break-all; padding: 2px 6px; background: var(--bg); border-radius: 4px; border: 1px solid var(--border); }
.error { color: var(--warn); padding: 24px 0; overflow-wrap: anywhere; }
.trait { display: inline-block; margin: 2px 4px 2px 0; padding: 3px 9px; background: var(--panel-2); border: 1px solid var(--border); border-radius: 3px; font-size: 11px; }
.trait.legendary { color: var(--warn); border-color: var(--warn); font-weight: 600; }
.empty { padding: 18px; }
</style>
</head>
<body>
<header>
  <a class="logo" href="/">⟁ ENTROPY<span>CHAIN</span></a>
  <span class="tag net">{{.NetworkTag}}</span>
  <nav><a href="/">Dashboard</a><a href="/stats">Stats</a><a href="/entropy">Entropy Beacon</a><a href="/rpc/info">RPC</a></nav>
  <form action="/search" method="get" role="search">
    <input name="q" aria-label="Search" placeholder="Search block / hash / address / token / lottery…" autocomplete="off">
  </form>
</header>
<main class="container">
{{if eq .Body "dashboard"}}{{template "dashboard" .Data}}
{{else if eq .Body "block"}}{{template "block" .Data}}
{{else if eq .Body "tx"}}{{template "tx" .Data}}
{{else if eq .Body "address"}}{{template "address" .Data}}
{{else if eq .Body "token"}}{{template "token" .Data}}
{{else if eq .Body "nft"}}{{template "nft" .Data}}
{{else if eq .Body "lottery"}}{{template "lottery" .Data}}
{{else if eq .Body "entropy"}}{{template "entropy" .Data}}
{{else if eq .Body "stats"}}{{template "stats" .Data}}
{{else}}<div class="error">{{.Data}}</div>{{end}}
</main>
</body></html>{{end}}

{{define "dashboard"}}
<div class="stats">
  <div class="stat"><div class="label">Height</div><div class="value">{{comma .Tip.Height}}</div></div>
  <div class="stat"><div class="label">Issued ENTRO</div><div class="value">{{entro .Supply}}</div></div>
  <div class="stat"><div class="label">Difficulty</div><div class="value">{{comma .Tip.Difficulty}}</div></div>
  <div class="stat"><div class="label">Tokens</div><div class="value">{{.TokenCount}}</div></div>
  <div class="stat"><div class="label">NFTs</div><div class="value">{{.NFTs}}</div></div>
  <div class="stat"><div class="label">Lotteries</div><div class="value">{{.Lotteries}}</div></div>
</div>
<div class="panel">
  <h2>Recent Blocks</h2>
  <table>
    <tr><th>Height</th><th>Hash</th><th>Txs</th><th>Time</th><th>Difficulty</th><th>Beacon</th></tr>
    {{range .Blocks}}<tr>
      <td><a href="/block/{{.Height}}">{{.Height}}</a></td>
      <td><a href="/block/{{.Hash}}" class="hash">{{short .Hash}}</a></td>
      <td>{{.TxCount}}</td>
      <td class="dim">{{ago .Timestamp}}</td>
      <td class="mono">{{comma .Difficulty}}</td>
      <td><span class="entropy">{{short16 .EntropySeed}}</span></td>
    </tr>{{end}}
  </table>
</div>
<div class="panel">
  <h2>Tokens</h2>
  <table>
    <tr><th>Symbol</th><th>Name</th><th>Supply</th><th>Creator</th></tr>
    {{range .Tokens}}<tr>
      <td><a href="/token/{{.Symbol}}"><strong>{{.Symbol}}</strong></a></td>
      <td class="dim">{{.Name}}</td>
      <td class="mono">{{amount .TotalSupply .Decimals}}</td>
      <td><a href="/address/{{.Creator}}" class="addr">{{shortAddr .Creator}}</a></td>
    </tr>{{else}}<tr><td colspan="4" class="dim empty">No tokens created yet.</td></tr>{{end}}
  </table>
</div>
{{end}}

{{define "block"}}
<h1>Block #{{.B.Height}}</h1>
<div class="panel"><div class="panel-body">
  <div class="kv"><span class="k">Hash</span><span class="v">{{.Hash}}</span></div>
  <div class="kv"><span class="k">Status</span><span class="v">{{if .Main}}On main chain{{else}}Side branch (orphaned){{end}}</span></div>
  <div class="kv"><span class="k">Previous</span><span class="v"><a href="/block/{{.B.PrevHash}}" class="hash">{{.B.PrevHash}}</a></span></div>
  <div class="kv"><span class="k">Merkle Root</span><span class="v">{{.B.MerkleRootHex}}</span></div>
  <div class="kv"><span class="k">Timestamp</span><span class="v">{{utc .B.Timestamp}} ({{ago .B.Timestamp}})</span></div>
  <div class="kv"><span class="k">Difficulty</span><span class="v">{{comma .B.Difficulty}}</span></div>
  <div class="kv"><span class="k">Cumulative Work</span><span class="v">{{if .Work}}{{comma .Work}}{{end}}</span></div>
  <div class="kv"><span class="k">Nonce</span><span class="v">{{.B.Nonce}}</span></div>
  <div class="kv"><span class="k">Miner Nonce</span><span class="v">{{.B.MinerNonce}}</span></div>
  <div class="kv"><span class="k">Beacon</span><span class="v"><span class="entropy">{{.Beacon}}</span></span></div>
  <div class="kv"><span class="k">Header Seed (legacy)</span><span class="v">{{.B.EntropySeed}}</span></div>
</div></div>
<div class="panel">
  <h2>Transactions ({{len .B.Transactions}})</h2>
  <table>
    <tr><th>Hash</th><th>Type</th><th>Sender</th><th>Fee</th></tr>
    {{range .B.Transactions}}<tr>
      <td><a href="/tx/{{.Hash}}" class="hash">{{short16 .Hash}}</a></td>
      <td><span class="tag {{tagClass .TxType}}">{{upper .TxType}}</span></td>
      <td>{{if .Sender}}<a href="/address/{{.Sender}}" class="addr">{{shortAddr .Sender}}</a>{{else}}<span class="dim">—</span>{{end}}</td>
      <td class="mono">{{.Fee}}</td>
    </tr>{{end}}
  </table>
</div>
{{end}}

{{define "tx"}}
<h1>Transaction</h1>
<div class="panel"><div class="panel-body">
  <div class="kv"><span class="k">Hash</span><span class="v">{{.Hash}}</span></div>
  <div class="kv"><span class="k">Type</span><span class="v">{{.Tx.TxType}}</span></div>
  <div class="kv"><span class="k">Block</span><span class="v">{{if .Pending}}<span class="dim">pending (in mempool)</span>{{else}}<a href="/block/{{.Height}}">#{{.Height}}</a>{{end}}</span></div>
  <div class="kv"><span class="k">Sender</span><span class="v">{{if .Tx.Sender}}<a href="/address/{{.Tx.Sender}}" class="addr">{{.Tx.Sender}}</a>{{else}}<span class="dim">—</span>{{end}}</span></div>
  <div class="kv"><span class="k">Fee</span><span class="v">{{.Tx.Fee}}</span></div>
  <div class="kv"><span class="k">Timestamp</span><span class="v">{{utc .Time}}</span></div>
</div></div>
<div class="panel">
  <h2>Payload</h2>
  <div class="panel-body">{{range .Payload}}
    <div class="kv"><span class="k">{{.Key}}</span><span class="v">{{if eq .Kind "addr"}}<a href="/address/{{.Value}}" class="addr">{{.Value}}</a>{{else if eq .Kind "hash"}}<span class="hash">{{.Value}}</span>{{else}}{{.Value}}{{end}}</span></div>
  {{else}}<span class="dim">Empty.</span>{{end}}</div>
</div>
{{end}}

{{define "address"}}
<h1>Address</h1>
<div class="panel"><div class="panel-body">
  <div class="kv"><span class="k">Address</span><span class="v">{{.Addr}}{{if not .Valid}} <span class="tag coinbase">invalid checksum</span>{{end}}</span></div>
  <div class="kv"><span class="k">Created Tokens</span><span class="v">{{or .Created "none"}}</span></div>
  <div class="kv"><span class="k">Created NFT Collections</span><span class="v">{{or .Collections "none"}}</span></div>
</div></div>
<div class="panel">
  <h2>Balances</h2>
  <table><tr><th>Token</th><th>Amount</th></tr>
  {{range .Balances}}<tr><td><a href="/token/{{.Token}}"><strong>{{.Token}}</strong></a></td><td class="mono">{{amount .Amount .Decimals}}</td></tr>
  {{else}}<tr><td colspan="2" class="dim">No balance.</td></tr>{{end}}</table>
</div>
<div class="panel">
  <h2>Recent Activity</h2>
  <table><tr><th>Tx</th><th>Type</th><th>Block</th><th>From</th></tr>
  {{range .History}}<tr>
    <td><a href="/tx/{{.Tx.Hash}}" class="hash">{{short16 .Tx.Hash}}</a></td>
    <td><span class="tag {{tagClass .Tx.TxType}}">{{upper .Tx.TxType}}</span></td>
    <td><a href="/block/{{.Height}}">#{{.Height}}</a></td>
    <td>{{if .Tx.Sender}}<a href="/address/{{.Tx.Sender}}" class="addr">{{shortAddr .Tx.Sender}}</a>{{else}}<span class="dim">block reward</span>{{end}}</td>
  </tr>{{else}}<tr><td colspan="4" class="dim">No activity.</td></tr>{{end}}</table>
</div>
<div class="panel">
  <h2>NFTs Owned ({{.NFTCount}})</h2>
  <table><tr><th>NFT</th><th>Minted</th></tr>
  {{range .NFTs}}<tr><td><a href="/nft/{{.Collection}}/{{.TokenID}}">{{.Collection}} #{{.TokenID}}</a></td><td><a href="/block/{{.MintedAtHeight}}">#{{.MintedAtHeight}}</a></td></tr>
  {{else}}<tr><td colspan="2" class="dim">No NFTs owned.</td></tr>{{end}}</table>
</div>
{{end}}

{{define "token"}}
<h1>{{.Symbol}}</h1>
<div class="panel"><div class="panel-body">
  <div class="kv"><span class="k">Name</span><span class="v">{{.Info.Name}}</span></div>
  <div class="kv"><span class="k">Decimals</span><span class="v">{{.Info.Decimals}}</span></div>
  <div class="kv"><span class="k">Total Supply</span><span class="v">{{amount .Supply .Info.Decimals}}</span></div>
  <div class="kv"><span class="k">Creator</span><span class="v">{{if .Info.Creator}}<a href="/address/{{.Info.Creator}}" class="addr">{{.Info.Creator}}</a>{{else}}— (block rewards){{end}}</span></div>
  <div class="kv"><span class="k">Holders</span><span class="v">{{.HolderCount}}</span></div>
</div></div>
<div class="panel">
  <h2>Top Holders</h2>
  <table><tr><th>Address</th><th>Balance</th><th>%</th></tr>
  {{$d := .Info.Decimals}}{{range .Holders}}<tr>
    <td><a href="/address/{{.Address}}" class="addr">{{shortAddr .Address}}</a></td>
    <td class="mono">{{amount .Balance $d}}</td><td class="dim">{{.Pct}}</td>
  </tr>{{else}}<tr><td colspan="3" class="dim">No holders.</td></tr>{{end}}</table>
</div>
{{end}}

{{define "nft"}}
<h1>{{.Col}} #{{.TID}}</h1>
<div class="panel"><div class="panel-body">
  <div class="kv"><span class="k">Collection</span><span class="v">{{.Col}}</span></div>
  <div class="kv"><span class="k">Token ID</span><span class="v">{{.TID}}</span></div>
  <div class="kv"><span class="k">Owner</span><span class="v"><a href="/address/{{.N.Owner}}" class="addr">{{.N.Owner}}</a></span></div>
  <div class="kv"><span class="k">Creator</span><span class="v"><a href="/address/{{.N.Creator}}" class="addr">{{.N.Creator}}</a></span></div>
  <div class="kv"><span class="k">Metadata URI</span><span class="v">{{or .N.MetadataURI "none"}}</span></div>
  <div class="kv"><span class="k">Minted at Height</span><span class="v"><a href="/block/{{.N.MintedAtHeight}}">#{{.N.MintedAtHeight}}</a></span></div>
  {{if .N.EntropySourceHeight}}
  <div class="kv"><span class="k">Traits Revealed By</span><span class="v"><a href="/block/{{.N.EntropySourceHeight}}">#{{.N.EntropySourceHeight}}</a>{{if .N.TraitsPending}} <span class="dim">(not mined yet)</span>{{end}}</span></div>
  {{if .Seed}}<div class="kv"><span class="k">Source Beacon</span><span class="v"><span class="entropy">{{.Seed}}</span></span></div>{{end}}
  {{end}}
</div></div>
<div class="panel">
  <h2>Traits</h2>
  <div class="panel-body">{{range .Traits}}<span class="trait{{if .Legendary}} legendary{{end}}"><strong>{{.Key}}:</strong> {{.Value}}</span>{{else}}<span class="dim">{{if .N.TraitsPending}}Traits are revealed when block #{{.N.EntropySourceHeight}} is mined.{{else}}No traits.{{end}}</span>{{end}}</div>
</div>
{{end}}

{{define "lottery"}}
<h1>Lottery: {{.ID}}</h1>
<div class="panel"><div class="panel-body">
  <div class="kv"><span class="k">Status</span><span class="v">{{.Status}}</span></div>
  <div class="kv"><span class="k">Organizer</span><span class="v"><a href="/address/{{.L.Organizer}}" class="addr">{{shortAddr .L.Organizer}}</a></span></div>
  <div class="kv"><span class="k">Prize</span><span class="v">{{entro .L.PrizeAmount}} {{.L.Token}}</span></div>
  <div class="kv"><span class="k">Ticket Price</span><span class="v">{{entro .L.TicketPrice}} ENTRO</span></div>
  <div class="kv"><span class="k">Entry Pool</span><span class="v">{{entro .L.EntryPool}} ENTRO</span></div>
  <div class="kv"><span class="k">Draw Block{{if gt .L.DrawBlocks 1}}s{{end}}</span><span class="v">#{{.L.DrawHeight}}{{if gt .L.DrawBlocks 1}} + {{.L.DrawBlocks}} consecutive beacons{{end}} (current: #{{.Height}})</span></div>
  <div class="kv"><span class="k">Entrants</span><span class="v">{{len .L.Entrants}}</span></div>
  <div class="kv"><span class="k">Winner</span><span class="v">{{if .Winner}}<a href="/address/{{.Winner}}" class="addr">{{.Winner}}</a>{{else}}<span class="dim">not drawn</span>{{end}}</span></div>
  {{if .L.EntropyUsed}}
  <div class="kv"><span class="k">Beacon Used</span><span class="v"><span class="entropy">{{.L.EntropyUsed}}</span></span></div>
  {{end}}
</div></div>
<div class="panel">
  <h2>Entrants ({{len .L.Entrants}})</h2>
  <table>{{$w := .Winner}}{{range .L.Entrants}}<tr><td><a href="/address/{{.}}" class="addr">{{shortAddr .}}</a>{{if eq . $w}} 🏆{{end}}</td></tr>
  {{else}}<tr><td class="dim">No entrants yet.</td></tr>{{end}}</table>
</div>
{{end}}

{{define "entropy"}}
<h1>Entropy Beacon</h1>
<p class="dim">Every block's beacon is SHA256d("entropy" ‖ block hash). The block hash covers the proof of work, so no one knows a beacon before its block is solved, and a miner can bias it only by discarding a solved block. Lotteries and random mints consume these beacons.</p>
<div class="panel">
  <table><tr><th>Height</th><th>Beacon</th><th>Time</th></tr>
  {{range .}}<tr><td><a href="/block/{{.Height}}">#{{.Height}}</a></td><td><span class="entropy">{{.EntropySeed}}</span></td><td class="dim">{{ago .Timestamp}}</td></tr>{{end}}
  </table>
</div>
{{end}}
`

// statsTemplate renders the /stats page; charts arrive as trusted SVG built
// by charts.go, which escapes every string it embeds.
const statsTemplate = `
{{define "stats"}}
<style>
.range { display: flex; gap: 6px; flex-wrap: wrap; margin: -8px 0 20px; }
.range a { padding: 5px 12px; border: 1px solid var(--border); border-radius: 999px; font-size: 12px; color: var(--muted); }
.range a.on { border-color: var(--accent); color: var(--accent); }
.range a:hover { text-decoration: none; color: var(--text); }
.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; margin-bottom: 24px; }
.tiles .stat .note { color: var(--muted); font-size: 11.5px; margin-top: 4px; }
.charts { display: grid; grid-template-columns: repeat(auto-fit, minmax(460px, 1fr)); gap: 16px; margin-bottom: 24px; }
@media (max-width: 560px) { .charts { grid-template-columns: 1fr; } }
.chartbox { background: var(--panel); border: 1px solid var(--border); border-radius: 6px; padding: 14px 14px 10px; }
.chartbox svg.chart { width: 100%; height: auto; display: block; font-family: inherit; }
.chartbox.wide { grid-column: 1 / -1; }
.chartbox .why { color: var(--muted); font-size: 12px; margin: 6px 4px 2px; line-height: 1.5; }
h2.group { font-size: 13px; text-transform: uppercase; letter-spacing: .6px; color: var(--muted); margin: 28px 0 12px; }
.counters { display: flex; gap: 24px; flex-wrap: wrap; padding: 14px 18px; }
.counters b { color: var(--text); font-size: 18px; display: block; }
</style>
<h1>Network stats</h1>
<div class="range">{{range .Ranges}}<a href="/stats?range={{.Key}}"{{if .Active}} class="on"{{end}}>{{.Label}}</a>{{end}}</div>
{{if not .V.Blocks}}<div class="error">No blocks mined yet.</div>{{else}}
<div class="tiles">{{range .Tiles}}<div class="stat"><div class="label">{{.Label}}</div><div class="value">{{.Value}}</div><div class="note">{{.Note}}</div></div>{{end}}</div>

<h2 class="group">Is the chain running on schedule?</h2>
<div class="charts">{{range $i, $p := .Chain}}<div class="chartbox{{if eq $i 0}} wide{{end}}">{{$p.Chart}}<p class="why">{{$p.Note}}</p></div>{{end}}</div>

<h2 class="group">Who mines, and what people do</h2>
<div class="charts">{{range .Activity}}<div class="chartbox">{{.Chart}}<p class="why">{{.Note}}</p></div>{{end}}</div>

<h2 class="group">Is the randomness fair?</h2>
<div class="chartbox" style="margin-bottom:24px">{{.Randomness.Chart}}<p class="why">{{.Randomness.Note}}</p></div>

<h2 class="group">Is this node healthy? <span class="dim" style="text-transform:none;letter-spacing:0">(since it started {{.Uptime}} ago)</span></h2>
<div class="charts">{{range .Health}}<div class="chartbox">{{.Chart}}<p class="why">{{.Note}}</p></div>{{end}}</div>
<div class="panel"><div class="counters">
  <div>Blocks accepted from peers<b>{{.V.Accepted}}</b></div>
  <div>Invalid blocks rejected<b>{{.V.Rejected}}</b></div>
  <div>Blocks that arrived before their parent<b>{{.V.Orphans}}</b></div>
  <div>Reorganizations<b>{{len .V.Reorgs}}</b></div>
</div></div>
<div class="panel">
  <h2>Reorganizations seen by this node</h2>
  <table><tr><th>When (UTC)</th><th>Depth</th><th>From tip</th><th>To tip</th></tr>
  {{range .V.Reorgs}}<tr><td class="mono">{{.Time.UTC.Format "2006-01-02 15:04:05"}}</td><td>{{.Depth}} blocks</td>
    <td><a href="/block/{{.OldTip}}" class="hash">{{short .OldTip}}</a></td><td><a href="/block/{{.NewTip}}" class="hash">{{short .NewTip}}</a></td></tr>
  {{else}}<tr><td colspan="4" class="dim">None since the node started. Good: the network agrees on one chain.</td></tr>{{end}}</table>
</div>
{{end}}
{{end}}
`
