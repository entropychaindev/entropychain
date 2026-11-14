// Command questd is the Entropy Quest game master.
//
// It follows a node over RPC, replays every block from the game's start
// height through package questmaster, signs and submits the hero NFTs, item
// NFTs, payouts and refunds it owes, and serves the game's JSON API. It keeps
// no database: on every start it rebuilds its state from the chain, and the
// transactions it owes come out with the same hashes, so nothing is ever paid
// twice.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
	"github.com/entropychaindev/entropychain/go/internal/quest"
	"github.com/entropychaindev/entropychain/go/internal/questmaster"
	"github.com/entropychaindev/entropychain/go/internal/wallet"
)

type config struct {
	rpc, listen, cors, siteURL string
	addr, heroCol, itemCol     string
	start                      int64
	confirm                    int64
}

type server struct {
	cfg     config
	created map[string]bool // collections we asked the chain to create
	w       *wallet.Wallet
	hc      *http.Client
	mu      sync.RWMutex
	m       *questmaster.Master
	hashes  map[int64]string // applied height -> block hash, for reorg checks
	tip     int64
	purse   int64 // game wallet balance, base units
	synced  bool
}

func main() {
	var cfg config
	walletPath := flag.String("wallet", "quest-wallet.json", "wallet file holding the game master key")
	flag.StringVar(&cfg.addr, "address", "", "game master address (default: the wallet's first)")
	flag.StringVar(&cfg.rpc, "rpc", "http://127.0.0.1:8001", "node RPC base URL")
	flag.StringVar(&cfg.listen, "listen", "127.0.0.1:8095", "API listen address")
	flag.StringVar(&cfg.cors, "cors", "", "Access-Control-Allow-Origin for the API (empty: none)")
	flag.StringVar(&cfg.siteURL, "site", "https://entropychain.io/quest/", "game page URL, used in NFT metadata links")
	flag.StringVar(&cfg.heroCol, "hero-collection", "eq-heroes", "hero NFT collection id")
	flag.StringVar(&cfg.itemCol, "item-collection", "eq-items", "item NFT collection id")
	flag.Int64Var(&cfg.start, "start", 1, "first block height the game reads")
	flag.Int64Var(&cfg.confirm, "confirm", 1, "blocks to wait after a beacon before acting on it")
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	w, err := wallet.Open(*walletPath)
	if err != nil {
		log.Fatalf("wallet: %v", err)
	}
	if w.IsEncrypted() {
		pass := os.Getenv("QUEST_WALLET_PASSPHRASE")
		if err := w.Unlock(pass); err != nil {
			log.Fatalf("wallet is encrypted; set QUEST_WALLET_PASSPHRASE: %v", err)
		}
	}
	if cfg.addr == "" {
		if a := w.Addresses(); len(a) > 0 {
			cfg.addr = a[0]
		}
	}
	if !slices.Contains(w.Addresses(), cfg.addr) {
		log.Fatalf("address %q is not in %s", cfg.addr, *walletPath)
	}

	s := &server{cfg: cfg, w: w, hc: &http.Client{Timeout: 20 * time.Second}, created: map[string]bool{}}
	s.reset()
	log.Printf("Entropy Quest game master %s, rules v%d, reading from block %d", cfg.addr, quest.RulesVersion, cfg.start)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := &http.Server{Addr: cfg.listen, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("API on http://%s", cfg.listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	s.loop(ctx)
	_ = srv.Shutdown(context.Background())
}

func (s *server) reset() {
	s.m = questmaster.NewMaster(s.cfg.addr, s.cfg.heroCol, s.cfg.itemCol, s.cfg.siteURL, s.cfg.start)
	s.hashes = map[int64]string{}
	s.synced = false
}

// ---------------------------------------------------------------------------
// Following the chain

func (s *server) loop(ctx context.Context) {
	collectionsReady := false
	for {
		if !collectionsReady {
			collectionsReady = s.ensureCollections()
		}
		if err := s.step(); err != nil {
			log.Printf("sync: %v", err)
		}
		if collectionsReady && s.synced {
			s.deliver()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// step applies every block whose beacon block is confirmed.
func (s *server) step() error {
	var info struct {
		Height int64 `json:"height"`
	}
	if err := s.get("/rpc/info", &info); err != nil {
		return err
	}
	var bal struct {
		Balance int64 `json:"balance"`
	}
	if err := s.get("/rpc/balance/"+s.cfg.addr+"/ENTRO", &bal); err == nil {
		s.mu.Lock()
		s.purse = bal.Balance
		s.mu.Unlock()
	}

	// Reorg check: the last applied block must still be on the main chain.
	s.mu.RLock()
	h := s.m.Height
	want := s.hashes[h]
	s.mu.RUnlock()
	if want != "" {
		b, err := s.block(h)
		if err != nil {
			return err
		}
		if b.Hash != want {
			log.Printf("block %d changed (reorg); rebuilding from block %d", h, s.cfg.start)
			s.mu.Lock()
			s.reset()
			s.mu.Unlock()
		}
	}

	var next *questmaster.Block
	for {
		s.mu.RLock()
		h := s.m.Height + 1
		s.mu.RUnlock()
		if h+1+s.cfg.confirm > info.Height+1 { // need h+1 plus confirmations
			break
		}
		cur := next
		if cur == nil || cur.Height != h {
			b, err := s.block(h)
			if err != nil {
				return err
			}
			cur = &b
		}
		nb, err := s.block(h + 1)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.m.Apply(*cur, nb)
		s.hashes[h] = cur.Hash
		delete(s.hashes, h-50)
		s.tip = info.Height
		s.mu.Unlock()
		next = &nb
		if h%500 == 0 {
			log.Printf("replayed to block %d / %d", h, info.Height)
		}
	}
	// Blocks past the confirmed point may already hold outputs we sent.
	for h := s.m.Height + 1; h <= info.Height; h++ {
		b, err := s.block(h)
		if err != nil {
			break
		}
		s.mu.Lock()
		s.m.Peek(b)
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.tip = info.Height
	if !s.synced {
		log.Printf("in sync at block %d: %d heroes, %d runs, %d unsent outputs",
			s.m.Height, len(s.m.Heroes), len(s.m.Runs), len(s.m.Unsent()))
	}
	s.synced = true
	s.mu.Unlock()
	return nil
}

// block fetches one block and checks its hash.
func (s *server) block(h int64) (questmaster.Block, error) {
	var raw map[string]json.RawMessage
	if err := s.get(fmt.Sprintf("/rpc/block/%d", h), &raw); err != nil {
		return questmaster.Block{}, err
	}
	var hash string
	_ = json.Unmarshal(raw["hash"], &hash)
	delete(raw, "hash")
	body, _ := json.Marshal(raw)
	var b core.Block
	if err := json.Unmarshal(body, &b); err != nil {
		return questmaster.Block{}, fmt.Errorf("block %d: %w", h, err)
	}
	qb := questmaster.FromCore(&b)
	if qb.Hash != hash {
		return questmaster.Block{}, fmt.Errorf("block %d: hash mismatch", h)
	}
	return qb, nil
}

// ensureCollections creates the hero and item collections once.
func (s *server) ensureCollections() bool {
	var cols struct {
		Collections []struct {
			ID string `json:"collection_id"`
		} `json:"collections"`
	}
	if err := s.get("/rpc/collections?creator="+s.cfg.addr, &cols); err != nil {
		log.Printf("collections: %v", err)
		return false
	}
	have := map[string]bool{}
	for _, c := range cols.Collections {
		have[c.ID] = true
	}
	ready := true
	for _, c := range []struct{ id, name, desc string }{
		{s.cfg.heroCol, "Entropy Quest Heroes", "Heroes summoned by the block beacon. Play at " + s.cfg.siteURL},
		{s.cfg.itemCol, "Entropy Quest Items", "Loot found in Entropy Quest expeditions. Play at " + s.cfg.siteURL},
	} {
		if have[c.id] {
			continue
		}
		ready = false
		if s.created[c.id] {
			continue // already submitted; wait for a block
		}
		s.created[c.id] = true
		tx, err := core.NewTx(core.TxNFTCreate, s.cfg.addr, map[string]any{"collection_id": c.id, "name": c.name, "description": c.desc}, questmaster.OutputFee)
		if err == nil {
			err = s.submit(tx)
		}
		if err != nil && !strings.Contains(err.Error(), "already") {
			log.Printf("creating collection %s: %v", c.id, err)
			delete(s.created, c.id) // try again next round (e.g. once funded)
		} else {
			log.Printf("creating collection %s; waiting for it to be mined", c.id)
		}
	}
	return ready
}

// deliver signs and submits outputs that are neither on chain nor waiting in
// the mempool. A payout the treasury cannot cover waits for the next round.
func (s *server) deliver() {
	var mp struct {
		Hashes []string `json:"tx_hashes"`
	}
	if err := s.get("/rpc/mempool", &mp); err != nil {
		return
	}
	queued := map[string]bool{}
	for _, h := range mp.Hashes {
		queued[h] = true
	}
	s.mu.RLock()
	unsent := s.m.Unsent()
	s.mu.RUnlock()
	sent := 0
	for _, o := range unsent {
		if queued[o.Hash] || sent >= 40 {
			continue
		}
		err := s.submit(o.Tx)
		switch {
		case err == nil:
			sent++
		case strings.Contains(err.Error(), "insufficient"):
			log.Printf("treasury too low for %s %s; will retry", o.Kind, o.Hash[:12])
			return
		case strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "already"):
		default:
			log.Printf("output %s %s: %v", o.Kind, o.Hash[:12], err)
		}
	}
	if sent > 0 {
		log.Printf("sent %d outputs", sent)
	}
}

func (s *server) submit(tx *core.Transaction) error {
	if tx.Signature == nil {
		if err := s.w.SignTx(tx); err != nil {
			return err
		}
	}
	body, _ := json.Marshal(map[string]any{"tx": tx})
	resp, err := s.hc.Post(s.cfg.rpc+"/rpc/submit_tx", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = json.Unmarshal(b, &r)
	if !r.OK {
		return errors.New(r.Reason)
	}
	return nil
}

func (s *server) get(path string, out any) error {
	resp, err := s.hc.Get(s.cfg.rpc + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	return pyjson.Decode(body, out)
}

// ---------------------------------------------------------------------------
// API

type heroView struct {
	quest.Hero
	Owner    string         `json:"owner"`
	Level    int            `json:"level"`
	XPLevel  int64          `json:"xp_level"` // XP at which the current level began
	XPNext   int64          `json:"xp_next"`  // XP for the next level (0 at max)
	Progress quest.Progress `json:"progress"`
	Stats    quest.Stats    `json:"stats"` // without gear
	Minted   bool           `json:"minted"`
	Born     int64          `json:"born"`
}

type itemView struct {
	quest.Item
	RarityName string `json:"rarity_name"`
	Owner      string `json:"owner"`
	Minted     bool   `json:"minted"`
}

type runSummary struct {
	Tx       string `json:"tx"`
	Height   int64  `json:"height"`
	Player   string `json:"player"`
	HeroID   string `json:"hero_id"`
	HeroName string `json:"hero_name"`
	Class    string `json:"class"`
	Dungeon  int    `json:"dungeon"`
	Cleared  bool   `json:"cleared"`
	Survived bool   `json:"survived"`
	Payout   int64  `json:"payout"`
	XP       int64  `json:"xp"`
	Items    int    `json:"items"`
}

func (s *server) heroView(h *questmaster.HeroState) heroView {
	l := quest.Level(h.Progress.XP)
	v := heroView{Hero: h.Hero, Owner: h.Owner, Level: l, XPLevel: quest.XPForLevel(l), Progress: h.Progress,
		Stats: quest.Effective(h.Hero, l, nil), Born: h.Born}
	if l < quest.MaxLevel {
		v.XPNext = quest.XPForLevel(l + 1)
	}
	for _, o := range s.m.Outputs {
		if o.Kind == "hero" && o.For == h.Hero.SummonTx {
			v.Minted = o.Height > 0
		}
	}
	return v
}

func (s *server) itemView(it *questmaster.ItemState) itemView {
	v := itemView{Item: it.Item, RarityName: quest.Rarities[it.Item.Rarity], Owner: it.Owner}
	if rec := s.m.Runs[it.Run]; rec != nil {
		for _, o := range rec.Outputs {
			if o.Kind == "item" && o.Tx.Data["token_id"] == it.Item.ID {
				v.Minted = o.Height > 0
			}
		}
	}
	return v
}

func summary(r *questmaster.RunRecord) runSummary {
	return runSummary{Tx: r.Tx, Height: r.Height, Player: r.Player, HeroID: r.Hero.ID, HeroName: r.Hero.Name,
		Class: r.Hero.Class, Dungeon: r.Dungeon, Cleared: r.Result.Cleared, Survived: r.Result.Survived,
		Payout: r.Result.Payout, XP: r.Result.XP, Items: len(r.Result.Items)}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		writeJSON(w, 200, map[string]any{
			"game": quest.GameTag, "rules_version": quest.RulesVersion,
			"game_address": s.cfg.addr, "hero_collection": s.cfg.heroCol, "item_collection": s.cfg.itemCol,
			"summon_fee": quest.SummonFee, "dungeons": quest.Dungeons, "max_level": quest.MaxLevel,
			"start_height": s.cfg.start, "height": s.m.Height, "tip": s.tip, "synced": s.synced,
			"treasury": s.purse, "unsent": len(s.m.Unsent()),
			"heroes": len(s.m.Heroes), "runs": len(s.m.Runs), "items": len(s.m.Items),
		})
	})
	mux.HandleFunc("GET /api/player/{addr}", func(w http.ResponseWriter, r *http.Request) {
		addr := r.PathValue("addr")
		s.mu.RLock()
		defer s.mu.RUnlock()
		heroes := []heroView{}
		for _, h := range s.m.HeroesOf(addr) {
			heroes = append(heroes, s.heroView(h))
		}
		items := []itemView{}
		for _, it := range s.m.ItemsOf(addr) {
			items = append(items, s.itemView(it))
		}
		runs := []runSummary{}
		for i := len(s.m.Order) - 1; i >= 0 && len(runs) < 25; i-- {
			if rec := s.m.Runs[s.m.Order[i]]; rec.Player == addr {
				runs = append(runs, summary(rec))
			}
		}
		rejects := []*questmaster.Rejected{}
		for _, rj := range s.m.Rejects {
			if rj.Player == addr {
				rejects = append(rejects, rj)
			}
		}
		slices.SortFunc(rejects, func(a, b *questmaster.Rejected) int { return int(b.Height - a.Height) })
		writeJSON(w, 200, map[string]any{"address": addr, "height": s.m.Height, "heroes": heroes,
			"items": items, "runs": runs, "rejects": rejects[:min(10, len(rejects))]})
	})
	mux.HandleFunc("GET /api/hero/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		h := s.m.Heroes[r.PathValue("id")]
		if h == nil {
			writeJSON(w, 404, map[string]any{"error": "no such hero"})
			return
		}
		runs := []runSummary{}
		for i := len(h.Runs) - 1; i >= 0 && len(runs) < 50; i-- {
			runs = append(runs, summary(s.m.Runs[h.Runs[i]]))
		}
		writeJSON(w, 200, map[string]any{"hero": s.heroView(h), "runs": runs})
	})
	mux.HandleFunc("GET /api/tx/{tx}", func(w http.ResponseWriter, r *http.Request) {
		// What became of a player transaction: a run, a summon or a refund.
		tx := r.PathValue("tx")
		s.mu.RLock()
		defer s.mu.RUnlock()
		switch {
		case s.m.Runs[tx] != nil:
			writeJSON(w, 200, map[string]any{"status": "run", "run": s.m.Runs[tx]})
		case s.m.Summons[tx] != "":
			writeJSON(w, 200, map[string]any{"status": "summon", "hero": s.heroView(s.m.Heroes[s.m.Summons[tx]])})
		case s.m.Rejects[tx] != nil:
			writeJSON(w, 200, map[string]any{"status": "refund", "reject": s.m.Rejects[tx]})
		default:
			writeJSON(w, 200, map[string]any{"status": "waiting", "height": s.m.Height, "tip": s.tip})
		}
	})
	mux.HandleFunc("GET /api/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := []heroView{}
		for _, h := range s.m.Leaderboard(20) {
			out = append(out, s.heroView(h))
		}
		writeJSON(w, 200, map[string]any{"heroes": out})
	})
	mux.HandleFunc("GET /api/activity", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := []runSummary{}
		for i := len(s.m.Order) - 1; i >= 0 && len(out) < 25; i-- {
			out = append(out, summary(s.m.Runs[s.m.Order[i]]))
		}
		writeJSON(w, 200, map[string]any{"runs": out})
	})
	return s.cors(mux)
}

func (s *server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.cors != "" {
			w.Header().Set("Access-Control-Allow-Origin", s.cfg.cors)
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
