// Package pyjson reproduces Python's json.dumps(obj, sort_keys=True) byte
// for byte, and decodes JSON while preserving the int/float distinction.
//
// Transaction hashes are SHA-256d over that Python output, so any deviation
// here (separators, float formatting, unicode escaping) changes tx hashes,
// merkle roots and block hashes, and splits the Go node from Python peers.
package pyjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Number is a JSON number literal kept verbatim, so an integer stays an
// integer and a float stays a float through decode -> hash -> re-encode,
// exactly as Python's json module treats them.
type Number string

// FloatNumber returns the Number Python would produce for float f.
func FloatNumber(f float64) Number { return Number(FloatRepr(f)) }

// IntNumber returns the Number for integer i.
func IntNumber(i int64) Number { return Number(strconv.FormatInt(i, 10)) }

func (n Number) MarshalJSON() ([]byte, error) {
	if n == "" {
		return nil, errors.New("pyjson: empty Number")
	}
	return []byte(n), nil
}

func (n *Number) UnmarshalJSON(b []byte) error {
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&num); err != nil {
		return fmt.Errorf("expected a number: %w", err)
	}
	*n = Number(num)
	return nil
}

// IsFloat reports whether the literal is a float in Python's eyes.
func (n Number) IsFloat() bool { return strings.ContainsAny(string(n), ".eE") }

// Float64 returns the numeric value.
func (n Number) Float64() (float64, error) { return strconv.ParseFloat(string(n), 64) }

// Decode unmarshals JSON with numbers preserved as json.Number. Trailing
// data is an error.
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("pyjson: trailing data")
	}
	return nil
}

// Dumps returns Python's json.dumps(v, sort_keys=True).
func Dumps(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeValue(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeValue(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, x)
	case json.Number:
		return writeNumber(b, string(x))
	case Number:
		return writeNumber(b, string(x))
	case int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case *big.Int:
		b.WriteString(x.String())
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return errors.New("pyjson: non-finite float")
		}
		b.WriteString(FloatRepr(x))
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		// Go compares strings bytewise; for valid UTF-8 that is the same
		// order as Python's code-point comparison.
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, k)
			b.WriteString(": ")
			if err := writeValue(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := writeValue(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, e)
		}
		b.WriteByte(']')
	default:
		return fmt.Errorf("pyjson: unsupported type %T", v)
	}
	return nil
}

// writeNumber normalises a JSON literal the way Python's round trip does:
// json.loads makes it an int or float, json.dumps prints repr().
func writeNumber(b *bytes.Buffer, lit string) error {
	if strings.ContainsAny(lit, ".eE") {
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("pyjson: bad float %q", lit)
		}
		b.WriteString(FloatRepr(f))
		return nil
	}
	n, ok := new(big.Int).SetString(lit, 10)
	if !ok {
		return fmt.Errorf("pyjson: bad integer %q", lit)
	}
	b.WriteString(n.String())
	return nil
}

// FloatRepr formats f like Python's repr(float): shortest round-trip digits,
// fixed notation for 1e-4 <= |f| < 1e16 (always with a ".0" if integral),
// otherwise exponent notation such as "1e+16" or "1.5e-05".
func FloatRepr(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(sci[strings.IndexByte(sci, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return sci
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsRune(s, '.') {
		s += ".0"
	}
	return s
}

const hexDigits = "0123456789abcdef"

// writeString escapes like Python with ensure_ascii=True: only printable
// ASCII is emitted raw; everything else becomes \uXXXX (surrogate pairs
// above the BMP), with the short escapes Python uses for common controls.
func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				b.WriteByte(byte(r))
			case r < 0x10000:
				writeU(b, r)
			default:
				r1, r2 := utf16.EncodeRune(r)
				writeU(b, r1)
				writeU(b, r2)
			}
		}
	}
	b.WriteByte('"')
}

func writeU(b *bytes.Buffer, r rune) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[(r>>12)&0xf])
	b.WriteByte(hexDigits[(r>>8)&0xf])
	b.WriteByte(hexDigits[(r>>4)&0xf])
	b.WriteByte(hexDigits[r&0xf])
}

// ---- Typed accessors for decoded data maps ----

// AsInt returns v as an int64 if it is a JSON integer (not a float, not a
// bool) that fits in 64 bits.
func AsInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		return parseInt(string(x))
	case Number:
		return parseInt(string(x))
	case int64:
		return x, true
	case int:
		return int64(x), true
	}
	return 0, false
}

func parseInt(lit string) (int64, bool) {
	if strings.ContainsAny(lit, ".eE") {
		return 0, false
	}
	i, err := strconv.ParseInt(lit, 10, 64)
	return i, err == nil
}

// AsString returns v as a string if it is one.
func AsString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}
