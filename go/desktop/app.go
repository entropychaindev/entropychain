package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/entropychaindev/entropychain/go/internal/chain"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/miner"
	"github.com/entropychaindev/entropychain/go/internal/p2p"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
	"github.com/entropychaindev/entropychain/go/internal/wallet"
)

// Network settings for the public testnet. The public testnet runs with
// the relaxed Argon2 memory cost (TESTNET_FAST in the old Python miner).
const (
	defaultP2PPort = 9001
	testnetFast    = true
	explorerURL    = "https://explorer.entropychain.io"
	websiteURL     = "https://entropychain.io"
)

var defaultSeeds = []p2p.Addr{{Host: "seed.entropychain.io", Port: 9001}}

// App is bound to the frontend; its exported methods are callable from JS.
type App struct {
	ctx     context.Context
	dataDir string
	logs    *logBuffer

	mu       sync.Mutex
	chain    *chain.Chain
	node     *p2p.Node
	wallet   *wallet.Wallet
	active   string
	startErr string

	minerCancel context.CancelFunc
	miner       *miner.Miner
	threads     int
	minedBase   uint64

	rateMu     sync.Mutex
	lastHashes uint64
	lastSample time.Time
	hashRate   float64
}

func NewApp() *App {
	return &App{dataDir: appDataDir(), logs: newLogBuffer(500)}
}

// appDataDir matches miner_app/paths.py so existing wallets are found.
// ENTROPYCHAIN_DATADIR overrides it (for testing or running several copies).
func appDataDir() string {
	if d := os.Getenv("ENTROPYCHAIN_DATADIR"); d != "" {
		return d
	}
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support")
	default:
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
	}
	return filepath.Join(base, "EntropyChain")
}

func (a *App) walletPath() string { return filepath.Join(a.dataDir, "data", "wallet.json") }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	_ = os.MkdirAll(filepath.Join(a.dataDir, "data"), 0o700)

	var out io.Writer = a.logs
	if f, err := os.OpenFile(filepath.Join(a.dataDir, "miner.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		out = io.MultiWriter(f, a.logs)
	}
	log.SetOutput(out)
	log.SetFlags(log.Ltime)
	a.logs.onLine = func(line string) { wruntime.EventsEmit(ctx, "log", line) }

	if testnetFast {
		params.EnableTestnetFast()
	}
	w, err := wallet.Open(a.walletPath())
	if err != nil {
		a.startErr = "Could not read wallet: " + err.Error()
		return
	}
	a.wallet = w
	if addrs := w.Addresses(); len(addrs) > 0 {
		a.active = addrs[0]
	}

	dir := filepath.Join(a.dataDir, "data", "chaindata")
	c, err := chain.Open(dir)
	if errors.Is(err, chain.ErrWrongNetwork) {
		// Chain data from an older testnet. Testnets get reset, so move it
		// aside (never delete it) and start syncing the current network.
		old := dir + "-old-" + time.Now().Format("20060102-150405")
		if rerr := os.Rename(dir, old); rerr != nil {
			err = fmt.Errorf("%v; could not move it aside: %w", err, rerr)
		} else {
			log.Printf("Found chain data from an older network; moved it to %s and starting fresh", old)
			c, err = chain.Open(dir)
		}
	}
	if err != nil {
		a.startErr = "Could not open chain database: " + err.Error()
		log.Print(a.startErr)
		return
	}
	a.chain = c
	a.startNode(ctx)
}

// startNode runs the P2P node, falling back to an ephemeral port if 9001
// is taken (e.g. by a separate entropyd on the same machine).
func (a *App) startNode(ctx context.Context) {
	seeds := defaultSeeds
	if s := os.Getenv("ENTROPYCHAIN_SEEDS"); s != "" {
		if parsed, err := p2p.ParseAddrs(s); err == nil {
			seeds = parsed
		} else {
			log.Printf("Ignoring ENTROPYCHAIN_SEEDS: %v", err)
		}
	}
	for _, port := range []int{defaultP2PPort, 0} {
		n := p2p.New(a.chain, port, seeds, p2p.DefaultMaxPeers)
		if err := n.Start(ctx); err != nil {
			log.Printf("P2P port %d unavailable: %v", port, err)
			continue
		}
		a.node = n
		break
	}
	a.chain.Subscribe(func(ev chain.Event) {
		wruntime.EventsEmit(ctx, "tip", ev.NewTip.Height)
	})
}

func (a *App) shutdown(ctx context.Context) {
	a.StopMining()
	if a.chain != nil {
		_ = a.chain.Close()
	}
}

// ---- State & accounts ----

type Account struct {
	Address string `json:"address"`
	Label   string `json:"label"`
}

type State struct {
	HasWallet  bool      `json:"hasWallet"`
	Accounts   []Account `json:"accounts"`
	Active     string    `json:"active"`
	Network    string    `json:"network"`
	Version    string    `json:"version"`
	DataDir    string    `json:"dataDir"`
	WalletPath string    `json:"walletPath"`
	MaxThreads int       `json:"maxThreads"`
	Error      string    `json:"error"`
}

func (a *App) GetState() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := State{Network: strings.ToUpper(params.GenesisNetworkTag), Version: version,
		DataDir: a.dataDir, WalletPath: a.walletPath(), MaxThreads: runtime.NumCPU(), Error: a.startErr, Active: a.active}
	if a.wallet != nil {
		for _, addr := range a.wallet.Addresses() {
			s.Accounts = append(s.Accounts, Account{addr, a.wallet.Label(addr)})
		}
		s.HasWallet = len(s.Accounts) > 0
	}
	return s
}

func (a *App) requireWallet() error {
	if a.wallet == nil {
		return errors.New("wallet unavailable")
	}
	if !a.wallet.IsUnlocked() {
		return errors.New("this wallet is passphrase-encrypted; unlock support is not in the desktop app yet")
	}
	return nil
}

// CreateAccount generates a new keypair and makes it active.
func (a *App) CreateAccount(label string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireWallet(); err != nil {
		return "", err
	}
	if label == "" {
		label = "default"
	}
	addr, err := a.wallet.CreateAddress(label)
	if err == nil {
		a.active = addr
		log.Printf("Created account %s (%s)", addr, label)
	}
	return addr, err
}

// ImportAccount adds an existing private key and makes it active.
func (a *App) ImportAccount(privHex, label string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireWallet(); err != nil {
		return "", err
	}
	privHex = strings.TrimSpace(strings.TrimPrefix(privHex, "0x"))
	if len(privHex) != 64 {
		return "", errors.New("private key must be 64 hexadecimal characters")
	}
	if label == "" {
		label = "imported"
	}
	addr, err := a.wallet.ImportPrivateKey(privHex, label)
	if err == nil {
		a.active = addr
		log.Printf("Imported account %s (%s)", addr, label)
	}
	return addr, err
}

// SetActive switches the active account.
func (a *App) SetActive(addr string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, x := range a.wallet.Addresses() {
		if x == addr {
			a.active = addr
			return nil
		}
	}
	return errors.New("unknown account")
}

// ExportKey returns the private key of an account.
func (a *App) ExportKey(addr string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireWallet(); err != nil {
		return "", err
	}
	return a.wallet.ExportPrivateKey(addr)
}

// BackupWallet copies wallet.json to a location the user picks.
func (a *App) BackupWallet() (string, error) {
	dest, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Save wallet backup",
		DefaultFilename: "entropychain-wallet-" + time.Now().Format("20060102") + ".json",
		Filters:         []wruntime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}},
	})
	if err != nil || dest == "" {
		return "", err
	}
	data, err := os.ReadFile(a.walletPath())
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

// OpenDataFolder opens the app data directory in the file manager.
func (a *App) OpenDataFolder() error {
	cmd := map[string]string{"windows": "explorer", "darwin": "open"}[runtime.GOOS]
	if cmd == "" {
		cmd = "xdg-open"
	}
	return exec.Command(cmd, a.dataDir).Start()
}

// OpenExplorer opens the block explorer for an address (or the home page).
func (a *App) OpenExplorer(addr string) {
	url := explorerURL
	if addr != "" {
		url += "/address/" + addr
	}
	wruntime.BrowserOpenURL(a.ctx, url)
}

// OpenWebsite opens the project website.
func (a *App) OpenWebsite() { wruntime.BrowserOpenURL(a.ctx, websiteURL) }

// ---- Mining ----

type Status struct {
	Ready      bool    `json:"ready"`
	Height     int64   `json:"height"`
	Peers      int     `json:"peers"`
	Difficulty uint64  `json:"difficulty"`
	TipHash    string  `json:"tipHash"`
	TipAgeSec  int64   `json:"tipAgeSec"`
	Mining     bool    `json:"mining"`
	Waiting    bool    `json:"waiting"`
	Threads    int     `json:"threads"`
	HashRate   float64 `json:"hashRate"`
	Mined      uint64  `json:"mined"`
	Balance    string  `json:"balance"`
	Mempool    int     `json:"mempool"`
}

func (a *App) GetStatus() Status {
	a.mu.Lock()
	c, n, m, active, threads := a.chain, a.node, a.miner, a.active, a.threads
	mining := a.minerCancel != nil
	a.mu.Unlock()
	if c == nil {
		return Status{}
	}
	tip := c.Tip()
	s := Status{Ready: true, Height: tip.Height, Difficulty: tip.Difficulty, TipHash: tip.Hash,
		TipAgeSec: time.Now().Unix() - int64(tip.Timestamp), Mining: mining, Threads: threads,
		Mempool: len(c.MempoolTxs())}
	if n != nil {
		s.Peers = n.PeerCount()
	}
	if active != "" {
		s.Balance = core.FormatAmount(c.Balance(active, params.NativeToken))
	}
	if m != nil {
		s.Waiting = mining && m.Waiting()
		s.Mined = m.BlocksFound()
		s.HashRate = a.sampleRate(m.HashCount())
	}
	return s
}

// sampleRate turns the miner's cumulative hash count into a smoothed rate.
func (a *App) sampleRate(total uint64) float64 {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	now := time.Now()
	if !a.lastSample.IsZero() {
		if dt := now.Sub(a.lastSample).Seconds(); dt >= 1 {
			inst := float64(total-a.lastHashes) / dt
			if a.hashRate == 0 {
				a.hashRate = inst
			} else {
				a.hashRate = 0.7*a.hashRate + 0.3*inst
			}
			a.lastHashes, a.lastSample = total, now
		}
	} else {
		a.lastHashes, a.lastSample = total, now
	}
	return a.hashRate
}

// StartMining mines to the active account on the given number of threads.
func (a *App) StartMining(threads int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.chain == nil {
		return errors.New("chain is not available")
	}
	if a.active == "" {
		return errors.New("create or import an account first")
	}
	if a.minerCancel != nil {
		return nil
	}
	threads = max(1, min(threads, runtime.NumCPU()))
	ctx, cancel := context.WithCancel(a.ctx)
	var ready func() bool
	if a.node != nil {
		ready = a.node.Synced
	}
	m := miner.New(a.chain, a.active, threads, ready)
	a.miner, a.minerCancel, a.threads = m, cancel, threads
	a.rateMu.Lock()
	a.lastSample, a.hashRate = time.Time{}, 0
	a.rateMu.Unlock()
	go m.Run(ctx)
	return nil
}

// StopMining stops the miner.
func (a *App) StopMining() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.minerCancel != nil {
		a.minerCancel()
		a.minerCancel = nil
		log.Printf("Mining stopped")
	}
}

// Logs returns recent log lines.
func (a *App) Logs() []string { return a.logs.lines() }

// ---- Wallet ----

type Balance struct {
	Token   string `json:"token"`
	Units   string `json:"units"`
	Display string `json:"display"`
}

func (a *App) decimals(token string) int64 {
	if token == params.NativeToken {
		return 8
	}
	if t, ok := a.chain.Token(token); ok {
		return t.Decimals
	}
	return 8
}

// FormatUnits renders integer units with the given decimals.
func FormatUnits(units, decimals int64) string {
	neg := units < 0
	if neg {
		units = -units
	}
	s := strconv.FormatInt(units, 10)
	if decimals > 0 {
		if int64(len(s)) <= decimals {
			s = strings.Repeat("0", int(decimals)-len(s)+1) + s
		}
		cut := len(s) - int(decimals)
		whole, frac := s[:cut], strings.TrimRight(s[cut:], "0")
		s = groupThousands(whole)
		if frac != "" {
			s += "." + frac
		}
	} else {
		s = groupThousands(s)
	}
	if neg {
		s = "-" + s
	}
	return s
}

func groupThousands(s string) string {
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Balances lists the active account's balances, ENTRO first.
func (a *App) Balances() []Balance {
	if a.chain == nil || a.active == "" {
		return nil
	}
	bals := a.chain.Balances(a.active)
	if _, ok := bals[params.NativeToken]; !ok {
		bals[params.NativeToken] = 0
	}
	out := make([]Balance, 0, len(bals))
	for tok, v := range bals {
		out = append(out, Balance{tok, strconv.FormatInt(v, 10), FormatUnits(v, a.decimals(tok))})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Token == params.NativeToken) != (out[j].Token == params.NativeToken) {
			return out[i].Token == params.NativeToken
		}
		return out[i].Token < out[j].Token
	})
	return out
}

type Activity struct {
	Height   int64  `json:"height"`
	Hash     string `json:"hash"`
	Outgoing bool   `json:"outgoing"`
	Text     string `json:"text"`
	Amount   string `json:"amount"`
}

func short(addr string) string {
	if len(addr) < 14 {
		return addr
	}
	return addr[:8] + "…" + addr[len(addr)-4:]
}

// History returns recent activity for the active account.
func (a *App) History() []Activity {
	if a.chain == nil || a.active == "" {
		return nil
	}
	me := a.active
	var out []Activity
	for _, it := range a.chain.History(me, 50, -1) {
		tx, d := it.Tx, it.Tx.Data
		str := func(k string) string { s, _ := pyjson.AsString(d[k]); return s }
		num := func(k string) int64 { v, _ := pyjson.AsInt(d[k]); return v }
		act := Activity{Height: it.Height, Hash: tx.Hash(), Outgoing: tx.Sender == me}
		switch tx.TxType {
		case core.TxCoinbase:
			act.Text = "Mined block reward"
			act.Amount = "+" + core.FormatAmount(num("amount")) + " ENTRO"
		case core.TxTransfer:
			tok := str("token")
			if tok == "" {
				tok = params.NativeToken
			}
			amt := FormatUnits(num("amount"), a.decimals(tok)) + " " + tok
			if act.Outgoing {
				act.Text, act.Amount = "Sent to "+short(str("recipient")), "-"+amt
			} else {
				act.Text, act.Amount = "Received from "+short(tx.Sender), "+"+amt
			}
		case core.TxTokenCreate:
			act.Text = "Created token " + strings.ToUpper(str("symbol"))
		case core.TxTokenMint:
			sym := strings.ToUpper(str("symbol"))
			amt := FormatUnits(num("amount"), a.decimals(sym)) + " " + sym
			if act.Outgoing {
				act.Text = "Minted " + sym + " to " + short(str("recipient"))
			} else {
				act.Text, act.Amount = "Received "+sym+" (minted)", "+"+amt
			}
		case core.TxNFTCreate:
			act.Text = "Created collection " + str("collection_id")
		case core.TxNFTMint, core.TxNFTRandomMint:
			act.Text = fmt.Sprintf("NFT %s#%s minted", str("collection_id"), str("token_id"))
		case core.TxNFTTransfer:
			if act.Outgoing {
				act.Text = fmt.Sprintf("Sent NFT %s#%s", str("collection_id"), str("token_id"))
			} else {
				act.Text = fmt.Sprintf("Received NFT %s#%s", str("collection_id"), str("token_id"))
			}
		case core.TxLotteryCreate:
			act.Text = "Created lottery " + str("lottery_id")
		case core.TxLotteryEnter:
			act.Text = "Entered lottery " + str("lottery_id")
		case core.TxLotteryDraw:
			act.Text = "Lottery " + str("lottery_id") + " drawn"
		default:
			act.Text = tx.TxType
		}
		if tx.TxType != core.TxCoinbase && act.Outgoing && tx.Fee > 0 && act.Amount == "" {
			act.Amount = "-" + core.FormatAmount(tx.Fee) + " ENTRO fee"
		}
		out = append(out, act)
	}
	return out
}

func (a *App) submit(tx *core.Transaction, err error) (string, error) {
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	err = a.requireWallet()
	if err == nil {
		err = a.wallet.SignTx(tx)
	}
	a.mu.Unlock()
	if err != nil {
		return "", err
	}
	if err := a.chain.Submit(tx); err != nil {
		return "", err
	}
	if a.node != nil {
		a.node.AnnounceTx(tx)
	}
	log.Printf("Submitted %s %s…", tx.TxType, tx.Hash()[:12])
	return tx.Hash(), nil
}

// Send transfers ENTRO or a token from the active account.
func (a *App) Send(recipient, amount, token string, fee int64) (string, error) {
	recipient = strings.TrimSpace(recipient)
	if !crypto.AddressIsValid(recipient) {
		return "", errors.New("invalid recipient address")
	}
	token = strings.ToUpper(strings.TrimSpace(token))
	if token == "" {
		token = params.NativeToken
	}
	units, err := core.ParseAmount(amount, int(a.decimals(token)))
	if err != nil {
		return "", err
	}
	if units <= 0 {
		return "", errors.New("amount must be positive")
	}
	return a.submit(core.NewTransfer(a.active, recipient, units, token, fee))
}

// ---- Tokens ----

type TokenInfo struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Decimals int64  `json:"decimals"`
	Supply   string `json:"supply"`
	Held     string `json:"held"`
}

// MyTokens lists tokens created by the active account.
func (a *App) MyTokens() []TokenInfo {
	if a.chain == nil || a.active == "" {
		return nil
	}
	var out []TokenInfo
	for _, sym := range a.chain.TokenSymbols() {
		t, _ := a.chain.Token(sym)
		if t.Creator != a.active {
			continue
		}
		out = append(out, TokenInfo{sym, t.Name, t.Decimals, FormatUnits(t.TotalSupply, t.Decimals),
			FormatUnits(a.chain.Balance(a.active, sym), t.Decimals)})
	}
	return out
}

// CreateToken creates a token with supply given in whole tokens.
func (a *App) CreateToken(symbol, name, supply string, decimals int64) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if len(symbol) < 1 || len(symbol) > 12 {
		return "", errors.New("symbol must be 1-12 characters")
	}
	for _, r := range symbol {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "", errors.New("symbol may only contain letters and digits")
		}
	}
	if _, exists := a.chain.Token(symbol); exists || symbol == params.NativeToken {
		return "", fmt.Errorf("token %s already exists", symbol)
	}
	if strings.TrimSpace(name) == "" {
		return "", errors.New("name is required")
	}
	units, err := core.ParseAmount(supply, int(decimals))
	if err != nil {
		return "", err
	}
	if units <= 0 {
		return "", errors.New("supply must be positive")
	}
	return a.submit(core.NewTokenCreate(a.active, symbol, strings.TrimSpace(name), units, decimals, 100))
}

// MintToken mints more of a token the active account created.
func (a *App) MintToken(symbol, amount, recipient string) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	t, ok := a.chain.Token(symbol)
	if !ok {
		return "", fmt.Errorf("token %s does not exist", symbol)
	}
	if t.Creator != a.active {
		return "", errors.New("only the token's creator can mint it")
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		recipient = a.active
	}
	if !crypto.AddressIsValid(recipient) {
		return "", errors.New("invalid recipient address")
	}
	units, err := core.ParseAmount(amount, int(t.Decimals))
	if err != nil {
		return "", err
	}
	if units <= 0 {
		return "", errors.New("amount must be positive")
	}
	return a.submit(core.NewTokenMint(a.active, symbol, recipient, units, 10))
}

// ---- Log buffer ----

type logBuffer struct {
	mu     sync.Mutex
	buf    []string
	max    int
	part   string
	onLine func(string)
}

func newLogBuffer(n int) *logBuffer { return &logBuffer{max: n} }

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.part += string(p)
	var emitted []string
	for {
		i := strings.IndexByte(l.part, '\n')
		if i < 0 {
			break
		}
		line := l.part[:i]
		l.part = l.part[i+1:]
		l.buf = append(l.buf, line)
		if len(l.buf) > l.max {
			l.buf = l.buf[len(l.buf)-l.max:]
		}
		emitted = append(emitted, line)
	}
	cb := l.onLine
	l.mu.Unlock()
	if cb != nil {
		for _, line := range emitted {
			cb(line)
		}
	}
	return len(p), nil
}

func (l *logBuffer) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.buf...)
}
