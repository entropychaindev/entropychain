// Command entropy is the Entropy Chain wallet CLI (the Go port of cli.py).
// It signs locally with wallet.json and submits through a node's RPC.
//
//	entropy [--rpc URL] [--wallet PATH] <command> [args] [flags]
//
// ENTROPY_RPC, ENTROPY_WALLET and ENTROPY_PASSPHRASE are honoured.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
	"github.com/entropychaindev/entropychain/go/internal/rpc"
	"github.com/entropychaindev/entropychain/go/internal/wallet"
)

type env struct {
	rpc        *rpc.Client
	walletPath string
}

type command struct {
	name, args, help string
	run              func(e *env, fs *flag.FlagSet, args []string) error
	flags            func(fs *flag.FlagSet)
}

var commands []command

func register(c command) { commands = append(commands, c) }

func main() {
	global := flag.NewFlagSet("entropy", flag.ExitOnError)
	rpcURL := global.String("rpc", envOr("ENTROPY_RPC", "http://127.0.0.1:8001"), "node RPC URL")
	walletPath := global.String("wallet", envOr("ENTROPY_WALLET", "./wallet.json"), "wallet file")
	global.Usage = usage
	_ = global.Parse(os.Args[1:])
	if global.NArg() == 0 {
		usage()
		os.Exit(2)
	}
	name, rest := global.Arg(0), global.Args()[1:]
	e := &env{rpc: rpc.NewClient(*rpcURL), walletPath: *walletPath}
	for _, c := range commands {
		if c.name != name {
			continue
		}
		fs := flag.NewFlagSet(name, flag.ExitOnError)
		fs.Usage = func() {
			fmt.Fprintf(os.Stderr, "usage: entropy %s %s\n  %s\n", c.name, c.args, c.help)
			fs.PrintDefaults()
		}
		if c.flags != nil {
			c.flags(fs)
		}
		args, err := parseInterspersed(fs, rest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		if err := c.run(e, fs, args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", name)
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: entropy [--rpc URL] [--wallet PATH] <command> [args]")
	fmt.Fprintln(os.Stderr, "\ncommands:")
	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-18s %s\n", c.name, c.help)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// parseInterspersed lets flags appear before or after positional args,
// like click does in cli.py.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" || isNumber(a) {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // let fs.Parse report it
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return pos, nil
}

func isNumber(s string) bool {
	_, err := fmt.Sscanf(s, "%g", new(float64))
	return err == nil
}

func need(args []string, n int, fs *flag.FlagSet) error {
	if len(args) != n {
		fs.Usage()
		return fmt.Errorf("expected %d argument(s), got %d", n, len(args))
	}
	return nil
}

// ---- Wallet helpers ----

func openWallet(e *env, unlock bool) (*wallet.Wallet, error) {
	w, err := wallet.Open(e.walletPath)
	if err != nil {
		return nil, err
	}
	if unlock && w.IsEncrypted() && !w.IsUnlocked() {
		pass := os.Getenv("ENTROPY_PASSPHRASE")
		if pass == "" {
			if pass, err = prompt("Wallet passphrase: "); err != nil {
				return nil, err
			}
		}
		if err := w.Unlock(pass); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func prompt(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

func sender(w *wallet.Wallet, from string) (string, error) {
	addrs := w.Addresses()
	if len(addrs) == 0 {
		return "", errors.New("wallet has no addresses; run `entropy new-address` first")
	}
	if from == "" {
		return addrs[0], nil
	}
	return w.Resolve(from)
}

func fromFlag(fs *flag.FlagSet) *string {
	return fs.String("from", "", "sender address or label (default: first address)")
}

// signAndSubmit signs tx with the wallet and submits it.
func signAndSubmit(e *env, w *wallet.Wallet, tx *core.Transaction, err error) error {
	if err != nil {
		return err
	}
	if err := w.SignTx(tx); err != nil {
		return err
	}
	var res struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
		TxHash string `json:"tx_hash"`
	}
	if err := e.rpc.Post("/rpc/submit_tx", map[string]any{"tx": tx}, &res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("rejected: %s", res.Reason)
	}
	fmt.Printf("✓ Submitted %s\n", res.TxHash)
	return nil
}

func tokenDecimals(e *env, sym string) int {
	sym = strings.ToUpper(sym)
	if sym == params.NativeToken {
		return 8
	}
	var t struct {
		Decimals json.Number `json:"decimals"`
	}
	if e.rpc.Get("/rpc/token/"+sym, &t) == nil {
		if d, err := t.Decimals.Int64(); err == nil && d >= 0 && d <= 18 {
			return int(d)
		}
	}
	return 8
}

func chainHeight(e *env) (int64, error) {
	var info struct {
		Height int64 `json:"height"`
	}
	err := e.rpc.Get("/rpc/info", &info)
	return info.Height, err
}

func printJSON(v any) {
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

func checkAddr(a string) error {
	if !crypto.AddressIsValid(a) {
		return fmt.Errorf("invalid address %q", a)
	}
	return nil
}

func init() {
	register(command{name: "info", help: "show node and chain status",
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			var info map[string]any
			if err := e.rpc.Get("/rpc/info", &info); err != nil {
				return err
			}
			printJSON(info)
			return nil
		}})

	var label *string
	var encrypt *bool
	register(command{name: "new-address", help: "create a new keypair in the wallet",
		flags: func(fs *flag.FlagSet) {
			label = fs.String("label", "", "optional label")
			encrypt = fs.Bool("encrypt", false, "encrypt a brand-new wallet with a passphrase")
		},
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			if *encrypt && !w.IsEncrypted() && len(w.Addresses()) == 0 {
				p1, err := prompt("New passphrase: ")
				if err != nil {
					return err
				}
				p2, _ := prompt("Repeat passphrase: ")
				if p1 == "" || p1 != p2 {
					return errors.New("passphrases empty or do not match")
				}
				if err := w.Encrypt(p1); err != nil {
					return err
				}
			}
			addr, err := w.CreateAddress(*label)
			if err != nil {
				return err
			}
			fmt.Println(addr)
			return nil
		}})

	register(command{name: "addresses", help: "list wallet addresses",
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			w, err := openWallet(e, false)
			if err != nil {
				return err
			}
			for _, a := range w.Addresses() {
				fmt.Printf("%s  %s\n", a, w.Label(a))
			}
			if w.IsEncrypted() {
				fmt.Fprintln(os.Stderr, "(wallet is encrypted; labels hidden until unlocked)")
			}
			return nil
		}})

	register(command{name: "encrypt", help: "encrypt the wallet (or change its passphrase)",
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			p1, err := prompt("New passphrase: ")
			if err != nil {
				return err
			}
			p2, _ := prompt("Repeat passphrase: ")
			if p1 == "" || p1 != p2 {
				return errors.New("passphrases empty or do not match")
			}
			if err := w.Encrypt(p1); err != nil {
				return err
			}
			fmt.Println("✓ Wallet encrypted")
			return nil
		}})

	register(command{name: "export-key", args: "ADDRESS", help: "print an address's private key",
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 1, fs); err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			k, err := w.ExportPrivateKey(args[0])
			if err != nil {
				return err
			}
			fmt.Println(k)
			return nil
		}})

	var importLabel *string
	register(command{name: "import-key", args: "PRIVATE_KEY_HEX", help: "import a private key",
		flags: func(fs *flag.FlagSet) { importLabel = fs.String("label", "", "optional label") },
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 1, fs); err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			addr, err := w.ImportPrivateKey(args[0], *importLabel)
			if err != nil {
				return err
			}
			fmt.Println(addr)
			return nil
		}})

	var balToken *string
	register(command{name: "balance", args: "ADDRESS", help: "show balances",
		flags: func(fs *flag.FlagSet) { balToken = fs.String("token", "", "one token (default: all)") },
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 1, fs); err != nil {
				return err
			}
			if *balToken != "" {
				var r struct {
					Token   string      `json:"token"`
					Balance json.Number `json:"balance"`
				}
				if err := e.rpc.Get("/rpc/balance/"+args[0]+"/"+*balToken, &r); err != nil {
					return err
				}
				v, _ := r.Balance.Int64()
				fmt.Printf("%s %s\n", core.FormatAmount(v), r.Token)
				return nil
			}
			var bals map[string]json.Number
			if err := e.rpc.Get("/rpc/balance/"+args[0], &bals); err != nil {
				return err
			}
			syms := make([]string, 0, len(bals))
			for s := range bals {
				syms = append(syms, s)
			}
			sort.Strings(syms)
			if len(syms) == 0 {
				fmt.Println("0 ENTRO")
			}
			for _, s := range syms {
				v, _ := bals[s].Int64()
				fmt.Printf("%s %s\n", core.FormatAmount(v), s)
			}
			return nil
		}})

	var sendToken, sendFrom *string
	var sendFee *int64
	register(command{name: "send", args: "RECIPIENT AMOUNT", help: "transfer ENTRO or a token",
		flags: func(fs *flag.FlagSet) {
			sendToken = fs.String("token", params.NativeToken, "token symbol")
			sendFrom = fromFlag(fs)
			sendFee = fs.Int64("fee", 10, "fee in smallest units")
		},
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 2, fs); err != nil {
				return err
			}
			if err := checkAddr(args[0]); err != nil {
				return err
			}
			tok := strings.ToUpper(*sendToken)
			amount, err := core.ParseAmount(args[1], tokenDecimals(e, tok))
			if err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *sendFrom)
			if err != nil {
				return err
			}
			fmt.Printf("Sending %s %s from %s to %s (fee %d)\n", args[1], tok, from, args[0], *sendFee)
			tx, err := core.NewTransfer(from, args[0], amount, tok, *sendFee)
			return signAndSubmit(e, w, tx, err)
		}})

	var ctDecimals, ctFee *int64
	var ctFrom *string
	register(command{name: "create-token", args: "SYMBOL NAME SUPPLY", help: "create a fungible token",
		flags: func(fs *flag.FlagSet) {
			ctDecimals = fs.Int64("decimals", 8, "token decimals")
			ctFrom = fromFlag(fs)
			ctFee = fs.Int64("fee", 100, "fee in smallest units")
		},
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 3, fs); err != nil {
				return err
			}
			supply, err := core.ParseAmount(args[2], int(*ctDecimals))
			if err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *ctFrom)
			if err != nil {
				return err
			}
			tx, err := core.NewTokenCreate(from, args[0], args[1], supply, *ctDecimals, *ctFee)
			return signAndSubmit(e, w, tx, err)
		}})

	var mtFrom *string
	var mtFee *int64
	register(command{name: "mint-token", args: "SYMBOL RECIPIENT AMOUNT", help: "mint more of a token you created",
		flags: func(fs *flag.FlagSet) { mtFrom = fromFlag(fs); mtFee = fs.Int64("fee", 10, "fee") },
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 3, fs); err != nil {
				return err
			}
			if err := checkAddr(args[1]); err != nil {
				return err
			}
			amount, err := core.ParseAmount(args[2], tokenDecimals(e, args[0]))
			if err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *mtFrom)
			if err != nil {
				return err
			}
			tx, err := core.NewTokenMint(from, args[0], args[1], amount, *mtFee)
			return signAndSubmit(e, w, tx, err)
		}})

	var ccDesc, ccFrom *string
	var ccFee *int64
	register(command{name: "create-collection", args: "COLLECTION_ID NAME", help: "create an NFT collection",
		flags: func(fs *flag.FlagSet) {
			ccDesc = fs.String("description", "", "description")
			ccFrom = fromFlag(fs)
			ccFee = fs.Int64("fee", 100, "fee")
		},
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 2, fs); err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *ccFrom)
			if err != nil {
				return err
			}
			tx, err := core.NewNFTCreate(from, args[0], args[1], *ccDesc, *ccFee)
			return signAndSubmit(e, w, tx, err)
		}})

	var mnURI, mnTraits, mnFrom *string
	var mnFee *int64
	register(command{name: "mint-nft", args: "COLLECTION_ID TOKEN_ID RECIPIENT", help: "mint an NFT",
		flags: func(fs *flag.FlagSet) {
			mnURI = fs.String("uri", "", "metadata URI")
			mnTraits = fs.String("traits", "", "JSON object of traits")
			mnFrom = fromFlag(fs)
			mnFee = fs.Int64("fee", 5, "fee")
		},
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 3, fs); err != nil {
				return err
			}
			if err := checkAddr(args[2]); err != nil {
				return err
			}
			var traits map[string]any
			if *mnTraits != "" {
				if err := pyjson.Decode([]byte(*mnTraits), &traits); err != nil {
					return fmt.Errorf("--traits: %w", err)
				}
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *mnFrom)
			if err != nil {
				return err
			}
			tx, err := core.NewNFTMint(from, args[0], args[1], args[2], *mnURI, traits, *mnFee)
			return signAndSubmit(e, w, tx, err)
		}})

	var rmFee *int64
	var rmURI, rmFrom *string
	register(command{name: "random-mint-nft", args: "COLLECTION_ID TOKEN_ID RECIPIENT", help: "mint an NFT whose traits the next block reveals",
		flags: func(fs *flag.FlagSet) {
			rmURI = fs.String("uri", "", "metadata URI")
			rmFrom = fromFlag(fs)
			rmFee = fs.Int64("fee", 5, "fee")
		},
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 3, fs); err != nil {
				return err
			}
			if err := checkAddr(args[2]); err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *rmFrom)
			if err != nil {
				return err
			}
			fmt.Printf("Random-minting %s#%s -> %s (traits revealed by the next block)\n", args[0], args[1], args[2])
			tx, err := core.NewNFTRandomMint(from, args[0], args[1], args[2], *rmURI, *rmFee)
			return signAndSubmit(e, w, tx, err)
		}})

	var tnFrom *string
	var tnFee *int64
	register(command{name: "transfer-nft", args: "COLLECTION_ID TOKEN_ID RECIPIENT", help: "transfer an NFT",
		flags: func(fs *flag.FlagSet) { tnFrom = fromFlag(fs); tnFee = fs.Int64("fee", 1, "fee") },
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 3, fs); err != nil {
				return err
			}
			if err := checkAddr(args[2]); err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *tnFrom)
			if err != nil {
				return err
			}
			tx, err := core.NewNFTTransfer(from, args[0], args[1], args[2], *tnFee)
			return signAndSubmit(e, w, tx, err)
		}})

	var clPrize, clTicket, clFrom *string
	var clDrawIn, clDrawBlocks, clFee *int64
	register(command{name: "create-lottery", args: "LOTTERY_ID", help: "create an ENTRO lottery",
		flags: func(fs *flag.FlagSet) {
			clPrize = fs.String("prize", "", "prize in ENTRO (required)")
			clTicket = fs.String("ticket", "", "ticket price in ENTRO (required)")
			clDrawIn = fs.Int64("draw-in", 0, "draw N blocks from now (required, >= 2)")
			clDrawBlocks = fs.Int64("draw-blocks", 1, "aggregate this many consecutive block beacons (1-16)")
			clFrom = fromFlag(fs)
			clFee = fs.Int64("fee", 50, "fee")
		},
		run: func(e *env, fs *flag.FlagSet, args []string) error {
			if err := need(args, 1, fs); err != nil {
				return err
			}
			if *clPrize == "" || *clTicket == "" || *clDrawIn < 2 {
				return errors.New("--prize, --ticket and --draw-in (>= 2) are required")
			}
			prize, err := core.ParseAmount(*clPrize, 8)
			if err != nil {
				return err
			}
			ticket, err := core.ParseAmount(*clTicket, 8)
			if err != nil {
				return err
			}
			h, err := chainHeight(e)
			if err != nil {
				return err
			}
			w, err := openWallet(e, true)
			if err != nil {
				return err
			}
			from, err := sender(w, *clFrom)
			if err != nil {
				return err
			}
			fmt.Printf("Creating lottery %q: prize=%s ENTRO, ticket=%s ENTRO, draw at block %d\n",
				args[0], *clPrize, *clTicket, h+*clDrawIn)
			tx, err := core.NewLotteryCreate(from, args[0], prize, ticket, h+*clDrawIn, *clDrawBlocks, "", *clFee)
			return signAndSubmit(e, w, tx, err)
		}})

	for _, lc := range []struct {
		name, help string
		make       func(sender, id string, fee int64) (*core.Transaction, error)
	}{
		{"enter-lottery", "buy a lottery ticket", core.NewLotteryEnter},
		{"draw-lottery", "draw a lottery (after its draw block)", core.NewLotteryDraw},
	} {
		lc := lc
		var from *string
		var fee *int64
		register(command{name: lc.name, args: "LOTTERY_ID", help: lc.help,
			flags: func(fs *flag.FlagSet) { from = fromFlag(fs); fee = fs.Int64("fee", 1, "fee") },
			run: func(e *env, fs *flag.FlagSet, args []string) error {
				if err := need(args, 1, fs); err != nil {
					return err
				}
				w, err := openWallet(e, true)
				if err != nil {
					return err
				}
				s, err := sender(w, *from)
				if err != nil {
					return err
				}
				tx, err := lc.make(s, args[0], *fee)
				return signAndSubmit(e, w, tx, err)
			}})
	}

	for _, sc := range []struct{ name, args, help, path string }{
		{"show-lottery", "LOTTERY_ID", "show lottery state", "/rpc/lottery/%s"},
		{"show-nft", "COLLECTION_ID TOKEN_ID", "show an NFT", "/rpc/nft/%s/%s"},
		{"history", "ADDRESS", "recent transactions for an address", "/rpc/history/%s"},
		{"block", "HEIGHT", "show a main-chain block", "/rpc/block/%s"},
		{"peers", "", "show the node's peers", "/rpc/peers"},
		{"mempool", "", "show pending transactions", "/rpc/mempool"},
	} {
		sc := sc
		n := strings.Count(sc.path, "%s")
		register(command{name: sc.name, args: sc.args, help: sc.help,
			run: func(e *env, fs *flag.FlagSet, args []string) error {
				if err := need(args, n, fs); err != nil {
					return err
				}
				vals := make([]any, n)
				for i := range vals {
					vals[i] = args[i]
				}
				var out any
				if err := e.rpc.Get(fmt.Sprintf(sc.path, vals...), &out); err != nil {
					return err
				}
				printJSON(out)
				return nil
			}})
	}
}
