package main

// NFTs and lotteries for the desktop app: miners can see what they own or
// have won, send NFTs, and enter lotteries without needing the web wallet.
// Creating collections and lotteries stays in the web wallet.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
)

type Trait struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type NFTView struct {
	Collection   string  `json:"collection"`
	TokenID      string  `json:"tokenId"`
	MetadataURI  string  `json:"metadataUri"`
	Traits       []Trait `json:"traits"`
	Legendary    bool    `json:"legendary"`
	ColorSeed    string  `json:"colorSeed"`
	Random       bool    `json:"random"`
	Pending      bool    `json:"pending"`
	RevealHeight int64   `json:"revealHeight"`
	MintedAt     int64   `json:"mintedAt"`
	Creator      string  `json:"creator"`
}

// MyNFTs lists the NFTs the active account owns, newest first.
func (a *App) MyNFTs() []NFTView {
	if a.chain == nil || a.active == "" {
		return nil
	}
	var out []NFTView
	for _, n := range a.chain.NFTsByOwner(a.active) {
		v := NFTView{Collection: n.Collection, TokenID: n.TokenID, MetadataURI: n.MetadataURI,
			MintedAt: n.MintedAtHeight, Creator: n.Creator, Pending: n.TraitsPending}
		if n.EntropySourceHeight != nil {
			v.Random, v.RevealHeight = true, *n.EntropySourceHeight
		}
		v.Traits, v.Legendary, v.ColorSeed = traitList(n.Traits)
		out = append(out, v)
	}
	return out
}

// traitList flattens any trait value (a map from nft_mint, or the random
// traits struct) into sorted key/value strings for display.
func traitList(t any) ([]Trait, bool, string) {
	if t == nil {
		return nil, false, ""
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, false, ""
	}
	var m map[string]any
	if pyjson.Decode(raw, &m) != nil {
		return nil, false, ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "entropy_source_height" { // shown separately as "revealed by"
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []Trait
	legendary, color := false, ""
	for _, k := range keys {
		var s string
		switch v := m[k].(type) {
		case string:
			s = v
		case bool:
			s = map[bool]string{true: "yes", false: "no"}[v]
			if k == "is_legendary" {
				legendary = v
			}
		default:
			b, _ := pyjson.Dumps(v)
			s = string(b)
		}
		if k == "color_seed" {
			color = s
		}
		out = append(out, Trait{strings.ReplaceAll(k, "_", " "), s})
	}
	return out, legendary, color
}

// SendNFT transfers one of the active account's NFTs.
func (a *App) SendNFT(collection, tokenID, recipient string) (string, error) {
	recipient = strings.TrimSpace(recipient)
	if !crypto.AddressIsValid(recipient) {
		return "", errors.New("invalid recipient address")
	}
	if recipient == a.active {
		return "", errors.New("that is this account's own address")
	}
	return a.submit(core.NewNFTTransfer(a.active, collection, tokenID, recipient, 1))
}

// ---- NFT metadata (images) ----

type NFTMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

var (
	metaMu    sync.Mutex
	metaCache = map[string]NFTMeta{}
	metaHTTP  = &http.Client{Timeout: 6 * time.Second}
)

// NFTMetadata fetches an NFT's metadata JSON so the app can show its image.
// Only https URLs are fetched, responses are capped at 256 KB, and only an
// https image URL is passed back to the UI.
func (a *App) NFTMetadata(uri string) (NFTMeta, error) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return NFTMeta{}, errors.New("only https metadata links are loaded")
	}
	metaMu.Lock()
	if m, ok := metaCache[uri]; ok {
		metaMu.Unlock()
		return m, nil
	}
	metaMu.Unlock()

	resp, err := metaHTTP.Get(u.String())
	if err != nil {
		return NFTMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return NFTMeta{}, fmt.Errorf("metadata returned HTTP %d", resp.StatusCode)
	}
	var m NFTMeta
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&m); err != nil {
		return NFTMeta{}, errors.New("metadata is not valid JSON")
	}
	if iu, err := url.Parse(m.Image); err != nil || iu.Scheme != "https" {
		m.Image = ""
	}
	metaMu.Lock()
	metaCache[uri] = m
	metaMu.Unlock()
	return m, nil
}

// ---- Lotteries ----

type LotteryView struct {
	ID          string `json:"id"`
	Organizer   string `json:"organizer"`
	Prize       string `json:"prize"`
	Pool        string `json:"pool"`
	Ticket      string `json:"ticket"`
	DrawHeight  int64  `json:"drawHeight"`
	DrawBlocks  int64  `json:"drawBlocks"`
	Entrants    int    `json:"entrants"`
	Status      string `json:"status"` // open | closed | ready | drawn
	BlocksLeft  int64  `json:"blocksLeft"`
	Entered     bool   `json:"entered"`
	IsOrganizer bool   `json:"isOrganizer"`
	Winner      string `json:"winner"`
	YouWon      bool   `json:"youWon"`
	DrawnAt     int64  `json:"drawnAt"`
}

// Lotteries lists active lotteries, then finished ones the active account
// took part in, then other recently finished ones.
func (a *App) Lotteries() []LotteryView {
	if a.chain == nil {
		return nil
	}
	h := a.chain.Height()
	me := a.active
	var active, mine, others []LotteryView
	for _, l := range a.chain.Lotteries() {
		k := max(l.DrawBlocks, 1)
		v := LotteryView{ID: l.ID, Organizer: l.Organizer, DrawHeight: l.DrawHeight, DrawBlocks: k,
			Entrants: len(l.Entrants), IsOrganizer: l.Organizer == me,
			Prize:  FormatUnits(l.PrizeAmount, a.decimals(l.Token)) + " " + l.Token,
			Pool:   core.FormatAmount(l.EntryPool) + " " + params.NativeToken,
			Ticket: core.FormatAmount(l.TicketPrice) + " " + params.NativeToken}
		for _, e := range l.Entrants {
			if e == me {
				v.Entered = true
			}
		}
		switch {
		case l.Drawn:
			v.Status = "drawn"
			if l.Winner != nil {
				v.Winner, v.YouWon = *l.Winner, *l.Winner == me
			}
			if l.DrawnAtHeight != nil {
				v.DrawnAt = *l.DrawnAtHeight
			}
		case h+1 < l.DrawHeight:
			v.Status, v.BlocksLeft = "open", l.DrawHeight-1-h
		case h >= l.DrawHeight+k-1:
			v.Status = "ready"
		default:
			v.Status, v.BlocksLeft = "closed", l.DrawHeight+k-1-h
		}
		switch {
		case v.Status != "drawn":
			active = append(active, v)
		case v.Entered || v.IsOrganizer:
			mine = append(mine, v)
		default:
			others = append(others, v)
		}
	}
	if len(others) > 10 {
		others = others[:10]
	}
	return append(append(active, mine...), others...)
}

// EnterLottery buys one ticket for the active account.
func (a *App) EnterLottery(id string) (string, error) {
	return a.submit(core.NewLotteryEnter(a.active, id, 1))
}

// DrawLottery submits the draw for a lottery whose draw window has passed.
// Anyone may do this; the winner is fixed by the chain, not by the sender.
func (a *App) DrawLottery(id string) (string, error) {
	return a.submit(core.NewLotteryDraw(a.active, id, 1))
}
