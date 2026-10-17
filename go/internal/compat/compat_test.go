// Package compat checks the Go implementation against vectors produced by
// the Python reference code (testdata/gen_vectors.py).
package compat

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/entropychaindev/entropychain/go/internal/consensus"
	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
	"github.com/entropychaindev/entropychain/go/internal/pyjson"
	"github.com/entropychaindev/entropychain/go/internal/wallet"
)

type vectors struct {
	Floats []struct {
		Value string `json:"value"`
		JSON  string `json:"json"`
	} `json:"floats"`
	CanonicalJSON []struct {
		Input  string `json:"input"`
		Python string `json:"python"`
	} `json:"canonical_json"`
	Keys []struct {
		PrivateKey            string `json:"private_key"`
		PublicKey             string `json:"public_key"`
		Address               string `json:"address"`
		PublicKeyUncompressed string `json:"public_key_uncompressed"`
		AddressUncompressed   string `json:"address_uncompressed"`
	} `json:"keys"`
	Signatures []struct {
		PrivateKey string `json:"private_key"`
		PublicKey  string `json:"public_key"`
		Message    string `json:"message"`
		Signature  string `json:"signature"`
	} `json:"signatures"`
	InvalidAddresses []string `json:"invalid_addresses"`
	Transactions     []struct {
		JSON           string `json:"json"`
		SigningPayload string `json:"signing_payload"`
		Hash           string `json:"hash"`
		SignatureValid bool   `json:"signature_valid"`
	} `json:"transactions"`
	Merkle []struct {
		Hashes []string `json:"hashes"`
		Root   string   `json:"root"`
	} `json:"merkle"`
	Genesis struct {
		JSON   string `json:"json"`
		Header string `json:"header"`
		Hash   string `json:"hash"`
		TxHash string `json:"tx_hash"`
	} `json:"genesis"`
	Pow []struct {
		Header    string `json:"header"`
		Nonce     string `json:"nonce"`
		Hash      string `json:"hash"`
		Meets1000 bool   `json:"meets_1000"`
		Meets2    bool   `json:"meets_2"`
	} `json:"pow"`
	Block struct {
		JSON   string `json:"json"`
		Header string `json:"header"`
		Hash   string `json:"hash"`
	} `json:"block"`
	AdjustDifficulty []struct {
		Prev   uint64 `json:"prev"`
		Actual int64  `json:"actual"`
		Result uint64 `json:"result"`
	} `json:"adjust_difficulty"`
	BlockReward []struct {
		Height int64 `json:"height"`
		Reward int64 `json:"reward"`
	} `json:"block_reward"`
	PickWinner []struct {
		Seed      string   `json:"seed"`
		LotteryID string   `json:"lottery_id"`
		Entrants  []string `json:"entrants"`
		Winner    string   `json:"winner"`
	} `json:"pick_winner"`
	RandomTraits []struct {
		Seed         string          `json:"seed"`
		CollectionID string          `json:"collection_id"`
		TokenID      string          `json:"token_id"`
		Traits       json.RawMessage `json:"traits"`
	} `json:"random_traits"`
	EncryptedWallet struct {
		File       string `json:"file"`
		Passphrase string `json:"passphrase"`
		Address    string `json:"address"`
		PrivateKey string `json:"private_key"`
	} `json:"encrypted_wallet"`
}

func load(t *testing.T) *vectors {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors.json"))
	if err != nil {
		t.Fatalf("read vectors (run python testdata/gen_vectors.py): %v", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return &v
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFloatRepr(t *testing.T) {
	for _, c := range load(t).Floats {
		f, err := strconv.ParseFloat(c.Value, 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := pyjson.FloatRepr(f); got != c.JSON {
			t.Errorf("FloatRepr(%s) = %s, want %s", c.Value, got, c.JSON)
		}
	}
}

func TestCanonicalJSON(t *testing.T) {
	for _, c := range load(t).CanonicalJSON {
		var v any
		if err := pyjson.Decode([]byte(c.Input), &v); err != nil {
			t.Fatal(err)
		}
		got, err := pyjson.Dumps(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.Python {
			t.Errorf("Dumps(%s)\n got %s\nwant %s", c.Input, got, c.Python)
		}
	}
}

func TestKeysAndAddresses(t *testing.T) {
	v := load(t)
	for _, k := range v.Keys {
		pub, err := crypto.PrivToPub(unhex(t, k.PrivateKey))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(pub) != k.PublicKey {
			t.Errorf("pubkey mismatch for %s", k.Address)
		}
		if a := crypto.PubkeyToAddress(pub); a != k.Address {
			t.Errorf("address %s, want %s", a, k.Address)
		}
		if a := crypto.PubkeyToAddress(unhex(t, k.PublicKeyUncompressed)); a != k.AddressUncompressed {
			t.Errorf("uncompressed address %s, want %s", a, k.AddressUncompressed)
		}
		if !crypto.AddressIsValid(k.Address) {
			t.Errorf("valid address rejected: %s", k.Address)
		}
	}
	for _, a := range v.InvalidAddresses {
		if crypto.AddressIsValid(a) {
			t.Errorf("invalid address accepted: %q", a)
		}
	}
}

func TestSignatures(t *testing.T) {
	n := secp256k1.S256().N
	for _, s := range load(t).Signatures {
		priv, pub, msg, pySig := unhex(t, s.PrivateKey), unhex(t, s.PublicKey), unhex(t, s.Message), unhex(t, s.Signature)
		if !crypto.Verify(pub, pySig, msg) {
			t.Errorf("Python signature rejected (msg %s)", s.Message)
		}
		goSig, err := crypto.Sign(priv, msg)
		if err != nil {
			t.Fatal(err)
		}
		if !crypto.Verify(pub, goSig, msg) {
			t.Errorf("Go signature failed own verify")
		}
		// Both use RFC 6979 with SHA-256, so r must match and s must be
		// equal or the low-s complement.
		if hex.EncodeToString(goSig[:32]) != hex.EncodeToString(pySig[:32]) {
			t.Errorf("RFC6979 r differs from Python")
		}
		goS := new(secp256k1.ModNScalar)
		goS.SetByteSlice(goSig[32:])
		pyS := new(secp256k1.ModNScalar)
		pyS.SetByteSlice(pySig[32:])
		if !goS.Equals(pyS) && !goS.Equals(new(secp256k1.ModNScalar).NegateVal(pyS)) {
			t.Errorf("s is neither Python's s nor n-s (n=%x)", n)
		}
		tampered := append([]byte{}, msg...)
		tampered = append(tampered, 'x')
		if crypto.Verify(pub, pySig, tampered) {
			t.Errorf("signature verified for tampered message")
		}
	}
}

func TestTransactions(t *testing.T) {
	for i, c := range load(t).Transactions {
		var tx core.Transaction
		if err := json.Unmarshal([]byte(c.JSON), &tx); err != nil {
			t.Fatalf("tx %d decode: %v", i, err)
		}
		if string(tx.SigningPayload()) != c.SigningPayload {
			t.Errorf("tx %d payload\n got %s\nwant %s", i, tx.SigningPayload(), c.SigningPayload)
		}
		if tx.Hash() != c.Hash {
			t.Errorf("tx %d hash %s, want %s", i, tx.Hash(), c.Hash)
		}
		if tx.VerifySignature() != c.SignatureValid {
			t.Errorf("tx %d signature valid=%v, want %v", i, !c.SignatureValid, c.SignatureValid)
		}
		// Re-encoding and decoding must preserve the hash (wire round trip).
		enc, err := json.Marshal(&tx)
		if err != nil {
			t.Fatal(err)
		}
		var again core.Transaction
		if err := json.Unmarshal(enc, &again); err != nil || again.Hash() != c.Hash {
			t.Errorf("tx %d round trip changed hash (%v)", i, err)
		}
	}
}

func TestMerkle(t *testing.T) {
	for _, c := range load(t).Merkle {
		if got := core.MerkleRoot(c.Hashes); got != c.Root {
			t.Errorf("merkle(%d) = %s, want %s", len(c.Hashes), got, c.Root)
		}
	}
}

func TestGenesis(t *testing.T) {
	v := load(t)
	g := core.GenesisBlock()
	if g.Transactions[0].Hash() != v.Genesis.TxHash {
		t.Errorf("genesis tx hash %s, want %s", g.Transactions[0].Hash(), v.Genesis.TxHash)
	}
	if hex.EncodeToString(g.HeaderBytes()) != v.Genesis.Header {
		t.Errorf("genesis header mismatch")
	}
	if g.Hash() != v.Genesis.Hash {
		t.Errorf("genesis hash %s, want %s", g.Hash(), v.Genesis.Hash)
	}
	var decoded core.Block
	if err := json.Unmarshal([]byte(v.Genesis.JSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Hash() != v.Genesis.Hash {
		t.Errorf("decoded genesis hash mismatch")
	}
}

func TestBlock(t *testing.T) {
	v := load(t)
	var b core.Block
	if err := json.Unmarshal([]byte(v.Block.JSON), &b); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(b.HeaderBytes()) != v.Block.Header {
		t.Errorf("header mismatch")
	}
	if b.Hash() != v.Block.Hash {
		t.Errorf("block hash %s, want %s", b.Hash(), v.Block.Hash)
	}
	if core.MerkleRoot(b.TxHashes()) != b.MerkleRootHex {
		t.Errorf("merkle root mismatch")
	}
	enc, _ := json.Marshal(&b)
	var again core.Block
	if err := json.Unmarshal(enc, &again); err != nil || again.Hash() != v.Block.Hash {
		t.Errorf("block round trip changed hash (%v)", err)
	}
}

func TestPow(t *testing.T) {
	for _, c := range load(t).Pow {
		nonce, _ := strconv.ParseUint(c.Nonce, 10, 64)
		h := consensus.PowHash(unhex(t, c.Header), nonce)
		if hex.EncodeToString(h) != c.Hash {
			t.Errorf("pow hash %x, want %s", h, c.Hash)
		}
		if consensus.HashMeetsDifficulty(h, 1000) != c.Meets1000 || consensus.HashMeetsDifficulty(h, 2) != c.Meets2 {
			t.Errorf("difficulty check mismatch for %s", c.Hash)
		}
	}
}

func TestDifficultyRewardRandomness(t *testing.T) {
	v := load(t)
	for _, c := range v.AdjustDifficulty {
		if got := consensus.AdjustDifficulty(c.Prev, c.Actual); got != c.Result {
			t.Errorf("AdjustDifficulty(%d, %d) = %d, want %d", c.Prev, c.Actual, got, c.Result)
		}
	}
	for _, c := range v.BlockReward {
		if got := consensus.BlockReward(c.Height); got != c.Reward {
			t.Errorf("BlockReward(%d) = %d, want %d", c.Height, got, c.Reward)
		}
	}
	for _, c := range v.PickWinner {
		if got := consensus.PickWinner(c.Seed, c.LotteryID, c.Entrants); got != c.Winner {
			t.Errorf("PickWinner(%s) = %s, want %s", c.LotteryID, got, c.Winner)
		}
	}
	for _, c := range v.RandomTraits {
		var want map[string]any
		_ = json.Unmarshal(c.Traits, &want)
		got := consensus.DeriveRandomTraits(c.Seed, c.CollectionID, c.TokenID, 0)
		if float64(got.Rarity) != want["rarity"] || float64(got.Power) != want["power"] ||
			got.Element != want["element"] || got.ColorSeed != want["color_seed"] ||
			got.IsLegendary != want["is_legendary"] {
			t.Errorf("traits for %s/%s = %+v, want %v", c.CollectionID, c.TokenID, got, want)
		}
	}
}

func TestEncryptedWallet(t *testing.T) {
	v := load(t).EncryptedWallet
	path := filepath.Join(t.TempDir(), "wallet.json")
	if err := os.WriteFile(path, []byte(v.File), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := wallet.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !w.IsEncrypted() || w.IsUnlocked() {
		t.Fatal("expected a locked encrypted wallet")
	}
	if addrs := w.Addresses(); len(addrs) != 1 || addrs[0] != v.Address {
		t.Fatalf("addresses visible while locked = %v", addrs)
	}
	if err := w.Unlock("wrong"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	if err := w.Unlock(v.Passphrase); err != nil {
		t.Fatal(err)
	}
	if k, _ := w.ExportPrivateKey(v.Address); k != v.PrivateKey {
		t.Fatal("decrypted key mismatch")
	}
	// Re-save with Go and read back.
	if _, err := w.CreateAddress("go"); err != nil {
		t.Fatal(err)
	}
	w2, _ := wallet.Open(path)
	if err := w2.Unlock(v.Passphrase); err != nil || len(w2.Addresses()) != 2 || w2.Addresses()[0] != v.Address {
		t.Fatalf("Go-saved wallet did not round trip: %v %v", err, w2.Addresses())
	}
}
