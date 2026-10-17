package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/miner"
	"github.com/entropychaindev/entropychain/go/internal/params"
)

func TestMain(m *testing.M) {
	params.GenesisDifficulty = 1
	params.ArgonMemoryKB = 8
	os.Exit(m.Run())
}

func mineOn(t *testing.T, c *chain.Chain, to string, txs ...*core.Transaction) {
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

// The web wallet lists lotteries and links history entries to the explorer.
func TestLotteriesAndHistory(t *testing.T) {
	c, err := chain.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	priv, pub, _ := crypto.GenerateKeypair()
	a := crypto.PubkeyToAddress(pub)
	mineOn(t, c, a)
	mineOn(t, c, a)
	tx, _ := core.NewLotteryCreate(a, "weekly", 1000, 10, c.Height()+10, 1, "", 50)
	_ = tx.SignWith(priv, pub)
	mineOn(t, c, a, tx)

	s := New(c, nil, "")
	get := func(path string, out any) {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s -> %d", path, rec.Code)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var lots struct {
		Count     int              `json:"count"`
		Lotteries []map[string]any `json:"lotteries"`
	}
	get("/rpc/lotteries", &lots)
	if lots.Count != 1 || lots.Lotteries[0]["lottery_id"] != "weekly" {
		t.Fatalf("lotteries: %+v", lots)
	}
	if e, ok := lots.Lotteries[0]["entrants"].([]any); !ok || len(e) != 0 {
		t.Fatalf("entrants should be an empty array: %#v", lots.Lotteries[0]["entrants"])
	}
	var hist struct {
		History []map[string]any `json:"history"`
	}
	get("/rpc/history/"+a, &hist)
	found := false
	for _, h := range hist.History {
		if h["tx_hash"] == tx.Hash() {
			found = true
		}
		if hs, _ := h["tx_hash"].(string); len(hs) != 64 || strings.Trim(hs, "0123456789abcdef") != "" {
			t.Errorf("bad tx_hash %v", h["tx_hash"])
		}
	}
	if !found {
		t.Error("lottery_create missing from history")
	}
}

// A browser sends an OPTIONS preflight before POSTing JSON. It must get a
// 2xx whether or not the node itself adds CORS headers (in production the
// reverse proxy adds them), or the web wallet cannot submit transactions.
func TestPreflightAnswered(t *testing.T) {
	for _, cors := range []string{"", "*"} {
		s := New(nil, nil, cors) // preflight never touches the chain
		for _, path := range []string{"/rpc/submit_tx", "/rpc/info"} {
			req := httptest.NewRequest(http.MethodOptions, path, nil)
			req.Header.Set("Origin", "https://wallet.entropychain.io")
			req.Header.Set("Access-Control-Request-Method", "POST")
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code < 200 || rec.Code > 299 {
				t.Errorf("cors=%q OPTIONS %s -> %d, want 2xx", cors, path, rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != cors {
				t.Errorf("cors=%q: Access-Control-Allow-Origin = %q", cors, got)
			}
		}
	}
}
