package chain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/entropychaindev/entropychain/go/internal/consensus"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
)

// Transaction rules. These follow blockchain.py's validate_transaction and
// _apply_tx, with these deliberate tightenings (every one only rejects
// things Python would accept, so a Go node still follows a Python chain
// that never exercised them):
//
//   - Transactions in a block are validated in order against the state left
//     by the previous ones. Python checked them all against the pre-block
//     state, which allowed overdrafts within a single block.
//   - A transaction already on chain can never be included again (Python
//     skipped the duplicate check for block transactions: replay).
//   - Amounts, prices, heights and fees must be JSON integers that fit in
//     int64, and fees must be non-negative. Balance arithmetic is checked
//     for overflow.
//   - Fields Python reads unconditionally while applying (and would crash
//     on) are required: token name, lottery token/ticket_price/draw_height.
//   - Recipient addresses are validated for token_mint too.
//   - Token symbols must be ASCII so upper-casing is identical everywhere.
//   - A lottery draw must come in a block after its draw window, not in the
//     draw block itself.
//   - Beacon v2 (testnet-4): randomness comes from consensus.Beacon(block
//     hash), not the header's miner-chosen entropy_seed. Lotteries use the
//     beacon of their draw block (or several aggregated, draw_blocks), and
//     nft_random_mint takes its traits from the block AFTER the one that
//     includes it, so neither the miner nor the minter can pick the outcome
//     without discarding solved blocks.
//   - Lotteries in a non-native token must have ticket_price 0 (Python paid
//     the ENTRO ticket pool out as token units).
//   - Coinbase must pay a valid address, carry the block height and no fee.

func getInt(d map[string]any, key string, def int64, required bool) (int64, error) {
	v, ok := d[key]
	if !ok {
		if required {
			return 0, fmt.Errorf("missing %s", key)
		}
		return def, nil
	}
	i, ok := pyjson.AsInt(v)
	if !ok {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return i, nil
}

func getStr(d map[string]any, key, def string, required bool) (string, error) {
	v, ok := d[key]
	if !ok {
		if required {
			return "", fmt.Errorf("missing %s", key)
		}
		return def, nil
	}
	s, ok := pyjson.AsString(v)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func normSymbol(d map[string]any) (string, error) {
	sym, err := getStr(d, "symbol", "", false)
	if err != nil {
		return "", err
	}
	if !isASCII(sym) || len(sym) > params.MaxSymbolLen {
		return "", errors.New("invalid symbol")
	}
	return strings.ToUpper(sym), nil
}

func validAddr(d map[string]any, key string) (string, error) {
	a, err := getStr(d, key, "", false)
	if err != nil || !crypto.AddressIsValid(a) {
		return "", fmt.Errorf("invalid %s", key)
	}
	return a, nil
}

// ctx carries what rules need beyond the state.
type ctx struct {
	height   int64                     // height of the block the tx goes into
	beaconAt func(height int64) string // beacon of a main-chain block below height
	skipSig  bool                      // signature already verified (mempool cache)
}

// checkTx validates a non-coinbase transaction against the state.
func (s *State) checkTx(tx *core.Transaction, c ctx) error {
	if s.seen(tx.Hash()) {
		return errors.New("duplicate transaction")
	}
	if tx.TxType == core.TxCoinbase {
		return errors.New("coinbase not allowed here")
	}
	if !crypto.AddressIsValid(tx.Sender) {
		return errors.New("invalid sender address")
	}
	if !c.skipSig && !tx.VerifySignature() {
		return errors.New("invalid signature")
	}
	if tx.Fee < 0 {
		return errors.New("fee cannot be negative")
	}
	native := s.balance(tx.Sender, params.NativeToken)
	if native < tx.Fee {
		return errors.New("insufficient balance for fee")
	}
	d := tx.Data

	switch tx.TxType {
	case core.TxTransfer:
		token, err := getStr(d, "token", params.NativeToken, false)
		if err != nil {
			return err
		}
		amount, err := getInt(d, "amount", 0, false)
		if err != nil {
			return err
		}
		if _, err := validAddr(d, "recipient"); err != nil {
			return errors.New("invalid recipient")
		}
		if amount <= 0 {
			return errors.New("amount must be positive")
		}
		if isNative(token) {
			need, err := addChecked(amount, tx.Fee)
			if err != nil || native < need {
				return errors.New("insufficient balance")
			}
		} else if s.balance(tx.Sender, token) < amount {
			return errors.New("insufficient token balance")
		}

	case core.TxTokenCreate:
		sym, err := normSymbol(d)
		if err != nil {
			return err
		}
		if sym == "" || sym == params.NativeToken {
			return errors.New("invalid symbol")
		}
		if _, ok := s.tokens[sym]; ok {
			return errors.New("token already exists")
		}
		supply, err := getInt(d, "total_supply", 0, false)
		if err != nil {
			return err
		}
		if supply <= 0 {
			return errors.New("total_supply must be positive")
		}
		if _, err := getStr(d, "name", "", true); err != nil {
			return err
		}
		dec, err := getInt(d, "decimals", 8, false)
		if err != nil {
			return err
		}
		if dec < 0 || dec > 36 {
			return errors.New("decimals out of range")
		}

	case core.TxTokenMint:
		sym, err := normSymbol(d)
		if err != nil {
			return err
		}
		t, ok := s.tokens[sym]
		if !ok {
			return errors.New("token does not exist")
		}
		if t.Creator != tx.Sender {
			return errors.New("only creator can mint")
		}
		amount, err := getInt(d, "amount", 0, false)
		if err != nil {
			return err
		}
		if amount <= 0 {
			return errors.New("amount must be positive")
		}
		if _, err := addChecked(t.TotalSupply, amount); err != nil {
			return errors.New("total_supply overflow")
		}
		if _, err := validAddr(d, "recipient"); err != nil {
			return errors.New("invalid recipient")
		}

	case core.TxNFTCreate:
		cid, err := getStr(d, "collection_id", "", false)
		if err != nil {
			return err
		}
		if cid == "" {
			return errors.New("collection_id missing or already exists")
		}
		if _, ok := s.collections[cid]; ok {
			return errors.New("collection_id missing or already exists")
		}
		if _, err := getStr(d, "name", "", true); err != nil {
			return err
		}
		if _, err := getStr(d, "description", "", false); err != nil {
			return err
		}

	case core.TxNFTMint, core.TxNFTRandomMint:
		cid, err := getStr(d, "collection_id", "", false)
		if err != nil {
			return err
		}
		tid, err := getStr(d, "token_id", "", false)
		if err != nil {
			return err
		}
		col, ok := s.collections[cid]
		if !ok {
			return errors.New("collection does not exist")
		}
		if col.Creator != tx.Sender {
			return errors.New("only collection creator can mint")
		}
		if tid == "" {
			return errors.New("token_id missing")
		}
		if _, ok := s.nfts[nftKey{cid, tid}]; ok {
			return errors.New("NFT token_id already minted")
		}
		if _, err := validAddr(d, "recipient"); err != nil {
			return errors.New("invalid recipient")
		}
		if _, err := getStr(d, "metadata_uri", "", false); err != nil {
			return err
		}
		if tx.TxType == core.TxNFTRandomMint {
			// The minter no longer chooses the entropy source: traits come
			// from the next block's beacon, unknown when the tx is signed.
			if _, ok := d["entropy_height"]; ok {
				return errors.New("entropy_height is not allowed: traits come from the next block's beacon")
			}
		}

	case core.TxNFTTransfer:
		cid, err1 := getStr(d, "collection_id", "", false)
		tid, err2 := getStr(d, "token_id", "", false)
		if err := errors.Join(err1, err2); err != nil {
			return err
		}
		n, ok := s.nfts[nftKey{cid, tid}]
		if !ok {
			return errors.New("NFT does not exist")
		}
		if n.Owner != tx.Sender {
			return errors.New("sender does not own this NFT")
		}
		if _, err := validAddr(d, "recipient"); err != nil {
			return errors.New("invalid recipient")
		}

	case core.TxLotteryCreate:
		lid, err := getStr(d, "lottery_id", "", false)
		if err != nil {
			return err
		}
		if lid == "" {
			return errors.New("lottery_id missing or already exists")
		}
		if _, ok := s.lotteries[lid]; ok {
			return errors.New("lottery_id missing or already exists")
		}
		prize, err := getInt(d, "prize_amount", 0, true)
		if err != nil {
			return err
		}
		if prize <= 0 {
			return errors.New("prize_amount must be positive")
		}
		price, err := getInt(d, "ticket_price", 0, true)
		if err != nil {
			return err
		}
		if price < 0 {
			return errors.New("ticket_price cannot be negative")
		}
		draw, err := getInt(d, "draw_height", 0, true)
		if err != nil {
			return err
		}
		if draw < c.height+1 {
			return errors.New("draw_height must be at least 2 blocks in future")
		}
		k, err := getInt(d, "draw_blocks", 1, false)
		if err != nil {
			return err
		}
		if k < 1 || k > consensus.MaxDrawBlocks {
			return fmt.Errorf("draw_blocks must be 1-%d", consensus.MaxDrawBlocks)
		}
		token, err := getStr(d, "token", "", true)
		if err != nil {
			return err
		}
		if isNative(token) {
			need, err := addChecked(prize, tx.Fee)
			if err != nil || native < need {
				return errors.New("insufficient balance for prize + fee")
			}
		} else {
			if price != 0 {
				return errors.New("token lotteries must have ticket_price 0")
			}
			if s.balance(tx.Sender, token) < prize {
				return errors.New("insufficient token balance for prize")
			}
		}

	case core.TxLotteryEnter:
		lid, err := getStr(d, "lottery_id", "", false)
		if err != nil {
			return err
		}
		lot, ok := s.lotteries[lid]
		if !ok {
			return errors.New("lottery does not exist")
		}
		if lot.Drawn {
			return errors.New("lottery already drawn")
		}
		if c.height >= lot.DrawHeight {
			return errors.New("lottery entry closed (draw block reached)")
		}
		for _, e := range lot.Entrants {
			if e == tx.Sender {
				return errors.New("address already entered")
			}
		}
		need, err := addChecked(lot.TicketPrice, tx.Fee)
		if err != nil || native < need {
			return errors.New("insufficient balance for ticket + fee")
		}
		if _, err := addChecked(lot.EntryPool, lot.TicketPrice); err != nil {
			return errors.New("entry pool overflow")
		}

	case core.TxLotteryDraw:
		lid, err := getStr(d, "lottery_id", "", false)
		if err != nil {
			return err
		}
		lot, ok := s.lotteries[lid]
		if !ok {
			return errors.New("lottery does not exist")
		}
		if lot.Drawn {
			return errors.New("lottery already drawn")
		}
		if last := lot.DrawHeight + lot.DrawBlocks - 1; c.height <= last {
			return fmt.Errorf("draw allowed only after block %d", last)
		}
		if len(lot.Entrants) == 0 {
			return errors.New("no entrants - lottery cannot draw")
		}
		if _, err := addChecked(lot.PrizeAmount, lot.EntryPool); err != nil {
			return errors.New("payout overflow")
		}

	default:
		return fmt.Errorf("unknown tx type: %s", tx.TxType)
	}
	return nil
}

// applyTx mutates state for a transaction that passed checkTx (or that is
// being replayed from an already-validated block).
func (s *State) applyTx(tx *core.Transaction, c ctx) error {
	d := tx.Data
	h := c.height
	addrs := []string{tx.Sender}

	if tx.TxType == core.TxCoinbase {
		recipient, _ := getStr(d, "recipient", "", false)
		amount, _ := getInt(d, "amount", 0, false)
		if err := s.addBalance(recipient, params.NativeToken, amount); err != nil {
			return err
		}
		s.markSeen(tx.Hash())
		s.recordHistory(tx, h, []string{recipient})
		return nil
	}

	if tx.Fee > 0 {
		if err := s.addBalance(tx.Sender, params.NativeToken, -tx.Fee); err != nil {
			return err
		}
	}

	switch tx.TxType {
	case core.TxTransfer:
		token, _ := getStr(d, "token", params.NativeToken, false)
		amount, _ := getInt(d, "amount", 0, false)
		recipient, _ := getStr(d, "recipient", "", false)
		if err := s.addBalance(tx.Sender, token, -amount); err != nil {
			return err
		}
		if err := s.addBalance(recipient, token, amount); err != nil {
			return err
		}
		addrs = append(addrs, recipient)

	case core.TxTokenCreate:
		sym, _ := normSymbol(d)
		name, _ := getStr(d, "name", "", true)
		supply, _ := getInt(d, "total_supply", 0, false)
		dec, _ := getInt(d, "decimals", 8, false)
		s.setToken(sym, &Token{Name: name, TotalSupply: supply, Decimals: dec, Creator: tx.Sender})
		if err := s.addBalance(tx.Sender, sym, supply); err != nil {
			return err
		}

	case core.TxTokenMint:
		sym, _ := normSymbol(d)
		amount, _ := getInt(d, "amount", 0, false)
		recipient, _ := getStr(d, "recipient", "", false)
		t := *s.tokens[sym]
		var err error
		if t.TotalSupply, err = addChecked(t.TotalSupply, amount); err != nil {
			return err
		}
		s.setToken(sym, &t)
		if err := s.addBalance(recipient, sym, amount); err != nil {
			return err
		}
		addrs = append(addrs, recipient)

	case core.TxNFTCreate:
		cid, _ := getStr(d, "collection_id", "", false)
		name, _ := getStr(d, "name", "", true)
		desc, _ := getStr(d, "description", "", false)
		s.setCollection(cid, &Collection{Name: name, Description: desc, Creator: tx.Sender, CreatedAtHeight: h})

	case core.TxNFTMint:
		cid, _ := getStr(d, "collection_id", "", false)
		tid, _ := getStr(d, "token_id", "", false)
		recipient, _ := getStr(d, "recipient", "", false)
		uri, _ := getStr(d, "metadata_uri", "", false)
		traits, ok := d["traits"]
		if !ok {
			traits = map[string]any{}
		}
		s.setNFT(nftKey{cid, tid}, &NFT{Owner: recipient, MetadataURI: uri, Traits: traits,
			MintedAtHeight: h, Creator: tx.Sender})
		addrs = append(addrs, recipient)

	case core.TxNFTRandomMint:
		cid, _ := getStr(d, "collection_id", "", false)
		tid, _ := getStr(d, "token_id", "", false)
		recipient, _ := getStr(d, "recipient", "", false)
		uri, _ := getStr(d, "metadata_uri", "", false)
		// Traits are revealed by the next block's beacon; until that block
		// exists they are unknown to everyone, the minter included.
		src := h + 1
		s.setNFT(nftKey{cid, tid}, &NFT{Owner: recipient, MetadataURI: uri,
			MintedAtHeight: h, Creator: tx.Sender, EntropySourceHeight: &src})
		addrs = append(addrs, recipient)

	case core.TxNFTTransfer:
		cid, _ := getStr(d, "collection_id", "", false)
		tid, _ := getStr(d, "token_id", "", false)
		recipient, _ := getStr(d, "recipient", "", false)
		n := *s.nfts[nftKey{cid, tid}]
		n.Owner = recipient
		s.setNFT(nftKey{cid, tid}, &n)
		addrs = append(addrs, recipient)

	case core.TxLotteryCreate:
		lid, _ := getStr(d, "lottery_id", "", false)
		prize, _ := getInt(d, "prize_amount", 0, true)
		price, _ := getInt(d, "ticket_price", 0, true)
		draw, _ := getInt(d, "draw_height", 0, true)
		k, _ := getInt(d, "draw_blocks", 1, false)
		token, _ := getStr(d, "token", "", true)
		if err := s.addBalance(tx.Sender, token, -prize); err != nil {
			return err
		}
		s.setLottery(lid, &Lottery{Organizer: tx.Sender, PrizeAmount: prize, TicketPrice: price,
			DrawHeight: draw, DrawBlocks: k, Token: token, Entrants: []string{}, CreatedAtHeight: h})

	case core.TxLotteryEnter:
		lid, _ := getStr(d, "lottery_id", "", false)
		lot := s.lotteries[lid].clone()
		if err := s.addBalance(tx.Sender, params.NativeToken, -lot.TicketPrice); err != nil {
			return err
		}
		lot.EntryPool += lot.TicketPrice
		lot.Entrants = append(lot.Entrants, tx.Sender)
		s.setLottery(lid, lot)

	case core.TxLotteryDraw:
		lid, _ := getStr(d, "lottery_id", "", false)
		lot := s.lotteries[lid].clone()
		beacons := make([]string, 0, lot.DrawBlocks)
		for b := lot.DrawHeight; b < lot.DrawHeight+lot.DrawBlocks; b++ {
			beacons = append(beacons, c.beaconAt(b))
		}
		seed := consensus.AggregateBeacons(beacons)
		winner := consensus.PickWinner(seed, lid, lot.Entrants)
		if err := s.addBalance(winner, lot.Token, lot.PrizeAmount+lot.EntryPool); err != nil {
			return err
		}
		lot.Drawn, lot.Winner, lot.EntropyUsed, lot.DrawnAtHeight = true, &winner, &seed, &h
		s.setLottery(lid, lot)
		addrs = append(addrs, winner)
	}

	s.markSeen(tx.Hash())
	s.recordHistory(tx, h, addrs)
	return nil
}

// checkCoinbase validates a block's coinbase.
func checkCoinbase(cb *core.Transaction, height, totalFees int64) error {
	if cb.Fee != 0 {
		return errors.New("coinbase fee must be 0")
	}
	recipient, err := getStr(cb.Data, "recipient", "", false)
	if err != nil || !crypto.AddressIsValid(recipient) {
		return errors.New("coinbase recipient invalid")
	}
	amount, err := getInt(cb.Data, "amount", 0, false)
	if err != nil {
		return err
	}
	expected, err := addChecked(consensus.BlockReward(height), totalFees)
	if err != nil || amount != expected {
		return errors.New("coinbase amount mismatch")
	}
	ch, err := getInt(cb.Data, "height", -1, true)
	if err != nil || ch != height {
		return errors.New("coinbase height mismatch")
	}
	return nil
}
