package crypto

import (
	"errors"
	"math/big"
)

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var b58Index = func() [256]int8 {
	var idx [256]int8
	for i := range idx {
		idx[i] = -1
	}
	for i := 0; i < len(b58Alphabet); i++ {
		idx[b58Alphabet[i]] = int8(i)
	}
	return idx
}()

var big58 = big.NewInt(58)

// Base58Encode encodes with the Bitcoin alphabet; leading zero bytes
// become leading '1's.
func Base58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	var out []byte
	mod := new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, big58, mod)
		out = append(out, b58Alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// Base58Decode is the inverse of Base58Encode.
func Base58Decode(s string) ([]byte, error) {
	n := new(big.Int)
	for i := 0; i < len(s); i++ {
		v := b58Index[s[i]]
		if v < 0 {
			return nil, errors.New("invalid base58 character")
		}
		n.Mul(n, big58)
		n.Add(n, big.NewInt(int64(v)))
	}
	body := n.Bytes()
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), body...), nil
}
