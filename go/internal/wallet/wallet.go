// Package wallet reads and writes wallet.json files compatible with
// wallet.py: either a plaintext {addr: {private_key, public_key, label}}
// map, or an encrypted envelope (PBKDF2-SHA256 -> AES-256-GCM).
package wallet

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/pbkdf2"

	"github.com/entropychaindev/entropychain/go/internal/core"
	"github.com/entropychaindev/entropychain/go/internal/crypto"
)

const (
	PBKDF2Iterations = 100_000
	keyLen           = 32
	saltLen          = 16
	nonceLen         = 12
)

// ErrLocked is returned when an operation needs the decrypted keys.
var ErrLocked = errors.New("wallet is locked")

// Key is one keypair entry.
type Key struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Label      string `json:"label"`
}

// Wallet holds keys in insertion order (the first address is the default
// mining address, as in node.py).
type Wallet struct {
	path       string
	encrypted  bool
	unlocked   bool
	order      []string
	keys       map[string]Key
	salt       []byte
	iterations int
	passphrase string
}

type envelope struct {
	Encrypted  bool     `json:"encrypted"`
	KDF        string   `json:"kdf"`
	Iterations int      `json:"iterations"`
	Salt       string   `json:"salt"`
	Nonce      string   `json:"nonce"`
	Ciphertext string   `json:"ciphertext"`
	Addresses  []string `json:"addresses"`
}

// Open loads path if it exists, or returns an empty plaintext wallet.
func Open(path string) (*Wallet, error) {
	w := &Wallet{path: path, unlocked: true, keys: map[string]Key{}, iterations: PBKDF2Iterations}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("unrecognized wallet format at %s: %w", path, err)
	}
	if raw, ok := probe["encrypted"]; ok && string(raw) == "true" {
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, err
		}
		w.encrypted, w.unlocked = true, false
		w.order = env.Addresses
		w.iterations = env.Iterations
		if w.iterations == 0 {
			w.iterations = PBKDF2Iterations
		}
		if w.salt, err = hex.DecodeString(env.Salt); err != nil {
			return nil, fmt.Errorf("bad wallet salt: %w", err)
		}
		return w, nil
	}
	if w.order, w.keys, err = decodeKeys(data); err != nil {
		return nil, err
	}
	return w, nil
}

// decodeKeys parses {addr: Key, ...} preserving key order.
func decodeKeys(data []byte) ([]string, map[string]Key, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("wallet: expected a JSON object")
	}
	var order []string
	keys := map[string]Key{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		addr := t.(string)
		var k Key
		if err := dec.Decode(&k); err != nil {
			return nil, nil, fmt.Errorf("wallet entry %s: %w", addr, err)
		}
		if _, dup := keys[addr]; !dup {
			order = append(order, addr)
		}
		keys[addr] = k
	}
	return order, keys, nil
}

func encodeKeys(order []string, keys map[string]Key, indent bool) []byte {
	var b bytes.Buffer
	b.WriteString("{")
	for i, a := range order {
		if i > 0 {
			b.WriteString(",")
		}
		ab, _ := json.Marshal(a)
		var kb []byte
		if indent {
			kb, _ = json.MarshalIndent(keys[a], "  ", "  ")
			b.WriteString("\n  ")
		} else {
			kb, _ = json.Marshal(keys[a])
		}
		b.Write(ab)
		b.WriteString(": ")
		b.Write(kb)
	}
	if indent && len(order) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}")
	return b.Bytes()
}

func deriveKey(passphrase string, salt []byte, iterations int) []byte {
	return pbkdf2.Key([]byte(passphrase), salt, iterations, keyLen, sha256.New)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Path returns the wallet file path.
func (w *Wallet) Path() string { return w.path }

// IsEncrypted reports whether the file is passphrase-encrypted.
func (w *Wallet) IsEncrypted() bool { return w.encrypted }

// IsUnlocked reports whether private keys are available.
func (w *Wallet) IsUnlocked() bool { return w.unlocked }

// Addresses returns all addresses (available even while locked).
func (w *Wallet) Addresses() []string { return append([]string(nil), w.order...) }

// Label returns an address's label ("" while locked).
func (w *Wallet) Label(addr string) string { return w.keys[addr].Label }

// Unlock decrypts an encrypted wallet.
func (w *Wallet) Unlock(passphrase string) error {
	if !w.encrypted {
		return nil
	}
	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	salt, err1 := hex.DecodeString(env.Salt)
	nonce, err2 := hex.DecodeString(env.Nonce)
	ct, err3 := hex.DecodeString(env.Ciphertext)
	if err := errors.Join(err1, err2, err3); err != nil {
		return fmt.Errorf("corrupt wallet envelope: %w", err)
	}
	iters := env.Iterations
	if iters == 0 {
		iters = PBKDF2Iterations
	}
	gcm, err := newGCM(deriveKey(passphrase, salt, iters))
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return errors.New("wrong passphrase")
	}
	order, keys, err := decodeKeys(plain)
	if err != nil {
		return err
	}
	w.order, w.keys = order, keys
	w.unlocked, w.passphrase, w.salt, w.iterations = true, passphrase, salt, iters
	return nil
}

// Lock forgets decrypted keys.
func (w *Wallet) Lock() {
	if w.encrypted {
		w.keys, w.unlocked, w.passphrase = map[string]Key{}, false, ""
	}
}

// Encrypt converts the wallet to the encrypted format (or changes the
// passphrase) and saves it.
func (w *Wallet) Encrypt(passphrase string) error {
	if !w.unlocked {
		return ErrLocked
	}
	w.salt = make([]byte, saltLen)
	if _, err := rand.Read(w.salt); err != nil {
		return err
	}
	w.encrypted, w.passphrase = true, passphrase
	return w.Save()
}

// Save writes the wallet in its current format with 0600 permissions.
func (w *Wallet) Save() error {
	var out []byte
	if w.encrypted {
		if !w.unlocked {
			return ErrLocked
		}
		nonce := make([]byte, nonceLen)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		gcm, err := newGCM(deriveKey(w.passphrase, w.salt, w.iterations))
		if err != nil {
			return err
		}
		ct := gcm.Seal(nil, nonce, encodeKeys(w.order, w.keys, false), nil)
		out, err = json.MarshalIndent(envelope{
			Encrypted: true, KDF: "pbkdf2-sha256", Iterations: w.iterations,
			Salt: hex.EncodeToString(w.salt), Nonce: hex.EncodeToString(nonce),
			Ciphertext: hex.EncodeToString(ct), Addresses: w.order,
		}, "", "  ")
		if err != nil {
			return err
		}
	} else {
		out = encodeKeys(w.order, w.keys, true)
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.path)
}

func (w *Wallet) add(priv, pub []byte, label string) (string, error) {
	if !w.unlocked {
		return "", ErrLocked
	}
	addr := crypto.PubkeyToAddress(pub)
	if _, ok := w.keys[addr]; !ok {
		w.order = append(w.order, addr)
	}
	w.keys[addr] = Key{PrivateKey: hex.EncodeToString(priv), PublicKey: hex.EncodeToString(pub), Label: label}
	return addr, w.Save()
}

// CreateAddress generates and saves a new keypair.
func (w *Wallet) CreateAddress(label string) (string, error) {
	priv, pub, err := crypto.GenerateKeypair()
	if err != nil {
		return "", err
	}
	return w.add(priv, pub, label)
}

// ImportPrivateKey adds an existing hex private key.
func (w *Wallet) ImportPrivateKey(privHex, label string) (string, error) {
	priv, err := hex.DecodeString(privHex)
	if err != nil {
		return "", err
	}
	pub, err := crypto.PrivToPub(priv)
	if err != nil {
		return "", err
	}
	return w.add(priv, pub, label)
}

// ExportPrivateKey returns an address's hex private key.
func (w *Wallet) ExportPrivateKey(addr string) (string, error) {
	if !w.unlocked {
		return "", ErrLocked
	}
	k, ok := w.keys[addr]
	if !ok {
		return "", fmt.Errorf("address %s not in wallet", addr)
	}
	return k.PrivateKey, nil
}

// Resolve accepts an address or a label and returns the address.
func (w *Wallet) Resolve(addrOrLabel string) (string, error) {
	if _, ok := w.keys[addrOrLabel]; ok {
		return addrOrLabel, nil
	}
	for _, a := range w.order {
		if a == addrOrLabel || (w.keys[a].Label != "" && w.keys[a].Label == addrOrLabel) {
			return a, nil
		}
	}
	return "", fmt.Errorf("no address or label %q in wallet", addrOrLabel)
}

// SignTx signs tx with the key for its sender.
func (w *Wallet) SignTx(tx *core.Transaction) error {
	if !w.unlocked {
		return ErrLocked
	}
	k, ok := w.keys[tx.Sender]
	if !ok {
		return fmt.Errorf("address %s not in wallet", tx.Sender)
	}
	priv, err1 := hex.DecodeString(k.PrivateKey)
	pub, err2 := hex.DecodeString(k.PublicKey)
	if err := errors.Join(err1, err2); err != nil {
		return fmt.Errorf("corrupt key for %s: %w", tx.Sender, err)
	}
	return tx.SignWith(priv, pub)
}
