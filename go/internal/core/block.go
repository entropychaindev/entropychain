package core

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/params"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
)

// ZeroHash is 32 zero bytes in hex.
var ZeroHash = hex.EncodeToString(make([]byte, 32))

// MerkleRoot is the pairwise SHA256d merkle root of hex tx hashes; an odd
// layer duplicates its last element. An empty list gives ZeroHash.
func MerkleRoot(txHashes []string) string {
	if len(txHashes) == 0 {
		return ZeroHash
	}
	layer := make([][]byte, len(txHashes))
	for i, h := range txHashes {
		layer[i], _ = hex.DecodeString(h)
	}
	for len(layer) > 1 {
		if len(layer)%2 == 1 {
			layer = append(layer, layer[len(layer)-1])
		}
		next := make([][]byte, 0, len(layer)/2)
		for i := 0; i < len(layer); i += 2 {
			next = append(next, crypto.Sha256d(append(append([]byte{}, layer[i]...), layer[i+1]...)))
		}
		layer = next
	}
	return hex.EncodeToString(layer[0])
}

// Block mirrors block.py. Height is int64 in memory but serialized as a
// little-endian uint32 in the header, as Python's struct.pack("<I") does.
type Block struct {
	Height        int64
	PrevHash      string
	MerkleRootHex string
	Timestamp     float64
	Difficulty    uint64
	EntropySeed   string // 32 bytes hex
	MinerNonce    string // 16 bytes hex
	Nonce         uint64 // PoW search nonce
	Transactions  []*Transaction
}

type blockWire struct {
	Height        *int64         `json:"height"`
	PrevHash      *string        `json:"prev_hash"`
	MerkleRootHex *string        `json:"merkle_root_hex"`
	Timestamp     *pyjson.Number `json:"timestamp"`
	Difficulty    *uint64        `json:"difficulty"`
	EntropySeed   *string        `json:"entropy_seed"`
	MinerNonce    *string        `json:"miner_nonce"`
	Nonce         *uint64        `json:"nonce"`
	Transactions  []*Transaction `json:"transactions"`
}

// MarshalJSON emits the same field order as Python's asdict(), with the
// timestamp formatted as a Python float.
func (b *Block) MarshalJSON() ([]byte, error) {
	ts := pyjson.FloatNumber(b.Timestamp)
	txs := b.Transactions
	if txs == nil {
		txs = []*Transaction{}
	}
	return json.Marshal(blockWire{
		Height: &b.Height, PrevHash: &b.PrevHash, MerkleRootHex: &b.MerkleRootHex,
		Timestamp: &ts, Difficulty: &b.Difficulty, EntropySeed: &b.EntropySeed,
		MinerNonce: &b.MinerNonce, Nonce: &b.Nonce, Transactions: txs,
	})
}

// UnmarshalJSON decodes strictly (unknown fields rejected, all header
// fields required) and checks field formats.
func (b *Block) UnmarshalJSON(data []byte) error {
	var w blockWire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return fmt.Errorf("block: %w", err)
	}
	if w.Height == nil || w.PrevHash == nil || w.MerkleRootHex == nil || w.Timestamp == nil ||
		w.Difficulty == nil || w.EntropySeed == nil || w.MinerNonce == nil || w.Nonce == nil {
		return errors.New("block: missing header field")
	}
	ts, err := w.Timestamp.Float64()
	if err != nil || math.IsNaN(ts) || math.IsInf(ts, 0) {
		return errors.New("block: bad timestamp")
	}
	blk := Block{
		Height: *w.Height, PrevHash: *w.PrevHash, MerkleRootHex: *w.MerkleRootHex,
		Timestamp: ts, Difficulty: *w.Difficulty, EntropySeed: *w.EntropySeed,
		MinerNonce: *w.MinerNonce, Nonce: *w.Nonce, Transactions: w.Transactions,
	}
	if err := blk.CheckFormat(); err != nil {
		return err
	}
	*b = blk
	return nil
}

func isLowerHex(s string, nBytes int) bool {
	if len(s) != nBytes*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// CheckFormat validates field encodings. Python accepts any hex that
// bytes.fromhex accepts; the Go node requires canonical lowercase hex of
// the exact length Python's own miner produces.
func (b *Block) CheckFormat() error {
	switch {
	case b.Height < 0 || b.Height > math.MaxUint32:
		return errors.New("block: height out of range")
	case !isLowerHex(b.PrevHash, 32):
		return errors.New("block: bad prev_hash")
	case !isLowerHex(b.MerkleRootHex, 32):
		return errors.New("block: bad merkle_root_hex")
	case !isLowerHex(b.EntropySeed, 32):
		return errors.New("block: bad entropy_seed")
	case !isLowerHex(b.MinerNonce, 16):
		return errors.New("block: bad miner_nonce")
	}
	for _, tx := range b.Transactions {
		if tx == nil {
			return errors.New("block: null transaction")
		}
	}
	return nil
}

// HeaderBytes serializes the header for PoW (the PoW nonce is appended
// separately).
func (b *Block) HeaderBytes() []byte {
	h := make([]byte, 0, 4+32+32+8+8+32+16)
	h = binary.LittleEndian.AppendUint32(h, uint32(b.Height))
	if b.PrevHash == "" {
		h = append(h, make([]byte, 32)...)
	} else {
		ph, _ := hex.DecodeString(b.PrevHash)
		h = append(h, ph...)
	}
	mr, _ := hex.DecodeString(b.MerkleRootHex)
	h = append(h, mr...)
	h = binary.LittleEndian.AppendUint64(h, math.Float64bits(b.Timestamp))
	h = binary.LittleEndian.AppendUint64(h, b.Difficulty)
	es, _ := hex.DecodeString(b.EntropySeed)
	h = append(h, es...)
	mn, _ := hex.DecodeString(b.MinerNonce)
	h = append(h, mn...)
	return h
}

// HashWithHeader returns the block hash given precomputed header bytes.
func HashWithHeader(header []byte, nonce uint64) string {
	buf := binary.LittleEndian.AppendUint64(append([]byte{}, header...), nonce)
	return hex.EncodeToString(crypto.Sha256d(buf))
}

// Hash returns SHA256d(header || LE64(nonce)) in hex.
func (b *Block) Hash() string {
	return HashWithHeader(b.HeaderBytes(), b.Nonce)
}

// TxHashes returns the hashes of all transactions in order.
func (b *Block) TxHashes() []string {
	out := make([]string, len(b.Transactions))
	for i, tx := range b.Transactions {
		out[i] = tx.Hash()
	}
	return out
}

// GenesisBlock builds the deterministic genesis block from params.
func GenesisBlock() *Block {
	tx := GenesisTx()
	return &Block{
		Height:        0,
		PrevHash:      ZeroHash,
		MerkleRootHex: MerkleRoot([]string{tx.Hash()}),
		Timestamp:     params.GenesisTimestamp,
		Difficulty:    params.GenesisDifficulty,
		EntropySeed:   ZeroHash,
		MinerNonce:    hex.EncodeToString(make([]byte, 16)),
		Nonce:         0,
		Transactions:  []*Transaction{tx},
	}
}
