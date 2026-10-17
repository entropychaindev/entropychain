// Package crypto implements Entropy Chain's primitives (crypto_utils.py):
// secp256k1 keys, raw r||s ECDSA signatures over SHA-256, double SHA-256,
// and Base58Check addresses with version byte 0x21 (addresses start 'E').
package crypto

import (
	"bytes"
	"crypto/sha256"
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/ripemd160" //nolint:staticcheck // required by the address format
)

// AddrVersion is the Base58Check version byte for addresses.
const AddrVersion = 0x21

// Sha256 returns SHA-256(data).
func Sha256(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

// Sha256d returns SHA-256(SHA-256(data)), the chain's standard hash.
func Sha256d(data []byte) []byte {
	return Sha256(Sha256(data))
}

func hash160(data []byte) []byte {
	h := ripemd160.New()
	h.Write(Sha256(data))
	return h.Sum(nil)
}

// PubkeyToAddress returns Base58Check(0x21 || RIPEMD160(SHA256(pubkey))).
// The address commits to the exact pubkey bytes, so a compressed and an
// uncompressed encoding of the same key give different addresses.
func PubkeyToAddress(pubkey []byte) string {
	versioned := append([]byte{AddrVersion}, hash160(pubkey)...)
	checksum := Sha256d(versioned)[:4]
	return Base58Encode(append(versioned, checksum...))
}

// AddressIsValid checks length, checksum and version byte.
func AddressIsValid(addr string) bool {
	decoded, err := Base58Decode(addr)
	if err != nil || len(decoded) != 25 {
		return false
	}
	payload, checksum := decoded[:21], decoded[21:]
	if !bytes.Equal(Sha256d(payload)[:4], checksum) {
		return false
	}
	return payload[0] == AddrVersion
}

// GenerateKeypair returns (32-byte private key, 33-byte compressed pubkey).
func GenerateKeypair() (priv, pub []byte, err error) {
	k, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	return k.Serialize(), k.PubKey().SerializeCompressed(), nil
}

// ErrBadPrivateKey is returned for keys outside [1, n-1].
var ErrBadPrivateKey = errors.New("invalid private key")

func parsePrivateKey(priv []byte) (*secp256k1.PrivateKey, error) {
	if len(priv) != 32 {
		return nil, ErrBadPrivateKey
	}
	var s secp256k1.ModNScalar
	if overflow := s.SetByteSlice(priv); overflow || s.IsZero() {
		return nil, ErrBadPrivateKey
	}
	return secp256k1.NewPrivateKey(&s), nil
}

// PrivToPub returns the compressed public key for a private key.
func PrivToPub(priv []byte) ([]byte, error) {
	k, err := parsePrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return k.PubKey().SerializeCompressed(), nil
}

// Sign returns a 64-byte r||s RFC 6979 signature over SHA-256(message).
// The s value is normalised low, which Python's verifier accepts.
func Sign(priv, message []byte) ([]byte, error) {
	k, err := parsePrivateKey(priv)
	if err != nil {
		return nil, err
	}
	sig := ecdsa.Sign(k, Sha256(message))
	r, s := sig.R(), sig.S()
	out := make([]byte, 64)
	r.PutBytesUnchecked(out[:32])
	s.PutBytesUnchecked(out[32:])
	return out, nil
}

// ParsePubkey accepts the encodings python-ecdsa's VerifyingKey.from_string
// accepts: 33-byte compressed, 65-byte uncompressed (0x04), and 64-byte raw
// x||y.
func ParsePubkey(pub []byte) (*secp256k1.PublicKey, error) {
	switch {
	case len(pub) == 64:
		return secp256k1.ParsePubKey(append([]byte{0x04}, pub...))
	case len(pub) == 65 && pub[0] != 0x04:
		return nil, errors.New("unsupported public key format")
	}
	return secp256k1.ParsePubKey(pub)
}

// Verify checks a 64-byte r||s signature over SHA-256(message). High-s
// signatures are accepted, matching python-ecdsa.
func Verify(pub, sig, message []byte) bool {
	if len(sig) != 64 {
		return false
	}
	key, err := ParsePubkey(pub)
	if err != nil {
		return false
	}
	var r, s secp256k1.ModNScalar
	if r.SetByteSlice(sig[:32]) || r.IsZero() {
		return false
	}
	if s.SetByteSlice(sig[32:]) || s.IsZero() {
		return false
	}
	return ecdsa.NewSignature(&r, &s).Verify(Sha256(message), key)
}
