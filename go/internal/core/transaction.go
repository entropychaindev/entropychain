// Package core defines transactions and blocks (transactions.py, block.py)
// with serialization that hashes identically to the Python implementation.
package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
)

// Transaction types.
const (
	TxTransfer      = "transfer"
	TxTokenCreate   = "token_create"
	TxTokenMint     = "token_mint"
	TxNFTCreate     = "nft_create"
	TxNFTMint       = "nft_mint"
	TxNFTTransfer   = "nft_transfer"
	TxLotteryCreate = "lottery_create"
	TxLotteryEnter  = "lottery_enter"
	TxLotteryDraw   = "lottery_draw"
	TxNFTRandomMint = "nft_random_mint"
	TxCoinbase      = "coinbase"
)

// Transaction is a native typed transaction. Fields mirror the Python
// dataclass and serialize in the same order.
//
// A Transaction is immutable once built: the signing payload and hash are
// computed by finalize() at construction/decode time, so it can be shared
// between goroutines without locking.
type Transaction struct {
	TxType    string         `json:"tx_type"`
	Sender    string         `json:"sender"`
	Timestamp pyjson.Number  `json:"timestamp"`
	Nonce     string         `json:"nonce"`
	Data      map[string]any `json:"data"`
	PublicKey *string        `json:"public_key"`
	Signature *string        `json:"signature"`
	Fee       int64          `json:"fee"`

	payload []byte
	hash    string
}

// finalize computes the signing payload and hash. It fails if the data
// cannot be represented the way Python would hash it.
func (tx *Transaction) finalize() error {
	if tx.Data == nil {
		tx.Data = map[string]any{}
	}
	p, err := pyjson.Dumps(map[string]any{
		"tx_type":   tx.TxType,
		"sender":    tx.Sender,
		"timestamp": tx.Timestamp,
		"nonce":     tx.Nonce,
		"data":      tx.Data,
		"fee":       tx.Fee,
	})
	if err != nil {
		return err
	}
	tx.payload = p
	tx.hash = hex.EncodeToString(crypto.Sha256d(p))
	return nil
}

// SigningPayload returns the bytes that are signed and hashed.
func (tx *Transaction) SigningPayload() []byte { return tx.payload }

// Hash returns the hex transaction hash.
func (tx *Transaction) Hash() string { return tx.hash }

// SignWith signs the transaction. The signature does not affect the hash.
func (tx *Transaction) SignWith(priv, pub []byte) error {
	sig, err := crypto.Sign(priv, tx.payload)
	if err != nil {
		return err
	}
	pk, s := hex.EncodeToString(pub), hex.EncodeToString(sig)
	tx.PublicKey, tx.Signature = &pk, &s
	return nil
}

// VerifySignature checks that the pubkey matches the sender address and
// the signature is valid. Coinbase transactions are unsigned.
func (tx *Transaction) VerifySignature() bool {
	if tx.TxType == TxCoinbase {
		return true
	}
	if tx.PublicKey == nil || tx.Signature == nil || *tx.PublicKey == "" || *tx.Signature == "" {
		return false
	}
	pub, err := hex.DecodeString(*tx.PublicKey)
	if err != nil {
		return false
	}
	sig, err := hex.DecodeString(*tx.Signature)
	if err != nil {
		return false
	}
	if crypto.PubkeyToAddress(pub) != tx.Sender {
		return false
	}
	return crypto.Verify(pub, sig, tx.payload)
}

// UnmarshalJSON decodes strictly, the way Transaction(**d) does in Python:
// unknown fields are rejected and numbers keep their int/float identity.
// Unlike Python, tx_type, sender, timestamp and nonce are required, since a
// missing timestamp or nonce would be filled with a random local value and
// make the hash non-deterministic.
func (tx *Transaction) UnmarshalJSON(b []byte) error {
	var w struct {
		TxType    *string         `json:"tx_type"`
		Sender    *string         `json:"sender"`
		Timestamp *pyjson.Number  `json:"timestamp"`
		Nonce     *string         `json:"nonce"`
		Data      json.RawMessage `json:"data"`
		PublicKey *string         `json:"public_key"`
		Signature *string         `json:"signature"`
		Fee       *json.Number    `json:"fee"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return fmt.Errorf("transaction: %w", err)
	}
	if w.TxType == nil || w.Sender == nil || w.Timestamp == nil || w.Nonce == nil {
		return errors.New("transaction: missing tx_type, sender, timestamp or nonce")
	}
	t := Transaction{
		TxType:    *w.TxType,
		Sender:    *w.Sender,
		Timestamp: *w.Timestamp,
		Nonce:     *w.Nonce,
		PublicKey: w.PublicKey,
		Signature: w.Signature,
		Data:      map[string]any{},
	}
	if len(w.Data) > 0 {
		var d any
		if err := pyjson.Decode(w.Data, &d); err != nil {
			return fmt.Errorf("transaction data: %w", err)
		}
		m, ok := d.(map[string]any)
		if !ok {
			return errors.New("transaction: data must be an object")
		}
		t.Data = m
	}
	if w.Fee != nil {
		fee, ok := pyjson.AsInt(*w.Fee)
		if !ok {
			return errors.New("transaction: fee must be an integer")
		}
		t.Fee = fee
	}
	if err := t.finalize(); err != nil {
		return fmt.Errorf("transaction: %w", err)
	}
	*tx = t
	return nil
}

// ---- Constructors (transactions.py make_*) ----

func randomNonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Now returns the current time as Python's time.time() would.
func Now() float64 {
	return float64(time.Now().UnixMicro()) / 1e6
}

// NewTx builds and finalizes an unsigned transaction.
func NewTx(txType, sender string, data map[string]any, fee int64) (*Transaction, error) {
	tx := &Transaction{
		TxType:    txType,
		Sender:    sender,
		Timestamp: pyjson.FloatNumber(Now()),
		Nonce:     randomNonce(),
		Data:      data,
		Fee:       fee,
	}
	if err := tx.finalize(); err != nil {
		return nil, err
	}
	return tx, nil
}

// NewCoinbase builds the block reward transaction.
func NewCoinbase(miner string, reward, height int64) *Transaction {
	tx, err := NewTx(TxCoinbase, "", map[string]any{
		"recipient": miner, "amount": reward, "height": height,
	}, 0)
	if err != nil {
		panic(err) // only plain strings and ints: cannot fail
	}
	return tx
}

func NewTransfer(sender, recipient string, amount int64, token string, fee int64) (*Transaction, error) {
	if token == "" {
		token = params.NativeToken
	}
	return NewTx(TxTransfer, sender, map[string]any{
		"recipient": recipient, "amount": amount, "token": token,
	}, fee)
}

func NewTokenCreate(sender, symbol, name string, totalSupply, decimals, fee int64) (*Transaction, error) {
	return NewTx(TxTokenCreate, sender, map[string]any{
		"symbol": strings.ToUpper(symbol), "name": name,
		"total_supply": totalSupply, "decimals": decimals,
	}, fee)
}

func NewTokenMint(sender, symbol, recipient string, amount, fee int64) (*Transaction, error) {
	return NewTx(TxTokenMint, sender, map[string]any{
		"symbol": strings.ToUpper(symbol), "recipient": recipient, "amount": amount,
	}, fee)
}

func NewNFTCreate(sender, collectionID, name, description string, fee int64) (*Transaction, error) {
	return NewTx(TxNFTCreate, sender, map[string]any{
		"collection_id": collectionID, "name": name, "description": description,
	}, fee)
}

func NewNFTMint(sender, collectionID, tokenID, recipient, metadataURI string, traits map[string]any, fee int64) (*Transaction, error) {
	if traits == nil {
		traits = map[string]any{}
	}
	return NewTx(TxNFTMint, sender, map[string]any{
		"collection_id": collectionID, "token_id": tokenID, "recipient": recipient,
		"metadata_uri": metadataURI, "traits": traits,
	}, fee)
}

func NewNFTTransfer(sender, collectionID, tokenID, recipient string, fee int64) (*Transaction, error) {
	return NewTx(TxNFTTransfer, sender, map[string]any{
		"collection_id": collectionID, "token_id": tokenID, "recipient": recipient,
	}, fee)
}

// NewLotteryCreate opens a lottery. drawBlocks > 1 aggregates that many
// consecutive beacons starting at drawHeight (it is omitted when 1).
func NewLotteryCreate(sender, lotteryID string, prize, ticketPrice, drawHeight, drawBlocks int64, token string, fee int64) (*Transaction, error) {
	if token == "" {
		token = params.NativeToken
	}
	data := map[string]any{
		"lottery_id": lotteryID, "prize_amount": prize, "ticket_price": ticketPrice,
		"draw_height": drawHeight, "token": token,
	}
	if drawBlocks > 1 {
		data["draw_blocks"] = drawBlocks
	}
	return NewTx(TxLotteryCreate, sender, data, fee)
}

func NewLotteryEnter(sender, lotteryID string, fee int64) (*Transaction, error) {
	return NewTx(TxLotteryEnter, sender, map[string]any{"lottery_id": lotteryID}, fee)
}

func NewLotteryDraw(sender, lotteryID string, fee int64) (*Transaction, error) {
	return NewTx(TxLotteryDraw, sender, map[string]any{"lottery_id": lotteryID}, fee)
}

// NewNFTRandomMint mints an NFT whose traits are revealed by the beacon of
// the block after the one that includes it.
func NewNFTRandomMint(sender, collectionID, tokenID, recipient, metadataURI string, fee int64) (*Transaction, error) {
	return NewTx(TxNFTRandomMint, sender, map[string]any{
		"collection_id": collectionID, "token_id": tokenID, "recipient": recipient,
		"metadata_uri": metadataURI,
	}, fee)
}

// GenesisTx is the memorial coinbase that fixes the network identity.
func GenesisTx() *Transaction {
	tx := &Transaction{
		TxType:    TxCoinbase,
		Sender:    "",
		Timestamp: pyjson.FloatNumber(params.GenesisTimestamp),
		Nonce:     "genesis",
		Data: map[string]any{
			"recipient":   "",
			"amount":      int64(0),
			"height":      int64(0),
			"message":     params.GenesisMessage,
			"network_tag": params.GenesisNetworkTag,
			"operator":    params.GenesisOperator,
		},
	}
	if err := tx.finalize(); err != nil {
		panic(err)
	}
	return tx
}

// FormatAmount renders integer units with 8 decimals, e.g. 150000000 -> "1.5".
func FormatAmount(units int64) string {
	sign := ""
	if units < 0 {
		sign, units = "-", -units
	}
	whole, frac := units/100_000_000, units%100_000_000
	if frac == 0 {
		return sign + strconv.FormatInt(whole, 10)
	}
	return sign + strings.TrimRight(fmt.Sprintf("%d.%08d", whole, frac), "0")
}

// ParseAmount converts "1.5" to integer units with the given decimals.
func ParseAmount(s string, decimals int) (int64, error) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > decimals {
		return 0, fmt.Errorf("too many decimal places (max %d)", decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	v, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	return v, nil
}
