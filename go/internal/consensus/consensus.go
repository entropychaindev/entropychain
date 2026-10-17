// Package consensus implements Proof-of-Entropy (consensus.py) and the
// deterministic randomness helpers from blockchain.py.
package consensus

import (
	"encoding/binary"
	"encoding/hex"
	"math/big"

	"golang.org/x/crypto/argon2"

	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/params"
)

// PowHash is Argon2id(password = header || LE64(nonce),
// salt = SHA256d(header)[:16]).
func PowHash(header []byte, nonce uint64) []byte {
	payload := make([]byte, len(header)+8)
	copy(payload, header)
	binary.LittleEndian.PutUint64(payload[len(header):], nonce)
	salt := crypto.Sha256d(header)[:16]
	return argon2.IDKey(payload, salt, params.ArgonTimeCost, params.ArgonMemoryKB,
		params.ArgonParallelism, params.ArgonHashLen)
}

var two256 = new(big.Int).Lsh(big.NewInt(1), 256)

// Target returns 2^256 / max(difficulty, 1).
func Target(difficulty uint64) *big.Int {
	if difficulty == 0 {
		difficulty = 1
	}
	return new(big.Int).Div(two256, new(big.Int).SetUint64(difficulty))
}

// HashMeetsTarget reports whether big-endian h < target.
func HashMeetsTarget(h []byte, target *big.Int) bool {
	return new(big.Int).SetBytes(h).Cmp(target) < 0
}

// HashMeetsDifficulty reports whether h is below 2^256 / difficulty.
func HashMeetsDifficulty(h []byte, difficulty uint64) bool {
	return HashMeetsTarget(h, Target(difficulty))
}

// AdjustDifficulty retargets so the next window averages TargetBlockTime,
// clamped to a 4x change either way.
func AdjustDifficulty(prev uint64, actualWindowSeconds int64) uint64 {
	expected := int64(params.TargetBlockTime * params.DifficultyWindow)
	actual := max(actualWindowSeconds, 1)

	p := new(big.Int).SetUint64(prev)
	n := new(big.Int).Mul(p, big.NewInt(expected))
	n.Quo(n, big.NewInt(actual))

	lo := new(big.Int).Quo(p, big.NewInt(4))
	hi := new(big.Int).Mul(p, big.NewInt(4))
	if n.Cmp(lo) < 0 {
		n = lo
	}
	if n.Cmp(hi) > 0 {
		n = hi
	}
	if n.Sign() < 1 {
		return 1
	}
	if !n.IsUint64() {
		return ^uint64(0)
	}
	return n.Uint64()
}

// DeriveEntropySeed returns SHA256d(prevHash || minerNonce).
func DeriveEntropySeed(prevHash, minerNonce []byte) []byte {
	return crypto.Sha256d(append(append([]byte{}, prevHash...), minerNonce...))
}

// Beacon returns a block's randomness (beacon v2): SHA256d("entropy" || block
// hash). The block hash covers the proof-of-work nonce, so no one can know a
// block's beacon before the block is solved, and a miner can bias it only by
// discarding solved blocks. (The header's entropy_seed, by contrast, depends
// only on the parent hash and a miner-chosen nonce, so a miner could pick it
// for free; it remains in the header for format compatibility only.)
func Beacon(blockHashHex string) string {
	h, _ := hex.DecodeString(blockHashHex)
	return hex.EncodeToString(crypto.Sha256d(append([]byte("entropy"), h...)))
}

// AggregateBeacons combines several consecutive beacons into one value, so
// biasing the result requires controlling every block involved. A single
// beacon is returned unchanged.
func AggregateBeacons(beacons []string) string {
	if len(beacons) == 1 {
		return beacons[0]
	}
	var buf []byte
	for _, b := range beacons {
		raw, _ := hex.DecodeString(b)
		buf = append(buf, raw...)
	}
	return hex.EncodeToString(crypto.Sha256d(buf))
}

// MaxDrawBlocks bounds how many beacons a lottery may aggregate.
const MaxDrawBlocks = 16

// BlockReward returns the coinbase subsidy at height.
func BlockReward(height int64) int64 {
	halvings := height / params.HalvingInterval
	if halvings >= 64 {
		return 0
	}
	return params.InitialReward >> halvings
}

// PickWinner selects a lottery winner from a block's entropy seed.
func PickWinner(entropySeedHex, lotteryID string, entrants []string) string {
	seed, _ := hex.DecodeString(entropySeedHex)
	h := crypto.Sha256(append(seed, lotteryID...))
	idx := new(big.Int).Mod(new(big.Int).SetBytes(h), big.NewInt(int64(len(entrants))))
	return entrants[idx.Int64()]
}

// RandomTraits are the traits derived for nft_random_mint.
type RandomTraits struct {
	Rarity              int    `json:"rarity"`
	Power               int    `json:"power"`
	Element             string `json:"element"`
	ColorSeed           string `json:"color_seed"`
	IsLegendary         bool   `json:"is_legendary"`
	EntropySourceHeight int64  `json:"entropy_source_height"`
}

var elements = []string{"fire", "water", "earth", "air", "void"}

// DeriveRandomTraits mirrors derive_random_traits in blockchain.py.
func DeriveRandomTraits(entropySeedHex, collectionID, tokenID string, sourceHeight int64) RandomTraits {
	seed, _ := hex.DecodeString(entropySeedHex)
	buf := append(append(seed, collectionID...), tokenID...)
	h := crypto.Sha256(buf)
	return RandomTraits{
		Rarity:              int(h[0]),
		Power:               int(h[1]),
		Element:             elements[int(h[2])%5],
		ColorSeed:           hex.EncodeToString(h[3:6]),
		IsLegendary:         h[6] < 8,
		EntropySourceHeight: sourceHeight,
	}
}

// IssuedSupply returns the total ENTRO created by block rewards up to and
// including height (fees only move existing coins).
func IssuedSupply(height int64) int64 {
	var total int64
	for h := int64(1); h <= height; {
		halvings := h / params.HalvingInterval
		if halvings >= 64 {
			break
		}
		end := min((halvings+1)*params.HalvingInterval-1, height)
		total += (end - h + 1) * (params.InitialReward >> halvings)
		h = end + 1
	}
	return total
}
