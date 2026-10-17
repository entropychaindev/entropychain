// Package explorer serves the HTML block explorer (the Go port of
// explorer.py) directly from the node's chain state. All output goes
// through html/template, so on-chain strings are always escaped.
package explorer

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/consensus"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
	"github.com/entropychaindev/entropychain/go/internal/stats"
)

// Server is the explorer HTTP handler.
type Server struct {
	chain *chain.Chain
	api   http.Handler // the node's RPC handler, mounted at /rpc/
	stats *stats.Collector
	mux   *http.ServeMux
	tpl   *template.Template
}

// New builds the explorer. api, if non-nil, serves /rpc/ requests; st, if
// non-nil, powers the /stats page.
func New(c *chain.Chain, api http.Handler, st *stats.Collector) *Server {
	s := &Server{chain: c, api: api, stats: st, mux: http.NewServeMux()}
	s.tpl = template.Must(template.New("").Funcs(funcs).Parse(templates + statsTemplate))
	s.mux.HandleFunc("GET /{$}", s.dashboard)
	s.mux.HandleFunc("GET /block/{key}", s.block)
	s.mux.HandleFunc("GET /tx/{hash}", s.tx)
	s.mux.HandleFunc("GET /address/{addr}", s.address)
	s.mux.HandleFunc("GET /token/{symbol}", s.token)
	s.mux.HandleFunc("GET /nft/{collection}/{token_id}", s.nft)
	s.mux.HandleFunc("GET /lottery/{lid}", s.lottery)
	s.mux.HandleFunc("GET /entropy", s.entropy)
	s.mux.HandleFunc("GET /search", s.search)
	s.mux.HandleFunc("GET /stats", s.statsPage)
	if api != nil {
		s.mux.Handle("/rpc/", api)
	}
	return s
}

// ListenAndServe runs the explorer until it fails.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	return srv.ListenAndServe()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; img-src 'self' data:")
	s.mux.ServeHTTP(w, r)
}

// ---- Template helpers ----

var funcs = template.FuncMap{
	"amount": func(units, decimals int64) string { return formatUnits(units, decimals) },
	"entro":  func(units int64) string { return formatUnits(units, 8) },
	"comma": func(v any) string {
		switch x := v.(type) {
		case int:
			return group(strconv.Itoa(x))
		case int64:
			return group(strconv.FormatInt(x, 10))
		case uint64:
			return group(strconv.FormatUint(x, 10))
		case fmt.Stringer:
			return group(x.String())
		}
		return fmt.Sprint(v)
	},
	"short":     func(h string) string { return shorten(h, 12) },
	"short16":   func(h string) string { return shorten(h, 16) },
	"shortAddr": shortAddr,
	"ago":       ago,
	"utc": func(ts float64) string {
		return time.Unix(int64(ts), 0).UTC().Format("2006-01-02 15:04:05 UTC")
	},
	"tagClass": func(t string) string {
		switch {
		case t == core.TxCoinbase:
			return "coinbase"
		case strings.HasPrefix(t, "lottery"):
			return "lottery"
		case strings.Contains(t, "nft"):
			return "nft"
		}
		return ""
	},
	"upper": strings.ToUpper,
}

func formatUnits(units, decimals int64) string {
	neg := units < 0
	if neg {
		units = -units
	}
	s := strconv.FormatInt(units, 10)
	out := group(s)
	if decimals > 0 {
		if int64(len(s)) <= decimals {
			s = strings.Repeat("0", int(decimals)-len(s)+1) + s
		}
		cut := len(s) - int(decimals)
		out = group(s[:cut])
		if frac := strings.TrimRight(s[cut:], "0"); frac != "" {
			out += "." + frac
		}
	}
	if neg {
		out = "-" + out
	}
	return out
}

func group(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func shorten(h string, n int) string {
	if len(h) <= n {
		return h
	}
	return h[:n] + "…"
}

func shortAddr(a string) string {
	if len(a) <= 16 {
		return a
	}
	return a[:6] + "…" + a[len(a)-6:]
}

func ago(ts float64) string {
	d := time.Since(time.Unix(int64(ts), 0))
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// ---- Rendering ----

type pageData struct {
	Title      string
	NetworkTag string
	Body       string // name of the content template
	Data       any
}

func (s *Server) render(w http.ResponseWriter, status int, title, body string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	err := s.tpl.ExecuteTemplate(w, "layout", pageData{title, strings.ToUpper(params.GenesisNetworkTag), body, data})
	if err != nil {
		log.Printf("explorer template %s: %v", body, err)
	}
}

func (s *Server) notFound(w http.ResponseWriter, what string) {
	s.render(w, http.StatusNotFound, what+" Not Found", "error", what+" not found.")
}

// ---- Pages ----

type blockRow struct {
	Height      int64
	Hash        string
	TxCount     int
	Timestamp   float64
	Difficulty  uint64
	EntropySeed string
}

type tokenRow struct {
	Symbol string
	chain.Token
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	tip := s.chain.Tip()
	nfts, lotteries := s.chain.Counts()
	var blocks []blockRow
	for h := tip.Height; h >= 0 && h > tip.Height-15; h-- {
		if b := s.chain.BlockByHeight(h); b != nil {
			h := b.Hash()
			blocks = append(blocks, blockRow{b.Height, h, len(b.Transactions), b.Timestamp, b.Difficulty, consensus.Beacon(h)})
		}
	}
	tokens := s.chain.Tokens()
	var trows []tokenRow
	for _, sym := range s.chain.TokenSymbols() {
		trows = append(trows, tokenRow{sym, tokens[sym]})
	}
	s.render(w, 200, "Dashboard", "dashboard", map[string]any{
		"Tip": tip, "Supply": consensus.IssuedSupply(tip.Height), "Tokens": trows,
		"TokenCount": len(trows), "NFTs": nfts, "Lotteries": lotteries, "Blocks": blocks,
	})
}

func (s *Server) block(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var b *core.Block
	if h, err := strconv.ParseInt(key, 10, 64); err == nil {
		b = s.chain.BlockByHeight(h)
	} else {
		b = s.chain.BlockByHash(key)
	}
	if b == nil {
		s.notFound(w, "Block")
		return
	}
	h := b.Hash()
	s.render(w, 200, fmt.Sprintf("Block #%d", b.Height), "block", map[string]any{
		"B": b, "Hash": h, "Main": s.chain.IsMainChain(h), "Work": s.chain.Work(h), "Beacon": consensus.Beacon(h),
	})
}

type kv struct {
	Key, Kind, Value string // Kind: addr | hash | text
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func payload(d map[string]any) []kv {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		v := d[k]
		if str, ok := v.(string); ok {
			switch {
			case crypto.AddressIsValid(str):
				out = append(out, kv{k, "addr", str})
			case hexRe.MatchString(str):
				out = append(out, kv{k, "hash", str})
			default:
				out = append(out, kv{k, "text", str})
			}
			continue
		}
		raw, err := pyjson.Dumps(v)
		if err != nil {
			raw, _ = json.Marshal(v)
		}
		out = append(out, kv{k, "text", string(raw)})
	}
	return out
}

func (s *Server) tx(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	it, ok := s.chain.TxByHash(hash)
	pending := false
	if !ok {
		if tx := s.chain.MempoolTx(hash); tx != nil {
			it, ok, pending = chain.HistoryItem{Height: -1, Tx: tx}, true, true
		}
	}
	if !ok {
		s.notFound(w, "Transaction")
		return
	}
	ts, _ := it.Tx.Timestamp.Float64()
	s.render(w, 200, "Transaction", "tx", map[string]any{
		"Tx": it.Tx, "Hash": hash, "Height": it.Height, "Pending": pending,
		"Time": ts, "Payload": payload(it.Tx.Data),
	})
}

type balRow struct {
	Token    string
	Amount   int64
	Decimals int64
}

func (s *Server) decimals(token string) int64 {
	if t, ok := s.chain.Token(token); ok {
		return t.Decimals
	}
	return 8
}

func (s *Server) address(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")
	var bals []balRow
	for tok, v := range s.chain.Balances(addr) {
		if v != 0 {
			bals = append(bals, balRow{tok, v, s.decimals(tok)})
		}
	}
	sort.Slice(bals, func(i, j int) bool {
		if (bals[i].Token == params.NativeToken) != (bals[j].Token == params.NativeToken) {
			return bals[i].Token == params.NativeToken
		}
		return bals[i].Token < bals[j].Token
	})
	var created []string
	tokens := s.chain.Tokens()
	for _, sym := range s.chain.TokenSymbols() {
		if tokens[sym].Creator == addr {
			created = append(created, sym)
		}
	}
	var cols []string
	for _, c := range s.chain.Collections(addr) {
		cols = append(cols, c.ID)
	}
	nfts := s.chain.NFTsByOwner(addr)
	total := len(nfts)
	if len(nfts) > 50 {
		nfts = nfts[:50]
	}
	s.render(w, 200, "Address "+shortAddr(addr), "address", map[string]any{
		"Addr": addr, "Valid": crypto.AddressIsValid(addr), "Balances": bals,
		"Created": strings.Join(created, ", "), "Collections": strings.Join(cols, ", "),
		"NFTs": nfts, "NFTCount": total, "History": s.chain.History(addr, 25, -1),
	})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	sym := strings.ToUpper(r.PathValue("symbol"))
	var info chain.Token
	var supply int64
	if sym == params.NativeToken {
		info = chain.Token{Name: "Entropy Chain Native Token", Decimals: 8, Creator: ""}
		supply = consensus.IssuedSupply(s.chain.Height())
	} else {
		t, ok := s.chain.Token(sym)
		if !ok {
			s.notFound(w, "Token")
			return
		}
		info, supply = t, t.TotalSupply
	}
	holders, count := s.chain.Holders(sym, 50)
	type hrow struct {
		Address string
		Balance int64
		Pct     string
	}
	rows := make([]hrow, len(holders))
	for i, h := range holders {
		pct := 0.0
		if supply > 0 {
			pct = float64(h.Balance) / float64(supply) * 100
		}
		rows[i] = hrow{h.Address, h.Balance, fmt.Sprintf("%.2f%%", pct)}
	}
	s.render(w, 200, "Token "+sym, "token", map[string]any{
		"Symbol": sym, "Info": info, "Supply": supply, "Holders": rows, "HolderCount": count,
	})
}

type trait struct {
	Key, Value string
	Legendary  bool
}

func traitList(v any) []trait {
	raw, _ := json.Marshal(v)
	var m map[string]any
	if pyjson.Decode(raw, &m) != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []trait
	for _, k := range keys {
		val := m[k]
		var sv string
		if str, ok := val.(string); ok {
			sv = str
		} else {
			b, _ := pyjson.Dumps(val)
			sv = string(b)
		}
		out = append(out, trait{k, sv, k == "is_legendary" && val == true})
	}
	return out
}

func (s *Server) nft(w http.ResponseWriter, r *http.Request) {
	col, tid := r.PathValue("collection"), r.PathValue("token_id")
	n, ok := s.chain.NFT(col, tid)
	if !ok {
		s.notFound(w, "NFT")
		return
	}
	data := map[string]any{"Col": col, "TID": tid, "N": n, "Traits": traitList(n.Traits)}
	if n.EntropySourceHeight != nil {
		data["Seed"] = s.chain.Beacon(*n.EntropySourceHeight) // "" until that block exists
	}
	s.render(w, 200, fmt.Sprintf("NFT %s#%s", col, tid), "nft", data)
}

func (s *Server) lottery(w http.ResponseWriter, r *http.Request) {
	lid := r.PathValue("lid")
	l, ok := s.chain.Lottery(lid)
	if !ok {
		s.notFound(w, "Lottery")
		return
	}
	height := s.chain.Height()
	status := "🟢 OPEN"
	switch {
	case l.Drawn:
		status = "🏆 DRAWN"
	case height+1 > l.DrawHeight+l.DrawBlocks-1:
		status = "🟡 READY TO DRAW"
	case height+1 >= l.DrawHeight:
		status = "🔒 ENTRY CLOSED"
	}
	winner := ""
	if l.Winner != nil {
		winner = *l.Winner
	}
	s.render(w, 200, "Lottery "+lid, "lottery", map[string]any{
		"ID": lid, "L": l, "Status": status, "Height": height, "Winner": winner,
	})
}

func (s *Server) entropy(w http.ResponseWriter, r *http.Request) {
	tip := s.chain.Height()
	var rows []blockRow
	for h := tip; h >= 1 && h > tip-30; h-- {
		if b := s.chain.BlockByHeight(h); b != nil {
			rows = append(rows, blockRow{Height: b.Height, Timestamp: b.Timestamp, EntropySeed: consensus.Beacon(b.Hash())})
		}
	}
	s.render(w, 200, "Entropy Beacon", "entropy", rows)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	redirect := func(path string) { http.Redirect(w, r, path, http.StatusFound) }
	switch {
	case q == "":
		s.render(w, http.StatusBadRequest, "Search", "error", "Empty query.")
	case isDigits(q):
		redirect("/block/" + q)
	case hexRe.MatchString(strings.ToLower(q)):
		if s.chain.HasBlock(strings.ToLower(q)) {
			redirect("/block/" + strings.ToLower(q))
		} else {
			redirect("/tx/" + strings.ToLower(q))
		}
	case crypto.AddressIsValid(q):
		redirect("/address/" + q)
	default:
		up := strings.ToUpper(q)
		if _, ok := s.chain.Token(up); ok || up == params.NativeToken {
			redirect("/token/" + up)
			return
		}
		if _, ok := s.chain.Lottery(q); ok {
			redirect("/lottery/" + q)
			return
		}
		s.render(w, http.StatusNotFound, "Not Found", "error", "No match for: "+q)
	}
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != "" && len(s) < 19
}
